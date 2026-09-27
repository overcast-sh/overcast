package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const iamActionAssumeRoleWithWebIdentity = "sts:AssumeRoleWithWebIdentity"

// webIdentityDeniedMessage is what AWS answers an AssumeRoleWithWebIdentity
// the role's trust policy does not allow, whatever the reason.
const webIdentityDeniedMessage = "Not authorized to perform " + iamActionAssumeRoleWithWebIdentity

// iamAuthorizedByTrustPolicy reports whether AWS authorises op by the trust
// policy of the role it assumes rather than by the caller's identity
// policies. Only sts:AssumeRoleWithWebIdentity is: the STS API Reference says
// calling it "does not require the use of AWS security credentials", so an SDK
// sends it unsigned and there is no caller principal to hold policies.
//
// sts:AssumeRoleWithSAML is authorised the same way on AWS, but Overcast does
// not serve it: the router answers it 501, so it names no operation here and
// an unsigned one is refused like any other.
//
// As with iamServedWithoutPermission, the router must serve the request as
// that Query call (op.query). An ?Action=AssumeRoleWithWebIdentity on a
// request routed anywhere else is not that call and borrows nothing.
func iamAuthorizedByTrustPolicy(op iamOperation) bool {
	return op.query && op.action == iamActionAssumeRoleWithWebIdentity
}

// authorizeWebIdentity decides an AssumeRoleWithWebIdentity call as AWS does,
// by the trust policy of the role named by RoleArn, and answers it when that
// refuses it. It reports whether the call may be served.
//
// The token's claims are read but, as with SigV4 credentials, not verified:
// its signature, expiry and whether its issuer is a registered OIDC provider
// are not checked.
func authorizeWebIdentity(w http.ResponseWriter, r *http.Request, st state.Store, logger *zap.Logger) bool {
	caller, aerr := webIdentityCallerOf(r)
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return false
	}
	roleARN := r.Form.Get("RoleArn")
	trust, err := roleTrustPolicy(r.Context(), st, roleARN)
	if err != nil {
		return refuseWebIdentity(w, r, logger, err.Error())
	}
	res := iampolicy.Evaluate(iampolicy.Input{
		Request:        caller.request(r, roleARN),
		ResourcePolicy: trust,
	})
	if len(res.Unsupported) > 0 {
		if logger != nil {
			logger.Warn("iam enforcement denied a web identity it could not evaluate",
				zap.String("role", roleARN),
				zap.Strings("unsupported", res.Unsupported),
			)
		}
		return refuseWebIdentity(w, r, logger, "trust policy uses a construct the evaluator does not implement")
	}
	if res.Decision != iampolicy.DecisionAllowed {
		return refuseWebIdentity(w, r, logger, "trust policy did not allow the web identity")
	}
	return true
}

// refuseWebIdentity answers a web identity the trust policy did not admit,
// with the 403 AccessDenied AWS gives whatever the reason, and logs why.
func refuseWebIdentity(w http.ResponseWriter, r *http.Request, logger *zap.Logger, reason string) bool {
	if logger != nil {
		logger.Debug("iam enforcement denied a web identity",
			zap.String("role", r.Form.Get("RoleArn")),
			zap.String("reason", reason),
		)
	}
	protocol.WriteQueryXMLError(w, r, queryAccessDenied("sts", webIdentityDeniedMessage))
	return false
}

// webIdentityCaller is the identity an AssumeRoleWithWebIdentity call
// presents: the provider that issued its token, and the claims a trust
// policy's conditions read, keyed as IAM condition keys.
type webIdentityCaller struct {
	provider string
	claims   map[string]string
}

// webIdentityCallerOf reads the caller's identity from the request's
// WebIdentityToken. An OAuth 2.0 access token (Amazon, Facebook) is opaque and
// names its provider in ProviderId; anything else must be an OpenID Connect
// ID token naming its issuer.
func webIdentityCallerOf(r *http.Request) (webIdentityCaller, *protocol.AWSError) {
	if providerID := strings.TrimSpace(r.Form.Get("ProviderId")); providerID != "" {
		return webIdentityCaller{provider: providerID}, nil
	}
	claims, err := serviceutil.DecodeJWTClaims(r.Form.Get("WebIdentityToken"))
	issuer, _ := claims["iss"].(string)
	if err != nil || issuer == "" {
		return webIdentityCaller{}, &protocol.AWSError{
			Code:       "InvalidIdentityToken",
			Message:    "The ID Token provided is not a valid JWT. (You may see this error if you sent an Access Token)",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	provider := strings.TrimPrefix(issuer, "https://")
	return webIdentityCaller{provider: provider, claims: oidcConditionKeys(provider, claims)}, nil
}

// oidcConditionKeys are the <provider>:sub and <provider>:aud condition keys
// an ID token sets. As the IAM User Guide documents, aud carries the token's
// azp claim when it has one, and its aud claim otherwise.
func oidcConditionKeys(provider string, claims map[string]any) map[string]string {
	prefix := strings.ToLower(provider) + ":"
	keys := make(map[string]string, 2)
	if sub, ok := claims["sub"].(string); ok {
		keys[prefix+"sub"] = sub
	}
	if aud := jwtAudience(claims); aud != "" {
		keys[prefix+"aud"] = aud
	}
	return keys
}

// jwtAudience is the audience a trust policy's aud condition key is compared
// with: azp when set, else aud, which a JWT may give as a string or a list.
func jwtAudience(claims map[string]any) string {
	if azp, ok := claims["azp"].(string); ok && azp != "" {
		return azp
	}
	switch aud := claims["aud"].(type) {
	case string:
		return aud
	case []any:
		if len(aud) > 0 {
			first, _ := aud[0].(string)
			return first
		}
	}
	return ""
}

// request is the evaluation of the caller assuming roleARN. A trust policy
// names the provider as its IAM oidc-provider ARN in the role's account, or,
// for a provider AWS knows (accounts.google.com, cognito-identity.amazonaws.com),
// by its bare name; the caller matches either.
func (c webIdentityCaller) request(r *http.Request, roleARN string) iampolicy.Request {
	reqCtx := buildIAMRequestContext(r, "sts")
	for k, v := range c.claims {
		reqCtx[k] = v
	}
	return iampolicy.Request{
		Action:   iamActionAssumeRoleWithWebIdentity,
		Resource: roleARN,
		Context:  reqCtx,
		Federated: []string{
			c.provider,
			"arn:aws:iam::" + accountFromARN(roleARN) + ":oidc-provider/" + c.provider,
		},
	}
}

// roleTrustPolicy compiles the trust policy of the role roleARN names. A role
// that does not exist, or whose trust policy cannot be read, is an error: AWS
// refuses both with the same AccessDenied as a trust policy that does not
// allow the caller.
func roleTrustPolicy(ctx context.Context, st state.Store, roleARN string) ([]iampolicy.Statement, error) {
	roleName := roleARN[strings.LastIndex(roleARN, "/")+1:]
	raw, found, err := st.Get(ctx, iamRolesNamespace, roleName)
	if err != nil {
		return nil, err
	}
	var role iamRoleRecord
	if !found || json.Unmarshal([]byte(raw), &role) != nil || role.Arn != roleARN {
		return nil, fmt.Errorf("no readable role %q", roleARN)
	}
	return iampolicy.ParseDocument(role.AssumeRolePolicyDocument, iampolicy.SourceRef{
		ID:   roleARN,
		Type: iampolicy.SourceTypeResourcePolicy,
	})
}
