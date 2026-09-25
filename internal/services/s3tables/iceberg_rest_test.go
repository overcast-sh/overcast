package s3tables

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// memoryS3 is an in-memory S3 accessor: buckets and objects, keyed
// "bucket/key".
type memoryS3 struct {
	buckets map[string]bool
	objects map[string][]byte
}

func withMemoryS3(s *Service) *memoryS3 {
	m := &memoryS3{buckets: map[string]bool{}, objects: map[string][]byte{}}
	s.InitS3Access(
		func(_ context.Context, bucket, _ string) *protocol.AWSError {
			m.buckets[bucket] = true
			return nil
		},
		func(_ context.Context, bucket, key string, body []byte, _ events.S3PutObjectOptions) (events.S3PutObjectResult, *protocol.AWSError) {
			m.objects[bucket+"/"+key] = body
			return events.S3PutObjectResult{}, nil
		},
		func(_ context.Context, bucket, key, _ string) ([]byte, *protocol.AWSError) {
			if body, ok := m.objects[bucket+"/"+key]; ok {
				return body, nil
			}
			return nil, &protocol.AWSError{Code: "NoSuchKey", Message: "The specified key does not exist.", HTTPStatus: http.StatusNotFound}
		},
	)
	return m
}

// icebergTable creates table name in namespace "ns" through the catalog.
func icebergTable(t *testing.T, s *Service, arn, name string) *icebergLoadTableResult {
	t.Helper()
	var schema icebergmeta.Schema
	if err := json.Unmarshal([]byte(`{"type":"struct","schema-id":0,"fields":[{"id":1,"name":"id","required":true,"type":"long"}]}`), &schema); err != nil {
		t.Fatal(err)
	}
	out, aerr := s.icebergCreateTableTyped(context.Background(), &icebergCreateTableRequest{
		icebergPath: icebergPath{BucketARN: arn, Namespace: "ns"}, Name: name, Schema: &schema,
	})
	if aerr != nil {
		t.Fatalf("create %s: %v", name, aerr)
	}
	return out
}

func commitRequest(arn, table string, updates ...icebergmeta.Update) *icebergCommitTableRequest {
	return &icebergCommitTableRequest{
		icebergPath:   icebergPath{BucketARN: arn, Namespace: "ns", Table: table},
		CommitRequest: icebergmeta.CommitRequest{Updates: updates},
	}
}

func TestIcebergCommit_thatChangesNothingKeepsTheTableAsItIs(t *testing.T) {
	// Given: a table and its S3 Tables pointer
	s, _ := newTestService(t)
	s3 := withMemoryS3(s)
	arn := seed(t, s)
	created := icebergTable(t, s, arn, "t")
	ctx := context.Background()
	before, _ := s.getTableMetadataLocationTyped(ctx, &tableRequest{TableBucketARN: arn, Namespace: "ns", Name: "t"})
	files := len(s3.objects)

	// When: a commit removes a property the table does not have
	out, aerr := s.icebergCommitTableTyped(ctx, commitRequest(arn, "t",
		icebergmeta.Update{Action: "remove-properties", Removals: []string{"absent"}}))

	// Then: no file is written, and the pointer and its token are unchanged
	if aerr != nil || out.MetadataLocation != created.MetadataLocation || len(s3.objects) != files {
		t.Fatalf("commit = %+v, %v; files %d -> %d", out, aerr, files, len(s3.objects))
	}
	after, _ := s.getTableMetadataLocationTyped(ctx, &tableRequest{TableBucketARN: arn, Namespace: "ns", Name: "t"})
	if after.VersionToken != before.VersionToken {
		t.Errorf("version token rotated on a no-op commit")
	}
}

func TestIcebergCommit_refusesWhatS3TablesDoesNotAllow(t *testing.T) {
	cases := map[string]func(t *testing.T, s *Service, arn string) *protocol.AWSError{
		"moving a table out of its warehouse": func(t *testing.T, s *Service, arn string) *protocol.AWSError {
			icebergTable(t, s, arn, "t")
			_, aerr := s.icebergCommitTableTyped(context.Background(), commitRequest(arn, "t",
				icebergmeta.Update{Action: "set-location", Location: "s3://elsewhere--table-s3"}))
			return aerr
		},
		"a staged table in another table's warehouse": func(t *testing.T, s *Service, arn string) *protocol.AWSError {
			other := icebergTable(t, s, arn, "other")
			req := commitRequest(arn, "staged",
				icebergmeta.Update{Action: "assign-uuid", UUID: "u"},
				icebergmeta.Update{Action: "add-schema", Schema: &other.Metadata.Schemas[0]},
				icebergmeta.Update{Action: "set-current-schema", SchemaID: intPtr(-1)},
				icebergmeta.Update{Action: "add-spec", Spec: &icebergmeta.PartitionSpec{}},
				icebergmeta.Update{Action: "set-default-spec", SpecID: intPtr(-1)},
				icebergmeta.Update{Action: "add-sort-order", SortOrder: &icebergmeta.SortOrder{}},
				icebergmeta.Update{Action: "set-default-sort-order", SortOrderID: intPtr(-1)},
				icebergmeta.Update{Action: "set-location", Location: other.Metadata.Location})
			req.Requirements = []icebergmeta.Requirement{{Type: icebergmeta.AssertCreate}}
			_, aerr := s.icebergCommitTableTyped(context.Background(), req)
			return aerr
		},
		"registering a file outside its table's location": func(t *testing.T, s *Service, arn string) *protocol.AWSError {
			created := icebergTable(t, s, arn, "t")
			raw, _ := json.Marshal(created.Metadata)
			_, _ = s.putObject(context.Background(), "another-bucket", "metadata/00000-x.metadata.json", raw, s3PutJSON)
			_, aerr := s.icebergRegisterTableTyped(context.Background(), &icebergRegisterTableRequest{
				icebergPath: icebergPath{BucketARN: arn, Namespace: "ns"}, Name: "copy",
				MetadataLocation: "s3://another-bucket/metadata/00000-x.metadata.json",
			})
			return aerr
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a bucket with a namespace, over an in-memory S3
			s, _ := newTestService(t)
			withMemoryS3(s)
			arn := seed(t, s)

			// When: the catalog is asked for it
			aerr := run(t, s, arn)

			// Then: it is a bad request
			if aerr == nil || aerr.Code != "BadRequestException" {
				t.Errorf("aerr = %v", aerr)
			}
		})
	}
}

