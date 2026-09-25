package eventbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type createEventBusRequest struct {
	Name             string              `json:"Name" cbor:"Name"`
	Description      string              `json:"Description" cbor:"Description"`
	KmsKeyIdentifier string              `json:"KmsKeyIdentifier" cbor:"KmsKeyIdentifier"`
	DeadLetterConfig *ebDeadLetterConfig `json:"DeadLetterConfig" cbor:"DeadLetterConfig"`
	Tags             []tagEntry          `json:"Tags" cbor:"Tags"`
}

type createEventBusResponse struct {
	EventBusArn      string              `json:"EventBusArn" cbor:"EventBusArn"`
	Description      string              `json:"Description,omitempty" cbor:"Description,omitempty"`
	KmsKeyIdentifier string              `json:"KmsKeyIdentifier,omitempty" cbor:"KmsKeyIdentifier,omitempty"`
	DeadLetterConfig *ebDeadLetterConfig `json:"DeadLetterConfig,omitempty" cbor:"DeadLetterConfig,omitempty"`
}

type describeEventBusRequest struct {
	Name string `json:"Name" cbor:"Name"`
}

type describeEventBusResponse struct {
	Name             string              `json:"Name" cbor:"Name"`
	Arn              string              `json:"Arn" cbor:"Arn"`
	Description      string              `json:"Description,omitempty" cbor:"Description,omitempty"`
	KmsKeyIdentifier string              `json:"KmsKeyIdentifier,omitempty" cbor:"KmsKeyIdentifier,omitempty"`
	DeadLetterConfig *ebDeadLetterConfig `json:"DeadLetterConfig,omitempty" cbor:"DeadLetterConfig,omitempty"`
	// Policy is the JSON-encoded resource policy built from PutPermission
	// grants (busPolicyJSON in permissions.go), omitted entirely once no
	// permission has ever been granted on the bus — the same shape AWS uses.
	Policy string `json:"Policy,omitempty" cbor:"Policy,omitempty"`
}

type listEventBusesRequest struct {
	NamePrefix string `json:"NamePrefix" cbor:"NamePrefix"`
	// Limit is a pointer so an absent member (the default page size) is told
	// apart from an explicit 0, which the model's @range(1, 100) rejects.
	Limit     *int   `json:"Limit" cbor:"Limit"`
	NextToken string `json:"NextToken" cbor:"NextToken"`
}

type eventBusResponse struct {
	Name string `json:"Name" cbor:"Name"`
	Arn  string `json:"Arn" cbor:"Arn"`
}

