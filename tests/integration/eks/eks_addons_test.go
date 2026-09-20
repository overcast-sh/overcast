package eks_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEKSDeleteClusterClearsAddonAndFargateMetadata(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "addon-fargate-cleanup-cluster", nil)
	_ = mustCreateAddon(t, srv.URL, "addon-fargate-cleanup-cluster", "coredns", "v1.10.1-eksbuild.1")
	_ = mustCreateFargateProfile(t, srv.URL, "addon-fargate-cleanup-cluster", "apps", []map[string]any{{"namespace": "default"}})

	expectStatus(t, eksCall(t, http.MethodDelete, srv.URL+"/clusters/addon-fargate-cleanup-cluster", nil), http.StatusOK)
	_ = mustCreateCluster(t, srv.URL, "addon-fargate-cleanup-cluster", nil)

	addonsBody := expectJSONStatus(t, eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-fargate-cleanup-cluster/addons", nil), http.StatusOK)
	addons, _ := addonsBody["addons"].([]any)
	if len(addons) != 0 {
		t.Fatalf("expected no retained addons after cluster delete, got %v", addons)
	}

	fargateBody := expectJSONStatus(t, eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-fargate-cleanup-cluster/fargate-profiles", nil), http.StatusOK)
	profiles, _ := fargateBody["fargateProfileNames"].([]any)
	if len(profiles) != 1 || profiles[0] != "default" {
		t.Fatalf("expected only synthetic default fargate profile after recreate, got %v", profiles)
	}
}

func TestEKSFargateProfileLifecycle(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "fargate-cluster", nil)

	fp := mustCreateFargateProfile(t, srv.URL, "fargate-cluster", "my-profile", []map[string]any{{"namespace": "app"}})
	if fp["fargateProfileName"] != "my-profile" {
		t.Fatalf("expected fargateProfileName my-profile, got %v", fp["fargateProfileName"])
	}
	if fp["status"] != "ACTIVE" {
		t.Fatalf("expected status ACTIVE, got %v", fp["status"])
	}
	fpARN, _ := fp["fargateProfileArn"].(string)
	if fpARN == "" {
		t.Fatalf("expected fargateProfileArn in response, got %#v", fp)
	}
	mustTagResource(t, srv.URL, fpARN, map[string]string{"owner": "fargate-delete-test"})

	listResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/fargate-cluster/fargate-profiles", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list fargate profiles, got %d", listResp.StatusCode)
	}
	listBody := decodeBody(t, listResp)
	names, _ := listBody["fargateProfileNames"].([]any)
	found := false
	for _, n := range names {
		if n == "my-profile" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected my-profile in list, got %v", names)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/fargate-cluster/fargate-profiles/my-profile", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe fargate profile, got %d", descResp.StatusCode)
	}
	descBody := decodeBody(t, descResp)
	described, ok := descBody["fargateProfile"].(map[string]any)
	if !ok {
		t.Fatalf("expected fargateProfile in describe response, got %#v", descBody)
	}
	if described["fargateProfileName"] != "my-profile" {
		t.Fatalf("expected my-profile, got %v", described["fargateProfileName"])
	}

	delResp := eksCall(t, http.MethodDelete, srv.URL+"/clusters/fargate-cluster/fargate-profiles/my-profile", nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for delete fargate profile, got %d", delResp.StatusCode)
	}
	delBody := decodeBody(t, delResp)
	deleted, ok := delBody["fargateProfile"].(map[string]any)
	if !ok {
		t.Fatalf("expected fargateProfile in delete response, got %#v", delBody)
	}
	if deleted["status"] != "DELETING" {
		t.Fatalf("expected status DELETING, got %v", deleted["status"])
	}

	gone := eksCall(t, http.MethodGet, srv.URL+"/clusters/fargate-cluster/fargate-profiles/my-profile", nil)
	_ = expectResourceNotFound(t, gone)

	_ = mustCreateFargateProfile(t, srv.URL, "fargate-cluster", "my-profile", []map[string]any{{"namespace": "app"}})

	tags := listResourceTags(t, srv.URL, fpARN)
	if len(tags) != 0 {
		t.Fatalf("expected no retained fargate profile tags after delete/recreate, got %#v", tags)
	}
}

