package lambdaweb

// hooks.go — how the control plane tells the data plane that state changed.

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// InitChangeHook wires the function the control plane calls after every
// successful write to a web function's revisions or endpoints, so the data
// plane can rebuild its routing table from the store. region and function name
// the web function that changed. It is nil-safe: with no hook wired, writes
// simply take effect.
//
// The hook runs with the service's write lock held, so it must not call back
// into the Service's write path (markRevisionFailed included); reading the
// store is fine. It must return promptly; an endpoint delete waits on it for
// the data plane to drain.
func (s *Service) InitChangeHook(fn func(region, function string)) {
	if fn == nil {
		s.changed = nil
		return
	}
	s.changed = func(region, function string) error {
		fn(region, function)
		return nil
	}
}

// InitChangeHookErr is InitChangeHook for a data plane that can refuse a
// change: an error moves the endpoint being updated to a Failed update status
// (updateStatusReason carries the error) instead of Successful.
func (s *Service) InitChangeHookErr(fn func(region, function string) error) { s.changed = fn }

// onChange calls the change hook, if any.
func (s *Service) onChange(_ context.Context, region, function string) error {
	if s.changed == nil {
		return nil
	}
	return s.changed(region, function)
}

// DebugNamespace names the service's runtime state in /_overcast/debug/state.
// It makes the service a router.DebugStateProvider, which is how a reset
// reaches it.
func (s *Service) DebugNamespace() string { return "lambda-web:runtime" }

// DebugStateKeys lists the endpoints with environments, as {function}/{endpoint}.
func (s *Service) DebugStateKeys(context.Context) ([]string, error) {
	s.dp.rtMu.Lock()
	defer s.dp.rtMu.Unlock()
	var keys []string
	for _, rt := range s.dp.rts {
		if rt.envs.Load() > 0 {
			keys = append(keys, rt.function+"/"+rt.endpoint)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// DebugStateValues reports the live environment count per endpoint.
func (s *Service) DebugStateValues(ctx context.Context) (map[string]string, error) {
	keys, _ := s.DebugStateKeys(ctx)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		fn, ep, _ := strings.Cut(k, "/")
		out[k] = strconv.Itoa(s.liveEnvironments(fn, ep)) + " environments"
	}
	return out, nil
}

// DebugResetState settles the data plane after the store was wiped: the route
// table is rebuilt from what is left, which retires and drains every
// environment of an endpoint that no longer exists.
func (s *Service) DebugResetState(ctx context.Context) error {
	s.invalidateRoutes()
	_, err := s.routes(ctx)
	return err
}
