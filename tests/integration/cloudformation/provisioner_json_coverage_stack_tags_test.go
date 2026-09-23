package cloudformation_test

// provisioner_json_coverage_stack_tags_test.go — #2060 follow-up: joining
// stackTagPropagationResourceTypes (provisioner.go) only for a handler whose
// Update actually reconciles the effective tags in place. Each test below
// proves that for one of the seven types the Tags-forwarding fix newly
// registered there: a stack-tag-only update (the template's own resource
// properties, including its own Tags, are byte-for-byte identical — only the
// tags passed to CreateStack/UpdateStack itself change) still reaches the
// resource and reconciles its tags, the same proof
// cloudtrail_stack_tags_test.go and transfer_stack_tags_test.go already carry
// for the resource types #1310 fixed.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// decodeTagList reads a {"Tags": [{"Key":…, "Value":…}, …]} response body —
// ACM, Athena and Shield's ListTags*/ListTagsForResource shape — into a
// key/value map, asserting a 200 status along the way.
func decodeTagList(t *testing.T, resp *http.Response) map[string]string {
	t.Helper()
	return decodeTagListField(t, resp, "Tags")
}

// decodeTagListField is decodeTagList generalised over the response's field
// name, for OpenSearch's ListTags, which answers "TagList" instead of "Tags".
func decodeTagListField(t *testing.T, resp *http.Response, field string) map[string]string {
	t.Helper()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string][]struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode tag list: %v", err)
	}
	out := make(map[string]string, len(result[field]))
	for _, tag := range result[field] {
		out[tag.Key] = tag.Value
	}
	return out
}

// ── AWS::CertificateManager::Certificate ────────────────────────────────────

