// Package eventbridge provides emulation of Amazon EventBridge.
//
// Implemented: CreateEventBus, DescribeEventBus, ListEventBuses, TagResource,
// ListTagsForResource, DeleteEventBus, PutRule, DescribeRule, ListRules,
// PutTargets, ListTargetsByRule, RemoveTargets, DisableRule, EnableRule,
// DeleteRule, PutEvents, TestEventPattern, PutPermission, RemovePermission.
package eventbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/eventtarget"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	serviceName   = "eventbridge"
	targetPrefix  = "AWSEvents."
	nsBuses       = "eb:buses"
	nsRules       = "eb:rules"
	nsTags        = "eb:tags"
	nsTargets     = "eb:targets"
	nsLastFire    = "eb:last-fire"
	nsNextFire    = "eb:next-fire"
	nsPermissions = "eb:permissions"
	engineTick    = time.Second
)

// Service implements router.Service and router.TargetDispatcher for EventBridge.
type Service struct {
	cfg     *config.Config
	store   state.Store
	clk     clock.Clock
	log     *serviceutil.ServiceLogger
	bus     *events.Bus
	typedOp map[string]op.Operation

	// patterns caches compiled rule event patterns across PutEvents calls.
	patterns *patternCache
	// deliveries is the console's recent target-delivery outcome feed.
	deliveries *deliveryLog
	// targets dispatches to the sinks a rule's targets name. Built once from
	// the root router in InitRouter; nil until then.
	targets *eventtarget.Dispatcher
	// lambdaAuth answers whether events.amazonaws.com may invoke a Lambda
	// target. nil when the server was wired without Lambda.
	lambdaAuth events.FunctionInvokeAuthorizer

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// New returns a configured EventBridge Service.
//
// It allocates only — no store reads, no network, nothing that would put work
// on the router's construction path (docs/dev/performance.md § Startup budget).
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	s := &Service{
		cfg:        cfg,
		store:      store,
		clk:        clk,
		log:        serviceutil.NewServiceLogger(logger, serviceName),
		patterns:   newPatternCache(),
		deliveries: newDeliveryLog(),
		stopCh:     make(chan struct{}),
	}
	s.typedOp = s.typedOps()
	return s
}

// InitRouter wires the root router for same-process target delivery.
func (s *Service) InitRouter(router http.Handler) {
	s.targets = eventtarget.NewDispatcher(router, s.cfg.Region)
	s.startEngine()
}

// InitLambdaAuthorizer wires Lambda's resource-policy authorizer. With
// OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY set, a Lambda target whose function
// does not grant events.amazonaws.com fails delivery — retried, then
// dead-lettered or dropped — instead of invoking.
func (s *Service) InitLambdaAuthorizer(auth events.FunctionInvokeAuthorizer) {
	s.lambdaAuth = auth
}

// dispatcher returns the target dispatcher, or nil before InitRouter has run.
func (s *Service) dispatcher() *eventtarget.Dispatcher { return s.targets }

// Stop terminates the scheduled-rule engine.
func (s *Service) Stop(ctx context.Context) {
	s.stopOnce.Do(func() { close(s.stopCh) })
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// InitBus wires the event bus for event bus/rule lifecycle events.
func (s *Service) InitBus(bus *events.Bus) {
	s.bus = bus
}

// BusPublisher returns the narrow interface another service uses to emit its
// own events onto the default bus, the way AWS services publish
// service-originated events — S3's EventBridge bucket notifications, for
// example. It goes through exactly the path PutEvents uses, so rule matching,
// input transformers, retries and dead-lettering behave identically.
func (s *Service) BusPublisher() events.BusPublisher { return (*busPublisher)(s) }

// busPublisher keeps PublishBusEvent off Service's own method set, so the
// service-to-service entry point is reached only through BusPublisher.
type busPublisher Service

func (p *busPublisher) PublishBusEvent(ctx context.Context, entry events.BusEntry) error {
	resources := make([]any, 0, len(entry.Resources))
	for _, resource := range entry.Resources {
		resources = append(resources, resource)
	}
	(*Service)(p).deliverEntries(ctx, []string{uuid.New().String()}, []map[string]any{{
		"Source":     entry.Source,
		"DetailType": entry.DetailType,
		"Detail":     entry.Detail,
		"Resources":  resources,
	}})
	return nil
}

// publish emits an event if the bus is wired.
func (s *Service) publish(r *http.Request, t events.Type, payload any) {
	if s.bus != nil {
		s.bus.Publish(r.Context(), events.Event{Type: t, Payload: payload})
	}
}

// region returns the per-request region, falling back to the configured default.
func (s *Service) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.cfg.Region)
}

