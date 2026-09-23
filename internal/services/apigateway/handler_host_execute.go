package apigateway

// handler_host_execute.go — Host-based invoke routing.
//
// Real AWS exposes both REST (v1) and HTTP (v2) API invoke URLs as
// {apiId}.execute-api.{region}.amazonaws.com, with the request path shape
// differing by API type:
//
//	REST v1:      /{stage}/{resourcePath...}                (stage always required)
//	HTTP v2:      /{resourcePath...}                         ($default stage, no prefix)
//	              /{stage}/{resourcePath...}                 (named, non-default stage)
//
// The Host header alone can't say which of the two an {apiId} names, or (for
// v2) whether the first path segment is a stage name or already part of the
// resource path. middleware.HostDispatch (see Service.HostRouteRewrite)
// therefore does the minimum possible: it rewrites the request onto
// /_overcast/apigateway/execute-api/{apiId}/{region}/*, preserving the client's path
// and method untouched. ExecuteByHost, registered for that route, resolves
// API kind + stage using the exact same store lookups ExecuteRestAPI and
// ExecuteV2API already use for path-style invokes' cross-region fallback,
// then re-enters those handlers unchanged (via a synthetic chi route
// context) so every downstream behaviour — Cognito/JWT authorizers, API key
// enforcement, stage variables, integration dispatch, event publishing —
// runs exactly as it does for path-style invokes.

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// HostRouteRewrite adapts a Host-routed API Gateway invoke request
// ({apiId}.execute-api.{region}.{base}/...) to the emulator's internal
// /_overcast/apigateway/execute-api/{apiId}/{region}/* marker route (see
// ExecuteByHost for why the client's path is passed through verbatim rather
// than being parsed here). AWS matches resources against the still-encoded
// path, so a %2F inside a segment must reach the router intact (#2136).
func (s *Service) HostRouteRewrite(r *http.Request, m middleware.HostRouteMatch) {
	region := m.Region
	if region == "" {
		region = "-"
	}
	middleware.PrefixPath(r, "/_overcast/apigateway/execute-api/"+m.ID+"/"+region)
}

// ExecuteByHost handles /_overcast/apigateway/execute-api/{apiId}/{region}/* — see
// the file doc above.
func (h *Handler) ExecuteByHost(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "apiId")
	region := chi.URLParam(r, "region")
	clientPath := "/" + strings.TrimPrefix(chi.URLParam(r, "*"), "/")

	ctx := r.Context()
	if region != "" && region != "-" {
		ctx = middleware.ContextWithRegion(ctx, region)
		r = r.WithContext(ctx)
	}

	// Try REST (v1) first — in the request's region, then scanning every
	// region, mirroring ExecuteRestAPI's own cross-region fallback.
	if api, aerr := h.store.getRestAPI(r.Context(), apiID); aerr == nil && api != nil {
		h.dispatchHostRestAPI(w, r, apiID, clientPath)
		return
	}
	if foundRegion := h.store.findRestAPIRegion(r.Context(), apiID); foundRegion != "" {
		h.dispatchHostRestAPI(w, r, apiID, clientPath)
		return
	}

	// Then HTTP (v2).
	if api, aerr := h.store.getV2API(r.Context(), apiID); aerr == nil && api != nil {
		h.dispatchHostV2API(w, r, apiID, clientPath)
		return
	}
	if foundRegion := h.store.findV2APIRegion(r.Context(), apiID); foundRegion != "" {
		h.dispatchHostV2API(w, r, apiID, clientPath)
		return
	}

	// Unknown apiId under either partition — same response real AWS gives a
	// custom domain with no matching API: 403 Forbidden.
	writeGatewayError(w, http.StatusForbidden, "Forbidden")
}

// dispatchHostRestAPI resolves the REST v1 stage (always the first path
// segment — v1 has no $default concept) and re-enters ExecuteRestAPI via a
// synthetic chi route context carrying the params it expects.
func (h *Handler) dispatchHostRestAPI(w http.ResponseWriter, r *http.Request, apiID, clientPath string) {
	stage, rest := splitHostInvokePath(clientPath)
	if stage == "" {
		writeGatewayError(w, http.StatusForbidden, "Missing Authentication Token")
		return
	}
	r = withHostRouteParams(r, map[string]string{
		"restApiId": apiID,
		"stageName": stage,
		"*":         strings.TrimPrefix(rest, "/"),
	})
	h.ExecuteRestAPI(w, r)
}

// dispatchHostV2API resolves the HTTP v2 stage. AWS invoke semantics: a
// request path is only stage-prefixed when the first segment names a stage
// that actually exists; otherwise the whole path belongs to the implicit
// $default stage. Re-enters ExecuteV2API via a synthetic chi route context.
func (h *Handler) dispatchHostV2API(w http.ResponseWriter, r *http.Request, apiID, clientPath string) {
	candidateStage, rest := splitHostInvokePath(clientPath)
	stageName := "$default"
	requestPath := clientPath
	if candidateStage != "" {
		if stg, _ := h.store.getV2Stage(r.Context(), apiID, candidateStage); stg != nil {
			stageName = candidateStage
			requestPath = rest
		}
	}
	r = withHostRouteParams(r, map[string]string{
		"apiId":     apiID,
		"stageName": stageName,
		"*":         strings.TrimPrefix(requestPath, "/"),
	})
	h.ExecuteV2API(w, r)
}

// splitHostInvokePath splits a leading-slash path into its first segment
// (candidate stage name) and the remainder (still leading-slash, "/" if
// nothing follows). Returns stage="" for "/" (no segments at all).
func splitHostInvokePath(p string) (stage, rest string) {
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == "" {
		return "", "/"
	}
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx], trimmed[idx:]
	}
	return trimmed, "/"
}

// withHostRouteParams returns a shallow copy of r carrying a fresh chi route
// context populated with params, so a handler written to read chi.URLParam
// off a normally-matched route (ExecuteRestAPI, ExecuteV2API) can be called
// directly from a differently-shaped route without any change to that
// handler.
func withHostRouteParams(r *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
