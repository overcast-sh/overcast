package main

// docker_backend_test.go — unit tests for docker_backend.go. None of these
// need a real docker daemon: dockerRun is a package var (see that file's
// comment), so every test here swaps it for a fake that inspects the argv it
// was given and returns canned output — the same reason this repo's
// docker-gated tests otherwise have a habit of silently skipping (see the
// memory note on SkipWithoutDocker) is exactly what this seam avoids.

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
)

// withFakeDockerRun replaces dockerRun for the duration of one test with fn,
// restoring the original afterwards.
func withFakeDockerRun(t *testing.T, fn func(args ...string) (string, error)) {
	t.Helper()
	prev := dockerRun
	dockerRun = fn
	t.Cleanup(func() { dockerRun = prev })
}

// recordingDockerRun returns a fake dockerRun that appends every call's argv
// to *calls (space-joined, for easy substring assertions) and returns a
// fixed (stdout, err) pair for every call.
func recordingDockerRun(calls *[][]string, stdout string, err error) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		*calls = append(*calls, args)
		return stdout, err
	}
}

func TestResolveDockerImage_ImageFlagWins(t *testing.T) {
	got, err := resolveDockerImage("my/custom:tag", "alpha", "1.2.3", nil)
	if err != nil {
		t.Fatalf("resolveDockerImage: %v", err)
	}
	if got != "my/custom:tag" {
		t.Errorf("got %q, want the --image override untouched", got)
	}
}

func TestResolveDockerImage_ChannelOverridesVersion(t *testing.T) {
	for _, ch := range []string{"alpha", "beta", "latest"} {
		got, err := resolveDockerImage("", ch, "1.2.3", nil)
		if err != nil {
			t.Fatalf("resolveDockerImage(channel=%s): %v", ch, err)
		}
		want := dockerImageRepo + ":" + ch
		if got != want {
			t.Errorf("resolveDockerImage(channel=%s) = %q, want %q", ch, got, want)
		}
	}
}

func TestResolveDockerImage_RejectsUnknownChannel(t *testing.T) {
	if _, err := resolveDockerImage("", "nightly", "1.2.3", nil); err == nil {
		t.Fatal("resolveDockerImage(channel=nightly) succeeded, want an error")
	}
}

func TestResolveDockerImage_VersionPinnedDefault(t *testing.T) {
	got, err := resolveDockerImage("", "", "0.0.1-alpha.25", nil)
	if err != nil {
		t.Fatalf("resolveDockerImage: %v", err)
	}
	want := dockerImageRepo + ":0.0.1-alpha.25"
	if got != want {
		t.Errorf("got %q, want %q (pinned to the CLI's own version)", got, want)
	}
}

// TestResolveDockerImage_DevBuildFallsBackToAlpha covers the one case with no
// matching version tag: an unreleased "dev" build. It must fall back to
// :alpha and say so via the note callback, rather than trying (and failing)
// to pull ghcr.io/overcast-sh/overcast:dev.
func TestResolveDockerImage_DevBuildFallsBackToAlpha(t *testing.T) {
	var noted string
	got, err := resolveDockerImage("", "", "dev", func(s string) { noted = s })
	if err != nil {
		t.Fatalf("resolveDockerImage: %v", err)
	}
	want := dockerImageRepo + ":alpha"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if noted == "" {
		t.Error("expected a note explaining the fallback to :alpha, got none")
	}
	if !strings.Contains(noted, "alpha") {
		t.Errorf("note %q does not mention the :alpha fallback", noted)
	}
}

func TestResolveDockerImage_DevBuildWithExplicitImageSkipsNote(t *testing.T) {
	var noted string
	got, err := resolveDockerImage("my/custom:tag", "", "dev", func(s string) { noted = s })
	if err != nil {
		t.Fatalf("resolveDockerImage: %v", err)
	}
	if got != "my/custom:tag" {
		t.Errorf("got %q, want the explicit --image untouched", got)
	}
	if noted != "" {
		t.Errorf("unexpected note %q when --image was given explicitly", noted)
	}
}

