package lambdadocker_test

// lambda_init_test.go — the in-container init, end to end.
//
// Every Lambda container Overcast starts runs `/var/overcast/init` as PID 1,
// with the command the container would otherwise have run as its argument list.
// The init owns the runtime's stdout and stderr, tags each line with the
// invocation that was in flight when it read it, and ships the lines to
// Overcast on one long-lived connection. These tests exercise what that buys
// through the API a user sees: exact per-invocation tails, exact CloudWatch
// ordering, extensions for image functions, and `docker logs` unchanged.
//
// All of them need Docker. They also need the init artefacts, which are build
// output — requireLambdaInit builds them once per test binary if they are
// missing, so `go test` just works.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/tests/helpers"
	"go.uber.org/zap"
)

// ─── shared helpers ──────────────────────────────────────────────────────────

// dockerClient is a client for the daemon the test server uses.
func dockerClient(t *testing.T) *docker.Client {
	t.Helper()
	return docker.NewClient(helpers.TestDockerSocket(), zap.NewNop())
}

// lambdaContainerID finds the running container for a function, by the name
// ContainerRuntime gives it.
func lambdaContainerID(t *testing.T, fn string) string {
	t.Helper()
	dc := dockerClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	containers, err := dc.ListContainers(ctx, "lambda")
	if err != nil {
		t.Fatalf("list lambda containers: %v", err)
	}
	prefix := "/overcast-lambda-" + fn + "-"
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.HasPrefix(name, prefix) {
				return c.ID
			}
		}
	}
	t.Fatalf("no running container named %s*; got %+v", prefix, containers)
	return ""
}

