// The IAM operations a `cdk deploy` reaches through CloudFormation, exercised
// as a direct API caller would (issue #154): CreateRole, PutRolePolicy,
// AttachRolePolicy, CreateInstanceProfile and AddRoleToInstanceProfile, plus
// the refusals AWS's API Reference documents for each.
//
// The DeleteRole and DeleteInstanceProfile ordering rules live in
// iam_delete_conflict_test.go, which owns the DeleteConflict table.
package iam_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const cdkPolicyDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:PutLogEvents","Resource":"*"}]}`

// ─── CreateRole ───────────────────────────────────────────────────────────────

// TestCreateRole_duplicateName pins AWS's EntityAlreadyExists (409): a second
// CreateRole for a name already in the account is refused, not overwritten.
// https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateRole.html
func TestCreateRole_duplicateName(t *testing.T) {
	// Given: a role already exists
	srv := helpers.NewTestServer(t)
	createRole(t, srv, "app-role")

	// When: the same name is created again
	resp := iamCall(t, srv, "CreateRole", url.Values{
		"RoleName":                 {"app-role"},
		"AssumeRolePolicyDocument": {validAssumePolicy},
	})
	defer resp.Body.Close()

	// Then: AWS's EntityAlreadyExists
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertQueryXMLError(t, resp, "EntityAlreadyExists")
}

