package lambdaweb

// provider_docker_ready.go — knowing when the server is up, and why it is not.
//
// Ready is "a TCP connection to the server is accepted", probed with a backoff
// from 2 ms to 50 ms so a server that listens within a few milliseconds of
// starting is found within a few milliseconds. Whether it is not coming is
// decided by the container itself: the waiter closes Done the moment the
// process exits, so an environment that dies during init is reported at once,
// with what it printed, rather than after the init timeout.

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
)

const (
	probeBackoffMin = 2 * time.Millisecond
	probeBackoffMax = 50 * time.Millisecond
	probeDialLimit  = 250 * time.Millisecond

	// publishedProbeHold is how long a connection through a published port is
	// watched. A port forwarder (docker-proxy, Docker Desktop's backend) accepts
	// the connection whether or not anything listens behind it, and closes it
	// when it finds nothing there; a connection that is still open after this
	// long reached the server.
	publishedProbeHold = 150 * time.Millisecond

	defaultInitTimeout = 10 * time.Second
)

// awaitReady returns nil once e accepts connections, else why not. The caller
// removes the container on a failure.
func (p *DockerProvider) awaitReady(ctx context.Context, e *dockerEnv, timeout time.Duration) *initFailure {
	if timeout <= 0 {
		timeout = defaultInitTimeout
	}
	deadline := p.clk.After(timeout)

	if fail := p.resolveAddr(ctx, e); fail != nil {
		return fail
	}
	delay := probeBackoffMin
	for {
		if isClosed(e.done) {
			return p.exitFailure(e)
		}
		if probeListening(ctx, e.addr, e.direct) {
			// A forwarder keeps accepting for a moment after its container has
			// died, so through a published port a positive probe is confirmed
			// with the daemon. Done is the arbiter for the rest: a process that
			// listened and died in the same instant is still a failed start.
			if !e.direct && !p.stillRunning(ctx, e) {
				return p.exitFailure(e)
			}
			if isClosed(e.done) {
				return p.exitFailure(e)
			}
			return nil
		}
		select {
		case <-e.done:
			return p.exitFailure(e)
		case <-ctx.Done():
			return internalFailure("environment start cancelled: " + ctx.Err().Error())
		case <-deadline:
			return p.timeoutFailure(e, timeout)
		case <-p.clk.After(delay):
		}
		delay = min(delay*2, probeBackoffMax)
	}
}

// stillRunning asks the daemon whether the container's process is alive. When
// it is not, it waits briefly for the waiter to record the exit.
func (p *DockerProvider) stillRunning(ctx context.Context, e *dockerEnv) bool {
	info, err := e.dc.InspectContainer(ctx, e.id)
	if err != nil || info.State.Running {
		return true
	}
	select {
	case <-e.done:
	case <-p.clk.After(2 * time.Second):
	case <-ctx.Done():
	}
	return false
}

// resolveAddr finds the address Overcast dials: the container's own address
// when Overcast runs in Docker, else the loopback port the daemon published.
func (p *DockerProvider) resolveAddr(ctx context.Context, e *dockerEnv) *initFailure {
	if ip := dataplane.ContainerAddr(ctx, e.dc, p.cfg, e.id); ip != "" {
		e.addr, e.direct = net.JoinHostPort(ip, strconv.Itoa(webPort)), true
		return nil
	}
	info, err := e.dc.InspectContainer(ctx, e.id)
	if err != nil {
		return internalFailure("inspect container: " + err.Error())
	}
	if port := publishedPort(info); port != "" {
		e.addr = net.JoinHostPort("127.0.0.1", port)
		return nil
	}
	if !info.State.Running {
		// Gone before its port could be read: it died during start.
		select {
		case <-e.done:
			return p.exitFailure(e)
		case <-p.clk.After(5 * time.Second):
		case <-ctx.Done():
		}
	}
	return internalFailure("the container has no published port " + webPortKey)
}

