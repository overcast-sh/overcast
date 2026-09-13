package eks

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	fakeK3sContainerID = "k3s-container-1"
	testK3sImage       = "rancher/k3s:v1.31.3-k3s1"
	testCAData         = "TEST-CA-DATA"
)

// fakeK3sDaemon speaks just enough of the Docker Engine API to bring up one
// k3s control plane — and, like a machine that has never run one, it starts
// with no images. containers/create answers 404 "No such image" for anything
// that was not pulled through it first, which is exactly what the daemon told
// Overcast in #892.
type fakeK3sDaemon struct {
	mu      sync.Mutex
	pulls   []string
	creates []string
	images  map[string]bool

	// requests is every call in arrival order, as "METHOD path", so a test
	// can assert on the order two operations happened in — a network connect
	// before a container start, say — rather than only that both happened.
	requests []string
	// connects is every networks/{id}/connect the daemon served: which
	// container joined which network advertising which aliases.
	connects []networkConnect

	// pullErr, when non-empty, fails images/create with this daemon message.
	pullErr string
	// createErr, when non-empty, fails containers/create with a 500 carrying
	// this message even for an image the daemon holds.
	createErr string

	// readyzPort is the host port containers/{id}/json advertises for
	// 6443/tcp — where the readiness poll dials the k3s API.
	readyzPort string

	// adoptable, when set, is the cluster name whose managed container
	// (overcast-eks-<name>) the daemon already holds, running, from an earlier
	// Overcast run — the shape restart adoption finds.
	adoptable string

	// inspected closes when the daemon first serves the container inspect, so
	// a test can act at the point a bootstrap has reached the readiness poll.
	inspected     chan struct{}
	inspectedOnce sync.Once

	srv *httptest.Server
}

func newFakeK3sDaemon(t *testing.T) *fakeK3sDaemon {
	t.Helper()

	fd := &fakeK3sDaemon{images: map[string]bool{}, inspected: make(chan struct{})}
	fd.srv = httptest.NewServer(http.HandlerFunc(fd.serve))
	t.Cleanup(fd.srv.Close)
	return fd
}

// networkConnect is one networks/{id}/connect call as the daemon saw it.
type networkConnect struct {
	network   string
	container string
	aliases   []string
}

func (fd *fakeK3sDaemon) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	fd.mu.Lock()
	fd.requests = append(fd.requests, r.Method+" "+path)
	adoptable := fd.adoptable
	fd.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/connect"):
		var body struct {
			Container      string `json:"Container"`
			EndpointConfig *struct {
				Aliases []string `json:"Aliases"`
			} `json:"EndpointConfig"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// /v1.45/networks/<name>/connect
		parts := strings.Split(strings.Trim(path, "/"), "/")
		connect := networkConnect{network: parts[len(parts)-2], container: body.Container}
		if body.EndpointConfig != nil {
			connect.aliases = body.EndpointConfig.Aliases
		}
		fd.mu.Lock()
		fd.connects = append(fd.connects, connect)
		fd.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && adoptable != "" &&
		strings.HasSuffix(path, "/containers/overcast-eks-"+adoptable+"/json"):
		fd.mu.Lock()
		port := fd.readyzPort
		fd.mu.Unlock()
		labels, _ := json.Marshal(docker.ManagedLabels(serviceName, adoptable))
		fmt.Fprintf(w, `{"Id":%q,"Name":"/overcast-eks-%s","Config":{"Labels":%s},`+
			`"State":{"Status":"running","Running":true},`+
			`"NetworkSettings":{"Ports":{"6443/tcp":[{"HostIp":"0.0.0.0","HostPort":%q}]}}}`,
			fakeK3sContainerID, adoptable, labels, port)

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/images/create"):
		image := r.URL.Query().Get("fromImage")
		if tag := r.URL.Query().Get("tag"); tag != "" {
			image += ":" + tag
		}
		fd.mu.Lock()
		fd.pulls = append(fd.pulls, image)
		pullErr := fd.pullErr
		if pullErr == "" {
			fd.images[image] = true
		}
		fd.mu.Unlock()
		if pullErr != "" {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"message":%q}`, pullErr)
			return
		}
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1.45/images/") && strings.HasSuffix(path, "/json"):
		image := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.45/images/"), "/json")
		fd.mu.Lock()
		held := fd.images[image]
		fd.mu.Unlock()
		if !held {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"Architecture":"amd64","Os":"linux"}`)

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/containers/create"):
		var req struct {
			Image string `json:"Image"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		fd.mu.Lock()
		held := fd.images[req.Image]
		createErr := fd.createErr
		if held && createErr == "" {
			fd.creates = append(fd.creates, req.Image)
		}
		fd.mu.Unlock()
		switch {
		case !held:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"message":"No such image: %s"}`, req.Image)
		case createErr != "":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"message":%q}`, createErr)
		default:
			fmt.Fprintf(w, `{"Id":%q}`, fakeK3sContainerID)
		}

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/"+fakeK3sContainerID+"/json"):
		fd.inspectedOnce.Do(func() { close(fd.inspected) })
		fd.mu.Lock()
		port := fd.readyzPort
		fd.mu.Unlock()
		fmt.Fprintf(w, `{"Id":%q,"State":{"Status":"running","Running":true},`+
			`"NetworkSettings":{"Ports":{"6443/tcp":[{"HostIp":"0.0.0.0","HostPort":%q}]}}}`,
			fakeK3sContainerID, port)

	case r.Method == http.MethodGet && strings.Contains(path, "/archive"):
		w.Write(k3sKubeconfigTar(testCAData)) //nolint:errcheck

	default:
		// container start, network connect, remove, …
		w.WriteHeader(http.StatusNoContent)
	}
}

