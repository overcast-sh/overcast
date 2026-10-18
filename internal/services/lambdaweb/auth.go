package lambdaweb

// auth.go — authorisation of data-plane requests to IamAuth endpoints
// (docs/plans/lambda-web-data-plane.md, "IamAuth").
//
// AWS: every request to an IamAuth endpoint must be signed with SigV4 (service
// "lambda", the endpoint domain's Region) or, for a MultiRegion domain, SigV4a
// (Region set "*"), and carry X-Amz-Content-Sha256 even with no body
// (UNSIGNED-PAYLOAD is allowed; Lambda does not check the body against it;
// chunked-body signing is unsupported). Anything else is 403 with no
// lambda-web-error-type. The caller must then be allowed
// lambda:InvokeWebFunctionEndpoint on the endpoint ARN: a same-account caller
// by its identity policy OR the function's resource policy, a cross-account
// caller by BOTH, and an explicit Deny anywhere wins.
// https://docs.aws.amazon.com/lambda/latest/dg/web-functions-endpoint-access-control.html
//
// Overcast is not a security boundary (AGENTS.md non-goals), so the checks are
// tiered by the repo's existing switches:
//
//	always                  header shape, content-hash header, scope service and region
//	SigV4 validation on     the signature is verified against the access key's secret
//	                        (OVERCAST_SIGV4_VALIDATE; SigV4 via middleware.ValidateSigV4,
//	                        SigV4a via sigv4a.go)
//	IAM enforcement on      lambda:InvokeWebFunctionEndpoint is evaluated (OVERCAST_ENFORCE_IAM)
//
// Why the SigV4 middleware does not do this: HostAddressing rewrites the path
// to /_overcast/lambdaweb/invoke/{id}/{region}/… before the chain runs, so the
// chain would canonicalise a path the client never signed (and knows nothing of
// SigV4a). middleware.SigV4 skips LambdaWebInvokePrefix; this file verifies
// against the original path, derived by [Authorizer.originalPath].
//
// CloudFront origin access control: CloudFront reaches the endpoint as the
// service principal cloudfront.amazonaws.com with aws:SourceArn set to the
// distribution. The engine passes the distribution ARN, already verified
// against the per-process internal token (serviceutil.TakeCloudFrontSource), as
// authContext.SourceARN; a request with one is a service caller and its own
// signature (X-Amz-Cf-Authorization, signed with a fixed emulator credential)
// is not checked. An in-process caller may use [WithServiceCaller] instead.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/middleware"
)

const (
	// invokeEndpointAction is the IAM action a data-plane request needs.
	invokeEndpointAction = "lambda:InvokeWebFunctionEndpoint"

	authAlgSigV4  = "AWS4-HMAC-SHA256"
	authAlgSigV4a = "AWS4-ECDSA-P256-SHA256"

	headerContentSHA   = "X-Amz-Content-Sha256"
	headerAmzDate      = "X-Amz-Date"
	headerRegionSet    = "X-Amz-Region-Set"
	signingServiceName = "lambda"

	cloudFrontPrincipal = "cloudfront.amazonaws.com"

	policyCacheSize = 256
)

// ServiceCaller is an AWS service calling an endpoint on its own authority:
// the Principal is the service principal ("cloudfront.amazonaws.com") and
// SourceARN/SourceAccount the aws:SourceArn and aws:SourceAccount it acts for.
type ServiceCaller struct {
	Principal     string
	SourceARN     string
	SourceAccount string
}

type serviceCallerKey struct{}

// WithServiceCaller marks ctx as a request made by a service. Unlike the
// headers it cannot come from a client, so it is always honoured.
func WithServiceCaller(ctx context.Context, c ServiceCaller) context.Context {
	return context.WithValue(ctx, serviceCallerKey{}, c)
}

// AuthenticatedPrincipal is who a request was authenticated as.
type AuthenticatedPrincipal struct {
	// AccessKeyID is the credential's access key; empty for a service caller.
	AccessKeyID string
	// ARN and Account are the resolved IAM principal, empty when IAM
	// enforcement is off or the key names no principal.
	ARN     string
	Account string
	// Service is the service principal of a service caller.
	Service string
}

type principalSlotKey struct{}

// PrincipalSlot receives the authenticated principal of a request.
type PrincipalSlot struct{ principal AuthenticatedPrincipal }

// Principal returns what Authorize recorded; the zero value before it ran or
// when it rejected the request.
func (s *PrincipalSlot) Principal() AuthenticatedPrincipal { return s.principal }

