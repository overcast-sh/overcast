package cloudformation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/trace"
)

// provisioner creates/updates/deletes resources asynchronously via the router.
// It dispatches internal HTTP requests to the emulator's own router so that
// each resource is created through its service's handler — no direct coupling.
type provisioner struct {
	cfg    *config.Config
	store  *cfnStore
	clk    clock.Clock
	log    *serviceutil.ServiceLogger
	bus    *events.Bus
	router http.Handler // the main emulator router

	mu     sync.Mutex
	wg     sync.WaitGroup
	cancel context.CancelFunc
	ctx    context.Context
}

type stackCompletionFunc func(ctx context.Context, stack *Stack)

func newProvisioner(cfg *config.Config, store *cfnStore, clk clock.Clock, log *serviceutil.ServiceLogger) *provisioner {
	ctx, cancel := context.WithCancel(context.Background())
	return &provisioner{
		cfg:    cfg,
		store:  store,
		clk:    clk,
		log:    log,
		ctx:    ctx,
		cancel: cancel,
	}
}

// initRouter sets the HTTP handler used for internal dispatch. Called after
// the router is fully constructed to avoid circular dependencies.
func (p *provisioner) initRouter(router http.Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.router = router
}

// initBus sets the event bus after construction.
func (p *provisioner) initBus(bus *events.Bus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bus = bus
}

// stop cancels all in-flight provisioning and waits for goroutines to drain.
// It honours the deadline on ctx so that a stuck goroutine cannot block
// shutdown indefinitely.
func (p *provisioner) stop(ctx context.Context) {
	p.cancel()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// regionCtx returns p.ctx enriched with the stack's region so that
// region-scoped store operations resolve the correct namespace.
func (p *provisioner) regionCtx(region string) context.Context {
	if region == "" {
		region = p.cfg.Region
	}
	return middleware.ContextWithRegion(p.ctx, region)
}

// operationCtx is regionCtx plus the token of the stack operation about to
// run, which a failed dispatch names in the resource's status reason — see
// withOperationToken.
//
// Every stack operation starts from here: createStack, updateStack,
// deleteStack, rollbackStack and continueUpdateRollback all build their context
// through provisionAsync, and the two synchronous wrappers below take the same
// route. A nested stack is deliberately not one of them — it provisions under
// its parent's context and so records the parent's token, which is the same
// token its own events already carry (see resolveContext.ClientRequestToken).
func (p *provisioner) operationCtx(stack *Stack) context.Context {
	return withOperationToken(p.regionCtx(stack.Region), stack.ClientRequestToken)
}

// ── Asynchronous provisioning ──────────────────────────────────────────────

// provisionAsync runs one stack operation on a provisioning goroutine and
// waits briefly for it, so a fast stack is already terminal by the time an SDK
// waiter issues its first DescribeStacks.
//
// It is the only place an async provisioning context is built, and rec is a
// required parameter for that reason: every internal call the operation makes
// records a trace hop against rec, and a helper that let a caller omit it is
// exactly how DeleteStack and RollbackStack came to record no hops at all.
// Pass the originating request's recorder — trace.RecorderFromContext returns
// nil when debug tracing is off, and nil is the correct value to pass then.
//
// The recorder outlives the HTTP request that created it, which is the point:
// provisioning continues after the caller has been answered, and the hops it
// records still belong to the request that asked for the work.
func (p *provisioner) provisionAsync(stack *Stack, rec *trace.Recorder, run func(ctx context.Context)) {
	ctx := p.operationCtx(stack)
	if rec != nil {
		ctx = trace.ContextWithRecorder(ctx, rec)
	}
	done := make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(done)
		run(ctx)
	}()
	p.awaitBriefly(done)
}

// ── Create stack (async) ───────────────────────────────────────────────────

// createStack provisions all resources in a template asynchronously, but waits
// briefly for fast stacks so SDK waiters can observe the terminal status on
// their immediate first DescribeStacks call.
func (p *provisioner) createStack(stack *Stack, tmpl *Template, onComplete stackCompletionFunc, rec *trace.Recorder) {
	p.provisionAsync(stack, rec, func(ctx context.Context) {
		p.provisionStackResourcesCtx(ctx, stack, tmpl)
		if onComplete != nil {
			onComplete(ctx, stack)
		}
	})
}

func (p *provisioner) awaitBriefly(done <-chan struct{}) {
	if p == nil || p.cfg == nil || p.cfg.CFNSyncWait <= 0 {
		return
	}
	budget := p.cfg.CFNSyncWait
	if budget <= 0 {
		return
	}
	select {
	case <-done:
	case <-p.clk.After(budget):
	}
}

func changeSetExecutionStatus(stackStatus string) string {
	switch stackStatus {
	case StatusCreateComplete, StatusUpdateComplete:
		return ExecStatusExecuteComplete
	case StatusCreateFailed, StatusUpdateFailed, StatusRollbackComplete, StatusRollbackFailed,
		StatusUpdateRollbackComplete, StatusUpdateRollbackFailed:
		return ExecStatusExecuteFailed
	default:
		return ExecStatusExecuteInProgress
	}
}

func (p *provisioner) completeChangeSet(cs *ChangeSet) stackCompletionFunc {
	if cs == nil {
		return nil
	}
	return func(ctx context.Context, stack *Stack) {
		log := p.log.WithRecorder(ctx)
		status := changeSetExecutionStatus(stack.Status)
		if status == ExecStatusExecuteInProgress {
			return
		}
		cs.ExecutionStatus = status
		if err := p.store.putChangeSet(ctx, cs); err != nil {
			log.Warn("cfn: failed to persist changeset execution status",
				zap.String("changeSet", cs.ChangeSetName),
				zap.String("status", status),
				zap.Error(err))
		}
	}
}

// provisionStackResources is the synchronous core of stack provisioning.
// It builds the resolve context, provisions each resource in dependency order,
// resolves outputs, and sets the final stack status. Both top-level createStack
// (async) and nestedStackHandler (inline) use this method.
func (p *provisioner) provisionStackResources(stack *Stack, tmpl *Template) {
	p.provisionStackResourcesCtx(p.operationCtx(stack), stack, tmpl)
}

func (p *provisioner) provisionStackResourcesCtx(ctx context.Context, stack *Stack, tmpl *Template) {
	log := p.log.WithRecorder(ctx)

	// A create builds the stack from nothing, so it owns the whole resource
	// list and output set rather than adding to what is there. Only CreateStack
	// gets that for free, by constructing a fresh record; ExecuteChangeSet
	// provisions a CREATE change set over the stored one, which for a stack
	// being deployed a second time still describes the attempt that failed. Left
	// in place, those records are appended to rather than replaced — so the
	// stack ends up owning two entries per logical ID, and DescribeStackResources
	// answers with the older one, reporting the previous run's failure reason
	// over the reason this run actually failed for.
	stack.Resources = nil
	stack.Outputs = nil

	rCtx := p.buildResolveContext(stack, tmpl)

	// Emit the initial stack CREATE_IN_PROGRESS event (the handler already
	// set this status; we record the event so DescribeStackEvents has history).
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusCreateInProgress, "User Initiated")

	// Determine resource ordering (respecting DependsOn).
	order, err := topoSort(tmpl.Resources)
	if err != nil {
		p.failStack(ctx, stack, StatusCreateFailed, fmt.Sprintf("dependency cycle: %v", err))
		return
	}

	// Provision each resource in order.
	stackStart := p.clk.Now()
	for _, logicalID := range order {
		if ctx.Err() != nil {
			p.failStack(ctx, stack, StatusCreateFailed, "cancelled")
			return
		}
		res := tmpl.Resources[logicalID]

		// Emit CREATE_IN_PROGRESS before attempting provisioning.
		p.recordEvent(ctx, stack, logicalID, "", res.Type, ResourceCreateInProgress, "")

		// An unresolvable dynamic reference fails the resource. Provisioning it
		// anyway would persist the literal "{{resolve:...}}" text as the
		// property's value, which every service downstream then treats as data.
		props, recordedProps, provErr := p.resolveProperties(res, rCtx)
		propsHash := hashResourceProperties(res.Type, recordedProps, stack.Tags)
		resStart := p.clk.Now()
		var physID string
		if provErr == nil {
			physID, provErr = p.provisionResource(ctx, logicalID, res, props, rCtx)
		}
		resElapsed := p.clk.Since(resStart)
		now := p.clk.Now()
		if provErr != nil {
			// Record failed resource state and emit CREATE_FAILED with reason.
			// physID is empty for a create that never got as far as naming the
			// resource, and set for one that failed to stabilize — which leaves
			// a real resource behind for rollback to delete.
			stack.Resources = append(stack.Resources, StackResource{
				LogicalID:           logicalID,
				PhysicalID:          physID,
				Type:                res.Type,
				Status:              ResourceCreateFailed,
				StatusReason:        provErr.Error(),
				Timestamp:           now,
				PropertiesHash:      propsHash,
				Properties:          recordedProps,
				DeletionPolicy:      res.DeletionPolicy,
				UpdateReplacePolicy: res.UpdateReplacePolicy,
			})
			p.recordEvent(ctx, stack, logicalID, physID, res.Type, ResourceCreateFailed, provErr.Error())

			// Read the evidence now, while the failed resource's service-side
			// records still exist — the rollback below is about to delete
			// them, and after it there is nothing left to ask. The write is
			// deferred so the entry records the status the stack finishes at
			// rather than the in-progress one it has here. Both branches
			// return, so this runs at the end of whichever one is taken —
			// including DisableRollback, which deletes nothing but should
			// still leave the tab with something to show.
			diagnosis := p.collectDeployDiagnostics(ctx, stack, deployOperationCreate, stack.Resources)
			defer p.recordDeployDiagnostics(ctx, stack, diagnosis)

			if stack.DisableRollback {
				// DisableRollback: leave partial stack, status CREATE_FAILED.
				p.failStack(ctx, stack, StatusCreateFailed, createFailureSummary(logicalID))
				return
			}

			// Default behaviour: roll back already-created resources, then set
			// status to ROLLBACK_COMPLETE (matching real AWS CloudFormation).
			// The operation's RetainExceptOnCreate rides along: it is what
			// decides whether a resource this create made and marked Retain is
			// deleted here or left behind for the next deploy to collide with.
			p.rollbackCreate(ctx, stack, rCtx,
				fmt.Sprintf("resource %s failed: %v", logicalID, provErr), createRollbackOptions{
					retainExceptOnCreate: stack.RetainExceptOnCreate,
				})
			return
		}

		// Record successful resource state and emit CREATE_COMPLETE.
		stack.Resources = append(stack.Resources, StackResource{
			LogicalID:           logicalID,
			PhysicalID:          physID,
			Type:                res.Type,
			Status:              ResourceCreateComplete,
			StatusReason:        rCtx.EmulationLimitation,
			Timestamp:           now,
			Attributes:          rCtx.Attributes[logicalID],
			PropertiesHash:      propsHash,
			Properties:          recordedProps,
			DeletionPolicy:      res.DeletionPolicy,
			UpdateReplacePolicy: res.UpdateReplacePolicy,
		})
		rCtx.Resources[logicalID] = physID
		// The resource succeeded; the reason, when there is one, says what
		// Overcast will not do with it. It rides the CREATE_COMPLETE event so a
		// deploy shows it as the resource goes by, not only on a later describe.
		p.recordEvent(ctx, stack, logicalID, physID, res.Type, ResourceCreateComplete, rCtx.EmulationLimitation)
		p.publishResourceEvent(ctx, events.CFNResourceProvisioned, stack.StackName, logicalID, res.Type, physID)
		log.Debug("cfn: resource provisioned",
			zap.String("stack", stack.StackName),
			zap.String("logicalId", logicalID),
			zap.String("type", res.Type),
			zap.Duration("elapsed", resElapsed))

	}

	// Resolve outputs. An output whose Fn::GetStackOutput cannot be resolved
	// fails the operation the way a resource would have: AWS validates the
	// reference during the operation, and an output that quietly read as
	// empty would be found by whatever consumes it next, not here.
	outputs, outErr := p.resolveOutputs(tmpl, rCtx)
	if outErr != nil {
		if stack.DisableRollback {
			p.failStack(ctx, stack, StatusCreateFailed, outErr.Error())
			return
		}
		p.rollbackCreate(ctx, stack, rCtx, outErr.Error(), createRollbackOptions{
			retainExceptOnCreate: stack.RetainExceptOnCreate,
		})
		return
	}
	stack.Outputs = outputs

	// Mark stack complete and emit the final stack event.
	stack.Status = StatusCreateComplete
	stack.StatusReason = ""
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusCreateComplete, "")
	// This deploy succeeded, so any diagnosis of the last one that failed no
	// longer describes anything. Cleared before the flush so the removal is
	// persisted with the rest of the terminal state.
	p.clearDeployDiagnostics(ctx, stack)
	p.persistTerminalState(ctx, stack)
	p.publishStackEvent(ctx, events.CFNStackCreated, stack)
	log.Debug("cfn: stack provisioned",
		zap.String("stack", stack.StackName),
		zap.Int("resources", len(order)),
		zap.Duration("elapsed", p.clk.Since(stackStart)))
}

// ── Update stack (async) ───────────────────────────────────────────────────

// updateStack applies tmpl to stack. previous is the generation the caller has
// just overwritten on the stack record — see stackGeneration — and is what a
// rollback restores.
func (p *provisioner) updateStack(stack *Stack, tmpl *Template, previous stackGeneration, onComplete stackCompletionFunc, rec *trace.Recorder) {
	p.provisionAsync(stack, rec, func(ctx context.Context) {
		p.updateStackResourcesCtx(ctx, stack, tmpl, previous)
		if onComplete != nil {
			onComplete(ctx, stack)
		}
	})
}

func (p *provisioner) updateStackResources(stack *Stack, tmpl *Template, previous stackGeneration) {
	p.updateStackResourcesCtx(p.operationCtx(stack), stack, tmpl, previous)
}

func (p *provisioner) updateStackResourcesCtx(ctx context.Context, stack *Stack, tmpl *Template, previous stackGeneration) {
	log := p.log.WithRecorder(ctx)
	rCtx := p.buildResolveContext(stack, tmpl)
	rCtx.PreviousStackTags = previous.Tags

	// Emit the initial stack UPDATE_IN_PROGRESS event.
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusUpdateInProgress, "User Initiated")

	// For simplicity: treat all resources as requiring re-creation.
	// existing tracks resources still to be accounted for; entries are removed
	// as the template's resources are processed, so whatever remains at the end
	// is what the template dropped and the cleanup phase must delete.
	existing := map[string]StackResource{}
	// preUpdate is the same set, kept intact: rollback restores the stack's
	// resource list from it. It must not be the map above — that one is
	// emptied as the update proceeds, so using it would drop every resource
	// the update had already handled and leave the stack owning nothing.
	preUpdate := map[string]StackResource{}
	for _, r := range stack.Resources {
		existing[r.LogicalID] = r
		preUpdate[r.LogicalID] = r
	}

	order, err := topoSort(tmpl.Resources)
	if err != nil {
		p.failStack(ctx, stack, StatusUpdateFailed, fmt.Sprintf("dependency cycle: %v", err))
		return
	}

	var newResources []StackResource
	// Old resources that replacements superseded. They stay alive until the
	// whole update succeeds — see updateResource — and are deleted in the
	// cleanup phase at the end, or left standing for rollback if it fails.
	var superseded []supersededResource
	// logical ID → the replacement's new physical ID, so rollback knows which
	// resources it created and must remove.
	replacedBy := map[string]string{}
	// Tracks only updates that actually succeeded through resourceUpdater.
	// Replacement fallbacks can return the same physical ID for upsert-shaped
	// services and must not be replayed as in-place updates during rollback.
	inPlaceUpdated := map[string]bool{}
	// A handler may fail after applying a mutation and also fail its own
	// compensation. Keep that distinct from a validation rejection so rollback
	// does not report a clean restoration over service-side state it could not
	// restore.
	dirtyUpdates := map[string]bool{}

	for _, logicalID := range order {
		if ctx.Err() != nil {
			// A cancelled update stops where it stands. No reverse pass runs
			// regardless of DisableRollback — the context that would drive it
			// is the one that died — so, as on the no-rollback failure
			// branches below, the stack record is the only account of where
			// the walk stopped and must keep everything the attempt reached.
			// Discarding the accumulated records would persist a pre-update
			// state that no longer matches the service-side resources.
			stack.Resources = retainedUpdateResources(newResources, stack.Resources)
			p.failStack(ctx, stack, StatusUpdateFailed, "cancelled")
			return
		}
		res := tmpl.Resources[logicalID]

		// CloudFormation compares the literal dynamic-reference string before it
		// retrieves the secret. Per AWS, an unchanged containing resource does
		// not retrieve the reference at all. Besides preserving that deliberately
		// stale value, deferring expansion avoids an internal API call per secret
		// on no-op stack updates.
		recordedProps := resolveAllProperties(res.Properties, rCtx)
		// An Fn::GetStackOutput that failed to resolve is taken here, with the
		// resource that made it still selected. Left on the context it would be
		// charged to whichever resource next expands its properties — and an
		// unchanged resource never does, so its own failure would otherwise
		// skip it and fail its neighbour.
		resolveErr := rCtx.takeDynamicRefErr()
		propsHash := hashResourceProperties(res.Type, recordedProps, stack.Tags)

		// Same logical ID and type, with a resource still behind the record.
		// That last part is what makes the record a prior state worth diffing:
		// a create that failed before naming anything, or one a rollback has
		// since deleted, leaves a record the stack no longer has a resource for,
		// and it belongs on the create branch below. Treated as existing it was
		// compared against a resource that is not there — and because a failed
		// create records no property hash, resourcePropertiesMatch read that as
		// "unchanged" and skipped it — so a redeploy silently declined to
		// provision the resource and carried the failed attempt's status and
		// reason forward as if they still described something.
		if old, ok := existing[logicalID]; ok && old.Type == res.Type && old.existsServiceSide() {
			// Diff the resolved properties and either skip (no change), update
			// in-place (handler supports it), or fall back to delete + create
			// (handler doesn't).
			rCtx.Resources[logicalID] = old.PhysicalID
			if old.Attributes != nil {
				if rCtx.Attributes == nil {
					rCtx.Attributes = make(map[string]map[string]string)
				}
				rCtx.Attributes[logicalID] = old.Attributes
			}

			if resolveErr == nil && resourcePropertiesMatch(old.PropertiesHash, res.Type, recordedProps, stack.Tags, previous.Tags) {
				// No change, or legacy resource without a recorded hash —
				// treat as unchanged. (Stacks created before property
				// hashing was added have no recorded hash; without a
				// known prior state we assume the user did not intend
				// to mutate every resource on the next update.)
				newResources = append(newResources, StackResource{
					LogicalID:           logicalID,
					PhysicalID:          old.PhysicalID,
					Type:                res.Type,
					Status:              old.Status,
					StatusReason:        old.StatusReason,
					Timestamp:           old.Timestamp,
					Attributes:          old.Attributes,
					PropertiesHash:      propsHash,
					Properties:          recordedProps,
					DeletionPolicy:      res.DeletionPolicy,
					UpdateReplacePolicy: res.UpdateReplacePolicy,
				})
				delete(existing, logicalID)
				continue
			}

			// Properties changed — attempt update.
			props, refErr := expandResourceProperties(res.Type, recordedProps, rCtx)
			if resolveErr != nil {
				refErr = resolveErr
			}
			p.recordEvent(ctx, stack, logicalID, old.PhysicalID, res.Type, ResourceUpdateInProgress, "")
			outcome, updErr := resourceUpdateOutcome{}, refErr
			if updErr == nil {
				outcome, updErr = p.updateResource(ctx, logicalID, res, props, old.PhysicalID, &old, rCtx)
			}
			physID := outcome.PhysicalID
			if outcome.Replaced() {
				// Rollback must remove the replacement whether or not the
				// original is retained.
				replacedBy[logicalID] = physID
				if !outcome.RetainReplaced {
					// The original stays alive until the whole update succeeds,
					// so a later failure can still roll back to it.
					superseded = append(superseded, supersededResource{
						LogicalID: logicalID, Type: old.Type, PhysicalID: outcome.ReplacedPhysicalID,
						Properties: old.Properties,
					})
				}
			}
			if outcome.UpdatedInPlace {
				inPlaceUpdated[logicalID] = true
			}
			now := p.clk.Now()
			if updErr != nil {
				resourceDirty := isDirtyUpdateFailure(updErr)
				resourceStateChanged := resourceDirty || outcome.UpdatedInPlace
				if resourceDirty {
					dirtyUpdates[logicalID] = true
				}
				failedResource := StackResource{
					LogicalID:    logicalID,
					PhysicalID:   old.PhysicalID,
					Type:         res.Type,
					Status:       ResourceUpdateFailed,
					StatusReason: updErr.Error(),
					Timestamp:    now,
				}
				if resourceStateChanged {
					if outcome.PhysicalID != "" {
						failedResource.PhysicalID = outcome.PhysicalID
					}
					failedResource.Attributes = rCtx.Attributes[logicalID]
					failedResource.PropertiesHash = propsHash
					failedResource.Properties = recordedProps
					failedResource.DeletionPolicy = res.DeletionPolicy
					failedResource.UpdateReplacePolicy = res.UpdateReplacePolicy
				}
				newResources = append(newResources, failedResource)
				p.recordEvent(ctx, stack, logicalID, failedResource.PhysicalID, res.Type, ResourceUpdateFailed, updErr.Error())
				// Capture before the rollback, as on the create path. The
				// attempted list is passed rather than stack.Resources: on an
				// update the stack record still holds the pre-update
				// generation, so the resource that just failed is only in here.
				diagnosis := p.collectDeployDiagnostics(ctx, stack, deployOperationUpdate, newResources)
				defer p.recordDeployDiagnostics(ctx, stack, diagnosis)
				if stack.DisableRollback {
					if !resourceStateChanged {
						newResources[len(newResources)-1] = retainPreviousResourceState(failedResource, old)
					}
					stack.Resources = retainedUpdateResources(newResources, stack.Resources)
					p.failStack(ctx, stack, StatusUpdateFailed,
						fmt.Sprintf("resource %s failed: %v", logicalID, updErr))
					return
				}
				p.rollbackUpdate(ctx, stack, newResources, preUpdate, replacedBy, inPlaceUpdated, dirtyUpdates, rCtx, previous,
					fmt.Sprintf("resource %s failed: %v", logicalID, updErr))
				return
			}
			newResources = append(newResources, StackResource{
				LogicalID:           logicalID,
				PhysicalID:          physID,
				Type:                res.Type,
				Status:              ResourceUpdateComplete,
				StatusReason:        rCtx.EmulationLimitation,
				Timestamp:           now,
				Attributes:          rCtx.Attributes[logicalID],
				PropertiesHash:      propsHash,
				Properties:          recordedProps,
				DeletionPolicy:      res.DeletionPolicy,
				UpdateReplacePolicy: res.UpdateReplacePolicy,
			})
			rCtx.Resources[logicalID] = physID
			// As on the create path: the reason, when there is one, says what
			// Overcast will not do with the resource, and rides the
			// UPDATE_COMPLETE event so a deploy shows it as the resource goes by.
			p.recordEvent(ctx, stack, logicalID, physID, res.Type, ResourceUpdateComplete, rCtx.EmulationLimitation)
			p.publishResourceEvent(ctx, events.CFNResourceProvisioned, stack.StackName, logicalID, res.Type, physID)
			delete(existing, logicalID)
			continue
		}

		props, refErr := expandResourceProperties(res.Type, recordedProps, rCtx)
		if resolveErr != nil {
			refErr = resolveErr
		}

		// New (or different type) — emit CREATE_IN_PROGRESS before provisioning.
		p.recordEvent(ctx, stack, logicalID, "", res.Type, ResourceCreateInProgress, "")

		var physID string
		provErr := refErr
		if provErr == nil {
			physID, provErr = p.provisionResource(ctx, logicalID, res, props, rCtx)
		}
		now := p.clk.Now()
		if provErr != nil {
			// As on the create path: a resource that failed to stabilize has a
			// physical ID, and rollback needs it to clean the resource up.
			newResources = append(newResources, StackResource{
				LogicalID:    logicalID,
				PhysicalID:   physID,
				Type:         res.Type,
				Status:       ResourceCreateFailed,
				StatusReason: provErr.Error(),
				Timestamp:    now,
			})
			p.recordEvent(ctx, stack, logicalID, physID, res.Type, ResourceCreateFailed, provErr.Error())
			diagnosis := p.collectDeployDiagnostics(ctx, stack, deployOperationUpdate, newResources)
			defer p.recordDeployDiagnostics(ctx, stack, diagnosis)
			if stack.DisableRollback {
				stack.Resources = retainedUpdateResources(newResources, stack.Resources)
				p.failStack(ctx, stack, StatusUpdateFailed,
					fmt.Sprintf("resource %s failed: %v", logicalID, provErr))
				return
			}
			// Roll back: delete newly created resources (those not in `existing`)
			// in reverse order, then restore the previous resource list.
			p.rollbackUpdate(ctx, stack, newResources, preUpdate, replacedBy, inPlaceUpdated, dirtyUpdates, rCtx, previous,
				fmt.Sprintf("resource %s failed: %v", logicalID, provErr))
			return
		}
		newResources = append(newResources, StackResource{
			LogicalID:           logicalID,
			PhysicalID:          physID,
			Type:                res.Type,
			Status:              ResourceCreateComplete,
			StatusReason:        rCtx.EmulationLimitation,
			Timestamp:           now,
			Attributes:          rCtx.Attributes[logicalID],
			PropertiesHash:      propsHash,
			Properties:          recordedProps,
			DeletionPolicy:      res.DeletionPolicy,
			UpdateReplacePolicy: res.UpdateReplacePolicy,
		})
		rCtx.Resources[logicalID] = physID
		// The resource succeeded; the reason, when there is one, says what
		// Overcast will not do with it. It rides the CREATE_COMPLETE event so a
		// deploy shows it as the resource goes by, not only on a later describe.
		p.recordEvent(ctx, stack, logicalID, physID, res.Type, ResourceCreateComplete, rCtx.EmulationLimitation)
		p.publishResourceEvent(ctx, events.CFNResourceProvisioned, stack.StackName, logicalID, res.Type, physID)
	}

	// Outputs are resolved before the cleanup phase deletes anything, so an
	// output whose Fn::GetStackOutput cannot be resolved can still roll back
	// to the originals the update superseded or removed.
	outputs, outErr := p.resolveOutputs(tmpl, rCtx)
	if outErr != nil {
		p.rollbackUpdate(ctx, stack, newResources, preUpdate, replacedBy, inPlaceUpdated, dirtyUpdates, rCtx, previous, outErr.Error())
		return
	}

	// Cleanup phase. Every resource is updated; what remains is removing what
	// the update superseded. CloudFormation reports this separately rather than
	// folding it into UPDATE_IN_PROGRESS, so a stack held up by a resource that
	// will not delete is visibly stuck here rather than looking mid-update.
	stack.Status = StatusUpdateCompleteCleanupInProgress
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack",
		StatusUpdateCompleteCleanupInProgress, "")

	// Delete removed resources, honouring DeletionPolicy=Retain.
	//
	// Walked in reverse of the stack's own resource order, not over `existing`
	// directly: that map's iteration order is random, and teardown order is not
	// arbitrary. stack.Resources is still the pre-update list here (it is
	// replaced below) and it is in dependency order, so reversing it deletes
	// dependents before what they depend on — the same rule deleteStackResources
	// follows. An update that drops a role and the instance profile holding it
	// has to remove the profile first, or IAM refuses the role with
	// DeleteConflict and it survives the update that was meant to remove it.
	for i := len(stack.Resources) - 1; i >= 0; i-- {
		logicalID := stack.Resources[i].LogicalID
		old, stillToDelete := existing[logicalID]
		if !stillToDelete {
			continue
		}
		if !old.existsServiceSide() {
			// Nothing stands behind the record — a create that failed before
			// naming anything, or one a rollback already deleted. Deleting by an
			// empty physical ID would report a teardown that never happened, and
			// deleting by the ID of an already-deleted resource would tear down
			// whatever the loop above has just re-created under the same name.
			continue
		}
		if old.shouldRetainOnDelete() {
			log.Info("cfn: retaining removed resource (DeletionPolicy=Retain)",
				zap.String("type", old.Type),
				zap.String("logicalId", logicalID),
				zap.String("physicalId", old.PhysicalID))
			continue
		}
		p.recordEvent(ctx, stack, logicalID, old.PhysicalID, old.Type, ResourceDeleteInProgress, "")
		// A teardown that could not finish here does not fail the update. This
		// is the cleanup phase, which AWS runs after the update has already
		// succeeded and does not roll back — so the event says the resource is
		// still standing and the stack still completes, rather than either
		// lying about the delete or failing an update that worked.
		if err := p.deleteResource(ctx, logicalID, old.Type, old.PhysicalID, old.Properties, rCtx); err != nil {
			p.recordEvent(ctx, stack, logicalID, old.PhysicalID, old.Type, ResourceDeleteFailed, err.Error())
			continue
		}
		p.recordEvent(ctx, stack, logicalID, old.PhysicalID, old.Type, ResourceDeleteComplete, "")
		p.publishResourceEvent(ctx, events.CFNResourceDeleted, stack.StackName, logicalID, old.Type, old.PhysicalID)
	}

	// The originals that replacements superseded, deleted here rather than at
	// the point of replacement so that a failure anywhere earlier could still
	// roll back to them. Reversed for the same reason as the loop above: they
	// were appended in dependency order as the update walked the template.
	for i := len(superseded) - 1; i >= 0; i-- {
		s := superseded[i]
		p.recordEvent(ctx, stack, s.LogicalID, s.PhysicalID, s.Type, ResourceDeleteInProgress, "")
		if err := p.deleteResource(ctx, s.LogicalID, s.Type, s.PhysicalID, s.Properties, rCtx); err != nil {
			p.recordEvent(ctx, stack, s.LogicalID, s.PhysicalID, s.Type, ResourceDeleteFailed, err.Error())
			continue
		}
		p.recordEvent(ctx, stack, s.LogicalID, s.PhysicalID, s.Type, ResourceDeleteComplete, "")
	}

	stack.Resources = newResources
	stack.Outputs = outputs
	now := p.clk.Now()
	stack.UpdatedAt = &now
	stack.Status = StatusUpdateComplete
	stack.StatusReason = ""
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusUpdateComplete, "")
	// As on the create path: a deploy that succeeded retires the diagnosis of
	// the one that failed before it.
	p.clearDeployDiagnostics(ctx, stack)
	p.persistTerminalState(ctx, stack)
	p.publishStackEvent(ctx, events.CFNStackUpdated, stack)
}

// retainPreviousResourceState completes the record of a resource whose update
// was rejected before it reached the service, for a stack that will not roll
// back.
//
// Nothing was applied, so the resource still holds precisely what it held
// before — recording that is what makes the retained UPDATE_FAILED record
// truthful. It also matters to whatever runs next: a record carrying no
// properties reads to the following update as "no known prior state", which
// resourcePropertiesMatch treats as unchanged, so the resource that just failed
// would be skipped rather than re-attempted.
//
// Only the no-rollback path needs this. On the rollback path the pre-update
// record is restored wholesale instead.
func retainPreviousResourceState(failed, previous StackResource) StackResource {
	failed.Attributes = previous.Attributes
	failed.PropertiesHash = previous.PropertiesHash
	failed.Properties = previous.Properties
	failed.DeletionPolicy = previous.DeletionPolicy
	failed.UpdateReplacePolicy = previous.UpdateReplacePolicy
	return failed
}

// retainedUpdateResources is the stack's resource list after an update that
// failed and will not roll back.
//
// With rollback disabled the stack record is the only account of where the
// attempt stopped, so it keeps everything the attempt reached — updated
// resources at their attempted state, the failing one UPDATE_FAILED — followed
// by the prior record of every resource the walk never got to. Both halves
// matter: without the first, DescribeStackResources reports pre-update
// properties for resources that have already changed; without the second, the
// stack disowns resources that are still standing, and the next operation
// treats them as new.
//
// prior is in dependency order and attempted follows the template's, so
// appending the untouched remainder keeps a later reverse walk (DeleteStack,
// and the update cleanup phase) tearing dependents down before what they
// depend on.
func retainedUpdateResources(attempted, prior []StackResource) []StackResource {
	reached := make(map[string]bool, len(attempted))
	for _, r := range attempted {
		reached[r.LogicalID] = true
	}
	retained := make([]StackResource, 0, len(attempted)+len(prior))
	retained = append(retained, attempted...)
	for _, r := range prior {
		if !reached[r.LogicalID] {
			retained = append(retained, r)
		}
	}
	return retained
}

// ── Delete stack (async) ───────────────────────────────────────────────────

func (p *provisioner) deleteStack(stack *Stack, rec *trace.Recorder) {
	p.provisionAsync(stack, rec, func(ctx context.Context) {
		p.deleteStackResourcesCtx(ctx, stack)
	})
}