func TestEKSAddonLifecycle(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "addon-cluster", nil)

	addon := mustCreateAddon(t, srv.URL, "addon-cluster", "vpc-cni", "v1.16.0-eksbuild.1")
	if addon["addonName"] != "vpc-cni" {
		t.Fatalf("expected addonName vpc-cni, got %v", addon["addonName"])
	}
	if addon["status"] != "ACTIVE" {
		t.Fatalf("expected status ACTIVE, got %v", addon["status"])
	}
	addonARN, _ := addon["addonArn"].(string)
	if addonARN == "" {
		t.Fatalf("expected addonArn in response, got %#v", addon)
	}
	mustTagResource(t, srv.URL, addonARN, map[string]string{"owner": "addon-delete-test"})

	listResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-cluster/addons", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for list addons, got %d", listResp.StatusCode)
	}
	listBody := decodeBody(t, listResp)
	names, _ := listBody["addons"].([]any)
	found := false
	for _, n := range names {
		if n == "vpc-cni" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected vpc-cni in list, got %v", names)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-cluster/addons/vpc-cni", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe addon, got %d", descResp.StatusCode)
	}
	descBody := decodeBody(t, descResp)
	described, ok := descBody["addon"].(map[string]any)
	if !ok {
		t.Fatalf("expected addon in describe response, got %#v", descBody)
	}
	if described["addonName"] != "vpc-cni" {
		t.Fatalf("expected vpc-cni, got %v", described["addonName"])
	}

	delResp := eksCall(t, http.MethodDelete, srv.URL+"/clusters/addon-cluster/addons/vpc-cni", nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for delete addon, got %d", delResp.StatusCode)
	}
	delBody := decodeBody(t, delResp)
	deleted, ok := delBody["addon"].(map[string]any)
	if !ok {
		t.Fatalf("expected addon in delete response, got %#v", delBody)
	}
	if deleted["status"] != "DELETING" {
		t.Fatalf("expected status DELETING, got %v", deleted["status"])
	}

	goneResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-cluster/addons/vpc-cni", nil)
	_ = expectResourceNotFound(t, goneResp)

	_ = mustCreateAddon(t, srv.URL, "addon-cluster", "vpc-cni", "v1.16.0-eksbuild.1")

	tags := listResourceTags(t, srv.URL, addonARN)
	if len(tags) != 0 {
		t.Fatalf("expected no retained addon tags after delete/recreate, got %#v", tags)
	}
}

func TestEKSUpdateAddon(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "addon-update-cluster", nil)
	_ = mustCreateAddon(t, srv.URL, "addon-update-cluster", "vpc-cni", "v1.16.0-eksbuild.1")

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/addon-update-cluster/addons/vpc-cni/update", map[string]any{
		"addonVersion":        "v1.18.3-eksbuild.3",
		"configurationValues": "{\"env\":{\"AWS_VPC_K8S_CNI_LOGLEVEL\":\"DEBUG\"}}",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update addon, got %d", updateResp.StatusCode)
	}
	updateBody := decodeBody(t, updateResp)
	update, ok := updateBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update in response, got %#v", updateBody)
	}
	if update["type"] != "AddonUpdate" {
		t.Fatalf("expected update type AddonUpdate, got %v", update["type"])
	}
	updateID, _ := update["id"].(string)
	if updateID == "" {
		t.Fatalf("expected non-empty update id")
	}

	descUpdateResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-update-cluster/updates/"+updateID, nil)
	if descUpdateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe update, got %d", descUpdateResp.StatusCode)
	}
	_ = decodeBody(t, descUpdateResp)

	descAddonResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-update-cluster/addons/vpc-cni", nil)
	if descAddonResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe addon, got %d", descAddonResp.StatusCode)
	}
	descAddonBody := decodeBody(t, descAddonResp)
	addon, ok := descAddonBody["addon"].(map[string]any)
	if !ok {
		t.Fatalf("expected addon in describe response, got %#v", descAddonBody)
	}
	if addon["addonVersion"] != "v1.18.3-eksbuild.3" {
		t.Fatalf("expected addonVersion to be updated, got %v", addon["addonVersion"])
	}
	if addon["configurationValues"] != "{\"env\":{\"AWS_VPC_K8S_CNI_LOGLEVEL\":\"DEBUG\"}}" {
		t.Fatalf("expected configurationValues to be updated, got %v", addon["configurationValues"])
	}
}

