package lambdadocker_test

// telemetry_init_phase_test.go — what an extension subscribed to the Telemetry
// API sees of the INIT phase.
//
// The awkward part of these three records is *when* they happen.
// platform.initStart is published before the extensions are even started, and
// the pair that closes the phase can beat an extension that is still
// registering — yet an extension's whole job is to be told about the phase it
// was started in, and AWS delivers them. So the records are held per
// environment and replayed to a subscription made during INIT.
//
// The function here logs in Text format on purpose: the CloudWatch copy of
// these records exists only under LogFormat: JSON, and a subscriber's copy is
// not affected by the log format at all. AWS: "Configuring the format of the
// system logs Lambda sends to CloudWatch doesn't affect Lambda Telemetry API
// behavior."

import (
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// initPhaseRecordTypes are the three records AWS reports for an execution
// environment's INIT phase, in the order they happen.
var initPhaseRecordTypes = []string{"platform.initStart", "platform.initRuntimeDone", "platform.initReport"}

// skipIfHostCannotReachContainerIPs skips a test whose subject is a delivery
// Overcast makes from this process *into* a container, addressed by the
// container's own bridge IP.
//
// The Telemetry API is the only such path. Its destination is a listener the
// extension stands up inside the sandbox and subscribes as
// "http://127.0.0.1:<port>"; lambda.normalizeExtensionLogURI rewrites the
// loopback to the container's IP, because from the emulator's side that is
// where the listener is, and RuntimeAPIServer posts the records there.
//
// That holds whenever this process shares a network with the containers — a
// daemon on this kernel, or an Overcast that is itself containerised — and it
// cannot hold on Docker Desktop, whose engine runs inside a VM the host has no
// route into. Measured on this machine (Windows 11, Docker Desktop 29.7.2,
// engine linux/amd64): an alpine container listening on 9999, dialled from the
// host at the bridge IP the daemon reports for it, times out; the emulator's
// own attempt in this test failed the same way, three times, and logged
// "dropping a telemetry delivery — its destination failed every attempt".
//
// The missing capability is host-to-container-IP routing, and it is the host's
// rather than the test's — which is why this skips rather than working around
// it. What it is not is harmless: an extension that subscribes to the Telemetry
// API receives nothing on Docker Desktop, so on most developer machines that
// AWS surface is inert. The emulator is honest about it in a warning, but a
// warning is not a delivery, and closing the gap needs a delivery path that
// already reaches inside the container — the in-container init has one
// (internal/lambdainit/proxy_linux.go) and nothing wires telemetry to it.
//
// containerendpoint answers both halves of "can this process reach a container
// IP" and is the emulator's own answer to it, so the gate cannot drift from
// what the delivery actually does. It is deliberately not a runtime.GOOS check:
// a Linux host running Docker Desktop has the same VM boundary, and a Windows
// process inside a container that shares the daemon's network does not — GOOS
// answers neither.
func skipIfHostCannotReachContainerIPs(t *testing.T) {
	t.Helper()

	// TODO(priority:P2): deliver Telemetry API records through the in-container init, not from the host
	// RuntimeAPIServer posts an extension's telemetry destination at the container's bridge IP
	// (lambda.normalizeExtensionLogURI). Docker Desktop's engine is in a VM with no route from the host, so
	// every delivery times out and extension subscriptions are inert on Windows and macOS — most developer
	// machines. The in-container init already proxies the Runtime API inside the sandbox
	// (internal/lambdainit/proxy_linux.go); carrying telemetry deliveries over that channel would work on every
	// platform. Delete skipIfHostCannotReachContainerIPs in tests/integration/lambdadocker when it does.
	if containerendpoint.RunningInContainer() {
		return
	}
	dc := docker.NewClient(helpers.TestDockerSocket(), zap.NewNop())
	if containerendpoint.NativeLinuxDaemon(t.Context(), dc) {
		return
	}
	t.Skipf("skipping: this host cannot route to a container's bridge IP — the Docker daemon at %s is not on "+
		"this kernel (Docker Desktop runs it in a VM) and this process is not itself containerised, so the "+
		"Telemetry API destination the extension stands up inside the sandbox is unreachable from here. "+
		"Docker itself is available; see this function's comment", helpers.TestDockerSocket())
}

func TestInvoke_extensionSubscribedToTelemetryReceivesTheInitPhaseRecords(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	skipIfHostCannotReachContainerIPs(t)
	requireLambdaInit(t)

	image := buildLambdaImage(t, `FROM public.ecr.aws/lambda/nodejs:20
COPY app.js /var/task/app.js
COPY ext.js /opt/extension-src/ext.js
COPY collector.sh /opt/extensions/collector
RUN chmod 0755 /opt/extensions/collector
CMD ["app.handler"]
`, map[string]string{
		"app.js": `
exports.handler = async () => {
  console.log("handler ran marker-handler");
  return { ok: true };
};
`,
		"collector.sh": "#!/bin/sh\nexec /var/lang/bin/node /opt/extension-src/ext.js\n",
		// A minimal Telemetry API consumer: register, stand up the destination
		// the records will be POSTed to, subscribe to the platform stream, then
		// long-poll for events the way AWS requires so the environment is
		// reported ready. Everything it is sent it prints, one line per record,
		// which is how the test gets to see it — an extension's stdout is an
		// `extension` record and reaches CloudWatch like any other output.
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
  const headers = await call(
    "POST",
    "/2020-01-01/extension/register",
    { "Lambda-Extension-Name": "collector", "Content-Type": "application/json" },
    JSON.stringify({ events: ["INVOKE", "SHUTDOWN"] }),
  );
  const id = headers["lambda-extension-identifier"];

  const server = http.createServer((req, res) => {
    let body = "";
    req.on("data", chunk => { body += chunk; });
    req.on("end", () => {
      res.writeHead(200);
      res.end();
      try {
        for (const event of JSON.parse(body)) {
          console.log("TELEMETRY " + event.type + " " + JSON.stringify(event.record));
        }
      } catch (err) {
        console.log("TELEMETRY undecodable " + err);
      }
    });
  });
  await new Promise(resolve => server.listen(9999, "0.0.0.0", resolve));

  // Subscribed via the Telemetry API — the endpoint AWS documents as
  // superseding the Logs API and the one current observability extensions
  // call — so this test drives the modern surface end to end; the Logs API
  // path keeps its own coverage at the unit level.
  await call(
    "PUT",
    "/2022-07-01/telemetry",
    { "Lambda-Extension-Identifier": id, "Content-Type": "application/json" },
    JSON.stringify({
      schemaVersion: "2022-12-13",
      types: ["platform"],
      buffering: { timeoutMs: 25, maxBytes: 262144, maxItems: 1000 },
      destination: { protocol: "HTTP", URI: "http://127.0.0.1:9999" },
    }),
  );
  console.log("collector subscribed marker-subscribed");

  for (;;) {
    await call("GET", "/2020-01-01/extension/event/next", { "Lambda-Extension-Identifier": id });
  }
})().catch(err => console.error("collector failed: " + err));
`,
	})

	// The emulator's own warnings are the only place a delivery it gave up on
	// is reported — a subscriber cannot tell a record it never received from
	// one that was never sent. Kept and printed only when this test fails, so a
	// CI occurrence says which of the two happened instead of leaving it to be
	// guessed at (#1437).
	core, observed := observer.New(zap.WarnLevel)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, entry := range observed.All() {
			t.Logf("server %s: %s %v", entry.Level, entry.Message, entry.ContextMap())
		}
	})

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker(), helpers.WithLogger(zap.New(core)))
	createImageFunction(t, srv, "telemetry-init-fn", image, nil)
	waitForFunctionActive(t, srv, "telemetry-init-fn")

	tail := string(invokeForLogTail(t, srv, "telemetry-init-fn", []byte("{}")))
	if !strings.Contains(tail, "handler ran marker-handler") {
		t.Fatalf("the function did not run:\n%s", tail)
	}
	// A platform record never enters a request's log buffer, so none of them is
	// in the tail. The extension's own printed echo of one is a different
	// thing: that is container output written while the invocation was in
	// flight, and it belongs to the invocation exactly as it does on AWS.
	for _, line := range strings.Split(tail, "\n") {
		if strings.HasPrefix(line, `{"time":"`) && strings.Contains(line, `"type":"platform.`) {
			t.Errorf("a platform record leaked into X-Amz-Log-Result: %q\n%s", line, tail)
		}
	}

	// Wait for all three, not for the last of them. A batch keeps its own
	// records in order, but the phase does not arrive as one batch — the
	// replay a subscription is opened with and the pair the first GET /next
	// publishes are separate deliveries, and separate deliveries are posted by
	// a pool of workers that share no ordering. So "initReport has arrived"
	// says nothing about the records published before it, and waiting on one
	// of the three to then assert the other two were already there is a race —
	// the one this test lost in #1437. Waiting for all three is also what
	// separates late from lost: a record the emulator gave up on is still
	// missing when the budget runs out, and the dump names what did arrive.
	messages := logEventsFor(t, srv, "/aws/lambda/telemetry-init-fn", func(m []string) bool {
		for _, eventType := range initPhaseRecordTypes {
			if indexOfLine(m, "TELEMETRY "+eventType+" ") < 0 {
				return false
			}
		}
		return true
	})

	// The records the extension was handed are the AWS shapes, not strings of
	// them: it parsed each event's `record` as an object to print it.
	if record := telemetryRecord(t, messages, "platform.initStart"); record != nil {
		if record["initializationType"] != "on-demand" || record["phase"] != "init" {
			t.Errorf("the initStart record the extension received = %#v", record)
		}
		if record["functionName"] != "telemetry-init-fn" || record["functionVersion"] != "$LATEST" {
			t.Errorf("the initStart record the extension received = %#v", record)
		}
	}
	if record := telemetryRecord(t, messages, "platform.initReport"); record != nil {
		if record["status"] != "success" {
			t.Errorf("the initReport record the extension received = %#v", record)
		}
		metrics, _ := record["metrics"].(map[string]any)
		duration, _ := metrics["durationMs"].(float64)
		if duration <= 0 {
			t.Errorf("the initReport record carries durationMs %#v, want a real measurement", metrics["durationMs"])
		}
	}

	// This function logs in Text format, so none of it is in CloudWatch as a
	// platform record — only the extension's own echo of what it was sent.
	for _, msg := range messages {
		if strings.HasPrefix(msg, `{"time":"`) && strings.Contains(msg, `"type":"platform.init`) {
			t.Errorf("Text log format wrote an init-phase record to CloudWatch: %q", msg)
		}
	}
}

// telemetryRecord decodes the `record` object out of the extension's echo of
// one Telemetry API event.
func telemetryRecord(t *testing.T, messages []string, eventType string) map[string]any {
	t.Helper()
	prefix := "TELEMETRY " + eventType + " "
	for _, msg := range messages {
		at := strings.Index(msg, prefix)
		if at < 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(msg[at+len(prefix):]), &record); err != nil {
			t.Errorf("the %s record the extension printed is not an object: %q", eventType, msg)
			return nil
		}
		return record
	}
	return nil
}
