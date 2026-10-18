package lambdaweb

// lifecycle.go — revisions and endpoints become real: a revision is Pending
// while a worker fetches and checks its package, then Active or Failed; an
// endpoint is Pending until it is registered with the data plane, reports
// updateStatus InProgress while a change is applied, and is Deleting until its
// traffic has drained. See docs/plans/lambda-web-data-plane.md, "Revisions and
// endpoints become real".

import (
	"context"
	"slices"
	"sync"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// Update statuses beyond updateSuccessful (types.go): EndpointUpdateStatus.
const (
	updateInProgress = "InProgress"
	updateFailed     = "Failed"
)

// revisionWorkers bounds how many packages are fetched and checked at once.
const revisionWorkers = 4

// lifecycleState is the Service's lifecycle machinery. All of it is lazy: New
// stays pure assignment, and no goroutine exists until a revision needs one.
type lifecycleState struct {
	// changed is the data plane's change hook (hooks.go); nil until wired.
	changed func(region, function string) error

	wmu     sync.Mutex
	started bool
	stopped bool
	queue   chan revKey
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	resumeOnce sync.Once
}

// revKey names one revision awaiting its package check.
type revKey struct{ region, function, id string }

// ─── State accessors ─────────────────────────────────────────────────────────

// revState is a revision's state; a record stored before the data plane has
// none and reads as Active.
func revState(rev *revision) string {
	if rev.State == "" {
		return stateActive
	}
	return rev.State
}

// epState is an endpoint's state; a record without one reads as Active.
func epState(ep *endpoint) string {
	if ep.State == "" {
		return stateActive
	}
	return ep.State
}

// epUpdateStatus is an endpoint's updateStatus. A record without one reads as
// Successful, and an endpoint that is not Active has none yet.
func epUpdateStatus(ep *endpoint) string {
	if ep.UpdateStatus != "" {
		return ep.UpdateStatus
	}
	if epState(ep) == stateActive {
		return updateSuccessful
	}
	return ""
}

func codeRefOf(rev *revision) codeRef {
	o := rev.BuildConfig.CodeConfig.S3Object
	return codeRef{Bucket: o.Bucket, Key: o.Key, VersionID: o.VersionID}
}

// ─── Starting a revision ─────────────────────────────────────────────────────

// pinRevision resolves the revision's S3 object when a code source is wired and
// stores the pinned version id in the revision. Without a code source there is
// nothing to read (a control-plane-only service) and the revision is left as
// given.
func (s *Service) pinRevision(ctx context.Context, rev *revision) *protocol.AWSError {
	if s.code == nil {
		return nil
	}
	pinned, _, aerr := s.code.Resolve(ctx, codeRefOf(rev))
	if aerr != nil {
		return aerr
	}
	rev.BuildConfig.CodeConfig.S3Object.VersionID = pinned.VersionID
	return nil
}

// startRevision sends a stored Pending revision on its way: to the worker when
// there is a package to check, or straight to Active when there is no code
// source to check it with. The caller holds the service lock.
func (s *Service) startRevision(ctx context.Context, fn *webFunction, rev *revision) *protocol.AWSError {
	if s.code == nil {
		return s.activateRevision(ctx, fn, rev)
	}
	s.enqueueRevision(revKey{fn.Region, fn.Name, rev.ID})
	return nil
}

// activateRevision makes a Pending revision Active, announces it, points
// LatestRevision endpoints at it and cleans up old revisions. The caller holds
// the service lock.
func (s *Service) activateRevision(ctx context.Context, fn *webFunction, rev *revision) *protocol.AWSError {
	rev.State, rev.StateReason, rev.Errors = stateActive, "", nil
	if aerr := putRecord(ctx, s, nsRevisions, childKey(fn.Region, fn.Name, rev.ID), rev); aerr != nil {
		return aerr
	}
	s.publish(ctx, events.LambdaWebRevisionCreated, fn, rev.ID, "")
	if aerr := s.routeLatest(ctx, fn); aerr != nil {
		return aerr
	}
	return s.cleanupRevisions(ctx, fn, rev.ID)
}

// failRevision makes a revision Failed. Failed is permanent. The change hook
// runs so the data plane starts answering 409 RevisionNotActive for it. The
// caller holds the service lock.
func (s *Service) failRevision(ctx context.Context, fn *webFunction, rev *revision, code, msg string) *protocol.AWSError {
	rev.State, rev.StateReason = stateFailed, msg
	rev.Errors = []revisionError{{Attribute: codeAttrS3Object, ErrorCode: code, ErrorMessage: msg}}
	if aerr := putRecord(ctx, s, nsRevisions, childKey(fn.Region, fn.Name, rev.ID), rev); aerr != nil {
		return aerr
	}
	if err := s.onChange(ctx, fn.Region, fn.Name); err != nil {
		s.log.Warn("lambda-web: change hook failed after a revision failed", zap.String("function", fn.Name), zap.Error(err))
	}
	return nil
}

// markRevisionFailed is how the data plane reports that an environment could
// not fetch a revision's code (the S3 object or the bucket's access is gone).
// An Active or Pending revision becomes Failed with code and msg in errors[];
// one already Failed is left alone. It takes the service lock, so it must not
// be called from the change hook.
func (s *Service) markRevisionFailed(ctx context.Context, region, function, id, code, msg string) {
	defer s.lock()()
	rev, found, aerr := s.loadRevision(ctx, region, function, id)
	if aerr != nil || !found || revState(rev) == stateFailed {
		return
	}
	fn, found, aerr := s.loadFunction(ctx, region, function)
	if aerr != nil || !found {
		return
	}
	if aerr := s.failRevision(ctx, fn, rev, code, msg); aerr != nil {
		s.log.Error("lambda-web: could not record a failed revision", zap.String("function", function), zap.String("revision", id), zap.String("error", aerr.Message))
	}
}

// ─── The worker ──────────────────────────────────────────────────────────────

// enqueueRevision hands a Pending revision to the worker pool, starting it on
// first use. After Stop nothing is queued: the revision stays Pending and the
// next start resumes it.
func (s *Service) enqueueRevision(k revKey) {
	lc := &s.lifecycleState
	lc.wmu.Lock()
	defer lc.wmu.Unlock()
	if lc.stopped {
		return
	}
	if !lc.started {
		lc.started = true
		lc.queue = make(chan revKey, 256)
		lc.ctx, lc.cancel = context.WithCancel(context.Background())
		for range revisionWorkers {
			lc.wg.Add(1)
			go s.revisionWorker(lc.ctx, lc.queue)
		}
	}
	select {
	case lc.queue <- k:
	default:
		// The buffer is full: wait for room off the caller's path, so a burst
		// never blocks a request (and never drops a revision).
		lc.wg.Add(1)
		go func() {
			defer lc.wg.Done()
			select {
			case lc.queue <- k:
			case <-lc.ctx.Done():
			}
		}()
	}
}

func (s *Service) revisionWorker(ctx context.Context, queue <-chan revKey) {
	defer s.lifecycleState.wg.Done()
	for {
		select {
		case k := <-queue:
			s.processRevision(ctx, k)
		case <-ctx.Done():
			return
		}
	}
}

// processRevision fetches and checks one Pending revision's package and moves
// it to Active or Failed. The fetch runs without the service lock.
func (s *Service) processRevision(ctx context.Context, k revKey) {
	rev, found, aerr := s.loadRevision(ctx, k.region, k.function, k.id)
	if aerr != nil || !found || revState(rev) != statePending {
		return
	}
	var perr *packageError
	if s.code != nil {
		body, aerr := s.code.Fetch(ctx, codeRefOf(rev))
		switch {
		case ctx.Err() != nil:
			return // stopping: the revision stays Pending and is resumed next start
		case aerr != nil:
			perr = &packageError{codeCodeUnavailable, reasonCodeUnavailRsn + aerr.Message}
		default:
			if perr = validatePackage(body); perr != nil {
				perr.Message = reasonPackagePrefix + perr.Message
			}
		}
	}

	defer s.lock()()
	// The revision may have been deleted, or failed, while its package was read.
	rev, found, aerr = s.loadRevision(ctx, k.region, k.function, k.id)
	if aerr != nil || !found || revState(rev) != statePending {
		return
	}
	fn, found, aerr := s.loadFunction(ctx, k.region, k.function)
	if aerr != nil || !found {
		return
	}
	if perr != nil {
		aerr = s.failRevision(ctx, fn, rev, perr.Code, perr.Message)
	} else {
		aerr = s.activateRevision(ctx, fn, rev)
	}
	if aerr != nil {
		s.log.Error("lambda-web: could not finish preparing a revision", zap.String("function", k.function), zap.String("revision", k.id), zap.String("error", aerr.Message))
	}
}

// Resume queues every Pending revision in the store. The engine calls it at
// start; it also runs by itself on the first use of the service, so a restart
// finishes the revisions it interrupted either way.
func (s *Service) Resume(ctx context.Context) {
	revs, aerr := scanRecords[revision](ctx, s, nsRevisions, "")
	if aerr != nil {
		s.log.Error("lambda-web: could not scan for pending revisions", zap.String("error", aerr.Message))
		return
	}
	for _, rev := range revs {
		if revState(rev) == statePending {
			s.enqueueRevision(revKey{rev.Region, rev.Function, rev.ID})
		}
	}
}

// ensureResumed runs Resume once, on the first use of the service.
func (s *Service) ensureResumed() {
	s.resumeOnce.Do(func() { s.Resume(context.Background()) })
}

// stopLifecycle stops the revision workers and waits for them until ctx ends.
// Revisions still Pending stay so and are resumed on the next start. The engine
// calls it from its own Stop (router.Stopper).
func (s *Service) stopLifecycle(ctx context.Context) {
	lc := &s.lifecycleState
	lc.wmu.Lock()
	lc.stopped = true
	if lc.cancel != nil {
		lc.cancel()
	}
	lc.wmu.Unlock()
	done := make(chan struct{})
	go func() {
		lc.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// ─── Routing ─────────────────────────────────────────────────────────────────

// routeLatest points every LatestRevision endpoint of fn at the newest Active
// revision, applying each change as an endpoint update. The change hook runs
// once whether or not an endpoint changed: a revision becoming Active changes
// what the data plane may serve. The caller holds the service lock.
func (s *Service) routeLatest(ctx context.Context, fn *webFunction) *protocol.AWSError {
	revs, aerr := s.listRevisions(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return aerr
	}
	eps, aerr := s.listEndpoints(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return aerr
	}
	var changed []*endpoint
	for _, ep := range eps {
		if ep.AutoDeploy != deployLatestRevision || epState(ep) == stateDeleting {
			continue
		}
		want := latestWeights(revs, ep.Type)
		if want == nil || slices.Equal(ep.Weights, want) {
			continue
		}
		ep.Weights = want
		changed = append(changed, ep)
	}
	return s.commitEndpoints(ctx, fn, changed)
}

// cleanupRevisions deletes the oldest settled revisions that no endpoint routes
// to once a function holds more than 50, as AWS does when a new revision becomes
// Active. Pending revisions are neither counted nor deleted, and the revision
// that just became Active is never deleted.
func (s *Service) cleanupRevisions(ctx context.Context, fn *webFunction, keep string) *protocol.AWSError {
	revs, aerr := s.listRevisions(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return aerr
	}
	settled := slices.DeleteFunc(slices.Clone(revs), func(r *revision) bool { return revState(r) == statePending })
	if len(settled) <= maxRevisionsPerFn {
		return nil
	}
	eps, aerr := s.listEndpoints(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return aerr
	}
	excess := len(settled) - maxRevisionsPerFn
	for _, rev := range settled {
		if excess == 0 {
			break
		}
		if rev.ID == keep || slices.ContainsFunc(eps, func(ep *endpoint) bool { return routesTo(ep, rev.ID) }) {
			continue
		}
		if aerr := deleteRecord(ctx, s, nsRevisions, childKey(fn.Region, fn.Name, rev.ID)); aerr != nil {
			return aerr
		}
		s.publish(ctx, events.LambdaWebRevisionDeleted, fn, rev.ID, "")
		excess--
	}
	return nil
}

// ─── Endpoint lifecycle ──────────────────────────────────────────────────────

// registerEndpoint completes a created endpoint: once the data plane has taken
// it (the change hook ran), it is Active. A refusal makes it Failed. The caller
// holds the service lock.
func (s *Service) registerEndpoint(ctx context.Context, fn *webFunction, ep *endpoint) *protocol.AWSError {
	if err := s.onChange(ctx, fn.Region, fn.Name); err != nil {
		ep.State, ep.StateReason = stateFailed, "The endpoint could not be registered: "+err.Error()
	} else {
		ep.State, ep.StateReason, ep.UpdateStatus, ep.UpdateStatusReason = stateActive, "", updateSuccessful, ""
	}
	ep.UpdatedAt = s.now()
	return putRecord(ctx, s, nsEndpoints, childKey(fn.Region, fn.Name, ep.Name), ep)
}

// beginEndpointUpdate persists eps (already modified) as updateStatus
// InProgress. The caller holds the service lock.
func (s *Service) beginEndpointUpdate(ctx context.Context, fn *webFunction, eps []*endpoint) *protocol.AWSError {
	for _, ep := range eps {
		ep.UpdateStatus, ep.UpdateStatusReason, ep.UpdatedAt = updateInProgress, "", s.now()
		if aerr := putRecord(ctx, s, nsEndpoints, childKey(fn.Region, fn.Name, ep.Name), ep); aerr != nil {
			return aerr
		}
	}
	return nil
}

// finishEndpointUpdate settles eps after the change hook ran: Successful, or
// Failed with the hook's error as updateStatusReason. The caller holds the
// service lock.
func (s *Service) finishEndpointUpdate(ctx context.Context, fn *webFunction, eps []*endpoint, hookErr error) *protocol.AWSError {
	for _, ep := range eps {
		if hookErr != nil {
			ep.UpdateStatus, ep.UpdateStatusReason = updateFailed, "The update could not be applied: "+hookErr.Error()
		} else {
			ep.UpdateStatus, ep.UpdateStatusReason = updateSuccessful, ""
		}
		ep.UpdatedAt = s.now()
		if aerr := putRecord(ctx, s, nsEndpoints, childKey(fn.Region, fn.Name, ep.Name), ep); aerr != nil {
			return aerr
		}
		s.publish(ctx, events.LambdaWebEndpointUpdated, fn, "", ep.Name)
	}
	return nil
}

// commitEndpoints applies eps' pending changes as one endpoint update each:
// InProgress, the change hook, then Successful (or Failed). With no endpoints
// it only runs the hook. The caller holds the service lock.
func (s *Service) commitEndpoints(ctx context.Context, fn *webFunction, eps []*endpoint) *protocol.AWSError {
	if aerr := s.beginEndpointUpdate(ctx, fn, eps); aerr != nil {
		return aerr
	}
	return s.finishEndpointUpdate(ctx, fn, eps, s.onChange(ctx, fn.Region, fn.Name))
}
