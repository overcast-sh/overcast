// DeleteSecret's recovery window and the RestoreSecret that cancels it (#178).
//
// AWS schedules a delete rather than performing one: the secret keeps its
// record with a DeletedDate, disappears from ListSecrets unless the caller asks
// for planned deletions, refuses every value operation, and can be brought back
// with RestoreSecret until the window runs out. ForceDeleteWithoutRecovery is
// the opt-out, and supplying it together with RecoveryWindowInDays is an error.
package secretsmanager_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// smError decodes a Secrets Manager JSON error into its code and message.
func smError(t *testing.T, resp *http.Response) (code, message string) {
	t.Helper()
	var errResp struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, resp, &errResp)
	return errResp.Type, errResp.Message
}

// deleteSecret calls DeleteSecret with the supplied parameters.
func deleteSecret(t *testing.T, srv *helpers.TestServer, body map[string]any) *http.Response {
	t.Helper()
	return smCall(t, srv, "DeleteSecret", body)
}

// ─── Parameter validation ────────────────────────────────────────────────────

func TestDeleteSecret_forceAndRecoveryWindowTogether(t *testing.T) {
	// Given: a secret exists
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "both-params", "val")

	// When: DeleteSecret supplies both ForceDeleteWithoutRecovery and RecoveryWindowInDays
	resp := deleteSecret(t, srv, map[string]any{
		"SecretId":                   "both-params",
		"ForceDeleteWithoutRecovery": true,
		"RecoveryWindowInDays":       7,
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException names the conflict
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	code, msg := smError(t, resp)
	if code != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException, got %q (%s)", code, msg)
	}
	if !strings.Contains(msg, "ForceDeleteWithoutRecovery") || !strings.Contains(msg, "RecoveryWindowInDays") {
		t.Errorf("expected the message to name both parameters, got %q", msg)
	}

	// And: the secret survives
	get := smCall(t, srv, "GetSecretValue", map[string]any{"SecretId": "both-params"})
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusOK)
}

func TestDeleteSecret_recoveryWindowOutOfRange(t *testing.T) {
	// Given: a secret exists
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "range-check", "val")

	for _, days := range []int{0, 6, 31} {
		// When: DeleteSecret asks for a window outside AWS's 7..30
		resp := deleteSecret(t, srv, map[string]any{
			"SecretId":             "range-check",
			"RecoveryWindowInDays": days,
		})

		// Then: InvalidParameterException
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		code, msg := smError(t, resp)
		if code != "InvalidParameterException" {
			t.Errorf("days=%d: expected InvalidParameterException, got %q (%s)", days, code, msg)
		}
		resp.Body.Close()
	}

	// And: the secret survives
	get := smCall(t, srv, "GetSecretValue", map[string]any{"SecretId": "range-check"})
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusOK)
}

func TestDeleteSecret_recoveryWindowBoundsAreAccepted(t *testing.T) {
	// Given: a server whose clock is stopped
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	for _, days := range []int{7, 30} {
		// When: DeleteSecret asks for a window at the edge of AWS's range
		createSecret(t, srv, "edge-window", "val")
		resp := deleteSecret(t, srv, map[string]any{
			"SecretId":             "edge-window",
			"RecoveryWindowInDays": days,
		})
		helpers.AssertStatus(t, resp, http.StatusOK)

		// Then: DeletionDate is the request time plus that window
		var result struct {
			DeletionDate float64 `json:"DeletionDate"`
		}
		helpers.DecodeJSON(t, resp, &result)
		want := float64(srv.Clock.Now().Add(time.Duration(days) * 24 * time.Hour).Unix())
		if result.DeletionDate != want {
			t.Errorf("days=%d: expected DeletionDate %v, got %v", days, want, result.DeletionDate)
		}

		// Cleanup: make the name reusable for the next iteration
		purge := deleteSecret(t, srv, map[string]any{
			"SecretId":                   "edge-window",
			"ForceDeleteWithoutRecovery": true,
		})
		helpers.AssertStatus(t, purge, http.StatusOK)
		purge.Body.Close()
	}
}

// ─── Scheduled deletion ──────────────────────────────────────────────────────

