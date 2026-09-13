package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/boottime"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/trace"
)

// debugEC2Provider is the subset of the EC2 service needed by the debug
// namespace. Defined here to avoid a circular import between router and
// internal/services/ec2.
type debugEC2Provider interface {
	DebugVPCsHandler() http.HandlerFunc
	// NetworkProblems feeds the vpc-network-isolation-stale advisory (see
	// advisories.go): VPCs whose Docker network could not be brought to the
	// isolation their internet-gateway state calls for.
	NetworkProblems() []dataplane.VPCNetworkProblem
}

// debugLambdaProvider is the subset of the Lambda service needed by the
// debug namespace, mirroring debugEC2Provider — including reading
// docker.VolumeOwnershipProblem rather than a lambda-package type, the same
// way debugEC2Provider reads dataplane.VPCNetworkProblem rather than one of
// ec2's own, so this file depends only on the neutral, already-shared docker
// package and never needs to import internal/services/lambda.
type debugLambdaProvider interface {
	// InitVolumeProblems feeds the lambda-init-volume-foreign advisory (see
	// advisories.go): init volumes matching this build's content hash that
	// this instance reused but does not own, per docker.LabelInstance.
	InitVolumeProblems() []docker.VolumeOwnershipProblem

	// RuntimeAPIReachability feeds the lambda-runtime-api-unreachable
	// advisory: which address containers were told to dial for the Runtime
	// API, whether one was ever seen to reach it, and what every candidate
	// did. Read live rather than captured, because it is not settled until
	// the Lambda service's own Docker probe answers — some seconds after the
	// router is built.
	RuntimeAPIReachability() containerendpoint.Listen
}

// DebugStateProvider is implemented by services with data outside the
// generic kv store (a dedicated SQL table, e.g. DynamoDB items or CloudWatch
// Logs events) that still needs to appear as a virtual namespace in
// /_overcast/debug/state and be clearable via /_overcast/reset. See storage-plan.md
// item 2.3 — the raw state debugger only enumerates the generic kv store by
// default, so a dedicated table is invisible (and immune to /_overcast/reset)
// without one of these.
//
// The router holds a slice of these (one per opted-in service) instead of
// hardcoding a single provider — see debugHandlers.
type DebugStateProvider interface {
	// DebugNamespace is the virtual namespace name shown in /_overcast/debug/state's
	// top-level listing (e.g. "dynamodb:items", "logs:events").
	DebugNamespace() string
	// DebugStateKeys returns every key in this virtual namespace (used by
	// the top-level /_overcast/debug/state listing's key list).
	DebugStateKeys(ctx context.Context) ([]string, error)
	// DebugStateValues returns key->raw-value for /_overcast/debug/state/<namespace>.
	DebugStateValues(ctx context.Context) (map[string]string, error)
	// DebugResetState clears all data in this provider's dedicated storage.
	DebugResetState(ctx context.Context) error
}

// debugHandlers registers the /_overcast/debug/* endpoint namespace.
// These are only mounted when cfg.Debug == true — this is instrumentation
// for expensive or leaky machinery (state dumps, request tracing, pprof), not
// where destructive-but-cheap operations belong. Reset lives outside this
// gate; see reset.go.
//
// Equivalent to LocalStack's /_localstack/* endpoints — useful for:
//   - Inspecting what's stored (useful when debugging test failures)
//   - Verifying configuration is what you expect
//   - Capturing traces and profiles
//
// A web UI for these endpoints is planned. For now they return JSON.
func debugHandlers(cfg *config.Config, store state.Store, ec2Svc debugEC2Provider, lambdaSvc debugLambdaProvider, providers []DebugStateProvider, traceBuf *trace.Buffer, dockerStatus func() *docker.Status, dnsRefusals func() []dataplane.Refusal) func(chi.Router) {
	return func(r chi.Router) {
		r.Get("/health", debugHealth(cfg, store))
		r.Get("/config", debugConfig(cfg))
		r.Get("/state", debugState(store, providers))
		r.Get("/state/{namespace}", debugStateNamespace(store, providers))
		r.Get("/metrics", debugMetrics(cfg, store, ec2Svc, lambdaSvc, dockerStatus, dnsRefusals))

		// ---- Request tracing --------------------------------------------------
		r.Get("/trace/{requestId}", debugTraceGet(traceBuf))
		r.Get("/traces", debugTraceList(traceBuf))
		r.Get("/traces/count", debugTraceCount(traceBuf))
		r.Get("/traces/search", debugTraceSearch(traceBuf))

		// ---- Service-specific debug endpoints ---------------------------------
		if ec2Svc != nil {
			r.Get("/ec2/vpcs", ec2Svc.DebugVPCsHandler())
		}

		// pprof endpoints — goroutine, heap, CPU, etc.
		r.HandleFunc("/pprof/", pprof.Index)
		r.HandleFunc("/pprof/cmdline", pprof.Cmdline)
		r.HandleFunc("/pprof/profile", pprof.Profile)
		r.HandleFunc("/pprof/symbol", pprof.Symbol)
		r.HandleFunc("/pprof/trace", pprof.Trace)
		r.Handle("/pprof/goroutine", pprof.Handler("goroutine"))
		r.Handle("/pprof/heap", pprof.Handler("heap"))
		r.Handle("/pprof/allocs", pprof.Handler("allocs"))
		r.Handle("/pprof/block", pprof.Handler("block"))
		r.Handle("/pprof/mutex", pprof.Handler("mutex"))
		r.Handle("/pprof/threadcreate", pprof.Handler("threadcreate"))
	}
}

