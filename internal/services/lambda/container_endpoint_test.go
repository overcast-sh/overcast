package lambda

import (
	"slices"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
)

// container_endpoint_test.go covers the Lambda runtime's use of
// internal/containerendpoint. The mechanisms themselves — which origins get
// rewritten, which hostnames get /etc/hosts entries — are that package's own
// tests; these pin that the container runtime actually routes function
// environment and container creation through it.
//
// Background: AWS SDKs resolve the SQS endpoint from the QueueUrl rather than
// from client configuration, and AWS_ENDPOINT_URL does not override that. So a
// queue URL minted on the host and handed to a function through its environment
// would otherwise send the function's SQS client to its own container.

func newTestRuntime(cfg *config.Config, endpoint string) *ContainerRuntime {
	return &ContainerRuntime{
		cfg:              cfg,
		overcastEndpoint: endpoint,
		endpoint:         containerendpoint.New(cfg, endpoint),
	}
}

func TestBuildEnv_rewritesHostMintedURLs(t *testing.T) {
	// Given: a function whose environment carries URLs minted by a host-side
	// deploy (CDK/CloudFormation writing queue.queueUrl into function env).
	runtime := newTestRuntime(&config.Config{Region: "us-east-1", AccountID: "000000000000", Port: 4566}, "http://172.18.0.1:4566")
	fn := &Function{
		Name: "demo", Runtime: "nodejs22.x", Handler: "index.handler",
		MemorySize: 128, Timeout: 3,
		Environment: map[string]string{
			"QUEUE_URL":     "http://localhost:4566/000000000000/orders",
			"DLQ_URL":       "http://127.0.0.1:4566/000000000000/orders-dlq",
			"UPSTREAM_URL":  "https://api.example.com/v1",
			"LOCAL_DEV_URL": "http://localhost:3000/callback",
		},
	}

	// When: the container environment is built.
	env := envMap(runtime.buildEnv(fn, "stream", initTypeOnDemand, "172.18.0.1:41001", nil))

	// Then: Overcast loopback origins are re-pointed at the container-reachable
	// endpoint, and everything else is left as the user set it.
	for name, want := range map[string]string{
		"QUEUE_URL":     "http://172.18.0.1:4566/000000000000/orders",
		"DLQ_URL":       "http://172.18.0.1:4566/000000000000/orders-dlq",
		"UPSTREAM_URL":  "https://api.example.com/v1",
		"LOCAL_DEV_URL": "http://localhost:3000/callback",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

func TestBuildEnv_leavesSplitHorizonHostsAlone(t *testing.T) {
	// Given: a queue URL minted on a split-horizon hostname, which containers
	// resolve to Overcast via their /etc/hosts entries.
	runtime := newTestRuntime(&config.Config{Region: "us-east-1", AccountID: "000000000000", Port: 4566}, "http://172.18.0.1:4566")
	fn := &Function{
		Name: "demo", Runtime: "nodejs22.x", Handler: "index.handler",
		MemorySize: 128, Timeout: 3,
		Environment: map[string]string{
			"QUEUE_URL": "http://localhost.overcast.sh:4566/000000000000/orders",
		},
	}

	// When: the container environment is built.
	env := envMap(runtime.buildEnv(fn, "stream", initTypeOnDemand, "172.18.0.1:41001", nil))

	// Then: the URL is preserved — it already works on both sides of the boundary.
	want := "http://localhost.overcast.sh:4566/000000000000/orders"
	if env["QUEUE_URL"] != want {
		t.Errorf("QUEUE_URL = %q, want %q", env["QUEUE_URL"], want)
	}
}

func TestContainerRuntimeExtraHosts_mapsSplitHorizonNamesToOvercast(t *testing.T) {
	// Given: a runtime whose containers reach Overcast by IP on the Lambda network.
	runtime := newTestRuntime(&config.Config{Port: 4566}, "http://172.18.0.1:4566")

	// When: the entries handed to Docker at container creation are computed.
	got := runtime.extraHosts()

	// Then: split-horizon names shadow their public 127.0.0.1 record with
	// Overcast's address, so one URL is dialable from host and container alike.
	if !slices.Contains(got, "localhost.overcast.sh:172.18.0.1") {
		t.Errorf("extraHosts() = %v, missing localhost.overcast.sh:172.18.0.1", got)
	}

	// And: nothing claims localhost, which the container needs for its own loopback.
	if slices.Contains(got, "localhost:172.18.0.1") {
		t.Errorf("extraHosts() = %v, must not remap localhost", got)
	}
}

func TestContainerRuntimeExtraHosts_pointsSandboxLocaldomainAtTheSandboxItself(t *testing.T) {
	// Given: the same runtime.
	runtime := newTestRuntime(&config.Config{Port: 4566}, "http://172.18.0.1:4566")

	// When: the entries handed to Docker at container creation are computed.
	got := runtime.extraHosts()

	// Then: the name AWS documents every telemetry destination under resolves
	// to the execution environment's own loopback, as it does on AWS — so an
	// extension that subscribes http://sandbox.localdomain:<port> is delivered
	// to rather than dropped (#1837).
	if !slices.Contains(got, "sandbox.localdomain:127.0.0.1") {
		t.Errorf("extraHosts() = %v, missing sandbox.localdomain:127.0.0.1", got)
	}

	// And: it is Lambda's alone. containerendpoint.Mapper.ExtraHosts is shared
	// with ECS, which has no sandbox and no use for a name pointing at the
	// container instead of at Overcast.
	if shared := runtime.endpoint.ExtraHosts(); slices.ContainsFunc(shared, func(e string) bool {
		return strings.HasPrefix(e, "sandbox.localdomain:")
	}) {
		t.Errorf("containerendpoint.Mapper.ExtraHosts() = %v, must not carry the Lambda-only name", shared)
	}
}

func TestContainerRuntimeExtraHosts_pinsTheSandboxNameEvenWithNoOvercastEndpoint(t *testing.T) {
	// Given: a runtime that could not resolve a container-reachable address for
	// Overcast, so the mapper is inert and produces no entries at all.
	runtime := newTestRuntime(&config.Config{Port: 4566}, "")
	if shared := runtime.endpoint.ExtraHosts(); len(shared) != 0 {
		t.Fatalf("ExtraHosts() = %v, want none from an inert mapper", shared)
	}

	// When/Then: the sandbox still resolves its own name. Nothing about it
	// depends on where Overcast is, and a container that starts without an
	// endpoint still runs a function whose extensions can subscribe.
	if got := runtime.extraHosts(); !slices.Contains(got, "sandbox.localdomain:127.0.0.1") {
		t.Errorf("extraHosts() = %v, missing sandbox.localdomain:127.0.0.1", got)
	}
}