// TestEKSUpdateAddonEchoesResolveConflicts proves the #1979 claim that
// resolveConflicts is echoed into the update's params: DescribeUpdate shows
// {type: ResolveConflicts, value: OVERWRITE} per the API reference, and the
// addon update itself proceeds normally.
// https://docs.aws.amazon.com/eks/latest/APIReference/API_UpdateAddon.html
func TestEKSUpdateAddonEchoesResolveConflicts(t *testing.T) {
	srv := newEKSServer(t)

	_ = mustCreateCluster(t, srv.URL, "addon-resolve-conflicts-cluster", nil)
	_ = mustCreateAddon(t, srv.URL, "addon-resolve-conflicts-cluster", "vpc-cni", "v1.16.0-eksbuild.1")

	updateResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/addon-resolve-conflicts-cluster/addons/vpc-cni/update", map[string]any{
		"addonVersion":     "v1.18.3-eksbuild.3",
		"resolveConflicts": "OVERWRITE",
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for update addon, got %d", updateResp.StatusCode)
	}
	updateBody := decodeBody(t, updateResp)
	update, ok := updateBody["update"].(map[string]any)
	if !ok {
		t.Fatalf("expected update in response, got %#v", updateBody)
	}
	updateID, _ := update["id"].(string)
	if updateID == "" {
		t.Fatalf("expected non-empty update id")
	}
	if !addonUpdateParamsContain(update["params"], "ResolveConflicts", "OVERWRITE") {
		t.Fatalf("expected update.params to contain {type: ResolveConflicts, value: OVERWRITE}, got %#v", update["params"])
	}

	descUpdateResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-resolve-conflicts-cluster/updates/"+updateID, nil)
	if descUpdateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe update, got %d", descUpdateResp.StatusCode)
	}
	descUpdateBody := decodeBody(t, descUpdateResp)
	descUpdate, _ := descUpdateBody["update"].(map[string]any)
	if !addonUpdateParamsContain(descUpdate["params"], "ResolveConflicts", "OVERWRITE") {
		t.Fatalf("expected DescribeUpdate params to contain {type: ResolveConflicts, value: OVERWRITE}, got %#v", descUpdate["params"])
	}

	descAddonResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-resolve-conflicts-cluster/addons/vpc-cni", nil)
	if descAddonResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for describe addon, got %d", descAddonResp.StatusCode)
	}
	descAddonBody := decodeBody(t, descAddonResp)
	addon, _ := descAddonBody["addon"].(map[string]any)
	if addon["addonVersion"] != "v1.18.3-eksbuild.3" {
		t.Fatalf("expected addon update to proceed alongside resolveConflicts, addonVersion got %v", addon["addonVersion"])
	}
}

// addonUpdateParamsContain reports whether an Update.params slice (as
// decoded from JSON) contains an entry with the given type and value.
func addonUpdateParamsContain(params any, wantType, wantValue string) bool {
	list, _ := params.([]any)
	for _, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == wantType && m["value"] == wantValue {
			return true
		}
	}
	return false
}

func TestEKSDescribeAddonVersions(t *testing.T) {
	srv := newEKSServer(t)

	resp := eksCall(t, http.MethodGet, srv.URL+"/addons/supported-versions?addonName=vpc-cni", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for vpc-cni versions, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	addons, _ := body["addons"].([]any)
	if len(addons) == 0 {
		t.Fatalf("expected at least one addon version entry, got empty")
	}
	entry, _ := addons[0].(map[string]any)
	if entry["addonName"] != "vpc-cni" {
		t.Fatalf("expected addonName vpc-cni, got %v", entry["addonName"])
	}
	versions, _ := entry["addonVersions"].([]any)
	if len(versions) == 0 {
		t.Fatalf("expected at least one version in addonVersions")
	}

	resp2 := eksCall(t, http.MethodGet, srv.URL+"/addons/supported-versions?addonName=my-custom-addon", nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for unknown addon, got %d", resp2.StatusCode)
	}
	body2 := decodeBody(t, resp2)
	addons2, _ := body2["addons"].([]any)
	if len(addons2) != 0 {
		t.Fatalf("expected empty addon catalog for unknown addon, got %v", body2["addons"])
	}
}

func TestEKSDescribeAddonConfiguration(t *testing.T) {
	srv := newEKSServer(t)

	resp := eksCall(t, http.MethodGet, srv.URL+"/addons/configuration-schemas?addonName=vpc-cni&addonVersion=v1.18.3-eksbuild.3", nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for addon configuration, got %d body=%q", resp.StatusCode, string(raw))
	}
	body := decodeBody(t, resp)
	if body["addonName"] != "vpc-cni" {
		t.Fatalf("expected addonName vpc-cni, got %v", body["addonName"])
	}
	if body["addonVersion"] != "v1.18.3-eksbuild.3" {
		t.Fatalf("expected addonVersion v1.18.3-eksbuild.3, got %v", body["addonVersion"])
	}
	schema, _ := body["configurationSchema"].(string)
	if !strings.Contains(schema, "AWS_VPC_K8S_CNI_LOGLEVEL") {
		t.Fatalf("expected configuration schema to include AWS_VPC_K8S_CNI_LOGLEVEL, got %q", schema)
	}

	unknown := eksCall(t, http.MethodGet, srv.URL+"/addons/configuration-schemas?addonName=unknown-addon&addonVersion=v1.0.0", nil)
	_ = expectResourceNotFound(t, unknown)
}

// TestEKSCreateAddonRejectsUnknownName covers #1692: CreateAddon only accepts
// an addonName DescribeAddonVersions publishes. An invented name must be
// rejected the way real EKS rejects it — InvalidParameterException — rather
// than silently creating an add-on no AWS account could ever have.
func TestEKSCreateAddonRejectsUnknownName(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "addon-unknown-name-cluster", nil)

	resp := eksCall(t, http.MethodPost, srv.URL+"/clusters/addon-unknown-name-cluster/addons", map[string]any{
		"addonName": "totally-invented-addon",
	})
	body := expectJSONStatus(t, resp, http.StatusBadRequest)
	if body["__type"] != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException for an unpublished addonName, got %#v", body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "totally-invented-addon") {
		t.Fatalf("expected the error message to name the rejected addon, got %q", msg)
	}

	// And: no addon was created.
	listResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-unknown-name-cluster/addons", nil)
	listBody := expectJSONStatus(t, listResp, http.StatusOK)
	if addons, _ := listBody["addons"].([]any); len(addons) != 0 {
		t.Fatalf("expected no addons after a rejected CreateAddon, got %v", addons)
	}
}

