package cloudformation_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// TestUpdateStack_DynamoDBTableGlobalSecondaryIndexCreate covers half of the
// documented remaining gap (docs/dev/compatibility/services/cloudformation.yaml):
// "GlobalSecondaryIndexes changes are silently ignored on stack updates (GSIs
// are forwarded on create only; no GlobalSecondaryIndexUpdates dispatch)".
// DynamoDB's UpdateTable already reconciles GSI create/delete/throughput —
// CloudFormation just never called it.
func TestUpdateStack_DynamoDBTableGlobalSecondaryIndexCreate(t *testing.T) {
	// Given: a stack-created table with no secondary indexes
	srv := helpers.NewTestServer(t)
	const stackName = "dynamodb-gsi-create-stack"
	const tableName = "cfn-gsi-create-table"
	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {dynamodbGSITemplate(tableName, "")},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	putResp := dynamodbCall(t, srv, "PutItem", map[string]any{
		"TableName": tableName,
		"Item": map[string]any{
			"id":     map[string]string{"S": "user#1"},
			"status": map[string]string{"S": "active"},
		},
	})
	defer putResp.Body.Close()
	helpers.AssertStatus(t, putResp, http.StatusOK)

	// When: a GSI is added on a stack update
	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {dynamodbGSITemplate(tableName, dynamodbByStatusGSI)},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: the index is immediately queryable, backfilled with existing items
	queryResp := dynamodbCall(t, srv, "Query", map[string]any{
		"TableName":              tableName,
		"IndexName":              "by-status",
		"KeyConditionExpression": "#s = :status",
		"ExpressionAttributeNames": map[string]string{
			"#s": "status",
		},
		"ExpressionAttributeValues": map[string]any{
			":status": map[string]string{"S": "active"},
		},
	})
	defer queryResp.Body.Close()
	helpers.AssertStatus(t, queryResp, http.StatusOK)
	var result struct {
		Count int `json:"Count"`
	}
	helpers.DecodeJSON(t, queryResp, &result)
	if result.Count != 1 {
		t.Errorf("GSI Query count = %d, want 1", result.Count)
	}
}

func TestUpdateStack_DynamoDBTableGlobalSecondaryIndexDelete(t *testing.T) {
	// Given: a stack-created table with a GSI
	srv := helpers.NewTestServer(t)
	const stackName = "dynamodb-gsi-delete-stack"
	const tableName = "cfn-gsi-delete-table"
	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {dynamodbGSITemplate(tableName, dynamodbByStatusGSI)},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// When: the GSI is removed on a stack update
	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {dynamodbGSITemplate(tableName, "")},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: DescribeTable no longer lists it
	descResp := dynamodbCall(t, srv, "DescribeTable", map[string]any{"TableName": tableName})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var result struct {
		Table struct {
			GlobalSecondaryIndexes []struct {
				IndexName string `json:"IndexName"`
			} `json:"GlobalSecondaryIndexes"`
		} `json:"Table"`
	}
	helpers.DecodeJSON(t, descResp, &result)
	if len(result.Table.GlobalSecondaryIndexes) != 0 {
		t.Errorf("GlobalSecondaryIndexes after delete = %#v, want none", result.Table.GlobalSecondaryIndexes)
	}
}

