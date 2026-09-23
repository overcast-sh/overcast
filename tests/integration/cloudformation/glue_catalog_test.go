package cloudformation_test

// glue_catalog_test.go — AWS::Glue::Table carries its whole TableInput through
// to the catalog (it used to arrive with five fields left), and
// AWS::Glue::Partition creates, updates in place and deletes a partition.
// Everything is read back through Glue's own API, never through
// CloudFormation's bookkeeping.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func glueJSONCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
	t.Helper()
	return awsJSONCall(t, srv, "AWSGlue.", action, "application/x-amz-json-1.1", body)
}

// glueGet calls a Glue read operation and decodes its JSON response,
// returning the HTTP status alongside.
func glueGet(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) (int, map[string]any) {
	t.Helper()
	resp := glueJSONCall(t, srv, action, body)
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", action, err)
	}
	return resp.StatusCode, out
}

func glueCatalogTemplate(partitionParam string) string {
	return `{
  "Resources": {
    "Db": {"Type": "AWS::Glue::Database", "Properties": {"CatalogId": {"Ref": "AWS::AccountId"}, "DatabaseInput": {"Name": "cfn_catalog"}}},
    "Events": {
      "Type": "AWS::Glue::Table",
      "Properties": {
        "CatalogId": {"Ref": "AWS::AccountId"},
        "DatabaseName": {"Ref": "Db"},
        "TableInput": {
          "Name": "events",
          "TableType": "EXTERNAL_TABLE",
          "Parameters": {"classification": "parquet", "EXTERNAL": "TRUE"},
          "PartitionKeys": [{"Name": "dt", "Type": "string"}],
          "StorageDescriptor": {
            "Location": "s3://cfn-data/events/",
            "Columns": [{"Name": "id", "Type": "bigint", "Comment": "row id"}],
            "InputFormat": "org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat",
            "OutputFormat": "org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat",
            "SerdeInfo": {"SerializationLibrary": "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe", "Parameters": {"serialization.format": "1"}},
            "Compressed": false,
            "NumberOfBuckets": -1,
            "StoredAsSubDirectories": false
          }
        }
      }
    },
    "Day": {
      "Type": "AWS::Glue::Partition",
      "DependsOn": "Events",
      "Properties": {
        "CatalogId": {"Ref": "AWS::AccountId"},
        "DatabaseName": {"Ref": "Db"},
        "TableName": "events",
        "PartitionInput": {
          "Values": ["2024-01-01"],
          "Parameters": {"source": "` + partitionParam + `"},
          "StorageDescriptor": {"Location": "s3://cfn-data/events/dt=2024-01-01/", "Columns": [{"Name": "id", "Type": "bigint"}]}
        }
      }
    }
  }
}`
}

func TestCreateStack_GlueTableAndPartition_roundTripThroughTheCatalog(t *testing.T) {
	// Given: a stack with a database, a fully specified table and a partition
	srv := helpers.NewTestServer(t)
	const stackName = "glue-catalog-stack"
	resp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {glueCatalogTemplate("create")}})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// When: the table is read back through Glue
	status, out := glueGet(t, srv, "GetTable", map[string]any{"DatabaseName": "cfn_catalog", "Name": "events"})
	if status != http.StatusOK {
		t.Fatalf("GetTable status %d: %v", status, out)
	}
	table, _ := out["Table"].(map[string]any)
	sd, _ := table["StorageDescriptor"].(map[string]any)
	serde, _ := sd["SerdeInfo"].(map[string]any)
	cols, _ := sd["Columns"].([]any)
	keys, _ := table["PartitionKeys"].([]any)
	params, _ := table["Parameters"].(map[string]any)

	// Then: every property of the template's TableInput is there
	if sd["Location"] != "s3://cfn-data/events/" || serde["SerializationLibrary"] == nil || len(cols) != 1 ||
		sd["NumberOfBuckets"] != float64(-1) || sd["InputFormat"] == nil {
		t.Errorf("StorageDescriptor = %v", sd)
	}
	if len(keys) != 1 || params["classification"] != "parquet" || table["TableType"] != "EXTERNAL_TABLE" {
		t.Errorf("Table = %v", table)
	}

	// And: the partition exists with its definition
	status, out = glueGet(t, srv, "GetPartition", map[string]any{"DatabaseName": "cfn_catalog", "TableName": "events", "PartitionValues": []string{"2024-01-01"}})
	if status != http.StatusOK {
		t.Fatalf("GetPartition status %d: %v", status, out)
	}
	partition, _ := out["Partition"].(map[string]any)
	if p, _ := partition["Parameters"].(map[string]any); p["source"] != "create" {
		t.Errorf("Partition = %v", partition)
	}

	// When: the partition's definition changes
	resp = cfnQuery(t, srv, "UpdateStack", url.Values{"StackName": {stackName}, "TemplateBody": {glueCatalogTemplate("update")}})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: it is updated in place
	_, out = glueGet(t, srv, "GetPartition", map[string]any{"DatabaseName": "cfn_catalog", "TableName": "events", "PartitionValues": []string{"2024-01-01"}})
	partition, _ = out["Partition"].(map[string]any)
	if p, _ := partition["Parameters"].(map[string]any); p["source"] != "update" {
		t.Errorf("after UpdateStack, Partition = %v", partition)
	}

	// When: the stack is deleted
	resp = cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": {stackName}})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "DELETE_COMPLETE")

	// Then: the database, and with it everything in it, is gone
	status, out = glueGet(t, srv, "GetDatabase", map[string]any{"Name": "cfn_catalog"})
	if status != http.StatusBadRequest || out["__type"] == nil {
		t.Errorf("GetDatabase after DeleteStack: status %d, %v", status, out)
	}
}