// TestEKSCreateAddonRejectsMissingName covers the other half of #1692:
// addonName is @required, so an absent value must fail the same way real EKS
// fails it, not default to an empty or placeholder name.
func TestEKSCreateAddonRejectsMissingName(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "addon-missing-name-cluster", nil)

	resp := eksCall(t, http.MethodPost, srv.URL+"/clusters/addon-missing-name-cluster/addons", map[string]any{})
	body := expectJSONStatus(t, resp, http.StatusBadRequest)
	if body["__type"] != "InvalidParameterException" {
		t.Fatalf("expected InvalidParameterException for a missing addonName, got %#v", body)
	}
}

// TestEKSCreateAddonAcceptsEveryPublishedName proves CreateAddon and
// DescribeAddonVersions agree: every name the catalog advertises must also be
// accepted by CreateAddon, so the "small published list" is genuinely one
// source of truth rather than two lists that can drift apart.
func TestEKSCreateAddonAcceptsEveryPublishedName(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "addon-published-names-cluster", nil)

	resp := eksCall(t, http.MethodGet, srv.URL+"/addons/supported-versions", nil)
	body := expectJSONStatus(t, resp, http.StatusOK)
	addons, _ := body["addons"].([]any)
	if len(addons) == 0 {
		t.Fatalf("expected DescribeAddonVersions to publish at least one addon")
	}
	for _, raw := range addons {
		entry, _ := raw.(map[string]any)
		name, _ := entry["addonName"].(string)
		if name == "" {
			t.Fatalf("addon entry has no addonName: %#v", entry)
		}
		t.Run(name, func(t *testing.T) {
			created := mustCreateAddon(t, srv.URL, "addon-published-names-cluster", name, "")
			if created["addonName"] != name {
				t.Fatalf("expected addonName %s, got %v", name, created["addonName"])
			}
			expectStatus(t, eksCall(t, http.MethodDelete, srv.URL+"/clusters/addon-published-names-cluster/addons/"+name, nil), http.StatusOK)
		})
	}
}

func TestEKSCreateAddonPreservesFullShape(t *testing.T) {
	srv := newEKSServer(t)
	_ = mustCreateCluster(t, srv.URL, "addon-shape-cluster", nil)

	createResp := eksCall(t, http.MethodPost, srv.URL+"/clusters/addon-shape-cluster/addons", map[string]any{
		"addonName":             "vpc-cni",
		"addonVersion":          "v1.18.0-eksbuild.1",
		"configurationValues":   `{"env":{"ENABLE_PREFIX_DELEGATION":"true"}}`,
		"serviceAccountRoleArn": "arn:aws:iam::000000000000:role/vpc-cni-role",
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}

	descResp := eksCall(t, http.MethodGet, srv.URL+"/clusters/addon-shape-cluster/addons/vpc-cni", nil)
	if descResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", descResp.StatusCode)
	}
	body := decodeBody(t, descResp)
	addon, _ := body["addon"].(map[string]any)

	if addon["addonVersion"] != "v1.18.0-eksbuild.1" {
		t.Fatalf("expected addonVersion v1.18.0-eksbuild.1, got %v", addon["addonVersion"])
	}
	if addon["configurationValues"] != `{"env":{"ENABLE_PREFIX_DELEGATION":"true"}}` {
		t.Fatalf("expected configurationValues round-trip, got %v", addon["configurationValues"])
	}
	if addon["serviceAccountRoleArn"] != "arn:aws:iam::000000000000:role/vpc-cni-role" {
		t.Fatalf("expected serviceAccountRoleArn round-trip, got %v", addon["serviceAccountRoleArn"])
	}
}