// TestCreateRole_pathAppearsInTheARN pins the ARN format for a role created
// under a non-default Path — the form CDK's `Role` with a path produces, and
// the string every downstream GetAtt "Arn" consumer sees.
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_identifiers.html
func TestCreateRole_pathAppearsInTheARN(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a role is created under /service-role/
	resp := iamCall(t, srv, "CreateRole", url.Values{
		"RoleName":                 {"app-role"},
		"Path":                     {"/service-role/"},
		"AssumeRolePolicyDocument": {validAssumePolicy},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the ARN carries the path between "role" and the name
	var decoded struct {
		Arn  string `xml:"CreateRoleResult>Role>Arn"`
		Path string `xml:"CreateRoleResult>Role>Path"`
	}
	helpers.DecodeXML(t, resp, &decoded)
	if want := "arn:aws:iam::000000000000:role/service-role/app-role"; decoded.Arn != want {
		t.Errorf("Arn = %q, want %q", decoded.Arn, want)
	}
	if decoded.Path != "/service-role/" {
		t.Errorf("Path = %q, want /service-role/", decoded.Path)
	}
}

// ─── PutRolePolicy / AttachRolePolicy on a missing role ───────────────────────

// TestRolePolicyOperations_unknownRole pins NoSuchEntity (404) for the two
// operations a CloudFormation AWS::IAM::Role translates its Policies and
// ManagedPolicyArns into. A stack whose role failed to create must not have
// its policies land silently.
// https://docs.aws.amazon.com/IAM/latest/APIReference/API_PutRolePolicy.html
// https://docs.aws.amazon.com/IAM/latest/APIReference/API_AttachRolePolicy.html
func TestRolePolicyOperations_unknownRole(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		params url.Values
	}{
		{"PutRolePolicy", "PutRolePolicy", url.Values{
			"RoleName": {"ghost"}, "PolicyName": {"inline"}, "PolicyDocument": {cdkPolicyDoc},
		}},
		{"AttachRolePolicy", "AttachRolePolicy", url.Values{
			"RoleName": {"ghost"}, "PolicyArn": {"arn:aws:iam::aws:policy/ReadOnlyAccess"},
		}},
		{"GetRolePolicy", "GetRolePolicy", url.Values{
			"RoleName": {"ghost"}, "PolicyName": {"inline"},
		}},
		{"ListAttachedRolePolicies", "ListAttachedRolePolicies", url.Values{
			"RoleName": {"ghost"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: no such role
			srv := helpers.NewTestServer(t)

			// When: the operation names it anyway
			resp := iamCall(t, srv, tc.action, tc.params)
			defer resp.Body.Close()

			// Then: NoSuchEntity, 404
			helpers.AssertStatus(t, resp, http.StatusNotFound)
			helpers.AssertQueryXMLError(t, resp, "NoSuchEntity")
		})
	}
}

// TestAttachRolePolicy_awsManagedARNIsAccepted records a deliberate divergence.
//
// AWS answers NoSuchEntity when PolicyArn names a policy that does not exist.
// Overcast does not model the AWS-managed policies (arn:aws:iam::aws:policy/…),
// and every CDK stack attaches several of them — AWSLambdaBasicExecutionRole
// before anything else — so an existence check here would refuse the most
// common template in the ecosystem. The ARN is stored and listed back instead.
// Tracked in docs/dev/compatibility/services/iam.yaml as intentionally partial.
func TestAttachRolePolicy_awsManagedARNIsAccepted(t *testing.T) {
	// Given: a role, and an AWS-managed policy ARN that Overcast has no record of
	srv := helpers.NewTestServer(t)
	createRole(t, srv, "app-role")
	const managed = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"

	// When: it is attached
	resp := iamCall(t, srv, "AttachRolePolicy", url.Values{
		"RoleName": {"app-role"}, "PolicyArn": {managed},
	})
	defer resp.Body.Close()

	// Then: it succeeds, and the attachment is listed back under its ARN
	helpers.AssertStatus(t, resp, http.StatusOK)
	listed := iamCall(t, srv, "ListAttachedRolePolicies", url.Values{"RoleName": {"app-role"}})
	defer listed.Body.Close()
	var decoded struct {
		Arns  []string `xml:"ListAttachedRolePoliciesResult>AttachedPolicies>member>PolicyArn"`
		Names []string `xml:"ListAttachedRolePoliciesResult>AttachedPolicies>member>PolicyName"`
	}
	helpers.DecodeXML(t, listed, &decoded)
	if len(decoded.Arns) != 1 || decoded.Arns[0] != managed {
		t.Fatalf("attached policy ARNs = %v, want [%s]", decoded.Arns, managed)
	}
	if len(decoded.Names) != 1 || decoded.Names[0] != "AWSLambdaBasicExecutionRole" {
		t.Errorf("attached policy names = %v, want [AWSLambdaBasicExecutionRole]", decoded.Names)
	}
}

// ─── Inline policy name ordering ──────────────────────────────────────────────

// TestListInlinePolicies_namesAreOrdered pins a stable order for the three
// inline-policy listings. Every other IAM listing sorts by name in
// internal/services/iam/store.go; these three were built straight from a Go map,
// whose iteration order is deliberately randomised, so a caller diffing two
// responses saw spurious changes.
func TestListInlinePolicies_namesAreOrdered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		params  url.Values
		names   func(t *testing.T, resp *http.Response) []string
		arrange func(t *testing.T, srv *helpers.TestServer, policyName string)
	}{
		{
			name:   "role",
			action: "ListRolePolicies",
			params: url.Values{"RoleName": {"app-role"}},
			names: func(t *testing.T, resp *http.Response) []string {
				var decoded struct {
					Names []string `xml:"ListRolePoliciesResult>PolicyNames>member"`
				}
				helpers.DecodeXML(t, resp, &decoded)
				return decoded.Names
			},
			arrange: func(t *testing.T, srv *helpers.TestServer, policyName string) {
				putRolePolicy(t, srv, "app-role", policyName, cdkPolicyDoc)
			},
		},
		{
			name:   "user",
			action: "ListUserPolicies",
			params: url.Values{"UserName": {"alice"}},
			names: func(t *testing.T, resp *http.Response) []string {
				var decoded struct {
					Names []string `xml:"ListUserPoliciesResult>PolicyNames>member"`
				}
				helpers.DecodeXML(t, resp, &decoded)
				return decoded.Names
			},
			arrange: func(t *testing.T, srv *helpers.TestServer, policyName string) {
				putUserPolicy(t, srv, "alice", policyName, cdkPolicyDoc)
			},
		},
		{
			name:   "group",
			action: "ListGroupPolicies",
			params: url.Values{"GroupName": {"developers"}},
			names: func(t *testing.T, resp *http.Response) []string {
				var decoded struct {
					Names []string `xml:"ListGroupPoliciesResult>PolicyNames>member"`
				}
				helpers.DecodeXML(t, resp, &decoded)
				return decoded.Names
			},
			arrange: func(t *testing.T, srv *helpers.TestServer, policyName string) {
				iamOK(t, srv, "PutGroupPolicy", url.Values{
					"GroupName": {"developers"}, "PolicyName": {policyName},
					"PolicyDocument": {cdkPolicyDoc},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a principal carrying inline policies added out of order
			srv := helpers.NewTestServer(t)
			createRole(t, srv, "app-role")
			createUser(t, srv, "alice")
			createGroup(t, srv, "developers")
			for _, policyName := range []string{"zeta", "alpha", "middle", "beta"} {
				tc.arrange(t, srv, policyName)
			}

			// When: the names are listed
			resp := iamCall(t, srv, tc.action, tc.params)
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)

			// Then: they come back sorted, the same on every call
			got := tc.names(t, resp)
			want := []string{"alpha", "beta", "middle", "zeta"}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("policy names = %v, want %v", got, want)
			}
		})
	}
}

// ─── Instance profiles ────────────────────────────────────────────────────────

