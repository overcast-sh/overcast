package cognito

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// MFA factor names, spelled as AWS spells them in ChallengeName, in the
// MFAS_CAN_CHOOSE / MFAS_CAN_SETUP challenge parameters, and in
// UserMFASettingList / PreferredMfaSetting.
//
// Email MFA is deliberately absent. The pinned ChallengeNameType enum has no
// EMAIL_MFA member even though the RespondToAuthChallenge documentation offers
// it as a SELECT_MFA_TYPE answer, and no evidence was found for the value AWS
// reports in UserMFASettingList for it — so the emulator issues no email MFA
// challenge rather than inventing a wire value. EmailMfaConfiguration on the
// pool stays configuration-only.
const (
	mfaFactorSMS  = "SMS_MFA"
	mfaFactorTOTP = "SOFTWARE_TOKEN_MFA"
)

const (
	// mfaSessionTTL is how long a Session issued with an MFA challenge stays
	// usable. AWS gives MFA sessions three minutes.
	mfaSessionTTL = 3 * time.Minute

	// smsMfaCodeTTL bounds the delivered SMS_MFA code. It matches the session
	// lifetime, so in practice an expired session is reported before an
	// expired code; the code check is kept for the paths (SELECT_MFA_TYPE,
	// a re-delivered code) where the two can diverge.
	smsMfaCodeTTL = 3 * time.Minute
)

// mfaRequired reports whether the pool forces every user through MFA.
func mfaRequired(pool *UserPool) bool {
	return pool != nil && pool.MfaConfiguration == "ON"
}

// poolSmsMfaEnabled reports whether SMS is an available MFA factor on the pool.
// SmsMfaConfigType has no Enabled flag: its presence is what enables SMS.
func poolSmsMfaEnabled(pool *UserPool) bool {
	return pool != nil && pool.SmsMfaConfiguration != nil
}

// poolTotpMfaEnabled reports whether TOTP is an available MFA factor on the pool.
func poolTotpMfaEnabled(pool *UserPool) bool {
	return pool != nil && pool.SoftwareTokenMfaConfiguration != nil && pool.SoftwareTokenMfaConfiguration.Enabled
}

// poolMfaSetupOptions lists the factors an MFA_SETUP challenge can offer.
func poolMfaSetupOptions(pool *UserPool) []string {
	options := make([]string, 0, 2)
	if poolSmsMfaEnabled(pool) {
		options = append(options, mfaFactorSMS)
	}
	if poolTotpMfaEnabled(pool) {
		options = append(options, mfaFactorTOTP)
	}
	return options
}

// smsMfaActivated reports whether SMS MFA is active for this user.
//
// A user who called SetUserMFAPreference is taken at their word. Beyond that,
// AWS activates SMS MFA implicitly for any user with a verified phone number
// when the pool requires MFA and SMS is an enabled factor — the classic
// pre-TOTP behaviour that MFAOptions was built around ("Adding MFA to a user
// pool", Amazon Cognito Developer Guide). Without that, a required-MFA
// SMS-only pool would send every user to MFA_SETUP.
func smsMfaActivated(pool *UserPool, u *User) bool {
	if u == nil || u.phoneNumber() == "" {
		return false
	}
	if u.SMSMfaEnabled {
		return true
	}
	return mfaRequired(pool) && poolSmsMfaEnabled(pool) && u.getAttr("phone_number_verified") == "true"
}

// totpMfaActivated reports whether TOTP MFA is active for this user.
//
// Unlike SMS this is not gated on the pool's SoftwareTokenMfaConfiguration:
// AWS refuses SetUserMFAPreference for a factor the pool has not enabled, so a
// user preference already implies the pool allowed it, and gating again here
// would silently disable TOTP for every pool that never called
// SetUserPoolMfaConfig.
func totpMfaActivated(u *User) bool {
	return u != nil && u.MFAEnabled && u.TOTPVerified
}

// userMfaFactors lists the MFA factors active for the user, in the order AWS
// reports them.
func userMfaFactors(pool *UserPool, u *User) []string {
	factors := make([]string, 0, 2)
	if smsMfaActivated(pool, u) {
		factors = append(factors, mfaFactorSMS)
	}
	if totpMfaActivated(u) {
		factors = append(factors, mfaFactorTOTP)
	}
	return factors
}

