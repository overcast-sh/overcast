package lambdadocker_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// logging_json_invoke_test.go covers Lambda's advanced logging controls end to
// end, through a real container invoke: what a function configured for the JSON
// log format actually writes to CloudWatch Logs, and what each of the two log
// levels filters out of it.
//
// The handlers here write their application records with process.stdout.write
// rather than console.log on purpose. The managed Node runtime reads
// AWS_LAMBDA_LOG_FORMAT and structures console output itself — which is real
// AWS behaviour and is exercised alongside — but a record it produced would
// leave it ambiguous whether Overcast filtered anything. A raw stdout write
// reaches the log stream untouched, so what happens to it is Overcast's own
// ApplicationLogLevel filtering and nothing else.
//
// Every assertion about a record's *absence* is made only after a later record
// from the same ordered stream has been observed, so "not there" can never mean
// "not there yet".

// ─── helpers ─────────────────────────────────────────────────────────────────

// createFunctionWithCodeAndLogging creates a function with a real code zip and
// an explicit LoggingConfig.
func createFunctionWithCodeAndLogging(t *testing.T, srv *helpers.TestServer, name string, zipBytes []byte, logging map[string]any) {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
		"FunctionName":  name,
		"Runtime":       "nodejs20.x",
		"Handler":       "index.handler",
		"Role":          "arn:aws:iam::000000000000:role/lambda-role",
		"Timeout":       10,
		"MemorySize":    128,
		"Code":          map[string]any{"ZipFile": zipBytes},
		"LoggingConfig": logging,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// filterLogMessages returns every message currently in a log group, across all
// of its streams.
func filterLogMessages(t *testing.T, srv *helpers.TestServer, group string) []string {
	t.Helper()
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
		return nil
	}
	var result struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode FilterLogEvents response %s: %v", raw, err)
	}
	messages := make([]string, 0, len(result.Events))
	for _, e := range result.Events {
		messages = append(messages, e.Message)
	}
	return messages
}

// waitForLogMessages polls a log group until every one of matches has found a
// message, then returns the whole group.
//
// Callers name the *last* record of each stream their scenario cares about,
// which is what makes a later assertion of absence sound. Two streams have to
// be named separately because they reach CloudWatch by different routes: the
// platform records are written synchronously by the execution environment,
// while the function's own output travels the async batcher, so neither one
// arriving proves anything about the other.
func waitForLogMessages(t *testing.T, srv *helpers.TestServer, group string, matches map[string]func(string) bool) []string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var messages []string
	for time.Now().Before(deadline) {
		messages = filterLogMessages(t, srv, group)
		missing := ""
		for what, match := range matches {
			if !containsMessage(messages, match) {
				missing = what
				break
			}
		}
		if missing == "" {
			return messages
		}
		time.Sleep(100 * time.Millisecond)
	}
	for what, match := range matches {
		if !containsMessage(messages, match) {
			t.Errorf("no %s in log group %q within 30s", what, group)
		}
	}
	t.Fatalf("log group %q held %d messages:\n%s", group, len(messages), strings.Join(messages, "\n"))
	return nil
}

func containsMessage(messages []string, match func(string) bool) bool {
	for _, msg := range messages {
		if match(msg) {
			return true
		}
	}
	return false
}

func hasSubstring(want string) func(string) bool {
	return func(msg string) bool { return strings.Contains(msg, want) }
}

// platformEventType returns the Telemetry API event type of a message, or "" if
// the message is not a platform record.
func platformEventType(msg string) string {
	if !strings.HasPrefix(msg, "{") {
		return ""
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(msg), &event); err != nil {
		return ""
	}
	if !strings.HasPrefix(event.Type, "platform.") {
		return ""
	}
	return event.Type
}

func isPlatformEvent(eventType string) func(string) bool {
	return func(msg string) bool { return platformEventType(msg) == eventType }
}

