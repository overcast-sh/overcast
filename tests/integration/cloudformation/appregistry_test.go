package cloudformation_test

// appregistry_test.go — #1763: AWS::ServiceCatalogAppRegistry::Application's
// Create used to forward Tags with no stack-tag merge, and Update never
// reconciled a tag change at all (the type sat in stackTagPropagationExclusions
// for exactly that reason). Also covers the two resource types that had no
// CFN handler before this change — AttributeGroup and
// AttributeGroupAssociation — even though the underlying AppRegistry service
// operations they dispatch to already existed.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// appregistryGet issues an AppRegistry REST-JSON GET at one of the service's
// modeled bindings, signed as servicecatalog.
func appregistryGet(t *testing.T, srv *helpers.TestServer, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=test/20250101/us-east-1/servicecatalog/aws4_request, SignedHeaders=host, Signature=fake")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("AppRegistry GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("AppRegistry GET %s: decode: %v", path, err)
	}
	return resp.StatusCode, body
}

// appregistryListTags reads the shared ARN-keyed tag store's view of arn —
// the same store TagResource/UntagResource write to (#1763's Update
// reconciliation), which is not the same thing GetApplication's own `tags`
// field reports (see the TagResource capability note: "Inert tier — merges
// into the shared ARN-keyed tag store").
func appregistryListTags(t *testing.T, srv *helpers.TestServer, arn string) map[string]string {
	t.Helper()
	status, body := appregistryGet(t, srv, "/tags/"+url.PathEscape(arn))
	if status != http.StatusOK {
		t.Fatalf("ListTagsForResource(%s): HTTP %d: %#v", arn, status, body)
	}
	tags, _ := body["tags"].(map[string]any)
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// TestUpdateStack_AppRegistryApplication_stackTagChangeReconciles asserts
// that a stack-tag-only update reaches AppRegistry's TagResource/
// UntagResource without replacing the application.
func TestUpdateStack_AppRegistryApplication_stackTagChangeReconciles(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "appregistry-app-stack-tags-update"
	const template = `{
  "Resources": {
    "App": {
      "Type": "AWS::ServiceCatalogAppRegistry::Application",
      "Properties": {
        "Name": "stack-tags-app",
        "Tags": {"owner": "resource"}
      }
    }
  },
  "Outputs": {
    "AppId": {"Value": {"Ref": "App"}}
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

	appID := describeStackOutputs(t, srv, stackName)["AppId"]
	arn := fmt.Sprintf("arn:aws:servicecatalog:us-east-1:000000000000:/applications/%s", appID)

	if got := appregistryListTags(t, srv, arn); got["env"] != "dev" || got["owner"] != "resource" {
		t.Fatalf("tags after create = %#v, want env=dev (stack) and owner=resource (resource)", got)
	}

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {template},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	if got := describeStackOutputs(t, srv, stackName)["AppId"]; got != appID {
		t.Fatalf("application was replaced on a tags-only update: before %q, after %q", appID, got)
	}
	if got := appregistryListTags(t, srv, arn); got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}

// TestUpdateStack_AppRegistryApplication_resourceTagRemoved asserts that
// removing a resource-level tag on update actually removes it from the
// shared tag store, not just stops re-adding it.
func TestUpdateStack_AppRegistryApplication_resourceTagRemoved(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "appregistry-app-tag-removed"
	before := `{
  "Resources": {
    "App": {
      "Type": "AWS::ServiceCatalogAppRegistry::Application",
      "Properties": {
        "Name": "tag-removed-app",
        "Tags": {"team": "payments"}
      }
    }
  },
  "Outputs": {"AppId": {"Value": {"Ref": "App"}}}
}`
	after := `{
  "Resources": {
    "App": {
      "Type": "AWS::ServiceCatalogAppRegistry::Application",
      "Properties": {
        "Name": "tag-removed-app"
      }
    }
  },
  "Outputs": {"AppId": {"Value": {"Ref": "App"}}}
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {before}})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")
	appID := describeStackOutputs(t, srv, stackName)["AppId"]
	arn := fmt.Sprintf("arn:aws:servicecatalog:us-east-1:000000000000:/applications/%s", appID)
	if got := appregistryListTags(t, srv, arn); got["team"] != "payments" {
		t.Fatalf("tags after create = %#v, want team=payments", got)
	}

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{"StackName": {stackName}, "TemplateBody": {after}})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	if got := appregistryListTags(t, srv, arn); len(got) != 0 {
		t.Fatalf("tags after removing Tags = %#v, want empty", got)
	}
}

// ── AWS::ServiceCatalogAppRegistry::AttributeGroup /
//    AttributeGroupAssociation ───────────────────────────────────────────

// TestCreateStack_AppRegistryAttributeGroup_provisionsAndAssociates covers
// the two resource types that had no CFN handler at all before #1763: a
// template declaring an AttributeGroup and associating it with an
// Application must produce a real attribute group (readable through
// GetAttributeGroup) and a real association (visible through
// ListAssociatedAttributeGroups), not a synthetic physical ID.
func TestCreateStack_AppRegistryAttributeGroup_provisionsAndAssociates(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "appregistry-attribute-group-stack"
	const template = `{
  "Resources": {
    "App": {
      "Type": "AWS::ServiceCatalogAppRegistry::Application",
      "Properties": {"Name": "ag-app"}
    },
    "Group": {
      "Type": "AWS::ServiceCatalogAppRegistry::AttributeGroup",
      "Properties": {
        "Name": "ag-group",
        "Attributes": {"stage": "prod"},
        "Tags": {"owner": "resource"}
      }
    },
    "Assoc": {
      "Type": "AWS::ServiceCatalogAppRegistry::AttributeGroupAssociation",
      "Properties": {
        "Application": {"Ref": "App"},
        "AttributeGroup": {"Ref": "Group"}
      }
    }
  },
  "Outputs": {
    "AppId": {"Value": {"Ref": "App"}},
    "GroupId": {"Value": {"Ref": "Group"}}
  }
}`

	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	outputs := describeStackOutputs(t, srv, stackName)
	appID, groupID := outputs["AppId"], outputs["GroupId"]
	if appID == "" || groupID == "" {
		t.Fatalf("missing outputs: %#v", outputs)
	}

	status, group := appregistryGet(t, srv, "/attribute-groups/"+groupID)
	if status != http.StatusOK {
		t.Fatalf("GetAttributeGroup: HTTP %d: %#v", status, group)
	}
	if group["name"] != "ag-group" {
		t.Fatalf("attribute group name = %#v, want ag-group", group["name"])
	}
	tags, _ := group["tags"].(map[string]any)
	if tags["owner"] != "resource" {
		t.Fatalf("attribute group tags = %#v, want owner=resource", group["tags"])
	}

	status, assocs := appregistryGet(t, srv, "/applications/"+appID+"/attribute-groups")
	if status != http.StatusOK {
		t.Fatalf("ListAssociatedAttributeGroups: HTTP %d: %#v", status, assocs)
	}
	ids, _ := assocs["attributeGroups"].([]any)
	found := false
	for _, id := range ids {
		if s, ok := id.(string); ok && s == groupID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListAssociatedAttributeGroups(%s) = %#v, want to contain %q", appID, ids, groupID)
	}

	// The association is replacement-only (both properties are "Update
	// requires: Replacement" on real AWS), so deleting the stack must
	// disassociate before the two resources go, not leave a dangling
	// association — verified indirectly by a clean DeleteStack below.
	del := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": {stackName}})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "DELETE_COMPLETE")
}
