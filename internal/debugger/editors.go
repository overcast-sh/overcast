package debugger

// editors.go — what a developer pastes into their editor, per protocol per
// editor, as text/template sources. They live here, once, in Go, so the
// console and the CLI render the same text and a protocol's instructions are
// never written twice. descriptor.go renders them.

import (
	"encoding/json"
	"strings"
	"text/template"
)

// EditorTemplate is one editor's instructions for one protocol. Body is a
// text/template rendered with editorData; Kind tells the console how to show
// it and which comment syntax a leading hint line uses.
type EditorTemplate struct {
	ID       string // "vscode" | "jetbrains" | "devtools" | "cli"
	Label    string
	Kind     string // "json" | "steps" | "shell"
	Body     string // template source
	Verified bool   // false renders as a note: the template has not been tried end to end
	tmpl     *template.Template
}

// Editor ids and labels, shared so every protocol names them identically.
const (
	EditorVSCode    = "vscode"
	EditorJetBrains = "jetbrains"
	EditorDevTools  = "devtools"
	EditorCLI       = "cli"
)

// Editor kinds. json is JSONC — VS Code's launch.json accepts // comments,
// which is where a leading hint or a code snippet goes.
const (
	KindJSON  = "json"
	KindSteps = "steps"
	KindShell = "shell"
)

var editorLabels = map[string]string{
	EditorVSCode:    "VS Code",
	EditorJetBrains: "JetBrains",
	EditorDevTools:  "Chrome DevTools",
	EditorCLI:       "Command line",
}

// commentPrefix is how a leading line is commented out per kind, so a hint
// never breaks the body it precedes.
func commentPrefix(kind string) string {
	switch kind {
	case KindJSON:
		return "// "
	case KindShell:
		return "# "
	default:
		return ""
	}
}

// editorData is what every template sees.
type editorData struct {
	Name       string // "Overcast: <resource>", the launch configuration's display name
	Resource   string
	Host       string
	Port       int
	LocalRoot  string // the user's spelling, or the literal ${localRoot} placeholder
	RemoteRoot string
}

// localRootPlaceholder is left in a body when no local root is known, so the
// user sees exactly where their path goes.
const localRootPlaceholder = "${localRoot}"

var templateFuncs = template.FuncMap{
	// json quotes a string for a JSON body; Windows paths carry backslashes.
	"json": func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			return `""`
		}
		return string(b)
	},
	// snippet renders the debugpy listen snippet with each line commented
	// by prefix, so the same text serves a JSONC body and a plain one.
	"snippet": func(prefix string) string {
		lines := strings.Split(dapListenSnippet, "\n")
		for i, l := range lines {
			lines[i] = prefix + l
		}
		return strings.Join(lines, "\n")
	},
}

// newEditor parses a template at package init, so a malformed template is a
// build-time panic rather than an empty editor tab.
func newEditor(id, kind, body string, verified bool) EditorTemplate {
	return EditorTemplate{
		ID:       id,
		Label:    editorLabels[id],
		Kind:     kind,
		Body:     body,
		Verified: verified,
		tmpl:     template.Must(template.New(id).Funcs(templateFuncs).Parse(body)),
	}
}

// template returns the parsed body. A template built outside this package —
// a Protocol from elsewhere returning a bare EditorTemplate — is parsed on
// first use rather than assumed.
func (et EditorTemplate) template() (*template.Template, error) {
	if et.tmpl != nil {
		return et.tmpl, nil
	}
	return template.New(et.ID).Funcs(templateFuncs).Parse(et.Body)
}

// dapListenSnippet is what a Python handler adds so debugpy listens on the
// port Overcast proxies; OVERCAST_DEBUG_PORT is set for every protocol.
const dapListenSnippet = `Before the handler runs, once per process:
  import debugpy, os
  debugpy.listen(("0.0.0.0", int(os.environ["OVERCAST_DEBUG_PORT"])))`