// freeLoopbackPort asks the OS for a free port by opening then immediately
// closing a listener, so tests exercising portFree/resolveDockerPorts have a
// real, currently-free port to assert against without hardcoding one that
// might collide with something else running on the test machine.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// freeLoopbackPortPair reserves two distinct free ports — for the API and
// the web console — by holding two ephemeral listeners open at once, then
// releases both so the code under test can bind them. Both come from the
// OS, never arithmetic: a derived port like port+1 is not reserved by
// anything, so another test binary running in parallel (go test -p N) can
// already hold it — that flake happened ("startDocker: ui-port N is already
// in use") — and port+1 overflows 65535 when the draw is the last port.
func freeLoopbackPortPair(t *testing.T) (port, uiPort int) {
	t.Helper()
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	defer ln1.Close()
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a second free port: %v", err)
	}
	defer ln2.Close()
	return ln1.Addr().(*net.TCPAddr).Port, ln2.Addr().(*net.TCPAddr).Port
}

func TestPortFree_TrueForAnUnboundPort(t *testing.T) {
	port := freeLoopbackPort(t)
	if !portFree(port) {
		t.Errorf("portFree(%d) = false for a port nothing is listening on", port)
	}
}

func TestPortFree_FalseForABoundPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if portFree(port) {
		t.Errorf("portFree(%d) = true for a port this test is actively listening on", port)
	}
}

// TestResolveDockerPorts_ExplicitUsesAsIs covers the "caller actually asked
// for these ports" path: both must be free, and the pair returned is exactly
// what was given, no scanning.
func TestResolveDockerPorts_ExplicitUsesAsIs(t *testing.T) {
	// Explicit mode does not require adjacency, which two independent
	// ephemeral draws also exercise. (A derived port like port+1000 once
	// flaked here too: ephemeral draw 65506 → "ui-port 66506 already in use".)
	port, uiPort := freeLoopbackPortPair(t)

	gotPort, gotUI, err := resolveDockerPorts(port, uiPort, true)
	if err != nil {
		t.Fatalf("resolveDockerPorts: %v", err)
	}
	if gotPort != port || gotUI != uiPort {
		t.Errorf("resolveDockerPorts(explicit) = (%d, %d), want (%d, %d)", gotPort, gotUI, port, uiPort)
	}
}

func TestResolveDockerPorts_ExplicitErrorsWhenBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	if _, _, err := resolveDockerPorts(busyPort, busyPort+1, true); err == nil {
		t.Fatal("resolveDockerPorts(explicit) on a busy port succeeded, want an error")
	}
}

// TestResolveDockerPorts_ExplicitUIPortZeroDisablesConsole mirrors the
// native backend's "0 = disable the web console" meaning: a caller who
// explicitly asks for --ui-port 0 must get 0 back untouched, and it must
// never be probed with portFree (0 is not a real port to bind).
func TestResolveDockerPorts_ExplicitUIPortZeroDisablesConsole(t *testing.T) {
	port := freeLoopbackPort(t)
	gotPort, gotUI, err := resolveDockerPorts(port, 0, true)
	if err != nil {
		t.Fatalf("resolveDockerPorts: %v", err)
	}
	if gotPort != port || gotUI != 0 {
		t.Errorf("resolveDockerPorts(explicit, ui=0) = (%d, %d), want (%d, 0)", gotPort, gotUI, port)
	}
}

// TestResolveDockerPorts_ScansWhenNotExplicit exercises the default path
// without depending on the real 4566/4567 (which may or may not be free on
// the machine running the suite, and CI should never touch anyway): it binds
// a port to force the scan to skip past it, then confirms the returned pair
// is a free adjacent pair strictly after the occupied one.
func TestResolveDockerPorts_ScansWhenNotExplicit(t *testing.T) {
	// Occupy a port to prove the scan actually skips a busy candidate rather
	// than always returning its starting point.
	ln, err := net.Listen("tcp", "127.0.0.1:4566")
	if err != nil {
		t.Skipf("port 4566 unavailable in this test environment, cannot exercise the scan's starting point: %v", err)
	}
	defer ln.Close()

	gotPort, gotUI, err := resolveDockerPorts(0, 0, false)
	if err != nil {
		t.Fatalf("resolveDockerPorts: %v", err)
	}
	if gotPort == 4566 {
		t.Errorf("resolveDockerPorts returned the occupied port 4566")
	}
	if gotUI != gotPort+1 {
		t.Errorf("resolveDockerPorts = (%d, %d), want an adjacent pair", gotPort, gotUI)
	}
	if !portFree(gotPort) || !portFree(gotUI) {
		t.Errorf("resolveDockerPorts returned a pair that is not actually free: (%d, %d)", gotPort, gotUI)
	}
}

