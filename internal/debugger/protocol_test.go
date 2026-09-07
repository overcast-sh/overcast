package debugger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInject_appendsOnceWithOneSpace(t *testing.T) {
	tests := []struct {
		name     string
		protocol Protocol
		key      string
		before   map[string]string
		want     string
	}{
		{
			name: "inspector fresh", protocol: inspector{}, key: nodeOptionsEnv,
			before: map[string]string{},
			want:   "--inspect=0.0.0.0:9229",
		},
		{
			name: "inspector appends to user options", protocol: inspector{}, key: nodeOptionsEnv,
			before: map[string]string{nodeOptionsEnv: "--max-old-space-size=512 "},
			want:   "--max-old-space-size=512 --inspect=0.0.0.0:9229",
		},
		{
			name: "inspector already present", protocol: inspector{}, key: nodeOptionsEnv,
			before: map[string]string{nodeOptionsEnv: "--inspect-brk=9230"},
			want:   "--inspect-brk=9230",
		},
		{
			name: "jdwp fresh", protocol: jdwp{}, key: javaToolOptionsEnv,
			before: map[string]string{},
			want:   "-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:9229",
		},
		{
			name: "jdwp appends", protocol: jdwp{}, key: javaToolOptionsEnv,
			before: map[string]string{javaToolOptionsEnv: "-Xmx256m"},
			want:   "-Xmx256m -agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:9229",
		},
		{
			name: "jdwp already present", protocol: jdwp{}, key: javaToolOptionsEnv,
			before: map[string]string{javaToolOptionsEnv: "-Xrunjdwp:transport=dt_socket,address=5005"},
			want:   "-Xrunjdwp:transport=dt_socket,address=5005",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a container environment
			env := tc.before

			// When: the flag is injected twice
			tc.protocol.Inject(env, 9229)
			tc.protocol.Inject(env, 9229)

			// Then: it is present exactly once, appended with one space
			assert.Equal(t, tc.want, env[tc.key])
		})
	}
}

func TestInject_dapAndPassthroughLeaveEnvAlone(t *testing.T) {
	for _, p := range []Protocol{dap{}, passthrough{}} {
		// Given: an environment
		env := map[string]string{"A": "1"}

		// When: the protocol injects
		p.Inject(env, 9229)

		// Then: nothing changed — the port arrives through OVERCAST_DEBUG_PORT
		assert.Equal(t, map[string]string{"A": "1"}, env, p.Name())
	}
}

func TestDetect_inspectorSpellings(t *testing.T) {
	tests := []struct {
		options  string
		wantPort int
		wantOK   bool
	}{
		{"", 0, false},
		{"--max-old-space-size=512", 0, false},
		{"--inspect", 9229, true},
		{"--inspect=9230", 9230, true},
		{"--inspect=0.0.0.0:9231", 9231, true},
		{"--inspect=localhost:9232", 9232, true},
		{"--inspect=[::]:9233", 9233, true},
		{"--inspect-brk", 9229, true},
		{"--inspect-brk=9234", 9234, true},
		{"--inspect-wait=0.0.0.0:9235", 9235, true},
		{"--inspect --inspect-port=9236", 9236, true},
		{"--inspect-port=9237", 0, false},
		{"--require ./trace.js --inspect=9238 --no-warnings", 9238, true},
		{"--inspect=notaport", 9229, true},
	}
	for _, tc := range tests {
		t.Run(tc.options, func(t *testing.T) {
			// Given: NODE_OPTIONS as a user might write it
			env := map[string]string{nodeOptionsEnv: tc.options}

			// When: the inspector looks for its flag
			port, ok := inspector{}.Detect(env)

			// Then: every activating spelling is found with its port
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantPort, port)
		})
	}
}

