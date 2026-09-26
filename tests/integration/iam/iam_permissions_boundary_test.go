package iam_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// Permissions boundaries: a user's or role's boundary caps what its identity
// policies can grant, so the effective permissions are the intersection of the
// two and an explicit deny in either is final.
//
// AWS reference: IAM User Guide § "Permissions boundaries for IAM entities" and
// § "Policy evaluation logic — evaluating effective permissions with boundaries".

const (
	allowSQSDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`
	allowS3Doc  = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`
	denyS3Doc   = `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":"*","Resource":"*"},` +
		`{"Effect":"Deny","Action":"s3:GetObject","Resource":"*"}]}`
)

// ─── Fixture helpers ─────────────────────────────────────────────────────────

func putUserPermissionsBoundary(t *testing.T, srv *helpers.TestServer, user, boundaryArn string) {
	t.Helper()
	iamOK(t, srv, "PutUserPermissionsBoundary", url.Values{
		"UserName": {user}, "PermissionsBoundary": {boundaryArn},
	})
}

func putRolePermissionsBoundary(t *testing.T, srv *helpers.TestServer, role, boundaryArn string) {
	t.Helper()
	iamOK(t, srv, "PutRolePermissionsBoundary", url.Values{
		"RoleName": {role}, "PermissionsBoundary": {boundaryArn},
	})
}

// seedBoundedUser writes a user record straight to the store, which is the only
// way to arrange a boundary ARN that resolves to nothing: the API refuses to
// attach one that does not exist. accessKeys, when given, make the user
// resolvable by request-time enforcement.
func seedBoundedUser(t *testing.T, srv *helpers.TestServer, name, inlineDoc, boundaryArn string, accessKeys ...string) {
	t.Helper()
	keys := make([]map[string]string, 0, len(accessKeys))
	for _, key := range accessKeys {
		keys = append(keys, map[string]string{"AccessKeyId": key})
	}
	record := map[string]any{
		"UserName":            name,
		"Arn":                 "arn:aws:iam::000000000000:user/" + name,
		"Path":                "/",
		"InlinePolicies":      map[string]string{"inline-1": inlineDoc},
		"PermissionsBoundary": boundaryArn,
		"AccessKeys":          keys,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal user record: %v", err)
	}
	if err := srv.Store.Set(context.Background(), "iam:users", name, string(raw)); err != nil {
		t.Fatalf("seed user record: %v", err)
	}
}

// seedRoleBoundary rewrites a seeded role record with a permissions boundary.
func seedRoleBoundary(t *testing.T, srv *helpers.TestServer, roleName, inlineDoc, boundaryArn string) {
	t.Helper()
	record := map[string]any{
		"RoleName":            roleName,
		"Arn":                 "arn:aws:iam::000000000000:role/" + roleName,
		"InlinePolicies":      map[string]string{"inline-1": inlineDoc},
		"PermissionsBoundary": boundaryArn,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal role record: %v", err)
	}
	if err := srv.Store.Set(context.Background(), "iam:roles", roleName, string(raw)); err != nil {
		t.Fatalf("seed role record: %v", err)
	}
}

// seedRawPolicyRecord writes a managed-policy record verbatim under a policy
// ARN, so a test can arrange one that does not decode.
func seedRawPolicyRecord(t *testing.T, srv *helpers.TestServer, arn, raw string) {
	t.Helper()
	if err := srv.Store.Set(context.Background(), "iam:policies", arn, raw); err != nil {
		t.Fatalf("seed policy record: %v", err)
	}
}

// seedManagedPolicy writes a well-formed managed-policy record under a policy ARN.
func seedManagedPolicy(t *testing.T, srv *helpers.TestServer, arn, document string) {
	t.Helper()
	record := map[string]any{"PolicyName": arn, "Arn": arn, "Document": document}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal policy record: %v", err)
	}
	seedRawPolicyRecord(t, srv, arn, string(raw))
}

type attachedBoundaryXML struct {
	PermissionsBoundaryType string `xml:"PermissionsBoundaryType"`
	PermissionsBoundaryArn  string `xml:"PermissionsBoundaryArn"`
}

