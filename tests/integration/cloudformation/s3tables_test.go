package cloudformation_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// s3tablesStackTemplate is the shape a CDK S3 Tables stack produces: a table
// bucket with maintenance and metrics, a namespace, a table with a schema and
// compaction, and a policy on each.
const s3tablesStackTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Bucket": {
      "Type": "AWS::S3Tables::TableBucket",
      "Properties": {
        "TableBucketName": "cfn-tables",
        "UnreferencedFileRemoval": {"Status": "Enabled", "UnreferencedDays": 7, "NoncurrentDays": 14},
        "MetricsConfiguration": {"Status": "Enabled"},
        "ReplicationConfiguration": {
          "Role": "arn:aws:iam::000000000000:role/replication",
          "Rules": [{"Destinations": [{"DestinationTableBucketARN": "arn:aws:s3tables:us-west-2:000000000000:bucket/replica"}]}]
        },
        "Tags": [{"Key": "team", "Value": "data"}]
      }
    },
    "Ns": {
      "Type": "AWS::S3Tables::Namespace",
      "Properties": {"TableBucketARN": {"Fn::GetAtt": ["Bucket", "TableBucketARN"]}, "Namespace": "sales"}
    },
    "Orders": {
      "Type": "AWS::S3Tables::Table",
      "DependsOn": "Ns",
      "Properties": {
        "TableBucketARN": {"Fn::GetAtt": ["Bucket", "TableBucketARN"]},
        "Namespace": "sales",
        "TableName": "orders",
        "OpenTableFormat": "ICEBERG",
        "IcebergMetadata": {"IcebergSchema": {"SchemaFieldList": [
          {"Name": "id", "Type": "long", "Required": true},
          {"Name": "total", "Type": "decimal(10,2)"}
        ]}},
        "Compaction": {"Status": "Disabled"}
      }
    },
    "BucketPolicy": {
      "Type": "AWS::S3Tables::TableBucketPolicy",
      "Properties": {
        "TableBucketARN": {"Ref": "Bucket"},
        "ResourcePolicy": {"Version": "2012-10-17", "Statement": []}
      }
    },
    "OrdersPolicy": {
      "Type": "AWS::S3Tables::TablePolicy",
      "Properties": {
        "TableARN": {"Ref": "Orders"},
        "ResourcePolicy": {"Version": "2012-10-17", "Statement": []}
      }
    }
  },
  "Outputs": {
    "BucketRef": {"Value": {"Ref": "Bucket"}},
    "NamespaceRef": {"Value": {"Ref": "Ns"}},
    "TableRef": {"Value": {"Ref": "Orders"}},
    "TableWarehouse": {"Value": {"Fn::GetAtt": ["Orders", "WarehouseLocation"]}},
    "TableVersion": {"Value": {"Fn::GetAtt": ["Orders", "VersionToken"]}},
    "PolicyTableName": {"Value": {"Fn::GetAtt": ["OrdersPolicy", "TableName"]}}
  }
}`

func TestCreateStack_S3Tables(t *testing.T) {
	// Given: a stack of every S3 Tables resource type
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"s3tables-stack"},
		"TemplateBody": []string{s3tablesStackTemplate},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: it completes
	waitForStackStatus(t, srv, "s3tables-stack", "CREATE_COMPLETE")
	out := describeStackOutputs(t, srv, "s3tables-stack")
	bucketARN := "arn:aws:s3tables:us-east-1:000000000000:bucket/cfn-tables"

	// And: Ref and GetAtt follow each type's primary identifier
	if out["BucketRef"] != bucketARN {
		t.Errorf("BucketRef = %q", out["BucketRef"])
	}
	if out["NamespaceRef"] != bucketARN+"|sales" {
		t.Errorf("NamespaceRef = %q", out["NamespaceRef"])
	}
	if !strings.HasPrefix(out["TableRef"], bucketARN+"/table/") || !strings.HasSuffix(out["TableWarehouse"], "--table-s3") ||
		out["TableVersion"] == "" || out["PolicyTableName"] != "orders" {
		t.Errorf("outputs = %v", out)
	}

	// And: the resources exist through the service, configured as declared
	c := s3tables.New(s3tables.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
	ctx := context.Background()
	tbl, err := c.GetTable(ctx, &s3tables.GetTableInput{TableArn: aws.String(out["TableRef"])})
	if err != nil || tbl.MetadataLocation == nil {
		t.Fatalf("GetTable = %+v, %v", tbl, err)
	}
	maint, err := c.GetTableMaintenanceConfiguration(ctx, &s3tables.GetTableMaintenanceConfigurationInput{
		TableBucketARN: aws.String(bucketARN), Namespace: aws.String("sales"), Name: aws.String("orders"),
	})
	if err != nil || maint.Configuration["icebergCompaction"].Status != types.MaintenanceStatusDisabled {
		t.Errorf("table maintenance = %+v, %v", maint, err)
	}
	bm, err := c.GetTableBucketMaintenanceConfiguration(ctx, &s3tables.GetTableBucketMaintenanceConfigurationInput{TableBucketARN: aws.String(bucketARN)})
	if err != nil {
		t.Fatalf("bucket maintenance: %v", err)
	}
	ufr, _ := bm.Configuration["icebergUnreferencedFileRemoval"].Settings.(*types.TableBucketMaintenanceSettingsMemberIcebergUnreferencedFileRemoval)
	if ufr == nil || aws.ToInt32(ufr.Value.UnreferencedDays) != 7 || aws.ToInt32(ufr.Value.NonCurrentDays) != 14 {
		t.Errorf("bucket maintenance = %+v", bm.Configuration)
	}
	if _, err := c.GetTableBucketMetricsConfiguration(ctx, &s3tables.GetTableBucketMetricsConfigurationInput{TableBucketARN: aws.String(bucketARN)}); err != nil {
		t.Errorf("metrics configuration: %v", err)
	}
	if _, err := c.GetTablePolicy(ctx, &s3tables.GetTablePolicyInput{
		TableBucketARN: aws.String(bucketARN), Namespace: aws.String("sales"), Name: aws.String("orders"),
	}); err != nil {
		t.Errorf("table policy: %v", err)
	}
	repl, err := c.GetTableBucketReplication(ctx, &s3tables.GetTableBucketReplicationInput{TableBucketARN: aws.String(bucketARN)})
	if err != nil || len(repl.Configuration.Rules) != 1 ||
		aws.ToString(repl.Configuration.Rules[0].Destinations[0].DestinationTableBucketARN) != "arn:aws:s3tables:us-west-2:000000000000:bucket/replica" {
		t.Errorf("bucket replication = %+v, %v", repl, err)
	}
	tags, err := c.ListTagsForResource(ctx, &s3tables.ListTagsForResourceInput{ResourceArn: aws.String(bucketARN)})
	if err != nil || tags.Tags["team"] != "data" {
		t.Errorf("bucket tags = %+v, %v", tags, err)
	}

	// When: the stack is deleted
	del := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": []string{"s3tables-stack"}})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: every resource goes, bucket last
	helpers.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool {
		list, err := c.ListTableBuckets(ctx, &s3tables.ListTableBucketsInput{})
		return err == nil && len(list.TableBuckets) == 0
	}, "timed out waiting for the table bucket to be deleted with the stack")
}

func TestUpdateStack_S3TablesRenamesInPlaceAndDropsRemovedConfiguration(t *testing.T) {
	// Given: the stack above
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"s3tables-update"},
		"TemplateBody": []string{s3tablesStackTemplate},
	})
	resp.Body.Close()
	waitForStackStatus(t, srv, "s3tables-update", "CREATE_COMPLETE")
	before := describeStackOutputs(t, srv, "s3tables-update")

	// When: the table is renamed and the bucket's replication is removed
	updated := strings.Replace(s3tablesStackTemplate, `"TableName": "orders"`, `"TableName": "orders_v2"`, 1)
	start := strings.Index(updated, `"ReplicationConfiguration"`)
	end := strings.Index(updated, `"Tags": [{"Key": "team"`)
	updated = updated[:start] + updated[end:]
	up := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    []string{"s3tables-update"},
		"TemplateBody": []string{updated},
	})
	up.Body.Close()
	waitForStackStatus(t, srv, "s3tables-update", "UPDATE_COMPLETE")

	// Then: the table kept its ARN under the new name, and replication is gone
	after := describeStackOutputs(t, srv, "s3tables-update")
	if after["TableRef"] != before["TableRef"] {
		t.Errorf("table was replaced: %q -> %q", before["TableRef"], after["TableRef"])
	}
	c := s3tables.New(s3tables.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
	ctx := context.Background()
	tbl, err := c.GetTable(ctx, &s3tables.GetTableInput{TableArn: aws.String(after["TableRef"])})
	if err != nil || aws.ToString(tbl.Name) != "orders_v2" {
		t.Errorf("GetTable = %+v, %v", tbl, err)
	}
	bucketARN := after["BucketRef"]
	if _, err := c.GetTableBucketReplication(ctx, &s3tables.GetTableBucketReplicationInput{TableBucketARN: aws.String(bucketARN)}); err == nil {
		t.Error("bucket replication still configured after it left the template")
	}
}
