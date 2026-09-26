package cloudformation

// AWS::S3Tables::* — thin handlers over the S3 Tables REST-JSON API.
//
// Every call is signed for "s3tables": the S3 Tables roots (/buckets,
// /tables, …) are also legal S3 bucket names, and the router hands a request
// to S3 Tables only when its credential scope says so.
//
// Ref follows each type's registry primaryIdentifier, which is what
// CloudFormation returns: the table bucket ARN, "<bucket ARN>|<namespace>"
// for a namespace, the table ARN, and the ARN each policy is attached to. (The
// reference pages' prose says "name" for the bucket, namespace and table; the
// published resource schemas identify them by ARN.)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/overcast-sh/overcast/internal/config"
)

const (
	cfnS3TablesTableBucket       = "AWS::S3Tables::TableBucket"
	cfnS3TablesTable             = "AWS::S3Tables::Table"
	cfnS3TablesNamespace         = "AWS::S3Tables::Namespace"
	cfnS3TablesTableBucketPolicy = "AWS::S3Tables::TableBucketPolicy"
	cfnS3TablesTablePolicy       = "AWS::S3Tables::TablePolicy"
)

// s3tablesCall dispatches one S3 Tables operation and decodes its response
// into out when out is non-nil.
func s3tablesCall(ctx context.Context, router http.Handler, region, method, path, op string, body, out any) error {
	return signedRESTJSON(ctx, router, "s3tables", region, method, path, op, body, out)
}

func s3tablesRequest(ctx context.Context, router http.Handler, region, method, path, contentType string, body []byte) (*httptest.ResponseRecorder, error) {
	return restCall("s3tables", region, method, path, contentType, body, scopedAuthHeader("s3tables", region)).do(ctx, router)
}

