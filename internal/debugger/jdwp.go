package debugger

import (
	"strconv"
	"strings"
)

// jdwp is the JVM's debug wire protocol, started by the jdwp agent. It is
// binary and undocumented enough that Overcast does not read it, so the
// timeout policy falls back to attached for Java.
type jdwp struct{}

const jdwpName = "jdwp"

// javaToolOptionsEnv is the variable every JVM reads before its command line,
// which makes it the one place an agent can be added to a managed runtime.
const javaToolOptionsEnv = "JAVA_TOOL_OPTIONS"

func (jdwp) Name() string       { return jdwpName }
func (jdwp) Runtimes() []string { return []string{"java"} }

// Inject adds the agent listening on every interface with suspend=n, so a
// function whose editor is not yet attached still runs. A user's own jdwp
// agent is left alone, whatever its settings.
func (jdwp) Inject(env map[string]string, port int) {
	if hasJDWPAgent(env[javaToolOptionsEnv]) {
		return
	}
	appendOption(env, javaToolOptionsEnv,
		"-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:"+strconv.Itoa(port))
}

// Detect reads the port from an existing agent string's address=[host:]port.
// An agent with server=n dials out to the editor rather than listening, so
// there is nothing to proxy and it is not reported.
func (jdwp) Detect(env map[string]string) (int, bool) {
	return detectJDWP(env[javaToolOptionsEnv])
}

func (jdwp) Editors() []EditorTemplate { return jdwpEditors }

// hasJDWPAgent recognises -agentlib:jdwp, -agentpath:…/jdwp and the legacy
// -Xrunjdwp, so Inject never doubles an agent the user configured.
func hasJDWPAgent(options string) bool {
	for _, token := range strings.Fields(options) {
		if isJDWPAgent(token) {
			return true
		}
	}
	return false
}

func isJDWPAgent(token string) bool {
	return strings.HasPrefix(token, "-agentlib:jdwp") ||
		strings.HasPrefix(token, "-Xrunjdwp") ||
		(strings.HasPrefix(token, "-agentpath:") && strings.Contains(token, "jdwp"))
}

func detectJDWP(options string) (int, bool) {
	for _, token := range strings.Fields(options) {
		if !isJDWPAgent(token) {
			continue
		}
		_, params, _ := strings.Cut(token, "=")
		port, listening := 0, true
		for _, kv := range strings.Split(params, ",") {
			key, value, _ := strings.Cut(kv, "=")
			switch key {
			case "address":
				if p, ok := hostPort(value); ok {
					port = p
				}
			case "server":
				listening = strings.EqualFold(value, "y")
			}
		}
		if port != 0 && listening {
			return port, true
		}
	}
	return 0, false
}
