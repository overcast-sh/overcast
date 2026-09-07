// Package bff implements the browser-facing API layer (BFF) for the Overcast
// web console. It exposes a single http.Handler that serves:
//
//   - GET /api/* — thin proxies / adapters that call the emulator's internal
//     endpoints and return JSON the SPA expects
//   - /* — SPA static files with index.html fallback for client-side routing
//
// This is the only implementation of /api/*. It began as a like-for-like Go
// port of a Hono/Node BFF that lived in web/api/src/; that mirror was retired
// in #1104, and the vite dev server's web/api/src/app.ts is now a thin proxy
// that forwards every /api/* request here. Method, path, request headers,
// response shape and streaming behaviour are therefore defined by this
// package alone — see docs/plans/dev-bff-consolidation.md.
package bff

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/overcast-sh/overcast/internal/docsindex"
	"github.com/overcast-sh/overcast/internal/docssearch"
)

const (
	endpointHeader = "x-overcast-endpoint"
	regionHeader   = "x-overcast-region"
	defaultUIPort  = 4567
)

// defaultAPIURL is the fallback endpoint used when the browser does not send
// an x-overcast-endpoint header. It is set from UIConfig.APIPort in
// NewHandler so that the BFF proxies to the correct port even when the
// emulator listens on a non-standard port.
var defaultAPIURL = "http://localhost:4566"

var bffHTTPClient = &http.Client{Timeout: 30 * time.Second}

// bffStreamingClient is used for long-lived streaming requests (SSE) where a
// total-request timeout would kill the connection prematurely. http.Client's
// Timeout covers reading the response body, so any handler that proxies a
// stream — the event feed, the Lambda invoke progress stream — must use this
// client. A Lambda invocation may legitimately run for the function's full
// configured timeout, up to AWS's 15-minute maximum.
var bffStreamingClient = &http.Client{}

// UIConfig is injected into the served index.html so the bundled SPA knows
// where to reach the emulator API without any client-side guessing.
type UIConfig struct {
	// APIPort is the port the emulator API is listening on (default 4566).
	// This is the port the BFF itself dials, so it must stay the port we
	// actually listen on even when host callers reach us on another one.
	APIPort int
	// BrowserAPIPort is the port a browser on the host must dial to reach the
	// API, which differs from APIPort when Overcast runs in a container
	// published on a different host port (`-p 4580:4566`). Zero means "same as
	// APIPort" — the case for a native binary and for a 1:1 port mapping.
	//
	// The distinction matters because the two ports are used from opposite
	// sides of the container boundary: BrowserAPIPort goes into the SPA's
	// apiBaseUrl, while APIPort is what this process dials on localhost.
	BrowserAPIPort int
	// Region is the default AWS region the emulator advertises.
	Region string
	// Debug indicates whether OVERCAST_DEBUG is enabled for the emulator.
	Debug bool
	// TLS indicates the emulator API (and this UI server) are served over
	// HTTPS. The SPA bootstrap then derives an https API base URL — an https
	// page cannot call an http API (mixed content) — and the BFF's own proxy
	// clients dial https.
	TLS bool
	// TLSTrustPEM holds PEM certificates the BFF's proxy clients trust in
	// addition to the system roots when TLS is set — the local overcast CA
	// in auto mode, or the configured certificate chain in explicit mode.
	// Without it the BFF could not verify the very server it fronts.
	TLSTrustPEM []byte
	// InDocker indicates the server process is running inside a Docker container.
	InDocker bool
}

// browserPort returns the port the SPA should be told to use.
func (c UIConfig) browserPort() int {
	if c.BrowserAPIPort > 0 {
		return c.BrowserAPIPort
	}
	return c.APIPort
}

