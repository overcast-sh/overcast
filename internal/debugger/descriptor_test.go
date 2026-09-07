package debugger

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// stripJSONC removes // comment lines so a JSONC launch configuration can be
// checked as JSON.
func stripJSONC(body string) string {
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func TestRenderEditors_everyProtocolEveryEditor(t *testing.T) {
	data := editorData{
		Name: "Overcast: my-fn", Resource: "my-fn", Host: "127.0.0.1", Port: 9229,
		LocalRoot: `C:\src\app`, RemoteRoot: "/var/task",
	}
	wantEditors := map[string][]string{
		inspectorName:   {EditorVSCode, EditorJetBrains, EditorDevTools, EditorCLI},
		jdwpName:        {EditorVSCode, EditorJetBrains, EditorCLI},
		dapName:         {EditorVSCode, EditorJetBrains},
		passthroughName: {EditorVSCode, EditorJetBrains, EditorCLI},
	}
	for _, p := range []Protocol{inspector{}, jdwp{}, dap{}, passthrough{}} {
		t.Run(p.Name(), func(t *testing.T) {
			// Given: a known local root
			// When: every editor template renders
			editors := renderEditors(p, data)

			// Then: each editor is present, labelled, and carries the real
			// host and port; JSON bodies are valid JSONC with the Windows
			// path escaped
			ids := make([]string, 0, len(editors))
			for _, e := range editors {
				ids = append(ids, e.ID)
				assert.NotEmpty(t, e.Label, e.ID)
				if e.Verified {
					// An unverified template is one that does not go
					// through Overcast's port (PyCharm's pydevd).
					assert.Contains(t, e.Body, "9229", e.ID)
					assert.Contains(t, e.Body, "127.0.0.1", e.ID)
				}
				assert.NotContains(t, e.Body, localRootPlaceholder, e.ID)
				assert.NotContains(t, e.Body, "template error", e.ID)
				if e.Kind == KindJSON {
					var cfg map[string]any
					require.NoError(t, json.Unmarshal([]byte(stripJSONC(e.Body)), &cfg), "%s body:\n%s", e.ID, e.Body)
					assert.Equal(t, "attach", cfg["request"])
					assert.Equal(t, "Overcast: my-fn", cfg["name"])
				}
			}
			assert.Equal(t, wantEditors[p.Name()], ids)
		})
	}
}

func TestRenderEditors_unknownLocalRoot(t *testing.T) {
	// Given: no local root
	data := editorData{Name: "Overcast: fn", Resource: "fn", Host: "127.0.0.1", Port: 9229, RemoteRoot: "/var/task"}

	for _, p := range []Protocol{inspector{}, jdwp{}, dap{}} {
		// When: the editors render
		editors := renderEditors(p, data)

		// Then: every body that maps paths keeps the literal placeholder and
		// opens with a line naming the tags, commented for its kind
		for _, e := range editors {
			if !strings.Contains(e.Body, localRootPlaceholder) {
				continue
			}
			first := strings.SplitN(e.Body, "\n", 2)[0]
			assert.True(t, strings.HasPrefix(first, commentPrefix(e.Kind)), "%s/%s: %q", p.Name(), e.ID, first)
			assert.Contains(t, first, TagSourcePath, "%s/%s", p.Name(), e.ID)
			assert.Contains(t, first, TagHotReloadPath, "%s/%s", p.Name(), e.ID)
			if e.Kind == KindJSON {
				var cfg map[string]any
				require.NoError(t, json.Unmarshal([]byte(stripJSONC(e.Body)), &cfg), "%s/%s", p.Name(), e.ID)
			}
		}
	}

	// And: passthrough, which maps nothing, carries no hint
	for _, e := range renderEditors(passthrough{}, data) {
		assert.NotContains(t, e.Body, localRootPlaceholder)
		assert.NotContains(t, e.Body, TagSourcePath+" (")
	}
}

func TestRenderEditors_verifiedFlag(t *testing.T) {
	// Given: the dap protocol, whose JetBrains steps are unverified
	// When: rendered
	editors := renderEditors(dap{}, editorData{Host: "h", Port: 1, LocalRoot: "/l", RemoteRoot: "/r"})

	// Then: the flag comes through per editor
	byID := map[string]bool{}
	for _, e := range editors {
		byID[e.ID] = e.Verified
	}
	assert.True(t, byID[EditorVSCode])
	assert.False(t, byID[EditorJetBrains])
}

func TestRenderEditors_dapCarriesListenSnippet(t *testing.T) {
	// Given: the dap protocol
	// When: VS Code's body renders
	editors := renderEditors(dap{}, editorData{Host: "h", Port: 1, LocalRoot: "/l", RemoteRoot: "/r"})

	// Then: the debugpy.listen snippet reading OVERCAST_DEBUG_PORT precedes
	// the configuration as JSONC comments
	assert.Contains(t, editors[0].Body, "// "+strings.Split(dapListenSnippet, "\n")[0])
	assert.Contains(t, editors[0].Body, "debugpy.listen")
	assert.Contains(t, editors[0].Body, DebugPortEnv)
}

func TestSetupFor(t *testing.T) {
	tests := []struct {
		service Service
		arn     string
		wantCLI string
		wantCDK string
	}{
		{ServiceLambda, "arn:aws:lambda:us-east-1:000000000000:function:fn",
			"aws lambda tag-resource --resource arn:aws:lambda:us-east-1:000000000000:function:fn --tags overcast:debug=true",
			`cdk.Tags.of(fn).add("overcast:debug", "true")`},
		{ServiceLambda, "",
			"aws lambda tag-resource --resource <function-arn> --tags overcast:debug=true",
			`cdk.Tags.of(fn).add("overcast:debug", "true")`},
		{ServiceECS, "arn:aws:ecs:us-east-1:000000000000:task-definition/web:3",
			"aws ecs tag-resource --resource-arn arn:aws:ecs:us-east-1:000000000000:task-definition/web:3 --tags key=overcast:debug,value=true",
			`cdk.Tags.of(taskDefinition).add("overcast:debug", "true")`},
	}
	for _, tc := range tests {
		t.Run(string(tc.service)+"/"+tc.arn, func(t *testing.T) {
			// Given: a service and maybe an ARN
			// When: the setup block renders
			setup := SetupFor(tc.service, tc.arn)

			// Then: the flag, CLI command and CDK line are copy-paste ready
			assert.Equal(t, "OVERCAST_DEBUGGER=true", setup.Flag)
			assert.Equal(t, tc.wantCLI, setup.TagCLI)
			assert.Equal(t, tc.wantCDK, setup.TagCDK)
		})
	}
}

func TestUntaggedDescriptor(t *testing.T) {
	// Given: a function no tag mentions
	// When: an entry is synthesised
	d := UntaggedDescriptor(ServiceLambda, "fn", "", "arn:fn")

	// Then: it is off, says why, has setup, and serialises with every field
	assert.Equal(t, "lambda/fn", d.ID)
	assert.False(t, d.Enabled)
	assert.Equal(t, ReasonNotTagged, d.Reason)
	assert.Equal(t, "inert", d.State)
	assert.Contains(t, d.Setup.TagCLI, "arn:fn")
	b, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"editors":[]`)
	assert.Contains(t, string(b), `"setup":{`)
}

func TestTarget_descriptorShape(t *testing.T) {
	// Given: a bound inspector target with everything set
	mock := clock.NewMock()
	m := newTestManager(t, mock, config.DebuggerTimeoutPaused)
	spec := Spec{Service: ServiceLambda, Tagged: true, FlagOn: true, SourcePath: "/c/src/app", SourcePathRaw: `C:\src\app`}
	tgt, err := m.Ensure("lambda/my-fn", spec, Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)
	tgt.SetUpstream("127.0.0.1:55012")
	tgt.SetContainerID("0a1b")
	tgt.SetRemoteRoot("/var/task")
	tgt.SetResourceARN("arn:fn")

	// When: the descriptor renders and is serialised
	d := tgt.Descriptor()
	b, err := json.Marshal(d)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	// Then: every key of the plan's shape is present with the right value
	for key, want := range map[string]any{
		"id": "lambda/my-fn", "service": "lambda", "resource": "my-fn", "container": "",
		"enabled": true, "reason": "", "protocol": "inspector", "protocolSource": "runtime",
		"state": "listening", "attachedSince": "", "pausedSince": "", "containerId": "0a1b",
		"upstream": "127.0.0.1:55012", "remoteRoot": "/var/task", "localRoot": `C:\src\app`,
		"timeoutPolicy": "paused",
	} {
		assert.Equal(t, want, got[key], key)
	}
	assert.Equal(t, map[string]any{"host": "127.0.0.1", "port": float64(tgt.Port())}, got["listen"])
	setup := got["setup"].(map[string]any)
	assert.Contains(t, setup["tagCli"], "arn:fn")
	assert.Len(t, got["editors"], 4)
	assert.Equal(t, `C:\src\app`, d.LocalRoot)
	assert.Contains(t, d.Editors[0].Body, `"localRoot": "C:\\src\\app"`)
}

func TestDescriptor_wildcardListenHostRendersAsLoopback(t *testing.T) {
	// Given: a manager bound on every interface, as Overcast in Docker is
	m := NewManager(clock.NewMock(), nil, "0.0.0.0", freePortRange(t), config.DebuggerTimeoutAttached)
	t.Cleanup(m.Close)
	target, err := m.Ensure("lambda/fn", Spec{Service: ServiceLambda, Tagged: true, FlagOn: true},
		Resolution{Protocol: inspector{}, Source: SourceRuntime})
	require.NoError(t, err)

	// When: the descriptor is rendered
	d := target.Descriptor()

	// Then: the editor is told to dial loopback, in the listen block and in
	// every template, because the published port is on the developer's machine
	assert.Equal(t, "127.0.0.1", d.Listen.Host)
	for _, e := range d.Editors {
		assert.NotContains(t, e.Body, "0.0.0.0:", e.ID)
	}
}
