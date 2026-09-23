package appsync

// handler_host_execute.go — Host-based GraphQL routing.
//
// Real AWS serves AppSync GraphQL at
// {apiId}.appsync-api.{region}.amazonaws.com/graphql. The emulator already
// exposes the same execution logic at the path-style /_overcast/appsync/apis/{apiId}/graphql
// (see service.go), so the Host-routed form only needs a path rewrite — no
// store lookups, no ambiguity to resolve (unlike API Gateway's execute-api,
// an AppSync apiId always means exactly one thing: a GraphQL API).
//
// Real AWS also serves realtime WebSocket traffic on a distinct
// appsync-realtime-api host. Overcast colocates both under the same
// /_overcast/appsync/apis/{apiId} prefix (see service.go's /realtime route), so this same
// rewrite transparently also makes host-routed realtime connections work —
// a separate "appsync-realtime-api" label/row is not needed.

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// HostRouteRewrite adapts a Host-routed AppSync request
// ({apiId}.appsync-api.{region}.{base}/graphql) to the emulator's existing
// /_overcast/appsync/apis/{apiId}/graphql path-style route.
func (s *Service) HostRouteRewrite(r *http.Request, m middleware.HostRouteMatch) {
	// The realtime host serves exactly one endpoint. Real AWS puts
	// subscriptions on {apiId}.appsync-realtime-api.{region}.{base}/graphql,
	// and Amplify derives that host by substituting into the GraphQL URL
	// rather than reading dns.REALTIME — so the hostname has to route
	// regardless of the path the client asks for. Overcast serves it at
	// /_overcast/appsync/apis/{apiId}/realtime.
	//
	// Only Path is rewritten: AppSync carries connection auth in the query
	// string (?header=...&payload=...), which must survive untouched.
	if m.Label == middleware.LabelAppSyncRealtimeAPI {
		r.URL.Path = "/_overcast/appsync/apis/" + m.ID + "/realtime"
		r.URL.RawPath = ""
		return
	}

	middleware.PrefixPath(r, "/_overcast/appsync/apis/"+m.ID)
}
