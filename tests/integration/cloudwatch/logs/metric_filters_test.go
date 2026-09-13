package logs_test

// metric_filters_test.go — the wire contract of PutMetricFilter,
// DescribeMetricFilters, DeleteMetricFilter and TestMetricFilter (#1949) over
// AWS JSON 1.1 and Smithy RPC v2 CBOR. The metric-publishing side is covered
// in tests/integration/cloudwatch (an alarm fires from log lines) and by the
// package's own unit tests.

import (
	"net/http"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

type describedMetricFilter struct {
	FilterName            string `json:"filterName" cbor:"filterName"`
	FilterPattern         string `json:"filterPattern" cbor:"filterPattern"`
	LogGroupName          string `json:"logGroupName" cbor:"logGroupName"`
	CreationTime          int64  `json:"creationTime" cbor:"creationTime"`
	MetricTransformations []struct {
		MetricName      string            `json:"metricName" cbor:"metricName"`
		MetricNamespace string            `json:"metricNamespace" cbor:"metricNamespace"`
		MetricValue     string            `json:"metricValue" cbor:"metricValue"`
		DefaultValue    *float64          `json:"defaultValue" cbor:"defaultValue"`
		Dimensions      map[string]string `json:"dimensions" cbor:"dimensions"`
		Unit            string            `json:"unit" cbor:"unit"`
	} `json:"metricTransformations" cbor:"metricTransformations"`
}

type describeMetricFiltersResult struct {
	MetricFilters []describedMetricFilter `json:"metricFilters" cbor:"metricFilters"`
	NextToken     string                  `json:"nextToken" cbor:"nextToken"`
}

func putMetricFilter(t *testing.T, srv *helpers.TestServer, group, name, pattern string, transformation map[string]any) {
	t.Helper()
	resp := logsCall(t, srv, "PutMetricFilter", map[string]any{
		"logGroupName":          group,
		"filterName":            name,
		"filterPattern":         pattern,
		"metricTransformations": []map[string]any{transformation},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func describeMetricFilters(t *testing.T, srv *helpers.TestServer, body map[string]any) describeMetricFiltersResult {
	t.Helper()
	resp := logsCall(t, srv, "DescribeMetricFilters", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out describeMetricFiltersResult
	helpers.DecodeJSON(t, resp, &out)
	return out
}

func TestPutMetricFilter_success(t *testing.T) {
	// Given: a log group
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/mf/put")

	// When: the AWS reference filter is put
	resp := logsCall(t, srv, "PutMetricFilter", map[string]any{
		"logGroupName":  "/mf/put",
		"filterName":    "my-metric-filter",
		"filterPattern": "[ip, identity, user_id, timestamp, request, status_code, size]",
		"metricTransformations": []map[string]any{{
			"metricValue":     "$size",
			"metricNamespace": "MyApp",
			"metricName":      "Volume",
			"dimensions":      map[string]string{"Request": "$request", "UserId": "$user_id"},
			"unit":            "Count",
		}},
	})
	defer resp.Body.Close()

	// Then: it succeeds with an empty body, and DescribeMetricFilters
	// reports it in the documented shape
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	if body := helpers.ReadBody(t, resp); body != "{}" {
		t.Fatalf("PutMetricFilter body = %q, want {}", body)
	}
	described := describeMetricFilters(t, srv, map[string]any{"logGroupName": "/mf/put"})
	if len(described.MetricFilters) != 1 {
		t.Fatalf("metricFilters = %+v, want one", described.MetricFilters)
	}
	got := described.MetricFilters[0]
	if got.FilterName != "my-metric-filter" || got.LogGroupName != "/mf/put" || got.CreationTime == 0 {
		t.Fatalf("metricFilters[0] = %+v", got)
	}
	if len(got.MetricTransformations) != 1 {
		t.Fatalf("metricTransformations = %+v, want one", got.MetricTransformations)
	}
	tr := got.MetricTransformations[0]
	if tr.MetricName != "Volume" || tr.MetricNamespace != "MyApp" || tr.MetricValue != "$size" || tr.Unit != "Count" {
		t.Fatalf("metricTransformations[0] = %+v", tr)
	}
	if tr.Dimensions["Request"] != "$request" || tr.Dimensions["UserId"] != "$user_id" {
		t.Fatalf("dimensions = %v", tr.Dimensions)
	}
	if tr.DefaultValue != nil {
		t.Fatalf("defaultValue = %v, want omitted", *tr.DefaultValue)
	}
}

func TestPutMetricFilter_errors(t *testing.T) {
	// Given: a log group
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/mf/errors")
	transformation := map[string]any{"metricName": "Errors", "metricNamespace": "App", "metricValue": "1"}

	for name, tc := range map[string]struct {
		body map[string]any
		code string
	}{
		"missing log group": {
			body: map[string]any{"logGroupName": "/mf/missing", "filterName": "f", "filterPattern": "ERROR", "metricTransformations": []map[string]any{transformation}},
			code: "ResourceNotFoundException",
		},
		"invalid filter name": {
			body: map[string]any{"logGroupName": "/mf/errors", "filterName": "a:b", "filterPattern": "ERROR", "metricTransformations": []map[string]any{transformation}},
			code: "InvalidParameterException",
		},
		"two transformations": {
			body: map[string]any{"logGroupName": "/mf/errors", "filterName": "f", "filterPattern": "ERROR", "metricTransformations": []map[string]any{transformation, transformation}},
			code: "InvalidParameterException",
		},
		"defaultValue with dimensions": {
			body: map[string]any{"logGroupName": "/mf/errors", "filterName": "f", "filterPattern": "[a, b]", "metricTransformations": []map[string]any{{
				"metricName": "Errors", "metricNamespace": "App", "metricValue": "1", "defaultValue": 0, "dimensions": map[string]string{"A": "$a"},
			}}},
			code: "InvalidParameterException",
		},
	} {
		t.Run(name, func(t *testing.T) {
			// When: the request is put
			resp := logsCall(t, srv, "PutMetricFilter", tc.body)
			defer resp.Body.Close()

			// Then: it is refused with the modeled error
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, tc.code)
			helpers.AssertRequestID(t, resp)
		})
	}
}

func TestDescribeMetricFilters_pagesAndFilters(t *testing.T) {
	// Given: three filters across two groups
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/mf/describe-a")
	createLogGroup(t, srv, "/mf/describe-b")
	putMetricFilter(t, srv, "/mf/describe-a", "errors", "ERROR", map[string]any{"metricName": "Errors", "metricNamespace": "App", "metricValue": "1"})
	putMetricFilter(t, srv, "/mf/describe-a", "warnings", "WARN", map[string]any{"metricName": "Warnings", "metricNamespace": "App", "metricValue": "1"})
	putMetricFilter(t, srv, "/mf/describe-b", "errors-b", "ERROR", map[string]any{"metricName": "Errors", "metricNamespace": "App", "metricValue": "1"})

	// When / Then: the group filter, the prefix, the metric filter and
	// limit + nextToken each narrow the name-sorted list
	byGroup := describeMetricFilters(t, srv, map[string]any{"logGroupName": "/mf/describe-a"})
	if len(byGroup.MetricFilters) != 2 || byGroup.MetricFilters[0].FilterName != "errors" || byGroup.MetricFilters[1].FilterName != "warnings" {
		t.Fatalf("by group = %+v", byGroup.MetricFilters)
	}
	byPrefix := describeMetricFilters(t, srv, map[string]any{"logGroupName": "/mf/describe-a", "filterNamePrefix": "warn"})
	if len(byPrefix.MetricFilters) != 1 || byPrefix.MetricFilters[0].FilterName != "warnings" {
		t.Fatalf("by prefix = %+v", byPrefix.MetricFilters)
	}
	byMetric := describeMetricFilters(t, srv, map[string]any{"metricName": "Errors", "metricNamespace": "App"})
	if len(byMetric.MetricFilters) != 2 || byMetric.MetricFilters[0].FilterName != "errors" || byMetric.MetricFilters[1].FilterName != "errors-b" {
		t.Fatalf("by metric = %+v", byMetric.MetricFilters)
	}
	first := describeMetricFilters(t, srv, map[string]any{"limit": 2})
	if len(first.MetricFilters) != 2 || first.NextToken == "" {
		t.Fatalf("first page = %+v", first)
	}
	second := describeMetricFilters(t, srv, map[string]any{"limit": 2, "nextToken": first.NextToken})
	if len(second.MetricFilters) != 1 || second.MetricFilters[0].FilterName != "warnings" || second.NextToken != "" {
		t.Fatalf("second page = %+v", second)
	}

	// And: metricName without metricNamespace is refused
	resp := logsCall(t, srv, "DescribeMetricFilters", map[string]any{"metricName": "Errors"})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestDeleteMetricFilter_success(t *testing.T) {
	// Given: a filter
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/mf/delete")
	putMetricFilter(t, srv, "/mf/delete", "errors", "ERROR", map[string]any{"metricName": "Errors", "metricNamespace": "App", "metricValue": "1"})

	// When: it is deleted
	resp := logsCall(t, srv, "DeleteMetricFilter", map[string]any{"logGroupName": "/mf/delete", "filterName": "errors"})
	defer resp.Body.Close()

	// Then: the delete succeeds, the group lists nothing, and deleting it
	// again is not found
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	if left := describeMetricFilters(t, srv, map[string]any{"logGroupName": "/mf/delete"}); len(left.MetricFilters) != 0 {
		t.Fatalf("metricFilters after delete = %+v", left.MetricFilters)
	}
	again := logsCall(t, srv, "DeleteMetricFilter", map[string]any{"logGroupName": "/mf/delete", "filterName": "errors"})
	defer again.Body.Close()
	helpers.AssertStatus(t, again, http.StatusBadRequest)
	helpers.AssertJSONError(t, again, "ResourceNotFoundException")
}

func TestTestMetricFilter_success(t *testing.T) {
	// Given: a running server — TestMetricFilter needs no log group
	srv := helpers.NewTestServer(t)

	// When: AWS's second reference example is run
	resp := logsCall(t, srv, "TestMetricFilter", map[string]any{
		"filterPattern": "[..., size]",
		"logEventMessages": []string{
			`127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`,
			`127.0.0.1 - frank [10/Oct/2000:13:35:22 -0700] "GET /apache_pb.gif HTTP/1.0" 500 5324`,
		},
	})
	defer resp.Body.Close()

	// Then: every message matches, numbered from zero, with the documented
	// extracted values
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	var out struct {
		Matches []struct {
			EventNumber     int64             `json:"eventNumber"`
			EventMessage    string            `json:"eventMessage"`
			ExtractedValues map[string]string `json:"extractedValues"`
		} `json:"matches"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Matches) != 2 {
		t.Fatalf("matches = %+v, want two", out.Matches)
	}
	if out.Matches[0].EventNumber != 0 || out.Matches[1].EventNumber != 1 {
		t.Fatalf("eventNumbers = %d, %d, want 0, 1", out.Matches[0].EventNumber, out.Matches[1].EventNumber)
	}
	want := map[string]string{
		"$size": "1534", "$6": "200", "$4": "10/Oct/2000:13:25:15 -0700", "$5": "GET /apache_pb.gif HTTP/1.0",
		"$2": "-", "$3": "frank", "$1": "127.0.0.1",
	}
	if len(out.Matches[0].ExtractedValues) != len(want) {
		t.Fatalf("extractedValues = %v, want %v", out.Matches[0].ExtractedValues, want)
	}
	for k, v := range want {
		if out.Matches[0].ExtractedValues[k] != v {
			t.Fatalf("extractedValues[%s] = %q, want %q", k, out.Matches[0].ExtractedValues[k], v)
		}
	}
}

func TestTestMetricFilter_textPatternReportsEmptyExtractedValues(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: a text pattern is tested
	resp := logsCall(t, srv, "TestMetricFilter", map[string]any{
		"filterPattern":    `"[ERROR]"`,
		"logEventMessages": []string{"[INFO] fine", "[ERROR] boom"},
	})
	defer resp.Body.Close()

	// Then: the match carries an empty extractedValues object, present on
	// the wire as `{}` the way AWS's own example shows it
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if body != `{"matches":[{"eventNumber":1,"eventMessage":"[ERROR] boom","extractedValues":{}}]}` {
		t.Fatalf("body = %s", body)
	}
}

func TestMetricFilters_CBOR(t *testing.T) {
	// Given: a log group
	srv := helpers.NewTestServer(t)
	createLogGroup(t, srv, "/mf/cbor")

	// When: the filter is put, described, tested and deleted over RPC v2 CBOR
	put := logsCBORCall(t, srv, "PutMetricFilter", map[string]any{
		"logGroupName":  "/mf/cbor",
		"filterName":    "errors",
		"filterPattern": `{ $.level = "ERROR" }`,
		"metricTransformations": []map[string]any{{
			"metricName": "Errors", "metricNamespace": "App", "metricValue": "1", "defaultValue": 0.0,
		}},
	})
	defer put.Body.Close()
	helpers.AssertStatus(t, put, http.StatusOK)

	describe := logsCBORCall(t, srv, "DescribeMetricFilters", map[string]any{"logGroupName": "/mf/cbor"})
	defer describe.Body.Close()
	helpers.AssertStatus(t, describe, http.StatusOK)
	var described describeMetricFiltersResult
	if err := cbor.NewDecoder(describe.Body).Decode(&described); err != nil {
		t.Fatalf("decode CBOR DescribeMetricFilters: %v", err)
	}

	test := logsCBORCall(t, srv, "TestMetricFilter", map[string]any{
		"filterPattern":    `{ $.level = "ERROR" }`,
		"logEventMessages": []string{`{"level":"ERROR"}`, `{"level":"INFO"}`},
	})
	defer test.Body.Close()
	helpers.AssertStatus(t, test, http.StatusOK)
	var tested struct {
		Matches []struct {
			EventNumber     int64             `cbor:"eventNumber"`
			ExtractedValues map[string]string `cbor:"extractedValues"`
		} `cbor:"matches"`
	}
	if err := cbor.NewDecoder(test.Body).Decode(&tested); err != nil {
		t.Fatalf("decode CBOR TestMetricFilter: %v", err)
	}

	del := logsCBORCall(t, srv, "DeleteMetricFilter", map[string]any{"logGroupName": "/mf/cbor", "filterName": "errors"})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	missing := logsCBORCall(t, srv, "DeleteMetricFilter", map[string]any{"logGroupName": "/mf/cbor", "filterName": "errors"})
	defer missing.Body.Close()

	// Then: every response carries the same shape the JSON path does
	if len(described.MetricFilters) != 1 || described.MetricFilters[0].FilterName != "errors" {
		t.Fatalf("CBOR metricFilters = %+v", described.MetricFilters)
	}
	tr := described.MetricFilters[0].MetricTransformations
	if len(tr) != 1 || tr[0].MetricName != "Errors" || tr[0].DefaultValue == nil || *tr[0].DefaultValue != 0 {
		t.Fatalf("CBOR metricTransformations = %+v", tr)
	}
	if len(tested.Matches) != 1 || tested.Matches[0].EventNumber != 0 || tested.Matches[0].ExtractedValues["$.level"] != "ERROR" {
		t.Fatalf("CBOR matches = %+v", tested.Matches)
	}
	helpers.AssertStatus(t, missing, http.StatusBadRequest)
	if got := decodeCBORErrorType(t, missing); got != "ResourceNotFoundException" {
		t.Fatalf("CBOR error __type = %q, want ResourceNotFoundException", got)
	}
}
