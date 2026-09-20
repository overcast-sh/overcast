package cognito

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

type deviceWire struct {
	DeviceKey                   string          `json:"DeviceKey"`
	DeviceAttributes            []UserAttribute `json:"DeviceAttributes,omitempty"`
	DeviceCreateDate            float64         `json:"DeviceCreateDate,omitempty"`
	DeviceLastAuthenticatedDate float64         `json:"DeviceLastAuthenticatedDate,omitempty"`
	DeviceLastModifiedDate      float64         `json:"DeviceLastModifiedDate,omitempty"`
}

type ConfirmDeviceResp struct {
	UserConfirmationNecessary bool `json:"UserConfirmationNecessary"`
}

type ListDevicesResp struct {
	Devices         []deviceWire `json:"Devices"`
	PaginationToken string       `json:"PaginationToken,omitempty"`
}

type GetDeviceResp struct {
	Device deviceWire `json:"Device"`
}

func (s *Service) maybeStartDeviceAuthChallenge(ctx context.Context, pool *UserPool, u *User, params map[string]string) (*InitiateAuthResp, *protocol.AWSError) {
	deviceKey := params["DEVICE_KEY"]
	if deviceKey == "" || pool.DeviceConfiguration == nil {
		return nil, nil
	}
	device, ok := findUserDevice(u, deviceKey)
	if !ok || device.DeviceRememberedStatus != "remembered" {
		return nil, nil
	}
	session, err := s.issueDeviceChallengeSession(ctx, pool.ID, u.Username, deviceKey, "device_srp", 5*time.Minute)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &InitiateAuthResp{
		ChallengeName: "DEVICE_SRP_AUTH",
		ChallengeParameters: map[string]string{
			"USERNAME":   u.Username,
			"DEVICE_KEY": deviceKey,
		},
		Session: session,
	}, nil
}

func (s *Service) attachNewDeviceMetadata(ctx context.Context, pool *UserPool, result *authResultWire, params map[string]string) {
	if pool.DeviceConfiguration == nil || params["DEVICE_KEY"] != "" || result == nil {
		return
	}
	deviceKey := s.region(ctx) + "_" + uuid.NewString()
	result.NewDeviceMetadata = &NewDeviceMetadata{DeviceKey: deviceKey, DeviceGroupKey: pool.ID}
}

func (s *Service) issueDeviceChallengeSession(ctx context.Context, poolID, username, deviceKey, tokenType string, ttl time.Duration) (string, error) {
	now := s.clk.Now()
	t := &Token{Value: generateToken(), Type: tokenType, Username: username, UserPoolID: poolID, DeviceKey: deviceKey, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.saveToken(ctx, t); err != nil {
		return "", err
	}
	return t.Value, nil
}

func (s *Service) startDeviceSRPChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if responses["DEVICE_KEY"] == "" || responses["SRP_A"] == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DEVICE_KEY and SRP_A are required in ChallengeResponses.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "device_srp" || st.UserPoolID != client.UserPoolID || st.DeviceKey != responses["DEVICE_KEY"] {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	_ = s.removeToken(ctx, session)
	verifierSession, err := s.issueDeviceChallengeSession(ctx, client.UserPoolID, st.Username, st.DeviceKey, "device_verifier", 5*time.Minute)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	params := srpChallengeParameters(st.Username)
	params["DEVICE_KEY"] = st.DeviceKey
	return &RespondToAuthChallengeResp{ChallengeName: "DEVICE_PASSWORD_VERIFIER", ChallengeParameters: params, Session: verifierSession}, nil
}

func (s *Service) completeDevicePasswordVerifierTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if responses["DEVICE_KEY"] == "" || responses["PASSWORD_CLAIM_SIGNATURE"] == "" || responses["PASSWORD_CLAIM_SECRET_BLOCK"] == "" || responses["TIMESTAMP"] == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DEVICE_KEY, PASSWORD_CLAIM_SIGNATURE, PASSWORD_CLAIM_SECRET_BLOCK, and TIMESTAMP are required in ChallengeResponses.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "device_verifier" || st.UserPoolID != client.UserPoolID || st.DeviceKey != responses["DEVICE_KEY"] {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	u, err := s.loadUser(ctx, client.UserPoolID, st.Username)
	if err != nil || u == nil {
		return nil, errNotAuthorized("Invalid session.")
	}
	if device, ok := findUserDevice(u, st.DeviceKey); !ok || device.DeviceRememberedStatus != "remembered" {
		return nil, errNotAuthorized("Invalid device.")
	}
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenDevice)
	if aerr != nil {
		return nil, aerr
	}
	_ = s.removeToken(ctx, session)
	updateUserDeviceAuthTime(u, st.DeviceKey, s.clk.Now())
	_ = s.saveUser(ctx, u)
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: responses["USERNAME"]})
	return &RespondToAuthChallengeResp{AuthenticationResult: result, ChallengeParameters: map[string]string{}}, nil
}

