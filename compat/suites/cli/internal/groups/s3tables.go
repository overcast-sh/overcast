package groups

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// S3Tables returns the S3 Tables service group.
//
// Every call is signed. S3 Tables' paths (/buckets, /tables, …) are also legal
// S3 bucket names, and the emulator tells the two apart by the SigV4 signing
// name — so an unsigned call reaches S3, exactly as it would on AWS, where no
// unsigned S3 Tables request exists.
func S3Tables() ServiceGroup {
	g := &s3tablesCliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"s3tables-tables:CreateTableBucket":                     g.CreateTableBucket,
			"s3tables-tables:GetTableBucket":                        g.GetTableBucket,
			"s3tables-tables:ListTableBuckets":                      g.ListTableBuckets,
			"s3tables-tables:CreateNamespace":                       g.CreateNamespace,
			"s3tables-tables:ListNamespaces":                        g.ListNamespaces,
			"s3tables-tables:CreateTable":                           g.CreateTable,
			"s3tables-tables:GetTable":                              g.GetTable,
			"s3tables-tables:ListTables":                            g.ListTables,
			"s3tables-tables:UpdateTableMetadataLocation":           g.UpdateTableMetadataLocation,
			"s3tables-tables:UpdateTableMetadataLocationStaleToken": g.UpdateTableMetadataLocationStaleToken,
			"s3tables-tables:DeleteTable":                           g.DeleteTable,
			"s3tables-tables:DeleteNamespace":                       g.DeleteNamespace,
			"s3tables-tables:DeleteTableBucket":                     g.DeleteTableBucket,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"s3tables-tables": g.teardown,
		},
	}
}

type s3tablesCliGroup struct{}

const s3tablesCliTable = "orders"

// s3tablesBucketName is the run's table bucket: lowercase letters, digits and
// hyphens only.
func s3tablesBucketName(runID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(runID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	name := "s3tables-tables-" + strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// s3tablesNamespace: namespace names allow underscores, not hyphens.
func s3tablesNamespace(runID string) string {
	return strings.ReplaceAll(s3tablesBucketName(runID), "-", "_")
}

func (g *s3tablesCliGroup) arn(t *harness.TestContext) (string, error) {
	arn := t.GetString("s3tables_bucket_arn")
	if arn == "" {
		return "", fmt.Errorf("no table bucket from CreateTableBucket")
	}
	return arn, nil
}

func (g *s3tablesCliGroup) tableArgs(t *harness.TestContext, arn string) []string {
	return []string{"--table-bucket-arn", arn, "--namespace", s3tablesNamespace(t.RunID), "--name", s3tablesCliTable}
}

func (g *s3tablesCliGroup) teardown(_ context.Context, t *harness.TestContext) error {
	arn := t.GetString("s3tables_bucket_arn")
	if arn == "" {
		return nil
	}
	awscli.RunSigned(t.Endpoint, t.Region, append([]string{"s3tables", "delete-table"}, g.tableArgs(t, arn)...)...) //nolint:errcheck
	awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-namespace",                                          //nolint:errcheck
		"--table-bucket-arn", arn, "--namespace", s3tablesNamespace(t.RunID))
	awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-table-bucket", "--table-bucket-arn", arn) //nolint:errcheck
	return nil
}

func (g *s3tablesCliGroup) CreateTableBucket(_ context.Context, t *harness.TestContext) error {
	name := s3tablesBucketName(t.RunID)
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "create-table-bucket", "--name", name)
	if err != nil {
		return err
	}
	arn, _ := out["arn"].(string)
	if !strings.HasPrefix(arn, "arn:aws:s3tables:") || !strings.HasSuffix(arn, ":bucket/"+name) {
		return fmt.Errorf("CreateTableBucket: unexpected ARN %q", arn)
	}
	t.Set("s3tables_bucket_arn", arn)
	return nil
}

func (g *s3tablesCliGroup) GetTableBucket(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "get-table-bucket", "--table-bucket-arn", arn)
	if err != nil {
		return err
	}
	if out["arn"] != arn || out["name"] != s3tablesBucketName(t.RunID) {
		return fmt.Errorf("GetTableBucket: got %v / %v", out["arn"], out["name"])
	}
	if s, _ := out["createdAt"].(string); s == "" {
		return fmt.Errorf("GetTableBucket: missing createdAt")
	}
	if s, _ := out["ownerAccountId"].(string); s == "" {
		return fmt.Errorf("GetTableBucket: missing ownerAccountId")
	}
	return nil
}

func (g *s3tablesCliGroup) ListTableBuckets(_ context.Context, t *harness.TestContext) error {
	name := s3tablesBucketName(t.RunID)
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "list-table-buckets", "--prefix", name)
	if err != nil {
		return err
	}
	list, _ := out["tableBuckets"].([]any)
	for _, item := range list {
		if b, _ := item.(map[string]any); b["name"] == name {
			return nil
		}
	}
	return fmt.Errorf("ListTableBuckets: %q not listed in %v", name, out)
}

func (g *s3tablesCliGroup) CreateNamespace(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	ns := s3tablesNamespace(t.RunID)
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "create-namespace", "--table-bucket-arn", arn, "--namespace", ns)
	if err != nil {
		return err
	}
	got, _ := out["namespace"].([]any)
	if len(got) != 1 || got[0] != ns || out["tableBucketARN"] != arn {
		return fmt.Errorf("CreateNamespace: got %v", out)
	}
	return nil
}

