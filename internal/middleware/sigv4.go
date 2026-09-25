package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
)

type sigV4AccessKeyContextKey struct{}

// SigV4AccessKeyFromContext returns the credential access key attached to a
// signed request. It is available even when signature validation is disabled.
func SigV4AccessKeyFromContext(ctx context.Context) string {
	accessKey, _ := ctx.Value(sigV4AccessKeyContextKey{}).(string)
	return accessKey
}

const (
	defaultSigV4Secret = "test"
	sigV4Algorithm     = "AWS4-HMAC-SHA256"
	maxSigV4ClockSkew  = 5 * time.Minute
	unsignedPayload    = "UNSIGNED-PAYLOAD"
)

// DefaultSigV4Secret is the fallback signing secret used when no
// SecretResolver is configured or when the resolver cannot find a
// secret for the given access key. The value "test" matches the
// default credentials used by the AWS SDK and CLI in local-dev
// workflows.
const DefaultSigV4Secret = defaultSigV4Secret

// credentialScope parses the SigV4 Authorization header's Credential scope.
// Returns the parts: [accessKey, date, region, service, "aws4_request"].
// Returns nil if not parseable.
func credentialScope(r *http.Request) []string {
	auth := r.Header.Get("Authorization")
	const prefix = "Credential="
	idx := strings.Index(auth, prefix)
	if idx < 0 {
		return nil
	}
	cred := auth[idx+len(prefix):]
	if i := strings.IndexByte(cred, ','); i >= 0 {
		cred = cred[:i]
	}
	parts := strings.SplitN(cred, "/", 6)
	if len(parts) < 4 {
		return nil
	}
	return parts
}

