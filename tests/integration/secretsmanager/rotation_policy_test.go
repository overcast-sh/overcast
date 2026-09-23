// Package secretsmanager_test — rotation protocol and resource-policy coverage.
//
// These exercise the halves of Secrets Manager that #476 promotes out of
// "config only" and 501: the version-stage machinery a rotation Lambda drives
// (PutSecretValue staging labels, GetSecretValue by stage,
// UpdateSecretVersionStage), RotateSecret's validation contract, and the four
// resource-policy operations.
//
// The rotation *state machine* itself — the four-step invoke sequence — is unit
// tested in internal/services/secretsmanager/rotation_test.go against a fake
// rotation function, because driving it here would need a real Lambda container.
package secretsmanager_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const testRotationLambdaARN = "arn:aws:lambda:us-east-1:000000000000:function:rotate-fn"

// ─── ARN shape ───────────────────────────────────────────────────────────────

func TestCreateSecret_arnCarriesRandomSuffix(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: two secrets with the same-shaped name are created
	resp := smCall(t, srv, "CreateSecret", map[string]any{
		"Name": "suffix/one", "SecretString": "v",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var first struct {
		ARN string `json:"ARN"`
	}
	helpers.DecodeJSON(t, resp, &first)

	// Then: the ARN ends in AWS's six-character random suffix, not the bare name
	const prefix = "arn:aws:secretsmanager:us-east-1:000000000000:secret:suffix/one-"
	if len(first.ARN) != len(prefix)+6 || first.ARN[:len(prefix)] != prefix {
		t.Fatalf("ARN = %q, want %q plus a 6-character suffix", first.ARN, prefix)
	}
}

func TestGetSecretValue_byPartialARN(t *testing.T) {
	// Given: a secret whose ARN carries the random suffix
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "partial-arn", "hunter2")

	// When: the caller uses the partial ARN (no suffix), which AWS accepts
	resp := smCall(t, srv, "GetSecretValue", map[string]any{
		"SecretId": "arn:aws:secretsmanager:us-east-1:000000000000:secret:partial-arn",
	})
	defer resp.Body.Close()

	// Then: the secret resolves
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got struct {
		SecretString string `json:"SecretString"`
	}
	helpers.DecodeJSON(t, resp, &got)
	if got.SecretString != "hunter2" {
		t.Fatalf("SecretString = %q, want %q", got.SecretString, "hunter2")
	}
}

// ─── Version stages ──────────────────────────────────────────────────────────

func TestPutSecretValue_pendingStageLeavesCurrentAlone(t *testing.T) {
	// Given: a secret with a current value
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "staged", "current-value")

	// When: a new version is staged AWSPENDING, as a rotation function's
	// createSecret step does
	resp := smCall(t, srv, "PutSecretValue", map[string]any{
		"SecretId":           "staged",
		"SecretString":       "pending-value",
		"VersionStages":      []string{"AWSPENDING"},
		"ClientRequestToken": "11111111-1111-1111-1111-111111111111",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var put struct {
		VersionId     string   `json:"VersionId"`
		VersionStages []string `json:"VersionStages"`
	}
	helpers.DecodeJSON(t, resp, &put)

	// Then: the ClientRequestToken is the version ID, staged AWSPENDING
	if put.VersionId != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("VersionId = %q, want the ClientRequestToken", put.VersionId)
	}
	if len(put.VersionStages) != 1 || put.VersionStages[0] != "AWSPENDING" {
		t.Fatalf("VersionStages = %v, want [AWSPENDING]", put.VersionStages)
	}

	// And: AWSCURRENT still resolves to the original value
	cur := smCall(t, srv, "GetSecretValue", map[string]any{"SecretId": "staged"})
	defer cur.Body.Close()
	var current struct {
		SecretString string `json:"SecretString"`
	}
	helpers.DecodeJSON(t, cur, &current)
	if current.SecretString != "current-value" {
		t.Fatalf("AWSCURRENT SecretString = %q, want %q", current.SecretString, "current-value")
	}
}

func TestGetSecretValue_byVersionStage(t *testing.T) {
	// Given: a secret with an AWSPENDING version staged alongside AWSCURRENT
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "stage-lookup", "current-value")
	resp := smCall(t, srv, "PutSecretValue", map[string]any{
		"SecretId":           "stage-lookup",
		"SecretString":       "pending-value",
		"VersionStages":      []string{"AWSPENDING"},
		"ClientRequestToken": "22222222-2222-2222-2222-222222222222",
	})
	resp.Body.Close()

	// When: the caller asks for the AWSPENDING stage
	got := smCall(t, srv, "GetSecretValue", map[string]any{
		"SecretId": "stage-lookup", "VersionStage": "AWSPENDING",
	})
	defer got.Body.Close()

	// Then: the pending value comes back
	helpers.AssertStatus(t, got, http.StatusOK)
	var pending struct {
		SecretString  string   `json:"SecretString"`
		VersionStages []string `json:"VersionStages"`
	}
	helpers.DecodeJSON(t, got, &pending)
	if pending.SecretString != "pending-value" {
		t.Fatalf("SecretString = %q, want %q", pending.SecretString, "pending-value")
	}
	if len(pending.VersionStages) != 1 || pending.VersionStages[0] != "AWSPENDING" {
		t.Fatalf("VersionStages = %v, want [AWSPENDING]", pending.VersionStages)
	}
}