// WithPrincipalSlot returns a context whose slot [Authorizer.Authorize] fills
// on success. Authorize has no way to hand back a new context, so the engine
// stamps the slot first and reads it after, for logging and telemetry.
func WithPrincipalSlot(ctx context.Context) (context.Context, *PrincipalSlot) {
	s := &PrincipalSlot{}
	return context.WithValue(ctx, principalSlotKey{}, s), s
}

// AuthorizerOptions configures an [Authorizer]. Every callback is called on
// the request path and must be cheap: the caller caches what it hands over.
type AuthorizerOptions struct {
	Clock clock.Clock
	Log   *zap.Logger
	// ValidateSignatures verifies signatures cryptographically
	// (config.SigV4Validate).
	ValidateSignatures bool
	// EnforceIAM evaluates lambda:InvokeWebFunctionEndpoint (config.EnforceIAM).
	EnforceIAM bool
	// Secrets resolves an access key's secret for signature validation
	// (middleware.NewSecretResolver(store)).
	Secrets middleware.SecretResolver
	// Principals resolves access keys to identity policies for IAM
	// enforcement (middleware.NewIAMPrincipalResolver(store)).
	Principals *middleware.IAMPrincipalResolver
	// ResourceTags returns the tags of the function with the given ARN, for
	// aws:ResourceTag/* conditions. Nil means no tags. Cached by the caller.
	ResourceTags func(ctx context.Context, functionARN string) map[string]string
	// OrgID returns the AWS Organizations ID of an account, for
	// aws:PrincipalOrgID conditions; "" when it has none. Nil means none.
	OrgID func(ctx context.Context, accountID string) string
	// OriginalPath returns the escaped request path the client signed. Nil
	// strips the /_overcast/lambdaweb/invoke/{id}/{region} prefix
	// HostAddressing added.
	OriginalPath func(r *http.Request) string
}

// Authorizer implements invokeAuth.
type Authorizer struct {
	opts AuthorizerOptions

	// policies caches compiled resource policies by revision token. It is
	// created on first use so NewAuthorizer stays a pure assignment.
	policiesOnce sync.Once
	policies     *policyCache
}

var _ invokeAuth = (*Authorizer)(nil)

// NewAuthorizer returns an Authorizer. It does no work: all state is created
// on first use.
func NewAuthorizer(opts AuthorizerOptions) *Authorizer {
	return &Authorizer{opts: opts}
}

func (a *Authorizer) now() time.Time {
	if a.opts.Clock != nil {
		return a.opts.Clock.Now()
	}
	return time.Now()
}

func (a *Authorizer) deny(reason string) *platformError {
	if a.opts.Log != nil {
		a.opts.Log.Debug("lambda-web: request refused", zap.String("reason", reason))
	}
	return errForbidden()
}

// Authorize implements invokeAuth.
func (a *Authorizer) Authorize(ctx context.Context, r *http.Request, c authContext) *platformError {
	slot, _ := ctx.Value(principalSlotKey{}).(*PrincipalSlot)

	// A fronting service (CloudFront OAC) authenticates by being that service.
	if sc, ok := a.serviceCaller(ctx, c); ok {
		if perr := a.authorizeService(ctx, c, sc); perr != nil {
			return perr
		}
		if slot != nil {
			slot.principal = AuthenticatedPrincipal{Service: sc.Principal}
		}
		return nil
	}

	sig, reason := parseSignature(r)
	if reason != "" {
		return a.deny(reason)
	}
	if reason := a.checkScope(sig, r, c); reason != "" {
		return a.deny(reason)
	}
	if a.opts.ValidateSignatures {
		if reason := a.verify(r, sig); reason != "" {
			return a.deny(reason)
		}
	}
	principal := AuthenticatedPrincipal{AccessKeyID: sig.accessKey}
	if a.opts.EnforceIAM {
		var reason string
		principal, reason = a.authorizeIAM(ctx, r, c, sig.accessKey)
		if reason != "" {
			return a.deny(reason)
		}
	}
	if slot != nil {
		slot.principal = principal
	}
	return nil
}

// ─── Signature shape ─────────────────────────────────────────────────────────

// signature is a parsed Authorization header.
type signature struct {
	v4a           bool
	accessKey     string
	date          string // scope date, yyyymmdd
	region        string // SigV4 only
	service       string
	signedHeaders string // as sent, ';'-separated
	sig           string
	amzDate       string
	regionSet     string // SigV4a only: X-Amz-Region-Set
	payloadHash   string
}

