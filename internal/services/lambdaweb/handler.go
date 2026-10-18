package lambdaweb

// handler.go — the data-plane request path. A request for an endpoint's domain
// is rewritten by host routing (HostRouteRewrite) to
// /_overcast/lambdaweb/invoke/{id}/{region}/…, keeping its Host, and arrives
// here. See docs/plans/lambda-web-data-plane.md for the sequence.

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// invokeBase is where host-routed requests land. The path is under
// /_overcast/, so it is not an AWS binding and IAM enforcement skips it; the
// handler authorises IamAuth endpoints itself.
const invokeBase = "/_overcast/lambdaweb/invoke"

const (
	// maxHeadBytes is the request line + headers + blank line limit (128 KiB).
	maxHeadBytes = 128 * 1024
	// maxResponseHeaderBytes bounds the application's response headers
	// (Runtime.InvalidResponse beyond it).
	maxResponseHeaderBytes = 128 * 1024
)

// HostRouteRewrite adapts a Host-routed request to the invoke route. It is the
// middleware.HostRouteRow rewrite for the "lambda-web" label.
func (s *Service) HostRouteRewrite(r *http.Request, m middleware.HostRouteMatch) {
	region := m.Region
	if region == "" {
		region = "-"
	}
	middleware.PrefixPath(r, invokeBase+"/"+m.ID+"/"+region)
}

// reqState is what the proxy callbacks need to know about the request in
// flight. It travels in the request context.
type reqState struct {
	id        string
	host      string
	endpoint  string
	path      string
	rawPath   string
	peerIP    string
	es        *envState
	perr      *platformError
	logStream string
}

type reqStateKey struct{}

// supportedMethods are the methods endpoints accept; anything else is a 400
// before the application sees it.
func supportedMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodOptions, http.MethodDelete:
		return true
	}
	return false
}

// serve handles one data-plane request.
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	start := s.clk.Now()
	id := newRequestID()
	cw := &countingWriter{ResponseWriter: w}

	perr, ev := s.dispatch(cw, r, id, start)
	if perr != nil && !cw.wrote {
		writePlatformError(cw, perr, id)
	}
	if ev != nil {
		ev.Status = cw.statusOr(http.StatusOK)
		ev.ResponseBytes = cw.bytes
		ev.Duration = s.clk.Since(start)
		if perr != nil {
			ev.ErrorType = perr.Type
		}
		s.telemetry().RequestDone(*ev)
	}
}

