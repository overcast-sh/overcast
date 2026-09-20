// Package logs_test contains integration tests for the CloudWatch Logs emulator.
//
// TDD contract: every handler in internal/services/logs/ must have a
// corresponding failing test here before implementation begins.
//
// Run: go test ./tests/integration/logs/...
package logs_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go/eventstream"
	"github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestStartLiveTail_opensWithInitialResponseFrame(t *testing.T) {
	// Given: a log group an AWS SDK for Go v2 client would tail
	srv := helpers.NewTestServer(t)
	groupName := "/aws/lambda/live-tail-initial-response"
	groupARN := "arn:aws:logs:us-east-1:000000000000:log-group:" + groupName
	createLogGroup(t, srv, groupName)

	// When: the session is opened under a deadline. For an awsjson1.1
	// event-stream operation the smithy-go runtime parks the deserializer on
	// `ir := <-eventReader.initialResponse` before it yields the stream, so a
	// session that opens with sessionStart deadlocks the client instead of
	// failing it (#1064). The deadline is what turns that hang back into a
	// test failure.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp := logsCallCtx(t, ctx, srv, "StartLiveTail", map[string]any{
		"logGroupIdentifiers": []string{groupARN},
	})
	defer resp.Body.Close()

	// Then: the response is an event stream whose very first frame is the
	// initial-response message carrying the operation's (empty) output
	// document. Decoded with the SDK's own decoder, so the CRCs are checked
	// the way a real client checks them.
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/vnd.amazon.eventstream")

	decoder := eventstream.NewDecoder()
	first, err := decoder.Decode(resp.Body, nil)
	if err != nil {
		t.Fatalf("decode first event-stream frame: %v", err)
	}
	for _, want := range []struct{ name, value string }{
		{":message-type", "event"},
		{":event-type", "initial-response"},
		{":content-type", "application/json"},
	} {
		got := first.Headers.Get(want.name)
		if got == nil {
			t.Fatalf("first frame is missing the %s header; headers: %v", want.name, first.Headers)
		}
		if got.String() != want.value {
			t.Fatalf("first frame %s = %q, want %q", want.name, got.String(), want.value)
		}
	}
	if string(first.Payload) != "{}" {
		t.Fatalf("initial-response payload = %q, want %q", first.Payload, "{}")
	}

	// And: sessionStart still follows it, unchanged.
	second, err := decoder.Decode(resp.Body, nil)
	if err != nil {
		t.Fatalf("decode second event-stream frame: %v", err)
	}
	if got := second.Headers.Get(":event-type"); got == nil || got.String() != "sessionStart" {
		t.Fatalf("second frame event type = %v, want sessionStart", got)
	}
}

func TestStartLiveTail_streamsMatchingEvents(t *testing.T) {
	// Given: a log group with two streams exists
	srv := helpers.NewTestServer(t)
	groupName := "/aws/lambda/live-tail"
	groupARN := "arn:aws:logs:us-east-1:000000000000:log-group:" + groupName
	createLogGroup(t, srv, groupName)
	createLogStream(t, srv, groupName, "app/one")
	createLogStream(t, srv, groupName, "app/two")

	// When: StartLiveTail is opened with a stream prefix and filter pattern
	resp := logsCall(t, srv, "StartLiveTail", map[string]any{
		"logGroupIdentifiers":   []string{groupARN},
		"logStreamNamePrefixes": []string{"app/"},
		"logEventFilterPattern": "ERROR",
	})
	defer resp.Body.Close()

	// Then: the response is an AWS event-stream session
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/vnd.amazon.eventstream")
	readLiveTailSessionOpen(t, resp.Body)

	// When: matching and non-matching log events are written
	putLogEvents(t, srv, groupName, "app/one", []logEvent{
		{Timestamp: evtTS(1000), Message: "INFO ignored"},
		{Timestamp: evtTS(1001), Message: "ERROR accepted"},
	})
	putLogEvents(t, srv, groupName, "app/two", []logEvent{
		{Timestamp: evtTS(1002), Message: "ERROR accepted too"},
	})

	// Then: Live Tail emits sessionUpdates containing only matching events. The
	// one-second tick can split sequential writes across updates on slower CI.
	results := make([]liveTailTestResult, 0, 2)
	for attempts := 0; attempts < 3 && len(results) < 2; attempts++ {
		payload := readLiveTailUpdate(t, resp.Body)
		if payload.SessionMetadata.Sampled {
			t.Fatal("expected sampled=false")
		}
		results = append(results, payload.SessionResults...)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 matching events, got %d: %+v", len(results), results)
	}

	seen := make(map[string]liveTailTestResult, len(results))
	for _, result := range results {
		if result.LogGroupIdentifier != groupARN {
			t.Fatalf("logGroupIdentifier = %q, want %q", result.LogGroupIdentifier, groupARN)
		}
		if result.IngestionTime == 0 {
			t.Fatalf("expected ingestionTime for event: %+v", result)
		}
		seen[result.LogStreamName+"\x00"+result.Message] = result
	}
	if got, ok := seen["app/one\x00ERROR accepted"]; !ok || got.Timestamp != evtTS(1001) {
		t.Fatalf("missing app/one ERROR event, got: %+v", results)
	}
	if got, ok := seen["app/two\x00ERROR accepted too"]; !ok || got.Timestamp != evtTS(1002) {
		t.Fatalf("missing app/two ERROR event, got: %+v", results)
	}
}

