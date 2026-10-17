// Package backup_test contains integration tests for the AWS Backup emulator.
//
// Every request below is addressed at the binding the pinned AWS model gives
// the operation (backup-2018-11-15), because that is the only address an AWS
// SDK will ever use. #815: Overcast registered no chi routes for Backup at all
// and dispatched the whole service off an invented `X-Amz-Target: AWSBackup.*`
// namespace, so none of these paths answered.
//
// Run: go test ./tests/integration/backup/...
package backup_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The modeled bindings. Vaults hang off /backup-vaults and plans off
// /backup/plans — two subtrees, not one prefix. See
// internal/awsapi/manifest.gen.go.
const (
	pathVaults = "/backup-vaults"
	pathPlans  = "/backup/plans"

	defaultRegion = "us-east-1"
)

// backupDo performs a Backup REST-JSON request in region, signed the way an
// SDK signs one: Backup's SigV4 signing name is "backup" (aws.auth#sigv4 in
// the pinned model), which is also the key middleware classifies it under.
func backupDo(t *testing.T, srv *helpers.TestServer, method, path, region string, body any) *http.Response {
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
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=test/20260811/"+region+"/backup/aws4_request, SignedHeaders=host, Signature=x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func decodeMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	out := map[string]any{}
	helpers.DecodeJSON(t, resp, &out)
	return out
}

func createVault(t *testing.T, srv *helpers.TestServer, name string) map[string]any {
	t.Helper()
	resp := backupDo(t, srv, http.MethodPut, pathVaults+"/"+name, defaultRegion, map[string]any{})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	return decodeMap(t, resp)
}