// parseSignature reads r's Authorization header and the headers AWS requires
// beside it. reason is "" when the request is well formed.
func parseSignature(r *http.Request) (s signature, reason string) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	var rest string
	switch {
	case strings.HasPrefix(auth, authAlgSigV4+" "):
		rest = auth[len(authAlgSigV4)+1:]
	case strings.HasPrefix(auth, authAlgSigV4a+" "):
		s.v4a = true
		rest = auth[len(authAlgSigV4a)+1:]
	case auth == "":
		return s, "no Authorization header"
	default:
		return s, "unsupported authorization algorithm"
	}

	var credential string
	for rest != "" {
		var field string
		field, rest, _ = strings.Cut(rest, ",")
		key, val, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			continue
		}
		switch key {
		case "Credential":
			credential = val
		case "SignedHeaders":
			s.signedHeaders = val
		case "Signature":
			s.sig = val
		}
	}
	if s.sig == "" || s.signedHeaders == "" || credential == "" {
		return s, "Authorization header lacks Credential, SignedHeaders or Signature"
	}

	// SigV4:  AKID/date/region/service/aws4_request
	// SigV4a: AKID/date/service/aws4_request (the Region set is a header)
	var terminator string
	var ok bool
	s.accessKey, credential, ok = strings.Cut(credential, "/")
	if !ok || s.accessKey == "" {
		return s, "malformed credential scope"
	}
	s.date, credential, ok = strings.Cut(credential, "/")
	if !ok {
		return s, "malformed credential scope"
	}
	if !s.v4a {
		s.region, credential, ok = strings.Cut(credential, "/")
		if !ok {
			return s, "malformed credential scope"
		}
	}
	s.service, terminator, ok = strings.Cut(credential, "/")
	if !ok || terminator != "aws4_request" || strings.Contains(terminator, "/") {
		return s, "malformed credential scope"
	}

	s.amzDate = r.Header.Get(headerAmzDate)
	if len(s.amzDate) != len("20060102T150405Z") || !strings.HasPrefix(s.amzDate, s.date) {
		return s, "missing or malformed X-Amz-Date"
	}
	s.payloadHash = r.Header.Get(headerContentSHA)
	if s.payloadHash == "" {
		return s, "missing X-Amz-Content-Sha256"
	}
	if strings.HasPrefix(s.payloadHash, "STREAMING-") {
		return s, "chunked body signing is not supported"
	}
	if s.v4a {
		s.regionSet = r.Header.Get(headerRegionSet)
		if s.regionSet == "" {
			return s, "SigV4a request lacks X-Amz-Region-Set"
		}
	}
	return s, ""
}

// checkScope compares the signed scope with the endpoint the request reached.
func (a *Authorizer) checkScope(s signature, r *http.Request, c authContext) string {
	if s.service != signingServiceName {
		return "credential scope service is " + s.service + ", want lambda"
	}
	multi := c.Endpoint != nil && c.Endpoint.Type == endpointMultiRegion
	switch {
	case s.v4a:
		if multi {
			if !regionSetCoversAll(s.regionSet, c.Endpoint.Regions) {
				return "X-Amz-Region-Set does not cover the endpoint"
			}
		} else if !regionSetHas(s.regionSet, c.Region) {
			return "X-Amz-Region-Set does not contain " + c.Region
		}
	case multi:
		return "a MultiRegion endpoint requires SigV4a"
	case s.region != c.Region:
		return "credential scope region is " + s.region + ", want " + c.Region
	}
	return ""
}

// regionSetHas reports whether a SigV4a region set (comma-separated, with "*"
// and trailing-"*" wildcards such as "us-*") names region.
func regionSetHas(set, region string) bool {
	for set != "" {
		var item string
		item, set, _ = strings.Cut(set, ",")
		item = strings.TrimSpace(item)
		switch {
		case item == region:
			return true
		case item == "*":
			return true
		case strings.HasSuffix(item, "*") && strings.HasPrefix(region, item[:len(item)-1]):
			return true
		}
	}
	return false
}

// regionSetCoversAll reports whether set names every one of regions. A
// MultiRegion endpoint is signed for "*", which covers any list, even an empty
// one.
func regionSetCoversAll(set string, regions []string) bool {
	if strings.TrimSpace(set) == "*" {
		return true
	}
	if len(regions) == 0 {
		return false
	}
	for _, region := range regions {
		if !regionSetHas(set, region) {
			return false
		}
	}
	return true
}