func TestStartLiveTail_manySmallWritesAllArrive(t *testing.T) {
	// Given: an open Live Tail session on one stream
	srv := helpers.NewTestServer(t)
	groupName := "/aws/lambda/live-tail-small-writes"
	groupARN := "arn:aws:logs:us-east-1:000000000000:log-group:" + groupName
	createLogGroup(t, srv, groupName)
	createLogStream(t, srv, groupName, "app/one")

	resp := logsCall(t, srv, "StartLiveTail", map[string]any{
		"logGroupIdentifiers": []string{groupARN},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	readLiveTailSessionOpen(t, resp.Body)

	// When: thirty separate PutLogEvents calls land between drains. Real
	// producers write one call per line all the time — the awslogs driver, a
	// Lambda runtime, a CLI loop — and thirty lines in a second is a quiet
	// stream, nowhere near AWS's 500-events-per-second sampling threshold.
	const writes = 30
	for i := 0; i < writes; i++ {
		putLogEvents(t, srv, groupName, "app/one", []logEvent{
			{Timestamp: evtTS(int64(2000 + i)), Message: "line " + strconv.Itoa(i)},
		})
	}

	// Then: every line is delivered, none sampled away. The session buffer
	// must be bounded by AWS's five-thousand-event contract, not by a count
	// of however many write calls the producer happened to split lines into.
	seen := make(map[string]bool, writes)
	for attempts := 0; attempts < 5 && len(seen) < writes; attempts++ {
		payload := readLiveTailUpdate(t, resp.Body)
		if payload.SessionMetadata.Sampled {
			t.Fatal("expected sampled=false for 30 events in a second")
		}
		for _, result := range payload.SessionResults {
			seen[result.Message] = true
		}
	}
	if len(seen) != writes {
		missing := make([]string, 0)
		for i := 0; i < writes; i++ {
			if !seen["line "+strconv.Itoa(i)] {
				missing = append(missing, strconv.Itoa(i))
			}
		}
		t.Fatalf("delivered %d of %d events; missing: %v", len(seen), writes, missing)
	}
}

func TestStartLiveTail_overflowDropsOldestAndSaysSampled(t *testing.T) {
	// Given: an open Live Tail session
	srv := helpers.NewTestServer(t)
	groupName := "/aws/lambda/live-tail-overflow"
	groupARN := "arn:aws:logs:us-east-1:000000000000:log-group:" + groupName
	createLogGroup(t, srv, groupName)
	createLogStream(t, srv, groupName, "app/one")

	resp := logsCall(t, srv, "StartLiveTail", map[string]any{
		"logGroupIdentifiers": []string{groupARN},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	readLiveTailSessionOpen(t, resp.Body)

	// When: more events than the session buffer holds land between drains
	// (AWS buffers 5,000 events and drops the oldest beyond that).
	const total = 5600
	events := make([]logEvent, total)
	for i := range events {
		events[i] = logEvent{Timestamp: evtTS(int64(3000 + i)), Message: "flood " + strconv.Itoa(i)}
	}
	putLogEvents(t, srv, groupName, "app/one", events)

	// Then: the update that follows the overflow admits it sampled, and what
	// it delivers is the newest events — the oldest are what a lagging tail
	// loses, per the AWS buffering contract.
	payload := readLiveTailUpdate(t, resp.Body)
	for attempts := 0; attempts < 3 && len(payload.SessionResults) == 0; attempts++ {
		payload = readLiveTailUpdate(t, resp.Body)
	}
	if len(payload.SessionResults) == 0 {
		t.Fatal("no session results delivered after overflow")
	}
	if !payload.SessionMetadata.Sampled {
		t.Fatal("expected sampled=true after the buffer dropped events")
	}
	first := payload.SessionResults[0].Message
	if first != "flood 600" {
		t.Fatalf("first delivered message = %q, want %q (oldest 600 dropped)", first, "flood 600")
	}
}

// ---- CreateLogGroup --------------------------------------------------------

func TestCreateLogGroup_success(t *testing.T) {
	// Given: no groups exist
	srv := helpers.NewTestServer(t)

	// When: CreateLogGroup is called with a valid name
	resp := logsCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: 200 with empty body (AWS returns empty JSON object)
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestCreateLogGroup_missingName(t *testing.T) {
	// Given: no groups exist
	srv := helpers.NewTestServer(t)

	// When: CreateLogGroup is called without a name
	resp := logsCall(t, srv, "CreateLogGroup", map[string]any{})
	defer resp.Body.Close()

	// Then: 400 InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestCreateLogGroup_duplicate(t *testing.T) {
	// Given: a log group already exists
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")

	// When: CreateLogGroup is called again with the same name
	resp := logsCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceAlreadyExistsException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceAlreadyExistsException")
}

// CreateLogGroup takes a `tags` map in AWS, applied as part of creating the
// group rather than by a follow-up TagLogGroup call.
func TestCreateLogGroup_withTags(t *testing.T) {
	// Given: no groups exist
	srv := helpers.NewTestServer(t)

	// When: CreateLogGroup is called with create-time tags
	resp := logsCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/tagged-on-create",
		"tags":         map[string]string{"environment": "development", "owner": "platform"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the tags are readable without any further call
	if got := listTagsLogGroup(t, srv, "/aws/lambda/tagged-on-create"); !reflect.DeepEqual(got, map[string]string{
		"environment": "development",
		"owner":       "platform",
	}) {
		t.Fatalf("tags = %#v", got)
	}
}

// The typed request model carries `tags` too, so the CBOR path must apply them.
func TestRPCv2CBOR_CreateLogGroup_withTags(t *testing.T) {
	// Given: no groups exist
	srv := helpers.NewTestServer(t)

	// When: CreateLogGroup is called over CBOR with create-time tags
	resp := logsCBORCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/cbor-tagged",
		"tags":         map[string]string{"environment": "development"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the tags are readable
	if got := listTagsLogGroup(t, srv, "/aws/lambda/cbor-tagged"); !reflect.DeepEqual(got, map[string]string{
		"environment": "development",
	}) {
		t.Fatalf("tags = %#v", got)
	}
}

// invalidTagCases are the tag maps AWS's CloudWatch Logs model and tagging
// rules reject: more than 50 pairs, an empty key, an over-length key or value,
// and the reserved `aws:` key prefix.
func invalidTagCases() map[string]map[string]string {
	tooMany := make(map[string]string, 51)
	for i := 0; i < 51; i++ {
		tooMany["key"+strconv.Itoa(i)] = "value"
	}
	return map[string]map[string]string{
		"more than 50 tags": tooMany,
		"empty key":         {"": "value"},
		"key over 128":      {strings.Repeat("k", 129): "value"},
		"value over 256":    {"key": strings.Repeat("v", 257)},
		"reserved prefix":   {"aws:created-by": "overcast"},
	}
}

// A rejected CreateLogGroup must not leave a half-made log group behind — the
// tags are part of the create, so failing validation fails the whole call.
func TestCreateLogGroup_invalidTagsRejected(t *testing.T) {
	for name, tags := range invalidTagCases() {
		t.Run(name, func(t *testing.T) {
			// Given: no groups exist
			srv := helpers.NewTestServer(t)

			// When: CreateLogGroup is called with tags AWS rejects
			resp := logsCall(t, srv, "CreateLogGroup", map[string]any{
				"logGroupName": "/aws/lambda/bad-tags",
				"tags":         tags,
			})
			defer resp.Body.Close()

			// Then: 400 InvalidParameterException
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "InvalidParameterException")

			// And: no log group was created
			resp2 := logsCall(t, srv, "DescribeLogGroups", map[string]any{})
			defer resp2.Body.Close()
			var result struct {
				LogGroups []struct {
					LogGroupName string `json:"logGroupName"`
				} `json:"logGroups"`
			}
			helpers.DecodeJSON(t, resp2, &result)
			if len(result.LogGroups) != 0 {
				t.Fatalf("log group created despite rejected tags: %+v", result.LogGroups)
			}
		})
	}
}

// TagLogGroup applies the same validation, and a rejected call must leave the
// group's existing tags exactly as they were.
func TestTagLogGroup_invalidTagsRejected(t *testing.T) {
	for name, tags := range invalidTagCases() {
		t.Run(name, func(t *testing.T) {
			// Given: a log group with one tag already applied
			srv := helpers.NewTestServer(t)
			createLogGroup(t, srv, "/aws/lambda/existing-tags")
			resp := logsCall(t, srv, "TagLogGroup", map[string]any{
				"logGroupName": "/aws/lambda/existing-tags",
				"tags":         map[string]string{"environment": "development"},
			})
			helpers.AssertStatus(t, resp, http.StatusOK)
			resp.Body.Close()

			// When: TagLogGroup is called with tags AWS rejects
			resp = logsCall(t, srv, "TagLogGroup", map[string]any{
				"logGroupName": "/aws/lambda/existing-tags",
				"tags":         tags,
			})
			defer resp.Body.Close()

			// Then: 400 InvalidParameterException
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "InvalidParameterException")

			// And: the existing tags are untouched
			if got := listTagsLogGroup(t, srv, "/aws/lambda/existing-tags"); !reflect.DeepEqual(got, map[string]string{
				"environment": "development",
			}) {
				t.Fatalf("tags mutated by rejected call: %#v", got)
			}
		})
	}
}

// Tag validation runs on the request before the log group is resolved, so an
// invalid tag map on a missing group reports the parameter error.
func TestTagLogGroup_invalidTagsBeatMissingGroup(t *testing.T) {
	// Given: no log groups exist
	srv := helpers.NewTestServer(t)

	// When: TagLogGroup names a missing group with a reserved tag key
	resp := logsCall(t, srv, "TagLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/no-such-group",
		"tags":         map[string]string{"aws:created-by": "overcast"},
	})
	defer resp.Body.Close()

	// Then: the parameter error wins
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestRPCv2CBOR_DescribeLogGroups(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := logsCBORCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/cbor-alpha",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = logsCBORCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": "/custom/cbor-beta",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = logsCBORCall(t, srv, "DescribeLogGroups", map[string]any{
		"logGroupNamePrefix": "/aws/lambda",
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/cbor")

	var result struct {
		LogGroups []struct {
			LogGroupName string `cbor:"logGroupName"`
			ARN          string `cbor:"arn"`
		} `cbor:"logGroups"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode CBOR DescribeLogGroups response: %v", err)
	}
	if len(result.LogGroups) != 1 {
		t.Fatalf("expected 1 log group, got %d", len(result.LogGroups))
	}
	if result.LogGroups[0].LogGroupName != "/aws/lambda/cbor-alpha" {
		t.Fatalf("logGroupName = %q", result.LogGroups[0].LogGroupName)
	}
	if result.LogGroups[0].ARN == "" {
		t.Fatal("expected arn")
	}
}

// ---- DescribeLogGroups -----------------------------------------------------

func TestDescribeLogGroups_empty(t *testing.T) {
	// Given: no groups exist
	srv := helpers.NewTestServer(t)

	// When: DescribeLogGroups is called
	resp := logsCall(t, srv, "DescribeLogGroups", map[string]any{})
	defer resp.Body.Close()

	// Then: 200 with empty logGroups array
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		LogGroups []any `json:"logGroups"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.LogGroups) != 0 {
		t.Errorf("expected 0 log groups, got %d", len(result.LogGroups))
	}
}

func TestDescribeLogGroups_returnsAll(t *testing.T) {
	// Given: three log groups exist
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/fn-alpha")
	createLogGroup(t, srv, "/aws/lambda/fn-beta")
	createLogGroup(t, srv, "/aws/lambda/fn-gamma")

	// When: DescribeLogGroups is called
	resp := logsCall(t, srv, "DescribeLogGroups", map[string]any{})
	defer resp.Body.Close()

	// Then: all three groups are returned
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
			ARN          string `json:"arn"`
			CreationTime int64  `json:"creationTime"`
		} `json:"logGroups"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.LogGroups) != 3 {
		t.Errorf("expected 3 log groups, got %d", len(result.LogGroups))
	}
	for _, g := range result.LogGroups {
		if g.ARN == "" {
			t.Errorf("log group %q: expected ARN to be set", g.LogGroupName)
		}
		if g.CreationTime == 0 {
			t.Errorf("log group %q: expected CreationTime to be set", g.LogGroupName)
		}
	}
}

func TestDescribeLogGroups_withPrefix(t *testing.T) {
	// Given: groups under /aws/lambda and /aws/rds
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/fn-a")
	createLogGroup(t, srv, "/aws/lambda/fn-b")
	createLogGroup(t, srv, "/aws/rds/cluster-1")

	// When: DescribeLogGroups is called with a logGroupNamePrefix
	resp := logsCall(t, srv, "DescribeLogGroups", map[string]any{
		"logGroupNamePrefix": "/aws/lambda",
	})
	defer resp.Body.Close()

	// Then: only the lambda groups are returned
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
		} `json:"logGroups"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.LogGroups) != 2 {
		t.Errorf("expected 2 log groups, got %d", len(result.LogGroups))
	}
}

// ---- CreateLogStream -------------------------------------------------------

func TestCreateLogStream_success(t *testing.T) {
	// Given: a log group exists
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")

	// When: CreateLogStream is called with a valid name
	resp := logsCall(t, srv, "CreateLogStream", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "2026/03/28/[$LATEST]abc123",
	})
	defer resp.Body.Close()

	// Then: 200 with empty body
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestCreateLogStream_missingGroupName(t *testing.T) {
	// Given: nothing
	srv := helpers.NewTestServer(t)

	// When: CreateLogStream is called without logGroupName
	resp := logsCall(t, srv, "CreateLogStream", map[string]any{
		"logStreamName": "some-stream",
	})
	defer resp.Body.Close()

	// Then: 400 InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestCreateLogStream_missingStreamName(t *testing.T) {
	// Given: a log group exists
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")

	// When: CreateLogStream is called without logStreamName
	resp := logsCall(t, srv, "CreateLogStream", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: 400 InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestCreateLogStream_groupNotFound(t *testing.T) {
	// Given: the log group does not exist
	srv := helpers.NewTestServer(t)

	// When: CreateLogStream is called referencing a non-existent group
	resp := logsCall(t, srv, "CreateLogStream", map[string]any{
		"logGroupName":  "/aws/lambda/no-such-function",
		"logStreamName": "some-stream",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestCreateLogStream_duplicate(t *testing.T) {
	// Given: a group and stream already exist
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")

	// When: CreateLogStream is called again with the same names
	resp := logsCall(t, srv, "CreateLogStream", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "my-stream",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceAlreadyExistsException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceAlreadyExistsException")
}

// ---- DescribeLogStreams -----------------------------------------------------

func TestDescribeLogStreams_empty(t *testing.T) {
	// Given: a group with no streams
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")

	// When: DescribeLogStreams is called
	resp := logsCall(t, srv, "DescribeLogStreams", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: 200 with empty logStreams array
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		LogStreams []any `json:"logStreams"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.LogStreams) != 0 {
		t.Errorf("expected 0 log streams, got %d", len(result.LogStreams))
	}
}

func TestDescribeLogStreams_returnsAll(t *testing.T) {
	// Given: a group with two streams
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "stream-a")
	createLogStream(t, srv, "/aws/lambda/my-function", "stream-b")

	// When: DescribeLogStreams is called
	resp := logsCall(t, srv, "DescribeLogStreams", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: both streams are returned
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		LogStreams []struct {
			LogStreamName string `json:"logStreamName"`
			ARN           string `json:"arn"`
			CreationTime  int64  `json:"creationTime"`
		} `json:"logStreams"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.LogStreams) != 2 {
		t.Errorf("expected 2 log streams, got %d", len(result.LogStreams))
	}
	for _, s := range result.LogStreams {
		if s.ARN == "" {
			t.Errorf("stream %q: expected ARN to be set", s.LogStreamName)
		}
		if s.CreationTime == 0 {
			t.Errorf("stream %q: expected CreationTime to be set", s.LogStreamName)
		}
	}
}

func TestDescribeLogStreams_withPrefix(t *testing.T) {
	// Given: a group with streams under two prefixes
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "2026/03/28/stream-a")
	createLogStream(t, srv, "/aws/lambda/my-function", "2026/03/28/stream-b")
	createLogStream(t, srv, "/aws/lambda/my-function", "2026/03/27/stream-c")

	// When: DescribeLogStreams is called with a logStreamNamePrefix
	resp := logsCall(t, srv, "DescribeLogStreams", map[string]any{
		"logGroupName":        "/aws/lambda/my-function",
		"logStreamNamePrefix": "2026/03/28",
	})
	defer resp.Body.Close()

	// Then: only streams with the matching prefix are returned
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		LogStreams []struct {
			LogStreamName string `json:"logStreamName"`
		} `json:"logStreams"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.LogStreams) != 2 {
		t.Errorf("expected 2 log streams, got %d", len(result.LogStreams))
	}
}

func TestDescribeLogStreams_groupNotFound(t *testing.T) {
	// Given: the log group does not exist
	srv := helpers.NewTestServer(t)

	// When: DescribeLogStreams is called for a non-existent group
	resp := logsCall(t, srv, "DescribeLogStreams", map[string]any{
		"logGroupName": "/aws/lambda/no-such-function",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ---- PutLogEvents ----------------------------------------------------------

func TestPutLogEvents_success(t *testing.T) {
	// Given: a group and stream exist
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")

	// When: PutLogEvents is called with two events
	resp := logsCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "my-stream",
		"logEvents": []map[string]any{
			{"timestamp": evtTS(0), "message": "START RequestId: abc-123"},
			{"timestamp": evtTS(1000), "message": "END RequestId: abc-123"},
		},
	})
	defer resp.Body.Close()

	// Then: 200 with a nextSequenceToken
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		NextSequenceToken string `json:"nextSequenceToken"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if result.NextSequenceToken == "" {
		t.Error("expected nextSequenceToken to be set")
	}
}

// TestPutLogEvents_sequenceTokenIsIgnored pins the 2023 AWS behavior change
// documented on PutLogEvents: "The sequenceToken parameter is now ignored in
// PutLogEvents actions. PutLogEvents actions are always accepted and never
// return InvalidSequenceTokenException or DataAlreadyAcceptedException even
// if the sequence token is not valid." A stale or entirely garbage
// sequenceToken must not raise either exception, and nextSequenceToken must
// still come back so a caller that still threads the (now-vestigial) token
// through keeps working.
func TestPutLogEvents_sequenceTokenIsIgnored(t *testing.T) {
	// Given: a group and stream exist, and one batch has already advanced the
	// stream's internal sequence counter past its initial value.
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/seq-token-fn")
	createLogStream(t, srv, "/aws/lambda/seq-token-fn", "seq-stream")

	first := logsCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/seq-token-fn",
		"logStreamName": "seq-stream",
		"logEvents": []map[string]any{
			{"timestamp": evtTS(0), "message": "first batch"},
		},
	})
	defer first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)

	// When: a second batch is sent with a sequenceToken that is neither the
	// value PutLogEvents just returned nor anything derived from it — just
	// garbage a caller might have cached from a stale client.
	resp := logsCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/seq-token-fn",
		"logStreamName": "seq-stream",
		"sequenceToken": "not-a-real-token",
		"logEvents": []map[string]any{
			{"timestamp": evtTS(1000), "message": "second batch"},
		},
	})
	defer resp.Body.Close()

	// Then: the call still succeeds — no InvalidSequenceTokenException, no
	// DataAlreadyAcceptedException — and nextSequenceToken is still populated.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		NextSequenceToken string `json:"nextSequenceToken"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.NextSequenceToken == "" {
		t.Error("expected nextSequenceToken to still be set when sequenceToken is garbage")
	}
}

func TestPutLogEvents_groupNotFound(t *testing.T) {
	// Given: no group exists
	srv := helpers.NewTestServer(t)

	// When: PutLogEvents references a non-existent group
	resp := logsCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/no-such-function",
		"logStreamName": "my-stream",
		"logEvents":     []map[string]any{{"timestamp": evtTS(0), "message": "hello"}},
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestPutLogEvents_streamNotFound(t *testing.T) {
	// Given: a group exists but the stream does not
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")

	// When: PutLogEvents references a non-existent stream
	resp := logsCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "no-such-stream",
		"logEvents":     []map[string]any{{"timestamp": evtTS(0), "message": "hello"}},
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestPutLogEvents_missingEvents(t *testing.T) {
	// Given: a group and stream exist
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")

	// When: PutLogEvents is called with no events
	resp := logsCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "my-stream",
	})
	defer resp.Body.Close()

	// Then: 400 InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ---- GetLogEvents ----------------------------------------------------------

func TestGetLogEvents_success(t *testing.T) {
	// Given: a stream with three events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(0), Message: "START RequestId: abc-123"},
		{Timestamp: evtTS(1000), Message: "log line one"},
		{Timestamp: evtTS(2000), Message: "END RequestId: abc-123"},
	})

	// When: GetLogEvents is called
	resp := logsCall(t, srv, "GetLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "my-stream",
	})
	defer resp.Body.Close()

	// Then: all three events are returned in order
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Events []struct {
			Timestamp     int64  `json:"timestamp"`
			Message       string `json:"message"`
			IngestionTime int64  `json:"ingestionTime"`
		} `json:"events"`
		NextForwardToken  string `json:"nextForwardToken"`
		NextBackwardToken string `json:"nextBackwardToken"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result.Events))
	}
	if result.Events[0].Message != "START RequestId: abc-123" {
		t.Errorf("expected first event message 'START RequestId: abc-123', got %q", result.Events[0].Message)
	}
	if result.Events[0].IngestionTime == 0 {
		t.Error("expected IngestionTime to be set")
	}
	if result.NextForwardToken == "" {
		t.Error("expected nextForwardToken to be set")
	}
	if result.NextBackwardToken == "" {
		t.Error("expected nextBackwardToken to be set")
	}
}

func TestGetLogEvents_emptyStream(t *testing.T) {
	// Given: a stream with no events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")

	// When: GetLogEvents is called
	resp := logsCall(t, srv, "GetLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "my-stream",
	})
	defer resp.Body.Close()

	// Then: empty events array, tokens still present
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Events            []any  `json:"events"`
		NextForwardToken  string `json:"nextForwardToken"`
		NextBackwardToken string `json:"nextBackwardToken"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(result.Events))
	}
}

