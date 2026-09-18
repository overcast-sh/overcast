package stepfunctions

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type createStateMachineRequest struct {
	Name                 string         `json:"name" cbor:"name"`
	Definition           string         `json:"definition" cbor:"definition"`
	RoleArn              string         `json:"roleArn" cbor:"roleArn"`
	Type                 string         `json:"type" cbor:"type"`
	LoggingConfiguration map[string]any `json:"loggingConfiguration" cbor:"loggingConfiguration"`
	TracingConfiguration map[string]any `json:"tracingConfiguration" cbor:"tracingConfiguration"`
	Tags                 []sfnTag       `json:"tags" cbor:"tags"`
	Publish              bool           `json:"publish" cbor:"publish"`
	VersionDescription   string         `json:"versionDescription" cbor:"versionDescription"`
}

// sfnTag is the wire shape of Step Functions' tag list, shared by
// CreateStateMachine and TagResource.
type sfnTag struct {
	Key   string `json:"key" cbor:"key"`
	Value string `json:"value" cbor:"value"`
}

type createStateMachineResponse struct {
	StateMachineArn        string  `json:"stateMachineArn" cbor:"stateMachineArn"`
	CreationDate           float64 `json:"creationDate" cbor:"creationDate"`
	StateMachineVersionArn string  `json:"stateMachineVersionArn,omitempty" cbor:"stateMachineVersionArn,omitempty"`
}

type describeStateMachineRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
}

type describeStateMachineResponse struct {
	StateMachineArn      string         `json:"stateMachineArn" cbor:"stateMachineArn"`
	Name                 string         `json:"name" cbor:"name"`
	Definition           string         `json:"definition" cbor:"definition"`
	RoleArn              string         `json:"roleArn" cbor:"roleArn"`
	Type                 string         `json:"type" cbor:"type"`
	Status               string         `json:"status" cbor:"status"`
	CreationDate         float64        `json:"creationDate" cbor:"creationDate"`
	LoggingConfiguration map[string]any `json:"loggingConfiguration" cbor:"loggingConfiguration"`
	TracingConfiguration map[string]any `json:"tracingConfiguration" cbor:"tracingConfiguration"`
	// RevisionID is absent until the state machine is first updated.
	// Description is a version's, present only when a version ARN was
	// described.
	RevisionID  string `json:"revisionId,omitempty" cbor:"revisionId,omitempty"`
	Description string `json:"description,omitempty" cbor:"description,omitempty"`
}

type updateStateMachineRequest struct {
	StateMachineArn      string         `json:"stateMachineArn" cbor:"stateMachineArn"`
	Definition           string         `json:"definition" cbor:"definition"`
	RoleArn              string         `json:"roleArn" cbor:"roleArn"`
	LoggingConfiguration map[string]any `json:"loggingConfiguration" cbor:"loggingConfiguration"`
	TracingConfiguration map[string]any `json:"tracingConfiguration" cbor:"tracingConfiguration"`
	Publish              bool           `json:"publish" cbor:"publish"`
	VersionDescription   string         `json:"versionDescription" cbor:"versionDescription"`
}

type updateStateMachineResponse struct {
	UpdateDate             float64 `json:"updateDate" cbor:"updateDate"`
	RevisionID             string  `json:"revisionId,omitempty" cbor:"revisionId,omitempty"`
	StateMachineVersionArn string  `json:"stateMachineVersionArn,omitempty" cbor:"stateMachineVersionArn,omitempty"`
}

type listStateMachinesRequest struct {
	MaxResults int    `json:"maxResults" cbor:"maxResults"`
	NextToken  string `json:"nextToken" cbor:"nextToken"`
}

type stateMachineListItem struct {
	StateMachineArn string  `json:"stateMachineArn" cbor:"stateMachineArn"`
	Name            string  `json:"name" cbor:"name"`
	Type            string  `json:"type" cbor:"type"`
	CreationDate    float64 `json:"creationDate" cbor:"creationDate"`
}

