package cloudformation_test

// Integration coverage for AWS::StepFunctions::StateMachineVersion and
// AWS::StepFunctions::StateMachineAlias, the StateMachine resource's
// StateMachineRevisionId attribute, and its DefinitionS3Location property.
// Each test provisions through CloudFormation and reads the result back
// through Step Functions' own API, so template and service must agree.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// sfnVersionedTemplate is the documented "publish a version for the latest
// revision" pattern: a version keyed on the state machine's revision, and an
// alias routing all traffic to it.
func sfnVersionedTemplate(result string) string {
	return fmt.Sprintf(`{
  "Resources": {
    "Workflow": {
      "Type": "AWS::StepFunctions::StateMachine",
      "Properties": {
        "StateMachineName": "cfn-versioned",
        "DefinitionString": "{\"StartAt\":\"P\",\"States\":{\"P\":{\"Type\":\"Pass\",\"Result\":\"%s\",\"End\":true}}}",
        "RoleArn": "arn:aws:iam::000000000000:role/sfn-role"
      }
    },
    "Version": {
      "Type": "AWS::StepFunctions::StateMachineVersion",
      "Properties": {
        "StateMachineArn": {"Ref": "Workflow"},
        "StateMachineRevisionId": {"Fn::GetAtt": ["Workflow", "StateMachineRevisionId"]},
        "Description": "release %s"
      }
    },
    "Prod": {
      "Type": "AWS::StepFunctions::StateMachineAlias",
      "Properties": {
        "Name": "PROD",
        "Description": "production traffic",
        "RoutingConfiguration": [{"StateMachineVersionArn": {"Ref": "Version"}, "Weight": 100}]
      }
    }
  },
  "Outputs": {
    "VersionRef": {"Value": {"Ref": "Version"}},
    "VersionArn": {"Value": {"Fn::GetAtt": ["Version", "Arn"]}},
    "AliasRef": {"Value": {"Ref": "Prod"}},
    "AliasArn": {"Value": {"Fn::GetAtt": ["Prod", "Arn"]}},
    "RevisionId": {"Value": {"Fn::GetAtt": ["Workflow", "StateMachineRevisionId"]}}
  }
}`, result, result)
}

func TestCreateStack_StepFunctionsVersionAndAlias(t *testing.T) {
	// Given: a template with a state machine, a version and an alias
	srv := helpers.NewTestServer(t)
	stackName := "sfn-versioned"
	smARN := sfnStateMachineARN("cfn-versioned")

	// When: the stack is created
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {sfnVersionedTemplate("v1")},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// Then: Ref and GetAtt Arn name version 1 and the PROD alias
	outputs := describeStackOutputs(t, srv, stackName)
	if outputs["VersionRef"] != smARN+":1" || outputs["VersionArn"] != smARN+":1" {
		t.Errorf("version outputs = %q / %q, want %q", outputs["VersionRef"], outputs["VersionArn"], smARN+":1")
	}
	if outputs["AliasRef"] != smARN+":PROD" || outputs["AliasArn"] != smARN+":PROD" {
		t.Errorf("alias outputs = %q / %q, want %q", outputs["AliasRef"], outputs["AliasArn"], smARN+":PROD")
	}
	// And: a never-updated state machine's revision is INITIAL
	if outputs["RevisionId"] != "INITIAL" {
		t.Errorf("StateMachineRevisionId = %q, want INITIAL", outputs["RevisionId"])
	}
	// And: the version carries its description, and the alias routes to it
	version := describeSFNStateMachine(t, srv, smARN+":1")
	if version["description"] != "release v1" {
		t.Errorf("version description = %v", version["description"])
	}
	alias := describeSFNAlias(t, srv, smARN+":PROD")
	if alias.Description != "production traffic" || len(alias.RoutingConfiguration) != 1 ||
		alias.RoutingConfiguration[0].StateMachineVersionArn != smARN+":1" || alias.RoutingConfiguration[0].Weight != 100 {
		t.Errorf("alias = %+v", alias)
	}
}