// Name satisfies router.Service.
func (s *Service) Name() string { return serviceName }

// RegisterRoutes satisfies router.Service. EventBridge's AWS surface is
// dispatched by X-Amz-Target; the only routes it registers are the web
// console's read-only delivery-visibility endpoints.
func (s *Service) RegisterRoutes(r chi.Router) { s.registerAdminRoutes(r) }

// TargetPrefix satisfies router.TargetDispatcher.
func (s *Service) TargetPrefix() string { return targetPrefix }

// Dispatch satisfies router.TargetDispatcher.
func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code:       "UnsupportedProtocol",
				Message:    "EventBridge does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		// Preserve AWS JSON 1.1 on the existing switch path until JSON
		// wire-byte goldens cover EventBridge. CBOR uses typed operations.
		if c.Name() != codec.NameRPCv2CBOR {
			s.dispatchLegacy(w, r, opName)
			return
		}
		if typed, ok := s.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		c.WriteError(w, r, protocol.ErrNotImplemented)
		return
	}

	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), targetPrefix)
	s.dispatchLegacy(w, r, op)
}

func (s *Service) dispatchLegacy(w http.ResponseWriter, r *http.Request, op string) {
	switch op {
	case "CreateEventBus":
		s.createEventBus(w, r)
	case "DescribeEventBus":
		s.describeEventBus(w, r)
	case "ListEventBuses":
		s.listEventBuses(w, r)
	case "TagResource":
		s.tagResource(w, r)
	case "ListTagsForResource":
		s.listTagsForResource(w, r)
	case "UntagResource":
		s.untagResource(w, r)
	case "DeleteEventBus":
		s.deleteEventBus(w, r)
	case "PutRule":
		s.putRule(w, r)
	case "DescribeRule":
		s.describeRule(w, r)
	case "ListRules":
		s.listRules(w, r)
	case "PutTargets":
		s.putTargets(w, r)
	case "ListTargetsByRule":
		s.listTargetsByRule(w, r)
	case "RemoveTargets":
		s.removeTargets(w, r)
	case "DisableRule":
		s.disableRule(w, r)
	case "EnableRule":
		s.enableRule(w, r)
	case "DeleteRule":
		s.deleteRule(w, r)
	case "PutEvents":
		s.putEvents(w, r)
	case "TestEventPattern":
		s.testEventPattern(w, r)
	case "PutPermission":
		s.putPermission(w, r)
	case "RemovePermission":
		s.removePermission(w, r)
	default:
		protocol.NotImplementedJSON(w, r)
	}
}

type eventBus struct {
	Name             string              `json:"Name" cbor:"Name"`
	ARN              string              `json:"Arn" cbor:"Arn"`
	Description      string              `json:"Description" cbor:"Description"`
	KmsKeyIdentifier string              `json:"KmsKeyIdentifier,omitempty" cbor:"KmsKeyIdentifier,omitempty"`
	DeadLetterConfig *ebDeadLetterConfig `json:"DeadLetterConfig,omitempty" cbor:"DeadLetterConfig,omitempty"`
}