func TestDeleteSecret_defaultWindowSchedulesRatherThanDeletes(t *testing.T) {
	// Given: a secret exists and the clock is stopped
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	createSecret(t, srv, "scheduled", "val")

	// When: DeleteSecret is called with neither parameter
	resp := deleteSecret(t, srv, map[string]any{"SecretId": "scheduled"})
	defer resp.Body.Close()

	// Then: DeletionDate is 30 days out, AWS's default window
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Name         string  `json:"Name"`
		DeletionDate float64 `json:"DeletionDate"`
	}
	helpers.DecodeJSON(t, resp, &result)
	want := float64(srv.Clock.Now().Add(30 * 24 * time.Hour).Unix())
	if result.DeletionDate != want {
		t.Errorf("expected DeletionDate %v (now + 30 days), got %v", want, result.DeletionDate)
	}

	// And: DescribeSecret still finds it and reports DeletedDate
	desc := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "scheduled"})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusOK)
	var described struct {
		Name        string  `json:"Name"`
		DeletedDate float64 `json:"DeletedDate"`
	}
	helpers.DecodeJSON(t, desc, &described)
	if described.DeletedDate != want {
		t.Errorf("expected DescribeSecret DeletedDate %v, got %v", want, described.DeletedDate)
	}

	// And: GetSecretValue refuses it as marked for deletion
	get := smCall(t, srv, "GetSecretValue", map[string]any{"SecretId": "scheduled"})
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusBadRequest)
	code, msg := smError(t, get)
	if code != "InvalidRequestException" {
		t.Errorf("expected InvalidRequestException, got %q (%s)", code, msg)
	}
	if !strings.Contains(msg, "marked for deletion") {
		t.Errorf("expected a marked-for-deletion message, got %q", msg)
	}
}

func TestDeleteSecret_scheduledSecretIsHiddenFromListSecrets(t *testing.T) {
	// Given: two secrets, one scheduled for deletion
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "listed-live", "val")
	createSecret(t, srv, "listed-pending", "val")
	del := deleteSecret(t, srv, map[string]any{"SecretId": "listed-pending"})
	helpers.AssertStatus(t, del, http.StatusOK)
	del.Body.Close()

	// When: ListSecrets is called without IncludePlannedDeletion
	names := listSecretNames(t, srv, nil)

	// Then: only the live secret is listed
	if contains(names, "listed-pending") || !contains(names, "listed-live") {
		t.Errorf("expected only listed-live, got %v", names)
	}

	// And: IncludePlannedDeletion brings the scheduled one back
	withPending := listSecretNames(t, srv, map[string]any{"IncludePlannedDeletion": true})
	if !contains(withPending, "listed-pending") || !contains(withPending, "listed-live") {
		t.Errorf("expected both secrets with IncludePlannedDeletion, got %v", withPending)
	}
}

func TestDeleteSecret_valueOperationsRefuseAScheduledSecret(t *testing.T) {
	// Given: a secret scheduled for deletion
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "refuse-writes", "val")
	del := deleteSecret(t, srv, map[string]any{"SecretId": "refuse-writes"})
	helpers.AssertStatus(t, del, http.StatusOK)
	del.Body.Close()

	cases := map[string]map[string]any{
		"PutSecretValue": {"SecretId": "refuse-writes", "SecretString": "new"},
		"UpdateSecret":   {"SecretId": "refuse-writes", "Description": "new"},
		"DeleteSecret":   {"SecretId": "refuse-writes"},
	}
	for operation, body := range cases {
		// When: the operation targets the scheduled secret
		resp := smCall(t, srv, operation, body)

		// Then: InvalidRequestException, not a silent success
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		code, msg := smError(t, resp)
		if code != "InvalidRequestException" {
			t.Errorf("%s: expected InvalidRequestException, got %q (%s)", operation, code, msg)
		}
		resp.Body.Close()
	}
}

func TestCreateSecret_nameScheduledForDeletion(t *testing.T) {
	// Given: a secret scheduled for deletion
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "taken-name", "val")
	del := deleteSecret(t, srv, map[string]any{"SecretId": "taken-name"})
	helpers.AssertStatus(t, del, http.StatusOK)
	del.Body.Close()

	// When: a new secret reuses the name
	resp := smCall(t, srv, "CreateSecret", map[string]any{"Name": "taken-name", "SecretString": "other"})
	defer resp.Body.Close()

	// Then: InvalidRequestException, because the name is still held
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	code, msg := smError(t, resp)
	if code != "InvalidRequestException" {
		t.Errorf("expected InvalidRequestException, got %q (%s)", code, msg)
	}
	if !strings.Contains(msg, "scheduled for deletion") {
		t.Errorf("expected a scheduled-for-deletion message, got %q", msg)
	}
}