// ---- Handler implementations -----------------------------------------------

type debugHealthResponse struct {
	Status        string            `json:"status"`
	Timestamp     string            `json:"timestamp"`
	Uptime        string            `json:"uptime"`
	GoVersion     string            `json:"go_version"`
	Services      []string          `json:"services"`
	State         string            `json:"state"`
	ServiceStates map[string]string `json:"serviceStates,omitempty"`
	Persistent    *persistentHealth `json:"persistent,omitempty"`
	Debug         bool              `json:"debug"`
}

var (
	startTime   = processStartTime()
	goStartTime = boottime.GoStart
)

func debugHealth(cfg *config.Config, store state.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svcStates := make(map[string]string, len(cfg.ServiceStates))
		for svc, mode := range cfg.ServiceStates {
			svcStates[svc] = string(mode)
		}
		persistent := persistentHealthSnapshot(store)
		status := "ok"
		if persistent != nil && !persistent.Healthy {
			status = "degraded"
		}
		resp := &debugHealthResponse{
			Status:        status,
			Timestamp:     time.Now().UTC().Format(time.RFC3339),
			Uptime:        time.Since(startTime).Round(time.Second).String(),
			GoVersion:     runtime.Version(),
			Services:      config.AllServices(),
			State:         string(cfg.State),
			ServiceStates: svcStates,
			Persistent:    persistent,
			Debug:         cfg.Debug,
		}
		writeDebugJSON(w, http.StatusOK, resp)
	}
}

type debugConfigResponse struct {
	Host          string            `json:"host"`
	Port          int               `json:"port"`
	Services      []string          `json:"services"`
	State         string            `json:"state"`
	ServiceStates map[string]string `json:"serviceStates,omitempty"`
	DataDir       string            `json:"data_dir"`
	Region        string            `json:"region"`
	AccountID     string            `json:"account_id"`
	LogLevel      string            `json:"log_level"`
	Debug         bool              `json:"debug"`
	TLS           bool              `json:"tls_enabled"`
	// TLS cert/key paths are deliberately omitted from the response.
}

func debugConfig(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svcStates := make(map[string]string, len(cfg.ServiceStates))
		for svc, mode := range cfg.ServiceStates {
			svcStates[svc] = string(mode)
		}
		resp := &debugConfigResponse{
			Host:          cfg.Host,
			Port:          cfg.Port,
			Services:      config.AllServices(),
			State:         string(cfg.State),
			ServiceStates: svcStates,
			DataDir:       cfg.DataDir,
			Region:        cfg.Region,
			AccountID:     cfg.AccountID,
			LogLevel:      cfg.LogLevel,
			Debug:         cfg.Debug,
			TLS:           cfg.TLSEnabled(),
		}
		writeDebugJSON(w, http.StatusOK, resp)
	}
}

const (
	debugStateValuePreviewBytes = 100 * 1024
	debugStateTruncatedSuffix   = "...(truncated)"
)