// ─── Cryptographic validation ────────────────────────────────────────────────

const lambdaWebPathSegments = 2 // {id}/{region} after the invoke prefix

// originalPath is the escaped path the client signed.
func (a *Authorizer) originalPath(r *http.Request) string {
	if a.opts.OriginalPath != nil {
		return a.opts.OriginalPath(r)
	}
	return StripInvokePrefix(r.URL.EscapedPath())
}

// StripInvokePrefix removes /_overcast/lambdaweb/invoke/{id}/{region} from an
// escaped path HostAddressing rewrote, leaving the path the client sent. A
// path without the prefix is returned unchanged; an empty result is "/".
func StripInvokePrefix(p string) string {
	if !strings.HasPrefix(p, middleware.LambdaWebInvokePrefix) {
		return p
	}
	rest := p[len(middleware.LambdaWebInvokePrefix):]
	for i := 0; i < lambdaWebPathSegments; i++ {
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return "/"
		}
		rest = rest[j+1:]
	}
	return "/" + rest
}

// verify checks the signature against the stored secret.
func (a *Authorizer) verify(r *http.Request, s signature) string {
	t, err := time.Parse("20060102T150405Z", s.amzDate)
	if err != nil {
		return "malformed X-Amz-Date"
	}
	if d := a.now().Sub(t); d > 5*time.Minute || d < -5*time.Minute {
		return "signature expired or clock skew too large"
	}
	if !signedHeaderPresent(s.signedHeaders, "host") {
		return "host is not a signed header"
	}
	path := a.originalPath(r)
	if path == "" {
		path = "/"
	}
	// SDK signers escape an already-escaped path again for every service but
	// S3; a client that disabled that signs the path once.
	paths := [2]string{escapeSignedPath(path), path}
	tries := 1
	if strings.IndexByte(path, '%') >= 0 {
		tries = 2
	}
	for _, p := range paths[:tries] {
		if a.verifyAt(r, s, p) {
			return ""
		}
	}
	return "signature mismatch"
}

func (a *Authorizer) verifyAt(r *http.Request, s signature, canonicalPath string) bool {
	if s.v4a {
		return verifySigV4a(r, s, middleware.SigningSecret(r, a.opts.Secrets, s.accessKey), canonicalPath)
	}
	signed, aerr := middleware.ValidateSigV4(r, a.opts.Clock, a.opts.Secrets, canonicalPath)
	return signed && aerr == nil
}