func (fd *fakeK3sDaemon) pulledImages() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]string(nil), fd.pulls...)
}

func (fd *fakeK3sDaemon) createdImages() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]string(nil), fd.creates...)
}

func (fd *fakeK3sDaemon) requestLog() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]string(nil), fd.requests...)
}

func (fd *fakeK3sDaemon) networkConnects() []networkConnect {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]networkConnect(nil), fd.connects...)
}

// k3sKubeconfigTar renders the archive `docker cp` would return for
// /etc/rancher/k3s/k3s.yaml, carrying caData.
func k3sKubeconfigTar(caData string) []byte {
	body := "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: " + caData + "\n"
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "k3s.yaml", Mode: 0o600, Size: int64(len(body))})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	return buf.Bytes()
}

// newK3sReadyzServer stands in for the k3s API server on its published host
// port, answering the readiness probe pollK3sReady makes.
func newK3sReadyzServer(t *testing.T) string {
	t.Helper()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split readyz server address: %v", err)
	}
	return port
}

// newFakeDaemonService builds a live-mode EKS service wired to fd. Wired
// directly rather than through SetDocker so no startup reconciliation
// goroutine can race the recorded-call assertions.
func newFakeDaemonService(t *testing.T, fd *fakeK3sDaemon) *Service {
	t.Helper()

	svc := New(
		&config.Config{
			Region:    liveModeTestRegion,
			AccountID: "000000000000",
			EKSMode:   config.EKSModeLive,
			Network:   "overcast",
		},
		state.NewMemoryStore(), zap.NewNop(), clock.New(),
	)
	dc := docker.NewClient("tcp://"+fd.srv.Listener.Addr().String(), zap.NewNop())
	svc.docker = dc
	svc.puller = docker.NewImagePuller(dc)
	svc.dockerReady.Store(true)
	return svc
}

// putCreatingCluster persists the CREATING record createCluster writes before
// it hands off to startLiveCluster.
func putCreatingCluster(t *testing.T, svc *Service, name string) *Cluster {
	t.Helper()

	cluster := &Cluster{
		Name:                 name,
		Arn:                  svc.clusterARN(liveModeTestRegion, name),
		CreatedAt:            svc.clk.Now(),
		Version:              "1.31",
		Status:               "CREATING",
		CertificateAuthority: map[string]any{},
	}
	if err := svc.putCluster(context.Background(), liveModeTestRegion, cluster); err != nil {
		t.Fatalf("put creating cluster: %v", err)
	}
	svc.setLiveClusterRuntime(liveModeTestRegion, name, &liveClusterRuntime{})
	return cluster
}

func readCluster(t *testing.T, svc *Service, name string) *Cluster {
	t.Helper()

	cluster, found, err := svc.getCluster(context.Background(), liveModeTestRegion, name)
	if err != nil || !found {
		t.Fatalf("read cluster %q: found=%v err=%v", name, found, err)
	}
	return cluster
}

