package s3tables_test

// GetTable's legacy binding (#2264).
//
// S3 Tables launched with GetTable at GET /tables/{tableBucketARN}/{namespace}/{name}.
// On 2025-06-06 the model moved it to GET /get-table with every member in the
// query string, so that a table could also be named by tableArn
// (aws/api-models-aws a4eef7c, botocore 75060cf). SDKs generated before then
// still send the path form: the Rust SDK's aws-sdk-s3tables 1.0.0 and the .NET
// SDK's AWSSDK.S3Tables 4.0.0, which the rust-sdk and dotnet-sdk compat suites
// pin, both do. The change shipped as a non-breaking api-change, so AWS is
// taken to answer both bindings (inferred, not verified against real AWS; see
// s3tables.fillLegacyGetTable). Overcast served only the
// new binding, so those SDKs got a 403 telling them to scope their credential
// to MediaStore.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
)

// labelEscape percent-encodes a value for a non-greedy Smithy path label the
// way smithy-rs and the .NET SDK do: everything outside the RFC 3986
// unreserved set, so an ARN's ':' and '/' both travel escaped.
func labelEscape(v string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(v)
}

// legacyGetTable sends GetTable the way aws-sdk-s3tables 1.0.0 (Rust) and
// AWSSDK.S3Tables 4.0.0 (.NET) put it on the wire: a GET with no body on the
// path binding, SigV4-signed for "s3tables".
func (f *fixture) legacyGetTable(t *testing.T, name string, contentType string) (int, []byte) {
	t.Helper()
	path := "/tables/" + labelEscape(f.bucketARN) + "/" + labelEscape(f.namespace) + "/" + labelEscape(name)
	req, err := http.NewRequest(http.MethodGet, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	empty := sha256.Sum256(nil)
	creds := aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, hex.EncodeToString(empty[:]), "s3tables", "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func TestGetTable_legacyPathBindingFromOlderSDKs(t *testing.T) {
	// Given: a table, and what the current binding answers for it
	f := newFixture(t, "legacy-bucket")
	f.createTable(t, "orders", nil)
	current, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
	})
	if err != nil {
		t.Fatalf("GetTable on /get-table: %v", err)
	}

	// The Rust SDK sends no Content-Type on a bodiless GET; the .NET SDK
	// sends one anyway. Neither may change the answer.
	for _, contentType := range []string{"", "application/json"} {
		t.Run("content-type="+contentType, func(t *testing.T) {
			// When: an older SDK reads it on the path binding
			status, raw := f.legacyGetTable(t, "orders", contentType)

			// Then: S3 Tables answers it, with the same table
			if status != http.StatusOK {
				t.Fatalf("legacy GetTable = %d %s", status, raw)
			}
			var got struct {
				Name         string   `json:"name"`
				Namespace    []string `json:"namespace"`
				TableARN     string   `json:"tableARN"`
				VersionToken string   `json:"versionToken"`
				Format       string   `json:"format"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decoding %s: %v", raw, err)
			}
			if got.Name != "orders" || got.TableARN != aws.ToString(current.TableARN) ||
				got.VersionToken != aws.ToString(current.VersionToken) || got.Format != "ICEBERG" ||
				len(got.Namespace) != 1 || got.Namespace[0] != f.namespace {
				t.Errorf("legacy GetTable = %s, want the table /get-table returns (%s)", raw, aws.ToString(current.TableARN))
			}
		})
	}
}

func TestGetTable_legacyPathBindingForAMissingTable(t *testing.T) {
	// Given: a namespace with no table called "absent"
	f := newFixture(t, "legacy-missing")

	// When: an older SDK reads it on the path binding
	status, raw := f.legacyGetTable(t, "absent", "")

	// Then: S3 Tables refuses it as the current binding would, not as another
	// service would
	if status != http.StatusNotFound || !strings.Contains(string(raw), "NotFoundException") {
		t.Errorf("legacy GetTable of a missing table = %d %s, want 404 NotFoundException", status, raw)
	}
}
