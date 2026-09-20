package cognito

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// looksLikeEmail returns true if s contains an "@" — a simple heuristic
// matching AWS Cognito's own check for email-based usernames.
func looksLikeEmail(s string) bool { return strings.Contains(s, "@") }

// looksLikePhone returns true if s starts with "+".
func looksLikePhone(s string) bool { return strings.HasPrefix(s, "+") }

// setAttrIfMissing adds a user attribute only if it doesn't already exist in the slice.
func setAttrIfMissing(attrs *[]UserAttribute, name, value string) {
	for _, a := range *attrs {
		if a.Name == name {
			return
		}
	}
	*attrs = append(*attrs, UserAttribute{Name: name, Value: value})
}

// autoSetUsernameAttribute sets the email or phone_number attribute based on
// the pool's UsernameAttributes and the value the caller supplied as Username.
func autoSetUsernameAttribute(usernameAttrs []string, attrs *[]UserAttribute, username string) {
	for _, attr := range usernameAttrs {
		switch attr {
		case "email":
			if looksLikeEmail(username) {
				setAttrIfMissing(attrs, "email", username)
				return
			}
		case "phone_number":
			if looksLikePhone(username) {
				setAttrIfMissing(attrs, "phone_number", username)
				return
			}
		}
	}
}

// adminCreateUser — AdminCreateUser
// Creates a user with FORCE_CHANGE_PASSWORD status and optionally sends a
// temp-password email if the user has an email attribute and MessageAction != "SUPPRESS".
func (s *Service) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		UserPoolID         string          `json:"UserPoolId"`
		Username           string          `json:"Username"`
		TemporaryPassword  string          `json:"TemporaryPassword"`
		UserAttributes     []UserAttribute `json:"UserAttributes"`
		MessageAction      string          `json:"MessageAction"`
		ForceAliasCreation bool            `json:"ForceAliasCreation"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}

	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}

	attrs := req.UserAttributes
	if attrs == nil {
		attrs = []UserAttribute{}
	}

	// When the pool uses UsernameAttributes (email/phone sign-in), the
	// caller-supplied Username is actually the attribute value (e.g. an email
	// address). AWS auto-generates a UUID as the internal username and sets
	// the matching attribute automatically.
	internalUsername := req.Username
	sub := uuid.NewString()
	if len(pool.UsernameAttributes) > 0 {
		internalUsername = sub
		// Auto-set the attribute the caller passed as Username.
		autoSetUsernameAttribute(pool.UsernameAttributes, &attrs, req.Username)
	}

	// Reject duplicate: check by internal username key and by attribute value.
	existing, err := s.resolveUser(r.Context(), pool, req.Username)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	if existing != nil {
		protocol.WriteJSONError(w, r, errUsernameExists(req.Username))
		return
	}
	aliasOwner, aliasAttr, err := s.findVerifiedAliasOwner(r.Context(), pool, attrs)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	if aliasOwner != nil {
		if !req.ForceAliasCreation {
			protocol.WriteJSONError(w, r, errAliasExists())
			return
		}
		if err := s.migrateVerifiedAlias(r.Context(), aliasOwner, aliasAttr); err != nil {
			protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
			return
		}
	}

	tempPw := req.TemporaryPassword
	if tempPw == "" {
		tempPw = generateTempPassword(pool)
	} else if aerr := validatePassword(pool, tempPw); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	hash, err := hashPassword(tempPw)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}

	// PreSignUp fires for AdminCreateUser too, but AWS documents that its
	// autoConfirmUser/autoVerifyEmail/autoVerifyPhone response fields are
	// ignored for this trigger source — only a function error is honored,
	// failing AdminCreateUser the way AWS does.
	if _, aerr := s.runPreSignUp(r.Context(), pool, triggerSourcePreSignUpAdminCreate, "", req.Username, attrs); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	u := &User{
		Username:          internalUsername,
		Sub:               sub,
		UserPoolID:        req.UserPoolID,
		CreatedAt:         s.clk.Now(),
		Status:            StatusForceChangePassword,
		Enabled:           true,
		PasswordHash:      string(hash),
		PlaintextPassword: tempPw,
		TempPassword:      tempPw,
		Attributes:        attrs,
	}
	u.setAttr("sub", sub)
	if err := s.saveUser(r.Context(), u); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}

	// Send temp-password email/SMS unless suppressed.
	if req.MessageAction != "SUPPRESS" {
		msgOverride, aerr := s.runCustomMessage(r.Context(), pool, triggerSourceCustomMessageAdminUser, "", u.Username, tempPw, req.Username, u.Attributes)
		if aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		if emailAddr := u.email(); emailAddr != "" {
			s.sendTempPasswordEmail(pool, emailAddr, u.Username, tempPw, msgOverride)
		}
		if phone := u.phoneNumber(); phone != "" {
			s.sendTempPasswordSMS(pool, phone, u.Username, tempPw, msgOverride)
		}
	}

	log.Info("admin created user",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username))
	s.publish(r, events.CognitoUserCreated, events.ResourcePayload{Name: req.Username})
	s.writeJSON(w, r, http.StatusOK, map[string]any{"User": toUserWire(u)})
}

// adminDeleteUser — AdminDeleteUser.
func (s *Service) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		UserPoolID string `json:"UserPoolId"`
		Username   string `json:"Username"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	if _, ok := s.requirePool(r.Context(), w, r, req.UserPoolID); !ok {
		return
	}
	u, ok := s.requireUser(r.Context(), w, r, req.UserPoolID, req.Username)
	if !ok {
		return
	}
	if err := s.removeUser(r.Context(), req.UserPoolID, u.Username); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	log.Info("admin deleted user",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username))
	s.publish(r, events.CognitoUserDeleted, events.ResourcePayload{Name: req.Username})
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// adminGetUser — AdminGetUser.
func (s *Service) adminGetUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolID string `json:"UserPoolId"`
		Username   string `json:"Username"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}
	u, ok := s.requireUser(r.Context(), w, r, req.UserPoolID, req.Username)
	if !ok {
		return
	}
	uw := toUserWire(u)
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"Username":             uw.Username,
		"UserAttributes":       uw.Attributes,
		"UserCreateDate":       uw.UserCreateDate,
		"UserLastModifiedDate": uw.UserLastModifiedDate,
		"Enabled":              uw.Enabled,
		"UserStatus":           uw.UserStatus,
		"PreferredMfaSetting":  preferredMfaSetting(pool, u),
		"UserMFASettingList":   userMfaFactors(pool, u),
	})
}

// adminSetUserPassword — AdminSetUserPassword.
func (s *Service) adminSetUserPassword(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		UserPoolID string `json:"UserPoolId"`
		Username   string `json:"Username"`
		Password   string `json:"Password"`
		Permanent  bool   `json:"Permanent"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Password, "Password") {
		return
	}
	if _, ok := s.requirePool(r.Context(), w, r, req.UserPoolID); !ok {
		return
	}
	u, ok := s.requireUser(r.Context(), w, r, req.UserPoolID, req.Username)
	if !ok {
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	u.PasswordHash = string(hash)
	u.PlaintextPassword = req.Password
	u.TempPassword = ""
	if req.Permanent && u.Status == StatusForceChangePassword {
		u.Status = StatusConfirmed
	}
	if err := s.saveUser(r.Context(), u); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	log.Info("admin set user password",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username),
		zap.Bool("permanent", req.Permanent))
	s.publish(r, events.CognitoUserUpdated, events.ResourcePayload{Name: req.Username})
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// adminConfirmSignUp — AdminConfirmSignUp.
func (s *Service) adminConfirmSignUp(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		UserPoolID string `json:"UserPoolId"`
		Username   string `json:"Username"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}
	u, ok := s.requireUser(r.Context(), w, r, req.UserPoolID, req.Username)
	if !ok {
		return
	}
	u.Status = StatusConfirmed
	u.ConfirmationCode = ""
	if u.email() != "" {
		u.setAttr("email_verified", "true")
	}
	if err := s.saveUser(r.Context(), u); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	s.runPostConfirmation(r.Context(), pool, triggerSourcePostConfirmSignUp, "", u.Username, u.Attributes)
	log.Info("admin confirmed user signup",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username))
	s.publish(r, events.CognitoUserConfirmed, events.ResourcePayload{Name: req.Username})
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// adminUpdateUserAttributes — AdminUpdateUserAttributes.
func (s *Service) adminUpdateUserAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolID     string          `json:"UserPoolId"`
		Username       string          `json:"Username"`
		UserAttributes []UserAttribute `json:"UserAttributes"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}
	u, ok := s.requireUser(r.Context(), w, r, req.UserPoolID, req.Username)
	if !ok {
		return
	}
	immediateAttrs := make([]UserAttribute, 0, len(req.UserAttributes))
	for _, attr := range req.UserAttributes {
		if verifiedAttributeName(attr.Name) != "" && requiresVerificationBeforeUpdate(pool, attr.Name) && !hasVerificationBypass(req.UserAttributes, attr.Name) {
			continue
		}
		immediateAttrs = append(immediateAttrs, attr)
	}
	aliasOwner, _, err := s.findVerifiedAliasOwner(r.Context(), pool, attributesAfterUpdate(u, immediateAttrs))
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	if aliasOwner != nil && aliasOwner.Username != u.Username {
		protocol.WriteJSONError(w, r, errAliasExists())
		return
	}
	if _, aerr := s.updateAttributesWithVerification(r.Context(), pool, u, req.UserAttributes, true); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if err := s.saveUser(r.Context(), u); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	s.publish(r, events.CognitoUserUpdated, events.ResourcePayload{Name: req.Username})
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// adminDeleteUserAttributes — AdminDeleteUserAttributes.
func (s *Service) adminDeleteUserAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolID         string   `json:"UserPoolId"`
		Username           string   `json:"Username"`
		UserAttributeNames []string `json:"UserAttributeNames"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	if _, ok := s.requirePool(r.Context(), w, r, req.UserPoolID); !ok {
		return
	}
	u, ok := s.requireUser(r.Context(), w, r, req.UserPoolID, req.Username)
	if !ok {
		return
	}
	for _, name := range req.UserAttributeNames {
		u.removeAttr(name)
	}
	if err := s.saveUser(r.Context(), u); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	s.publish(r, events.CognitoUserUpdated, events.ResourcePayload{Name: req.Username})
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// adminDisableUser — AdminDisableUser.
func (s *Service) adminDisableUser(w http.ResponseWriter, r *http.Request) {
	s.setUserEnabled(w, r, false)
}

// adminEnableUser — AdminEnableUser.
func (s *Service) adminEnableUser(w http.ResponseWriter, r *http.Request) {
	s.setUserEnabled(w, r, true)
}

func (s *Service) setUserEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	var req struct {
		UserPoolID string `json:"UserPoolId"`
		Username   string `json:"Username"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	if _, ok := s.requirePool(r.Context(), w, r, req.UserPoolID); !ok {
		return
	}
	u, ok := s.requireUser(r.Context(), w, r, req.UserPoolID, req.Username)
	if !ok {
		return
	}
	u.Enabled = enabled
	if err := s.saveUser(r.Context(), u); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	s.publish(r, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// listUsers — ListUsers.
func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolID      string   `json:"UserPoolId"`
		AttributesToGet []string `json:"AttributesToGet"`
		Filter          string   `json:"Filter"`
		Limit           int      `json:"Limit"`
		PaginationToken string   `json:"PaginationToken"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if _, ok := s.requirePool(r.Context(), w, r, req.UserPoolID); !ok {
		return
	}
	users, err := s.scanUsers(r.Context(), req.UserPoolID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	out, nextToken, aerr := filterAndPageUsers(users, req.Filter, req.AttributesToGet, req.Limit, req.PaginationToken)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	resp := map[string]any{"Users": out}
	if nextToken != "" {
		resp["PaginationToken"] = nextToken
	}
	s.writeJSON(w, r, http.StatusOK, resp)
}
