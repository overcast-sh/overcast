package ecr_test

// docker_scanner_annotations_test.go pins the without-Docker and
// never-scans behaviour that internal/services/ecr/capabilities_dev.go now
// documents for issue #141: which operations degrade when no Docker daemon
// is available, and which scanning operations are accepted but never run a
// scan. None of these tests requires a running Docker daemon or registry
// container — helpers.NewTestServer's default config leaves
// LambdaDockerSocket empty, which is exactly the "no Docker" state these
// capabilities describe.

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// TestGetAuthorizationToken_withoutDocker_fallsBackToTheAPIPort pins the
// GetAuthorizationToken capability Note: without a reachable Docker daemon,
// ensureRegistry never starts a container, so proxyEndpoint names Overcast's
// own API port rather than a registry, and the token authenticates against
// nothing there.
func TestGetAuthorizationToken_withoutDocker_fallsBackToTheAPIPort(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithAccountID("000000000000"))

	resp := ecrCall(t, srv, "GetAuthorizationToken", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := mustDecode(t, resp)
	data := body["authorizationData"].([]any)
	entry := data[0].(map[string]any)

	proxy, _ := entry["proxyEndpoint"].(string)
	if proxy != srv.ExternalBase() {
		t.Fatalf("proxyEndpoint = %q, want the emulator's own API base %q — no Docker means no registry to name", proxy, srv.ExternalBase())
	}

	// The password half of the token falls back the same way registryEndpoint
	// documents: no registry was ever started, so no per-instance password was
	// ever generated, and the placeholder "test" ships instead.
	tokenStr, _ := entry["authorizationToken"].(string)
	decoded, err := base64.StdEncoding.DecodeString(tokenStr)
	if err != nil {
		t.Fatalf("decode authorizationToken: %v", err)
	}
	if string(decoded) != "AWS:test" {
		t.Fatalf("authorizationToken decodes to %q, want the no-registry placeholder %q", decoded, "AWS:test")
	}
}

// TestPutImageScanningConfiguration_neverTriggersAScan pins the Inert
// capability Note on PutImageScanningConfiguration: the setting round-trips
// through DescribeRepositories, but turning scanOnPush on never makes
// DescribeImageScanFindings report anything but scanner-unavailable — no
// scan engine runs regardless of the configuration.
func TestPutImageScanningConfiguration_neverTriggersAScan(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithRegion("us-east-1"), helpers.WithAccountID("000000000000"))
	ecrCall(t, srv, "CreateRepository", map[string]any{"repositoryName": "scan-config"}).Body.Close()
	ecrCall(t, srv, "PutImage", map[string]any{
		"repositoryName": "scan-config",
		"imageManifest":  `{"schemaVersion":2}`,
		"imageTag":       "latest",
	}).Body.Close()

	resp := ecrCall(t, srv, "PutImageScanningConfiguration", map[string]any{
		"repositoryName":             "scan-config",
		"imageScanningConfiguration": map[string]any{"scanOnPush": true},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PutImageScanningConfiguration: expected 200, got %d", resp.StatusCode)
	}
	body := mustDecode(t, resp)
	resp.Body.Close()
	cfg, ok := body["imageScanningConfiguration"].(map[string]any)
	if !ok || cfg["scanOnPush"] != true {
		t.Fatalf("PutImageScanningConfiguration response = %#v, want scanOnPush echoed back true", body)
	}

	// DescribeRepositories must agree — the setting is stored, not discarded.
	describeResp := ecrCall(t, srv, "DescribeRepositories", map[string]any{"repositoryNames": []string{"scan-config"}})
	describeBody := mustDecode(t, describeResp)
	describeResp.Body.Close()
	repos := describeBody["repositories"].([]any)
	repo := repos[0].(map[string]any)
	repoCfg := repo["imageScanningConfiguration"].(map[string]any)
	if repoCfg["scanOnPush"] != true {
		t.Fatalf("DescribeRepositories imageScanningConfiguration = %#v, want scanOnPush true after PutImageScanningConfiguration", repoCfg)
	}

	// But no scan ever ran: findings still report scanner-unavailable, exactly
	// as they would with scanOnPush left at its default false.
	findingsResp := ecrCall(t, srv, "DescribeImageScanFindings", map[string]any{
		"repositoryName": "scan-config",
		"imageId":        map[string]any{"imageTag": "latest"},
	})
	defer findingsResp.Body.Close()
	if findingsResp.StatusCode != http.StatusOK {
		t.Fatalf("DescribeImageScanFindings: expected 200, got %d", findingsResp.StatusCode)
	}
	findingsBody := mustDecode(t, findingsResp)
	status := findingsBody["imageScanStatus"].(map[string]any)
	if status["status"] != "UNSUPPORTED_IMAGE" {
		t.Fatalf("imageScanStatus = %#v, want UNSUPPORTED_IMAGE even after scanOnPush was enabled — no scan engine is emulated", status)
	}
}

// TestScanningOperations_notImplementedReturn501 pins that the three
// scanning operations Overcast declares but does not implement —
// StartImageScan, GetRegistryScanningConfiguration and
// PutRegistryScanningConfiguration — answer 501 rather than a bare 404 or a
// misleading 200, per capabilities_dev.go's StatusUnsupported rows for them.
func TestScanningOperations_notImplementedReturn501(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithRegion("us-east-1"), helpers.WithAccountID("000000000000"))

	for _, op := range []string{"StartImageScan", "GetRegistryScanningConfiguration", "PutRegistryScanningConfiguration"} {
		t.Run(op, func(t *testing.T) {
			resp := ecrCall(t, srv, op, map[string]any{})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotImplemented {
				t.Fatalf("%s: expected 501, got %d", op, resp.StatusCode)
			}
			if got := resp.Header.Get("x-emulator-unsupported"); !strings.EqualFold(got, "true") {
				t.Errorf("%s: x-emulator-unsupported header = %q, want true", op, got)
			}
		})
	}
}
