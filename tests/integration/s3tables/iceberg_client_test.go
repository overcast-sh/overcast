package s3tables_test

// A minimal Iceberg REST catalog client for the integration tests: what
// PyIceberg and Spark's RESTCatalog send, signed the way their SigV4 support
// signs it (signing name "s3tables"), or unsigned against the /_overcast
// mount.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	s3tablessvc "github.com/overcast-sh/overcast/internal/services/s3tables"
)

type icebergClient struct {
	t      *testing.T
	root   string
	signed bool
	prefix string
}

// newIcebergClient configures a client for the fixture's bucket the way the
// Iceberg clients do: GET /v1/config?warehouse=<bucket ARN>, then every path
// under the prefix the config overrides.
func (f *fixture) newIcebergClient(t *testing.T, signed bool) *icebergClient {
	t.Helper()
	root := f.srv.URL + s3tablessvc.IcebergRoot
	if !signed {
		root = f.srv.URL + s3tablessvc.IcebergInternalRoot
	}
	c := &icebergClient{t: t, root: root, signed: signed}
	var cfg struct {
		Defaults  map[string]string `json:"defaults"`
		Overrides map[string]string `json:"overrides"`
		Endpoints []string          `json:"endpoints"`
	}
	c.mustDo(http.MethodGet, "/v1/config?warehouse="+url.QueryEscape(f.bucketARN), nil, http.StatusOK, &cfg)
	c.prefix = cfg.Overrides["prefix"]
	// The S3 endpoint is minted like every client-facing URL: the configured
	// hostname, on the port the client called.
	_, port, _ := strings.Cut(strings.TrimPrefix(f.srv.URL, "http://"), ":")
	if c.prefix == "" || !strings.HasSuffix(cfg.Defaults["s3.endpoint"], ":"+port) || len(cfg.Endpoints) == 0 {
		t.Fatalf("config = %+v", cfg)
	}
	return c
}

// do sends one request to a path under the catalog root and returns the
// status and the body.
func (c *icebergClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			c.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, c.root+path, bytes.NewReader(payload))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.signed {
		sum := sha256.Sum256(payload)
		creds := aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}
		if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, hex.EncodeToString(sum[:]), "s3tables", "us-east-1", time.Now()); err != nil {
			c.t.Fatal(err)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// mustDo sends a request that must answer want, decoding the body into out
// when out is non-nil.
func (c *icebergClient) mustDo(method, path string, body any, want int, out any) {
	c.t.Helper()
	status, raw := c.do(method, path, body)
	if status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
}

// mustFail sends a request that must fail with the given status and Iceberg
// exception type, in the spec's error model.
func (c *icebergClient) mustFail(method, path string, body any, wantStatus int, wantType string) {
	c.t.Helper()
	status, raw := c.do(method, path, body)
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if status != wantStatus || e.Error.Type != wantType || e.Error.Code != wantStatus || e.Error.Message == "" {
		c.t.Fatalf("%s %s = %d %s, want %d %s", method, path, status, raw, wantStatus, wantType)
	}
}

// under is a path beneath the bucket's prefix.
func (c *icebergClient) under(path string) string { return "/v1/" + c.prefix + path }

// loadTableResult is the spec's LoadTableResult, with the metadata kept
// loosely typed so the tests read it as a client would.
type loadTableResult struct {
	MetadataLocation string         `json:"metadata-location"`
	Metadata         map[string]any `json:"metadata"`
}

func (r loadTableResult) number(key string) float64 {
	v, _ := r.Metadata[key].(float64)
	return v
}

// eventsTable is a create-table body with a nested schema, a partition spec
// and a sort order, in the numbering a client proposes.
func eventsTable(name string, stage bool) map[string]any {
	return map[string]any{
		"name": name,
		"schema": map[string]any{
			"type": "struct", "schema-id": 0, "identifier-field-ids": []int{1},
			"fields": []any{
				map[string]any{"id": 1, "name": "id", "required": true, "type": "long"},
				map[string]any{"id": 2, "name": "ts", "required": false, "type": "timestamptz"},
				map[string]any{"id": 3, "name": "tags", "required": false, "type": map[string]any{
					"type": "list", "element-id": 4, "element": "string", "element-required": false}},
			},
		},
		"partition-spec": map[string]any{"spec-id": 0, "fields": []any{
			map[string]any{"source-id": 2, "field-id": 1000, "name": "ts_day", "transform": "day"}}},
		"write-order": map[string]any{"order-id": 1, "fields": []any{
			map[string]any{"source-id": 1, "transform": "identity", "direction": "asc", "null-order": "nulls-first"}}},
		"stage-create": stage,
		"properties":   map[string]string{"owner": "tests"},
	}
}

// appendCommit is the commit an append sends: a snapshot on main, guarded by
// main pointing where the writer last saw it.
func appendCommit(tableUUID string, parent *int64, snapshotID, sequence int64, location string) map[string]any {
	snapshot := map[string]any{
		"snapshot-id": snapshotID, "sequence-number": sequence, "timestamp-ms": time.Now().UnixMilli(),
		"manifest-list": location + "/metadata/snap-" + time.Now().Format("150405.000") + ".avro",
		"summary":       map[string]string{"operation": "append"}, "schema-id": 0,
	}
	if parent != nil {
		snapshot["parent-snapshot-id"] = *parent
	}
	return map[string]any{
		"requirements": []any{
			map[string]any{"type": "assert-table-uuid", "uuid": tableUUID},
			map[string]any{"type": "assert-ref-snapshot-id", "ref": "main", "snapshot-id": parent},
		},
		"updates": []any{
			map[string]any{"action": "add-snapshot", "snapshot": snapshot},
			map[string]any{"action": "set-snapshot-ref", "ref-name": "main", "snapshot-id": snapshotID, "type": "branch"},
		},
	}
}
