package groups

import (
	"context"
	"fmt"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// S3TablesSchemaV2 returns the s3tables-schemav2 group: create-table with a
// nested schemaV2 — a list, a map and a struct as Iceberg type documents —
// partitioned by a field of the struct, then the metadata file it wrote.
//
// Every call is signed: S3 Tables' paths are also legal S3 bucket names.
func S3TablesSchemaV2() ServiceGroup {
	g := &s3tablesSchemaV2CliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"s3tables-schemav2:CreateTable":              g.CreateTable,
			"s3tables-schemav2:GetTableMetadataLocation": g.GetTableMetadataLocation,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"s3tables-schemav2": g.setup,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"s3tables-schemav2": g.teardown,
		},
	}
}

type s3tablesSchemaV2CliGroup struct{}

const s3tablesSchemaV2CliTable = "events"

// s3tablesSchemaV2Metadata is the table's metadata.iceberg, as the CLI takes it:
// members by their SDK names, and each nested type in the Iceberg spec's JSON.
const s3tablesSchemaV2Metadata = `{"iceberg":{
  "schemaV2":{"type":"struct","identifierFieldIds":[1],"fields":[
    {"id":1,"name":"id","required":true,"type":"long"},
    {"id":2,"name":"tags","required":false,"type":{"type":"list","element-id":5,"element":"string","element-required":false}},
    {"id":3,"name":"attributes","required":false,"type":{"type":"map","key-id":6,"key":"string","value-id":7,"value":"string","value-required":false}},
    {"id":4,"name":"customer","required":true,"type":{"type":"struct","fields":[{"id":8,"name":"region","required":true,"type":"string"}]}}
  ]},
  "partitionSpec":{"fields":[{"sourceId":8,"transform":"identity","name":"region"}]}
}}`

func s3tablesSchemaV2Namespace(runID string) string {
	return strings.ReplaceAll(tableBucketName("s3tables-v2-", runID), "-", "_")
}

// tableArgs names the group's table.
func (g *s3tablesSchemaV2CliGroup) tableArgs(t *harness.TestContext) []string {
	return []string{"--table-bucket-arn", t.GetString("s3tables_v2_bucket_arn"),
		"--namespace", s3tablesSchemaV2Namespace(t.RunID), "--name", s3tablesSchemaV2CliTable}
}

func (g *s3tablesSchemaV2CliGroup) setup(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "create-table-bucket", "--name", tableBucketName("s3tables-v2-", t.RunID))
	if err != nil {
		return err
	}
	arn, _ := out["arn"].(string)
	t.Set("s3tables_v2_bucket_arn", arn)
	return awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "create-namespace", "--table-bucket-arn", arn, "--namespace", s3tablesSchemaV2Namespace(t.RunID))
}

func (g *s3tablesSchemaV2CliGroup) teardown(_ context.Context, t *harness.TestContext) error {
	arn := t.GetString("s3tables_v2_bucket_arn")
	if arn == "" {
		return nil
	}
	awscli.RunSigned(t.Endpoint, t.Region, append([]string{"s3tables", "delete-table"}, g.tableArgs(t)...)...)                                           //nolint:errcheck
	awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-namespace", "--table-bucket-arn", arn, "--namespace", s3tablesSchemaV2Namespace(t.RunID)) //nolint:errcheck
	awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-table-bucket", "--table-bucket-arn", arn)                                                 //nolint:errcheck
	return nil
}

func (g *s3tablesSchemaV2CliGroup) CreateTable(_ context.Context, t *harness.TestContext) error {
	args := append([]string{"s3tables", "create-table"}, g.tableArgs(t)...)
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, append(args, "--format", "ICEBERG", "--metadata", s3tablesSchemaV2Metadata)...)
	if err != nil {
		return err
	}
	tableARN, _ := out["tableARN"].(string)
	token, _ := out["versionToken"].(string)
	if !strings.Contains(tableARN, "/table/") || token == "" {
		return fmt.Errorf("create-table: tableARN %q, versionToken %q", tableARN, token)
	}
	return nil
}

func (g *s3tablesSchemaV2CliGroup) GetTableMetadataLocation(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, append([]string{"s3tables", "get-table-metadata-location"}, g.tableArgs(t)...)...)
	if err != nil {
		return err
	}
	loc, _ := out["metadataLocation"].(string)
	warehouse, _ := out["warehouseLocation"].(string)
	if warehouse == "" || !strings.HasPrefix(loc, warehouse+"/metadata/") || !strings.HasSuffix(loc, ".metadata.json") {
		return fmt.Errorf("get-table-metadata-location: metadataLocation %q, warehouseLocation %q", loc, warehouse)
	}
	return nil
}
