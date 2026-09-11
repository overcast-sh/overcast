package debugger

import (
	"net/url"
	"strings"
	"time"
)

// Descriptor is one target as the console and the CLI see it — the JSON
// shape in docs/plans/compute-debugger.md § 6. Field types stay plain
// (strings, ints, bools, nested structs, slices) because cmd/tsgen renders
// this struct for the web console; there is deliberately no custom
// MarshalJSON to hide anything from it.
type Descriptor struct {
	ID        string `json:"id"`
	Service   string `json:"service"`   // "lambda" | "ecs"
	Resource  string `json:"resource"`  // function name, or task id
	Container string `json:"container"` // ECS container name
	Enabled   bool   `json:"enabled"`
	Reason    string `json:"reason"` // why not, when !enabled

	Protocol       string `json:"protocol"`
	ProtocolSource string `json:"protocolSource"` // "tag" | "runtime" | "env" | "fallback"
	Listen         Listen `json:"listen"`
	State          string `json:"state"` // "inert" | "unbound" | "listening" | "attached" | "paused" | "error"
	AttachedSince  string `json:"attachedSince"`
	PausedSince    string `json:"pausedSince"`
	ContainerID    string `json:"containerId"`
	Upstream       string `json:"upstream"`
	RemoteRoot     string `json:"remoteRoot"`
	LocalRoot      string `json:"localRoot"` // as the user wrote it; "" when unknown
	TimeoutPolicy  string `json:"timeoutPolicy"`

	// ConsoleDebug says the console can open a session of its own on this
	// target — true only for a protocol it ships a client for (the
	// inspector). BridgePath is where: the /_overcast/... path of the
	// WebSocket bridge, which the BFF serves under /api as well, so the
	// console never spells the path itself (docs/plans/compute-debugger-console.md
	// § 3.1–3.2). Empty for an entry synthesised for an untagged resource.
	ConsoleDebug bool   `json:"consoleDebug"`
	BridgePath   string `json:"bridgePath"`

	Setup   Setup    `json:"setup"` // always present
	Editors []Editor `json:"editors"`
}

// Listen is the host and port an editor attaches to.
type Listen struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Setup is how to turn the debugger on: the flag, and the tag as a CLI
// command and a CDK line. Present even when everything is already on, so the
// console never has to know how to spell them.
type Setup struct {
	Flag   string `json:"flag"`
	TagCLI string `json:"tagCli"`
	TagCDK string `json:"tagCdk"`
}

// Editor is one rendered editor tab.
type Editor struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Kind     string `json:"kind"` // "json" | "steps" | "shell"
	Body     string `json:"body"`
	Verified bool   `json:"verified"`
}

// TargetList is the GET /_overcast/debugger/targets body.
type TargetList struct {
	Targets []Descriptor `json:"targets"`
}

// ReasonNotTagged is the reason on a synthesised entry for a resource no tag
// ever mentioned.
const ReasonNotTagged = "not tagged"

// SetupFor renders the setup block for a service. arn is the ARN of the
// resource that carries the tag — the function, or the task definition —
// and may be empty, in which case a placeholder says what belongs there.
func SetupFor(service Service, arn string) Setup {
	setup := Setup{Flag: "OVERCAST_DEBUGGER=true"}
	switch service {
	case ServiceECS:
		if arn == "" {
			arn = "<task-definition-arn>"
		}
		setup.TagCLI = "aws ecs tag-resource --resource-arn " + arn + " --tags key=" + TagDebug + ",value=true"
		setup.TagCDK = `cdk.Tags.of(taskDefinition).add("` + TagDebug + `", "true")`
	case ServiceLambda:
		if arn == "" {
			arn = "<function-arn>"
		}
		setup.TagCLI = "aws lambda tag-resource --resource " + arn + " --tags " + TagDebug + "=true"
		setup.TagCDK = `cdk.Tags.of(fn).add("` + TagDebug + `", "true")`
	}
	return setup
}

// UntaggedDescriptor is the entry the endpoint synthesises for a resource that
// exists but carries no debug tag, so the console tab always has something to
// render: off, why, and how to turn it on. Services implement Describer with
// it.
func UntaggedDescriptor(service Service, resource, container, arn string) Descriptor {
	return Descriptor{
		ID:        TargetID(service, resource, container),
		Service:   string(service),
		Resource:  resource,
		Container: container,
		Reason:    ReasonNotTagged,
		State:     string(StateInert),
		Setup:     SetupFor(service, arn),
		Editors:   []Editor{},
	}
}