// decodePlatformEvent decodes the named platform record out of a set of
// messages as untyped JSON, so the test can assert on the JSON types of every
// field rather than on whatever Go types a struct would have coerced them to.
func decodePlatformEvent(t *testing.T, messages []string, eventType string) (event, record map[string]any) {
	t.Helper()
	for _, msg := range messages {
		if platformEventType(msg) != eventType {
			continue
		}
		if err := json.Unmarshal([]byte(msg), &event); err != nil {
			t.Fatalf("decode %s record %q: %v", eventType, msg, err)
		}
		record, ok := event["record"].(map[string]any)
		if !ok {
			t.Fatalf("%s record is not an object: %q", eventType, msg)
		}
		return event, record
	}
	t.Fatalf("no %s record in:\n%s", eventType, strings.Join(messages, "\n"))
	return nil, nil
}

// assertPlatformEnvelope checks the two fields every platform record carries:
// the millisecond-precision UTC timestamp and the event type.
func assertPlatformEnvelope(t *testing.T, event map[string]any, eventType string) {
	t.Helper()
	if event["type"] != eventType {
		t.Errorf("type = %#v, want %q", event["type"], eventType)
	}
	stamp, ok := event["time"].(string)
	if !ok {
		t.Fatalf("time = %#v, want a string", event["time"])
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", stamp); err != nil {
		t.Errorf("time %q is not AWS's millisecond-precision UTC form: %v", stamp, err)
	}
}

// numberField asserts that a record member is a JSON number and returns it. A
// string that happens to hold digits is a wire-format regression, not a pass.
func numberField(t *testing.T, record map[string]any, key string) float64 {
	t.Helper()
	value, ok := record[key].(float64)
	if !ok {
		t.Fatalf("%s = %#v, want a JSON number", key, record[key])
	}
	return value
}

func stringField(t *testing.T, record map[string]any, key string) string {
	t.Helper()
	value, ok := record[key].(string)
	if !ok {
		t.Fatalf("%s = %#v, want a JSON string", key, record[key])
	}
	return value
}

// The plain-text records, byte for byte. Text mode has to keep producing
// exactly these: they are what every log parser written against Lambda reads,
// and they are the thing JSON mode replaces.
var (
	textStartLine  = regexp.MustCompile(`^START RequestId: [0-9a-f-]{36} Version: \$LATEST$`)
	textEndLine    = regexp.MustCompile(`^END RequestId: [0-9a-f-]{36}$`)
	textReportLine = regexp.MustCompile(`^REPORT RequestId: [0-9a-f-]{36}\tDuration: \d+\.\d{2} ms\tBilled Duration: \d+ ms\tMemory Size: 128 MB\tMax Memory Used: \d+ MB(\tInit Duration: \d+\.\d{2} ms)?$`)
)

func matchesPattern(re *regexp.Regexp) func(string) bool {
	return func(msg string) bool { return re.MatchString(msg) }
}

// applicationRecordHandler writes three application records straight to stdout,
// bypassing the managed runtime's own console formatting. They are emitted in
// filtering order — the two that a level above INFO drops first, the ERROR
// record that survives every level below FATAL last — so waiting on the last
// one settles the fate of the first two.
const applicationRecordHandler = `
exports.handler = async () => {
  process.stdout.write(JSON.stringify({level: "INFO", message: "app-structured-info"}) + "\n");
  process.stdout.write("app-unstructured-line\n");
  process.stdout.write(JSON.stringify({level: "ERROR", message: "app-structured-error"}) + "\n");
  return { ok: true };
};
`

// ─── platform records ────────────────────────────────────────────────────────

