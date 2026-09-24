package cloudformation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"

	"github.com/overcast-sh/overcast/internal/config"
)

// provisioner_athena.go — AWS::Athena::WorkGroup, NamedQuery, PreparedStatement
// and DataCatalog. Athena's API members are spelled as the templates spell
// them, so properties forward under their own names.

// athenaCall dispatches one Athena operation, naming it in any error. Deletes
// go through internalJSON directly, since teardownError names the operation.
func athenaCall(ctx context.Context, router http.Handler, region, operation string, body map[string]any) (*httptest.ResponseRecorder, error) {
	rec, err := internalJSON(ctx, router, region, "AmazonAthena."+operation, body)
	if err != nil {
		return rec, fmt.Errorf("%s: %w", operation, err)
	}
	return rec, nil
}

// athenaARN is the ARN Athena's tag operations address a resource by.
func athenaARN(rCtx *resolveContext, kind, name string) string {
	return fmt.Sprintf("arn:aws:athena:%s:%s:%s/%s", rCtx.Region, rCtx.AccountID, kind, name)
}

// athenaReconcileTags applies the difference between a resource's desired and
// previous tags, stack tags included.
func athenaReconcileTags(ctx context.Context, router http.Handler, rCtx *resolveContext, arn string, props, oldProps map[string]any) error {
	upserts, removals := logsLogGroupTagChanges(
		mergeResourceTags(rCtx.StackTags, props["Tags"]),
		mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"]))
	if len(upserts) > 0 {
		if _, err := athenaCall(ctx, router, rCtx.Region, "TagResource", map[string]any{"ResourceARN": arn, "Tags": ecrTagsFromMap(upserts)}); err != nil {
			return err
		}
	}
	if len(removals) > 0 {
		if _, err := athenaCall(ctx, router, rCtx.Region, "UntagResource", map[string]any{"ResourceARN": arn, "TagKeys": removals}); err != nil {
			return err
		}
	}
	return nil
}

// athenaTagsBody adds a resource's tags, stack tags merged, to a create body.
func athenaTagsBody(body map[string]any, rCtx *resolveContext, props map[string]any) {
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = ecrTagsFromMap(tags)
	}
}

// athenaPropertiesChanged reports whether any of names differs between two property sets.
func athenaPropertiesChanged(props, oldProps map[string]any, names ...string) bool {
	for _, name := range names {
		if !reflect.DeepEqual(props[name], oldProps[name]) {
			return true
		}
	}
	return false
}

// ── AWS::Athena::WorkGroup ──────────────────────────────────────────────────
//
// https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-athena-workgroup.html
//
// Only Name requires replacement. Everything else updates in place through
// UpdateWorkGroup, which takes the configuration as ConfigurationUpdates, and
// the tag operations. CreateWorkGroup has no State member, so a workgroup
// created DISABLED is created and then disabled.

type athenaWorkGroupHandler struct{}

var athenaWorkGroupProperties = []string{"Name", "Description", "State", "Tags", "WorkGroupConfiguration", "RecursiveDeleteOption"}

func (h *athenaWorkGroupHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenAthena)
	}
	// The schema's property is WorkGroupConfiguration; Configuration is the
	// CreateWorkGroup member it maps onto.
	body := map[string]any{"Name": name}
	forwardPropertiesAs(props, body, map[string]string{"Description": "Description"})
	cfg, err := athenaCoerceConfiguration(athenaConfiguration(props))
	if err != nil {
		return "", nil, err
	}
	if cfg != nil {
		body["Configuration"] = cfg
	}
	athenaTagsBody(body, rCtx, props)
	noteUnconsumedProperties(ctx, "AWS::Athena::WorkGroup", props, athenaWorkGroupProperties...)
	if _, err := athenaCall(ctx, router, rCtx.Region, "CreateWorkGroup", body); err != nil {
		return "", nil, err
	}
	if state, _ := props["State"].(string); state != "" {
		if _, err := athenaCall(ctx, router, rCtx.Region, "UpdateWorkGroup", map[string]any{"WorkGroup": name, "State": state}); err != nil {
			return "", nil, err
		}
	}
	attrs, err := athenaWorkGroupAttrs(ctx, router, rCtx.Region, name)
	return name, attrs, err
}

