// Package eks_test contains integration tests for the EKS emulator.
//
// Run: go test ./tests/integration/eks/...
package eks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/tests/helpers"
)

func eksCall(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http request: %v", err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func newEKSServer(t *testing.T) *helpers.TestServer {
	t.Helper()
	return helpers.NewTestServer(t)
}

func expectStatus(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		body := helpers.ReadBody(t, resp)
		t.Fatalf("expected %d, got %d; body=%s", expected, resp.StatusCode, body)
	}
	if expected == http.StatusNotImplemented {
		body := decodeBody(t, resp)
		if body["__type"] != "NotImplemented" {
			t.Fatalf("expected NotImplemented body for 501 response, got %#v", body)
		}
		return
	}
	if expected == http.StatusServiceUnavailable {
		body := decodeBody(t, resp)
		if body["__type"] != "ServiceUnavailableException" {
			t.Fatalf("expected ServiceUnavailableException body for 503 response, got %#v", body)
		}
		return
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
}

func expectJSONStatus(t *testing.T, resp *http.Response, expected int) map[string]any {
	t.Helper()
	if resp.StatusCode != expected {
		body := helpers.ReadBody(t, resp)
		t.Fatalf("expected %d, got %d; body=%s", expected, resp.StatusCode, body)
	}
	return decodeBody(t, resp)
}

func expectResourceNotFound(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, resp, http.StatusNotFound)
	if body["__type"] != "ResourceNotFoundException" {
		t.Fatalf("expected ResourceNotFoundException for 404 response, got %#v", body)
	}
	msg, _ := body["message"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Fatalf("expected non-empty message for ResourceNotFoundException, got %#v", body)
	}
	return body
}

func mustCreateCluster(t *testing.T, baseURL, name string, extraFields map[string]any) map[string]any {
	t.Helper()
	payload := map[string]any{
		"name":    name,
		"roleArn": "arn:aws:iam::000000000000:role/eks-role",
	}
	for key, value := range extraFields {
		payload[key] = value
	}
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters", payload), http.StatusCreated)
	cluster, _ := body["cluster"].(map[string]any)
	return cluster
}

func mustCreatePodIdentityAssociation(t *testing.T, baseURL, clusterName string, payload map[string]any) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/pod-identity-associations", payload), http.StatusCreated)
	association, _ := body["association"].(map[string]any)
	return association
}

func mustCreateNodegroup(t *testing.T, baseURL, clusterName, nodegroupName string, subnets []string) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/node-groups", map[string]any{
		"nodegroupName": nodegroupName,
		"nodeRole":      "arn:aws:iam::000000000000:role/eks-node-role",
		"subnets":       subnets,
	}), http.StatusCreated)
	nodegroup, _ := body["nodegroup"].(map[string]any)
	return nodegroup
}

func mustCreateFargateProfile(t *testing.T, baseURL, clusterName, profileName string, selectors []map[string]any) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/fargate-profiles", map[string]any{
		"fargateProfileName":  profileName,
		"podExecutionRoleArn": "arn:aws:iam::000000000000:role/fargate-pod-exec",
		"selectors":           selectors,
	}), http.StatusCreated)
	profile, _ := body["fargateProfile"].(map[string]any)
	return profile
}

func mustCreateAddon(t *testing.T, baseURL, clusterName, addonName, addonVersion string) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/addons", map[string]any{
		"addonName":    addonName,
		"addonVersion": addonVersion,
	}), http.StatusCreated)
	addon, _ := body["addon"].(map[string]any)
	return addon
}

func mustCreateAccessEntry(t *testing.T, baseURL, clusterName, principalArn string, extraFields map[string]any) map[string]any {
	t.Helper()
	payload := map[string]any{"principalArn": principalArn}
	for key, value := range extraFields {
		payload[key] = value
	}
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/access-entries", payload), http.StatusCreated)
	entry, _ := body["accessEntry"].(map[string]any)
	return entry
}

func oidcConfigPayload(name string) map[string]any {
	return map[string]any{
		"oidc": map[string]any{
			"identityProviderConfigName": name,
			"issuerUrl":                  "https://idp.example.com",
			"clientId":                   "kubernetes",
			"usernameClaim":              "sub",
			"groupsClaim":                "groups",
		},
	}
}

func mustAssociateIdentityProviderConfig(t *testing.T, baseURL, clusterName, configName string) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/identity-provider-configs/associate", oidcConfigPayload(configName)), http.StatusOK)
	update, _ := body["update"].(map[string]any)
	return update
}

// mustDescribeIdentityProviderConfig returns the OidcIdentityProviderConfig the
// modeled DescribeIdentityProviderConfig nests under
// identityProviderConfig.oidc.
func mustDescribeIdentityProviderConfig(t *testing.T, baseURL, clusterName, configName string) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/identity-provider-configs/describe",
		map[string]any{"identityProviderConfig": map[string]any{"type": "oidc", "name": configName}}), http.StatusOK)
	config, ok := body["identityProviderConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected identityProviderConfig in describe response, got %#v", body)
	}
	oidc, ok := config["oidc"].(map[string]any)
	if !ok {
		t.Fatalf("expected oidc in identityProviderConfig, got %#v", config)
	}
	return oidc
}

func mustAssociateAccessPolicy(t *testing.T, baseURL, clusterName, principalArn string, payload map[string]any) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodPost, baseURL+"/clusters/"+clusterName+"/access-entries/"+url.PathEscape(principalArn)+"/access-policies", payload), http.StatusCreated)
	association, _ := body["associatedAccessPolicy"].(map[string]any)
	return association
}

func mustTagResource(t *testing.T, baseURL, resourceARN string, tags map[string]string) {
	t.Helper()
	expectStatus(t, eksCall(t, http.MethodPost, baseURL+"/tags/"+url.PathEscape(resourceARN), map[string]any{"tags": tags}), http.StatusOK)
}

func listResourceTags(t *testing.T, baseURL, resourceARN string) map[string]any {
	t.Helper()
	body := expectJSONStatus(t, eksCall(t, http.MethodGet, baseURL+"/tags/"+url.PathEscape(resourceARN), nil), http.StatusOK)
	tags, _ := body["tags"].(map[string]any)
	return tags
}

func TestEKSClusterLifecycle(t *testing.T) {
	srv := newEKSServer(t)

	cluster := mustCreateCluster(t, srv.URL, "demo-cluster", map[string]any{
		"version": "1.31",
	})
	if cluster["name"] != "demo-cluster" {
		t.Fatalf("unexpected cluster name: %v", cluster["name"])
	}

	listResp := eksCall(t, http.MethodGet, srv.URL+"/clusters", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}
	listBody := decodeBody(t, listResp)
	names, ok := listBody["clusters"].([]any)
	if !ok || len(names) != 1 || names[0] != "demo-cluster" {
		t.Fatalf("unexpected clusters list: %#v", listBody["clusters"])
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	descBody := decodeBody(t, descResp)
	descCluster := descBody["cluster"].(map[string]any)
	if descCluster["status"] != "ACTIVE" {
		t.Fatalf("unexpected cluster status: %v", descCluster["status"])
	}

	delResp := eksCall(t, http.MethodDelete, srv.URL+"/clusters/demo-cluster", nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", delResp.StatusCode)
	}
	_ = decodeBody(t, delResp)

	_ = expectResourceNotFound(t, eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster", nil))
}

func TestEKSLiveModeDescribeClusterReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-cluster", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListClustersFiltersLegacyMockRecords(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-filter-cluster", nil)

	// Sanity check the source record exists in mock mode.
	mockList := expectJSONStatus(t, eksCall(t, http.MethodGet, mockSrv.URL+"/clusters", nil), http.StatusOK)
	mockClusters, _ := mockList["clusters"].([]any)
	if len(mockClusters) != 1 || mockClusters[0] != "live-filter-cluster" {
		t.Fatalf("expected mock mode to list seeded cluster, got %#v", mockList["clusters"])
	}

	liveList := expectJSONStatus(t, eksCall(t, http.MethodGet, liveSrv.URL+"/clusters", nil), http.StatusOK)
	liveClusters, _ := liveList["clusters"].([]any)
	if len(liveClusters) != 0 {
		t.Fatalf("expected live mode to filter legacy mock records from list, got %#v", liveList["clusters"])
	}
}

func TestEKSLiveModeDeleteLegacyMockClusterAllowed(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-delete-legacy", nil)

	deleteResp := eksCall(t, http.MethodDelete, liveSrv.URL+"/clusters/live-delete-legacy", nil)
	expectStatus(t, deleteResp, http.StatusOK)

	// Ensure the cluster is gone from the underlying shared store, not just hidden by live-mode filtering.
	_ = expectResourceNotFound(t, eksCall(t, http.MethodGet, mockSrv.URL+"/clusters/live-delete-legacy", nil))
}