// logEventsFor polls FilterLogEvents until want is satisfied, and returns the
// messages in the order CloudWatch reports them.
func logEventsFor(t *testing.T, srv *helpers.TestServer, group string, want func([]string) bool) []string {
	t.Helper()
	const (
		idleBudget    = 15 * time.Second
		overallBudget = 2 * time.Minute
	)
	started := time.Now()
	overallDeadline := started.Add(overallBudget)
	idleDeadline := started.Add(idleBudget)
	var seen int
	var messages []string
	for time.Now().Before(overallDeadline) && time.Now().Before(idleDeadline) {
		body, _ := json.Marshal(map[string]any{"logGroupName": group})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		req.Header.Set("X-Amz-Target", "Logs_20140328.FilterLogEvents")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("FilterLogEvents: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		var result struct {
			Events []struct {
				Message string `json:"message"`
			} `json:"events"`
		}
		_ = json.Unmarshal(raw, &result)
		messages = messages[:0]
		for _, e := range result.Events {
			messages = append(messages, e.Message)
		}
		if len(messages) > seen {
			seen = len(messages)
			idleDeadline = time.Now().Add(idleBudget)
		}
		if want(messages) {
			return messages
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the log group %s never held what the test was waiting for after %v; got %d events:\n%s",
		group, time.Since(started).Round(time.Millisecond), len(messages), strings.Join(messages, "\n"))
	return nil
}

// indexOfLine returns the position of the first message equal to or containing
// want, or -1.
func indexOfLine(messages []string, want string) int {
	for i, m := range messages {
		if strings.Contains(m, want) {
			return i
		}
	}
	return -1
}

// ─── exact per-invocation attribution ────────────────────────────────────────

// TestInvoke_logTail_exactAttribution is the property the whole in-container
// init exists for: two back-to-back invocations on one warm container, each
// tail holding exactly its own lines and nothing of the other's — including a
// line the handler prints after a delay, which is the case a silence-based wait
// could never get right.
//
// Ordering is asserted per stream. stdout and stderr are two pipes with two
// readers, so which of them lands first is racy here exactly as it is on AWS;
// what is not racy is that a line goes to the invocation that was in flight
// when it was written.
func TestInvoke_logTail_exactAttribution(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async (event) => {
  const m = event.marker;
  console.log("A " + m);
  console.log("B " + m);
  console.error("E " + m);
  await new Promise(r => setTimeout(r, 120));
  console.log("late " + m);
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "attrib-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "attrib-fn")

	first := string(invokeForLogTail(t, srv, "attrib-fn", []byte(`{"marker":"one"}`)))
	second := string(invokeForLogTail(t, srv, "attrib-fn", []byte(`{"marker":"two"}`)))

	for _, tc := range []struct {
		name   string
		tail   string
		marker string
		other  string
	}{
		{name: "first invocation", tail: first, marker: "one", other: "two"},
		{name: "second invocation", tail: second, marker: "two", other: "one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(strings.TrimRight(tc.tail, "\n"), "\n")

			// Every one of this invocation's own lines is present.
			for _, want := range []string{"A " + tc.marker, "B " + tc.marker, "E " + tc.marker, "late " + tc.marker} {
				if indexOfLine(lines, want) < 0 {
					t.Errorf("the tail is missing %q:\n%s", want, tc.tail)
				}
			}
			// And none of the other invocation's.
			if strings.Contains(tc.tail, " "+tc.other) {
				t.Errorf("the tail leaked the other invocation's output:\n%s", tc.tail)
			}

			// START opens it, END and REPORT close it, in that order.
			start := indexOfLine(lines, "START RequestId:")
			end := indexOfLine(lines, "END RequestId:")
			report := indexOfLine(lines, "REPORT RequestId:")
			if start != 0 {
				t.Errorf("START is at %d, want the first line:\n%s", start, tc.tail)
			}
			if !(start < end && end < report) {
				t.Errorf("START/END/REPORT are at %d/%d/%d, want that order:\n%s", start, end, report, tc.tail)
			}
			// The handler's own lines sit between START and END, and the one
			// printed after a delay is among them rather than after REPORT.
			for _, want := range []string{"A " + tc.marker, "B " + tc.marker, "late " + tc.marker} {
				if at := indexOfLine(lines, want); at < start || at > end {
					t.Errorf("%q is at %d, want between START (%d) and END (%d):\n%s", want, at, start, end, tc.tail)
				}
			}
			// Per-pipe order is the order the handler wrote them in.
			if a, b := indexOfLine(lines, "A "+tc.marker), indexOfLine(lines, "B "+tc.marker); a > b {
				t.Errorf("stdout lines are out of order: A at %d, B at %d:\n%s", a, b, tc.tail)
			}
			if b, late := indexOfLine(lines, "B "+tc.marker), indexOfLine(lines, "late "+tc.marker); b > late {
				t.Errorf("stdout lines are out of order: B at %d, late at %d:\n%s", b, late, tc.tail)
			}
		})
	}

	// Exactly one request ID per tail, and two different ones.
	if requestIDOf(t, first) == requestIDOf(t, second) {
		t.Error("both tails carry the same request ID, so one of them is the wrong invocation's")
	}
}

// requestIDOf reads the request ID out of a tail's START line.
func requestIDOf(t *testing.T, tail string) string {
	t.Helper()
	for _, line := range strings.Split(tail, "\n") {
		if rest, ok := strings.CutPrefix(line, "START RequestId: "); ok {
			id, _, _ := strings.Cut(rest, " ")
			return id
		}
	}
	t.Fatalf("no START line in the tail:\n%s", tail)
	return ""
}

// ─── `docker logs` is unchanged ──────────────────────────────────────────────

// The init owns the runtime's pipes, so the container's own stdout would go
// dark unless the init wrote every line back to it. It does — byte for byte —
// which is what keeps `docker logs -f` working for a human and keeps the
// startup-exit capture in awaitContainerIP able to explain a container that
// died. Its own diagnostics are there too, on stderr, clearly labelled.
func TestInvoke_containerLogsStillCarryTheHandlersOutput(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  console.log("teed to the daemon marker-tee");
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "tee-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "tee-fn")

	resp := invokeFunction(t, srv, "tee-fn", []byte("{}"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("invoke returned %d, want 200", resp.StatusCode)
	}

	id := lambdaContainerID(t, "tee-fn")
	dc := dockerClient(t)
	deadline := time.Now().Add(30 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		raw, err := dc.ContainerLogs(ctx, id, "200")
		cancel()
		if err != nil {
			t.Fatalf("docker logs: %v", err)
		}
		logs = string(docker.DemuxStream(raw))
		if strings.Contains(logs, "marker-tee") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(logs, "teed to the daemon marker-tee") {
		t.Errorf("`docker logs` does not carry the handler's line:\n%s", logs)
	}
	if !strings.Contains(logs, "[overcast-init]") {
		t.Errorf("`docker logs` carries no [overcast-init] diagnostics:\n%s", logs)
	}
	// The init's own diagnostics are for a human reading the daemon's copy and
	// must never be published as the function's output.
	tail := string(invokeForLogTail(t, srv, "tee-fn", []byte("{}")))
	if strings.Contains(tail, "[overcast-init]") {
		t.Errorf("an [overcast-init] diagnostic reached the invocation tail:\n%s", tail)
	}
}

// ─── CloudWatch ordering, including the crash path ───────────────────────────

// The handler's output must be in CloudWatch between START and END for the
// invocation it belongs to. Before the init this was the one thing the two
// transports could not agree on: END was written by the host the moment the
// response arrived, while the handler's line was still travelling through the
// daemon.
func TestInvoke_cloudWatchOrderingIsExact(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  console.log("ordered marker-order");
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "order-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "order-fn")

	resp := invokeFunction(t, srv, "order-fn", []byte("{}"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("invoke returned %d, want 200", resp.StatusCode)
	}

	messages := logEventsFor(t, srv, "/aws/lambda/order-fn", func(m []string) bool {
		return indexOfLine(m, "REPORT RequestId:") >= 0 && indexOfLine(m, "marker-order") >= 0
	})

	start := indexOfLine(messages, "START RequestId:")
	handler := indexOfLine(messages, "marker-order")
	end := indexOfLine(messages, "END RequestId:")
	report := indexOfLine(messages, "REPORT RequestId:")
	if !(start >= 0 && start < handler && handler < end && end < report) {
		t.Errorf("CloudWatch order is START=%d handler=%d END=%d REPORT=%d, want strictly increasing:\n%s",
			start, handler, end, report, strings.Join(messages, "\n"))
	}
}

// A handler that prints and then kills its own process is where the old drain
// was most exposed: the container is gone moments later, so anything still in
// the daemon's pipe was lost. The init drains, flushes and closes its stream
// when its child exits, so the host waits for an event rather than for silence.
func TestInvoke_crashedHandlersOutputLandsBeforeEndAndReport(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  console.log("dying words marker-crash");
  process.exit(1);
};
`)
	createFunctionWithCode(t, srv, "crash-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "crash-fn")

	resp := invokeFunction(t, srv, "crash-fn", []byte("{}"))
	resp.Body.Close()

	messages := logEventsFor(t, srv, "/aws/lambda/crash-fn", func(m []string) bool {
		return indexOfLine(m, "REPORT RequestId:") >= 0
	})

	handler := indexOfLine(messages, "marker-crash")
	end := indexOfLine(messages, "END RequestId:")
	report := indexOfLine(messages, "REPORT RequestId:")
	if handler < 0 {
		t.Fatalf("the crashing handler's last line never reached CloudWatch:\n%s", strings.Join(messages, "\n"))
	}
	if !(handler < end && end < report) {
		t.Errorf("order is handler=%d END=%d REPORT=%d, want the handler's line first:\n%s",
			handler, end, report, strings.Join(messages, "\n"))
	}
}

// ─── image functions ─────────────────────────────────────────────────────────

// buildLambdaImage builds a local image from the Lambda base image via the
// daemon API, so an image-function test needs no Docker CLI. It returns the
// tag.
func buildLambdaImage(t *testing.T, dockerfile string, files map[string]string) string {
	t.Helper()
	tag := fmt.Sprintf("overcast-test-lambda-image:%d", time.Now().UnixNano())

	var ctxTar bytes.Buffer
	tw := tar.NewWriter(&ctxTar)
	write := func(name, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("build context header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("build context write %s: %v", name, err)
		}
	}
	write("Dockerfile", dockerfile)
	for name, content := range files {
		write(name, content)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	client := daemonHTTPClient(t)
	req, err := http.NewRequest(http.MethodPost,
		"http://docker/v1.45/build?t="+tag+"&dockerfile=Dockerfile&rm=1&forcerm=1", bytes.NewReader(ctxTar.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("docker build: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || bytes.Contains(body, []byte(`"errorDetail"`)) {
		t.Fatalf("docker build failed (status %d):\n%s", resp.StatusCode, body)
	}

	t.Cleanup(func() {
		delReq, err := http.NewRequest(http.MethodDelete, "http://docker/v1.45/images/"+tag+"?force=1", nil)
		if err != nil {
			return
		}
		if delResp, err := client.Do(delReq); err == nil {
			_, _ = io.Copy(io.Discard, delResp.Body)
			delResp.Body.Close()
		}
	})
	return tag
}

// daemonHTTPClient talks to the Docker daemon over its Unix socket. The build
// endpoint is not on internal/docker.Client — nothing in Overcast builds images
// — so these tests speak to it directly. Unix only, which is where the
// Docker-gated tests run (helpers.SkipWithoutDocker gates them).
func daemonHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	socket := helpers.TestDockerSocket()
	return &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
}

// createImageFunction creates a PackageType=Image function.
func createImageFunction(t *testing.T, srv *helpers.TestServer, name, imageURI string, cfg *imageConfigReq) {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: name,
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Timeout:      30,
		MemorySize:   512,
		Code:         &lambdaCode{ImageUri: imageURI},
		ImageConfig:  cfg,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// An image function is where the init changed the most: before it, the daemon
// merged the image's own ENTRYPOINT and CMD into the create request, and the
// provisioning archive was not copied in at all. Now the init is the entrypoint
// and the original command is its argument list — so an ImageConfig entrypoint
// override has to reach the child, and the tail has to work exactly as it does
// for a zip function.
func TestInvoke_imageFunction_withImageConfigEntrypoint(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	image := buildLambdaImage(t, `FROM public.ecr.aws/lambda/nodejs:20
COPY app.js /var/task/app.js
CMD ["app.baked"]
`, map[string]string{"app.js": `
exports.baked = async () => ({ from: "the image's own CMD" });
exports.overridden = async () => {
  console.log("image handler marker-image");
  return { from: "the ImageConfig command" };
};
`})

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	createImageFunction(t, srv, "image-fn", image, &imageConfigReq{
		EntryPoint: []string{"/lambda-entrypoint.sh"},
		Command:    []string{"app.overridden"},
	})
	waitForFunctionActive(t, srv, "image-fn")

	tail := string(invokeForLogTail(t, srv, "image-fn", []byte("{}")))
	if !strings.Contains(tail, "image handler marker-image") {
		t.Errorf("the image function's tail does not carry its own output:\n%s", tail)
	}
	if !strings.Contains(tail, "START RequestId:") || !strings.Contains(tail, "REPORT RequestId:") {
		t.Errorf("the image function's tail is missing the platform lines:\n%s", tail)
	}
}

// With no ImageConfig at all, the child is the image's own ENTRYPOINT+CMD, read
// back from the daemon because the daemon can no longer merge them in.
func TestInvoke_imageFunction_usesTheImagesOwnEntrypoint(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	image := buildLambdaImage(t, `FROM public.ecr.aws/lambda/nodejs:20
COPY app.js /var/task/app.js
CMD ["app.baked"]
`, map[string]string{"app.js": `
exports.baked = async () => {
  console.log("baked handler marker-baked");
  return { ok: true };
};
`})

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	createImageFunction(t, srv, "image-baked-fn", image, nil)
	waitForFunctionActive(t, srv, "image-baked-fn")

	tail := string(invokeForLogTail(t, srv, "image-baked-fn", []byte("{}")))
	if !strings.Contains(tail, "baked handler marker-baked") {
		t.Errorf("the image's baked-in CMD did not run:\n%s", tail)
	}
}

// Extensions now start for container-image functions too — the init launches
// /opt/extensions itself, so the shell shim that only zip functions were given
// is gone. Their output is the container's output like any other, and it
// carries no [overcast-extension:<name>] prefix any more: the init tags the
// frame instead, which is what makes it an `extension` record for a Logs API
// subscriber (asserted against the sink in
// internal/services/lambda/container_logs_test.go).
func TestInvoke_imageFunction_startsExtensions(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	image := buildLambdaImage(t, `FROM public.ecr.aws/lambda/nodejs:20
COPY app.js /var/task/app.js
COPY ext.js /opt/extension-src/ext.js
COPY watcher.sh /opt/extensions/watcher
RUN chmod 0755 /opt/extensions/watcher
CMD ["app.handler"]
`, map[string]string{
		"app.js": `
exports.handler = async () => {
  console.log("handler beside an extension marker-ext");
  return { ok: true };
};
`,
		"watcher.sh": "#!/bin/sh\nexec /var/lang/bin/node /opt/extension-src/ext.js\n",
		// A minimal external extension: register, then long-poll for events the
		// way AWS requires, so the environment is reported ready. Node rather
		// than curl because the base image is guaranteed to have it.
		"ext.js": `
const http = require("http");
const [host, port] = process.env.AWS_LAMBDA_RUNTIME_API.split(":");

function call(method, path, headers, body) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host, port, method, path, headers }, res => {
      res.resume();
      res.on("end", () => resolve(res.headers));
    });
    req.on("error", reject);
    if (body) req.write(body);
    req.end();
  });
}