// NewHandler returns an http.Handler that mounts all BFF routes under /api/
// and serves the embedded SPA for everything else.
//
// staticFS must be rooted at the dist directory (files accessible as "index.html",
// "assets/...", etc.). docsFS must be rooted at the published docs directory
// (files accessible as "services/s3.md", "cdk/local-vpc.md", etc.).
//
// cfg is injected into every served index.html so the SPA can reach the API
// without user configuration. Pass a zero value from dev/test callers that
// don't embed the UI.
func NewHandler(staticFS, docsFS fs.FS, cfg UIConfig) http.Handler {
	if cfg.APIPort > 0 {
		scheme := "http"
		if cfg.TLS {
			scheme = "https"
		}
		defaultAPIURL = fmt.Sprintf("%s://localhost:%d", scheme, cfg.APIPort)
	}
	configureAPITransports(cfg)

	r := chi.NewRouter()
	r.Use(corsMiddleware)

	// ── Simple JSON proxies ────────────────────────────────────────────────
	r.Get("/api/health", proxyJSONHandler("/_overcast/health"))
	// The daemon's CA certificate (PEM, public half only) — mirrored on the
	// UI origin for symmetry with the API's /_overcast/ca.pem, so either
	// port can hand a browser or script the cert to trust.
	r.Get("/api/ca.pem", handleCACert)
	// Settings → HTTPS: status is a plain JSON proxy; the mutating setup
	// route needs its own handler (Origin forwarding + no client timeout —
	// see handleTLSSetup).
	r.Get("/api/settings/https", proxyJSONHandler("/_overcast/tls/status"))
	r.Post("/api/settings/https/enable", handleTLSSetup)
	r.Get("/api/metrics", proxyJSONHandler("/_overcast/metrics"))
	r.Get("/api/topology", handleTopology)
	r.Get("/api/debug/state", handleDebugState)
	r.Get("/api/debug/state/*", handleDebugNamespace)
	r.Get("/api/debug/metrics", handleDebugMetrics)
	r.Get("/api/debug/trace/{requestId}", handleDebugTrace)
	r.Get("/api/debug/trace/{requestId}/events", handleDebugTraceEvents)
	r.Get("/api/debug/traces", handleDebugTraces)
	r.Get("/api/debug/traces/count", handleDebugTraceCount)
	r.Get("/api/debug/traces/search", handleDebugTraceSearch)
	// Compute debugger (docs/plans/compute-debugger.md § 6): the list and one
	// target, status and body passed through — an unknown resource's 404 is
	// what the console renders as "off".
	r.Get("/api/debugger/targets", proxyJSONHandler("/_overcast/debugger/targets"))
	r.Get("/api/debugger/targets/{service}/{resource}", handleDebuggerTarget)
	r.Get("/api/lambda/runtimes", proxyJSONHandler("/_overcast/lambda/runtimes"))
	r.Get("/api/lambda/layers/{layerName}/versions/{version}/metadata", handleLambdaLayerMetadata)
	r.Get("/api/lambda/instances", handleLambdaInstances)
	r.Get("/api/lambda/functions/{name}/metrics", handleLambdaMetrics)
	r.Get("/api/lambda/functions/{name}/source", handleLambdaSourceGet)
	r.Put("/api/lambda/functions/{name}/source", handleLambdaSourcePut)
	r.Post("/api/lambda/functions/{name}/invoke-with-progress", handleLambdaInvoke)
	r.Get("/api/lambda/functions/{name}/test-events", handleLambdaTestEventsGet)
	r.Put("/api/lambda/functions/{name}/test-events/{eventName}", handleLambdaTestEventPut)
	r.Delete("/api/lambda/functions/{name}/test-events/{eventName}", handleLambdaTestEventDelete)
	r.Get("/api/ecs/tasks/{taskArn}/logs/{container}", handleECSTaskLogs)
	r.Get("/api/ecs/clusters/{cluster}/tasks", handleECSClusterTasks)
	r.Get("/api/cloudformation/stacks/{stackName}/diagnostics", handleCFNStackDiagnostics)
	r.Get("/api/mail/messages", handleMailList)
	r.Get("/api/mail/messages/{id}", handleMailGet)
	r.Delete("/api/mail/messages", handleMailDeleteAll)
	r.Delete("/api/mail/messages/{id}", handleMailDeleteOne)
	r.Get("/api/inbox/messages", handleMailList)
	r.Get("/api/inbox/messages/{id}", handleMailGet)
	r.Delete("/api/inbox/messages", handleMailDeleteAll)
	r.Delete("/api/inbox/messages/{id}", handleMailDeleteOne)
	r.Get("/api/rds/instances/{id}/logs", handleRDSLogs)
	r.Get("/api/eventbridge/deliveries", handleEventBridgeDeliveries)
	r.Get("/api/eventbridge/rule-targets", handleEventBridgeRuleTargets)
	r.Get("/api/pipes/wiring", handlePipeWiring)
	r.Get("/api/pipes/deliveries", handlePipeDeliveries)
	r.Get("/api/preflight/region", handlePreflightRegion)

	// ── SSE proxy ─────────────────────────────────────────────────────────
	r.Get("/api/events", handleEvents)

	// ── S3 routes ─────────────────────────────────────────────────────────
	r.Get("/api/s3/buckets/{bucket}/objects/{key:.+}/download", handleS3Download)
	r.Post("/api/s3/buckets/{bucket}/objects/archive", handleS3Archive)
	r.Put("/api/s3/buckets/{bucket}/objects/{key:.+}", handleS3Upload)
	r.Delete("/api/s3/buckets/{bucket}/objects-by-prefix", handleS3BulkDelete)

	// ── SQS routes ────────────────────────────────────────────────────────
	r.Get("/api/sqs/queues/{name}/messages", handleSQSPeek)
	r.Get("/api/sqs/queues/{name}/metrics", handleSQSMetrics)

	// ── SNS routes ────────────────────────────────────────────────────────
	r.Get("/api/sns/topics/{topicName}/metrics", handleSNSMetrics)

	// ── DynamoDB routes ───────────────────────────────────────────────────
	r.Get("/api/dynamodb/tables/{name}/metrics", handleDynamoDBMetrics)

	// ── API Gateway routes ────────────────────────────────────────────────
	r.Get("/api/apigateway/restapis/{apiId}/metrics", handleAPIGatewayRestApiMetrics)
	r.Get("/api/apigateway/apis/{apiId}/metrics", handleAPIGatewayApiMetrics)

	// ── Docs ──────────────────────────────────────────────────────────────
	// The navigation and the search corpus are both derived from docsFS, once,
	// on first use. They used to be generated files committed to the repository
	// (web/src/docs-nav.gen.ts and internal/docssearch/index.gen.jsonl); see
	// internal/docsindex for why they are not any more.
	docs := newDocsIndex(docsFS)
	r.Get("/api/docs/nav", handleDocsNav(docs))
	r.Get("/api/docs/search", handleDocsSearch(docs))
	r.Get("/api/docs/page", handleDocsPage(docsFS))
	// Registered last of the /api/docs/* set: chi matches static segments
	// before a wildcard, so "nav" and "search" reach their own handlers rather
	// than being read as service names.
	r.Get("/api/docs/{service}", handleDocs(docsFS))

	// ── SPA fallback ──────────────────────────────────────────────────────
	r.NotFound(spaHandlerFunc(staticFS, cfg))
	r.Get("/*", spaHandlerFunc(staticFS, cfg))

	return r
}

// configureAPITransports points the BFF's proxy clients at the emulator API's
// scheme. With TLS on, both clients get a transport whose root pool is the
// system pool plus cfg.TLSTrustPEM (the local overcast CA, or the operator's
// own chain) — the leaf covers "localhost", which is what the BFF dials.
// Without TLS the default transports are restored, so tests and dev callers
// that build multiple handlers can flip modes freely.
func configureAPITransports(cfg UIConfig) {
	if !cfg.TLS {
		bffHTTPClient.Transport = nil
		bffStreamingClient.Transport = nil
		return
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(cfg.TLSTrustPEM)
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		tr = &http.Transport{}
	}
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	bffHTTPClient.Transport = tr
	bffStreamingClient.Transport = tr.Clone()
}

// ── Middleware ─────────────────────────────────────────────────────────────

var allowedOrigins = map[string]bool{
	"http://localhost:3000":  true,
	"https://localhost:3000": true,
	"http://localhost:5173":  true,
	"https://localhost:5173": true,
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, x-overcast-endpoint, x-overcast-region")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Helpers ────────────────────────────────────────────────────────────────

// resolveEndpoint returns the emulator endpoint URL derived from the
// x-overcast-endpoint request header. Falls back to the configured API
// URL (default http://localhost:4566, overridden from UIConfig.APIPort).
func resolveEndpoint(r *http.Request) string {
	if ep := r.Header.Get(endpointHeader); ep != "" {
		return normalizeEndpoint(strings.TrimRight(ep, "/"))
	}
	return defaultAPIURL
}

// normalizeEndpoint maps an endpoint the browser derived from its own origin
// back to one this process can dial.
//
// In a container published as `-p 4580:4566` the SPA is correctly told the API
// is on 4580 and echoes that back in the x-overcast-endpoint header, but 4580
// exists only on the host: from inside the container the very same API is on
// 4566. Without this the browser reaches the API directly but every /api/*
// proxy call fails.
//
// Only loopback hosts on our own published port are rewritten, so pointing the
// console at a genuinely different emulator still works — that endpoint names
// another host.
func normalizeEndpoint(ep string) string {
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return ep
	}
	// Any loopback endpoint must be rewritten to the internal API URL,
	// because from inside the container the emulator is always on
	// localhost:APIPort regardless of host port mappings.
	if isLoopbackHost(u.Hostname()) {
		return defaultAPIURL
	}
	return ep
}

// isLoopbackHost reports whether hostname refers to this machine.
func isLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