// ebDeadLetterConfig is CreateEventBus/DescribeEventBus's DeadLetterConfig
// member — the SQS queue EventBridge would use as a dead-letter queue for the
// bus itself. It is stored and echoed back verbatim; Overcast does not
// deliver failed bus-level operations to it (that is a real-AWS behavior
// with no analogue in this emulator, distinct from the per-target
// DeadLetterConfig ebTarget already carries and delivery.go does act on).
type ebDeadLetterConfig struct {
	Arn string `json:"Arn,omitempty" cbor:"Arn,omitempty"`
}

type ebRule struct {
	Name         string `json:"Name" cbor:"Name"`
	ARN          string `json:"Arn" cbor:"Arn"`
	EventBusName string `json:"EventBusName" cbor:"EventBusName"`
	State        string `json:"State" cbor:"State"`
	Description  string `json:"Description" cbor:"Description"`
	RoleARN      string `json:"RoleArn,omitempty" cbor:"RoleArn,omitempty"`
	EventPattern string `json:"EventPattern" cbor:"EventPattern"`
	ScheduleExpr string `json:"ScheduleExpression" cbor:"ScheduleExpression"`
}

// ebInputTransformer is AWS's Target.InputTransformer: named JSONPath
// selections substituted into a template before delivery.
type ebInputTransformer struct {
	InputPathsMap map[string]string `json:"InputPathsMap,omitempty" cbor:"InputPathsMap,omitempty"`
	InputTemplate string            `json:"InputTemplate" cbor:"InputTemplate"`
}

type ebTarget struct {
	ID               string              `json:"Id" cbor:"Id"`
	ARN              string              `json:"Arn" cbor:"Arn"`
	RoleARN          string              `json:"RoleArn,omitempty" cbor:"RoleArn,omitempty"`
	Input            string              `json:"Input,omitempty" cbor:"Input,omitempty"`
	InputPath        string              `json:"InputPath,omitempty" cbor:"InputPath,omitempty"`
	InputTransformer *ebInputTransformer `json:"InputTransformer,omitempty" cbor:"InputTransformer,omitempty"`
	ECSParams        map[string]any      `json:"EcsParameters,omitempty" cbor:"EcsParameters,omitempty"`
	KinesisParams    map[string]any      `json:"KinesisParameters,omitempty" cbor:"KinesisParameters,omitempty"`
	SQSParams        map[string]any      `json:"SqsParameters,omitempty" cbor:"SqsParameters,omitempty"`
	RetryPolicy      map[string]any      `json:"RetryPolicy,omitempty" cbor:"RetryPolicy,omitempty"`
	DLQConfig        map[string]any      `json:"DeadLetterConfig,omitempty" cbor:"DeadLetterConfig,omitempty"`

	// kind caches the target type resolved at PutTargets time. It is not part
	// of the wire shape and is not persisted; delivery re-resolves it from the
	// ARN for targets loaded from the store.
	kind eventtarget.Kind `json:"-" cbor:"-"`
}

// displayType names the target's resolved type for the console ("Lambda",
// "Step Functions", …), or "Unknown" for an ARN that no longer classifies.
func (t ebTarget) displayType() string {
	kind := t.kind
	if kind == "" {
		resolved, err := eventtarget.Classify(t.ARN)
		if err != nil {
			return "Unknown"
		}
		kind = resolved
	}
	return eventtarget.DisplayName(kind)
}

