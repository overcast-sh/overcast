package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewClient_UnixSocket(t *testing.T) {
	c := NewClient("/var/run/docker.sock", zap.NewNop())
	if c.host != "http://docker" {
		t.Errorf("expected host http://docker, got %s", c.host)
	}
}

func TestNewClient_TCP(t *testing.T) {
	c := NewClient("tcp://dind:2375", zap.NewNop())
	if c.host != "http://dind:2375" {
		t.Errorf("expected host http://dind:2375, got %s", c.host)
	}
}

func TestNewClient_TCPLocalhost(t *testing.T) {
	c := NewClient("tcp://127.0.0.1:2375", zap.NewNop())
	if c.host != "http://127.0.0.1:2375" {
		t.Errorf("expected host http://127.0.0.1:2375, got %s", c.host)
	}
}

func TestNewClient_BarePathIsUnix(t *testing.T) {
	c := NewClient("/tmp/custom.sock", zap.NewNop())
	if c.host != "http://docker" {
		t.Errorf("expected host http://docker for bare path, got %s", c.host)
	}
}

func TestEndpointAliases_filtersNonDNSAddresses(t *testing.T) {
	// Given: endpoint addresses containing DNS names, duplicate names, and direct IP/localhost addresses.
	addresses := []string{
		"cache.ap-southeast-2.serverless.localhost",
		"127.0.0.1",
		"localhost",
		"10.0.0.5",
		"cache.ap-southeast-2.serverless.localhost",
		"reader.ap-southeast-2.serverless.localhost",
	}

	// When: Docker aliases are built.
	got := EndpointAliases(addresses...)

	// Then: only unique DNS hostnames remain, preserving first-seen order.
	want := []string{"cache.ap-southeast-2.serverless.localhost", "reader.ap-southeast-2.serverless.localhost"}
	if !slices.Equal(got, want) {
		t.Fatalf("EndpointAliases() = %#v, want %#v", got, want)
	}
}

func TestInfo_returnsHostResources(t *testing.T) {
	// Given: a fake Docker daemon reporting its CPU and memory resources.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1.45/info" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		// A real /info response carries dozens of fields; only the resource
		// ones matter here, plus one extra to prove unknown fields are ignored.
		_, _ = w.Write([]byte(`{"NCPU":8,"MemTotal":16777216000,"ServerVersion":"27.0.1"}`))
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: daemon system info is fetched.
	info, err := client.Info(context.Background())

	// Then: the daemon's host resources are reported — the machine containers
	// actually run on, not necessarily the machine Overcast runs on.
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.NCPU != 8 {
		t.Fatalf("NCPU = %d, want 8", info.NCPU)
	}
	if info.MemTotal != 16777216000 {
		t.Fatalf("MemTotal = %d, want 16777216000", info.MemTotal)
	}
}

func TestInfo_daemonErrorIsReturned(t *testing.T) {
	// Given: a daemon that answers /info with a server error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When/Then: the error surfaces instead of a zero-value SystemInfo.
	if _, err := client.Info(context.Background()); err == nil {
		t.Fatal("Info: expected an error for a 500 response")
	}
}