// resolveEndpointQP is like resolveEndpoint but also checks query parameters.
// This is needed for routes where the browser cannot send custom headers:
// <a> download links (navigational GET with no header control) and
// EventSource connections (which don't support custom headers).
func resolveEndpointQP(r *http.Request) string {
	if ep := r.Header.Get(endpointHeader); ep != "" {
		return normalizeEndpoint(strings.TrimRight(ep, "/"))
	}
	if ep := r.URL.Query().Get(endpointHeader); ep != "" {
		return normalizeEndpoint(strings.TrimRight(ep, "/"))
	}
	if ep := r.URL.Query().Get("ep"); ep != "" {
		return normalizeEndpoint(strings.TrimRight(ep, "/"))
	}
	return defaultAPIURL
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func copyResponseBody(w http.ResponseWriter, src io.Reader) bool {
	if _, err := io.Copy(w, src); err != nil {
		return false
	}
	return true
}

// escapeKeySegments URL-escapes each segment of a key path, preserving "/"
// separators.  This matches how S3 path-style URLs represent object keys:
// "/" in the key becomes a literal "/" in the URL, not "%2F".
func escapeKeySegments(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func doGet(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return bffHTTPClient.Do(req)
}

func doGetWithRegion(ctx context.Context, u string, incoming *http.Request) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	forwardRegion(req, incoming)
	return bffHTTPClient.Do(req)
}

func doPost(ctx context.Context, u, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return bffHTTPClient.Do(req)
}

//nolint:unused // Kept for BFF routes that proxy Query-protocol form posts.
func doPostForm(ctx context.Context, u string, form url.Values) (*http.Response, error) {
	return doPost(ctx, u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
}

// proxyJSONHandler proxies the request to the emulator's internal path and
// copies the JSON response verbatim. Used for simple pass-through routes.
func proxyJSONHandler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ep := resolveEndpoint(r)
		resp, err := doGet(r.Context(), ep+path)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		if !copyResponseBody(w, resp.Body) {
			return
		}
	}
}

// handleDebuggerTarget proxies GET /_overcast/debugger/targets/{service}/{resource},
// carrying the optional ?container= an ECS task's containers are told apart by.
// The emulator's status is passed through as-is: the synthesised "not tagged"
// entry is a 200, and a resource no service can describe is a 404 the console
// treats as "off" rather than as a failure.
func handleDebuggerTarget(w http.ResponseWriter, r *http.Request) {
	path := "/_overcast/debugger/targets/" + url.PathEscape(chi.URLParam(r, "service")) +
		"/" + url.PathEscape(chi.URLParam(r, "resource"))
	if container := r.URL.Query().Get("container"); container != "" {
		path += "?container=" + url.QueryEscape(container)
	}
	proxyJSONHandler(path)(w, r)
}

func handleDebugState(w http.ResponseWriter, r *http.Request) {
	proxyDebugState(w, r, "/_overcast/debug/state")
}

func handleDebugNamespace(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimPrefix(r.URL.EscapedPath(), "/api/debug/state/")
	proxyDebugState(w, r, "/_overcast/debug/state/"+namespace)
}

// handleDebugMetrics proxies GET /_overcast/debug/metrics — the storage diagnostics +
// advisories payload behind the web UI's Metrics & Health page (see
// internal/router/debug.go's debugMetrics and advisories.go). Shares
// proxyDebugState's "DebugDisabled" 404 translation rather than
// proxyJSONHandler's plain pass-through, since /_overcast/debug/* only exists at all
// when the emulator has OVERCAST_DEBUG=true — the Metrics & Health page needs
// the same recognizable, non-generic error the raw-state debugger already
// gives so its empty/degraded state can explain why, instead of showing a
// bare "HTTP 404".
func handleDebugMetrics(w http.ResponseWriter, r *http.Request) {
	proxyDebugState(w, r, "/_overcast/debug/metrics")
}

func handleDebugTrace(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "requestId")
	proxyDebugJSON(w, r, "/_overcast/debug/trace/"+url.PathEscape(requestID))
}

func handleDebugTraceEvents(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "requestId")
	proxyDebugJSON(w, r, "/_overcast/events/request/"+url.PathEscape(requestID))
}

func handleDebugTraces(w http.ResponseWriter, r *http.Request) {
	proxyDebugJSON(w, r, "/_overcast/debug/traces")
}

func handleDebugTraceCount(w http.ResponseWriter, r *http.Request) {
	proxyDebugJSON(w, r, "/_overcast/debug/traces/count")
}

// handleDebugTraceSearch proxies the deep scan of trace bodies, hop errors and
// log entries.
//
// It is the one debug proxy where cancellation is load-bearing: the scan on the
// far side runs for as long as its budget allows, and an abandoned search has
// to stop rather than run to completion for a result nobody will read.
// proxyDebugJSON passes r.Context() to the upstream call, so a browser that
// aborts the fetch closes this connection, which cancels that context, which
// ends the scan.
func handleDebugTraceSearch(w http.ResponseWriter, r *http.Request) {
	proxyDebugJSON(w, r, "/_overcast/debug/traces/search")
}

// proxyDebugJSON proxies a debug endpoint, forwarding query params and
// passing through the response status and body without translating 404s
// (unlike proxyDebugState which maps 404 to "DebugDisabled").
func proxyDebugJSON(w http.ResponseWriter, r *http.Request, path string) {
	ep := resolveEndpoint(r)
	url := ep + path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	resp, err := doGet(r.Context(), url)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	copyDebugResponse(w, resp)
}

func proxyDebugState(w http.ResponseWriter, r *http.Request, path string) {
	ep := resolveEndpoint(r)
	debugURL := ep + path
	if r.URL.RawQuery != "" {
		debugURL += "?" + r.URL.RawQuery
	}
	resp, err := doGet(r.Context(), debugURL)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "debug state fetch failed")
		return
	}
	defer resp.Body.Close()

	selectedKey := r.URL.Query().Get("key")
	if resp.StatusCode == http.StatusNotFound && selectedKey == "" {
		writeDebugDisabledError(w)
		return
	}
	if selectedKey != "" {
		copyDebugResponse(w, resp)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSONError(w, http.StatusBadGateway, "debug state fetch failed")
		return
	}

	copyDebugResponse(w, resp)
}

func copyDebugResponse(w http.ResponseWriter, resp *http.Response) {
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// writeDebugDisabledError is shared by every /_overcast/debug/* proxy in this file
// (raw state and metrics/advisories alike) — a stable "error": "DebugDisabled"
// code callers can key off of, plus a human-readable message covering both
// use cases so it reads correctly regardless of which /_overcast/debug/* route hit it.
func writeDebugDisabledError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}{
		Error:   "DebugDisabled",
		Message: "OVERCAST_DEBUG must be enabled to inspect raw state or storage diagnostics.",
	})
}

// assetsDirPrefix is where Vite emits the hashed bundle (`web/dist/assets/`).
// The SPA never routes below it, so a miss under this prefix is a missing
// file rather than a client-side route.
const assetsDirPrefix = "assets/"

// spaHandlerFunc serves static files from staticFS; unmatched paths fall back
// to index.html for client-side routing. When serving index.html it injects a
// <script>window.__OVERCAST__ = {...}</script> tag so the bundled SPA can
// reach the API without user configuration.
func spaHandlerFunc(staticFS fs.FS, cfg UIConfig) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(staticFS))
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// Only index.html gets the config injection — other assets are hashed
		// and cached aggressively, so we serve them verbatim via FileServer.
		if p == "index.html" {
			serveIndexHTML(w, r, staticFS, cfg)
			return
		}
		f, err := staticFS.Open(p)
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Nothing under the bundle directory is ever a client-side route, so a
		// miss there is a missing build artefact — say 404 instead of handing
		// back index.html with a 200. Falling back made a genuinely absent
		// chunk (a stale hash, a half-copied dist) surface in the browser as
		// `Unexpected token '<'` from a script that "loaded", and made a plain
		// `curl` of any asset path succeed whether or not the file existed —
		// which is what left #1609 unfalsifiable from the outside.
		if strings.HasPrefix(p, assetsDirPrefix) {
			http.NotFound(w, r)
			return
		}
		// Client-side route — serve index.html
		serveIndexHTML(w, r, staticFS, cfg)
	}
}

