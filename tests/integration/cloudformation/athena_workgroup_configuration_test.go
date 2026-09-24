package cloudformation_test

// athena_workgroup_configuration_test.go — AWS::Athena::WorkGroup's schema
// names the configuration property WorkGroupConfiguration
// (https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-athena-workgroup.html).
// The handler used to read the CreateWorkGroup API member's name,
// Configuration, from the template instead, so a real template's
// WorkGroupConfiguration was silently dropped — the same property-vs-API-member
// confusion #1309 fixed for AWS::CloudTrail::Trail's TrailName.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func athenaJSONCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
	t.Helper()
	return awsJSONCall(t, srv, "AmazonAthena.", action, "application/x-amz-json-1.1", body)
}

// getWorkGroupConfiguration reads the workgroup back through Athena's own
// GetWorkGroup — never through CloudFormation's bookkeeping — and returns its
// Configuration member (nil when the workgroup has none).
func getWorkGroupConfiguration(t *testing.T, srv *helpers.TestServer, name string) map[string]any {
	t.Helper()
	resp := athenaJSONCall(t, srv, "GetWorkGroup", map[string]any{"WorkGroup": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		WorkGroup struct {
			Name          string         `json:"Name"`
			Configuration map[string]any `json:"Configuration"`
		} `json:"WorkGroup"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode GetWorkGroup: %v", err)
	}
	if result.WorkGroup.Name != name {
		t.Fatalf("GetWorkGroup(%q).Name = %q", name, result.WorkGroup.Name)
	}
	return result.WorkGroup.Configuration
}

func outputLocation(config map[string]any) string {
	rc, _ := config["ResultConfiguration"].(map[string]any)
	loc, _ := rc["OutputLocation"].(string)
	return loc
}

// TestCreateStack_AthenaWorkGroupConfiguration_isForwarded asserts that a
// template using the real schema property WorkGroupConfiguration provisions a
// workgroup whose configuration Athena reports back through GetWorkGroup.
func TestCreateStack_AthenaWorkGroupConfiguration_isForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "athena-wgconfig-create-stack"
	const wgName = "cfn-wgconfig-workgroup"
	const location = "s3://athena-results-bucket/cfn-wgconfig/"
	template := `{
  "Resources": {
    "WorkGroup": {
      "Type": "AWS::Athena::WorkGroup",
      "Properties": {
        "Name": "` + wgName + `",
        "WorkGroupConfiguration": {
          "EnforceWorkGroupConfiguration": true,
          "ResultConfiguration": {"OutputLocation": "` + location + `"}
        }
      }
    }
  },
  "Outputs": {
    "WorkGroupRef": {"Value": {"Ref": "WorkGroup"}}
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	config := getWorkGroupConfiguration(t, srv, wgName)
	if got := outputLocation(config); got != location {
		t.Fatalf("GetWorkGroup(%q).Configuration.ResultConfiguration.OutputLocation = %q, want %q (Configuration: %#v)", wgName, got, location, config)
	}
	if enforce, _ := config["EnforceWorkGroupConfiguration"].(bool); !enforce {
		t.Fatalf("GetWorkGroup(%q).Configuration.EnforceWorkGroupConfiguration = %v, want true", wgName, config["EnforceWorkGroupConfiguration"])
	}

	outputs := describeStackOutputs(t, srv, stackName)
	if outputs["WorkGroupRef"] != wgName {
		t.Fatalf("Ref(WorkGroup) = %q, want %q", outputs["WorkGroupRef"], wgName)
	}
}

// TestCreateStack_AthenaConfiguration_isNotAcceptedAsAnAlias pins the alpha
// no-shims policy: a template that sets the API member's name (Configuration)
// instead of the schema property (WorkGroupConfiguration) does not get it
// honored. Overcast has no per-property "unrecognised property" diagnostic
// channel (only a resource-type-level one — see fidelity.go), so the
// observable outcome is a workgroup with none of that configuration, exactly as any
// other unrecognised property is ignored.
func TestCreateStack_AthenaConfiguration_isNotAcceptedAsAnAlias(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "athena-config-not-alias-stack"
	const wgName = "cfn-config-alias-workgroup"
	template := `{
  "Resources": {
    "WorkGroup": {
      "Type": "AWS::Athena::WorkGroup",
      "Properties": {
        "Name": "` + wgName + `",
        "Configuration": {
          "ResultConfiguration": {"OutputLocation": "s3://wrong-key-bucket/"}
        }
      }
    }
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// Athena reports the engine version of every workgroup, so the check is
	// that nothing from the template's Configuration arrived.
	if config := getWorkGroupConfiguration(t, srv, wgName); outputLocation(config) != "" {
		t.Fatalf("template key Configuration was honored as an alias for WorkGroupConfiguration: %#v", config)
	}
}
