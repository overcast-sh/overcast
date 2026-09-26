package athena

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
)

// engine_container.go — creating the engine's container, and finding it on
// the network.
//
// Addresses follow docs/dev/container-networking.md. The engine is a
// container calling Overcast (§1): Glue and S3 are reached through the
// engine gateway (engine_gateway.go). Overcast calling the engine is the
// other direction:
// its address on the control plane when Overcast is itself in a container,
// else the port the engine publishes on the host's loopback — loopback only,
// since the engine runs any SQL for anyone who reaches it.

// engineLogTail is how many log lines a failed start reports.
const engineLogTail = "40"

var enginePortKey = strconv.Itoa(enginePort) + "/tcp"

// engineContainer is one started engine container: its ID, the endpoint
// Overcast reaches it on, and the settings it was configured with.
type engineContainer struct {
	id       string
	endpoint string
	settings engineSettings
}

// startContainer creates, configures and starts one engine container. A
// container that was created is returned even with an error, so that it can
// be removed.
func (m *engineManager) startContainer(ctx context.Context) (engineContainer, error) {
	var c engineContainer
	dc := m.docker
	gateway, accessKey, err := m.gateway.open(ctx, dc, m.cfg, m.log.ZapLogger())
	if err != nil {
		return c, fmt.Errorf("reach Overcast from the engine: %w", err)
	}
	overcast := containerendpoint.New(m.cfg, gateway)
	c.settings = engineSettings{
		Overcast:  overcast.Endpoint(),
		AccessKey: accessKey,
		Region:    m.cfg.Region,
		AccountID: m.cfg.AccountID,
		Memory:    m.cfg.AthenaEngineMemory,
	}
	archive, err := engineConfigArchive(renderEngineFiles(c.settings))
	if err != nil {
		return c, err
	}
	if c.id, err = dc.CreateContainer(ctx, "overcast-athena-engine-"+uuid.NewString()[:8], m.containerRequest(ctx, overcast.ExtraHosts())); err != nil {
		return c, fmt.Errorf("create container: %w", err)
	}
	if err := dc.CopyToContainer(ctx, c.id, "/", bytes.NewReader(archive)); err != nil {
		return c, fmt.Errorf("configure container: %w", err)
	}
	if err := dc.StartContainer(ctx, c.id); err != nil {
		return c, fmt.Errorf("start container: %w", err)
	}
	c.endpoint, err = m.containerEndpoint(ctx, c.id)
	return c, err
}

// containerRequest is the engine's container: the image's launcher pointed
// at the rendered configuration, on the control plane, within its memory
// limit. extraHosts make Overcast's name resolve where Docker does not
// synthesise it.
func (m *engineManager) containerRequest(ctx context.Context, extraHosts []string) *docker.CreateContainerRequest {
	return &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image:        m.cfg.AthenaEngineImage,
			Cmd:          []string{"/usr/lib/trino/bin/launcher", "run", "--etc-dir", engineEtcDir},
			ExposedPorts: map[string]struct{}{enginePortKey: {}},
			Labels:       m.instances.ManagedLabels(ctx, serviceName, engineResourceID),
		},
		HostConfig: &docker.HostConfig{
			NetworkMode: dataplane.Primary(m.cfg),
			Memory:      m.cfg.AthenaEngineMemory,
			MemorySwap:  m.cfg.AthenaEngineMemory,
			ExtraHosts:  extraHosts,
			PortBindings: map[string][]docker.PortBinding{
				enginePortKey: {{HostIP: "127.0.0.1"}},
			},
		},
		NetworkingConfig: dataplane.PrimaryEndpoints(m.cfg),
	}
}

// containerEndpoint is the origin Overcast reaches the engine on.
func (m *engineManager) containerEndpoint(ctx context.Context, id string) (string, error) {
	if addr := dataplane.ContainerAddr(ctx, m.docker, m.cfg, id); addr != "" {
		return "http://" + addr + ":" + strconv.Itoa(enginePort), nil
	}
	info, err := m.docker.InspectContainer(ctx, id)
	if err != nil {
		return "", fmt.Errorf("inspect container: %w", err)
	}
	for _, b := range info.NetworkSettings.Ports[enginePortKey] {
		if b.HostPort != "" {
			return "http://127.0.0.1:" + b.HostPort, nil
		}
	}
	return "", fmt.Errorf("container published no port for %s", enginePortKey)
}

// containerExited reports why the container is no longer running, with the
// end of its log, or "" while it still runs.
func (m *engineManager) containerExited(ctx context.Context, id string) string {
	info, err := m.docker.InspectContainer(ctx, id)
	if err != nil {
		if docker.IsNotFound(err) {
			return "the engine container is gone"
		}
		return ""
	}
	if info.State.Running {
		return ""
	}
	reason := fmt.Sprintf("the engine container exited (%s, code %d)", info.State.Status, info.State.ExitCode)
	if info.State.OOMKilled {
		reason += " having run out of memory; raise ATHENA_ENGINE_MEMORY"
	}
	if logs, err := m.docker.ContainerLogs(ctx, id, engineLogTail); err == nil && len(logs) > 0 {
		reason += ": " + strings.TrimSpace(string(docker.DemuxStream(logs)))
	}
	return reason
}