// debugState (the top-level /_overcast/debug/state namespace listing) intentionally
// stays unpaginated (storage-plan.md item 3.13). It returns namespace -> key
// list only, never values — a bare key list for even a very large namespace
// (sqs:messages, logs:events) is orders of magnitude smaller than the
// per-namespace *values* response debugStateNamespace used to return
// unbounded (the actual multi-MB risk item 3.13 called out). If a namespace
// with an extreme key count ever makes this response itself a problem,
// paginating this endpoint too is a natural follow-up, but there is no
// evidence of that today, so it's left as-is rather than paginated
// speculatively.
func debugState(store state.Store, providers []DebugStateProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// List all namespaces currently present and their keys. Store backends keep
		// namespace/key scans indexed, but this debug endpoint still enumerates all
		// keys so the UI can build a complete hierarchy.
		namespaces, err := store.ListNamespaces(r.Context())
		if err != nil {
			writeDebugJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		result := make(map[string][]string)
		for _, ns := range namespaces {
			keys, err := store.List(r.Context(), ns, "")
			if err != nil || len(keys) == 0 {
				continue
			}
			result[ns] = keys
		}
		for _, p := range providers {
			if p == nil {
				continue
			}
			keys, err := p.DebugStateKeys(r.Context())
			if err != nil {
				writeDebugJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if len(keys) > 0 {
				result[p.DebugNamespace()] = keys
			}
		}
		writeDebugJSON(w, http.StatusOK, result)
	}
}

// debugStateNamespacePage is the paginated response shape for
// GET /_overcast/debug/state/{namespace} (storage-plan.md item 3.13, backend half).
//
// Contract for consumers (e.g. the web debug UI):
//   - Query params: ?after=<key> (exclusive cursor, omit/empty for the first
//     page) and ?limit=<n> (defaults to debugStateDefaultPageLimit, capped at
//     debugStateMaxPageLimit). Neither collides with the pre-existing ?key=
//     single-value param, which still short-circuits to
//     writeDebugStateRawValue and ignores after/limit entirely.
//   - Values is the page's key -> value map, values truncated per the
//     existing truncateDebugStateValue 100KB-preview logic.
//   - NextKey, when non-empty, is the cursor to pass as ?after= to fetch the
//     next page. An empty/absent NextKey means this was the last page.
type debugStateNamespacePage struct {
	Values  map[string]string `json:"values"`
	NextKey string            `json:"nextKey,omitempty"`
}

const (
	// debugStateDefaultPageLimit is used when ?limit= is absent or invalid.
	debugStateDefaultPageLimit = 500
	// debugStateMaxPageLimit caps ?limit= so a caller can't force an
	// arbitrarily large single response even by asking for one.
	debugStateMaxPageLimit = 5000
)

func debugStateNamespace(store state.Store, providers []DebugStateProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ns := chi.URLParam(r, "namespace")
		// Replace URL-encoded colons: "s3:buckets" → "s3:buckets"
		ns = strings.ReplaceAll(ns, "%3A", ":")
		if key := r.URL.Query().Get("key"); key != "" {
			writeDebugStateRawValue(w, r, store, providers, ns, key)
			return
		}
		after := r.URL.Query().Get("after")
		limit := parseDebugStateLimit(r.URL.Query().Get("limit"))

		var page map[string]string
		var nextKey string
		if p := debugProviderForNamespace(providers, ns); p != nil {
			// Virtual (DebugStateProvider-backed) namespaces don't expose a
			// paginated read of their own — DebugStateProvider is also
			// implemented outside this package (e.g. DynamoDB's item
			// backend), so extending that interface is a larger, separate
			// change. Fetch the full map (existing behavior; CloudWatch
			// Logs's provider already caps this internally — see
			// debugScan) and apply the same after/limit windowing in Go, so
			// callers see one consistent paginated contract regardless of
			// which kind of namespace they're reading.
			values, err := p.DebugStateValues(r.Context())
			if err != nil {
				writeDebugJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			page, nextKey = paginateDebugStateValues(values, after, limit)
		} else {
			// A single Scan(...Page) round trip instead of List+per-key Get
			// (storage-plan.md item 3.1 — this endpoint was itself a named
			// example of the N+1 pattern) — and paginated, so a namespace
			// with millions of keys (sqs:messages, logs:events-shaped
			// tables) never has to return them all in one response
			// (storage-plan.md item 3.13).
			pairs, nk, err := store.ScanPage(r.Context(), ns, "", after, limit)
			if err != nil {
				writeDebugJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			page = make(map[string]string, len(pairs))
			for _, kv := range pairs {
				page[kv.Key] = kv.Value
			}
			nextKey = nk
		}
		truncateDebugStateValues(page)
		writeDebugJSON(w, http.StatusOK, debugStateNamespacePage{Values: page, NextKey: nextKey})
	}
}

// parseDebugStateLimit parses the ?limit= query parameter, falling back to
// debugStateDefaultPageLimit for an absent, non-numeric, or non-positive
// value, and capping at debugStateMaxPageLimit.
func parseDebugStateLimit(raw string) int {
	if raw == "" {
		return debugStateDefaultPageLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return debugStateDefaultPageLimit
	}
	if n > debugStateMaxPageLimit {
		return debugStateMaxPageLimit
	}
	return n
}

// paginateDebugStateValues applies the same after/limit windowing ScanPage
// uses to an already-fetched, non-paginated map — the fallback path for
// DebugStateProvider-backed virtual namespaces (see debugStateNamespace).
// after is an exclusive cursor (matching ScanPage's startAfter semantics);
// limit <= 0 means "no limit", also matching ScanPage.
func paginateDebugStateValues(values map[string]string, after string, limit int) (map[string]string, string) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	start := 0
	if after != "" {
		start = sort.SearchStrings(keys, after)
		if start < len(keys) && keys[start] == after {
			start++
		}
	}
	end := len(keys)
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	page := make(map[string]string, end-start)
	for _, k := range keys[start:end] {
		page[k] = values[k]
	}
	nextKey := ""
	if end < len(keys) {
		nextKey = keys[end-1]
	}
	return page, nextKey
}

func writeDebugStateRawValue(w http.ResponseWriter, r *http.Request, store state.Store, providers []DebugStateProvider, namespace, key string) {
	var value string
	var found bool
	if p := debugProviderForNamespace(providers, namespace); p != nil {
		values, err := p.DebugStateValues(r.Context())
		if err != nil {
			writeDebugJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		value, found = values[key]
	} else {
		var err error
		value, found, err = store.Get(r.Context(), namespace, key)
		if err != nil {
			writeDebugJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	if !found {
		writeDebugJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("state key %q not found", key)})
		return
	}
	writeDebugRawValue(w, value)
}

func writeDebugRawValue(w http.ResponseWriter, value string) {
	if json.Valid([]byte(value)) {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(value))
}

func truncateDebugStateValues(values map[string]string) {
	for key, value := range values {
		values[key] = truncateDebugStateValue(value)
	}
}

func truncateDebugStateValue(value string) string {
	if len(value) <= debugStateValuePreviewBytes {
		return value
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err == nil {
		truncated := truncateDebugJSONStrings(decoded)
		if raw, err := json.Marshal(truncated); err == nil {
			return string(raw)
		}
	}
	return value[:debugStateValuePreviewBytes] + debugStateTruncatedSuffix
}

func truncateDebugJSONStrings(value any) any {
	switch v := value.(type) {
	case string:
		if len(v) <= debugStateValuePreviewBytes {
			return v
		}
		return v[:debugStateValuePreviewBytes] + debugStateTruncatedSuffix
	case []any:
		for i, item := range v {
			v[i] = truncateDebugJSONStrings(item)
		}
		return v
	case map[string]any:
		for key, item := range v {
			v[key] = truncateDebugJSONStrings(item)
		}
		return v
	default:
		return value
	}
}

// debugProviderForNamespace returns the provider whose virtual namespace
// matches ns, or nil if none of providers own it.
func debugProviderForNamespace(providers []DebugStateProvider, ns string) DebugStateProvider {
	for _, p := range providers {
		if p != nil && p.DebugNamespace() == ns {
			return p
		}
	}
	return nil
}

// debugMetricsResponse is the JSON body for GET /_overcast/debug/metrics
// (storage-plan.md item 3.6). Stores is one entry per distinct underlying
// store that implements state.DebugMetricsReporter — see
// state.DebugMetricsSnapshot's doc comment for why a *state.NamespacedStore
// yields a list here instead of one merged entry. Every store implementation
// in this codebase implements the interface (if only to report
// state.StoreCounters — see that type's doc comment), so this is empty
// (never null) only for a hypothetical future Store that doesn't.
//
// Advisories is the server-computed diagnostics list the web UI's Metrics &
// Health page renders generically (see advisories.go's computeAdvisories) —
// always present (never null), empty when nothing needs attention.
//
// Query params: ?includeRowCounts=true additionally computes
// NamespaceRowCounts per store — for TierCached namespaces this issues one
// SQL COUNT(*) per namespace, so it is opt-in rather than always computed.
type debugMetricsResponse struct {
	// state.DebugMetrics carries the JSON tags for this wire shape;
	// DebugFlushRecord timestamps are stored UTC and marshal as RFC 3339.
	Stores     []state.DebugMetrics `json:"stores"`
	Advisories []Advisory           `json:"advisories"`
}

func debugMetrics(cfg *config.Config, store state.Store, vpcs debugEC2Provider, lambdaSvc debugLambdaProvider, dockerStatus func() *docker.Status, dnsRefusals func() []dataplane.Refusal) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var networkProblems []dataplane.VPCNetworkProblem
		if vpcs != nil {
			networkProblems = vpcs.NetworkProblems()
		}
		var refusals []dataplane.Refusal
		if dnsRefusals != nil {
			refusals = dnsRefusals()
		}
		var initVolumeProblems []docker.VolumeOwnershipProblem
		var runtimeAPI containerendpoint.Listen
		if lambdaSvc != nil {
			initVolumeProblems = lambdaSvc.InitVolumeProblems()
			runtimeAPI = lambdaSvc.RuntimeAPIReachability()
		}
		opts := state.DebugMetricsOptions{
			IncludeNamespaceRowCounts: r.URL.Query().Get("includeRowCounts") == "true",
		}
		snapshots, _ := state.DebugMetricsSnapshot(r.Context(), store, opts)
		if snapshots == nil {
			snapshots = []state.DebugMetrics{}
		}
		health, hasHealth := state.PersistentHealthSnapshot(store)
		advisories := computeAdvisories(advisoryInput{
			StateBackend:             cfg.State,
			StateSource:              cfg.StateSource,
			SQLiteAvailable:          config.SQLiteSupported(),
			Stores:                   snapshots,
			Health:                   health,
			HasHealth:                hasHealth,
			ExistingDatabase:         config.HasExistingDatabase(cfg.DataDir),
			Networks:                 dockerNetworkStatuses(dockerStatus),
			VPCNetworkProblems:       networkProblems,
			DNSRefusals:              refusals,
			LambdaInitVolumeProblems: initVolumeProblems,
			RuntimeAPI:               runtimeAPI,
			VPCEgress:                dataplane.EgressMode(cfg),
			PlacementEnforced:        dataplane.PlacementEnforced(cfg),
			ControlPlane:             controlPlaneDecision(dockerStatus, cfg.ControlNetwork()),
		})
		if advisories == nil {
			advisories = []Advisory{}
		}
		writeDebugJSON(w, http.StatusOK, debugMetricsResponse{Stores: snapshots, Advisories: advisories})
	}
}

// ---- Helpers ---------------------------------------------------------------

func writeDebugJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ---- Trace handlers ---------------------------------------------------------

func debugTraceGet(buf *trace.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if buf == nil {
			writeDebugJSON(w, http.StatusNotFound, map[string]string{"error": "trace buffer not available"})
			return
		}
		requestID := chi.URLParam(r, "requestId")
		entry, found := buf.Get(requestID)
		if !found {
			writeDebugJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found", "requestId": requestID})
			return
		}
		writeDebugJSON(w, http.StatusOK, entry)
	}
}

