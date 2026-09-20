package cognito

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// createUserPool — CreateUserPool.
//
// Delegates to CreateUserPoolTyped (typed_logic.go) so the legacy
// JSON1.0/1.1 path and the CBOR typed path share one implementation — the
// legacy copy previously re-implemented this inline and, like the typed
// path, modeled no UserPoolTags request member at all (#1196).
func (s *Service) createUserPool(w http.ResponseWriter, r *http.Request) {
	var req CreateUserPoolReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.PoolName, "PoolName") {
		return
	}
	resp, aerr := s.CreateUserPoolTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"UserPool": resp.UserPool})
}

// describeUserPool — DescribeUserPool.
func (s *Service) describeUserPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolID string `json:"UserPoolId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}
	users, err := s.scanUsers(r.Context(), req.UserPoolID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	wire := toUserPoolWire(pool)
	wire.EstimatedNumberOfUsers = len(users)
	// Include the domain in the response if one is configured.
	if d, _ := s.loadDomainForPool(r.Context(), req.UserPoolID); d != nil {
		wire.Domain = d.Domain
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"UserPool": wire})
}

// deleteUserPool — DeleteUserPool.
func (s *Service) deleteUserPool(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		UserPoolID string `json:"UserPoolId"`
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
	if err := s.store.Delete(r.Context(), nsPools, s.poolKey(r.Context(), req.UserPoolID)); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	// Best-effort cleanup of the RSA signing key for this pool.
	_ = s.removeSigningKey(r.Context(), req.UserPoolID)
	log.Info("user pool deleted", zap.String("poolId", req.UserPoolID))
	s.publish(r, events.CognitoUserPoolDeleted, events.ResourcePayload{Name: req.UserPoolID})
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// listUserPools — ListUserPools.
func (s *Service) listUserPools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.scanPools(r.Context())
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	type poolDesc struct {
		ID               string  `json:"Id"`
		Name             string  `json:"Name"`
		CreationDate     float64 `json:"CreationDate"`
		LastModifiedDate float64 `json:"LastModifiedDate"`
	}
	out := make([]poolDesc, 0, len(pools))
	for _, p := range pools {
		epoch := float64(p.CreatedAt.Unix())
		out = append(out, poolDesc{ID: p.ID, Name: p.Name, CreationDate: epoch, LastModifiedDate: epoch})
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"UserPools": out})
}

// updateUserPool — UpdateUserPool.
func (s *Service) updateUserPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserPoolID                  string                           `json:"UserPoolId"`
		UserPoolTier                string                           `json:"UserPoolTier"`
		VerificationMessageTemplate *verificationMessageTemplateWire `json:"VerificationMessageTemplate"`
		AdminCreateUserConfig       *adminCreateUserConfigWire       `json:"AdminCreateUserConfig"`
		EmailConfiguration          *emailConfigurationWire          `json:"EmailConfiguration"`
		UserAttributeUpdateSettings *userAttributeUpdateSettingsWire `json:"UserAttributeUpdateSettings"`
		AutoVerifiedAttributes      []string                         `json:"AutoVerifiedAttributes"`
		DeviceConfiguration         *DeviceConfiguration             `json:"DeviceConfiguration"`
		UsernameAttributes          []string                         `json:"UsernameAttributes"`
		AliasAttributes             []string                         `json:"AliasAttributes"`
		Policies                    *userPoolPoliciesWire            `json:"Policies"`
		AccountRecoverySetting      *AccountRecoverySetting          `json:"AccountRecoverySetting"`
		SmsConfiguration            *SmsConfiguration                `json:"SmsConfiguration"`
		SmsAuthenticationMessage    string                           `json:"SmsAuthenticationMessage"`
		SmsVerificationMessage      string                           `json:"SmsVerificationMessage"`
		EmailVerificationMessage    string                           `json:"EmailVerificationMessage"`
		EmailVerificationSubject    string                           `json:"EmailVerificationSubject"`
		LambdaConfig                *LambdaConfig                    `json:"LambdaConfig"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}
	if aerr := validateUserPoolPolicies(req.Policies); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if aerr := validateUserPoolTier(req.UserPoolTier); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	newTier := pool.UserPoolTier
	if req.UserPoolTier != "" {
		newTier = req.UserPoolTier
	}
	if aerr := validateTierForSignInPolicy(newTier, req.Policies); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if v := req.VerificationMessageTemplate; v != nil {
		opt := v.DefaultEmailOption
		if opt == "" {
			opt = "CONFIRM_WITH_CODE"
		}
		pool.VerificationMessageTemplate = &VerificationMessageTemplate{
			DefaultEmailOption: opt,
			EmailMessage:       v.EmailMessage,
			EmailMessageByLink: v.EmailMessageByLink,
			EmailSubject:       v.EmailSubject,
			EmailSubjectByLink: v.EmailSubjectByLink,
			SmsMessage:         v.SmsMessage,
		}
	}
	if a := req.AdminCreateUserConfig; a != nil {
		validityDays := a.UnusedAccountValidityDays
		if validityDays == 0 {
			validityDays = 7 // AWS default
		}
		cfg := &AdminCreateUserConfig{
			AllowAdminCreateUserOnly:  a.AllowAdminCreateUserOnly,
			UnusedAccountValidityDays: validityDays,
		}
		if t := a.InviteMessageTemplate; t != nil {
			cfg.InviteMessageTemplate = &InviteMessageTemplate{
				EmailMessage: t.EmailMessage,
				EmailSubject: t.EmailSubject,
				SMSMessage:   t.SMSMessage,
			}
		}
		pool.AdminCreateUserConfig = cfg
	}
	if e := req.EmailConfiguration; e != nil {
		pool.EmailConfiguration = &EmailConfiguration{
			EmailSendingAccount: e.EmailSendingAccount,
			SourceArn:           e.SourceArn,
			From:                e.From,
			ReplyToEmailAddress: e.ReplyToEmailAddress,
		}
	}
	if aerr := applyUserAttributeUpdateSettings(pool, req.UserAttributeUpdateSettings); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if aerr := applyAutoVerifiedAttributes(pool, req.AutoVerifiedAttributes); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if req.DeviceConfiguration != nil {
		pool.DeviceConfiguration = req.DeviceConfiguration
	}
	if req.UsernameAttributes != nil {
		pool.UsernameAttributes = req.UsernameAttributes
	}
	if req.AliasAttributes != nil {
		pool.AliasAttributes = req.AliasAttributes
	}
	if req.Policies != nil {
		pool.Policies = mergeUserPoolPolicies(pool.Policies, req.Policies)
	}
	if req.UserPoolTier != "" {
		pool.UserPoolTier = req.UserPoolTier
	}
	if req.AccountRecoverySetting != nil {
		pool.AccountRecoverySetting = req.AccountRecoverySetting
	}
	if req.SmsConfiguration != nil {
		pool.SmsConfiguration = req.SmsConfiguration
	}
	if req.SmsAuthenticationMessage != "" {
		pool.SmsAuthenticationMessage = req.SmsAuthenticationMessage
	}
	if req.SmsVerificationMessage != "" {
		pool.SmsVerificationMessage = req.SmsVerificationMessage
	}
	if req.EmailVerificationMessage != "" {
		pool.EmailVerificationMessage = req.EmailVerificationMessage
	}
	if req.EmailVerificationSubject != "" {
		pool.EmailVerificationSubject = req.EmailVerificationSubject
	}
	if req.LambdaConfig != nil {
		pool.LambdaConfig = req.LambdaConfig
	}

	if err := s.savePool(r.Context(), pool); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// ─── domain operations ────────────────────────────────────────────────────────