func createPlan(t *testing.T, srv *helpers.TestServer, name string) map[string]any {
	t.Helper()
	resp := backupDo(t, srv, http.MethodPut, pathPlans, defaultRegion, map[string]any{
		"BackupPlan": map[string]any{
			"BackupPlanName": name,
			"Rules": []map[string]any{
				{"RuleName": "daily", "TargetBackupVaultName": "default"},
			},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	return decodeMap(t, resp)
}

// ─── CreateBackupVault ───────────────────────────────────────────────────────

func TestCreateBackupVault_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateBackupVault is called at PUT /backup-vaults/{BackupVaultName}
	resp := backupDo(t, srv, http.MethodPut, pathVaults+"/vault-a", defaultRegion, map[string]any{})
	defer resp.Body.Close()

	// Then: the modeled CreateBackupVaultOutput comes back
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	body := decodeMap(t, resp)
	if got := body["BackupVaultName"]; got != "vault-a" {
		t.Errorf("BackupVaultName = %v, want vault-a", got)
	}
	if got := body["BackupVaultArn"]; got != "arn:aws:backup:us-east-1:000000000000:backup-vault:vault-a" {
		t.Errorf("BackupVaultArn = %v", got)
	}
	// CreationDate is a restJson1 timestamp with no timestampFormat trait, so
	// it is epoch seconds — a JSON number. The Go SDK's deserialiser rejects a
	// string outright ("expected timestamp to be a JSON Number").
	if _, ok := body["CreationDate"].(float64); !ok {
		t.Errorf("CreationDate = %#v, want a JSON number", body["CreationDate"])
	}
}

func TestCreateBackupVault_duplicateName(t *testing.T) {
	// Given: a vault already exists
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	// When: the same name is created again
	resp := backupDo(t, srv, http.MethodPut, pathVaults+"/vault-a", defaultRegion, map[string]any{})
	defer resp.Body.Close()

	// Then: the modeled AlreadyExistsException, which backup answers 400 —
	// none of its client errors carry an httpError trait.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "AlreadyExistsException")
}

func TestCreateBackupVault_invalidName(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: a name outside BackupVaultName's pattern is used
	resp := backupDo(t, srv, http.MethodPut, pathVaults+"/a", defaultRegion, map[string]any{})
	defer resp.Body.Close()

	// Then: InvalidParameterValueException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

// ─── DescribeBackupVault ─────────────────────────────────────────────────────

func TestDescribeBackupVault_success(t *testing.T) {
	// Given: a vault exists
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	// When: DescribeBackupVault is called at GET /backup-vaults/{BackupVaultName}
	resp := backupDo(t, srv, http.MethodGet, pathVaults+"/vault-a", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the modeled DescribeBackupVaultOutput members Overcast can populate
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	if got := body["BackupVaultName"]; got != "vault-a" {
		t.Errorf("BackupVaultName = %v", got)
	}
	if got := body["VaultType"]; got != "BACKUP_VAULT" {
		t.Errorf("VaultType = %v, want BACKUP_VAULT", got)
	}
	if got := body["VaultState"]; got != "AVAILABLE" {
		t.Errorf("VaultState = %v, want AVAILABLE", got)
	}
	if got, ok := body["NumberOfRecoveryPoints"].(float64); !ok || got != 0 {
		t.Errorf("NumberOfRecoveryPoints = %#v, want 0", body["NumberOfRecoveryPoints"])
	}
}

func TestDescribeBackupVault_notFound(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: an unknown vault is described
	resp := backupDo(t, srv, http.MethodGet, pathVaults+"/nope", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, which backup answers 400 rather than 404
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDescribeBackupVault_foreignAccount(t *testing.T) {
	// Given: a vault exists in this account
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	// When: backupVaultAccountId names another account. It is an httpQuery
	// member, not a body member, and Overcast emulates exactly one account.
	resp := backupDo(t, srv, http.MethodGet,
		pathVaults+"/vault-a?backupVaultAccountId=111122223333", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the vault is not found in that account
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── ListBackupVaults ────────────────────────────────────────────────────────

func TestListBackupVaults_success(t *testing.T) {
	// Given: two vaults exist
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")
	createVault(t, srv, "vault-b")

	// When: ListBackupVaults is called at GET /backup-vaults
	resp := backupDo(t, srv, http.MethodGet, pathVaults, defaultRegion, nil)
	defer resp.Body.Close()

	// Then: both are listed under BackupVaultList
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	list, ok := body["BackupVaultList"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("BackupVaultList = %#v, want two vaults", body["BackupVaultList"])
	}
}

func TestListBackupVaults_paginates(t *testing.T) {
	// Given: two vaults exist
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")
	createVault(t, srv, "vault-b")

	// When: maxResults limits the page. It is an httpQuery member — this is a
	// GET with no request body to carry it in.
	first := backupDo(t, srv, http.MethodGet, pathVaults+"?maxResults=1", defaultRegion, nil)
	defer first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)
	page := decodeMap(t, first)
	token, _ := page["NextToken"].(string)
	if token == "" {
		t.Fatalf("expected a NextToken on a truncated page, got %#v", page)
	}

	// Then: the token fetches the rest
	second := backupDo(t, srv, http.MethodGet, pathVaults+"?maxResults=1&nextToken="+token, defaultRegion, nil)
	defer second.Body.Close()
	helpers.AssertStatus(t, second, http.StatusOK)
	rest := decodeMap(t, second)
	list, ok := rest["BackupVaultList"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("second page = %#v, want one vault", rest["BackupVaultList"])
	}
	if rest["NextToken"] != nil {
		t.Errorf("NextToken = %v on the last page, want it absent", rest["NextToken"])
	}
}

func TestListBackupVaults_filters(t *testing.T) {
	// Given: one vault
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	// When/Then: every vault here is a standard, unshared one, so the two
	// modeled filters either match it or exclude it — never answer with a
	// vault they were asked to exclude.
	cases := map[string]int{
		"?vaultType=BACKUP_VAULT":                      1,
		"?vaultType=LOGICALLY_AIR_GAPPED_BACKUP_VAULT": 0,
		"?shared=true":                                 0,
		"?shared=false":                                1,
	}
	for query, want := range cases {
		t.Run(query, func(t *testing.T) {
			resp := backupDo(t, srv, http.MethodGet, pathVaults+query, defaultRegion, nil)
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)
			list, _ := decodeMap(t, resp)["BackupVaultList"].([]any)
			if len(list) != want {
				t.Errorf("BackupVaultList has %d entries, want %d", len(list), want)
			}
		})
	}
}

func TestListBackupVaults_invalidNextToken(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: an undecodable continuation token is presented
	resp := backupDo(t, srv, http.MethodGet, pathVaults+"?nextToken=not-a-token", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: it is an error rather than a silent restart at page one
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestListBackupVaults_regionsAreIsolated(t *testing.T) {
	// Given: a vault created in us-east-1
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	// When: another region lists its vaults
	resp := backupDo(t, srv, http.MethodGet, pathVaults, "eu-west-1", nil)
	defer resp.Body.Close()

	// Then: it sees none of them — vault names are unique per account per
	// region, and the ARN names the creating region.
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	if list, _ := body["BackupVaultList"].([]any); len(list) != 0 {
		t.Errorf("BackupVaultList = %#v, want empty in eu-west-1", body["BackupVaultList"])
	}
}

// ─── DeleteBackupVault ───────────────────────────────────────────────────────

func TestDeleteBackupVault_success(t *testing.T) {
	// Given: a vault exists
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	// When: it is deleted
	resp := backupDo(t, srv, http.MethodDelete, pathVaults+"/vault-a", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: 200 with no body — DeleteBackupVault's modeled output is Unit, and
	// the SDK discards whatever arrives.
	helpers.AssertStatus(t, resp, http.StatusOK)
	if body := helpers.ReadBody(t, resp); body != "" {
		t.Errorf("body = %q, want empty", body)
	}

	// And: the vault is gone
	after := backupDo(t, srv, http.MethodGet, pathVaults+"/vault-a", defaultRegion, nil)
	defer after.Body.Close()
	helpers.AssertJSONError(t, after, "ResourceNotFoundException")
}

func TestDeleteBackupVault_notFound(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: an unknown vault is deleted
	resp := backupDo(t, srv, http.MethodDelete, pathVaults+"/nope", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── CreateBackupPlan ────────────────────────────────────────────────────────

func TestCreateBackupPlan_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateBackupPlan is called at PUT /backup/plans
	body := createPlan(t, srv, "plan-a")

	// Then: the modeled CreateBackupPlanOutput comes back
	planID, _ := body["BackupPlanId"].(string)
	if planID == "" {
		t.Fatalf("BackupPlanId missing: %#v", body)
	}
	if got := body["BackupPlanArn"]; got != "arn:aws:backup:us-east-1:000000000000:backup-plan:"+planID {
		t.Errorf("BackupPlanArn = %v", got)
	}
	if got, _ := body["VersionId"].(string); got == "" {
		t.Errorf("VersionId missing: %#v", body)
	}
	if _, ok := body["CreationDate"].(float64); !ok {
		t.Errorf("CreationDate = %#v, want a JSON number", body["CreationDate"])
	}
}

func TestCreateBackupPlan_distinctIDsAtOneInstant(t *testing.T) {
	// Given: a server whose clock does not advance between calls
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	// When: two plans are created at the same instant
	first := createPlan(t, srv, "plan-a")
	second := createPlan(t, srv, "plan-b")

	// Then: they are different plans. A clock-derived identifier collided
	// here, and the second create silently replaced the first.
	if first["BackupPlanId"] == second["BackupPlanId"] {
		t.Fatalf("both plans got BackupPlanId %v", first["BackupPlanId"])
	}
	resp := backupDo(t, srv, http.MethodGet, pathPlans, defaultRegion, nil)
	defer resp.Body.Close()
	list, _ := decodeMap(t, resp)["BackupPlansList"].([]any)
	if len(list) != 2 {
		t.Fatalf("BackupPlansList has %d entries, want 2", len(list))
	}
}

func TestCreateBackupPlan_missingName(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: the required BackupPlanName is omitted
	resp := backupDo(t, srv, http.MethodPut, pathPlans, defaultRegion, map[string]any{
		"BackupPlan": map[string]any{"Rules": []any{}},
	})
	defer resp.Body.Close()

	// Then: MissingParameterValueException — the error backup models for a
	// missing required parameter. It declares no ValidationException at all.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "MissingParameterValueException")
}

// ─── GetBackupPlan ───────────────────────────────────────────────────────────

func TestGetBackupPlan_success(t *testing.T) {
	// Given: a plan exists
	srv := helpers.NewTestServer(t)
	planID, _ := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	// When: GetBackupPlan is called at GET /backup/plans/{BackupPlanId}
	resp := backupDo(t, srv, http.MethodGet, pathPlans+"/"+planID, defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the plan comes back under the modeled BackupPlan member
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	plan, ok := body["BackupPlan"].(map[string]any)
	if !ok {
		t.Fatalf("BackupPlan = %#v", body["BackupPlan"])
	}
	if got := plan["BackupPlanName"]; got != "plan-a" {
		t.Errorf("BackupPlanName = %v", got)
	}
	rules, ok := plan["Rules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("Rules = %#v, want the one rule it was created with", plan["Rules"])
	}
}

func TestGetBackupPlan_unknownVersion(t *testing.T) {
	// Given: a plan exists
	srv := helpers.NewTestServer(t)
	planID, _ := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	// When: versionId names a version this emulator does not hold. It is an
	// httpQuery member; Overcast keeps only the current version.
	resp := backupDo(t, srv, http.MethodGet, pathPlans+"/"+planID+"?versionId=nope", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException rather than the current version dressed
	// up as the one that was asked for
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestGetBackupPlan_notFound(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: an unknown plan is fetched
	resp := backupDo(t, srv, http.MethodGet, pathPlans+"/nope", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── UpdateBackupPlan ────────────────────────────────────────────────────────

func TestUpdateBackupPlan_success(t *testing.T) {
	// Given: a plan exists
	srv := helpers.NewTestServer(t)
	created := createPlan(t, srv, "plan-a")
	planID, _ := created["BackupPlanId"].(string)

	// When: UpdateBackupPlan is called at POST /backup/plans/{BackupPlanId}
	resp := backupDo(t, srv, http.MethodPost, pathPlans+"/"+planID, defaultRegion, map[string]any{
		"BackupPlan": map[string]any{
			"BackupPlanName": "plan-a-renamed",
			"Rules": []map[string]any{
				{"RuleName": "weekly", "TargetBackupVaultName": "default"},
			},
		},
	})
	defer resp.Body.Close()

	// Then: a new version is minted and the plan is updated
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	if body["VersionId"] == created["VersionId"] {
		t.Errorf("VersionId unchanged after an update: %v", body["VersionId"])
	}
	if _, ok := body["CreationDate"].(float64); !ok {
		t.Errorf("CreationDate = %#v, want the modeled member", body["CreationDate"])
	}

	get := backupDo(t, srv, http.MethodGet, pathPlans+"/"+planID, defaultRegion, nil)
	defer get.Body.Close()
	plan, _ := decodeMap(t, get)["BackupPlan"].(map[string]any)
	if got := plan["BackupPlanName"]; got != "plan-a-renamed" {
		t.Errorf("BackupPlanName = %v after update", got)
	}
}

func TestUpdateBackupPlan_versionsAreDistinctEachTime(t *testing.T) {
	// Given: a plan that has already been updated once
	srv := helpers.NewTestServer(t)
	planID, _ := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	update := func() string {
		t.Helper()
		resp := backupDo(t, srv, http.MethodPost, pathPlans+"/"+planID, defaultRegion, map[string]any{
			"BackupPlan": map[string]any{
				"BackupPlanName": "plan-a",
				"Rules":          []map[string]any{{"RuleName": "daily", "TargetBackupVaultName": "default"}},
			},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		version, _ := decodeMap(t, resp)["VersionId"].(string)
		return version
	}
	first := update()

	// When: it is updated again
	second := update()

	// Then: the second update mints its own version. A hardcoded "v2" made
	// every update after the first indistinguishable from it.
	if first == second {
		t.Errorf("two updates both produced VersionId %q", first)
	}
}

func TestUpdateBackupPlan_notFound(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: an unknown plan is updated
	resp := backupDo(t, srv, http.MethodPost, pathPlans+"/nope", defaultRegion, map[string]any{
		"BackupPlan": map[string]any{"BackupPlanName": "x", "Rules": []any{}},
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── ListBackupPlans / DeleteBackupPlan ──────────────────────────────────────

func TestListBackupPlans_success(t *testing.T) {
	// Given: a plan exists
	srv := helpers.NewTestServer(t)
	createPlan(t, srv, "plan-a")

	// When: ListBackupPlans is called at GET /backup/plans
	resp := backupDo(t, srv, http.MethodGet, pathPlans, defaultRegion, nil)
	defer resp.Body.Close()

	// Then: it is listed under the modeled BackupPlansList
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	list, ok := body["BackupPlansList"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("BackupPlansList = %#v", body["BackupPlansList"])
	}
	entry, _ := list[0].(map[string]any)
	if got := entry["BackupPlanName"]; got != "plan-a" {
		t.Errorf("BackupPlanName = %v", got)
	}
}

func TestDeleteBackupPlan_success(t *testing.T) {
	// Given: a plan exists
	srv := helpers.NewTestServer(t)
	created := createPlan(t, srv, "plan-a")
	planID, _ := created["BackupPlanId"].(string)

	// When: it is deleted at DELETE /backup/plans/{BackupPlanId}
	resp := backupDo(t, srv, http.MethodDelete, pathPlans+"/"+planID, defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the modeled DeleteBackupPlanOutput, VersionId included
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	if body["BackupPlanId"] != planID {
		t.Errorf("BackupPlanId = %v, want %v", body["BackupPlanId"], planID)
	}
	if body["VersionId"] != created["VersionId"] {
		t.Errorf("VersionId = %v, want %v", body["VersionId"], created["VersionId"])
	}
	if _, ok := body["DeletionDate"].(float64); !ok {
		t.Errorf("DeletionDate = %#v, want a JSON number", body["DeletionDate"])
	}

	// And: it is gone
	after := backupDo(t, srv, http.MethodGet, pathPlans+"/"+planID, defaultRegion, nil)
	defer after.Body.Close()
	helpers.AssertJSONError(t, after, "ResourceNotFoundException")
}

// ─── The surface Backup does not serve ───────────────────────────────────────

func TestBackupJobs_answersNotImplemented(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: a modeled Backup path Overcast does not implement is called
	resp := backupDo(t, srv, http.MethodGet, "/backup-jobs", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the generated registry answers 501, not a bare 404. The routes are
	// registered as absolute paths for exactly this reason — a chi sub-router
	// for /backup-vaults or /backup would own its whole subtree and answer its
	// own NotFound instead.
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
}

func TestBackupVaultSubResource_isNotAnsweredByDescribe(t *testing.T) {
	// Given: a vault exists
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	// When: a sub-resource beneath it — one Overcast does not implement — is
	// requested
	resp := backupDo(t, srv, http.MethodGet, pathVaults+"/vault-a/recovery-points", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: DescribeBackupVault does not answer for it
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
}

func TestBackupTargetPrefix_isNoLongerDispatched(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: a client speaks the X-Amz-Target namespace Overcast invented
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSBackup.ListBackupVaults")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	// Then: it is not served. Every one of backup's 109 modeled operations
	// carries an empty target prefix, so this namespace was a wire contract
	// AWS does not have — and having it is what let #815 hide.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("AWSBackup.ListBackupVaults still answers 200; the invented target namespace is still dispatched")
	}
}

// AWS binds Backup's three collection operations to a URI with a trailing
// slash — ListBackupVaults to GET /backup-vaults/, and ListBackupPlans and
// CreateBackupPlan to /backup/plans/ — and every AWS SDK sends exactly that.
// Overcast registered only the slash-less form, so all three answered 501 to a
// signed client. Worse unsigned: /backup-vaults/ fell through to S3's wildcard
// bucket route and came back 200 with a <ListBucketResult>, which a client can
// mistake for success.
//
// The slash-less forms stay registered: they are what the handwritten callers
// that worked before this fix send, and dropping them would be a second break.
func TestCollectionRoutes_trailingSlash(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")
	createPlan(t, srv, "plan-a")

	// When: each collection is addressed both ways, as the models bind them
	// and as the pre-existing callers send them.
	for _, tc := range []struct {
		name, method, path, listKey string
	}{
		{"ListBackupVaults slashless", http.MethodGet, pathVaults, "BackupVaultList"},
		{"ListBackupVaults trailing", http.MethodGet, pathVaults + "/", "BackupVaultList"},
		{"ListBackupPlans slashless", http.MethodGet, pathPlans, "BackupPlansList"},
		{"ListBackupPlans trailing", http.MethodGet, pathPlans + "/", "BackupPlansList"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := backupDo(t, srv, tc.method, tc.path, defaultRegion, nil)
			defer resp.Body.Close()

			// Then: Backup answers, with its own modeled output.
			helpers.AssertStatus(t, resp, http.StatusOK)
			body := decodeMap(t, resp)
			if _, ok := body[tc.listKey].([]any); !ok {
				t.Fatalf("%s = %#v, want a list — Backup did not answer this path", tc.listKey, body[tc.listKey])
			}
		})
	}

	// CreateBackupPlan is a PUT to the same collection, so it needs the same
	// pair. A minted BackupPlanId proves Backup handled it rather than a
	// fallback returning something that merely parses.
	t.Run("CreateBackupPlan trailing", func(t *testing.T) {
		resp := backupDo(t, srv, http.MethodPut, pathPlans+"/", defaultRegion, map[string]any{
			"BackupPlan": map[string]any{
				"BackupPlanName": "plan-via-trailing-slash",
				"Rules": []any{map[string]any{
					"RuleName": "r", "TargetBackupVaultName": "vault-a",
				}},
			},
		})
		defer resp.Body.Close()

		helpers.AssertStatus(t, resp, http.StatusOK)
		body := decodeMap(t, resp)
		if id, _ := body["BackupPlanId"].(string); id == "" {
			t.Fatalf("BackupPlanId missing: %#v", body)
		}
	})
}

// A path *below* a collection that Backup does not implement must still reach
// the generated 501 rather than being captured by the new trailing-slash
// routes. This is the fallback the absolute-path registration exists to
// protect, and it is the thing a chi sub-router would have eaten.
func TestCollectionRoutes_trailingSlashDoesNotSwallowUnimplemented(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")

	resp := backupDo(t, srv, http.MethodGet, pathVaults+"/vault-a/recovery-points", defaultRegion, nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("GET %s/vault-a/recovery-points = 200; an unimplemented sub-resource must not be served", pathVaults)
	}
}

// GetBackupPlan is bound to a trailing slash too — GET
// /backup/plans/{backupPlanId}/ in the pinned model, unlike UpdateBackupPlan
// and DeleteBackupPlan on the same resource, which carry none. #966 registered
// the pair for the three *collection* routes and stopped there, so the one item
// route AWS models with a slash stayed unreachable: every AWS SDK sends the
// slash, and the request fell past Backup into S3's wildcard.
//
// Both spellings are registered, as they are for the collections, so the
// handwritten callers reaching the slash-less form keep working.
func TestGetBackupPlan_trailingSlash(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")
	planID, _ := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	if planID == "" {
		t.Fatalf("createPlan returned no BackupPlanId")
	}

	for _, path := range []string{
		pathPlans + "/" + planID,
		pathPlans + "/" + planID + "/",
	} {
		t.Run(path, func(t *testing.T) {
			resp := backupDo(t, srv, http.MethodGet, path, defaultRegion, nil)
			defer resp.Body.Close()

			helpers.AssertStatus(t, resp, http.StatusOK)
			body := decodeMap(t, resp)
			if got := body["BackupPlanId"]; got != planID {
				t.Fatalf("BackupPlanId = %v, want %s — Backup did not answer this path", got, planID)
			}
			plan, _ := body["BackupPlan"].(map[string]any)
			if got := plan["BackupPlanName"]; got != "plan-a" {
				t.Errorf("BackupPlan.BackupPlanName = %v, want plan-a", got)
			}
		})
	}
}

// The trailing-slash item route must not capture the plan sub-resources Backup
// does not implement — /backup/plans/{id}/versions/ is one (selections is
// implemented now, #2308) — which has to keep reaching the generated 501. Same
// guard as the collection routes carry, for the same reason.
func TestGetBackupPlan_trailingSlashDoesNotSwallowUnimplemented(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createVault(t, srv, "vault-a")
	planID, _ := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	for _, suffix := range []string{"/versions/"} {
		path := pathPlans + "/" + planID + suffix
		resp := backupDo(t, srv, http.MethodGet, path, defaultRegion, nil)
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("GET %s = %d, want 501; an unimplemented sub-resource must not be served", path, resp.StatusCode)
		}
		resp.Body.Close() //nolint:errcheck
	}
}
