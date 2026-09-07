package ecs

import (
	"slices"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
)

// task_endpoint_test.go covers endpoint reachability for ECS task containers.
//
// ECS tasks are siblings of the Overcast container, not children of it, so the
// same problem Lambda containers have applies: AWS SDKs resolve the SQS
// endpoint from the QueueUrl rather than from client configuration, and
// AWS_ENDPOINT_URL does not override that. A task handed a queue URL on
// localhost:4566 sends its SQS client to its own loopback.

func TestBuildContainerEnv_pointsAWSEndpointURLAtAReachableAddress(t *testing.T) {
	// Given: Overcast reachable from the ECS network at a routable IP.
	ep := containerendpoint.New(&config.Config{Port: 4566}, "http://172.18.0.1:4566")
	cd := ContainerDefinition{Name: "app"}

	// When: the container environment is built.
	env := envMap(buildContainerEnv(cd, nil, ep))

	// Then: AWS_ENDPOINT_URL names Overcast on a split-horizon hostname, and
	// that name is pinned to the resolved address in the task's /etc/hosts — so
	// it resolves without depending on any external DNS, which is what
	// host.docker.internal could not promise on native Linux.
	//
	// A name rather than the address because an SDK derives virtual-hosted URLs
	// from the endpoint ({bucket}.s3.{host}); from an IP it cannot.
	const want = "http://localhost.overcast.sh:4566"
	if got := env["AWS_ENDPOINT_URL"]; got != want {
		t.Errorf("AWS_ENDPOINT_URL = %q, want %q", got, want)
	}
	if hosts := ep.ExtraHosts(); !slices.Contains(hosts, "localhost.overcast.sh:172.18.0.1") {
		t.Errorf("ExtraHosts() = %v, want the advertised name pinned to the resolved address", hosts)
	}
}

func TestBuildContainerEnv_rewritesLoopbackOvercastURLs(t *testing.T) {
	// Given: a task definition whose environment carries URLs minted by a
	// host-side deploy (CDK/CloudFormation writing queue.queueUrl into env).
	ep := containerendpoint.New(&config.Config{Port: 4566}, "http://172.18.0.1:4566")
	cd := ContainerDefinition{
		Name: "app",
		Environment: []KeyValuePair{
			{Name: "QUEUE_URL", Value: "http://localhost:4566/000000000000/orders"},
			{Name: "DLQ_URL", Value: "http://127.0.0.1:4566/000000000000/orders-dlq"},
			{Name: "CONFIG_JSON", Value: `{"queue":"http://localhost:4566/000000000000/c"}`},
			{Name: "UPSTREAM_URL", Value: "https://api.example.com/v1"},
			{Name: "LOCAL_DEV_URL", Value: "http://localhost:3000/callback"},
		},
	}

	// When: the container environment is built.
	env := envMap(buildContainerEnv(cd, nil, ep))

	// Then: Overcast loopback origins are re-pointed at the container-reachable
	// endpoint, wherever they appear in the value.
	for name, want := range map[string]string{
		"QUEUE_URL":   "http://172.18.0.1:4566/000000000000/orders",
		"DLQ_URL":     "http://172.18.0.1:4566/000000000000/orders-dlq",
		"CONFIG_JSON": `{"queue":"http://172.18.0.1:4566/000000000000/c"}`,
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}

	// And: URLs that are not Overcast are left alone — a third-party API, and a
	// loopback URL on a port Overcast does not serve.
	if env["UPSTREAM_URL"] != "https://api.example.com/v1" {
		t.Errorf("UPSTREAM_URL = %q, want it untouched", env["UPSTREAM_URL"])
	}
	if env["LOCAL_DEV_URL"] != "http://localhost:3000/callback" {
		t.Errorf("LOCAL_DEV_URL = %q, want it untouched", env["LOCAL_DEV_URL"])
	}
}