func TestConnectNetworkWithAliases_sendsEndpointConfig(t *testing.T) {
	// Given: a fake Docker daemon that captures network connect payloads.
	var got struct {
		Container      string            `json:"Container"`
		EndpointConfig *EndpointSettings `json:"EndpointConfig"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.45/networks/lambda-net/connect" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode connect payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: a container is connected with DNS aliases.
	err := client.ConnectNetworkWithAliases(context.Background(), "lambda-net", "container-1", []string{"cache.localhost"})

	// Then: Docker receives EndpointConfig aliases.
	if err != nil {
		t.Fatalf("ConnectNetworkWithAliases: %v", err)
	}
	if got.Container != "container-1" {
		t.Fatalf("Container = %q, want container-1", got.Container)
	}
	if got.EndpointConfig == nil || !slices.Equal(got.EndpointConfig.Aliases, []string{"cache.localhost"}) {
		t.Fatalf("EndpointConfig = %#v, want aliases", got.EndpointConfig)
	}
}

func TestContainerStatsOneShot_usesOneShotAndParsesSample(t *testing.T) {
	// Given: a fake Docker daemon that records the stats query and returns a
	// single stats sample.
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1.45/containers/abc123/stats" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{
			"memory_stats": {"usage": 134217728, "stats": {"inactive_file": 33554432}},
			"cpu_stats": {"cpu_usage": {"total_usage": 5000000}, "system_cpu_usage": 100000000, "online_cpus": 4}
		}`))
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: a stats sample is fetched.
	stats, err := client.ContainerStatsOneShot(context.Background(), "abc123")

	// Then: one-shot=true is requested — stream=false alone makes the daemon
	// wait an extra collection cycle (~1–2 s), which is what used to overrun
	// caller timeouts on slow hosts and surface as "memory used: 0".
	if err != nil {
		t.Fatalf("ContainerStatsOneShot: %v", err)
	}
	if gotQuery.Get("stream") != "false" || gotQuery.Get("one-shot") != "true" {
		t.Fatalf("stats query = %v, want stream=false and one-shot=true", gotQuery)
	}
	// Memory follows docker-CLI semantics: usage minus inactive file cache.
	if stats.MemoryUsageBytes != 134217728-33554432 {
		t.Fatalf("MemoryUsageBytes = %d, want %d", stats.MemoryUsageBytes, 134217728-33554432)
	}
	if stats.CPUTotalUsage != 5000000 || stats.SystemCPUUsage != 100000000 || stats.OnlineCPUs != 4 {
		t.Fatalf("cpu sample = %+v, want total=5000000 system=100000000 cpus=4", stats)
	}
}