// indexHeadClose matches </head> (case-insensitive). We insert the bootstrap
// script immediately before it so it executes before any bundled JS runs.
var indexHeadClose = regexp.MustCompile(`(?i)</head>`)

func serveIndexHTML(w http.ResponseWriter, r *http.Request, staticFS fs.FS, cfg UIConfig) {
	raw, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		// web/dist holds only its committed .gitkeep placeholder, so this
		// binary embeds no SPA. That is a build-step omission, not a runtime
		// fault — say so rather than returning a bare 500. Release builds and
		// `make ci-local` both assert a real index.html, so this should only
		// ever be reachable from a local `go build` that skipped the SPA.
		http.Error(w, "web UI not built into this binary — run `make build-web`, then rebuild. "+
			"The API is unaffected and still served on the API port.", http.StatusServiceUnavailable)
		return
	}

	bootstrap := buildBootstrapScript(r, cfg)
	body := indexHeadClose.ReplaceAll(raw, []byte(bootstrap+"</head>"))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// index.html varies by request Host (for the bridge-vs-direct API URL) so
	// disable caching — the hashed bundle files it references are still cached.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(body) //nolint:errcheck
}

// buildBootstrapScript returns a <script> tag that sets window.__OVERCAST__
// based on the incoming request's Host header. The scheme is http unless the
// emulator is configured for TLS or the request itself arrived over TLS.
// When the API port cannot be determined (Docker remap without socket),
// apiBaseUrl is empty and endpointKnown is false — the SPA shows the
// connection dialog instead of guessing.
func buildBootstrapScript(r *http.Request, cfg UIConfig) string {
	apiBaseURL, endpointKnown := deriveAPIBaseURL(r, cfg)
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	payload, _ := json.Marshal(map[string]any{
		"apiBaseUrl":    apiBaseURL,
		"region":        region,
		"debug":         cfg.Debug,
		"endpointKnown": endpointKnown,
		"inDocker":      cfg.InDocker,
	})
	return `<script>window.__OVERCAST__=` + string(payload) + `;</script>`
}

func deriveAPIBaseURL(r *http.Request, cfg UIConfig) (url string, endpointKnown bool) {
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	if strings.EqualFold(host, "overcast-app.local") {
		return "http://overcast.local", true
	}
	hostname := host
	hasPort := false
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
		hasPort = true
	}
	scheme := "http"
	if cfg.TLS || r.TLS != nil {
		scheme = "https"
	}
	if !hasPort {
		return scheme + "://" + hostname, true
	}
	if apiPort := cfg.browserPort(); apiPort > 0 && apiPort != cfg.APIPort {
		return scheme + "://" + hostname + ":" + strconv.Itoa(apiPort), true
	}
	if _, portStr, err := net.SplitHostPort(host); err == nil {
		if p, _ := strconv.Atoi(portStr); p == defaultUIPort || p == cfg.APIPort+1 {
			return scheme + "://" + hostname + ":" + strconv.Itoa(cfg.APIPort), true
		}
	}
	return "", false
}

// ── Route handlers ─────────────────────────────────────────────────────────

