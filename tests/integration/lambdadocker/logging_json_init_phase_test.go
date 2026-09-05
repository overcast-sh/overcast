package lambdadocker_test

// logging_json_init_phase_test.go — the init-phase platform records, end to
// end.
//
// platform.initStart, platform.initRuntimeDone and platform.initReport are
// sourced from the in-container init (internal/lambdainit), which is PID 1 in
// the execution environment and the proxy in front of the Runtime API. It
// publishes them on the same seq-ordered stream as the container's output, so
// what these tests pin is the thing that arrangement buys: the records land in
// CloudWatch in the position AWS puts them in, around output the host never
// sees on any other channel.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// initPhaseHandler prints one line while the module is loading — inside the
// INIT phase, before any invocation exists — and one from the handler.
const initPhaseHandler = `
process.stdout.write("init-phase-marker\n");
exports.handler = async () => {
  process.stdout.write("handler-marker\n");
  return { ok: true };
};
`

func TestInvoke_jsonLogFormatInitPhaseRecords(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	// Given: a function logging JSON at the system level that shows an
	// on-demand cold start's init records (all three are DEBUG there).
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", initPhaseHandler)
	createFunctionWithCodeAndLogging(t, srv, "json-init-fn", code,
		map[string]any{"LogFormat": "JSON", "SystemLogLevel": "DEBUG"})
	waitForFunctionActive(t, srv, "json-init-fn")

	// When: it is invoked, cold
	resp := invokeFunction(t, srv, "json-init-fn", map[string]any{})
	payload, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if fnErr := resp.Header.Get("X-Amz-Function-Error"); fnErr != "" {
		t.Fatalf("unexpected function error %q: %s", fnErr, payload)
	}

	// Then: all three init records are in CloudWatch. platform.report is the
	// last record of the invocation, so once it is there every earlier record
	// has been decided; the handler's own line settles the async batcher too.
	messages := waitForLogMessages(t, srv, "/aws/lambda/json-init-fn", map[string]func(string) bool{
		"platform.report record": isPlatformEvent("platform.report"),
		"handler output":         hasSubstring("handler-marker"),
	})

	startEvent, startRecord := decodePlatformEvent(t, messages, "platform.initStart")
	assertPlatformEnvelope(t, startEvent, "platform.initStart")
	if got := stringField(t, startRecord, "initializationType"); got != "on-demand" {
		t.Errorf("platform.initStart initializationType = %q, want %q", got, "on-demand")
	}
	if got := stringField(t, startRecord, "phase"); got != "init" {
		t.Errorf("platform.initStart phase = %q, want %q", got, "init")
	}
	if got := stringField(t, startRecord, "functionName"); got != "json-init-fn" {
		t.Errorf("platform.initStart functionName = %q, want the function's name", got)
	}
	if got := stringField(t, startRecord, "functionVersion"); got != "$LATEST" {
		t.Errorf("platform.initStart functionVersion = %q, want %q", got, "$LATEST")
	}

	doneEvent, doneRecord := decodePlatformEvent(t, messages, "platform.initRuntimeDone")
	assertPlatformEnvelope(t, doneEvent, "platform.initRuntimeDone")
	if got := stringField(t, doneRecord, "status"); got != "success" {
		t.Errorf("platform.initRuntimeDone status = %q, want %q", got, "success")
	}

	reportEvent, reportRecord := decodePlatformEvent(t, messages, "platform.initReport")
	assertPlatformEnvelope(t, reportEvent, "platform.initReport")
	if got := stringField(t, reportRecord, "status"); got != "success" {
		t.Errorf("platform.initReport status = %q, want %q", got, "success")
	}
	if got := stringField(t, reportRecord, "phase"); got != "init" {
		t.Errorf("platform.initReport phase = %q, want %q", got, "init")
	}
	metrics, ok := reportRecord["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("platform.initReport metrics = %#v, want an object", reportRecord["metrics"])
	}
	if got := numberField(t, metrics, "durationMs"); got <= 0 {
		t.Errorf("platform.initReport durationMs = %v, want a real measurement", got)
	}

	// And: the ordering the frame stream exists to guarantee. The INIT phase's
	// own output is between initStart and the pair that closes the phase, and
	// the invocation's START follows all of it.
	initStart := indexOfPlatformEvent(messages, "platform.initStart")
	initOutput := indexOfLine(messages, "init-phase-marker")
	initRuntimeDone := indexOfPlatformEvent(messages, "platform.initRuntimeDone")
	initReport := indexOfPlatformEvent(messages, "platform.initReport")
	start := indexOfPlatformEvent(messages, "platform.start")
	if initOutput < 0 {
		t.Fatalf("the INIT phase's own output never reached CloudWatch:\n%s", strings.Join(messages, "\n"))
	}
	ordered := []struct {
		what string
		at   int
	}{
		{"platform.initStart", initStart},
		{"the INIT phase's output", initOutput},
		{"platform.initRuntimeDone", initRuntimeDone},
		{"platform.initReport", initReport},
		{"platform.start", start},
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].at >= ordered[i].at {
			t.Errorf("%s is at %d and %s at %d — the init phase must be reported around its own output, and before START:\n%s",
				ordered[i-1].what, ordered[i-1].at, ordered[i].what, ordered[i].at, strings.Join(messages, "\n"))
		}
	}
}

// Text mode writes none of them. AWS's plain-text counterpart of
// platform.initStart is an INIT_START line whose whole content is a managed
// runtime's version and that version's ARN — neither of which Overcast has —
// and the other two records have no plain-text counterpart at all. What Text
// mode does keep is exactly what it kept before: the container's own output,
// and the START / END / REPORT lines.
func TestInvoke_textLogFormatWritesNoInitPhaseRecords(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", initPhaseHandler)
	createFunctionWithCode(t, srv, "text-init-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "text-init-fn")

	resp := invokeFunction(t, srv, "text-init-fn", map[string]any{})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	messages := waitForLogMessages(t, srv, "/aws/lambda/text-init-fn", map[string]func(string) bool{
		"REPORT line":    matchesPattern(textReportLine),
		"handler output": hasSubstring("handler-marker"),
	})
	for _, eventType := range []string{"platform.initStart", "platform.initRuntimeDone", "platform.initReport"} {
		if containsMessage(messages, isPlatformEvent(eventType)) {
			t.Errorf("Text log format wrote a %s record:\n%s", eventType, strings.Join(messages, "\n"))
		}
	}
	if indexOfLine(messages, "init-phase-marker") < 0 {
		t.Errorf("Text mode lost the INIT phase's own output:\n%s", strings.Join(messages, "\n"))
	}
	if !containsMessage(messages, matchesPattern(textStartLine)) {
		t.Errorf("Text mode lost its START line:\n%s", strings.Join(messages, "\n"))
	}
}

// indexOfPlatformEvent is indexOfLine for a platform record: it matches on the
// decoded event type rather than on a substring, so a line that merely mentions
// one — an extension echoing what it was sent, say — is not mistaken for it.
func indexOfPlatformEvent(messages []string, eventType string) int {
	for i, msg := range messages {
		if platformEventType(msg) == eventType {
			return i
		}
	}
	return -1
}