func TestBuildContainerEnv_rewritesOverrideURLs(t *testing.T) {
	// Given: a RunTask container override supplying a queue URL, which is how
	// one-off tasks are usually parameterised.
	ep := containerendpoint.New(&config.Config{Port: 4566}, "http://172.18.0.1:4566")
	cd := ContainerDefinition{Name: "app"}
	co := &ContainerOverride{
		Name: "app",
		Environment: []KeyValuePair{
			{Name: "QUEUE_URL", Value: "http://localhost:4566/000000000000/orders"},
		},
	}

	// When: the container environment is built.
	env := envMap(buildContainerEnv(cd, co, ep))

	// Then: overrides get the same treatment as task-definition environment.
	want := "http://172.18.0.1:4566/000000000000/orders"
	if env["QUEUE_URL"] != want {
		t.Errorf("QUEUE_URL = %q, want %q", env["QUEUE_URL"], want)
	}
}

func TestBuildContainerEnv_leavesSplitHorizonHostsAlone(t *testing.T) {
	// Given: a queue URL minted on a split-horizon hostname, which the task
	// container resolves to Overcast via its /etc/hosts entries.
	ep := containerendpoint.New(&config.Config{Port: 4566}, "http://172.18.0.1:4566")
	cd := ContainerDefinition{
		Name: "app",
		Environment: []KeyValuePair{
			{Name: "QUEUE_URL", Value: "http://localhost.overcast.sh:4566/000000000000/orders"},
		},
	}

	// When: the container environment is built.
	env := envMap(buildContainerEnv(cd, nil, ep))

	// Then: the URL is preserved — it already works on both sides of the
	// container boundary.
	want := "http://localhost.overcast.sh:4566/000000000000/orders"
	if env["QUEUE_URL"] != want {
		t.Errorf("QUEUE_URL = %q, want %q", env["QUEUE_URL"], want)
	}
}

func TestBuildContainerEnv_overrideWinsOverTaskDefinition(t *testing.T) {
	// Given: the same variable set in both the task definition and a container
	// override.
	ep := containerendpoint.New(&config.Config{Port: 4566}, "http://172.18.0.1:4566")
	cd := ContainerDefinition{
		Name:        "app",
		Environment: []KeyValuePair{{Name: "STAGE", Value: "from-taskdef"}},
	}
	co := &ContainerOverride{
		Name:        "app",
		Environment: []KeyValuePair{{Name: "STAGE", Value: "from-override"}},
	}

	// When: the container environment is built.
	env := buildContainerEnv(cd, co, ep)

	// Then: the override is applied last, so Docker resolves it as the winner —
	// matching ECS, where container overrides take precedence.
	if got := envMap(env)["STAGE"]; got != "from-override" {
		t.Errorf("STAGE = %q, want %q", got, "from-override")
	}
	if slices.Index(env, "STAGE=from-taskdef") > slices.Index(env, "STAGE=from-override") {
		t.Errorf("env = %v, want the override to come last", env)
	}
}

func TestBuildContainerEnv_withoutAResolvedEndpoint(t *testing.T) {
	// Given: no endpoint mapper (Docker unavailable when the task was built).
	cd := ContainerDefinition{
		Name:        "app",
		Environment: []KeyValuePair{{Name: "QUEUE_URL", Value: "http://localhost:4566/000000000000/orders"}},
	}

	// When: the container environment is built.
	env := envMap(buildContainerEnv(cd, nil, nil))

	// Then: user environment survives untouched and no endpoint is claimed,
	// rather than the task failing to start.
	if env["QUEUE_URL"] != "http://localhost:4566/000000000000/orders" {
		t.Errorf("QUEUE_URL = %q, want it untouched", env["QUEUE_URL"])
	}
	if _, ok := env["AWS_ENDPOINT_URL"]; ok {
		t.Errorf("AWS_ENDPOINT_URL = %q, want it unset", env["AWS_ENDPOINT_URL"])
	}
}