func TestGetLogEvents_timeRange(t *testing.T) {
	// Given: a stream with events at t=0, t=1000, t=2000, t=3000 ms
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "before-range"},
		{Timestamp: evtTS(2000), Message: "in-range-start"},
		{Timestamp: evtTS(3000), Message: "in-range-end"},
		{Timestamp: evtTS(4000), Message: "after-range"},
	})

	// When: GetLogEvents is called with startTime=2000, endTime=3000
	resp := logsCall(t, srv, "GetLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "my-stream",
		"startTime":     evtTS(2000),
		"endTime":       evtTS(3001),
	})
	defer resp.Body.Close()

	// Then: the two events at t=2000 and t=3000 are returned.
	// endTime is exclusive per AWS docs, so endTime=3001 includes t=3000.
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Events []struct {
			Timestamp int64  `json:"timestamp"`
			Message   string `json:"message"`
		} `json:"events"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events in range, got %d", len(result.Events))
	}
	if result.Events[0].Message != "in-range-start" {
		t.Errorf("expected first in-range event 'in-range-start', got %q", result.Events[0].Message)
	}
}

func TestGetLogEvents_groupNotFound(t *testing.T) {
	// Given: no group exists
	srv := helpers.NewTestServer(t)

	// When: GetLogEvents references a non-existent group
	resp := logsCall(t, srv, "GetLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/no-such-function",
		"logStreamName": "my-stream",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestGetLogEvents_streamNotFound(t *testing.T) {
	// Given: a group exists but the stream does not
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")

	// When: GetLogEvents references a non-existent stream
	resp := logsCall(t, srv, "GetLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "no-such-stream",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestRPCv2CBOR_LogEventsRoundTrip(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := logsCBORCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/cbor-events",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = logsCBORCall(t, srv, "CreateLogStream", map[string]any{
		"logGroupName":  "/aws/lambda/cbor-events",
		"logStreamName": "stream",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = logsCBORCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/cbor-events",
		"logStreamName": "stream",
		"logEvents": []map[string]any{
			{"timestamp": evtTS(1000), "message": "hello cbor"},
			{"timestamp": evtTS(2000), "message": "goodbye cbor"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var putOut struct {
		NextSequenceToken string `cbor:"nextSequenceToken"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&putOut); err != nil {
		t.Fatalf("decode CBOR PutLogEvents response: %v", err)
	}
	resp.Body.Close()
	if putOut.NextSequenceToken == "" {
		t.Fatal("expected nextSequenceToken")
	}

	resp = logsCBORCall(t, srv, "GetLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/cbor-events",
		"logStreamName": "stream",
		"startTime":     evtTS(1500),
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var getOut struct {
		Events []struct {
			Timestamp int64  `cbor:"timestamp"`
			Message   string `cbor:"message"`
		} `cbor:"events"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&getOut); err != nil {
		t.Fatalf("decode CBOR GetLogEvents response: %v", err)
	}
	if len(getOut.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(getOut.Events))
	}
	if getOut.Events[0].Message != "goodbye cbor" {
		t.Fatalf("message = %q", getOut.Events[0].Message)
	}
}

// ---- DeleteLogGroup --------------------------------------------------------

func TestDeleteLogGroup_success(t *testing.T) {
	// Given: a log group with a stream and events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "hello"},
	})

	// When: DeleteLogGroup is called
	resp := logsCall(t, srv, "DeleteLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: the group is gone
	descResp := logsCall(t, srv, "DescribeLogGroups", map[string]any{})
	defer descResp.Body.Close()
	var descResult struct {
		LogGroups []any `json:"logGroups"`
	}
	helpers.DecodeJSON(t, descResp, &descResult)
	if len(descResult.LogGroups) != 0 {
		t.Errorf("expected 0 log groups, got %d", len(descResult.LogGroups))
	}
}

func TestDeleteLogGroup_notFound(t *testing.T) {
	// Given: no groups exist
	srv := helpers.NewTestServer(t)

	// When: DeleteLogGroup is called for a non-existent group
	resp := logsCall(t, srv, "DeleteLogGroup", map[string]any{
		"logGroupName": "/aws/lambda/nope",
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDeleteLogGroup_missingName(t *testing.T) {
	// Given: a test server
	srv := helpers.NewTestServer(t)

	// When: DeleteLogGroup is called without a name
	resp := logsCall(t, srv, "DeleteLogGroup", map[string]any{})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ---- DeleteLogStream (implemented) -----------------------------------------

func TestDeleteLogStream_success(t *testing.T) {
	// Given: a group and stream exist
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")

	// When: DeleteLogStream is called
	resp := logsCall(t, srv, "DeleteLogStream", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"logStreamName": "my-stream",
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ---- FilterLogEvents -------------------------------------------------------

func TestFilterLogEvents_noFilter(t *testing.T) {
	// Given: two streams with events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "stream-a")
	createLogStream(t, srv, "/aws/lambda/my-function", "stream-b")
	putLogEvents(t, srv, "/aws/lambda/my-function", "stream-a", []logEvent{
		{Timestamp: evtTS(1000), Message: "alpha first"},
		{Timestamp: evtTS(3000), Message: "alpha third"},
	})
	putLogEvents(t, srv, "/aws/lambda/my-function", "stream-b", []logEvent{
		{Timestamp: evtTS(2000), Message: "beta second"},
	})

	// When: FilterLogEvents is called without a filter pattern
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: all events from all streams returned, sorted by timestamp
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result.Events))
	}
	if result.Events[0].Message != "alpha first" {
		t.Errorf("expected first event 'alpha first', got %q", result.Events[0].Message)
	}
	if result.Events[1].Message != "beta second" {
		t.Errorf("expected second event 'beta second', got %q", result.Events[1].Message)
	}
	if result.Events[2].Message != "alpha third" {
		t.Errorf("expected third event 'alpha third', got %q", result.Events[2].Message)
	}
}

func TestFilterLogEvents_withFilterPattern(t *testing.T) {
	// Given: a stream with mixed log levels
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "INFO starting up"},
		{Timestamp: evtTS(2000), Message: "ERROR something broke"},
		{Timestamp: evtTS(3000), Message: "INFO recovered"},
		{Timestamp: evtTS(4000), Message: "ERROR another failure"},
	})

	// When: FilterLogEvents is called with filterPattern "ERROR"
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"filterPattern": "ERROR",
	})
	defer resp.Body.Close()

	// Then: only ERROR events are returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events matching 'ERROR', got %d", len(result.Events))
	}
	if result.Events[0].Message != "ERROR something broke" {
		t.Errorf("unexpected first event: %q", result.Events[0].Message)
	}
}

func TestFilterLogEvents_withQuotedTerms(t *testing.T) {
	// Given: a stream with various messages
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "request completed successfully"},
		{Timestamp: evtTS(2000), Message: "request failed with error"},
		{Timestamp: evtTS(3000), Message: "another error occurred"},
	})

	// When: FilterLogEvents uses a quoted exact phrase
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"filterPattern": `"request failed"`,
	})
	defer resp.Body.Close()

	// Then: only the matching event
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_multipleTerms(t *testing.T) {
	// Given: a stream with various messages
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "ERROR user abc-123 not found"},
		{Timestamp: evtTS(2000), Message: "INFO user abc-123 logged in"},
		{Timestamp: evtTS(3000), Message: "ERROR system crash"},
	})

	// When: FilterLogEvents uses space-separated terms (AND logic)
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/my-function",
		"filterPattern": "ERROR abc-123",
	})
	defer resp.Body.Close()

	// Then: only events containing both terms
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event matching both terms, got %d", len(result.Events))
	}
	if result.Events[0].Message != "ERROR user abc-123 not found" {
		t.Errorf("unexpected event: %q", result.Events[0].Message)
	}
}

func TestFilterLogEvents_timeRange(t *testing.T) {
	// Given: a stream with events over a time range
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "too early"},
		{Timestamp: evtTS(2000), Message: "in range"},
		{Timestamp: evtTS(3000), Message: "also in range"},
		{Timestamp: evtTS(4000), Message: "too late"},
	})

	// When: FilterLogEvents is called with time bounds
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
		"startTime":    evtTS(2000),
		"endTime":      evtTS(3000),
	})
	defer resp.Body.Close()

	// Then: only events in range [startTime, endTime]
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events in range, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_logStreamNames(t *testing.T) {
	// Given: three streams with events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "stream-a")
	createLogStream(t, srv, "/aws/lambda/my-function", "stream-b")
	createLogStream(t, srv, "/aws/lambda/my-function", "stream-c")
	putLogEvents(t, srv, "/aws/lambda/my-function", "stream-a", []logEvent{
		{Timestamp: evtTS(1000), Message: "from a"},
	})
	putLogEvents(t, srv, "/aws/lambda/my-function", "stream-b", []logEvent{
		{Timestamp: evtTS(2000), Message: "from b"},
	})
	putLogEvents(t, srv, "/aws/lambda/my-function", "stream-c", []logEvent{
		{Timestamp: evtTS(3000), Message: "from c"},
	})

	// When: FilterLogEvents specifies only stream-a and stream-c
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":   "/aws/lambda/my-function",
		"logStreamNames": []string{"stream-a", "stream-c"},
	})
	defer resp.Body.Close()

	// Then: only events from those two streams
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_logStreamNamePrefix(t *testing.T) {
	// Given: streams with prefix-based naming
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "2026/03/28/a")
	createLogStream(t, srv, "/aws/lambda/my-function", "2026/03/28/b")
	createLogStream(t, srv, "/aws/lambda/my-function", "2026/03/27/c")
	putLogEvents(t, srv, "/aws/lambda/my-function", "2026/03/28/a", []logEvent{
		{Timestamp: evtTS(1000), Message: "28-a"},
	})
	putLogEvents(t, srv, "/aws/lambda/my-function", "2026/03/28/b", []logEvent{
		{Timestamp: evtTS(2000), Message: "28-b"},
	})
	putLogEvents(t, srv, "/aws/lambda/my-function", "2026/03/27/c", []logEvent{
		{Timestamp: evtTS(3000), Message: "27-c"},
	})

	// When: FilterLogEvents uses logStreamNamePrefix
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":        "/aws/lambda/my-function",
		"logStreamNamePrefix": "2026/03/28",
	})
	defer resp.Body.Close()

	// Then: only events from streams matching the prefix
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events from 03/28 streams, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_jsonFieldEquals(t *testing.T) {
	// Given: a stream with JSON log events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/json-filter")
	createLogStream(t, srv, "/aws/lambda/json-filter", "stream")
	putLogEvents(t, srv, "/aws/lambda/json-filter", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: `{"level":"ERROR","msg":"disk full"}`},
		{Timestamp: evtTS(2000), Message: `{"level":"INFO","msg":"started"}`},
		{Timestamp: evtTS(3000), Message: `{"level":"ERROR","msg":"timeout"}`},
	})

	// When: FilterLogEvents uses a JSON equality filter
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/json-filter",
		"filterPattern": `{ $.level = "ERROR" }`,
	})
	defer resp.Body.Close()

	// Then: only ERROR events are returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 JSON-matched events, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_jsonNumericComparison(t *testing.T) {
	// Given: a stream with JSON events containing numeric fields
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/json-numeric")
	createLogStream(t, srv, "/aws/lambda/json-numeric", "stream")
	putLogEvents(t, srv, "/aws/lambda/json-numeric", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: `{"statusCode":200}`},
		{Timestamp: evtTS(2000), Message: `{"statusCode":404}`},
		{Timestamp: evtTS(3000), Message: `{"statusCode":503}`},
	})

	// When: FilterLogEvents uses a numeric comparison
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/json-numeric",
		"filterPattern": `{ $.statusCode >= 400 }`,
	})
	defer resp.Body.Close()

	// Then: only events with statusCode >= 400
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events with status >= 400, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_jsonAndOr(t *testing.T) {
	// Given: a stream with varied JSON events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/json-logic")
	createLogStream(t, srv, "/aws/lambda/json-logic", "stream")
	putLogEvents(t, srv, "/aws/lambda/json-logic", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: `{"level":"ERROR","code":503}`},
		{Timestamp: evtTS(2000), Message: `{"level":"ERROR","code":400}`},
		{Timestamp: evtTS(3000), Message: `{"level":"WARN","code":503}`},
	})

	// When: FilterLogEvents uses && combinator
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/json-logic",
		"filterPattern": `{ $.level = "ERROR" && $.code >= 500 }`,
	})
	defer resp.Body.Close()

	// Then: only events matching both conditions
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event matching AND, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_jsonExists(t *testing.T) {
	// Given: a stream with some events containing an "error" field
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/json-exists")
	createLogStream(t, srv, "/aws/lambda/json-exists", "stream")
	putLogEvents(t, srv, "/aws/lambda/json-exists", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: `{"error":"disk full","req":"abc"}`},
		{Timestamp: evtTS(2000), Message: `{"msg":"ok","req":"def"}`},
		{Timestamp: evtTS(3000), Message: `{"error":"timeout","req":"ghi"}`},
	})

	// When: FilterLogEvents uses EXISTS
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/json-exists",
		"filterPattern": `{ $.error EXISTS }`,
	})
	defer resp.Body.Close()

	// Then: only events with the "error" field
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events with error field, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_jsonNestedField(t *testing.T) {
	// Given: a stream with nested JSON events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/json-nested")
	createLogStream(t, srv, "/aws/lambda/json-nested", "stream")
	putLogEvents(t, srv, "/aws/lambda/json-nested", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: `{"request":{"method":"GET","path":"/health"}}`},
		{Timestamp: evtTS(2000), Message: `{"request":{"method":"POST","path":"/api"}}`},
	})

	// When: FilterLogEvents uses a nested field path
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/json-nested",
		"filterPattern": `{ $.request.method = "POST" }`,
	})
	defer resp.Body.Close()

	// Then: only the POST event
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 1 {
		t.Fatalf("expected 1 nested-field match, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_textOrInclude(t *testing.T) {
	// Given: a stream with mixed log levels
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/text-or")
	createLogStream(t, srv, "/aws/lambda/text-or", "stream")
	putLogEvents(t, srv, "/aws/lambda/text-or", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "ERROR disk full"},
		{Timestamp: evtTS(2000), Message: "WARN memory high"},
		{Timestamp: evtTS(3000), Message: "INFO all good"},
	})

	// When: FilterLogEvents uses ? prefix for OR matching
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/text-or",
		"filterPattern": "?ERROR ?WARN",
	})
	defer resp.Body.Close()

	// Then: ERROR and WARN events returned, not INFO
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events matching OR filter, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_jsonNonJSONMessages(t *testing.T) {
	// Given: a stream with mixed JSON and plain-text messages
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/json-mixed")
	createLogStream(t, srv, "/aws/lambda/json-mixed", "stream")
	putLogEvents(t, srv, "/aws/lambda/json-mixed", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: `{"level":"ERROR"}`},
		{Timestamp: evtTS(2000), Message: "plain text ERROR line"},
		{Timestamp: evtTS(3000), Message: `not json {"level":"ERROR"}`},
	})

	// When: a JSON filter is applied
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/json-mixed",
		"filterPattern": `{ $.level = "ERROR" }`,
	})
	defer resp.Body.Close()

	// Then: only the valid JSON event matches
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 1 {
		t.Fatalf("expected 1 JSON match among mixed messages, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_invalidFilterPattern(t *testing.T) {
	// Given: a log group exists
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/bad-filter")

	// When: FilterLogEvents uses a malformed JSON filter
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/bad-filter",
		"filterPattern": `{ $.level "ERROR" }`,
	})
	defer resp.Body.Close()

	// Then: 400 InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ---- Space-delimited (columnar) filter integration tests -------------------

func TestFilterLogEvents_spaceDelimitedBasic(t *testing.T) {
	// Given: a stream with space-delimited log events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/space-basic")
	createLogStream(t, srv, "/aws/lambda/space-basic", "stream-1")
	putLogEvents(t, srv, "/aws/lambda/space-basic", "stream-1", []logEvent{
		{Timestamp: evtTS(1000), Message: "INFO 2024-01-01 GET /index.html 200 1234"},
		{Timestamp: evtTS(2000), Message: "ERROR 2024-01-01 POST /api/v1 500 5678"},
		{Timestamp: evtTS(3000), Message: "INFO 2024-01-01 GET /page.html 404 910"},
	})

	// When: filter for status 4*
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/space-basic",
		"filterPattern": "[level, date, method, request, status = 4*, bytes]",
	})
	defer resp.Body.Close()

	// Then: only the 404 event matches
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
	if result.Events[0].Timestamp != evtTS(3000) {
		t.Errorf("expected timestamp evtTS(3000) = %d, got %d", evtTS(3000), result.Events[0].Timestamp)
	}
}

func TestFilterLogEvents_spaceDelimitedRegex(t *testing.T) {
	// Given: a stream with IP-prefixed log events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/space-regex")
	createLogStream(t, srv, "/aws/lambda/space-regex", "stream-1")
	putLogEvents(t, srv, "/aws/lambda/space-regex", "stream-1", []logEvent{
		{Timestamp: evtTS(1000), Message: "127.0.0.1 frank /index.html 404 1534"},
		{Timestamp: evtTS(2000), Message: "192.168.1.1 bob /index.html 404 1534"},
		{Timestamp: evtTS(3000), Message: "127.0.0.9 alice /page.html 403 2000"},
	})

	// When: filter using regex on IP
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/space-regex",
		"filterPattern": `[ip=%127\.0\.0\.[1-9]%, user, request, status, bytes]`,
	})
	defer resp.Body.Close()

	// Then: only 127.0.0.x events match
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_spaceDelimitedCompoundOr(t *testing.T) {
	// Given: a stream with events at various status codes
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/space-compound")
	createLogStream(t, srv, "/aws/lambda/space-compound", "stream-1")
	putLogEvents(t, srv, "/aws/lambda/space-compound", "stream-1", []logEvent{
		{Timestamp: evtTS(1000), Message: "GET /page 404 1534"},
		{Timestamp: evtTS(2000), Message: "POST /api 200 1534"},
		{Timestamp: evtTS(3000), Message: "DELETE /old 410 1534"},
	})

	// When: filter for status_code = 404 || status_code = 410
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/space-compound",
		"filterPattern": "[method, request, status = 404 || status = 410, bytes]",
	})
	defer resp.Body.Close()

	// Then: 404 and 410 events match
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result.Events))
	}
}

func TestFilterLogEvents_spaceDelimitedEllipsis(t *testing.T) {
	// Given: a stream with multi-field log events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/space-ellipsis")
	createLogStream(t, srv, "/aws/lambda/space-ellipsis", "stream-1")
	putLogEvents(t, srv, "/aws/lambda/space-ellipsis", "stream-1", []logEvent{
		{Timestamp: evtTS(1000), Message: "127.0.0.1 frank admin 2024-01-01 /index.html 404 1534"},
		{Timestamp: evtTS(2000), Message: "127.0.0.1 bob admin 2024-01-01 /api 200 1534"},
	})

	// When: filter using ellipsis to skip leading fields
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/space-ellipsis",
		"filterPattern": "[..., request = *.html*, status = 4*, bytes]",
	})
	defer resp.Body.Close()

	// Then: only the first event matches
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
	if result.Events[0].Timestamp != evtTS(1000) {
		t.Errorf("expected timestamp evtTS(1000) = %d, got %d", evtTS(1000), result.Events[0].Timestamp)
	}
}

func TestFilterLogEvents_spaceDelimitedBracketQuoteFields(t *testing.T) {
	// Given: a stream with bracket/quote-delimited fields (NGINX-like)
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/space-bracket")
	createLogStream(t, srv, "/aws/lambda/space-bracket", "stream-1")
	putLogEvents(t, srv, "/aws/lambda/space-bracket", "stream-1", []logEvent{
		{Timestamp: evtTS(1000), Message: `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`},
		{Timestamp: evtTS(2000), Message: `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /api HTTP/1.0" 200 1534`},
	})

	// When: filter matching 7 fields (bracket+quote groups count as 1 each)
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/space-bracket",
		"filterPattern": "[ip, env, user, timestamp, request = *.html*, status = 4*, bytes]",
	})
	defer resp.Body.Close()

	// Then: only the first event matches
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
	if result.Events[0].Timestamp != evtTS(1000) {
		t.Errorf("expected timestamp evtTS(1000) = %d, got %d", evtTS(1000), result.Events[0].Timestamp)
	}
}