func TestEKSLiveModeUpdateClusterConfigReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-update-config-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-update-config-cluster/update-config", map[string]any{
		"logging": map[string]any{"clusterLogging": []any{}},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListUpdatesReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-list-updates-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-list-updates-cluster/updates", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListInsightsReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-list-insights-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-list-insights-cluster/insights", map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribeUpdateReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-update-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-update-cluster/updates/upd-123", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribeInsightReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-insight-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-insight-cluster/insights/platform-version-check", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUpdateClusterVersionReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-update-version-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-update-version-cluster/updates", map[string]any{
		"version": "1.32",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListNodegroupsReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-nodegroups-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-nodegroups-cluster/node-groups", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeCreateNodegroupReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-create-nodegroup-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-create-nodegroup-cluster/node-groups", map[string]any{
		"nodegroupName": "workers-a",
		"nodeRole":      "arn:aws:iam::000000000000:role/eks-node-role",
		"subnets":       []string{"subnet-1"},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribeNodegroupReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-nodegroup-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-nodegroup-cluster/node-groups/workers-a", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDeleteNodegroupReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-delete-nodegroup-cluster", nil)

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/clusters/live-delete-nodegroup-cluster/node-groups/workers-a", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListAccessEntriesReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-access-entries-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-access-entries-cluster/access-entries", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribeAccessEntryReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-access-entry-cluster", nil)

	principalArn := "arn:aws:iam::000000000000:role/dev-admin"
	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-access-entry-cluster/access-entries/"+url.PathEscape(principalArn), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeCreateAccessEntryReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-create-access-entry-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-create-access-entry-cluster/access-entries", map[string]any{
		"principalArn": "arn:aws:iam::000000000000:role/dev-admin",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUpdateAccessEntryReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-update-access-entry-cluster", nil)

	principalArn := "arn:aws:iam::000000000000:role/dev-admin"
	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-update-access-entry-cluster/access-entries/"+url.PathEscape(principalArn), map[string]any{
		"username": "dev-admin",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDeleteAccessEntryReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-delete-access-entry-cluster", nil)

	principalArn := "arn:aws:iam::000000000000:role/dev-admin"
	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/clusters/live-delete-access-entry-cluster/access-entries/"+url.PathEscape(principalArn), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeAssociateAccessPolicyReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-associate-access-policy-cluster", nil)

	principalArn := "arn:aws:iam::000000000000:role/dev-admin"
	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-associate-access-policy-cluster/access-entries/"+url.PathEscape(principalArn)+"/access-policies", map[string]any{
		"policyArn": "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListAssociatedAccessPoliciesReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-list-associated-access-policies-cluster", nil)

	principalArn := "arn:aws:iam::000000000000:role/dev-admin"
	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-list-associated-access-policies-cluster/access-entries/"+url.PathEscape(principalArn)+"/access-policies", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDisassociateAccessPolicyReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-disassociate-access-policy-cluster", nil)

	principalArn := "arn:aws:iam::000000000000:role/dev-admin"
	policyArn := "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/clusters/live-disassociate-access-policy-cluster/access-entries/"+url.PathEscape(principalArn)+"/access-policies/"+url.PathEscape(policyArn), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListIdentityProviderConfigsReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-idp-configs-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-idp-configs-cluster/identity-provider-configs", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribeIdentityProviderConfigReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-idp-config-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-describe-idp-config-cluster/identity-provider-configs/describe",
		map[string]any{"identityProviderConfig": map[string]any{"type": "oidc", "name": "oidc-primary"}})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeAssociateIdentityProviderConfigReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-associate-idp-config-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-associate-idp-config-cluster/identity-provider-configs/associate", oidcConfigPayload("oidc-primary"))
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDisassociateIdentityProviderConfigReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-disassociate-idp-config-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-disassociate-idp-config-cluster/identity-provider-configs/disassociate", map[string]any{
		"identityProviderConfig": map[string]any{
			"type": "oidc",
			"name": "oidc-primary",
		},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListPodIdentityAssociationsReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-list-pod-identity-associations-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-list-pod-identity-associations-cluster/pod-identity-associations", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribePodIdentityAssociationReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-pod-identity-association-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-pod-identity-association-cluster/pod-identity-associations/pia-123", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeCreatePodIdentityAssociationReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-create-pod-identity-association-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-create-pod-identity-association-cluster/pod-identity-associations", map[string]any{
		"namespace":      "default",
		"serviceAccount": "api-sa",
		"roleArn":        "arn:aws:iam::000000000000:role/eks-pod-role",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUpdatePodIdentityAssociationReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-update-pod-identity-association-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-update-pod-identity-association-cluster/pod-identity-associations/pia-123", map[string]any{
		"roleArn": "arn:aws:iam::000000000000:role/eks-pod-role-updated",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDeletePodIdentityAssociationReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-delete-pod-identity-association-cluster", nil)

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/clusters/live-delete-pod-identity-association-cluster/pod-identity-associations/pia-123", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListFargateProfilesReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-fargate-profiles-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-fargate-profiles-cluster/fargate-profiles", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribeFargateProfileReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-fargate-profile-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-fargate-profile-cluster/fargate-profiles/fp-1", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeCreateFargateProfileReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-create-fargate-profile-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-create-fargate-profile-cluster/fargate-profiles", map[string]any{
		"fargateProfileName":  "fp-1",
		"podExecutionRoleArn": "arn:aws:iam::000000000000:role/eks-fargate",
		"selectors": []map[string]any{
			{"namespace": "default"},
		},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDeleteFargateProfileReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-delete-fargate-profile-cluster", nil)

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/clusters/live-delete-fargate-profile-cluster/fargate-profiles/fp-1", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListAddonsReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-addons-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-addons-cluster/addons", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDescribeAddonReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-describe-addon-cluster", nil)

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/clusters/live-describe-addon-cluster/addons/vpc-cni", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUpdateNodegroupConfigReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-update-ng-config-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-update-ng-config-cluster/node-groups/ng-1/update-config", map[string]any{
		"scalingConfig": map[string]any{"desiredSize": 3},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUpdateNodegroupVersionReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-update-ng-version-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-update-ng-version-cluster/node-groups/ng-1/update-version", map[string]any{
		"version": "1.32",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeCreateAddonReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-create-addon-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-create-addon-cluster/addons", map[string]any{
		"addonName": "vpc-cni",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUpdateAddonReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-update-addon-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/clusters/live-update-addon-cluster/addons/vpc-cni/update", map[string]any{
		"addonVersion": "v1.15.0",
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeDeleteAddonReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-delete-addon-cluster", nil)

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/clusters/live-delete-addon-cluster/addons/vpc-cni", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListTagsForLegacyMockClusterReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	cluster := mustCreateCluster(t, mockSrv.URL, "live-list-tags-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(arn), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockClusterReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	cluster := mustCreateCluster(t, mockSrv.URL, "live-tag-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(arn), map[string]any{"tags": map[string]string{"env": "live"}})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockClusterMalformedRequestStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	cluster := mustCreateCluster(t, mockSrv.URL, "live-malformed-tag-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(arn), map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockClusterReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	cluster := mustCreateCluster(t, mockSrv.URL, "live-untag-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(arn)+"?tagKeys=env", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockClusterMissingTagKeysStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	cluster := mustCreateCluster(t, mockSrv.URL, "live-malformed-untag-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(arn), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListAccessPoliciesStillWorks(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEKSMode(config.EKSModeLive),
	)

	body := expectJSONStatus(t, eksCall(t, http.MethodGet, srv.URL+"/access-policies", nil), http.StatusOK)
	policies, _ := body["accessPolicies"].([]any)
	if len(policies) == 0 {
		t.Fatalf("expected at least one access policy in live mode, got %v", body["accessPolicies"])
	}
}

func TestEKSLiveModeDescribeClusterVersionsStillWorks(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEKSMode(config.EKSModeLive),
	)

	body := expectJSONStatus(t, eksCall(t, http.MethodGet, srv.URL+"/cluster-versions", nil), http.StatusOK)
	versions, _ := body["clusterVersions"].([]any)
	if len(versions) == 0 {
		t.Fatalf("expected at least one cluster version in live mode, got %v", body["clusterVersions"])
	}
}

func TestEKSLiveModeDescribeAddonVersionsStillWorks(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEKSMode(config.EKSModeLive),
	)

	body := expectJSONStatus(t, eksCall(t, http.MethodGet, srv.URL+"/addons/supported-versions?addonName=vpc-cni", nil), http.StatusOK)
	addons, _ := body["addons"].([]any)
	if len(addons) == 0 {
		t.Fatalf("expected at least one addon catalog entry in live mode, got %v", body["addons"])
	}
}

func TestEKSLiveModeDescribeAddonVersionsUnknownStillReturnsEmpty(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEKSMode(config.EKSModeLive),
	)

	body := expectJSONStatus(t, eksCall(t, http.MethodGet, srv.URL+"/addons/supported-versions?addonName=does-not-exist", nil), http.StatusOK)
	addons, _ := body["addons"].([]any)
	if len(addons) != 0 {
		t.Fatalf("expected empty addon catalog for unknown addon in live mode, got %v", body["addons"])
	}
}

func TestEKSLiveModeDescribeAddonConfigurationStillWorks(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEKSMode(config.EKSModeLive),
	)

	body := expectJSONStatus(t, eksCall(t, http.MethodGet, srv.URL+"/addons/configuration-schemas?addonName=vpc-cni&addonVersion=v1.18.3-eksbuild.3", nil), http.StatusOK)
	if body["addonName"] != "vpc-cni" {
		t.Fatalf("expected addonName vpc-cni in live mode, got %#v", body)
	}
	if body["configurationSchema"] == nil {
		t.Fatalf("expected configurationSchema in live mode response, got %#v", body)
	}
}

func TestEKSLiveModeDescribeAddonConfigurationMissingStillReturnsNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = expectResourceNotFound(t, eksCall(t, http.MethodGet, srv.URL+"/addons/configuration-schemas?addonName=does-not-exist&addonVersion=v1.0.0", nil))
}