// preferredMfaSetting returns the user's preferred factor, or "" when they have
// not chosen one or it is no longer active.
func preferredMfaSetting(pool *UserPool, u *User) string {
	if u == nil || u.PreferredMfa == "" {
		return ""
	}
	if !slices.Contains(userMfaFactors(pool, u), u.PreferredMfa) {
		return ""
	}
	return u.PreferredMfa
}

// maskPhoneNumber renders a phone number the way AWS renders it in
// CODE_DELIVERY_DESTINATION: the leading "+" and the last four digits are kept
// and everything between them is masked.
func maskPhoneNumber(phone string) string {
	if phone == "" {
		return ""
	}
	prefix := ""
	rest := phone
	if strings.HasPrefix(phone, "+") {
		prefix, rest = "+", phone[1:]
	}
	if len(rest) <= 4 {
		return prefix + rest
	}
	return prefix + strings.Repeat("*", len(rest)-4) + rest[len(rest)-4:]
}

// maskEmailAddress renders an email address the way AWS renders it in
// CodeDeliveryDetails: the first character and the domain are kept, for
// example j***@example.com. An address with nothing before the @, or none at
// all, is returned without a leading character rather than indexed off its own
// end.
func maskEmailAddress(email string) string {
	at := strings.Index(email, "@")
	switch {
	case at < 0:
		return email
	case at > 1:
		return email[:1] + "***" + email[at:]
	default:
		return "***" + email[at:]
	}
}