// deleteStackResourcesCtx is the synchronous core of stack deletion.
// It tears down all resources in reverse order and marks the stack as
// DELETE_COMPLETE. Both top-level deleteStack (async) and nestedStackHandler
// (inline) use this method, and both pass a context carrying the originating
// request's trace recorder so every teardown call is recorded as a hop.
func (p *provisioner) deleteStackResourcesCtx(ctx context.Context, stack *Stack) {
	log := p.log.WithRecorder(ctx)

	rCtx := &resolveContext{
		Region:    stack.Region,
		AccountID: p.cfg.AccountID,
		StackName: stack.StackName,
		StackID:   stack.StackID,
	}
	if rCtx.Region == "" {
		rCtx.Region = p.cfg.Region
	}

	// Emit the initial stack DELETE_IN_PROGRESS event.
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusDeleteInProgress, "User Initiated")

	// Delete resources in reverse order, honouring DeletionPolicy=Retain.
	for i := len(stack.Resources) - 1; i >= 0; i-- {
		r := stack.Resources[i]
		if ctx.Err() != nil {
			p.failStack(ctx, stack, StatusDeleteFailed, "cancelled")
			return
		}
		if r.PhysicalID == "" {
			continue
		}
		if r.shouldRetainOnDelete() {
			log.Info("cfn: retaining resource on stack delete (DeletionPolicy=Retain)",
				zap.String("type", r.Type),
				zap.String("logicalId", r.LogicalID),
				zap.String("physicalId", r.PhysicalID))
			stack.Resources[i].Status = ResourceDeleteSkipped
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteSkipped, "DeletionPolicy=Retain")
			continue
		}
		stack.Resources[i].Status = ResourceDeleteInProgress
		p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteInProgress, "")
		if err := p.deleteResource(ctx, r.LogicalID, r.Type, r.PhysicalID, r.Properties, rCtx); err != nil {
			// The resource is still standing — it refused, or the delete
			// failed. Leave it in the stack's resource list: AWS keeps a
			// DELETE_FAILED resource visible so the retry, once the cause is
			// cleared, knows what is still out there. Dropping it here would
			// destroy the last record of a resource nothing else names.
			stack.Resources[i].Status = ResourceDeleteFailed
			stack.Resources[i].StatusReason = err.Error()
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteFailed, err.Error())
			p.failStack(ctx, stack, StatusDeleteFailed, err.Error())
			return
		}
		stack.Resources[i].Status = ResourceDeleteComplete
		p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteComplete, "")
		p.publishResourceEvent(ctx, events.CFNResourceDeleted, stack.StackName, r.LogicalID, r.Type, r.PhysicalID)
	}

	now := p.clk.Now()
	stack.DeletedAt = &now
	stack.Status = StatusDeleteComplete
	stack.StatusReason = ""
	stack.Resources = nil
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusDeleteComplete, "")
	p.persistTerminalState(ctx, stack)
	p.publishStackEvent(ctx, events.CFNStackDeleted, stack)
}

// ── Resource provisioning ──────────────────────────────────────────────────

// resolveHandler returns the resource handler for a given CloudFormation type.
// It first checks the static resourceHandlers map, then handles provisioner-
// linked types that require access to the provisioner (custom resources and
// nested stacks).
func (p *provisioner) resolveHandler(resType string) (resourceHandler, bool) {
	if h, ok := resourceHandlers[resType]; ok {
		return h, true
	}
	// Custom::* and AWS::CloudFormation::CustomResource both use the custom
	// resource protocol (Lambda invocation via ServiceToken).
	if isCustomResourceType(resType) {
		return &customResourceHandler{p: p}, true
	}
	// Nested stacks require synchronous provisioning through the provisioner.
	if resType == "AWS::CloudFormation::Stack" {
		return &nestedStackHandler{p: p}, true
	}
	return nil, false
}

// isCustomResourceType reports whether resType is provisioned through the
// custom resource protocol (Custom::* or AWS::CloudFormation::CustomResource).
func isCustomResourceType(resType string) bool {
	return strings.HasPrefix(resType, "Custom::") || resType == "AWS::CloudFormation::CustomResource"
}

// provisionResource creates a resource by dispatching an internal HTTP request.
// props are the already-resolved properties (after Ref / Fn::GetAtt / etc.
// substitution). Callers resolve once, hash, and pass in.
func (p *provisioner) provisionResource(ctx context.Context, logicalID string, res TemplateResource, props map[string]any, rCtx *resolveContext) (string, error) {
	log := p.log.WithRecorder(ctx)
	p.mu.Lock()
	router := p.router
	p.mu.Unlock()
	if router == nil {
		return "", fmt.Errorf("router not initialised")
	}

	// Handlers name unnamed resources from the logical ID (see
	// resolveContext.generatedName). Provisioning walks the dependency order
	// one resource at a time, so a single field on the shared context is safe.
	rCtx.LogicalID = logicalID

	// Collect whatever the services report as inert while this resource is
	// provisioned, for the caller to record as its status reason. Same
	// single-field-on-a-shared-context reasoning as LogicalID above:
	// provisioning walks the dependency order one resource at a time.
	ctx, limitations := withLimitationCollector(ctx)
	rCtx.EmulationLimitation = ""
	defer func() { rCtx.EmulationLimitation = limitations.reason() }()

	handler, ok := p.resolveHandler(res.Type)
	if !ok {
		// Unknown resource type — generate a fake physical ID and succeed.
		// This allows templates with unsupported resources to partially deploy.
		physID := fmt.Sprintf("%s-%s-stub", rCtx.StackName, logicalID)
		log.Warn("cfn: unsupported resource type, creating stub",
			zap.String("type", res.Type),
			zap.String("logicalId", logicalID),
			zap.String("physicalId", physID))
		limitations.add(unrecognizedResourceMessage(res.Type))
		return physID, nil
	}

	physID, attrs, err := handler.Create(ctx, router, p.cfg, props, rCtx)
	if err != nil {
		// A handler may create the primary resource before a later configuration
		// call fails. Preserve its identity so the caller can record the failed
		// resource and rollback can delete what exists service-side.
		return physID, err
	}
	// Store attributes for Fn::GetAtt resolution.
	if len(attrs) > 0 {
		if rCtx.Attributes == nil {
			rCtx.Attributes = make(map[string]map[string]string)
		}
		rCtx.Attributes[logicalID] = attrs
	}

	// The resource exists but may not yet be usable — see resourceStabilizer.
	// The physical ID travels out with a failure here precisely because the
	// resource is real: the caller records it against the failed resource so
	// rollback can delete it.
	if stabilizer, ok := handler.(resourceStabilizer); ok {
		if err := stabilizer.Stabilize(ctx, router, p.cfg, p.clk, physID, rCtx); err != nil {
			return physID, err
		}
	}

	// CDK's Application construct propagates `awsApplication=<app-arn>` to
	// every resource in the stack. Honour it by recording a direct resource
	// association so the UI can resolve ownership via ListAssociatedResources.
	if appTag := extractAwsApplicationTag(props); appTag != "" && physID != "" {
		p.autoAssociateResource(ctx, rCtx, appTag, physID)
	}
	// The resource is real and stable. Say so if it is a deliberate no-op or
	// backed by an inert/stub-tier service — see fidelity.go — unless the
	// handler already reported something more specific.
	addFidelityFallback(limitations, res.Type, handler, true)
	return physID, nil
}

// updateResource updates an existing resource in place when the handler
// supports it (resourceUpdater interface). Otherwise it replaces the resource:
// a new one is created and the old one is handed back to the caller to delete
// once the whole stack update has succeeded.
//
// Returns what the update did (see resourceUpdateOutcome) and an error.
//
// Replacement creates before deleting, as real CloudFormation does. The old
// resource is what an update rolls back to, so destroying it up front would
// make rollback impossible: a create that then failed would leave nothing
// behind at all. Deletion is deferred to the caller's cleanup phase, which
// only runs once every resource in the stack has been updated successfully.
//
// If the resource has UpdateReplacePolicy=Retain (or Snapshot) the original is
// orphaned rather than deleted, so it outlives the stack. It is still reported
// as replaced, because rollback must remove the replacement regardless.
func (p *provisioner) updateResource(ctx context.Context, logicalID string, res TemplateResource, props map[string]any, oldPhysicalID string, oldResource *StackResource, rCtx *resolveContext) (resourceUpdateOutcome, error) {
	log := p.log.WithRecorder(ctx)
	p.mu.Lock()
	router := p.router
	p.mu.Unlock()
	if router == nil {
		return resourceUpdateOutcome{}, fmt.Errorf("router not initialised")
	}

	// Same reason as in provisionResource: an update may fall through to a
	// create, which needs the logical ID to name an unnamed resource.
	rCtx.LogicalID = logicalID

	handler, ok := p.resolveHandler(res.Type)
	if !ok {
		// Unknown resource type — keep stub physical ID, no-op.
		rCtx.EmulationLimitation = unrecognizedResourceMessage(res.Type)
		return resourceUpdateOutcome{PhysicalID: oldPhysicalID}, nil
	}

	// Prefer in-place update when supported.
	if updater, ok := handler.(resourceUpdater); ok {
		var oldProps map[string]any
		if oldResource != nil {
			oldProps = oldResource.Properties
		}
		// Same reasoning as provisionResource: gather whatever the dispatched
		// calls report as inert while this update runs, so the caller can
		// record it as the resource's status reason.
		ctx, limitations := withLimitationCollector(ctx)
		physID, attrs, err := updater.Update(ctx, router, p.cfg, oldPhysicalID, props, oldProps, rCtx)
		if err == nil {
			if physID == "" {
				physID = oldPhysicalID
			}
			outcome := resourceUpdateOutcome{PhysicalID: physID, UpdatedInPlace: true}
			if len(attrs) > 0 {
				if rCtx.Attributes == nil {
					rCtx.Attributes = make(map[string]map[string]string)
				}
				rCtx.Attributes[logicalID] = attrs
			}
			// The change has been applied; the resource may still be settling
			// into it — see resourceStabilizer. Failing to settle is terminal
			// for the resource rather than a reason to replace it, so it takes
			// the same updateFailure path a handler's own failure does. The
			// successful outcome still travels back so rollback can reverse it.
			if stabilizer, ok := handler.(resourceStabilizer); ok {
				if stErr := stabilizer.Stabilize(ctx, router, p.cfg, p.clk, physID, rCtx); stErr != nil {
					return outcome, failUpdate(stErr)
				}
			}
			addFidelityFallback(limitations, res.Type, handler, true)
			rCtx.EmulationLimitation = limitations.reason()
			return outcome, nil
		}
		// An update that applied and then failed is terminal — replacing the
		// resource would neither fix it nor be what AWS does. See updateFailure.
		var failed updateFailure
		if errors.As(err, &failed) {
			return resourceUpdateOutcome{}, err
		}
		// Sentinel: fall through to replacement (mirrors AWS "Replacement: Yes"
		// for properties like resource Name or DynamoDB KeySchema).
		if !errors.Is(err, errReplacementRequired) {
			log.Warn("cfn: in-place update failed, falling back to replace",
				zap.String("type", res.Type),
				zap.String("logicalId", logicalID),
				zap.Error(err))
		}
	}

	// Replacement path: create the new resource first, leaving the old one
	// untouched. If this fails the caller rolls back to the old resource,
	// which is only possible because it still exists.
	//
	// A name the template supplies is the caller's problem — creating a second
	// resource with the same name will 409, exactly as it does on AWS, which
	// is why AWS treats a name change as replacement and generates unique
	// names for resources the template does not name (see generatedName).
	newPhysID, err := p.provisionResource(ctx, logicalID, res, props, rCtx)
	if err != nil {
		// Create may have named the replacement before its stabilizer failed.
		// Preserve both IDs so the caller can delete the failed replacement
		// during rollback while leaving the original resource in place.
		if newPhysID != "" {
			return resourceUpdateOutcome{
				PhysicalID:         newPhysID,
				ReplacedPhysicalID: oldPhysicalID,
			}, err
		}
		return resourceUpdateOutcome{}, err
	}

	// A "replacement" that hands back the original's physical ID is not a
	// replacement — there is only one resource. Services whose create is an
	// upsert keyed by a caller-pinned name (CloudWatch PutMetricAlarm, Route53
	// UPSERT-shaped records) do exactly this. Reporting it as replaced would
	// hand that one ID to the post-success cleanup and to rollback, both of
	// which delete it — destroying the resource behind a stack that claims
	// UPDATE_COMPLETE. Real CloudFormation cannot reach this state: it refuses
	// to replace a custom-named resource whose name did not change.
	if newPhysID == oldPhysicalID {
		return resourceUpdateOutcome{PhysicalID: newPhysID}, nil
	}

	outcome := resourceUpdateOutcome{PhysicalID: newPhysID, ReplacedPhysicalID: oldPhysicalID}
	if oldResource != nil && oldResource.shouldRetainOnReplace() {
		// UpdateReplacePolicy=Retain orphans the original instead of deleting
		// it. Still a replacement, so rollback removes the replacement.
		outcome.RetainReplaced = true
		log.Info("cfn: retaining old resource on replacement (UpdateReplacePolicy=Retain)",
			zap.String("type", res.Type),
			zap.String("logicalId", logicalID),
			zap.String("orphanedPhysicalId", oldPhysicalID))
	}
	return outcome, nil
}

// supersededResource is an old physical resource that a replacement replaced.
// It is deleted only after the whole stack update succeeds — until then it is
// what the update rolls back to.
type supersededResource struct {
	LogicalID  string
	Type       string
	PhysicalID string
	// Properties the superseded resource was provisioned with, for handlers
	// whose teardown needs them (see resourcePropertiesDeleter).
	Properties map[string]any
}

// resourceUpdateOutcome describes what an update did to a single resource.
//
// Replacement and retention are separate facts, and conflating them loses one:
// a retained original must not be deleted at cleanup, but the resource was
// still replaced, and rollback has to remove the replacement either way.
type resourceUpdateOutcome struct {
	// PhysicalID is the resource's physical ID after the update.
	PhysicalID string
	// ReplacedPhysicalID is the original that a replacement superseded, or ""
	// when the resource was updated in place.
	ReplacedPhysicalID string
	// RetainReplaced reports that UpdateReplacePolicy keeps the original, so
	// the cleanup phase must leave it alone.
	RetainReplaced bool
	UpdatedInPlace bool
}

// Replaced reports whether this update replaced the resource rather than
// updating it in place.
func (o resourceUpdateOutcome) Replaced() bool { return o.ReplacedPhysicalID != "" }

// hashProps returns a stable sha256 hash of the resolved property map.
// Used by UpdateStack to detect property drift and reprovision only
// resources whose properties actually changed.
func hashProps(props map[string]any) string {
	// json.Marshal of a map produces keys in sorted order in Go's encoding/json,
	// so the hash is stable across runs for equivalent inputs.
	data, err := json.Marshal(props)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// stackTagPropagationResourceTypes is the effective-stack-tag mechanism's
// membership (#1143, extended by #1310): every resource type here merges the
// stack's tags into its own tags at create (mergeResourceTags), and
// reconciles a stack-tag-only change on update by treating it as a properties
// change here — hashResourceProperties/resourcePropertiesMatch don't skip the
// resource just because its own property map is unchanged.
//
// AWS::CloudFormation::Stack participates too but is handled separately
// below: it merges tags for every logical resource in the nested template,
// not just itself, so its EffectiveTags shape uses mergeNestedStackTags, not
// mergeResourceTags.
//
// This set must stay in sync with the resource handlers that actually forward
// Tags; TestStackTagPropagationCoverage (stack_tag_propagation_coverage_dev_test.go,
// dev-tagged) parses every handler's Create/Update method and fails if one
// forwards Tags while being a member of neither this set nor
// stackTagPropagationExclusions below.
var stackTagPropagationResourceTypes = map[string]bool{
	"AWS::Lambda::Function":           true,
	"AWS::Lambda::EventSourceMapping": true,
	"AWS::Logs::LogGroup":             true,
	"AWS::SecretsManager::Secret":     true,
	"AWS::SQS::Queue":                 true,
	"AWS::Kinesis::Stream":            true,
	"AWS::ECR::Repository":            true,
	// Already merged stack tags at create and reconciled them via their
	// service's own Tag*/Untag* on update, but were missing from this set —
	// a stack-tag-only change never reached their Update at all, since
	// resourcePropertiesMatch treated the resource as unchanged (#1310).
	"AWS::DynamoDB::Table":             true,
	"AWS::SSM::Parameter":              true,
	"AWS::StepFunctions::StateMachine": true,
	// Gained Tags support in #1308/#1310; stack-tag propagation added
	// alongside so there is no parallel, resource-tags-only mechanism (#1310).
	"AWS::CloudTrail::Trail":    true,
	"AWS::Transfer::Server":     true,
	"AWS::Transfer::User":       true,
	"AWS::IAM::ManagedPolicy":   true,
	"AWS::IAM::InstanceProfile": true,
	// IAM::Role and IAM::User already threaded Tags via the same
	// iamTags/iamTagMutations helpers ManagedPolicy and InstanceProfile use;
	// leaving them out while fixing the other two would itself be the
	// parallel mechanism #1310 asks not to create.
	"AWS::IAM::Role": true,
	"AWS::IAM::User": true,
}

// stackTagPropagationExclusions (stack_tag_propagation_coverage_dev_test.go)
// lists every resource type whose Create or Update handler forwards Tags but
// is deliberately not a member of stackTagPropagationResourceTypes, each with
// the reason found during #1310's audit — kept next to the dev-tagged
// TestStackTagPropagationCoverage that is its only reader, since nothing in
// the default build consults it.

// hashResourceProperties includes only tags CloudFormation propagates outside
// the resource property map. Resource-level tags are already present in props;
// merging here also avoids updates when they override a changed stack tag.
func hashResourceProperties(resourceType string, props map[string]any, stackTags []Tag) string {
	switch {
	case resourceType == "AWS::CloudFormation::Stack":
		return hashProps(map[string]any{
			"Properties":    props,
			"EffectiveTags": mergeNestedStackTags(stackTags, props["Tags"]),
		})
	case stackTagPropagationResourceTypes[resourceType]:
		return hashProps(map[string]any{
			"Properties":    props,
			"EffectiveTags": mergeResourceTags(stackTags, props["Tags"]),
		})
	default:
		return hashProps(props)
	}
}

func resourcePropertiesMatch(oldHash, resourceType string, props map[string]any, stackTags, previousStackTags []Tag) bool {
	if oldHash == hashResourceProperties(resourceType, props, stackTags) {
		return true
	}
	// Hashes written before propagated-tag tracking contained only the property
	// map. Preserve their no-op behavior when effective tags did not change,
	// while still reconciling a real stack-only tag delta.
	if resourceType != "AWS::CloudFormation::Stack" && !stackTagPropagationResourceTypes[resourceType] {
		return oldHash == ""
	}
	var currentTags, previousTags any
	if resourceType == "AWS::CloudFormation::Stack" {
		currentTags = mergeNestedStackTags(stackTags, props["Tags"])
		previousTags = mergeNestedStackTags(previousStackTags, props["Tags"])
	} else {
		currentTags = mergeResourceTags(stackTags, props["Tags"])
		previousTags = mergeResourceTags(previousStackTags, props["Tags"])
	}
	effectiveTagsUnchanged := reflect.DeepEqual(currentTags, previousTags)
	return effectiveTagsUnchanged && (oldHash == "" || oldHash == hashProps(props))
}

// deleteResource tears down a provisioned resource, and reports whatever the
// handler answers.
//
// It used to report only a refusal by the resource itself (errDeletionBlocked)
// and swallow every other error, which meant DeleteStack recorded
// DELETE_COMPLETE over a resource whose delete had genuinely failed — and, with
// it, dropped the stack's record of the resource, the only thing that still
// said the resource was out there. The filter was there to keep a resource that
// was already gone from wedging a teardown, back when a handler could not tell
// the two apart. Every handler now can: teardownError answers nil for a
// resource that is absent and reports everything else, so the failures reaching
// here are resources that are still standing.
//
// What each caller does with that is its own decision, and they differ as they
// do on AWS: DeleteStack stops, because a teardown that cannot finish must not
// claim it did, while the update cleanup phase records DELETE_FAILED on the
// resource and lets an update that already succeeded complete.
func (p *provisioner) deleteResource(ctx context.Context, logicalID, resType, physicalID string, props map[string]any, rCtx *resolveContext) error {
	log := p.log.WithRecorder(ctx)
	p.mu.Lock()
	router := p.router
	p.mu.Unlock()
	if router == nil {
		return nil
	}

	handler, ok := p.resolveHandler(resType)
	if !ok {
		return nil // stub resources have nothing to delete
	}

	err := p.invokeDelete(ctx, handler, router, physicalID, props, rCtx)
	if err == nil {
		return nil
	}
	log.Warn("cfn: failed to delete resource",
		zap.String("type", resType),
		zap.String("logicalId", logicalID),
		zap.String("physicalId", physicalID),
		zap.Error(err))
	return err
}

// ── Helpers ────────────────────────────────────────────────────────────────

func (p *provisioner) buildResolveContext(stack *Stack, tmpl *Template) *resolveContext {
	params := make(map[string]string)
	for _, param := range stack.Parameters {
		params[param.Key] = param.Value
	}
	// Apply defaults for unset parameters. Empty string is a valid default —
	// CDK bootstrap templates rely on empty-string defaults for optional
	// parameters like FileAssetsBucketName so that Fn::Equals/Fn::If
	// resolves correctly.
	for name, def := range tmpl.Parameters {
		if _, ok := params[name]; !ok {
			params[name] = string(def.Default)
		}
	}

	region := stack.Region
	if region == "" {
		region = p.cfg.Region
	}

	// Record each declared parameter's type so Ref can give the value the
	// shape the type promises (a CommaDelimitedList resolves to a list).
	paramTypes := make(map[string]string, len(tmpl.Parameters))
	for name, def := range tmpl.Parameters {
		paramTypes[name] = def.Type
	}

	// Collect cross-stack exports for Fn::ImportValue resolution.
	exports := p.collectExports(stack)

	return &resolveContext{
		Region:             region,
		AccountID:          p.cfg.AccountID,
		StackName:          stack.StackName,
		StackID:            stack.StackID,
		ClientRequestToken: stack.ClientRequestToken,
		StackTags:          append([]Tag(nil), stack.Tags...),
		Params:             params,
		ParamTypes:         paramTypes,
		Resources:          make(map[string]string),
		Conditions:         evaluateConditions(tmpl.Conditions, params),
		Mappings:           tmpl.Mappings,
		Exports:            exports,
		DynamicRef:         p.dynamicRefResolver(region),
		StackOutput:        p.stackOutputResolver(),
	}
}

// resolveProperties returns the two forms of a resource's resolved properties,
// and surfaces any dynamic reference that could not be resolved.
//
// recorded has the intrinsics resolved but every "{{resolve:...}}" left exactly
// as the template wrote it. It is what gets hashed for change detection and
// what gets stored on the stack resource, because that is what CloudFormation
// itself compares — the literal reference string, not the value behind it:
// "Updating only the secret value in Secrets Manager doesn't automatically
// cause CloudFormation to retrieve the new value." Hashing the resolved form
// instead would mean a rotated secret made every stack think its database had
// changed, and — now that a MasterUsername change forces replacement — could
// replace a database because a secret rotated. It also keeps resolved secrets
// out of the store: for secure references AWS "never stores the actual secure
// string value... only the literal dynamic reference".
//
// expanded additionally has the references resolved, and is what the resource
// handler is given. That is the one place AWS does let the value reach: "the
// secret value may show up in the service whose resource it's being used in".
//
// Expansion errors are taken with the resource selected for dispatch so a
// failure cannot be attributed to whichever resource is processed next.
func (p *provisioner) resolveProperties(res TemplateResource, rCtx *resolveContext) (expanded, recorded map[string]any, err error) {
	recorded = resolveAllProperties(res.Properties, rCtx)
	expanded, err = expandResourceProperties(res.Type, recorded, rCtx)
	return expanded, recorded, err
}

// expandRecordedProperties resolves dynamic references only after
// CloudFormation has selected the containing resource for creation or update.
// No-op stack updates therefore avoid both expansion work and secret reads.
//
// resType is the resource type the properties belong to, which decides where
// an ssm-secure reference is allowed to appear; "" allows none.
//
// Callers that know they may be handling a custom resource should go through
// expandResourceProperties instead, which routes a secure reference to a
// rejection rather than resolving it.
func expandRecordedProperties(resType string, recorded map[string]any, rCtx *resolveContext) (expanded map[string]any, err error) {
	expanded, _ = expandDynamicRefs(resType, recorded, rCtx).(map[string]any)
	if expanded == nil {
		expanded = recorded
	}
	return expanded, rCtx.takeDynamicRefErr()
}

// collectExports gathers all cross-stack exports from completed stacks in the
// same region. Uses a background context so region is derived from the stack.
func (p *provisioner) collectExports(stack *Stack) map[string]string {
	region := stack.Region
	if region == "" {
		region = p.cfg.Region
	}
	ctx := middleware.ContextWithRegion(p.ctx, region)
	allExports, aerr := p.store.listExports(ctx)
	if aerr != nil {
		return nil
	}
	exports := make(map[string]string, len(allExports))
	for _, e := range allExports {
		exports[e.Name] = e.Value
	}
	return exports
}

func (p *provisioner) resolveOutputs(tmpl *Template, rCtx *resolveContext) ([]Output, error) {
	if tmpl.Outputs == nil {
		return nil, nil
	}
	outputs := make([]Output, 0, len(tmpl.Outputs))
	for name, o := range tmpl.Outputs {
		if o.Condition != "" && !rCtx.Conditions[o.Condition] {
			continue
		}
		// Dynamic references are deliberately not expanded in outputs. Real
		// CloudFormation leaves them as written here — an output built from a
		// secretsmanager reference comes back as the literal
		// "{{resolve:secretsmanager:...}}" string, with no error — and an
		// output is exactly where a resolved secret would be most exposed:
		// DescribeStacks returns it, and CloudFormation "doesn't redact or
		// obfuscate any information you include in the Outputs section".
		val := resolveIntrinsics(o.Value, rCtx)
		out := Output{
			Key:         name,
			Value:       fmt.Sprintf("%v", val),
			Description: o.Description,
		}
		if o.Export != nil {
			out.ExportName = fmt.Sprintf("%v", resolveIntrinsics(o.Export.Name, rCtx))
		}
		// The value or the export name may carry an Fn::GetStackOutput; a
		// failure is taken here so it is attributed to this output rather than
		// left for whatever resolves next.
		if err := rCtx.takeDynamicRefErr(); err != nil {
			return nil, fmt.Errorf("output %s: %w", name, err)
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}

// createFailureSummary renders the stack-level StackStatusReason AWS sets on a
// stack left in CREATE_FAILED because rollback was disabled — a summary naming
// the logical IDs, not the service error that caused it. The error itself stays
// on the resource record and on its CREATE_FAILED event, which is where AWS
// keeps it and where a client looks for the detail.
//
// Provisioning stops at the first failed resource, so the list AWS renders as
// "[MyBucket, MyQueue]" always has a single entry here.
//
// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/stack-failure-options.html
func createFailureSummary(logicalID string) string {
	return fmt.Sprintf("The following resource(s) failed to create: [%s]", logicalID)
}

// failStack moves a stack to a terminal failure status and records why.
//
// The reason is logged as well as recorded on the stack, because the two are
// read in different places and only one of them is the whole story. A stack
// event says what failed; the log line lands in the trace of the request that
// started the operation (the provisioning goroutine carries its recorder — see
// provisionAsync), next to every internal call the operation made. A reader who
// has the stack event has to be able to get from it to that trace, and a reader
// who has the trace has to find the failure in it without knowing to go and
// look at DescribeStackEvents.
func (p *provisioner) failStack(ctx context.Context, stack *Stack, status, reason string) {
	stack.Status = status
	stack.StatusReason = reason
	p.log.WithRecorder(ctx).Error("cfn: stack failed",
		zap.String("stack", stack.StackName),
		zap.String("status", status),
		zap.String("reason", reason))
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", status, reason)
	p.persistTerminalState(ctx, stack)
	p.publishStackEvent(ctx, events.CFNStackFailed, stack)
}

// persistTerminalState pushes a stack that has just reached a terminal status
// out to the persistent store, so a restart straight after the operation
// returns still finds it.
//
// A flush that does not complete is reported and nothing more. It used to fail
// the stack — CREATE_FAILED with "persistent state flush failed: context
// deadline exceeded" — which was the wrong reading of what a failed flush
// means. Every resource in the stack exists and answers requests; the flush
// decides only whether the record of them is in SQLite yet. And it is not lost
// either way: HybridStore.flushOnce puts an uncommitted batch back at the head
// of the pending queue and leaves the pending log untruncated, so the periodic
// flusher retries it and an unclean exit replays it. Failing a stack that is
// fully provisioned, over state that is still on its way to disk, rolls back a
// deploy that had already succeeded.
//
// The flush is bounded because the caller is a provisioning goroutine an SDK
// waiter is watching, not because five seconds means anything: this store-wide
// flush carries every service's pending writes, and a deploy that has just
// uploaded its assets can easily have more than five seconds of them queued.
// Whether the store is keeping up is a question the store answers — see
// PersistentHealth's pendingWrites and DebugMetrics.FlushHistory — and it must
// not be answered through a stack's status.
func (p *provisioner) persistTerminalState(ctx context.Context, stack *Stack) {
	if err := p.flushCriticalState(ctx); err != nil {
		p.log.WithRecorder(ctx).Warn("cfn: terminal stack state not yet persisted",
			zap.String("stack", stack.StackName),
			zap.String("status", stack.Status),
			zap.Error(err))
	}
}

func (p *provisioner) flushCriticalState(ctx context.Context) error {
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return p.store.flush(flushCtx)
}

type createRollbackOptions struct {
	retainExceptOnCreate bool
}

// rollbackCreate is the default failure handler for CreateStack.
// It deletes created resources in reverse order while honouring their deletion
// policies, then completes or fails the rollback according to deletion results.
func (p *provisioner) rollbackCreate(
	ctx context.Context,
	stack *Stack,
	rCtx *resolveContext,
	reason string,
	opts createRollbackOptions,
) {
	log := p.log.WithRecorder(ctx)
	stack.Status = StatusRollbackInProgress
	stack.StatusReason = reason
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusRollbackInProgress, reason)
	p.publishStackEvent(ctx, events.CFNStackFailed, stack)

	rollbackFailed := false
	for i := len(stack.Resources) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			rollbackFailed = true
			break
		}
		r := stack.Resources[i]
		if !r.existsServiceSide() {
			continue
		}
		if r.shouldRetainOnCreateRollback(opts.retainExceptOnCreate) {
			log.Info("cfn: retaining resource on create rollback (DeletionPolicy=Retain)",
				zap.String("type", r.Type),
				zap.String("logicalId", r.LogicalID),
				zap.String("physicalId", r.PhysicalID))
			stack.Resources[i].Status = ResourceDeleteSkipped
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteSkipped, "DeletionPolicy=Retain")
			continue
		}
		stack.Resources[i].Status = ResourceDeleteInProgress
		p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteInProgress, "")

		handler, ok := p.resolveHandler(r.Type)
		if !ok {
			stack.Resources[i].Status = ResourceDeleteComplete
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteComplete, "")
			continue
		}
		p.mu.Lock()
		router := p.router
		p.mu.Unlock()
		if err := p.invokeDelete(ctx, handler, router, r.PhysicalID, r.Properties, rCtx); err != nil {
			log.Warn("cfn: rollback: failed to delete resource",
				zap.String("logicalId", r.LogicalID),
				zap.String("type", r.Type),
				zap.Error(err))
			stack.Resources[i].Status = ResourceDeleteFailed
			stack.Resources[i].StatusReason = err.Error()
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteFailed, err.Error())
			rollbackFailed = true
		} else {
			stack.Resources[i].Status = ResourceDeleteComplete
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteComplete, "")
			p.publishResourceEvent(ctx, events.CFNResourceDeleted, stack.StackName, r.LogicalID, r.Type, r.PhysicalID)
		}
	}

	if rollbackFailed {
		stack.Status = StatusRollbackFailed
	} else {
		stack.Status = StatusRollbackComplete
		stack.StatusReason = ""
	}
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", stack.Status, stack.StatusReason)
	if err := p.flushCriticalState(ctx); err != nil {
		log.Warn("cfn: failed to flush rollback state", zap.String("stack", stack.StackName), zap.Error(err))
	}
	p.publishStackEvent(ctx, events.CFNStackFailed, stack)
}

