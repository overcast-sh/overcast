package cloudformation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/overcast-sh/overcast/internal/config"
)

// Step Functions versions and aliases, and the state machine helpers they
// share with sfnStateMachineHandler (provisioner_resources.go).

// sfnInitialRevisionID is what GetAtt StateMachineRevisionId reports for a
// state machine that has never been updated, and what
// PublishStateMachineVersion's revisionId accepts for that revision.
const sfnInitialRevisionID = "INITIAL"

// sfnDefinitionS3Location fetches the ASL document a StateMachine's
// DefinitionS3Location {Bucket, Key, Version} names, through S3's own GET
// Object, so a missing object fails the resource the way it does on AWS.
func sfnDefinitionS3Location(ctx context.Context, router http.Handler, region string, raw any) (string, error) {
	loc, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("DefinitionS3Location must be an object with Bucket and Key")
	}
	bucket, _ := loc["Bucket"].(string)
	key, _ := loc["Key"].(string)
	if bucket == "" || key == "" {
		return "", fmt.Errorf("DefinitionS3Location requires Bucket and Key")
	}
	path := "/" + bucket + "/" + key
	if version := fmt.Sprint(loc["Version"]); loc["Version"] != nil && version != "" {
		path += "?versionId=" + url.QueryEscape(version)
	}
	rec, err := internalS3Request(ctx, router, region, http.MethodGet, path, "", nil)
	if err != nil {
		return "", fmt.Errorf("DefinitionS3Location s3://%s/%s: %w", bucket, key, err)
	}
	return rec.Body.String(), nil
}

// sfnRevisionAttr reads a state machine's current revisionId for GetAtt
// StateMachineRevisionId: INITIAL until the state machine is first updated.
func sfnRevisionAttr(ctx context.Context, router http.Handler, region, arn string) (string, error) {
	rec, err := internalJSON(ctx, router, region, "AWSStepFunctions.DescribeStateMachine", map[string]any{"stateMachineArn": arn})
	if err != nil {
		return "", fmt.Errorf("DescribeStateMachine: %w", err)
	}
	var resp struct {
		RevisionID string `json:"revisionId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("DescribeStateMachine: parse response: %w", err)
	}
	if resp.RevisionID == "" {
		return sfnInitialRevisionID, nil
	}
	return resp.RevisionID, nil
}

// ── AWS::StepFunctions::StateMachineVersion ────────────────────────────────

// sfnStateMachineVersionHandler publishes a version. Every property is
// "Update requires: Replacement", so there is no Update: the provisioner's
// replacement path publishes the new version first and deletes the old one
// only once the whole stack update has succeeded — after any alias pointing at
// it has moved on, which DeleteStateMachineVersion would otherwise refuse.
type sfnStateMachineVersionHandler struct{}

func (h *sfnStateMachineVersionHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	smArn, _ := props["StateMachineArn"].(string)
	if smArn == "" {
		return "", nil, fmt.Errorf("AWS::StepFunctions::StateMachineVersion: StateMachineArn is required")
	}
	body := map[string]any{"stateMachineArn": smArn}
	if v, _ := props["StateMachineRevisionId"].(string); v != "" {
		body["revisionId"] = v
	}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	noteUnconsumedProperties(ctx, "AWS::StepFunctions::StateMachineVersion", props,
		"StateMachineArn", "StateMachineRevisionId", "Description")

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.PublishStateMachineVersion", body)
	if err != nil {
		return "", nil, fmt.Errorf("PublishStateMachineVersion: %w", err)
	}
	var resp struct {
		StateMachineVersionArn string `json:"stateMachineVersionArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("PublishStateMachineVersion: parse response: %w", err)
	}
	// Ref and GetAtt Arn are both the version ARN.
	return resp.StateMachineVersionArn, map[string]string{"Arn": resp.StateMachineVersionArn}, nil
}

func (h *sfnStateMachineVersionHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.DeleteStateMachineVersion", map[string]any{
		"stateMachineVersionArn": physicalID,
	})
	return teardownError("DeleteStateMachineVersion", rec, err)
}

// ── AWS::StepFunctions::StateMachineAlias ──────────────────────────────────

// sfnStateMachineAliasHandler manages an alias. Name is "Update requires:
// Replacement"; Description, RoutingConfiguration and DeploymentPreference
// update in place.
//
// DeploymentPreference is applied as an immediate, all-at-once shift of every
// execution to its StateMachineVersionArn. CloudFormation's LINEAR and CANARY
// traffic shifting and its alarm-driven rollback are not simulated; a
// template that asks for them gets that reported as a limitation.
type sfnStateMachineAliasHandler struct{}

// sfnAliasNameMaxLen is CreateStateMachineAlias's name length limit.
const sfnAliasNameMaxLen = 80