func signedHeaderPresent(list, name string) bool {
	for list != "" {
		var h string
		h, list, _ = strings.Cut(list, ";")
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// escapeSignedPath escapes an escaped path a second time, as AWS SDKs do for
// the canonical URI of every service but S3: everything but unreserved
// characters and "/" becomes %XX.
func escapeSignedPath(p string) string {
	const hex = "0123456789ABCDEF"
	clean := true
	for i := 0; i < len(p); i++ {
		if !unreservedOrSlash(p[i]) {
			clean = false
			break
		}
	}
	if clean {
		return p
	}
	var b strings.Builder
	b.Grow(len(p) + 8)
	for i := 0; i < len(p); i++ {
		c := p[i]
		if unreservedOrSlash(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

func unreservedOrSlash(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.' || c == '~' || c == '/'
}

// ─── Service callers ─────────────────────────────────────────────────────────

// serviceCaller reports whether the request is a fronting service's. The
// engine sets authContext.SourceARN only from serviceutil.TakeCloudFrontSource,
// which accepts it solely with the per-process internal token, so a client
// cannot forge it; the in-process context value is equally unforgeable.
func (a *Authorizer) serviceCaller(ctx context.Context, c authContext) (ServiceCaller, bool) {
	if c.SourceARN != "" {
		return ServiceCaller{Principal: cloudFrontPrincipal, SourceARN: c.SourceARN}, true
	}
	if sc, ok := ctx.Value(serviceCallerKey{}).(ServiceCaller); ok && sc.Principal != "" {
		return sc, true
	}
	return ServiceCaller{}, false
}

// authorizeService admits a service caller. With IAM enforcement off the
// service is trusted; with it on, only the function's resource policy can
// allow it (a service has no identity policy).
func (a *Authorizer) authorizeService(ctx context.Context, c authContext, sc ServiceCaller) *platformError {
	if !a.opts.EnforceIAM {
		return nil
	}
	in, reason := a.decisionInput(ctx, c)
	if reason != "" {
		return a.deny(reason)
	}
	in.Caller = callerIdentity{Service: sc.Principal}
	in.Context["aws:principalservicename"] = sc.Principal
	in.Context["aws:principalisawsservice"] = "true"
	if sc.SourceARN != "" {
		in.Context["aws:sourcearn"] = sc.SourceARN
	}
	if sc.SourceAccount != "" {
		in.Context["aws:sourceaccount"] = sc.SourceAccount
	} else if acct := accountOfARN(sc.SourceARN); acct != "" {
		in.Context["aws:sourceaccount"] = acct
	}
	if d := decideInvoke(in); !d.Allowed {
		return a.deny(d.Reason)
	}
	return nil
}

func accountOfARN(arn string) string {
	f := strings.SplitN(arn, ":", 6)
	if len(f) < 6 {
		return ""
	}
	return f[4]
}

// ─── IAM ─────────────────────────────────────────────────────────────────────

func (a *Authorizer) authorizeIAM(ctx context.Context, r *http.Request, c authContext, accessKey string) (AuthenticatedPrincipal, string) {
	out := AuthenticatedPrincipal{AccessKeyID: accessKey}
	in, reason := a.decisionInput(ctx, c)
	if reason != "" {
		return out, reason
	}
	p := a.opts.Principals.Resolve(ctx, accessKey)
	if p.Err != nil {
		return out, "principal policies could not be read: " + p.Err.Error()
	}
	out.ARN, out.Account = p.ARN, p.Account
	in.Caller = callerIdentity{ARN: p.ARN, Account: p.Account, Known: p.Known}
	in.Identity, in.Boundary = p.Statements, p.Boundary
	for k, v := range p.Context {
		in.Context[k] = v
	}
	if a.opts.OrgID != nil && p.Account != "" {
		if org := a.opts.OrgID(ctx, p.Account); org != "" {
			in.Context["aws:principalorgid"] = org
		}
	}
	if ip := clientIP(r); ip != "" {
		in.Context["aws:sourceip"] = ip
	}
	in.Context["aws:currenttime"] = a.now().UTC().Format(time.RFC3339)
	if d := decideInvoke(in); !d.Allowed {
		return out, d.Reason
	}
	return out, ""
}

// decisionInput fills what does not depend on the caller: the action, the
// endpoint ARN, the function's resource policy and the resource's context keys.
func (a *Authorizer) decisionInput(ctx context.Context, c authContext) (decisionInput, string) {
	in := decisionInput{
		Action:          invokeEndpointAction,
		Resource:        c.EndpointARN,
		EndpointAccount: accountOfARN(c.EndpointARN),
		Context:         make(map[string]string, 12),
	}
	if c.Policy != nil && c.Policy.Policy != "" {
		fnARN := ""
		if c.Function != nil {
			fnARN = c.Function.ARN
		}
		stmts, err := a.policyStatements(fnARN, c.Policy)
		if err != nil {
			return in, "resource policy could not be parsed: " + err.Error()
		}
		in.ResourcePolicy = stmts
	}
	if c.Region != "" {
		in.Context["aws:requestedregion"] = c.Region
	}
	if c.Endpoint != nil {
		in.Context["lambda:resource/webfunctionauthtype"] = c.Endpoint.AuthType
	}
	if a.opts.ResourceTags != nil && c.Function != nil {
		for k, v := range a.opts.ResourceTags(ctx, c.Function.ARN) {
			in.Context["aws:resourcetag/"+strings.ToLower(k)] = v
		}
	}
	return in, ""
}

func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i >= 0 && !strings.Contains(addr[i+1:], "]") {
		addr = addr[:i]
	}
	return strings.Trim(addr, "[]")
}

// policyStatements returns the compiled resource policy, cached by the
// policy's revision token.
func (a *Authorizer) policyStatements(functionARN string, rec *policyRecord) ([]iampolicy.Statement, error) {
	a.policiesOnce.Do(func() { a.policies = newPolicyCache(policyCacheSize) })
	key := functionARN + "|" + rec.RevisionID
	if rec.RevisionID != "" {
		if e, ok := a.policies.get(key); ok {
			return e.stmts, e.err
		}
	}
	stmts, err := iampolicy.ParseDocument(rec.Policy, iampolicy.SourceRef{ID: functionARN, Type: iampolicy.SourceTypeResourcePolicy})
	if rec.RevisionID != "" {
		a.policies.put(key, policyEntry{stmts: stmts, err: err})
	}
	return stmts, err
}
