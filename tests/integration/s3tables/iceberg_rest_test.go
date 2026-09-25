package s3tables_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
)

func TestIcebergREST_namespaces(t *testing.T) {
	// Given: a table bucket reached through the signed catalog
	f := newFixture(t, "iceberg-namespaces")
	c := f.newIcebergClient(t, true)

	// When: a namespace is created with properties
	var created struct {
		Namespace  []string          `json:"namespace"`
		Properties map[string]string `json:"properties"`
	}
	c.mustDo(http.MethodPost, c.under("/namespaces"), map[string]any{"namespace": []string{"sales"}, "properties": map[string]string{"owner": "a"}}, http.StatusOK, &created)

	// Then: it is an S3 Tables namespace, listed beside the fixture's
	if created.Namespace[0] != "sales" || created.Properties["owner"] != "a" {
		t.Errorf("created = %+v", created)
	}
	if _, err := f.tables.GetNamespace(f.ctx, &s3tables.GetNamespaceInput{TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String("sales")}); err != nil {
		t.Errorf("GetNamespace: %v", err)
	}
	var list struct {
		Namespaces [][]string `json:"namespaces"`
	}
	c.mustDo(http.MethodGet, c.under("/namespaces"), nil, http.StatusOK, &list)
	if len(list.Namespaces) != 2 || list.Namespaces[0][0] != "analytics" || list.Namespaces[1][0] != "sales" {
		t.Errorf("namespaces = %v", list.Namespaces)
	}

	// And: its properties are set and removed in one step
	var props struct {
		Updated, Removed, Missing []string
	}
	c.mustDo(http.MethodPost, c.under("/namespaces/sales/properties"),
		map[string]any{"removals": []string{"owner", "absent"}, "updates": map[string]string{"team": "b"}}, http.StatusOK, &props)
	if strings.Join(props.Updated, ",") != "team" || strings.Join(props.Removed, ",") != "owner" || strings.Join(props.Missing, ",") != "absent" {
		t.Errorf("properties update = %+v", props)
	}
	var loaded struct {
		Properties map[string]string `json:"properties"`
	}
	c.mustDo(http.MethodGet, c.under("/namespaces/sales"), nil, http.StatusOK, &loaded)
	if len(loaded.Properties) != 1 || loaded.Properties["team"] != "b" {
		t.Errorf("properties = %v", loaded.Properties)
	}
	c.mustFail(http.MethodPost, c.under("/namespaces/sales/properties"),
		map[string]any{"removals": []string{"k"}, "updates": map[string]string{"k": "v"}}, http.StatusUnprocessableEntity, "UnprocessableEntityException")

	// And: HEAD, the errors and drop answer as the spec says
	c.mustDo(http.MethodHead, c.under("/namespaces/sales"), nil, http.StatusNoContent, nil)
	c.mustFail(http.MethodGet, c.under("/namespaces/nope"), nil, http.StatusNotFound, "NoSuchNamespaceException")
	c.mustFail(http.MethodPost, c.under("/namespaces"), map[string]any{"namespace": []string{"sales"}}, http.StatusConflict, "AlreadyExistsException")
	c.mustFail(http.MethodPost, c.under("/namespaces"), map[string]any{"namespace": []string{"a", "b"}}, http.StatusBadRequest, "BadRequestException")
	c.mustDo(http.MethodPost, c.under("/namespaces/sales/tables"), eventsTable("orders", false), http.StatusOK, nil)
	c.mustFail(http.MethodDelete, c.under("/namespaces/sales"), nil, http.StatusConflict, "NamespaceNotEmptyException")
	c.mustDo(http.MethodDelete, c.under("/namespaces/sales/tables/orders?purgeRequested=true"), nil, http.StatusNoContent, nil)
	c.mustDo(http.MethodDelete, c.under("/namespaces/sales"), nil, http.StatusNoContent, nil)
	c.mustDo(http.MethodHead, c.under("/namespaces/sales"), nil, http.StatusNotFound, nil)
}

