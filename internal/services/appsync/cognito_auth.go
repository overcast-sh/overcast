package appsync

// cognito_auth.go — AMAZON_COGNITO_USER_POOLS authorization.
//
// Two postures, one code path, chosen by OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH:
//
//   - relaxed (default) — a Bearer token must be present and its payload is
//     decoded so claims reach $ctx.identity. Nothing about it is verified,
//     which is what lets a resolver test mint an unsigned JWT without standing
//     up Cognito. This is the emulator's documented posture (AGENTS.md
//     § Non-goals: "not a security boundary").
//   - strict — the token must be one the local Cognito service minted for the
//     user pool this API names. Verification is delegated to Cognito itself
//     through events.CognitoTokenValidator (the same narrow interface API
//     Gateway's authorizers use), so there is no HTTP call back into
//     Overcast's own JWKS endpoint and no key material is copied or cached
//     here: Cognito reads the pool's RSA key from the shared state.Store,
//     which is already the single source of truth for it. A rotated or
//     regenerated key is therefore picked up on the next request with nothing
//     to invalidate.
//
// Both the primary authentication type and every entry in
// additionalAuthenticationProviders reach this file through tryAuth, so the
// posture cannot differ between them.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// cognitoUserPoolConfig is the subset of AppSync's UserPoolConfig that
// authorization reads. AppSync stores the block as passthrough JSON
// (types.go), so it is decoded per request rather than at CreateGraphqlApi
// time.
type cognitoUserPoolConfig struct {
	UserPoolID       string `json:"userPoolId"`
	AppIDClientRegex string `json:"appIdClientRegex"`
}

// authenticateCognito authorizes a request under AMAZON_COGNITO_USER_POOLS and
// returns the documented $ctx.identity map.
//
// https://docs.aws.amazon.com/appsync/latest/devguide/resolver-context-reference.html
func (h *Handler) authenticateCognito(r *http.Request, userPoolCfg json.RawMessage) (map[string]any, *protocol.AWSError) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, unauthorizedError("Missing or invalid Authorization header for AMAZON_COGNITO_USER_POOLS.")
	}
	if h.cfg != nil && h.cfg.EnforceAppSyncCognitoAuth {
		claims, authErr := h.verifyCognitoToken(r, token, userPoolCfg)
		if authErr != nil {
			return nil, authErr
		}
		return cognitoIdentity(r, claims), nil
	}
	return cognitoIdentity(r, parseJWTClaims(token)), nil
}

// cognitoIdentity builds the AMAZON_COGNITO_USER_POOLS identity map. Group
// membership is not a field of its own on AWS — it reaches resolvers as the
// "cognito:groups" claim, so it needs no special handling here.
func cognitoIdentity(r *http.Request, claims map[string]any) map[string]any {
	return map[string]any{
		"sub":                 claims["sub"],
		"issuer":              claims["iss"],
		"username":            claimString(claims, "cognito:username", "username"),
		"claims":              claims,
		"sourceIp":            sourceIPs(r),
		"defaultAuthStrategy": "ALLOW",
	}
}

// verifyCognitoToken enforces the checks AWS applies to a user pool token,
// cheapest and most specific first, and returns the verified claims.
//
// The claim checks run before the signature check on purpose: they are the
// ones that decide whether this token even belongs to this API, and settling
// that first keeps a token from an unrelated issuer out of Cognito's key
// lookup entirely.
func (h *Handler) verifyCognitoToken(r *http.Request, token string, userPoolCfg json.RawMessage) (map[string]any, *protocol.AWSError) {
	var cfg cognitoUserPoolConfig
	if len(userPoolCfg) > 0 {
		if err := json.Unmarshal(userPoolCfg, &cfg); err != nil {
			return nil, unauthorizedError("Invalid userPoolConfig for AMAZON_COGNITO_USER_POOLS.")
		}
	}
	if cfg.UserPoolID == "" {
		return nil, unauthorizedError("No Cognito user pool is configured for AMAZON_COGNITO_USER_POOLS.")
	}
	if h.cognitoValidator == nil {
		return nil, unauthorizedError("Cognito token verification is unavailable.")
	}

	claims, err := serviceutil.DecodeJWTClaims(token)
	if err != nil {
		return nil, unauthorizedError("Unable to parse JWT token.")
	}

	// The pool is identified by the issuer's last path segment, never by
	// comparing the whole issuer string: Overcast's Cognito mints "iss" on the
	// caller's own origin (internal/services/cognito/handler_auth.go
	// § issuerURL, docs/plans/client-facing-url-minting.md), so two callers who
	// reached Overcast under different names hold different issuer strings for
	// the same pool and both must validate. AWS's own format —
	// https://cognito-idp.{region}.amazonaws.com/{poolId} — has the pool ID in
	// the same position, so a token from real Cognito for a same-named pool
	// matches too.
	iss, _ := claims["iss"].(string)
	if iss == "" || !strings.HasSuffix(iss, "/"+cfg.UserPoolID) {
		return nil, unauthorizedError("Token issuer is not the user pool configured for this API.")
	}

	// AppSync's Cognito mode is documented against user pool tokens, and a
	// user pool mints exactly two kinds a client can present. Anything else
	// (a refresh token, an OIDC token from another provider) is refused.
	// https://docs.aws.amazon.com/cognito/latest/developerguide/amazon-cognito-user-pools-using-the-id-token.html
	switch tokenUse, _ := claims["token_use"].(string); tokenUse {
	case "id", "access":
	default:
		return nil, unauthorizedError("Token is not an Amazon Cognito user pool ID or access token.")
	}

	// appIdClientRegex restricts which app clients of the pool may call the
	// API. The client ID is "aud" in an ID token and "client_id" in an access
	// token — Cognito renders the same value in both.
	if cfg.AppIDClientRegex != "" {
		matched, matchErr := regexp.MatchString(cfg.AppIDClientRegex, claimString(claims, "aud", "client_id"))
		if matchErr != nil || !matched {
			return nil, unauthorizedError("Token was not issued to an app client permitted by appIdClientRegex.")
		}
	}

	// Signature and expiry are Cognito's to answer: it holds the pool's key.
	verified, err := h.cognitoValidator.ValidateCognitoToken(r.Context(), token)
	if err != nil {
		h.log.Debug("rejected AppSync Cognito token",
			zap.String("userPoolId", cfg.UserPoolID), zap.Error(err))
		return nil, unauthorizedError("Unable to verify the Amazon Cognito token.")
	}
	return verified, nil
}