func (s *Service) createEventBus(w http.ResponseWriter, r *http.Request) {
	// Delegates to createEventBusTyped (typed_logic.go) so the legacy
	// JSON1.0/1.1 path and the CBOR typed path share one implementation —
	// the legacy copy previously re-implemented this inline and silently
	// ignored Tags (#1196).
	var req createEventBusRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.createEventBusTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) describeEventBus(w http.ResponseWriter, r *http.Request) {
	// Delegates to describeEventBusTyped (typed_logic.go) so the legacy
	// JSON1.0/1.1 path and the CBOR typed path share one implementation —
	// the legacy copy previously re-implemented this inline and dropped
	// Description, DeadLetterConfig, KmsKeyIdentifier and Policy (#2076).
	var req describeEventBusRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.describeEventBusTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) listEventBuses(w http.ResponseWriter, r *http.Request) {
	// Delegates to listEventBusesTyped (typed_logic.go) so the legacy
	// JSON1.0/1.1 path and the CBOR typed path share one implementation — the
	// legacy copy previously decoded no request at all, and so ignored
	// NamePrefix, Limit and NextToken (#2110).
	var req listEventBusesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.listEventBusesTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) tagResource(w http.ResponseWriter, r *http.Request) {
	// Tags are sent as an array of {Key, Value} objects by AWS SDKs.
	var req struct {
		ResourceARN string `json:"ResourceARN"`
		Tags        []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	incoming := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		incoming[t.Key] = t.Value
	}
	if aerr := s.applyResourceTags(r.Context(), req.ResourceARN, incoming); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) listTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	stored, aerr := s.listResourceTags(r.Context(), req.ResourceARN)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	// Return Tags as array of {Key, Value} objects (AWS SDK wire format).
	tags := serviceutil.TagElements(stored, func(k, v string) map[string]string {
		return map[string]string{"Key": k, "Value": v}
	})
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"Tags": tags})
}

func (s *Service) untagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if aerr := s.removeResourceTags(r.Context(), req.ResourceARN, req.TagKeys); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) deleteEventBus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	arn, aerr := s.deleteEventBusRecord(r.Context(), req.Name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	s.publish(r, events.EventBridgeBusDeleted, events.ResourcePayload{Name: req.Name, ARN: arn})
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) putRule(w http.ResponseWriter, r *http.Request) {
	// Delegates to putRuleTyped (typed_logic.go) so the legacy JSON1.0/1.1
	// path and the CBOR typed path share one implementation — the legacy
	// copy previously re-implemented this inline and silently ignored Tags
	// (#1196).
	var req putRuleRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.putRuleTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) describeRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"Name"`
		EventBusName string `json:"EventBusName"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	key := serviceutil.RegionKey(s.region(r.Context()), req.EventBusName+"/"+req.Name)
	raw, found, err := s.store.Get(r.Context(), nsRules, key)
	if err != nil || !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: "Rule not found.", HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	var rule ebRule
	json.Unmarshal([]byte(raw), &rule) //nolint:errcheck
	protocol.WriteJSON(w, r, http.StatusOK, rule)
}

func (s *Service) listRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventBusName string `json:"EventBusName"`
		NamePrefix   string `json:"NamePrefix"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	kvs, err := s.store.Scan(r.Context(), nsRules, serviceutil.RegionKey(s.region(r.Context()), req.EventBusName+"/"))
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	items := make([]ebRule, 0, len(kvs))
	for _, kv := range kvs {
		var rule ebRule
		if json.Unmarshal([]byte(kv.Value), &rule) == nil {
			items = append(items, rule)
		}
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"Rules": items})
}

func (s *Service) putTargets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule         string     `json:"Rule"`
		EventBusName string     `json:"EventBusName"`
		Targets      []ebTarget `json:"Targets"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	accepted, failed, aerr := validateTargets(req.Targets)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	key := ruleKey(s.region(r.Context()), req.EventBusName, req.Rule)
	existing, err := s.loadTargets(r.Context(), req.EventBusName, req.Rule)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	merged := mergeTargets(existing, accepted)
	b, _ := json.Marshal(merged)
	if err := s.store.Set(r.Context(), nsTargets, key, string(b)); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, targetsMutationBody(failed))
}

// targetsMutationBody renders AWS's PutTargets/RemoveTargets response.
// FailedEntries is always present — the SDKs model it as a list, and an
// omitted list is not the same wire shape as an empty one.
func targetsMutationBody(failed []failedTargetEntry) map[string]any {
	entries := failed
	if entries == nil {
		entries = []failedTargetEntry{}
	}
	return map[string]any{
		"FailedEntryCount": len(entries),
		"FailedEntries":    entries,
	}
}