// rollbackUpdate is the default failure handler for UpdateStack.
//
// It undoes the update in reverse order and restores the previous resource
// list, marking the stack UPDATE_ROLLBACK_COMPLETE when every resource is
// restored. Four kinds of resource are handled:
//
//   - Resources created by this update (absent before it) are deleted, unless
//     the template marks them Retain and the operation did not ask for
//     RetainExceptOnCreate — the same decision, over the same helper, that the
//     create rollback makes.
//   - Resources this update *replaced* are rolled back to the original: the
//     replacement that was created is deleted, and the original — still alive,
//     because replacement creates before deleting and defers the delete to the
//     post-success cleanup — is what the stack keeps. This is why a failed
//     replacement leaves the resource intact rather than destroying it.
//   - Resources successfully updated in place are passed back through their
//     resourceUpdater with the previous properties.
//   - Resources whose updater could not compensate an applied mutation, or
//     whose reverse update fails here, are retained as UPDATE_FAILED and make
//     the rollback fail truthfully.
//
// The stack's own metadata is restored alongside them, from
// previousGeneration: the update overwrote the record with what it was
// attempting before any of this began.
//
// replacedBy maps a logical ID to the physical ID of the replacement created
// for it, for exactly that second case.
//
// The retention in the first case reads stack.RetainExceptOnCreate directly
// rather than taking options the way rollbackCreate does, because an update has
// no equivalent of createRollbackOptions to thread it through. A retained
// resource is dropped from the restored list along with the rest of the
// attempted ones: it is orphaned, exactly as DELETE_SKIPPED means on the create
// and delete paths, and the stack that reports UPDATE_ROLLBACK_COMPLETE is the
// pre-update one.
//
// The second case stays unconditional, and DeletionPolicy is not the policy
// that would speak to it. The original is alive and is what the stack rolls
// back to, so keeping the replacement as well would leave two resources where
// the template asks for one, the second under a name the restored stack does
// not record — the orphan RetainExceptOnCreate exists to prevent, not one to
// produce. UpdateReplacePolicy is the policy for a replacement, and
// shouldRetainOnReplace already applies it to the original on the way forward.
func (p *provisioner) rollbackUpdate(ctx context.Context, stack *Stack, attempted []StackResource, previous map[string]StackResource, replacedBy map[string]string, inPlaceUpdated, dirtyUpdates map[string]bool, rCtx *resolveContext, previousGeneration stackGeneration, reason string) {
	log := p.log.WithRecorder(ctx)
	stack.Status = StatusUpdateRollbackInProgress
	stack.StatusReason = reason
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusUpdateRollbackInProgress, reason)
	p.publishStackEvent(ctx, events.CFNStackFailed, stack)

	// Removing what the failed update created is its own reported phase, just
	// as it is on the success path.
	stack.Status = StatusUpdateRollbackCompleteCleanupInProgress
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack",
		StatusUpdateRollbackCompleteCleanupInProgress, reason)

	rollbackFailed := false
	var rollbackErr error
	// Track resources that were created during the failed update but could
	// not be deleted during rollback. Real CloudFormation keeps these in the
	// stack's resource list with status DELETE_FAILED so subsequent
	// operations can see them (instead of treating them as new and double-
	// creating against an orphaned service-side resource).
	var orphaned []StackResource
	dirtyResources := make(map[string]StackResource, len(dirtyUpdates))
	// Delete newly created resources in reverse order. Resources that existed
	// before the update (present in `previous`) are left untouched.
	for i := len(attempted) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			rollbackFailed = true
			break
		}
		r := attempted[i]

		// Replaced by this update: the replacement we created must go, and the
		// original — still alive, because replacement defers its delete to the
		// post-success cleanup — is what the stack rolls back to.
		//
		// Checked before `previous` deliberately: the caller removes entries
		// from that map as it processes them, so a resource updated earlier in
		// this run is no longer in it.
		if newPhysID, wasReplaced := replacedBy[r.LogicalID]; wasReplaced && newPhysID != "" {
			handler, ok := p.resolveHandler(r.Type)
			if !ok {
				continue
			}
			p.mu.Lock()
			router := p.router
			p.mu.Unlock()
			p.recordEvent(ctx, stack, r.LogicalID, newPhysID, r.Type, ResourceDeleteInProgress, "")
			if err := p.invokeDelete(ctx, handler, router, newPhysID, r.Properties, rCtx); err != nil {
				log.Warn("cfn: update rollback: failed to delete replacement",
					zap.String("logicalId", r.LogicalID),
					zap.String("type", r.Type),
					zap.String("physicalId", newPhysID),
					zap.Error(err))
				p.recordEvent(ctx, stack, r.LogicalID, newPhysID, r.Type, ResourceDeleteFailed, err.Error())
				rollbackFailed = true
				continue
			}
			p.recordEvent(ctx, stack, r.LogicalID, newPhysID, r.Type, ResourceDeleteComplete, "")
			continue
		}

		// Present in the pre-update list *and* backed by a resource then. A
		// record the update inherited with nothing behind it — a create that had
		// failed before naming anything, or one an earlier rollback deleted — is
		// a resource this update created from scratch, whatever the map says, so
		// it belongs in the delete pass below. Reading membership alone left it
		// standing while the restored list disowned it.
		if old, wasExisting := previous[r.LogicalID]; wasExisting && old.existsServiceSide() {
			if dirtyUpdates[r.LogicalID] {
				rollbackFailed = true
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf(
					"restore %s: resource may still be mutated after failed compensation: %s",
					r.LogicalID, r.StatusReason))
				dirtyResources[r.LogicalID] = r
				continue
			}
			if inPlaceUpdated[r.LogicalID] && r.PhysicalID == old.PhysicalID {
				if err := p.rollbackInPlaceUpdate(ctx, stack, r, old, rCtx); err != nil {
					rollbackFailed = true
					rollbackErr = errors.Join(rollbackErr, err)
					r.Status = ResourceUpdateFailed
					r.StatusReason = err.Error()
					r.Timestamp = p.clk.Now()
					dirtyResources[r.LogicalID] = r
				}
			}
			continue
		}
		if !r.existsServiceSide() {
			continue
		}
		// A resource this update created is still a resource the template
		// asked to keep, and the same operation flag opts it back out — the
		// create path makes exactly this decision, over the same helper.
		// Skipping it here also drops the record: the restore below rebuilds
		// the list from `previous`, so the resource is orphaned rather than
		// left in a stack that no longer provisions it, which is what
		// DELETE_SKIPPED means everywhere else.
		if r.shouldRetainOnCreateRollback(stack.RetainExceptOnCreate) {
			log.Info("cfn: retaining resource on update rollback (DeletionPolicy=Retain)",
				zap.String("type", r.Type),
				zap.String("logicalId", r.LogicalID),
				zap.String("physicalId", r.PhysicalID))
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteSkipped, "DeletionPolicy=Retain")
			continue
		}
		handler, ok := p.resolveHandler(r.Type)
		if !ok {
			continue
		}
		p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteInProgress, "")
		p.mu.Lock()
		router := p.router
		p.mu.Unlock()
		if err := p.invokeDelete(ctx, handler, router, r.PhysicalID, r.Properties, rCtx); err != nil {
			log.Warn("cfn: update rollback: failed to delete new resource",
				zap.String("logicalId", r.LogicalID),
				zap.String("type", r.Type),
				zap.Error(err))
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteFailed, err.Error())
			rollbackFailed = true
			// Keep the resource in stack state so the next UpdateStack routes
			// through the update/skip path instead of provisioning fresh.
			r.Status = ResourceDeleteFailed
			r.StatusReason = err.Error()
			r.Timestamp = p.clk.Now()
			orphaned = append(orphaned, r)
		} else {
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteComplete, "")
			p.publishResourceEvent(ctx, events.CFNResourceDeleted, stack.StackName, r.LogicalID, r.Type, r.PhysicalID)
		}
	}

	// Restore the pre-update resource list, plus any orphaned resources that
	// could not be cleaned up. This matches real AWS: UPDATE_ROLLBACK leaves
	// failed-delete resources visible in the stack so they're not double-
	// created on the next attempt.
	restored := make([]StackResource, 0, len(previous)+len(orphaned))
	for logicalID, res := range previous {
		if dirty, ok := dirtyResources[logicalID]; ok {
			restored = append(restored, dirty)
			continue
		}
		restored = append(restored, res)
	}
	restored = append(restored, orphaned...)
	stack.Resources = restored

	// The resources are only half of what the update changed. The record was
	// overwritten with the attempted template, parameters and tags before the
	// first resource was touched, so a stack left holding them would report the
	// attempt that failed over resources that have been put back: GetTemplate
	// would serve the template that failed, and the next update would resolve
	// its parameters from it.
	if err := p.restoreStackGeneration(ctx, stack, previousGeneration); err != nil {
		log.Warn("cfn: update rollback: failed to restore stack metadata",
			zap.String("stack", stack.StackName), zap.Error(err))
		rollbackFailed = true
		rollbackErr = errors.Join(rollbackErr, err)
	}

	if rollbackFailed {
		stack.Status = StatusUpdateRollbackFailed
		if rollbackErr != nil {
			stack.StatusReason = fmt.Sprintf("%s; rollback failed: %v", reason, rollbackErr)
		}
	} else {
		stack.Status = StatusUpdateRollbackComplete
		stack.StatusReason = ""
	}
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", stack.Status, stack.StatusReason)
	if err := p.flushCriticalState(ctx); err != nil {
		log.Warn("cfn: failed to flush update rollback state", zap.String("stack", stack.StackName), zap.Error(err))
	}
	p.publishStackEvent(ctx, events.CFNStackFailed, stack)
}

// restoreStackGeneration puts the template, parameters and tags a failed update
// superseded back on the stack, and persists them.
//
// The write is made here rather than left to the caller's terminal recordEvent
// because its outcome decides that terminal status: a restoration that did not
// reach the store has not happened, and the stack must not then report
// UPDATE_ROLLBACK_COMPLETE over a record that still describes the attempt.
func (p *provisioner) restoreStackGeneration(ctx context.Context, stack *Stack, previous stackGeneration) error {
	stack.TemplateBody = previous.TemplateBody
	stack.Parameters = append([]Parameter(nil), previous.Parameters...)
	stack.Tags = append([]Tag(nil), previous.Tags...)
	if err := p.store.putStack(ctx, stack); err != nil {
		return fmt.Errorf("restore stack metadata: %w", err)
	}
	return nil
}

// rollbackInPlaceUpdate restores a resource that was successfully changed
// before a later resource failed. Reusing the resourceUpdater in reverse keeps
// validation, persistence, and service behavior out of CloudFormation.
func (p *provisioner) rollbackInPlaceUpdate(ctx context.Context, stack *Stack, attempted, previous StackResource, rCtx *resolveContext) error {
	p.recordEvent(ctx, stack, previous.LogicalID, previous.PhysicalID, previous.Type, ResourceUpdateInProgress, "Update rollback initiated")
	handler, ok := p.resolveHandler(previous.Type)
	if !ok {
		return p.failRollbackInPlaceUpdate(ctx, stack, previous, errors.New("resource handler is unavailable"))
	}
	updater, ok := handler.(resourceUpdater)
	if !ok {
		return p.failRollbackInPlaceUpdate(ctx, stack, previous, errors.New("resource handler cannot update in place"))
	}
	p.mu.Lock()
	router := p.router
	p.mu.Unlock()
	if router == nil {
		return p.failRollbackInPlaceUpdate(ctx, stack, previous, errors.New("router not initialised"))
	}

	rollbackCtx := *rCtx
	rollbackCtx.StackTags = append([]Tag(nil), rCtx.PreviousStackTags...)
	rollbackCtx.PreviousStackTags = append([]Tag(nil), rCtx.StackTags...)
	physicalID, _, err := updater.Update(ctx, router, p.cfg, previous.PhysicalID, previous.Properties, attempted.Properties, &rollbackCtx)
	if err == nil && physicalID != "" && physicalID != previous.PhysicalID {
		err = fmt.Errorf("rollback changed physical ID from %q to %q", previous.PhysicalID, physicalID)
	}
	if err == nil {
		if stabilizer, ok := handler.(resourceStabilizer); ok {
			err = stabilizer.Stabilize(ctx, router, p.cfg, p.clk, previous.PhysicalID, rCtx)
		}
	}
	if err != nil {
		return p.failRollbackInPlaceUpdate(ctx, stack, previous, err)
	}
	if rCtx.Attributes == nil {
		rCtx.Attributes = make(map[string]map[string]string)
	}
	rCtx.Attributes[previous.LogicalID] = previous.Attributes
	rCtx.Resources[previous.LogicalID] = previous.PhysicalID
	p.recordEvent(ctx, stack, previous.LogicalID, previous.PhysicalID, previous.Type, ResourceUpdateComplete, "")
	return nil
}

func (p *provisioner) failRollbackInPlaceUpdate(ctx context.Context, stack *Stack, previous StackResource, err error) error {
	rollbackErr := fmt.Errorf("restore %s: %w", previous.LogicalID, err)
	p.recordEvent(ctx, stack, previous.LogicalID, previous.PhysicalID, previous.Type, ResourceUpdateFailed, rollbackErr.Error())
	return rollbackErr
}

// ── Rollback stack (async) ─────────────────────────────────────────────────

type rollbackStackOptions struct {
	createPath           bool
	retainExceptOnCreate bool
}

// rollbackStack services an operator-requested RollbackStack, mirroring the
// async shape of createStack/updateStack so that a fast rollback is already
// terminal by the time the caller's next DescribeStacks lands.
//
// createPath selects the CREATE_FAILED → ROLLBACK_COMPLETE flow (unwind
// everything the failed create built) over the UPDATE_FAILED →
// UPDATE_ROLLBACK_COMPLETE flow.
func (p *provisioner) rollbackStack(stack *Stack, opts rollbackStackOptions, rec *trace.Recorder) {
	p.provisionAsync(stack, rec, func(ctx context.Context) {
		p.rollbackStackResourcesCtx(ctx, stack, opts)
	})
}

// rollbackStackResourcesCtx is the synchronous core of an explicit rollback.
func (p *provisioner) rollbackStackResourcesCtx(
	ctx context.Context,
	stack *Stack,
	opts rollbackStackOptions,
) {
	rCtx := p.rollbackResolveContext(stack)

	if opts.createPath {
		// A failed create unwinds exactly like an automatic create rollback.
		p.rollbackCreate(ctx, stack, rCtx, "User Initiated", createRollbackOptions{
			retainExceptOnCreate: opts.retainExceptOnCreate,
		})
		return
	}
	p.rollbackToStable(ctx, stack, rCtx, "User Initiated", nil)
}

// rollbackResolveContext builds the resolve context a rollback's deletes run
// under. The stored template drives Ref/GetAtt resolution; a stack that failed
// early may have no usable template, and an empty one still yields a valid
// context because the resource list — not the template — is the source of
// truth for what has to be retired.
func (p *provisioner) rollbackResolveContext(stack *Stack) *resolveContext {
	tmpl, err := parseTemplate(stack.TemplateBody)
	if err != nil || tmpl == nil {
		tmpl = &Template{Resources: map[string]TemplateResource{}}
	}
	rCtx := p.buildResolveContext(stack, tmpl)
	for _, r := range stack.Resources {
		if r.PhysicalID != "" {
			rCtx.Resources[r.LogicalID] = r.PhysicalID
		}
	}
	return rCtx
}

// ── Continue update rollback (async) ───────────────────────────────────────

// resourceSkip is one resolved entry of ContinueUpdateRollback's
// ResourcesToSkip: a logical ID, paired with the stack that actually holds it
// — the stack the request named, or one of the nested stacks beneath it. The
// wire value the caller sent is not kept, because the only thing that reads it
// is the validation that produced this, and it still has it.
type resourceSkip struct {
	stackName string
	logicalID string
}

// maxContinueRollbackDepth caps how far continueUpdateRollbackCtx descends
// through nested stacks. Nesting is finite in practice — AWS's own limit is
// five levels — and a bound here means a cycle in stored state (a child whose
// resource list names an ancestor, which no correct provisioning writes but a
// hand-edited store could) costs a bounded walk rather than the goroutine's
// stack.
const maxContinueRollbackDepth = 8

// continueUpdateRollback services ContinueUpdateRollback, mirroring the async
// shape of the other stack operations so that a fast rollback is already
// terminal by the time the caller's next DescribeStacks lands.
func (p *provisioner) continueUpdateRollback(stack *Stack, skips []resourceSkip, rec *trace.Recorder) {
	p.provisionAsync(stack, rec, func(ctx context.Context) {
		p.continueUpdateRollbackCtx(ctx, stack, skips, 0)
	})
}

// continueUpdateRollbackCtx retries the rollback for one stack and every nested
// stack under it that is still wedged.
//
// The children go first, and they have to: a parent's nested stack resource is
// only as rolled back as the child behind it, so completing the parent over a
// child still in UPDATE_ROLLBACK_FAILED would report a recovery that half the
// stack has not made — and would leave the child visible in DescribeStacks as
// permanently stuck, with no operation left that accepts it. This is also what
// makes a nested ResourcesToSkip member mean anything: the skip is applied by
// the child's own rollback, not the parent's.
func (p *provisioner) continueUpdateRollbackCtx(ctx context.Context, stack *Stack, skips []resourceSkip, depth int) {
	if depth < maxContinueRollbackDepth {
		for _, child := range p.wedgedChildStacks(ctx, stack, skips) {
			// The child was left behind by an earlier operation and still
			// carries that operation's token. This retry belongs to the
			// caller's, and the child's events have to say so.
			child.ClientRequestToken = stack.ClientRequestToken
			p.continueUpdateRollbackCtx(ctx, child, skips, depth+1)
		}
	}
	p.rollbackToStable(ctx, stack, p.rollbackResolveContext(stack), "User Initiated", skipsForStack(skips, stack.StackName))
}

// wedgedChildStacks returns the nested stacks under stack that this operation
// has to continue: the ones a failed rollback left in UPDATE_ROLLBACK_FAILED,
// plus any the caller named a resource inside via ResourcesToSkip.
func (p *provisioner) wedgedChildStacks(ctx context.Context, stack *Stack, skips []resourceSkip) []*Stack {
	var children []*Stack
	for _, r := range stack.Resources {
		if r.Type != "AWS::CloudFormation::Stack" || r.PhysicalID == "" {
			continue
		}
		childName := stackNameFromARN(r.PhysicalID)
		if childName == "" {
			continue
		}
		child, _ := p.store.getStack(ctx, childName)
		if child == nil {
			continue
		}
		if child.Status != StatusUpdateRollbackFailed && len(skipsForStack(skips, childName)) == 0 {
			continue
		}
		children = append(children, child)
	}
	return children
}

// skipsForStack narrows a resolved ResourcesToSkip list to the logical IDs that
// belong to one stack.
func skipsForStack(skips []resourceSkip, stackName string) map[string]bool {
	var out map[string]bool
	for _, s := range skips {
		if s.stackName != stackName {
			continue
		}
		if out == nil {
			out = make(map[string]bool, len(skips))
		}
		out[s.logicalID] = true
	}
	return out
}

// rollbackToStable services RollbackStack for a stack whose update failed, or
// whose automatic update rollback failed and is being retried — and
// ContinueUpdateRollback, which is the same retry with a skip list.
//
// Overcast keeps no snapshot of each resource's pre-update properties, so it
// cannot literally restore prior configuration the way real CloudFormation
// does. What it can do — and what unblocks a client stuck behind
// UPDATE_FAILED — is retire the resources the failed attempt left in a failed
// state and drive the stack to a terminal UPDATE_ROLLBACK_COMPLETE.
//
// skip holds the logical IDs ContinueUpdateRollback was asked to leave alone,
// and is nil for every other caller. See the loop for what skipping one means.
func (p *provisioner) rollbackToStable(ctx context.Context, stack *Stack, rCtx *resolveContext, reason string, skip map[string]bool) {
	log := p.log.WithRecorder(ctx)
	// An explicit RollbackStack on a stack a failed update left UPDATE_FAILED
	// is the second — and last — chance to read the evidence: the resources
	// that failed are still standing at this point, and the loop below is what
	// removes them. If this capture finds nothing because the service records
	// have since been reaped, the entry the original failure wrote is left
	// alone; see recordDeployDiagnostics.
	diagnosis := p.collectDeployDiagnostics(ctx, stack, deployOperationUpdate, stack.Resources)
	defer p.recordDeployDiagnostics(ctx, stack, diagnosis)
	stack.Status = StatusUpdateRollbackInProgress
	stack.StatusReason = reason
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", StatusUpdateRollbackInProgress, reason)
	p.publishStackEvent(ctx, events.CFNStackFailed, stack)

	rollbackFailed := false
	// Walk in reverse so deletes happen in reverse dependency order, then flip
	// the survivors back to template order at the end.
	kept := make([]StackResource, 0, len(stack.Resources))
	for i := len(stack.Resources) - 1; i >= 0; i-- {
		r := stack.Resources[i]
		if ctx.Err() != nil {
			rollbackFailed = true
			kept = append(kept, r)
			continue
		}

		if skip[r.LogicalID] {
			// AWS's own description of what skipping does, and it is
			// deliberately a lie about the resource in exchange for the truth
			// about the stack: "CloudFormation sets the status of the
			// specified resources to UPDATE_COMPLETE and continues to roll
			// back the stack. After the rollback is complete, the state of the
			// skipped resources will be inconsistent with the state of the
			// resources in the stack template." The physical resource is left
			// exactly as the failed attempt left it — untouched, not retried —
			// which is the point: the operator has decided this one is theirs
			// to deal with, and wants the rest of the stack back.
			r.Status = ResourceUpdateComplete
			r.StatusReason = ""
			r.Timestamp = p.clk.Now()
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceUpdateComplete, "")
			kept = append(kept, r)
			continue
		}

		switch r.Status {
		case ResourceCreateFailed:
			// The failed attempt half-created this resource. With no physical
			// resource behind it there is nothing to retire and it simply
			// leaves the stack; otherwise delete it.
			if r.PhysicalID == "" {
				continue
			}
			if err := p.deleteRollbackResource(ctx, stack, r, rCtx); err != nil {
				r.Status = ResourceDeleteFailed
				r.StatusReason = err.Error()
				r.Timestamp = p.clk.Now()
				kept = append(kept, r)
				rollbackFailed = true
			}
		case ResourceDeleteFailed:
			// Orphaned by an earlier automatic rollback that could not clean
			// up. Retrying that delete is the point of an explicit rollback.
			if err := p.deleteRollbackResource(ctx, stack, r, rCtx); err != nil {
				r.StatusReason = err.Error()
				r.Timestamp = p.clk.Now()
				kept = append(kept, r)
				rollbackFailed = true
			}
		case ResourceUpdateFailed:
			// The physical resource keeps whatever the failed update left on
			// it (see the method comment). Record the AWS-observable outcome —
			// the resource is back under the stack's last known stable state —
			// rather than leaving a failed status that blocks future updates.
			r.Status = ResourceUpdateComplete
			r.StatusReason = ""
			r.Timestamp = p.clk.Now()
			p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceUpdateComplete, "")
			kept = append(kept, r)
		default:
			kept = append(kept, r)
		}
	}
	slices.Reverse(kept)
	stack.Resources = kept

	if rollbackFailed {
		stack.Status = StatusUpdateRollbackFailed
	} else {
		stack.Status = StatusUpdateRollbackComplete
		stack.StatusReason = ""
	}
	p.recordEvent(ctx, stack, stack.StackName, stack.StackID, "AWS::CloudFormation::Stack", stack.Status, stack.StatusReason)
	if err := p.flushCriticalState(ctx); err != nil {
		log.Warn("cfn: failed to flush rollback state", zap.String("stack", stack.StackName), zap.Error(err))
	}
	p.publishStackEvent(ctx, events.CFNStackFailed, stack)
}

// deleteRollbackResource deletes one resource as part of an explicit rollback,
// emitting the DELETE_IN_PROGRESS / DELETE_COMPLETE / DELETE_FAILED events that
// DescribeStackEvents surfaces. A resource type with no registered handler is
// treated as already gone — the same allowance rollbackCreate makes.
func (p *provisioner) deleteRollbackResource(ctx context.Context, stack *Stack, r StackResource, rCtx *resolveContext) error {
	log := p.log.WithRecorder(ctx)
	p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteInProgress, "")

	handler, ok := p.resolveHandler(r.Type)
	if !ok {
		p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteComplete, "")
		return nil
	}

	p.mu.Lock()
	router := p.router
	p.mu.Unlock()

	if err := p.invokeDelete(ctx, handler, router, r.PhysicalID, r.Properties, rCtx); err != nil {
		log.Warn("cfn: rollback: failed to delete resource",
			zap.String("logicalId", r.LogicalID),
			zap.String("type", r.Type),
			zap.Error(err))
		p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteFailed, err.Error())
		return err
	}

	p.recordEvent(ctx, stack, r.LogicalID, r.PhysicalID, r.Type, ResourceDeleteComplete, "")
	p.publishResourceEvent(ctx, events.CFNResourceDeleted, stack.StackName, r.LogicalID, r.Type, r.PhysicalID)
	return nil
}

func (p *provisioner) publishStackEvent(ctx context.Context, t events.Type, stack *Stack) {
	p.mu.Lock()
	bus := p.bus
	p.mu.Unlock()
	if bus == nil {
		return
	}
	bus.Publish(ctx, events.Event{
		Type:    t,
		Source:  "cloudformation",
		Payload: events.CFNStackPayload{StackName: stack.StackName, Status: stack.Status},
	})
}

func (p *provisioner) publishResourceEvent(ctx context.Context, t events.Type, stackName, logicalID, resType, physicalID string) {
	p.mu.Lock()
	bus := p.bus
	p.mu.Unlock()
	if bus == nil {
		return
	}
	bus.Publish(ctx, events.Event{
		Type:   t,
		Source: "cloudformation",
		Payload: events.CFNResourcePayload{
			StackName:         stackName,
			LogicalResourceID: logicalID,
			ResourceType:      resType,
			PhysicalID:        physicalID,
		},
	})
}

// recordEvent appends an immutable lifecycle event to the stack's separate
// event store and persists the current stack metadata. All provisioning state
// transitions call this so that DescribeStackEvents always returns accurate,
// ordered history without embedding the growing event list in the stack blob.
func (p *provisioner) recordEvent(ctx context.Context, stack *Stack, logicalID, physicalID, resType, status, reason string) {
	log := p.log.WithRecorder(ctx)
	event := StackEvent{
		EventID:              uuid.New().String(),
		StackID:              stack.StackID,
		StackName:            stack.StackName,
		LogicalResourceID:    logicalID,
		PhysicalResourceID:   physicalID,
		ResourceType:         resType,
		ResourceStatus:       status,
		ResourceStatusReason: reason,
		Timestamp:            p.clk.Now(),
		ClientRequestToken:   stack.ClientRequestToken,
	}
	if err := p.store.appendStackEvent(ctx, stack.StackID, event); err != nil {
		log.Error("cfn: failed to persist stack event", zap.Error(err))
	}
	if err := p.store.putStack(ctx, stack); err != nil {
		log.Error("cfn: failed to persist stack state", zap.Error(err))
	}
}

// evaluateConditions evaluates template conditions against parameters.
// For simplicity, we support Fn::Equals only (the most common condition).
func evaluateConditions(conditions map[string]any, params map[string]string) map[string]bool {
	result := make(map[string]bool, len(conditions))
	for name, cond := range conditions {
		result[name] = evalCondition(cond, params)
	}
	return result
}

func evalCondition(cond any, params map[string]string) bool {
	m, ok := cond.(map[string]any)
	if !ok {
		return false
	}
	if eq, ok := m["Fn::Equals"]; ok {
		arr, ok := eq.([]any)
		if !ok || len(arr) != 2 {
			return false
		}
		a := resolveConditionValue(arr[0], params)
		b := resolveConditionValue(arr[1], params)
		return a == b
	}
	if not, ok := m["Fn::Not"]; ok {
		arr, ok := not.([]any)
		if !ok || len(arr) != 1 {
			return false
		}
		return !evalCondition(arr[0], params)
	}
	if and, ok := m["Fn::And"]; ok {
		arr, ok := and.([]any)
		if !ok {
			return false
		}
		for _, item := range arr {
			if !evalCondition(item, params) {
				return false
			}
		}
		return true
	}
	if or, ok := m["Fn::Or"]; ok {
		arr, ok := or.([]any)
		if !ok {
			return false
		}
		for _, item := range arr {
			if evalCondition(item, params) {
				return true
			}
		}
		return false
	}
	return false
}

func resolveConditionValue(v any, params map[string]string) string {
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if ref, ok := val["Ref"]; ok {
			name, _ := ref.(string)
			if p, ok := params[name]; ok {
				return p
			}
			return name
		}
	}
	return cfnScalarString(v)
}

// ── Topology sort ──────────────────────────────────────────────────────────

// topoSort returns a topological ordering of resources respecting DependsOn.
// implicitResourceDeps scans a property value tree and returns all logical
// resource IDs referenced via Ref, Fn::GetAtt, or Fn::Sub.  These create
// implicit dependency edges in real AWS CloudFormation — no explicit DependsOn
// is required when a resource references another via an intrinsic function.
func implicitResourceDeps(v any, resourceNames map[string]struct{}) []string {
	seen := map[string]struct{}{}
	collectResourceRefs(v, resourceNames, seen)
	result := make([]string, 0, len(seen))
	for k := range seen {
		result = append(result, k)
	}
	return result
}

func collectResourceRefs(v any, resourceNames map[string]struct{}, seen map[string]struct{}) {
	switch val := v.(type) {
	case map[string]any:
		if len(val) == 1 {
			// Ref
			if ref, ok := val["Ref"]; ok {
				if name, ok := ref.(string); ok {
					if _, is := resourceNames[name]; is {
						seen[name] = struct{}{}
					}
				}
				return
			}
			// Fn::GetAtt
			if ga, ok := val["Fn::GetAtt"]; ok {
				switch g := ga.(type) {
				case []any:
					if len(g) >= 1 {
						if name, ok := g[0].(string); ok {
							if _, is := resourceNames[name]; is {
								seen[name] = struct{}{}
							}
						}
					}
				case string:
					// "LogicalId.Attribute" form
					if dot := strings.IndexByte(g, '.'); dot > 0 {
						name := g[:dot]
						if _, is := resourceNames[name]; is {
							seen[name] = struct{}{}
						}
					}
				}
				return
			}
			// Fn::Sub
			if sub, ok := val["Fn::Sub"]; ok {
				collectSubResourceRefs(sub, resourceNames, seen)
				return
			}
		}
		// Not an intrinsic — recurse into all child values.
		for _, child := range val {
			collectResourceRefs(child, resourceNames, seen)
		}
	case []any:
		for _, item := range val {
			collectResourceRefs(item, resourceNames, seen)
		}
	}
}

// collectSubResourceRefs extracts resource logical IDs from an Fn::Sub value.
// It handles both the string form ("${LogicalId.Attr}") and the
// [string, {vars}] form, and also recurses into the variable-map values.
func collectSubResourceRefs(sub any, resourceNames map[string]struct{}, seen map[string]struct{}) {
	var tmplStr string
	switch val := sub.(type) {
	case string:
		tmplStr = val
	case []any:
		if len(val) >= 1 {
			if s, ok := val[0].(string); ok {
				tmplStr = s
			}
		}
		if len(val) >= 2 {
			// Variable map may itself contain intrinsics.
			collectResourceRefs(val[1], resourceNames, seen)
		}
	default:
		return
	}

	// Scan for ${VarName} and ${VarName.Attr} patterns.
	for {
		start := strings.Index(tmplStr, "${")
		if start < 0 {
			break
		}
		end := strings.Index(tmplStr[start:], "}")
		if end < 0 {
			break
		}
		varName := tmplStr[start+2 : start+end]
		// Strip .Attribute suffix if present.
		if dot := strings.IndexByte(varName, '.'); dot >= 0 {
			varName = varName[:dot]
		}
		if _, is := resourceNames[varName]; is {
			seen[varName] = struct{}{}
		}
		tmplStr = tmplStr[start+end+1:]
	}
}