(async () => {
  console.log("extension up marker-extension-line");
  const headers = await call(
    "POST",
    "/2020-01-01/extension/register",
    { "Lambda-Extension-Name": "watcher", "Content-Type": "application/json" },
    JSON.stringify({ events: ["INVOKE", "SHUTDOWN"] }),
  );
  const id = headers["lambda-extension-identifier"];
  console.log("extension registered id=" + id);
  for (;;) {
    await call("GET", "/2020-01-01/extension/event/next", { "Lambda-Extension-Identifier": id });
  }
})().catch(err => console.error("extension failed: " + err));
`,
	})

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	createImageFunction(t, srv, "image-ext-fn", image, nil)
	waitForFunctionActive(t, srv, "image-ext-fn")

	tail := string(invokeForLogTail(t, srv, "image-ext-fn", []byte("{}")))
	if !strings.Contains(tail, "handler beside an extension marker-ext") {
		t.Errorf("the handler's own output is missing from the tail:\n%s", tail)
	}

	messages := logEventsFor(t, srv, "/aws/lambda/image-ext-fn", func(m []string) bool {
		return indexOfLine(m, "extension registered id=") >= 0
	})
	if indexOfLine(messages, "marker-extension-line") < 0 {
		t.Errorf("the extension's own stdout never reached CloudWatch:\n%s", strings.Join(messages, "\n"))
	}
	if indexOfLine(messages, "[overcast-extension:") >= 0 {
		t.Errorf("an extension line still carries the old prefix convention:\n%s", strings.Join(messages, "\n"))
	}
}

// ─── the seeded init volume ──────────────────────────────────────────────────

// The init is not copied into a container any more; it is mounted from a volume
// seeded once per Overcast process. Two things have to be true against a real
// daemon for that to work, and neither is worth assuming: an archive extracted
// into a *created but never started* container has to reach the volume mounted
// over its destination, and a function container has to be able to exec what is
// in that volume as its entrypoint.
//
// The second is proved by the invoke succeeding at all — the container's
// entrypoint is /var/overcast/init and nothing put it there but the volume. The
// first is proved by reading the volume back through a container that had no
// part in seeding it.
func TestInvoke_initIsMountedFromASeededVolume(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  console.log("ran from a volume-mounted init marker-vol");
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "vol-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "vol-fn")

	// The function runs, so its entrypoint was executable — from the volume.
	tail := string(invokeForLogTail(t, srv, "vol-fn", []byte("{}")))
	if !strings.Contains(tail, "ran from a volume-mounted init marker-vol") {
		t.Fatalf("the function did not run under an init mounted from a volume:\n%s", tail)
	}

	dc := dockerClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The volume exists, is ours, and is addressed by what is in it: the init's
	// own content hash and the architecture. This build's volume is therefore
	// found by name — "the first one carrying the label" is a different volume
	// on any daemon that has hosted more than one build of the init, which a
	// development machine does within an afternoon.
	volumes, err := dc.ListVolumes(ctx, "lambda")
	if err != nil {
		t.Fatalf("list lambda volumes: %v", err)
	}
	wantVolume := "overcast-lambda-init-" + initArtefactVersion(t, "amd64") + "-amd64"
	var initVolume string
	var labelled []string
	for _, v := range volumes {
		if v.Labels[docker.LabelLambdaInitVersion] == "" {
			continue
		}
		labelled = append(labelled, v.Name)
		if v.Name == wantVolume {
			initVolume = v.Name
		}
	}
	if initVolume == "" {
		t.Fatalf("no volume named %s; volumes carrying %s: %v", wantVolume, docker.LabelLambdaInitVersion, labelled)
	}
	if got := volumeVersionLabel(volumes, initVolume); got != initArtefactVersion(t, "amd64") {
		t.Errorf("%s = %q, want the artefact's content hash %q", docker.LabelLambdaInitVersion, got, initArtefactVersion(t, "amd64"))
	}

	// Read it back through a container that had nothing to do with seeding it.
	want := initArtefactSize(t, "amd64")
	reader, err := dc.CreateContainer(ctx, "overcast-test-init-volume-reader-"+strconv.FormatInt(time.Now().UnixNano(), 10),
		&docker.CreateContainerRequest{
			ContainerConfig: &docker.ContainerConfig{
				Image:      "public.ecr.aws/lambda/nodejs:20",
				Entrypoint: []string{"/bin/sh"},
				Cmd:        []string{"-c", "test -x /var/overcast/init && wc -c < /var/overcast/init"},
			},
			Platform: "linux/amd64",
			HostConfig: &docker.HostConfig{
				Mounts: []docker.Mount{{Type: "volume", Source: initVolume, Target: "/var/overcast", ReadOnly: true}},
			},
		})
	if err != nil {
		t.Fatalf("create the container that reads the init volume: %v", err)
	}
	defer func() { _ = dc.RemoveContainerForce(reader) }()
	if err := dc.StartContainer(ctx, reader); err != nil {
		t.Fatalf("start the reader: %v", err)
	}
	code2, err := dc.WaitContainer(ctx, reader)
	if err != nil {
		t.Fatalf("wait for the reader: %v", err)
	}
	raw, _ := dc.ContainerLogs(ctx, reader, "20")
	out := strings.TrimSpace(string(docker.DemuxStream(raw)))
	if code2 != 0 {
		t.Fatalf("the init in the volume is missing or not executable (exit %d): %s", code2, out)
	}
	if got, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err != nil {
		t.Fatalf("could not read the init's size from %q: %v", out, err)
	} else if got != want {
		t.Errorf("the volume holds %d bytes, want the %d-byte artefact — the seed is incomplete", got, want)
	}
}

// initArtefactPath is where the built init this checkout embeds lives.
func initArtefactPath(t *testing.T, goarch string) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "internal", "services", "lambda", "initbin", "dist", "lambda-init-linux-"+goarch)
}

// initArtefactVersion is the content hash the init's volume is addressed by —
// the same derivation initVolumeName uses, computed from the artefact rather
// than trusted from a label, which is what makes the assertion about *this*
// build's volume rather than whichever one the daemon listed first.
func initArtefactVersion(t *testing.T, goarch string) string {
	t.Helper()
	binary, err := os.ReadFile(initArtefactPath(t, goarch))
	if err != nil {
		t.Fatalf("read the init artefact: %v", err)
	}
	sum := sha256.Sum256(binary)
	return hex.EncodeToString(sum[:])[:12]
}

// volumeVersionLabel is one volume's init-version label, by name.
func volumeVersionLabel(volumes []docker.VolumeSummary, name string) string {
	for _, v := range volumes {
		if v.Name == name {
			return v.Labels[docker.LabelLambdaInitVersion]
		}
	}
	return ""
}

// initArtefactSize is the size of the built init this checkout embeds.
func initArtefactSize(t *testing.T, goarch string) int64 {
	t.Helper()
	info, err := os.Stat(initArtefactPath(t, goarch))
	if err != nil {
		t.Fatalf("stat the init artefact: %v", err)
	}
	return info.Size()
}

// ─── output that precedes an invocation ──────────────────────────────────────

// INIT-phase output belongs in front of the first invocation's START, which is
// where AWS puts it. It nearly did not: the init opens its log channel as the
// container starts, and while the host could only resolve that connection by
// the container's *address* — which Docker does not report until the container
// is running — the INIT frames queued behind a registration that had not landed
// and arrived after the host had already written START.
//
// A custom image ENTRYPOINT is the sharpest form of it: the line is printed
// before the runtime interface client even exists.
func TestInvoke_initPhaseOutputPrecedesTheFirstStart(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	image := buildLambdaImage(t, `FROM public.ecr.aws/lambda/nodejs:20
COPY app.js /var/task/app.js
COPY entry.sh /var/runtime/custom-entry.sh
RUN chmod 0755 /var/runtime/custom-entry.sh
ENTRYPOINT ["/var/runtime/custom-entry.sh"]
CMD ["app.handler"]
`, map[string]string{
		"app.js": `
