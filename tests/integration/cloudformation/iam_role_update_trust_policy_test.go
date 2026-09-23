package cloudformation_test

// Integration coverage for #1982: iamRoleHandler.Update re-marshalled
// AssumeRolePolicyDocument with json.Marshal on both the new and the old
// property, rather than rendering it through policyDocumentJSON as
// iamRoleHandler.Create does (the #1717 contract: a string-form document is
// passed through untouched, an object-form one is marshalled). A trust
// policy supplied as a JSON string was therefore double-encoded on update:
// UpdateAssumeRolePolicy received a quoted JSON string instead of a
// document, which IAM's policy-document check (#1852) now refuses outright
// as MalformedPolicyDocument.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestUpdateStack_IAMRole_stringFormTrustPolicy_doesNotDoubleEncode(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "iam-role-string-trust-update"
	const roleName = "cfn-string-trust-update-role"

	template := func(service string) string {
		doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + service + `"},"Action":"sts:AssumeRole"}]}`
		escaped := strings.ReplaceAll(doc, `"`, `\"`)
		return `{"Resources": {"Role": {"Type": "AWS::IAM::Role", "Properties": {
      "RoleName": "` + roleName + `",
      "AssumeRolePolicyDocument": "` + escaped + `"
    }}}}`
	}

	// Given: a role created with a string-form trust policy (CloudFormation
	// types AssumeRolePolicyDocument as `Json`, which accepts either form).
	createIAMStack(t, srv, stackName, template("ec2.amazonaws.com"))

	// When: the stack updates that same string-form property to a different
	// principal. updateIAMStack fails the test with the stack's failure
	// reason if the update does not reach UPDATE_COMPLETE — which, before
	// this fix, it never did: the re-marshalled string reached
	// UpdateAssumeRolePolicy quoted, and IAM rejected it as
	// MalformedPolicyDocument.
	updateIAMStack(t, srv, stackName, template("lambda.amazonaws.com"))

	// Then: GetRole returns the new document, decodable, with the new
	// principal — not the old one, and not a quoted, double-encoded string.
	resp := iamQuery(t, srv, "GetRole", url.Values{"RoleName": {roleName}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Document string `xml:"GetRoleResult>Role>AssumeRolePolicyDocument"`
	}
	helpers.DecodeXML(t, resp, &out)

	var doc struct {
		Statement []struct {
			Principal struct {
				Service string `json:"Service"`
			} `json:"Principal"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(out.Document), &doc); err != nil {
		t.Fatalf("AssumeRolePolicyDocument did not decode as a policy document (looks double-encoded): %v; got %q", err, out.Document)
	}
	if len(doc.Statement) != 1 || doc.Statement[0].Principal.Service != "lambda.amazonaws.com" {
		t.Fatalf("AssumeRolePolicyDocument = %q, want the updated principal lambda.amazonaws.com", out.Document)
	}
}