func (h *athenaWorkGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if athenaPropertiesChanged(props, oldProps, "Name") {
		return "", nil, errReplacementRequired
	}
	description, _ := props["Description"].(string)
	state, _ := props["State"].(string)
	if state == "" {
		state = "ENABLED"
	}
	body := map[string]any{"WorkGroup": physicalID, "Description": description, "State": state}
	next, err := athenaCoerceConfiguration(athenaConfiguration(props))
	if err != nil {
		return "", nil, failUpdate(err)
	}
	prev, err := athenaCoerceConfiguration(athenaConfiguration(oldProps))
	if err != nil {
		return "", nil, failUpdate(err)
	}
	if updates := athenaConfigurationUpdates(next, prev); len(updates) > 0 {
		body["ConfigurationUpdates"] = updates
	}
	if _, err := athenaCall(ctx, router, rCtx.Region, "UpdateWorkGroup", body); err != nil {
		return "", nil, failUpdate(err)
	}
	if err := athenaReconcileTags(ctx, router, rCtx, athenaARN(rCtx, "workgroup", physicalID), props, oldProps); err != nil {
		return "", nil, failUpdate(err)
	}
	attrs, err := athenaWorkGroupAttrs(ctx, router, rCtx.Region, physicalID)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	return physicalID, attrs, nil
}

// athenaConfiguration is a template's WorkGroupConfiguration, nil when unset.
func athenaConfiguration(props map[string]any) map[string]any {
	cfg, _ := props["WorkGroupConfiguration"].(map[string]any)
	return cfg
}