// dispatch runs the request through the sequence in the plan. It returns the
// platform error to write, if any, and the telemetry event for requests that
// got past routing.
func (s *Service) dispatch(w *countingWriter, r *http.Request, id string, start time.Time) (*platformError, *requestEvent) {
	// A request that CloudFront originated in this process carries a trusted
	// source ARN; anything an external client forged is stripped here, before
	// the headers can reach the application.
	sourceARN, _ := serviceutil.TakeCloudFrontSource(r)

	// 1. protocol checks
	if r.ProtoMajor == 1 && r.ProtoMinor == 0 {
		return errHTTPVersion(), nil
	}
	if !supportedMethod(r.Method) {
		return errMalformed("The request method is not supported."), nil
	}
	host := hostOnly(r.Host)
	if host == "" {
		return errNoHost(), nil
	}
	if headBytes(r) > maxHeadBytes {
		return errHeadersTooLarge(), nil
	}

	// 2. route
	tbl, err := s.routes(r.Context())
	if err != nil {
		s.log.Error("lambda-web: routing table", zap.Error(err))
		return &platformError{Status: http.StatusInternalServerError, Message: "Internal error"}, nil
	}
	ro := tbl.byHost[strings.ToLower(chi.URLParam(r, "id"))]
	if ro == nil {
		return errNoEndpoint(), nil
	}
	ep := ro.endpoint
	ev := &requestEvent{
		Env:       envRef{Region: ro.servedRegion, Function: ro.function.Name, Endpoint: ep.Name},
		RequestID: id, Method: r.Method, Path: r.URL.Path, Start: start,
		RequestBytes: max(r.ContentLength, 0),
	}

	// 3. state
	if !ro.active() {
		return errEndpointNotActive(), ev
	}
	if ro.rt.disabled.Load() {
		return errEndpointDisabled(), ev
	}
	if s.provider == nil {
		return &platformError{Status: http.StatusServiceUnavailable, Message: "Web function environments are not available: Docker is not configured."}, ev
	}

	// 4. auth
	if perr := s.authorize(r, ro, sourceARN); perr != nil {
		return perr, ev
	}

	// 5. throttle
	s.startEngine()
	now := start.UnixNano()
	if !s.dp.acct.Load().allow(now) {
		return s.throttled(ro, errTypeAccountRequestThrottle, "The account request rate limit was exceeded."), nil
	}
	if !ro.rt.limiter.Load().allow(now) {
		return s.throttled(ro, errTypeEndpointRequestThrot, "The endpoint request rate limit was exceeded."), nil
	}

	// 6. revision
	rr := pickRevision(ro)
	if rr == nil || !revisionActive(rr.rev) {
		return errRevisionNotActive(), ev
	}
	ev.Env.RevisionID = rr.rev.ID

	// 7. environment, within the revision's timeout
	timeout := 30 * time.Second
	if v := rr.rev.Service.TimeoutSeconds; v != nil && *v > 0 {
		timeout = time.Duration(*v) * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	acq, perr := s.acquire(rr)
	if perr != nil {
		if perr.Status == http.StatusTooManyRequests {
			s.telemetry().Throttled(throttleEvent{Region: ro.servedRegion, Function: ro.function.Name, Endpoint: ep.Name, ErrorType: perr.Type, At: start})
			return perr, nil // Throttled already counted it
		}
		return perr, ev
	}
	es := acq.es
	ev.ColdStart = acq.cold
	ev.Concurrent = int(es.inflight.Load())
	defer es.release(s.clk.Now().UnixNano())
	select {
	case <-es.ready:
	case <-ctx.Done():
		if r.Context().Err() != nil {
			return nil, nil // the client went away
		}
		return errTimeout(), ev
	}
	if es.failure != nil {
		return s.startFailureError(ro, rr.rev, es.failure), ev
	}
	ev.Env = es.ref
	es.served.Add(1)

	// 8. proxy
	st := &reqState{
		id: id, endpoint: ep.Name, es: es, logStream: es.ref.LogStream,
		host: s.forwardHost(r, ro), peerIP: peerIP(r),
	}
	st.path, st.rawPath = originalPath(r, ro)
	out := r.WithContext(context.WithValue(ctx, reqStateKey{}, st))
	es.proxy.ServeHTTP(w, out)
	return st.perr, ev
}

// authorize applies the endpoint's auth type. ApplicationManaged endpoints
// are not authorised at all; IamAuth endpoints go to the wired authoriser, or,
// with none wired, to a structural check (a signed request carrying the
// content hash header).
func (s *Service) authorize(r *http.Request, rt *route, sourceARN string) *platformError {
	if rt.endpoint.AuthType != authIam {
		return nil
	}
	if s.auth == nil {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-") || r.Header.Get("X-Amz-Content-Sha256") == "" {
			return errForbidden()
		}
		return nil
	}
	return s.auth.Authorize(r.Context(), r, authContext{
		Function: rt.function, Endpoint: rt.endpoint, EndpointARN: rt.endpointARN,
		Policy: rt.policy, Region: rt.servedRegion, SourceARN: sourceARN,
	})
}

func (s *Service) throttled(rt *route, errType, msg string) *platformError {
	perr := errThrottle(errType, msg)
	s.telemetry().Throttled(throttleEvent{
		Region: rt.servedRegion, Function: rt.function.Name, Endpoint: rt.endpoint.Name,
		ErrorType: errType, At: s.clk.Now(),
	})
	return perr
}

// pickRevision chooses among the endpoint's revisions in proportion to their
// weights: one random draw over at most two entries.
func pickRevision(rt *route) *routeRev {
	switch len(rt.revs) {
	case 0:
		return nil
	case 1:
		return &rt.revs[0]
	}
	n := rand.IntN(rt.totalWeight)
	for i := range rt.revs {
		if n < rt.revs[i].cumulative {
			return &rt.revs[i]
		}
	}
	return &rt.revs[len(rt.revs)-1]
}

// startFailureError turns a failed environment start into the response the
// first request sees.
func (s *Service) startFailureError(rt *route, rev *revision, f *initFailure) *platformError {
	if f.Type == errTypeCodeUnavailable {
		// The package is gone from S3: AWS moves the revision to Failed when it
		// next verifies access, permanently.
		if s.dp.codeGone != nil {
			s.dp.codeGone(rt.servedRegion, rt.function.Name, rev.ID, f.Detail)
		}
		return errRevisionNotActive()
	}
	return initFailureError(f)
}

func initFailureError(f *initFailure) *platformError {
	msg := "The application failed to start: " + f.Type
	if d := strings.TrimSpace(f.Detail); d != "" {
		if len(d) > 1024 {
			d = d[:1024]
		}
		msg += "\n" + d
	}
	return &platformError{Status: http.StatusBadGateway, Type: f.Type, Message: msg}
}