func TestInvoke_jsonLogFormatPlatformRecords(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given: a function configured for the JSON log format
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => {
  console.log("app-console-line");
  return { ok: true };
};
`)
	createFunctionWithCodeAndLogging(t, srv, "json-platform-fn", code, map[string]any{"LogFormat": "JSON"})
	waitForFunctionActive(t, srv, "json-platform-fn")

	// When: it is invoked
	resp := invokeFunction(t, srv, "json-platform-fn", map[string]any{})
	payload, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if fnErr := resp.Header.Get("X-Amz-Function-Error"); fnErr != "" {
		t.Fatalf("unexpected function error %q: %s", fnErr, payload)
	}

	// Then: the invocation's platform records reach CloudWatch as Telemetry-API
	// shaped JSON. platform.report is the last record of an invocation, so once
	// it is visible every other platform record has been decided.
	messages := waitForLogMessages(t, srv, "/aws/lambda/json-platform-fn", map[string]func(string) bool{
		"platform.report record": isPlatformEvent("platform.report"),
	})

	startEvent, startRecord := decodePlatformEvent(t, messages, "platform.start")
	assertPlatformEnvelope(t, startEvent, "platform.start")
	requestID := stringField(t, startRecord, "requestId")
	if requestID == "" {
		t.Error("platform.start carries an empty requestId")
	}
	if got := stringField(t, startRecord, "version"); got != "$LATEST" {
		t.Errorf("platform.start version = %q, want %q", got, "$LATEST")
	}

	reportEvent, reportRecord := decodePlatformEvent(t, messages, "platform.report")
	assertPlatformEnvelope(t, reportEvent, "platform.report")
	if got := stringField(t, reportRecord, "requestId"); got != requestID {
		t.Errorf("platform.report requestId = %q, want the invocation's %q", got, requestID)
	}
	if got := stringField(t, reportRecord, "status"); got != "success" {
		t.Errorf("platform.report status = %q, want %q", got, "success")
	}
	metrics, ok := reportRecord["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("platform.report metrics = %#v, want an object", reportRecord["metrics"])
	}
	numberField(t, metrics, "durationMs")
	numberField(t, metrics, "billedDurationMs")
	numberField(t, metrics, "maxMemoryUsedMB")
	if got := numberField(t, metrics, "memorySizeMB"); got != 128 {
		t.Errorf("platform.report memorySizeMB = %v, want the configured 128", got)
	}
	// This is the environment's first invocation, so the report carries the
	// cold start's init duration — the one metric that appears once and never
	// again for the same execution environment.
	numberField(t, metrics, "initDurationMs")

	// And: none of the plain-text records JSON mode replaces are also written.
	for _, msg := range messages {
		if strings.HasPrefix(msg, "START RequestId:") || strings.HasPrefix(msg, "END RequestId:") ||
			strings.HasPrefix(msg, "REPORT RequestId:") {
			t.Errorf("JSON log format emitted a plain-text platform line: %q", msg)
		}
	}

	// And: the managed runtime structured its own console output, because
	// AWS_LAMBDA_LOG_FORMAT reached the container.
	withConsole := waitForLogMessages(t, srv, "/aws/lambda/json-platform-fn", map[string]func(string) bool{
		"console output": hasSubstring("app-console-line"),
	})
	if !containsMessage(withConsole, func(msg string) bool {
		return strings.Contains(msg, "app-console-line") && strings.Contains(msg, `"level":"INFO"`)
	}) {
		t.Errorf("console output was not structured by the runtime:\n%s", strings.Join(withConsole, "\n"))
	}
}

// ─── SystemLogLevel ──────────────────────────────────────────────────────────

func TestInvoke_jsonSystemLogLevelFiltersPlatformRecords(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	code := makeZip(t, "index.js", `
exports.handler = async () => ({ ok: true });
`)

	t.Run("default INFO suppresses runtimeDone", func(t *testing.T) {
		// Given: JSON logging with no explicit system level, i.e. AWS's INFO
		srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
		createFunctionWithCodeAndLogging(t, srv, "sys-default-fn", code, map[string]any{"LogFormat": "JSON"})
		waitForFunctionActive(t, srv, "sys-default-fn")

		// When: it is invoked
		resp := invokeFunction(t, srv, "sys-default-fn", map[string]any{})
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		// Then: start (INFO) and report (INFO on success) are delivered, but
		// runtimeDone is DEBUG on success and is filtered out.
		messages := waitForLogMessages(t, srv, "/aws/lambda/sys-default-fn", map[string]func(string) bool{
			"platform.report record": isPlatformEvent("platform.report"),
		})
		if !containsMessage(messages, isPlatformEvent("platform.start")) {
			t.Errorf("INFO system level dropped platform.start:\n%s", strings.Join(messages, "\n"))
		}
		if containsMessage(messages, isPlatformEvent("platform.runtimeDone")) {
			t.Errorf("INFO system level delivered the DEBUG-level platform.runtimeDone:\n%s",
				strings.Join(messages, "\n"))
		}
		// The init-phase records are DEBUG too for an on-demand cold start —
		// initStart because Overcast never sets runtimeVersion, initReport
		// because the environment is on-demand — so a default log stream is
		// complete without them, and lowering the level is what produces them
		// (TestInvoke_jsonLogFormatInitPhaseRecords).
		for _, eventType := range []string{"platform.initStart", "platform.initRuntimeDone", "platform.initReport"} {
			if containsMessage(messages, isPlatformEvent(eventType)) {
				t.Errorf("INFO system level delivered the DEBUG-level %s:\n%s",
					eventType, strings.Join(messages, "\n"))
			}
		}
	})

	t.Run("DEBUG delivers runtimeDone", func(t *testing.T) {
		// Given: the same function with SystemLogLevel=DEBUG
		srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
		createFunctionWithCodeAndLogging(t, srv, "sys-debug-fn", code,
			map[string]any{"LogFormat": "JSON", "SystemLogLevel": "DEBUG"})
		waitForFunctionActive(t, srv, "sys-debug-fn")

		// When: it is invoked
		resp := invokeFunction(t, srv, "sys-debug-fn", map[string]any{})
		payload, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		// Then: the DEBUG-level runtimeDone record is delivered, carrying the
		// status and response size the plain-text END line has nowhere to put.
		messages := waitForLogMessages(t, srv, "/aws/lambda/sys-debug-fn", map[string]func(string) bool{
			"platform.report record": isPlatformEvent("platform.report"),
		})
		doneEvent, doneRecord := decodePlatformEvent(t, messages, "platform.runtimeDone")
		assertPlatformEnvelope(t, doneEvent, "platform.runtimeDone")
		if got := stringField(t, doneRecord, "status"); got != "success" {
			t.Errorf("platform.runtimeDone status = %q, want %q", got, "success")
		}
		metrics, ok := doneRecord["metrics"].(map[string]any)
		if !ok {
			t.Fatalf("platform.runtimeDone metrics = %#v, want an object", doneRecord["metrics"])
		}
		numberField(t, metrics, "durationMs")
		if got := numberField(t, metrics, "producedBytes"); got != float64(len(payload)) {
			t.Errorf("platform.runtimeDone producedBytes = %v, want the %d-byte response", got, len(payload))
		}
	})
}

// ─── ApplicationLogLevel ─────────────────────────────────────────────────────

func TestInvoke_jsonApplicationLogLevelFiltersFunctionOutput(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	code := makeZip(t, "index.js", applicationRecordHandler)

	t.Run("ERROR keeps only the error record", func(t *testing.T) {
		// Given: JSON logging filtered to ERROR
		srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
		createFunctionWithCodeAndLogging(t, srv, "app-error-fn", code,
			map[string]any{"LogFormat": "JSON", "ApplicationLogLevel": "ERROR"})
		waitForFunctionActive(t, srv, "app-error-fn")

		// When: the handler writes an INFO record, an unstructured line and an
		// ERROR record, in that order
		resp := invokeFunction(t, srv, "app-error-fn", map[string]any{})
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		// Then: only the ERROR record survives. The unstructured line goes with
		// the INFO one: AWS assigns "log messages with no level" the level
		// INFO, which ERROR filters out — it is not exempt from filtering.
		messages := waitForLogMessages(t, srv, "/aws/lambda/app-error-fn", map[string]func(string) bool{
			"ERROR application record": hasSubstring("app-structured-error"),
		})
		if containsMessage(messages, hasSubstring("app-structured-info")) {
			t.Errorf("ApplicationLogLevel=ERROR delivered an INFO record:\n%s", strings.Join(messages, "\n"))
		}
		if containsMessage(messages, hasSubstring("app-unstructured-line")) {
			t.Errorf("ApplicationLogLevel=ERROR delivered an unstructured line, which AWS levels at INFO:\n%s",
				strings.Join(messages, "\n"))
		}
	})

	t.Run("default INFO keeps the unstructured line", func(t *testing.T) {
		// Given: JSON logging at AWS's default application level
		srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
		createFunctionWithCodeAndLogging(t, srv, "app-info-fn", code, map[string]any{"LogFormat": "JSON"})
		waitForFunctionActive(t, srv, "app-info-fn")

		// When: the same three records are written
		resp := invokeFunction(t, srv, "app-info-fn", map[string]any{})
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		// Then: all three are delivered — which is what pins the unstructured
		// line's assigned level at exactly INFO rather than something quieter.
		messages := waitForLogMessages(t, srv, "/aws/lambda/app-info-fn", map[string]func(string) bool{
			"ERROR application record": hasSubstring("app-structured-error"),
		})
		for _, want := range []string{"app-structured-info", "app-unstructured-line"} {
			if !containsMessage(messages, hasSubstring(want)) {
				t.Errorf("ApplicationLogLevel=INFO dropped %q:\n%s", want, strings.Join(messages, "\n"))
			}
		}
	})
}

// ─── Text mode regression fence ──────────────────────────────────────────────

func TestInvoke_textLogFormatPlainTextRecords(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given: a function left on the default Text log format
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", applicationRecordHandler)
	createFunctionWithCode(t, srv, "text-platform-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "text-platform-fn")

	// When: it is invoked
	resp := invokeFunction(t, srv, "text-platform-fn", map[string]any{})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the plain-text START / END / REPORT lines are unchanged, byte for
	// byte, and no JSON platform record appears alongside them.
	messages := waitForLogMessages(t, srv, "/aws/lambda/text-platform-fn", map[string]func(string) bool{
		"plain-text REPORT line":   matchesPattern(textReportLine),
		"ERROR application record": hasSubstring("app-structured-error"),
	})
	for name, pattern := range map[string]*regexp.Regexp{
		"START":  textStartLine,
		"END":    textEndLine,
		"REPORT": textReportLine,
	} {
		if !containsMessage(messages, matchesPattern(pattern)) {
			t.Errorf("no plain-text %s line matching %s:\n%s", name, pattern, strings.Join(messages, "\n"))
		}
	}
	for _, msg := range messages {
		if eventType := platformEventType(msg); eventType != "" {
			t.Errorf("Text log format emitted the JSON record %s: %q", eventType, msg)
		}
	}

	// And: Text mode filters nothing, so every application record is delivered
	// whatever level its content claims.
	for _, want := range []string{"app-structured-info", "app-unstructured-line", "app-structured-error"} {
		if !containsMessage(messages, hasSubstring(want)) {
			t.Errorf("Text log format dropped %q:\n%s", want, strings.Join(messages, "\n"))
		}
	}
}

// ─── warm environment retirement ─────────────────────────────────────────────

// TestUpdateFunctionConfiguration_loggingChangeRetiresWarmEnvironment pins the
// reason the logging configuration is part of functionInstanceIdentity: it
// reaches the container as environment variables, which are baked in when the
// execution environment is created. A warm container reused across the change
// would keep serving the old ones.
func TestUpdateFunctionConfiguration_loggingChangeRetiresWarmEnvironment(t *testing.T) {
	helpers.SkipWithoutDocker(t)

	// Given: a Text-mode function with a warm execution environment, whose
	// handler reports the logging variables its container was started with
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
exports.handler = async () => ({
  logFormat: process.env.AWS_LAMBDA_LOG_FORMAT || "unset",
  logLevel: process.env.AWS_LAMBDA_LOG_LEVEL || "unset",
});
`)
	createFunctionWithCode(t, srv, "logging-env-fn", "nodejs20.x", "index.handler", code)
	waitForFunctionActive(t, srv, "logging-env-fn")

	// The second invoke is served by the environment the first one created, so
	// there is definitely a warm container to retire.
	invokeLoggingEnv(t, srv, "logging-env-fn", "Text", "unset")
	invokeLoggingEnv(t, srv, "logging-env-fn", "Text", "unset")

	// When: the logging configuration changes
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/logging-env-fn/configuration"), map[string]any{
		"LoggingConfig": map[string]any{"LogFormat": "JSON", "ApplicationLogLevel": "ERROR"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	waitForFunctionActive(t, srv, "logging-env-fn")

	// Then: the next invocation runs in a new environment carrying the new
	// variables, rather than the warm one carrying the old ones.
	invokeLoggingEnv(t, srv, "logging-env-fn", "JSON", "ERROR")

	// And the same in reverse: AWS_LAMBDA_LOG_LEVEL is not set at all in Text
	// mode, so returning to Text has to retire the JSON environment too.
	resp = doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/logging-env-fn/configuration"), map[string]any{
		"LoggingConfig": map[string]any{"LogFormat": "Text"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	waitForFunctionActive(t, srv, "logging-env-fn")

	invokeLoggingEnv(t, srv, "logging-env-fn", "Text", "unset")
}

// invokeLoggingEnv invokes the logging-env handler and asserts the logging
// variables the serving container was started with.
func invokeLoggingEnv(t *testing.T, srv *helpers.TestServer, name, wantFormat, wantLevel string) {
	t.Helper()
	resp := invokeFunction(t, srv, name, map[string]any{})
	helpers.AssertStatus(t, resp, http.StatusOK)
	if fnErr := resp.Header.Get("X-Amz-Function-Error"); fnErr != "" {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("unexpected function error %q: %s", fnErr, body)
	}
	var got struct {
		LogFormat string `json:"logFormat"`
		LogLevel  string `json:"logLevel"`
	}
	decodeJSON(t, resp, &got)
	if got.LogFormat != wantFormat || got.LogLevel != wantLevel {
		t.Fatalf("container environment = (AWS_LAMBDA_LOG_FORMAT=%q, AWS_LAMBDA_LOG_LEVEL=%q), want (%q, %q)",
			got.LogFormat, got.LogLevel, wantFormat, wantLevel)
	}
}

// ─── CloudWatch ordering of the whole INIT-to-invoke arc ─────────────────────

// TestInvoke_initPhaseRecordsAreOrderedInCloudWatch pins the order the log
// stream tells the story in, under LogFormat JSON at SystemLogLevel DEBUG
// (the init-phase records are DEBUG, so the default INFO hides them):
//
//	platform.initStart → the INIT phase's own output → platform.initRuntimeDone
//	→ platform.initReport → platform.start → the handler's output
//	→ platform.runtimeDone → platform.report
//
// The records ride the init's sequence-ordered frame stream and the invoke
// path holds each synthesised record until the output it must follow has been
// ingested, so this order is a guarantee, not a race that usually comes out
// right.
func TestInvoke_initPhaseRecordsAreOrderedInCloudWatch(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	requireLambdaInit(t)

	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	code := makeZip(t, "index.js", `
process.stdout.write("during-init\n");
exports.handler = async () => {
  process.stdout.write("during-invoke\n");
  return { ok: true };
};
`)
	createFunctionWithCodeAndLogging(t, srv, "ordered-fn", code, map[string]any{
		"LogFormat":      "JSON",
		"SystemLogLevel": "DEBUG",
	})
	waitForFunctionActive(t, srv, "ordered-fn")
	resp := invokeFunction(t, srv, "ordered-fn", map[string]any{})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	messages := logEventsFor(t, srv, "/aws/lambda/ordered-fn", func(m []string) bool {
		return indexOfLine(m, `"type":"platform.report"`) >= 0
	})

	// Every boundary of the arc, in the order the platform emits it.
	arc := []string{
		`"type":"platform.initStart"`,
		"during-init",
		`"type":"platform.initRuntimeDone"`,
		`"type":"platform.initReport"`,
		`"type":"platform.start"`,
		"during-invoke",
		`"type":"platform.runtimeDone"`,
		`"type":"platform.report"`,
	}
	last := -1
	for _, want := range arc {
		at := indexOfLine(messages, want)
		if at < 0 {
			t.Fatalf("the log stream is missing %q:\n%s", want, strings.Join(messages, "\n"))
		}
		if at <= last {
			t.Fatalf("%q is at %d, before its predecessor at %d — the arc is out of order:\n%s",
				want, at, last, strings.Join(messages, "\n"))
		}
		last = at
	}
}