func TestUpdateStack_DynamoDBTableGlobalSecondaryIndexThroughputUpdate(t *testing.T) {
	// Given: a provisioned-throughput table with a GSI
	srv := helpers.NewTestServer(t)
	const stackName = "dynamodb-gsi-throughput-stack"
	const tableName = "cfn-gsi-throughput-table"
	const initialTemplate = `{
  "Resources": {
    "Sessions": {
      "Type": "AWS::DynamoDB::Table",
      "Properties": {
        "TableName": "cfn-gsi-throughput-table",
        "AttributeDefinitions": [
          {"AttributeName": "id", "AttributeType": "S"},
          {"AttributeName": "status", "AttributeType": "S"}
        ],
        "KeySchema": [{"AttributeName": "id", "KeyType": "HASH"}],
        "BillingMode": "PROVISIONED",
        "ProvisionedThroughput": {"ReadCapacityUnits": 5, "WriteCapacityUnits": 5},
        "GlobalSecondaryIndexes": [{
          "IndexName": "by-status",
          "KeySchema": [{"AttributeName": "status", "KeyType": "HASH"}],
          "Projection": {"ProjectionType": "ALL"},
          "ProvisionedThroughput": {"ReadCapacityUnits": 5, "WriteCapacityUnits": 5}
        }]
      }
    }
  }
}`
	const updatedTemplate = `{
  "Resources": {
    "Sessions": {
      "Type": "AWS::DynamoDB::Table",
      "Properties": {
        "TableName": "cfn-gsi-throughput-table",
        "AttributeDefinitions": [
          {"AttributeName": "id", "AttributeType": "S"},
          {"AttributeName": "status", "AttributeType": "S"}
        ],
        "KeySchema": [{"AttributeName": "id", "KeyType": "HASH"}],
        "BillingMode": "PROVISIONED",
        "ProvisionedThroughput": {"ReadCapacityUnits": 5, "WriteCapacityUnits": 5},
        "GlobalSecondaryIndexes": [{
          "IndexName": "by-status",
          "KeySchema": [{"AttributeName": "status", "KeyType": "HASH"}],
          "Projection": {"ProjectionType": "ALL"},
          "ProvisionedThroughput": {"ReadCapacityUnits": 20, "WriteCapacityUnits": 15}
        }]
      }
    }
  }
}`
	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {initialTemplate},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// When: only the GSI's ProvisionedThroughput changes
	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {updatedTemplate},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: DescribeTable reflects the new GSI throughput
	descResp := dynamodbCall(t, srv, "DescribeTable", map[string]any{"TableName": tableName})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var result struct {
		Table struct {
			GlobalSecondaryIndexes []struct {
				IndexName             string `json:"IndexName"`
				ProvisionedThroughput struct {
					ReadCapacityUnits  int `json:"ReadCapacityUnits"`
					WriteCapacityUnits int `json:"WriteCapacityUnits"`
				} `json:"ProvisionedThroughput"`
			} `json:"GlobalSecondaryIndexes"`
		} `json:"Table"`
	}
	helpers.DecodeJSON(t, descResp, &result)
	if len(result.Table.GlobalSecondaryIndexes) != 1 {
		t.Fatalf("GlobalSecondaryIndexes = %#v, want exactly one", result.Table.GlobalSecondaryIndexes)
	}
	got := result.Table.GlobalSecondaryIndexes[0].ProvisionedThroughput
	if got.ReadCapacityUnits != 20 || got.WriteCapacityUnits != 15 {
		t.Errorf("GSI ProvisionedThroughput = %+v, want {20 15}", got)
	}
}

func TestUpdateStack_DynamoDBTableGlobalSecondaryIndexSchemaChangeRejected(t *testing.T) {
	// Given: a stack-created table with a GSI
	srv := helpers.NewTestServer(t)
	const stackName = "dynamodb-gsi-schema-reject-stack"
	const tableName = "cfn-gsi-schema-reject-table"
	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {dynamodbGSITemplate(tableName, dynamodbByStatusGSI)},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// When: the GSI's Projection changes in place (no add/remove)
	changedGSI := strings.Replace(dynamodbByStatusGSI, `"ALL"`, `"KEYS_ONLY"`, 1)
	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {dynamodbGSITemplate(tableName, changedGSI)},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_ROLLBACK_COMPLETE")

	// Then: CloudFormation reports the unsupported change and preserves the index
	reasons := strings.Join(describeStackEventReasons(t, srv, stackName), "\n")
	const wantReason = "KeySchema and Projection changes are not supported"
	if !strings.Contains(reasons, wantReason) {
		t.Errorf("stack event reasons = %q, want %q", reasons, wantReason)
	}
	descResp := dynamodbCall(t, srv, "DescribeTable", map[string]any{"TableName": tableName})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var result struct {
		Table struct {
			GlobalSecondaryIndexes []struct {
				Projection struct {
					ProjectionType string `json:"ProjectionType"`
				} `json:"Projection"`
			} `json:"GlobalSecondaryIndexes"`
		} `json:"Table"`
	}
	helpers.DecodeJSON(t, descResp, &result)
	if len(result.Table.GlobalSecondaryIndexes) != 1 || result.Table.GlobalSecondaryIndexes[0].Projection.ProjectionType != "ALL" {
		t.Errorf("GlobalSecondaryIndexes after rejected update = %#v, want ALL projection preserved", result.Table.GlobalSecondaryIndexes)
	}
}

const dynamodbByStatusGSI = `"GlobalSecondaryIndexes": [{
          "IndexName": "by-status",
          "KeySchema": [{"AttributeName": "status", "KeyType": "HASH"}],
          "Projection": {"ProjectionType": "ALL"}
        }],`

// dynamodbGSITemplate renders the table with or without the by-status GSI.
// The "status" attribute is defined only alongside the index that keys on
// it: DynamoDB rejects an AttributeDefinition no key or index uses
// ("Some AttributeDefinitions are not used"), so a template that always
// declared it could never have created the index-less table on AWS.
func dynamodbGSITemplate(tableName, gsi string) string {
	statusDef := ""
	if gsi != "" {
		statusDef = `,
          {"AttributeName": "status", "AttributeType": "S"}`
	}
	return `{
  "Resources": {
    "Sessions": {
      "Type": "AWS::DynamoDB::Table",
      "Properties": {
        "TableName": "` + tableName + `",
        "AttributeDefinitions": [
          {"AttributeName": "id", "AttributeType": "S"}` + statusDef + `
        ],
        "KeySchema": [{"AttributeName": "id", "KeyType": "HASH"}],
        ` + gsi + `
        "BillingMode": "PAY_PER_REQUEST"
      }
    }
  }
}`
}