type listEventBusesResponse struct {
	EventBuses []eventBusResponse `json:"EventBuses" cbor:"EventBuses"`
	NextToken  string             `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type tagResourceRequest struct {
	ResourceARN string     `json:"ResourceARN" cbor:"ResourceARN"`
	Tags        []tagEntry `json:"Tags" cbor:"Tags"`
}

type tagEntry struct {
	Key   string `json:"Key" cbor:"Key"`
	Value string `json:"Value" cbor:"Value"`
}

type listTagsForResourceRequest struct {
	ResourceARN string `json:"ResourceARN" cbor:"ResourceARN"`
}

type listTagsForResourceResponse struct {
	Tags []tagEntry `json:"Tags" cbor:"Tags"`
}

type deleteEventBusRequest struct {
	Name string `json:"Name" cbor:"Name"`
}

type putRuleRequest struct {
	Name         string     `json:"Name" cbor:"Name"`
	EventBusName string     `json:"EventBusName" cbor:"EventBusName"`
	State        string     `json:"State" cbor:"State"`
	Description  string     `json:"Description" cbor:"Description"`
	RoleARN      string     `json:"RoleArn" cbor:"RoleArn"`
	EventPattern string     `json:"EventPattern" cbor:"EventPattern"`
	ScheduleExpr string     `json:"ScheduleExpression" cbor:"ScheduleExpression"`
	Tags         []tagEntry `json:"Tags" cbor:"Tags"`
}

type putRuleResponse struct {
	RuleArn string `json:"RuleArn" cbor:"RuleArn"`
}

type describeRuleRequest struct {
	Name         string `json:"Name" cbor:"Name"`
	EventBusName string `json:"EventBusName" cbor:"EventBusName"`
}

type listRulesRequest struct {
	EventBusName string `json:"EventBusName" cbor:"EventBusName"`
	NamePrefix   string `json:"NamePrefix" cbor:"NamePrefix"`
}

type listRulesResponse struct {
	Rules []ebRule `json:"Rules" cbor:"Rules"`
}

type putTargetsRequest struct {
	Rule         string     `json:"Rule" cbor:"Rule"`
	EventBusName string     `json:"EventBusName" cbor:"EventBusName"`
	Targets      []ebTarget `json:"Targets" cbor:"Targets"`
}

type targetsMutationResponse struct {
	FailedEntryCount int                 `json:"FailedEntryCount" cbor:"FailedEntryCount"`
	FailedEntries    []failedTargetEntry `json:"FailedEntries" cbor:"FailedEntries"`
}

type listTargetsByRuleRequest struct {
	Rule         string `json:"Rule" cbor:"Rule"`
	EventBusName string `json:"EventBusName" cbor:"EventBusName"`
}

type listTargetsByRuleResponse struct {
	Targets []ebTarget `json:"Targets" cbor:"Targets"`
}

type removeTargetsRequest struct {
	Rule         string   `json:"Rule" cbor:"Rule"`
	EventBusName string   `json:"EventBusName" cbor:"EventBusName"`
	Ids          []string `json:"Ids" cbor:"Ids"`
}

type setRuleStateRequest struct {
	Name         string `json:"Name" cbor:"Name"`
	EventBusName string `json:"EventBusName" cbor:"EventBusName"`
}

type deleteRuleRequest struct {
	Name         string `json:"Name" cbor:"Name"`
	EventBusName string `json:"EventBusName" cbor:"EventBusName"`
	Force        bool   `json:"Force" cbor:"Force"`
}

type putEventsRequest struct {
	Entries []map[string]any `json:"Entries" cbor:"Entries"`
}

type untagResourceRequest struct {
	ResourceARN string   `json:"ResourceARN" cbor:"ResourceARN"`
	TagKeys     []string `json:"TagKeys" cbor:"TagKeys"`
}

type putEventsEntryResponse struct {
	EventId string `json:"EventId" cbor:"EventId"`
}

type putEventsResponse struct {
	FailedEntryCount int                      `json:"FailedEntryCount" cbor:"FailedEntryCount"`
	Entries          []putEventsEntryResponse `json:"Entries" cbor:"Entries"`
}

func (s *Service) createEventBusTyped(ctx context.Context, req *createEventBusRequest) (*createEventBusResponse, *protocol.AWSError) {
	incoming := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		incoming[t.Key] = t.Value
	}
	// Validated before the bus is written (#1196) — the same ordering
	// createStreamTyped uses (internal/services/kinesis/typed_logic.go), so a
	// rejected create leaves no bus behind.
	if aerr := serviceutil.ValidateTags(ebTagCfg, incoming); aerr != nil {
		return nil, aerr
	}
	arn := s.busARN(ctx, req.Name)
	bus := eventBus{
		Name:             req.Name,
		ARN:              arn,
		Description:      req.Description,
		KmsKeyIdentifier: req.KmsKeyIdentifier,
		DeadLetterConfig: req.DeadLetterConfig,
	}
	b, _ := json.Marshal(bus)
	if err := s.store.Set(ctx, nsBuses, serviceutil.RegionKey(s.region(ctx), req.Name), string(b)); err != nil {
		return nil, protocol.ErrInternalError
	}
	if len(incoming) > 0 {
		if aerr := s.tagStore().Save(ctx, arn, incoming); aerr != nil {
			return nil, aerr
		}
	}
	s.publishCtx(ctx, events.EventBridgeBusCreated, events.ResourcePayload{Name: req.Name, ARN: arn})
	return &createEventBusResponse{
		EventBusArn:      arn,
		Description:      req.Description,
		KmsKeyIdentifier: req.KmsKeyIdentifier,
		DeadLetterConfig: req.DeadLetterConfig,
	}, nil
}

func (s *Service) describeEventBusTyped(ctx context.Context, req *describeEventBusRequest) (*describeEventBusResponse, *protocol.AWSError) {
	// Name may be the bus ARN — the EventBusNameOrArn pattern admits the
	// prefix — and the bus is stored under its name either way.
	name := eventBusNameFromRef(req.Name)
	if name == "" {
		name = "default"
	}
	raw, found, err := s.store.Get(ctx, nsBuses, serviceutil.RegionKey(s.region(ctx), name))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	// A store miss used to be answered with a synthesized bus, so an
	// existence check succeeded here and failed on AWS (#2110). AWS models
	// ResourceNotFoundException as DescribeEventBus's error for a bus that
	// does not exist; only the default bus, which every account has and
	// which is never written to the store, is described without a record.
	var bus eventBus
	switch {
	case found:
		if err := json.Unmarshal([]byte(raw), &bus); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("eventbridge: decode bus %s: %w", name, err))
		}
	case name == "default":
		bus = eventBus{Name: name, ARN: s.busARN(ctx, name)}
	default:
		return nil, eventBusNotFound(name)
	}
	policy, aerr := s.busPolicyJSON(ctx, name)
	if aerr != nil {
		return nil, aerr
	}
	return &describeEventBusResponse{
		Name:             bus.Name,
		Arn:              bus.ARN,
		Description:      bus.Description,
		KmsKeyIdentifier: bus.KmsKeyIdentifier,
		DeadLetterConfig: bus.DeadLetterConfig,
		Policy:           policy,
	}, nil
}

// listEventBusesPageOptions is ListEventBuses's page size. The model caps
// Limit at 100 (LimitMax100) and the API reference documents no default for
// an omitted Limit, so the cap doubles as the default: a caller that never
// paginates sees at most one full page and a NextToken, the same shape it
// must already handle on AWS.
var listEventBusesPageOptions = serviceutil.PaginateOptions{DefaultLimit: 100, MaxLimit: 100}

func (s *Service) listEventBusesTyped(ctx context.Context, req *listEventBusesRequest) (*listEventBusesResponse, *protocol.AWSError) {
	// Limit is modeled @range(1, 100); an explicit value outside it is
	// refused rather than clamped, the loud direction when AWS's exact
	// answer is unverified.
	if req.Limit != nil && (*req.Limit < 1 || *req.Limit > listEventBusesPageOptions.MaxLimit) {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    fmt.Sprintf("1 validation error detected: Value '%d' at 'limit' failed to satisfy constraint: Member must have value between 1 and 100", *req.Limit),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	kvs, err := s.store.Scan(ctx, nsBuses, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]eventBusResponse, 0, len(kvs)+1)
	// The default bus is never written to the store but always exists, so
	// it heads the list whenever NamePrefix admits it. Stored buses follow
	// in key (name) order, which keeps the page boundaries stable.
	if strings.HasPrefix("default", req.NamePrefix) {
		items = append(items, eventBusResponse{Name: "default", Arn: s.busARN(ctx, "default")})
	}
	for _, kv := range kvs {
		var bus eventBus
		if json.Unmarshal([]byte(kv.Value), &bus) != nil || bus.Name == "default" {
			continue
		}
		if !strings.HasPrefix(bus.Name, req.NamePrefix) {
			continue
		}
		items = append(items, eventBusResponse{Name: bus.Name, Arn: bus.ARN})
	}
	limit := 0
	if req.Limit != nil {
		limit = *req.Limit
	}
	page, err := serviceutil.Paginate(items, limit, req.NextToken, listEventBusesPageOptions)
	if err != nil {
		// The model lists only InternalException for ListEventBuses; a token
		// this server never issued is a malformed request, answered with the
		// ValidationException EventBridge uses for those.
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "The NextToken provided is invalid.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return &listEventBusesResponse{EventBuses: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*struct{}, *protocol.AWSError) {
	incoming := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		incoming[t.Key] = t.Value
	}
	if aerr := s.applyResourceTags(ctx, req.ResourceARN, incoming); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*struct{}, *protocol.AWSError) {
	if aerr := s.removeResourceTags(ctx, req.ResourceARN, req.TagKeys); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceResponse, *protocol.AWSError) {
	stored, aerr := s.listResourceTags(ctx, req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	tags := serviceutil.TagElements(stored, func(k, v string) tagEntry {
		return tagEntry{Key: k, Value: v}
	})
	return &listTagsForResourceResponse{Tags: tags}, nil
}

func (s *Service) deleteEventBusTyped(ctx context.Context, req *deleteEventBusRequest) (*struct{}, *protocol.AWSError) {
	arn, aerr := s.deleteEventBusRecord(ctx, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	s.publishCtx(ctx, events.EventBridgeBusDeleted, events.ResourcePayload{Name: req.Name, ARN: arn})
	return &struct{}{}, nil
}

// validatePutRuleTrigger checks what a rule must carry before anything is
// stored. AWS documents both constraints on PutRule: "A rule must contain at
// least an EventPattern or ScheduleExpression", and EventPattern's "Length
// Constraints: Maximum length of 4096".
//
// The API reference publishes no message text for either, so the wording is
// Overcast's; the error code and the 400 status are the contractual part. AWS
// does not list ValidationException among PutRule's modeled errors either — it
// is the undeclared error real EventBridge answers a malformed request with,
// and the same one this package already returns for a schedule expression it
// cannot parse.
func validatePutRuleTrigger(req *putRuleRequest) *protocol.AWSError {
	if req.EventPattern == "" && req.ScheduleExpr == "" {
		return &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Parameter(s) EventPattern or ScheduleExpression must be specified.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if req.EventPattern == "" {
		return nil
	}
	if len(req.EventPattern) > maxEventPatternLength {
		return eventPatternTooLongError()
	}
	if err := validateEventPatternDocument(req.EventPattern); err != nil {
		return invalidEventPatternError(err)
	}
	return nil
}

// requireEventBus refuses a rule aimed at an event bus that does not exist,
// and returns the bus name the rule is stored under. Such a rule used to
// provision cleanly and then sit on a bus no PutEvents call could ever
// address. AWS answers ResourceNotFoundException ("An entity that you
// specified does not exist"); it publishes no message text, so the wording
// follows what this package already returns for a missing rule.
//
// The default bus is never written to the store — DescribeEventBus answers for
// it whether or not it was created, and for no other unstored name (#2110) —
// so it is always reachable. EventBusName
// may also be the bus ARN, which the API's own parameter pattern admits as an
// optional prefix; the name is returned either way, because every other rule
// path keys off the name.
func (s *Service) requireEventBus(ctx context.Context, ref string) (string, *protocol.AWSError) {
	name := eventBusNameFromRef(ref)
	if name == "" {
		name = "default"
	}
	if name == "default" {
		return name, nil
	}
	_, found, err := s.store.Get(ctx, nsBuses, serviceutil.RegionKey(s.region(ctx), name))
	if err != nil {
		return "", protocol.ErrInternalError
	}
	if !found {
		return "", eventBusNotFound(name)
	}
	return name, nil
}

// eventBusNotFound is the ResourceNotFoundException AWS answers for an event
// bus that does not exist ("An entity that you specified does not exist"). AWS
// publishes no message text; the wording is Overcast's.
func eventBusNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    fmt.Sprintf("Event bus %s does not exist.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

// requireRuleHasNoTargets refuses to delete a rule that still has targets.
// AWS states the ordering on DeleteRule — "Before you can delete the rule, you
// must remove all targets, using RemoveTargets" — and answers
// ValidationException for it. Deleting the rule anyway used to strand its
// targets, and hid the ordering mistake until the same code ran against AWS.
//
// Force is deliberately not a way around this. The API reference scopes it to
// managed rules ("If this is a managed rule, created by an AWS service on your
// behalf, you must specify Force as True to delete the rule. This parameter is
// ignored for rules that are not managed rules"), and Overcast has no managed
// rules, so it is a no-op here rather than an escape hatch.
//
// A rule that does not exist has no targets, which keeps DeleteRule idempotent:
// AWS documents that "If you call delete rule multiple times for the same rule,
// all calls will succeed."
//
// The API reference publishes no message text, so the wording is the one real
// EventBridge returns; the ValidationException code and 400 status are the
// contractual part.
func (s *Service) requireRuleHasNoTargets(ctx context.Context, busName, name string) *protocol.AWSError {
	if busName == "" {
		busName = "default"
	}
	targets, err := s.loadTargets(ctx, busName, name)
	if err != nil {
		return protocol.ErrInternalError
	}
	if len(targets) == 0 {
		return nil
	}
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    "Rule can't be deleted since it has targets.",
		HTTPStatus: http.StatusBadRequest,
	}
}

// eventBusNameFromRef resolves the bus name from either spelling the API's
// EventBusName parameter accepts — the name itself, or the bus ARN.
func eventBusNameFromRef(ref string) string {
	const marker = ":event-bus/"
	if idx := strings.Index(ref, marker); idx >= 0 {
		return ref[idx+len(marker):]
	}
	return ref
}

func (s *Service) putRuleTyped(ctx context.Context, req *putRuleRequest) (*putRuleResponse, *protocol.AWSError) {
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	if req.State == "" {
		req.State = "ENABLED"
	}
	// Everything a rule can be wrong about is checked before anything is
	// written, the same ordering CreateEventBus uses for tags (#1196), so a
	// refused PutRule leaves neither a new rule nor a half-updated one behind.
	if aerr := validatePutRuleTrigger(req); aerr != nil {
		return nil, aerr
	}
	if req.ScheduleExpr != "" {
		if _, err := nextRuleFire(req.ScheduleExpr, s.clk.Now(), s.clk.Now()); err != nil {
			return nil, scheduleValidationError(err)
		}
	}
	// The rule is stored under the bus *name* whichever of the two spellings
	// the caller used, because DescribeRule, ListRules, PutTargets and
	// delivery all key off the name. Keeping the ARN would file the rule
	// somewhere none of them look — the stranded rule this check exists to
	// prevent, arrived at by a different route.
	resolvedBus, aerr := s.requireEventBus(ctx, req.EventBusName)
	if aerr != nil {
		return nil, aerr
	}
	req.EventBusName = resolvedBus
	arn := s.ruleARN(ctx, req.EventBusName, req.Name)
	// PutRule's tags merge with whatever the rule already carries (AWS: "the
	// tags you specify ... are merged with any existing tags"), the same
	// semantic serviceutil.ApplyStoreTags already implements. Validated
	// against the merged set before the rule record is written (#1196), so a
	// rejected tag set leaves the existing rule untouched.
	existingTags, aerr := s.tagStore().Load(ctx, arn)
	if aerr != nil {
		return nil, aerr
	}
	merged := make(map[string]string, len(existingTags)+len(req.Tags))
	for k, v := range existingTags {
		merged[k] = v
	}
	for _, t := range req.Tags {
		merged[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(ebTagCfg, merged); aerr != nil {
		return nil, aerr
	}
	rule := ebRule{
		Name:         req.Name,
		ARN:          arn,
		EventBusName: req.EventBusName,
		State:        req.State,
		Description:  req.Description,
		RoleARN:      req.RoleARN,
		EventPattern: req.EventPattern,
		ScheduleExpr: req.ScheduleExpr,
	}
	key := serviceutil.RegionKey(s.region(ctx), req.EventBusName+"/"+req.Name)
	b, _ := json.Marshal(rule)
	if err := s.store.Set(ctx, nsRules, key, string(b)); err != nil {
		return nil, protocol.ErrInternalError
	}
	if len(merged) > 0 {
		if aerr := s.tagStore().Save(ctx, arn, merged); aerr != nil {
			return nil, aerr
		}
	}
	if req.ScheduleExpr != "" {
		now := s.clk.Now()
		s.setLastFire(ctx, key, now)
		s.setNextFire(ctx, key, req.ScheduleExpr, now, now)
	}
	s.publishCtx(ctx, events.EventBridgeRuleCreated, events.ResourcePayload{Name: req.Name, ARN: arn})
	return &putRuleResponse{RuleArn: arn}, nil
}

func (s *Service) describeRuleTyped(ctx context.Context, req *describeRuleRequest) (*ebRule, *protocol.AWSError) {
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	key := serviceutil.RegionKey(s.region(ctx), req.EventBusName+"/"+req.Name)
	raw, found, err := s.store.Get(ctx, nsRules, key)
	if err != nil || !found {
		return nil, ruleNotFound("Rule not found.")
	}
	var rule ebRule
	json.Unmarshal([]byte(raw), &rule) //nolint:errcheck
	return &rule, nil
}

func (s *Service) listRulesTyped(ctx context.Context, req *listRulesRequest) (*listRulesResponse, *protocol.AWSError) {
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	kvs, err := s.store.Scan(ctx, nsRules, serviceutil.RegionKey(s.region(ctx), req.EventBusName+"/"))
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	items := make([]ebRule, 0, len(kvs))
	for _, kv := range kvs {
		var rule ebRule
		if json.Unmarshal([]byte(kv.Value), &rule) == nil {
			items = append(items, rule)
		}
	}
	return &listRulesResponse{Rules: items}, nil
}

func (s *Service) putTargetsTyped(ctx context.Context, req *putTargetsRequest) (*targetsMutationResponse, *protocol.AWSError) {
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	accepted, failed, aerr := validateTargets(req.Targets)
	if aerr != nil {
		return nil, aerr
	}
	existing, err := s.loadTargets(ctx, req.EventBusName, req.Rule)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	merged := mergeTargets(existing, accepted)
	b, _ := json.Marshal(merged)
	if err := s.store.Set(ctx, nsTargets, ruleKey(s.region(ctx), req.EventBusName, req.Rule), string(b)); err != nil {
		return nil, protocol.ErrInternalError
	}
	return targetsMutation(failed), nil
}

func (s *Service) listTargetsByRuleTyped(ctx context.Context, req *listTargetsByRuleRequest) (*listTargetsByRuleResponse, *protocol.AWSError) {
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	targets, err := s.loadTargets(ctx, req.EventBusName, req.Rule)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if targets == nil {
		targets = []ebTarget{}
	}
	return &listTargetsByRuleResponse{Targets: targets}, nil
}

func (s *Service) removeTargetsTyped(ctx context.Context, req *removeTargetsRequest) (*targetsMutationResponse, *protocol.AWSError) {
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	existing, err := s.loadTargets(ctx, req.EventBusName, req.Rule)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	kept := removeTargetIDs(existing, req.Ids)
	b, _ := json.Marshal(kept)
	if err := s.store.Set(ctx, nsTargets, ruleKey(s.region(ctx), req.EventBusName, req.Rule), string(b)); err != nil {
		return nil, protocol.ErrInternalError
	}
	return targetsMutation(nil), nil
}

func (s *Service) disableRuleTyped(ctx context.Context, req *setRuleStateRequest) (*struct{}, *protocol.AWSError) {
	return s.setRuleStateTyped(ctx, req, "DISABLED")
}

func (s *Service) enableRuleTyped(ctx context.Context, req *setRuleStateRequest) (*struct{}, *protocol.AWSError) {
	return s.setRuleStateTyped(ctx, req, "ENABLED")
}

func (s *Service) setRuleStateTyped(ctx context.Context, req *setRuleStateRequest, state string) (*struct{}, *protocol.AWSError) {
	if req.EventBusName == "" {
		req.EventBusName = "default"
	}
	key := serviceutil.RegionKey(s.region(ctx), req.EventBusName+"/"+req.Name)
	raw, found, err := s.store.Get(ctx, nsRules, key)
	if err != nil || !found {
		return nil, ruleNotFound(fmt.Sprintf("Rule %s does not exist.", req.Name))
	}
	var rule ebRule
	if json.Unmarshal([]byte(raw), &rule) == nil {
		rule.State = state
		b, _ := json.Marshal(rule)
		s.store.Set(ctx, nsRules, key, string(b)) //nolint:errcheck
	}
	return &struct{}{}, nil
}

func (s *Service) deleteRuleTyped(ctx context.Context, req *deleteRuleRequest) (*struct{}, *protocol.AWSError) {
	if aerr := s.requireRuleHasNoTargets(ctx, req.EventBusName, req.Name); aerr != nil {
		return nil, aerr
	}
	arn, aerr := s.deleteRuleRecord(ctx, req.EventBusName, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	s.publishCtx(ctx, events.EventBridgeRuleDeleted, events.ResourcePayload{Name: req.Name, ARN: arn})
	return &struct{}{}, nil
}

func (s *Service) putEventsTyped(ctx context.Context, req *putEventsRequest) (*putEventsResponse, *protocol.AWSError) {
	eventIDs := make([]string, len(req.Entries))
	results := make([]putEventsEntryResponse, 0, len(req.Entries))
	for i := range req.Entries {
		eventIDs[i] = uuid.New().String()
		results = append(results, putEventsEntryResponse{EventId: eventIDs[i]})
	}
	s.deliverEntries(ctx, eventIDs, req.Entries)
	return &putEventsResponse{FailedEntryCount: 0, Entries: results}, nil
}

func (s *Service) publishCtx(ctx context.Context, t events.Type, payload any) {
	if s.bus != nil {
		s.bus.Publish(ctx, events.Event{Type: t, Payload: payload})
	}
}

func targetsMutation(failed []failedTargetEntry) *targetsMutationResponse {
	if failed == nil {
		failed = []failedTargetEntry{}
	}
	return &targetsMutationResponse{FailedEntryCount: len(failed), FailedEntries: failed}
}

func ruleNotFound(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

// ── TestEventPattern ─────────────────────────────────────────────────────────

type testEventPatternRequest struct {
	EventPattern string `json:"EventPattern" cbor:"EventPattern"`
	Event        string `json:"Event" cbor:"Event"`
}

type testEventPatternResponse struct {
	Result bool `json:"Result" cbor:"Result"`
}

// maxEventPatternLength is the documented EventPattern length constraint.
const maxEventPatternLength = 4096

// testEventPatternTyped evaluates req.Event against req.EventPattern with the
// same matcher PutEvents uses to select rules, so a pattern that passes here
// is one a rule would fire on. A pattern PutRule would refuse — unparseable,
// the wrong shape, or naming a match type this emulator does not evaluate — is
// the documented InvalidEventPatternException rather than a silent
// Result=false; the event must be a JSON object. AWS also lists id, account,
// source, time, region, resources and detail-type as mandatory envelope fields
// but documents no error for their absence, so they are not enforced here.
func (s *Service) testEventPatternTyped(_ context.Context, req *testEventPatternRequest) (*testEventPatternResponse, *protocol.AWSError) {
	if len(req.EventPattern) > maxEventPatternLength {
		return nil, eventPatternTooLongError()
	}
	pattern, err := parseEventPattern(req.EventPattern)
	if err != nil {
		return nil, invalidEventPatternError(err)
	}
	// TestEventPattern exists to tell a caller whether a pattern would work on
	// a rule, so it answers the same way PutRule does about the pattern
	// itself: a shape or match type PutRule refuses is an error here too, not
	// a Result=false that reads as "valid, just did not match".
	if err := validateEventPattern(pattern); err != nil {
		return nil, invalidEventPatternError(err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(req.Event), &event); err != nil || event == nil {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Event must be a valid JSON object.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return &testEventPatternResponse{Result: matchPatternMap(pattern, event)}, nil
}

// eventPatternTooLongError is the documented EventPattern length constraint,
// shared by PutRule and TestEventPattern so both refuse the same input.
func eventPatternTooLongError() *protocol.AWSError {
	return &protocol.AWSError{
		Code: "ValidationException",
		Message: fmt.Sprintf("1 validation error detected: Value at 'eventPattern' failed to satisfy constraint: "+
			"Member must have length less than or equal to %d", maxEventPatternLength),
		HTTPStatus: http.StatusBadRequest,
	}
}

func invalidEventPatternError(err error) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidEventPatternException",
		Message:    "Event pattern is not valid. Reason: " + err.Error(),
		HTTPStatus: http.StatusBadRequest,
	}
}

// ── PutPermission / RemovePermission ────────────────────────────────────────

// ebPermissionCondition is PutPermission's optional Condition member. The
// smithy model gives it a structured shape ({Type, Key, Value}), not the raw
// JSON string the (older) prose documentation describes; the SDKs send it as
// a nested object, so that is what Overcast decodes.
type ebPermissionCondition struct {
	Type  string `json:"Type" cbor:"Type"`
	Key   string `json:"Key" cbor:"Key"`
	Value string `json:"Value" cbor:"Value"`
}

type putPermissionRequest struct {
	EventBusName string                 `json:"EventBusName" cbor:"EventBusName"`
	Action       string                 `json:"Action" cbor:"Action"`
	Principal    string                 `json:"Principal" cbor:"Principal"`
	StatementId  string                 `json:"StatementId" cbor:"StatementId"`
	Condition    *ebPermissionCondition `json:"Condition" cbor:"Condition"`
	// Policy is a whole permission-policy JSON document, used "instead of"
	// StatementId/Action/Principal/Condition (AWS's own wording) — it
	// replaces the bus's entire policy rather than adding one statement.
	Policy string `json:"Policy" cbor:"Policy"`
}

type removePermissionRequest struct {
	StatementId          string `json:"StatementId" cbor:"StatementId"`
	RemoveAllPermissions bool   `json:"RemoveAllPermissions" cbor:"RemoveAllPermissions"`
	EventBusName         string `json:"EventBusName" cbor:"EventBusName"`
}

// putPermissionTyped grants (or replaces) a statement on the bus's resource
// policy. AWS documents two mutually exclusive shapes for the request: a
// single statement built from Action/Principal/StatementId/Condition, or a
// whole Policy document supplied "instead of" those parameters — see
// permissions.go for how each is turned into the statement list
// DescribeEventBus.Policy is rendered from.
//
// Enforcement is deliberately out of scope: the policy is stored and
// reported back through DescribeEventBus, not consulted to authorize
// PutEvents from another account — the same stored-not-enforced treatment
// Overcast gives every other IAM policy (config.Config.EnforceIAM gates
// identity-policy evaluation for the caller's own credentials; it has no
// bearing on this bus-level resource policy).
func (s *Service) putPermissionTyped(ctx context.Context, req *putPermissionRequest) (*struct{}, *protocol.AWSError) {
	busName, aerr := s.requireEventBus(ctx, req.EventBusName)
	if aerr != nil {
		return nil, aerr
	}
	var statements []map[string]any
	if req.Policy != "" {
		parsed, err := parsePolicyStatements(req.Policy)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "Policy is not valid JSON: " + err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		statements = parsed
	} else {
		if req.Action == "" || req.Principal == "" || req.StatementId == "" {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "Parameters Action, Principal and StatementId must be specified, or Policy must be specified instead.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		existing, aerr := s.loadBusPolicyStatements(ctx, busName)
		if aerr != nil {
			return nil, aerr
		}
		statement := map[string]any{
			"Sid":       req.StatementId,
			"Effect":    "Allow",
			"Principal": principalElement(req.Principal),
			"Action":    req.Action,
			"Resource":  s.busARN(ctx, busName),
		}
		if req.Condition != nil {
			statement["Condition"] = map[string]any{
				req.Condition.Type: map[string]any{req.Condition.Key: req.Condition.Value},
			}
		}
		statements = upsertStatement(existing, req.StatementId, statement)
	}
	if aerr := s.saveBusPolicyStatements(ctx, busName, statements); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

// removePermissionTyped revokes one statement by StatementId, or the whole
// policy at once with RemoveAllPermissions — AWS documents both, and a
// StatementId that does not name a current statement is
// ResourceNotFoundException.
func (s *Service) removePermissionTyped(ctx context.Context, req *removePermissionRequest) (*struct{}, *protocol.AWSError) {
	busName, aerr := s.requireEventBus(ctx, req.EventBusName)
	if aerr != nil {
		return nil, aerr
	}
	if req.RemoveAllPermissions {
		if aerr := s.saveBusPolicyStatements(ctx, busName, nil); aerr != nil {
			return nil, aerr
		}
		return &struct{}{}, nil
	}
	if req.StatementId == "" {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Parameter StatementId must be specified unless RemoveAllPermissions is set.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	statements, aerr := s.loadBusPolicyStatements(ctx, busName)
	if aerr != nil {
		return nil, aerr
	}
	kept := make([]map[string]any, 0, len(statements))
	found := false
	for _, st := range statements {
		if sid, _ := st["Sid"].(string); sid == req.StatementId {
			found = true
			continue
		}
		kept = append(kept, st)
	}
	if !found {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Statement with the id %s does not exist.", req.StatementId),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if aerr := s.saveBusPolicyStatements(ctx, busName, kept); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}