// ─── Proxy ───────────────────────────────────────────────────────────────────

var proxyBuffers = sync.Pool{New: func() any { b := make([]byte, 32*1024); return &b }}

type bufferPool struct{}

func (bufferPool) Get() []byte  { return *proxyBuffers.Get().(*[]byte) }
func (bufferPool) Put(b []byte) { proxyBuffers.Put(&b) }

// newEnvProxy builds the reverse proxy and keep-alive transport for one
// environment.
func (s *Service) newEnvProxy(addr string, concurrency int) (*httputil.ReverseProxy, *http.Transport) {
	tr := &http.Transport{
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           concurrency,
		MaxIdleConnsPerHost:    concurrency,
		IdleConnTimeout:        90 * time.Second,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	}
	rp := &httputil.ReverseProxy{
		Transport:      tr,
		BufferPool:     bufferPool{},
		Rewrite:        func(pr *httputil.ProxyRequest) { rewriteRequest(pr, addr) },
		ModifyResponse: modifyResponse,
		ErrorHandler:   s.proxyError,
	}
	return rp, tr
}

// rewriteRequest points the outbound request at the environment and applies
// the documented header rules.
func rewriteRequest(pr *httputil.ProxyRequest, addr string) {
	st, _ := pr.In.Context().Value(reqStateKey{}).(*reqState)
	out := pr.Out
	out.URL.Scheme = "http"
	out.URL.Host = addr
	out.URL.Path = st.path
	out.URL.RawPath = st.rawPath
	out.URL.RawQuery = pr.In.URL.RawQuery
	out.Host = st.host

	h := out.Header
	// Rewrite has already dropped RFC 7230 hop-by-hop headers (and those named
	// in Connection); remove the rest Lambda documents.
	for _, k := range removedRequestHeaders {
		h.Del(k)
	}
	for k := range h {
		if len(k) >= 11 && strings.EqualFold(k[:11], "lambda-web-") {
			delete(h, k)
		}
	}
	// Authorization carrying an AWS signature is validated and consumed here;
	// the application never sees credentials.
	if strings.HasPrefix(h.Get("Authorization"), "AWS4-") {
		h.Del("Authorization")
		h.Del("X-Amz-Security-Token")
	}
	// Rewrite removed the X-Forwarded-* family; Lambda appends the connecting
	// address to X-Forwarded-For and forwards X-Forwarded-Host untouched.
	if prior := pr.In.Header.Values("X-Forwarded-For"); len(prior) > 0 {
		h.Set("X-Forwarded-For", strings.Join(prior, ", ")+", "+st.peerIP)
	} else {
		h.Set("X-Forwarded-For", st.peerIP)
	}
	if v := pr.In.Header.Values("X-Forwarded-Host"); len(v) > 0 {
		h["X-Forwarded-Host"] = v
	}
	h.Set("Lambda-Web-Request-Id", st.id)
	h.Set("Lambda-Web-Endpoint-Name", st.endpoint)
}

// removedRequestHeaders are the request headers Lambda drops beyond the
// hop-by-hop set Go's proxy already removes.
var removedRequestHeaders = []string{
	"Expect", "Proxy-Connection", "X-Real-Ip",
	"X-Http-Method-Override", "X-Http-Method", "X-Method-Override",
}

func modifyResponse(resp *http.Response) error {
	st, _ := resp.Request.Context().Value(reqStateKey{}).(*reqState)
	resp.Header.Del("Trailer")
	resp.Trailer = nil
	if st != nil {
		resp.Header.Set("Lambda-Web-Request-Id", st.id)
		resp.Header.Set("Lambda-Web-Logstream", st.logStream)
	}
	return nil
}

// proxyError maps a failure to reach or read the application onto the error
// types AWS documents.
func (s *Service) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := r.Context().Value(reqStateKey{}).(*reqState)
	if st == nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	var perr *platformError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		perr = errTimeout()
	case errors.Is(err, context.Canceled):
		return // the client went away; there is nobody to answer
	default:
		perr = s.classifyEnvError(st.es, err)
	}
	st.perr = perr
	writePlatformError(w, perr, st.id)
}