type getUserBoundaryResponse struct {
	XMLName xml.Name `xml:"GetUserResponse"`
	User    struct {
		UserName            string               `xml:"UserName"`
		PermissionsBoundary *attachedBoundaryXML `xml:"PermissionsBoundary"`
	} `xml:"GetUserResult>User"`
}

type getRoleBoundaryResponse struct {
	XMLName xml.Name `xml:"GetRoleResponse"`
	Role    struct {
		RoleName            string               `xml:"RoleName"`
		PermissionsBoundary *attachedBoundaryXML `xml:"PermissionsBoundary"`
	} `xml:"GetRoleResult>Role"`
}

func getUserBoundary(t *testing.T, srv *helpers.TestServer, name string) *attachedBoundaryXML {
	t.Helper()
	resp := iamCall(t, srv, "GetUser", url.Values{"UserName": {name}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var decoded getUserBoundaryResponse
	helpers.DecodeXML(t, resp, &decoded)
	return decoded.User.PermissionsBoundary
}

func getRoleBoundary(t *testing.T, srv *helpers.TestServer, name string) *attachedBoundaryXML {
	t.Helper()
	resp := iamCall(t, srv, "GetRole", url.Values{"RoleName": {name}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var decoded getRoleBoundaryResponse
	helpers.DecodeXML(t, resp, &decoded)
	return decoded.Role.PermissionsBoundary
}

// simulateGetObject simulates s3:GetObject for a principal ARN.
func simulateGetObject(t *testing.T, srv *helpers.TestServer, principalArn string) evaluationXML {
	t.Helper()
	results := simulate(t, srv, "SimulatePrincipalPolicy", url.Values{
		"PolicySourceArn":       {principalArn},
		"ActionNames.member.1":  {"s3:GetObject"},
		"ResourceArns.member.1": {"arn:aws:s3:::reports/q1.csv"},
	})
	if len(results) != 1 {
		t.Fatalf("EvaluationResults = %d, want 1", len(results))
	}
	return results[0]
}

// assertBoundaryDecision checks the EvalDecision and the
// PermissionsBoundaryDecisionDetail AWS reports alongside it.
func assertBoundaryDecision(t *testing.T, got evaluationXML, wantDecision string, wantAllowedByBoundary bool) {
	t.Helper()
	if got.EvalDecision != wantDecision {
		t.Fatalf("EvalDecision = %q, want %q", got.EvalDecision, wantDecision)
	}
	if len(got.PermissionsBoundaries) != 1 {
		t.Fatalf("PermissionsBoundaryDecisionDetail = %#v, want exactly one", got.PermissionsBoundaries)
	}
	if got.PermissionsBoundaries[0].AllowedByPermissionsBoundary != wantAllowedByBoundary {
		t.Fatalf("AllowedByPermissionsBoundary = %v, want %v",
			got.PermissionsBoundaries[0].AllowedByPermissionsBoundary, wantAllowedByBoundary)
	}
}

// ─── Boundary round-trip ─────────────────────────────────────────────────────

func TestCreateUser_permissionsBoundary_roundTrips(t *testing.T) {
	// Given: a managed policy to use as a boundary
	srv := helpers.NewTestServer(t)
	arn := createPolicyWithDocument(t, srv, "boundary", allowS3Doc)

	// When: a user is created with that boundary
	resp := iamCall(t, srv, "CreateUser", url.Values{
		"UserName": {"bounded"}, "PermissionsBoundary": {arn},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: GetUser reports it in AWS's AttachedPermissionsBoundary shape
	boundary := getUserBoundary(t, srv, "bounded")
	if boundary == nil {
		t.Fatal("GetUser returned no PermissionsBoundary")
	}
	// The type is the enum's wire value, not its member name: the model names
	// the one member of PermissionsBoundaryAttachmentType "Policy" but gives it
	// the value "PermissionsBoundaryPolicy", and the value is what AWS sends.
	if boundary.PermissionsBoundaryType != "PermissionsBoundaryPolicy" || boundary.PermissionsBoundaryArn != arn {
		t.Fatalf("PermissionsBoundary = %+v, want type PermissionsBoundaryPolicy and arn %s", *boundary, arn)
	}
}

func TestCreateRole_permissionsBoundary_roundTrips(t *testing.T) {
	// Given: a managed policy to use as a boundary
	srv := helpers.NewTestServer(t)
	arn := createPolicyWithDocument(t, srv, "boundary", allowS3Doc)

	// When: a role is created with that boundary
	resp := iamCall(t, srv, "CreateRole", url.Values{
		"RoleName":                 {"bounded-role"},
		"AssumeRolePolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`},
		"PermissionsBoundary":      {arn},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: GetRole reports it, typed with the enum's wire value
	boundary := getRoleBoundary(t, srv, "bounded-role")
	if boundary == nil || boundary.PermissionsBoundaryArn != arn || boundary.PermissionsBoundaryType != "PermissionsBoundaryPolicy" {
		t.Fatalf("PermissionsBoundary = %+v, want type PermissionsBoundaryPolicy and arn %s", boundary, arn)
	}
}

func TestPutUserPermissionsBoundary_unknownPolicy_noSuchEntity(t *testing.T) {
	// Given: a user and no such managed policy
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "bounded")

	// When: a boundary naming a policy that does not exist is attached
	resp := iamCall(t, srv, "PutUserPermissionsBoundary", url.Values{
		"UserName":            {"bounded"},
		"PermissionsBoundary": {"arn:aws:iam::000000000000:policy/ghost"},
	})
	defer resp.Body.Close()

	// Then: AWS refuses rather than storing a boundary that resolves to nothing
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertQueryXMLError(t, resp, "NoSuchEntity")
}

func TestPutUserPermissionsBoundary_unknownUser_noSuchEntity(t *testing.T) {
	// Given: a managed policy but no such user
	srv := helpers.NewTestServer(t)
	arn := createPolicyWithDocument(t, srv, "boundary", allowS3Doc)

	// When: the boundary is attached to a user that does not exist
	resp := iamCall(t, srv, "PutUserPermissionsBoundary", url.Values{
		"UserName": {"ghost"}, "PermissionsBoundary": {arn},
	})
	defer resp.Body.Close()

	// Then: NoSuchEntity
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertQueryXMLError(t, resp, "NoSuchEntity")
}

func TestDeleteUserPermissionsBoundary_noBoundaryAttached_succeeds(t *testing.T) {
	// Given: a user with no boundary
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "unbounded")

	// When: the boundary is removed anyway
	resp := iamCall(t, srv, "DeleteUserPermissionsBoundary", url.Values{"UserName": {"unbounded"}})
	defer resp.Body.Close()

	// Then: the call is idempotent, as on AWS
	helpers.AssertStatus(t, resp, http.StatusOK)
	if boundary := getUserBoundary(t, srv, "unbounded"); boundary != nil {
		t.Fatalf("PermissionsBoundary = %+v, want none", *boundary)
	}
}

func TestDeletePolicy_usedAsPermissionsBoundary_deleteConflict(t *testing.T) {
	// Given: a managed policy attached to a user as its permissions boundary
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "bounded")
	arn := createPolicyWithDocument(t, srv, "boundary", allowS3Doc)
	putUserPermissionsBoundary(t, srv, "bounded", arn)

	// When: the policy is deleted
	resp := iamCall(t, srv, "DeletePolicy", url.Values{"PolicyArn": {arn}})
	defer resp.Body.Close()

	// Then: IAM refuses while it still bounds an entity
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertQueryXMLError(t, resp, "DeleteConflict")
}

// ─── SimulatePrincipalPolicy ─────────────────────────────────────────────────

func TestSimulatePrincipalPolicy_boundaryAllowsIdentityAllow_allowed(t *testing.T) {
	// Given: a user allowed to read, bounded by a policy that also allows S3
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "reader")
	putUserPolicy(t, srv, "reader", "read-reports", allowS3ReadDoc)
	putUserPermissionsBoundary(t, srv, "reader", createPolicyWithDocument(t, srv, "boundary", allowS3Doc))

	// When: the action is simulated
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/reader")

	// Then: the intersection still allows it
	assertBoundaryDecision(t, got, "allowed", true)
}

func TestSimulatePrincipalPolicy_boundaryDoesNotAllow_implicitDeny(t *testing.T) {
	// Given: a user allowed to read, bounded by a policy covering only SQS
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "reader")
	putUserPolicy(t, srv, "reader", "read-reports", allowS3ReadDoc)
	putUserPermissionsBoundary(t, srv, "reader", createPolicyWithDocument(t, srv, "boundary", allowSQSDoc))

	// When: the action is simulated
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/reader")

	// Then: the boundary caps the identity allow, which AWS reports as an
	// implicit deny
	assertBoundaryDecision(t, got, "implicitDeny", false)
}

func TestSimulatePrincipalPolicy_boundaryExplicitDeny_explicitDeny(t *testing.T) {
	// Given: a user allowed to read, bounded by a policy that denies the action
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "reader")
	putUserPolicy(t, srv, "reader", "read-reports", allowS3ReadDoc)
	putUserPermissionsBoundary(t, srv, "reader", createPolicyWithDocument(t, srv, "boundary", denyS3Doc))

	// When: the action is simulated
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/reader")

	// Then: an explicit deny in the boundary is final
	assertBoundaryDecision(t, got, "explicitDeny", false)
}

func TestSimulatePrincipalPolicy_roleBoundaryDoesNotAllow_implicitDeny(t *testing.T) {
	// Given: a role allowed to read, bounded by a policy covering only SQS
	srv := helpers.NewTestServer(t)
	createRole(t, srv, "task-role")
	putRolePolicy(t, srv, "task-role", "role-read", allowS3ReadDoc)
	putRolePermissionsBoundary(t, srv, "task-role", createPolicyWithDocument(t, srv, "boundary", allowSQSDoc))

	// When: the action is simulated
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:role/task-role")

	// Then: roles are bounded exactly as users are
	assertBoundaryDecision(t, got, "implicitDeny", false)
}

func TestSimulatePrincipalPolicy_boundaryReplaced_appliesTheNewBoundary(t *testing.T) {
	// Given: a bounded user whose boundary blocks the action
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "reader")
	putUserPolicy(t, srv, "reader", "read-reports", allowS3ReadDoc)
	putUserPermissionsBoundary(t, srv, "reader", createPolicyWithDocument(t, srv, "narrow", allowSQSDoc))
	assertBoundaryDecision(t, simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/reader"),
		"implicitDeny", false)

	// When: the boundary is replaced with one that permits it
	putUserPermissionsBoundary(t, srv, "reader", createPolicyWithDocument(t, srv, "wide", allowS3Doc))

	// Then: the new boundary decides
	assertBoundaryDecision(t, simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/reader"),
		"allowed", true)
}

func TestSimulatePrincipalPolicy_boundaryRemoved_noBoundaryDetail(t *testing.T) {
	// Given: a bounded user whose boundary blocks the action
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "reader")
	putUserPolicy(t, srv, "reader", "read-reports", allowS3ReadDoc)
	putUserPermissionsBoundary(t, srv, "reader", createPolicyWithDocument(t, srv, "boundary", allowSQSDoc))

	// When: the boundary is removed
	iamOK(t, srv, "DeleteUserPermissionsBoundary", url.Values{"UserName": {"reader"}})

	// Then: the identity policies alone decide, and AWS reports no boundary detail
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/reader")
	if got.EvalDecision != "allowed" {
		t.Fatalf("EvalDecision = %q, want allowed", got.EvalDecision)
	}
	if len(got.PermissionsBoundaries) != 0 {
		t.Fatalf("PermissionsBoundaryDecisionDetail = %#v, want none", got.PermissionsBoundaries)
	}
}