type listStateMachinesResponse struct {
	StateMachines []stateMachineListItem `json:"stateMachines" cbor:"stateMachines"`
	NextToken     string                 `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

type startExecutionRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
	Input           string `json:"input" cbor:"input"`
	Name            string `json:"name" cbor:"name"`
}

type startExecutionResponse struct {
	ExecutionArn string  `json:"executionArn" cbor:"executionArn"`
	StartDate    float64 `json:"startDate" cbor:"startDate"`
}

type deleteStateMachineRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
}

func (h *Handler) createStateMachineTyped(ctx context.Context, req *createStateMachineRequest) (*createStateMachineResponse, *protocol.AWSError) {
	if req.Name == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidName",
			Message:    "Value null at 'name' failed to satisfy constraint",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if req.VersionDescription != "" && !req.Publish {
		return nil, errVersionDescriptionWithoutPublish()
	}
	if aerr := validateDescription("versionDescription", req.VersionDescription); aerr != nil {
		return nil, aerr
	}
	if aerr := validateDefinitionForType(req.Definition, req.Type); aerr != nil {
		return nil, aerr
	}
	existing, err := h.store.GetStateMachine(ctx, req.Name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		smType := req.Type
		if smType == "" {
			smType = "STANDARD"
		}
		if existing.Definition == req.Definition && existing.RoleArn == req.RoleArn && existing.Type == smType {
			resp := &createStateMachineResponse{
				StateMachineArn: existing.ARN,
				CreationDate:    float64(existing.CreatedAt.UnixMilli()) / 1000.0,
			}
			// An idempotent repeat with publish=true answers with the version
			// already published for this revision (publishVersion is itself
			// idempotent on the revision).
			if req.Publish {
				v, aerr := h.publishVersion(ctx, existing, req.VersionDescription)
				if aerr != nil {
					return nil, aerr
				}
				resp.StateMachineVersionArn = v.ARN
			}
			return resp, nil
		}
		return nil, &protocol.AWSError{
			Code:       "StateMachineAlreadyExists",
			Message:    fmt.Sprintf("State Machine Already Exists: '%s'", req.Name),
			HTTPStatus: http.StatusConflict,
		}
	}
	smType := req.Type
	if smType == "" {
		smType = "STANDARD"
	}
	// Validated before the state machine is written (#1196) — a rejected
	// create leaves no state machine behind.
	tags := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		tags[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(sfnTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	now := h.clk.Now()
	// The typed path used to hardcode h.cfg.Region here instead of resolving
	// the request's actual region (as execution_ops.go's StartExecution
	// already does) — a pre-existing divergence between this path and the
	// legacy JSON handler unified into it by #1196. A CBOR create used to
	// mint an ARN in the account's default region regardless of the request
	// region; fixed to match.
	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	arn := protocol.ARN(region, h.cfg.AccountID, "states", "stateMachine:"+req.Name)
	sm := &StateMachine{
		Name:                 req.Name,
		ARN:                  arn,
		Definition:           req.Definition,
		RoleArn:              req.RoleArn,
		Type:                 smType,
		Status:               "ACTIVE",
		CreatedAt:            now,
		LoggingConfiguration: req.LoggingConfiguration,
		TracingConfiguration: req.TracingConfiguration,
		Tags:                 tags,
	}
	if err := h.store.PutStateMachine(ctx, sm); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	h.publishCtx(ctx, events.SFNStateMachineCreated, events.ResourcePayload{Name: req.Name})
	resp := &createStateMachineResponse{
		StateMachineArn: arn,
		CreationDate:    float64(now.UnixMilli()) / 1000.0,
	}
	if req.Publish {
		v, aerr := h.publishVersion(ctx, sm, req.VersionDescription)
		if aerr != nil {
			return nil, aerr
		}
		resp.StateMachineVersionArn = v.ARN
	}
	return resp, nil
}

// describeStateMachineTyped describes a state machine, or — given a version
// ARN — that version's snapshot. An alias ARN is not a state machine
// DescribeStateMachine can describe; DescribeStateMachineAlias does that.
func (h *Handler) describeStateMachineTyped(ctx context.Context, req *describeStateMachineRequest) (*describeStateMachineResponse, *protocol.AWSError) {
	if arn, ok := parseSMARN(req.StateMachineArn); ok {
		switch {
		case arn.isVersion():
			return h.describeVersion(ctx, arn, req.StateMachineArn)
		case arn.isAlias():
			return nil, errInvalidArn(req.StateMachineArn)
		}
	}
	name := extractSMName(req.StateMachineArn)
	sm, err := h.store.GetStateMachine(ctx, name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if sm == nil {
		return nil, errSMNotFound(req.StateMachineArn)
	}
	return &describeStateMachineResponse{
		StateMachineArn:      sm.ARN,
		Name:                 sm.Name,
		Definition:           sm.Definition,
		RoleArn:              sm.RoleArn,
		Type:                 sm.Type,
		Status:               sm.Status,
		CreationDate:         float64(sm.CreatedAt.UnixMilli()) / 1000.0,
		LoggingConfiguration: sfnLoggingConfigOrDefault(sm.LoggingConfiguration),
		TracingConfiguration: sfnTracingConfigOrDefault(sm.TracingConfiguration),
		RevisionID:           sm.RevisionID,
	}, nil
}

// updateStateMachineTyped implements UpdateStateMachine. Every field besides
// StateMachineArn is optional and left unchanged when omitted, matching real
// AWS: an update supplies only the properties it wants to change.
//
// An update that changes the definition, role, logging or tracing
// configuration starts a new revision (a fresh revisionId). publish=true then
// publishes that revision as a version — or returns the version already
// published for it when nothing changed.
func (h *Handler) updateStateMachineTyped(ctx context.Context, req *updateStateMachineRequest) (*updateStateMachineResponse, *protocol.AWSError) {
	if req.VersionDescription != "" && !req.Publish {
		return nil, errVersionDescriptionWithoutPublish()
	}
	if aerr := validateDescription("versionDescription", req.VersionDescription); aerr != nil {
		return nil, aerr
	}
	name := extractSMName(req.StateMachineArn)
	sm, err := h.store.GetStateMachine(ctx, name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if sm == nil {
		return nil, errSMNotFound(req.StateMachineArn)
	}
	changed := false
	if req.Definition != "" {
		if aerr := validateDefinitionForType(req.Definition, sm.Type); aerr != nil {
			return nil, aerr
		}
		changed = changed || sm.Definition != req.Definition
		sm.Definition = req.Definition
	}
	if req.RoleArn != "" {
		changed = changed || sm.RoleArn != req.RoleArn
		sm.RoleArn = req.RoleArn
	}
	if req.LoggingConfiguration != nil {
		changed = changed || !reflect.DeepEqual(sm.LoggingConfiguration, req.LoggingConfiguration)
		sm.LoggingConfiguration = req.LoggingConfiguration
	}
	if req.TracingConfiguration != nil {
		changed = changed || !reflect.DeepEqual(sm.TracingConfiguration, req.TracingConfiguration)
		sm.TracingConfiguration = req.TracingConfiguration
	}
	if changed {
		sm.RevisionID = uuid.NewString()
	}
	if err := h.store.PutStateMachine(ctx, sm); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	h.publishCtx(ctx, events.SFNStateMachineUpdated, events.ResourcePayload{Name: name})
	resp := &updateStateMachineResponse{
		UpdateDate: float64(h.clk.Now().UnixMilli()) / 1000.0,
		RevisionID: sm.RevisionID,
	}
	if req.Publish {
		v, aerr := h.publishVersion(ctx, sm, req.VersionDescription)
		if aerr != nil {
			return nil, aerr
		}
		resp.StateMachineVersionArn = v.ARN
	}
	return resp, nil
}

// listStateMachinesTyped lists state machines by name, a page at a time.
func (h *Handler) listStateMachinesTyped(ctx context.Context, req *listStateMachinesRequest) (*listStateMachinesResponse, *protocol.AWSError) {
	sms, err := h.store.ListStateMachines(ctx)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	sort.Slice(sms, func(i, j int) bool { return sms[i].Name < sms[j].Name })
	page, aerr := paginate(sms, req.MaxResults, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	items := make([]stateMachineListItem, 0, len(page.Items))
	for _, sm := range page.Items {
		items = append(items, stateMachineListItem{
			StateMachineArn: sm.ARN,
			Name:            sm.Name,
			Type:            sm.Type,
			CreationDate:    float64(sm.CreatedAt.UnixMilli()) / 1000.0,
		})
	}
	return &listStateMachinesResponse{StateMachines: items, NextToken: page.NextToken}, nil
}

// deleteStateMachineTyped deletes a state machine together with every version
// and alias published under it.
func (h *Handler) deleteStateMachineTyped(ctx context.Context, req *deleteStateMachineRequest) (*struct{}, *protocol.AWSError) {
	name := extractSMName(req.StateMachineArn)
	if err := h.deleteVersionsAndAliases(ctx, name); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := h.store.DeleteStateMachine(ctx, name); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	h.publishCtx(ctx, events.SFNStateMachineDeleted, events.ResourcePayload{Name: name})
	return &struct{}{}, nil
}

func (h *Handler) publishCtx(ctx context.Context, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: t, Payload: payload})
	}
}

type listTagsForResourceTypedResponse struct {
	Tags []sfnTag `json:"tags" cbor:"tags"`
}

// taggedResource resolves a TagResource/UntagResource/ListTagsForResource ARN
// to load and save functions for its record: a state machine or an activity,
// the two resource types Step Functions tags.
func (h *Handler) taggedResource(arn string) (
	load func(ctx context.Context, key string) (serviceutil.Taggable, *protocol.AWSError),
	save func(ctx context.Context, resource serviceutil.Taggable) *protocol.AWSError,
	key string,
) {
	if isActivityARN(arn) {
		load = func(ctx context.Context, _ string) (serviceutil.Taggable, *protocol.AWSError) {
			return h.loadActivity(ctx, arn)
		}
		save = func(ctx context.Context, resource serviceutil.Taggable) *protocol.AWSError {
			if err := h.store.PutActivity(ctx, resource.(*Activity)); err != nil {
				return protocol.Wrap(protocol.ErrInternalError, err)
			}
			return nil
		}
		return load, save, activityNameFromARN(arn)
	}
	load = func(ctx context.Context, key string) (serviceutil.Taggable, *protocol.AWSError) {
		// A version or alias ARN is not taggable on AWS.
		if parsed, ok := parseSMARN(arn); ok && parsed.qualifier != "" {
			return nil, errInvalidArn(arn)
		}
		sm, err := h.store.GetStateMachine(ctx, key)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if sm == nil {
			return nil, errSMNotFound(arn)
		}
		return sm, nil
	}
	save = func(ctx context.Context, resource serviceutil.Taggable) *protocol.AWSError {
		if err := h.store.PutStateMachine(ctx, resource.(*StateMachine)); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	return load, save, extractSMName(arn)
}

func (h *Handler) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*struct{}, *protocol.AWSError) {
	incoming := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		incoming[t.Key] = t.Value
	}
	load, save, key := h.taggedResource(req.ResourceArn)
	if aerr := serviceutil.ApplyInlineTags(ctx, key, incoming, sfnTagCfg, load, save); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*struct{}, *protocol.AWSError) {
	load, save, key := h.taggedResource(req.ResourceArn)
	if aerr := serviceutil.RemoveInlineTags(ctx, key, req.TagKeys, load, save); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (h *Handler) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceTypedResponse, *protocol.AWSError) {
	load, _, key := h.taggedResource(req.ResourceArn)
	tags, aerr := serviceutil.ListInlineTags(ctx, key, load)
	if aerr != nil {
		return nil, aerr
	}
	return &listTagsForResourceTypedResponse{
		Tags: serviceutil.TagElements(tags, func(k, v string) sfnTag { return sfnTag{Key: k, Value: v} }),
	}, nil
}