func topoSort(resources map[string]TemplateResource) ([]string, error) {
	// Build a set of all resource logical IDs so we can distinguish resource
	// references from parameter/pseudo-parameter references in intrinsics.
	resourceNames := make(map[string]struct{}, len(resources))
	for name := range resources {
		resourceNames[name] = struct{}{}
	}

	deps := make(map[string][]string, len(resources))
	for name, res := range resources {
		explicit := parseDependsOn(res.DependsOn)
		implicit := implicitResourceDeps(res.Properties, resourceNames)

		// Merge, deduplicating, removing any self-reference.
		merged := make(map[string]struct{}, len(explicit)+len(implicit))
		for _, d := range explicit {
			if d != name {
				merged[d] = struct{}{}
			}
		}
		for _, d := range implicit {
			if d != name {
				merged[d] = struct{}{}
			}
		}
		all := make([]string, 0, len(merged))
		for d := range merged {
			all = append(all, d)
		}
		deps[name] = all
	}

	var result []string
	visited := make(map[string]int) // 0=unvisited, 1=visiting, 2=visited

	var visit func(string) error
	visit = func(name string) error {
		switch visited[name] {
		case 1:
			return fmt.Errorf("cycle at %s", name)
		case 2:
			return nil
		}
		visited[name] = 1
		for _, dep := range deps[name] {
			if _, ok := resources[dep]; !ok {
				continue // ignore missing deps
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		visited[name] = 2
		result = append(result, name)
		return nil
	}

	// Sort keys for deterministic order.
	keys := make([]string, 0, len(resources))
	for k := range resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, name := range keys {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func parseDependsOn(v any) []string {
	switch val := v.(type) {
	case string:
		if val != "" {
			return []string{val}
		}
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case []string:
		return val
	}
	return nil
}

// ── Resource handlers ──────────────────────────────────────────────────────

// resourceHandler defines how to create and delete a specific AWS resource type.
type resourceHandler interface {
	Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (physicalID string, attrs map[string]string, err error)
	Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error
}

// resourcePropertiesDeleter is implemented by resource handlers whose teardown
// needs the properties the resource was provisioned with, because the physical
// ID alone does not record what the resource attached itself to.
//
// AWS::IAM::Policy is the case this exists for: the resource is an inline
// policy document written onto the roles, users and groups its properties name,
// and nothing in its physical ID says which those were. Real CloudFormation's
// provider removes the document from each of them on delete — which is what
// lets the role underneath be deleted afterwards, now that IAM enforces
// DeleteConflict as AWS does.
//
// Handlers that do not implement this fall back to plain Delete.
type resourcePropertiesDeleter interface {
	DeleteWithProperties(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error
}

// invokeDelete tears down one resource, preferring the properties-aware
// teardown when the handler implements it. Every delete path goes through
// here so the choice is made in one place.
func (p *provisioner) invokeDelete(ctx context.Context, handler resourceHandler, router http.Handler, physicalID string, props map[string]any, rCtx *resolveContext) error {
	if pd, ok := handler.(resourcePropertiesDeleter); ok {
		return pd.DeleteWithProperties(ctx, router, p.cfg, physicalID, props, rCtx)
	}
	return handler.Delete(ctx, router, p.cfg, physicalID, rCtx)
}

// resourceUpdater is implemented by resource handlers that support in-place
// updates (e.g. Lambda function code/configuration). Handlers that do not
// implement this interface fall back to delete + create on UpdateStack.
type resourceUpdater interface {
	Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (newPhysicalID string, attrs map[string]string, err error)
}

// resourceStabilizer is implemented by resource handlers whose resource is not
// finished when the call that created or changed it returns. Real
// CloudFormation's resource-provider contract has the same split: a create
// reports IN_PROGRESS and is re-invoked until the resource settles, and nothing
// downstream of it runs until it does. An RDS DB instance reaching "available"
// and an ECS service reaching its desired count are what this exists for — in
// both cases the service API is asynchronous by design and answers long before
// the database or the container is usable.
//
// The provisioner calls Stabilize after Create and after a successful in-place
// Update so that a handler cannot implement one and forget the other, and so
// that the three rules a failed stabilization has to obey live in one place
// rather than in each handler:
//
//   - The resource is real. Create named it before the wait began, so the
//     physical ID travels out with the error and rollback deletes it. Dropping
//     it strands a database that nothing is left holding the name of.
//   - A failed update is terminal, never answered with a replacement. The
//     change was applied to the resource that exists; building a second one
//     fixes nothing (see updateFailure).
//   - The wait ends when the provisioner's context does, so a stuck resource
//     cannot outlive a shutdown.
//
// Stabilize must be safe to call on a resource that is already settled: the
// first status read may find that the service completed the work immediately.
type resourceStabilizer interface {
	Stabilize(ctx context.Context, router http.Handler, cfg *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error
}

// errReplacementRequired is returned by an Update implementation when one or
// more changed properties cannot be applied in place (for example renaming an
// SQS queue or changing a DynamoDB key schema). The provisioner reacts by
// taking the replacement path — create the new resource and delete the old
// one — which mirrors how AWS CloudFormation handles "Replacement: Yes"
// property changes.
var errReplacementRequired = errors.New("cfn: replacement required")

// errDeletionBlocked is returned by a Delete implementation when the resource
// itself refuses to be deleted: a non-empty S3 bucket, an RDS cluster with
// DeletionProtection enabled, an IAM entity IAM answers DeleteConflict for, an
// EC2 entity EC2 answers DependencyViolation for, or a nested stack whose own
// teardown failed. The provisioner reacts by failing the stack operation
// instead of reporting a deletion that did not happen, which is what AWS
// does: the delete fails, the resource survives, and the operator clears the
// block and tries again.
//
// It no longer decides whether a teardown stops — every error does that now,
// because every one of them means the resource is still standing. What it
// still marks is which kind of standing it is: a refusal is a lasting
// condition an operator has to clear, where a failure may be transient and a
// retry may be all it takes. That distinction reaches the operator through the
// error text, which becomes the resource's DELETE_FAILED status reason, and a
// nested stack carries its child's reason out under the same sentinel so the
// parent's event says what the child refused over.
var errDeletionBlocked = errors.New("cfn: deletion blocked by the resource")

// updateFailure marks an update error the provisioner must answer by failing
// the resource rather than by replacing it. Its dirty bit distinguishes a
// rejected update from one whose handler could not compensate an already
// applied mutation. The default for an update error is
// replacement, which is right when the update could not be applied — but wrong
// once it has been: a resource that applied its update and then failed to
// settle is not fixed by building a second copy of it, and CloudFormation does
// not try. An ECS service whose new deployment cannot start its tasks is the
// case this exists for; replacing it would create a second service under a
// generated name and delete the one that works.
type updateFailure struct {
	err   error
	dirty bool
}

func (e updateFailure) Error() string { return e.err.Error() }
func (e updateFailure) Unwrap() error { return e.err }

// failUpdate marks err as terminal for the resource — see updateFailure.
func failUpdate(err error) error { return updateFailure{err: err} }

// failDirtyUpdate marks a terminal update whose handler could not compensate
// a mutation it had already applied. Stack rollback must retain that resource
// as failed instead of claiming the previous state was restored.
func failDirtyUpdate(err error) error { return updateFailure{err: err, dirty: true} }

func isDirtyUpdateFailure(err error) bool {
	var failed updateFailure
	return errors.As(err, &failed) && failed.dirty
}

// asBool coerces a CFN property value (which may be a real bool, a string
// "true"/"false", or nil) to a boolean. Used in handler Update methods to
// detect immutable property changes.
func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	default:
		return false
	}
}

// resourceHandlers maps CloudFormation resource types to their handlers.
var resourceHandlers = map[string]resourceHandler{
	// SQS
	"AWS::SQS::Queue":       &sqsQueueHandler{},
	"AWS::SQS::QueuePolicy": &stubResourceHandler{},
	// SNS
	"AWS::SNS::Topic":        &snsTopicHandler{},
	"AWS::SNS::Subscription": &snsSubscriptionHandler{},
	// S3
	"AWS::S3::Bucket":       &s3BucketHandler{},
	"AWS::S3::BucketPolicy": &s3BucketPolicyHandler{},
	// DynamoDB
	"AWS::DynamoDB::Table": &dynamodbTableHandler{},
	// Lambda
	"AWS::Lambda::Function":           &lambdaFunctionHandler{},
	"AWS::Lambda::Alias":              &lambdaAliasHandler{},
	"AWS::Lambda::Url":                &lambdaUrlHandler{},
	"AWS::Lambda::EventSourceMapping": &lambdaEventSourceMappingHandler{},
	"AWS::Lambda::Permission":         &lambdaPermissionHandler{},
	"AWS::Lambda::LayerVersion":       &lambdaLayerVersionHandler{},
	"AWS::Lambda::CodeSigningConfig":  &lambdaCodeSigningConfigHandler{},
	// IAM
	"AWS::IAM::Role":              &iamRoleHandler{},
	"AWS::IAM::Policy":            &iamPolicyHandler{},
	"AWS::IAM::ManagedPolicy":     &iamManagedPolicyHandler{},
	"AWS::IAM::Group":             &iamGroupHandler{},
	"AWS::IAM::InstanceProfile":   &iamInstanceProfileHandler{},
	"AWS::IAM::ServiceLinkedRole": &iamServiceLinkedRoleHandler{},
	// CloudWatch Logs
	"AWS::Logs::LogGroup":  &logsLogGroupHandler{},
	"AWS::Logs::LogStream": &logsLogStreamHandler{},
	// SSM
	"AWS::SSM::Parameter": &ssmParameterHandler{},
	// Secrets Manager
	"AWS::SecretsManager::Secret": &secretsManagerSecretHandler{},
	// KMS
	"AWS::KMS::Key":   &kmsKeyHandler{},
	"AWS::KMS::Alias": &kmsAliasHandler{},
	// CDK metadata
	"AWS::CDK::Metadata":                       &stubResourceHandler{},
	"AWS::CloudFormation::WaitConditionHandle": &stubResourceHandler{},
	"AWS::CloudFormation::WaitCondition":       &stubResourceHandler{},
	// EC2
	"AWS::EC2::VPC":                         &ec2VPCHandler{},
	"AWS::EC2::Subnet":                      &ec2SubnetHandler{},
	"AWS::EC2::SecurityGroup":               &ec2SecurityGroupHandler{},
	"AWS::EC2::InternetGateway":             &ec2InternetGatewayHandler{},
	"AWS::EC2::VPNGateway":                  &ec2VPNGatewayHandler{},
	"AWS::EC2::VPCGatewayAttachment":        &ec2VPCGatewayAttachmentHandler{},
	"AWS::EC2::RouteTable":                  &ec2RouteTableHandler{},
	"AWS::EC2::Route":                       &ec2RouteHandler{},
	"AWS::EC2::SubnetRouteTableAssociation": &ec2SubnetRouteTableAssociationHandler{},
	"AWS::EC2::NatGateway":                  &ec2NatGatewayHandler{},
	"AWS::EC2::EIP":                         &ec2EIPHandler{},
	"AWS::EC2::LaunchTemplate":              &ec2LaunchTemplateHandler{},
	// Step Functions
	"AWS::StepFunctions::StateMachine": &sfnStateMachineHandler{},
	// EventBridge
	"AWS::Events::Rule":     &eventsRuleHandler{},
	"AWS::Events::EventBus": &eventsEventBusHandler{},
	// API Gateway
	"AWS::ApiGateway::RestApi":          &apigwRestApiHandler{},
	"AWS::ApiGateway::Resource":         &apigwResourceHandler{},
	"AWS::ApiGateway::Method":           &apigwMethodHandler{},
	"AWS::ApiGateway::Deployment":       &apigwDeploymentHandler{},
	"AWS::ApiGateway::Stage":            &apigwStageHandler{},
	"AWS::ApiGateway::Account":          &stubResourceHandler{},
	"AWS::ApiGateway::ApiKey":           &apigwApiKeyHandler{},
	"AWS::ApiGateway::UsagePlan":        &apigwUsagePlanHandler{},
	"AWS::ApiGateway::UsagePlanKey":     &apigwUsagePlanKeyHandler{},
	"AWS::ApiGateway::Authorizer":       &apigwAuthorizerHandler{},
	"AWS::ApiGateway::Model":            &apigwModelHandler{},
	"AWS::ApiGateway::RequestValidator": &apigwRequestValidatorHandler{},
	"AWS::ApiGatewayV2::Api":            &apigwV2ApiHandler{},
	"AWS::ApiGatewayV2::Stage":          &apigwV2StageHandler{},
	"AWS::ApiGatewayV2::Integration":    &apigwV2IntegrationHandler{},
	"AWS::ApiGatewayV2::Route":          &apigwV2RouteHandler{},
	// ECS
	"AWS::ECS::Cluster":        &ecsClusterHandler{},
	"AWS::ECS::TaskDefinition": &ecsTaskDefinitionHandler{},
	"AWS::ECS::Service":        &ecsServiceHandler{},
	// Service Catalog AppRegistry
	"AWS::ServiceCatalogAppRegistry::Application":         &appregistryApplicationHandler{},
	"AWS::ServiceCatalogAppRegistry::ResourceAssociation": &appregistryResourceAssociationHandler{},
	// RDS
	"AWS::RDS::DBInstance":       &rdsDBInstanceHandler{},
	"AWS::RDS::DBCluster":        &rdsDBClusterHandler{},
	"AWS::RDS::DBSubnetGroup":    &rdsDBSubnetGroupHandler{},
	"AWS::RDS::DBParameterGroup": &rdsDBParameterGroupHandler{},
	// Kinesis
	"AWS::Kinesis::Stream": &kinesisStreamHandler{},
	// Cognito
	"AWS::Cognito::UserPool":       &cognitoUserPoolHandler{},
	"AWS::Cognito::UserPoolClient": &cognitoUserPoolClientHandler{},
	// AppSync
	"AWS::AppSync::Api":                      &appsyncEventsApiHandler{},
	"AWS::AppSync::GraphQLApi":               &appsyncGraphQLApiHandler{},
	"AWS::AppSync::GraphQLSchema":            &appsyncGraphQLSchemaHandler{},
	"AWS::AppSync::ChannelNamespace":         &appsyncChannelNamespaceHandler{},
	"AWS::AppSync::ApiKey":                   &appsyncApiKeyHandler{},
	"AWS::AppSync::DataSource":               &appsyncDataSourceHandler{},
	"AWS::AppSync::Resolver":                 &appsyncResolverHandler{},
	"AWS::AppSync::FunctionConfiguration":    &appsyncFunctionConfigurationHandler{},
	"AWS::AppSync::DomainName":               &appsyncDomainNameHandler{},
	"AWS::AppSync::DomainNameApiAssociation": &appsyncDomainNameApiAssociationHandler{},
	"AWS::AppSync::ApiCache":                 &appsyncApiCacheHandler{},
	"AWS::AppSync::SourceApiAssociation":     &appsyncSourceApiAssociationHandler{},
	// ElastiCache
	"AWS::ElastiCache::CacheCluster":     &elastiCacheCacheClusterHandler{},
	"AWS::ElastiCache::ServerlessCache":  &elastiCacheServerlessCacheHandler{},
	"AWS::ElastiCache::ReplicationGroup": &elastiCacheReplicationGroupHandler{},
	"AWS::ElastiCache::SubnetGroup":      &elastiCacheSubnetGroupHandler{},
	"AWS::ElastiCache::ParameterGroup":   &stubResourceHandler{},
	// CloudFront
	"AWS::CloudFront::Distribution": &cloudfrontDistributionHandler{},
	// SES
	"AWS::SES::Template":         &sesTemplateHandler{},
	"AWS::SES::ConfigurationSet": &stubResourceHandler{},
	// Certificate Manager
	"AWS::CertificateManager::Certificate": &acmCertificateHandler{},
	// ECR
	"AWS::ECR::Repository": &ecrRepositoryHandler{},
	// CloudTrail
	"AWS::CloudTrail::Trail": &cloudtrailTrailHandler{},
	// Backup
	"AWS::Backup::BackupVault": &backupBackupVaultHandler{},
	"AWS::Backup::BackupPlan":  &backupBackupPlanHandler{},
	// Transfer
	"AWS::Transfer::Server": &transferServerHandler{},
	"AWS::Transfer::User":   &transferUserHandler{},
	// EFS
	"AWS::EFS::FileSystem":  &efsFileSystemHandler{},
	"AWS::EFS::MountTarget": &efsMountTargetHandler{},
	"AWS::EFS::AccessPoint": &efsAccessPointHandler{},
	// Shield
	"AWS::Shield::Protection": &shieldProtectionHandler{},
	// Firehose
	"AWS::KinesisFirehose::DeliveryStream": &firehoseDeliveryStreamHandler{},
	// Athena
	"AWS::Athena::WorkGroup": &athenaWorkGroupHandler{},
	// Glue
	"AWS::Glue::Database": &glueDatabaseHandler{},
	"AWS::Glue::Table":    &glueTableHandler{},
	// CloudWatch
	"AWS::CloudWatch::Alarm": &cloudwatchAlarmHandler{},
	// EventBridge
	"AWS::Events::Connection": &stubResourceHandler{},
	// Scheduler
	"AWS::Scheduler::Schedule":      &schedulerScheduleHandler{},
	"AWS::Scheduler::ScheduleGroup": &schedulerScheduleGroupHandler{},
	// OpenSearch
	"AWS::OpenSearchService::Domain": &opensearchDomainHandler{},
	// AppConfig
	"AWS::AppConfig::Application":          &appconfigApplicationHandler{},
	"AWS::AppConfig::Environment":          &appconfigEnvironmentHandler{},
	"AWS::AppConfig::ConfigurationProfile": &appconfigConfigurationProfileHandler{},
	// ELBv2
	"AWS::ElasticLoadBalancingV2::LoadBalancer": &elbv2LoadBalancerHandler{},
	"AWS::ElasticLoadBalancingV2::TargetGroup":  &elbv2TargetGroupHandler{},
	"AWS::ElasticLoadBalancingV2::Listener":     &elbv2ListenerHandler{},
	// Auto Scaling
	"AWS::AutoScaling::AutoScalingGroup":    &autoscalingASGHandler{},
	"AWS::AutoScaling::LaunchConfiguration": &autoscalingLaunchConfigHandler{},
	// Route53
	"AWS::Route53::HostedZone":  &route53HostedZoneHandler{},
	"AWS::Route53::RecordSet":   &route53RecordSetHandler{},
	"AWS::Route53::HealthCheck": &route53HealthCheckHandler{},
	// EKS
	"AWS::EKS::Cluster":                &eksClusterHandler{},
	"AWS::EKS::Nodegroup":              &eksNodegroupHandler{},
	"AWS::EKS::FargateProfile":         &eksFargateProfileHandler{},
	"AWS::EKS::Addon":                  &eksAddonHandler{},
	"AWS::EKS::AccessEntry":            &eksAccessEntryHandler{},
	"AWS::EKS::PodIdentityAssociation": &eksPodIdentityAssociationHandler{},
	// MSK
	"AWS::MSK::Cluster":       &mskClusterHandler{},
	"AWS::MSK::Configuration": &mskConfigurationHandler{},
	// Pipes
	"AWS::Pipes::Pipe": &pipesPipeHandler{},
	// IAM (additional)
	"AWS::IAM::User":      &iamUserHandler{},
	"AWS::IAM::AccessKey": &iamAccessKeyHandler{},
	// WAFv2
	"AWS::WAFv2::WebACL": &wafv2WebACLHandler{},
	// API Gateway V2 (additional)
	"AWS::ApiGatewayV2::Deployment": &stubResourceHandler{},
	// DynamoDB (additional)
	"AWS::DynamoDB::GlobalTable": &dynamodbGlobalTableHandler{},
}

// ── Stub resource handler ──────────────────────────────────────────────────

// stubResourceHandler generates a fake physical ID and does nothing on delete.
type stubResourceHandler struct{}

func (h *stubResourceHandler) Create(_ context.Context, _ http.Handler, _ *config.Config, _ map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return fmt.Sprintf("stub-%s-%d", rCtx.StackName, len(rCtx.Resources)), nil, nil
}

func (h *stubResourceHandler) Delete(_ context.Context, _ http.Handler, _ *config.Config, _ string, _ *resolveContext) error {
	return nil
}

// ── Internal dispatch ──────────────────────────────────────────────────────

// internalCall describes one internal service-to-service request: the HTTP
// call to make, and enough about what it is aimed at to label the trace hop it
// produces.
//
// target and action identify the operation for the two RPC-shaped protocols;
// a REST call leaves both empty and is identified by its method and path. They
// are kept raw rather than pre-resolved into hop labels because resolving them
// walks long prefix chains and concatenates strings, and an untraced deploy —
// the normal case, issuing thousands of these — must not pay for labels that
// nothing will read. See hopLabels.
type internalCall struct {
	method      string
	path        string
	contentType string
	body        []byte
	region      string
	headers     []http.Header // extra request headers, applied in order

	target string // X-Amz-Target, for the JSON protocols
	action string // Action parameter, for the Query protocol

	// service names the service this call reaches, for dispatches whose path
	// cannot be classified. Leave it empty to let serviceFromPath infer it,
	// which is right for every REST path carrying a distinctive prefix.
	service string
}

// hopLabels renders the call for the trace UI: which service and operation it
// is aimed at, and a human-readable target. Called only when a trace is being
// recorded.
func (c internalCall) hopLabels() (service, operation, targetURI string) {
	switch {
	case c.target != "":
		service, operation = serviceFromTarget(c.target)
		return service, operation, "POST " + c.path + " (X-Amz-Target: " + c.target + ")"
	case c.action != "":
		return serviceFromAction(c.action), c.action, "POST " + c.path + "?Action=" + c.action
	default:
		return c.restService(), c.method, c.method + " " + c.path
	}
}

// serviceKey names the service this call reaches, for the failure reason a
// non-2xx response becomes — see status_reason.go.
//
// It goes through hopLabels rather than repeating its switch. hopLabels avoids
// that work on the hot path deliberately, but a dispatch that has already
// failed is not the hot path, and one string the caller discards is cheaper
// than a second copy of the classification.
func (c internalCall) serviceKey() string {
	service, _, _ := c.hopLabels()
	return service
}

// restService names the service a REST dispatch reaches: what the caller
// stated, or what the path betrays.
//
// Inference is a prefix match and nothing else. S3 is the service it cannot
// help with — its REST paths are bare "/{bucket}[/{key}]", indistinguishable
// from any other service's, which is why S3 hops used to render with a blank
// Service. Making S3 the fallback would have fixed those and mislabelled every
// path the switch does not yet know, including the next service added to the
// provisioner. A blank label is a visible gap; a confidently wrong one is a
// lie the reader has no reason to doubt. So S3's dispatches state their
// service — see internalS3Request — and inference stays conservative.
func (c internalCall) restService() string {
	if c.service != "" {
		return c.service
	}
	return serviceFromPath(c.path)
}

// do dispatches the call into the emulator router and records exactly one
// trace hop for it.
//
// Every internal dispatch helper in this package funnels through here, and
// that is the point: hop recording used to be copy-pasted into each helper,
// and the two helpers written without it (CloudFront's If-Match flow and
// AppSync Events) dropped their hops silently for as long as they existed.
//
// Nothing below is CloudFormation-specific but the "cloudformation" caller
// label, so this is the shape to lift into a shared package when the other
// in-process dispatchers adopt it — internal/eventtarget,
// internal/alarmaction, stepfunctions' service integrations, autoscaling's
// reconciler and dynamodb's stream fan-out all repeat some of it today.
func (c internalCall) do(ctx context.Context, router http.Handler) (*httptest.ResponseRecorder, error) {
	req, err := http.NewRequestWithContext(ctx, c.method, c.path, bytes.NewReader(c.body))
	if err != nil {
		return nil, err
	}
	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}
	if c.region != "" {
		req.Header.Set("X-Overcast-Region", c.region)
	}
	if c.target != "" {
		req.Header.Set("X-Amz-Target", c.target)
	}
	for _, header := range c.headers {
		for name, values := range header {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
	}

	rec := httptest.NewRecorder()

	// Debug tracing off: dispatch and return without paying for a clock read,
	// a minted request ID, or a hop. A large deploy issues thousands of these.
	recorder := trace.RecorderFromContext(ctx)
	if recorder == nil {
		router.ServeHTTP(rec, req)
		noteLimitations(ctx, rec)
		return rec, statusError(ctx, c.serviceKey(), rec)
	}
	defer noteLimitations(ctx, rec)

	childID := linkChildRequest(req, recorder)
	start := time.Now()
	router.ServeHTTP(rec, req)
	duration := time.Since(start)

	var hopErr string
	if rec.Code >= 400 {
		hopErr = rec.Body.String()
	}
	service, operation, targetURI := c.hopLabels()
	recorder.AddHop(trace.Hop{
		CallerService:  "cloudformation",
		Service:        service,
		Operation:      operation,
		RequestID:      childID,
		TargetURI:      targetURI,
		RequestBody:    c.body,
		ResponseStatus: rec.Code,
		ResponseBody:   rec.Body.Bytes(),
		Duration:       duration,
		Timestamp:      start,
		Error:          hopErr,
	})
	return rec, statusError(ctx, service, rec)
}

// teardownError classifies the outcome of a delete dispatched while tearing a
// resource down, so that every resource handler answers the question the same
// way.
//
// A teardown succeeds only when the resource is gone, and there are two ways
// for it to be gone: this delete removed it, or it was not there to begin with.
// Anything else — the service refused, the service failed, the call never
// reached it — is reported, because the resource is still standing and no
// caller may record DELETE_COMPLETE over it. That is what the rollback paths
// exist to notice: a rollback that cannot delete what the failed create built
// reaches ROLLBACK_FAILED, which is the signal an operator needs, rather than
// claiming a teardown that did not happen.
//
// Handlers used to discard the dispatch result outright and return nil. It read
// as defensive — a resource that is already gone must never wedge a teardown —
// but it bought that safety by reporting every failure as a success.
// resourceAlreadyGone buys the same safety precisely.
//
// The operation name travels in the error because it reaches the operator as a
// stack event's ResourceStatusReason, where a bare "HTTP 500" says nothing
// about what was being torn down.
//
// Every Delete in the package that dispatches now goes through here, which is
// what lets both teardown paths treat an error as a resource still standing.
// The two that do not are correct as they are: the custom resource's delete is
// a Lambda invoke with no response to classify, and the S3 bucket's non-empty
// refusal is an errDeletionBlocked the stack has to see.
//
// EC2's own refusal answers here too: DeleteInternetGateway, the main-table
// case of DeleteRouteTable, DeleteVpnGateway, and — since #1135 —
// DeleteSecurityGroup, DeleteSubnet, and DeleteVpc all reject a delete with
// DependencyViolation while something is still attached, and every EC2
// handler's Delete dispatches through this same function — so wrapping that
// one code in errDeletionBlocked here reaches every one of them at once
// rather than needing a per-handler classifier the way IAM's DeleteConflict
// does.
func teardownError(op string, rec *httptest.ResponseRecorder, err error) error {
	if err == nil || resourceAlreadyGone(rec) {
		return nil
	}
	if ec2DependencyViolation(rec) {
		return fmt.Errorf("%w: %s: %v", errDeletionBlocked, op, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// ec2DependencyViolation reports whether a failed dispatch is EC2 answering
// DependencyViolation — the code EC2 uses across its resource types for "a
// dependent still exists," the same standing condition IAM's DeleteConflict
// and RDS's DeletionProtection describe for their own services. It scans the
// body rather than checking the HTTP status because EC2 answers
// DependencyViolation with a plain 400, the status code it also uses for
// every other validation failure a Query-protocol delete can hit.
func ec2DependencyViolation(rec *httptest.ResponseRecorder) bool {
	if rec == nil {
		return false
	}
	return strings.Contains(lettersLower(rec.Body.String()), "dependencyviolation")
}

// resourceAlreadyGone reports whether a failed dispatch failed only because the
// resource it names does not exist.
//
// AWS has no single shape for that answer and the emulator's services mirror
// each one, so two signals are needed rather than one:
//
//   - HTTP 404. Every REST-shaped service answers this way, and Athena answers
//     it for an absent workgroup under a generic InvalidRequestException.
//   - Absence named in the error body. That covers both the specific codes
//     (ResourceNotFoundException, DBSubnetGroupNotFoundFault,
//     AWS.SimpleQueueService.NonExistentQueue, NoSuchEntity,
//     TemplateDoesNotExist) — several of which carry HTTP 400, so the status
//     alone is not enough — and the message under a code that does not name it
//     at all. Auto Scaling's DeleteAutoScalingGroup answers a plain
//     ValidationError for a group that is not there, and its message is the
//     only thing separating that from a genuinely invalid request, on AWS as
//     well as here.
//
// The body is scanned rather than parsed because the four protocols in play
// spell the same answer four ways — JSON's __type, Query XML's Code, REST
// JSON's message — and an error body carries nothing but that code and its
// message, so a scan reads exactly the two fields a parser would.
func resourceAlreadyGone(rec *httptest.ResponseRecorder) bool {
	if rec == nil {
		return false
	}
	if rec.Code == http.StatusNotFound {
		return true
	}
	return namesAbsence(lettersLower(rec.Body.String()))
}

// absentResourcePhrases are the ways AWS spells "it is not there" once
// punctuation and case are stripped: "ResourceNotFoundException" and "name not
// found" both reduce to notfound, "does not exist" and "TemplateDoesNotExist"
// both to notexist.
var absentResourcePhrases = []string{"notfound", "nonexistent", "nosuch", "notexist"}

// lettersLower reduces a response body to lowercase letters, so that a phrase
// can be matched across the punctuation, tag names and word breaks the four
// protocols disagree about.
func lettersLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
		} else if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// linkChildRequest wires an internal dispatch into the trace graph: the child
// request is told which request it descends from, and is given the request ID
// it must answer under. It returns that child request ID for the hop.
//
// Pinning the child's ID is what makes the link reliable. Reading it back off
// the response — which is what the hop used to do — made the link depend on
// which header the target service's protocol happens to use: the JSON and
// Query protocols answer with x-amzn-requestid, S3's REST XML with
// x-amz-request-id, and some successful responses (S3's idempotent
// CreateBucket among them) set neither. Every one of those hops recorded an
// empty request ID and linked to nothing. middleware.RequestID honours a
// caller-supplied x-amzn-requestid, so the ID pinned here is the ID the child
// trace is stored under.
func linkChildRequest(req *http.Request, rec *trace.Recorder) string {
	if parentID := rec.RequestID(); parentID != "" {
		req.Header.Set("X-Overcast-Parent-Request-Id", parentID)
	}
	childID := protocol.NewRequestID()
	req.Header.Set("x-amzn-requestid", childID)
	return childID
}

// internalRequest dispatches an HTTP request to the emulator router.
// The region parameter is forwarded via X-Overcast-Region so that services
// build ARNs in the correct region.
//
// extra carries operation parameters a service models as request headers
// rather than in the body — S3's x-amz-transition-default-minimum-object-size
// and CloudFront's If-Match, for example. It is variadic so the common
// headerless call stays unchanged.
func internalRequest(ctx context.Context, router http.Handler, region, method, path, contentType string, body []byte, extra ...http.Header) (*httptest.ResponseRecorder, error) {
	return restCall("", region, method, path, contentType, body, extra...).do(ctx, router)
}

// internalS3Request is internalRequest for a dispatch to S3, which states the
// service because S3's paths cannot betray it — see internalCall.restService.
func internalS3Request(ctx context.Context, router http.Handler, region, method, path, contentType string, body []byte, extra ...http.Header) (*httptest.ResponseRecorder, error) {
	return restCall("s3", region, method, path, contentType, body, extra...).do(ctx, router)
}

// templateFetchContext returns the context for the internal S3 GET that
// fetches a TemplateURL.
//
// It deliberately does not descend from the caller's context: chi's route
// context would leak out of the CloudFormation request into the dispatch and
// be matched against the S3 route. It has to carry the trace recorder even so
// — starting from a bare Background() dropped it, which made every
// TemplateURL fetch invisible in the trace and left the S3 request it issued
// with no parent to link back to.
func templateFetchContext(ctx context.Context) context.Context {
	return trace.ContextWithRecorder(context.Background(), trace.RecorderFromContext(ctx))
}

// restCall builds the REST-shaped dispatch both helpers above send.
func restCall(service, region, method, path, contentType string, body []byte, extra ...http.Header) internalCall {
	return internalCall{
		method:      method,
		path:        path,
		contentType: contentType,
		body:        body,
		region:      region,
		headers:     extra,
		service:     service,
	}
}

// internalJSON dispatches a JSON POST with X-Amz-Target header.
func internalJSON(ctx context.Context, router http.Handler, region, target string, body any) (*httptest.ResponseRecorder, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return internalCall{
		method:      http.MethodPost,
		path:        "/",
		contentType: "application/x-amz-json-1.0",
		body:        data,
		region:      region,
		target:      target,
	}.do(ctx, router)
}

// internalGET dispatches a plain GET to an emulator-only /_overcast/ endpoint
// over the same router every other internal call goes through.
//
// It exists because the ECS container-log endpoint the deploy-diagnostics
// collector reads is a REST GET on an /_overcast/ path, while internalJSON is
// built for an X-Amz-Target JSON POST. Re-implementing the dispatch would have
// meant a second copy of the trace-hop recording internalCall.do already owns
// — the duplication that once cost CloudFront's If-Match flow and AppSync
// Events their hops entirely. internalCall was already the lower-level helper;
// this is only a named entry point that states its service, for the same
// reason internalS3Request states S3's: serviceFromPath cannot infer a service
// from an /_overcast/ path, and a blank hop label is a visible gap where a
// guess would be a quiet lie.
func internalGET(ctx context.Context, router http.Handler, service, region, path string) (*httptest.ResponseRecorder, error) {
	return restCall(service, region, http.MethodGet, path, "", nil).do(ctx, router)
}

// internalQuery dispatches a Query-protocol POST.
func internalQuery(ctx context.Context, router http.Handler, region string, params map[string]string) (*httptest.ResponseRecorder, error) {
	form := make(url.Values, len(params))
	for k, v := range params {
		form.Set(k, v)
	}
	return internalCall{
		method:      http.MethodPost,
		path:        "/",
		contentType: "application/x-www-form-urlencoded",
		body:        []byte(form.Encode()),
		region:      region,
		action:      params["Action"],
	}.do(ctx, router)
}

// serviceFromTarget, serviceFromAction and serviceFromPath name the service a
// dispatch is aimed at, so a hop reads as "sqs / CreateQueue" rather than as a
// bare URL. They are best-effort labels for the trace UI; nothing routes on
// them.
func serviceFromTarget(target string) (service, operation string) {
	if idx := strings.LastIndex(target, "."); idx >= 0 {
		operation = target[idx+1:]
		prefix := target[:idx]
		switch {
		case strings.HasPrefix(prefix, "AmazonSQS"):
			service = "sqs"
		case strings.HasPrefix(prefix, "DynamoDB_"):
			service = "dynamodb"
		case strings.HasPrefix(prefix, "AWSLambda"):
			service = "lambda"
		case strings.HasPrefix(prefix, "AmazonEC2ContainerService"):
			service = "ecs"
		case strings.HasPrefix(prefix, "TrentService"):
			service = "kms"
		case strings.HasPrefix(prefix, "AmazonStates"), strings.HasPrefix(prefix, "AWSStepFunctions"):
			service = "stepfunctions"
		case strings.HasPrefix(prefix, "AmazonSSM"):
			service = "ssm"
		case strings.HasPrefix(prefix, "secretsmanager"):
			service = "secretsmanager"
		case strings.HasPrefix(prefix, "Logs_"):
			service = "logs"
		case strings.HasPrefix(prefix, "AmazonSNS"):
			service = "sns"
		case strings.HasPrefix(prefix, "AWSEvents"):
			service = "events"
		case strings.HasPrefix(prefix, "AmazonKinesis"):
			service = "kinesis"
		case strings.HasPrefix(prefix, "AWSWAF_"):
			service = "waf"
		case strings.HasPrefix(prefix, "EKS"):
			service = "eks"
		case strings.HasPrefix(prefix, "Kafka"):
			service = "msk"
		case strings.HasPrefix(prefix, "EFS_"):
			service = "efs"
		case strings.HasPrefix(prefix, "CertificateManager"):
			service = "acm"
		case strings.HasPrefix(prefix, "TransferService"):
			service = "transfer"
		case strings.HasPrefix(prefix, "AWSShield_"):
			service = "shield"
		case strings.HasPrefix(prefix, "Firehose_"):
			service = "firehose"
		case strings.HasPrefix(prefix, "AmazonAthena"):
			service = "athena"
		case strings.HasPrefix(prefix, "AWSGlue"):
			service = "glue"
		case strings.HasPrefix(prefix, "GraniteServiceVersion"):
			service = "cloudwatch"
		case strings.HasPrefix(prefix, "Scheduler"):
			service = "scheduler"
		case strings.HasPrefix(prefix, "AWSCognitoIdentityProviderService"):
			service = "cognito"
		case strings.HasPrefix(prefix, "com.amazonaws.cloudtrail"):
			service = "cloudtrail"
		case strings.HasPrefix(prefix, "AWSAppSync"):
			service = "appsync"
		case strings.HasPrefix(prefix, "AmazonElastiCache"):
			service = "elasticache"
		case strings.HasPrefix(prefix, "AWSSecurityTokenService"):
			service = "sts"
		case strings.HasPrefix(prefix, "AmazonEC2"):
			service = "ec2"
		case strings.HasPrefix(prefix, "ElasticLoadBalancing"):
			service = "elbv2"
		case strings.HasPrefix(prefix, "AutoScaling_"):
			service = "autoscaling"
		case strings.HasPrefix(prefix, "AmazonRDS"):
			service = "rds"
		default:
			service = prefix
		}
	}
	return
}

func serviceFromAction(action string) string {
	switch {
	case strings.Contains(action, "Role") || strings.Contains(action, "Policy") || strings.Contains(action, "User") || strings.Contains(action, "InstanceProfile") || strings.Contains(action, "AccessKey") || strings.Contains(action, "ServiceLinkedRole"):
		return "iam"
	case strings.Contains(action, "Vpc") || strings.Contains(action, "Subnet") || strings.Contains(action, "SecurityGroup") ||
		strings.Contains(action, "Route") || strings.Contains(action, "InternetGateway") || strings.Contains(action, "NatGateway") ||
		strings.Contains(action, "Address") || strings.Contains(action, "NetworkInterface") || strings.Contains(action, "NetworkAcl") ||
		strings.Contains(action, "VpcEndpoint") || strings.Contains(action, "VpcPeering") || strings.Contains(action, "Volume") ||
		strings.Contains(action, "Instance") || strings.Contains(action, "Image") || strings.Contains(action, "Snapshot") ||
		strings.Contains(action, "KeyPair") || strings.Contains(action, "PlacementGroup") || strings.Contains(action, "Eip") ||
		strings.Contains(action, "EgressOnly"):
		return "ec2"
	case strings.HasPrefix(action, "Create") && strings.Contains(action, "Topic") || strings.HasPrefix(action, "Delete") && strings.Contains(action, "Topic") ||
		strings.Contains(action, "Subscribe") || strings.Contains(action, "Unsubscribe") || strings.Contains(action, "TagResource") && strings.HasPrefix(action, "Tag"):
		return "sns"
	case strings.Contains(action, "LoadBalancer") || strings.Contains(action, "TargetGroup") || strings.Contains(action, "Listener") || strings.Contains(action, "Rule"):
		return "elbv2"
	case strings.Contains(action, "DB") || strings.Contains(action, "Db"):
		return "rds"
	case strings.Contains(action, "Cache") || strings.Contains(action, "ReplicationGroup"):
		return "elasticache"
	case strings.Contains(action, "AutoScaling") || strings.Contains(action, "LaunchConfiguration"):
		return "autoscaling"
	case strings.Contains(action, "HostedZone") || strings.Contains(action, "RecordSet") || strings.Contains(action, "HealthCheck"):
		return "route53"
	case strings.Contains(action, "Parameter") || strings.Contains(action, "MaintenanceWindow") || strings.Contains(action, "Association"):
		return "ssm"
	case strings.Contains(action, "Key") || strings.Contains(action, "Alias") || strings.Contains(action, "Grant"):
		return "kms"
	case strings.Contains(action, "Template") || strings.Contains(action, "Identity") || strings.Contains(action, "Receipt"):
		return "ses"
	case strings.Contains(action, "Alarm") || strings.Contains(action, "Metric"):
		return "cloudwatch"
	case strings.Contains(action, "LogGroup") || strings.Contains(action, "LogStream"):
		return "logs"
	case strings.Contains(action, "UserPool") || strings.Contains(action, "IdentityPool") || strings.Contains(action, "UserPoolClient") || strings.Contains(action, "UserPoolDomain"):
		return "cognito"
	case strings.Contains(action, "Function") || strings.Contains(action, "EventSourceMapping"):
		return "lambda"
	case strings.Contains(action, "Queue"):
		return "sqs"
	case strings.Contains(action, "StateMachine") || strings.Contains(action, "Execution") || strings.Contains(action, "Activity"):
		return "stepfunctions"
	default:
		return ""
	}
}

func serviceFromPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/restapis"), strings.HasPrefix(path, "/v2/apis"), strings.HasPrefix(path, "/apikeys"), strings.HasPrefix(path, "/usageplans"), strings.HasPrefix(path, "/tags/"):
		return "apigateway"
	case strings.Contains(path, "/2015-03-31/"):
		return "lambda"
	case strings.HasPrefix(path, "/v1/pipes"):
		return "pipes"
	case strings.HasPrefix(path, "/2020-05-31/"):
		return "cloudfront"
	case strings.HasPrefix(path, "/2013-04-01/"):
		return "route53"
	case strings.HasPrefix(path, "/v1/apis"), strings.HasPrefix(path, "/v1/domainnames"), strings.HasPrefix(path, "/v1/sourceApis"), strings.HasPrefix(path, "/v1/mergedApis"):
		return "appsync"
	case strings.HasPrefix(path, "/v2/email/"):
		return "ses"
	case strings.HasPrefix(path, "/2015-02-01/"):
		return "efs"
	case strings.HasPrefix(path, "/2021-01-01/"):
		return "opensearch"
	case strings.HasPrefix(path, "/applications"):
		return "appregistry"
	case strings.HasPrefix(path, "/backup-vaults"), strings.HasPrefix(path, "/backup/plans"):
		return "backup"
	default:
		return ""
	}
}

// ── Concrete resource handlers ─────────────────────────────────────────────

// ── SQS Queue handler ─────────────────────────────────────────────────────

type sqsQueueHandler struct{}

// cfnFloatProp reads a numeric CFN property, which may arrive as float64,
// json.Number, or the string form a raw template is free to write.
func cfnFloatProp(props map[string]any, name string) (float64, bool) {
	v, ok := props[name]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func cfnScalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func sqsQueueAttributesFromProps(props map[string]any) map[string]string {
	attrs := make(map[string]string)
	for _, name := range []string{
		"VisibilityTimeout",
		"MessageRetentionPeriod",
		"DelaySeconds",
		"MaximumMessageSize",
		"ReceiveMessageWaitTimeSeconds",
		"ContentBasedDeduplication",
		"DeduplicationScope",
		"FifoQueue",
		"FifoThroughputLimit",
		"KmsDataKeyReusePeriodSeconds",
		"KmsMasterKeyId",
		"SqsManagedSseEnabled",
	} {
		if v, ok := props[name]; ok {
			attrs[name] = cfnScalarString(v)
		}
	}
	if rp, ok := props["RedrivePolicy"]; ok {
		if b, err := json.Marshal(rp); err == nil {
			attrs["RedrivePolicy"] = string(b)
		}
	}
	if rap, ok := props["RedriveAllowPolicy"]; ok {
		if b, err := json.Marshal(rap); err == nil {
			attrs["RedriveAllowPolicy"] = string(b)
		}
	}
	if raw, ok := props["Attributes"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				attrs[k] = s
			}
		}
	}
	return attrs
}

func (h *sqsQueueHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Extract current queue name from ARN (last segment).
	oldName := physicalID
	if parts := strings.Split(physicalID, ":"); len(parts) > 0 {
		oldName = parts[len(parts)-1]
	}
	// QueueName is immutable in AWS — request replacement.
	if n, ok := props["QueueName"].(string); ok && n != "" && n != oldName {
		return "", nil, errReplacementRequired
	}
	// FIFO ↔ standard transitions also require replacement.
	if _, ok := props["FifoQueue"]; ok {
		if strings.HasSuffix(oldName, ".fifo") != asBool(props["FifoQueue"]) {
			return "", nil, errReplacementRequired
		}
	}

	attrs := sqsQueueAttributesFromProps(props)
	queueURL := fmt.Sprintf("%s/%s/%s", cfg.ExternalBaseURL(), cfg.AccountID, oldName)

	if len(attrs) > 0 {
		body := map[string]any{"QueueUrl": queueURL, "Attributes": attrs}
		if _, err := internalJSON(ctx, router, rCtx.Region, "AmazonSQS.SetQueueAttributes", body); err != nil {
			return "", nil, fmt.Errorf("sqs SetQueueAttributes: %w", err)
		}
	}
	if err := updateSQSQueueTags(ctx, router, rCtx.Region, queueURL, rCtx.StackTags, rCtx.PreviousStackTags, props["Tags"], oldProps["Tags"]); err != nil {
		return "", nil, err
	}
	arn := protocol.ARN(rCtx.Region, cfg.AccountID, "sqs", oldName)
	return arn, map[string]string{"Ref": queueURL, "QueueName": oldName, "Arn": arn, "QueueUrl": queueURL}, nil
}

func (h *sqsQueueHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	queueName, _ := props["QueueName"].(string)
	if queueName == "" {
		max := maxNameLenSQS
		fifo := asBool(props["FifoQueue"])
		if fifo {
			// ".fifo" is mandatory on a FIFO queue name and counts toward
			// SQS's 80-character limit, so it has to come out of the budget
			// generatedNameWithin truncates to, not be appended after.
			max -= len(".fifo")
		}
		queueName = rCtx.generatedNameWithin(max)
		if fifo {
			queueName += ".fifo"
		}
	}

	body := map[string]any{
		"QueueName": queueName,
	}

	attrs := sqsQueueAttributesFromProps(props)
	if len(attrs) > 0 {
		body["Attributes"] = attrs
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["tags"] = tags
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonSQS.CreateQueue", body)
	if err != nil {
		return "", nil, fmt.Errorf("sqs CreateQueue: %w", err)
	}

	// Extract QueueUrl from JSON response.
	var resp struct {
		QueueUrl string `json:"QueueUrl"`
	}
	arn := protocol.ARN(rCtx.Region, cfg.AccountID, "sqs", queueName)
	queueURL := ""
	if json.Unmarshal(rec.Body.Bytes(), &resp) == nil && resp.QueueUrl != "" {
		queueURL = resp.QueueUrl
	}
	cfnAttrs := map[string]string{
		"Ref":       queueURL,
		"QueueName": queueName,
		"Arn":       arn,
		"QueueUrl":  queueURL,
	}
	return arn, cfnAttrs, nil
}

// updateSQSQueueTags reconciles a queue's tags on Update: added/changed keys
// go through TagQueue, keys dropped from the template go through UntagQueue.
// Mirrors updateLambdaTags's diff shape for the SQS JSON protocol.
func updateSQSQueueTags(ctx context.Context, router http.Handler, region, queueURL string, stackTags, priorStackTags []Tag, rawTags, rawPrior any) error {
	tags := mergeResourceTags(stackTags, rawTags)
	prior := mergeResourceTags(priorStackTags, rawPrior)

	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)

	if len(added) > 0 {
		body := map[string]any{"QueueUrl": queueURL, "Tags": added}
		if _, err := internalJSON(ctx, router, region, "AmazonSQS.TagQueue", body); err != nil {
			return fmt.Errorf("sqs TagQueue: %w", err)
		}
	}
	if len(removed) > 0 {
		body := map[string]any{"QueueUrl": queueURL, "TagKeys": removed}
		if _, err := internalJSON(ctx, router, region, "AmazonSQS.UntagQueue", body); err != nil {
			return fmt.Errorf("sqs UntagQueue: %w", err)
		}
	}
	return nil
}

func (h *sqsQueueHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Extract queue name from ARN.
	name := physicalID
	if parts := strings.Split(physicalID, ":"); len(parts) > 0 {
		name = parts[len(parts)-1]
	}
	queueURL := fmt.Sprintf("%s/%s/%s", cfg.ExternalBaseURL(), cfg.AccountID, name)
	body := map[string]any{
		"QueueUrl": queueURL,
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonSQS.DeleteQueue", body)
	return teardownError("DeleteQueue", rec, err)
}

// ── SNS Topic handler ──────────────────────────────────────────────────────

type snsAttribute struct {
	name  string
	value string
}

// snsAttributesFromProps translates CloudFormation's typed property values to
// the strings accepted by SNS's Set*Attributes APIs. It owns no SNS behavior:
// validation, defaults, and persistence remain in the SNS service.
func snsAttributesFromProps(props map[string]any, scalarNames, jsonNames []string) ([]snsAttribute, error) {
	attrs := make([]snsAttribute, 0, len(scalarNames)+len(jsonNames))
	for _, name := range scalarNames {
		if value, ok := props[name]; ok && value != nil {
			attrs = append(attrs, snsAttribute{name: name, value: cfnScalarString(value)})
		}
	}
	for _, name := range jsonNames {
		if value, ok := props[name]; ok && value != nil {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("marshal SNS attribute %s: %w", name, err)
			}
			attrs = append(attrs, snsAttribute{name: name, value: string(encoded)})
		}
	}
	return attrs, nil
}

func snsTopicAttributesFromProps(props map[string]any) ([]snsAttribute, error) {
	return snsAttributesFromProps(props,
		[]string{"ContentBasedDeduplication", "DisplayName", "FifoTopic", "FifoThroughputScope", "KmsMasterKeyId", "SignatureVersion", "TracingConfig"},
		[]string{"ArchivePolicy", "DataProtectionPolicy", "DeliveryPolicy"},
	)
}

func snsSubscriptionAttributesFromProps(props map[string]any) ([]snsAttribute, error) {
	return snsAttributesFromProps(props,
		[]string{"FilterPolicyScope", "RawMessageDelivery", "SubscriptionRoleArn"},
		[]string{"DeliveryPolicy", "FilterPolicy", "RedrivePolicy", "ReplayPolicy"},
	)
}

func snsRemovedAttribute(props, oldProps map[string]any, names []string) string {
	for _, name := range names {
		oldValue, hadOldValue := oldProps[name]
		if !hadOldValue || oldValue == nil {
			continue
		}
		newValue, hasNewValue := props[name]
		if !hasNewValue || newValue == nil {
			return name
		}
	}
	return ""
}

func cfnPropertyChanged(props, oldProps map[string]any, name string) (bool, error) {
	oldValue, err := json.Marshal(oldProps[name])
	if err != nil {
		return false, fmt.Errorf("marshal previous CloudFormation property %s: %w", name, err)
	}
	newValue, err := json.Marshal(props[name])
	if err != nil {
		return false, fmt.Errorf("marshal CloudFormation property %s: %w", name, err)
	}
	return !bytes.Equal(oldValue, newValue), nil
}

func applySNSAttributes(ctx context.Context, router http.Handler, region, action, resourceParam, resourceARN string, attrs []snsAttribute) error {
	for _, attr := range attrs {
		params := map[string]string{
			"Action":         action,
			resourceParam:    resourceARN,
			"AttributeName":  attr.name,
			"AttributeValue": attr.value,
			"Version":        "2010-03-31",
		}
		if _, err := internalQuery(ctx, router, region, params); err != nil {
			return fmt.Errorf("sns %s(%s): %w", action, attr.name, err)
		}
	}
	return nil
}

// applySNSTopicTags dispatches to SNS rather than treating tags as
// CloudFormation metadata, so CDK tag updates and drift detection work
// against the authoritative SNS state.
func applySNSTopicTags(ctx context.Context, router http.Handler, region, topicARN string, props map[string]any) error {
	rawTags, ok := props["Tags"]
	if !ok || rawTags == nil {
		return nil
	}
	params, err := snsTopicTagParams(topicARN, rawTags)
	if err != nil {
		return err
	}
	if len(params) == 3 {
		return nil
	}
	if _, err := internalQuery(ctx, router, region, params); err != nil {
		return fmt.Errorf("sns TagResource: %w", err)
	}
	return nil
}

func snsTopicTagParams(topicARN string, rawTags any) (map[string]string, error) {
	tags, ok := rawTags.([]any)
	if !ok {
		return nil, fmt.Errorf("SNS Topic Tags must be an array")
	}
	params := map[string]string{
		"Action":      "TagResource",
		"ResourceArn": topicARN,
		"Version":     "2010-03-31",
	}
	for i, rawTag := range tags {
		tag, ok := rawTag.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("SNS Topic Tags entry must be an object")
		}
		key, ok := tag["Key"]
		if !ok || key == nil {
			return nil, fmt.Errorf("SNS Topic Tags entry must contain Key")
		}
		prefix := fmt.Sprintf("Tags.Tag.%d", i+1)
		params[prefix+".Key"] = cfnScalarString(key)
		if value, ok := tag["Value"]; ok && value != nil {
			params[prefix+".Value"] = cfnScalarString(value)
		}
	}
	return params, nil
}

func snsSubscriptionRequestRegion(props map[string]any, stackRegion string) (string, error) {
	rawRegion, ok := props["Region"]
	if !ok || rawRegion == nil {
		return stackRegion, nil
	}
	region := cfnScalarString(rawRegion)
	if region == "" || region == stackRegion {
		return stackRegion, nil
	}
	return "", fmt.Errorf("AWS::SNS::Subscription Region %q is not implemented for cross-region subscriptions", region)
}

func subscribeSNSInline(ctx context.Context, router http.Handler, region, topicARN string, props map[string]any) error {
	rawSubscriptions, ok := props["Subscription"]
	if !ok || rawSubscriptions == nil {
		return nil
	}
	subscriptions, ok := rawSubscriptions.([]any)
	if !ok {
		return fmt.Errorf("SNS Topic Subscription must be an array")
	}
	for _, rawSubscription := range subscriptions {
		subscription, ok := rawSubscription.(map[string]any)
		if !ok {
			return fmt.Errorf("SNS Topic Subscription entry must be an object")
		}
		params := map[string]string{
			"Action":   "Subscribe",
			"TopicArn": topicARN,
			"Protocol": cfnScalarString(subscription["Protocol"]),
			"Endpoint": cfnScalarString(subscription["Endpoint"]),
			"Version":  "2010-03-31",
		}
		if _, err := internalQuery(ctx, router, region, params); err != nil {
			return fmt.Errorf("sns Subscribe inline subscription: %w", err)
		}
	}
	return nil
}

type snsTopicHandler struct{}

func (h *snsTopicHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// physicalID is the topic ARN; last segment is the topic name.
	oldName := physicalID
	if i := strings.LastIndex(physicalID, ":"); i >= 0 {
		oldName = physicalID[i+1:]
	}
	if n, ok := props["TopicName"].(string); ok && n != "" && n != oldName {
		return "", nil, errReplacementRequired
	}
	if asBool(props["FifoTopic"]) != asBool(oldProps["FifoTopic"]) {
		return "", nil, errReplacementRequired
	}
	if changed, err := cfnPropertyChanged(props, oldProps, "Subscription"); err != nil {
		return "", nil, err
	} else if changed {
		return "", nil, failUpdate(fmt.Errorf("AWS::SNS::Topic Subscription updates are not implemented"))
	}
	if name := snsRemovedAttribute(props, oldProps, []string{"ArchivePolicy", "ContentBasedDeduplication", "DataProtectionPolicy", "DeliveryPolicy", "DisplayName", "FifoThroughputScope", "KmsMasterKeyId", "SignatureVersion", "TracingConfig"}); name != "" {
		return "", nil, failUpdate(fmt.Errorf("removing SNS topic attribute %s is not implemented", name))
	}

	attrs, err := snsTopicAttributesFromProps(props)
	if err != nil {
		return "", nil, err
	}
	if err := applySNSAttributes(ctx, router, rCtx.Region, "SetTopicAttributes", "TopicArn", physicalID, attrs); err != nil {
		return "", nil, err
	}
	if err := applySNSTopicTags(ctx, router, rCtx.Region, physicalID, props); err != nil {
		return "", nil, err
	}
	return physicalID, map[string]string{"TopicName": oldName, "TopicArn": physicalID}, nil
}

func (h *snsTopicHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	topicName, _ := props["TopicName"].(string)
	if topicName == "" {
		topicName = rCtx.generatedNameWithin(maxNameLenSNS)
	}

	params := map[string]string{
		"Action":  "CreateTopic",
		"Name":    topicName,
		"Version": "2010-03-31",
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("sns CreateTopic: %w", err)
	}
	arn := extractXMLValue(rec.Body.String(), "TopicArn")
	if arn == "" {
		arn = protocol.ARN(rCtx.Region, cfg.AccountID, "sns", topicName)
	}
	topicAttributes, err := snsTopicAttributesFromProps(props)
	if err != nil {
		return "", nil, err
	}
	if err := applySNSAttributes(ctx, router, rCtx.Region, "SetTopicAttributes", "TopicArn", arn, topicAttributes); err != nil {
		return "", nil, err
	}
	if err := applySNSTopicTags(ctx, router, rCtx.Region, arn, props); err != nil {
		return "", nil, err
	}
	if err := subscribeSNSInline(ctx, router, rCtx.Region, arn, props); err != nil {
		return "", nil, err
	}
	attrs := map[string]string{
		"TopicName": topicName,
		"TopicArn":  arn,
	}
	return arn, attrs, nil
}

func (h *snsTopicHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":   "DeleteTopic",
		"TopicArn": physicalID,
		"Version":  "2010-03-31",
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteTopic", rec, err)
}

// ── SNS Subscription handler ───────────────────────────────────────────────

type snsSubscriptionHandler struct{}

func (h *snsSubscriptionHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	topicArn, _ := props["TopicArn"].(string)
	proto, _ := props["Protocol"].(string)
	endpoint, _ := props["Endpoint"].(string)

	params := map[string]string{
		"Action":   "Subscribe",
		"TopicArn": topicArn,
		"Protocol": proto,
		"Endpoint": endpoint,
		"Version":  "2010-03-31",
	}
	subscriptionRegion, err := snsSubscriptionRequestRegion(props, rCtx.Region)
	if err != nil {
		return "", nil, err
	}
	rec, err := internalQuery(ctx, router, subscriptionRegion, params)
	if err != nil {
		return "", nil, fmt.Errorf("sns Subscribe: %w", err)
	}
	arn := extractXMLValue(rec.Body.String(), "SubscriptionArn")
	if arn == "" {
		arn = fmt.Sprintf("stub-sub-%s-%d", rCtx.StackName, len(rCtx.Resources))
	}
	attributes, err := snsSubscriptionAttributesFromProps(props)
	if err != nil {
		return "", nil, err
	}
	if err := applySNSAttributes(ctx, router, subscriptionRegion, "SetSubscriptionAttributes", "SubscriptionArn", arn, attributes); err != nil {
		return "", nil, err
	}
	attrs := map[string]string{
		"Arn":      arn,
		"TopicArn": topicArn,
		"Protocol": proto,
		"Endpoint": endpoint,
	}
	return arn, attrs, nil
}

func (h *snsSubscriptionHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":          "Unsubscribe",
		"SubscriptionArn": physicalID,
		"Version":         "2010-03-31",
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("Unsubscribe", rec, err)
}

func (h *snsSubscriptionHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, name := range []string{"TopicArn", "Protocol", "Endpoint", "Region"} {
		if cfnScalarString(props[name]) != cfnScalarString(oldProps[name]) {
			return "", nil, errReplacementRequired
		}
	}
	if name := snsRemovedAttribute(props, oldProps, []string{"DeliveryPolicy", "FilterPolicy", "FilterPolicyScope", "RawMessageDelivery", "RedrivePolicy", "ReplayPolicy", "SubscriptionRoleArn"}); name != "" {
		return "", nil, failUpdate(fmt.Errorf("removing SNS subscription attribute %s is not implemented", name))
	}
	attributes, err := snsSubscriptionAttributesFromProps(props)
	if err != nil {
		return "", nil, err
	}
	if err := applySNSAttributes(ctx, router, rCtx.Region, "SetSubscriptionAttributes", "SubscriptionArn", physicalID, attributes); err != nil {
		return "", nil, err
	}
	return physicalID, map[string]string{
		"Arn":      physicalID,
		"TopicArn": cfnScalarString(props["TopicArn"]),
		"Protocol": cfnScalarString(props["Protocol"]),
		"Endpoint": cfnScalarString(props["Endpoint"]),
	}, nil
}

// ── S3 Bucket handler ──────────────────────────────────────────────────────

type s3BucketHandler struct{}

func (h *s3BucketHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	decoded, err := decodeS3BucketProperties(props)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	oldDecoded, err := decodeS3BucketProperties(oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	// BucketName is immutable, and so are BucketNamespace and
	// BucketNamePrefix — AWS documents both as update:Replacement, the same
	// as BucketName itself, since either one changes what bucket the
	// resource names.
	if decoded.BucketName != oldDecoded.BucketName {
		return "", nil, errReplacementRequired
	}
	if cfnS3OptionalStringChanged(decoded.BucketNamespace, oldDecoded.BucketNamespace) {
		return "", nil, errReplacementRequired
	}
	if cfnS3OptionalStringChanged(decoded.BucketNamePrefix, oldDecoded.BucketNamePrefix) {
		return "", nil, errReplacementRequired
	}
	operations, err := planS3BucketOperations(decoded, oldDecoded)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	compensations, err := planS3BucketOperations(oldDecoded, decoded)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	applied, err := applyS3BucketOperations(ctx, router, rCtx.Region, physicalID, operations)
	if err != nil {
		compensationErr := compensateS3BucketOperations(ctx, router, rCtx.Region, physicalID, operations[:applied], compensations)
		return "", nil, failUpdate(errors.Join(err, compensationErr))
	}
	return physicalID, s3BucketAttrs(cfg, physicalID, rCtx.Region), nil
}

func (h *s3BucketHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	decoded, err := decodeS3BucketProperties(props)
	if err != nil {
		return "", nil, err
	}
	operations, err := planS3BucketOperations(decoded, nil)
	if err != nil {
		return "", nil, err
	}
	bucketName := decoded.BucketName
	switch {
	case decoded.BucketNamePrefix != nil:
		// CFN's analogue of the console auto-appending the account-regional
		// suffix: the API itself requires the full name, but BucketNamePrefix
		// lets a template supply only the customer-chosen part.
		bucketName = *decoded.BucketNamePrefix + serviceutil.AccountRegionalBucketSuffix(rCtx.AccountID, rCtx.Region)
	case bucketName == "":
		bucketName = rCtx.generatedNameLowerWithin(maxNameLenS3)
	}

	var extraHeaders []http.Header
	if namespace := s3BucketEffectiveNamespace(decoded); namespace != "" {
		header := http.Header{}
		header.Set("X-Amz-Bucket-Namespace", namespace)
		extraHeaders = append(extraHeaders, header)
	}

	_, err = internalS3Request(ctx, router, rCtx.Region, http.MethodPut, "/"+bucketName, "", nil, extraHeaders...)
	if err != nil {
		return "", nil, fmt.Errorf("s3 CreateBucket: %w", err)
	}
	if _, err := applyS3BucketOperations(ctx, router, rCtx.Region, bucketName, operations); err != nil {
		return bucketName, nil, err
	}
	return bucketName, s3BucketAttrs(cfg, bucketName, rCtx.Region), nil
}

// s3BucketAttrs builds the Fn::GetAtt attributes for an AWS::S3::Bucket.
//
// DomainName and RegionalDomainName are minted on the hostname THIS emulator
// answers on, not the literal "amazonaws.com" AWS returns. They are not
// decoration: CDK wires an S3 origin into a CloudFront distribution by
// Fn::GetAtt-ing RegionalDomainName, so an amazonaws.com value made the
// distribution front the real AWS bucket of that name — or nothing at all —
// rather than the bucket the same stack had just created here.
//
// Minted through the shared helper every other client-facing hostname uses, so
// the grammar cannot drift from what the router resolves: the result is
// "{bucket}.s3[.{region}].{host}", which HostClassifier tier A recognises by
// its ".s3." separator on any base, including OVERCAST_HOSTNAME and the
// wildcard-DNS domains.
func s3BucketAttrs(cfg *config.Config, bucket, region string) map[string]string {
	base := "http://localhost:4566"
	if cfg != nil {
		base = cfg.ExternalBaseURL()
	}
	return map[string]string{
		"Arn":                 fmt.Sprintf("arn:aws:s3:::%s", bucket),
		"BucketName":          bucket,
		"DomainName":          serviceutil.HostRoutedHostnameFromBase(base, "s3", bucket, ""),
		"DualStackDomainName": serviceutil.HostRoutedHostnameFromBase(base, "s3.dualstack", bucket, region),
		"RegionalDomainName":  serviceutil.HostRoutedHostnameFromBase(base, "s3", bucket, region),
		"WebsiteURL":          serviceutil.HostRoutedURLFromBase(base, "s3-website", bucket, region, ""),
	}
}

func (h *s3BucketHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalS3Request(ctx, router, rCtx.Region, http.MethodDelete, "/"+physicalID, "", nil)
	if err == nil || (rec != nil && rec.Code == http.StatusNotFound) {
		return nil
	}
	return fmt.Errorf("%w: s3 DeleteBucket: %v", errDeletionBlocked, err)
}

// ── DynamoDB Table handler ─────────────────────────────────────────────────

type dynamodbTableHandler struct{}

type dynamodbTTLConfig struct {
	specified     bool
	enabled       bool
	attributeName string
}

func (h *dynamodbTableHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// TableName and KeySchema are immutable.
	if n, ok := props["TableName"].(string); ok && n != "" && n != physicalID {
		return "", nil, errReplacementRequired
	}
	newTTL, err := parseDynamoDBTTLConfig(props, rCtx.LogicalID)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	oldTTL, err := parseDynamoDBTTLConfig(oldProps, rCtx.LogicalID)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	if changed, err := cfnPropertyChanged(props, oldProps, "LocalSecondaryIndexes"); err != nil {
		return "", nil, failUpdate(err)
	} else if changed {
		return "", nil, failUpdate(fmt.Errorf("AWS::DynamoDB::Table LocalSecondaryIndexes updates are not supported"))
	}
	// Diff and validate GlobalSecondaryIndexes before dispatching anything, so
	// a template with an unsupported index-schema change fails whole rather
	// than half-applied.
	gsiUpdates, err := dynamodbGSIUpdates(props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}

	reqBody := map[string]any{"TableName": physicalID}
	haveMutable := false
	if v, ok := props["BillingMode"]; ok {
		reqBody["BillingMode"] = v
		haveMutable = true
	}
	if v, ok := props["ProvisionedThroughput"]; ok {
		reqBody["ProvisionedThroughput"] = v
		haveMutable = true
	}
	if v, ok := props["AttributeDefinitions"]; ok {
		reqBody["AttributeDefinitions"] = v
		haveMutable = true
	}
	if v, ok := props["StreamSpecification"].(map[string]any); ok {
		if _, hasViewType := v["StreamViewType"]; hasViewType {
			v["StreamEnabled"] = true
		}
		reqBody["StreamSpecification"] = v
		haveMutable = true
	}
	// TimeToLiveSpecification is a separate API call. Apply it first so a
	// later UpdateTable failure can restore the previous TTL configuration.
	restoreTTL, ttlDirty, err := reconcileDynamoDBTableTTL(ctx, router, rCtx.Region, physicalID, newTTL, oldTTL)
	if err != nil {
		if ttlDirty {
			return "", nil, failDirtyUpdate(fmt.Errorf("dynamodb UpdateTimeToLive: %w", err))
		}
		return "", nil, failUpdate(fmt.Errorf("dynamodb UpdateTimeToLive: %w", err))
	}
	if haveMutable {
		if _, err := internalJSON(ctx, router, rCtx.Region, "DynamoDB_20120810.UpdateTable", reqBody); err != nil {
			updateErr := fmt.Errorf("dynamodb UpdateTable: %w", err)
			if restoreTTL == nil {
				return "", nil, updateErr
			}
			if restoreErr := restoreTTL(); restoreErr != nil {
				return "", nil, failDirtyUpdate(errors.Join(updateErr, fmt.Errorf("restore DynamoDB TTL after failed table update: %w", restoreErr)))
			}
			return "", nil, failUpdate(updateErr)
		}
	}
	// GlobalSecondaryIndexUpdates: one operation per UpdateTable call, matching
	// AWS's one-index-change-at-a-time rule; a failure partway through leaves
	// earlier index changes applied with no compensation, so it is dirty.
	for _, update := range gsiUpdates {
		body := map[string]any{
			"TableName":                   physicalID,
			"GlobalSecondaryIndexUpdates": []any{update},
		}
		if _, err := internalJSON(ctx, router, rCtx.Region, "DynamoDB_20120810.UpdateTable", body); err != nil {
			return "", nil, failDirtyUpdate(fmt.Errorf("dynamodb UpdateTable (GlobalSecondaryIndexUpdates): %w", err))
		}
	}
	if err := reconcileDynamoDBTags(ctx, router, rCtx.Region, dynamodbTableARN(rCtx.Region, rCtx.AccountID, physicalID), rCtx.StackTags, rCtx.PreviousStackTags, props["Tags"], oldProps["Tags"]); err != nil {
		return "", nil, failDirtyUpdate(err)
	}
	return physicalID, map[string]string{"TableName": physicalID}, nil
}

func (h *dynamodbTableHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	ttl, err := parseDynamoDBTTLConfig(props, rCtx.LogicalID)
	if err != nil {
		return "", nil, err
	}
	tableName, _ := props["TableName"].(string)
	if tableName == "" {
		tableName = rCtx.generatedName()
	}

	// Build the CreateTable request.
	reqBody := map[string]any{
		"TableName": tableName,
	}
	// Copy key schema and attribute definitions.
	if ks, ok := props["KeySchema"]; ok {
		reqBody["KeySchema"] = ks
	}
	if ad, ok := props["AttributeDefinitions"]; ok {
		reqBody["AttributeDefinitions"] = ad
	}
	// BillingMode and ProvisionedThroughput are forwarded exactly as declared.
	// DynamoDB owns the defaulting (an omitted BillingMode is PROVISIONED) and
	// the required/forbidden throughput combinations, so an invalid template
	// surfaces DynamoDB's own modeled ValidationException through stack
	// failure rather than being silently rewritten to on-demand billing here.
	if bt, ok := props["BillingMode"]; ok {
		reqBody["BillingMode"] = bt
	}
	if pt, ok := props["ProvisionedThroughput"]; ok {
		reqBody["ProvisionedThroughput"] = pt
	}
	if gsi, ok := props["GlobalSecondaryIndexes"]; ok {
		reqBody["GlobalSecondaryIndexes"] = gsi
	}
	if lsi, ok := props["LocalSecondaryIndexes"]; ok {
		reqBody["LocalSecondaryIndexes"] = lsi
	}
	if ss, ok := props["StreamSpecification"].(map[string]any); ok {
		// CloudFormation templates set StreamViewType without an explicit StreamEnabled.
		// Enable the stream whenever StreamViewType is provided.
		if _, hasViewType := ss["StreamViewType"]; hasViewType {
			ss["StreamEnabled"] = true
		}
		reqBody["StreamSpecification"] = ss
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "DynamoDB_20120810.CreateTable", reqBody)
	if err != nil {
		return "", nil, fmt.Errorf("dynamodb CreateTable: %w", err)
	}
	if ttl.enabled {
		if err := updateDynamoDBTableTTL(ctx, router, rCtx.Region, tableName, dynamodbTTLSpec(ttl)); err != nil {
			ttlErr := fmt.Errorf("dynamodb UpdateTimeToLive: %w", err)
			if _, deleteErr := internalJSON(ctx, router, rCtx.Region, "DynamoDB_20120810.DeleteTable", map[string]any{"TableName": tableName}); deleteErr != nil {
				return tableName, nil, errors.Join(ttlErr, fmt.Errorf("delete DynamoDB table after TTL failure: %w", deleteErr))
			}
			return "", nil, ttlErr
		}
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		if err := dynamodbTagResource(ctx, router, rCtx.Region, dynamodbTableARN(rCtx.Region, rCtx.AccountID, tableName), tags); err != nil {
			tagErr := fmt.Errorf("dynamodb TagResource: %w", err)
			if _, deleteErr := internalJSON(ctx, router, rCtx.Region, "DynamoDB_20120810.DeleteTable", map[string]any{"TableName": tableName}); deleteErr != nil {
				return tableName, nil, errors.Join(tagErr, fmt.Errorf("delete DynamoDB table after TagResource failure: %w", deleteErr))
			}
			return "", nil, tagErr
		}
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if td, ok := resp["TableDescription"].(map[string]any); ok {
			if arn, ok := td["TableArn"].(string); ok {
				attrs := map[string]string{
					"Arn":       arn,
					"TableName": tableName,
				}
				if sa, ok := td["LatestStreamArn"].(string); ok {
					attrs["StreamArn"] = sa
				}
				return tableName, attrs, nil
			}
		}
	}
	return tableName, map[string]string{"TableName": tableName}, nil
}

// errDynamoDBTTLAttributeChange is AWS's refusal of a TimeToLiveSpecification
// whose AttributeName changes while TTL is enabled. The AWS::DynamoDB::Table
// provider rejects the template outright rather than issuing a disable and an
// enable: DynamoDB would refuse the second call as another UpdateTimeToLive
// inside the same transition window, leaving TTL disabled and the stack
// half-applied.
var errDynamoDBTTLAttributeChange = errors.New(
	"Invalid request provided: Cannot change time-to-live attribute name. " +
		"To update this property, you must first disable TTL then enable TTL with the new attribute name.")

// reconcileDynamoDBTableTTL applies the desired CloudFormation TTL state and
// returns a compensating function when it changed DynamoDB.
//
// The bool result is true only when a failed transition could not restore its
// previous configuration; callers must retain that dirty resource for stack
// rollback rather than claiming it is unchanged.
func reconcileDynamoDBTableTTL(ctx context.Context, router http.Handler, region, tableName string, newConfig, oldConfig dynamodbTTLConfig) (func() error, bool, error) {
	if reflect.DeepEqual(newConfig, oldConfig) {
		return nil, false, nil
	}

	if oldConfig.enabled && newConfig.enabled && oldConfig.attributeName != newConfig.attributeName {
		return nil, false, errDynamoDBTTLAttributeChange
	}

	appliedConfig, changed, err := applyDynamoDBTableTTL(ctx, router, region, tableName, oldConfig, newConfig)
	if err != nil || !changed {
		return nil, false, err
	}
	return func() error {
		_, _, restoreErr := applyDynamoDBTableTTL(ctx, router, region, tableName, appliedConfig, oldConfig)
		return restoreErr
	}, false, nil
}

func applyDynamoDBTableTTL(ctx context.Context, router http.Handler, region, tableName string, current, desired dynamodbTTLConfig) (dynamodbTTLConfig, bool, error) {
	if reflect.DeepEqual(current, desired) {
		return current, false, nil
	}
	if !current.enabled && !desired.enabled {
		return current, false, nil
	}
	if !desired.specified {
		if !current.enabled {
			return current, false, nil
		}
		if err := updateDynamoDBTableTTL(ctx, router, region, tableName, dynamodbTTLDisableSpec(current)); err != nil {
			return current, false, err
		}
		return dynamodbTTLConfig{specified: true, attributeName: current.attributeName}, true, nil
	}
	if current.enabled && !desired.enabled {
		if err := updateDynamoDBTableTTL(ctx, router, region, tableName, dynamodbTTLDisableSpec(current)); err != nil {
			return current, false, err
		}
		return dynamodbTTLConfig{specified: true, attributeName: current.attributeName}, true, nil
	}
	if err := updateDynamoDBTableTTL(ctx, router, region, tableName, dynamodbTTLSpec(desired)); err != nil {
		return current, false, err
	}
	return desired, true, nil
}

func parseDynamoDBTTLConfig(props map[string]any, logicalID string) (dynamodbTTLConfig, error) {
	raw, specified := props["TimeToLiveSpecification"]
	if !specified || raw == nil {
		return dynamodbTTLConfig{}, nil
	}
	spec, ok := raw.(map[string]any)
	if !ok {
		return dynamodbTTLConfig{}, dynamodbTTLValidationError(logicalID, "#/TimeToLiveSpecification: expected type: JSONObject")
	}
	rawEnabled, hasEnabled := spec["Enabled"]
	if !hasEnabled || rawEnabled == nil {
		return dynamodbTTLConfig{}, dynamodbTTLValidationError(logicalID, "#/TimeToLiveSpecification: required key [Enabled] not found")
	}
	enabled, validEnabled := rawEnabled.(bool)
	if !validEnabled {
		if value, ok := rawEnabled.(string); ok && (strings.EqualFold(value, "true") || strings.EqualFold(value, "false")) {
			enabled = strings.EqualFold(value, "true")
			validEnabled = true
		}
	}
	if !validEnabled {
		return dynamodbTTLConfig{}, dynamodbTTLValidationError(logicalID, "#/TimeToLiveSpecification/Enabled: expected type: Boolean")
	}

	attributeName := ""
	if rawAttribute, hasAttribute := spec["AttributeName"]; hasAttribute && rawAttribute != nil {
		var validAttribute bool
		attributeName, validAttribute = rawAttribute.(string)
		if !validAttribute {
			return dynamodbTTLConfig{}, dynamodbTTLValidationError(logicalID, "#/TimeToLiveSpecification/AttributeName: expected type: String")
		}
		length := utf8.RuneCountInString(attributeName)
		if length < 1 {
			return dynamodbTTLConfig{}, dynamodbTTLValidationError(logicalID, fmt.Sprintf("#/TimeToLiveSpecification/AttributeName: expected minLength: 1, actual: %d", length))
		}
		if length > 255 {
			return dynamodbTTLConfig{}, dynamodbTTLValidationError(logicalID, fmt.Sprintf("#/TimeToLiveSpecification/AttributeName: expected maxLength: 255, actual: %d", length))
		}
	}
	if enabled && attributeName == "" {
		return dynamodbTTLConfig{}, dynamodbTTLValidationError(logicalID, "#/TimeToLiveSpecification: required key [AttributeName] not found")
	}
	return dynamodbTTLConfig{specified: true, enabled: enabled, attributeName: attributeName}, nil
}

func dynamodbTTLValidationError(logicalID, detail string) error {
	resource := logicalID
	if resource == "" {
		resource = "AWS::DynamoDB::Table"
	}
	return fmt.Errorf("Properties validation failed for resource %s with message: %s", resource, detail)
}

func dynamodbTTLSpec(config dynamodbTTLConfig) map[string]any {
	return map[string]any{
		"Enabled":       config.enabled,
		"AttributeName": config.attributeName,
	}
}

func dynamodbTTLDisableSpec(config dynamodbTTLConfig) map[string]any {
	return map[string]any{
		"Enabled":       false,
		"AttributeName": config.attributeName,
	}
}

func updateDynamoDBTableTTL(ctx context.Context, router http.Handler, region, tableName string, spec map[string]any) error {
	_, err := internalJSON(ctx, router, region, "DynamoDB_20120810.UpdateTimeToLive", map[string]any{
		"TableName":               tableName,
		"TimeToLiveSpecification": spec,
	})
	return err
}

func (h *dynamodbTableHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	name := physicalID
	if i := strings.LastIndex(physicalID, "/"); i >= 0 {
		name = physicalID[i+1:]
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "DynamoDB_20120810.DeleteTable", map[string]any{
		"TableName": name,
	})
	return teardownError("DeleteTable", rec, err)
}

// dynamodbTableARN builds the ARN DynamoDB's own handler stamps onto a table
// (internal/services/dynamodb/handler.go), for CloudFormation calls — like
// TagResource — that address a table by ARN rather than by name.
func dynamodbTableARN(region, accountID, tableName string) string {
	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", region, accountID, tableName)
}

func dynamodbTagResource(ctx context.Context, router http.Handler, region, tableARN string, tags map[string]string) error {
	tagList := make([]map[string]string, 0, len(tags))
	for key, value := range tags {
		tagList = append(tagList, map[string]string{"Key": key, "Value": value})
	}
	_, err := internalJSON(ctx, router, region, "DynamoDB_20120810.TagResource", map[string]any{
		"ResourceArn": tableARN,
		"Tags":        tagList,
	})
	return err
}

func dynamodbUntagResource(ctx context.Context, router http.Handler, region, tableARN string, keys []string) error {
	_, err := internalJSON(ctx, router, region, "DynamoDB_20120810.UntagResource", map[string]any{
		"ResourceArn": tableARN,
		"TagKeys":     keys,
	})
	return err
}

// reconcileDynamoDBTags mirrors updateLambdaTags's add/remove diff (#523),
// dispatched through DynamoDB's JSON-RPC Tag/UntagResource rather than
// Lambda's REST tag endpoints.
func reconcileDynamoDBTags(ctx context.Context, router http.Handler, region, tableARN string, stackTags, priorStackTags []Tag, rawTags, rawPrior any) error {
	tags := mergeResourceTags(stackTags, rawTags)
	prior := mergeResourceTags(priorStackTags, rawPrior)
	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	var removed []string
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	if len(added) > 0 {
		if err := dynamodbTagResource(ctx, router, region, tableARN, added); err != nil {
			return fmt.Errorf("dynamodb TagResource: %w", err)
		}
	}
	if len(removed) > 0 {
		if err := dynamodbUntagResource(ctx, router, region, tableARN, removed); err != nil {
			return fmt.Errorf("dynamodb UntagResource: %w", err)
		}
	}
	return nil
}

// dynamodbIndexedGSIs reads the GlobalSecondaryIndexes property into a
// name → definition map so create/update/delete can be diffed by IndexName.
func dynamodbIndexedGSIs(props map[string]any) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	raw, ok := props["GlobalSecondaryIndexes"]
	if !ok || raw == nil {
		return out, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("GlobalSecondaryIndexes must be a list")
	}
	for _, item := range list {
		gsi, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("GlobalSecondaryIndexes entries must be objects")
		}
		name, _ := gsi["IndexName"].(string)
		if name == "" {
			return nil, fmt.Errorf("GlobalSecondaryIndexes entries require IndexName")
		}
		out[name] = gsi
	}
	return out, nil
}

// dynamodbGSIUpdates diffs desired against previous GlobalSecondaryIndexes
// and returns one GlobalSecondaryIndexUpdates entry per changed index, in
// delete-then-create-then-throughput-update order, ready to dispatch one at a
// time — DynamoDB's UpdateTable accepts only one index operation per call,
// same as AWS. A KeySchema or Projection change on an existing index is
// rejected rather than attempted as an implicit delete+recreate, the same
// conservative stance the LocalSecondaryIndexes update check above takes:
// AWS itself requires that as two separate operations, and index deletion
// here discards the index's rows.
func dynamodbGSIUpdates(props, oldProps map[string]any) ([]map[string]any, error) {
	desired, err := dynamodbIndexedGSIs(props)
	if err != nil {
		return nil, err
	}
	previous, err := dynamodbIndexedGSIs(oldProps)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(desired)+len(previous))
	seen := make(map[string]struct{}, len(desired)+len(previous))
	for name := range desired {
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for name := range previous {
		if _, ok := seen[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var deletes, creates, updates []map[string]any
	for _, name := range names {
		newGSI, isNew := desired[name]
		oldGSI, existed := previous[name]
		switch {
		case existed && !isNew:
			deletes = append(deletes, map[string]any{"Delete": map[string]any{"IndexName": name}})
		case isNew && !existed:
			creates = append(creates, map[string]any{"Create": newGSI})
		case isNew && existed:
			schemaChanged, err := cfnPropertyChanged(newGSI, oldGSI, "KeySchema")
			if err != nil {
				return nil, err
			}
			projectionChanged, err := cfnPropertyChanged(newGSI, oldGSI, "Projection")
			if err != nil {
				return nil, err
			}
			if schemaChanged || projectionChanged {
				return nil, fmt.Errorf("AWS::DynamoDB::Table GlobalSecondaryIndexes %s: KeySchema and Projection changes are not supported; remove and re-add the index in a separate update", name)
			}
			throughputChanged, err := cfnPropertyChanged(newGSI, oldGSI, "ProvisionedThroughput")
			if err != nil {
				return nil, err
			}
			if throughputChanged {
				updates = append(updates, map[string]any{"Update": map[string]any{"IndexName": name, "ProvisionedThroughput": newGSI["ProvisionedThroughput"]}})
			}
		}
	}
	result := make([]map[string]any, 0, len(deletes)+len(creates)+len(updates))
	result = append(result, deletes...)
	result = append(result, creates...)
	result = append(result, updates...)
	return result, nil
}

// ── DynamoDB GlobalTable handler ────────────────────────────────────────────

// dynamodbGlobalTableHandler provisions AWS::DynamoDB::GlobalTable as a real
// table for the stack's own deploy region, replacing the no-op stub that
// previously registered it (#523): a stack that declared a global table
// provisioned "successfully" and created no table at all, so every later
// operation against it failed. Overcast emulates a single region, so
// cross-region replication is out of scope — the contract here is to
// provision the Replicas entry matching rCtx.Region faithfully, or fail the
// stack loudly when no Replica names that region, rather than silently doing
// nothing.
//
// Reuses AWS::DynamoDB::Table's handler outright: translate GlobalTable's
// top-level-plus-matching-Replica properties into the Table's property shape
// (see dynamodbGlobalTableReplicaProps) and delegate, so GSI/TTL/Tags
// reconciliation, billing-mode fidelity and error handling live in exactly
// one place rather than a second copy drifting from it.
type dynamodbGlobalTableHandler struct{}

func (h *dynamodbGlobalTableHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	tableProps, found, err := dynamodbGlobalTableReplicaProps(props, rCtx.Region)
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, fmt.Errorf("AWS::DynamoDB::GlobalTable must declare a Replica for the stack's deploy region %s; Overcast emulates a single region and provisions only the Replica matching it", rCtx.Region)
	}
	return (&dynamodbTableHandler{}).Create(ctx, router, cfg, tableProps, rCtx)
}

func (h *dynamodbGlobalTableHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	oldTableProps, oldFound, err := dynamodbGlobalTableReplicaProps(oldProps, rCtx.Region)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	newTableProps, newFound, err := dynamodbGlobalTableReplicaProps(props, rCtx.Region)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	if !newFound {
		if oldFound {
			// The stack's own region dropped out of Replicas: the table this
			// resource stands for no longer has anywhere to live, so replace
			// it (delete the old table, evaluate the new template fresh)
			// rather than leaving a table CloudFormation no longer accounts
			// for or silently keeping the old one running unchanged.
			return "", nil, errReplacementRequired
		}
		return "", nil, failUpdate(fmt.Errorf("AWS::DynamoDB::GlobalTable must declare a Replica for the stack's deploy region %s", rCtx.Region))
	}
	return (&dynamodbTableHandler{}).Update(ctx, router, cfg, physicalID, newTableProps, oldTableProps, rCtx)
}

func (h *dynamodbGlobalTableHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	return (&dynamodbTableHandler{}).Delete(ctx, router, cfg, physicalID, rCtx)
}

// dynamodbGlobalTableReplicaProps translates AWS::DynamoDB::GlobalTable's
// top-level-plus-Replicas shape into the property shape AWS::DynamoDB::Table
// accepts, selecting the Replicas entry naming region. found is false when no
// Replicas entry names region.
//
// Global tables always stream (replication requires it), so
// StreamSpecification is forced on to NEW_AND_OLD_IMAGES regardless of the
// property — which this resource does not even expose, unlike
// AWS::DynamoDB::Table.
//
// PROVISIONED billing on a global table is expressed only through
// WriteProvisionedThroughputSettings/ReadProvisionedThroughputSettings' own
// auto-scaling settings, which nothing in Overcast's DynamoDB emulation runs;
// forwarding a guessed fixed ProvisionedThroughput derived from them would be
// worse than failing, so that combination is rejected outright. A BillingMode
// left unset defaults to PAY_PER_REQUEST here — global tables provisioned
// through the modern replication model default to on-demand, unlike
// AWS::DynamoDB::Table's PROVISIONED default.
func dynamodbGlobalTableReplicaProps(props map[string]any, region string) (map[string]any, bool, error) {
	rawReplicas, ok := props["Replicas"]
	if !ok || rawReplicas == nil {
		return nil, false, nil
	}
	replicas, ok := rawReplicas.([]any)
	if !ok {
		return nil, false, fmt.Errorf("AWS::DynamoDB::GlobalTable Replicas must be a list")
	}
	var replica map[string]any
	for _, item := range replicas {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("AWS::DynamoDB::GlobalTable Replicas entries must be objects")
		}
		if entryRegion, _ := entry["Region"].(string); entryRegion == region {
			replica = entry
			break
		}
	}
	if replica == nil {
		return nil, false, nil
	}

	if _, provisioned := props["WriteProvisionedThroughputSettings"]; provisioned {
		return nil, false, fmt.Errorf("AWS::DynamoDB::GlobalTable WriteProvisionedThroughputSettings (PROVISIONED billing) is not supported by CloudFormation provisioning; use BillingMode PAY_PER_REQUEST")
	}
	if _, provisioned := replica["ReadProvisionedThroughputSettings"]; provisioned {
		return nil, false, fmt.Errorf("AWS::DynamoDB::GlobalTable ReadProvisionedThroughputSettings (PROVISIONED billing) is not supported by CloudFormation provisioning; use BillingMode PAY_PER_REQUEST")
	}

	tableProps := map[string]any{}
	for _, property := range []string{"TableName", "AttributeDefinitions", "KeySchema", "GlobalSecondaryIndexes", "TimeToLiveSpecification", "Tags"} {
		if v, ok := props[property]; ok {
			tableProps[property] = v
		}
	}
	billingMode, _ := props["BillingMode"].(string)
	switch billingMode {
	case "", "PAY_PER_REQUEST":
		tableProps["BillingMode"] = "PAY_PER_REQUEST"
	case "PROVISIONED":
		return nil, false, fmt.Errorf("AWS::DynamoDB::GlobalTable BillingMode PROVISIONED is not supported by CloudFormation provisioning; use BillingMode PAY_PER_REQUEST")
	default:
		return nil, false, fmt.Errorf("AWS::DynamoDB::GlobalTable BillingMode %q is not a supported value", billingMode)
	}
	tableProps["StreamSpecification"] = map[string]any{"StreamViewType": "NEW_AND_OLD_IMAGES"}
	return tableProps, true, nil
}

// ── Lambda Function handler ────────────────────────────────────────────────

type lambdaFunctionHandler struct{}

// lambdaCreateFunctionBody builds the CreateFunction request an
// AWS::Lambda::Function template resource dispatches, with code already
// packaged. It is a pure function of its inputs so the set of request members
// this resource can forward is derivable by running it — see
// TestLambdaProvisionerForwardsOnlyReviewedGatedMembers.
func lambdaCreateFunctionBody(funcName string, code map[string]any, props map[string]any, stackTags []Tag) map[string]any {
	body := map[string]any{
		"FunctionName": funcName,
		"Runtime":      props["Runtime"],
		"Handler":      props["Handler"],
		"Role":         props["Role"],
		"Code":         code,
	}
	for _, property := range lambdaCreateFunctionForwardedProperties {
		copyAnyProp(body, props, property, property)
	}
	copyAnyProp(body, props, "KmsKeyArn", "KMSKeyArn")
	if tagMap := mergeResourceTags(stackTags, props["Tags"]); len(tagMap) > 0 {
		body["Tags"] = tagMap
	}
	if publish, _ := props["PublishToLatestPublished"].(bool); publish {
		body["PublishTo"] = "LATEST_PUBLISHED"
	}
	return body
}

// lambdaCreateFunctionForwardedProperties are the template properties copied
// straight through to CreateFunction under the same name. CodeSigningConfigArn
// is set by CDK's Function `codeSigningConfig` prop and is passed through so
// the association survives a deploy — Lambda stores it without enforcing
// signature validation.
var lambdaCreateFunctionForwardedProperties = []string{
	"Architectures", "VpcConfig", "FileSystemConfigs", "ImageConfig", "PackageType",
	"DeadLetterConfig", "TracingConfig", "EphemeralStorage", "SnapStart",
	"CapacityProviderConfig", "DurableConfig", "TenancyConfig",
	"Description", "Environment", "Timeout", "MemorySize", "LoggingConfig", "Layers",
	"CodeSigningConfigArn",
}

func (h *lambdaFunctionHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	funcName, _ := props["FunctionName"].(string)
	if funcName == "" {
		funcName = rCtx.generatedNameWithin(maxNameLenLambda)
	}
	if err := checkLambdaFunctionAuxiliaryPropertySupport(props, nil); err != nil {
		return "", nil, err
	}

	// A template's Code.ZipFile is inline source text; the Lambda API's is
	// base64 of a zip archive. Package it rather than just encoding it — see
	// inlineCodeZip.
	runtime, _ := props["Runtime"].(string)
	handler, _ := props["Handler"].(string)
	code, _ := props["Code"].(map[string]any)
	if zf, ok := code["ZipFile"].(string); ok {
		packaged, err := inlineCodeZip(zf, runtime, handler)
		if err != nil {
			return "", nil, fmt.Errorf("package inline function code: %w", err)
		}
		code["ZipFile"] = base64.StdEncoding.EncodeToString(packaged)
	}

	data, _ := json.Marshal(lambdaCreateFunctionBody(funcName, code, props, rCtx.StackTags))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2015-03-31/functions", "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("lambda CreateFunction: %w", err)
	}
	if reserved, ok := props["ReservedConcurrentExecutions"]; ok {
		if err := putLambdaReservedConcurrency(ctx, router, rCtx.Region, funcName, reserved); err != nil {
			_, cleanupErr := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/2015-03-31/functions/"+url.PathEscape(funcName), "", nil)
			if cleanupErr != nil {
				return funcName, nil, errors.Join(err, fmt.Errorf("lambda cleanup DeleteFunction: %w", cleanupErr))
			}
			return "", nil, err
		}
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if arn, ok := resp["FunctionArn"].(string); ok {
			attrs := map[string]string{
				"Arn":          arn,
				"FunctionName": funcName,
			}
			return funcName, attrs, nil
		}
	}
	return funcName, map[string]string{"FunctionName": funcName}, nil
}

