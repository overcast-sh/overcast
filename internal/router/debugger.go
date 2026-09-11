package router

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/debugger"
)

// debuggerDescribers is the router's debugger.Describer: it hands an untagged
// resource to the compute service that owns it, keyed by the service segment
// of the target id. Each compute service registers itself once, where both are
// constructed in New, and nothing here knows what either does with the
// request.
type debuggerDescribers map[debugger.Service]debugger.Describer

// DescribeUntagged implements debugger.Describer.
func (d debuggerDescribers) DescribeUntagged(ctx context.Context, service debugger.Service, resource string) (debugger.Descriptor, bool) {
	describer, ok := d[service]
	if !ok {
		return debugger.Descriptor{}, false
	}
	return describer.DescribeUntagged(ctx, service, resource)
}

// registerDebuggerRoutes mounts the emulator-only debugger endpoints
// (docs/plans/compute-debugger.md § 6): the target list, one target's
// descriptor, and the console's WebSocket bridge onto a target
// (docs/plans/compute-debugger-console.md § 3.1). They live under /_overcast/
// like the rest of the console's API and, unlike /_overcast/debug/*, are not
// gated on OVERCAST_DEBUG: the Debug tab has to be able to say "not enabled"
// on a default configuration, and the list is a cheap read of an in-memory
// registry.
func registerDebuggerRoutes(r chi.Router, manager *debugger.Manager, describers debuggerDescribers) {
	h := debugger.NewHandler(manager, describers)
	r.Get("/_overcast/debugger/targets", h.ListTargets)
	r.Get("/_overcast/debugger/targets/{service}/{resource}", h.GetTarget)
	r.Get("/_overcast/debugger/targets/{service}/{resource}/ws", h.Bridge)
}