func TestFilterLogEvents_groupNotFound(t *testing.T) {
	// Given: no group exists
	srv := helpers.NewTestServer(t)

	// When: FilterLogEvents is called for a non-existent group
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName": "/aws/lambda/no-such-function",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestFilterLogEvents_returnsStreamName(t *testing.T) {
	// Given: a stream with events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/my-function")
	createLogStream(t, srv, "/aws/lambda/my-function", "my-stream")
	putLogEvents(t, srv, "/aws/lambda/my-function", "my-stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "hello"},
	})

	// When: FilterLogEvents is called
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName": "/aws/lambda/my-function",
	})
	defer resp.Body.Close()

	// Then: each event includes the logStreamName
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
	if result.Events[0].LogStreamName != "my-stream" {
		t.Errorf("expected logStreamName 'my-stream', got %q", result.Events[0].LogStreamName)
	}
}

// ---- Request ID is present on every response --------------------------------

func TestEveryResponse_hasRequestID(t *testing.T) {
	// Given: a log group exists for error-path cases
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/req-id-test")

	cases := []struct {
		action string
		body   map[string]any
	}{
		{"CreateLogGroup", map[string]any{"logGroupName": "/aws/lambda/req-id-test-2"}},
		{"DescribeLogGroups", map[string]any{}},
		{"CreateLogGroup", map[string]any{"logGroupName": "/aws/lambda/req-id-test"}}, // duplicate — error path
	}

	for _, tc := range cases {
		resp := logsCall(t, srv, tc.action, tc.body)
		resp.Body.Close()
		helpers.AssertRequestID(t, resp)
	}
}

