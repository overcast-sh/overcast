package cognito

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// The four MFA operations below are implemented once, in typed_logic.go, and
// the classic X-Amz-Target API decodes the AWS request shape and delegates.
// They used to be byte-for-byte copies of the typed versions, which is how SMS
// MFA preferences came to be dropped on one path and not the other.

// associateSoftwareToken — AssociateSoftwareToken
// Generates a TOTP secret for the caller and stores it on the user record
// (pending verification via VerifySoftwareToken). Accepts either an AccessToken
// or the Session of an MFA_SETUP challenge.
func (s *Service) associateSoftwareToken(w http.ResponseWriter, r *http.Request) {
	var req AssociateSoftwareTokenReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.AssociateSoftwareTokenTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	s.writeJSON(w, r, http.StatusOK, resp)
}

// verifySoftwareToken — VerifySoftwareToken
// Verifies that the user can produce a valid TOTP code for their stored secret.
// On success, marks TOTPVerified = true so MFA can be enabled.
func (s *Service) verifySoftwareToken(w http.ResponseWriter, r *http.Request) {
	var req VerifySoftwareTokenReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserCode, "UserCode") {
		return
	}
	resp, aerr := s.VerifySoftwareTokenTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	s.writeJSON(w, r, http.StatusOK, resp)
}

// setUserMFAPreference — SetUserMFAPreference
// Activates or deactivates the calling user's MFA factors.
func (s *Service) setUserMFAPreference(w http.ResponseWriter, r *http.Request) {
	var req SetUserMFAPreferenceReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.AccessToken, "AccessToken") {
		return
	}
	if _, aerr := s.SetUserMFAPreferenceTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}

// adminSetUserMFAPreference — AdminSetUserMFAPreference
// Like SetUserMFAPreference but identified by UserPoolId + Username.
func (s *Service) adminSetUserMFAPreference(w http.ResponseWriter, r *http.Request) {
	var req AdminSetUserMFAPreferenceReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if !serviceutil.RequireString(w, r, req.UserPoolID, "UserPoolId") {
		return
	}
	if !serviceutil.RequireString(w, r, req.Username, "Username") {
		return
	}
	if _, aerr := s.AdminSetUserMFAPreferenceTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{})
}