// Descriptor renders the target for the console and the CLI.
func (t *Target) Descriptor() Descriptor {
	t.mu.Lock()
	d := Descriptor{
		ID:            t.id,
		Service:       string(t.service),
		Resource:      t.resource,
		Container:     t.container,
		Enabled:       t.enabled,
		Reason:        t.reason,
		Listen:        Listen{Host: dialHost(t.host), Port: t.port},
		State:         string(t.stateLocked()),
		AttachedSince: formatTime(t.attachedSince),
		PausedSince:   formatTime(t.pausedSince),
		ContainerID:   t.containerID,
		Upstream:      t.upstream,
		RemoteRoot:    t.remoteRoot,
		LocalRoot:     t.spec.SourcePathRaw,
		TimeoutPolicy: t.policy.String(),
		BridgePath:    BridgePath(t.service, t.resource, t.container),
		Setup:         SetupFor(t.service, t.arn),
		Editors:       []Editor{},
	}
	protocol := t.res.Protocol
	t.mu.Unlock()

	if protocol == nil {
		return d
	}
	d.Protocol = protocol.Name()
	d.ProtocolSource = string(t.res.Source)
	if cp, ok := protocol.(ConsoleProtocol); ok {
		d.ConsoleDebug = cp.ConsoleDebug()
	}
	d.Editors = renderEditors(protocol, editorData{
		Name:       "Overcast: " + t.resource,
		Resource:   t.resource,
		Host:       d.Listen.Host,
		Port:       d.Listen.Port,
		LocalRoot:  d.LocalRoot,
		RemoteRoot: d.RemoteRoot,
	})
	return d
}

// BridgePath is the path of a target's WebSocket bridge on the API listener:
// GET /_overcast/debugger/targets/{service}/{resource}/ws, with ?container=
// for an ECS task's container. The BFF serves the same path under /api, so
// the console prefixes it and nothing else.
func BridgePath(service Service, resource, container string) string {
	p := "/_overcast/debugger/targets/" + url.PathEscape(string(service)) + "/" + url.PathEscape(resource) + "/ws"
	if container != "" {
		p += "?container=" + url.QueryEscape(container)
	}
	return p
}

// localRootHint is the leading line that says which tag fills the placeholder.
const localRootHint = "Set " + TagSourcePath + " (or " + TagHotReloadPath + ") on the resource to the local source root, and it replaces " + localRootPlaceholder + " below."

// renderEditors executes every template of a protocol. An unknown local root
// is left as a literal placeholder with a leading line naming the tag that
// fills it — only when the body actually uses the root, so passthrough's
// instructions do not carry an irrelevant hint.
func renderEditors(p Protocol, data editorData) []Editor {
	templates := p.Editors()
	editors := make([]Editor, 0, len(templates))
	unknownRoot := data.LocalRoot == ""
	if unknownRoot {
		data.LocalRoot = localRootPlaceholder
	}
	for _, et := range templates {
		var body strings.Builder
		tmpl, err := et.template()
		if err == nil {
			err = tmpl.Execute(&body, data)
		}
		if err != nil {
			// The package's own templates are parsed at init and the data is
			// a flat struct, so an error here is a programming mistake —
			// surfaced in the tab rather than hidden behind an empty one.
			body.Reset()
			body.WriteString(commentPrefix(et.Kind) + "template error: " + err.Error())
		}
		rendered := body.String()
		if unknownRoot && strings.Contains(rendered, localRootPlaceholder) {
			rendered = commentPrefix(et.Kind) + localRootHint + "\n" + rendered
		}
		editors = append(editors, Editor{
			ID:       et.ID,
			Label:    et.Label,
			Kind:     et.Kind,
			Body:     rendered,
			Verified: et.Verified,
		})
	}
	return editors
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// dialHost is the address an editor dials for a listener bound on host. A
// wildcard bind — Overcast in Docker listening on every interface so a
// published port range reaches it — is loopback from the editor's side, and
// "0.0.0.0" in a launch configuration does not connect on every platform.
func dialHost(host string) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return host
}
