// iam_policy_document_encoding_test.go pins issue #2180: IAM returns every
// policy document in a response URL-encoded per RFC 3986 — the role's trust
// policy on CreateRole/GetRole/ListRoles (and every other Role it returns),
// and each inline and trust document in GetAccountAuthorizationDetails.
//
// Per the IAM API Reference (API_Role.html, AssumeRolePolicyDocument; and
// API_RoleDetail.html, API_PolicyDetail.html): "The policy document returned
// in this structure is URL-encoded compliant with RFC 3986". botocore decodes
// these on the client; the Go, JavaScript, Java, Rust and .NET SDKs do not.
// So the tests read the raw Query-protocol XML body — never an SDK — or they
// could not tell the two forms apart.
package iam_test

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// spacedTrustPolicy carries whitespace so the tests also pin that a space
// goes out as %20 (RFC 3986) and not as form encoding's "+".
const spacedTrustPolicy = `{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}]}`

// spacedIdentityPolicy is an identity policy with whitespace, for the same reason.
const spacedIdentityPolicy = `{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::bucket/key with space"}]}`

// assertRFC3986PolicyDocument fails unless raw is the RFC 3986 percent-encoding
// of want: not itself JSON, no "+" for a space, and decoding it once gives the
// document the caller sent, byte for byte.
func assertRFC3986PolicyDocument(t *testing.T, member, raw, want string) {
	t.Helper()
	if raw == "" {
		t.Fatalf("%s: missing from the response", member)
	}
	if json.Valid([]byte(raw)) {
		t.Errorf("%s is returned as raw JSON; AWS URL-encodes it per RFC 3986: %q", member, raw)
		return
	}
	if strings.Contains(raw, "+") {
		t.Errorf("%s encodes a space as '+' (form encoding), not %%20 (RFC 3986): %q", member, raw)
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		t.Errorf("%s is not valid percent-encoding: %v; raw %q", member, err, raw)
		return
	}
	if decoded != want {
		t.Errorf("%s decodes to %q, want the stored document %q", member, decoded, want)
	}
}