// lambdaStabilizeTimeout bounds the wait for a function to become invocable.
// AWS's own FunctionActive waiter allows five minutes (5s × 60 attempts); this
// is twice that, because on real AWS the Pending window is seconds — a minute
// or two for a function with VPC or EFS resources to configure — while here it
// is however long the image takes to pull, which on a cold pull of a large
// runtime image is minutes. The function's own "Failed" state is the fast way
// out, so this budget only bites on one that never resolves either way.
const lambdaStabilizeTimeout = 10 * time.Minute

// lambdaFunctionStatuses is botocore's FunctionActive waiter, whose acceptors
// over GetFunctionConfiguration are success on Active, failure on Failed, and
// retry on Pending — the same three this classifies, with anything else
// treated as work in progress.
//
// "Failed" is terminal by AWS's own account of it — a function whose
// provisioning failed has to be deleted and recreated, so waiting one out only
// delays the stack's rollback. "Inactive" is not, and the waiter does not name
// it either: it means Lambda reclaimed an idle function's resources, and the
// next invoke puts it back into Pending.
var lambdaFunctionStatuses = statusVocabulary{
	ready:  []string{"Active"},
	failed: []string{"Failed"},
}

// Stabilize holds the resource open until Lambda reports the function Active,
// which is what real CloudFormation waits for and what Overcast did not: a
// stack completed while the function's image was still being pulled, and AWS
// documents that an invoke — or an UpdateFunctionCode, or a PublishVersion —
// against a Pending function fails.
//
// The wait deliberately stops there. "Active" means deployed, not working: a
// function with a broken handler reaches Active on real AWS too and fails at
// invoke, so proving the handler runs is not something CloudFormation does and
// not something this should start doing. It would make every stack pay a cold
// start, and would fail deploys that AWS completes.
//
// LastUpdateStatus is the other half of what real CloudFormation reads — the
// update side of the same question, which AWS ships a separate FunctionUpdated
// waiter for — and the Lambda service now reports it (#1550), so it is folded
// into the same wait: an update that is still landing keeps the resource open,
// and one that failed fails the stack the way a failed create does. A function
// that has never carried the field (created before it existed) reports nothing
// there and is judged on State alone.
//
// See resourceStabilizer.
func (h *lambdaFunctionHandler) Stabilize(ctx context.Context, router http.Handler, _ *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	name := physicalID
	if i := strings.LastIndex(physicalID, ":"); i >= 0 {
		name = physicalID[i+1:]
	}
	subject := fmt.Sprintf("function %s", name)
	return awaitResourceReady(ctx, clk, stabilizeWait{
		subject:  subject,
		goal:     "become Active",
		timeout:  lambdaStabilizeTimeout,
		statuses: lambdaFunctionStatuses,
		describe: func(ctx context.Context) (string, string, error) {
			rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodGet,
				"/2015-03-31/functions/"+url.PathEscape(name)+"/configuration", "", nil)
			if err != nil {
				return "", "", fmt.Errorf("lambda GetFunctionConfiguration: %s: %w", subject, err)
			}
			var resp struct {
				State                      string `json:"State"`
				StateReason                string `json:"StateReason"`
				StateReasonCode            string `json:"StateReasonCode"`
				LastUpdateStatus           string `json:"LastUpdateStatus"`
				LastUpdateStatusReason     string `json:"LastUpdateStatusReason"`
				LastUpdateStatusReasonCode string `json:"LastUpdateStatusReasonCode"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				return "", "", fmt.Errorf("lambda GetFunctionConfiguration: parse response: %w", err)
			}
			// StateReason is where the reason a function is not Active ends up
			// — a failed image pull says so there. The code on its own still
			// beats reporting nothing.
			reason := resp.StateReason
			if reason == "" {
				reason = resp.StateReasonCode
			}
			// An update in flight or failed is reported through the same
			// vocabulary: "InProgress" is neither ready nor failed, so it keeps
			// polling, and "Failed" is one of the terminal words.
			switch resp.LastUpdateStatus {
			case "InProgress":
				return "UpdateInProgress", reason, nil
			case "Failed":
				if r := resp.LastUpdateStatusReason; r != "" {
					reason = r
				} else if r := resp.LastUpdateStatusReasonCode; r != "" {
					reason = r
				}
				return "Failed", reason, nil
			}
			return resp.State, reason, nil
		},
	})
}

func (h *lambdaFunctionHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	name := physicalID
	if i := strings.LastIndex(physicalID, ":"); i >= 0 {
		name = physicalID[i+1:]
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/2015-03-31/functions/"+url.PathEscape(name), "", nil)
	return teardownError("lambda DeleteFunction", rec, err)
}

// Update implements in-place updates for AWS::Lambda::Function. Code changes
// are dispatched to UpdateFunctionCode (PUT /functions/{name}/code) and the
// remaining mutable configuration is dispatched to UpdateFunctionConfiguration
// (PUT /functions/{name}/configuration). The function name is immutable on
// real AWS, so when it changes the provisioner falls back to replacement.
func (h *lambdaFunctionHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name := physicalID
	if i := strings.LastIndex(physicalID, ":"); i >= 0 {
		name = physicalID[i+1:]
	}
	// FunctionName is immutable. Adding, changing, or explicitly removing it
	// all change the physical-name contract and therefore require replacement.
	if !reflect.DeepEqual(props["FunctionName"], oldProps["FunctionName"]) {
		return "", nil, errReplacementRequired
	}
	packageType, _ := props["PackageType"].(string)
	if packageType == "" {
		packageType = "Zip"
	}
	oldPackageType, _ := oldProps["PackageType"].(string)
	if oldPackageType == "" {
		oldPackageType = "Zip"
	}
	if packageType != oldPackageType {
		return "", nil, errReplacementRequired
	}

	for _, property := range []string{"DurableConfig", "TenancyConfig"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	if err := validateLambdaFunctionRequiredPropertyRemovals(props, oldProps, packageType, rCtx.LogicalID); err != nil {
		return "", nil, failUpdate(err)
	}
	if err := checkLambdaFunctionAuxiliaryPropertySupport(props, oldProps); err != nil {
		return "", nil, failUpdate(err)
	}

	arn, completed, err := h.applyUpdate(ctx, router, name, props, oldProps, rCtx)
	if err != nil {
		// A Lambda function update spans several APIs. Restore only mutations
		// that completed, in reverse order, so a rejected phase cannot change
		// the function's revision or LastModified during compensation.
		compensationErr := h.compensateUpdate(ctx, router, name, oldProps, props, completed, rCtx)
		if compensationErr == nil {
			return "", nil, failUpdate(err)
		}
		compensationErr = fmt.Errorf("restore Lambda function after failed update: %w", compensationErr)
		return "", nil, failDirtyUpdate(errors.Join(err, compensationErr))
	}
	attrs := map[string]string{"FunctionName": name, "Arn": protocol.LambdaARN(rCtx.Region, rCtx.AccountID, name)}
	if arn != "" {
		attrs["Arn"] = arn
	}
	return name, attrs, nil
}

type lambdaUpdateProgress struct {
	tags          bool
	configuration bool
	code          bool
	codeSigning   bool
}

// applyUpdate tracks tags first, then follows AWS's documented
// configuration-before-code ordering and CloudFormation's separate association
// and concurrency operations.
func (h *lambdaFunctionHandler) applyUpdate(ctx context.Context, router http.Handler, name string, props, prior map[string]any, rCtx *resolveContext) (string, lambdaUpdateProgress, error) {
	var completed lambdaUpdateProgress
	applied, err := updateLambdaTags(ctx, router, rCtx.Region, protocol.LambdaARN(rCtx.Region, rCtx.AccountID, name), rCtx.StackTags, rCtx.PreviousStackTags, props["Tags"], prior["Tags"])
	completed.tags = applied
	if err != nil {
		return "", completed, err
	}
	arn, applied, err := h.updateConfiguration(ctx, router, name, props, prior, rCtx)
	if err != nil {
		return "", completed, err
	}
	completed.configuration = applied
	if applied, err = h.updateCode(ctx, router, name, props, prior, rCtx); err != nil {
		return "", completed, err
	}
	completed.code = applied
	if applied, err = h.updateCodeSigningConfig(ctx, router, name, props, prior, rCtx); err != nil {
		return "", completed, err
	}
	completed.codeSigning = applied
	if err = h.updateConcurrency(ctx, router, name, props, prior, rCtx); err != nil {
		return "", completed, err
	}
	return arn, completed, nil
}

func (h *lambdaFunctionHandler) compensateUpdate(ctx context.Context, router http.Handler, name string, props, prior map[string]any, completed lambdaUpdateProgress, rCtx *resolveContext) error {
	var compensationErr error
	if completed.codeSigning {
		if _, err := h.updateCodeSigningConfig(ctx, router, name, props, prior, rCtx); err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	if completed.code {
		if _, err := h.updateCode(ctx, router, name, props, prior, rCtx); err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	if completed.configuration {
		if _, _, err := h.updateConfiguration(ctx, router, name, props, prior, rCtx); err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	if completed.tags {
		if _, err := updateLambdaTags(ctx, router, rCtx.Region, protocol.LambdaARN(rCtx.Region, rCtx.AccountID, name), rCtx.PreviousStackTags, rCtx.StackTags, props["Tags"], prior["Tags"]); err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	return compensationErr
}

// lambdaUpdateFunctionConfigurationBody builds the UpdateFunctionConfiguration
// request for a changed AWS::Lambda::Function. Pure, for the same reason as
// lambdaCreateFunctionBody.
func lambdaUpdateFunctionConfigurationBody(props, prior map[string]any) map[string]any {
	cfgBody := map[string]any{}
	for _, k := range lambdaUpdateFunctionConfigurationForwardedProperties {
		if v, ok := props[k]; ok && !reflect.DeepEqual(v, prior[k]) {
			cfgBody[k] = v
		}
	}
	if !reflect.DeepEqual(props["KmsKeyArn"], prior["KmsKeyArn"]) {
		copyAnyProp(cfgBody, props, "KmsKeyArn", "KMSKeyArn")
	}
	clearValues := map[string]any{
		"Description": "", "Environment": map[string]any{"Variables": map[string]any{}}, "Layers": []any{},
		"LoggingConfig": map[string]any{}, "VpcConfig": map[string]any{}, "FileSystemConfigs": []any{}, "ImageConfig": map[string]any{},
		"Timeout": 3, "MemorySize": 128,
	}
	for key, empty := range clearValues {
		if _, present := props[key]; !present {
			if _, existed := prior[key]; existed {
				cfgBody[key] = empty
			}
		}
	}
	return cfgBody
}

// lambdaUpdateFunctionConfigurationForwardedProperties are the template
// properties copied straight through to UpdateFunctionConfiguration under the
// same name when they change.
var lambdaUpdateFunctionConfigurationForwardedProperties = []string{
	"Runtime", "Handler", "Role", "Description", "Environment", "Timeout", "MemorySize", "Layers", "LoggingConfig",
	"VpcConfig", "FileSystemConfigs", "ImageConfig", "DeadLetterConfig", "TracingConfig", "EphemeralStorage",
	"SnapStart", "CapacityProviderConfig",
}

func (h *lambdaFunctionHandler) updateConfiguration(ctx context.Context, router http.Handler, name string, props, prior map[string]any, rCtx *resolveContext) (string, bool, error) {
	cfgBody := lambdaUpdateFunctionConfigurationBody(props, prior)
	var arn string
	if len(cfgBody) == 0 {
		return "", false, nil
	}
	data, _ := json.Marshal(cfgBody)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, "/2015-03-31/functions/"+url.PathEscape(name)+"/configuration", "application/json", data)
	if err != nil {
		return "", false, fmt.Errorf("lambda UpdateFunctionConfiguration: %w", err)
	}
	var resp map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &resp) == nil {
		arn, _ = resp["FunctionArn"].(string)
	}
	return arn, true, nil
}

func (h *lambdaFunctionHandler) updateCode(ctx context.Context, router http.Handler, name string, props, prior map[string]any, rCtx *resolveContext) (bool, error) {
	publish, _ := props["PublishToLatestPublished"].(bool)
	priorPublish, _ := prior["PublishToLatestPublished"].(bool)
	if reflect.DeepEqual(props["Code"], prior["Code"]) &&
		reflect.DeepEqual(props["Architectures"], prior["Architectures"]) &&
		(!publish || priorPublish) {
		return false, nil
	}
	// CFN templates supply ZipFile as inline source; UpdateFunctionCode expects
	// a base64 zip archive.
	if code, ok := props["Code"].(map[string]any); ok {
		body, err := lambdaUpdateFunctionCodeBody(code, props, prior)
		if err != nil {
			return false, err
		}
		data, _ := json.Marshal(body)
		if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, "/2015-03-31/functions/"+url.PathEscape(name)+"/code", "application/json", data); err != nil {
			return false, fmt.Errorf("lambda UpdateFunctionCode: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// lambdaUpdateFunctionCodeBody builds the UpdateFunctionCode request for a
// changed AWS::Lambda::Function. It errors only when the template's inline
// ZipFile cannot be packaged. Pure otherwise, for the same reason as
// lambdaCreateFunctionBody.
func lambdaUpdateFunctionCodeBody(code, props, prior map[string]any) (map[string]any, error) {
	body := map[string]any{}
	if zf, ok := code["ZipFile"].(string); ok {
		runtime, _ := props["Runtime"].(string)
		handler, _ := props["Handler"].(string)
		packaged, err := inlineCodeZip(zf, runtime, handler)
		if err != nil {
			return nil, fmt.Errorf("package inline function code: %w", err)
		}
		body["ZipFile"] = base64.StdEncoding.EncodeToString(packaged)
	}
	for _, property := range lambdaUpdateFunctionCodeForwardedCodeProperties {
		if v, ok := code[property]; ok {
			body[property] = v
		}
	}
	if v, ok := props["Architectures"]; ok {
		body["Architectures"] = v
	} else if _, existed := prior["Architectures"]; existed {
		body["Architectures"] = []string{"x86_64"}
	}
	if publish, _ := props["PublishToLatestPublished"].(bool); publish {
		body["PublishTo"] = "LATEST_PUBLISHED"
	}
	return body, nil
}

// lambdaUpdateFunctionCodeForwardedCodeProperties are the Code sub-properties
// copied straight through to UpdateFunctionCode under the same name.
var lambdaUpdateFunctionCodeForwardedCodeProperties = []string{
	"S3Bucket", "S3Key", "S3ObjectVersion", "ImageUri", "S3ObjectStorageMode", "SourceKMSKeyArn",
}

func (h *lambdaFunctionHandler) updateCodeSigningConfig(ctx context.Context, router http.Handler, name string, props, prior map[string]any, rCtx *resolveContext) (bool, error) {
	if reflect.DeepEqual(props["CodeSigningConfigArn"], prior["CodeSigningConfigArn"]) {
		return false, nil
	}
	path := "/2020-06-30/functions/" + url.PathEscape(name) + "/code-signing-config"
	if arn, _ := props["CodeSigningConfigArn"].(string); arn != "" {
		data, err := json.Marshal(map[string]any{"CodeSigningConfigArn": arn})
		if err != nil {
			return false, fmt.Errorf("lambda PutFunctionCodeSigningConfig: marshal request: %w", err)
		}
		if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, path, "application/json", data); err != nil {
			return false, fmt.Errorf("lambda PutFunctionCodeSigningConfig: %w", err)
		}
		return true, nil
	}
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil); err != nil {
		return false, fmt.Errorf("lambda DeleteFunctionCodeSigningConfig: %w", err)
	}
	return true, nil
}

func checkLambdaFunctionAuxiliaryPropertySupport(props, prior map[string]any) error {
	for _, property := range []string{"RuntimeManagementConfig", "RecursiveLoop", "FunctionScalingConfig"} {
		if reflect.DeepEqual(props[property], prior[property]) {
			continue
		}
		return fmt.Errorf("AWS::Lambda::Function property %s is not supported by CloudFormation provisioning", property)
	}
	return nil
}

func validateLambdaFunctionRequiredPropertyRemovals(props, prior map[string]any, packageType, logicalID string) error {
	required := []string{"Role", "Code"}
	if packageType == "Zip" {
		required = append(required, "Runtime", "Handler")
	}
	return validateRequiredPropertyRemovals(props, prior, logicalID, "AWS::Lambda::Function", required...)
}

func validateRequiredPropertyRemovals(props, prior map[string]any, logicalID, resourceType string, required ...string) error {
	for _, property := range required {
		if _, existed := prior[property]; !existed {
			continue
		}
		if value, present := props[property]; present && value != nil && fmt.Sprint(value) != "" {
			continue
		}
		return requiredPropertyMissing(logicalID, resourceType, property)
	}
	return nil
}

// requiredPropertyMissing formats the same "Properties validation failed for
// resource ..." error CloudFormation returns for a template-required property
// that is entirely absent — the Create-time counterpart to
// validateRequiredPropertyRemovals, which checks the same shape when an
// in-place update instead drops a property a prior template set.
func requiredPropertyMissing(logicalID, resourceType, property string) error {
	resource := logicalID
	if resource == "" {
		resource = resourceType
	}
	return fmt.Errorf("Properties validation failed for resource %s with message: #: required key [%s] not found", resource, property)
}

func updateLambdaTags(ctx context.Context, router http.Handler, region, resourceARN string, stackTags, priorStackTags []Tag, rawTags, rawPrior any) (bool, error) {
	tags := mergeResourceTags(stackTags, rawTags)
	prior := mergeResourceTags(priorStackTags, rawPrior)
	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	path := "/2017-03-31/tags/" + url.PathEscape(resourceARN)
	applied := false
	if len(added) > 0 {
		data, err := json.Marshal(map[string]any{"Tags": added})
		if err != nil {
			return false, fmt.Errorf("lambda TagResource: marshal request: %w", err)
		}
		if _, err := internalRequest(ctx, router, region, http.MethodPost, path, "application/json", data); err != nil {
			return false, fmt.Errorf("lambda TagResource: %w", err)
		}
		applied = true
	}
	if len(removed) > 0 {
		query := url.Values{"tagKeys": removed}.Encode()
		if _, err := internalRequest(ctx, router, region, http.MethodDelete, path+"?"+query, "", nil); err != nil {
			return applied, fmt.Errorf("lambda UntagResource: %w", err)
		}
		applied = true
	}
	return applied, nil
}

func (h *lambdaFunctionHandler) updateConcurrency(ctx context.Context, router http.Handler, name string, props, prior map[string]any, rCtx *resolveContext) error {
	if reflect.DeepEqual(props["ReservedConcurrentExecutions"], prior["ReservedConcurrentExecutions"]) {
		return nil
	}
	if reserved, ok := props["ReservedConcurrentExecutions"]; ok {
		if err := putLambdaReservedConcurrency(ctx, router, rCtx.Region, name, reserved); err != nil {
			return err
		}
		return nil
	} else if _, existed := prior["ReservedConcurrentExecutions"]; existed {
		path := "/2017-10-31/functions/" + url.PathEscape(name) + "/concurrency"
		if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil); err != nil {
			return fmt.Errorf("lambda DeleteFunctionConcurrency: %w", err)
		}
	}
	return nil
}

// ── Lambda reserved concurrency and permission handlers ──────────────────

// putLambdaReservedConcurrency dispatches CloudFormation's function property
// through Lambda's separate reserved-concurrency API.
func putLambdaReservedConcurrency(ctx context.Context, router http.Handler, region, functionName string, reserved any) error {
	body, err := json.Marshal(map[string]any{"ReservedConcurrentExecutions": reserved})
	if err != nil {
		return fmt.Errorf("lambda PutFunctionConcurrency: marshal request: %w", err)
	}
	path := "/2017-10-31/functions/" + url.PathEscape(functionName) + "/concurrency"
	if _, err := internalRequest(ctx, router, region, http.MethodPut, path, "application/json", body); err != nil {
		return fmt.Errorf("lambda PutFunctionConcurrency: %w", err)
	}
	return nil
}

// lambdaPermissionHandler keeps CloudFormation thin: Lambda validates and
// stores the resource-policy statement, while CloudFormation owns only the
// generated statement ID needed to remove it later.
type lambdaPermissionHandler struct{}

func (h *lambdaPermissionHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	functionName, _ := props["FunctionName"].(string)
	statementID := rCtx.generatedNameWithin(100)
	body := map[string]any{"StatementId": statementID}
	for _, property := range []string{"Action", "EventSourceToken", "FunctionUrlAuthType", "InvokedViaFunctionUrl", "Principal", "PrincipalOrgID", "SourceAccount", "SourceArn"} {
		copyAnyProp(body, props, property, property)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("lambda AddPermission: marshal request: %w", err)
	}
	path := "/2015-03-31/functions/" + url.PathEscape(functionName) + "/policy"
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/json", data); err != nil {
		return "", nil, fmt.Errorf("lambda AddPermission: %w", err)
	}
	physicalID := functionName + "|" + statementID
	return physicalID, map[string]string{"Ref": statementID}, nil
}

func (h *lambdaPermissionHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "|", 2)
	if len(parts) != 2 {
		return nil
	}
	path := "/2015-03-31/functions/" + url.PathEscape(parts[0]) + "/policy/" + url.PathEscape(parts[1])
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("lambda RemovePermission", rec, err)
}

// ── Lambda Alias handler ──────────────────────────────────────────────────

type lambdaAliasHandler struct{}

func (h *lambdaAliasHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	functionName, _ := props["FunctionName"].(string)
	aliasName, _ := props["Name"].(string)
	functionVersion, _ := props["FunctionVersion"].(string)
	if functionName == "" || aliasName == "" || functionVersion == "" {
		return "", nil, fmt.Errorf("Lambda Alias: FunctionName, Name, and FunctionVersion are required")
	}
	functionName = cfnLambdaFunctionName(functionName)
	body := map[string]any{"Name": aliasName, "FunctionVersion": functionVersion}
	if desc, ok := props["Description"]; ok {
		body["Description"] = desc
	}
	data, _ := json.Marshal(body)
	path := "/2015-03-31/functions/" + url.PathEscape(functionName) + "/aliases"
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("lambda CreateAlias: %w", err)
	}
	var resp map[string]any
	attrs := map[string]string{"Name": aliasName, "FunctionVersion": functionVersion}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if arn, ok := resp["AliasArn"].(string); ok {
			attrs["Ref"] = arn
			attrs["AliasArn"] = arn
		}
	}
	return functionName + ":" + aliasName, attrs, nil
}

func cfnLambdaFunctionName(identifier string) string {
	if idx := strings.Index(identifier, ":function:"); idx >= 0 {
		name := identifier[idx+len(":function:"):]
		if colonIdx := strings.IndexByte(name, ':'); colonIdx >= 0 {
			name = name[:colonIdx]
		}
		return name
	}
	return identifier
}

func (h *lambdaAliasHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, ":", 2)
	if len(parts) != 2 {
		return nil
	}
	path := "/2015-03-31/functions/" + url.PathEscape(parts[0]) + "/aliases/" + url.PathEscape(parts[1])
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteAlias", rec, err)
}

// ── Lambda Function URL handler ───────────────────────────────────────────
//
// Thin translation to the internal CreateFunctionUrlConfig/DeleteFunctionUrlConfig
// REST endpoints (internal/services/lambda/handler_url.go) — no protocol
// logic duplicated here, per CONTRIBUTING's CloudFormation integration
// guidance.

type lambdaUrlHandler struct{}

func (h *lambdaUrlHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	targetArn, _ := props["TargetFunctionArn"].(string)
	authType, _ := props["AuthType"].(string)
	if targetArn == "" || authType == "" {
		return "", nil, fmt.Errorf("Lambda Url: TargetFunctionArn and AuthType are required")
	}
	functionName := cfnLambdaFunctionName(targetArn)
	qualifier, _ := props["Qualifier"].(string)

	body := map[string]any{"AuthType": authType}
	if cors, ok := props["Cors"]; ok {
		body["Cors"] = cors
	}
	if invokeMode, ok := props["InvokeMode"].(string); ok && invokeMode != "" {
		body["InvokeMode"] = invokeMode
	}
	data, _ := json.Marshal(body)
	path := "/2021-10-31/functions/" + url.PathEscape(functionName) + "/url"
	if qualifier != "" {
		path += "?Qualifier=" + url.QueryEscape(qualifier)
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("lambda CreateFunctionUrlConfig: %w", err)
	}
	attrs := map[string]string{}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if functionArn, ok := resp["FunctionArn"].(string); ok {
			attrs["FunctionArn"] = functionArn
		}
		if functionURL, ok := resp["FunctionUrl"].(string); ok {
			// Real AWS templates consume this via Fn::GetAtt ... FunctionUrl;
			// used as Ref too since it's the only value most callers want.
			attrs["Ref"] = functionURL
			attrs["FunctionUrl"] = functionURL
		}
	}
	return functionName + ":" + qualifier, attrs, nil
}

func (h *lambdaUrlHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	functionName, qualifier, _ := strings.Cut(physicalID, ":")
	if functionName == "" {
		return nil
	}
	path := "/2021-10-31/functions/" + url.PathEscape(functionName) + "/url"
	if qualifier != "" {
		path += "?Qualifier=" + url.QueryEscape(qualifier)
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteFunctionUrlConfig", rec, err)
}

func mergeResourceTags(stackTags []Tag, rawResourceTags any) map[string]string {
	tags, ok := rawResourceTags.([]any)
	if !ok {
		return mergeStackTags(stackTags, nil)
	}
	resourceTags := make(map[string]string, len(tags))
	for _, item := range tags {
		kv, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := kv["Key"].(string)
		if strings.TrimSpace(key) == "" {
			continue
		}
		val, _ := kv["Value"].(string)
		resourceTags[key] = val
	}
	return mergeStackTags(stackTags, resourceTags)
}

func mergeStackTags(stackTags []Tag, resourceTags map[string]string) map[string]string {
	if len(stackTags) == 0 && len(resourceTags) == 0 {
		return nil
	}
	out := make(map[string]string, len(stackTags)+len(resourceTags))
	for _, tag := range stackTags {
		if strings.TrimSpace(tag.Key) == "" {
			continue
		}
		out[tag.Key] = tag.Value
	}
	for key, value := range resourceTags {
		// Resource-level tags override stack-level tags on key collision.
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ── IAM teardown ───────────────────────────────────────────────────────────

// iamTeardownError is teardownError plus the one outcome only IAM has: a
// refusal that must fail the stack outright.
//
// Three outcomes, and the middle one is the point:
//
//   - The entity is already gone, which is a successful teardown. Nothing here
//     may wedge a stack over a resource that no longer exists. Shared with
//     every other handler, via resourceAlreadyGone.
//   - HTTP 409 — AWS's DeleteConflict: the entity still has dependencies, so
//     IAM refuses and the entity survives. That refusal is wrapped in
//     errDeletionBlocked, which marks it as the standing condition it is:
//     nothing will change until the operator clears the dependency. Real
//     CloudFormation fails the stack here too.
//   - Anything else — reported unwrapped. It fails the teardown just the same,
//     because the entity is still standing either way; the wrapping only says
//     whether a retry alone could get past it.
//
// The refusal's own code and message travel in the error so they reach the
// stack event and the resource's status reason: tooling and operators key on
// AWS's wording, not on the fact that something failed.
func iamTeardownError(action, name string, rec *httptest.ResponseRecorder, err error) error {
	if err == nil || resourceAlreadyGone(rec) {
		return nil
	}
	if rec != nil && rec.Code == http.StatusConflict {
		body := rec.Body.String()
		return fmt.Errorf("%w: iam %s %s: %s: %s", errDeletionBlocked, action, name,
			extractXMLValue(body, "Code"), extractXMLValue(body, "Message"))
	}
	return fmt.Errorf("iam %s %s: %w", action, name, err)
}

// ── IAM Role handler ───────────────────────────────────────────────────────

type iamRoleHandler struct{}

func (h *iamRoleHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidatePrincipalProperties(props, false); err != nil {
		return "", nil, err
	}
	roleName, _ := props["RoleName"].(string)
	if roleName == "" {
		roleName = rCtx.generatedNameWithin(maxNameLenIAM)
	}
	// Required by CloudFormation's AWS::IAM::Role, and refused by IAM when
	// missing; the old "{}" default only ever produced a role whose trust policy
	// AWS would not have accepted, and since #1717 IAM refuses it too. Failing
	// here names the property instead of surfacing a MalformedPolicyDocument
	// about a document the template never wrote. A string form is passed
	// through: the property is CloudFormation's `Json` type, which allows one.
	ap, ok := props["AssumeRolePolicyDocument"]
	if !ok {
		return "", nil, fmt.Errorf("Role: AssumeRolePolicyDocument is required")
	}
	assumePolicy := string(policyDocumentJSON(ap))

	params := map[string]string{
		"Action":                   "CreateRole",
		"RoleName":                 roleName,
		"AssumeRolePolicyDocument": assumePolicy,
		"Version":                  "2010-05-08",
	}
	for _, property := range []string{"Path", "Description", "PermissionsBoundary"} {
		if value, _ := props[property].(string); value != "" {
			params[property] = value
		}
	}
	if value, ok := props["MaxSessionDuration"]; ok && value != nil {
		params["MaxSessionDuration"] = cfnScalarString(value)
	}
	tags, err := iamEffectiveTags(props, rCtx.StackTags)
	if err != nil {
		return "", nil, err
	}
	iamTagParams(params, tags)
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("iam CreateRole: %w", err)
	}
	arn := extractXMLValue(rec.Body.String(), "Arn")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:iam::%s:role/%s", rCtx.AccountID, roleName)
	}
	roleID := extractXMLValue(rec.Body.String(), "RoleId")
	attrs := map[string]string{
		"Arn":      arn,
		"RoleId":   roleID,
		"RoleName": roleName,
	}
	// The template's actual permissions: AttachRolePolicy per ManagedPolicyArns
	// entry, PutRolePolicy per Policies entry (#521 — these used to be parsed
	// into nothing, leaving a CDK role with no permissions at all).
	managed, err := iamManagedPolicyMutations("Role", roleName, props, nil)
	if err != nil {
		return "", nil, err
	}
	inline, err := iamInlinePolicyMutations("Role", roleName, props, nil)
	if err != nil {
		return "", nil, err
	}
	if err := newIAMTransaction(ctx, router, rCtx.Region).apply(append(managed, inline...)); err != nil {
		if cleanupErr := iamQuery(ctx, router, rCtx.Region, "DeleteRole", map[string]string{"RoleName": roleName}); cleanupErr != nil {
			return "", nil, fmt.Errorf("%w; cleanup newly-created role: %v", err, cleanupErr)
		}
		return "", nil, err
	}
	// CloudFormation Ref on AWS::IAM::Role returns the role name, not the ARN.
	return roleName, attrs, nil
}

// DeleteWithProperties detaches the role's template-declared managed policies
// and removes its inline policies before deleting it: IAM answers
// DeleteConflict while they remain, and nothing but the stored properties
// records what the Create above attached (#710).
func (h *iamRoleHandler) DeleteWithProperties(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	name := physicalID
	if i := strings.LastIndex(physicalID, "/"); i >= 0 {
		name = physicalID[i+1:]
	}
	return iamPrincipalTeardown(ctx, router, rCtx, func() error {
		return h.Delete(ctx, router, cfg, physicalID, rCtx)
	}, func() ([]iamMutation, error) {
		managed, err := iamManagedPolicyMutations("Role", name, nil, props)
		if err != nil {
			return nil, err
		}
		inline, err := iamInlinePolicyMutations("Role", name, nil, props)
		if err != nil {
			return nil, err
		}
		return append(managed, inline...), nil
	})
}

func (h *iamRoleHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	name := physicalID
	if i := strings.LastIndex(physicalID, "/"); i >= 0 {
		name = physicalID[i+1:]
	}
	params := map[string]string{
		"Action":   "DeleteRole",
		"RoleName": name,
		"Version":  "2010-05-08",
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return iamTeardownError("DeleteRole", name, rec, err)
}

func (h *iamRoleHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidatePrincipalProperties(props, false); err != nil {
		return "", nil, failUpdate(err)
	}
	name := physicalID
	if i := strings.LastIndex(physicalID, "/"); i >= 0 {
		name = physicalID[i+1:]
	}
	// RoleName and Path are immutable in AWS.
	if n, ok := props["RoleName"].(string); ok && n != "" && n != name {
		return "", nil, errReplacementRequired
	}
	if iamJSONPropertyChanged(props, oldProps, "Path") {
		return "", nil, errReplacementRequired
	}
	mutations := make([]iamMutation, 0)
	// AssumeRolePolicyDocument is the most commonly changed property in dev.
	if ap, ok := props["AssumeRolePolicyDocument"]; ok && ap != nil && iamJSONPropertyChanged(props, oldProps, "AssumeRolePolicyDocument") {
		document, _ := json.Marshal(ap)
		oldDocument, _ := json.Marshal(oldProps["AssumeRolePolicyDocument"])
		mutations = append(mutations, iamMutation{
			action: "UpdateAssumeRolePolicy", params: map[string]string{"RoleName": name, "PolicyDocument": string(document)},
			undoAction: "UpdateAssumeRolePolicy", undoParams: map[string]string{"RoleName": name, "PolicyDocument": string(oldDocument)},
		})
	}
	// Description and MaxSessionDuration are in-place via UpdateRole. A removed
	// property resets to its AWS default rather than lingering.
	if iamJSONPropertyChanged(props, oldProps, "Description") || iamJSONPropertyChanged(props, oldProps, "MaxSessionDuration") {
		params := map[string]string{"RoleName": name}
		undo := map[string]string{"RoleName": name}
		if iamJSONPropertyChanged(props, oldProps, "Description") {
			params["Description"], _ = props["Description"].(string)
			undo["Description"], _ = oldProps["Description"].(string)
		}
		if iamJSONPropertyChanged(props, oldProps, "MaxSessionDuration") {
			params["MaxSessionDuration"] = iamSessionDurationParam(props)
			undo["MaxSessionDuration"] = iamSessionDurationParam(oldProps)
		}
		mutations = append(mutations, iamMutation{action: "UpdateRole", params: params, undoAction: "UpdateRole", undoParams: undo})
	}
	if iamJSONPropertyChanged(props, oldProps, "PermissionsBoundary") {
		mutations = append(mutations, iamBoundaryMutation("Role", name, props, oldProps))
	}
	managed, err := iamManagedPolicyMutations("Role", name, props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	inline, err := iamInlinePolicyMutations("Role", name, props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	tags, err := iamTagMutations("Role", name, props, oldProps, rCtx.StackTags, rCtx.PreviousStackTags)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	mutations = append(mutations, managed...)
	mutations = append(mutations, inline...)
	mutations = append(mutations, tags...)
	if err := newIAMTransaction(ctx, router, rCtx.Region).apply(mutations); err != nil {
		return "", nil, classifyIAMTransactionFailure(err)
	}
	arn := fmt.Sprintf("arn:aws:iam::%s:role/%s", rCtx.AccountID, name)
	return name, map[string]string{"Arn": arn, "RoleName": name}, nil
}

// iamSessionDurationParam renders a property set's MaxSessionDuration for
// UpdateRole, substituting AWS's 3600-second default when the template no
// longer sets one.
func iamSessionDurationParam(props map[string]any) string {
	if value, ok := props["MaxSessionDuration"]; ok && value != nil {
		return cfnScalarString(value)
	}
	return "3600"
}

// iamBoundaryMutation reconciles the PermissionsBoundary property: present
// means Put<Principal>PermissionsBoundary, absent means Delete, and the undo
// restores the previous state the same way.
func iamBoundaryMutation(principalType, principalName string, props, oldProps map[string]any) iamMutation {
	nameKey := principalType + "Name"
	action := "Delete" + principalType + "PermissionsBoundary"
	params := map[string]string{nameKey: principalName}
	if boundary, _ := props["PermissionsBoundary"].(string); boundary != "" {
		action = "Put" + principalType + "PermissionsBoundary"
		params["PermissionsBoundary"] = boundary
	}
	undoAction := "Delete" + principalType + "PermissionsBoundary"
	undoParams := map[string]string{nameKey: principalName}
	if oldBoundary, _ := oldProps["PermissionsBoundary"].(string); oldBoundary != "" {
		undoAction = "Put" + principalType + "PermissionsBoundary"
		undoParams["PermissionsBoundary"] = oldBoundary
	}
	return iamMutation{action: action, params: params, undoAction: undoAction, undoParams: undoParams}
}

// ── CloudWatch Logs LogGroup handler ───────────────────────────────────────

type logsLogGroupHandler struct{}

// logsLogGroupTagMap converts CloudFormation's [{Key, Value}] Tags shape to
// CloudWatch Logs' string map used by CreateLogGroup and TagLogGroup.
//
// This is shape translation only. Whether a key or value is *acceptable* —
// length, the reserved `aws:` prefix, an empty key, the 50-tag limit — is the
// Logs service's business and is checked there, so an invalid tag surfaces as
// the same InvalidParameterException a direct SDK caller would see. The
// duplicate-key check stays: CloudFormation's Tags is a list, and collapsing
// two entries with the same key into one map entry would silently pick a
// winner the template never asked for.
func logsLogGroupTagMap(raw any) (map[string]string, error) {
	tags := make(map[string]string)
	if raw == nil {
		return tags, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("Logs::LogGroup Tags must be an array")
	}
	for i, item := range items {
		tag, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Logs::LogGroup Tags[%d] must be an object", i)
		}
		key, ok := tag["Key"].(string)
		if !ok {
			return nil, fmt.Errorf("Logs::LogGroup Tags[%d].Key must be a string", i)
		}
		value, ok := tag["Value"].(string)
		if !ok {
			return nil, fmt.Errorf("Logs::LogGroup Tags[%d].Value must be a string", i)
		}
		if _, duplicate := tags[key]; duplicate {
			return nil, fmt.Errorf("Logs::LogGroup Tags contains duplicate key %q", key)
		}
		tags[key] = value
	}
	return tags, nil
}

func logsLogGroupTagChanges(want, have map[string]string) (map[string]string, []string) {
	upserts := make(map[string]string)
	for key, value := range want {
		if old, ok := have[key]; !ok || old != value {
			upserts[key] = value
		}
	}
	removals := make([]string, 0)
	for key := range have {
		if _, ok := want[key]; !ok {
			removals = append(removals, key)
		}
	}
	sort.Strings(removals)
	return upserts, removals
}

func putLogsLogGroupTags(ctx context.Context, router http.Handler, region, logGroupName string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	_, err := internalJSON(ctx, router, region, "Logs_20140328.TagLogGroup", map[string]any{
		"logGroupName": logGroupName,
		"tags":         tags,
	})
	return err
}

func untagLogsLogGroup(ctx context.Context, router http.Handler, region, logGroupName string, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	_, err := internalJSON(ctx, router, region, "Logs_20140328.UntagLogGroup", map[string]any{
		"logGroupName": logGroupName,
		"tags":         tags,
	})
	return err
}

// restoreLogsLogGroupTags restores the old desired set after a later in-place
// operation fails. This keeps an update failure reversible instead of leaving
// a partial tag mutation for CloudFormation to report as cleanly rolled back.
func restoreLogsLogGroupTags(ctx context.Context, router http.Handler, region, logGroupName string, oldTags, newTags map[string]string) error {
	upserts, removals := logsLogGroupTagChanges(oldTags, newTags)
	if err := putLogsLogGroupTags(ctx, router, region, logGroupName, upserts); err != nil {
		return fmt.Errorf("restore TagLogGroup: %w", err)
	}
	if err := untagLogsLogGroup(ctx, router, region, logGroupName, removals); err != nil {
		return fmt.Errorf("restore UntagLogGroup: %w", err)
	}
	return nil
}

func (h *logsLogGroupHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["LogGroupName"].(string)
	if name == "" {
		name = "/aws/cloudformation/" + rCtx.generatedName()
	}
	resourceTags, err := logsLogGroupTagMap(props["Tags"])
	if err != nil {
		return "", nil, err
	}
	tags := mergeStackTags(rCtx.StackTags, resourceTags)

	// CreateLogGroup takes the tags itself, so the group and its tags are one
	// atomic write and a rejected tag map creates nothing to clean up.
	body := map[string]any{"logGroupName": name}
	if len(tags) > 0 {
		body["tags"] = tags
	}
	_, err = internalJSON(ctx, router, rCtx.Region, "Logs_20140328.CreateLogGroup", body)
	if err != nil {
		return "", nil, fmt.Errorf("logs CreateLogGroup: %w", err)
	}
	cleanup := func(operation string, operationErr error) (string, map[string]string, error) {
		cleanupBody := map[string]any{"logGroupName": name}
		if _, cleanupErr := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.DeleteLogGroup", cleanupBody); cleanupErr != nil {
			return name, nil, errors.Join(
				fmt.Errorf("logs %s: %w", operation, operationErr),
				fmt.Errorf("logs cleanup DeleteLogGroup: %w", cleanupErr),
			)
		}
		return "", nil, fmt.Errorf("logs %s: %w", operation, operationErr)
	}
	// CloudFormation accepts RetentionInDays as either a number or the string
	// form a String-typed Ref or Parameter produces ("7" rather than 7); the
	// Logs service itself decodes a strict int, so the value is coerced here
	// the way real CloudFormation coerces it before dispatching.
	if rd, ok := props["RetentionInDays"]; ok && rd != nil {
		retention, err := cfnInt64(rd)
		if err != nil {
			return cleanup("PutRetentionPolicy", fmt.Errorf("RetentionInDays: %w", err))
		}
		body := map[string]any{
			"logGroupName":    name,
			"retentionInDays": retention,
		}
		if _, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.PutRetentionPolicy", body); err != nil {
			return cleanup("PutRetentionPolicy", err)
		}
	}
	arn := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s:*", rCtx.Region, rCtx.AccountID, name)
	attrs := map[string]string{
		"Arn":          arn,
		"LogGroupName": name,
	}
	return name, attrs, nil
}

func (h *logsLogGroupHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"logGroupName": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.DeleteLogGroup", body)
	return teardownError("DeleteLogGroup", rec, err)
}

func (h *logsLogGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// LogGroupName is immutable in AWS — a rename forces replacement.
	if n, ok := props["LogGroupName"].(string); ok && n != "" && n != physicalID {
		return "", nil, errReplacementRequired
	}
	newResourceTags, err := logsLogGroupTagMap(props["Tags"])
	if err != nil {
		return "", nil, failUpdate(err)
	}
	oldResourceTags, err := logsLogGroupTagMap(oldProps["Tags"])
	if err != nil {
		return "", nil, failUpdate(err)
	}
	newTags := mergeStackTags(rCtx.StackTags, newResourceTags)
	oldTags := mergeStackTags(rCtx.PreviousStackTags, oldResourceTags)
	upserts, removals := logsLogGroupTagChanges(newTags, oldTags)
	if err := putLogsLogGroupTags(ctx, router, rCtx.Region, physicalID, upserts); err != nil {
		return "", nil, failUpdate(fmt.Errorf("logs TagLogGroup: %w", err))
	}
	if err := untagLogsLogGroup(ctx, router, rCtx.Region, physicalID, removals); err != nil {
		if restoreErr := restoreLogsLogGroupTags(ctx, router, rCtx.Region, physicalID, oldTags, newTags); restoreErr != nil {
			return "", nil, failDirtyUpdate(fmt.Errorf("logs UntagLogGroup: %w; tag compensation: %v", err, restoreErr))
		}
		return "", nil, failUpdate(fmt.Errorf("logs UntagLogGroup: %w", err))
	}

	retentionChanged, err := cfnPropertyChanged(props, oldProps, "RetentionInDays")
	if err != nil {
		return "", nil, failUpdate(err)
	}
	// Apply RetentionInDays in place. Logs themselves are preserved.
	if retentionChanged {
		if rd, ok := props["RetentionInDays"]; ok && rd != nil {
			retention, err := cfnInt64(rd)
			if err != nil {
				if restoreErr := restoreLogsLogGroupTags(ctx, router, rCtx.Region, physicalID, oldTags, newTags); restoreErr != nil {
					return "", nil, failDirtyUpdate(fmt.Errorf("RetentionInDays: %w; tag compensation: %v", err, restoreErr))
				}
				return "", nil, failUpdate(fmt.Errorf("RetentionInDays: %w", err))
			}
			body := map[string]any{
				"logGroupName":    physicalID,
				"retentionInDays": retention,
			}
			if _, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.PutRetentionPolicy", body); err != nil {
				if restoreErr := restoreLogsLogGroupTags(ctx, router, rCtx.Region, physicalID, oldTags, newTags); restoreErr != nil {
					return "", nil, failDirtyUpdate(fmt.Errorf("logs PutRetentionPolicy: %w; tag compensation: %v", err, restoreErr))
				}
				return "", nil, failUpdate(fmt.Errorf("logs PutRetentionPolicy: %w", err))
			}
		} else if oldRetention, hadRetention := oldProps["RetentionInDays"]; hadRetention && oldRetention != nil {
			body := map[string]any{"logGroupName": physicalID}
			if _, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.DeleteRetentionPolicy", body); err != nil {
				if restoreErr := restoreLogsLogGroupTags(ctx, router, rCtx.Region, physicalID, oldTags, newTags); restoreErr != nil {
					return "", nil, failDirtyUpdate(fmt.Errorf("logs DeleteRetentionPolicy: %w; tag compensation: %v", err, restoreErr))
				}
				return "", nil, failUpdate(fmt.Errorf("logs DeleteRetentionPolicy: %w", err))
			}
		}
	}
	arn := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s:*", rCtx.Region, rCtx.AccountID, physicalID)
	return physicalID, map[string]string{"Arn": arn, "LogGroupName": physicalID}, nil
}

// ── SSM Parameter handler ──────────────────────────────────────────────────

type ssmParameterHandler struct{}

// ssmParameterScalars pulls the PutParameter-shaped scalars off an
// AWS::SSM::Parameter resource's properties. Every field is optional except
// Value, which the caller validates separately.
func ssmParameterScalars(props map[string]any) map[string]any {
	body := map[string]any{}
	for prop, wireKey := range map[string]string{
		"Description":    "Description",
		"Tier":           "Tier",
		"DataType":       "DataType",
		"AllowedPattern": "AllowedPattern",
		"Policies":       "Policies",
	} {
		if v, _ := props[prop].(string); v != "" {
			body[wireKey] = v
		}
	}
	return body
}

// ssmParameterTagsFromProps converts AWS::SSM::Parameter's Tags property to a
// plain map. Unlike most taggable resources, SSM::Parameter renders Tags as a
// JSON object of key to value rather than a list of {Key, Value} pairs:
// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-ssm-parameter.html#cfn-ssm-parameter-tags
// The list shape is accepted too, defensively, in case a template supplies it.
func ssmParameterTagsFromProps(raw any) map[string]string {
	switch v := raw.(type) {
	case map[string]any:
		if len(v) == 0 {
			return nil
		}
		out := make(map[string]string, len(v))
		for k, val := range v {
			s, _ := val.(string)
			out[k] = s
		}
		return out
	case []any:
		return mergeResourceTags(nil, v)
	default:
		return nil
	}
}

// addSSMParameterTags and removeSSMParameterTags dispatch to SSM's own tag
// operations rather than folding Tags into PutParameter: PutParameter's Tags
// field only applies at creation and is rejected together with Overwrite, so
// AddTagsToResource/RemoveTagsFromResource is the only path that also covers
// updates.
func addSSMParameterTags(ctx context.Context, router http.Handler, region, name string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	tagList := make([]map[string]string, 0, len(tags))
	for k, v := range tags {
		tagList = append(tagList, map[string]string{"Key": k, "Value": v})
	}
	body := map[string]any{
		"ResourceType": "Parameter",
		"ResourceId":   name,
		"Tags":         tagList,
	}
	if _, err := internalJSON(ctx, router, region, "AmazonSSM.AddTagsToResource", body); err != nil {
		return fmt.Errorf("ssm AddTagsToResource: %w", err)
	}
	return nil
}

func removeSSMParameterTags(ctx context.Context, router http.Handler, region, name string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	body := map[string]any{
		"ResourceType": "Parameter",
		"ResourceId":   name,
		"TagKeys":      keys,
	}
	if _, err := internalJSON(ctx, router, region, "AmazonSSM.RemoveTagsFromResource", body); err != nil {
		return fmt.Errorf("ssm RemoveTagsFromResource: %w", err)
	}
	return nil
}

// reconcileSSMParameterTags diffs desired against previous and applies only
// the change, mirroring updateLambdaTags' add/remove split.
func reconcileSSMParameterTags(ctx context.Context, router http.Handler, region, name string, tags, prior map[string]string) error {
	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	if err := addSSMParameterTags(ctx, router, region, name, added); err != nil {
		return err
	}
	return removeSSMParameterTags(ctx, router, region, name, removed)
}

func (h *ssmParameterHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = "/" + rCtx.generatedName()
	}
	paramType, _ := props["Type"].(string)
	if paramType == "" {
		paramType = "String"
	}
	value, _ := props["Value"].(string)

	body := ssmParameterScalars(props)
	body["Name"] = name
	body["Type"] = paramType
	body["Value"] = value
	_, err := internalJSON(ctx, router, rCtx.Region, "AmazonSSM.PutParameter", body)
	if err != nil {
		return "", nil, fmt.Errorf("ssm PutParameter: %w", err)
	}

	tags := mergeStackTags(rCtx.StackTags, ssmParameterTagsFromProps(props["Tags"]))
	if err := addSSMParameterTags(ctx, router, rCtx.Region, name, tags); err != nil {
		return "", nil, err
	}

	attrs := map[string]string{
		"Type":  paramType,
		"Value": value,
	}
	return name, attrs, nil
}

func (h *ssmParameterHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"Name": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonSSM.DeleteParameter", body)
	return teardownError("DeleteParameter", rec, err)
}

func (h *ssmParameterHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Name is immutable in AWS — changing it forces replacement.
	if n, ok := props["Name"].(string); ok && n != "" && n != physicalID {
		return "", nil, errReplacementRequired
	}
	name := physicalID
	paramType, _ := props["Type"].(string)
	if paramType == "" {
		paramType = "String"
	}
	value, _ := props["Value"].(string)
	body := ssmParameterScalars(props)
	body["Name"] = name
	body["Type"] = paramType
	body["Value"] = value
	body["Overwrite"] = true
	if _, err := internalJSON(ctx, router, rCtx.Region, "AmazonSSM.PutParameter", body); err != nil {
		return "", nil, fmt.Errorf("ssm PutParameter (overwrite): %w", err)
	}

	tags := mergeStackTags(rCtx.StackTags, ssmParameterTagsFromProps(props["Tags"]))
	prior := mergeStackTags(rCtx.PreviousStackTags, ssmParameterTagsFromProps(oldProps["Tags"]))
	if err := reconcileSSMParameterTags(ctx, router, rCtx.Region, name, tags, prior); err != nil {
		return "", nil, failUpdate(fmt.Errorf("ssm tags: %w", err))
	}

	return name, map[string]string{"Type": paramType, "Value": value}, nil
}

// ── Secrets Manager Secret handler ─────────────────────────────────────────

type secretsManagerSecretHandler struct{}

func (h *secretsManagerSecretHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, prior map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Name is immutable. physicalID is the ARN; last segment after final ':'
	// is `<name>-<suffix>`, but for emulator purposes the user-supplied Name
	// is what we compare against. If it changed, replace.
	if n, ok := props["Name"].(string); ok && n != "" {
		// Extract the secret name embedded in the ARN.
		if i := strings.LastIndex(physicalID, ":"); i >= 0 {
			tail := physicalID[i+1:]
			// ARN tail is `<name>-<6 random>`; strip suffix when present.
			if j := strings.LastIndex(tail, "-"); j >= 0 && len(tail)-j == 7 {
				tail = tail[:j]
			}
			if tail != "" && tail != n {
				return "", nil, errReplacementRequired
			}
		}
	}

	// AWS re-generates the secret value when GenerateSecretString changes. That
	// update behavior is tracked separately in #678.
	updateBody, restoreBody, valueVersionChanged := secretsManagerUpdateBodies(physicalID, props, prior)
	updateApplied := false
	if len(updateBody) > 1 {
		if _, err := internalJSON(ctx, router, rCtx.Region, "secretsmanager.UpdateSecret", updateBody); err != nil {
			return "", nil, failUpdate(fmt.Errorf("secretsmanager UpdateSecret: %w", err))
		}
		updateApplied = true
	}
	newTags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	oldTags := mergeResourceTags(rCtx.PreviousStackTags, prior["Tags"])
	if !reflect.DeepEqual(newTags, oldTags) {
		tagsApplied, err := updateSecretsManagerTags(ctx, router, rCtx.Region, physicalID, newTags, oldTags)
		if err != nil {
			var compensationErr error
			// Reverse the forward order: restore tags before metadata/KMS.
			if tagsApplied {
				_, compensationErr = updateSecretsManagerTags(ctx, router, rCtx.Region, physicalID, oldTags, newTags)
				if compensationErr != nil {
					compensationErr = fmt.Errorf("restore Secrets Manager tags: %w", compensationErr)
				}
			}
			if updateApplied && len(restoreBody) > 1 {
				if _, restoreErr := internalJSON(ctx, router, rCtx.Region, "secretsmanager.UpdateSecret", restoreBody); restoreErr != nil {
					compensationErr = errors.Join(compensationErr, fmt.Errorf("restore Secrets Manager metadata and KMS key: %w", restoreErr))
				}
			}
			if valueVersionChanged {
				compensationErr = errors.Join(compensationErr, errors.New("secret value version cannot be removed during compensation"))
			}
			if compensationErr != nil {
				return "", nil, failDirtyUpdate(errors.Join(err, compensationErr))
			}
			return "", nil, failUpdate(err)
		}
	}
	name, _ := props["Name"].(string)
	attrs := map[string]string{"Arn": physicalID}
	if name != "" {
		attrs["Name"] = name
	}
	return physicalID, attrs, nil
}

func secretsManagerUpdateBodies(secretID string, props, prior map[string]any) (update, restore map[string]any, valueVersionChanged bool) {
	update = map[string]any{"SecretId": secretID}
	restore = map[string]any{"SecretId": secretID}
	for _, property := range []string{"Description", "KmsKeyId"} {
		value, present := props[property]
		oldValue, oldPresent := prior[property]
		if present == oldPresent && reflect.DeepEqual(value, oldValue) {
			continue
		}
		if present && value != nil {
			update[property] = value
		} else {
			update[property] = ""
		}
		if oldPresent && oldValue != nil {
			restore[property] = oldValue
		} else {
			restore[property] = ""
		}
	}
	if value, present := props["SecretString"]; present && value != nil {
		oldValue, oldPresent := prior["SecretString"]
		if !oldPresent || !reflect.DeepEqual(value, oldValue) {
			update["SecretString"] = value
			valueVersionChanged = true
		}
	}
	return update, restore, valueVersionChanged
}

func (h *secretsManagerSecretHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedName()
	}
	body := map[string]any{"Name": name}
	sv, haveLiteral := props["SecretString"]
	haveLiteral = haveLiteral && sv != nil
	genRaw, haveGenerated := props["GenerateSecretString"]
	haveGenerated = haveGenerated && genRaw != nil
	switch {
	case haveLiteral && haveGenerated:
		return "", nil, fmt.Errorf("you can't specify both the SecretString and GenerateSecretString properties")
	case haveLiteral:
		body["SecretString"] = sv
	case haveGenerated:
		// Without this the secret is created with an AWSCURRENT version holding
		// nothing, and every GetSecretValue against it answers
		// ResourceNotFoundException — a value-less staged version is what an
		// in-flight rotation looks like. Failing the resource on a malformed
		// property beats falling back to that.
		gen, ok := genRaw.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("GenerateSecretString must be an object, got %T", genRaw)
		}
		value, err := generatedSecretString(ctx, router, rCtx, gen)
		if err != nil {
			return "", nil, err
		}
		body["SecretString"] = value
	}
	if desc, ok := props["Description"]; ok {
		body["Description"] = desc
	}
	if kmsKeyID, ok := props["KmsKeyId"]; ok {
		body["KmsKeyId"] = kmsKeyID
	}
	if tags := secretsManagerTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = tags
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "secretsmanager.CreateSecret", body)
	if err != nil {
		return "", nil, fmt.Errorf("secretsmanager CreateSecret: %w", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if arn, ok := resp["ARN"].(string); ok {
			return arn, map[string]string{"Arn": arn, "Name": name}, nil
		}
	}
	return name, map[string]string{"Name": name}, nil
}

// generatedSecretString resolves AWS::SecretsManager::Secret's
// GenerateSecretString into the value CloudFormation stores as the secret's
// first version. The password itself comes from the service's own
// GetRandomPassword, so the exclusion rules live in one place.
func generatedSecretString(ctx context.Context, router http.Handler, rCtx *resolveContext, gen map[string]any) (string, error) {
	config, err := parseGeneratedSecretString(gen)
	if err != nil {
		return "", err
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "secretsmanager.GetRandomPassword", config.passwordRequest)
	if err != nil {
		return "", fmt.Errorf("secretsmanager GetRandomPassword: %w", err)
	}
	var resp struct {
		RandomPassword string `json:"RandomPassword"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("secretsmanager GetRandomPassword: %w", err)
	}
	if resp.RandomPassword == "" {
		return "", fmt.Errorf("secretsmanager GetRandomPassword returned no password")
	}
	if config.template == nil {
		return resp.RandomPassword, nil
	}

	// With a template the secret is that JSON object with the generated
	// password added under GenerateStringKey, which is how the CDK's
	// `{ username, password }` database credentials are built.
	config.template[config.key] = resp.RandomPassword
	out, err := json.Marshal(config.template)
	if err != nil {
		return "", fmt.Errorf("GenerateSecretString: %w", err)
	}
	return string(out), nil
}

type generatedSecretStringConfig struct {
	template        map[string]any
	key             string
	passwordRequest map[string]any
}

// parseGeneratedSecretString validates the CloudFormation-only composition
// rules before GetRandomPassword is dispatched. Password policy validation
// remains owned by Secrets Manager itself.
func parseGeneratedSecretString(gen map[string]any) (*generatedSecretStringConfig, error) {
	allowed := map[string]struct{}{
		"SecretStringTemplate": {}, "GenerateStringKey": {}, "PasswordLength": {},
		"ExcludeCharacters": {}, "ExcludeNumbers": {}, "ExcludePunctuation": {},
		"ExcludeUppercase": {}, "ExcludeLowercase": {}, "IncludeSpace": {},
		"RequireEachIncludedType": {},
	}
	for member := range gen {
		if _, ok := allowed[member]; !ok {
			return nil, fmt.Errorf("GenerateSecretString: unknown member %q", member)
		}
	}
	config := &generatedSecretStringConfig{passwordRequest: map[string]any{}}
	templateValue, haveTemplate := gen["SecretStringTemplate"]
	keyValue, haveKey := gen["GenerateStringKey"]
	if haveTemplate != haveKey {
		return nil, fmt.Errorf("GenerateSecretString: SecretStringTemplate and GenerateStringKey must be specified together")
	}
	if haveTemplate {
		template, ok := templateValue.(string)
		if !ok || template == "" {
			return nil, fmt.Errorf("GenerateSecretString: SecretStringTemplate must be a non-empty JSON object string")
		}
		key, ok := keyValue.(string)
		if !ok || key == "" {
			return nil, fmt.Errorf("GenerateSecretString: GenerateStringKey must be a non-empty string")
		}
		if err := json.Unmarshal([]byte(template), &config.template); err != nil || config.template == nil {
			if err == nil {
				err = errors.New("decoded to null")
			}
			return nil, fmt.Errorf("GenerateSecretString: SecretStringTemplate is not a JSON object: %w", err)
		}
		if _, exists := config.template[key]; exists {
			return nil, fmt.Errorf("GenerateSecretString: GenerateStringKey %q already exists in SecretStringTemplate", key)
		}
		config.key = key
	}

	if raw, ok := gen["PasswordLength"]; ok {
		length, err := cfnInt64(raw)
		if err != nil {
			return nil, fmt.Errorf("GenerateSecretString: PasswordLength must be an integer: %w", err)
		}
		config.passwordRequest["PasswordLength"] = length
	}
	if raw, ok := gen["ExcludeCharacters"]; ok {
		excluded, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("GenerateSecretString: ExcludeCharacters must be a string, got %T", raw)
		}
		config.passwordRequest["ExcludeCharacters"] = excluded
	}
	for _, flag := range []string{
		"ExcludeNumbers",
		"ExcludePunctuation",
		"ExcludeUppercase",
		"ExcludeLowercase",
		"IncludeSpace",
		"RequireEachIncludedType",
	} {
		if raw, ok := gen[flag]; ok {
			value, err := cfnBool(raw)
			if err != nil {
				return nil, fmt.Errorf("GenerateSecretString: %s must be a boolean: %w", flag, err)
			}
			config.passwordRequest[flag] = value
		}
	}
	return config, nil
}

func cfnInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, fmt.Errorf("got %v", typed)
		}
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("got %T", value)
	}
}