func TestDeleteSecret_forceRemovesImmediately(t *testing.T) {
	// Given: a secret exists and the clock is stopped
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	createSecret(t, srv, "forced", "val")

	// When: DeleteSecret forces the delete
	resp := deleteSecret(t, srv, map[string]any{
		"SecretId":                   "forced",
		"ForceDeleteWithoutRecovery": true,
	})
	defer resp.Body.Close()

	// Then: DeletionDate is now — there is no window
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		DeletionDate float64 `json:"DeletionDate"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.DeletionDate != float64(srv.Clock.Now().Unix()) {
		t.Errorf("expected DeletionDate %v, got %v", float64(srv.Clock.Now().Unix()), result.DeletionDate)
	}

	// And: the secret is gone, not merely hidden — the name is free again
	desc := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "forced"})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusBadRequest)
	helpers.AssertJSONError(t, desc, "ResourceNotFoundException")

	recreate := smCall(t, srv, "CreateSecret", map[string]any{"Name": "forced", "SecretString": "again"})
	defer recreate.Body.Close()
	helpers.AssertStatus(t, recreate, http.StatusOK)
}

func TestDeleteSecret_forceRemovesASecretAlreadyScheduled(t *testing.T) {
	// Given: a secret scheduled for deletion
	srv := helpers.NewTestServer(t)
	createSecret(t, srv, "force-over-pending", "val")
	del := deleteSecret(t, srv, map[string]any{"SecretId": "force-over-pending"})
	helpers.AssertStatus(t, del, http.StatusOK)
	del.Body.Close()

	// When: a forced delete follows
	resp := deleteSecret(t, srv, map[string]any{
		"SecretId":                   "force-over-pending",
		"ForceDeleteWithoutRecovery": true,
	})
	defer resp.Body.Close()

	// Then: it succeeds and the record is gone
	helpers.AssertStatus(t, resp, http.StatusOK)
	desc := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "force-over-pending"})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusBadRequest)
	helpers.AssertJSONError(t, desc, "ResourceNotFoundException")
}

// ─── RestoreSecret ───────────────────────────────────────────────────────────

func TestRestoreSecret_cancelsTheScheduledDeletion(t *testing.T) {
	// Given: a secret scheduled for deletion
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	createSecret(t, srv, "restore-me", "val")
	del := deleteSecret(t, srv, map[string]any{"SecretId": "restore-me"})
	helpers.AssertStatus(t, del, http.StatusOK)
	del.Body.Close()

	// When: RestoreSecret is called before the window runs out
	resp := smCall(t, srv, "RestoreSecret", map[string]any{"SecretId": "restore-me"})
	defer resp.Body.Close()

	// Then: it answers with the secret's identity
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ARN  string `json:"ARN"`
		Name string `json:"Name"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Name != "restore-me" || result.ARN == "" {
		t.Errorf("expected ARN and Name for restore-me, got %+v", result)
	}

	// And: the value is readable again and DeletedDate is cleared
	get := smCall(t, srv, "GetSecretValue", map[string]any{"SecretId": "restore-me"})
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusOK)

	desc := smCall(t, srv, "DescribeSecret", map[string]any{"SecretId": "restore-me"})
	defer desc.Body.Close()
	var described struct {
		DeletedDate float64 `json:"DeletedDate"`
	}
	helpers.DecodeJSON(t, desc, &described)
	if described.DeletedDate != 0 {
		t.Errorf("expected DeletedDate cleared, got %v", described.DeletedDate)
	}

	// And: it is listed again without IncludePlannedDeletion
	if !contains(listSecretNames(t, srv, nil), "restore-me") {
		t.Error("expected the restored secret to be listed again")
	}
}

func TestRestoreSecret_notFound(t *testing.T) {
	// Given: no secrets exist
	srv := helpers.NewTestServer(t)

	// When: RestoreSecret names an unknown secret
	resp := smCall(t, srv, "RestoreSecret", map[string]any{"SecretId": "nope"})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func listSecretNames(t *testing.T, srv *helpers.TestServer, body map[string]any) []string {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	resp := smCall(t, srv, "ListSecrets", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		SecretList []struct {
			Name string `json:"Name"`
		} `json:"SecretList"`
	}
	helpers.DecodeJSON(t, resp, &result)
	names := make([]string, 0, len(result.SecretList))
	for _, s := range result.SecretList {
		names = append(names, s.Name)
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