func TestUpdateStack_StepFunctionsNewRevisionPublishesAndShiftsAlias(t *testing.T) {
	// Given: a deployed version 1 behind the PROD alias
	srv := helpers.NewTestServer(t)
	stackName := "sfn-versioned-update"
	smARN := sfnStateMachineARN("cfn-versioned")
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {sfnVersionedTemplate("v1")},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// When: the definition changes
	upd := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {sfnVersionedTemplate("v2")},
	})
	defer upd.Body.Close()
	helpers.AssertStatus(t, upd, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: the new revision is published as version 2, the alias follows it,
	// and the replaced version 1 is cleaned up
	outputs := describeStackOutputs(t, srv, stackName)
	if outputs["VersionRef"] != smARN+":2" {
		t.Errorf("VersionRef = %q, want %q", outputs["VersionRef"], smARN+":2")
	}
	if outputs["RevisionId"] == "" || outputs["RevisionId"] == "INITIAL" {
		t.Errorf("RevisionId = %q, want the updated revision", outputs["RevisionId"])
	}
	alias := describeSFNAlias(t, srv, smARN+":PROD")
	if len(alias.RoutingConfiguration) != 1 || alias.RoutingConfiguration[0].StateMachineVersionArn != smARN+":2" {
		t.Errorf("alias routing = %+v, want version 2", alias.RoutingConfiguration)
	}
	list := sfnCFNJSONCall(t, srv, "ListStateMachineVersions", map[string]any{"stateMachineArn": smARN})
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	var versions struct {
		StateMachineVersions []struct {
			StateMachineVersionArn string `json:"stateMachineVersionArn"`
		} `json:"stateMachineVersions"`
	}
	helpers.DecodeJSON(t, list, &versions)
	if len(versions.StateMachineVersions) != 1 || versions.StateMachineVersions[0].StateMachineVersionArn != smARN+":2" {
		t.Errorf("versions = %+v, want only version 2", versions.StateMachineVersions)
	}
	version := describeSFNStateMachine(t, srv, smARN+":2")
	if !strings.Contains(fmt.Sprint(version["definition"]), `"v2"`) {
		t.Errorf("version 2 definition = %v", version["definition"])
	}
}

func TestCreateStack_StepFunctionsAliasDeploymentPreference(t *testing.T) {
	// Given: an alias deployed with a LINEAR DeploymentPreference
	srv := helpers.NewTestServer(t)
	stackName := "sfn-alias-deployment"
	smARN := sfnStateMachineARN("cfn-deployment")
	template := `{
  "Resources": {
    "Workflow": {
      "Type": "AWS::StepFunctions::StateMachine",
      "Properties": {
        "StateMachineName": "cfn-deployment",
        "DefinitionString": "{\"StartAt\":\"P\",\"States\":{\"P\":{\"Type\":\"Pass\",\"End\":true}}}",
        "RoleArn": "arn:aws:iam::000000000000:role/sfn-role"
      }
    },
    "Version": {
      "Type": "AWS::StepFunctions::StateMachineVersion",
      "Properties": {"StateMachineArn": {"Ref": "Workflow"}}
    },
    "Live": {
      "Type": "AWS::StepFunctions::StateMachineAlias",
      "Properties": {
        "Name": "LIVE",
        "DeploymentPreference": {
          "StateMachineVersionArn": {"Ref": "Version"},
          "Type": "LINEAR",
          "Percentage": 20,
          "Interval": 5
        }
      }
    }
  }
}`

	// When: the stack is created
	resp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {template}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// Then: the alias routes all traffic to the version at once
	alias := describeSFNAlias(t, srv, smARN+":LIVE")
	if len(alias.RoutingConfiguration) != 1 || alias.RoutingConfiguration[0].StateMachineVersionArn != smARN+":1" ||
		alias.RoutingConfiguration[0].Weight != 100 {
		t.Errorf("alias routing = %+v, want version 1 at weight 100", alias.RoutingConfiguration)
	}
	// And: the gradual shift Overcast does not simulate is reported
	if reasons := describeStackResourceReasons(t, srv, stackName); !strings.Contains(reasons, "DeploymentPreference") {
		t.Errorf("stack resources carry no DeploymentPreference limitation:\n%s", reasons)
	}
}

