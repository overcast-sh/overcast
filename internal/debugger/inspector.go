package debugger

import (
	"strconv"
	"strings"
)

// inspector is Node.js's debugger: the Chrome DevTools Protocol over a
// WebSocket, opened by --inspect. It is the one protocol with a pause
// observer, because CDP is JSON and says "Debugger.paused" in so many words.
type inspector struct{}

const inspectorName = "inspector"

// nodeOptionsEnv is where Node reads extra flags from; a handler's command
// line is the runtime's, so this is the only place a flag can be added.
const nodeOptionsEnv = "NODE_OPTIONS"

func (inspector) Name() string       { return inspectorName }
func (inspector) Runtimes() []string { return []string{"nodejs"} }

// Inject adds --inspect bound to every interface — the proxy reaches the
// container over its network, never loopback — unless the user already
// asked for an inspector of their own, whose port Detect reports instead.
func (inspector) Inject(env map[string]string, port int) {
	if _, present := detectInspect(env[nodeOptionsEnv]); present {
		return
	}
	appendOption(env, nodeOptionsEnv, "--inspect=0.0.0.0:"+strconv.Itoa(port))
}

// Detect recognises --inspect, --inspect-brk and --inspect-wait, each with an
// optional =[host:]port, plus --inspect-port=[host:]port which only changes
// the port. Node's default is 9229 when no port is given.
func (inspector) Detect(env map[string]string) (int, bool) {
	return detectInspect(env[nodeOptionsEnv])
}

func (inspector) Editors() []EditorTemplate { return inspectorEditors }

func (inspector) NewObserver() Observer { return newCDPObserver() }

// nodeDefaultInspectPort is what --inspect without a port binds.
const nodeDefaultInspectPort = 9229

func detectInspect(options string) (int, bool) {
	port, active := 0, false
	for _, token := range strings.Fields(options) {
		flag, value, hasValue := strings.Cut(token, "=")
		switch flag {
		case "--inspect", "--inspect-brk", "--inspect-wait":
			active = true
			if hasValue {
				if p, ok := hostPort(value); ok {
					port = p
				}
			}
		case "--inspect-port":
			if p, ok := hostPort(value); ok {
				port = p
			}
		}
	}
	if !active {
		return 0, false
	}
	if port == 0 {
		port = nodeDefaultInspectPort
	}
	return port, true
}
