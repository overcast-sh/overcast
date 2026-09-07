package debugger

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func problemKeys(problems []Problem) []string {
	keys := make([]string, 0, len(problems))
	for _, p := range problems {
		keys = append(keys, p.Key)
	}
	return keys
}

func TestSpecFromTags_tagForms(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want Spec
	}{
		{
			name: "no tags",
			tags: nil,
			want: Spec{Service: ServiceLambda, FlagOn: true},
		},
		{
			name: "unrelated tags only",
			tags: map[string]string{"team": "payments", "overcast:hot-reload-pathology": "x"},
			want: Spec{Service: ServiceLambda, FlagOn: true},
		},
		{
			name: "debug true",
			tags: map[string]string{TagDebug: "true"},
			want: Spec{Service: ServiceLambda, FlagOn: true, Tagged: true},
		},
		{
			name: "debug spelled loosely",
			tags: map[string]string{TagDebug: " Yes "},
			want: Spec{Service: ServiceLambda, FlagOn: true, Tagged: true},
		},
		{
			name: "debug false",
			tags: map[string]string{TagDebug: "false"},
			want: Spec{Service: ServiceLambda, FlagOn: true},
		},
		{
			name: "port implies debug",
			tags: map[string]string{TagPort: "9230"},
			want: Spec{Service: ServiceLambda, FlagOn: true, Tagged: true, Port: 9230},
		},
		{
			name: "protocol override",
			tags: map[string]string{TagDebug: "true", TagProtocol: " JDWP "},
			want: Spec{Service: ServiceLambda, FlagOn: true, Tagged: true, Protocol: "jdwp"},
		},
		{
			name: "source path kept raw and normalised",
			tags: map[string]string{TagDebug: "true", TagSourcePath: `C:\src\app`},
			want: Spec{Service: ServiceLambda, FlagOn: true, Tagged: true, SourcePath: "/c/src/app", SourcePathRaw: `C:\src\app`},
		},
		{
			name: "hot reload path as fallback",
			tags: map[string]string{TagDebug: "true", TagHotReloadPath: "/home/dev/app"},
			want: Spec{Service: ServiceLambda, FlagOn: true, Tagged: true, SourcePath: "/home/dev/app", SourcePathRaw: "/home/dev/app"},
		},
		{
			name: "source path beats hot reload path",
			tags: map[string]string{TagDebug: "true", TagSourcePath: "/src", TagHotReloadPath: "/dist"},
			want: Spec{Service: ServiceLambda, FlagOn: true, Tagged: true, SourcePath: "/src", SourcePathRaw: "/src"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a function's tags with the flag on
			// When: the spec is parsed
			spec, problems := SpecFromTags(ServiceLambda, tc.tags, true)

			// Then: the spec reflects the tags and nothing was refused
			assert.Equal(t, tc.want, spec)
			assert.Empty(t, problems)
		})
	}
}

func TestSpecFromTags_flagOff(t *testing.T) {
	// Given: a tagged function and the Lambda flag off
	// When: the spec is parsed
	spec, problems := SpecFromTags(ServiceLambda, map[string]string{TagPort: "9229"}, false)

	// Then: the spec is tagged but not enabled, and exactly one problem
	// names the flag to set
	assert.True(t, spec.Tagged)
	assert.False(t, spec.FlagOn)
	assert.False(t, spec.Enabled())
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Hint, "OVERCAST_LAMBDA_DEBUGGER=true")
	assert.Contains(t, problems[0].Hint, "OVERCAST_DEBUGGER=true")
}

func TestSpecFromTags_flagOffUntagged(t *testing.T) {
	// Given: an untagged function and the flag off
	// When: the spec is parsed
	spec, problems := SpecFromTags(ServiceLambda, map[string]string{"team": "x"}, false)

	// Then: nothing is reported — the warning is for the user who asked
	assert.False(t, spec.Tagged)
	assert.Empty(t, problems)
}

func TestSpecFromTags_badValues(t *testing.T) {
	tests := []struct {
		name    string
		tags    map[string]string
		wantKey string
		wantHin string
		want    Spec
	}{
		{
			name:    "debug not a boolean",
			tags:    map[string]string{TagDebug: "maybe"},
			wantKey: TagDebug,
			wantHin: "true or false",
			want:    Spec{Service: ServiceLambda, FlagOn: true},
		},
		{
			name:    "port not a number",
			tags:    map[string]string{TagPort: "nine"},
			wantKey: TagPort,
			wantHin: "1 to 65535",
			want:    Spec{Service: ServiceLambda, FlagOn: true},
		},
		{
			name:    "port out of range",
			tags:    map[string]string{TagDebug: "true", TagPort: "70000"},
			wantKey: TagPort,
			wantHin: "1 to 65535",
			want:    Spec{Service: ServiceLambda, FlagOn: true, Tagged: true},
		},
		{
			name:    "unknown protocol",
			tags:    map[string]string{TagDebug: "true", TagProtocol: "gdb"},
			wantKey: TagProtocol,
			wantHin: "inspector, jdwp, dap, passthrough",
			want:    Spec{Service: ServiceLambda, FlagOn: true, Tagged: true},
		},
		{
			name:    "relative source path",
			tags:    map[string]string{TagDebug: "true", TagSourcePath: "src/app"},
			wantKey: TagSourcePath,
			wantHin: "not absolute",
			want:    Spec{Service: ServiceLambda, FlagOn: true, Tagged: true},
		},
		{
			name:    "container suffix on a function",
			tags:    map[string]string{TagDebug + "/app": "true"},
			wantKey: TagDebug + "/app",
			wantHin: "bare key " + TagDebug,
			want:    Spec{Service: ServiceLambda, FlagOn: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a tag with a value the parser cannot honour
			// When: the spec is parsed
			spec, problems := SpecFromTags(ServiceLambda, tc.tags, true)

			// Then: the tag is dropped, the rest applies, and the problem
			// names the key and the fix
			assert.Equal(t, tc.want, spec)
			require.Len(t, problems, 1)
			assert.Equal(t, tc.wantKey, problems[0].Key)
			assert.Contains(t, problems[0].Hint, tc.wantHin)
			assert.NotEmpty(t, problems[0].Fields())
		})
	}
}