func cfnBool(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		switch {
		case strings.EqualFold(typed, "true"):
			return true, nil
		case strings.EqualFold(typed, "false"):
			return false, nil
		}
	}
	return false, fmt.Errorf("got %v", value)
}

func secretsManagerTags(stackTags []Tag, rawResourceTags any) []map[string]string {
	return secretsManagerTagsFromMap(mergeResourceTags(stackTags, rawResourceTags))
}

func secretsManagerTagsFromMap(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]string{"Key": key, "Value": tags[key]})
	}
	return out
}

func updateSecretsManagerTags(ctx context.Context, router http.Handler, region, secretID string, tags, prior map[string]string) (bool, error) {
	changed := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			changed[key] = value
		}
	}
	if len(changed) > 0 {
		if _, err := internalJSON(ctx, router, region, "secretsmanager.TagResource", map[string]any{
			"SecretId": secretID,
			"Tags":     secretsManagerTagsFromMap(changed),
		}); err != nil {
			return false, fmt.Errorf("secretsmanager TagResource: %w", err)
		}
	}
	tagsApplied := len(changed) > 0
	removed := make([]string, 0)
	for key := range prior {
		if _, exists := tags[key]; !exists {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	if len(removed) > 0 {
		if _, err := internalJSON(ctx, router, region, "secretsmanager.UntagResource", map[string]any{
			"SecretId": secretID,
			"TagKeys":  removed,
		}); err != nil {
			// Reconcile the complete old tag set even when no TagResource call
			// preceded this one: a failed request may have removed some keys.
			return true, fmt.Errorf("secretsmanager UntagResource: %w", err)
		}
	}
	return tagsApplied, nil
}

func (h *secretsManagerSecretHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{
		"SecretId":                   physicalID,
		"ForceDeleteWithoutRecovery": true,
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "secretsmanager.DeleteSecret", body)
	return teardownError("DeleteSecret", rec, err)
}

// ── Custom resource handler (Lambda-backed) ────────────────────────────────

// customResourceHandler invokes a Lambda function using the CloudFormation
// custom resource protocol. It supports both Custom::* types and
// AWS::CloudFormation::CustomResource.
type customResourceHandler struct {
	p *provisioner
}

// cfnCustomResourceRequest is the payload sent to the Lambda backing function.
// See https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/crpg-ref-requesttypes.html
type cfnCustomResourceRequest struct {
	RequestType           string         `json:"RequestType"`
	ResponseURL           string         `json:"ResponseURL,omitempty"`
	StackId               string         `json:"StackId"`
	RequestId             string         `json:"RequestId"`
	PhysicalResourceId    string         `json:"PhysicalResourceId,omitempty"`
	ResourceProperties    map[string]any `json:"ResourceProperties"`
	OldResourceProperties map[string]any `json:"OldResourceProperties,omitempty"`
}

// cfnCustomResourceResponse is the expected response from the Lambda function.
type cfnCustomResourceResponse struct {
	Status             string            `json:"Status"`
	PhysicalResourceId string            `json:"PhysicalResourceId"`
	Data               map[string]string `json:"Data"`
	Reason             string            `json:"Reason"`
}

func (h *customResourceHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return h.invoke(ctx, router, cfg, "Create", "", props, nil, rCtx)
}

func (h *customResourceHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	_, _, err := h.invoke(ctx, router, cfg, "Delete", physicalID, nil, nil, rCtx)
	return err
}

func (h *customResourceHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// oldProps arrives in recorded form — the literal dynamic-reference text,
	// the same form props was in before the provisioner expanded it (see
	// expandResourceProperties) ahead of this call. Expanding oldProps here
	// with the same reject-secure/resolve-plain rule keeps
	// OldResourceProperties in the same form as ResourceProperties, so an
	// update whose properties did not change produces no difference between
	// the two payload halves.
	expandedOld, err := expandCustomResourceProperties(oldProps, rCtx)
	if err != nil {
		return "", nil, err
	}
	return h.invoke(ctx, router, cfg, "Update", physicalID, props, expandedOld, rCtx)
}

func (h *customResourceHandler) invoke(ctx context.Context, router http.Handler, _ *config.Config, reqType, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	log := h.p.log.WithRecorder(ctx)
	serviceToken, _ := props["ServiceToken"].(string)
	if serviceToken == "" {
		return "", nil, fmt.Errorf("custom resource missing ServiceToken")
	}

	// Extract function name from ARN (arn:aws:lambda:region:account:function:name).
	funcName := serviceToken
	if parts := strings.Split(serviceToken, ":"); len(parts) >= 7 {
		funcName = parts[6]
	}

	// stubResult returns a synthetic physical ID when Lambda invocation cannot
	// complete (Docker unavailable, function not found, etc.).
	stubResult := func() (string, map[string]string, error) {
		id := fmt.Sprintf("custom-resource-%s-%d", rCtx.StackName, len(rCtx.Resources))
		return id, nil, nil
	}

	payload := cfnCustomResourceRequest{
		RequestType:           reqType,
		StackId:               rCtx.StackID,
		RequestId:             uuid.New().String(),
		PhysicalResourceId:    physicalID,
		ResourceProperties:    props,
		OldResourceProperties: oldProps,
		// ResponseURL is empty — the emulator does not use the S3 callback
		// protocol; it reads the Lambda return value directly instead.
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("custom resource marshal: %w", err)
	}

	path := "/2015-03-31/functions/" + url.PathEscape(funcName) + "/invocations"
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/json", data)
	if err != nil {
		// Lambda function not found or invocation failed — degrade to stub.
		log.Warn("cfn: custom resource invoke failed, creating stub",
			zap.String("serviceToken", serviceToken),
			zap.Error(err))
		return stubResult()
	}

	// If the Lambda runtime returned a function error (e.g. Docker unavailable),
	// degrade gracefully — treat as a no-op stub so the stack can still deploy.
	if funcErr := rec.Header().Get("X-Amz-Function-Error"); funcErr != "" {
		log.Warn("cfn: custom resource Lambda returned function error, creating stub",
			zap.String("serviceToken", serviceToken),
			zap.String("functionError", funcErr))
		return stubResult()
	}

	var resp cfnCustomResourceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("custom resource response parse: %w", err)
	}
	if resp.Status != "SUCCESS" {
		reason := resp.Reason
		if reason == "" {
			reason = "custom resource returned FAILED"
		}
		return "", nil, fmt.Errorf("custom resource failed: %s", reason)
	}

	return resp.PhysicalResourceId, resp.Data, nil
}