func (s *Service) ConfirmDeviceTyped(ctx context.Context, req *ConfirmDeviceReq) (*ConfirmDeviceResp, *protocol.AWSError) {
	if req.DeviceKey == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DeviceKey is required.", HTTPStatus: 400}
	}
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	if _, ok := findUserDevice(u, req.DeviceKey); ok {
		return nil, &protocol.AWSError{Code: "DeviceKeyExistsException", Message: "Device already exists.", HTTPStatus: 400}
	}
	pool, aerr := s.requirePoolTyped(ctx, t.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	status := "remembered"
	confirmNeeded := false
	if pool.DeviceConfiguration != nil && pool.DeviceConfiguration.DeviceOnlyRememberedOnUserPrompt {
		status = "not_remembered"
		confirmNeeded = true
	}
	now := s.clk.Now()
	verifier := ""
	salt := ""
	if req.DeviceSecretVerifierConfig != nil {
		verifier = req.DeviceSecretVerifierConfig.PasswordVerifier
		salt = req.DeviceSecretVerifierConfig.Salt
	}
	u.Devices = append(u.Devices, UserDevice{DeviceKey: req.DeviceKey, DeviceGroupKey: pool.ID, DeviceName: req.DeviceName, PasswordVerifier: verifier, Salt: salt, DeviceRememberedStatus: status, DeviceCreateDate: now, DeviceLastModifiedDate: now})
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &ConfirmDeviceResp{UserConfirmationNecessary: confirmNeeded}, nil
}

func (s *Service) ListDevicesTyped(ctx context.Context, req *ListDevicesReq) (*ListDevicesResp, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	return listDevicesResponse(u.Devices, req.Limit, req.PaginationToken)
}

func (s *Service) GetDeviceTyped(ctx context.Context, req *DeviceKeyAccessReq) (*GetDeviceResp, *protocol.AWSError) {
	if req.DeviceKey == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DeviceKey is required.", HTTPStatus: 400}
	}
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	device, ok := findUserDevice(u, req.DeviceKey)
	if !ok {
		return nil, errDeviceNotFound()
	}
	return &GetDeviceResp{Device: toDeviceWire(*device)}, nil
}

func (s *Service) UpdateDeviceStatusTyped(ctx context.Context, req *UpdateDeviceStatusReq) (*struct{}, *protocol.AWSError) {
	if req.DeviceKey == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DeviceKey is required.", HTTPStatus: 400}
	}
	if aerr := validateDeviceRememberedStatus(req.DeviceRememberedStatus); aerr != nil {
		return nil, aerr
	}
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	if !updateUserDeviceStatus(u, req.DeviceKey, req.DeviceRememberedStatus, s.clk.Now()) {
		return nil, errDeviceNotFound()
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &struct{}{}, nil
}

func (s *Service) ForgetDeviceTyped(ctx context.Context, req *DeviceKeyAccessReq) (*struct{}, *protocol.AWSError) {
	if req.DeviceKey == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DeviceKey is required.", HTTPStatus: 400}
	}
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	for i, d := range u.Devices {
		if d.DeviceKey == req.DeviceKey {
			u.Devices = append(u.Devices[:i], u.Devices[i+1:]...)
			if err := s.saveUser(ctx, u); err != nil {
				return nil, protocol.Wrap(protocol.ErrInternalError, err)
			}
			return &struct{}{}, nil
		}
	}
	// AWS models ResourceNotFoundException on ForgetDevice/AdminForgetDevice and
	// reports an unrecognised device key rather than succeeding, so an unknown
	// key is an error here too — not a silent no-op (issues #110, #114).
	return nil, errDeviceNotFound()
}

func (s *Service) AdminGetDeviceTyped(ctx context.Context, req *AdminDeviceReq) (*GetDeviceResp, *protocol.AWSError) {
	u, aerr := s.requireAdminDeviceUser(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if req.DeviceKey == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DeviceKey is required.", HTTPStatus: 400}
	}
	device, ok := findUserDevice(u, req.DeviceKey)
	if !ok {
		return nil, errDeviceNotFound()
	}
	return &GetDeviceResp{Device: toDeviceWire(*device)}, nil
}