// ---- Test helpers ----------------------------------------------------------

// filterResult is the response shape for FilterLogEvents.
type filterResult struct {
	Events []struct {
		Timestamp     int64  `json:"timestamp"`
		Message       string `json:"message"`
		IngestionTime int64  `json:"ingestionTime"`
		LogStreamName string `json:"logStreamName"`
	} `json:"events"`
	NextToken          *string `json:"nextToken"`
	SearchedLogStreams []struct {
		LogStreamName      string `json:"logStreamName"`
		SearchedCompletely bool   `json:"searchedCompletely"`
	} `json:"searchedLogStreams"`
}

// logsCall sends a POST request to the CloudWatch Logs dispatcher with the
// given operation and JSON body. It is the low-level HTTP helper — prefer
// the typed helpers (createLogGroup, createLogStream, putLogEvents) for setup.
func logsCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	return logsCallCtx(t, context.Background(), srv, operation, body)
}

// logsCallCtx is logsCall bound to a context — what a streaming operation
// needs, so a session that never sends the frame a test is waiting for fails
// on the deadline instead of hanging the package.
func logsCallCtx(t *testing.T, ctx context.Context, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Logs_20140328."+operation)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logsCall %s: %v", operation, err)
	}
	return resp
}

func logsCBORCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := cbor.Marshal(body)
	if err != nil {
		t.Fatalf("marshal CBOR %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/service/Logs_20140328/operation/"+operation, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build CBOR %s request: %v", operation, err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logsCBORCall %s: %v", operation, err)
	}
	return resp
}