func TestSimulatePrincipalPolicy_suppliedBoundaryReplacesStored_allowed(t *testing.T) {
	// Given: a bounded user whose stored boundary blocks the action
	srv := helpers.NewTestServer(t)
	createUser(t, srv, "reader")
	putUserPolicy(t, srv, "reader", "read-reports", allowS3ReadDoc)
	putUserPermissionsBoundary(t, srv, "reader", createPolicyWithDocument(t, srv, "boundary", allowSQSDoc))

	// When: the caller asks what a different boundary would do — AWS allows only
	// one permissions boundary per simulation, so the supplied one stands in for
	// the stored one
	results := simulate(t, srv, "SimulatePrincipalPolicy", url.Values{
		"PolicySourceArn":                             {"arn:aws:iam::000000000000:user/reader"},
		"ActionNames.member.1":                        {"s3:GetObject"},
		"ResourceArns.member.1":                       {"arn:aws:s3:::reports/q1.csv"},
		"PermissionsBoundaryPolicyInputList.member.1": {allowS3Doc},
	})

	// Then: the supplied boundary decides
	assertBoundaryDecision(t, results[0], "allowed", true)
}

func TestSimulatePrincipalPolicy_boundaryPolicyMissing_implicitDeny(t *testing.T) {
	// Given: a user whose stored boundary ARN resolves to no managed policy
	srv := helpers.NewTestServer(t)
	seedBoundedUser(t, srv, "dangling", allowS3ReadDoc, "arn:aws:iam::000000000000:policy/ghost")

	// When: the action is simulated
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/dangling")

	// Then: a boundary that cannot be read grants nothing — it must not be read
	// as absent, which is what would let the identity allow through
	assertBoundaryDecision(t, got, "implicitDeny", false)
}

