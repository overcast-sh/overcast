package backup_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// Unsigned requests (#963).
//
// Overcast's REST fallback treats a request with no SigV4 credential scope as
// S3's (internal/router, addressesNonS3). Before #966 that meant an unsigned
// GET /backup-vaults/ — the URI every AWS SDK sends for ListBackupVaults — fell
// past Backup into S3's wildcard bucket route and came back HTTP 200 with a
// <ListBucketResult> naming a bucket "backup-vaults", which a caller reads as an
// empty-but-successful list. #966 registered the trailing-slash spellings, and
// the compat cli suite's natives, which run `--no-sign-request`, were the only
// thing that kept the unsigned path covered: every test in backup_test.go signs
// its requests through backupDo.
//
// Backup's routes are chi routes registered ahead of S3's wildcard, so today an
// unsigned request to any route Backup binds IS answered by Backup, with its own
// modeled JSON. These tests pin that. Unsigned is the only unauthenticated form
// an SDK produces (--no-sign-request, anonymous credentials); a bearer token is
// not something Backup's model accepts, so it is not covered here.

// backupUnsignedDo is backupDo without the Authorization header: what the AWS
// CLI sends under --no-sign-request.
func backupUnsignedDo(t *testing.T, srv *helpers.TestServer, method, path string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// assertBackupJSON checks that resp was written by Backup and not by S3: the
// status is want, the body is JSON of the type Backup's protocol declares, and
// it carries no S3 XML. It returns the decoded body ({} for an empty one).
func assertBackupJSON(t *testing.T, resp *http.Response, want int) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, want, raw)
	}
	if strings.Contains(string(raw), "<ListBucketResult") || strings.HasPrefix(strings.TrimSpace(string(raw)), "<") {
		t.Fatalf("an XML body means S3 answered, not Backup (#963): %s", raw)
	}
	out := map[string]any{}
	// An operation whose modeled output is Unit (DeleteBackupVault) answers with
	// no body and so no Content-Type; everything else is JSON.
	if len(bytes.TrimSpace(raw)) > 0 {
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
			t.Fatalf("Content-Type = %q, want a JSON type; body: %s", ct, raw)
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("body is not a JSON object: %v: %s", err, raw)
		}
	}
	return out
}

func vaultNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	items, ok := body["BackupVaultList"].([]any)
	if !ok {
		t.Fatalf("BackupVaultList = %#v, want a list — Backup did not answer (#963): %#v", body["BackupVaultList"], body)
	}
	var names []string
	for _, it := range items {
		m, _ := it.(map[string]any)
		name, _ := m["BackupVaultName"].(string)
		names = append(names, name)
	}
	return names
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The vault half of the cli native's unsigned calls, in the order it makes
// them: create, describe, list (both spellings and ?shared=true), delete.
func TestBackupVaults_unsigned(t *testing.T) {
	// Given: an empty store and a client that sends no Authorization header
	srv := helpers.NewTestServer(t)
	const name = "unsigned-vault"

	t.Run("CreateBackupVault", func(t *testing.T) {
		// When: the vault is created at PUT /backup-vaults/{name}
		body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodPut, pathVaults+"/"+name, map[string]any{}), http.StatusOK)

		// Then: the modeled CreateBackupVaultOutput comes back
		if got := body["BackupVaultName"]; got != name {
			t.Errorf("BackupVaultName = %v, want %s", got, name)
		}
		if arn, _ := body["BackupVaultArn"].(string); !strings.HasSuffix(arn, ":backup-vault:"+name) {
			t.Errorf("BackupVaultArn = %q, want a suffix of :backup-vault:%s", arn, name)
		}
	})

	t.Run("DescribeBackupVault", func(t *testing.T) {
		body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodGet, pathVaults+"/"+name, nil), http.StatusOK)
		if got := body["BackupVaultName"]; got != name {
			t.Errorf("BackupVaultName = %v, want %s", got, name)
		}
		if got := body["VaultState"]; got != "AVAILABLE" {
			t.Errorf("VaultState = %v, want AVAILABLE", got)
		}
	})

	// ListBackupVaults, both spellings: the trailing slash is what every SDK
	// sends and the one #963 sent to S3.
	for _, path := range []string{pathVaults, pathVaults + "/"} {
		t.Run("ListBackupVaults "+path, func(t *testing.T) {
			body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodGet, path, nil), http.StatusOK)
			if names := vaultNames(t, body); !contains(names, name) {
				t.Errorf("%q is not in %v", name, names)
			}
		})
	}

	// The by-shared filter is the cli native's guard against the S3
	// fall-through: an empty BackupVaultList is a legitimate answer, a missing
	// one is not.
	t.Run("ListBackupVaults shared", func(t *testing.T) {
		body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodGet, pathVaults+"/?shared=true", nil), http.StatusOK)
		if names := vaultNames(t, body); contains(names, name) {
			t.Errorf("shared=true returned the unshared vault %q", name)
		}
	})

	t.Run("DeleteBackupVault", func(t *testing.T) {
		resp := backupUnsignedDo(t, srv, http.MethodDelete, pathVaults+"/"+name, nil)
		assertBackupJSON(t, resp, http.StatusOK)

		// Then: the vault is gone, and Backup says so in its own error shape
		body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodGet, pathVaults+"/"+name, nil), http.StatusBadRequest)
		if got := body["__type"]; got != "ResourceNotFoundException" {
			t.Errorf("__type = %v, want ResourceNotFoundException", got)
		}
	})
}