func (s *Service) AdminListDevicesTyped(ctx context.Context, req *AdminListDevicesReq) (*ListDevicesResp, *protocol.AWSError) {
	u, aerr := s.requireAdminDeviceUser(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	return listDevicesResponse(u.Devices, req.Limit, req.PaginationToken)
}

func (s *Service) AdminUpdateDeviceStatusTyped(ctx context.Context, req *AdminUpdateDeviceStatusReq) (*struct{}, *protocol.AWSError) {
	u, aerr := s.requireAdminDeviceUser(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if req.DeviceKey == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DeviceKey is required.", HTTPStatus: 400}
	}
	if aerr := validateDeviceRememberedStatus(req.DeviceRememberedStatus); aerr != nil {
		return nil, aerr
	}
	if !updateUserDeviceStatus(u, req.DeviceKey, req.DeviceRememberedStatus, s.clk.Now()) {
		return nil, errDeviceNotFound()
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &struct{}{}, nil
}

func (s *Service) AdminForgetDeviceTyped(ctx context.Context, req *AdminDeviceReq) (*struct{}, *protocol.AWSError) {
	u, aerr := s.requireAdminDeviceUser(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if req.DeviceKey == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "DeviceKey is required.", HTTPStatus: 400}
	}
	for i, d := range u.Devices {
		if d.DeviceKey == req.DeviceKey {
			u.Devices = append(u.Devices[:i], u.Devices[i+1:]...)
			if err := s.saveUser(ctx, u); err != nil {
				return nil, protocol.Wrap(protocol.ErrInternalError, err)
			}
			return &struct{}{}, nil
		}
	}
	// AWS models ResourceNotFoundException on ForgetDevice/AdminForgetDevice and
	// reports an unrecognised device key rather than succeeding, so an unknown
	// key is an error here too — not a silent no-op (issues #110, #114).
	return nil, errDeviceNotFound()
}

func (s *Service) requireAdminDeviceUser(ctx context.Context, poolID, username string) (*User, *protocol.AWSError) {
	pool, aerr := s.requirePoolTyped(ctx, poolID)
	if aerr != nil {
		return nil, aerr
	}
	if username == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Username is required.", HTTPStatus: 400}
	}
	u, err := s.resolveUser(ctx, pool, username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errUserNotFound(username)
	}
	return u, nil
}

func findUserDevice(u *User, deviceKey string) (*UserDevice, bool) {
	for i := range u.Devices {
		if u.Devices[i].DeviceKey == deviceKey {
			return &u.Devices[i], true
		}
	}
	return nil, false
}

func updateUserDeviceAuthTime(u *User, deviceKey string, now time.Time) {
	for i := range u.Devices {
		if u.Devices[i].DeviceKey == deviceKey {
			u.Devices[i].DeviceLastAuthenticatedDate = now
			u.Devices[i].DeviceLastModifiedDate = now
			return
		}
	}
}

func updateUserDeviceStatus(u *User, deviceKey, status string, now time.Time) bool {
	for i := range u.Devices {
		if u.Devices[i].DeviceKey == deviceKey {
			u.Devices[i].DeviceRememberedStatus = status
			u.Devices[i].DeviceLastModifiedDate = now
			return true
		}
	}
	return false
}

func listDevicesResponse(devices []UserDevice, limit int, token string) (*ListDevicesResp, *protocol.AWSError) {
	if limit < 0 || limit > 60 {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Limit must be between 0 and 60.", HTTPStatus: 400}
	}
	start := 0
	if token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil || parsed < 0 || parsed > len(devices) {
			return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Invalid PaginationToken.", HTTPStatus: 400}
		}
		start = parsed
	}
	end := len(devices)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	out := make([]deviceWire, 0, end-start)
	for _, d := range devices[start:end] {
		out = append(out, toDeviceWire(d))
	}
	next := ""
	if end < len(devices) {
		next = strconv.Itoa(end)
	}
	return &ListDevicesResp{Devices: out, PaginationToken: next}, nil
}

func validateDeviceRememberedStatus(status string) *protocol.AWSError {
	if status == "" || status == "remembered" || status == "not_remembered" {
		return nil
	}
	return &protocol.AWSError{Code: "InvalidParameterException", Message: "Invalid DeviceRememberedStatus: " + status, HTTPStatus: 400}
}

func errDeviceNotFound() *protocol.AWSError {
	return &protocol.AWSError{Code: "ResourceNotFoundException", Message: "Device not found.", HTTPStatus: 400}
}

func toDeviceWire(d UserDevice) deviceWire {
	attrs := []UserAttribute{{Name: "device_status", Value: "valid"}}
	if d.DeviceName != "" {
		attrs = append(attrs, UserAttribute{Name: "device_name", Value: d.DeviceName})
	}
	if d.DeviceRememberedStatus != "" {
		attrs = append(attrs, UserAttribute{Name: "dev:device_remembered_status", Value: d.DeviceRememberedStatus})
	}
	return deviceWire{DeviceKey: d.DeviceKey, DeviceAttributes: attrs, DeviceCreateDate: float64(d.DeviceCreateDate.Unix()), DeviceLastAuthenticatedDate: float64(d.DeviceLastAuthenticatedDate.Unix()), DeviceLastModifiedDate: float64(d.DeviceLastModifiedDate.Unix())}
}