func TestSimulatePrincipalPolicy_boundaryPolicyCorrupt_isolated(t *testing.T) {
	// Given: a user whose boundary points at an undecodable policy record
	srv := helpers.NewTestServer(t)
	const arn = "arn:aws:iam::000000000000:policy/corrupt"
	seedBoundedUser(t, srv, "corrupted", allowS3ReadDoc, arn)
	seedRawPolicyRecord(t, srv, arn, "{not json")

	// When: the action is simulated
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/corrupted")

	// Then: the call still answers, with the boundary allowing nothing
	assertBoundaryDecision(t, got, "implicitDeny", false)

	// And: the corrupt record does not break unrelated reads
	listResp := iamCall(t, srv, "ListPolicies", nil)
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	if boundary := getUserBoundary(t, srv, "corrupted"); boundary == nil || boundary.PermissionsBoundaryArn != arn {
		t.Fatalf("GetUser PermissionsBoundary = %+v, want arn %s", boundary, arn)
	}
}

func TestSimulatePrincipalPolicy_boundaryDocumentUnparseable_implicitDeny(t *testing.T) {
	// Given: a user whose boundary policy record decodes but carries a document
	// the policy grammar rejects
	srv := helpers.NewTestServer(t)
	const arn = "arn:aws:iam::000000000000:policy/broken"
	seedBoundedUser(t, srv, "broken-boundary", allowS3ReadDoc, arn)
	seedRawPolicyRecord(t, srv, arn,
		`{"PolicyName":"broken","Arn":"`+arn+`","Document":"{\"Statement\":[{\"Effect\":\"Maybe\"}]}"}`)

	// When: the action is simulated
	got := simulateGetObject(t, srv, "arn:aws:iam::000000000000:user/broken-boundary")

	// Then: the same fail-closed reading applies as for a missing policy
	assertBoundaryDecision(t, got, "implicitDeny", false)
}

