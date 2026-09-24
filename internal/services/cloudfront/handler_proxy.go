package cloudfront

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// proxyClient is the HTTP client used for origin requests.
// It has a generous timeout; per-origin timeouts are applied via context.
var proxyClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // don't follow redirects — pass through
	},
}

// ProxyRequest handles GET/HEAD/POST/PUT/DELETE/PATCH requests to
// /_overcast/cloudfront/distributions/{distId}/* and forwards them to the matched origin.
func (h *Handler) ProxyRequest(w http.ResponseWriter, r *http.Request) {
	distID := chi.URLParam(r, "distId")
	ctx := r.Context()
	log := h.log.WithRecorder(ctx)

	// Wrap the response writer to capture status+bytes for access logging.
	rec := newResponseRecorder(w)
	w = rec

	var logCfg *DistributionConfig
	start := h.clk.Now()
	defer func() {
		if logCfg == nil {
			return
		}
		cacheStatus := "Miss"
		if xc := rec.Header().Get("X-Cache"); xc != "" {
			switch {
			case strings.Contains(xc, "Hit"):
				cacheStatus = "Hit"
			case strings.Contains(xc, "Error"):
				cacheStatus = "Error"
			}
		}
		h.writeAccessLog(logCfg, distID, r, rec.statusCode, rec.bytesWritten, cacheStatus, h.clk.Now().Sub(start))
	}()

	dist, aerr := h.requireDistribution(r, distID)
	if aerr != nil {
		http.Error(w, fmt.Sprintf("Distribution %q not found", distID), http.StatusBadGateway)
		return
	}

	if dist.Status != "Deployed" || !dist.DistributionConfig.Enabled {
		http.Error(w, "Distribution is not active", http.StatusServiceUnavailable)
		return
	}

	cfg := &dist.DistributionConfig
	logCfg = cfg

	// Continuous deployment: if this distribution has a CDP, potentially
	// route to the staging distribution based on the traffic weight.
	if cfg.ContinuousDeploymentPolicyId != "" {
		if stagingDistID := h.resolveStagingTarget(r.Context(), cfg, r); stagingDistID != "" {
			stagingDist, err := h.store.GetDistribution(r.Context(), stagingDistID)
			if err == nil && stagingDist != nil && stagingDist.DistributionConfig.Enabled {
				// Reuse the rest of ProxyRequest logic with staging dist config.
				// The simplest approach: rewrite distID and cfg, then continue.
				distID = stagingDistID
				cfg = &stagingDist.DistributionConfig
				logCfg = cfg
			}
		}
	}

	// The downstream path, percent-encoded as the viewer sent it. Everything
	// below works on this form: it is what the origin receives, what a
	// function's event.request.uri holds, and what the cache is keyed on.
	reqPath := viewerPath(r)

	// Apply DefaultRootObject when path is exactly "/".
	if reqPath == "/" && cfg.DefaultRootObject != "" {
		root := cfg.DefaultRootObject
		if root[0] != '/' {
			root = "/" + root
		}
		reqPath = escapeURIPath(root)
	}

	// Match the request path against CacheBehaviors, fall back to DefaultCacheBehavior.
	// This is the only match: a function that later changes the uri "doesn't
	// change the cache behavior for the request" (functions-event-structure),
	// so everything the behavior decides, its cache policy included, is fixed here.
	targetOriginID := cfg.DefaultCacheBehavior.TargetOriginId
	viewerProtoPolicy := cfg.DefaultCacheBehavior.ViewerProtocolPolicy
	behaviorFAs := cfg.DefaultCacheBehavior.FunctionAssociations
	cachePolicyID := cfg.DefaultCacheBehavior.CachePolicyId
	if cfg.CacheBehaviors != nil {
		matchPath := normalizePathForMatch(reqPath)
		for _, cb := range cfg.CacheBehaviors.Items {
			if matchPathPattern(cb.PathPattern, matchPath) {
				targetOriginID = cb.TargetOriginId
				viewerProtoPolicy = cb.ViewerProtocolPolicy
				behaviorFAs = cb.FunctionAssociations
				cachePolicyID = cb.CachePolicyId
				break
			}
		}
	}

	// ViewerProtocolPolicy enforcement.
	//
	// Enforced only when this server can actually answer over HTTPS. Overcast
	// listens for TLS only when OVERCAST_TLS_CERT and OVERCAST_TLS_KEY are both
	// set; without them, "redirect-to-https" would send a browser to a TLS
	// handshake against a plain-HTTP listener, and "https-only" would make the
	// distribution permanently unreachable — neither is a condition any client
	// can satisfy. Real AWS always serves HTTPS, so both are satisfiable there;
	// pointing a caller at a scheme the emulator does not serve is the worse
	// divergence, the same call docs/plans/host-routing-precedence.md §8 records
	// for AppSync's uris map.
	if isHTTPSViewerPolicy(viewerProtoPolicy) && !h.requestIsHTTPS(r) {
		if h.cfg != nil && h.cfg.TLSEnabled() {
			if viewerProtoPolicy == "https-only" {
				http.Error(w, "HTTPS is required", http.StatusForbidden)
				return
			}
			// Fold the Host into the Location we mint: a redirect target is
			// output, and output carries the canonical lowercase form.
			http.Redirect(w, r, "https://"+serviceutil.FoldHostname(r.Host)+r.RequestURI, http.StatusMovedPermanently)
			return
		}
		h.warnViewerPolicyNotEnforceable(ctx, distID, viewerProtoPolicy)
	}

	// Geo-restriction enforcement.
	if cfg.Restrictions != nil {
		rt := cfg.Restrictions.GeoRestriction.RestrictionType
		if rt == "whitelist" || rt == "blacklist" {
			clientCountry := r.Header.Get("CloudFront-Viewer-Country")
			if clientCountry == "" {
				clientCountry = r.Header.Get("CF-IPCountry") // fallback
			}
			if clientCountry != "" {
				inList := false
				for _, loc := range cfg.Restrictions.GeoRestriction.Items {
					if strings.EqualFold(loc, clientCountry) {
						inList = true
						break
					}
				}
				if rt == "whitelist" && !inList {
					w.Header().Set("X-Cache", "Error from cloudfront")
					http.Error(w, "Access denied by geo-restriction", http.StatusForbidden)
					return
				}
				if rt == "blacklist" && inList {
					w.Header().Set("X-Cache", "Error from cloudfront")
					http.Error(w, "Access denied by geo-restriction", http.StatusForbidden)
					return
				}
			}
			// If no country header is present, allow through (local dev — no real IP geolocation).
		}
	}

	// Run viewer-request CloudFront Functions.
	domainName := distID + ".cloudfront.net"
	fnResult, fnErr := h.runViewerRequest(r, distID, domainName, reqPath, behaviorFAs)
	if fnErr != nil {
		writeFunctionError(w, distID, fnErr)
		return
	}
	if fnResult != nil {
		if fnResult.isResponse {
			w.Header().Set("X-Amz-Cf-Pop", "DEV-P1")
			w.Header().Set("X-Amz-Cf-Id", distID)
			w.Header().Set("X-Cache", "FunctionGeneratedResponse")
			for k, v := range fnResult.headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(fnResult.statusCode)
			return
		}
		// Apply URI changes from the function.
		if fnResult.uri != "" {
			reqPath = fnResult.uri
		}
	}

	// Cache check for GET/HEAD requests.
	cacheable := r.Method == http.MethodGet || r.Method == http.MethodHead
	var cacheKey string
	if cacheable {
		cacheKey = proxyCacheKey(distID, reqPath, r)
		if entry := h.cache.get(cacheKey); entry != nil {
			// Viewer-response functions run on a hit too: the function
			// "executes regardless of whether the file is already in the
			// CloudFront cache" (lambda-cloudfront-trigger-events). The entry
			// holds the origin's headers and is shared, so the function works
			// on a copy — made only when there is a function to run.
			hdrs := entry.headers
			if behaviorFAs != nil && len(behaviorFAs.Items) > 0 && entry.statusCode < 400 {
				hdrs = copyHeaders(entry.headers)
				if err := h.runViewerResponse(r, distID, domainName, reqPath, behaviorFAs, entry.statusCode, hdrs); err != nil {
					writeFunctionError(w, distID, err)
					return
				}
			}
			for k, vals := range hdrs {
				for _, v := range vals {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Amz-Cf-Pop", "DEV-P1")
			w.Header().Set("X-Amz-Cf-Id", distID)
			w.Header().Set("Via", fmt.Sprintf("1.1 %s.cloudfront.net (CloudFront)", distID))
			w.Header().Set("X-Cache", "Hit from cloudfront")
			w.WriteHeader(entry.statusCode)
			_, _ = w.Write(entry.body)
			return
		}
	}

	// Resolve the target. A behavior's TargetOriginId names either an origin or
	// an origin GROUP; a group resolves to its members in failover order.
	candidates, failoverCodes := resolveOriginTargets(cfg, targetOriginID)
	if len(candidates) == 0 {
		log.Error("origin not found in distribution config",
			zap.String("distId", distID),
			zap.String("targetOriginId", targetOriginID),
		)
		http.Error(w, "Origin not found", http.StatusBadGateway)
		return
	}

	resp, originURL, err := h.dispatchToOrigins(ctx, r, reqPath, candidates, failoverCodes)
	if err != nil {
		log.Error("origin request failed",
			zap.String("originURL", originURL),
			zap.Error(err),
		)
		http.Error(w, "Origin request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Custom error responses — check before copying headers.
	if cfg.CustomErrorResponses != nil && resp.StatusCode >= 400 {
		for _, cer := range cfg.CustomErrorResponses.Items {
			if cer.ErrorCode == resp.StatusCode && cer.ResponseCode != "" {
				respCode, convErr := strconv.Atoi(cer.ResponseCode)
				if convErr != nil {
					respCode = cer.ErrorCode
				}
				w.Header().Set("X-Amz-Cf-Pop", "DEV-P1")
				w.Header().Set("X-Amz-Cf-Id", distID)
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("X-Cache", "Error from cloudfront")
				w.WriteHeader(respCode)
				fmt.Fprintf(w, "Error %d: %s\n", cer.ErrorCode, cer.ResponsePagePath)
				return
			}
		}
	}

	// Extract cache tags from the origin response before viewer-response
	// functions may modify headers, matching real CloudFront behavior.
	var cachedTags []string
	if cfg.CacheTagConfig != nil && cfg.CacheTagConfig.HeaderName != "" {
		for k, vals := range resp.Header {
			if strings.EqualFold(k, cfg.CacheTagConfig.HeaderName) {
				for _, v := range vals {
					cachedTags = append(cachedTags, parseCacheTags(v)...)
				}
				break
			}
		}
	}

	// Run viewer-response CloudFront Functions (may modify headers). They do
	// not run on an origin error: "If the origin returns an HTTP error of 400
	// and above, the CloudFront Function will not run" (functions-event-structure).
	respHeaders := copyHeaders(resp.Header)
	if resp.StatusCode < 400 {
		if err := h.runViewerResponse(r, distID, domainName, reqPath, behaviorFAs, resp.StatusCode, respHeaders); err != nil {
			writeFunctionError(w, distID, err)
			return
		}
	}

	// Copy (possibly modified) origin response headers.
	for k, vals := range respHeaders {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}

	// Add CloudFront-specific headers.
	w.Header().Set("X-Amz-Cf-Pop", "DEV-P1")
	w.Header().Set("X-Amz-Cf-Id", distID)
	w.Header().Set("Via", fmt.Sprintf("1.1 %s.cloudfront.net (CloudFront)", distID))
	w.Header().Set("X-Cache", "Miss from cloudfront")

	// For cacheable GET requests with 2xx responses, buffer the body so we can
	// store it in the cache; for all other cases stream directly.
	const maxProxyResponseBytes = 100 * 1024 * 1024 // 100 MB
	if r.Method == http.MethodGet && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxProxyResponseBytes))
		if readErr != nil {
			log.Error("failed to read origin response body", zap.Error(readErr))
			http.Error(w, "Failed to read origin response", http.StatusBadGateway)
			return
		}
		ttl := h.cacheTTL(ctx, cachePolicyID)

		// Cache the origin's headers, not the function's changes to them: the
		// viewer-response functions run again, per viewer, on every hit.
		h.cache.set(cacheKey, &cfCacheEntry{
			statusCode: resp.StatusCode,
			headers:    copyHeaders(resp.Header),
			body:       bodyBytes,
			tags:       cachedTags,
			expiresAt:  h.clk.Now().Add(ttl),
		})
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(bodyBytes)
	} else {
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// writeFunctionError answers a viewer whose request a CloudFront Function
// failed: 503 for an execution error, 502 for a validation error.
func writeFunctionError(w http.ResponseWriter, distID string, err error) {
	w.Header().Set("X-Amz-Cf-Pop", "DEV-P1")
	w.Header().Set("X-Amz-Cf-Id", distID)
	w.Header().Set("X-Cache", "Error from cloudfront")
	status := functionErrorStatus(err)
	http.Error(w, fmt.Sprintf("The CloudFront function failed (%d %s)", status, http.StatusText(status)), status)
}

// proxyCacheKey builds a cache key for a proxy request.
// The key includes distID and the request path+query so that different query
// strings get different cache entries.
func proxyCacheKey(distID, reqPath string, r *http.Request) string {
	qs := r.URL.RawQuery
	if qs != "" {
		return distID + ":" + reqPath + "?" + qs
	}
	return distID + ":" + reqPath
}

// cacheTTL returns the TTL to use for a cached response: the DefaultTTL of
// cachePolicyID, the policy of the behavior ProxyRequest matched, falling back
// to 86400 (24h). It takes the policy rather than a path so it cannot re-match
// behaviors on a uri a function has rewritten.
func (h *Handler) cacheTTL(ctx context.Context, cachePolicyID string) time.Duration {
	defaultTTL := int64(86400)
	if cachePolicyID != "" {
		if pol, err := h.store.GetCachePolicy(ctx, cachePolicyID); err == nil && pol != nil {
			if pol.CachePolicyConfig.DefaultTTL != nil {
				defaultTTL = *pol.CachePolicyConfig.DefaultTTL
			}
		}
	}
	if defaultTTL <= 0 {
		defaultTTL = 86400
	}
	return time.Duration(defaultTTL) * time.Second
}

// copyHeaders makes a deep copy of an http.Header map.
func copyHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// buildOriginRequest works out where to send a viewer request for this origin:
// the URL to dial, and the Host header to present when that differs.
//
// An origin naming an endpoint THIS emulator serves — an S3 bucket in any of
// its spellings, an API Gateway invoke host, a Lambda function URL, an AppSync
// endpoint — is answered locally, by dialling the emulator and preserving the
// origin's own Host. The router then classifies it with exactly the rules it
// applies to an inbound request from a client, so origin resolution and request
// routing cannot disagree about what a hostname means, and a service becomes
// usable as an origin the moment it becomes routable.
//
// This replaces a bespoke ".s3." prefix check that recognised only one S3
// spelling. A website endpoint, a legacy dash-region bucket, or any non-S3 AWS
// service fell through to "custom origin" and was dialled at its literal
// domain — so a distribution fronting an emulated service quietly reached out
// to real AWS instead.
//
// reqPath is percent-encoded and is appended as-is, so the origin — local or
// not — receives the URI byte-for-byte as the viewer (or a function) sent it.
func (h *Handler) buildOriginRequest(origin *Origin, reqPath, viewerHost string) (originURL, hostHeader string) {
	domain := origin.DomainName
	originPath := escapeURIPath(strings.TrimRight(origin.OriginPath, "/"))

	if h.servesOriginLocally(domain, viewerHost) {
		return fmt.Sprintf("http://127.0.0.1:%d%s%s", h.emulatorPort(), originPath, reqPath), domain
	}

	// A genuine third-party origin — dial it as configured.
	scheme := "http"
	port := 80
	if origin.CustomOriginConfig != nil {
		switch origin.CustomOriginConfig.OriginProtocolPolicy {
		case "https-only", "match-viewer":
			scheme = "https"
			port = origin.CustomOriginConfig.HTTPSPort
		default: // http-only
			port = origin.CustomOriginConfig.HTTPPort
		}
	}
	host := domain
	if port != 80 && port != 443 {
		host = fmt.Sprintf("%s:%d", domain, port)
	}
	return fmt.Sprintf("%s://%s%s%s", scheme, host, originPath, reqPath), ""
}

// servesOriginLocally reports whether this emulator answers for an origin's
// domain, using the same classifier that decides who owns an inbound Host.
//
// A claim of either kind means Overcast serves that hostname: S3 for a bucket
// subdomain, a host route for a service endpoint. Anything unclaimed is a real
// third-party origin. Note this asks "can this be routed here", not "is that
// service enabled" — a disabled service should answer with its own error rather
// than have CloudFront silently dial the internet on its behalf.
func (h *Handler) servesOriginLocally(domain, viewerHost string) bool {
	// The endpoint the viewer actually reached this server on counts as ours
	// too, ahead of the configured hostname — the same precedence every
	// client-facing URL is minted with (serviceutil.ClientBaseURL: the request
	// first, OVERCAST_HOSTNAME otherwise). A stack deployed against a server
	// reachable at some other name gets bucket and service domains on THAT
	// name, and those must resolve back here rather than be dialled outward.
	// Strictly a SUBDOMAIN of the viewer's endpoint, and never an IP literal.
	// An origin whose domain is exactly the endpoint host, or is an address on
	// the same IP, is a real origin reachable on its own port — a sibling
	// service on 127.0.0.1:9000 is not this emulator, and dialling it here
	// would swallow it. Only "{bucket}.s3.{endpoint}" and friends are ours.
	if base := serviceutil.FoldHostname(hostWithoutPort(viewerHost)); base != "" && !isIPLiteralHost(base) {
		if d := serviceutil.FoldHostname(domain); strings.HasSuffix(d, "."+base) {
			return true
		}
	}
	if h.originHosts == nil {
		return false
	}
	switch h.originHosts.Classify(domain).Kind {
	case middleware.HostClaimS3, middleware.HostClaimHostRoute:
		return true
	case middleware.HostClaimNone:
		return false
	}
	return false
}

// isIPLiteralHost reports whether a host is an IP address rather than a DNS
// name. Subdomain reasoning is meaningless for one.
func isIPLiteralHost(host string) bool {
	return net.ParseIP(strings.Trim(host, "[]")) != nil
}

// hostWithoutPort strips a trailing :port from a Host header value.
func hostWithoutPort(host string) string {
	if i := strings.LastIndexByte(host, ':'); i > 0 && !strings.Contains(host[i+1:], "]") {
		return host[:i]
	}
	return host
}

// emulatorPort is the port this server listens on, defaulting to the standard
// one for unit-constructed handlers with no config attached.
func (h *Handler) emulatorPort() int {
	if h.cfg != nil && h.cfg.Port > 0 {
		return h.cfg.Port
	}
	return 4566
}

// forwardHeaders copies relevant request headers to the outbound origin request.
func forwardHeaders(src, dst *http.Request) {
	for _, h := range []string{
		"Content-Type", "Accept", "Accept-Encoding", "Accept-Language",
		"Authorization", "Range", "If-Modified-Since", "If-None-Match",
		"Cache-Control", "Pragma", "Cookie",
	} {
		if v := src.Header.Get(h); v != "" {
			dst.Header.Set(h, v)
		}
	}
	// Forward X-Forwarded-For.
	xff := src.Header.Get("X-Forwarded-For")
	if xff != "" {
		dst.Header.Set("X-Forwarded-For", xff+", "+src.RemoteAddr)
	} else {
		dst.Header.Set("X-Forwarded-For", src.RemoteAddr)
	}
}

// matchPathPattern matches a CloudFront cache behavior path pattern against a
// request path, following the documented semantics of
// CacheBehavior.PathPattern:
//
//   - "*" matches zero or more characters and DOES match across "/" — the
//     pattern applies to the whole path, not segment by segment.
//   - "?" matches exactly one character.
//   - The leading "/" is optional; AWS states the behavior is the same with or
//     without it, so "api/*" and "/api/*" are one pattern.
//
// path.Match cannot express this: its "*" stops at "/", so the documentation's
// own "*.jpg" example would match nothing below the root. And because a request
// path always begins with "/", comparing a slash-less pattern against it
// directly meant such a behavior could never match — its requests fell through
// to the default behavior and were served by the wrong origin, silently.
//
// reqPath is the encoded path after normalizePathForMatch, the form AWS
// matches on ("CloudFront normalizes URI paths consistent with RFC 3986 and
// then matches the path with the correct cache behavior").
func matchPathPattern(pattern, reqPath string) bool {
	if pattern == "*" || pattern == "/*" {
		return true
	}
	return globMatch(withLeadingSlash(pattern), withLeadingSlash(reqPath))
}

// withLeadingSlash normalises the optional leading "/" of a path pattern so a
// pattern and a request path are always compared in the same form.
func withLeadingSlash(s string) string {
	if strings.HasPrefix(s, "/") {
		return s
	}
	return "/" + s
}

// viewerPath returns the request path below
// /_overcast/cloudfront/distributions/{distId}, percent-encoded exactly as the
// viewer sent it.
//
// chi routes on r.URL.RawPath when Go set it — only when the client's encoding
// differs from Go's default, such as a %2F inside a segment — and on the
// decoded r.URL.Path otherwise. So the wildcard alone is encoded for
// "/a%2Fb" but decoded for "/100%25", and appending a decoded "/100%" to an
// origin URL made it unparseable (a 502). With RawPath unset the viewer's
// encoding IS the default one, so re-escaping reproduces it exactly.
//
// The wildcard starts after the "/" that ends the route prefix, so that "/" is
// always the viewer's first one and is put back unconditionally: a path the
// viewer began with "//" keeps both.
func viewerPath(r *http.Request) string {
	p := "/" + chi.URLParam(r, "*")
	if r.URL.RawPath == "" {
		p = (&url.URL{Path: p}).EscapedPath()
	}
	return p
}

// normalizePathForMatch applies the RFC 3986 normalisation CloudFront performs
// before matching cache behaviors (DownloadDistValuesCacheBehavior, "Path
// normalization"), in RFC 3986 section 6's order:
//
//  1. Percent-encoding (section 6.2.2.2): an escaped UNRESERVED character
//     (ALPHA, DIGIT, "-", ".", "_", "~") is decoded, so "/%7Euser" matches a
//     "/~user/*" pattern. Every other escape stays as it is — "%2F" is not a
//     separator and "%40" is not "@" — which is harmless to the match because
//     a path pattern cannot itself contain "%".
//  2. Path segments (section 6.2.2.3): "." and ".." segments are resolved, and
//     repeated slashes are collapsed, which AWS names alongside ".." as
//     "normalized and removed". So "/a/b/.." matches "/a*", not "/a/b*".
//
// The result is for matching only; the origin still gets the raw path.
// Returns p unchanged, without allocating, when there is nothing to normalise.
func normalizePathForMatch(p string) string {
	p = decodeUnreservedEscapes(p)
	if !hasDotSegmentOrEmptySegment(p) {
		return p
	}
	return removeDotSegments(p)
}

// hasDotSegmentOrEmptySegment reports whether p, a path starting with "/",
// holds a "." or ".." segment or an empty one ("//"), without allocating.
func hasDotSegmentOrEmptySegment(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] != '/' {
			continue
		}
		seg := p[i+1:]
		if j := strings.IndexByte(seg, '/'); j >= 0 {
			seg = seg[:j]
		} else if seg == "" {
			return false // a single trailing "/" is not an empty segment
		}
		if seg == "" || seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// removeDotSegments resolves "." and ".." segments as RFC 3986 section 5.2.4
// does, dropping empty segments too so repeated slashes collapse. A ".." above
// the root stays at the root. A path ending in a dot segment or a slash keeps
// its trailing slash: "/a/b/.." is "/a/".
func removeDotSegments(p string) string {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch s {
		case "", ".":
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "/"
	}
	norm := "/" + strings.Join(out, "/")
	if last := segs[len(segs)-1]; last == "" || last == "." || last == ".." {
		norm += "/"
	}
	return norm
}

// decodeUnreservedEscapes decodes the percent-escapes of unreserved characters
// in p (RFC 3986 section 6.2.2.2) and leaves every other escape as it is.
// Returns p unchanged, without allocating, when it holds no escape.
func decodeUnreservedEscapes(p string) string {
	i := strings.IndexByte(p, '%')
	if i < 0 {
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	b.WriteString(p[:i])
	for ; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) && isHex(p[i+1]) && isHex(p[i+2]) {
			if c := unhex(p[i+1])<<4 | unhex(p[i+2]); isUnreserved(c) {
				b.WriteByte(c)
				i += 2
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

// escapeURIPath percent-encodes every byte of p that may not appear literally
// in a URI path, leaving valid escapes and legal characters untouched.
//
// A viewer's path is already in this form. A function's returned uri, or a
// DefaultRootObject, need not be: AWS recommends percent-encoding a function's
// uri but forwards a raw UTF-8 one too, and "/café" can only go on an HTTP/1.1
// request line as "/caf%C3%A9". A stray "%" is encoded rather than rejected,
// and "?" and "#" are encoded so they stay part of the path instead of starting
// a query or fragment.
//
// Returns p unchanged, without allocating, when nothing needs encoding.
func escapeURIPath(p string) string {
	i := 0
	for i < len(p) && !pathByteNeedsEscape(p, i) {
		i++
	}
	if i == len(p) {
		return p
	}
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(p) + 8)
	b.WriteString(p[:i])
	for ; i < len(p); i++ {
		if c := p[i]; pathByteNeedsEscape(p, i) {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xF])
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// pathByteNeedsEscape reports whether p[i] must be percent-encoded to sit in a
// URI path: anything outside RFC 3986's pchar set and "/", counting a "%" as
// legal only when it starts a well-formed escape.
func pathByteNeedsEscape(p string, i int) bool {
	c := p[i]
	switch {
	case isUnreserved(c):
		return false
	case c == '%':
		return i+2 >= len(p) || !isHex(p[i+1]) || !isHex(p[i+2])
	}
	return !strings.ContainsRune("/:@!$&'()*+,;=", rune(c))
}

func isUnreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}

// globMatch reports whether s matches a pattern of literals, "*" (any run of
// characters, "/" included) and "?" (exactly one character).
//
// Iterative rather than recursive, with a single backtrack point for the most
// recent "*": linear in practice, and allocation-free, which matters because
// this runs for every cache behavior on every proxied request. Deliberately not
// a regexp — compiling one per behavior per request would cost far more than
// the match, and the pattern is a glob, so regexp metacharacters in it ("." in
// "*.jpg", "+" in a path segment) must stay literal.
func globMatch(pattern, s string) bool {
	var (
		p, i  int
		starP = -1
		starI int
	)
	for i < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case p < len(pattern) && pattern[p] == '*':
			// Remember where to resume if the rest fails to match, then try
			// consuming nothing with this star.
			starP, starI = p, i
			p++
		case starP >= 0:
			// Backtrack: let the last star swallow one more character.
			starI++
			i = starI
			p = starP + 1
		default:
			return false
		}
	}
	// Trailing stars may match the empty remainder.
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// resolveStagingTarget checks the continuous deployment policy for the distribution
// and returns a staging distribution ID to route to, or "" to use the primary.
func (h *Handler) resolveStagingTarget(ctx context.Context, cfg *DistributionConfig, r *http.Request) string {
	pol, err := h.store.GetContinuousDeploymentPolicy(ctx, cfg.ContinuousDeploymentPolicyId)
	if err != nil || pol == nil || !pol.ContinuousDeploymentPolicyConfig.Enabled {
		return ""
	}

	tc := pol.ContinuousDeploymentPolicyConfig.TrafficConfig
	if tc == nil {
		return ""
	}

	// Find the staging distribution ID from the policy's StagingDistributionDnsNames.
	// The ID is the first label of the DomainName — which holds for AWS's
	// "{id}.cloudfront.net" and for the "{id}.cloudfront.{host}" Overcast mints.
	stagingDNS := pol.ContinuousDeploymentPolicyConfig.StagingDistributionDnsNames
	if len(stagingDNS.Items) == 0 {
		return ""
	}
	dnsName := stagingDNS.Items[0]
	stagingDistID := distributionIDFromDomainName(dnsName)
	if stagingDistID == dnsName {
		return "" // doesn't match expected format
	}

	switch tc.Type {
	case "SingleWeight":
		if tc.SingleWeightConfig == nil {
			return ""
		}
		weight := tc.SingleWeightConfig.Weight // float64 between 0 and 1
		if rand.Float64() < weight {
			return stagingDistID
		}
	case "SingleHeader":
		if tc.SingleHeaderConfig == nil {
			return ""
		}
		if r.Header.Get(tc.SingleHeaderConfig.Header) == tc.SingleHeaderConfig.Value {
			return stagingDistID
		}
	}
	return ""
}

// HostRouteRewrite adapts a request on a distribution's real CloudFront
// hostname ({distributionId}.cloudfront.net) to the internal proxy route this
// file serves.
//
// Unlike the other host-routed services the region is absent from the grammar:
// CloudFront is global, so the address is {id}.cloudfront.net with no region
// segment. middleware.ParseHostRoute already tolerates that — Region is
// optional — so "cloudfront" needs no special parsing, only this rewrite.
//
// The ID is upper-cased because a Host is case-insensitive (RFC 3986 §3.2.2)
// and browsers lowercase it before sending, while a distribution ID is
// uppercase ("E" + 13 uppercase alphanumerics, generateDistributionID) and the
// store looks it up verbatim. Without this, pasting a minted DomainName into a
// browser 502s with `Distribution "e…" not found` while curl -H Host works.
// CloudFront is the only host-routed service whose IDs are not already
// lowercase, which is why only this rewrite needs to canonicalise.
func (s *Service) HostRouteRewrite(r *http.Request, m middleware.HostRouteMatch) {
	middleware.PrefixPath(r, "/_overcast/cloudfront/distributions/"+strings.ToUpper(m.ID))
}

// isHTTPSViewerPolicy reports whether a ViewerProtocolPolicy requires the
// viewer connection to be HTTPS. "allow-all" (and any unrecognised value, which
// CreateDistribution validation should already have rejected) does not.
func isHTTPSViewerPolicy(policy string) bool {
	return policy == "https-only" || policy == "redirect-to-https"
}

// requestIsHTTPS reports whether the viewer hop was already HTTPS — either
// terminated by this server, or by a proxy in front of it that said so.
func (h *Handler) requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// warnViewerPolicyNotEnforceable explains, once per process, why a
// distribution's HTTPS viewer policy is being served as allow-all. Silence here
// would be its own trap: the policy is in the distribution config the user
// wrote, so its absence needs a reason they can find.
func (h *Handler) warnViewerPolicyNotEnforceable(ctx context.Context, distID, policy string) {
	h.tlsPolicyWarnOnce.Do(func() {
		log := h.log.WithRecorder(ctx)
		log.Warn("serving an HTTPS-only viewer protocol policy over plain HTTP",
			zap.String("distributionId", distID),
			zap.String("viewerProtocolPolicy", policy),
			zap.String("reason", "this server is not serving TLS, so the policy cannot be satisfied by any client"),
			zap.String("resolution", "set OVERCAST_TLS_CERT and OVERCAST_TLS_KEY to enforce it, or use a TLS-terminating proxy that sets X-Forwarded-Proto: https"),
		)
	})
}

// resolveOriginTargets maps a cache behavior's TargetOriginId to the origins
// that may serve it, in the order they should be tried.
//
// TargetOriginId names either an Origin or an OriginGroup — validateOriginRefs
// accepts both, per AWS's origin failover documentation. An origin resolves to
// itself with no failover; a group resolves to its members in declared order,
// with the status codes its FailoverCriteria lists.
//
// A group member naming an origin that does not exist is skipped rather than
// failing the whole lookup: the remaining members are still serviceable, and a
// group with no resolvable member returns nothing, which the caller reports as
// the same "Origin not found" a bad TargetOriginId gets.
func resolveOriginTargets(cfg *DistributionConfig, targetID string) ([]*Origin, map[int]bool) {
	byID := make(map[string]*Origin, len(cfg.Origins.Items))
	for i := range cfg.Origins.Items {
		byID[cfg.Origins.Items[i].ID] = &cfg.Origins.Items[i]
	}

	if origin, ok := byID[targetID]; ok {
		return []*Origin{origin}, nil
	}

	if cfg.OriginGroups == nil {
		return nil, nil
	}
	for _, group := range cfg.OriginGroups.Items {
		if group.ID != targetID {
			continue
		}
		members := make([]*Origin, 0, len(group.Members.Items))
		for _, m := range group.Members.Items {
			if origin, ok := byID[m.OriginId]; ok {
				members = append(members, origin)
			}
		}
		codes := make(map[int]bool, len(group.FailoverCriteria.StatusCodes.Items))
		for _, c := range group.FailoverCriteria.StatusCodes.Items {
			codes[c] = true
		}
		return members, codes
	}
	return nil, nil
}

// requestCanFailOver reports whether a viewer request may be retried against
// the next member of an origin group.
//
// AWS fails over only for GET, HEAD and OPTIONS — for any other method
// CloudFront returns the primary's response as-is, because replaying a request
// that changes state is not safe. The body check enforces the same thing
// mechanically: a request whose body has already been streamed to the first
// origin cannot be replayed to the second, and ContentLength 0 is the only case
// where there is provably nothing to replay (-1 means chunked, i.e. unknown).
func requestCanFailOver(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return r.ContentLength == 0
	default:
		return false
	}
}

// dispatchToOrigins sends the viewer request to the first candidate origin,
// falling back through the rest when the response is a failover trigger.
//
// Returns the response that should be served, the URL it came from (for error
// logging), and an error only when no candidate produced a response at all.
func (h *Handler) dispatchToOrigins(
	ctx context.Context, r *http.Request, reqPath string,
	candidates []*Origin, failoverCodes map[int]bool,
) (*http.Response, string, error) {
	log := h.log.WithRecorder(ctx)
	canFailOver := len(candidates) > 1 && requestCanFailOver(r)

	var lastErr error
	var lastURL string
	for i, origin := range candidates {
		originURL, originHost := h.buildOriginRequest(origin, reqPath, r.Host)
		lastURL = originURL
		isLast := i == len(candidates)-1

		outReq, err := http.NewRequestWithContext(ctx, r.Method, originURL, r.Body)
		if err != nil {
			lastErr = fmt.Errorf("build origin request: %w", err)
			if isLast || !canFailOver {
				return nil, originURL, lastErr
			}
			continue
		}
		if originHost != "" {
			// Present the origin's own Host so this emulator's router resolves
			// it the same way it would for an inbound client request.
			outReq.Host = originHost
		}
		forwardHeaders(r, outReq)
		if origin.CustomHeaders != nil {
			for _, ch := range origin.CustomHeaders.Items {
				outReq.Header.Set(ch.HeaderName, ch.HeaderValue)
			}
		}

		resp, err := proxyClient.Do(outReq)
		if err != nil {
			// A connection failure is a failover trigger in its own right, not
			// only a listed status code.
			lastErr = err
			if isLast || !canFailOver {
				return nil, originURL, lastErr
			}
			log.Warn("origin unreachable, failing over to the next group member",
				zap.String("originId", origin.ID),
				zap.String("originURL", originURL),
				zap.Error(err),
			)
			continue
		}

		if !isLast && canFailOver && failoverCodes[resp.StatusCode] {
			resp.Body.Close()
			log.Warn("origin returned a failover status, trying the next group member",
				zap.String("originId", origin.ID),
				zap.Int("status", resp.StatusCode),
			)
			continue
		}
		return resp, originURL, nil
	}
	return nil, lastURL, lastErr
}