// handleCACert proxies GET /_overcast/ca.pem — the emulator's local CA
// certificate — preserving status and content type verbatim (this is the one
// BFF route that is deliberately not JSON).
func handleCACert(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	resp, err := doGet(r.Context(), ep+"/_overcast/ca.pem")
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// handleTLSSetup proxies POST /api/settings/https/enable to the daemon's
// /_overcast/tls/setup (the in-daemon `overcast https enable`). Two ways this
// deliberately differs from proxyJSONHandler:
//
//   - The browser's Origin header is forwarded, so the daemon's cross-origin
//     guard (internal/router/tls_settings.go) judges the real page origin
//     rather than seeing a header-less proxy dial and waving it through.
//   - bffStreamingClient, not bffHTTPClient: a native trust install blocks on
//     the OS's approval prompt, and a user reading that dialog for more than
//     30 seconds must not have the request cut out from under the prompt.
func handleTLSSetup(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ep+"/_overcast/tls/setup", nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := bffStreamingClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleTopology(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	qs := ""
	if region := r.URL.Query().Get("region"); region != "" {
		qs = "?region=" + url.QueryEscape(region)
	}
	resp, err := doGet(r.Context(), ep+"/_overcast/topology"+qs)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "topology fetch failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// handleEvents proxies the SSE stream from /_overcast/events. Supports endpoint and
// region via query params (ep, region) in addition to headers, because
// browsers cannot send custom headers with EventSource.
func handleEvents(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpointQP(r)

	upstream := ep + "/_overcast/events"
	q := url.Values{}
	for _, s := range r.URL.Query()["source"] {
		q.Add("source", s)
	}
	// The resume point a reconnecting client sent, so /_overcast/events replays only
	// what the client is missing instead of its whole history buffer. It can
	// arrive either way — see the Last-Event-ID handling in
	// internal/router/events.go — and both have to survive this hop, since
	// the upstream request is built here rather than forwarded.
	if id := r.URL.Query().Get("last_event_id"); id != "" {
		q.Set("last_event_id", id)
	}
	if len(q) > 0 {
		upstream += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		req.Header.Set("Last-Event-ID", id)
	}

	resp, err := bffStreamingClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "event stream unavailable")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		writeJSONError(w, http.StatusBadGateway, "event stream unavailable")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// ── S3 ─────────────────────────────────────────────────────────────────────

func handleS3Download(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpointQP(r)
	bucket := chi.URLParam(r, "bucket")
	key := chi.URLParam(r, "key")

	// The chi param is raw (URL-encoded) from the request path.
	// Unescape to the real key, then escape each path segment individually
	// so that "/" separators are preserved.  url.PathEscape would encode
	// "/" to "%2F", but S3 path-style URLs use actual "/" as the key
	// hierarchy delimiter.
	realKey, _ := url.PathUnescape(key)
	upstream := fmt.Sprintf("%s/%s/%s", ep, bucket, escapeKeySegments(realKey))

	// A version-addressed read has to stay version-addressed across this hop.
	// Without ?versionId= S3 answers with whatever is current, so the download
	// link and the preview on an older version row would serve the newest
	// bytes while the metadata beside them — read through HeadObject, which
	// does carry the version — described the older one.
	//
	// The parameter is forwarded whenever the client sent it, "null" included:
	// that is the real version id of every object written while the bucket was
	// unversioned or suspended, not an absent value.
	if q := r.URL.Query(); q.Has("versionId") {
		upstream += "?versionId=" + url.QueryEscape(q.Get("versionId"))
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Range is the one request header this hop has a caller for. The object
	// preview asks for the first megabyte instead of the whole object and
	// reads the 206 back to decide whether to say the text was cut short;
	// with the header dropped every preview pulled the entire object into the
	// browser and then claimed to be complete.
	//
	// Nothing else is forwarded. GetObject also honours If-Match and
	// If-None-Match, but no caller here sends them, and a conditional the
	// browser attached from its own cache would come back as a bodyless 304 —
	// an empty preview — instead of the bytes that were asked for.
	if v := r.Header.Get("Range"); v != "" {
		req.Header.Set("Range", v)
	}

	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()

	// Whatever S3 answered reaches the browser as its own status: 206 for a
	// served range, 416 for one past the end of the object, and the 404 / 405
	// / 412 a version-addressed read can produce. Collapsing the success
	// statuses to 200 is what made the preview's truncation notice dead code.
	served := resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent

	copyHeader := func(name string) {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	for _, h := range []string{
		"Content-Type", "Content-Length", "ETag", "Last-Modified",
		// The ranged pair. Content-Range carries the window served and the
		// object's full size — a 416 sends it too, as "bytes */<size>", which
		// is how a caller learns what it should have asked for — and
		// Accept-Ranges advertises that ranges work here at all.
		"Content-Range", "Accept-Ranges",
	} {
		copyHeader(h)
	}

	if served {
		filename := realKey
		if i := strings.LastIndex(realKey, "/"); i >= 0 {
			filename = realKey[i+1:]
		}
		// A download filename describes bytes, so it is not attached to an
		// emulator error body.
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}

	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleS3Upload(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	bucket := chi.URLParam(r, "bucket")
	key := chi.URLParam(r, "key")

	realKey, _ := url.PathUnescape(key)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPut,
		fmt.Sprintf("%s/%s/%s", ep, bucket, escapeKeySegments(realKey)), r.Body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Forward relevant headers
	for _, h := range []string{"Content-Type", "Content-Length", "x-amz-storage-class",
		"x-object-content-disposition", "x-object-content-encoding",
		"x-object-content-language", "x-object-cache-control", "x-object-expires"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	// Forward x-amz-meta-* headers
	for k, vv := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
	}

	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// S3 ListObjectsV2 and DeleteObjects XML shapes for bulk-delete pagination.

type listBucketResult struct {
	Contents              []struct{ Key string }
	IsTruncated           bool
	NextContinuationToken string
}

func handleS3BulkDelete(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	bucket := chi.URLParam(r, "bucket")
	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		writeJSONError(w, http.StatusBadRequest, "prefix query parameter is required")
		return
	}

	deleted := 0
	token := ""

	for {
		listURL := fmt.Sprintf("%s/%s?list-type=2&prefix=%s&max-keys=1000",
			ep, bucket, url.QueryEscape(prefix))
		if token != "" {
			listURL += "&continuation-token=" + url.QueryEscape(token)
		}
		resp, err := doGet(r.Context(), listURL)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
			return
		}
		var result listBucketResult
		if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			writeJSONError(w, http.StatusBadGateway, "listing parse error")
			return
		}
		resp.Body.Close()

		if len(result.Contents) > 0 {
			var sb strings.Builder
			sb.WriteString(`<Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Quiet>true</Quiet>`)
			for _, obj := range result.Contents {
				sb.WriteString("<Object><Key>")
				if err := xml.EscapeText(&sb, []byte(obj.Key)); err != nil {
					writeJSONError(w, http.StatusInternalServerError, "failed to encode object key")
					return
				}
				sb.WriteString("</Key></Object>")
			}
			sb.WriteString("</Delete>")

			delResp, err := doPost(
				r.Context(),
				fmt.Sprintf("%s/%s?delete", ep, bucket),
				"application/xml",
				strings.NewReader(sb.String()),
			)
			if err != nil {
				writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
				return
			}
			delResp.Body.Close()
			deleted += len(result.Contents)
		}

		if !result.IsTruncated {
			break
		}
		token = result.NextContinuationToken
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": deleted})
}

// ── SQS ────────────────────────────────────────────────────────────────────

type sqsMessage struct {
	MessageID               string                `json:"messageId"`
	ReceiptHandle           string                `json:"receiptHandle"`
	Body                    string                `json:"body"`
	MD5OfBody               string                `json:"md5OfBody"`
	Attributes              map[string]string     `json:"attributes"`
	MessageAttributes       map[string]sqsMsgAttr `json:"messageAttributes"`
	Inflight                bool                  `json:"inflight"`
	Delayed                 bool                  `json:"delayed"`
	VisibleAfter            float64               `json:"visibleAfter"`
	ApproximateReceiveCount int                   `json:"approximateReceiveCount"`
}

type sqsMsgAttr struct {
	DataType    string `json:"dataType"`
	StringValue string `json:"stringValue"`
}

// handleSQSMetrics proxies GET /_overcast/sqs/queues/{name}/metrics — SQS's
// half of the Monitor tab read-through, mirroring handleLambdaMetrics.
func handleSQSMetrics(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	qs := ""
	if rng := r.URL.Query().Get("range"); rng != "" {
		qs = "?range=" + url.QueryEscape(rng)
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/sqs/queues/%s/metrics%s", ep, url.PathEscape(name), qs), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleSQSPeek(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")

	// GetQueueUrl via SQS JSON protocol (the emulator uses X-Amz-Target dispatch).
	body := `{"QueueName":"` + name + `"}`
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ep+"/", strings.NewReader(body))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.GetQueueUrl")
	forwardRegion(req, r)

	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Read the body once so we can use it for both error and success paths.
	respBody, _ := io.ReadAll(resp.Body)

	// If the emulator returned an error, forward it instead of silently masking it.
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody) //nolint:errcheck
		return
	}

	var queueURLResult struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if err := json.Unmarshal(respBody, &queueURLResult); err != nil {
		writeJSONError(w, http.StatusBadGateway, "queue URL parse error: "+string(respBody))
		return
	}

	queueURL := queueURLResult.QueueUrl
	if queueURL == "" {
		writeJSONError(w, http.StatusNotFound, "queue not found: GetQueueUrl returned empty (body: "+string(respBody)+")")
		return
	}

	// Peek: GET the queue URL on the emulator endpoint (not the returned host).
	peekPath := func() string {
		u, err := url.Parse(queueURL)
		if err != nil {
			return queueURL
		}
		return ep + u.Path
	}()

	peekResp, err := doGetWithRegion(r.Context(), peekPath, r)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer peekResp.Body.Close()

	var raw struct {
		Messages []struct {
			MessageId         string
			ReceiptHandle     string
			Body              string
			MD5OfBody         string
			Attributes        map[string]string
			MessageAttributes map[string]struct {
				DataType    string
				StringValue string
			}
			Inflight                bool
			Delayed                 bool
			VisibleAfter            float64
			ApproximateReceiveCount int
		}
	}
	if err := json.NewDecoder(peekResp.Body).Decode(&raw); err != nil {
		writeJSONError(w, http.StatusBadGateway, "message parse error")
		return
	}

	messages := make([]sqsMessage, 0, len(raw.Messages))
	for _, m := range raw.Messages {
		attrs := make(map[string]sqsMsgAttr, len(m.MessageAttributes))
		for k, v := range m.MessageAttributes {
			attrs[k] = sqsMsgAttr{DataType: v.DataType, StringValue: v.StringValue}
		}
		messages = append(messages, sqsMessage{
			MessageID:               m.MessageId,
			ReceiptHandle:           m.ReceiptHandle,
			Body:                    m.Body,
			MD5OfBody:               m.MD5OfBody,
			Attributes:              m.Attributes,
			MessageAttributes:       attrs,
			Inflight:                m.Inflight,
			Delayed:                 m.Delayed,
			VisibleAfter:            m.VisibleAfter,
			ApproximateReceiveCount: m.ApproximateReceiveCount,
		})
	}
	if messages == nil {
		messages = []sqsMessage{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"messages": messages})
}

// ── Lambda ─────────────────────────────────────────────────────────────────

func handleLambdaInstances(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	resp, err := doGet(r.Context(), ep+"/_overcast/lambda/instances")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"instances": []any{}})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"instances": []any{}})
		return
	}

	var raw struct {
		Instances []map[string]any `json:"instances"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"instances": []any{}})
		return
	}

	for i, inst := range raw.Instances {
		if _, ok := inst["instanceId"]; !ok {
			if id, ok := inst["id"]; ok {
				raw.Instances[i]["instanceId"] = id
			} else {
				raw.Instances[i]["instanceId"] = ""
			}
		}
	}
	if raw.Instances == nil {
		raw.Instances = []map[string]any{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"instances": raw.Instances})
}

// forwardRegion copies the X-Overcast-Region header from the incoming BFF
// request to the outgoing upstream request. Without this, the emulator's Region
// middleware falls back to its configured default, causing region-scoped store
// lookups to miss when the client is using a non-default region.
func forwardRegion(upstream *http.Request, incoming *http.Request) {
	if region := incoming.Header.Get(regionHeader); region != "" {
		upstream.Header.Set("X-Overcast-Region", region)
	}
}

// handleSNSMetrics proxies GET /_overcast/sns/topics/{topicName}/metrics —
// SNS's half of the Monitor tab read-through (phase 4), mirroring
// handleLambdaMetrics/handleSQSMetrics.
func handleSNSMetrics(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "topicName")
	qs := ""
	if rng := r.URL.Query().Get("range"); rng != "" {
		qs = "?range=" + url.QueryEscape(rng)
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/sns/topics/%s/metrics%s", ep, url.PathEscape(name), qs), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// handleDynamoDBMetrics proxies GET /_overcast/dynamodb/tables/{name}/metrics
// — DynamoDB's half of the Monitor tab read-through (phase 4), mirroring
// handleLambdaMetrics/handleSQSMetrics.
func handleDynamoDBMetrics(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	qs := ""
	if rng := r.URL.Query().Get("range"); rng != "" {
		qs = "?range=" + url.QueryEscape(rng)
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/dynamodb/tables/%s/metrics%s", ep, url.PathEscape(name), qs), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// apiGatewayMetricsQueryString builds the forwarded query string for both
// API Gateway Monitor proxies below — the one BFF metrics proxy pair that
// forwards more than "range" (handleLambdaMetrics/handleSQSMetrics/
// handleSNSMetrics/handleDynamoDBMetrics only ever forward "range"; API
// Gateway's emulator-side endpoints also accept "stage" — see
// internal/services/apigateway/handler_metrics.go, #1307).
func apiGatewayMetricsQueryString(r *http.Request) string {
	q := url.Values{}
	if rng := r.URL.Query().Get("range"); rng != "" {
		q.Set("range", rng)
	}
	if stage := r.URL.Query().Get("stage"); stage != "" {
		q.Set("stage", stage)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// handleAPIGatewayRestApiMetrics proxies
// GET /_overcast/apigateway/restapis/{apiId}/metrics — the REST (v1) half of
// the Monitor tab read-through (phase 4, #1307), mirroring
// handleLambdaMetrics/handleSNSMetrics/handleDynamoDBMetrics.
func handleAPIGatewayRestApiMetrics(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	apiID := chi.URLParam(r, "apiId")
	qs := apiGatewayMetricsQueryString(r)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/apigateway/restapis/%s/metrics%s", ep, url.PathEscape(apiID), qs), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// handleAPIGatewayApiMetrics proxies
// GET /_overcast/apigateway/apis/{apiId}/metrics — the HTTP (v2) half of the
// Monitor tab read-through (phase 4, #1307). "apis" matches API Gateway v2's
// own resource naming (CreateApi/GetApis/...), distinct from the REST
// endpoint's "restapis" above.
func handleAPIGatewayApiMetrics(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	apiID := chi.URLParam(r, "apiId")
	qs := apiGatewayMetricsQueryString(r)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/apigateway/apis/%s/metrics%s", ep, url.PathEscape(apiID), qs), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// handleLambdaMetrics proxies GET /_overcast/lambda/functions/{name}/metrics —
// the web Monitor tab's read-through into the shared service-metrics
// repository (docs/plans/service-metrics-platform.md phase 3). Forwards only
// the "range" query parameter the emulator-side allowlist endpoint accepts;
// this proxy never constructs or parses CloudWatch protocol itself.
func handleLambdaMetrics(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	qs := ""
	if rng := r.URL.Query().Get("range"); rng != "" {
		qs = "?range=" + url.QueryEscape(rng)
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/lambda/functions/%s/metrics%s", ep, url.PathEscape(name), qs), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleLambdaSourceGet(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	qs := ""
	if f := r.URL.Query().Get("file"); f != "" {
		qs = "?file=" + url.QueryEscape(f)
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/lambda/functions/%s/source%s", ep, url.PathEscape(name), qs), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleLambdaSourcePut(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPut,
		fmt.Sprintf("%s/_overcast/lambda/functions/%s/source", ep, url.PathEscape(name)), r.Body)
	req.Header.Set("Content-Type", "application/json")
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// handleLambdaLayerMetadata proxies GET /_overcast/lambda/layers/{layerName}/versions/{version}/metadata
// — whether a layer version's archive carries an external (non-runtime-native)
// Lambda extension, shown on the layer detail page. This route was missing
// from the BFF entirely until the dev-BFF-consolidation audit (#1104) found
// it: the Node dev server called the emulator directly for it, so a built
// binary 404'd here while dev worked — the drift class this proxy layer
// exists to make impossible.
func handleLambdaLayerMetadata(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	layerName := chi.URLParam(r, "layerName")
	version := chi.URLParam(r, "version")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/lambda/layers/%s/versions/%s/metadata",
			ep, url.PathEscape(layerName), url.PathEscape(version)), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleLambdaInvoke(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		fmt.Sprintf("%s/_overcast/lambda/functions/%s/invoke-with-progress", ep, url.PathEscape(name)),
		r.Body)
	req.Header.Set("Content-Type", "application/json")
	forwardRegion(req, r)
	// Streaming client: the invocation runs for as long as the function's own
	// timeout allows, and bffHTTPClient's 30 s cap would cut the stream mid-run
	// and leave the UI with no result event.
	resp, err := bffStreamingClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

func handleLambdaTestEventsGet(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/_overcast/lambda/functions/%s/test-events", ep, url.PathEscape(name)), nil)
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleLambdaTestEventPut(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	eventName := chi.URLParam(r, "eventName")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPut,
		fmt.Sprintf("%s/_overcast/lambda/functions/%s/test-events/%s",
			ep, url.PathEscape(name), url.PathEscape(eventName)), r.Body)
	req.Header.Set("Content-Type", "application/json")
	forwardRegion(req, r)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleLambdaTestEventDelete(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	name := chi.URLParam(r, "name")
	eventName := chi.URLParam(r, "eventName")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodDelete,
		fmt.Sprintf("%s/_overcast/lambda/functions/%s/test-events/%s",
			ep, url.PathEscape(name), url.PathEscape(eventName)), nil)
	forwardRegion(req, r)
	// bffHTTPClient, not http.DefaultClient: the shared client carries the
	// TLS trust configuration when the emulator API is served over HTTPS.
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	resp.Body.Close()
	w.WriteHeader(http.StatusNoContent)
}

// ── ECS ────────────────────────────────────────────────────────────────────

func handleECSTaskLogs(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	taskArn := chi.URLParam(r, "taskArn")
	container := chi.URLParam(r, "container")
	resp, err := doGet(r.Context(), fmt.Sprintf("%s/_overcast/ecs/tasks/%s/logs/%s",
		ep, url.PathEscape(taskArn), url.PathEscape(container)))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleECSClusterTasks(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	cluster := chi.URLParam(r, "cluster")
	resp, err := doGet(r.Context(), fmt.Sprintf("%s/_overcast/ecs/clusters/%s/tasks",
		ep, url.PathEscape(cluster)))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// ── CloudFormation ─────────────────────────────────────────────────────────

// handleCFNStackDiagnostics proxies the deploy-diagnostics journal behind the
// console's Diagnostics tab — why the stack's last deploy failed, as gathered
// before the rollback deleted the evidence.
//
// The upstream status is passed through rather than translated, and the 404
// especially: it is the ordinary answer for a stack that has never failed, and
// the console keys the tab's existence on it. Turning it into a 502 or an
// empty 200 would either show an error for a healthy stack or show a tab with
// nothing in it.
//
// The region is forwarded because the journal is region-scoped like every
// other CloudFormation record, so a console pointed at ap-southeast-2 must not
// be answered from us-east-1.
func handleCFNStackDiagnostics(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	stackName := chi.URLParam(r, "stackName")
	resp, err := doGetWithRegion(r.Context(), fmt.Sprintf("%s/_overcast/cloudformation/stacks/%s/diagnostics",
		ep, url.PathEscape(stackName)), r)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// ── EventBridge ────────────────────────────────────────────────────────────

// handleEventBridgeDeliveries proxies the console's per-target delivery
// outcome feed, so the bus view can say why an event did or did not reach a
// target.
func handleEventBridgeDeliveries(w http.ResponseWriter, r *http.Request) {
	proxyConsoleFeed(w, r, "/_overcast/eventbridge/deliveries", "bus", "rule", "limit")
}

// handleEventBridgeRuleTargets proxies each rule's targets with their resolved
// target type.
func handleEventBridgeRuleTargets(w http.ResponseWriter, r *http.Request) {
	proxyConsoleFeed(w, r, "/_overcast/eventbridge/rule-targets", "bus")
}

// ── EventBridge Pipes ──────────────────────────────────────────────────────

// handlePipeWiring proxies each pipe's resolved source, enrichment and target
// types, so the pipes view can tell a running pipe from one that is stored but
// inert.
func handlePipeWiring(w http.ResponseWriter, r *http.Request) {
	proxyConsoleFeed(w, r, "/_overcast/pipes/wiring")
}

// handlePipeDeliveries proxies the console's per-pipe execution feed.
func handlePipeDeliveries(w http.ResponseWriter, r *http.Request) {
	proxyConsoleFeed(w, r, "/_overcast/pipes/deliveries", "pipe", "limit")
}

// proxyConsoleFeed forwards a console GET to the emulator, passing through only
// the named query parameters and the caller's region.
func proxyConsoleFeed(w http.ResponseWriter, r *http.Request, path string, params ...string) {
	ep := resolveEndpoint(r)
	query := url.Values{}
	for _, name := range params {
		if v := r.URL.Query().Get(name); v != "" {
			query.Set(name, v)
		}
	}
	u := ep + path
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	resp, err := doGetWithRegion(r.Context(), u, r)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// ── Preflight ──────────────────────────────────────────────────────────────

// handlePreflightRegion proxies the region advisory a list page asks for when
// it has just rendered nothing: does this kind of resource exist in a region
// other than the one being shown, and how much of it.
//
// The region matters more here than on any other proxy in this file, because
// it is half the comparison rather than a lookup key. proxyConsoleFeed
// forwards it, and the emulator decides whether there is anything to say —
// this hop adds no judgement of its own, so the "only on a matched symptom"
// rule cannot drift between the two.
func handlePreflightRegion(w http.ResponseWriter, r *http.Request) {
	proxyConsoleFeed(w, r, "/_overcast/preflight/region", "kind")
}

// ── Mail ───────────────────────────────────────────────────────────────────

func handleMailList(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	u := ep + "/_overcast/ses/inbox/messages"
	if limit := r.URL.Query().Get("limit"); limit != "" {
		u += "?limit=" + url.QueryEscape(limit)
	}
	resp, err := doGet(r.Context(), u)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "mail fetch failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleMailGet(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	id := chi.URLParam(r, "id")
	resp, err := doGet(r.Context(), fmt.Sprintf("%s/_overcast/ses/inbox/messages/%s", ep, url.PathEscape(id)))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "mail fetch failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

func handleMailDeleteAll(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodDelete,
		ep+"/_overcast/ses/inbox/messages", nil)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "mail clear failed")
		return
	}
	resp.Body.Close()
	w.WriteHeader(http.StatusNoContent)
}

func handleMailDeleteOne(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	id := chi.URLParam(r, "id")
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodDelete,
		fmt.Sprintf("%s/_overcast/ses/inbox/messages/%s", ep, url.PathEscape(id)), nil)
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "mail delete failed")
		return
	}
	resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
}

// ── RDS ────────────────────────────────────────────────────────────────────

func handleRDSLogs(w http.ResponseWriter, r *http.Request) {
	ep := resolveEndpoint(r)
	id := chi.URLParam(r, "id")
	resp, err := doGet(r.Context(), fmt.Sprintf("%s/_overcast/rds/instances/%s/logs", ep, url.PathEscape(id)))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "emulator unreachable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if !copyResponseBody(w, resp.Body) {
		return
	}
}

// ── Docs ───────────────────────────────────────────────────────────────────

var safeServiceName = regexp.MustCompile(`^[a-z0-9_-]+$`)

// docsIndex parses the embedded docs into navigation entries and a search
// index, at most once, on the first request that needs either.
//
// Off the startup path deliberately: parsing every published page and
// inverting it costs on the order of 100ms (BenchmarkBuildIndex in
// internal/docsindex), and a run that never opens the console docs should not
// pay it at boot. A parse failure is kept rather than retried — the input is
// fixed at compile time, so a second attempt cannot differ.
// docsData is everything the docs endpoints serve, in the form they serve it:
// the navigation already encoded, and the search index already inverted.
//
// Both are derived from an immutable input — the docs compiled into this
// binary — so they are computed once and then only read. The navigation is
// held as bytes rather than as []docsindex.Entry because every request would
// otherwise re-project and re-encode the same 128 KB of JSON: measured at
// ~0.4ms of CPU and ~176 KB of garbage per request, for a body that cannot
// change until the binary does.
//
// Keeping the encoded form is also what lets the parsed corpus go. []Doc
// carries every page's raw Markdown, extracted prose and code spans — 2.6 MB
// that nothing reads once the navigation and the postings exist.
type docsData struct {
	navJSON []byte
	search  *docssearch.Index
	err     error
}

type docsIndex struct {
	get func() *docsData
}

// newDocsIndex prepares the docs endpoints without doing any of their work.
//
// Deliberately lazy rather than warmed in a background goroutine: building
// costs ~100ms of CPU, and a process that never opens the console docs — every
// CI run, every scripted use of the emulator — should not spend it. It is off
// the startup path either way, but a background build would still compete for
// CPU with startup on a single-core container. The cost lands instead on the
// first docs request of a process that asked for docs.
func newDocsIndex(docsFS fs.FS) *docsIndex {
	return &docsIndex{get: sync.OnceValue(func() *docsData {
		if docsFS == nil {
			return &docsData{err: errNoDocs}
		}
		docs, err := docsindex.Collect(docsFS)
		if err != nil {
			return &docsData{err: err}
		}
		// An empty corpus is a failure, not an empty page list: a slim build
		// embeds no docs, and answering 200 with [] would render an empty
		// sidebar and a search box over nothing, both of which look like
		// working features.
		if len(docs) == 0 {
			return &docsData{err: errNoDocs}
		}
		navJSON, err := json.Marshal(map[string]any{"entries": docsindex.Entries(docs)})
		if err != nil {
			return &docsData{err: err}
		}
		return &docsData{
			navJSON: navJSON,
			search:  docssearch.NewIndex(docsindex.SearchEntries(docs)),
		}
	})}
}

var errNoDocs = errors.New("no documentation is embedded in this build")

// handleDocsNav serves the docs sidebar and per-page table of contents: one
// entry per published doc, in path order. The console used to import this as a
// 7,000-line generated TypeScript module bundled into the SPA; it fetches it
// now, from the same binary it already fetches every doc body from.
func handleDocsNav(docs *docsIndex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := docs.get()
		if data.err != nil {
			http.Error(w, "docs navigation unavailable ("+data.err.Error()+").",
				http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data.navJSON) //nolint:errcheck // best-effort HTTP write.
	}
}

func handleDocsSearch(docs *docsIndex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		limit := 10
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err == nil && parsed > 0 && parsed <= 50 {
				limit = parsed
			}
		}
		data := docs.get()
		// An index that failed to build must not look like a corpus with no
		// matches: every query would answer "no results" and the console would
		// show a working search box over nothing. Say the index is missing, the
		// way a binary built without an SPA says so instead of 404ing.
		// data.err first, and separately: on the failure path there is no
		// index to ask, so the two cannot be folded into one expression.
		err := data.err
		if err == nil {
			err = data.search.Err()
		}
		if err != nil {
			http.Error(w, "docs search index unavailable ("+err.Error()+"). "+
				"The docs pages themselves are unaffected.", http.StatusServiceUnavailable)
			return
		}
		results := data.search.Search(query, limit)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query":   query,
			"results": results,
		})
	}
}

func handleDocsPage(docsFS fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		if !safeDocsPath(path) {
			writeJSONError(w, http.StatusNotFound, "NotFound")
			return
		}
		serveDocMarkdown(w, docsFS, path)
	}
}

var frontmatterDelim = []byte("---")

// stripFrontmatter removes a leading YAML frontmatter block (delimited by
// "---" lines) from doc content, as added by scripts/docs-index.go. Docs
// served without frontmatter (e.g. service reference pages) are returned
// unchanged. Malformed frontmatter (no closing delimiter) is also returned
// unchanged rather than guessed at, so a bad doc never renders truncated or
// mangled content.
//
// Line endings are handled a line at a time via IndexByte rather than a
// literal "\n---\n" search, so the result doesn't depend on the file's LF
// vs CRLF convention (checkout/editor dependent) and the function never
// allocates beyond the trailing slice it returns.
func stripFrontmatter(content []byte) []byte {
	first, rest, ok := cutLine(content)
	if !ok || !bytes.Equal(bytes.TrimRight(first, "\r"), frontmatterDelim) {
		return content
	}
	for {
		line, next, ok := cutLine(rest)
		if !ok {
			// No closing "---" found — leave content untouched.
			return content
		}
		if bytes.Equal(bytes.TrimRight(line, "\r"), frontmatterDelim) {
			return trimLeadingBlankLine(next)
		}
		rest = next
	}
}

// trimLeadingBlankLine removes a single leading newline (LF or CRLF) — the
// blank-line separator conventionally left between a frontmatter block and
// the doc body — so callers don't see a stray blank line at the top.
func trimLeadingBlankLine(b []byte) []byte {
	if bytes.HasPrefix(b, []byte("\r\n")) {
		return b[2:]
	}
	return bytes.TrimPrefix(b, []byte("\n"))
}

// cutLine splits b at the first '\n', returning the line (without the
// newline) and the remainder. ok is false if b contains no '\n', meaning
// there is no complete line left to consume.
func cutLine(b []byte) (line, rest []byte, ok bool) {
	idx := bytes.IndexByte(b, '\n')
	if idx == -1 {
		return nil, nil, false
	}
	return b[:idx], b[idx+1:], true
}

func safeDocsPath(path string) bool {
	if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return false
	}
	if path == "plans" || strings.HasPrefix(path, "plans/") {
		return false
	}
	if path == "dev" || strings.HasPrefix(path, "dev/") {
		return false
	}
	return strings.HasSuffix(path, ".md")
}

func handleDocs(docsFS fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := chi.URLParam(r, "service")
		if !safeServiceName.MatchString(service) {
			writeJSONError(w, http.StatusNotFound, "NotFound")
			return
		}
		serveDocMarkdown(w, docsFS, "services/"+service+".md")
	}
}

// serveDocMarkdown reads one embedded doc and writes it as markdown with any
// leading YAML frontmatter stripped — the single serving path shared by
// /api/docs/page and /api/docs/{service}, so the two endpoints can never
// diverge on frontmatter handling again (the service endpoint originally
// skipped stripping, and the service docs modal rendered raw frontmatter).
func serveDocMarkdown(w http.ResponseWriter, docsFS fs.FS, path string) {
	content, err := fs.ReadFile(docsFS, path)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "NotFound")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write(stripFrontmatter(content)) //nolint:errcheck // best-effort HTTP write.
}