// createUserPoolDomain — CreateUserPoolDomain.
func (s *Service) createUserPoolDomain(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		Domain     string `json:"Domain"`
		UserPoolID string `json:"UserPoolId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.Domain, "Domain") {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}
	// Check domain is not already taken.
	existing, err := s.loadDomain(r.Context(), req.Domain)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	if existing != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Domain " + req.Domain + " is already associated with a user pool.",
			HTTPStatus: 400,
		})
		return
	}
	d := &UserPoolDomain{
		Domain:     req.Domain,
		UserPoolID: req.UserPoolID,
		CreatedAt:  s.clk.Now(),
	}
	if err := s.saveDomain(r.Context(), d); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	// Store domain on pool for convenience.
	pool.Domain = req.Domain
	if err := s.savePool(r.Context(), pool); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	log.Info("user pool domain created",
		zap.String("poolId", req.UserPoolID), zap.String("domain", req.Domain))
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// describeUserPoolDomain — DescribeUserPoolDomain.
func (s *Service) describeUserPoolDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"Domain"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.Domain, "Domain") {
		return
	}
	d, err := s.loadDomain(r.Context(), req.Domain)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	if d == nil {
		// AWS returns an empty DomainDescription when the domain doesn't exist.
		s.writeJSON(w, r, http.StatusOK, map[string]any{"DomainDescription": map[string]any{}})
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"DomainDescription": map[string]any{
			"Domain":     d.Domain,
			"UserPoolId": d.UserPoolID,
			"Status":     "ACTIVE",
		},
	})
}

// deleteUserPoolDomain — DeleteUserPoolDomain.
func (s *Service) deleteUserPoolDomain(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		Domain     string `json:"Domain"`
		UserPoolID string `json:"UserPoolId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.Domain, "Domain") {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	pool, ok := s.requirePool(r.Context(), w, r, req.UserPoolID)
	if !ok {
		return
	}
	if err := s.removeDomain(r.Context(), req.Domain); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	if pool.Domain == req.Domain {
		pool.Domain = ""
		_ = s.savePool(r.Context(), pool)
	}
	log.Info("user pool domain deleted",
		zap.String("poolId", req.UserPoolID), zap.String("domain", req.Domain))
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// updateUserPoolDomain — UpdateUserPoolDomain.
func (s *Service) updateUserPoolDomain(w http.ResponseWriter, r *http.Request) {
	// UpdateUserPoolDomain in real AWS is used to update the SSL cert for custom domains.
	// In the emulator we accept but ignore — the domain remains active.
	var req struct {
		Domain     string `json:"Domain"`
		UserPoolID string `json:"UserPoolId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.Domain, "Domain") {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if _, ok := s.requirePool(r.Context(), w, r, req.UserPoolID); !ok {
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}
