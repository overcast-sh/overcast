package s3tables_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"
)

func TestIcebergREST_commitAppendsAndRefusesAStaleWriter(t *testing.T) {
	// Given: a table created through the catalog
	f := newFixture(t, "iceberg-commit")
	c := f.newIcebergClient(t, true)
	var created loadTableResult
	c.mustDo(http.MethodPost, c.under("/namespaces/analytics/tables"), eventsTable("events", false), http.StatusOK, &created)
	uuid := created.Metadata["table-uuid"].(string)
	location := created.Metadata["location"].(string)
	tablePath := c.under("/namespaces/analytics/tables/events")

	// When: an append commits its first snapshot
	first := appendCommit(uuid, nil, 101, 1, location)
	var committed loadTableResult
	c.mustDo(http.MethodPost, tablePath, first, http.StatusOK, &committed)

	// Then: the next metadata file is written and the table points at it —
	// the same pointer S3 Tables reports
	if !strings.Contains(committed.MetadataLocation, "/metadata/00001-") || committed.number("current-snapshot-id") != 101 {
		t.Errorf("commit = %q, current = %v", committed.MetadataLocation, committed.Metadata["current-snapshot-id"])
	}
	log, _ := committed.Metadata["metadata-log"].([]any)
	if len(log) != 1 || log[0].(map[string]any)["metadata-file"] != created.MetadataLocation {
		t.Errorf("metadata-log = %v", log)
	}
	if got := f.metadataLocation(t, "events"); got.location != committed.MetadataLocation {
		t.Errorf("S3 Tables metadata location = %q", got.location)
	}

	// And: a writer that saw the table before the append loses, as a conflict
	c.mustFail(http.MethodPost, tablePath, first, http.StatusConflict, "CommitFailedException")

	// And: one that saw the append commits on top of it
	parent := int64(101)
	c.mustDo(http.MethodPost, tablePath, appendCommit(uuid, &parent, 102, 2, location), http.StatusOK, &committed)
	if !strings.Contains(committed.MetadataLocation, "/metadata/00002-") || committed.number("last-sequence-number") != 2 {
		t.Errorf("second commit = %q, seq = %v", committed.MetadataLocation, committed.Metadata["last-sequence-number"])
	}
}

func TestIcebergREST_commitAndUpdateTableMetadataLocationShareOneSwap(t *testing.T) {
	// Given: a table with one appended snapshot, and the version token S3
	// Tables issued for it
	f := newFixture(t, "iceberg-swap")
	c := f.newIcebergClient(t, true)
	var created, committed loadTableResult
	c.mustDo(http.MethodPost, c.under("/namespaces/analytics/tables"), eventsTable("events", false), http.StatusOK, &created)
	uuid, location := created.Metadata["table-uuid"].(string), created.Metadata["location"].(string)
	tablePath := c.under("/namespaces/analytics/tables/events")
	c.mustDo(http.MethodPost, tablePath, appendCommit(uuid, nil, 7, 1, location), http.StatusOK, &committed)
	before := f.metadataLocation(t, "events")

	// When: an S3 Tables client rolls the table back to its first file
	if _, err := f.tables.UpdateTableMetadataLocation(f.ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("events"),
		VersionToken: aws.String(before.token), MetadataLocation: aws.String(created.MetadataLocation),
	}); err != nil {
		t.Fatalf("UpdateTableMetadataLocation: %v", err)
	}

	// Then: the catalog serves that file, and a REST writer that expected the
	// snapshot on main is refused
	var loaded loadTableResult
	c.mustDo(http.MethodGet, tablePath, nil, http.StatusOK, &loaded)
	if loaded.MetadataLocation != created.MetadataLocation {
		t.Errorf("loaded %q", loaded.MetadataLocation)
	}
	parent := int64(7)
	c.mustFail(http.MethodPost, tablePath, appendCommit(uuid, &parent, 8, 2, location), http.StatusConflict, "CommitFailedException")

	// And: a REST commit rotates the token, so the S3 Tables client's stale
	// one now conflicts
	c.mustDo(http.MethodPost, tablePath, appendCommit(uuid, nil, 9, 1, location), http.StatusOK, nil)
	_, err := f.tables.UpdateTableMetadataLocation(f.ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("events"),
		VersionToken: aws.String(before.token), MetadataLocation: aws.String(committed.MetadataLocation),
	})
	var conflict *types.ConflictException
	if !errors.As(err, &conflict) {
		t.Errorf("stale token err = %v, want ConflictException", err)
	}
}