func TestCheckDockerAvailable_MissingDaemonReported(t *testing.T) {
	withFakeDockerRun(t, func(args ...string) (string, error) {
		return "", errors.New("dial unix /var/run/docker.sock: connect: no such file or directory")
	})
	// This only exercises the daemon-reachability branch when the docker CLI
	// itself is on PATH; if it is not, checkDockerAvailable correctly stops
	// at the PATH check first (also an error) — either way an error is
	// expected here, so the test does not depend on which branch fires.
	if err := checkDockerAvailable(); err == nil {
		t.Fatal("checkDockerAvailable succeeded despite a failing docker daemon probe, want an error")
	}
}

func TestDockerContainerRunning_True(t *testing.T) {
	withFakeDockerRun(t, func(args ...string) (string, error) {
		return "true\n", nil
	})
	running, err := dockerContainerRunning("abc123")
	if err != nil {
		t.Fatalf("dockerContainerRunning: %v", err)
	}
	if !running {
		t.Error("dockerContainerRunning = false, want true")
	}
}

func TestDockerContainerRunning_False(t *testing.T) {
	withFakeDockerRun(t, func(args ...string) (string, error) {
		return "false\n", nil
	})
	running, err := dockerContainerRunning("abc123")
	if err != nil {
		t.Fatalf("dockerContainerRunning: %v", err)
	}
	if running {
		t.Error("dockerContainerRunning = true, want false")
	}
}

func TestDockerContainerRunning_InspectFailureIsAnError(t *testing.T) {
	withFakeDockerRun(t, func(args ...string) (string, error) {
		return "", errors.New("no such container: abc123")
	})
	if _, err := dockerContainerRunning("abc123"); err == nil {
		t.Fatal("dockerContainerRunning succeeded despite a failing docker inspect, want an error")
	}
}

func TestDockerContainerRunning_EmptyContainerIDIsAnError(t *testing.T) {
	if _, err := dockerContainerRunning(""); err == nil {
		t.Fatal("dockerContainerRunning(\"\") succeeded, want an error")
	}
}

func TestDockerContainerRunning_UsesInspectArgs(t *testing.T) {
	var gotArgs []string
	withFakeDockerRun(t, func(args ...string) (string, error) {
		gotArgs = args
		return "true", nil
	})
	if _, err := dockerContainerRunning("mycontainer"); err != nil {
		t.Fatalf("dockerContainerRunning: %v", err)
	}
	want := []string{"inspect", "-f", "{{.State.Running}}", "mycontainer"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("dockerRun called with %v, want %v", gotArgs, want)
	}
}

func TestLastNonEmptyLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abc123\n", "abc123"},
		{"abc123", "abc123"},
		{"line one\nline two\nabc123\n", "abc123"},
		{"line one\r\nline two\r\nabc123\r\n", "abc123"},
		{"abc123\n\n\n", "abc123"},
		{"", ""},
		{"\n\n", ""},
	}
	for _, c := range cases {
		if got := lastNonEmptyLine(c.in); got != c.want {
			t.Errorf("lastNonEmptyLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDockerContainerName(t *testing.T) {
	if got := dockerContainerName("default"); got != "overcast-default" {
		t.Errorf("dockerContainerName(default) = %q, want %q", got, "overcast-default")
	}
}

func TestShortContainerID(t *testing.T) {
	if got := shortContainerID("0123456789abcdef0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("shortContainerID(long) = %q, want the first 12 chars", got)
	}
	if got := shortContainerID("abc"); got != "abc" {
		t.Errorf("shortContainerID(short) = %q, want it returned untouched", got)
	}
}

// TestStartDocker_RequiresResolvedImage guards startDocker's precondition:
// cmd_start.go's RunE must resolve the image (resolveDockerImage) before
// calling startBackend("docker", ...). An empty opts.image is an internal
// bug, not a user-facing error path, so this asserts it fails loudly rather
// than silently running `docker run` with an empty image argument.
func TestStartDocker_RequiresResolvedImage(t *testing.T) {
	if _, err := startDocker(startOptions{name: "x"}); err == nil {
		t.Fatal("startDocker with an empty opts.image succeeded, want an error")
	}
}

// TestStartDocker_AssemblesRunArgs is the core of this backend's test
// coverage: given resolved options, does startDocker build the `docker run`
// argv the brief actually specifies? No real docker involved — dockerRun is
// faked to capture the argv and hand back a fixed container id.
func TestStartDocker_AssemblesRunArgs(t *testing.T) {
	port, uiPort := freeLoopbackPortPair(t)

	var calls [][]string
	withFakeDockerRun(t, recordingDockerRun(&calls, "abcdef0123456789\n", nil))

	rec, err := startDocker(startOptions{
		name:              "myinst",
		port:              port,
		uiPort:            uiPort,
		portsExplicit:     true,
		image:             "ghcr.io/overcast-sh/overcast:alpha",
		env:               map[string]string{"OVERCAST_LOG_LEVEL": "debug", "AWS_REGION": "eu-west-1"},
		state:             "hybrid",
		dataVolume:        "myvol",
		mountDockerSocket: true,
	})
	if err != nil {
		t.Fatalf("startDocker: %v", err)
	}
	// The last call is `docker run` itself; checkDockerAvailable's own probe
	// (a `docker version`) may run before it, so this asserts on the last
	// call rather than assuming there is only one.
	if len(calls) == 0 {
		t.Fatal("dockerRun was never called")
	}
	argv := calls[len(calls)-1]
	joined := strings.Join(argv, " ")

	mustContain := []string{
		"run", "-d",
		"--name overcast-myinst",
		fmt.Sprintf("-p 127.0.0.1:%d:4566", port),
		fmt.Sprintf("-p 127.0.0.1:%d:4567", uiPort),
		"-e OVERCAST_STATE=hybrid",
		"-e AWS_REGION=eu-west-1",
		"-e OVERCAST_LOG_LEVEL=debug",
		"-v myvol:/data",
		"-e OVERCAST_DATA_DIR=/data",
		"-v /var/run/docker.sock:/var/run/docker.sock",
	}
	for _, want := range mustContain {
		if !strings.Contains(joined, want) {
			t.Errorf("docker run argv %q does not contain %q", joined, want)
		}
	}
	if argv[len(argv)-1] != "ghcr.io/overcast-sh/overcast:alpha" {
		t.Errorf("last argv element = %q, want the image reference last", argv[len(argv)-1])
	}

	// Env keys sorted (AWS_REGION before OVERCAST_LOG_LEVEL) for a
	// deterministic argv, mirroring buildChildEnv's rationale.
	awsIdx := strings.Index(joined, "AWS_REGION")
	logIdx := strings.Index(joined, "OVERCAST_LOG_LEVEL")
	if awsIdx == -1 || logIdx == -1 || awsIdx > logIdx {
		t.Errorf("env flags not sorted: %q", joined)
	}

	if rec.Backend != "docker" {
		t.Errorf("rec.Backend = %q, want docker", rec.Backend)
	}
	if rec.ContainerID != "abcdef0123456789" {
		t.Errorf("rec.ContainerID = %q, want the trimmed docker run output", rec.ContainerID)
	}
	if rec.Image != "ghcr.io/overcast-sh/overcast:alpha" {
		t.Errorf("rec.Image = %q, want the resolved image persisted", rec.Image)
	}
	if rec.DataVolume != "myvol" || !rec.MountDockerSocket {
		t.Errorf("rec docker fields not persisted: %+v", rec)
	}
	if rec.Port != port || rec.UIPort != uiPort {
		t.Errorf("rec ports = (%d, %d), want (%d, %d)", rec.Port, rec.UIPort, port, uiPort)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", port); rec.Endpoint != want {
		t.Errorf("rec.Endpoint = %q, want %q", rec.Endpoint, want)
	}
}

// TestStartDocker_NoVolumeNoSocketNoExtraFlags is the mirror of
// TestStartDocker_AssemblesRunArgs's "everything on" case: with none of the
// optional flags set, the argv must not contain any of the mount/env flags
// they would have added — the allow-list philosophy cuts both ways.
func TestStartDocker_NoVolumeNoSocketNoExtraFlags(t *testing.T) {
	port, uiPort := freeLoopbackPortPair(t)
	var calls [][]string
	withFakeDockerRun(t, recordingDockerRun(&calls, "cid123\n", nil))

	if _, err := startDocker(startOptions{
		name:          "bare",
		port:          port,
		uiPort:        uiPort,
		portsExplicit: true,
		image:         "ghcr.io/overcast-sh/overcast:alpha",
	}); err != nil {
		t.Fatalf("startDocker: %v", err)
	}
	joined := strings.Join(calls[0], " ")
	for _, mustNotContain := range []string{"/var/run/docker.sock", "/data", "OVERCAST_DATA_DIR", "OVERCAST_STATE"} {
		if strings.Contains(joined, mustNotContain) {
			t.Errorf("docker run argv %q unexpectedly contains %q", joined, mustNotContain)
		}
	}
}

// TestStartDocker_ContainerIDIgnoresPullProgress covers a real bug found
// during manual smoke testing: when `docker run` has to pull the image
// first, CombinedOutput's stream carries "Unable to find image ... locally"
// plus the pull's progress lines ahead of the container id. Taking the whole
// trimmed output as the id stored that entire multi-line pull log as the
// "container id" — this pins the fix (lastNonEmptyLine): only the last line
// is kept.
func TestStartDocker_ContainerIDIgnoresPullProgress(t *testing.T) {
	port, uiPort := freeLoopbackPortPair(t)
	pullOutput := "Unable to find image 'ghcr.io/overcast-sh/overcast:alpha' locally\n" +
		"alpha: Pulling from overcast-sh/overcast\n" +
		"Digest: sha256:deadbeef\n" +
		"Status: Downloaded newer image for ghcr.io/overcast-sh/overcast:alpha\n" +
		"287718fc08eb1234567890abcdef1234567890abcdef1234567890abcdef12\n"
	withFakeDockerRun(t, func(args ...string) (string, error) {
		return pullOutput, nil
	})

	rec, err := startDocker(startOptions{
		name: "pulltest", port: port, uiPort: uiPort, portsExplicit: true,
		image: "ghcr.io/overcast-sh/overcast:alpha",
	})
	if err != nil {
		t.Fatalf("startDocker: %v", err)
	}
	want := "287718fc08eb1234567890abcdef1234567890abcdef1234567890abcdef12"
	if rec.ContainerID != want {
		t.Errorf("rec.ContainerID = %q, want just the last line %q", rec.ContainerID, want)
	}
}

// TestStartDocker_RunFailurePropagates asserts the `docker run` error itself
// comes back, not merely some error: only `run` fails here, so the daemon
// probe (`docker version`) and the port checks pass and the error can only
// have come from the run step.
func TestStartDocker_RunFailurePropagates(t *testing.T) {
	port, uiPort := freeLoopbackPortPair(t)
	runErr := errors.New("Unable to find image 'ghcr.io/overcast-sh/overcast:alpha' locally")
	withFakeDockerRun(t, func(args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", runErr
		}
		return "", nil
	})
	_, err := startDocker(startOptions{
		name: "x", port: port, uiPort: uiPort, portsExplicit: true, image: "ghcr.io/overcast-sh/overcast:alpha",
	})
	if !errors.Is(err, runErr) {
		t.Fatalf("startDocker error = %v, want it to wrap the docker run failure %q", err, runErr)
	}
}

func TestDockerLogsArgs_NoFollow(t *testing.T) {
	got := dockerLogsArgs("mycontainer", 50, false)
	want := []string{"logs", "--tail", "50", "mycontainer"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dockerLogsArgs = %v, want %v", got, want)
	}
}

func TestDockerLogsArgs_Follow(t *testing.T) {
	got := dockerLogsArgs("mycontainer", 100, true)
	want := []string{"logs", "--tail", "100", "-f", "mycontainer"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dockerLogsArgs = %v, want %v", got, want)
	}
}