func TestGetSecretValue_unknownVersionStage(t *testing.T) {
	// Given: a secret with only an AWSCURRENT version
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "no-pending", "v")

	// When: a stage nothing carries is requested
	resp := smCall(t, srv, "GetSecretValue", map[string]any{
		"SecretId": "no-pending", "VersionStage": "AWSPENDING",
	})
	defer resp.Body.Close()

	// Then: AWS answers ResourceNotFoundException, not an empty 200
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestUpdateSecretVersionStage_movesCurrent(t *testing.T) {
	// Given: a secret with an AWSPENDING version, as after createSecret
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "finish-me", "old-value")
	describe := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "finish-me"})
	var before struct {
		VersionIdsToStages map[string][]string `json:"VersionIdsToStages"`
	}
	helpers.DecodeJSON(t, describe, &before)
	describe.Body.Close()
	oldVersion := ""
	for id, stages := range before.VersionIdsToStages {
		for _, s := range stages {
			if s == "AWSCURRENT" {
				oldVersion = id
			}
		}
	}
	if oldVersion == "" {
		t.Fatal("setup: no AWSCURRENT version")
	}
	resp := smCall(t, srv, "PutSecretValue", map[string]any{
		"SecretId":           "finish-me",
		"SecretString":       "new-value",
		"VersionStages":      []string{"AWSPENDING"},
		"ClientRequestToken": "33333333-3333-3333-3333-333333333333",
	})
	resp.Body.Close()

	// When: finishSecret's UpdateSecretVersionStage moves AWSCURRENT across
	move := smCall(t, srv, "UpdateSecretVersionStage", map[string]any{
		"SecretId":            "finish-me",
		"VersionStage":        "AWSCURRENT",
		"MoveToVersionId":     "33333333-3333-3333-3333-333333333333",
		"RemoveFromVersionId": oldVersion,
	})
	defer move.Body.Close()

	// Then: the call succeeds and AWSCURRENT now reads the new value
	helpers.AssertStatus(t, move, http.StatusOK)
	cur := smCall(t, srv, "GetSecretValue", map[string]any{"SecretId": "finish-me"})
	defer cur.Body.Close()
	var current struct {
		SecretString  string   `json:"SecretString"`
		VersionId     string   `json:"VersionId"`
		VersionStages []string `json:"VersionStages"`
	}
	helpers.DecodeJSON(t, cur, &current)
	if current.SecretString != "new-value" {
		t.Fatalf("SecretString = %q, want %q", current.SecretString, "new-value")
	}
	if current.VersionId != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("VersionId = %q, want the promoted version", current.VersionId)
	}

	// And: the displaced version carries AWSPREVIOUS, as AWS does
	after := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "finish-me"})
	defer after.Body.Close()
	var post struct {
		VersionIdsToStages map[string][]string `json:"VersionIdsToStages"`
	}
	helpers.DecodeJSON(t, after, &post)
	if stages := post.VersionIdsToStages[oldVersion]; len(stages) != 1 || stages[0] != "AWSPREVIOUS" {
		t.Fatalf("displaced version stages = %v, want [AWSPREVIOUS]", stages)
	}
}

