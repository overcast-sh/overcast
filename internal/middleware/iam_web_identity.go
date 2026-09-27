package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const iamActionAssumeRoleWithWebIdentity = "sts:AssumeRoleWithWebIdentity"

// webIdentityDenied is what AWS answers an AssumeRoleWithWebIdentity the
// role's trust policy does not admit, whatever the reason, a role that does
// not exist included.
func webIdentityDenied() *protocol.AWSError {
	return queryAccessDenied("sts", "Not authorized to perform "+iamActionAssumeRoleWithWebIdentity)
}

// errNoSuchRole is a RoleArn that names no role in the store.
var errNoSuchRole = errors.New("no such role")

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
// by the trust policy of the role named by RoleArn. It returns the error to
// answer the call with, or nil when it may be served.
//
// The token's claims are read but, as with SigV4 credentials, not verified:
// its signature, expiry and whether its issuer is a registered OIDC provider
// are not checked.
func authorizeWebIdentity(r *http.Request, st state.Store, logger *zap.Logger) *protocol.AWSError {
	for _, param := range []string{"RoleArn", "RoleSessionName", "WebIdentityToken"} {
		if r.Form.Get(param) == "" {
			return protocol.ErrMissingParameter(param)
		}
	}
	caller, aerr := webIdentityCallerOf(r)
	if aerr != nil {
		return aerr
	}
	roleARN := r.Form.Get("RoleArn")
	trust, err := roleTrustPolicy(r.Context(), st, roleARN)
	switch {
	case errors.Is(err, errNoSuchRole):
		return refuseWebIdentity(logger, roleARN, "the role does not exist")
	case err != nil:
		if logger != nil {
			logger.Error("iam enforcement could not read a role's trust policy",
				zap.String("role", roleARN), zap.Error(err))
		}
		return protocol.Wrap(protocol.ErrInternalError, err)
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
		return webIdentityDenied()
	}
	if res.Decision != iampolicy.DecisionAllowed {
		return refuseWebIdentity(logger, roleARN, "the trust policy did not allow the web identity")
	}
	return nil
}

// refuseWebIdentity logs why a web identity was refused, and is the
// AccessDenied AWS gives it.
func refuseWebIdentity(logger *zap.Logger, roleARN, reason string) *protocol.AWSError {
	if logger != nil {
		logger.Debug("iam enforcement denied a web identity",
			zap.String("role", roleARN),
			zap.String("reason", reason),
		)
	}
	return webIdentityDenied()
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
// names its provider in ProviderId, and sets no claim keys; anything else must
// be an OpenID Connect ID token naming its issuer.
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
	provider := strings.TrimSuffix(strings.TrimPrefix(issuer, "https://"), "/")
	return webIdentityCaller{provider: provider, claims: oidcConditionKeys(provider, claims)}, nil
}

// oidcConditionKeys are the <provider>:sub and <provider>:aud condition keys
// an ID token sets, lower-cased as iampolicy.Request.Context is keyed.
//
// TODO(priority:P2): <provider>:amr, which Cognito identity pool roles test
// with ForAnyValue:StringLike, is a list, and neither the context nor the
// evaluator's set operators carry one yet, so such a trust policy fails
// closed (#2279).
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
// with. The IAM User Guide ("Available keys for AWS OIDC federation") gives
// accounts.google.com:aud the token's azp claim when it has one; it is applied
// here to every provider, since azp names the party the token was issued to.
// Otherwise it is aud, which a JWT may give as a list; the context holds one
// value, so a list contributes its first.
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
//
// The token's claim keys go in first, so no key derived from the request
// itself can be overwritten by a claim named after an untrusted issuer.
func (c webIdentityCaller) request(r *http.Request, roleARN string) iampolicy.Request {
	reqCtx := make(map[string]string, len(c.claims)+6)
	for k, v := range c.claims {
		reqCtx[k] = v
	}
	for k, v := range buildIAMRequestContext(r, "sts") {
		reqCtx[k] = v
	}
	reqCtx["sts:rolesessionname"] = r.Form.Get("RoleSessionName")
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

// roleTrustPolicy compiles the trust policy of the role roleARN names, or
// reports errNoSuchRole. A trust policy that does not parse compiles to no
// statements, which admit nothing.
func roleTrustPolicy(ctx context.Context, st state.Store, roleARN string) ([]iampolicy.Statement, error) {
	raw, found, err := iamRecordByARN(ctx, st, iamRolesNamespace, roleARN)
	if err != nil {
		return nil, err
	}
	var role iamRoleRecord
	if !found || json.Unmarshal([]byte(raw), &role) != nil {
		return nil, errNoSuchRole
	}
	trust, _ := iampolicy.ParseDocument(role.AssumeRolePolicyDocument, iampolicy.SourceRef{
		ID:   roleARN,
		Type: iampolicy.SourceTypeResourcePolicy,
	})
	return trust, nil
}