// TestCreateInstanceProfile_pathAppearsInTheARN pins the instance-profile ARN
// format, which AWS::IAM::InstanceProfile's GetAtt "Arn" returns verbatim.
func TestCreateInstanceProfile_pathAppearsInTheARN(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a profile is created under a path
	resp := iamCall(t, srv, "CreateInstanceProfile", url.Values{
		"InstanceProfileName": {"app-profile"},
		"Path":                {"/app/"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the ARN carries the path
	var decoded struct {
		Arn string `xml:"CreateInstanceProfileResult>InstanceProfile>Arn"`
	}
	helpers.DecodeXML(t, resp, &decoded)
	if want := "arn:aws:iam::000000000000:instance-profile/app/app-profile"; decoded.Arn != want {
		t.Errorf("Arn = %q, want %q", decoded.Arn, want)
	}
}

// TestAddRoleToInstanceProfile_secondRoleExceedsTheQuota pins AWS's hard
// one-role-per-instance-profile quota: "An instance profile can contain only
// one role, and this quota cannot be increased."
// https://docs.aws.amazon.com/IAM/latest/APIReference/API_AddRoleToInstanceProfile.html
func TestAddRoleToInstanceProfile_secondRoleExceedsTheQuota(t *testing.T) {
	// Given: a profile that already holds a role
	srv := helpers.NewTestServer(t)
	createInstanceProfile(t, srv, "app-profile")
	createRole(t, srv, "first-role")
	createRole(t, srv, "second-role")
	iamOK(t, srv, "AddRoleToInstanceProfile", url.Values{
		"InstanceProfileName": {"app-profile"}, "RoleName": {"first-role"},
	})

	// When: a different role is added to the same profile
	resp := iamCall(t, srv, "AddRoleToInstanceProfile", url.Values{
		"InstanceProfileName": {"app-profile"}, "RoleName": {"second-role"},
	})
	defer resp.Body.Close()

	// Then: LimitExceeded (409), with AWS's quota message
	helpers.AssertStatus(t, resp, http.StatusConflict)
	var errResp struct {
		Error struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	helpers.DecodeXML(t, resp, &errResp)
	if errResp.Error.Code != "LimitExceeded" {
		t.Errorf("error code = %q, want LimitExceeded", errResp.Error.Code)
	}
	if want := "Cannot exceed quota for InstanceSessionsPerInstanceProfile: 1"; errResp.Error.Message != want {
		t.Errorf("message = %q, want %q", errResp.Error.Message, want)
	}

	// And: the original role is untouched
	if got := instanceProfileRoles(t, srv, "app-profile"); len(got) != 1 || got[0] != "first-role" {
		t.Errorf("profile roles = %v, want [first-role]", got)
	}
}

// TestAddRoleToInstanceProfile_replacingTheRoleIsAllowed proves the quota is
// not a one-shot lock: AWS's own guidance is to "remove the existing role and
// then add a different role", which is exactly the sequence CloudFormation
// emits for a Roles change on AWS::IAM::InstanceProfile.
func TestAddRoleToInstanceProfile_replacingTheRoleIsAllowed(t *testing.T) {
	// Given: a profile holding one role
	srv := helpers.NewTestServer(t)
	createInstanceProfile(t, srv, "app-profile")
	createRole(t, srv, "first-role")
	createRole(t, srv, "second-role")
	iamOK(t, srv, "AddRoleToInstanceProfile", url.Values{
		"InstanceProfileName": {"app-profile"}, "RoleName": {"first-role"},
	})

	// When: the role is removed and a different one added
	iamOK(t, srv, "RemoveRoleFromInstanceProfile", url.Values{
		"InstanceProfileName": {"app-profile"}, "RoleName": {"first-role"},
	})
	iamOK(t, srv, "AddRoleToInstanceProfile", url.Values{
		"InstanceProfileName": {"app-profile"}, "RoleName": {"second-role"},
	})

	// Then: the profile holds the replacement
	if got := instanceProfileRoles(t, srv, "app-profile"); len(got) != 1 || got[0] != "second-role" {
		t.Errorf("profile roles = %v, want [second-role]", got)
	}
}

// TestAddRoleToInstanceProfile_unknownEntity pins NoSuchEntity for both of the
// two entities the call names.
func TestAddRoleToInstanceProfile_unknownEntity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params url.Values
	}{
		{"unknown profile", url.Values{"InstanceProfileName": {"ghost"}, "RoleName": {"app-role"}}},
		{"unknown role", url.Values{"InstanceProfileName": {"app-profile"}, "RoleName": {"ghost"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: one of the two entities exists
			srv := helpers.NewTestServer(t)
			createInstanceProfile(t, srv, "app-profile")
			createRole(t, srv, "app-role")

			// When: the call names something that does not
			resp := iamCall(t, srv, "AddRoleToInstanceProfile", tc.params)
			defer resp.Body.Close()

			// Then: NoSuchEntity, 404
			helpers.AssertStatus(t, resp, http.StatusNotFound)
			helpers.AssertQueryXMLError(t, resp, "NoSuchEntity")
		})
	}
}

// instanceProfileRoles returns the role names GetInstanceProfile reports.
func instanceProfileRoles(t *testing.T, srv *helpers.TestServer, profile string) []string {
	t.Helper()
	resp := iamCall(t, srv, "GetInstanceProfile", url.Values{"InstanceProfileName": {profile}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var decoded struct {
		Roles []string `xml:"GetInstanceProfileResult>InstanceProfile>Roles>member>RoleName"`
	}
	helpers.DecodeXML(t, resp, &decoded)
	return decoded.Roles
}