// ── Nested stack handler ───────────────────────────────────────────────────

// nestedStackHandler provisions an AWS::CloudFormation::Stack resource by
// fetching the child template, creating a child Stack, and provisioning its
// resources synchronously within the parent's goroutine. No additional
// goroutines are spawned — the parent blocks until the child completes.
type nestedStackHandler struct {
	p *provisioner
}

func (h *nestedStackHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	templateURL, _ := props["TemplateURL"].(string)
	if templateURL == "" {
		return "", nil, fmt.Errorf("nested stack missing TemplateURL")
	}

	// Fetch the child template via internal HTTP (supports S3 URLs served by
	// our own router or any reachable URL).
	tmplBody, err := h.fetchTemplate(ctx, router, rCtx.Region, templateURL)
	if err != nil {
		return "", nil, fmt.Errorf("nested stack fetch template: %w", err)
	}

	tmpl, err := parseTemplate(tmplBody)
	if err != nil {
		return "", nil, fmt.Errorf("nested stack parse template: %w", err)
	}

	// Build a unique child stack name from the parent name and a short UUID.
	childName := rCtx.StackName + "-NestedStack-" + uuid.New().String()[:8]

	childStackID := fmt.Sprintf("arn:aws:cloudformation:%s:%s:stack/%s/%s",
		rCtx.Region, cfg.AccountID, childName, uuid.NewString())

	// Build child parameters from Properties.Parameters map.
	var childParams []Parameter
	if paramMap, ok := props["Parameters"].(map[string]any); ok {
		for k, v := range paramMap {
			childParams = append(childParams, Parameter{
				Key:   k,
				Value: cfnScalarString(v),
			})
		}
		// Sort for deterministic ordering.
		sort.Slice(childParams, func(i, j int) bool {
			return childParams[i].Key < childParams[j].Key
		})
	}

	// Determine the root stack: if the parent is itself nested, inherit its root;
	// otherwise the parent is the root.
	rootID := rCtx.StackID
	if parentStack, _ := h.p.store.getStack(h.p.regionCtx(rCtx.Region), rCtx.StackName); parentStack != nil && parentStack.RootID != "" {
		rootID = parentStack.RootID
	}

	now := h.p.clk.Now()
	childStack := &Stack{
		StackName:          childName,
		StackID:            childStackID,
		ParentStackID:      rCtx.StackID,
		RootID:             rootID,
		Status:             StatusCreateInProgress,
		Parameters:         childParams,
		CreatedAt:          now,
		Region:             rCtx.Region,
		TemplateBody:       tmplBody,
		Tags:               mergeNestedStackTags(rCtx.StackTags, props["Tags"]),
		ClientRequestToken: rCtx.ClientRequestToken,
	}

	// Store the child stack so it appears in ListStacks/DescribeStacks.
	storeCtx := h.p.regionCtx(rCtx.Region)
	if err := h.p.store.putStack(storeCtx, childStack); err != nil {
		return "", nil, fmt.Errorf("nested stack store: %w", err)
	}

	// Provision child resources synchronously — no new goroutine.
	h.p.provisionStackResourcesCtx(ctx, childStack, tmpl)

	if childStack.Status != StatusCreateComplete {
		return "", nil, fmt.Errorf("nested stack %s failed: %s", childName, childStack.StatusReason)
	}

	// Build attributes from child outputs (Fn::GetAtt on nested stacks
	// uses "Outputs.<OutputKey>" as the attribute name).
	attrs := make(map[string]string)
	for _, out := range childStack.Outputs {
		attrs["Outputs."+out.Key] = out.Value
	}

	return childStackID, attrs, nil
}