func TestDeleteStack_StepFunctionsVersionAndAliasRemoved(t *testing.T) {
	// Given: a deployed state machine, version and alias
	srv := helpers.NewTestServer(t)
	stackName := "sfn-versioned-delete"
	smARN := sfnStateMachineARN("cfn-versioned")
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {sfnVersionedTemplate("v1")},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// When: the stack is deleted
	del := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": {stackName}})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "DELETE_COMPLETE")

	// Then: the alias, the version and the state machine are all gone
	for _, check := range []struct{ op, field, arn, code string }{
		{op: "DescribeStateMachineAlias", field: "stateMachineAliasArn", arn: smARN + ":PROD", code: "ResourceNotFound"},
		{op: "DescribeStateMachine", field: "stateMachineArn", arn: smARN + ":1", code: "StateMachineDoesNotExist"},
		{op: "DescribeStateMachine", field: "stateMachineArn", arn: smARN, code: "StateMachineDoesNotExist"},
	} {
		r := sfnCFNJSONCall(t, srv, check.op, map[string]any{check.field: check.arn})
		helpers.AssertJSONError(t, r, check.code)
		r.Body.Close()
	}
}

func TestCreateStack_StepFunctionsDefinitionS3Location(t *testing.T) {
	// Given: an ASL document in S3 carrying a DefinitionSubstitutions placeholder
	srv := helpers.NewTestServer(t)
	s3PutObject(t, srv, "sfn-definitions", "flows/workflow.asl.json",
		`{"StartAt":"P","States":{"P":{"Type":"Pass","Result":"${Greeting}","End":true}}}`)
	template := `{
  "Resources": {
    "Workflow": {
      "Type": "AWS::StepFunctions::StateMachine",
      "Properties": {
        "StateMachineName": "cfn-s3-definition",
        "DefinitionS3Location": {"Bucket": "sfn-definitions", "Key": "flows/workflow.asl.json"},
        "DefinitionSubstitutions": {"Greeting": "hello from s3"},
        "RoleArn": "arn:aws:iam::000000000000:role/sfn-role"
      }
    }
  }
}`

	// When: the stack is created
	resp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {"sfn-s3-definition"}, "TemplateBody": {template}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, "sfn-s3-definition", "CREATE_COMPLETE")

	// Then: the state machine holds the fetched definition with substitutions applied
	described := describeSFNStateMachine(t, srv, sfnStateMachineARN("cfn-s3-definition"))
	definition, _ := described["definition"].(string)
	if !strings.Contains(definition, `"hello from s3"`) || strings.Contains(definition, "${Greeting}") {
		t.Errorf("definition = %s, want the S3 document with Greeting substituted", definition)
	}
}

func TestCreateStack_StepFunctionsDefinitionS3LocationMissingObject(t *testing.T) {
	// Given: a DefinitionS3Location naming an object that does not exist
	srv := helpers.NewTestServer(t)
	s3PutObject(t, srv, "sfn-definitions-empty", "placeholder", "x")
	template := `{
  "Resources": {
    "Workflow": {
      "Type": "AWS::StepFunctions::StateMachine",
      "Properties": {
        "StateMachineName": "cfn-s3-missing",
        "DefinitionS3Location": {"Bucket": "sfn-definitions-empty", "Key": "absent.json"},
        "RoleArn": "arn:aws:iam::000000000000:role/sfn-role"
      }
    }
  }
}`

	// When: the stack is created
	resp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {"sfn-s3-missing"}, "TemplateBody": {template}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the create fails and rolls back rather than creating an empty machine
	waitForStackStatus(t, srv, "sfn-s3-missing", "ROLLBACK_COMPLETE")
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

type sfnAliasDescription struct {
	Description          string `json:"description"`
	RoutingConfiguration []struct {
		StateMachineVersionArn string `json:"stateMachineVersionArn"`
		Weight                 int    `json:"weight"`
	} `json:"routingConfiguration"`
}

func describeSFNAlias(t *testing.T, srv *helpers.TestServer, aliasARN string) sfnAliasDescription {
	t.Helper()
	resp := sfnCFNJSONCall(t, srv, "DescribeStateMachineAlias", map[string]any{"stateMachineAliasArn": aliasARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out sfnAliasDescription
	helpers.DecodeJSON(t, resp, &out)
	return out
}