func TestEKSLiveModeListTagsForLegacyMockNodegroupReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-list-nodegroup-tags-cluster", nil)
	nodegroup := mustCreateNodegroup(t, mockSrv.URL, "live-list-nodegroup-tags-cluster", "workers-a", []string{"subnet-1"})
	nodegroupARN, _ := nodegroup["nodegroupArn"].(string)
	if nodegroupARN == "" {
		t.Fatalf("expected nodegroup ARN in create response, got %#v", nodegroup)
	}

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(nodegroupARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockNodegroupReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-tag-nodegroup-tags-cluster", nil)
	nodegroup := mustCreateNodegroup(t, mockSrv.URL, "live-tag-nodegroup-tags-cluster", "workers-a", []string{"subnet-1"})
	nodegroupARN, _ := nodegroup["nodegroupArn"].(string)
	if nodegroupARN == "" {
		t.Fatalf("expected nodegroup ARN in create response, got %#v", nodegroup)
	}

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(nodegroupARN), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockNodegroupMalformedRequestStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-malformed-nodegroup-tag-cluster", nil)
	nodegroup := mustCreateNodegroup(t, mockSrv.URL, "live-malformed-nodegroup-tag-cluster", "workers-a", []string{"subnet-1"})
	nodegroupARN, _ := nodegroup["nodegroupArn"].(string)
	if nodegroupARN == "" {
		t.Fatalf("expected nodegroup ARN in create response, got %#v", nodegroup)
	}

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(nodegroupARN), map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockNodegroupReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-untag-nodegroup-tags-cluster", nil)
	nodegroup := mustCreateNodegroup(t, mockSrv.URL, "live-untag-nodegroup-tags-cluster", "workers-a", []string{"subnet-1"})
	nodegroupARN, _ := nodegroup["nodegroupArn"].(string)
	if nodegroupARN == "" {
		t.Fatalf("expected nodegroup ARN in create response, got %#v", nodegroup)
	}

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(nodegroupARN)+"?tagKeys=env", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockNodegroupMissingTagKeysStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-malformed-nodegroup-untag-cluster", nil)
	nodegroup := mustCreateNodegroup(t, mockSrv.URL, "live-malformed-nodegroup-untag-cluster", "workers-a", []string{"subnet-1"})
	nodegroupARN, _ := nodegroup["nodegroupArn"].(string)
	if nodegroupARN == "" {
		t.Fatalf("expected nodegroup ARN in create response, got %#v", nodegroup)
	}

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(nodegroupARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListTagsForLegacyMockAddonArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-list-addon-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	addonARN := "arn:aws:eks:us-east-1:000000000000:addon/" + clusterName + "/vpc-cni/mock-addon"

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(addonARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockAddonArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-tag-addon-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	addonARN := "arn:aws:eks:us-east-1:000000000000:addon/" + clusterName + "/vpc-cni/mock-addon"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(addonARN), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockAddonArnMalformedRequestStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-addon-tag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	addonARN := "arn:aws:eks:us-east-1:000000000000:addon/" + clusterName + "/vpc-cni/mock-addon"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(addonARN), map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockAddonArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-untag-addon-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	addonARN := "arn:aws:eks:us-east-1:000000000000:addon/" + clusterName + "/vpc-cni/mock-addon"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(addonARN)+"?tagKeys=env", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockAddonArnMissingTagKeysStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-addon-untag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	addonARN := "arn:aws:eks:us-east-1:000000000000:addon/" + clusterName + "/vpc-cni/mock-addon"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(addonARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockFargateProfileArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-tag-fargate-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	fargateARN := "arn:aws:eks:us-east-1:000000000000:fargateprofile/" + clusterName + "/fp-1/mock-fargate"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(fargateARN), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListTagsForLegacyMockFargateProfileArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-list-fargate-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	fargateARN := "arn:aws:eks:us-east-1:000000000000:fargateprofile/" + clusterName + "/fp-1/mock-fargate"

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(fargateARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockFargateProfileArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-untag-fargate-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	fargateARN := "arn:aws:eks:us-east-1:000000000000:fargateprofile/" + clusterName + "/fp-1/mock-fargate"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(fargateARN)+"?tagKeys=env", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockFargateProfileArnMalformedRequestStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-fargate-tag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	fargateARN := "arn:aws:eks:us-east-1:000000000000:fargateprofile/" + clusterName + "/fp-1/mock-fargate"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(fargateARN), map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockFargateProfileArnMissingTagKeysStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-fargate-untag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	fargateARN := "arn:aws:eks:us-east-1:000000000000:fargateprofile/" + clusterName + "/fp-1/mock-fargate"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(fargateARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockPodIdentityAssociationArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-untag-podid-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	podIDARN := "arn:aws:eks:us-east-1:000000000000:podidentityassociation/" + clusterName + "/pia-123"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(podIDARN)+"?tagKeys=env", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListTagsForLegacyMockPodIdentityAssociationArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-list-podid-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	podIDARN := "arn:aws:eks:us-east-1:000000000000:podidentityassociation/" + clusterName + "/pia-123"

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(podIDARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockPodIdentityAssociationArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-tag-podid-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	podIDARN := "arn:aws:eks:us-east-1:000000000000:podidentityassociation/" + clusterName + "/pia-123"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(podIDARN), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockPodIdentityAssociationArnMalformedRequestStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-podid-tag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	podIDARN := "arn:aws:eks:us-east-1:000000000000:podidentityassociation/" + clusterName + "/pia-123"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(podIDARN), map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockPodIdentityAssociationArnMissingTagKeysStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-podid-untag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	podIDARN := "arn:aws:eks:us-east-1:000000000000:podidentityassociation/" + clusterName + "/pia-123"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(podIDARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListTagsForLegacyMockIdentityProviderConfigArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-list-idp-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	idpARN := "arn:aws:eks:us-east-1:000000000000:identityproviderconfig/" + clusterName + "/oidc/okta-main"

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(idpARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockIdentityProviderConfigArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-tag-idp-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	idpARN := "arn:aws:eks:us-east-1:000000000000:identityproviderconfig/" + clusterName + "/oidc/okta-main"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(idpARN), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockIdentityProviderConfigArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-untag-idp-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	idpARN := "arn:aws:eks:us-east-1:000000000000:identityproviderconfig/" + clusterName + "/oidc/okta-main"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(idpARN)+"?tagKeys=env", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockIdentityProviderConfigArnMalformedRequestStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-idp-tag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	idpARN := "arn:aws:eks:us-east-1:000000000000:identityproviderconfig/" + clusterName + "/oidc/okta-main"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(idpARN), map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockIdentityProviderConfigArnMissingTagKeysStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-idp-untag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	idpARN := "arn:aws:eks:us-east-1:000000000000:identityproviderconfig/" + clusterName + "/oidc/okta-main"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(idpARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeListTagsForLegacyMockAccessEntryArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-list-access-entry-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	accessEntryARN := "arn:aws:eks:us-east-1:000000000000:access-entry/" + clusterName + "/arn%3Aaws%3Aiam%3A%3A000000000000%3Arole%2Fdev"

	resp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(accessEntryARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockAccessEntryArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-tag-access-entry-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	accessEntryARN := "arn:aws:eks:us-east-1:000000000000:access-entry/" + clusterName + "/arn%3Aaws%3Aiam%3A%3A000000000000%3Arole%2Fdev"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(accessEntryARN), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockAccessEntryArnReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-untag-access-entry-tags-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	accessEntryARN := "arn:aws:eks:us-east-1:000000000000:access-entry/" + clusterName + "/arn%3Aaws%3Aiam%3A%3A000000000000%3Arole%2Fdev"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(accessEntryARN)+"?tagKeys=env", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeTagLegacyMockAccessEntryArnMalformedRequestStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-access-entry-tag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	accessEntryARN := "arn:aws:eks:us-east-1:000000000000:access-entry/" + clusterName + "/arn%3Aaws%3Aiam%3A%3A000000000000%3Arole%2Fdev"

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(accessEntryARN), map[string]any{})
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSLiveModeUntagLegacyMockAccessEntryArnMissingTagKeysStillReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	clusterName := "live-malformed-access-entry-untag-cluster"
	_ = mustCreateCluster(t, mockSrv.URL, clusterName, nil)
	accessEntryARN := "arn:aws:eks:us-east-1:000000000000:access-entry/" + clusterName + "/arn%3Aaws%3Aiam%3A%3A000000000000%3Arole%2Fdev"

	resp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(accessEntryARN), nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

// The /tags path space is owned by the main router, which dispatches on the
// ARN's service prefix (pipes → Pipes, eks → EKS, apigateway/servicecatalog →
// API Gateway's ARN-keyed store, anything else → the router's REST fallback
// since #976). The tests below pin that a non-EKS ARN never reaches the EKS
// handlers — EKS live-mode gating must not swallow foreign-ARN tag traffic,
// which keeps flowing to its owner with API Gateway's semantics (PUT/POST/
// DELETE answer 204, GET answers 200).

func TestEKSLiveModeListTagsForNonEKSArnStillAllowed(t *testing.T) {
	store := state.NewMemoryStore()
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	arn := "arn:aws:apigateway:us-east-1::/restapis/live-abc"
	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(arn), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNoContent)

	getResp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(arn), nil)
	body := expectJSONStatus(t, getResp, http.StatusOK)
	tags, _ := body["tags"].(map[string]any)
	if tags["env"] != "live" {
		t.Fatalf("expected non-EKS ARN tag reads to remain functional in live mode, got %#v", body)
	}
}

func TestEKSLiveModeTagNonEKSArnStillAllowed(t *testing.T) {
	store := state.NewMemoryStore()
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	// An empty tags map would trip EKS's InvalidParameterException; API
	// Gateway's store accepts it as a 204 no-op — proof the request is
	// answered outside EKS regardless of its live-mode gating.
	arn := "arn:aws:apigateway:us-east-1::/restapis/live-abc"
	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(arn), map[string]any{
		"tags": map[string]string{"env": "live"},
	})
	expectStatus(t, resp, http.StatusNoContent)

	emptyResp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(arn), map[string]any{})
	expectStatus(t, emptyResp, http.StatusNoContent)

	getResp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(arn), nil)
	body := expectJSONStatus(t, getResp, http.StatusOK)
	tags, _ := body["tags"].(map[string]any)
	if tags["env"] != "live" {
		t.Fatalf("expected non-EKS ARN tags to remain functional in live mode, got %#v", tags)
	}
}

func TestEKSLiveModeUntagNonEKSArnStillAllowed(t *testing.T) {
	store := state.NewMemoryStore()
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	arn := "arn:aws:apigateway:us-east-1::/restapis/live-abc"
	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/tags/"+url.PathEscape(arn), map[string]any{
		"tags": map[string]string{"env": "live", "owner": "ci"},
	})
	expectStatus(t, resp, http.StatusNoContent)

	delResp := eksCall(t, http.MethodDelete, liveSrv.URL+"/tags/"+url.PathEscape(arn)+"?tagKeys=owner", nil)
	expectStatus(t, delResp, http.StatusNoContent)

	getResp := eksCall(t, http.MethodGet, liveSrv.URL+"/tags/"+url.PathEscape(arn), nil)
	body := expectJSONStatus(t, getResp, http.StatusOK)
	tags, _ := body["tags"].(map[string]any)
	if tags["env"] != "live" {
		t.Fatalf("expected env tag to remain after untag on non-EKS ARN, got %#v", tags)
	}
	if _, exists := tags["owner"]; exists {
		t.Fatalf("expected owner tag removed after untag on non-EKS ARN, got %#v", tags)
	}
}

func TestEKSLiveModeCreateClusterRequiresDocker(t *testing.T) {
	srv := helpers.NewTestServer(t,
		helpers.WithEKSMode(config.EKSModeLive),
	)

	resp := eksCall(t, http.MethodPost, srv.URL+"/clusters", map[string]any{
		"name":    "live-cluster",
		"roleArn": "arn:aws:iam::000000000000:role/eks-role",
	})
	expectStatus(t, resp, http.StatusServiceUnavailable)
}

func TestEKSInsights(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "insights-cluster", nil)

	listResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/insights-cluster/insights", map[string]any{})
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list insights, got %d", listResp.StatusCode)
	}
	listBody := decodeBody(t, listResp)
	insights, _ := listBody["insights"].([]any)
	if len(insights) == 0 {
		t.Fatalf("expected non-empty insights list, got %v", insights)
	}
	first, _ := insights[0].(map[string]any)
	if first["id"] == "" {
		t.Fatalf("expected insight id, got %v", first)
	}

	insightID, _ := first["id"].(string)
	describeResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/insights-cluster/insights/"+insightID, nil)
	if describeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe insight, got %d", describeResp.StatusCode)
	}
	describeBody := decodeBody(t, describeResp)
	insight, _ := describeBody["insight"].(map[string]any)
	if insight["id"] != insightID {
		t.Fatalf("expected insight id %q, got %v", insightID, insight["id"])
	}

	_ = expectResourceNotFound(t, eksCall(t, http.MethodGet, srv.URL+"/clusters/insights-cluster/insights/missing-id", nil))
	_ = expectResourceNotFound(t, eksCall(t, http.MethodPost, srv.URL+"/clusters/no-cluster/insights", map[string]any{}))
}

