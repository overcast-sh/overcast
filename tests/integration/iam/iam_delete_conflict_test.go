package iam_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// deleteConflictCase is one dependency that AWS refuses to delete through.
//
// setup creates the target entity plus the blocking dependency; remove clears
// the dependency through a modeled IAM API; deleteParams is the delete call
// under test. The table asserts the same three things for each: a 409
// DeleteConflict carrying AWS's message, then a successful delete once the
// dependency is gone.
type deleteConflictCase struct {
	name         string
	action       string
	setup        func(t *testing.T, srv *helpers.TestServer)
	remove       func(t *testing.T, srv *helpers.TestServer)
	deleteParams url.Values
	wantMessage  string
}

const conflictPolicyDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`

func deleteConflictCases() []deleteConflictCase {
	return []deleteConflictCase{
		// ── Users ──────────────────────────────────────────────────────────
		{
			name:   "user with an access key",
			action: "DeleteUser",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createUser(t, srv, "alice")
				iamOK(t, srv, "CreateAccessKey", url.Values{"UserName": {"alice"}})
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DeleteAccessKey", url.Values{
					"UserName": {"alice"}, "AccessKeyId": {accessKeyIDFor(t, srv, "alice")},
				})
			},
			deleteParams: url.Values{"UserName": {"alice"}},
			wantMessage:  "Cannot delete entity, must delete access keys first.",
		},
		{
			name:   "user with an inline policy",
			action: "DeleteUser",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createUser(t, srv, "alice")
				putUserPolicy(t, srv, "alice", "inline", conflictPolicyDoc)
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DeleteUserPolicy", url.Values{
					"UserName": {"alice"}, "PolicyName": {"inline"},
				})
			},
			deleteParams: url.Values{"UserName": {"alice"}},
			wantMessage:  "Cannot delete entity, must delete policies first.",
		},
		{
			name:   "user with an attached managed policy",
			action: "DeleteUser",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createUser(t, srv, "alice")
				arn := createPolicy(t, srv, "managed")
				attachUserPolicy(t, srv, "alice", arn)
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DetachUserPolicy", url.Values{
					"UserName":  {"alice"},
					"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"},
				})
			},
			deleteParams: url.Values{"UserName": {"alice"}},
			wantMessage:  "Cannot delete entity, must detach all policies first.",
		},
		{
			name:   "user in a group",
			action: "DeleteUser",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createUser(t, srv, "alice")
				createGroup(t, srv, "developers")
				addUserToGroup(t, srv, "alice", "developers")
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "RemoveUserFromGroup", url.Values{
					"UserName": {"alice"}, "GroupName": {"developers"},
				})
			},
			deleteParams: url.Values{"UserName": {"alice"}},
			wantMessage:  "Cannot delete entity, must remove users from group first.",
		},

		// ── Roles ──────────────────────────────────────────────────────────
		{
			name:   "role in an instance profile",
			action: "DeleteRole",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createRole(t, srv, "app-role")
				iamOK(t, srv, "CreateInstanceProfile", url.Values{"InstanceProfileName": {"app-profile"}})
				iamOK(t, srv, "AddRoleToInstanceProfile", url.Values{
					"InstanceProfileName": {"app-profile"}, "RoleName": {"app-role"},
				})
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "RemoveRoleFromInstanceProfile", url.Values{
					"InstanceProfileName": {"app-profile"}, "RoleName": {"app-role"},
				})
			},
			deleteParams: url.Values{"RoleName": {"app-role"}},
			wantMessage:  "Cannot delete entity, must remove roles from instance profile first.",
		},
		{
			name:   "role with an inline policy",
			action: "DeleteRole",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createRole(t, srv, "app-role")
				putRolePolicy(t, srv, "app-role", "inline", conflictPolicyDoc)
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DeleteRolePolicy", url.Values{
					"RoleName": {"app-role"}, "PolicyName": {"inline"},
				})
			},
			deleteParams: url.Values{"RoleName": {"app-role"}},
			wantMessage:  "Cannot delete entity, must delete policies first.",
		},
		{
			name:   "role with an attached managed policy",
			action: "DeleteRole",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createRole(t, srv, "app-role")
				arn := createPolicy(t, srv, "managed")
				iamOK(t, srv, "AttachRolePolicy", url.Values{
					"RoleName": {"app-role"}, "PolicyArn": {arn},
				})
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DetachRolePolicy", url.Values{
					"RoleName":  {"app-role"},
					"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"},
				})
			},
			deleteParams: url.Values{"RoleName": {"app-role"}},
			wantMessage:  "Cannot delete entity, must detach all policies first.",
		},

		// ── Groups ─────────────────────────────────────────────────────────
		{
			name:   "group with a member",
			action: "DeleteGroup",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createGroup(t, srv, "developers")
				createUser(t, srv, "alice")
				addUserToGroup(t, srv, "alice", "developers")
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "RemoveUserFromGroup", url.Values{
					"UserName": {"alice"}, "GroupName": {"developers"},
				})
			},
			deleteParams: url.Values{"GroupName": {"developers"}},
			wantMessage:  "Cannot delete entity, must remove users from group first.",
		},
		{
			name:   "group with an inline policy",
			action: "DeleteGroup",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createGroup(t, srv, "developers")
				putGroupPolicy(t, srv, "developers", "inline", conflictPolicyDoc)
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DeleteGroupPolicy", url.Values{
					"GroupName": {"developers"}, "PolicyName": {"inline"},
				})
			},
			deleteParams: url.Values{"GroupName": {"developers"}},
			wantMessage:  "Cannot delete entity, must delete policies first.",
		},
		{
			name:   "group with an attached managed policy",
			action: "DeleteGroup",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createGroup(t, srv, "developers")
				arn := createPolicy(t, srv, "managed")
				iamOK(t, srv, "AttachGroupPolicy", url.Values{
					"GroupName": {"developers"}, "PolicyArn": {arn},
				})
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DetachGroupPolicy", url.Values{
					"GroupName": {"developers"},
					"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"},
				})
			},
			deleteParams: url.Values{"GroupName": {"developers"}},
			wantMessage:  "Cannot delete entity, must detach all policies first.",
		},

		// ── Instance profiles ──────────────────────────────────────────────
		//
		// "Deletes the specified instance profile. The instance profile must
		// not have an associated role."
		// https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeleteInstanceProfile.html
		{
			name:   "instance profile holding a role",
			action: "DeleteInstanceProfile",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createRole(t, srv, "app-role")
				iamOK(t, srv, "CreateInstanceProfile", url.Values{"InstanceProfileName": {"app-profile"}})
				iamOK(t, srv, "AddRoleToInstanceProfile", url.Values{
					"InstanceProfileName": {"app-profile"}, "RoleName": {"app-role"},
				})
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "RemoveRoleFromInstanceProfile", url.Values{
					"InstanceProfileName": {"app-profile"}, "RoleName": {"app-role"},
				})
			},
			deleteParams: url.Values{"InstanceProfileName": {"app-profile"}},
			wantMessage:  "Cannot delete entity, must remove roles from instance profile first.",
		},

		// ── Managed policies ───────────────────────────────────────────────
		{
			name:   "managed policy attached to a role",
			action: "DeletePolicy",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createRole(t, srv, "app-role")
				arn := createPolicy(t, srv, "managed")
				iamOK(t, srv, "AttachRolePolicy", url.Values{
					"RoleName": {"app-role"}, "PolicyArn": {arn},
				})
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DetachRolePolicy", url.Values{
					"RoleName":  {"app-role"},
					"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"},
				})
			},
			deleteParams: url.Values{"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"}},
			wantMessage:  "Cannot delete a policy attached to entities.",
		},
		{
			name:   "managed policy attached to a user",
			action: "DeletePolicy",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createUser(t, srv, "alice")
				arn := createPolicy(t, srv, "managed")
				attachUserPolicy(t, srv, "alice", arn)
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DetachUserPolicy", url.Values{
					"UserName":  {"alice"},
					"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"},
				})
			},
			deleteParams: url.Values{"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"}},
			wantMessage:  "Cannot delete a policy attached to entities.",
		},
		{
			name:   "managed policy attached to a group",
			action: "DeletePolicy",
			setup: func(t *testing.T, srv *helpers.TestServer) {
				createGroup(t, srv, "developers")
				arn := createPolicy(t, srv, "managed")
				iamOK(t, srv, "AttachGroupPolicy", url.Values{
					"GroupName": {"developers"}, "PolicyArn": {arn},
				})
			},
			remove: func(t *testing.T, srv *helpers.TestServer) {
				iamOK(t, srv, "DetachGroupPolicy", url.Values{
					"GroupName": {"developers"},
					"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"},
				})
			},
			deleteParams: url.Values{"PolicyArn": {"arn:aws:iam::000000000000:policy/managed"}},
			wantMessage:  "Cannot delete a policy attached to entities.",
		},
	}
}

// accessKeyIDFor returns the user's first access key ID.
func accessKeyIDFor(t *testing.T, srv *helpers.TestServer, user string) string {
	t.Helper()
	resp := iamCall(t, srv, "ListAccessKeys", url.Values{"UserName": {user}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var decoded struct {
		IDs []string `xml:"ListAccessKeysResult>AccessKeyMetadata>member>AccessKeyId"`
	}
	helpers.DecodeXML(t, resp, &decoded)
	if len(decoded.IDs) == 0 {
		t.Fatal("ListAccessKeys returned no keys")
	}
	return decoded.IDs[0]
}

func TestDelete_conflictsWithDependents(t *testing.T) {
	for _, tc := range deleteConflictCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the entity exists with a dependency still attached
			srv := helpers.NewTestServer(t)
			tc.setup(t, srv)

			// When: the entity is deleted
			resp := iamCall(t, srv, tc.action, tc.deleteParams)
			defer resp.Body.Close()

			// Then: AWS's DeleteConflict, with the message naming what to clear
			helpers.AssertStatus(t, resp, http.StatusConflict)
			var errResp struct {
				Error struct {
					Code    string `xml:"Code"`
					Message string `xml:"Message"`
				} `xml:"Error"`
			}
			helpers.DecodeXML(t, resp, &errResp)
			if errResp.Error.Code != "DeleteConflict" {
				t.Errorf("error code = %q, want DeleteConflict", errResp.Error.Code)
			}
			if errResp.Error.Message != tc.wantMessage {
				t.Errorf("message = %q, want %q", errResp.Error.Message, tc.wantMessage)
			}

			// And: the entity is still there — a refused delete deletes nothing
			// When: the dependency is removed through a modeled IAM API
			tc.remove(t, srv)

			// Then: the delete succeeds
			ok := iamCall(t, srv, tc.action, tc.deleteParams)
			defer ok.Body.Close()
			helpers.AssertStatus(t, ok, http.StatusOK)
		})
	}
}

// TestDelete_conflictLeavesEntityIntact proves the refusal is not a partial
// delete: the entity and its dependency both survive a rejected call.
func TestDelete_conflictLeavesEntityIntact(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "alice")
	putUserPolicy(t, srv, "alice", "inline", conflictPolicyDoc)

	resp := iamCall(t, srv, "DeleteUser", url.Values{"UserName": {"alice"}})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusConflict)

	got := iamCall(t, srv, "GetUser", url.Values{"UserName": {"alice"}})
	defer got.Body.Close()
	helpers.AssertStatus(t, got, http.StatusOK)

	pol := iamCall(t, srv, "GetUserPolicy", url.Values{
		"UserName": {"alice"}, "PolicyName": {"inline"},
	})
	defer pol.Body.Close()
	helpers.AssertStatus(t, pol, http.StatusOK)
}

// TestDelete_notFoundBeatsConflict pins the validation order: an entity that
// does not exist is NoSuchEntity, never DeleteConflict.
func TestDelete_notFoundBeatsConflict(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := iamCall(t, srv, "DeleteUser", url.Values{"UserName": {"ghost"}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertQueryXMLError(t, resp, "NoSuchEntity")
}