// decodeCBORErrorType returns the __type discriminator from an RPC v2 CBOR
// error response body.
func decodeCBORErrorType(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Type string `cbor:"__type"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode CBOR error response: %v", err)
	}
	return body.Type
}

// listTagsLogGroup returns a log group's tags, failing the test if the call
// does not succeed.
func listTagsLogGroup(t *testing.T, srv *helpers.TestServer, name string) map[string]string {
	t.Helper()
	resp := logsCall(t, srv, "ListTagsLogGroup", map[string]any{"logGroupName": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Tags map[string]string `json:"tags"`
	}
	helpers.DecodeJSON(t, resp, &result)
	return result.Tags
}

// createLogGroup is a test setup helper that creates a log group and fails the
// test immediately if the call does not succeed.
func createLogGroup(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	resp := logsCall(t, srv, "CreateLogGroup", map[string]any{
		"logGroupName": name,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// createLogStream is a test setup helper that creates a log stream within the
// given group and fails the test immediately if the call does not succeed.
func createLogStream(t *testing.T, srv *helpers.TestServer, groupName, streamName string) {
	t.Helper()
	resp := logsCall(t, srv, "CreateLogStream", map[string]any{
		"logGroupName":  groupName,
		"logStreamName": streamName,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// evtBase anchors the small, readable relative offsets these tests use for
// event timestamps (0, 1000, 2000, …) inside PutLogEvents' accepted ingestion
// window. AWS discards a log event older than 14 days or more than two hours
// in the future and reports it only in `rejectedLogEventsInfo` (#1721), so a
// bare epoch-millisecond literal — over fifty years old — is data the emulator
// now correctly refuses to store. One fixed anchor per test binary keeps the
// offsets comparable across a test's own events while staying in the window.
var evtBase = time.Now().Add(-time.Hour).UnixMilli()

// evtTS turns one of those offsets into the absolute epoch millisecond the
// wire carries.
func evtTS(offset int64) int64 { return evtBase + offset }

// logEvent is the wire shape used by PutLogEvents.
type logEvent struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}

// putLogEvents is a test setup helper that writes events to a stream and fails
// the test immediately if the call does not succeed.
func putLogEvents(t *testing.T, srv *helpers.TestServer, groupName, streamName string, events []logEvent) {
	t.Helper()
	resp := logsCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  groupName,
		"logStreamName": streamName,
		"logEvents":     events,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

type eventStreamMessage struct {
	Headers map[string]string
	Payload []byte
}

type liveTailTestResult struct {
	LogGroupIdentifier string `json:"logGroupIdentifier"`
	LogStreamName      string `json:"logStreamName"`
	Message            string `json:"message"`
	Timestamp          int64  `json:"timestamp"`
	IngestionTime      int64  `json:"ingestionTime"`
}

type liveTailTestUpdate struct {
	SessionMetadata struct {
		Sampled bool `json:"sampled"`
	} `json:"sessionMetadata"`
	SessionResults []liveTailTestResult `json:"sessionResults"`
}

// readLiveTailSessionOpen consumes the two frames every StartLiveTail session
// opens with — the initial-response the SDK deserializers park on, then
// sessionStart — and fails the test if either is missing or out of order.
// TestStartLiveTail_opensWithInitialResponseFrame is where the opening frame's
// own headers and payload are asserted.
func readLiveTailSessionOpen(t *testing.T, r io.Reader) {
	t.Helper()
	initial := readEventStreamMessage(t, r)
	if initial.Headers[":event-type"] != "initial-response" {
		t.Fatalf("first event type = %q, want initial-response", initial.Headers[":event-type"])
	}
	start := readEventStreamMessage(t, r)
	if start.Headers[":event-type"] != "sessionStart" {
		t.Fatalf("second event type = %q, want sessionStart", start.Headers[":event-type"])
	}
}

func readLiveTailUpdate(t *testing.T, r io.Reader) liveTailTestUpdate {
	t.Helper()
	update := readEventStreamMessage(t, r)
	if update.Headers[":event-type"] != "sessionUpdate" {
		t.Fatalf("event type = %q, want sessionUpdate", update.Headers[":event-type"])
	}
	var payload liveTailTestUpdate
	if err := json.Unmarshal(update.Payload, &payload); err != nil {
		t.Fatalf("decode sessionUpdate payload: %v", err)
	}
	return payload
}

func readEventStreamMessage(t *testing.T, r io.Reader) eventStreamMessage {
	t.Helper()
	prelude := make([]byte, 12)
	if _, err := io.ReadFull(r, prelude); err != nil {
		t.Fatalf("read event-stream prelude: %v", err)
	}
	totalLen := int(binary.BigEndian.Uint32(prelude[0:4]))
	headersLen := int(binary.BigEndian.Uint32(prelude[4:8]))
	if totalLen < 16 || headersLen > totalLen-16 {
		t.Fatalf("invalid event-stream lengths: total=%d headers=%d", totalLen, headersLen)
	}
	rest := make([]byte, totalLen-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		t.Fatalf("read event-stream message: %v", err)
	}
	headersBuf := rest[:headersLen]
	payload := rest[headersLen : len(rest)-4]
	headers := make(map[string]string)
	for len(headersBuf) > 0 {
		nameLen := int(headersBuf[0])
		headersBuf = headersBuf[1:]
		if len(headersBuf) < nameLen+3 {
			t.Fatalf("invalid event-stream header")
		}
		name := string(headersBuf[:nameLen])
		headersBuf = headersBuf[nameLen:]
		valueType := headersBuf[0]
		headersBuf = headersBuf[1:]
		if valueType != 7 {
			t.Fatalf("unsupported event-stream header type %d for %s", valueType, name)
		}
		valueLen := int(binary.BigEndian.Uint16(headersBuf[:2]))
		headersBuf = headersBuf[2:]
		if len(headersBuf) < valueLen {
			t.Fatalf("invalid event-stream header value")
		}
		headers[name] = string(headersBuf[:valueLen])
		headersBuf = headersBuf[valueLen:]
	}
	return eventStreamMessage{Headers: headers, Payload: payload}
}

// ---- PutRetentionPolicy ----------------------------------------------------

func TestPutRetentionPolicy_success(t *testing.T) {
	// Given: a log group exists
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/retention-test")

	// When: PutRetentionPolicy is called with retentionInDays=7
	resp := logsCall(t, srv, "PutRetentionPolicy", map[string]any{
		"logGroupName":    "/aws/lambda/retention-test",
		"retentionInDays": 7,
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: DescribeLogGroups reflects the retention setting
	resp2 := logsCall(t, srv, "DescribeLogGroups", map[string]any{
		"logGroupNamePrefix": "/aws/lambda/retention-test",
	})
	defer resp2.Body.Close()
	var result struct {
		LogGroups []struct {
			LogGroupName    string `json:"logGroupName"`
			RetentionInDays int    `json:"retentionInDays"`
		} `json:"logGroups"`
	}
	helpers.DecodeJSON(t, resp2, &result)
	if len(result.LogGroups) != 1 {
		t.Fatalf("expected 1 log group, got %d", len(result.LogGroups))
	}
	if result.LogGroups[0].RetentionInDays != 7 {
		t.Errorf("expected retentionInDays=7, got %d", result.LogGroups[0].RetentionInDays)
	}
}

func TestPutRetentionPolicy_notFound(t *testing.T) {
	// Given: no log groups exist
	srv := helpers.NewTestServer(t)

	// When: PutRetentionPolicy is called on a non-existent group
	resp := logsCall(t, srv, "PutRetentionPolicy", map[string]any{
		"logGroupName":    "/aws/lambda/no-such-group",
		"retentionInDays": 7,
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// Real AWS restricts retentionInDays to a fixed value set and returns
// InvalidParameterException for anything else; arbitrary integers must not be
// accepted, whether from SDK callers or the CloudFormation path.
func TestPutRetentionPolicy_invalidValueRejected(t *testing.T) {
	// Given: a log group exists
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/retention-invalid")

	for _, days := range []int{-1, 0, 2, 6, 15, 366, 3654} {
		// When: PutRetentionPolicy is called with a value outside the allowed set
		resp := logsCall(t, srv, "PutRetentionPolicy", map[string]any{
			"logGroupName":    "/aws/lambda/retention-invalid",
			"retentionInDays": days,
		})

		// Then: 400 InvalidParameterException
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "InvalidParameterException")
		resp.Body.Close()
	}

	// And: the group's retention is unchanged
	resp := logsCall(t, srv, "DescribeLogGroups", map[string]any{
		"logGroupNamePrefix": "/aws/lambda/retention-invalid",
	})
	defer resp.Body.Close()
	var result struct {
		LogGroups []struct {
			RetentionInDays int `json:"retentionInDays"`
		} `json:"logGroups"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.LogGroups) != 1 || result.LogGroups[0].RetentionInDays != 0 {
		t.Fatalf("retention mutated by rejected values: %+v", result.LogGroups)
	}
}