func TestEKSNodegroupLifecycle(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", nil)

	nodegroup := mustCreateNodegroup(t, srv.URL, "demo-cluster", "workers-a", []string{"subnet-1", "subnet-2"})
	if nodegroup["nodegroupName"] != "workers-a" {
		t.Fatalf("unexpected nodegroup name: %v", nodegroup["nodegroupName"])
	}

	listNgResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/node-groups", nil)
	if listNgResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listNgResp.StatusCode)
	}
	listBody := decodeBody(t, listNgResp)
	nodegroups, ok := listBody["nodegroups"].([]any)
	if !ok || len(nodegroups) != 1 || nodegroups[0] != "workers-a" {
		t.Fatalf("unexpected nodegroups list: %#v", listBody["nodegroups"])
	}
}

func TestEKSDescribeNodegroup(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", nil)
	_ = mustCreateNodegroup(t, srv.URL, "demo-cluster", "workers-a", []string{"subnet-1", "subnet-2"})

	describeNgResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/node-groups/workers-a", nil)
	if describeNgResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe nodegroup, got %d", describeNgResp.StatusCode)
	}
	body := decodeBody(t, describeNgResp)
	nodegroup, ok := body["nodegroup"].(map[string]any)
	if !ok {
		t.Fatalf("expected nodegroup object, got %#v", body)
	}
	if nodegroup["nodegroupName"] != "workers-a" {
		t.Fatalf("unexpected nodegroup name: %v", nodegroup["nodegroupName"])
	}
	if nodegroup["clusterName"] != "demo-cluster" {
		t.Fatalf("unexpected cluster name: %v", nodegroup["clusterName"])
	}
}

func TestEKSDescribeFargateProfile(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", nil)

	describeDefaultResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/fargate-profiles/default", nil)
	if describeDefaultResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe fargate profile, got %d", describeDefaultResp.StatusCode)
	}
	defaultBody := decodeBody(t, describeDefaultResp)
	fargateProfile, ok := defaultBody["fargateProfile"].(map[string]any)
	if !ok {
		t.Fatalf("expected fargateProfile object, got %#v", defaultBody)
	}
	if fargateProfile["fargateProfileName"] != "default" {
		t.Fatalf("unexpected fargate profile name: %v", fargateProfile["fargateProfileName"])
	}

	_ = expectResourceNotFound(t, eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/fargate-profiles/nope", nil))
}

func TestEKSListFargateProfiles(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", map[string]any{
		"roleArn": "arn:aws:iam::000000000000:role/eks-cluster-role",
	})

	listResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/fargate-profiles", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list fargate profiles, got %d", listResp.StatusCode)
	}
	body := decodeBody(t, listResp)
	profiles, ok := body["fargateProfileNames"].([]any)
	if !ok {
		t.Fatalf("expected fargateProfileNames array, got %#v", body)
	}
	if len(profiles) != 1 || profiles[0] != "default" {
		t.Fatalf("unexpected fargate profile names: %#v", profiles)
	}
}

func TestEKSUpdateClusterVersion(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", map[string]any{
		"version": "1.31",
	})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/demo-cluster/updates", map[string]any{
		"version": "1.32",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update cluster version, got %d", updateResp.StatusCode)
	}
	updateBody := decodeBody(t, updateResp)
	update, ok := updateBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update object, got %#v", updateBody)
	}
	if update["status"] != "Successful" {
		t.Fatalf("unexpected update status: %v", update["status"])
	}

	describeResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster", nil)
	if describeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe cluster, got %d", describeResp.StatusCode)
	}
	describeBody := decodeBody(t, describeResp)
	cluster, ok := describeBody["cluster"].(map[string]any)
	if !ok {
		t.Fatalf("expected cluster object, got %#v", describeBody)
	}
	if cluster["version"] != "1.32" {
		t.Fatalf("expected cluster version 1.32, got %v", cluster["version"])
	}
}

func TestEKSUpdateNodegroupVersion(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", nil)
	_ = mustCreateNodegroup(t, srv.URL, "demo-cluster", "workers-a", []string{"subnet-1", "subnet-2"})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/demo-cluster/node-groups/workers-a/update-version", map[string]any{
		"version": "1.32",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update nodegroup version, got %d", updateResp.StatusCode)
	}
	updateBody := decodeBody(t, updateResp)
	update, ok := updateBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update object, got %#v", updateBody)
	}
	if update["status"] != "Successful" {
		t.Fatalf("unexpected update status: %v", update["status"])
	}

	describeResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/node-groups/workers-a", nil)
	if describeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe nodegroup, got %d", describeResp.StatusCode)
	}
	describeBody := decodeBody(t, describeResp)
	nodegroup, ok := describeBody["nodegroup"].(map[string]any)
	if !ok {
		t.Fatalf("expected nodegroup object, got %#v", describeBody)
	}
	if nodegroup["version"] != "1.32" {
		t.Fatalf("expected nodegroup version 1.32, got %v", nodegroup["version"])
	}
}

func TestEKSUpdateKubeconfig(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", nil)

	kubeconfigResp := eksCall(t, http.MethodPost, srv.URL+"/_overcast/eks/clusters/demo-cluster/kubeconfig", nil)
	if kubeconfigResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update kubeconfig, got %d", kubeconfigResp.StatusCode)
	}
	body := decodeBody(t, kubeconfigResp)
	kubeconfig, ok := body["kubeconfig"].(string)
	if !ok {
		t.Fatalf("expected kubeconfig string, got %#v", body)
	}
	if !strings.Contains(kubeconfig, "name: demo-cluster") {
		t.Fatalf("expected kubeconfig to include cluster name, got %q", kubeconfig)
	}
	if !strings.Contains(kubeconfig, "server: https://demo-cluster.mock.eks.local") {
		t.Fatalf("expected kubeconfig to include cluster endpoint, got %q", kubeconfig)
	}
}

func TestEKSLiveModeUpdateKubeconfigReturnsNotImplemented(t *testing.T) {
	store := state.NewMemoryStore()
	mockSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
	)
	liveSrv := helpers.NewTestServer(t,
		helpers.WithStore(store),
		helpers.WithEKSMode(config.EKSModeLive),
	)

	_ = mustCreateCluster(t, mockSrv.URL, "live-kubeconfig-cluster", nil)

	resp := eksCall(t, http.MethodPost, liveSrv.URL+"/_overcast/eks/clusters/live-kubeconfig-cluster/kubeconfig", nil)
	expectStatus(t, resp, http.StatusNotImplemented)
}

func TestEKSDescribeUpdate(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", map[string]any{
		"version": "1.31",
	})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/demo-cluster/updates", map[string]any{
		"version": "1.32",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update cluster version, got %d", updateResp.StatusCode)
	}
	updateBody := decodeBody(t, updateResp)
	update, ok := updateBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update object, got %#v", updateBody)
	}
	updateID, ok := update["id"].(string)
	if !ok || updateID == "" {
		t.Fatalf("expected non-empty update id, got %#v", update["id"])
	}

	describeResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/updates/"+updateID, nil)
	if describeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe update, got %d", describeResp.StatusCode)
	}
	describeBody := decodeBody(t, describeResp)
	describedUpdate, ok := describeBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update object, got %#v", describeBody)
	}
	if describedUpdate["id"] != updateID {
		t.Fatalf("expected update id %q, got %v", updateID, describedUpdate["id"])
	}
	if describedUpdate["status"] != "Successful" {
		t.Fatalf("unexpected update status: %v", describedUpdate["status"])
	}
}

func TestEKSListUpdates(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", map[string]any{
		"roleArn": "arn:aws:iam::000000000000:role/eks-cluster-role",
		"version": "1.31",
	})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/demo-cluster/updates", map[string]any{
		"version": "1.32",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update cluster version, got %d", updateResp.StatusCode)
	}
	_ = decodeBody(t, updateResp)

	listResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/updates", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list updates, got %d", listResp.StatusCode)
	}
	listBody := decodeBody(t, listResp)
	ids, ok := listBody["updateIds"].([]any)
	if !ok {
		t.Fatalf("expected updateIds array, got %#v", listBody)
	}
	if len(ids) != 1 {
		t.Fatalf("expected one update id, got %#v", ids)
	}
	id, ok := ids[0].(string)
	if !ok || id == "" {
		t.Fatalf("expected non-empty update id, got %#v", ids[0])
	}
}