// createRoleWithTrust creates a role with the given trust policy and returns
// the raw CreateRole body.
func createRoleWithTrust(t *testing.T, srv *helpers.TestServer, name, trust string) string {
	t.Helper()
	resp := iamCall(t, srv, "CreateRole", url.Values{
		"RoleName":                 {name},
		"AssumeRolePolicyDocument": {trust},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	return helpers.ReadBody(t, resp)
}

func TestCreateRole_trustPolicyIsURLEncoded(t *testing.T) {
	// Given: a server
	srv := helpers.NewTestServer(t)

	// When: a role is created
	body := createRoleWithTrust(t, srv, "enc-role", spacedTrustPolicy)

	// Then: the returned Role.AssumeRolePolicyDocument is RFC 3986 encoded
	var out struct {
		Document string `xml:"CreateRoleResult>Role>AssumeRolePolicyDocument"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal CreateRole: %v\n%s", err, body)
	}
	assertRFC3986PolicyDocument(t, "CreateRole Role.AssumeRolePolicyDocument", out.Document, spacedTrustPolicy)
}

func TestGetRole_trustPolicyIsURLEncoded(t *testing.T) {
	// Given: a role
	srv := helpers.NewTestServer(t)
	createRoleWithTrust(t, srv, "enc-role", spacedTrustPolicy)

	// When: GetRole is called
	resp := iamCall(t, srv, "GetRole", url.Values{"RoleName": {"enc-role"}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)

	// Then: the trust policy is RFC 3986 encoded
	var out struct {
		Document string `xml:"GetRoleResult>Role>AssumeRolePolicyDocument"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal GetRole: %v\n%s", err, body)
	}
	assertRFC3986PolicyDocument(t, "GetRole Role.AssumeRolePolicyDocument", out.Document, spacedTrustPolicy)
}

func TestGetRole_updatedTrustPolicyIsURLEncoded(t *testing.T) {
	// Given: a role whose trust policy was replaced by UpdateAssumeRolePolicy
	srv := helpers.NewTestServer(t)
	createRoleWithTrust(t, srv, "enc-role", validAssumePolicy)
	up := iamCall(t, srv, "UpdateAssumeRolePolicy", url.Values{
		"RoleName":       {"enc-role"},
		"PolicyDocument": {spacedTrustPolicy},
	})
	defer up.Body.Close()
	helpers.AssertStatus(t, up, http.StatusOK)

	// When: GetRole is called
	resp := iamCall(t, srv, "GetRole", url.Values{"RoleName": {"enc-role"}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)

	// Then: the new trust policy comes back encoded, and decodes to what was sent
	var out struct {
		Document string `xml:"GetRoleResult>Role>AssumeRolePolicyDocument"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal GetRole: %v\n%s", err, body)
	}
	assertRFC3986PolicyDocument(t, "GetRole Role.AssumeRolePolicyDocument", out.Document, spacedTrustPolicy)
}

func TestListRoles_trustPolicyIsURLEncoded(t *testing.T) {
	// Given: a role
	srv := helpers.NewTestServer(t)
	createRoleWithTrust(t, srv, "enc-role", spacedTrustPolicy)

	// When: ListRoles is called
	resp := iamCall(t, srv, "ListRoles", nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)

	// Then: each listed role's trust policy is RFC 3986 encoded
	var out struct {
		Roles []struct {
			RoleName string `xml:"RoleName"`
			Document string `xml:"AssumeRolePolicyDocument"`
		} `xml:"ListRolesResult>Roles>member"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal ListRoles: %v\n%s", err, body)
	}
	if len(out.Roles) != 1 || out.Roles[0].RoleName != "enc-role" {
		t.Fatalf("ListRoles roles = %+v, want just enc-role", out.Roles)
	}
	assertRFC3986PolicyDocument(t, "ListRoles Roles[].AssumeRolePolicyDocument", out.Roles[0].Document, spacedTrustPolicy)
}

func TestGetInstanceProfile_roleTrustPolicyIsURLEncoded(t *testing.T) {
	// Given: an instance profile holding a role
	srv := helpers.NewTestServer(t)
	createRoleWithTrust(t, srv, "enc-role", spacedTrustPolicy)
	createInstanceProfile(t, srv, "enc-profile")
	add := iamCall(t, srv, "AddRoleToInstanceProfile", url.Values{
		"InstanceProfileName": {"enc-profile"},
		"RoleName":            {"enc-role"},
	})
	defer add.Body.Close()
	helpers.AssertStatus(t, add, http.StatusOK)

	// When: GetInstanceProfile is called
	resp := iamCall(t, srv, "GetInstanceProfile", url.Values{"InstanceProfileName": {"enc-profile"}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)

	// Then: the embedded Role carries its trust policy RFC 3986 encoded, as
	// every Role structure does (API_Role.html)
	var out struct {
		Documents []string `xml:"GetInstanceProfileResult>InstanceProfile>Roles>member>AssumeRolePolicyDocument"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal GetInstanceProfile: %v\n%s", err, body)
	}
	if len(out.Documents) != 1 {
		t.Fatalf("GetInstanceProfile roles = %d, want 1\n%s", len(out.Documents), body)
	}
	assertRFC3986PolicyDocument(t, "GetInstanceProfile Roles[].AssumeRolePolicyDocument", out.Documents[0], spacedTrustPolicy)
}

func TestGetAccountAuthorizationDetails_policyDocumentsAreURLEncoded(t *testing.T) {
	// Given: a role with a trust policy and an inline policy, a user with an
	// inline policy and a group with an inline policy
	srv := helpers.NewTestServer(t)
	createRoleWithTrust(t, srv, "enc-role", spacedTrustPolicy)
	putRolePolicy(t, srv, "enc-role", "role-inline", spacedIdentityPolicy)
	createUser(t, srv, "enc-user")
	putUserPolicy(t, srv, "enc-user", "user-inline", spacedIdentityPolicy)
	createGroup(t, srv, "enc-group")
	gp := iamCall(t, srv, "PutGroupPolicy", url.Values{
		"GroupName":      {"enc-group"},
		"PolicyName":     {"group-inline"},
		"PolicyDocument": {spacedIdentityPolicy},
	})
	defer gp.Body.Close()
	helpers.AssertStatus(t, gp, http.StatusOK)

	// When: GetAccountAuthorizationDetails is called
	resp := iamCall(t, srv, "GetAccountAuthorizationDetails", nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)

	// Then: every policy document in it is RFC 3986 encoded
	type inline struct {
		PolicyName     string `xml:"PolicyName"`
		PolicyDocument string `xml:"PolicyDocument"`
	}
	var out struct {
		Users []struct {
			UserName string   `xml:"UserName"`
			Policies []inline `xml:"UserPolicyList>member"`
		} `xml:"GetAccountAuthorizationDetailsResult>UserDetailList>member"`
		Groups []struct {
			GroupName string   `xml:"GroupName"`
			Policies  []inline `xml:"GroupPolicyList>member"`
		} `xml:"GetAccountAuthorizationDetailsResult>GroupDetailList>member"`
		Roles []struct {
			RoleName string   `xml:"RoleName"`
			Trust    string   `xml:"AssumeRolePolicyDocument"`
			Policies []inline `xml:"RolePolicyList>member"`
		} `xml:"GetAccountAuthorizationDetailsResult>RoleDetailList>member"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal GetAccountAuthorizationDetails: %v\n%s", err, body)
	}
	if len(out.Roles) != 1 || len(out.Roles[0].Policies) != 1 {
		t.Fatalf("RoleDetailList = %+v, want enc-role with one inline policy", out.Roles)
	}
	assertRFC3986PolicyDocument(t, "RoleDetail.AssumeRolePolicyDocument", out.Roles[0].Trust, spacedTrustPolicy)
	assertRFC3986PolicyDocument(t, "RoleDetail.RolePolicyList[].PolicyDocument", out.Roles[0].Policies[0].PolicyDocument, spacedIdentityPolicy)
	if len(out.Users) != 1 || len(out.Users[0].Policies) != 1 {
		t.Fatalf("UserDetailList = %+v, want enc-user with one inline policy", out.Users)
	}
	assertRFC3986PolicyDocument(t, "UserDetail.UserPolicyList[].PolicyDocument", out.Users[0].Policies[0].PolicyDocument, spacedIdentityPolicy)
	if len(out.Groups) != 1 || len(out.Groups[0].Policies) != 1 {
		t.Fatalf("GroupDetailList = %+v, want enc-group with one inline policy", out.Groups)
	}
	assertRFC3986PolicyDocument(t, "GroupDetail.GroupPolicyList[].PolicyDocument", out.Groups[0].Policies[0].PolicyDocument, spacedIdentityPolicy)
}

func TestGetRolePolicy_documentIsURLEncoded(t *testing.T) {
	// Given: a role with an inline policy (already encoded before #2180; pinned
	// here alongside the rest so the family stays consistent)
	srv := helpers.NewTestServer(t)
	createRoleWithTrust(t, srv, "enc-role", spacedTrustPolicy)
	putRolePolicy(t, srv, "enc-role", "role-inline", spacedIdentityPolicy)

	// When: GetRolePolicy is called
	resp := iamCall(t, srv, "GetRolePolicy", url.Values{
		"RoleName":   {"enc-role"},
		"PolicyName": {"role-inline"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)

	// Then: the document is RFC 3986 encoded
	var out struct {
		Document string `xml:"GetRolePolicyResult>PolicyDocument"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal GetRolePolicy: %v\n%s", err, body)
	}
	assertRFC3986PolicyDocument(t, "GetRolePolicy PolicyDocument", out.Document, spacedIdentityPolicy)
}

func TestSimulatePrincipalPolicy_roleWithInlinePolicyAfterEncoding(t *testing.T) {
	// Given: a role whose inline policy allows s3:GetObject on a key with a
	// space — the stored document must stay raw for the evaluator even
	// though every response now encodes it
	srv := helpers.NewTestServer(t)
	body := createRoleWithTrust(t, srv, "enc-role", spacedTrustPolicy)
	putRolePolicy(t, srv, "enc-role", "role-inline", spacedIdentityPolicy)
	var created struct {
		Arn string `xml:"CreateRoleResult>Role>Arn"`
	}
	if err := xml.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("unmarshal CreateRole: %v", err)
	}

	// When: the role is simulated against that object
	resp := iamCall(t, srv, "SimulatePrincipalPolicy", url.Values{
		"PolicySourceArn":       {created.Arn},
		"ActionNames.member.1":  {"s3:GetObject"},
		"ResourceArns.member.1": {"arn:aws:s3:::bucket/key with space"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	simBody := helpers.ReadBody(t, resp)

	// Then: the decision is allowed
	if !strings.Contains(simBody, "<EvalDecision>allowed</EvalDecision>") {
		t.Errorf("SimulatePrincipalPolicy decision is not allowed:\n%s", simBody)
	}
}