func TestIcebergREST_createLoadAndList(t *testing.T) {
	// Given: the catalog
	f := newFixture(t, "iceberg-tables")
	c := f.newIcebergClient(t, true)

	// When: a table is created with a nested schema, partitioned and sorted
	var created loadTableResult
	c.mustDo(http.MethodPost, c.under("/namespaces/analytics/tables"), eventsTable("events", false), http.StatusOK, &created)

	// Then: its first metadata file is in its warehouse, and S3 Tables points
	// at the same file
	got, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("events"),
	})
	if err != nil {
		t.Fatalf("GetTable: %v", err)
	}
	if aws.ToString(got.MetadataLocation) != created.MetadataLocation ||
		!strings.HasPrefix(created.MetadataLocation, aws.ToString(got.WarehouseLocation)+"/metadata/00000-") {
		t.Errorf("metadata location %q, table %+v", created.MetadataLocation, got)
	}
	if created.number("last-column-id") != 4 || created.number("default-sort-order-id") != 1 || created.Metadata["table-uuid"] == "" {
		t.Errorf("metadata = %v", created.Metadata)
	}
	stored := f.readS3(t, created.MetadataLocation)
	if stored["table-uuid"] != created.Metadata["table-uuid"] {
		t.Errorf("stored metadata = %v", stored)
	}

	// And: it loads, lists and exists
	var loaded loadTableResult
	c.mustDo(http.MethodGet, c.under("/namespaces/analytics/tables/events"), nil, http.StatusOK, &loaded)
	if loaded.MetadataLocation != created.MetadataLocation {
		t.Errorf("loaded %q", loaded.MetadataLocation)
	}
	var list struct {
		Identifiers []struct {
			Namespace []string `json:"namespace"`
			Name      string   `json:"name"`
		} `json:"identifiers"`
	}
	c.mustDo(http.MethodGet, c.under("/namespaces/analytics/tables"), nil, http.StatusOK, &list)
	if len(list.Identifiers) != 1 || list.Identifiers[0].Name != "events" || list.Identifiers[0].Namespace[0] != "analytics" {
		t.Errorf("identifiers = %+v", list.Identifiers)
	}
	c.mustDo(http.MethodHead, c.under("/namespaces/analytics/tables/events"), nil, http.StatusNoContent, nil)

	// And: what S3 Tables does not allow, or the table does not have, is refused
	c.mustFail(http.MethodPost, c.under("/namespaces/analytics/tables"), eventsTable("events", false), http.StatusConflict, "AlreadyExistsException")
	withLocation := eventsTable("elsewhere", false)
	withLocation["location"] = "s3://mine/elsewhere"
	c.mustFail(http.MethodPost, c.under("/namespaces/analytics/tables"), withLocation, http.StatusBadRequest, "BadRequestException")
	c.mustFail(http.MethodGet, c.under("/namespaces/analytics/tables/nope"), nil, http.StatusNotFound, "NoSuchTableException")
	c.mustFail(http.MethodGet, c.under("/namespaces/analytics/views"), nil, http.StatusNotImplemented, "UnsupportedOperationException")
}