func (s *Service) listTargetsByRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule         string `json:"Rule"`
		EventBusName string `json:"EventBusName"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	targets, err := s.loadTargets(r.Context(), req.EventBusName, req.Rule)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if targets == nil {
		targets = []ebTarget{}
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"Targets": targets})
}

func (s *Service) removeTargets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule         string   `json:"Rule"`
		EventBusName string   `json:"EventBusName"`
		Ids          []string `json:"Ids"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	existing, err := s.loadTargets(r.Context(), req.EventBusName, req.Rule)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	kept := removeTargetIDs(existing, req.Ids)
	b, _ := json.Marshal(kept)
	if err := s.store.Set(r.Context(), nsTargets, ruleKey(s.region(r.Context()), req.EventBusName, req.Rule), string(b)); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, targetsMutationBody(nil))
}

// removeTargetIDs drops the named targets, keeping the order of the rest.
func removeTargetIDs(existing []ebTarget, ids []string) []ebTarget {
	removeSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		removeSet[id] = true
	}
	kept := make([]ebTarget, 0, len(existing))
	for _, t := range existing {
		if !removeSet[t.ID] {
			kept = append(kept, t)
		}
	}
	return kept
}

func (s *Service) disableRule(w http.ResponseWriter, r *http.Request) {
	s.setRuleState(w, r, "DISABLED")
}

func (s *Service) enableRule(w http.ResponseWriter, r *http.Request) {
	s.setRuleState(w, r, "ENABLED")
}

func (s *Service) setRuleState(w http.ResponseWriter, r *http.Request, state string) {
	var req struct {
		Name         string `json:"Name"`
		EventBusName string `json:"EventBusName"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	key := serviceutil.RegionKey(s.region(r.Context()), req.EventBusName+"/"+req.Name)
	raw, found, err := s.store.Get(r.Context(), nsRules, key)
	if err != nil || !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Rule %s does not exist.", req.Name),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	var rule ebRule
	if json.Unmarshal([]byte(raw), &rule) == nil {
		rule.State = state
		b, _ := json.Marshal(rule)
		s.store.Set(r.Context(), nsRules, key, string(b)) //nolint:errcheck
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) deleteRule(w http.ResponseWriter, r *http.Request) {
	// Delegates to deleteRuleTyped (typed_logic.go) so the legacy JSON1.0/1.1
	// path and the CBOR typed path share one implementation, and in
	// particular one copy of the targets-attached check.
	var req deleteRuleRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, aerr := s.deleteRuleTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) testEventPattern(w http.ResponseWriter, r *http.Request) {
	// Delegates to testEventPatternTyped (typed_logic.go) so the legacy
	// JSON1.1 path and the CBOR typed path share one implementation.
	var req testEventPatternRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.testEventPatternTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) putPermission(w http.ResponseWriter, r *http.Request) {
	// Delegates to putPermissionTyped (typed_logic.go) so the legacy
	// JSON1.0/1.1 path and the CBOR typed path share one implementation.
	var req putPermissionRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, aerr := s.putPermissionTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) removePermission(w http.ResponseWriter, r *http.Request) {
	// Delegates to removePermissionTyped (typed_logic.go) so the legacy
	// JSON1.0/1.1 path and the CBOR typed path share one implementation.
	var req removePermissionRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, aerr := s.removePermissionTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) putEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []map[string]any `json:"Entries"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	// Assign every entry an ID first, then fan the whole batch out in one
	// pass: deliverEntries reads each bus's rules once for the batch rather
	// than once per entry.
	eventIDs := make([]string, len(req.Entries))
	results := make([]map[string]any, 0, len(req.Entries))
	for i := range req.Entries {
		eventIDs[i] = uuid.New().String()
		results = append(results, map[string]any{"EventId": eventIDs[i]})
	}
	s.deliverEntries(r.Context(), eventIDs, req.Entries)
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"FailedEntryCount": 0,
		"Entries":          results,
	})
}
