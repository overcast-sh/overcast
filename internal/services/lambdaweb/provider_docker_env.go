package lambdaweb

// provider_docker_env.go — the container's environment and its create request.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
)

// envAppLogLevel is how the log level reaches bootstrap.mjs. It is private to
// Overcast: no AWS variable carries a web function's application log level.
const envAppLogLevel = "OVERCAST_WEB_APP_LOG_LEVEL"

// defaultNodeOptions caps the V8 heap at half the environment's memory, as
// the managed runtime does, unless the user sets NODE_OPTIONS themselves.
const defaultNodeOptions = "--max-old-space-size=1024"

// buildEnv assembles the container environment: the user's variables first
// (with URLs that point at Overcast's own host rewritten to the address a
// container reaches it on), then the runtime's, which win - the control plane
// rejects reserved names, and a runtime variable must not be shadowable.
func (p *DockerProvider) buildEnv(spec envSpec, mapper *containerendpoint.Mapper) []string {
	region := spec.Region
	if region == "" {
		region = p.cfg.Region
	}
	env := make(map[string]string, len(spec.Env)+24)
	for k, v := range spec.Env {
		env[k] = mapper.RewriteURLs(v)
	}

	memory := spec.MemoryMB
	if memory <= 0 {
		memory = defaultMemoryMB
	}
	appLevel := spec.AppLogLevel
	if appLevel == "" {
		appLevel = "INFO"
	}

	runtime := map[string]string{
		"AWS_LAMBDA_HTTP_ENDPOINT":        webListenAddr,
		"AWS_REGION":                      region,
		"AWS_DEFAULT_REGION":              region,
		"AWS_ACCOUNT_ID":                  p.cfg.AccountID,
		"AWS_LAMBDA_FUNCTION_NAME":        spec.Function,
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": fmt.Sprint(memory),
		"AWS_LAMBDA_LOG_GROUP_NAME":       spec.LogGroup,
		"AWS_LAMBDA_LOG_STREAM_NAME":      spec.LogStream,
		"LAMBDA_TASK_ROOT":                webTaskDir,
		// Execution-role credentials: the placeholders Lambda hands its
		// functions. Overcast accepts any, and a JS SDK's fromEnv chain wants a
		// session token to treat the process as running on Lambda.
		"AWS_ACCESS_KEY_ID":     "overcast",
		"AWS_SECRET_ACCESS_KEY": "overcast",
		"AWS_SESSION_TOKEN":     "overcast",
		envAppLogLevel:          appLevel,
	}
	if spec.Revision != nil && spec.Revision.BuildConfig.RuntimeConfig.Runtime != "" {
		runtime["AWS_EXECUTION_ENV"] = "AWS_Lambda_" + spec.Revision.BuildConfig.RuntimeConfig.Runtime
	}
	// Back to Overcast, by name where there is one - see
	// containerendpoint.Mapper.ClientEndpoint.
	endpoint := mapper.ClientEndpoint()
	if endpoint == "" {
		endpoint = mapper.Endpoint()
	}
	if endpoint != "" {
		runtime["AWS_ENDPOINT_URL"] = endpoint
		runtime["AWS_ENDPOINT_URL_SSM"] = endpoint
		runtime["AWS_ENDPOINT_URL_SECRETS_MANAGER"] = endpoint
	}
	for k, v := range mapper.CABundleEnv() {
		runtime[k] = v
	}
	for k, v := range runtime {
		env[k] = v
	}

	// Defaults the user may override.
	setDefault := func(k, v string) {
		if _, ok := spec.Env[k]; !ok {
			env[k] = v
		}
	}
	setDefault("NODE_OPTIONS", defaultNodeOptions)
	setDefault("TZ", ":/etc/localtime")
	setDefault("LANG", "en_US.UTF-8")

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// createRequest is the create call for one environment.
func (p *DockerProvider) createRequest(ctx context.Context, spec envSpec, image string, mapper *containerendpoint.Mapper) *docker.CreateContainerRequest {
	memory := spec.MemoryMB
	if memory <= 0 {
		memory = defaultMemoryMB
	}
	bytes := int64(memory) * 1024 * 1024

	labels := p.instanceDomain().ManagedLabels(ctx, dockerService, spec.Function)
	labels[labelRun] = p.runID()

	return &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image: image,
			Env:   p.buildEnv(spec, mapper),
			// No Lambda init and no Runtime API: the bootstrap is the process.
			Entrypoint:   []string{webNodeBinary, webBootstrapPath},
			WorkingDir:   webTaskDir,
			ExposedPorts: map[string]struct{}{webPortKey: {}},
			Labels:       labels,
		},
		HostConfig: &docker.HostConfig{
			NetworkMode: dataplane.Primary(p.cfg),
			Memory:      bytes,
			MemorySwap:  bytes, // equal to Memory: no swap
			NanoCPUs:    webCPUs * 1_000_000_000,
			// The execution environment's documented limits: 512 MB of /tmp, 1,024
			// open files, 1,024 processes and threads (the Lambda quotas page).
			Tmpfs:     map[string]string{"/tmp": "rw,size=512m,mode=1777"},
			PidsLimit: webMaxPids,
			Ulimits:   []docker.Ulimit{{Name: "nofile", Soft: webMaxFiles, Hard: webMaxFiles}},
			// Published on an ephemeral loopback port; read back after the
			// start. Used when Overcast runs on the host, where container
			// addresses are not routable (Docker Desktop).
			PortBindings: map[string][]docker.PortBinding{webPortKey: {{HostIP: "127.0.0.1", HostPort: "0"}}},
			// Split-horizon names resolve to Overcast inside the container; see
			// internal/containerendpoint.
			ExtraHosts: mapper.ExtraHosts(),
			Dns:        mapper.DNSServers(),
		},
	}
}

func sanitizeContainerName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() > 48 {
		return b.String()[:48]
	}
	return b.String()
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