func TestUpdateSecretVersionStage_removeFromWrongVersion(t *testing.T) {
	// Given: a secret whose AWSCURRENT sits on a different version
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "wrong-remove", "v")

	// When: the caller names a version that does not hold the stage
	resp := smCall(t, srv, "UpdateSecretVersionStage", map[string]any{
		"SecretId":            "wrong-remove",
		"VersionStage":        "AWSCURRENT",
		"RemoveFromVersionId": "44444444-4444-4444-4444-444444444444",
	})
	defer resp.Body.Close()

	// Then: AWS rejects it rather than silently succeeding
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ─── RotateSecret validation ─────────────────────────────────────────────────

func TestRotateSecret_noRotationLambdaConfigured(t *testing.T) {
	// Given: a secret with no rotation function
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "rot-no-lambda", "v")

	// When: rotation is requested without a Lambda ARN
	resp := smCall(t, srv, "RotateSecret", map[string]any{
		"SecretId":      "rot-no-lambda",
		"RotationRules": map[string]any{"AutomaticallyAfterDays": 30},
	})
	defer resp.Body.Close()

	// Then: AWS's InvalidRequestException — enabling rotation needs a function
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidRequestException")
}

func TestRotateSecret_scheduleOnly(t *testing.T) {
	// Given: a secret
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "rot-schedule", "v")

	// When: rotation is configured with RotateImmediately=false
	resp := smCall(t, srv, "RotateSecret", map[string]any{
		"SecretId":          "rot-schedule",
		"RotationLambdaARN": testRotationLambdaARN,
		"RotationRules":     map[string]any{"AutomaticallyAfterDays": 30},
		"RotateImmediately": false,
	})
	defer resp.Body.Close()

	// Then: the config is stored and nothing is invoked
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: DescribeSecret reports the schedule
	describe := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "rot-schedule"})
	defer describe.Body.Close()
	var got struct {
		RotationEnabled   bool    `json:"RotationEnabled"`
		RotationLambdaARN string  `json:"RotationLambdaARN"`
		NextRotationDate  float64 `json:"NextRotationDate"`
		LastRotatedDate   float64 `json:"LastRotatedDate"`
		RotationRules     struct {
			AutomaticallyAfterDays int64 `json:"AutomaticallyAfterDays"`
		} `json:"RotationRules"`
	}
	helpers.DecodeJSON(t, describe, &got)
	if !got.RotationEnabled {
		t.Error("RotationEnabled = false, want true")
	}
	if got.RotationLambdaARN != testRotationLambdaARN {
		t.Errorf("RotationLambdaARN = %q, want %q", got.RotationLambdaARN, testRotationLambdaARN)
	}
	if got.RotationRules.AutomaticallyAfterDays != 30 {
		t.Errorf("AutomaticallyAfterDays = %d, want 30", got.RotationRules.AutomaticallyAfterDays)
	}
	if got.NextRotationDate == 0 {
		t.Error("NextRotationDate = 0, want the scheduled next rotation")
	}
	if got.LastRotatedDate != 0 {
		t.Errorf("LastRotatedDate = %v, want 0 — nothing has rotated yet", got.LastRotatedDate)
	}
}

