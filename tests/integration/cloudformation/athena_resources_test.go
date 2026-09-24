package cloudformation_test

// athena_resources_test.go — AWS::Athena::WorkGroup, NamedQuery,
// PreparedStatement and DataCatalog through a stack's whole life: created
// with Ref and Fn::GetAtt wired to outputs, updated in place rather than
// replaced (#1759), and deleted (#2065).

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func athenaSDK(srv *helpers.TestServer) *athena.Client {
	return athena.New(athena.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

// athenaStackTemplate is one of each Athena resource; phase varies what an
// update changes.
func athenaStackTemplate(phase string) string {
	location, statement, description := "s3://results/v1/", "SELECT * FROM t WHERE id = ?", "first"
	cutoff := `"BytesScannedCutoffPerQuery": 20000000,`
	if phase == "update" {
		location, statement, description, cutoff = "s3://results/v2/", "SELECT id FROM t WHERE id = ?", "second", ""
	}
	return `{
  "Resources": {
    "WorkGroup": {
      "Type": "AWS::Athena::WorkGroup",
      "Properties": {
        "Name": "cfn-athena-wg",
        "Description": "` + description + `",
        "State": "DISABLED",
        "RecursiveDeleteOption": true,
        "Tags": [{"Key": "phase", "Value": "` + phase + `"}],
        "WorkGroupConfiguration": {
          ` + cutoff + `
          "EnforceWorkGroupConfiguration": true,
          "ResultConfiguration": {"OutputLocation": "` + location + `"}
        }
      }
    },
    "Saved": {
      "Type": "AWS::Athena::NamedQuery",
      "Properties": {"Database": "analytics", "QueryString": "SELECT 1", "WorkGroup": {"Ref": "WorkGroup"}}
    },
    "Statement": {
      "Type": "AWS::Athena::PreparedStatement",
      "Properties": {"StatementName": "by_id", "WorkGroup": {"Ref": "WorkGroup"}, "QueryStatement": "` + statement + `"}
    },
    "Catalog": {
      "Type": "AWS::Athena::DataCatalog",
      "Properties": {"Name": "cfn_hive", "Type": "HIVE", "Description": "` + description + `",
        "Parameters": {"metadata-function": "arn:aws:lambda:us-east-1:000000000000:function:meta"}}
    }
  },
  "Outputs": {
    "WorkGroupRef": {"Value": {"Ref": "WorkGroup"}},
    "CreationTime": {"Value": {"Fn::GetAtt": ["WorkGroup", "CreationTime"]}},
    "Engine": {"Value": {"Fn::GetAtt": ["WorkGroup", "WorkGroupConfiguration.EngineVersion.EffectiveEngineVersion"]}},
    "SavedRef": {"Value": {"Ref": "Saved"}},
    "SavedId": {"Value": {"Fn::GetAtt": ["Saved", "NamedQueryId"]}},
    "StatementRef": {"Value": {"Ref": "Statement"}},
    "CatalogRef": {"Value": {"Ref": "Catalog"}}
  }
}`
}

func runStackAction(t *testing.T, srv *helpers.TestServer, action, stackName, template, want string) {
	t.Helper()
	params := url.Values{"StackName": {stackName}}
	if template != "" {
		params.Set("TemplateBody", template)
	}
	resp := cfnQuery(t, srv, action, params)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, want)
}

func TestStack_AthenaResources_lifecycle(t *testing.T) {
	ctx := context.Background()
	srv := helpers.NewTestServer(t)
	c := athenaSDK(srv)
	const stack = "athena-resources"
	wgName := aws.String("cfn-athena-wg")

	// Given: the stack is created
	runStackAction(t, srv, "CreateStack", stack, athenaStackTemplate("create"), "CREATE_COMPLETE")
	outputs := describeStackOutputs(t, srv, stack)

	// Then: each resource exists as the template describes, and Ref/GetAtt resolve
	wg := mustCall(t, "GetWorkGroup", func() (*athena.GetWorkGroupOutput, error) {
		return c.GetWorkGroup(ctx, &athena.GetWorkGroupInput{WorkGroup: wgName})
	}).WorkGroup
	if wg.State != types.WorkGroupStateDisabled || aws.ToInt64(wg.Configuration.BytesScannedCutoffPerQuery) != 20000000 {
		t.Fatalf("WorkGroup = %+v / %+v", wg, wg.Configuration)
	}
	if outputs["WorkGroupRef"] != "cfn-athena-wg" || outputs["CreationTime"] == "" || outputs["Engine"] != "Athena engine version 3" ||
		outputs["StatementRef"] != "by_id" || outputs["CatalogRef"] != "cfn_hive" || outputs["SavedRef"] != outputs["SavedId"] {
		t.Fatalf("outputs = %v", outputs)
	}
	nq := mustCall(t, "GetNamedQuery", func() (*athena.GetNamedQueryOutput, error) {
		return c.GetNamedQuery(ctx, &athena.GetNamedQueryInput{NamedQueryId: aws.String(outputs["SavedId"])})
	}).NamedQuery
	if aws.ToString(nq.WorkGroup) != "cfn-athena-wg" || !strings.HasPrefix(aws.ToString(nq.Name), stack) {
		t.Fatalf("NamedQuery = %+v, want a generated name in the stack's workgroup", nq)
	}

	// When: the stack is updated
	runStackAction(t, srv, "UpdateStack", stack, athenaStackTemplate("update"), "UPDATE_COMPLETE")

	// Then: the workgroup changed in place, cutoff removed and tag replaced,
	// and the statement and catalog were updated
	wg = mustCall(t, "GetWorkGroup", func() (*athena.GetWorkGroupOutput, error) {
		return c.GetWorkGroup(ctx, &athena.GetWorkGroupInput{WorkGroup: wgName})
	}).WorkGroup
	if aws.ToString(wg.Description) != "second" || wg.Configuration.BytesScannedCutoffPerQuery != nil ||
		aws.ToString(wg.Configuration.ResultConfiguration.OutputLocation) != "s3://results/v2/" {
		t.Fatalf("updated WorkGroup = %+v / %+v", wg, wg.Configuration)
	}
	if got := describeStackOutputs(t, srv, stack)["CreationTime"]; got != outputs["CreationTime"] {
		t.Fatalf("CreationTime %s -> %s: the workgroup was replaced, not updated", outputs["CreationTime"], got)
	}
	tags := mustCall(t, "ListTagsForResource", func() (*athena.ListTagsForResourceOutput, error) {
		return c.ListTagsForResource(ctx, &athena.ListTagsForResourceInput{ResourceARN: aws.String("arn:aws:athena:us-east-1:000000000000:workgroup/cfn-athena-wg")})
	}).Tags
	if len(tags) != 1 || aws.ToString(tags[0].Value) != "update" {
		t.Fatalf("Tags = %+v", tags)
	}
	ps := mustCall(t, "GetPreparedStatement", func() (*athena.GetPreparedStatementOutput, error) {
		return c.GetPreparedStatement(ctx, &athena.GetPreparedStatementInput{StatementName: aws.String("by_id"), WorkGroup: wgName})
	}).PreparedStatement
	if aws.ToString(ps.QueryStatement) != "SELECT id FROM t WHERE id = ?" {
		t.Fatalf("QueryStatement = %q", aws.ToString(ps.QueryStatement))
	}
	catalog := mustCall(t, "GetDataCatalog", func() (*athena.GetDataCatalogOutput, error) {
		return c.GetDataCatalog(ctx, &athena.GetDataCatalogInput{Name: aws.String("cfn_hive")})
	}).DataCatalog
	if aws.ToString(catalog.Description) != "second" {
		t.Fatalf("DataCatalog = %+v", catalog)
	}

	// When: the stack is deleted
	runStackAction(t, srv, "DeleteStack", stack, "", "DELETE_COMPLETE")

	// Then: everything is gone
	if _, err := c.GetWorkGroup(ctx, &athena.GetWorkGroupInput{WorkGroup: wgName}); err == nil {
		t.Fatal("workgroup survived DeleteStack")
	}
	if _, err := c.GetDataCatalog(ctx, &athena.GetDataCatalogInput{Name: aws.String("cfn_hive")}); err == nil {
		t.Fatal("data catalog survived DeleteStack")
	}
}

func mustCall[T any](t *testing.T, what string, call func() (T, error)) T {
	t.Helper()
	v, err := call()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return v
}
