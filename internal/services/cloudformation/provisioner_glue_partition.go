package cloudformation

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/overcast-sh/overcast/internal/config"
)

// ── AWS::Glue::Partition ────────────────────────────────────────────────────
//
// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-glue-partition.html
//
// PartitionInput has the Glue API's PartitionInput shape, so it is forwarded
// as-is apart from Values, which are strings in the API but arrive as numbers
// from a template that writes `Values: [2020]` unquoted, as CloudFormation
// itself accepts. CatalogId, DatabaseName and TableName require replacement;
// PartitionInput updates in place through UpdatePartition, which also moves
// the partition when its Values change.
//
// The physical ID names the partition: database, table and each value,
// path-escaped and slash-joined, so Delete and Update can recover the values
// whatever characters they hold.

type gluePartitionHandler struct{}

// gluePartitionProperties are every AWS::Glue::Partition property.
var gluePartitionProperties = []string{"CatalogId", "DatabaseName", "TableName", "PartitionInput"}

func gluePartitionBody(props map[string]any) map[string]any {
	input := props["PartitionInput"]
	if in, ok := input.(map[string]any); ok {
		// props belongs to the resolved template; copy rather than mutate.
		normalised := maps.Clone(in)
		if _, has := in["Values"]; has {
			normalised["Values"] = gluePartitionValues(props)
		}
		input = normalised
	}
	body := map[string]any{
		"DatabaseName":   props["DatabaseName"],
		"TableName":      props["TableName"],
		"PartitionInput": input,
	}
	if catalogID, _ := props["CatalogId"].(string); catalogID != "" {
		body["CatalogId"] = catalogID
	}
	return body
}

func gluePartitionValues(props map[string]any) []string {
	input, _ := props["PartitionInput"].(map[string]any)
	raw, _ := input["Values"].([]any)
	values := make([]string, 0, len(raw))
	for _, v := range raw {
		// Spelled as written: 2020, not 2020.000000 or 2.02e+03.
		values = append(values, cfnScalarString(v))
	}
	return values
}

func gluePartitionPhysicalID(props map[string]any) string {
	db, _ := props["DatabaseName"].(string)
	table, _ := props["TableName"].(string)
	parts := []string{url.PathEscape(db), url.PathEscape(table)}
	for _, v := range gluePartitionValues(props) {
		parts = append(parts, url.PathEscape(v))
	}
	return strings.Join(parts, "/")
}

// parseGluePartitionPhysicalID reverses gluePartitionPhysicalID.
func parseGluePartitionPhysicalID(physicalID string) (db, table string, values []string, ok bool) {
	parts := strings.Split(physicalID, "/")
	if len(parts) < 3 {
		return "", "", nil, false
	}
	decoded := make([]string, len(parts))
	for i, p := range parts {
		s, err := url.PathUnescape(p)
		if err != nil {
			return "", "", nil, false
		}
		decoded[i] = s
	}
	return decoded[0], decoded[1], decoded[2:], true
}

func (h *gluePartitionHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if _, err := internalJSON(ctx, router, rCtx.Region, "AWSGlue.CreatePartition", gluePartitionBody(props)); err != nil {
		return "", nil, fmt.Errorf("CreatePartition: %w", err)
	}
	noteUnconsumedProperties(ctx, "AWS::Glue::Partition", props, gluePartitionProperties...)
	return gluePartitionPhysicalID(props), map[string]string{}, nil
}

func (h *gluePartitionHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	db, table, values, ok := parseGluePartitionPhysicalID(physicalID)
	if !ok {
		return nil
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSGlue.DeletePartition", map[string]any{
		"DatabaseName": db, "TableName": table, "PartitionValues": values,
	})
	return teardownError("DeletePartition", rec, err)
}

func (h *gluePartitionHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, _ string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, name := range []string{"CatalogId", "DatabaseName", "TableName"} {
		if cfnScalarString(props[name]) != cfnScalarString(oldProps[name]) {
			return "", nil, errReplacementRequired
		}
	}
	// The partition to update is the one oldProps describes, not the one
	// physicalID names: on a rollback the provisioner passes the previous
	// physical ID while the partition already lives at the new values.
	body := gluePartitionBody(props)
	body["PartitionValueList"] = gluePartitionValues(oldProps)
	if _, err := internalJSON(ctx, router, rCtx.Region, "AWSGlue.UpdatePartition", body); err != nil {
		return "", nil, fmt.Errorf("UpdatePartition: %w", err)
	}
	return gluePartitionPhysicalID(props), map[string]string{}, nil
}