exports.handler = async () => {
  console.log("handler ran marker-handler");
  return { ok: true };
};
`,
		"entry.sh": "#!/bin/sh\necho \"custom-entrypoint-ran marker-init\"\nexec /lambda-entrypoint.sh \"$@\"\n",
	})

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	createImageFunction(t, srv, "init-order-fn", image, nil)
	waitForFunctionActive(t, srv, "init-order-fn")

	tail := string(invokeForLogTail(t, srv, "init-order-fn", []byte("{}")))
	if !strings.Contains(tail, "handler ran marker-handler") {
		t.Fatalf("the function did not run:\n%s", tail)
	}
	// The entrypoint's line belongs to no invocation, so it is in no tail.
	if strings.Contains(tail, "marker-init") {
		t.Errorf("the INIT-phase line leaked into the invocation's tail:\n%s", tail)
	}

	messages := logEventsFor(t, srv, "/aws/lambda/init-order-fn", func(m []string) bool {
		return indexOfLine(m, "REPORT RequestId:") >= 0 && indexOfLine(m, "marker-init") >= 0
	})
	initLine := indexOfLine(messages, "marker-init")
	start := indexOfLine(messages, "START RequestId:")
	handler := indexOfLine(messages, "marker-handler")
	if initLine < 0 {
		t.Fatalf("the entrypoint's line never reached CloudWatch:\n%s", strings.Join(messages, "\n"))
	}
	if initLine > start {
		t.Errorf("the INIT-phase line is at %d and START at %d — INIT output must come first:\n%s",
			initLine, start, strings.Join(messages, "\n"))
	}
	if !(start < handler) {
		t.Errorf("START is at %d and the handler's line at %d:\n%s", start, handler, strings.Join(messages, "\n"))
	}
}

// A handler that prints after it has answered belongs to no invocation: not to
// the one that started the work, which has already been reported, and not to
// the next one, which had not begun. It reaches CloudWatch, and it reaches
// neither tail.
func TestInvoke_outputAfterTheResponseBelongsToNoInvocation(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async (event) => {
  const n = event.n;
  setTimeout(() => console.log("printed after the response marker-late-" + n), 40);
  console.log("in the handler marker-in-" + n);
  return { ok: true };
};
`)
	createFunctionWithCode(t, srv, "late-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "late-fn")

	first := string(invokeForLogTail(t, srv, "late-fn", []byte(`{"n":1}`)))
	// Long enough for the timer to fire while the environment is idle.
	time.Sleep(500 * time.Millisecond)
	second := string(invokeForLogTail(t, srv, "late-fn", []byte(`{"n":2}`)))

	if !strings.Contains(first, "in the handler marker-in-1") {
		t.Errorf("the first tail is missing its own output:\n%s", first)
	}
	if !strings.Contains(second, "in the handler marker-in-2") {
		t.Errorf("the second tail is missing its own output:\n%s", second)
	}
	// The line printed after the first invocation answered is in neither tail:
	// it was written when no invocation was in flight.
	if strings.Contains(first, "marker-late-1") {
		t.Errorf("output printed after the response landed in its own invocation's tail:\n%s", first)
	}
	if strings.Contains(second, "marker-late-1") {
		t.Errorf("output printed between invocations landed in the next invocation's tail:\n%s", second)
	}

	// It is not lost, though — it reaches CloudWatch like any other line.
	messages := logEventsFor(t, srv, "/aws/lambda/late-fn", func(m []string) bool {
		return indexOfLine(m, "marker-late-1") >= 0 && indexOfLine(m, "marker-in-2") >= 0
	})
	if indexOfLine(messages, "marker-late-1") < 0 {
		t.Errorf("the line printed between invocations never reached CloudWatch:\n%s", strings.Join(messages, "\n"))
	}
}
