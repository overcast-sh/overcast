package middleware

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// LambdaWebInvokePrefix is the path prefix HostAddressing rewrites a Lambda
// web function endpoint's Host-routed request under. The SigV4 middleware
// leaves these requests to the data plane's authoriser: by the time the
// middleware chain runs the path is no longer the one the client signed, and
// the authoriser knows the original.
const LambdaWebInvokePrefix = "/_overcast/lambdaweb/invoke/"

// ValidateSigV4 verifies a header-signed SigV4 request against the secret the
// resolver holds for its access key, falling back to DefaultSigV4Secret as the
// middleware does. canonicalPath is the canonical URI the client signed; ""
// means r's own escaped path. A caller whose path was rewritten after the
// client signed it passes the original. signed is false when r carries no
// SigV4 signature at all. The body is never read: the payload hash comes from
// X-Amz-Content-Sha256, which a caller that cannot trust a missing header must
// require first (without it this falls back to hashing the body).
func ValidateSigV4(r *http.Request, clk clock.Clock, resolver SecretResolver, canonicalPath string) (signed bool, aerr *protocol.AWSError) {
	if clk == nil {
		clk = clock.New()
	}
	return validateSigV4RequestAt(r, clk, resolver, canonicalPath)
}

// SigV4CanonicalRequest renders the canonical request the SigV4 and SigV4a
// signers hash, from r's method, query, headers and Host. canonicalPath is as
// for ValidateSigV4. It is exported for SigV4a verification, which signs the
// same canonical request under a different algorithm.
func SigV4CanonicalRequest(r *http.Request, signedHeaders []string, payloadHash, canonicalPath string) string {
	return canonicalRequestAt(r, signedHeaders, payloadHash, false, canonicalPath)
}

// SigningSecret returns the secret access key SigV4 validation signs with for
// accessKeyID: the resolver's, else DefaultSigV4Secret. SigV4a derives its
// ECDSA key from it.
func SigningSecret(r *http.Request, resolver SecretResolver, accessKeyID string) string {
	return resolveSecretForRequest(r, resolver, accessKeyID)
}