func TestEKSDescribeUpdate_afterNodegroupUpdate(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "demo-cluster", nil)
	_ = mustCreateNodegroup(t, srv.URL, "demo-cluster", "workers-a", []string{"subnet-1", "subnet-2"})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/demo-cluster/node-groups/workers-a/update-version", map[string]any{
		"version": "1.32",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update nodegroup version, got %d", updateResp.StatusCode)
	}
	updateBody := decodeBody(t, updateResp)
	update, ok := updateBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update object, got %#v", updateBody)
	}
	updateID, ok := update["id"].(string)
	if !ok || updateID == "" {
		t.Fatalf("expected non-empty update id, got %#v", update["id"])
	}

	describeResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/demo-cluster/updates/"+updateID, nil)
	if describeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe update, got %d", describeResp.StatusCode)
	}
	describeBody := decodeBody(t, describeResp)
	describedUpdate, ok := describeBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update object, got %#v", describeBody)
	}
	if describedUpdate["id"] != updateID {
		t.Fatalf("expected update id %q, got %v", updateID, describedUpdate["id"])
	}
}

func TestEKSDeleteNodegroup(t *testing.T) {
	srv := newEKSServer(t)

	// Create cluster.
	_ = mustCreateCluster(t, srv.URL, "del-ng-cluster", nil)

	// Create nodegroup.
	ng := mustCreateNodegroup(t, srv.URL, "del-ng-cluster", "workers-b", []string{"subnet-1"})
	ngARN, _ := ng["nodegroupArn"].(string)
	if ngARN == "" {
		t.Fatalf("expected nodegroupArn in create response, got %#v", ng)
	}

	mustTagResource(t, srv.URL, ngARN, map[string]string{"owner": "nodegroup-delete-test"})

	// Verify nodegroup exists.
	listResp1 := eksCall(t, http.MethodGet, srv.URL+"/clusters/del-ng-cluster/node-groups", nil)
	if listResp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list nodegroups, got %d", listResp1.StatusCode)
	}
	listBody1 := decodeBody(t, listResp1)
	names1, _ := listBody1["nodegroups"].([]any)
	if len(names1) != 1 {
		t.Fatalf("expected 1 nodegroup before delete, got %d", len(names1))
	}

	// Delete nodegroup.
	delResp := eksCall(t, http.MethodDelete, srv.URL+"/clusters/del-ng-cluster/node-groups/workers-b", nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for delete nodegroup, got %d", delResp.StatusCode)
	}
	delBody := decodeBody(t, delResp)
	deletedNG, ok := delBody["nodegroup"].(map[string]any)
	if !ok {
		t.Fatalf("expected nodegroup object in delete response, got %#v", delBody)
	}
	if deletedNG["status"] != "DELETING" {
		t.Fatalf("expected status DELETING, got %v", deletedNG["status"])
	}

	// Verify nodegroup is gone.
	listResp2 := eksCall(t, http.MethodGet, srv.URL+"/clusters/del-ng-cluster/node-groups", nil)
	if listResp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list after delete, got %d", listResp2.StatusCode)
	}
	listBody2 := decodeBody(t, listResp2)
	names2, _ := listBody2["nodegroups"].([]any)
	if len(names2) != 0 {
		t.Fatalf("expected 0 nodegroups after delete, got %d", len(names2))
	}

	// 404 on deleted nodegroup.
	_ = expectResourceNotFound(t, eksCall(t, http.MethodDelete, srv.URL+"/clusters/del-ng-cluster/node-groups/workers-b", nil))

	// Recreate nodegroup and ensure old tags were not retained.
	_ = mustCreateNodegroup(t, srv.URL, "del-ng-cluster", "workers-b", []string{"subnet-1"})

	tags := listResourceTags(t, srv.URL, ngARN)
	if len(tags) != 0 {
		t.Fatalf("expected no retained nodegroup tags after delete/recreate, got %#v", tags)
	}
}

func TestEKSTagOperations(t *testing.T) {
	srv := newEKSServer(t)

	// Create a cluster to have a real ARN to tag.
	cluster := mustCreateCluster(t, srv.URL, "tag-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected non-empty ARN, got %#v", cluster)
	}

	// ListTagsForResource — should be empty initially.
	tags1 := listResourceTags(t, srv.URL, arn)
	if len(tags1) != 0 {
		t.Fatalf("expected 0 tags initially, got %d", len(tags1))
	}
	// TagResource — add two tags.
	mustTagResource(t, srv.URL, arn, map[string]string{"env": "test", "owner": "ci"})

	// ListTagsForResource — should see both tags.
	tags2 := listResourceTags(t, srv.URL, arn)
	if tags2["env"] != "test" || tags2["owner"] != "ci" {
		t.Fatalf("expected env=test,owner=ci, got %#v", tags2)
	}

	// UntagResource — remove one tag.
	untagURL := srv.URL + "/tags/" + url.PathEscape(arn) + "?tagKeys=owner"
	untagResp := eksCall(t, http.MethodDelete, untagURL, nil)
	if untagResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for untag resource, got %d", untagResp.StatusCode)
	}

	// ListTagsForResource — only env should remain.
	tags3 := listResourceTags(t, srv.URL, arn)
	if tags3["env"] != "test" {
		t.Fatalf("expected env=test after untag, got %#v", tags3)
	}
	if _, has := tags3["owner"]; has {
		t.Fatalf("expected owner tag removed, still present in %#v", tags3)
	}
}

func TestEKSTagResourceRejectsEmptyTagsMap(t *testing.T) {
	srv := newEKSServer(t)

	cluster := mustCreateCluster(t, srv.URL, "tag-validation-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}

	body := expectJSONStatus(t, eksCall(t, http.MethodPost, srv.URL+"/tags/"+url.PathEscape(arn), map[string]any{}), http.StatusBadRequest)
	if body["__type"] != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException for empty tag map, got %#v", body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "tags map") {
		t.Fatalf("expected empty tag map message, got %#v", body)
	}
}

func TestEKSUntagResourceRejectsMissingTagKeys(t *testing.T) {
	srv := newEKSServer(t)

	cluster := mustCreateCluster(t, srv.URL, "untag-validation-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}
	mustTagResource(t, srv.URL, arn, map[string]string{"env": "test"})

	body := expectJSONStatus(t, eksCall(t, http.MethodDelete, srv.URL+"/tags/"+url.PathEscape(arn), nil), http.StatusBadRequest)
	if body["__type"] != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException for missing tagKeys, got %#v", body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "tagKeys") {
		t.Fatalf("expected missing tagKeys message, got %#v", body)
	}
}

func TestEKSDeleteClusterClearsClusterTags(t *testing.T) {
	srv := newEKSServer(t)

	cluster := mustCreateCluster(t, srv.URL, "tag-cleanup-cluster", nil)
	arn, _ := cluster["arn"].(string)
	if arn == "" {
		t.Fatalf("expected cluster ARN in create response, got %#v", cluster)
	}

	mustTagResource(t, srv.URL, arn, map[string]string{"env": "cleanup"})

	deleteResp := eksCall(t, http.MethodDelete, srv.URL+"/clusters/tag-cleanup-cluster", nil)
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for cluster delete, got %d", deleteResp.StatusCode)
	}

	_ = mustCreateCluster(t, srv.URL, "tag-cleanup-cluster", nil)

	tags := listResourceTags(t, srv.URL, arn)
	if len(tags) != 0 {
		t.Fatalf("expected no retained tags after cluster delete, got %#v", tags)
	}
}

func TestEKSDeleteClusterClearsChildResourceTags(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "child-tag-cleanup-cluster", nil)

	ng := mustCreateNodegroup(t, srv.URL, "child-tag-cleanup-cluster", "workers-a", []string{"subnet-1"})
	ngARN, _ := ng["nodegroupArn"].(string)
	if ngARN == "" {
		t.Fatalf("expected nodegroupArn in create response, got %#v", ng)
	}

	mustTagResource(t, srv.URL, ngARN, map[string]string{"owner": "cleanup-test"})

	deleteCluster := eksCall(t, http.MethodDelete, srv.URL+"/clusters/child-tag-cleanup-cluster", nil)
	if deleteCluster.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for cluster delete, got %d", deleteCluster.StatusCode)
	}

	_ = mustCreateCluster(t, srv.URL, "child-tag-cleanup-cluster", nil)

	_ = mustCreateNodegroup(t, srv.URL, "child-tag-cleanup-cluster", "workers-a", []string{"subnet-1"})

	tags := listResourceTags(t, srv.URL, ngARN)
	if len(tags) != 0 {
		t.Fatalf("expected no retained child-resource tags after cluster delete, got %#v", tags)
	}
}

func TestEKSDeleteClusterClearsUpdateHistory(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "update-cleanup-cluster", nil)

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/update-cleanup-cluster/updates", map[string]any{
		"version": "1.30",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update cluster version, got %d", updateResp.StatusCode)
	}

	listBeforeDelete := eksCall(t, http.MethodGet, srv.URL+"/clusters/update-cleanup-cluster/updates", nil)
	if listBeforeDelete.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list updates before delete, got %d", listBeforeDelete.StatusCode)
	}
	beforeBody := decodeBody(t, listBeforeDelete)
	beforeIDs, _ := beforeBody["updateIds"].([]any)
	if len(beforeIDs) == 0 {
		t.Fatalf("expected at least one update id before cluster delete, got %v", beforeIDs)
	}

	deleteCluster := eksCall(t, http.MethodDelete, srv.URL+"/clusters/update-cleanup-cluster", nil)
	if deleteCluster.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for cluster delete, got %d", deleteCluster.StatusCode)
	}

	_ = mustCreateCluster(t, srv.URL, "update-cleanup-cluster", nil)

	listAfterRecreate := eksCall(t, http.MethodGet, srv.URL+"/clusters/update-cleanup-cluster/updates", nil)
	if listAfterRecreate.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list updates after recreate, got %d", listAfterRecreate.StatusCode)
	}
	afterBody := decodeBody(t, listAfterRecreate)
	afterIDs, _ := afterBody["updateIds"].([]any)
	if len(afterIDs) != 0 {
		t.Fatalf("expected no retained update ids after cluster delete, got %v", afterIDs)
	}
}

func TestEKSDescribeClusterVersions(t *testing.T) {
	srv := newEKSServer(t)

	resp := eksCall(t, http.MethodGet, srv.URL+"/cluster-versions", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe cluster versions, got %d", resp.StatusCode)
	}

	body := decodeBody(t, resp)
	versions, _ := body["clusterVersions"].([]any)
	if len(versions) == 0 {
		t.Fatalf("expected at least one cluster version, got %v", versions)
	}

	v, _ := versions[0].(map[string]any)
	if v["clusterVersion"] == "" {
		t.Fatalf("expected clusterVersion in first item, got %v", v)
	}
	if v["defaultVersion"] == nil {
		t.Fatalf("expected defaultVersion in first item, got %v", v)
	}
}