const (
	inspectorVSCode = `{
  "type": "node",
  "request": "attach",
  "name": {{json .Name}},
  "address": {{json .Host}},
  "port": {{.Port}},
  "restart": true,
  "localRoot": {{json .LocalRoot}},
  "remoteRoot": {{json .RemoteRoot}},
  "skipFiles": ["<node_internals>/**", "/var/runtime/**"]
}`
	inspectorJetBrains = `1. Run → Edit Configurations… → + → Attach to Node.js/Chrome
2. Host: {{.Host}}   Port: {{.Port}}
3. Attach to: Node.js started with --inspect
4. Remote URLs of local files: map {{.LocalRoot}} to {{.RemoteRoot}}
5. Debug the configuration; it reconnects on its own when Overcast replaces the container`
	inspectorDevTools = `1. Open chrome://inspect in Chrome
2. Configure… → add {{.Host}}:{{.Port}}
3. Under Remote Target, click inspect on {{.Resource}}`
	inspectorCLI = `node inspect {{.Host}}:{{.Port}}`

	jdwpVSCode = `{
  "type": "java",
  "request": "attach",
  "name": {{json .Name}},
  "hostName": {{json .Host}},
  "port": {{.Port}},
  "sourcePaths": [{{json .LocalRoot}}]
}`
	jdwpJetBrains = `1. Run → Edit Configurations… → + → Remote JVM Debug
2. Debugger mode: Attach to remote JVM   Transport: Socket
3. Host: {{.Host}}   Port: {{.Port}}
4. Use module classpath: the module built from {{.LocalRoot}}
5. Debug the configuration`
	jdwpCLI = `jdb -attach {{.Host}}:{{.Port}}`

	dapVSCode = `{{snippet "// "}}
{
  "type": "debugpy",
  "request": "attach",
  "name": {{json .Name}},
  "connect": { "host": {{json .Host}}, "port": {{.Port}} },
  "pathMappings": [{ "localRoot": {{json .LocalRoot}}, "remoteRoot": {{json .RemoteRoot}} }]
}`
	dapJetBrains = `PyCharm attaches with its own pydevd protocol rather than debugpy, so the session bypasses Overcast's port and the invocation timeout is not suspended.
1. Run → Edit Configurations… → + → Python Debug Server; note the port it shows
2. Before the handler runs: import pydevd_pycharm; pydevd_pycharm.settrace("host.docker.internal", port=<that port>, suspend=False)
3. Path mappings: {{.LocalRoot}} = {{.RemoteRoot}}
4. Start the Debug Server configuration, then invoke {{.Resource}}`

	passthroughSteps = `Overcast forwards {{.Host}}:{{.Port}} to port {{.Port}} inside the container and sets OVERCAST_DEBUG_PORT={{.Port}} in its environment.
Start your runtime's debug server on that port — the flag is yours to set, in the image's command or an environment variable — then attach your editor's own configuration for the language to {{.Host}}:{{.Port}}.
Tag the resource overcast:debug-protocol=inspector, jdwp or dap to have Overcast inject the flag and render a ready-made configuration instead.`
	passthroughShell = `# Overcast forwards {{.Host}}:{{.Port}} to port {{.Port}} inside the container; OVERCAST_DEBUG_PORT={{.Port}} is set there.
# Start your runtime's debug server on that port, then attach your debugger's CLI to {{.Host}}:{{.Port}}.`
)

var (
	inspectorEditors = []EditorTemplate{
		newEditor(EditorVSCode, KindJSON, inspectorVSCode, true),
		newEditor(EditorJetBrains, KindSteps, inspectorJetBrains, true),
		newEditor(EditorDevTools, KindSteps, inspectorDevTools, true),
		newEditor(EditorCLI, KindShell, inspectorCLI, true),
	}
	jdwpEditors = []EditorTemplate{
		newEditor(EditorVSCode, KindJSON, jdwpVSCode, true),
		newEditor(EditorJetBrains, KindSteps, jdwpJetBrains, true),
		newEditor(EditorCLI, KindShell, jdwpCLI, true),
	}
	dapEditors = []EditorTemplate{
		newEditor(EditorVSCode, KindJSON, dapVSCode, true),
		newEditor(EditorJetBrains, KindSteps, dapJetBrains, false),
	}
	passthroughEditors = []EditorTemplate{
		newEditor(EditorVSCode, KindSteps, passthroughSteps, true),
		newEditor(EditorJetBrains, KindSteps, passthroughSteps, true),
		newEditor(EditorCLI, KindShell, passthroughShell, true),
	}
)