// Request-shape validation runs ahead of the resource lookup, as it does in
// AWS's own front end: an unsupported retention value on a log group that does
// not exist is reported as InvalidParameterException, not ResourceNotFound.
func TestPutRetentionPolicy_invalidValueBeatsMissingGroup(t *testing.T) {
	// Given: no log groups exist
	srv := helpers.NewTestServer(t)

	// When: PutRetentionPolicy names a missing group with an invalid value
	resp := logsCall(t, srv, "PutRetentionPolicy", map[string]any{
		"logGroupName":    "/aws/lambda/no-such-group",
		"retentionInDays": 2,
	})
	defer resp.Body.Close()

	// Then: the parameter error wins
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// The value set is enforced in the shared typed implementation, so the RPC v2
// CBOR path must reject exactly what JSON 1.1 rejects — and must leave an
// already-configured retention policy untouched when it does.
func TestRPCv2CBOR_PutRetentionPolicy_invalidValueRejected(t *testing.T) {
	// Given: a log group with a valid retention policy already applied
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/cbor-retention")
	resp := logsCBORCall(t, srv, "PutRetentionPolicy", map[string]any{
		"logGroupName":    "/aws/lambda/cbor-retention",
		"retentionInDays": 30,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: PutRetentionPolicy is called over CBOR with a value outside the set
	resp = logsCBORCall(t, srv, "PutRetentionPolicy", map[string]any{
		"logGroupName":    "/aws/lambda/cbor-retention",
		"retentionInDays": 2,
	})
	defer resp.Body.Close()

	// Then: 400 InvalidParameterException in the CBOR error shape
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	if got := decodeCBORErrorType(t, resp); got != "InvalidParameterException" {
		t.Fatalf("__type = %q, want InvalidParameterException", got)
	}

	// And: the previously applied retention policy is unchanged
	resp2 := logsCBORCall(t, srv, "DescribeLogGroups", map[string]any{
		"logGroupNamePrefix": "/aws/lambda/cbor-retention",
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var result struct {
		LogGroups []struct {
			RetentionInDays int `cbor:"retentionInDays"`
		} `cbor:"logGroups"`
	}
	if err := cbor.NewDecoder(resp2.Body).Decode(&result); err != nil {
		t.Fatalf("decode CBOR DescribeLogGroups response: %v", err)
	}
	if len(result.LogGroups) != 1 || result.LogGroups[0].RetentionInDays != 30 {
		t.Fatalf("retention mutated by rejected CBOR value: %+v", result.LogGroups)
	}
}

// Every documented value must be accepted.
func TestPutRetentionPolicy_allowedValuesAccepted(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/retention-allowed")

	for _, days := range []int{1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653} {
		resp := logsCall(t, srv, "PutRetentionPolicy", map[string]any{
			"logGroupName":    "/aws/lambda/retention-allowed",
			"retentionInDays": days,
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}
}

// ---- DeleteRetentionPolicy -------------------------------------------------

func TestDeleteRetentionPolicy_success(t *testing.T) {
	// Given: a log group exists with a retention policy set
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/delete-retention")
	resp := logsCall(t, srv, "PutRetentionPolicy", map[string]any{
		"logGroupName":    "/aws/lambda/delete-retention",
		"retentionInDays": 30,
	})
	resp.Body.Close()

	// When: DeleteRetentionPolicy is called
	resp = logsCall(t, srv, "DeleteRetentionPolicy", map[string]any{
		"logGroupName": "/aws/lambda/delete-retention",
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: DescribeLogGroups shows retentionInDays is 0 (unset)
	resp2 := logsCall(t, srv, "DescribeLogGroups", map[string]any{
		"logGroupNamePrefix": "/aws/lambda/delete-retention",
	})
	defer resp2.Body.Close()
	var result struct {
		LogGroups []struct {
			RetentionInDays int `json:"retentionInDays"`
		} `json:"logGroups"`
	}
	helpers.DecodeJSON(t, resp2, &result)
	if len(result.LogGroups) != 1 {
		t.Fatalf("expected 1 log group, got %d", len(result.LogGroups))
	}
	if result.LogGroups[0].RetentionInDays != 0 {
		t.Errorf("expected retentionInDays=0 after deletion, got %d", result.LogGroups[0].RetentionInDays)
	}
}

func TestDeleteRetentionPolicy_notFound(t *testing.T) {
	// Given: no log groups exist
	srv := helpers.NewTestServer(t)

	// When: DeleteRetentionPolicy is called on a non-existent group
	resp := logsCall(t, srv, "DeleteRetentionPolicy", map[string]any{
		"logGroupName": "/aws/lambda/no-such-group",
	})
	defer resp.Body.Close()

	// Then: 400 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ---- GetLogEvents pagination contract (pagination-plan.md G1) --------------

type getLogEventsResult struct {
	Events []struct {
		Timestamp     int64  `json:"timestamp"`
		Message       string `json:"message"`
		IngestionTime int64  `json:"ingestionTime"`
	} `json:"events"`
	NextForwardToken  string `json:"nextForwardToken"`
	NextBackwardToken string `json:"nextBackwardToken"`
}

func getLogEvents(t *testing.T, srv *helpers.TestServer, body map[string]any) getLogEventsResult {
	t.Helper()
	resp := logsCall(t, srv, "GetLogEvents", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result getLogEventsResult
	helpers.DecodeJSON(t, resp, &result)
	return result
}

// TestGetLogEvents_TailLoop_ForwardTokenDoesNotReRead is G1's headline
// failing-first test (pagination-plan.md G1): before this item,
// nextForwardToken was synthesized from len(allEvents) and never checked
// against a real position, so polling with the returned token re-fetched
// the full event set forever — exactly the bug a CloudWatch Logs tail loop
// (the standard client usage pattern for this operation) hits immediately.
func TestGetLogEvents_TailLoop_ForwardTokenDoesNotReRead(t *testing.T) {
	// Given: a stream with three events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/tail-loop")
	createLogStream(t, srv, "/aws/lambda/tail-loop", "stream")
	putLogEvents(t, srv, "/aws/lambda/tail-loop", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "one"},
		{Timestamp: evtTS(2000), Message: "two"},
		{Timestamp: evtTS(3000), Message: "three"},
	})

	// When: GetLogEvents is called from the head, then polled again with the
	// returned forward token (the standard CloudWatch Logs tail-loop pattern)
	first := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/tail-loop",
		"logStreamName": "stream",
		"startFromHead": true,
	})
	if len(first.Events) != 3 {
		t.Fatalf("first call: expected 3 events, got %d", len(first.Events))
	}
	if first.NextForwardToken == "" {
		t.Fatal("first call: expected a non-empty nextForwardToken")
	}
	second := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/tail-loop",
		"logStreamName": "stream",
		"nextToken":     first.NextForwardToken,
	})

	// Then: no new events have been written, so the poll must return ZERO
	// events — not the same 3 again (the exact broken behavior G1 fixes).
	if len(second.Events) != 0 {
		t.Fatalf("poll with forward token re-received %d events (want 0) — this is the exact broken behavior G1 fixes", len(second.Events))
	}
}

// TestGetLogEvents_SameTokenWhenExhausted pins the same-token-when-exhausted
// termination convention (pagination-plan.md G1's accept criterion): once a
// direction has caught up, GetLogEvents must echo back the SAME token it
// was given rather than a new one — SDK paginators and tail loops rely on
// comparing consecutive tokens to detect "nothing new yet."
func TestGetLogEvents_SameTokenWhenExhausted(t *testing.T) {
	// Given: a stream with one event
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/exhausted")
	createLogStream(t, srv, "/aws/lambda/exhausted", "stream")
	putLogEvents(t, srv, "/aws/lambda/exhausted", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "one"},
	})

	// When: GetLogEvents is called from the head, then polled again with the
	// returned forward token (nothing new has been written yet)
	first := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/exhausted",
		"logStreamName": "stream",
		"startFromHead": true,
	})
	token1 := first.NextForwardToken
	second := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/exhausted",
		"logStreamName": "stream",
		"nextToken":     token1,
	})

	// Then: the poll returns 0 events and echoes the SAME token back
	if len(second.Events) != 0 {
		t.Fatalf("expected 0 events on the exhausted poll, got %d", len(second.Events))
	}
	if second.NextForwardToken != token1 {
		t.Fatalf("nextForwardToken on an exhausted poll = %q, want the SAME token %q (same-token-when-exhausted)", second.NextForwardToken, token1)
	}

	// A third poll with the (unchanged) token must remain stable —
	// idempotent, never drifting to a new token on repeated empty polls.
	third := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/exhausted",
		"logStreamName": "stream",
		"nextToken":     token1,
	})
	if third.NextForwardToken != token1 {
		t.Fatalf("nextForwardToken drifted on a repeated exhausted poll: %q, want %q", third.NextForwardToken, token1)
	}

	// New events arriving after the exhausted poll must surface on the next
	// poll with the SAME token — proving the tail-loop actually progresses
	// once there's something new, not just that it stays stable when idle.
	putLogEvents(t, srv, "/aws/lambda/exhausted", "stream", []logEvent{
		{Timestamp: evtTS(2000), Message: "two"},
	})
	fourth := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/exhausted",
		"logStreamName": "stream",
		"nextToken":     token1,
	})
	if len(fourth.Events) != 1 || fourth.Events[0].Message != "two" {
		t.Fatalf("expected the newly-written event to surface on the next poll, got %+v", fourth.Events)
	}
}

