// Package eventbridge_test — CreateEventBus/DescribeEventBus's Description,
// DeadLetterConfig and KmsKeyIdentifier, and PutPermission/RemovePermission
// (#2076).
//
// CreateEventBus previously accepted only Name and Tags, silently dropping
// three more members AWS documents; PutPermission and RemovePermission were
// entirely unimplemented, so a bus's resource policy could never be set or
// read back. These are the failing-first cases for both gaps.
package eventbridge_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// describeEventBusBody performs DescribeEventBus and decodes the response.
func describeEventBusBody(t *testing.T, srv *helpers.TestServer, name string) map[string]any {
	t.Helper()
	body := map[string]any{}
	if name != "" {
		body["Name"] = name
	}
	resp := ebCall(t, srv, "DescribeEventBus", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	return out
}

// ─── CreateEventBus / DescribeEventBus ────────────────────────────────────

func TestCreateEventBus_forwardsDescriptionDeadLetterConfigAndKmsKeyIdentifier(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: CreateEventBus is called with Description, DeadLetterConfig and
	// KmsKeyIdentifier, none of which the old request shape carried.
	resp := ebCall(t, srv, "CreateEventBus", map[string]any{
		"Name":             "custom-bus",
		"Description":      "a custom bus",
		"KmsKeyIdentifier": "alias/my-key",
		"DeadLetterConfig": map[string]any{"Arn": "arn:aws:sqs:us-east-1:000000000000:dlq"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var created struct {
		EventBusArn      string `json:"EventBusArn"`
		Description      string `json:"Description"`
		KmsKeyIdentifier string `json:"KmsKeyIdentifier"`
		DeadLetterConfig struct {
			Arn string `json:"Arn"`
		} `json:"DeadLetterConfig"`
	}
	helpers.DecodeJSON(t, resp, &created)

	// Then: CreateEventBus's own response already echoes them back.
	if created.EventBusArn == "" {
		t.Fatal("expected EventBusArn to be set")
	}
	if created.Description != "a custom bus" {
		t.Errorf("CreateEventBus Description = %q, want %q", created.Description, "a custom bus")
	}
	if created.KmsKeyIdentifier != "alias/my-key" {
		t.Errorf("CreateEventBus KmsKeyIdentifier = %q, want %q", created.KmsKeyIdentifier, "alias/my-key")
	}
	if created.DeadLetterConfig.Arn != "arn:aws:sqs:us-east-1:000000000000:dlq" {
		t.Errorf("CreateEventBus DeadLetterConfig.Arn = %q, want the dlq ARN", created.DeadLetterConfig.Arn)
	}

	// And: DescribeEventBus reports the same three members back.
	described := describeEventBusBody(t, srv, "custom-bus")
	if described["Description"] != "a custom bus" {
		t.Errorf("DescribeEventBus Description = %v, want %q", described["Description"], "a custom bus")
	}
	if described["KmsKeyIdentifier"] != "alias/my-key" {
		t.Errorf("DescribeEventBus KmsKeyIdentifier = %v, want %q", described["KmsKeyIdentifier"], "alias/my-key")
	}
	dlq, ok := described["DeadLetterConfig"].(map[string]any)
	if !ok || dlq["Arn"] != "arn:aws:sqs:us-east-1:000000000000:dlq" {
		t.Errorf("DescribeEventBus DeadLetterConfig = %v, want the dlq ARN", described["DeadLetterConfig"])
	}
}

func TestDescribeEventBus_omitsUnsetOptionalFields(t *testing.T) {
	// Given: a bus created with none of the optional members set.
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "CreateEventBus", map[string]any{"Name": "bare-bus"})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: DescribeEventBus is called.
	described := describeEventBusBody(t, srv, "bare-bus")

	// Then: Description/KmsKeyIdentifier/DeadLetterConfig/Policy are absent
	// rather than present as empty strings/null, matching AWS's own shape
	// for a bus nothing has been configured on.
	for _, field := range []string{"KmsKeyIdentifier", "DeadLetterConfig", "Policy"} {
		if _, present := described[field]; present {
			t.Errorf("DescribeEventBus included unset field %s = %v", field, described[field])
		}
	}
}

// ─── PutPermission / RemovePermission ──────────────────────────────────────

func TestPutPermission_setsStatementReportedByDescribeEventBus(t *testing.T) {
	// Given: the default event bus.
	srv := helpers.NewTestServer(t)

	// When: PutPermission grants an external account access.
	resp := ebCall(t, srv, "PutPermission", map[string]any{
		"Action":      "events:PutEvents",
		"Principal":   "111122223333",
		"StatementId": "AllowAccount1",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: DescribeEventBus reports the statement in Policy.
	described := describeEventBusBody(t, srv, "")
	policy, _ := described["Policy"].(string)
	if policy == "" {
		t.Fatal("expected DescribeEventBus to report a non-empty Policy")
	}
	for _, want := range []string{"AllowAccount1", "events:PutEvents", "111122223333"} {
		if !strings.Contains(policy, want) {
			t.Errorf("Policy = %s, want it to contain %q", policy, want)
		}
	}
}

func TestPutPermission_wildcardPrincipalIsBare(t *testing.T) {
	// Given: the default bus.
	srv := helpers.NewTestServer(t)

	// When: PutPermission grants access to any account ("*").
	resp := ebCall(t, srv, "PutPermission", map[string]any{
		"Action":      "events:PutEvents",
		"Principal":   "*",
		"StatementId": "AllowAll",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the statement's Principal is the bare wildcard, not wrapped in an
	// {"AWS": ...} object the way an account ID is.
	described := describeEventBusBody(t, srv, "")
	policy, _ := described["Policy"].(string)
	if !strings.Contains(policy, `"Principal":"*"`) {
		t.Errorf("Policy = %s, want a bare wildcard Principal", policy)
	}
}

func TestPutPermission_wholePolicyReplacesDocument(t *testing.T) {
	// Given: a bus with one statement already granted via the individual
	// Action/Principal/StatementId parameters.
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutPermission", map[string]any{
		"Action":      "events:PutEvents",
		"Principal":   "111122223333",
		"StatementId": "First",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: PutPermission is called again with a whole Policy document
	// instead — AWS documents this as used "instead of" the individual
	// parameters, replacing rather than merging.
	wholePolicy := `{"Version":"2012-10-17","Statement":[{"Sid":"Replaced","Effect":"Allow","Principal":"*","Action":"events:PutEvents","Resource":"arn:aws:events:us-east-1:000000000000:event-bus/default"}]}`
	resp = ebCall(t, srv, "PutPermission", map[string]any{"Policy": wholePolicy})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: only the replacement statement remains.
	described := describeEventBusBody(t, srv, "")
	policy, _ := described["Policy"].(string)
	if strings.Contains(policy, "First") {
		t.Errorf("Policy = %s, want the original statement replaced, not merged", policy)
	}
	if !strings.Contains(policy, "Replaced") {
		t.Errorf("Policy = %s, want the replacement statement", policy)
	}
}

func TestPutPermission_missingRequiredFieldsIsValidationException(t *testing.T) {
	// Given: the default bus.
	srv := helpers.NewTestServer(t)

	// When: PutPermission is called with neither a whole Policy nor the full
	// Action/Principal/StatementId trio.
	resp := ebCall(t, srv, "PutPermission", map[string]any{
		"Action": "events:PutEvents",
	})
	defer resp.Body.Close()

	// Then: AWS's documented shape is enforced.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestPutPermission_unknownEventBusIsResourceNotFound(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: PutPermission targets a bus that was never created.
	resp := ebCall(t, srv, "PutPermission", map[string]any{
		"EventBusName": "does-not-exist",
		"Action":       "events:PutEvents",
		"Principal":    "111122223333",
		"StatementId":  "Sid1",
	})
	defer resp.Body.Close()

	// Then: AWS's documented ResourceNotFoundException is returned.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestRemovePermission_removesStatementByStatementId(t *testing.T) {
	// Given: a bus with two granted statements.
	srv := helpers.NewTestServer(t)
	for _, sid := range []string{"Keep", "Drop"} {
		resp := ebCall(t, srv, "PutPermission", map[string]any{
			"Action":      "events:PutEvents",
			"Principal":   "111122223333",
			"StatementId": sid,
		})
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
	}

	// When: RemovePermission revokes only one of them.
	resp := ebCall(t, srv, "RemovePermission", map[string]any{"StatementId": "Drop"})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the other statement is still reported, the removed one is gone.
	described := describeEventBusBody(t, srv, "")
	policy, _ := described["Policy"].(string)
	if !strings.Contains(policy, "Keep") {
		t.Errorf("Policy = %s, want the untouched statement kept", policy)
	}
	if strings.Contains(policy, "Drop") {
		t.Errorf("Policy = %s, want the removed statement gone", policy)
	}
}

func TestRemovePermission_removeAllPermissionsClearsPolicy(t *testing.T) {
	// Given: a bus with a granted statement.
	srv := helpers.NewTestServer(t)
	resp := ebCall(t, srv, "PutPermission", map[string]any{
		"Action":      "events:PutEvents",
		"Principal":   "111122223333",
		"StatementId": "Sid1",
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: RemovePermission is called with RemoveAllPermissions.
	resp = ebCall(t, srv, "RemovePermission", map[string]any{"RemoveAllPermissions": true})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: DescribeEventBus no longer reports a Policy at all.
	described := describeEventBusBody(t, srv, "")
	if _, present := described["Policy"]; present {
		t.Errorf("Policy = %v, want it absent after RemoveAllPermissions", described["Policy"])
	}
}

func TestRemovePermission_unknownStatementIdIsResourceNotFound(t *testing.T) {
	// Given: the default bus with no permissions granted.
	srv := helpers.NewTestServer(t)

	// When: RemovePermission names a statement that was never granted.
	resp := ebCall(t, srv, "RemovePermission", map[string]any{"StatementId": "NeverGranted"})
	defer resp.Body.Close()

	// Then: AWS's documented ResourceNotFoundException is returned.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}