func TestUpdateStack_ACMCertificate_stackTagChangeReconcilesWithoutReplacement(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "acm-stack-tags-update"
	template := `{
  "Resources": {
    "Cert": {
      "Type": "AWS::CertificateManager::Certificate",
      "Properties": {
        "DomainName": "stack-tags.example.com",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  },
  "Outputs": {
    "CertArn": {"Value": {"Ref": "Cert"}}
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"dev"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")
	arn := describeStackOutputs(t, srv, stackName)["CertArn"]

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: the certificate is reconciled in place, not replaced — same ARN —
	// and a tags-only change must not force replacement.
	newArn := describeStackOutputs(t, srv, stackName)["CertArn"]
	if newArn != arn {
		t.Fatalf("Certificate ARN changed (%q -> %q) on a tags-only update; expected in-place reconciliation", arn, newArn)
	}

	resp := acmJSONCall(t, srv, "ListTagsForCertificate", map[string]any{"CertificateArn": arn})
	defer resp.Body.Close()
	got := decodeTagList(t, resp)
	if got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled certificate tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}

// ── AWS::Athena::WorkGroup ──────────────────────────────────────────────────

func TestUpdateStack_AthenaWorkGroup_stackTagChangeReconcilesWithoutReplacement(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "athena-stack-tags-update"
	const wgName = "stack-tags-workgroup"
	template := `{
  "Resources": {
    "WorkGroup": {
      "Type": "AWS::Athena::WorkGroup",
      "Properties": {
        "Name": "` + wgName + `",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"dev"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// The physical ID (the workgroup name) is unchanged, since a name change
	// is the only thing that replaces this resource and the name here never
	// changed — a replaced workgroup would 404 on the original name below.
	arn := fmt.Sprintf("arn:aws:athena:us-east-1:000000000000:workgroup/%s", wgName)
	resp := athenaJSONCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer resp.Body.Close()
	got := decodeTagList(t, resp)
	if got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled workgroup tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}

// ── AWS::Shield::Protection ─────────────────────────────────────────────────

func TestUpdateStack_ShieldProtection_stackTagChangeReconcilesWithoutReplacement(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "shield-stack-tags-update"
	template := `{
  "Resources": {
    "Protection": {
      "Type": "AWS::Shield::Protection",
      "Properties": {
        "Name": "stack-tags-protection",
        "ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/stack-tags-lb/abc123",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  },
  "Outputs": {
    "ProtectionId": {"Value": {"Ref": "Protection"}}
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"dev"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")
	protectionID := describeStackOutputs(t, srv, stackName)["ProtectionId"]

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	newProtectionID := describeStackOutputs(t, srv, stackName)["ProtectionId"]
	if newProtectionID != protectionID {
		t.Fatalf("ProtectionId changed (%q -> %q) on a tags-only update; expected in-place reconciliation", protectionID, newProtectionID)
	}

	arn := fmt.Sprintf("arn:aws:shield::000000000000:protection/%s", protectionID)
	resp := shieldJSONCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer resp.Body.Close()
	got := decodeTagList(t, resp)
	if got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled protection tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}

// ── AWS::OpenSearchService::Domain ──────────────────────────────────────────

func TestUpdateStack_OpenSearchDomain_stackTagChangeReconcilesWithoutReplacement(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "opensearch-stack-tags-update"
	const domainName = "stack-tags-domain"
	template := `{
  "Resources": {
    "Domain": {
      "Type": "AWS::OpenSearchService::Domain",
      "Properties": {
        "DomainName": "` + domainName + `",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"dev"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")
	arn := describeStackResourceIDs(t, srv, stackName)["Domain"]

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	newArn := describeStackResourceIDs(t, srv, stackName)["Domain"]
	if newArn != arn {
		t.Fatalf("Domain ARN changed (%q -> %q) on a tags-only update; expected in-place reconciliation", arn, newArn)
	}

	tagsResp := opensearchCFNRequest(t, srv, http.MethodGet, "/2021-01-01/tags?arn="+url.QueryEscape(arn))
	defer tagsResp.Body.Close()
	got := decodeTagListField(t, tagsResp, "TagList")
	if got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled domain tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}

// ── AWS::AppConfig::Application / Environment / ConfigurationProfile ───────

func TestUpdateStack_AppConfig_stackTagChangeReconcilesWithoutReplacement(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "appconfig-stack-tags-update"
	template := `{
  "Resources": {
    "App": {
      "Type": "AWS::AppConfig::Application",
      "Properties": {
        "Name": "stack-tags-app",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    },
    "Env": {
      "Type": "AWS::AppConfig::Environment",
      "Properties": {
        "ApplicationId": {"Ref": "App"},
        "Name": "stack-tags-env",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    },
    "Profile": {
      "Type": "AWS::AppConfig::ConfigurationProfile",
      "Properties": {
        "ApplicationId": {"Ref": "App"},
        "Name": "stack-tags-profile",
        "LocationUri": "hosted",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  },
  "Outputs": {
    "ApplicationId": {"Value": {"Ref": "App"}},
    "EnvironmentId": {"Value": {"Fn::GetAtt": ["Env", "Id"]}},
    "ProfileId": {"Value": {"Fn::GetAtt": ["Profile", "Id"]}}
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"dev"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	outputsBefore := describeStackOutputs(t, srv, stackName)
	appID, envID, profID := outputsBefore["ApplicationId"], outputsBefore["EnvironmentId"], outputsBefore["ProfileId"]

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: none of the three were replaced — same IDs — and each one's tags
	// were reconciled in place.
	outputsAfter := describeStackOutputs(t, srv, stackName)
	if outputsAfter["ApplicationId"] != appID || outputsAfter["EnvironmentId"] != envID || outputsAfter["ProfileId"] != profID {
		t.Fatalf("an AppConfig resource was replaced on a tags-only update: before %#v, after %#v", outputsBefore, outputsAfter)
	}

	appARN := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s", appID)
	if got := appconfigListTags(t, srv, appARN); got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled application tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
	envARN := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s/environment/%s", appID, envID)
	if got := appconfigListTags(t, srv, envARN); got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled environment tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
	profARN := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s/configurationprofile/%s", appID, profID)
	if got := appconfigListTags(t, srv, profARN); got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled configuration profile tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}