// TestLiveClusterPullsK3sImageBeforeCreate is #892: on a machine that does not
// already hold the k3s image, EKS called containers/create straight away and
// the daemon answered 404.
func TestLiveClusterPullsK3sImageBeforeCreate(t *testing.T) {
	fd := newFakeK3sDaemon(t)
	fd.readyzPort = newK3sReadyzServer(t)
	svc := newFakeDaemonService(t, fd)

	cluster := putCreatingCluster(t, svc, "pull-me")
	svc.startLiveCluster(context.Background(), liveModeTestRegion, cluster)

	if pulls := fd.pulledImages(); len(pulls) != 1 || pulls[0] != testK3sImage {
		t.Fatalf("expected the k3s image to be pulled before create, got pulls=%v", pulls)
	}
	if creates := fd.createdImages(); len(creates) != 1 || creates[0] != testK3sImage {
		t.Fatalf("expected one container created from %s, got %v", testK3sImage, creates)
	}

	got := readCluster(t, svc, "pull-me")
	if got.Status != "ACTIVE" {
		t.Fatalf("expected cluster ACTIVE after a successful pull and start, got %q", got.Status)
	}
	if data, _ := got.CertificateAuthority["data"].(string); data != testCAData {
		t.Fatalf("expected certificate authority data %q, got %q", testCAData, data)
	}
}

// TestLiveClusterPullFailureReachesTerminalState covers the second half of
// #892: the reason a cluster cannot start belongs in the record a waiter
// polls, not only in a warn log line the caller never sees.
func TestLiveClusterPullFailureReachesTerminalState(t *testing.T) {
	fd := newFakeK3sDaemon(t)
	fd.pullErr = "manifest unknown: manifest tagged by \"v1.31.3-k3s1\" is not found"
	svc := newFakeDaemonService(t, fd)

	cluster := putCreatingCluster(t, svc, "no-such-tag")
	svc.startLiveCluster(context.Background(), liveModeTestRegion, cluster)

	got := readCluster(t, svc, "no-such-tag")
	if got.Status != "FAILED" {
		t.Fatalf("expected FAILED after the image could not be pulled, got %q", got.Status)
	}
	if !strings.Contains(clusterHealthMessage(t, got), "manifest unknown") {
		t.Fatalf("expected the daemon's reason in cluster health, got %#v", got.Health)
	}
}

// TestLiveClusterCreateFailureReachesTerminalState is the same requirement for
// a create the daemon refuses after the image is in place.
func TestLiveClusterCreateFailureReachesTerminalState(t *testing.T) {
	fd := newFakeK3sDaemon(t)
	fd.createErr = "cannot allocate memory"
	svc := newFakeDaemonService(t, fd)

	cluster := putCreatingCluster(t, svc, "wont-create")
	svc.startLiveCluster(context.Background(), liveModeTestRegion, cluster)

	got := readCluster(t, svc, "wont-create")
	if got.Status != "FAILED" {
		t.Fatalf("expected FAILED after containers/create was refused, got %q", got.Status)
	}
	if !strings.Contains(clusterHealthMessage(t, got), "cannot allocate memory") {
		t.Fatalf("expected the daemon's reason in cluster health, got %#v", got.Health)
	}
}

// clusterHealthMessage returns the single health issue message on c, failing
// the test when the shape is not the one DescribeCluster promises.
func clusterHealthMessage(t *testing.T, c *Cluster) string {
	t.Helper()

	raw, err := json.Marshal(c.Health)
	if err != nil {
		t.Fatalf("marshal cluster health: %v", err)
	}
	var health struct {
		Issues []struct {
			Code        string   `json:"code"`
			Message     string   `json:"message"`
			ResourceIDs []string `json:"resourceIds"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatalf("decode cluster health %s: %v", raw, err)
	}
	if len(health.Issues) != 1 {
		t.Fatalf("expected exactly one health issue, got %s", raw)
	}
	if health.Issues[0].Code == "" {
		t.Fatalf("expected a health issue code, got %s", raw)
	}
	if len(health.Issues[0].ResourceIDs) != 1 || health.Issues[0].ResourceIDs[0] != c.Name {
		t.Fatalf("expected the cluster named as the affected resource, got %s", raw)
	}
	return health.Issues[0].Message
}