// challengeParameterList renders a factor list the way ChallengeParameters
// carries one: a JSON array inside a string value.
func challengeParameterList(values []string) string {
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// startMfaChallenge decides which MFA challenge, if any, stands between a
// successful first factor and the user's tokens. A nil response means no
// challenge is owed and the caller should issue tokens.
func (s *Service) startMfaChallenge(ctx context.Context, pool *UserPool, u *User) (*InitiateAuthResp, *protocol.AWSError) {
	factors := userMfaFactors(pool, u)
	if len(factors) == 0 {
		if !mfaRequired(pool) {
			return nil, nil
		}
		canSetup := poolMfaSetupOptions(pool)
		if len(canSetup) == 0 {
			return nil, nil
		}
		session, err := s.issueOpaqueToken(ctx, u.UserPoolID, u.Username, "mfa", mfaSessionTTL)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		return &InitiateAuthResp{
			ChallengeName: "MFA_SETUP",
			Session:       session,
			ChallengeParameters: map[string]string{
				"MFAS_CAN_SETUP":  challengeParameterList(canSetup),
				"USER_ID_FOR_SRP": u.Username,
			},
		}, nil
	}
	if len(factors) > 1 {
		if preferred := preferredMfaSetting(pool, u); preferred != "" {
			factors = []string{preferred}
		}
	}
	session, err := s.issueOpaqueToken(ctx, u.UserPoolID, u.Username, "mfa", mfaSessionTTL)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if len(factors) > 1 {
		return &InitiateAuthResp{
			ChallengeName: "SELECT_MFA_TYPE",
			Session:       session,
			ChallengeParameters: map[string]string{
				"MFAS_CAN_CHOOSE": challengeParameterList(factors),
				"USER_ID_FOR_SRP": u.Username,
			},
		}, nil
	}
	return s.mfaFactorChallenge(ctx, pool, u, factors[0], session)
}

// mfaFactorChallenge builds the challenge for one chosen factor, delivering a
// code first where the factor needs one.
func (s *Service) mfaFactorChallenge(ctx context.Context, pool *UserPool, u *User, factor, session string) (*InitiateAuthResp, *protocol.AWSError) {
	switch factor {
	case mfaFactorSMS:
		params, aerr := s.deliverSmsMfaCode(ctx, pool, u)
		if aerr != nil {
			return nil, aerr
		}
		return &InitiateAuthResp{ChallengeName: mfaFactorSMS, Session: session, ChallengeParameters: params}, nil
	case mfaFactorTOTP:
		return &InitiateAuthResp{ChallengeName: mfaFactorTOTP, Session: session, ChallengeParameters: map[string]string{}}, nil
	default:
		return nil, errMFAMethodNotFound(factor)
	}
}

// deliverSmsMfaCode stores a fresh SMS_MFA code on the user, sends it, and
// returns the ChallengeParameters describing the delivery.
func (s *Service) deliverSmsMfaCode(ctx context.Context, pool *UserPool, u *User) (map[string]string, *protocol.AWSError) {
	phone := u.phoneNumber()
	if phone == "" {
		return nil, errMFAMethodNotFound(mfaFactorSMS)
	}
	code := generateCode()
	setAuthChallengeCode(u, mfaFactorSMS, code, s.clk.Now().Add(smsMfaCodeTTL))
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.sendMfaSMS(pool, phone, u.Username, code)
	return map[string]string{
		"CODE_DELIVERY_DELIVERY_MEDIUM": "SMS",
		"CODE_DELIVERY_DESTINATION":     maskPhoneNumber(phone),
		"USER_ID_FOR_SRP":               u.Username,
	}, nil
}

// asRespondResp re-shapes an MFA challenge for the RespondToAuthChallenge
// response, which carries the same four fields under a different type.
func asRespondResp(resp *InitiateAuthResp) *RespondToAuthChallengeResp {
	if resp == nil {
		return nil
	}
	return &RespondToAuthChallengeResp{
		AuthenticationResult: resp.AuthenticationResult,
		ChallengeName:        resp.ChallengeName,
		ChallengeParameters:  resp.ChallengeParameters,
		Session:              resp.Session,
	}
}

// requireMfaSession resolves the user behind an in-flight MFA session.
func (s *Service) requireMfaSession(ctx context.Context, poolID, session string) (*Token, *UserPool, *User, *protocol.AWSError) {
	if session == "" {
		return nil, nil, nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Session is required.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, nil, nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "mfa" || st.UserPoolID != poolID {
		return nil, nil, nil, errNotAuthorized("Invalid MFA session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, nil, nil, errNotAuthorized("MFA session has expired.")
	}
	pool, aerr := s.requirePoolTyped(ctx, poolID)
	if aerr != nil {
		return nil, nil, nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, poolID, st.Username)
	if aerr != nil {
		return nil, nil, nil, aerr
	}
	return st, pool, u, nil
}

// completeSmsMfaChallengeTyped answers an SMS_MFA challenge with the delivered
// code and issues tokens.
func (s *Service) completeSmsMfaChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	code := responses["SMS_MFA_CODE"]
	if code == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "SMS_MFA_CODE is required in ChallengeResponses.", HTTPStatus: 400}
	}
	_, _, u, aerr := s.requireMfaSession(ctx, client.UserPoolID, session)
	if aerr != nil {
		return nil, aerr
	}
	stored, ok := authChallengeCode(u, mfaFactorSMS)
	if !ok || stored.Code != code {
		return nil, errCodeMismatch()
	}
	if !stored.ExpiresAt.IsZero() && s.clk.Now().After(stored.ExpiresAt) {
		removeAuthChallengeCode(u, mfaFactorSMS)
		_ = s.saveUser(ctx, u)
		return nil, errExpiredCode()
	}
	removeAuthChallengeCode(u, mfaFactorSMS)
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	_ = s.removeToken(ctx, session)
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	s.log.WithRecorder(ctx).Info("user completed SMS MFA challenge",
		zap.String("poolId", client.UserPoolID), zap.String("username", u.Username))
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: u.Username})
	return &RespondToAuthChallengeResp{AuthenticationResult: result}, nil
}

// handleSelectMfaTypeChallengeTyped answers a SELECT_MFA_TYPE challenge by
// moving the same session on to the chosen factor's own challenge.
func (s *Service) handleSelectMfaTypeChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	answer := responses["ANSWER"]
	if answer == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "ANSWER is required in ChallengeResponses.", HTTPStatus: 400}
	}
	_, pool, u, aerr := s.requireMfaSession(ctx, client.UserPoolID, session)
	if aerr != nil {
		return nil, aerr
	}
	if !slices.Contains(userMfaFactors(pool, u), answer) {
		return nil, errMFAMethodNotFound(answer)
	}
	resp, aerr := s.mfaFactorChallenge(ctx, pool, u, answer, session)
	if aerr != nil {
		return nil, aerr
	}
	return asRespondResp(resp), nil
}