// DeleteWithProperties passes RecursiveDeleteOption through, so a workgroup
// holding named queries is only removed when the template allows it.
func (h *athenaWorkGroupHandler) DeleteWithProperties(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	body := map[string]any{"WorkGroup": physicalID}
	if recursive, err := cfnBool(props["RecursiveDeleteOption"]); err == nil {
		body["RecursiveDeleteOption"] = recursive
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonAthena.DeleteWorkGroup", body)
	return teardownError("DeleteWorkGroup", rec, err)
}

func (h *athenaWorkGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	return h.DeleteWithProperties(ctx, router, cfg, physicalID, nil, rCtx)
}

// athenaWorkGroupAttrs reads back what Fn::GetAtt exposes: CreationTime as
// whole seconds, and the effective engine version under both of the names
// the schema gives it.
func athenaWorkGroupAttrs(ctx context.Context, router http.Handler, region, name string) (map[string]string, error) {
	rec, err := athenaCall(ctx, router, region, "GetWorkGroup", map[string]any{"WorkGroup": name})
	if err != nil {
		return nil, err
	}
	var out struct {
		WorkGroup struct {
			CreationTime  float64
			Configuration struct {
				EngineVersion struct{ EffectiveEngineVersion string }
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("GetWorkGroup: decode: %w", err)
	}
	engine := out.WorkGroup.Configuration.EngineVersion.EffectiveEngineVersion
	return map[string]string{
		"Name":         name,
		"CreationTime": strconv.FormatInt(int64(out.WorkGroup.CreationTime), 10),
		"WorkGroupConfiguration.EngineVersion.EffectiveEngineVersion":        engine,
		"WorkGroupConfigurationUpdates.EngineVersion.EffectiveEngineVersion": engine,
	}, nil
}

// ── AWS::Athena::NamedQuery ─────────────────────────────────────────────────
//
// https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-athena-namedquery.html
//
// Every property requires replacement, so the handler has no Update. Name is
// optional in the template and required by the API, so an unnamed query gets
// a generated name. The physical ID is the NamedQueryId.

type athenaNamedQueryHandler struct{}

var athenaNamedQueryProperties = []string{"Name", "Database", "Description", "QueryString", "WorkGroup"}

func (h *athenaNamedQueryHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	forwardPropertiesAs(props, body, identityNames(athenaNamedQueryProperties))
	if name, _ := props["Name"].(string); name == "" {
		body["Name"] = rCtx.generatedNameWithin(maxNameLenAthena)
	}
	rec, err := athenaCall(ctx, router, rCtx.Region, "CreateNamedQuery", body)
	if err != nil {
		return "", nil, err
	}
	var out struct{ NamedQueryId string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.NamedQueryId == "" {
		return "", nil, fmt.Errorf("CreateNamedQuery: no NamedQueryId in %s", rec.Body.String())
	}
	noteUnconsumedProperties(ctx, "AWS::Athena::NamedQuery", props, athenaNamedQueryProperties...)
	return out.NamedQueryId, map[string]string{"NamedQueryId": out.NamedQueryId}, nil
}

func (h *athenaNamedQueryHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonAthena.DeleteNamedQuery", map[string]any{"NamedQueryId": physicalID})
	return teardownError("DeleteNamedQuery", rec, err)
}

// identityNames maps each name to itself, for forwardPropertiesAs where the
// API member is spelled as the property.
func identityNames(names []string) map[string]string {
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = n
	}
	return out
}

// ── AWS::Athena::PreparedStatement ──────────────────────────────────────────
//
// https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-athena-preparedstatement.html
//
// StatementName and WorkGroup require replacement; QueryStatement and
// Description update in place. A statement is identified by both, so the
// physical ID is "<workgroup>/<name>" — a workgroup name cannot hold a slash
// — while Ref returns the statement name, as the schema says.

type athenaPreparedStatementHandler struct{}

var athenaPreparedStatementProperties = []string{"StatementName", "WorkGroup", "QueryStatement", "Description"}

func (h *athenaPreparedStatementHandler) put(ctx context.Context, router http.Handler, operation string, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	forwardPropertiesAs(props, body, identityNames(athenaPreparedStatementProperties))
	if _, err := athenaCall(ctx, router, rCtx.Region, operation, body); err != nil {
		return "", nil, err
	}
	workGroup, _ := props["WorkGroup"].(string)
	name, _ := props["StatementName"].(string)
	return workGroup + "/" + name, map[string]string{"Ref": name}, nil
}

func (h *athenaPreparedStatementHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	noteUnconsumedProperties(ctx, "AWS::Athena::PreparedStatement", props, athenaPreparedStatementProperties...)
	return h.put(ctx, router, "CreatePreparedStatement", props, rCtx)
}

func (h *athenaPreparedStatementHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, _ string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if athenaPropertiesChanged(props, oldProps, "StatementName", "WorkGroup") {
		return "", nil, errReplacementRequired
	}
	id, attrs, err := h.put(ctx, router, "UpdatePreparedStatement", props, rCtx)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	return id, attrs, nil
}

func (h *athenaPreparedStatementHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	workGroup, name, ok := strings.Cut(physicalID, "/")
	if !ok {
		return nil
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonAthena.DeletePreparedStatement", map[string]any{"WorkGroup": workGroup, "StatementName": name})
	return teardownError("DeletePreparedStatement", rec, err)
}

// ── AWS::Athena::DataCatalog ────────────────────────────────────────────────
//
// https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-athena-datacatalog.html
//
// Name requires replacement; Type, Description and Parameters update in place
// through UpdateDataCatalog, and Tags through the tag operations. Status,
// Error and ConnectionType describe a FEDERATED catalog's provisioning, which
// Overcast does not emulate, so they are reported as not applied.

type athenaDataCatalogHandler struct{}

var athenaDataCatalogDefinition = []string{"Name", "Type", "Description"}

// athenaDataCatalogBody is a DataCatalog's create or update request, its
// Parameters stringified as the API's string map requires.
func athenaDataCatalogBody(props map[string]any) map[string]any {
	body := map[string]any{}
	forwardPropertiesAs(props, body, identityNames(athenaDataCatalogDefinition))
	if params := cfnStringMap(props["Parameters"]); params != nil {
		body["Parameters"] = params
	}
	return body
}

func (h *athenaDataCatalogHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := athenaDataCatalogBody(props)
	athenaTagsBody(body, rCtx, props)
	noteUnconsumedProperties(ctx, "AWS::Athena::DataCatalog", props, "Name", "Type", "Description", "Parameters", "Tags")
	if _, err := athenaCall(ctx, router, rCtx.Region, "CreateDataCatalog", body); err != nil {
		return "", nil, err
	}
	name, _ := props["Name"].(string)
	return name, map[string]string{}, nil
}

func (h *athenaDataCatalogHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if athenaPropertiesChanged(props, oldProps, "Name") {
		return "", nil, errReplacementRequired
	}
	if _, err := athenaCall(ctx, router, rCtx.Region, "UpdateDataCatalog", athenaDataCatalogBody(props)); err != nil {
		return "", nil, failUpdate(err)
	}
	if err := athenaReconcileTags(ctx, router, rCtx, athenaARN(rCtx, "datacatalog", physicalID), props, oldProps); err != nil {
		return "", nil, failUpdate(err)
	}
	return physicalID, map[string]string{}, nil
}

func (h *athenaDataCatalogHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonAthena.DeleteDataCatalog", map[string]any{"Name": physicalID})
	return teardownError("DeleteDataCatalog", rec, err)
}
