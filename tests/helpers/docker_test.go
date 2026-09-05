package helpers_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The Docker gate has to agree with the Docker CLI, and for a long time it
// could not: DockerAvailable's predecessor built its client from the empty
// string, which every platform's dialEndpoint reads as a Unix socket path, so
// the ping failed with "dial unix: missing address" whatever the daemon was
// doing. Every test behind helpers.SkipWithoutDocker — ECS service lifecycle,
// the CloudFormation ECS service stacks, and after #1785 the Lambda container
// suite — therefore skipped unconditionally, on Linux CI as much as on a
// Windows workstation, and reported green.
//
// A gate cannot be tested by asking it whether Docker is there; that is the
// question it exists to answer. So this asks a second, independent oracle —
// the `docker` CLI, with its own config, context and socket resolution — and
// requires the two to agree in the direction that matters: where the CLI
// reaches a daemon, the gate must not claim there is none. The reverse is not
// asserted, because a CLI that cannot reach a daemon says nothing about
// LAMBDA_DOCKER_SOCKET, and this test then has no oracle and skips.
func TestDockerAvailable_agreesWithTheDockerCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		t.Skipf("skipping: the docker CLI reaches no daemon on this host, so there is nothing for the gate to agree with: %v: %s",
			err, strings.TrimSpace(string(out)))
	}
	if err := helpers.DockerAvailable(ctx); err != nil {
		t.Fatalf("the docker CLI reaches a daemon (server %s) but the test gate does not, at %s: %v",
			strings.TrimSpace(string(out)), helpers.TestDockerSocket(), err)
	}
}

// The environmental cases below are the verbatim errors GitHub's runners
// produced when Docker Hub became unreachable mid-run (PR #491), which reds
// the ECR and ECS Docker tests without anything in Overcast having changed.
func TestRegistryUnreachable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "hub connect timeout",
			err: errors.New(`pull image alpine:latest: status 500: {"message":"Get ` +
				`\"https://registry-1.docker.io/v2/\": context deadline exceeded"}`),
			want: true,
		},
		{
			name: "hub client timeout awaiting headers",
			err: errors.New(`pull image alpine:latest: status 500: {"message":"Get ` +
				`\"https://registry-1.docker.io/v2/\": net/http: request canceled while ` +
				`waiting for connection (Client.Timeout exceeded while awaiting headers)"}`),
			want: true,
		},
		{
			name: "anonymous pull rate limit",
			err:  errors.New("pull image alpine:latest: toomanyrequests: You have reached your pull rate limit"),
			want: true,
		},
		{
			name: "dns failure",
			err:  errors.New(`pull image registry:2: dial tcp: lookup registry-1.docker.io: no such host`),
			want: true,
		},
		{
			name: "registry down",
			err:  errors.New("pull image registry:2: 503 Service Unavailable"),
			want: true,
		},
		{
			name: "unknown manifest is a real failure",
			err:  errors.New("pull image alpine:nonexistent: manifest unknown"),
			want: false,
		},
		{
			name: "unauthorized is a real failure",
			err:  errors.New("pull image private/thing:1: unauthorized: authentication required"),
			want: false,
		},
		{
			name: "no such image is a real failure",
			err:  errors.New(`create container: 404: {"message":"No such image: repo@sha256:abc"}`),
			want: false,
		},
		{
			name: "an unrecognised error stays a failure",
			err:  errors.New("pull image alpine:latest: something nobody has seen before"),
			want: false,
		},
		{
			name: "nil is not a failure at all",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := helpers.RegistryUnreachable(tc.err); got != tc.want {
				t.Fatalf("RegistryUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