func publishedPort(info *docker.ContainerInspect) string {
	for _, b := range info.NetworkSettings.Ports[webPortKey] {
		if n, err := strconv.Atoi(b.HostPort); err == nil && n > 0 {
			return b.HostPort
		}
	}
	return ""
}

// probeListening reports whether a server accepts connections at addr. direct
// addresses (a container's own IP) refuse a closed port, so a completed
// connect is the answer. Through a published port the forwarder accepts first
// and a closed backend only shows as the connection being closed again.
func probeListening(ctx context.Context, addr string, direct bool) bool {
	d := net.Dialer{Timeout: probeDialLimit}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer conn.Close()
	if direct {
		return true
	}
	_ = conn.SetReadDeadline(time.Now().Add(publishedProbeHold))
	var b [1]byte
	_, err = conn.Read(b[:])
	if err == nil {
		return true // the server spoke first
	}
	ne, ok := err.(net.Error)
	return ok && ne.Timeout() // still open and quiet: a server waiting for a request
}

// exitFailure is the failure for a process that exited before it listened.
func (p *DockerProvider) exitFailure(e *dockerEnv) *initFailure {
	code, oom := e.exitStatus()
	return classifyExit(code, oom, e.outputTail())
}

// timeoutFailure is the failure for a process that is running but never
// listened.
func (p *DockerProvider) timeoutFailure(e *dockerEnv, timeout time.Duration) *initFailure {
	msg := fmt.Sprintf("the server did not accept connections on %s within %s", webListenAddr, timeout)
	return &initFailure{Type: errTypeTimeoutListen, Detail: withTail(msg, e.outputTail())}
}

// outputTail is the end of what the container printed, bounded.
func (e *dockerEnv) outputTail() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := e.dc.ContainerLogs(ctx, e.id, "100")
	if err != nil {
		return ""
	}
	return boundedTail(strings.TrimSpace(string(docker.DemuxStream(raw))), outputTailBytes)
}

// withTail appends tail to msg, keeping the whole within outputTailBytes.
func withTail(msg, tail string) string {
	if tail == "" {
		return msg
	}
	room := outputTailBytes - len(msg) - 1
	if room <= 0 {
		return boundedTail(msg, outputTailBytes)
	}
	return msg + "\n" + boundedTail(tail, room)
}

// uncaughtException matches what Node prints when an exception goes uncaught:
// a stack frame, or the trailer that follows the trace.
var uncaughtException = regexp.MustCompile(`(?m)^\s+at \S|^Node\.js v\d`)

// classifyExit turns the end of a process that never listened into an init
// failure.
//
//   - killed for memory: Runtime.OutOfMemory
//   - bootstrap.mjs could not import the entry file (its marker, or its exit
//     code): Runtime.ImportCodeError
//   - the entry file threw as it ran, or an exception went uncaught while the
//     server was starting: Application.InitCodeError
//   - anything else: Runtime.ExitError
func classifyExit(code int, oom bool, tail string) *initFailure {
	detail := func(msg string) string { return withTail(msg, tail) }
	switch {
	case oom:
		return &initFailure{Type: errTypeOutOfMemory, Detail: detail("the process was killed for exceeding its memory")}
	case code == exitCodeImportError || strings.Contains(tail, "OVERCAST_WEB_IMPORT_ERROR"):
		return &initFailure{Type: errTypeImportCode, Detail: detail("the entry file could not be imported")}
	case code == exitCodeInitError || strings.Contains(tail, "OVERCAST_WEB_INIT_ERROR"):
		return &initFailure{Type: errTypeInitCode, Detail: detail("the entry file threw while it ran")}
	case code != 0 && uncaughtException.MatchString(tail):
		return &initFailure{Type: errTypeInitCode, Detail: detail("an exception was thrown during initialization")}
	}
	return &initFailure{Type: errTypeExitError, Detail: detail(fmt.Sprintf("the process exited with status %d before it started listening", code))}
}