func TestContainerStatsOneShot_cgroupV1Fallbacks(t *testing.T) {
	// Given: a daemon shaped like cgroup v1 — total_inactive_file instead of
	// inactive_file, per-CPU usage array instead of online_cpus.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"memory_stats": {"usage": 67108864, "stats": {"total_inactive_file": 16777216}},
			"cpu_stats": {"cpu_usage": {"total_usage": 1, "percpu_usage": [1, 2]}, "system_cpu_usage": 2}
		}`))
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: a stats sample is fetched.
	stats, err := client.ContainerStatsOneShot(context.Background(), "abc123")

	// Then: both v1 field spellings are honoured.
	if err != nil {
		t.Fatalf("ContainerStatsOneShot: %v", err)
	}
	if stats.MemoryUsageBytes != 67108864-16777216 {
		t.Fatalf("MemoryUsageBytes = %d, want %d", stats.MemoryUsageBytes, 67108864-16777216)
	}
	if stats.OnlineCPUs != 2 {
		t.Fatalf("OnlineCPUs = %d, want 2 (from percpu_usage length)", stats.OnlineCPUs)
	}
}

func TestCreateContainer_boundsConcurrentDockerMutations(t *testing.T) {
	// Given: a fake Docker daemon that records concurrent create requests.
	var inFlight int32
	var highWater int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.45/containers/create" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		current := atomic.AddInt32(&inFlight, 1)
		for {
			observed := atomic.LoadInt32(&highWater)
			if current <= observed || atomic.CompareAndSwapInt32(&highWater, observed, current) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		_ = json.NewEncoder(w).Encode(CreateContainerResponse{ID: "container-id"})
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
		sem:        make(chan struct{}, maxConcurrentOps),
	}

	// When: many create requests are issued concurrently.
	const requests = maxConcurrentOps * 4
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.CreateContainer(context.Background(), "", &CreateContainerRequest{ContainerConfig: &ContainerConfig{Image: "test"}})
			if err != nil {
				t.Errorf("CreateContainer: %v", err)
			}
		}()
	}

	for atomic.LoadInt32(&highWater) < maxConcurrentOps {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()

	// Then: Docker mutations are bounded by the explicit operation semaphore.
	if got := atomic.LoadInt32(&highWater); got > maxConcurrentOps {
		t.Fatalf("concurrent create requests = %d, want <= %d", got, maxConcurrentOps)
	}
}

func TestCreateContainer_sendsPlatformQuery(t *testing.T) {
	// Given: a fake Docker daemon that records the create-container query string.
	var gotName string
	var gotPlatform string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.45/containers/create" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotName = r.URL.Query().Get("name")
		gotPlatform = r.URL.Query().Get("platform")
		_ = json.NewEncoder(w).Encode(CreateContainerResponse{ID: "container-id"})
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: a platform-specific container create request is sent.
	_, err := client.CreateContainer(context.Background(), "lambda-demo", &CreateContainerRequest{
		ContainerConfig: &ContainerConfig{Image: "public.ecr.aws/lambda/nodejs:22"},
		Platform:        "linux/amd64",
	})

	// Then: Docker receives platform in the URL query where the Engine API expects it.
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if gotName != "lambda-demo" {
		t.Fatalf("name query = %q, want lambda-demo", gotName)
	}
	if gotPlatform != "linux/amd64" {
		t.Fatalf("platform query = %q, want linux/amd64", gotPlatform)
	}
}

func TestPullImageForPlatform_sendsPlatformQuery(t *testing.T) {
	// Given: a fake Docker daemon that records image pull query parameters.
	var gotImage string
	var gotPlatform string
	var gotTag string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.45/images/create":
			gotImage = r.URL.Query().Get("fromImage")
			gotTag = r.URL.Query().Get("tag")
			gotPlatform = r.URL.Query().Get("platform")
			_, _ = w.Write([]byte(`{"status":"done"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.45/images/prune":
			_, _ = w.Write([]byte(`{"ImagesDeleted":null,"SpaceReclaimed":0}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: an image is pulled for a specific platform.
	err := client.PullImageForPlatform(context.Background(), "public.ecr.aws/lambda/nodejs:22", "linux/amd64")

	// Then: Docker receives platform in the images/create URL query.
	if err != nil {
		t.Fatalf("PullImageForPlatform: %v", err)
	}
	// Docker takes the version in `tag`, never folded into `fromImage` — see
	// splitImageRef and TestPullImage_sendsTagSeparately.
	if gotImage != "public.ecr.aws/lambda/nodejs" {
		t.Fatalf("fromImage query = %q, want public.ecr.aws/lambda/nodejs", gotImage)
	}
	if gotTag != "22" {
		t.Fatalf("tag query = %q, want 22", gotTag)
	}
	if gotPlatform != "linux/amd64" {
		t.Fatalf("platform query = %q, want linux/amd64", gotPlatform)
	}
}

func TestImageMatchesPlatform(t *testing.T) {
	// Given: a local image inspect response for linux/arm64.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1.45/images/") || !strings.HasSuffix(r.URL.Path, "/json") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(ImageInspect{OS: "linux", Architecture: "arm64"})
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When/Then: only the matching platform is reported as present.
	match, err := client.ImageMatchesPlatform(context.Background(), "public.ecr.aws/lambda/nodejs:22", "linux/arm64")
	if err != nil || !match {
		t.Fatalf("ImageMatchesPlatform arm64 = %v, %v; want true, nil", match, err)
	}
	match, err = client.ImageMatchesPlatform(context.Background(), "public.ecr.aws/lambda/nodejs:22", "linux/amd64")
	if err != nil || match {
		t.Fatalf("ImageMatchesPlatform amd64 = %v, %v; want false, nil", match, err)
	}
}

// A daemon error on the logs endpoint must be returned as an error, not handed
// back as if it were log content. The body of a 404 is `{"message":"No such
// container: …"}`, and every caller runs the result through a Docker
// multiplex-frame stripper: the stripper eats the first 8 bytes as a frame
// header, reads `ssag` as a ~1.9 GB frame length, and emits the remainder. The
// RDS logs pane showed users `e":"No such container: 181c85f6…"}` because of
// this — an error message mangled into something that looked like corruption.
func TestContainerLogs_daemonErrorIsReturned(t *testing.T) {
	// Given: a daemon that answers the logs endpoint with 404 and an error body.
	const body = `{"message":"No such container: 181c85f63af6"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/logs") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: logs are fetched for a container the daemon does not have.
	logs, err := client.ContainerLogs(context.Background(), "181c85f63af6", "200")

	// Then: the caller gets an error naming the cause, and no bytes it could
	// mistake for log output.
	if err == nil {
		t.Fatalf("ContainerLogs: expected an error for a 404 response, got logs %q", logs)
	}
	if len(logs) != 0 {
		t.Errorf("ContainerLogs returned %d bytes alongside the error, want none: %q", len(logs), logs)
	}
	if !strings.Contains(err.Error(), "No such container") {
		t.Errorf("ContainerLogs error = %v, want it to carry the daemon's message", err)
	}
}

func TestContainerLogs_returnsBodyOnSuccess(t *testing.T) {
	// Given: a daemon that answers the logs endpoint with a multiplexed frame.
	frame := append([]byte{1, 0, 0, 0, 0, 0, 0, 5}, []byte("hello")...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(frame)
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), host: server.URL, logger: zap.NewNop(), sem: make(chan struct{}, maxConcurrentOps)}

	// When: logs are fetched.
	logs, err := client.ContainerLogs(context.Background(), "abc", "200")

	// Then: the raw stream is returned untouched for the caller to de-frame.
	if err != nil {
		t.Fatalf("ContainerLogs: %v", err)
	}
	if string(logs) != string(frame) {
		t.Errorf("ContainerLogs = %q, want %q", logs, frame)
	}
}

func TestContainerLogsStream_doesNotStarveCreateContainer(t *testing.T) {
	// Given: enough held log-follow streams to consume the old Docker transport limit.
	logStreams := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("follow") == "true":
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-logStreams
		case r.Method == http.MethodPost && r.URL.Path == "/v1.45/containers/create":
			_ = json.NewEncoder(w).Encode(CreateContainerResponse{ID: "container-id"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient("tcp://"+server.Listener.Addr().String(), zap.NewNop())

	var streams []interface{ Close() error }
	for i := 0; i < maxConcurrentOps; i++ {
		stream, err := client.ContainerLogsStream(context.Background(), "id", time.Time{})
		if err != nil {
			t.Fatalf("ContainerLogsStream %d: %v", i, err)
		}
		streams = append(streams, stream)
	}
	defer func() {
		close(logStreams)
		for _, stream := range streams {
			_ = stream.Close()
		}
	}()

	// When: a create request is sent while streams are still open.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := client.CreateContainer(ctx, "", &CreateContainerRequest{ContainerConfig: &ContainerConfig{Image: "test"}})

	// Then: the request succeeds instead of waiting behind long-lived log streams.
	if err != nil {
		t.Fatalf("CreateContainer while log streams are open: %v", err)
	}
}

// TestPullImage_sendsTagSeparately guards the wire shape of the pull request.
// Docker takes the version in `tag`, not folded into `fromImage`: sending a
// digest-pinned reference whole makes the daemon report a successful pull and
// store nothing under that digest, so the next container create fails with
// "No such image".
func TestPullImage_sendsTagSeparately(t *testing.T) {
	tests := []struct {
		name         string
		image        string
		wantFrom     string
		wantTag      string
		wantPlatform string
	}{
		{
			name:     "digest pin",
			image:    "registry.k8s.io/sig-storage/nfs-provisioner@sha256:c825f3d5",
			wantFrom: "registry.k8s.io/sig-storage/nfs-provisioner",
			wantTag:  "sha256:c825f3d5",
		},
		{
			name:     "plain tag",
			image:    "busybox:1.36",
			wantFrom: "busybox",
			wantTag:  "1.36",
		},
		{
			name:     "registry port is not a tag",
			image:    "localhost:5000/team/app",
			wantFrom: "localhost:5000/team/app",
			wantTag:  "",
		},
		{
			name:     "registry port with tag",
			image:    "localhost:5000/team/app:v2",
			wantFrom: "localhost:5000/team/app",
			wantTag:  "v2",
		},
		{
			name:     "no version",
			image:    "alpine",
			wantFrom: "alpine",
			wantTag:  "",
		},
		{
			name:         "platform is preserved",
			image:        "alpine:3.20",
			wantFrom:     "alpine",
			wantTag:      "3.20",
			wantPlatform: "linux/amd64",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/images/create") {
					gotQuery = r.URL.Query()
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusOK) // prune after pull
			}))
			defer srv.Close()

			c := NewClient("tcp://"+srv.Listener.Addr().String(), zap.NewNop())
			if err := c.PullImageForPlatform(context.Background(), tc.image, tc.wantPlatform); err != nil {
				t.Fatalf("PullImageForPlatform: %v", err)
			}
			if got := gotQuery.Get("fromImage"); got != tc.wantFrom {
				t.Errorf("fromImage: want %q, got %q", tc.wantFrom, got)
			}
			if got := gotQuery.Get("tag"); got != tc.wantTag {
				t.Errorf("tag: want %q, got %q", tc.wantTag, got)
			}
			if got := gotQuery.Get("platform"); got != tc.wantPlatform {
				t.Errorf("platform: want %q, got %q", tc.wantPlatform, got)
			}
		})
	}
}

// TestPullImage_doesNotPrune guards against reintroducing the prune-after-pull
// that made digest-pinned images unusable. "Dangling" means untagged, and an
// image pulled by digest has no tag, so pruning after a pull deletes exactly
// what was just fetched — the pull reports success and the next container
// create fails with "No such image".
func TestPullImage_doesNotPrune(t *testing.T) {
	var pruned atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/images/prune") {
			pruned.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient("tcp://"+srv.Listener.Addr().String(), zap.NewNop())
	if err := c.PullImage(context.Background(),
		"registry.k8s.io/sig-storage/nfs-provisioner@sha256:c825f3d5"); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	if pruned.Load() {
		t.Fatal("PullImage pruned dangling images; that deletes digest-pinned images it just pulled")
	}
}

// ── Exec ────────────────────────────────────────────────────────────────────

// execDaemon answers the three endpoints an exec runs through, recording the
// create body and reporting the exit status the test asked for.
type execDaemon struct {
	srv      *httptest.Server
	exitCode int
	running  bool
	output   string

	mu      sync.Mutex
	created map[string]any
}

func newExecDaemon(t *testing.T) *execDaemon {
	t.Helper()
	d := &execDaemon{}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode exec create: %v", err)
			}
			d.mu.Lock()
			d.created = body
			d.mu.Unlock()
			_, _ = w.Write([]byte(`{"Id":"exec-abc"}`))
		case strings.HasSuffix(r.URL.Path, "/exec/exec-abc/start"):
			_, _ = w.Write([]byte(d.output))
		case strings.HasSuffix(r.URL.Path, "/exec/exec-abc/json"):
			_, _ = w.Write([]byte(`{"Running":` + strconv.FormatBool(d.running) +
				`,"ExitCode":` + strconv.Itoa(d.exitCode) + `}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *execDaemon) client() *Client {
	return NewClient("tcp://"+d.srv.Listener.Addr().String(), zap.NewNop())
}

func (d *execDaemon) createBody() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.created
}

func TestExec_sendsCommandAndReturnsOutput(t *testing.T) {
	// Given: a daemon whose exec succeeds and writes to its stream.
	d := newExecDaemon(t)
	d.output = "ok\r\n"

	// When: a command with an argument containing spaces and quotes is run.
	const stmt = `ALTER USER 'a b'@'%' IDENTIFIED BY 'p''w';`
	res, err := d.client().Exec(context.Background(), "cafe",
		[]string{"mysql", "-e", stmt}, []string{"PGPASSWORD=old"})

	// Then: the exit status and output come back, and the argument reached the
	// daemon as one unmangled element — there is no shell to word-split it.
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Output != "ok\r\n" {
		t.Errorf("Output = %q, want the command's own bytes", res.Output)
	}

	body := d.createBody()
	cmd, _ := body["Cmd"].([]any)
	if len(cmd) != 3 || cmd[2] != stmt {
		t.Errorf("Cmd = %v, want the statement passed through verbatim", cmd)
	}
	if env, _ := body["Env"].([]any); len(env) != 1 || env[0] != "PGPASSWORD=old" {
		t.Errorf("Env = %v, want the caller's environment", body["Env"])
	}
	// A TTY is what makes the stream plain bytes rather than Docker's framed
	// multiplexing, which is the only reason Output can be read directly.
	if tty, _ := body["Tty"].(bool); !tty {
		t.Error("exec was created without a TTY; its output stream is multiplexed")
	}
}

// A command that ran and refused is an answer, not a transport failure — the
// caller has to be able to tell it from a command that never ran.
func TestExec_nonZeroExitIsReportedNotErrored(t *testing.T) {
	d := newExecDaemon(t)
	d.exitCode = 1
	d.output = "ERROR 1064 (42000): You have an error in your SQL syntax"

	res, err := d.client().Exec(context.Background(), "cafe", []string{"mysql"}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", res.ExitCode)
	}
	if !strings.Contains(res.Output, "1064") {
		t.Errorf("Output = %q, want the command's own explanation", res.Output)
	}
}

// The zero ExitCode of a command the daemon still calls running is not a
// success, and must not be returned as one.
func TestExec_stillRunningIsAnError(t *testing.T) {
	d := newExecDaemon(t)
	d.running = true

	if _, err := d.client().Exec(context.Background(), "cafe", []string{"mysql"}, nil); err == nil {
		t.Fatal("Exec reported success for a command the daemon still calls running")
	}
}

func TestExec_daemonErrorIsReturned(t *testing.T) {
	// Given: a daemon that refuses to exec into a container that is not running.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/exec") {
			t.Errorf("unexpected request after a failed exec create: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Container cafe is not running"}`))
	}))
	defer srv.Close()
	c := NewClient("tcp://"+srv.Listener.Addr().String(), zap.NewNop())

	_, err := c.Exec(context.Background(), "cafe", []string{"mysql"}, nil)
	if err == nil {
		t.Fatal("Exec: expected an error when the daemon refuses to create the exec")
	}
	if !strings.Contains(err.Error(), "is not running") {
		t.Errorf("Exec error = %v, want it to carry the daemon's message", err)
	}
}

func TestWaitContainerRemoved_blocksUntilDaemonConfirmsRemoval(t *testing.T) {
	// Given: a daemon that holds the wait request open — simulating a
	// container still unmounting layers in state "removing" — until a signal
	// says the removal actually finished.
	release := make(chan struct{})
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"StatusCode":0}`))
	}))
	defer srv.Close()
	c := NewClient("tcp://"+srv.Listener.Addr().String(), zap.NewNop())

	done := make(chan error, 1)
	go func() {
		done <- c.WaitContainerRemoved(context.Background(), "cafe123")
	}()

	// Then: the call has not returned while the daemon is still working.
	select {
	case err := <-done:
		t.Fatalf("WaitContainerRemoved returned (err=%v) before the daemon signalled removal", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitContainerRemoved: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitContainerRemoved did not return after the daemon signalled removal")
	}
	if !strings.Contains(gotPath, "/wait") || !strings.Contains(gotPath, "condition=removed") {
		t.Errorf("WaitContainerRemoved requested %q, want the wait endpoint with condition=removed", gotPath)
	}
}