func (h *sfnStateMachineAliasHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	routing, err := sfnAliasRouting(ctx, props)
	if err != nil {
		return "", nil, err
	}
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(sfnAliasNameMaxLen)
	}
	body := map[string]any{"name": name, "routingConfiguration": routing}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	// StateMachineArn has no CreateStateMachineAlias member: the alias belongs
	// to the state machine its versions do.
	noteUnconsumedProperties(ctx, "AWS::StepFunctions::StateMachineAlias", props,
		"Name", "Description", "RoutingConfiguration", "DeploymentPreference", "StateMachineArn")

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.CreateStateMachineAlias", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateStateMachineAlias: %w", err)
	}
	var resp struct {
		StateMachineAliasArn string `json:"stateMachineAliasArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateStateMachineAlias: parse response: %w", err)
	}
	return resp.StateMachineAliasArn, map[string]string{"Arn": resp.StateMachineAliasArn}, nil
}

func (h *sfnStateMachineAliasHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, _ map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if name, _ := props["Name"].(string); name != "" && !strings.HasSuffix(physicalID, ":"+name) {
		return "", nil, errReplacementRequired
	}
	routing, err := sfnAliasRouting(ctx, props)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	description, _ := props["Description"].(string)
	if _, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.UpdateStateMachineAlias", map[string]any{
		"stateMachineAliasArn": physicalID,
		"description":          description,
		"routingConfiguration": routing,
	}); err != nil {
		return "", nil, failUpdate(fmt.Errorf("UpdateStateMachineAlias: %w", err))
	}
	return physicalID, map[string]string{"Arn": physicalID}, nil
}

func (h *sfnStateMachineAliasHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.DeleteStateMachineAlias", map[string]any{
		"stateMachineAliasArn": physicalID,
	})
	return teardownError("DeleteStateMachineAlias", rec, err)
}

// sfnAliasRouting translates RoutingConfiguration, or DeploymentPreference in
// its place, into CreateStateMachineAlias's routingConfiguration. The two
// properties are mutually exclusive, as CloudFormation documents.
func sfnAliasRouting(ctx context.Context, props map[string]any) ([]map[string]any, error) {
	routing, hasRouting := props["RoutingConfiguration"].([]any)
	preference, hasPreference := props["DeploymentPreference"].(map[string]any)
	switch {
	case hasRouting && hasPreference:
		return nil, fmt.Errorf("AWS::StepFunctions::StateMachineAlias: RoutingConfiguration and DeploymentPreference are mutually exclusive")
	case hasPreference:
		versionArn, _ := preference["StateMachineVersionArn"].(string)
		if versionArn == "" {
			return nil, fmt.Errorf("AWS::StepFunctions::StateMachineAlias: DeploymentPreference.StateMachineVersionArn is required")
		}
		deployType, _ := preference["Type"].(string)
		if alarms, _ := preference["Alarms"].([]any); deployType != "ALL_AT_ONCE" || len(alarms) > 0 {
			noteLimitation(ctx, fmt.Sprintf(
				"Overcast: AWS::StepFunctions::StateMachineAlias DeploymentPreference (Type %s) was applied as an immediate all-at-once shift to %s; gradual traffic shifting and alarm-based rollback are not simulated.",
				deployType, versionArn))
		}
		return []map[string]any{{"stateMachineVersionArn": versionArn, "weight": 100}}, nil
	case hasRouting:
		out := make([]map[string]any, 0, len(routing))
		for _, item := range routing {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("AWS::StepFunctions::StateMachineAlias: RoutingConfiguration entries must be objects")
			}
			weight, err := sfnWeight(entry["Weight"])
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"stateMachineVersionArn": entry["StateMachineVersionArn"], "weight": weight})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("AWS::StepFunctions::StateMachineAlias: one of RoutingConfiguration or DeploymentPreference is required")
	}
}

// sfnWeight reads a RoutingConfiguration Weight, which a template may carry as
// a number or — through a parameter — as a string.
func sfnWeight(raw any) (int, error) {
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("AWS::StepFunctions::StateMachineAlias: Weight %q is not an integer", v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("AWS::StepFunctions::StateMachineAlias: Weight is required")
	}
}

// ── AWS::StepFunctions::Activity ───────────────────────────────────────────

// sfnActivityHandler manages an activity. Name and EncryptionConfiguration
// force replacement; Tags update in place.
type sfnActivityHandler struct{}

// sfnActivityNameMaxLen is the CreateActivity name length limit.
const sfnActivityNameMaxLen = 80

func (h *sfnActivityHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(sfnActivityNameMaxLen)
	}
	body := map[string]any{"name": name}
	if v, ok := props["EncryptionConfiguration"].(map[string]any); ok {
		// CloudFormation spells the members KmsKeyId, Type, ...; the API
		// kmsKeyId, type, ...
		encryption := make(map[string]any, len(v))
		for key, value := range v {
			r, size := utf8.DecodeRuneInString(key)
			encryption[string(unicode.ToLower(r))+key[size:]] = value
		}
		body["encryptionConfiguration"] = encryption
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["tags"] = sfnTagsWire(tags)
	}
	noteUnconsumedProperties(ctx, "AWS::StepFunctions::Activity", props, "Name", "EncryptionConfiguration", "Tags")

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.CreateActivity", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateActivity: %w", err)
	}
	var resp struct {
		ActivityArn string `json:"activityArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateActivity: parse response: %w", err)
	}
	return resp.ActivityArn, map[string]string{"Arn": resp.ActivityArn, "Name": name}, nil
}

func (h *sfnActivityHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name != "" && !strings.HasSuffix(physicalID, ":activity:"+name) {
		return "", nil, errReplacementRequired
	}
	if !reflect.DeepEqual(props["EncryptionConfiguration"], oldProps["EncryptionConfiguration"]) {
		return "", nil, errReplacementRequired
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		if err := sfnReconcileTags(ctx, router, rCtx.Region, physicalID, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("stepfunctions activity tags: %w", err))
		}
	}
	if name == "" {
		name = physicalID[strings.LastIndex(physicalID, ":")+1:]
	}
	return physicalID, map[string]string{"Arn": physicalID, "Name": name}, nil
}

func (h *sfnActivityHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.DeleteActivity", map[string]any{
		"activityArn": physicalID,
	})
	return teardownError("DeleteActivity", rec, err)
}