func TestEKSUpdateClusterConfig(t *testing.T) {
	srv := newEKSServer(t)

	// Create a cluster first.
	_ = mustCreateCluster(t, srv.URL, "cfg-cluster", map[string]any{
		"version": "1.29",
		"resourcesVpcConfig": map[string]any{
			"subnetIds": []string{"subnet-abc"},
		},
	})

	// UpdateClusterConfig — set logging types.
	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/cfg-cluster/update-config", map[string]any{
		"logging": map[string]any{
			"clusterLogging": []map[string]any{
				{"types": []string{"api", "audit"}, "enabled": true},
			},
		},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update config: expected 200, got %d", updateResp.StatusCode)
	}
	body := decodeBody(t, updateResp)
	update, _ := body["update"].(map[string]any)
	if update["type"] != "LoggingUpdate" {
		t.Fatalf("expected update type LoggingUpdate, got %v", update["type"])
	}
	if update["status"] != "Successful" {
		t.Fatalf("expected status Successful, got %v", update["status"])
	}
	updateID, _ := update["id"].(string)
	if updateID == "" {
		t.Fatalf("expected non-empty update id")
	}

	// DescribeUpdate should return the recorded update.
	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/cfg-cluster/updates/"+updateID, nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("describe update: expected 200, got %d", descResp.StatusCode)
	}
	descBody := decodeBody(t, descResp)
	descUpdate, _ := descBody["update"].(map[string]any)
	if descUpdate["id"] != updateID {
		t.Fatalf("expected update id %s, got %v", updateID, descUpdate["id"])
	}

	// DescribeCluster should reflect the updated logging config.
	clusterResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/cfg-cluster", nil)
	if clusterResp.StatusCode != http.StatusOK {
		t.Fatalf("describe cluster: expected 200, got %d", clusterResp.StatusCode)
	}
	clusterBody := decodeBody(t, clusterResp)
	cluster, _ := clusterBody["cluster"].(map[string]any)
	logging, _ := cluster["logging"].(map[string]any)
	if logging == nil {
		t.Fatalf("expected logging field on cluster after UpdateClusterConfig")
	}
}

func TestEKSUpdateNodegroupConfig(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "ngcfg-cluster", map[string]any{
		"version": "1.29",
		"resourcesVpcConfig": map[string]any{
			"subnetIds": []string{"subnet-abc"},
		},
	})
	_ = mustCreateNodegroup(t, srv.URL, "ngcfg-cluster", "workers-a", []string{"subnet-abc"})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/ngcfg-cluster/node-groups/workers-a/update-config", map[string]any{
		"labels": map[string]any{
			"env":  "dev",
			"team": "platform",
		},
		"scalingConfig": map[string]any{
			"minSize":     1,
			"maxSize":     4,
			"desiredSize": 2,
		},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update nodegroup config: expected 200, got %d", updateResp.StatusCode)
	}
	body := decodeBody(t, updateResp)
	update, _ := body["update"].(map[string]any)
	if update["type"] != "ConfigUpdate" {
		t.Fatalf("expected update type ConfigUpdate, got %v", update["type"])
	}
	updateID, _ := update["id"].(string)
	if updateID == "" {
		t.Fatalf("expected non-empty update id")
	}

	descUpdateResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/ngcfg-cluster/updates/"+updateID, nil)
	if descUpdateResp.StatusCode != http.StatusOK {
		t.Fatalf("describe update: expected 200, got %d", descUpdateResp.StatusCode)
	}

	descNGResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/ngcfg-cluster/node-groups/workers-a", nil)
	if descNGResp.StatusCode != http.StatusOK {
		t.Fatalf("describe nodegroup: expected 200, got %d", descNGResp.StatusCode)
	}
	descBody := decodeBody(t, descNGResp)
	nodegroup, _ := descBody["nodegroup"].(map[string]any)
	labels, _ := nodegroup["labels"].(map[string]any)
	if labels == nil || labels["env"] != "dev" {
		t.Fatalf("expected labels to include env=dev, got %v", nodegroup["labels"])
	}
	scaling, _ := nodegroup["scalingConfig"].(map[string]any)
	if scaling == nil || scaling["desiredSize"] != float64(2) {
		t.Fatalf("expected scalingConfig.desiredSize=2, got %v", nodegroup["scalingConfig"])
	}
}

func TestEKSCreateNodegroupPreservesFullShape(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "shape-cluster", nil)

	createResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/shape-cluster/node-groups", map[string]any{
		"nodegroupName":  "shape-ng",
		"nodeRole":       "arn:aws:iam::000000000000:role/node-role",
		"subnets":        []string{"subnet-aaa"},
		"instanceTypes":  []string{"t3.medium", "t3.large"},
		"amiType":        "AL2_x86_64",
		"capacityType":   "SPOT",
		"diskSize":       50,
		"releaseVersion": "1.31.3-20241201",
		"labels":         map[string]any{"env": "prod", "team": "platform"},
		"taints": []map[string]any{
			{"key": "dedicated", "value": "gpu", "effect": "NO_SCHEDULE"},
		},
		"scalingConfig": map[string]any{
			"minSize": 1, "maxSize": 5, "desiredSize": 3,
		},
		"updateConfig": map[string]any{
			"maxUnavailable": 1,
		},
		"launchTemplate": map[string]any{
			"id": "lt-0123456789abcdef0", "version": "$Latest",
		},
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/shape-cluster/node-groups/shape-ng", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	body := decodeBody(t, descResp)
	ng, _ := body["nodegroup"].(map[string]any)

	if ng["amiType"] != "AL2_x86_64" {
		t.Fatalf("expected amiType AL2_x86_64, got %v", ng["amiType"])
	}
	if ng["capacityType"] != "SPOT" {
		t.Fatalf("expected capacityType SPOT, got %v", ng["capacityType"])
	}
	if ng["diskSize"] != float64(50) {
		t.Fatalf("expected diskSize 50, got %v", ng["diskSize"])
	}
	if ng["releaseVersion"] != "1.31.3-20241201" {
		t.Fatalf("expected releaseVersion 1.31.3-20241201, got %v", ng["releaseVersion"])
	}
	instanceTypes, _ := ng["instanceTypes"].([]any)
	if len(instanceTypes) != 2 || instanceTypes[0] != "t3.medium" {
		t.Fatalf("expected instanceTypes [t3.medium t3.large], got %v", ng["instanceTypes"])
	}
	taints, _ := ng["taints"].([]any)
	if len(taints) != 1 {
		t.Fatalf("expected 1 taint, got %v", ng["taints"])
	}
	taint, _ := taints[0].(map[string]any)
	if taint["key"] != "dedicated" || taint["effect"] != "NO_SCHEDULE" {
		t.Fatalf("expected taint dedicated/NO_SCHEDULE, got %v", taint)
	}
	updateCfg, _ := ng["updateConfig"].(map[string]any)
	if updateCfg == nil || updateCfg["maxUnavailable"] != float64(1) {
		t.Fatalf("expected updateConfig.maxUnavailable=1, got %v", ng["updateConfig"])
	}
	lt, _ := ng["launchTemplate"].(map[string]any)
	if lt == nil || lt["id"] != "lt-0123456789abcdef0" {
		t.Fatalf("expected launchTemplate.id lt-0123456789abcdef0, got %v", ng["launchTemplate"])
	}
}

func TestEKSInlineTagsOnCreate(t *testing.T) {
	srv := newEKSServer(t)

	// CreateCluster with inline tags.
	cluster := mustCreateCluster(t, srv.URL, "inline-tag-cluster", map[string]any{
		"tags": map[string]string{"env": "test", "team": "platform"},
	})
	clusterARN, _ := cluster["arn"].(string)
	if clusterARN == "" {
		t.Fatalf("expected cluster ARN, got %#v", cluster)
	}
	tags := listResourceTags(t, srv.URL, clusterARN)
	if tags["env"] != "test" || tags["team"] != "platform" {
		t.Fatalf("expected inline cluster tags, got %#v", tags)
	}

	// CreateNodegroup with inline tags.
	createNGResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/inline-tag-cluster/node-groups", map[string]any{
		"nodegroupName": "tagged-ng",
		"nodeRole":      "arn:aws:iam::000000000000:role/node-role",
		"subnets":       []string{"subnet-aaa"},
		"tags":          map[string]string{"billing": "shared"},
	})
	if createNGResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for nodegroup create, got %d", createNGResp.StatusCode)
	}
	ngBody := decodeBody(t, createNGResp)
	ng, _ := ngBody["nodegroup"].(map[string]any)
	ngARN, _ := ng["nodegroupArn"].(string)
	ngTags := listResourceTags(t, srv.URL, ngARN)
	if ngTags["billing"] != "shared" {
		t.Fatalf("expected inline nodegroup tags, got %#v", ngTags)
	}

	// CreateFargateProfile with inline tags.
	createFPResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/inline-tag-cluster/fargate-profiles", map[string]any{
		"fargateProfileName":  "tagged-fp",
		"podExecutionRoleArn": "arn:aws:iam::000000000000:role/fargate-role",
		"selectors":           []map[string]any{{"namespace": "default"}},
		"tags":                map[string]string{"cost": "fargate"},
	})
	if createFPResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for fargate create, got %d", createFPResp.StatusCode)
	}
	fpBody := decodeBody(t, createFPResp)
	fp, _ := fpBody["fargateProfile"].(map[string]any)
	fpARN, _ := fp["fargateProfileArn"].(string)
	fpTags := listResourceTags(t, srv.URL, fpARN)
	if fpTags["cost"] != "fargate" {
		t.Fatalf("expected inline fargate profile tags, got %#v", fpTags)
	}

	// CreateAddon with inline tags.
	createAddonResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/inline-tag-cluster/addons", map[string]any{
		"addonName": "vpc-cni",
		"tags":      map[string]string{"managed": "true"},
	})
	if createAddonResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for addon create, got %d", createAddonResp.StatusCode)
	}
	addonBody := decodeBody(t, createAddonResp)
	addon, _ := addonBody["addon"].(map[string]any)
	addonARN, _ := addon["addonArn"].(string)
	addonTags := listResourceTags(t, srv.URL, addonARN)
	if addonTags["managed"] != "true" {
		t.Fatalf("expected inline addon tags, got %#v", addonTags)
	}
}

