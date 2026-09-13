package cloudwatch_test

// metric_filter_alarm_test.go — the acceptance test for #1949, end to end
// over the wire: log lines become a CloudWatch metric through a metric filter,
// and an alarm on that metric fires from them alone. No PutMetricData anywhere
// in this test; the datapoints exist only because PutLogEvents ran through the
// filter into the service-metrics recorder router.New wires for every
// service, and the unmodified alarm evaluator read them back.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// logsJSONCall is a CloudWatch Logs call over AWS JSON 1.1 — the Logs
// half of a test whose CloudWatch half goes through cwCall.
func logsJSONCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build %s request: %v", operation, err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Logs_20140328."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logsJSONCall %s: %v", operation, err)
	}
	return resp
}

func mustLogsJSONCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) {
	t.Helper()
	resp := logsJSONCall(t, srv, operation, body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestMetricFilter_alarmFiresFromLogEvents(t *testing.T) {
	// Given: a mock-clock server, a log group and stream, a metric filter
	// counting ERROR lines into App/Errors, and an alarm on that metric
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	const group, stream = "/app/api", "instance-1"
	mustLogsJSONCall(t, srv, "CreateLogGroup", map[string]any{"logGroupName": group})
	mustLogsJSONCall(t, srv, "CreateLogStream", map[string]any{"logGroupName": group, "logStreamName": stream})
	mustLogsJSONCall(t, srv, "PutMetricFilter", map[string]any{
		"logGroupName":  group,
		"filterName":    "errors",
		"filterPattern": "ERROR",
		"metricTransformations": []map[string]any{{
			"metricName": "Errors", "metricNamespace": "App", "metricValue": "1",
		}},
	})
	alarm := cwCall(t, srv, "PutMetricAlarm", url.Values{
		"AlarmName":          {"app-errors"},
		"Namespace":          {"App"},
		"MetricName":         {"Errors"},
		"Statistic":          {"Sum"},
		"Period":             {"60"},
		"EvaluationPeriods":  {"1"},
		"Threshold":          {"1"},
		"ComparisonOperator": {"GreaterThanThreshold"},
	})
	defer alarm.Body.Close()
	helpers.AssertStatus(t, alarm, http.StatusOK)

	// When: three log lines arrive, two of them errors, and the clock moves
	// past the end of the period they landed in
	now := srv.Clock.Now().UnixMilli()
	mustLogsJSONCall(t, srv, "PutLogEvents", map[string]any{
		"logGroupName":  group,
		"logStreamName": stream,
		"logEvents": []map[string]any{
			{"timestamp": now, "message": "ERROR upstream timed out"},
			{"timestamp": now, "message": "INFO request served"},
			{"timestamp": now, "message": "ERROR upstream timed out again"},
		},
	})
	srv.Clock.Add(90 * time.Second)

	// Then: the alarm is in ALARM, from log lines alone
	describe := cwCall(t, srv, "DescribeAlarms", url.Values{"AlarmNames.member.1": {"app-errors"}})
	defer describe.Body.Close()
	helpers.AssertStatus(t, describe, http.StatusOK)
	body, err := io.ReadAll(describe.Body)
	if err != nil {
		t.Fatalf("read DescribeAlarms body: %v", err)
	}
	if !strings.Contains(string(body), "<StateValue>ALARM</StateValue>") {
		t.Fatalf("expected ALARM state in response, got: %s", body)
	}

	// And: GetMetricStatistics reports the datapoint the filter published —
	// Sum 2 across the two matching lines, and Count for the unit it
	// defaulted to
	from := time.UnixMilli(now).UTC()
	stats := cwCall(t, srv, "GetMetricStatistics", url.Values{
		"Namespace":           {"App"},
		"MetricName":          {"Errors"},
		"StartTime":           {from.Add(-time.Minute).Format(time.RFC3339)},
		"EndTime":             {from.Add(2 * time.Minute).Format(time.RFC3339)},
		"Period":              {"60"},
		"Statistics.member.1": {"Sum"},
		"Statistics.member.2": {"SampleCount"},
	})
	defer stats.Body.Close()
	helpers.AssertStatus(t, stats, http.StatusOK)
	statsBody, err := io.ReadAll(stats.Body)
	if err != nil {
		t.Fatalf("read GetMetricStatistics body: %v", err)
	}
	for _, want := range []string{"<Sum>2</Sum>", "<SampleCount>2</SampleCount>", "<Unit>None</Unit>"} {
		if !strings.Contains(string(statsBody), want) {
			t.Fatalf("expected %s in GetMetricStatistics response, got: %s", want, statsBody)
		}
	}
}