func TestRotateSecret_invalidAutomaticallyAfterDays(t *testing.T) {
	// Given: a secret
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "rot-bad-days", "v")

	// When: the schedule is outside AWS's 1–1000 day range
	resp := smCall(t, srv, "RotateSecret", map[string]any{
		"SecretId":          "rot-bad-days",
		"RotationLambdaARN": testRotationLambdaARN,
		"RotationRules":     map[string]any{"AutomaticallyAfterDays": 5000},
		"RotateImmediately": false,
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestRotateSecret_immediateWithUninvokableFunction(t *testing.T) {
	// Given: a secret configured against a rotation function that does not exist
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "rot-missing-fn", "v")

	// When: rotation runs immediately (AWS's default)
	resp := smCall(t, srv, "RotateSecret", map[string]any{
		"SecretId":          "rot-missing-fn",
		"RotationLambdaARN": testRotationLambdaARN,
		"RotationRules":     map[string]any{"AutomaticallyAfterDays": 30},
	})
	defer resp.Body.Close()

	// Then: the failure surfaces rather than being swallowed behind a 200
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidRequestException")

	// And: AWSCURRENT is untouched — the secret is left in a usable state
	cur := smCall(t, srv, "GetSecretValue", map[string]any{"SecretId": "rot-missing-fn"})
	defer cur.Body.Close()
	helpers.AssertStatus(t, cur, http.StatusOK)
	var current struct {
		SecretString string `json:"SecretString"`
	}
	helpers.DecodeJSON(t, cur, &current)
	if current.SecretString != "v" {
		t.Fatalf("SecretString = %q, want the untouched %q", current.SecretString, "v")
	}
}

func TestCancelRotateSecret_keepsFunctionConfigured(t *testing.T) {
	// Given: a secret with a rotation schedule
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "cancel-keeps", "v")
	resp := smCall(t, srv, "RotateSecret", map[string]any{
		"SecretId":          "cancel-keeps",
		"RotationLambdaARN": testRotationLambdaARN,
		"RotationRules":     map[string]any{"AutomaticallyAfterDays": 7},
		"RotateImmediately": false,
	})
	resp.Body.Close()

	// When: rotation is cancelled
	cancel := smCall(t, srv, "CancelRotateSecret", map[string]any{"SecretId": "cancel-keeps"})
	defer cancel.Body.Close()
	helpers.AssertStatus(t, cancel, http.StatusOK)

	// Then: rotation is off but the function stays configured, as on AWS
	describe := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "cancel-keeps"})
	defer describe.Body.Close()
	var got struct {
		RotationEnabled   bool    `json:"RotationEnabled"`
		RotationLambdaARN string  `json:"RotationLambdaARN"`
		NextRotationDate  float64 `json:"NextRotationDate"`
	}
	helpers.DecodeJSON(t, describe, &got)
	if got.RotationEnabled {
		t.Error("RotationEnabled = true, want false after cancel")
	}
	if got.RotationLambdaARN != testRotationLambdaARN {
		t.Errorf("RotationLambdaARN = %q, want it retained", got.RotationLambdaARN)
	}
	if got.NextRotationDate != 0 {
		t.Errorf("NextRotationDate = %v, want 0 once rotation is cancelled", got.NextRotationDate)
	}
}

// ─── Resource policies ───────────────────────────────────────────────────────

const testResourcePolicy = `{"Version":"2012-10-17","Statement":[{"Sid":"ReadOnly","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`

func TestPutResourcePolicy_roundTrip(t *testing.T) {
	// Given: a secret with no policy
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "policy-secret", "v")

	// When: a resource policy is attached
	resp := smCall(t, srv, "PutResourcePolicy", map[string]any{
		"SecretId":       "policy-secret",
		"ResourcePolicy": testResourcePolicy,
	})
	defer resp.Body.Close()

	// Then: 200 with the secret's identity
	helpers.AssertStatus(t, resp, http.StatusOK)
	var put struct {
		ARN  string `json:"ARN"`
		Name string `json:"Name"`
	}
	helpers.DecodeJSON(t, resp, &put)
	if put.Name != "policy-secret" {
		t.Fatalf("Name = %q, want %q", put.Name, "policy-secret")
	}
	if put.ARN == "" {
		t.Fatal("ARN is empty")
	}

	// And: GetResourcePolicy returns exactly what was stored
	get := smCall(t, srv, "GetResourcePolicy", map[string]any{"SecretId": "policy-secret"})
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusOK)
	var got struct {
		ARN            string `json:"ARN"`
		Name           string `json:"Name"`
		ResourcePolicy string `json:"ResourcePolicy"`
	}
	helpers.DecodeJSON(t, get, &got)
	if got.ResourcePolicy != testResourcePolicy {
		t.Fatalf("ResourcePolicy = %q, want it round-tripped verbatim", got.ResourcePolicy)
	}
}

func TestGetResourcePolicy_noPolicyAttached(t *testing.T) {
	// Given: a secret with no policy
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "policy-absent", "v")

	// When: the policy is fetched
	resp := smCall(t, srv, "GetResourcePolicy", map[string]any{"SecretId": "policy-absent"})
	defer resp.Body.Close()

	// Then: AWS returns 200 with the identity and no ResourcePolicy field
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got struct {
		Name           string `json:"Name"`
		ResourcePolicy string `json:"ResourcePolicy"`
	}
	helpers.DecodeJSON(t, resp, &got)
	if got.Name != "policy-absent" {
		t.Fatalf("Name = %q, want %q", got.Name, "policy-absent")
	}
	if got.ResourcePolicy != "" {
		t.Fatalf("ResourcePolicy = %q, want empty", got.ResourcePolicy)
	}
}