// The plan half: create, get (both spellings), list (both spellings), update,
// delete.
func TestBackupPlans_unsigned(t *testing.T) {
	// Given: a vault for the rules to target, and a client that sends no
	// Authorization header
	srv := helpers.NewTestServer(t)
	assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodPut, pathVaults+"/plan-target", map[string]any{}), http.StatusOK)
	plan := func(name string) map[string]any {
		return map[string]any{"BackupPlan": map[string]any{
			"BackupPlanName": name,
			"Rules":          []any{map[string]any{"RuleName": "daily", "TargetBackupVaultName": "plan-target"}},
		}}
	}
	var planID string

	t.Run("CreateBackupPlan", func(t *testing.T) {
		// When: the plan is created at PUT /backup/plans/ (the trailing-slash
		// binding every SDK sends; unsigned it was a 404 before #966)
		body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodPut, pathPlans+"/", plan("unsigned-plan")), http.StatusOK)

		// Then: a minted BackupPlanId proves Backup handled it
		planID, _ = body["BackupPlanId"].(string)
		if planID == "" {
			t.Fatalf("BackupPlanId is empty: %#v", body)
		}
		if arn, _ := body["BackupPlanArn"].(string); !strings.HasSuffix(arn, ":backup-plan:"+planID) {
			t.Errorf("BackupPlanArn = %q, want a suffix of :backup-plan:%s", arn, planID)
		}
		if v, _ := body["VersionId"].(string); v == "" {
			t.Errorf("VersionId is empty: %#v", body)
		}
	})

	for _, suffix := range []string{"", "/"} {
		t.Run("GetBackupPlan id"+suffix, func(t *testing.T) {
			body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodGet, pathPlans+"/"+planID+suffix, nil), http.StatusOK)
			if got := body["BackupPlanId"]; got != planID {
				t.Errorf("BackupPlanId = %v, want %s", got, planID)
			}
		})
	}

	for _, path := range []string{pathPlans, pathPlans + "/"} {
		t.Run("ListBackupPlans "+path, func(t *testing.T) {
			body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodGet, path, nil), http.StatusOK)
			items, ok := body["BackupPlansList"].([]any)
			if !ok {
				t.Fatalf("BackupPlansList = %#v, want a list — Backup did not answer (#963)", body["BackupPlansList"])
			}
			found := false
			for _, it := range items {
				m, _ := it.(map[string]any)
				found = found || m["BackupPlanId"] == planID
			}
			if !found {
				t.Errorf("plan %s is not in %v", planID, items)
			}
		})
	}

	t.Run("UpdateBackupPlan", func(t *testing.T) {
		body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodPost, pathPlans+"/"+planID, plan("unsigned-plan-renamed")), http.StatusOK)
		if got := body["BackupPlanId"]; got != planID {
			t.Errorf("BackupPlanId = %v, want %s", got, planID)
		}
		if v, _ := body["VersionId"].(string); v == "" {
			t.Errorf("VersionId is empty: %#v", body)
		}
	})

	t.Run("DeleteBackupPlan", func(t *testing.T) {
		body := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodDelete, pathPlans+"/"+planID, nil), http.StatusOK)
		if got := body["BackupPlanId"]; got != planID {
			t.Errorf("BackupPlanId = %v, want %s", got, planID)
		}

		// Then: the plan is gone, in Backup's own error shape
		gone := assertBackupJSON(t, backupUnsignedDo(t, srv, http.MethodGet, pathPlans+"/"+planID+"/", nil), http.StatusBadRequest)
		if got := gone["__type"]; got != "ResourceNotFoundException" {
			t.Errorf("__type = %v, want ResourceNotFoundException", got)
		}
	})
}