func (h *nestedStackHandler) Delete(ctx context.Context, _ http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	// physicalID is the child stack's ARN. Extract the stack name from it:
	// format: arn:aws:cloudformation:<region>:<account>:stack/<name>/<uuid>
	childName := stackNameFromARN(physicalID)
	if childName == "" {
		return nil
	}

	storeCtx := h.p.regionCtx(rCtx.Region)
	childStack, _ := h.p.store.getStack(storeCtx, childName)
	if childStack == nil {
		return nil // already deleted or not found
	}

	childStack.Status = StatusDeleteInProgress
	// The child was created under an earlier operation and still carries that
	// operation's token. This delete belongs to the parent's, and its events
	// have to say so.
	childStack.ClientRequestToken = rCtx.ClientRequestToken
	h.p.deleteStackResourcesCtx(ctx, childStack)
	// A child that could not be torn down has to fail the parent. AWS reports
	// the nested stack resource as DELETE_FAILED and the parent stack with it —
	// swallowing it here would report the parent DELETE_COMPLETE over resources
	// the child is still holding, which is the same lie a swallowed resource
	// refusal tells one level down.
	if childStack.Status == StatusDeleteFailed {
		return fmt.Errorf("%w: nested stack %s: %s", errDeletionBlocked, childName, childStack.StatusReason)
	}
	return nil
}

func (h *nestedStackHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	childName := stackNameFromARN(physicalID)
	if childName == "" {
		return physicalID, nil, nil
	}

	storeCtx := h.p.regionCtx(rCtx.Region)
	childStack, _ := h.p.store.getStack(storeCtx, childName)
	if childStack == nil {
		return "", nil, fmt.Errorf("nested stack %s not found", childName)
	}

	templateURL, _ := props["TemplateURL"].(string)
	if templateURL == "" {
		return "", nil, fmt.Errorf("nested stack missing TemplateURL")
	}

	tmplBody, err := h.fetchTemplate(ctx, router, rCtx.Region, templateURL)
	if err != nil {
		return "", nil, fmt.Errorf("nested stack fetch template: %w", err)
	}

	tmpl, err := parseTemplate(tmplBody)
	if err != nil {
		return "", nil, fmt.Errorf("nested stack parse template: %w", err)
	}

	// Captured before the attempted values are written over it. The parent only
	// reverses a child update it recorded as an in-place success, so a child
	// whose own update fails is never handed back its metadata from above — its
	// own rollback is the only thing that can restore it.
	previous := captureStackGeneration(childStack)
	if previous.Tags == nil {
		// A child provisioned before nested-stack tags were recorded has none of
		// its own. Its effective tags were the parent's previous tags merged
		// with the resource's previous Tags property.
		previous.Tags = mergeNestedStackTags(rCtx.PreviousStackTags, oldProps["Tags"])
	}

	childStack.Parameters = nil
	if paramMap, ok := props["Parameters"].(map[string]any); ok {
		for k, v := range paramMap {
			childStack.Parameters = append(childStack.Parameters, Parameter{Key: k, Value: cfnScalarString(v)})
		}
		sort.Slice(childStack.Parameters, func(i, j int) bool {
			return childStack.Parameters[i].Key < childStack.Parameters[j].Key
		})
	}

	childStack.TemplateBody = tmplBody
	childStack.Status = StatusUpdateInProgress
	childStack.ClientRequestToken = rCtx.ClientRequestToken // see Delete
	childStack.Tags = mergeNestedStackTags(rCtx.StackTags, props["Tags"])

	if err := h.p.store.putStack(storeCtx, childStack); err != nil {
		return "", nil, fmt.Errorf("nested stack update store: %w", err)
	}

	h.p.updateStackResourcesCtx(ctx, childStack, tmpl, previous)

	if childStack.Status != StatusUpdateComplete {
		return "", nil, fmt.Errorf("nested stack %s update failed: %s", childName, childStack.StatusReason)
	}

	attrs := make(map[string]string)
	for _, out := range childStack.Outputs {
		attrs["Outputs."+out.Key] = out.Value
	}

	return childStack.StackID, attrs, nil
}

func mergeNestedStackTags(parent []Tag, raw any) []Tag {
	merged := mergeResourceTags(parent, raw)
	out := make([]Tag, 0, len(merged))
	for key, value := range merged {
		out = append(out, Tag{Key: key, Value: value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// stackNameFromARN extracts the stack name from a CloudFormation stack ARN.
// ARN format: arn:aws:cloudformation:<region>:<account>:stack/<name>/<uuid>.
func stackNameFromARN(arn string) string {
	const prefix = ":stack/"
	i := strings.Index(arn, prefix)
	if i < 0 {
		return ""
	}
	rest := arn[i+len(prefix):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// fetchTemplate retrieves a template body from a URL. If the URL points at our
// own emulator (same host), it dispatches an internal request to avoid a real
// network round-trip. Otherwise it performs a standard HTTP GET.
func (h *nestedStackHandler) fetchTemplate(ctx context.Context, router http.Handler, region, templateURL string) (string, error) {
	// Parse the URL to extract the path for internal dispatch.
	u, err := url.Parse(templateURL)
	if err != nil {
		return "", err
	}

	// Always try internal dispatch first — in tests the URL host points at
	// the httptest server which is backed by the same router.
	rec, err := internalRequest(ctx, router, region, http.MethodGet, u.Path, "", nil)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", templateURL, err)
	}
	// A nested stack's child arrives through the same TemplateURL parameter as
	// a root stack's, and AWS bounds it the same way. Without this the parent
	// deploys clean here and fails in the account on the child.
	fetched := rec.Body.String()
	if err := checkResolvedTemplateSize(fetched); err != nil {
		return "", err
	}
	return fetched, nil
}

// ── XML extraction helper ──────────────────────────────────────────────────

// extractXMLValue extracts the text content of a simple XML element.
// This is intentionally simple — not a real XML parser.
func extractXMLValue(xml, tag string) string {
	start := fmt.Sprintf("<%s>", tag)
	end := fmt.Sprintf("</%s>", tag)
	i := strings.Index(xml, start)
	if i < 0 {
		return ""
	}
	i += len(start)
	j := strings.Index(xml[i:], end)
	if j < 0 {
		return ""
	}
	return xml[i : i+j]
}