func TestDetect_jdwpSpellings(t *testing.T) {
	tests := []struct {
		options  string
		wantPort int
		wantOK   bool
	}{
		{"", 0, false},
		{"-Xmx256m", 0, false},
		{"-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=5005", 5005, true},
		{"-agentlib:jdwp=transport=dt_socket,server=y,suspend=y,address=*:5006", 5006, true},
		{"-agentlib:jdwp=transport=dt_socket,server=y,address=0.0.0.0:5007", 5007, true},
		{"-Xrunjdwp:transport=dt_socket,server=y,address=5008", 5008, true},
		{"-agentpath:/opt/libjdwp.so=transport=dt_socket,server=y,address=5009", 5009, true},
		{"-Xmx256m -agentlib:jdwp=transport=dt_socket,server=y,address=5010 -Dfoo=bar", 5010, true},
		{"-agentlib:jdwp=transport=dt_socket,server=n,address=host:5011", 0, false},
		{"-agentlib:jdwp=transport=dt_socket,server=y", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.options, func(t *testing.T) {
			// Given: JAVA_TOOL_OPTIONS as a user might write it
			env := map[string]string{javaToolOptionsEnv: tc.options}

			// When: jdwp looks for its agent
			port, ok := jdwp{}.Detect(env)

			// Then: a listening agent's port is found; a dialling one is not
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantPort, port)
		})
	}
}

func TestResolve_order(t *testing.T) {
	tests := []struct {
		name       string
		spec       Spec
		runtime    string
		env        map[string]string
		wantProto  string
		wantPort   int
		wantSource Source
	}{
		{
			name: "tag beats runtime", spec: Spec{Tagged: true, Protocol: "jdwp"}, runtime: "nodejs20.x",
			wantProto: "jdwp", wantSource: SourceTag,
		},
		{
			name: "tag keeps its fixed port", spec: Spec{Tagged: true, Protocol: "dap", Port: 5678},
			wantProto: "dap", wantPort: 5678, wantSource: SourceTag,
		},
		{
			name: "runtime prefix", spec: Spec{Tagged: true}, runtime: "python3.12",
			wantProto: "dap", wantSource: SourceRuntime,
		},
		{
			name: "runtime is case-insensitive", spec: Spec{Tagged: true}, runtime: "Java21",
			wantProto: "jdwp", wantSource: SourceRuntime,
		},
		{
			name: "runtime match takes the port from a user flag", spec: Spec{Tagged: true, Port: 9229}, runtime: "nodejs22.x",
			env:       map[string]string{nodeOptionsEnv: "--inspect=9333"},
			wantProto: "inspector", wantPort: 9333, wantSource: SourceRuntime,
		},
		{
			name: "env detection without a tag", spec: Spec{}, runtime: "provided.al2023",
			env:       map[string]string{javaToolOptionsEnv: "-agentlib:jdwp=transport=dt_socket,server=y,address=5005"},
			wantProto: "jdwp", wantPort: 5005, wantSource: SourceEnv,
		},
		{
			name: "tagged image falls back to passthrough", spec: Spec{Tagged: true, Port: 4000}, runtime: "",
			wantProto: "passthrough", wantPort: 4000, wantSource: SourceFallback,
		},
		{
			name: "tagged custom runtime falls back to passthrough", spec: Spec{Tagged: true}, runtime: "provided.al2",
			wantProto: "passthrough", wantSource: SourceFallback,
		},
		{
			name: "untagged and nothing detected is no target", spec: Spec{}, runtime: "nodejs20.x",
			wantProto: "", wantSource: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a spec, runtime and environment
			// When: the default registry resolves them
			res := Default.Resolve(tc.spec, tc.runtime, tc.env)

			// Then: the protocol, port and source follow the fixed order
			if tc.wantProto == "" {
				assert.Nil(t, res.Protocol)
				return
			}
			require.NotNil(t, res.Protocol)
			assert.Equal(t, tc.wantProto, res.Protocol.Name())
			assert.Equal(t, tc.wantPort, res.Port)
			assert.Equal(t, tc.wantSource, res.Source)
		})
	}
}

func TestResolve_untaggedNodeRuntimeIsNoTarget(t *testing.T) {
	// Given: an untagged Node function with a clean environment — runtime
	// matching alone must not create a target, or every function would get one
	// When: resolved
	res := Default.Resolve(Spec{}, "nodejs20.x", nil)

	// Then: the runtime step is skipped for the untagged and there is nothing
	assert.Nil(t, res.Protocol)
}

func TestRegistry_namesAndLookup(t *testing.T) {
	// Given: the default registry
	// When: names are listed and looked up
	names := Default.Names()
	_, ok := Default.Lookup("inspector")
	_, missing := Default.Lookup("gdb")

	// Then: passthrough is last so it never shadows a detection
	assert.Equal(t, []string{"inspector", "jdwp", "dap", "passthrough"}, names)
	assert.True(t, ok)
	assert.False(t, missing)
}