func TestWaitContainerRemoved_alreadyGoneIsSuccess(t *testing.T) {
	// Given: a daemon that has no record of the container any more — it
	// finished removing before this call was even made.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: gone123"}`))
	}))
	defer srv.Close()
	c := NewClient("tcp://"+srv.Listener.Addr().String(), zap.NewNop())

	// Then: the already-satisfied condition is not an error.
	if err := c.WaitContainerRemoved(context.Background(), "gone123"); err != nil {
		t.Fatalf("WaitContainerRemoved: %v, want nil for an already-removed container", err)
	}
}

func TestWaitContainerRemoved_daemonErrorIsReturned(t *testing.T) {
	// Given: a daemon that reports a real failure, not a 404.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"driver failed to remove root filesystem"}`))
	}))
	defer srv.Close()
	c := NewClient("tcp://"+srv.Listener.Addr().String(), zap.NewNop())

	err := c.WaitContainerRemoved(context.Background(), "cafe123")
	if err == nil {
		t.Fatal("WaitContainerRemoved: expected an error for a daemon failure")
	}
	if !strings.Contains(err.Error(), "driver failed to remove root filesystem") {
		t.Errorf("WaitContainerRemoved error = %v, want it to carry the daemon's message", err)
	}
}

// Transport is what a caller outside this package reaches for when it needs an
// Engine API endpoint Client does not implement, so it has to dial exactly as
// Client does — including on Windows, where an endpoint is a named pipe and a
// hand-rolled `unix` dial fails with "An invalid argument was supplied" (see
// Transport's comment and #1785). Asserting the two agree, endpoint by
// endpoint, is what stops a second dialer growing back by drift.
func TestTransport_dialsAsNewClientDoes(t *testing.T) {
	// The platform default is deliberately included by name for both
	// platforms: on each of them one of these is the shape that matters and
	// the other exercises the fall-through, and neither must be special-cased
	// by the caller.
	for _, endpoint := range []string{
		"/var/run/docker.sock",
		`npipe:////./pipe/docker_engine`,
		"tcp://dind:2375",
		"",
	} {
		t.Run(endpoint, func(t *testing.T) {
			transport, host := Transport(endpoint)
			client := NewClient(endpoint, zap.NewNop())
			if host != client.host {
				t.Errorf("Transport host = %q, NewClient host = %q", host, client.host)
			}
			clientTransport, ok := client.httpClient.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("NewClient transport is %T, not *http.Transport", client.httpClient.Transport)
			}
			if (transport.DialContext == nil) != (clientTransport.DialContext == nil) {
				t.Errorf("Transport dial func set = %v, NewClient dial func set = %v",
					transport.DialContext != nil, clientTransport.DialContext != nil)
			}
			if transport.ResponseHeaderTimeout != clientTransport.ResponseHeaderTimeout {
				t.Errorf("Transport ResponseHeaderTimeout = %v, NewClient = %v",
					transport.ResponseHeaderTimeout, clientTransport.ResponseHeaderTimeout)
			}
		})
	}
}

// Each call owns its transport: a caller that adjusts one — the image build
// streams for minutes and wants no response-header deadline — must not be
// changing the timeouts of every Docker client in the process.
func TestTransport_isNotShared(t *testing.T) {
	first, _ := Transport("/var/run/docker.sock")
	second, _ := Transport("/var/run/docker.sock")
	if first == second {
		t.Fatal("Transport returned the same *http.Transport twice")
	}
	first.ResponseHeaderTimeout = 0
	if second.ResponseHeaderTimeout == 0 {
		t.Fatal("adjusting one transport changed another")
	}
}
