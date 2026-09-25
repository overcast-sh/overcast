package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// setOwnerLabelsForTest installs owner labels for the duration of one test.
// The labels are process-wide, so no test that sets them may run in parallel.
func setOwnerLabelsForTest(t *testing.T, labels map[string]string) {
	t.Helper()
	SetOwnerLabels(labels)
	t.Cleanup(func() { SetOwnerLabels(nil) })
}

func TestCreateContainer_stampsOwnerLabels(t *testing.T) {
	// Given: a test binary has declared itself the owner of what it creates.
	setOwnerLabelsForTest(t, map[string]string{LabelTestPID: "4242", LabelTestHost: "ci-host"})
	var got CreateContainerRequest
	c := newVolumeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		w.Write([]byte(`{"Id":"abc"}`)) //nolint:errcheck
	})
	managed := ManagedLabels("ecs", "cluster/task")
	req := &CreateContainerRequest{ContainerConfig: &ContainerConfig{Image: "busybox:1.36", Labels: managed}}

	// When: any service creates a container through the shared client.
	if _, err := c.CreateContainer(context.Background(), "overcast-ecs-x", req); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	// Then: the daemon sees the service's labels and the owner's, and the
	// caller's map is left exactly as it passed it.
	if got.Labels[LabelService] != "ecs" || got.Labels[LabelTestPID] != "4242" || got.Labels[LabelTestHost] != "ci-host" {
		t.Fatalf("labels sent = %#v, want the managed labels plus the owner labels", got.Labels)
	}
	if _, leaked := managed[LabelTestPID]; leaked {
		t.Fatalf("caller's label map was mutated: %#v", managed)
	}
	if _, leaked := req.Labels[LabelTestPID]; leaked {
		t.Fatalf("caller's request was mutated: %#v", req.Labels)
	}
}

func TestCreateContainer_ownerLabelsNeverOverrideTheCaller(t *testing.T) {
	// Given: an owner label whose key the caller also sets.
	setOwnerLabelsForTest(t, map[string]string{LabelTestPID: "4242", LabelService: "owner"})
	var got CreateContainerRequest
	c := newVolumeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"Id":"abc"}`)) //nolint:errcheck
	})

	// When: the container is created.
	_, err := c.CreateContainer(context.Background(), "", &CreateContainerRequest{ContainerConfig: &ContainerConfig{Labels: ManagedLabels("ecs", "r")}})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	// Then: the caller's value wins; owner labels only add.
	if got.Labels[LabelService] != "ecs" {
		t.Fatalf("service label = %q, want the caller's %q", got.Labels[LabelService], "ecs")
	}
}

func TestCreateContainer_noOwnerLabelsByDefault(t *testing.T) {
	// Given: a production process, which never sets owner labels.
	var got CreateContainerRequest
	c := newVolumeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"Id":"abc"}`)) //nolint:errcheck
	})

	// When: a container is created.
	if _, err := c.CreateContainer(context.Background(), "", &CreateContainerRequest{ContainerConfig: &ContainerConfig{Labels: ManagedLabels("ecs", "r")}}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	// Then: nothing a test reaper keys on is present.
	for k := range got.Labels {
		if strings.HasPrefix(k, "overcast.test.") {
			t.Fatalf("production container carries test label %q: %#v", k, got.Labels)
		}
	}
}

func TestCreateVolume_neverCarriesOwnerLabels(t *testing.T) {
	// Given: owner labels are set.
	setOwnerLabelsForTest(t, map[string]string{LabelTestPID: "4242"})
	var got createVolumeRequest
	c := newVolumeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"Name":"v"}`)) //nolint:errcheck
	})

	// When: a volume is created.
	if err := c.CreateVolume(context.Background(), "overcast-lambda-init-abc", ManagedLabels("lambda", "init")); err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	// Then: it carries only the creator's labels. Volumes outlive the process
	// that made them on purpose — the Lambda init volume is shared by every
	// process running the same init — so stamping an owner on one would hand
	// it to the next test binary's reaper while others still mount it.
	if _, ok := got.Labels[LabelTestPID]; ok || got.Labels[LabelService] != "lambda" {
		t.Fatalf("labels sent = %#v, want only the managed labels", got.Labels)
	}
}

func TestListContainersWithLabel_filtersOnTheKeyAlone(t *testing.T) {
	// Given: a daemon that records the filter it was asked for.
	var filters string
	c := newVolumeTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		filters = r.URL.Query().Get("filters")
		if r.URL.Query().Get("all") != "true" {
			t.Errorf("stopped containers must be included: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`[{"Id":"c1","Labels":{"overcast.test.pid":"1"}}]`)) //nolint:errcheck
	})

	// When: the reaper asks for everything carrying the owner label.
	got, err := c.ListContainersWithLabel(context.Background(), LabelTestPID)
	if err != nil {
		t.Fatalf("ListContainersWithLabel: %v", err)
	}

	// Then: the filter is the bare key — whatever its value — and nothing else,
	// so a container without the managed label is still found.
	var parsed map[string][]string
	if err := json.Unmarshal([]byte(filters), &parsed); err != nil {
		t.Fatalf("filters %q: %v", filters, err)
	}
	if len(parsed) != 1 || len(parsed["label"]) != 1 || parsed["label"][0] != LabelTestPID {
		t.Fatalf("filters = %v, want only label=%s", parsed, LabelTestPID)
	}
	if len(got) != 1 || got[0].ID != "c1" {
		t.Fatalf("containers = %#v", got)
	}
}