// ─── Request-time enforcement ────────────────────────────────────────────────

const boundaryEnforceAuth = "AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/sqs/aws4_request, " +
	"SignedHeaders=host;x-amz-date, Signature=abc"

// iamCallWithAuth performs a signed IAM Query request, which is what an IAM
// mutation needs once enforcement is switched on.
func iamCallWithAuth(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	params.Set("Action", action)
	params.Set("Version", "2010-05-08")
	return queryCallWithAuthValues(t, srv, params,
		"AWS4-HMAC-SHA256 Credential=test/20260423/us-east-1/iam/aws4_request, "+
			"SignedHeaders=host;x-amz-date, Signature=abc")
}

// createQueueAsTestPrincipal issues an SQS CreateQueue signed by the "test"
// access key and returns the status code enforcement produced.
func createQueueAsTestPrincipal(t *testing.T, srv *helpers.TestServer, queueName string) int {
	t.Helper()
	resp := sqsCallWithAuth(t, srv, "AmazonSQS.CreateQueue",
		map[string]any{"QueueName": queueName}, boundaryEnforceAuth)
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestIAMEnforceIntegration_boundaryAllowsIdentityAllow(t *testing.T) {
	// Given: a principal allowed to create queues, bounded by a policy that
	// also allows SQS
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	const arn = "arn:aws:iam::000000000000:policy/boundary"
	seedManagedPolicy(t, srv, arn, allowSQSDoc)
	seedBoundedUser(t, srv, "test", allowSQSDoc, arn, "test")

	// When + Then: the intersection still allows the call
	if got := createQueueAsTestPrincipal(t, srv, "boundary-allowed"); got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}
}