func (g *s3tablesCliGroup) ListNamespaces(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "list-namespaces", "--table-bucket-arn", arn)
	if err != nil {
		return err
	}
	list, _ := out["namespaces"].([]any)
	if len(list) != 1 {
		return fmt.Errorf("ListNamespaces: expected 1 namespace, got %v", out)
	}
	ns, _ := list[0].(map[string]any)["namespace"].([]any)
	if len(ns) != 1 || ns[0] != s3tablesNamespace(t.RunID) {
		return fmt.Errorf("ListNamespaces: got %v", out)
	}
	return nil
}

func (g *s3tablesCliGroup) CreateTable(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "create-table",
		"--table-bucket-arn", arn, "--namespace", s3tablesNamespace(t.RunID), "--name", s3tablesCliTable, "--format", "ICEBERG")
	if err != nil {
		return err
	}
	tableARN, _ := out["tableARN"].(string)
	token, _ := out["versionToken"].(string)
	if !strings.HasPrefix(tableARN, arn+"/table/") || token == "" {
		return fmt.Errorf("CreateTable: got %v", out)
	}
	t.Set("s3tables_table_arn", tableARN)
	return nil
}

func (g *s3tablesCliGroup) GetTable(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	// Addressed by name rather than --table-arn, which later CLI releases added.
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, append([]string{"s3tables", "get-table"}, g.tableArgs(t, arn)...)...)
	if err != nil {
		return err
	}
	if out["name"] != s3tablesCliTable || out["format"] != "ICEBERG" || out["tableARN"] != t.GetString("s3tables_table_arn") {
		return fmt.Errorf("GetTable: got %v", out)
	}
	if w, _ := out["warehouseLocation"].(string); !strings.HasPrefix(w, "s3://") {
		return fmt.Errorf("GetTable: warehouseLocation %q is not an s3:// URI", w)
	}
	return nil
}

func (g *s3tablesCliGroup) ListTables(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "list-tables",
		"--table-bucket-arn", arn, "--namespace", s3tablesNamespace(t.RunID))
	if err != nil {
		return err
	}
	list, _ := out["tables"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != s3tablesCliTable {
		return fmt.Errorf("ListTables: got %v", out)
	}
	return nil
}

func (g *s3tablesCliGroup) metadataLocation(t *harness.TestContext, arn string) (map[string]any, error) {
	return awscli.RunOutputSigned(t.Endpoint, t.Region, append([]string{"s3tables", "get-table-metadata-location"}, g.tableArgs(t, arn)...)...)
}

func (g *s3tablesCliGroup) UpdateTableMetadataLocation(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	cur, err := g.metadataLocation(t, arn)
	if err != nil {
		return err
	}
	token, _ := cur["versionToken"].(string)
	warehouse, _ := cur["warehouseLocation"].(string)
	loc := warehouse + "/metadata/00001-compat.metadata.json"
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, append(append([]string{"s3tables", "update-table-metadata-location"}, g.tableArgs(t, arn)...),
		"--version-token", token, "--metadata-location", loc)...)
	if err != nil {
		return err
	}
	if out["metadataLocation"] != loc || out["versionToken"] == token {
		return fmt.Errorf("UpdateTableMetadataLocation: got %v (previous token %q)", out, token)
	}
	t.Set("s3tables_stale_token", token)
	return nil
}

func (g *s3tablesCliGroup) UpdateTableMetadataLocationStaleToken(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	cur, err := g.metadataLocation(t, arn)
	if err != nil {
		return err
	}
	warehouse, _ := cur["warehouseLocation"].(string)
	return assertAWSFailure(awscli.RunStatusSigned, t, "UpdateTableMetadataLocationStaleToken", "ConflictException", http.StatusConflict,
		append(append([]string{"s3tables", "update-table-metadata-location"}, g.tableArgs(t, arn)...),
			"--version-token", t.GetString("s3tables_stale_token"), "--metadata-location", warehouse+"/metadata/00002-compat.metadata.json")...)
}

func (g *s3tablesCliGroup) DeleteTable(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	if err := awscli.RunSigned(t.Endpoint, t.Region, append([]string{"s3tables", "delete-table"}, g.tableArgs(t, arn)...)...); err != nil {
		return err
	}
	return assertAWSFailure(awscli.RunStatusSigned, t, "DeleteTable", "NotFoundException", http.StatusNotFound,
		append([]string{"s3tables", "get-table"}, g.tableArgs(t, arn)...)...)
}

func (g *s3tablesCliGroup) DeleteNamespace(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	return awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-namespace", "--table-bucket-arn", arn, "--namespace", s3tablesNamespace(t.RunID))
}

func (g *s3tablesCliGroup) DeleteTableBucket(_ context.Context, t *harness.TestContext) error {
	arn, err := g.arn(t)
	if err != nil {
		return err
	}
	if err := awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-table-bucket", "--table-bucket-arn", arn); err != nil {
		return err
	}
	return assertAWSFailure(awscli.RunStatusSigned, t, "DeleteTableBucket", "NotFoundException", http.StatusNotFound,
		"s3tables", "get-table-bucket", "--table-bucket-arn", arn)
}