// TestGetLogEvents_TailLoop_ExactlyOnce drives a small-limit tail loop over
// a 5-event stream and asserts every event is seen exactly once, in order,
// and the loop terminates via the same-token convention.
func TestGetLogEvents_TailLoop_ExactlyOnce(t *testing.T) {
	// Given: a stream with five events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/exactly-once")
	createLogStream(t, srv, "/aws/lambda/exactly-once", "stream")
	putLogEvents(t, srv, "/aws/lambda/exactly-once", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "e1"},
		{Timestamp: evtTS(2000), Message: "e2"},
		{Timestamp: evtTS(3000), Message: "e3"},
		{Timestamp: evtTS(4000), Message: "e4"},
		{Timestamp: evtTS(5000), Message: "e5"},
	})

	// When: a tail loop drains the stream 2 events at a time
	var seen []string
	token := ""
	for pages := 0; pages < 20; pages++ {
		body := map[string]any{
			"logGroupName":  "/aws/lambda/exactly-once",
			"logStreamName": "stream",
			"limit":         2,
		}
		if token == "" {
			body["startFromHead"] = true
		} else {
			body["nextToken"] = token
		}
		page := getLogEvents(t, srv, body)
		for _, e := range page.Events {
			seen = append(seen, e.Message)
		}
		if page.NextForwardToken == token && token != "" {
			break // caught up
		}
		token = page.NextForwardToken
		if len(page.Events) == 0 {
			break
		}
	}

	// Then: every event was seen exactly once, in order
	want := []string{"e1", "e2", "e3", "e4", "e5"}
	if len(seen) != len(want) {
		t.Fatalf("tail loop collected %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("tail loop order = %v, want %v", seen, want)
		}
	}
}

// TestGetLogEvents_StartFromHead_BothDirections proves StartFromHead
// selects direction on a fresh (no-token) call: true returns the earliest
// events, false (the documented default) returns the latest.
func TestGetLogEvents_StartFromHead_BothDirections(t *testing.T) {
	// Given: a stream with three events
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/start-from-head")
	createLogStream(t, srv, "/aws/lambda/start-from-head", "stream")
	putLogEvents(t, srv, "/aws/lambda/start-from-head", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "oldest"},
		{Timestamp: evtTS(2000), Message: "middle"},
		{Timestamp: evtTS(3000), Message: "newest"},
	})

	// When/Then: startFromHead=true returns the earliest events
	head := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/start-from-head",
		"logStreamName": "stream",
		"startFromHead": true,
		"limit":         2,
	})
	if len(head.Events) != 2 || head.Events[0].Message != "oldest" || head.Events[1].Message != "middle" {
		t.Fatalf("startFromHead=true: got %+v, want [oldest middle]", head.Events)
	}

	// When/Then: startFromHead=false returns the latest events
	tail := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/start-from-head",
		"logStreamName": "stream",
		"startFromHead": false,
		"limit":         2,
	})
	if len(tail.Events) != 2 || tail.Events[0].Message != "middle" || tail.Events[1].Message != "newest" {
		t.Fatalf("startFromHead=false: got %+v, want [middle newest]", tail.Events)
	}

	// Default (omitted) must match startFromHead=false per AWS docs.
	defaultDir := getLogEvents(t, srv, map[string]any{
		"logGroupName":  "/aws/lambda/start-from-head",
		"logStreamName": "stream",
		"limit":         2,
	})
	if len(defaultDir.Events) != 2 || defaultDir.Events[0].Message != "middle" || defaultDir.Events[1].Message != "newest" {
		t.Fatalf("default direction: got %+v, want [middle newest] (default must match startFromHead=false)", defaultDir.Events)
	}
}

// TestGetLogEvents_InvalidNextToken proves a garbled nextToken returns
// GetLogEvents' documented InvalidParameterException instead of silently
// restarting the read.
func TestGetLogEvents_InvalidNextToken(t *testing.T) {
	// Given: a group and stream exist
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/bad-token")
	createLogStream(t, srv, "/aws/lambda/bad-token", "stream")

	// When: GetLogEvents is called with a garbled nextToken
	resp := logsCall(t, srv, "GetLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/bad-token",
		"logStreamName": "stream",
		"nextToken":     "not-a-real-token",
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException, not a silent restart
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ---- FilterLogEvents limit + nextToken (pagination-plan.md G6) -------------

// TestFilterLogEvents_paginationContract walks a multi-stream group with a
// filter pattern via helpers.PaginationContractTest (pagination-plan.md H2),
// proving exactly-once delivery, termination, and the documented
// invalid-token error — the same contract every other paginated operation
// in this codebase is held to.
func TestFilterLogEvents_paginationContract(t *testing.T) {
	// Given: two streams with 6 matching ERROR events interleaved by
	// timestamp, plus a few non-matching INFO events mixed in
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/filter-pagination")
	createLogStream(t, srv, "/aws/lambda/filter-pagination", "stream-a")
	createLogStream(t, srv, "/aws/lambda/filter-pagination", "stream-b")

	putLogEvents(t, srv, "/aws/lambda/filter-pagination", "stream-a", []logEvent{
		{Timestamp: evtTS(1000), Message: "ERROR e1"},
		{Timestamp: evtTS(3000), Message: "INFO ignored-1"},
		{Timestamp: evtTS(5000), Message: "ERROR e3"},
		{Timestamp: evtTS(7000), Message: "ERROR e5"},
	})
	putLogEvents(t, srv, "/aws/lambda/filter-pagination", "stream-b", []logEvent{
		{Timestamp: evtTS(2000), Message: "ERROR e2"},
		{Timestamp: evtTS(4000), Message: "INFO ignored-2"},
		{Timestamp: evtTS(6000), Message: "ERROR e4"},
		{Timestamp: evtTS(8000), Message: "ERROR e6"},
	})
	wantIDs := []string{"e1", "e2", "e3", "e4", "e5", "e6"}

	fetch := func(t *testing.T, token string) ([]string, string) {
		t.Helper()
		body := map[string]any{
			"logGroupName":  "/aws/lambda/filter-pagination",
			"filterPattern": "ERROR",
			"limit":         2,
		}
		if token != "" {
			body["nextToken"] = token
		}
		resp := logsCall(t, srv, "FilterLogEvents", body)
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var page filterResult
		helpers.DecodeJSON(t, resp, &page)
		ids := make([]string, len(page.Events))
		for i, e := range page.Events {
			// Message is "ERROR eN" — the last two characters are the ID.
			ids[i] = e.Message[len(e.Message)-2:]
		}
		next := ""
		if page.NextToken != nil {
			next = *page.NextToken
		}
		return ids, next
	}

	probe := func(t *testing.T) (int, string) {
		t.Helper()
		resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
			"logGroupName": "/aws/lambda/filter-pagination",
			"nextToken":    "not-a-real-token",
		})
		defer resp.Body.Close()
		var errResp struct {
			Type string `json:"__type"`
		}
		helpers.DecodeJSON(t, resp, &errResp)
		return resp.StatusCode, errResp.Type
	}

	// When/Then: walking FilterLogEvents with limit=2 must deliver each
	// matching event exactly once, in order, terminate, and reject a
	// garbled nextToken with the documented AWS error.
	helpers.PaginationContractTest(t, wantIDs, func(id string) string { return id }, fetch, probe,
		helpers.PaginationContractOptions{
			WantInvalidTokenStatus:    http.StatusBadRequest,
			WantInvalidTokenErrorCode: "InvalidParameterException",
		})
}

// TestFilterLogEvents_BufferedEventVisibleInWindow proves
// storage-access-plan.md A4's P4 requirement end-to-end: an event ingested
// less than one debounce interval ago (still sitting in the stream's write
// buffer, not yet flushed to the backend) is visible to FilterLogEvents —
// exactly as it was before the group-range pushdown replaced the old
// per-stream full reads.
func TestFilterLogEvents_BufferedEventVisibleInWindow(t *testing.T) {
	// Given: a stream with a single event, written immediately before the
	// FilterLogEvents call — well under the flush watermark and debounce
	// interval, so it should still be sitting in the write buffer
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/aws/lambda/filter-buffered")
	createLogStream(t, srv, "/aws/lambda/filter-buffered", "stream")
	putLogEvents(t, srv, "/aws/lambda/filter-buffered", "stream", []logEvent{
		{Timestamp: evtTS(1000), Message: "ERROR freshly buffered"},
	})

	// When: FilterLogEvents is called over a window covering that event
	resp := logsCall(t, srv, "FilterLogEvents", map[string]any{
		"logGroupName":  "/aws/lambda/filter-buffered",
		"filterPattern": "ERROR",
		"startTime":     evtTS(0),
		"endTime":       evtTS(2000),
	})
	defer resp.Body.Close()

	// Then: the buffered event is visible
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result filterResult
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Events) != 1 || result.Events[0].Message != "ERROR freshly buffered" {
		t.Fatalf("expected the freshly-buffered event to be visible, got %+v", result.Events)
	}
}