func TestIcebergREST_stagedCreate(t *testing.T) {
	// Given: a staged create, as Trino issues for CTAS
	f := newFixture(t, "iceberg-staged")
	c := f.newIcebergClient(t, true)
	var staged loadTableResult
	c.mustDo(http.MethodPost, c.under("/namespaces/analytics/tables"), eventsTable("ctas", true), http.StatusOK, &staged)

	// Then: it has metadata but no metadata file, its warehouse bucket already
	// exists for the engine's data files, and the table does not exist yet
	location := staged.Metadata["location"].(string)
	if staged.MetadataLocation != "" || !strings.HasSuffix(location, "--table-s3") {
		t.Fatalf("staged = %q at %q", staged.MetadataLocation, location)
	}
	if _, err := s3Client(f.srv).HeadBucket(f.ctx, &s3.HeadBucketInput{Bucket: aws.String(strings.TrimPrefix(location, "s3://"))}); err != nil {
		t.Errorf("HeadBucket: %v", err)
	}
	tablePath := c.under("/namespaces/analytics/tables/ctas")
	c.mustFail(http.MethodGet, tablePath, nil, http.StatusNotFound, "NoSuchTableException")

	// When: the create transaction commits with assert-create
	create := createTransaction(staged.Metadata)
	var committed loadTableResult
	c.mustDo(http.MethodPost, tablePath, create, http.StatusOK, &committed)

	// Then: the table exists, in the staged warehouse, with its first file
	if !strings.HasPrefix(committed.MetadataLocation, location+"/metadata/00000-") || committed.Metadata["table-uuid"] != staged.Metadata["table-uuid"] {
		t.Errorf("committed = %q %v", committed.MetadataLocation, committed.Metadata["table-uuid"])
	}
	got, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("ctas"),
	})
	if err != nil || aws.ToString(got.WarehouseLocation) != location ||
		!strings.HasSuffix(aws.ToString(got.TableARN), "/table/"+staged.Metadata["table-uuid"].(string)) {
		t.Errorf("GetTable = %+v, %v; want the staged warehouse and an ARN carrying the table-uuid", got, err)
	}

	// And: creating it again conflicts, and a commit to a table that was
	// never created is not found
	c.mustFail(http.MethodPost, tablePath, create, http.StatusConflict, "CommitFailedException")
	c.mustFail(http.MethodPost, c.under("/namespaces/analytics/tables/never"), map[string]any{"requirements": []any{}, "updates": []any{}},
		http.StatusNotFound, "NoSuchTableException")
}

// createTransaction is the commit that finishes a staged create: the staged
// metadata replayed as updates, guarded by assert-create.
func createTransaction(m map[string]any) map[string]any {
	schema := m["schemas"].([]any)[0]
	spec := m["partition-specs"].([]any)[0]
	var order any
	for _, o := range m["sort-orders"].([]any) {
		if o.(map[string]any)["order-id"] == m["default-sort-order-id"] {
			order = o
		}
	}
	return map[string]any{
		"requirements": []any{map[string]any{"type": "assert-create"}},
		"updates": []any{
			map[string]any{"action": "assign-uuid", "uuid": m["table-uuid"]},
			map[string]any{"action": "upgrade-format-version", "format-version": m["format-version"]},
			map[string]any{"action": "add-schema", "schema": schema},
			map[string]any{"action": "set-current-schema", "schema-id": -1},
			map[string]any{"action": "add-spec", "spec": spec},
			map[string]any{"action": "set-default-spec", "spec-id": -1},
			map[string]any{"action": "add-sort-order", "sort-order": order},
			map[string]any{"action": "set-default-sort-order", "sort-order-id": -1},
			map[string]any{"action": "set-location", "location": m["location"]},
			map[string]any{"action": "set-properties", "updates": m["properties"]},
		},
	}
}

type tablePointer struct{ location, token string }

// metadataLocation is the table's pointer as S3 Tables reports it.
func (f *fixture) metadataLocation(t *testing.T, name string) tablePointer {
	t.Helper()
	out, err := f.tables.GetTableMetadataLocation(f.ctx, &s3tables.GetTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String(name),
	})
	if err != nil {
		t.Fatalf("GetTableMetadataLocation: %v", err)
	}
	return tablePointer{location: aws.ToString(out.MetadataLocation), token: aws.ToString(out.VersionToken)}
}