func TestDeleteResourcePolicy_removesIt(t *testing.T) {
	// Given: a secret with a policy attached
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "policy-delete", "v")
	put := smCall(t, srv, "PutResourcePolicy", map[string]any{
		"SecretId": "policy-delete", "ResourcePolicy": testResourcePolicy,
	})
	put.Body.Close()

	// When: the policy is deleted
	resp := smCall(t, srv, "DeleteResourcePolicy", map[string]any{"SecretId": "policy-delete"})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: GetResourcePolicy no longer reports one
	get := smCall(t, srv, "GetResourcePolicy", map[string]any{"SecretId": "policy-delete"})
	defer get.Body.Close()
	var got struct {
		ResourcePolicy string `json:"ResourcePolicy"`
	}
	helpers.DecodeJSON(t, get, &got)
	if got.ResourcePolicy != "" {
		t.Fatalf("ResourcePolicy = %q, want empty after delete", got.ResourcePolicy)
	}
}

func TestPutResourcePolicy_malformed(t *testing.T) {
	// Given: a secret
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "policy-bad", "v")

	// When: the document is not a policy
	resp := smCall(t, srv, "PutResourcePolicy", map[string]any{
		"SecretId": "policy-bad", "ResourcePolicy": "{not json",
	})
	defer resp.Body.Close()

	// Then: MalformedPolicyDocumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "MalformedPolicyDocumentException")
}

func TestPutResourcePolicy_blockPublicPolicy(t *testing.T) {
	// Given: a secret and a policy open to every principal
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "policy-public", "v")
	const public = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`

	// When: BlockPublicPolicy is set
	resp := smCall(t, srv, "PutResourcePolicy", map[string]any{
		"SecretId": "policy-public", "ResourcePolicy": public, "BlockPublicPolicy": true,
	})
	defer resp.Body.Close()

	// Then: AWS refuses it with PublicPolicyException — the document is
	// well-formed, it is public, which is a different failure from
	// MalformedPolicyDocumentException (#1914).
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "PublicPolicyException")
}

func TestValidateResourcePolicy_valid(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: a well-formed policy is validated
	resp := smCall(t, srv, "ValidateResourcePolicy", map[string]any{
		"ResourcePolicy": testResourcePolicy,
	})
	defer resp.Body.Close()

	// Then: validation passes with no errors
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got struct {
		PolicyValidationPassed bool `json:"PolicyValidationPassed"`
		ValidationErrors       []struct {
			CheckName    string `json:"CheckName"`
			ErrorMessage string `json:"ErrorMessage"`
		} `json:"ValidationErrors"`
	}
	helpers.DecodeJSON(t, resp, &got)
	if !got.PolicyValidationPassed {
		t.Fatalf("PolicyValidationPassed = false, errors = %+v", got.ValidationErrors)
	}
	if len(got.ValidationErrors) != 0 {
		t.Fatalf("ValidationErrors = %+v, want none", got.ValidationErrors)
	}
}

func TestValidateResourcePolicy_reportsProblems(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: a syntactically valid document with no Statement is validated
	resp := smCall(t, srv, "ValidateResourcePolicy", map[string]any{
		"ResourcePolicy": `{"Version":"2012-10-17"}`,
	})
	defer resp.Body.Close()

	// Then: AWS answers 200 with the failure described, not an error response
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got struct {
		PolicyValidationPassed bool `json:"PolicyValidationPassed"`
		ValidationErrors       []struct {
			CheckName    string `json:"CheckName"`
			ErrorMessage string `json:"ErrorMessage"`
		} `json:"ValidationErrors"`
	}
	helpers.DecodeJSON(t, resp, &got)
	if got.PolicyValidationPassed {
		t.Fatal("PolicyValidationPassed = true, want false for a policy with no Statement")
	}
	if len(got.ValidationErrors) == 0 {
		t.Fatal("ValidationErrors is empty, want the failing check named")
	}
	if got.ValidationErrors[0].CheckName == "" || got.ValidationErrors[0].ErrorMessage == "" {
		t.Fatalf("ValidationErrors[0] = %+v, want CheckName and ErrorMessage populated", got.ValidationErrors[0])
	}
}

func TestGetResourcePolicy_secretNotFound(t *testing.T) {
	// Given: no secrets
	srv := helpers.NewTestServer(t)

	// When: a policy is fetched for a secret that does not exist
	resp := smCall(t, srv, "GetResourcePolicy", map[string]any{"SecretId": "nope"})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}