func debugTraceList(buf *trace.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if buf == nil {
			writeDebugJSON(w, http.StatusNotFound, map[string]string{"error": "trace buffer not available"})
			return
		}
		// status and method may each be repeated and/or comma-separated —
		// `?status=4xx&status=5xx` and `?status=4xx,5xx` both mean "any error"
		// — so the UI can ask for several classes at once without the list
		// being filtered client-side, which would leave the server-paginated
		// pages sparse.
		q := r.URL.Query()
		filter := trace.ListFilter{
			Service:  q.Get("service"),
			Methods:  trace.SplitFilterValues(q["method"]),
			Path:     q.Get("path"),
			Statuses: trace.SplitFilterValues(q["status"]),
			Search:   q.Get("search"),
			After:    q.Get("after"),
			Before:   q.Get("before"),
			HopsFor:  q.Get("hopsFor"),
			Limit:    parseDebugStateLimit(q.Get("limit")),
		}
		entries, nextCursor := buf.ListSummaries(filter)
		if entries == nil {
			entries = []trace.Summary{}
		}
		writeDebugJSON(w, http.StatusOK, debugTraceListResponse{Traces: entries, NextCursor: nextCursor})
	}
}

// debugTraceListResponse is one page of GET /_overcast/debug/traces. Traces is
// never null (empty when nothing matched); NextCursor is absent on the last
// page, and is what the caller passes back as ?after= for the next one.
type debugTraceListResponse struct {
	Traces     []trace.Summary `json:"traces"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// minDeepSearchQuery is the shortest query the deep scan will run.
//
// A one- or two-character query matches nearly every body in the ring. The
// result is expensive to produce and worthless to read, and it is never what
// anyone meant — it is what a search box looks like halfway through a word.
const minDeepSearchQuery = 3

// debugTraceSearch scans retained traces for a string in their bodies, hop
// errors and log entries — the fields the list's own search deliberately does
// not reach.
//
// One call scans a budget's worth and returns a cursor; the caller decides
// whether to continue. That is what makes an abandoned search cheap: the client
// stops asking, and a call already in flight stops as soon as the connection
// closes, because r.Context() is cancelled and DeepSearch checks it between
// hops rather than between traces.
func debugTraceSearch(buf *trace.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if buf == nil {
			writeDebugJSON(w, http.StatusNotFound, map[string]string{"error": "trace buffer not available"})
			return
		}
		q := r.URL.Query()
		query := strings.TrimSpace(q.Get("q"))
		if len([]rune(query)) < minDeepSearchQuery {
			// Said rather than silently ignored: a caller that gets an empty
			// result for a two-character query has no way to tell "nothing
			// matched" from "we did not look".
			writeDebugJSON(w, http.StatusBadRequest, map[string]any{
				"error":     "query too short",
				"minLength": minDeepSearchQuery,
			})
			return
		}
		result := buf.DeepSearch(r.Context(), trace.DeepFilter{
			Query:           query,
			Cursor:          q.Get("cursor"),
			IncludeInternal: q.Get("internal") == "true",
		})
		writeDebugJSON(w, http.StatusOK, result)
	}
}

func debugTraceCount(buf *trace.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if buf == nil {
			writeDebugJSON(w, http.StatusNotFound, map[string]string{"error": "trace buffer not available"})
			return
		}
		// The full retention picture, not just the occupancy: a list that
		// simply stops is indistinguishable from a bug, so the console needs
		// to be able to say what was reclaimed and under which rule.
		// `count` and `capacity` keep their original meanings for anything
		// already reading them.
		writeDebugJSON(w, http.StatusOK, buf.Stats())
	}
}

// controlPlaneDecision reads what this run resolved the control plane's
// isolation to out of the Docker status snapshot.
//
// The decision rather than the network record: see advisoryInput.ControlPlane.
// An absent decision is the zero value, which every rule reads as "nothing was
// established", not as "nothing is wrong".
func controlPlaneDecision(dockerStatus func() *docker.Status, name string) docker.NetworkDecision {
	if dockerStatus == nil || name == "" {
		return docker.NetworkDecision{}
	}
	s := dockerStatus()
	if s == nil {
		return docker.NetworkDecision{}
	}
	for _, d := range s.Decisions {
		if d.Network == name {
			return d
		}
	}
	return docker.NetworkDecision{}
}

// dockerNetworkStatuses reads the network section out of the Docker status
// snapshot, tolerating every way it can be absent — no Docker services wired, a
// probe that never completed, a build with no supervisor at all. An absent
// section is "nothing to say", not "nothing is wrong": see advisoryInput.Networks.
func dockerNetworkStatuses(dockerStatus func() *docker.Status) []docker.NetworkStatus {
	if dockerStatus == nil {
		return nil
	}
	s := dockerStatus()
	if s == nil {
		return nil
	}
	return s.Networks
}