func TestSpecFromTags_relativeHotReloadPathIsNotReported(t *testing.T) {
	// Given: a relative hot-reload path, which hot reload itself warns about
	// When: the spec is parsed
	spec, problems := SpecFromTags(ServiceLambda, map[string]string{TagDebug: "true", TagHotReloadPath: "app"}, true)

	// Then: no local root, and no second warning for the same tag
	assert.Empty(t, spec.SourcePathRaw)
	assert.Empty(t, problems)
}

func TestSpecsFromTaskTags_suffixes(t *testing.T) {
	// Given: a two-container task with a suffixed tag per container
	tags := map[string]string{
		TagPort + "/app":        "5005",
		TagDebug + "/worker":    "true",
		TagProtocol + "/worker": "dap",
		TagSourcePath + "/app":  "/home/dev/app",
	}

	// When: specs are resolved for the declared containers
	specs, problems := SpecsFromTaskTags(tags, []string{"app", "worker"}, true)

	// Then: each container has its own spec and nothing was refused
	assert.Empty(t, problems)
	require.Len(t, specs, 2)
	assert.Equal(t, Spec{Service: ServiceECS, Container: "app", FlagOn: true, Tagged: true, Port: 5005,
		SourcePath: "/home/dev/app", SourcePathRaw: "/home/dev/app"}, specs["app"])
	assert.Equal(t, Spec{Service: ServiceECS, Container: "worker", FlagOn: true, Tagged: true, Protocol: "dap"}, specs["worker"])
}

func TestSpecsFromTaskTags_bareKey(t *testing.T) {
	tests := []struct {
		name       string
		containers []string
		tags       map[string]string
		wantSpecs  int
		wantReason string
	}{
		{
			name:       "one container takes the bare key",
			containers: []string{"app"},
			tags:       map[string]string{TagDebug: "true"},
			wantSpecs:  1,
		},
		{
			name:       "suffixed value wins over bare",
			containers: []string{"app"},
			tags:       map[string]string{TagPort: "9229", TagPort + "/app": "9230"},
			wantSpecs:  1,
		},
		{
			name:       "two containers make the bare key ambiguous",
			containers: []string{"app", "sidecar"},
			tags:       map[string]string{TagDebug: "true"},
			wantReason: "ambiguous",
		},
		{
			name:       "no container at all",
			containers: nil,
			tags:       map[string]string{TagDebug: "true"},
			wantReason: "no container",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a task definition and a bare tag
			// When: specs are resolved
			specs, problems := SpecsFromTaskTags(tc.tags, tc.containers, true)

			// Then: the bare key applies only to the single container, and
			// is otherwise refused with a reason
			assert.Len(t, specs, tc.wantSpecs)
			if tc.wantReason == "" {
				assert.Empty(t, problems)
				if s, ok := specs["app"]; ok && tc.tags[TagPort+"/app"] != "" {
					assert.Equal(t, 9230, s.Port)
				}
				return
			}
			require.Len(t, problems, 1, "problems: %v", problemKeys(problems))
			assert.Contains(t, problems[0].Reason, tc.wantReason)
			if len(tc.containers) > 1 {
				assert.Contains(t, problems[0].Hint, TagDebug+"/app")
			}
		})
	}
}

func TestSpecsFromTaskTags_unknownAndEmptyContainer(t *testing.T) {
	// Given: tags naming a container the task does not declare, and one with
	// an empty suffix
	tags := map[string]string{
		TagDebug + "/ghost": "true",
		TagDebug + "/":      "true",
	}

	// When: specs are resolved
	specs, problems := SpecsFromTaskTags(tags, []string{"app"}, true)

	// Then: neither applies and both are explained
	assert.Empty(t, specs)
	require.Len(t, problems, 2)
	for _, p := range problems {
		assert.True(t, strings.HasPrefix(p.Key, TagDebug+"/"))
		assert.NotEmpty(t, p.Hint)
	}
}

func TestSpecsFromTaskTags_flagOff(t *testing.T) {
	// Given: two tagged containers and the ECS flag off
	tags := map[string]string{TagDebug + "/app": "true", TagDebug + "/worker": "true"}

	// When: specs are resolved
	specs, problems := SpecsFromTaskTags(tags, []string{"app", "worker"}, false)

	// Then: both specs exist, disabled, with exactly one flag warning
	require.Len(t, specs, 2)
	assert.False(t, specs["app"].Enabled())
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Hint, "OVERCAST_ECS_DEBUGGER=true")
}

func TestSpecsFromTaskTags_untagged(t *testing.T) {
	// Given: a task with no debugger tag
	// When: specs are resolved
	specs, problems := SpecsFromTaskTags(map[string]string{"env": "dev"}, []string{"app"}, true)

	// Then: nothing is allocated or reported
	assert.Nil(t, specs)
	assert.Nil(t, problems)
}