// classifyEnvError says why a request to a live-looking environment failed.
func (s *Service) classifyEnvError(es *envState, err error) *platformError {
	if es != nil && es.env != nil {
		select {
		case <-es.env.Done():
			reason := es.env.ExitReason()
			switch {
			case reason.OOM:
				return &platformError{Status: http.StatusServiceUnavailable, Type: errTypeOutOfMemory, Message: "The environment ran out of memory."}
			case reason.Code != 0:
				return &platformError{Status: http.StatusServiceUnavailable, Type: errTypeExitError, Message: "The application process exited with an error."}
			default:
				return &platformError{Status: http.StatusServiceUnavailable, Type: errTypeExit, Message: "The application process exited."}
			}
		default:
		}
	}
	var perr *url.Error
	if errors.As(err, &perr) && strings.Contains(err.Error(), "malformed HTTP") {
		return &platformError{Status: http.StatusBadGateway, Type: errTypeInvalidResp, Message: "The application returned an invalid response."}
	}
	if strings.Contains(err.Error(), "server response headers exceeded") {
		return &platformError{Status: http.StatusBadGateway, Type: errTypeInvalidResp, Message: "The application's response headers were too large."}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return &platformError{Status: http.StatusServiceUnavailable, Type: errTypeOriginRequest, Message: "The application closed the connection without a response."}
	}
	return &platformError{Status: http.StatusServiceUnavailable, Type: errTypeUnreachable, Message: "The application could not be reached."}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// forwardHost is the Host the application receives: the endpoint domain,
// lowercase, without a port. The request's own Host is used when it is one of
// this endpoint's domains; a request that reached the invoke path directly
// (no wildcard DNS) gets the endpoint's stored domain.
func (s *Service) forwardHost(r *http.Request, rt *route) string {
	h := hostOnly(r.Host)
	if strings.Contains(h, "."+hostLabel+".") && domainID(h) == rt.id {
		return h
	}
	if rt.endpoint.Type == endpointMultiRegion {
		return rt.endpoint.DomainName
	}
	return rt.endpoint.RegionDomain[rt.servedRegion]
}

// originalPath recovers the path the client sent, undoing the host-route
// prefix, keeping its percent-encoding.
func originalPath(r *http.Request, rt *route) (path, rawPath string) {
	escaped := r.URL.EscapedPath()
	rest := escaped
	if i := strings.Index(escaped, "/"+rt.id+"/"); i >= 0 {
		// invokeBase/{id}/{region}/rest
		after := escaped[i+len(rt.id)+2:]
		if j := strings.IndexByte(after, '/'); j >= 0 {
			rest = after[j:]
		} else {
			rest = "/"
		}
	}
	if rest == "" {
		rest = "/"
	}
	p, err := url.PathUnescape(rest)
	if err != nil {
		return rest, ""
	}
	if p == rest {
		return p, ""
	}
	return p, rest
}

func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func peerIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// headBytes approximates the size of the request line, headers and blank line
// as the client sent them.
func headBytes(r *http.Request) int {
	n := len(r.Method) + 1 + len(r.RequestURI) + len(" HTTP/1.1\r\n") + 2
	for k, vs := range r.Header {
		for _, v := range vs {
			n += len(k) + 2 + len(v) + 2
		}
	}
	n += len("Host: \r\n") + len(r.Host)
	return n
}

const requestIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// newRequestID is 26 characters of base32, the shape AWS's examples show.
func newRequestID() string {
	var b [26]byte
	for i := range b {
		b[i] = requestIDAlphabet[rand.IntN(len(requestIDAlphabet))]
	}
	return string(b[:])
}

// writePlatformError writes a response Lambda itself produces.
func writePlatformError(w http.ResponseWriter, e *platformError, requestID string) {
	h := w.Header()
	h.Set("Lambda-Web-Request-Id", requestID)
	if e.Type != "" {
		h.Set("Lambda-Web-Error-Type", e.Type)
	}
	if e.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(e.Status)
	_, _ = io.WriteString(w, e.Message)
}

// countingWriter records the status and body size of a response.
type countingWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

// RecordAWSError forwards to the wrapped writer so an AWS error written behind
// this one still reaches the trace and request log.
func (c *countingWriter) RecordAWSError(aerr *protocol.AWSError) {
	if rec, ok := c.ResponseWriter.(interface {
		RecordAWSError(*protocol.AWSError)
	}); ok {
		rec.RecordAWSError(aerr)
	}
}

func (c *countingWriter) WriteHeader(code int) {
	if !c.wrote {
		c.status, c.wrote = code, true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if !c.wrote {
		c.status, c.wrote = http.StatusOK, true
	}
	n, err := c.ResponseWriter.Write(p)
	c.bytes += int64(n)
	return n, err
}

func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *countingWriter) statusOr(def int) int {
	if c.status == 0 {
		return def
	}
	return c.status
}