func TestEKSCreateClusterPreservesNetworkAndEncryptionConfig(t *testing.T) {
	srv := newEKSServer(t)

	body := mustCreateCluster(t, srv.URL, "net-enc-cluster", map[string]any{
		"kubernetesNetworkConfig": map[string]any{
			"serviceIpv4Cidr": "172.20.0.0/16",
			"ipFamily":        "ipv4",
		},
		"encryptionConfig": []map[string]any{
			{"provider": map[string]any{"keyArn": "arn:aws:kms::000000000000:key/abc"}, "resources": []string{"secrets"}},
		},
	})
	clusterARN, _ := body["arn"].(string)
	if clusterARN == "" {
		t.Fatalf("expected cluster ARN, got %#v", body)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/net-enc-cluster", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	descBody := decodeBody(t, descResp)
	cluster, _ := descBody["cluster"].(map[string]any)

	netCfg, _ := cluster["kubernetesNetworkConfig"].(map[string]any)
	if netCfg == nil || netCfg["serviceIpv4Cidr"] != "172.20.0.0/16" {
		t.Fatalf("expected kubernetesNetworkConfig.serviceIpv4Cidr=172.20.0.0/16, got %#v", cluster["kubernetesNetworkConfig"])
	}
	encCfg, _ := cluster["encryptionConfig"].([]any)
	if len(encCfg) != 1 {
		t.Fatalf("expected 1 encryptionConfig entry, got %#v", cluster["encryptionConfig"])
	}
}

// TestEKSCreateClusterPreservesAccessConfigAndLogging proves the #1979 claim
// that accessConfig and logging, sent on CreateCluster itself (not via a
// later UpdateClusterConfig call), round-trip through both the create
// response and DescribeCluster.
// https://docs.aws.amazon.com/eks/latest/APIReference/API_CreateCluster.html
func TestEKSCreateClusterPreservesAccessConfigAndLogging(t *testing.T) {
	srv := newEKSServer(t)

	created := mustCreateCluster(t, srv.URL, "access-logging-cluster", map[string]any{
		"accessConfig": map[string]any{
			"authenticationMode": "API_AND_CONFIG_MAP",
		},
		"logging": map[string]any{
			"clusterLogging": []map[string]any{
				{"types": []string{"api", "audit"}, "enabled": true},
			},
		},
	})
	createdAccessCfg, _ := created["accessConfig"].(map[string]any)
	if createdAccessCfg == nil || createdAccessCfg["authenticationMode"] != "API_AND_CONFIG_MAP" {
		t.Fatalf("expected accessConfig.authenticationMode=API_AND_CONFIG_MAP in create response, got %#v", created["accessConfig"])
	}
	createdLogging, _ := created["logging"].(map[string]any)
	if createdLogging == nil {
		t.Fatalf("expected logging in create response, got %#v", created["logging"])
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/access-logging-cluster", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	body := decodeBody(t, descResp)
	cluster, _ := body["cluster"].(map[string]any)

	accessCfg, _ := cluster["accessConfig"].(map[string]any)
	if accessCfg == nil || accessCfg["authenticationMode"] != "API_AND_CONFIG_MAP" {
		t.Fatalf("expected DescribeCluster accessConfig.authenticationMode=API_AND_CONFIG_MAP, got %#v", cluster["accessConfig"])
	}
	logging, _ := cluster["logging"].(map[string]any)
	if logging == nil {
		t.Fatalf("expected DescribeCluster logging to round-trip, got %#v", cluster["logging"])
	}
	clusterLogging, _ := logging["clusterLogging"].([]any)
	if len(clusterLogging) != 1 {
		t.Fatalf("expected 1 clusterLogging entry, got %#v", logging["clusterLogging"])
	}
}

// TestEKSCreateClusterRejectsDuplicateName proves the #1979 claim that
// CreateCluster rejects an already-existing cluster name with
// ResourceInUseException, mirroring the equivalent duplicate-name paths
// CreateAccessEntry and CreatePodIdentityAssociation both already test.
// https://docs.aws.amazon.com/eks/latest/APIReference/API_CreateCluster.html#API_CreateCluster_Errors
func TestEKSCreateClusterRejectsDuplicateName(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "dup-cluster", nil)

	dupResp := eksCall(t, http.MethodPost, srv.URL+"/clusters", map[string]any{
		"name":    "dup-cluster",
		"roleArn": "arn:aws:iam::000000000000:role/eks-role",
	})
	if dupResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate cluster name, got %d", dupResp.StatusCode)
	}
	dupBody := decodeBody(t, dupResp)
	if dupBody["__type"] != "ResourceInUseException" {
		t.Fatalf("expected ResourceInUseException for duplicate cluster name, got %#v", dupBody)
	}
}

func TestEKSCreateFargateProfilePreservesSubnets(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "fargate-subnets-cluster", nil)

	createResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/fargate-subnets-cluster/fargate-profiles", map[string]any{
		"fargateProfileName":  "fp-subnets",
		"podExecutionRoleArn": "arn:aws:iam::000000000000:role/fargate-role",
		"subnets":             []string{"subnet-aaa", "subnet-bbb"},
		"selectors":           []map[string]any{{"namespace": "kube-system"}},
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/fargate-subnets-cluster/fargate-profiles/fp-subnets", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	body := decodeBody(t, descResp)
	fp, _ := body["fargateProfile"].(map[string]any)
	subnets, _ := fp["subnets"].([]any)
	if len(subnets) != 2 || subnets[0] != "subnet-aaa" {
		t.Fatalf("expected subnets [subnet-aaa, subnet-bbb], got %#v", fp["subnets"])
	}
}

// TestEKSCreateFargateProfileRequiresPodExecutionRoleArn proves the #1979
// claim that podExecutionRoleArn is a required CreateFargateProfile input.
// The pinned model marks it smithy.api#required (selectors is not — the
// model leaves selectors optional despite the surrounding prose describing
// how selectors are used), matching
// https://docs.aws.amazon.com/eks/latest/APIReference/API_CreateFargateProfile.html
// ("podExecutionRoleArn ... Required: Yes"; "selectors ... Required: No").
func TestEKSCreateFargateProfileRequiresPodExecutionRoleArn(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "fargate-required-cluster", nil)

	resp := eksCall(t, http.MethodPost, srv.URL+"/clusters/fargate-required-cluster/fargate-profiles", map[string]any{
		"fargateProfileName": "fp-missing-role",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing podExecutionRoleArn, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["__type"] != "MissingParameter" {
		t.Fatalf("expected MissingParameter for missing podExecutionRoleArn, got %#v", body)
	}
}

// TestEKSCreateFargateProfilePreservesPodExecutionRoleAndSelectors proves
// the #1979 claim that podExecutionRoleArn and selectors are stored and
// returned by both the create and DescribeFargateProfile responses.
// https://docs.aws.amazon.com/eks/latest/APIReference/API_CreateFargateProfile.html
func TestEKSCreateFargateProfilePreservesPodExecutionRoleAndSelectors(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "fargate-role-selectors-cluster", nil)

	createResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/fargate-role-selectors-cluster/fargate-profiles", map[string]any{
		"fargateProfileName":  "fp-role-selectors",
		"podExecutionRoleArn": "arn:aws:iam::000000000000:role/fargate-pod-exec-custom",
		"selectors": []map[string]any{
			{"namespace": "kube-system", "labels": map[string]any{"compute": "fargate"}},
		},
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}
	createBody := decodeBody(t, createResp)
	createdFP, _ := createBody["fargateProfile"].(map[string]any)
	if createdFP["podExecutionRoleArn"] != "arn:aws:iam::000000000000:role/fargate-pod-exec-custom" {
		t.Fatalf("expected podExecutionRoleArn in create response, got %v", createdFP["podExecutionRoleArn"])
	}
	createdSelectors, _ := createdFP["selectors"].([]any)
	if len(createdSelectors) != 1 {
		t.Fatalf("expected 1 selector in create response, got %#v", createdFP["selectors"])
	}
	createdSelector, _ := createdSelectors[0].(map[string]any)
	if createdSelector["namespace"] != "kube-system" {
		t.Fatalf("expected selector namespace kube-system in create response, got %#v", createdSelector)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/fargate-role-selectors-cluster/fargate-profiles/fp-role-selectors", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	descBody := decodeBody(t, descResp)
	fp, _ := descBody["fargateProfile"].(map[string]any)
	if fp["podExecutionRoleArn"] != "arn:aws:iam::000000000000:role/fargate-pod-exec-custom" {
		t.Fatalf("expected podExecutionRoleArn in describe response, got %v", fp["podExecutionRoleArn"])
	}
	selectors, _ := fp["selectors"].([]any)
	if len(selectors) != 1 {
		t.Fatalf("expected 1 selector in describe response, got %#v", fp["selectors"])
	}
	selector, _ := selectors[0].(map[string]any)
	if selector["namespace"] != "kube-system" {
		t.Fatalf("expected selector namespace kube-system in describe response, got %#v", selector)
	}
	labels, _ := selector["labels"].(map[string]any)
	if labels["compute"] != "fargate" {
		t.Fatalf("expected selector labels.compute=fargate in describe response, got %#v", selector["labels"])
	}
}

func TestEKSUpdateNodegroupConfigPreservesTaintsAndUpdateConfig(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "ng-update-cluster", nil)
	_ = mustCreateNodegroup(t, srv.URL, "ng-update-cluster", "ng-update", []string{"subnet-1"})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/ng-update-cluster/node-groups/ng-update/update-config", map[string]any{
		"taints": []map[string]any{
			{"key": "dedicated", "value": "gpu", "effect": "NO_SCHEDULE"},
		},
		"updateConfig": map[string]any{
			"maxUnavailable": float64(1),
		},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", updateResp.StatusCode)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/ng-update-cluster/node-groups/ng-update", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	body := decodeBody(t, descResp)
	ng, _ := body["nodegroup"].(map[string]any)
	taints, _ := ng["taints"].([]any)
	if len(taints) != 1 {
		t.Fatalf("expected 1 taint, got %#v", ng["taints"])
	}
	updCfg, _ := ng["updateConfig"].(map[string]any)
	if updCfg == nil || updCfg["maxUnavailable"] != float64(1) {
		t.Fatalf("expected updateConfig.maxUnavailable=1, got %#v", ng["updateConfig"])
	}
}