// completeMfaSetupChallengeTyped finishes an MFA_SETUP challenge. AWS completes
// TOTP setup through AssociateSoftwareToken and VerifySoftwareToken carrying
// the challenge session; this call is what turns that verified factor on and
// issues the tokens.
// The challenge responses carry only USERNAME, which the session already
// identifies, so they are ignored.
func (s *Service) completeMfaSetupChallengeTyped(ctx context.Context, client *UserPoolClient, session string, _ map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	_, _, u, aerr := s.requireMfaSession(ctx, client.UserPoolID, session)
	if aerr != nil {
		return nil, aerr
	}
	if !u.TOTPVerified {
		return nil, &protocol.AWSError{
			Code:       "SoftwareTokenMFANotFoundException",
			Message:    "Software token TOTP MFA not found.",
			HTTPStatus: 400,
		}
	}
	u.MFAEnabled = true
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	_ = s.removeToken(ctx, session)
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	s.log.WithRecorder(ctx).Info("user completed MFA setup challenge",
		zap.String("poolId", client.UserPoolID), zap.String("username", u.Username))
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: u.Username})
	return &RespondToAuthChallengeResp{AuthenticationResult: result}, nil
}

// applyMfaPreferences writes the SetUserMFAPreference/AdminSetUserMFAPreference
// settings onto the user.
func applyMfaPreferences(u *User, sms, softwareToken *MfaSettings) *protocol.AWSError {
	if sms != nil && softwareToken != nil && sms.PreferredMfa && softwareToken.PreferredMfa {
		return &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Only one MFA method can be set as preferred.",
			HTTPStatus: 400,
		}
	}
	if sms != nil {
		if sms.Enabled && u.phoneNumber() == "" {
			return &protocol.AWSError{
				Code:       "InvalidParameterException",
				Message:    "User does not have a phone number to receive SMS MFA messages.",
				HTTPStatus: 400,
			}
		}
		u.SMSMfaEnabled = sms.Enabled
		if sms.Enabled && sms.PreferredMfa {
			u.PreferredMfa = mfaFactorSMS
		} else if u.PreferredMfa == mfaFactorSMS {
			u.PreferredMfa = ""
		}
	}
	if softwareToken != nil {
		if softwareToken.Enabled && !u.TOTPVerified {
			return &protocol.AWSError{
				Code:       "InvalidParameterException",
				Message:    "You must verify your software token before enabling MFA.",
				HTTPStatus: 400,
			}
		}
		u.MFAEnabled = softwareToken.Enabled
		if softwareToken.Enabled && softwareToken.PreferredMfa {
			u.PreferredMfa = mfaFactorTOTP
		} else if u.PreferredMfa == mfaFactorTOTP {
			u.PreferredMfa = ""
		}
	}
	return nil
}

// softwareTokenEnrolment resolves the user enrolling a software token from
// either an access token (a signed-in user adding a factor) or the Session of
// an MFA_SETUP challenge (a user adding one mid-sign-in), and returns the
// session the response must carry back — empty for the access-token form.
//
// AWS hands a fresh Session out of every step of the setup flow, so the
// session form consumes the session it was given and mints its successor.
func (s *Service) softwareTokenEnrolment(ctx context.Context, accessToken, session string) (*User, string, *protocol.AWSError) {
	switch {
	case accessToken != "":
		t, aerr := s.validateAccessTokenTyped(ctx, accessToken)
		if aerr != nil {
			return nil, "", aerr
		}
		u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
		if aerr != nil {
			return nil, "", aerr
		}
		return u, "", nil
	case session != "":
		st, err := s.loadToken(ctx, session)
		if err != nil {
			return nil, "", protocol.Wrap(protocol.ErrInternalError, err)
		}
		if st == nil || st.Type != "mfa" {
			return nil, "", errNotAuthorized("Invalid MFA session.")
		}
		if s.clk.Now().After(st.ExpiresAt) {
			return nil, "", errNotAuthorized("MFA session has expired.")
		}
		u, aerr := s.requireUserTyped(ctx, st.UserPoolID, st.Username)
		if aerr != nil {
			return nil, "", aerr
		}
		_ = s.removeToken(ctx, session)
		next, err := s.issueOpaqueToken(ctx, st.UserPoolID, st.Username, "mfa", mfaSessionTTL)
		if err != nil {
			return nil, "", protocol.Wrap(protocol.ErrInternalError, err)
		}
		return u, next, nil
	default:
		return nil, "", &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "AccessToken or Session is required.",
			HTTPStatus: 400,
		}
	}
}

func errMFAMethodNotFound(factor string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "MFAMethodNotFoundException",
		Message:    "MFA method " + factor + " is not available for this user.",
		HTTPStatus: 400,
	}
}