func TestIAMEnforceIntegration_boundaryDoesNotAllow_denies(t *testing.T) {
	// Given: a principal allowed to create queues, bounded by an S3-only policy
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	const arn = "arn:aws:iam::000000000000:policy/boundary"
	seedManagedPolicy(t, srv, arn, allowS3Doc)
	seedBoundedUser(t, srv, "test", allowSQSDoc, arn, "test")

	// When + Then: the boundary caps the identity allow
	if got := createQueueAsTestPrincipal(t, srv, "boundary-denied"); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestIAMEnforceIntegration_boundaryExplicitDeny_denies(t *testing.T) {
	// Given: a principal allowed everything, bounded by a policy that denies
	// the action outright
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	const arn = "arn:aws:iam::000000000000:policy/boundary"
	seedManagedPolicy(t, srv, arn,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:CreateQueue","Resource":"*"}]}`)
	seedBoundedUser(t, srv, "test", allowSQSDoc, arn, "test")

	// When + Then: an explicit deny in the boundary is final
	if got := createQueueAsTestPrincipal(t, srv, "boundary-explicit-deny"); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestIAMEnforceIntegration_boundaryPolicyMissing_denies(t *testing.T) {
	// Given: a principal whose boundary ARN resolves to no managed policy
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedBoundedUser(t, srv, "test", allowSQSDoc, "arn:aws:iam::000000000000:policy/ghost", "test")

	// When + Then: enforcement fails closed rather than ignoring the boundary
	if got := createQueueAsTestPrincipal(t, srv, "boundary-missing"); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestIAMEnforceIntegration_roleSessionBoundary_denies(t *testing.T) {
	// Given: an assumed role allowed to create queues, bounded by an S3-only
	// policy
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	const arn = "arn:aws:iam::000000000000:policy/boundary"
	seedManagedPolicy(t, srv, arn, allowS3Doc)
	seedIAMRoleWithSession(t, srv, "ASIA-bounded", "arn:aws:iam::000000000000:role/ci-role", "ci-role", allowSQSDoc)
	seedRoleBoundary(t, srv, "ci-role", allowSQSDoc, arn)

	// When: the session calls SQS
	resp := sqsCallWithAuth(t, srv, "AmazonSQS.CreateQueue",
		map[string]any{"QueueName": "role-boundary-denied"},
		"AWS4-HMAC-SHA256 Credential=ASIA-bounded/20260423/us-east-1/sqs/aws4_request, "+
			"SignedHeaders=host;x-amz-date, Signature=abc")
	defer resp.Body.Close()

	// Then: the role's boundary caps the session exactly as a user's does
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestIAMEnforceIntegration_boundaryAttached_invalidatesCache(t *testing.T) {
	// Given: an unbounded principal whose first call has already warmed the
	// compiled-policy cache
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	const arn = "arn:aws:iam::000000000000:policy/iam-only"
	seedManagedPolicy(t, srv, arn,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"*"}]}`)
	seedBoundedUser(t, srv, "test",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:*","iam:*"],"Resource":"*"}]}`,
		"", "test")
	if got := createQueueAsTestPrincipal(t, srv, "before-boundary"); got != http.StatusOK {
		t.Fatalf("status before the boundary = %d, want %d", got, http.StatusOK)
	}

	// When: a boundary that excludes SQS is attached
	putResp := iamCallWithAuth(t, srv, "PutUserPermissionsBoundary", url.Values{
		"UserName": {"test"}, "PermissionsBoundary": {arn},
	})
	defer putResp.Body.Close()
	helpers.AssertStatus(t, putResp, http.StatusOK)

	// Then: the warm cache is discarded and the next call is capped
	if got := createQueueAsTestPrincipal(t, srv, "after-boundary"); got != http.StatusBadRequest {
		t.Fatalf("status after the boundary = %d, want %d", got, http.StatusBadRequest)
	}
}