func TestEKSUpdateClusterConfigPreservesKubernetesNetworkConfig(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "net-update-cluster", nil)

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/net-update-cluster/update-config", map[string]any{
		"kubernetesNetworkConfig": map[string]any{
			"serviceIpv4Cidr": "10.100.0.0/16",
			"ipFamily":        "ipv4",
		},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", updateResp.StatusCode)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/net-update-cluster", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	body := decodeBody(t, descResp)
	cluster, _ := body["cluster"].(map[string]any)
	netCfg, _ := cluster["kubernetesNetworkConfig"].(map[string]any)
	if netCfg == nil || netCfg["serviceIpv4Cidr"] != "10.100.0.0/16" {
		t.Fatalf("expected kubernetesNetworkConfig.serviceIpv4Cidr=10.100.0.0/16, got %#v", cluster["kubernetesNetworkConfig"])
	}
}

// TestEKSUpdateClusterConfigVpcConfigUpdate proves the #1979 claim that a
// resourcesVpcConfig-only UpdateClusterConfig request creates an Update of
// type VpcConfigUpdate, whose params are reflected by both DescribeUpdate
// and the next DescribeCluster.
// https://docs.aws.amazon.com/eks/latest/APIReference/API_UpdateClusterConfig.html
func TestEKSUpdateClusterConfigVpcConfigUpdate(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "vpc-cfg-cluster", map[string]any{
		"resourcesVpcConfig": map[string]any{
			"subnetIds": []string{"subnet-abc"},
		},
	})

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/vpc-cfg-cluster/update-config", map[string]any{
		"resourcesVpcConfig": map[string]any{
			"subnetIds":             []string{"subnet-abc", "subnet-def"},
			"endpointPrivateAccess": true,
		},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", updateResp.StatusCode)
	}
	body := decodeBody(t, updateResp)
	update, _ := body["update"].(map[string]any)
	if update["type"] != "VpcConfigUpdate" {
		t.Fatalf("expected update type VpcConfigUpdate, got %v", update["type"])
	}
	params, _ := update["params"].([]any)
	if len(params) != 1 {
		t.Fatalf("expected 1 update param, got %#v", update["params"])
	}
	param, _ := params[0].(map[string]any)
	if param["type"] != "VpcConfig" {
		t.Fatalf("expected update param type VpcConfig, got %#v", param)
	}
	updateID, _ := update["id"].(string)
	if updateID == "" {
		t.Fatalf("expected non-empty update id")
	}

	descUpdateResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/vpc-cfg-cluster/updates/"+updateID, nil)
	if descUpdateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe update, got %d", descUpdateResp.StatusCode)
	}
	descUpdateBody := decodeBody(t, descUpdateResp)
	descUpdate, _ := descUpdateBody["update"].(map[string]any)
	if descUpdate["type"] != "VpcConfigUpdate" {
		t.Fatalf("expected DescribeUpdate type VpcConfigUpdate, got %v", descUpdate["type"])
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/vpc-cfg-cluster", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	descBody := decodeBody(t, descResp)
	cluster, _ := descBody["cluster"].(map[string]any)
	vpcCfg, _ := cluster["resourcesVpcConfig"].(map[string]any)
	subnetIDs, _ := vpcCfg["subnetIds"].([]any)
	if len(subnetIDs) != 2 || subnetIDs[1] != "subnet-def" {
		t.Fatalf("expected resourcesVpcConfig.subnetIds [subnet-abc, subnet-def], got %#v", vpcCfg["subnetIds"])
	}
	if vpcCfg["endpointPrivateAccess"] != true {
		t.Fatalf("expected resourcesVpcConfig.endpointPrivateAccess=true, got %#v", vpcCfg["endpointPrivateAccess"])
	}
}

// TestEKSUpdateClusterConfigRejectsNoChanges proves the #1979 claim that a
// request with none of logging/resourcesVpcConfig/kubernetesNetworkConfig
// set is rejected with InvalidParameterException, since AWS documents that
// error as one UpdateClusterConfig can return but does not publish an exact
// runtime message for it — only the code and status are asserted here.
// https://docs.aws.amazon.com/eks/latest/APIReference/API_UpdateClusterConfig.html#API_UpdateClusterConfig_Errors
func TestEKSUpdateClusterConfigRejectsNoChanges(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "no-changes-cluster", nil)

	resp := eksCall(t, http.MethodPost, srv.URL+"/clusters/no-changes-cluster/update-config", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for no configuration changes, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["__type"] != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException for no configuration changes, got %#v", body)
	}
	msg, _ := body["message"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Fatalf("expected non-empty message for no configuration changes, got %#v", body)
	}
}

func TestEKSDescribeResourcesReturnInlineTags(t *testing.T) {
	srv := newEKSServer(t)

	// Cluster
	createdCluster := mustCreateCluster(t, srv.URL, "tags-cluster", map[string]any{
		"tags": map[string]string{"env": "test", "owner": "ci"},
	})
	createdClusterTags, _ := createdCluster["tags"].(map[string]any)
	if createdClusterTags["env"] != "test" || createdClusterTags["owner"] != "ci" {
		t.Fatalf("expected cluster create response tags {env:test, owner:ci}, got %#v", createdCluster["tags"])
	}
	descCluster := eksCall(t, http.MethodGet, srv.URL+"/clusters/tags-cluster", nil)
	if descCluster.StatusCode != http.StatusOK {
		t.Fatalf("cluster describe expected 200, got %d", descCluster.StatusCode)
	}
	clusterBody := decodeBody(t, descCluster)
	cluster, _ := clusterBody["cluster"].(map[string]any)
	clusterTags, _ := cluster["tags"].(map[string]any)
	if clusterTags["env"] != "test" || clusterTags["owner"] != "ci" {
		t.Fatalf("expected cluster tags {env:test, owner:ci}, got %#v", cluster["tags"])
	}

	// Nodegroup
	createNGResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/tags-cluster/node-groups", map[string]any{
		"nodegroupName": "tagged-ng",
		"nodeRole":      "arn:aws:iam::000000000000:role/eks-node-role",
		"subnets":       []string{"subnet-1"},
		"tags":          map[string]string{"team": "infra"},
	})
	if createNGResp.StatusCode != http.StatusCreated {
		t.Fatalf("nodegroup create expected 201, got %d", createNGResp.StatusCode)
	}
	createNGBody := decodeBody(t, createNGResp)
	createdNG, _ := createNGBody["nodegroup"].(map[string]any)
	createdNGTags, _ := createdNG["tags"].(map[string]any)
	if createdNGTags["team"] != "infra" {
		t.Fatalf("expected nodegroup create response tags {team:infra}, got %#v", createdNG["tags"])
	}
	descNG := eksCall(t, http.MethodGet, srv.URL+"/clusters/tags-cluster/node-groups/tagged-ng", nil)
	ngBody := decodeBody(t, descNG)
	ng, _ := ngBody["nodegroup"].(map[string]any)
	ngTags, _ := ng["tags"].(map[string]any)
	if ngTags["team"] != "infra" {
		t.Fatalf("expected nodegroup tags {team:infra}, got %#v", ng["tags"])
	}

	// FargateProfile
	createFPResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/tags-cluster/fargate-profiles", map[string]any{
		"fargateProfileName":  "tagged-fp",
		"podExecutionRoleArn": "arn:aws:iam::000000000000:role/fargate-role",
		"tags":                map[string]string{"stage": "dev"},
	})
	if createFPResp.StatusCode != http.StatusCreated {
		t.Fatalf("fargate profile create expected 201, got %d", createFPResp.StatusCode)
	}
	createFPBody := decodeBody(t, createFPResp)
	createdFP, _ := createFPBody["fargateProfile"].(map[string]any)
	createdFPTags, _ := createdFP["tags"].(map[string]any)
	if createdFPTags["stage"] != "dev" {
		t.Fatalf("expected fargate profile create response tags {stage:dev}, got %#v", createdFP["tags"])
	}
	descFP := eksCall(t, http.MethodGet, srv.URL+"/clusters/tags-cluster/fargate-profiles/tagged-fp", nil)
	fpBody := decodeBody(t, descFP)
	fp, _ := fpBody["fargateProfile"].(map[string]any)
	fpTags, _ := fp["tags"].(map[string]any)
	if fpTags["stage"] != "dev" {
		t.Fatalf("expected fargate-profile tags {stage:dev}, got %#v", fp["tags"])
	}

	// Addon
	createAddonResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/tags-cluster/addons", map[string]any{
		"addonName": "kube-proxy",
		"tags":      map[string]string{"purpose": "networking"},
	})
	if createAddonResp.StatusCode != http.StatusCreated {
		t.Fatalf("addon create expected 201, got %d", createAddonResp.StatusCode)
	}
	createAddonBody := decodeBody(t, createAddonResp)
	createdAddon, _ := createAddonBody["addon"].(map[string]any)
	createdAddonTags, _ := createdAddon["tags"].(map[string]any)
	if createdAddonTags["purpose"] != "networking" {
		t.Fatalf("expected addon create response tags {purpose:networking}, got %#v", createdAddon["tags"])
	}
	descAddon := eksCall(t, http.MethodGet, srv.URL+"/clusters/tags-cluster/addons/kube-proxy", nil)
	addonBody := decodeBody(t, descAddon)
	addon, _ := addonBody["addon"].(map[string]any)
	addonTags, _ := addon["tags"].(map[string]any)
	if addonTags["purpose"] != "networking" {
		t.Fatalf("expected addon tags {purpose:networking}, got %#v", addon["tags"])
	}

	// Tags added via TagResource also appear in describe
	eksCall(t, http.MethodPost, srv.URL+"/tags/"+url.PathEscape(cluster["arn"].(string)), map[string]any{
		"tags": map[string]string{"added": "later"},
	})
	descCluster2 := eksCall(t, http.MethodGet, srv.URL+"/clusters/tags-cluster", nil)
	clusterBody2 := decodeBody(t, descCluster2)
	cluster2, _ := clusterBody2["cluster"].(map[string]any)
	clusterTags2, _ := cluster2["tags"].(map[string]any)
	if clusterTags2["added"] != "later" {
		t.Fatalf("expected tag 'added' after TagResource, got %#v", cluster2["tags"])
	}
}
