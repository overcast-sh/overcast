package debugger

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
)

func TestTargetUpstreamFor_dialsTheContainerDirectlyOrItsPublishedPort(t *testing.T) {
	// Given: a bound target and an inspect naming the published host port
	m := NewManager(clock.NewMock(), nil, "127.0.0.1", freePortRange(t), config.DebuggerTimeoutAttached)
	t.Cleanup(m.Close)
	target, err := m.Ensure("ecs/task-1/app", Spec{Service: ServiceECS, Container: "app", Tagged: true, FlagOn: true},
		Resolution{Protocol: inspector{}, Source: SourceTag})
	require.NoError(t, err)
	require.Equal(t, StateUnbound, target.State(), target.Reason())
	port := strconv.Itoa(target.Port())
	inspect := &docker.ContainerInspect{}
	inspect.NetworkSettings.Ports = map[string][]docker.PortBinding{
		port + "/tcp": {{HostIP: "127.0.0.1", HostPort: "55012"}},
	}

	t.Run("overcast in docker dials the container ip on the debug port", func(t *testing.T) {
		// When: the container's address is routable
		got, ok := target.UpstreamFor("172.18.0.5", nil)

		// Then: the upstream is the container itself, on the same port number
		require.True(t, ok)
		assert.Equal(t, "172.18.0.5:"+port, got)
	})

	t.Run("native overcast dials the published loopback port", func(t *testing.T) {
		// When: the container's address is not routable
		got, ok := target.UpstreamFor("", inspect)

		// Then: the upstream is the host port Docker published
		require.True(t, ok)
		assert.Equal(t, "127.0.0.1:55012", got)
	})

	t.Run("no published port is reported rather than guessed", func(t *testing.T) {
		// When: the inspect carries no binding for the debug port
		_, ok := target.UpstreamFor("", &docker.ContainerInspect{})

		// Then: there is no upstream
		assert.False(t, ok)
	})
}
