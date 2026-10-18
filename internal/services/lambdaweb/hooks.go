package lambdaweb

// hooks.go — how the control plane tells the data plane that state changed.

import "context"

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