func s3tablesBucketPath(bucketARN string, rest ...string) string {
	p := "/buckets/" + url.PathEscape(bucketARN)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

func s3tablesTablePath(bucketARN, namespace, name string, rest ...string) string {
	p := "/tables/" + url.PathEscape(bucketARN) + "/" + url.PathEscape(namespace) + "/" + url.PathEscape(name)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// cfnLowerStatus maps CloudFormation's "Enabled"/"Disabled" onto the API's
// lower-case MaintenanceStatus.
func cfnLowerStatus(v any) string {
	s, _ := v.(string)
	return strings.ToLower(s)
}

func cfnObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// s3tablesPropChanged reports whether property name differs between props
// and old, compared as JSON. On create (old == nil) a set property counts as
// changed.
func s3tablesPropChanged(props, old map[string]any, name string) bool {
	if old == nil {
		return props[name] != nil
	}
	a, _ := json.Marshal(props[name])
	b, _ := json.Marshal(old[name])
	return string(a) != string(b)
}

// s3tablesEncryption maps EncryptionConfiguration {SSEAlgorithm, KMSKeyArn}.
func s3tablesEncryption(v any) map[string]any {
	m := cfnObject(v)
	if m == nil {
		return nil
	}
	out := map[string]any{"sseAlgorithm": m["SSEAlgorithm"]}
	if k, ok := m["KMSKeyArn"]; ok && k != nil {
		out["kmsKeyArn"] = k
	}
	return out
}

func s3tablesStorageClass(v any) map[string]any {
	m := cfnObject(v)
	if m == nil {
		return nil
	}
	return map[string]any{"storageClass": m["StorageClass"]}
}

// ── Table bucket ─────────────────────────────────────────────────────────────

type s3tablesTableBucketHandler struct{}

func (h *s3tablesTableBucketHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{"name": props["TableBucketName"]}
	if enc := s3tablesEncryption(props["EncryptionConfiguration"]); enc != nil {
		body["encryptionConfiguration"] = enc
	}
	if sc := s3tablesStorageClass(props["StorageClassConfiguration"]); sc != nil {
		body["storageClassConfiguration"] = sc
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	var out struct {
		ARN string `json:"arn"`
	}
	if err := s3tablesCall(ctx, router, rCtx.Region, http.MethodPut, "/buckets", "CreateTableBucket", body, &out); err != nil {
		return "", nil, err
	}
	if err := h.applyConfiguration(ctx, router, rCtx.Region, out.ARN, props, nil); err != nil {
		// No physical ID goes back with an error, so rollback cannot see the
		// bucket; remove it here or the next deploy meets "already exists".
		s3tablesRequest(ctx, router, rCtx.Region, http.MethodDelete, s3tablesBucketPath(out.ARN), "", nil) //nolint:errcheck
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, cfnS3TablesTableBucket, props, "TableBucketName", "EncryptionConfiguration",
		"StorageClassConfiguration", "Tags", "UnreferencedFileRemoval", "MetricsConfiguration", "ReplicationConfiguration")
	return out.ARN, map[string]string{"TableBucketARN": out.ARN}, nil
}

// applyConfiguration sends the properties the create call does not carry, each
// through its own operation, skipping any that did not change since old (nil
// on create).
func (h *s3tablesTableBucketHandler) applyConfiguration(ctx context.Context, router http.Handler, region, arn string, props, old map[string]any) error {
	if s3tablesPropChanged(props, old, "UnreferencedFileRemoval") {
		if err := s3tablesCall(ctx, router, region, http.MethodPut, s3tablesBucketPath(arn, "maintenance", "icebergUnreferencedFileRemoval"),
			"PutTableBucketMaintenanceConfiguration", map[string]any{"value": s3tablesUnreferencedFileRemoval(props["UnreferencedFileRemoval"])}, nil); err != nil {
			return err
		}
	}
	if s3tablesPropChanged(props, old, "MetricsConfiguration") {
		method, op := http.MethodDelete, "DeleteTableBucketMetricsConfiguration"
		if cfnLowerStatus(cfnObject(props["MetricsConfiguration"])["Status"]) == "enabled" {
			method, op = http.MethodPut, "PutTableBucketMetricsConfiguration"
		}
		if err := s3tablesCall(ctx, router, region, method, s3tablesBucketPath(arn, "metrics"), op, nil, nil); err != nil {
			return err
		}
	}
	if s3tablesPropChanged(props, old, "ReplicationConfiguration") {
		path := "/table-bucket-replication?tableBucketARN=" + url.QueryEscape(arn)
		if repl := cfnObject(props["ReplicationConfiguration"]); repl != nil {
			return s3tablesCall(ctx, router, region, http.MethodPut, path, "PutTableBucketReplication",
				map[string]any{"configuration": convertCFKeysToAPI(repl)}, nil)
		}
		return s3tablesCall(ctx, router, region, http.MethodDelete, path, "DeleteTableBucketReplication", nil, nil)
	}
	return nil
}

// s3tablesUnreferencedFileRemoval maps UnreferencedFileRemoval onto the
// icebergUnreferencedFileRemoval maintenance value. A property removed from
// the template (nil) goes back to AWS's default: enabled, 3 and 10 days.
func s3tablesUnreferencedFileRemoval(v any) map[string]any {
	ufr := cfnObject(v)
	if ufr == nil {
		return map[string]any{"status": "enabled", "settings": map[string]any{"icebergUnreferencedFileRemoval": map[string]any{
			"unreferencedDays": 3, "nonCurrentDays": 10,
		}}}
	}
	settings := map[string]any{}
	if v, ok := ufr["UnreferencedDays"]; ok {
		settings["unreferencedDays"] = v
	}
	if v, ok := ufr["NoncurrentDays"]; ok {
		settings["nonCurrentDays"] = v
	}
	value := map[string]any{"settings": map[string]any{"icebergUnreferencedFileRemoval": settings}}
	if s := cfnLowerStatus(ufr["Status"]); s != "" {
		value["status"] = s
	}
	return value
}

func (h *s3tablesTableBucketHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if fmt.Sprint(props["TableBucketName"]) != fmt.Sprint(oldProps["TableBucketName"]) {
		return "", nil, errReplacementRequired
	}
	region := rCtx.Region
	if enc := s3tablesEncryption(props["EncryptionConfiguration"]); enc != nil {
		if err := s3tablesCall(ctx, router, region, http.MethodPut, s3tablesBucketPath(physicalID, "encryption"),
			"PutTableBucketEncryption", map[string]any{"encryptionConfiguration": enc}, nil); err != nil {
			return "", nil, err
		}
	}
	if props["EncryptionConfiguration"] == nil && oldProps["EncryptionConfiguration"] != nil {
		if err := s3tablesCall(ctx, router, region, http.MethodDelete, s3tablesBucketPath(physicalID, "encryption"),
			"DeleteTableBucketEncryption", nil, nil); err != nil {
			return "", nil, err
		}
	}
	sc := s3tablesStorageClass(props["StorageClassConfiguration"])
	if sc == nil && oldProps["StorageClassConfiguration"] != nil {
		// Removed from the template: back to the default class.
		sc = map[string]any{"storageClass": "STANDARD"}
	}
	if sc != nil {
		if err := s3tablesCall(ctx, router, region, http.MethodPut, s3tablesBucketPath(physicalID, "storage-class"),
			"PutTableBucketStorageClass", map[string]any{"storageClassConfiguration": sc}, nil); err != nil {
			return "", nil, err
		}
	}
	if err := h.applyConfiguration(ctx, router, region, physicalID, props, oldProps); err != nil {
		return "", nil, err
	}
	if err := reconcileS3TablesTags(ctx, router, region, physicalID, rCtx.StackTags, rCtx.PreviousStackTags, props["Tags"], oldProps["Tags"]); err != nil {
		return "", nil, err
	}
	return physicalID, map[string]string{"TableBucketARN": physicalID}, nil
}

func (h *s3tablesTableBucketHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := s3tablesRequest(ctx, router, rCtx.Region, http.MethodDelete, s3tablesBucketPath(physicalID), "", nil)
	return teardownError("DeleteTableBucket", rec, err)
}

// reconcileS3TablesTags applies the difference between the effective tags
// before and after an update through TagResource and UntagResource.
func reconcileS3TablesTags(ctx context.Context, router http.Handler, region, arn string, stackTags, priorStackTags []Tag, rawTags, rawPrior any) error {
	added, removed := tagDelta(mergeResourceTags(stackTags, rawTags), mergeResourceTags(priorStackTags, rawPrior))
	path := "/tag/" + url.PathEscape(arn)
	if len(added) > 0 {
		if err := s3tablesCall(ctx, router, region, http.MethodPost, path, "TagResource", map[string]any{"tags": added}, nil); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		q := url.Values{"tagKeys": removed}
		if err := s3tablesCall(ctx, router, region, http.MethodDelete, path+"?"+q.Encode(), "UntagResource", nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// ── Namespace ────────────────────────────────────────────────────────────────

type s3tablesNamespaceHandler struct{}

func (h *s3tablesNamespaceHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	arn, _ := props["TableBucketARN"].(string)
	ns, _ := props["Namespace"].(string)
	if err := s3tablesCall(ctx, router, rCtx.Region, http.MethodPut, "/namespaces/"+url.PathEscape(arn),
		"CreateNamespace", map[string]any{"namespace": []string{ns}}, nil); err != nil {
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, cfnS3TablesNamespace, props, "TableBucketARN", "Namespace")
	return arn + "|" + ns, nil, nil
}

func (h *s3tablesNamespaceHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	arn, ns, ok := strings.Cut(physicalID, "|")
	if !ok {
		return nil
	}
	rec, err := s3tablesRequest(ctx, router, rCtx.Region, http.MethodDelete, "/namespaces/"+url.PathEscape(arn)+"/"+url.PathEscape(ns), "", nil)
	return teardownError("DeleteNamespace", rec, err)
}

// ── Table ────────────────────────────────────────────────────────────────────

type s3tablesTableHandler struct{}

// s3tablesIcebergMetadata maps IcebergMetadata onto the API's metadata.iceberg,
// whose Iceberg members carry hyphenated JSON names (source-id, field-id, …)
// rather than the lowerCamel convertCFKeysToAPI would produce.
func s3tablesIcebergMetadata(v any) map[string]any {
	m := cfnObject(v)
	if m == nil {
		return nil
	}
	out := map[string]any{}
	if schema := cfnObject(m["IcebergSchema"]); schema != nil {
		out["schema"] = map[string]any{"fields": cfnRenamedList(schema["SchemaFieldList"], s3tablesSchemaFieldNames)}
	}
	if v2 := cfnObject(m["IcebergSchemaV2"]); v2 != nil {
		schema := cfnRenamed(v2, s3tablesSchemaV2Names)
		schema["fields"] = cfnRenamedList(v2["SchemaV2FieldList"], s3tablesSchemaV2FieldNames)
		out["schemaV2"] = schema
	}
	if spec := cfnObject(m["IcebergPartitionSpec"]); spec != nil {
		ps := cfnRenamed(spec, map[string]string{"SpecId": "spec-id"})
		ps["fields"] = cfnRenamedList(spec["Fields"], s3tablesPartitionFieldNames)
		out["partitionSpec"] = ps
	}
	if order := cfnObject(m["IcebergSortOrder"]); order != nil {
		wo := cfnRenamed(order, map[string]string{"OrderId": "order-id"})
		wo["fields"] = cfnRenamedList(order["Fields"], s3tablesSortFieldNames)
		out["writeOrder"] = wo
	}
	if p, ok := m["TableProperties"]; ok && p != nil {
		out["properties"] = p
	}
	return out
}

// CloudFormation's property names for the Iceberg shapes, mapped onto the
// API's JSON names. A SchemaV2Field's Type is passed through untouched: a
// primitive's name, or a nested type written, as the resource reference asks,
// in the Iceberg spec's own lower-case JSON.
var (
	s3tablesSchemaFieldNames    = map[string]string{"Id": "id", "Name": "name", "Type": "type", "Required": "required"}
	s3tablesSchemaV2Names       = map[string]string{"SchemaV2FieldType": "type", "SchemaId": "schema-id", "IdentifierFieldIds": "identifier-field-ids"}
	s3tablesSchemaV2FieldNames  = map[string]string{"Id": "id", "Name": "name", "Type": "type", "Required": "required", "Doc": "doc"}
	s3tablesPartitionFieldNames = map[string]string{"SourceId": "source-id", "FieldId": "field-id", "Name": "name", "Transform": "transform"}
	s3tablesSortFieldNames      = map[string]string{"SourceId": "source-id", "Transform": "transform", "Direction": "direction", "NullOrder": "null-order"}
)

// cfnRenamed is the members of m that names lists, under their API names.
func cfnRenamed(m map[string]any, names map[string]string) map[string]any {
	out := make(map[string]any, len(names))
	forwardPropertiesAs(m, out, names)
	return out
}

// cfnRenamedList is cfnRenamed over each object of a CloudFormation list.
func cfnRenamedList(v any, names map[string]string) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, item := range list {
		out = append(out, cfnRenamed(cfnObject(item), names))
	}
	return out
}

// tableMaintenance maps Compaction and SnapshotManagement onto
// PutTableMaintenanceConfiguration values, keyed by maintenance type.
func s3tablesTableMaintenance(props map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	if c := cfnObject(props["Compaction"]); c != nil {
		settings := map[string]any{}
		if v, ok := c["TargetFileSizeMB"]; ok {
			settings["targetFileSizeMB"] = v
		}
		out["icebergCompaction"] = map[string]any{"status": cfnLowerStatus(c["Status"]), "settings": map[string]any{"icebergCompaction": settings}}
	}
	if s := cfnObject(props["SnapshotManagement"]); s != nil {
		settings := map[string]any{}
		if v, ok := s["MinSnapshotsToKeep"]; ok {
			settings["minSnapshotsToKeep"] = v
		}
		if v, ok := s["MaxSnapshotAgeHours"]; ok {
			settings["maxSnapshotAgeHours"] = v
		}
		out["icebergSnapshotManagement"] = map[string]any{"status": cfnLowerStatus(s["Status"]), "settings": map[string]any{"icebergSnapshotManagement": settings}}
	}
	for _, v := range out {
		if v["status"] == "" {
			delete(v, "status")
		}
	}
	return out
}

// s3tablesTableMaintenanceUpdate is s3tablesTableMaintenance for an update: a
// Compaction or SnapshotManagement property the template dropped goes back to
// AWS's default (enabled, 512 MB files; at least one snapshot for 120 hours).
func s3tablesTableMaintenanceUpdate(props, oldProps map[string]any) map[string]map[string]any {
	out := s3tablesTableMaintenance(props)
	if props["Compaction"] == nil && oldProps["Compaction"] != nil {
		out["icebergCompaction"] = map[string]any{"status": "enabled", "settings": map[string]any{
			"icebergCompaction": map[string]any{"targetFileSizeMB": 512, "strategy": "auto"},
		}}
	}
	if props["SnapshotManagement"] == nil && oldProps["SnapshotManagement"] != nil {
		out["icebergSnapshotManagement"] = map[string]any{"status": "enabled", "settings": map[string]any{
			"icebergSnapshotManagement": map[string]any{"minSnapshotsToKeep": 1, "maxSnapshotAgeHours": 120},
		}}
	}
	return out
}

func (h *s3tablesTableHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	bucketARN, _ := props["TableBucketARN"].(string)
	ns, _ := props["Namespace"].(string)
	name, _ := props["TableName"].(string)
	body := map[string]any{"name": name, "format": props["OpenTableFormat"]}
	if meta := s3tablesIcebergMetadata(props["IcebergMetadata"]); meta != nil {
		body["metadata"] = map[string]any{"iceberg": meta}
	}
	if sc := s3tablesStorageClass(props["StorageClassConfiguration"]); sc != nil {
		body["storageClassConfiguration"] = sc
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	var created struct {
		TableARN string `json:"tableARN"`
	}
	if err := s3tablesCall(ctx, router, rCtx.Region, http.MethodPut, "/tables/"+url.PathEscape(bucketARN)+"/"+url.PathEscape(ns),
		"CreateTable", body, &created); err != nil {
		return "", nil, err
	}
	for typ, value := range s3tablesTableMaintenance(props) {
		if err := s3tablesCall(ctx, router, rCtx.Region, http.MethodPut, s3tablesTablePath(bucketARN, ns, name, "maintenance", typ),
			"PutTableMaintenanceConfiguration", map[string]any{"value": value}, nil); err != nil {
			// As for the bucket: rollback cannot see a table it has no ID for.
			s3tablesRequest(ctx, router, rCtx.Region, http.MethodDelete, s3tablesTablePath(bucketARN, ns, name), "", nil) //nolint:errcheck
			return "", nil, err
		}
	}
	// WithoutMetadata ("Yes") is the absence of IcebergMetadata, which is
	// what the create call above already sent.
	noteUnconsumedProperties(ctx, cfnS3TablesTable, props, "TableBucketARN", "Namespace", "TableName", "OpenTableFormat",
		"IcebergMetadata", "WithoutMetadata", "StorageClassConfiguration", "Tags", "Compaction", "SnapshotManagement")
	attrs, err := s3tablesTableAttrs(ctx, router, rCtx.Region, created.TableARN)
	if err != nil {
		return "", nil, err
	}
	return created.TableARN, attrs, nil
}

type s3tablesTableInfo struct {
	Name              string   `json:"name"`
	Namespace         []string `json:"namespace"`
	TableARN          string   `json:"tableARN"`
	VersionToken      string   `json:"versionToken"`
	WarehouseLocation string   `json:"warehouseLocation"`
}

func s3tablesGetTable(ctx context.Context, router http.Handler, region, tableARN string) (*s3tablesTableInfo, error) {
	info, _, err := s3tablesLookupTable(ctx, router, region, tableARN)
	return info, err
}

// s3tablesLookupTable is GetTable by ARN, also returning the dispatch record so
// a teardown can tell "already gone" from a failure.
func s3tablesLookupTable(ctx context.Context, router http.Handler, region, tableARN string) (*s3tablesTableInfo, *httptest.ResponseRecorder, error) {
	rec, err := s3tablesRequest(ctx, router, region, http.MethodGet, "/get-table?tableArn="+url.QueryEscape(tableARN), "", nil)
	if err != nil {
		return nil, rec, fmt.Errorf("GetTable: %w", err)
	}
	var info s3tablesTableInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		return nil, rec, fmt.Errorf("GetTable: parse response: %w", err)
	}
	if len(info.Namespace) == 0 {
		return nil, rec, fmt.Errorf("GetTable: table %s has no namespace", tableARN)
	}
	return &info, rec, nil
}

// s3tablesBucketOf is the table bucket ARN a table ARN sits under.
func s3tablesBucketOf(tableARN string) string {
	bucket, _, _ := strings.Cut(tableARN, "/table/")
	return bucket
}

func s3tablesTableAttrs(ctx context.Context, router http.Handler, region, tableARN string) (map[string]string, error) {
	info, err := s3tablesGetTable(ctx, router, region, tableARN)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"TableARN":          info.TableARN,
		"VersionToken":      info.VersionToken,
		"WarehouseLocation": info.WarehouseLocation,
	}, nil
}

// Update renames the table in place for a Namespace or TableName change (both
// "No interruption" on AWS), re-applies maintenance, and reconciles tags. The
// create-only properties force a replacement.
func (h *s3tablesTableHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, name := range []string{"TableBucketARN", "OpenTableFormat", "IcebergMetadata", "WithoutMetadata", "StorageClassConfiguration"} {
		if s3tablesPropChanged(props, oldProps, name) {
			return "", nil, errReplacementRequired
		}
	}
	region := rCtx.Region
	info, err := s3tablesGetTable(ctx, router, region, physicalID)
	if err != nil {
		return "", nil, err
	}
	bucketARN := s3tablesBucketOf(physicalID)
	ns, _ := props["Namespace"].(string)
	name, _ := props["TableName"].(string)
	if ns != info.Namespace[0] || name != info.Name {
		body := map[string]any{"newNamespaceName": ns, "newName": name}
		if err := s3tablesCall(ctx, router, region, http.MethodPut, s3tablesTablePath(bucketARN, info.Namespace[0], info.Name, "rename"),
			"RenameTable", body, nil); err != nil {
			return "", nil, err
		}
	}
	for typ, value := range s3tablesTableMaintenanceUpdate(props, oldProps) {
		if err := s3tablesCall(ctx, router, region, http.MethodPut, s3tablesTablePath(bucketARN, ns, name, "maintenance", typ),
			"PutTableMaintenanceConfiguration", map[string]any{"value": value}, nil); err != nil {
			return "", nil, err
		}
	}
	if err := reconcileS3TablesTags(ctx, router, region, physicalID, rCtx.StackTags, rCtx.PreviousStackTags, props["Tags"], oldProps["Tags"]); err != nil {
		return "", nil, err
	}
	attrs, err := s3tablesTableAttrs(ctx, router, region, physicalID)
	if err != nil {
		return "", nil, err
	}
	return physicalID, attrs, nil
}

func (h *s3tablesTableHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	info, rec, err := s3tablesLookupTable(ctx, router, rCtx.Region, physicalID)
	if err != nil {
		// A table that is already gone is a completed teardown.
		return teardownError("GetTable", rec, err)
	}
	rec, err = s3tablesRequest(ctx, router, rCtx.Region, http.MethodDelete,
		s3tablesTablePath(s3tablesBucketOf(physicalID), info.Namespace[0], info.Name), "", nil)
	return teardownError("DeleteTable", rec, err)
}

// ── Policies ─────────────────────────────────────────────────────────────────

// s3tablesPolicyDocument renders ResourcePolicy, a Json property, as the
// string the API models.
func s3tablesPolicyDocument(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("ResourcePolicy: %w", err)
	}
	return string(data), nil
}

type s3tablesTableBucketPolicyHandler struct{}

func (h *s3tablesTableBucketPolicyHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	arn, _ := props["TableBucketARN"].(string)
	if err := h.put(ctx, router, rCtx.Region, arn, props); err != nil {
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, cfnS3TablesTableBucketPolicy, props, "TableBucketARN", "ResourcePolicy")
	return arn, nil, nil
}

func (h *s3tablesTableBucketPolicyHandler) put(ctx context.Context, router http.Handler, region, arn string, props map[string]any) error {
	doc, err := s3tablesPolicyDocument(props["ResourcePolicy"])
	if err != nil {
		return err
	}
	return s3tablesCall(ctx, router, region, http.MethodPut, s3tablesBucketPath(arn, "policy"),
		"PutTableBucketPolicy", map[string]any{"resourcePolicy": doc}, nil)
}

func (h *s3tablesTableBucketPolicyHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if fmt.Sprint(props["TableBucketARN"]) != fmt.Sprint(oldProps["TableBucketARN"]) {
		return "", nil, errReplacementRequired
	}
	return physicalID, nil, h.put(ctx, router, rCtx.Region, physicalID, props)
}

func (h *s3tablesTableBucketPolicyHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := s3tablesRequest(ctx, router, rCtx.Region, http.MethodDelete, s3tablesBucketPath(physicalID, "policy"), "", nil)
	return teardownError("DeleteTableBucketPolicy", rec, err)
}

type s3tablesTablePolicyHandler struct{}

func (h *s3tablesTablePolicyHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	arn, _ := props["TableARN"].(string)
	attrs, err := h.put(ctx, router, rCtx.Region, arn, props)
	if err != nil {
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, cfnS3TablesTablePolicy, props, "TableARN", "ResourcePolicy")
	return arn, attrs, nil
}

// put applies the policy to the table the ARN names and returns the
// attributes CloudFormation exposes: the table's bucket ARN, namespace and
// name, resolved at the time of the put.
func (h *s3tablesTablePolicyHandler) put(ctx context.Context, router http.Handler, region, tableARN string, props map[string]any) (map[string]string, error) {
	doc, err := s3tablesPolicyDocument(props["ResourcePolicy"])
	if err != nil {
		return nil, err
	}
	info, err := s3tablesGetTable(ctx, router, region, tableARN)
	if err != nil {
		return nil, err
	}
	bucketARN := s3tablesBucketOf(tableARN)
	if err := s3tablesCall(ctx, router, region, http.MethodPut, s3tablesTablePath(bucketARN, info.Namespace[0], info.Name, "policy"),
		"PutTablePolicy", map[string]any{"resourcePolicy": doc}, nil); err != nil {
		return nil, err
	}
	return map[string]string{"TableBucketARN": bucketARN, "Namespace": info.Namespace[0], "TableName": info.Name}, nil
}

func (h *s3tablesTablePolicyHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if fmt.Sprint(props["TableARN"]) != fmt.Sprint(oldProps["TableARN"]) {
		return "", nil, errReplacementRequired
	}
	attrs, err := h.put(ctx, router, rCtx.Region, physicalID, props)
	return physicalID, attrs, err
}

func (h *s3tablesTablePolicyHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	info, rec, err := s3tablesLookupTable(ctx, router, rCtx.Region, physicalID)
	if err != nil {
		return teardownError("GetTable", rec, err)
	}
	rec, err = s3tablesRequest(ctx, router, rCtx.Region, http.MethodDelete,
		s3tablesTablePath(s3tablesBucketOf(physicalID), info.Namespace[0], info.Name, "policy"), "", nil)
	return teardownError("DeleteTablePolicy", rec, err)
}