func TestIcebergREST_renameRegisterAndDrop(t *testing.T) {
	// Given: a table created through the catalog
	f := newFixture(t, "iceberg-lifecycle")
	c := f.newIcebergClient(t, true)
	var created loadTableResult
	c.mustDo(http.MethodPost, c.under("/namespaces/analytics/tables"), eventsTable("events", false), http.StatusOK, &created)

	// When: it is renamed
	c.mustDo(http.MethodPost, c.under("/tables/rename"), map[string]any{
		"source":      map[string]any{"namespace": []string{"analytics"}, "name": "events"},
		"destination": map[string]any{"namespace": []string{"analytics"}, "name": "events_v2"},
	}, http.StatusNoContent, nil)

	// Then: only the new name answers
	c.mustFail(http.MethodGet, c.under("/namespaces/analytics/tables/events"), nil, http.StatusNotFound, "NoSuchTableException")
	c.mustDo(http.MethodHead, c.under("/namespaces/analytics/tables/events_v2"), nil, http.StatusNoContent, nil)

	// And: a table in use keeps its warehouse to itself
	c.mustFail(http.MethodPost, c.under("/namespaces/analytics/register"),
		map[string]any{"name": "events_copy", "metadata-location": created.MetadataLocation}, http.StatusBadRequest, "BadRequestException")

	// And: a drop must purge, as on AWS
	c.mustFail(http.MethodDelete, c.under("/namespaces/analytics/tables/events_v2"), nil, http.StatusBadRequest, "BadRequestException")
	c.mustDo(http.MethodDelete, c.under("/namespaces/analytics/tables/events_v2?purgeRequested=True"), nil, http.StatusNoContent, nil)
	c.mustFail(http.MethodGet, c.under("/namespaces/analytics/tables/events_v2"), nil, http.StatusNotFound, "NoSuchTableException")

	// And: the dropped table's files, left in its warehouse, register it again
	var registered loadTableResult
	c.mustDo(http.MethodPost, c.under("/namespaces/analytics/register"),
		map[string]any{"name": "events_back", "metadata-location": created.MetadataLocation}, http.StatusOK, &registered)
	if registered.MetadataLocation != created.MetadataLocation || registered.Metadata["table-uuid"] != created.Metadata["table-uuid"] {
		t.Errorf("registered = %+v", registered)
	}
	got, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("events_back"),
	})
	if err != nil || !strings.HasSuffix(aws.ToString(got.TableARN), "/table/"+created.Metadata["table-uuid"].(string)) {
		t.Errorf("GetTable = %+v, %v; want the ARN to carry the table-uuid", got, err)
	}
	c.mustFail(http.MethodPost, c.under("/namespaces/analytics/register"),
		map[string]any{"name": "events_back", "metadata-location": created.MetadataLocation}, http.StatusConflict, "AlreadyExistsException")
}

func TestIcebergREST_unsignedMount(t *testing.T) {
	// Given: a client with no credentials
	f := newFixture(t, "iceberg-unsigned")
	c := f.newIcebergClient(t, false)

	// When: it creates a table through /_overcast/s3tables/iceberg
	c.mustDo(http.MethodPost, c.under("/namespaces/analytics/tables"), eventsTable("events", false), http.StatusOK, nil)

	// Then: the signed catalog sees the same table
	signed := f.newIcebergClient(t, true)
	signed.mustDo(http.MethodHead, signed.under("/namespaces/analytics/tables/events"), nil, http.StatusNoContent, nil)
}

func TestIcebergREST_bucketNamedIcebergIsStillS3(t *testing.T) {
	// Given: an S3 bucket called "iceberg" holding the key "v1/config"
	srv := newFixture(t, "iceberg-bucket").srv
	client := s3Client(srv)
	ctx := t.Context()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("iceberg")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("iceberg"), Key: aws.String("v1/config"), Body: bytes.NewReader([]byte("plain object")),
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// When: the object is read back over the S3 API
	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("iceberg"), Key: aws.String("v1/config")})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer out.Body.Close()
	body, _ := io.ReadAll(out.Body)

	// Then: S3 answered, not the catalog
	if string(body) != "plain object" {
		t.Errorf("body = %q", body)
	}
}

// readS3 reads the JSON object at an s3:// location through the S3 API.
func (f *fixture) readS3(t *testing.T, location string) map[string]any {
	t.Helper()
	bucket, key, _ := strings.Cut(strings.TrimPrefix(location, "s3://"), "/")
	out, err := s3Client(f.srv).GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("GetObject %s: %v", location, err)
	}
	defer out.Body.Close()
	var doc map[string]any
	if err := json.NewDecoder(out.Body).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", location, err)
	}
	return doc
}