func TestWriteIcebergError_usesTheSpecsModelAndHidesServerFaults(t *testing.T) {
	cases := map[string]struct {
		in         *protocol.AWSError
		wantStatus int
		wantType   string
		wantMsg    string
	}{
		"a sentinel with an Iceberg exception": {errTableNotFound, http.StatusNotFound, "NoSuchTableException", errTableNotFound.Message},
		"a client error keeps its code":        {badRequest("bad"), http.StatusBadRequest, "BadRequestException", "bad"},
		"a failed requirement":                 {icebergmetaError(errors.Join(icebergmeta.ErrRequirementFailed)), http.StatusConflict, "CommitFailedException", ""},
		"a server fault":                       {protocol.Wrap(protocol.ErrInternalError, errors.New("disk on fire")), http.StatusInternalServerError, "InternalServerError", protocol.ErrInternalError.Message},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: an error on its way to a catalog client
			w := httptest.NewRecorder()

			// When: it is written
			writeIcebergError(w, httptest.NewRequest(http.MethodGet, "/", nil), tc.in)

			// Then: it is the spec's error model, and a cause never leaks
			var body icebergErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			e := body.Error
			if w.Code != tc.wantStatus || e.Code != tc.wantStatus || e.Type != tc.wantType || (tc.wantMsg != "" && e.Message != tc.wantMsg) {
				t.Errorf("wrote %d %+v", w.Code, e)
			}
		})
	}
}

func TestIcebergCommit_racingWritersFromOneStateHaveExactlyOneWinner(t *testing.T) {
	// Given: a table with no snapshot, and writers that all saw it that way
	s, _ := newTestService(t)
	withMemoryS3(s)
	arn := seed(t, s)
	icebergTable(t, s, arn, "t")
	const writers = 8
	type outcome struct {
		out  *icebergLoadTableResult
		aerr *protocol.AWSError
	}
	results := make(chan outcome, writers)

	// When: each commits its own first snapshot at once
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			id := int64(i + 1)
			req := commitRequest(arn, "t",
				icebergmeta.Update{Action: "add-snapshot", Snapshot: &icebergmeta.Snapshot{
					SnapshotID: id, SequenceNumber: 1, TimestampMS: 1, ManifestList: "s3://m.avro",
					Summary: map[string]string{"operation": "append"},
				}},
				icebergmeta.Update{Action: "set-snapshot-ref", RefName: icebergmeta.MainBranch,
					SnapshotRef: icebergmeta.SnapshotRef{SnapshotID: id, Type: icebergmeta.RefBranch}})
			req.Requirements = []icebergmeta.Requirement{{Type: icebergmeta.AssertRefSnapshotID, Ref: icebergmeta.MainBranch}}
			out, aerr := s.icebergCommitTableTyped(context.Background(), req)
			results <- outcome{out, aerr}
		})
	}
	wg.Wait()
	close(results)

	// Then: one commit wins, every other is a CommitFailedException, and the
	// table points at the winner's file
	var winner *icebergLoadTableResult
	for r := range results {
		switch {
		case r.aerr == nil && winner == nil:
			winner = r.out
		case r.aerr == nil:
			t.Fatalf("a second commit won: %s", r.out.MetadataLocation)
		case r.aerr.Code != "CommitFailedException":
			t.Errorf("loser got %v, want CommitFailedException", r.aerr)
		}
	}
	if winner == nil {
		t.Fatal("no commit won")
	}
	loc, _ := s.getTableMetadataLocationTyped(context.Background(), &tableRequest{TableBucketARN: arn, Namespace: "ns", Name: "t"})
	if loc.MetadataLocation != winner.MetadataLocation {
		t.Errorf("table points at %q, winner wrote %q", loc.MetadataLocation, winner.MetadataLocation)
	}
}