// SigV4 validates AWS SigV4 signed requests when validation is enabled.
// Unsigned requests still pass through so emulator-internal endpoints and
// local no-auth workflows remain usable.
//
// secretResolver optionally resolves per-access-key secrets from IAM.
// When nil or when it returns no match the middleware falls back to
// DefaultSigV4Secret ("test") for backward compatibility.
func SigV4(validate bool, secretResolver SecretResolver, logger *zap.Logger, clk clock.Clock) func(http.Handler) http.Handler {
	if clk == nil {
		clk = clock.New()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if parts, signed, _ := extractSigV4Parts(r); signed && strings.TrimSpace(parts.AccessKey) != "" {
				r = r.WithContext(context.WithValue(r.Context(), sigV4AccessKeyContextKey{}, parts.AccessKey))
			}
			if validate {
				signed, err := validateSigV4Request(r, clk, secretResolver)
				if err != nil {
					writeSigV4Error(w, r, err)
					return
				}
				if signed {
					logger.Debug("validated SigV4 request",
						zap.String("service", detectService(r)),
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
					)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type sigV4Parts struct {
	AccessKey     string
	Date          string
	Region        string
	Service       string
	Scope         string
	SignedHeaders []string
	Signature     string
	AmzDate       string
	Expires       int
	Presigned     bool
}

func validateSigV4Request(r *http.Request, clk clock.Clock, secretResolver SecretResolver) (bool, *protocol.AWSError) {
	parts, signed, err := extractSigV4Parts(r)
	if err != nil {
		return true, err
	}
	if !signed {
		return false, nil
	}
	if parts.AccessKey == "" {
		return true, invalidSignature("missing access key")
	}
	t, parseErr := time.Parse("20060102T150405Z", parts.AmzDate)
	if parseErr != nil {
		return true, invalidSignature("invalid X-Amz-Date")
	}
	if delta := clk.Now().Sub(t); delta > maxSigV4ClockSkew || delta < -maxSigV4ClockSkew {
		return true, invalidSignature("signature expired or clock skew too large")
	}
	if parts.Presigned && parts.Expires > 0 && clk.Now().After(t.Add(time.Duration(parts.Expires)*time.Second)) {
		return true, invalidSignature("presigned URL has expired")
	}
	payloadHash, aerr := payloadHashForRequest(r, parts.Presigned)
	if aerr != nil {
		return true, aerr
	}
	canonicalRequest := canonicalRequest(r, parts.SignedHeaders, payloadHash, parts.Presigned)
	stringToSign := strings.Join([]string{
		sigV4Algorithm,
		parts.AmzDate,
		parts.Scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	secret := resolveSecretForRequest(r, secretResolver, parts.AccessKey)
	expected := computeSignature(secret, parts.Date, parts.Region, parts.Service, stringToSign)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(expected)), []byte(strings.ToLower(parts.Signature))) != 1 {
		return true, invalidSignature("signature mismatch")
	}
	return true, nil
}

// resolveSecretForRequest returns the signing secret for the given access key.
// It first consults the SecretResolver (IAM users, STS sessions). If none is
// found it falls back to DefaultSigV4Secret so existing local-dev workflows
// keep working without IAM setup.
func resolveSecretForRequest(r *http.Request, resolver SecretResolver, accessKeyID string) string {
	if resolver != nil {
		secret, found, err := resolver.ResolveSecret(r.Context(), accessKeyID)
		if err == nil && found && secret != "" {
			return secret
		}
	}
	return DefaultSigV4Secret
}

func extractSigV4Parts(r *http.Request) (sigV4Parts, bool, *protocol.AWSError) {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		parts, err := parseAuthorizationHeader(auth)
		if err != nil {
			return sigV4Parts{}, true, invalidSignature(err.Error())
		}
		parts.AmzDate = r.Header.Get("X-Amz-Date")
		if parts.AmzDate == "" {
			return sigV4Parts{}, true, invalidSignature("missing X-Amz-Date header")
		}
		return parts, true, nil
	}
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") == "" && q.Get("X-Amz-Signature") == "" {
		return sigV4Parts{}, false, nil
	}
	if q.Get("X-Amz-Algorithm") != sigV4Algorithm {
		return sigV4Parts{}, true, invalidSignature("unsupported X-Amz-Algorithm")
	}
	parts, err := parsePresignedQuery(q)
	if err != nil {
		return sigV4Parts{}, true, invalidSignature(err.Error())
	}
	parts.Presigned = true
	return parts, true, nil
}

func parseAuthorizationHeader(auth string) (sigV4Parts, error) {
	if !strings.HasPrefix(auth, sigV4Algorithm+" ") {
		return sigV4Parts{}, fmt.Errorf("unsupported authorization algorithm")
	}
	rest := strings.TrimPrefix(auth, sigV4Algorithm+" ")
	fields := strings.Split(rest, ",")
	values := map[string]string{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[parts[0]] = parts[1]
	}
	cred := values["Credential"]
	credParts := strings.Split(cred, "/")
	if len(credParts) != 5 {
		return sigV4Parts{}, fmt.Errorf("invalid credential scope")
	}
	signedHeaders := strings.Split(values["SignedHeaders"], ";")
	if len(signedHeaders) == 0 || signedHeaders[0] == "" {
		return sigV4Parts{}, fmt.Errorf("missing signed headers")
	}
	return sigV4Parts{
		AccessKey:     credParts[0],
		Date:          credParts[1],
		Region:        credParts[2],
		Service:       credParts[3],
		Scope:         strings.Join(credParts[1:], "/"),
		SignedHeaders: signedHeaders,
		Signature:     values["Signature"],
	}, nil
}

func parsePresignedQuery(q url.Values) (sigV4Parts, error) {
	cred := q.Get("X-Amz-Credential")
	credParts := strings.Split(cred, "/")
	if len(credParts) != 5 {
		return sigV4Parts{}, fmt.Errorf("invalid X-Amz-Credential scope")
	}
	expires, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil {
		return sigV4Parts{}, fmt.Errorf("invalid X-Amz-Expires")
	}
	signedHeaders := strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	if len(signedHeaders) == 0 || signedHeaders[0] == "" {
		return sigV4Parts{}, fmt.Errorf("missing X-Amz-SignedHeaders")
	}
	sig := q.Get("X-Amz-Signature")
	if sig == "" {
		return sigV4Parts{}, fmt.Errorf("missing X-Amz-Signature")
	}
	return sigV4Parts{
		AccessKey:     credParts[0],
		Date:          credParts[1],
		Region:        credParts[2],
		Service:       credParts[3],
		Scope:         strings.Join(credParts[1:], "/"),
		SignedHeaders: signedHeaders,
		Signature:     sig,
		AmzDate:       q.Get("X-Amz-Date"),
		Expires:       expires,
	}, nil
}

func payloadHashForRequest(r *http.Request, presigned bool) (string, *protocol.AWSError) {
	if v := r.Header.Get("X-Amz-Content-Sha256"); v != "" {
		return v, nil
	}
	if presigned {
		return unsignedPayload, nil
	}
	if r.Body == nil {
		return sha256Hex(nil), nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", protocol.Wrap(protocol.ErrInternalError, err)
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return sha256Hex(body), nil
}

func canonicalRequest(r *http.Request, signedHeaders []string, payloadHash string, presigned bool) string {
	return strings.Join([]string{
		r.Method,
		canonicalPath(r.URL),
		canonicalQuery(r.URL.Query(), presigned),
		canonicalHeaders(r, signedHeaders),
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n")
}

func canonicalPath(u *url.URL) string {
	if u == nil || u.EscapedPath() == "" {
		return "/"
	}
	return u.EscapedPath()
}

func canonicalQuery(values url.Values, presigned bool) string {
	type pair struct{ key, value string }
	pairs := make([]pair, 0)
	for key, vals := range values {
		if presigned && strings.EqualFold(key, "X-Amz-Signature") {
			continue
		}
		sort.Strings(vals)
		for _, value := range vals {
			pairs = append(pairs, pair{key: awsURLEscape(key), value: awsURLEscape(value)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.key+"="+p.value)
	}
	return strings.Join(out, "&")
}

func canonicalHeaders(r *http.Request, signedHeaders []string) string {
	parts := make([]string, 0, len(signedHeaders))
	for _, h := range signedHeaders {
		name := strings.ToLower(h)
		parts = append(parts, name+":"+normalizeHeaderValue(headerValue(r, name))+"\n")
	}
	return strings.Join(parts, "")
}

func headerValue(r *http.Request, name string) string {
	if name == "host" {
		if r.Host != "" {
			return r.Host
		}
		return r.URL.Host
	}
	return r.Header.Get(name)
}

func normalizeHeaderValue(v string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(v)), " ")
}

func computeSignature(secret, date, region, service, stringToSign string) string {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	return hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(msg))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	if b == nil {
		b = []byte{}
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func awsURLEscape(s string) string {
	encoded := url.QueryEscape(s)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}

func invalidSignature(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidSignatureException",
		Message:    msg,
		HTTPStatus: http.StatusForbidden,
	}
}

func writeSigV4Error(w http.ResponseWriter, r *http.Request, aerr *protocol.AWSError) {
	// Use identified codec from protocol middleware (always-on).
	if c, _ := codec.FromContext(r.Context()); c != nil {
		c.WriteError(w, r, aerr)
		return
	}
	// Legacy fallback: codec not identified (e.g. S3 bespoke routes).
	service := detectService(r)
	switch service {
	case "s3", "cloudfront":
		protocol.WriteXMLError(w, r, aerr)
	case "sns", "iam", "sts", "ec2", "cloudformation", "rds", "ses", "cloudwatch":
		protocol.WriteQueryXMLError(w, r, aerr)
	default:
		protocol.WriteJSONError(w, r, aerr)
	}
}

// CredentialAccessKey is the access key a request's SigV4 Authorization
// header names, or "" when it names none.
func CredentialAccessKey(r *http.Request) string {
	if scope := credentialScope(r); scope != nil {
		return scope[0]
	}
	return ""
}
