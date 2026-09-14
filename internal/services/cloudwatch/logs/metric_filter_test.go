package logs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/metrics"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// ---- Harness ----------------------------------------------------------------

// fakeRecorder is a metrics.Recorder that keeps every observation it is
// handed, so a test can assert on exactly what a metric filter published.
type fakeRecorder struct {
	mu   sync.Mutex
	seen []metrics.Observation
}

func (r *fakeRecorder) Observe(_ context.Context, o metrics.Observation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, o)
	return nil
}

func (r *fakeRecorder) observations() []metrics.Observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]metrics.Observation, len(r.seen))
	copy(out, r.seen)
	return out
}

// newMetricFilterService builds a Logs service on a memory store with a mock
// clock and a fake recorder wired through InitMetrics — the same entry point
// router.New uses.
func newMetricFilterService(t *testing.T) (*Service, *fakeRecorder, *clock.Mock) {
	t.Helper()
	mock := clock.NewMock()
	mock.Set(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	svc := New(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, state.NewMemoryStore(), zap.NewNop(), mock)
	t.Cleanup(func() { svc.Stop(context.Background()) })
	rec := &fakeRecorder{}
	svc.InitMetrics(rec)
	return svc, rec, mock
}

func mustCreateGroup(t *testing.T, svc *Service, name string) {
	t.Helper()
	if _, aerr := svc.handler.createLogGroupTyped(context.Background(), &createLogGroupRequest{LogGroupName: name}); aerr != nil {
		t.Fatalf("CreateLogGroup %s: %v", name, aerr)
	}
}

func mustCreateStream(t *testing.T, svc *Service, group, stream string) {
	t.Helper()
	if _, aerr := svc.handler.createLogStreamTyped(context.Background(), &createLogStreamRequest{LogGroupName: group, LogStreamName: stream}); aerr != nil {
		t.Fatalf("CreateLogStream %s/%s: %v", group, stream, aerr)
	}
}

func strPtr(s string) *string { return &s }

func countTransformation(namespace, name string) []metricTransformationWire {
	return []metricTransformationWire{{MetricName: name, MetricNamespace: namespace, MetricValue: "1"}}
}

func putFilter(t *testing.T, svc *Service, group, name, pattern string, transformations []metricTransformationWire) {
	t.Helper()
	_, aerr := svc.handler.putMetricFilterTyped(context.Background(), &putMetricFilterRequest{
		LogGroupName: group, FilterName: name, FilterPattern: strPtr(pattern), MetricTransformations: transformations,
	})
	if aerr != nil {
		t.Fatalf("PutMetricFilter %s: %v", name, aerr)
	}
}

func describeFilters(t *testing.T, svc *Service, req *describeMetricFiltersRequest) *describeMetricFiltersResponse {
	t.Helper()
	resp, aerr := svc.handler.describeMetricFiltersTyped(context.Background(), req)
	if aerr != nil {
		t.Fatalf("DescribeMetricFilters: %v", aerr)
	}
	return resp
}

func filterNames(resp *describeMetricFiltersResponse) []string {
	out := make([]string, 0, len(resp.MetricFilters))
	for _, f := range resp.MetricFilters {
		out = append(out, f.FilterName)
	}
	return out
}

func assertAWSError(t *testing.T, aerr *protocol.AWSError, code string) {
	t.Helper()
	if aerr == nil {
		t.Fatalf("expected %s, got success", code)
	}
	if aerr.Code != code {
		t.Fatalf("error code = %s (%s), want %s", aerr.Code, aerr.Message, code)
	}
}

// ---- PutMetricFilter --------------------------------------------------------

func TestPutMetricFilter_validation(t *testing.T) {
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	valid := func() *putMetricFilterRequest {
		return &putMetricFilterRequest{
			LogGroupName: "g", FilterName: "f", FilterPattern: strPtr("ERROR"),
			MetricTransformations: countTransformation("App", "Errors"),
		}
	}
	zero := 0.0
	cases := []struct {
		name   string
		mutate func(r *putMetricFilterRequest)
		code   string
	}{
		{name: "missing logGroupName", mutate: func(r *putMetricFilterRequest) { r.LogGroupName = "" }, code: "InvalidParameterException"},
		{name: "unknown log group", mutate: func(r *putMetricFilterRequest) { r.LogGroupName = "nope" }, code: "ResourceNotFoundException"},
		{name: "missing filterName", mutate: func(r *putMetricFilterRequest) { r.FilterName = "" }, code: "InvalidParameterException"},
		{name: "filterName over 512", mutate: func(r *putMetricFilterRequest) { r.FilterName = strings.Repeat("a", 513) }, code: "InvalidParameterException"},
		{name: "filterName with colon", mutate: func(r *putMetricFilterRequest) { r.FilterName = "a:b" }, code: "InvalidParameterException"},
		{name: "filterName with asterisk", mutate: func(r *putMetricFilterRequest) { r.FilterName = "a*" }, code: "InvalidParameterException"},
		{name: "missing filterPattern", mutate: func(r *putMetricFilterRequest) { r.FilterPattern = nil }, code: "InvalidParameterException"},
		{name: "filterPattern over 1024", mutate: func(r *putMetricFilterRequest) { r.FilterPattern = strPtr(strings.Repeat("a", 1025)) }, code: "InvalidParameterException"},
		{name: "unparseable filterPattern", mutate: func(r *putMetricFilterRequest) { r.FilterPattern = strPtr("{ $.a ~ 1 }") }, code: "InvalidParameterException"},
		{name: "no transformations", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations = nil }, code: "InvalidParameterException"},
		{name: "two transformations", mutate: func(r *putMetricFilterRequest) {
			r.MetricTransformations = append(r.MetricTransformations, r.MetricTransformations[0])
		}, code: "InvalidParameterException"},
		{name: "missing metricName", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricName = "" }, code: "InvalidParameterException"},
		{name: "metricName with dollar", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricName = "a$b" }, code: "InvalidParameterException"},
		{name: "metricName over 255", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricName = strings.Repeat("a", 256) }, code: "InvalidParameterException"},
		{name: "missing metricNamespace", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricNamespace = "" }, code: "InvalidParameterException"},
		{name: "metricNamespace with colon", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricNamespace = "a:b" }, code: "InvalidParameterException"},
		{name: "missing metricValue", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricValue = "" }, code: "InvalidParameterException"},
		{name: "non-numeric literal metricValue", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricValue = "lots" }, code: "InvalidParameterException"},
		{name: "metricValue over 100", mutate: func(r *putMetricFilterRequest) {
			r.MetricTransformations[0].MetricValue = "$" + strings.Repeat("a", 100)
		}, code: "InvalidParameterException"},
		{name: "field metricValue on a text pattern", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].MetricValue = "$size" }, code: "InvalidParameterException"},
		{name: "invalid unit", mutate: func(r *putMetricFilterRequest) { r.MetricTransformations[0].Unit = "Furlongs" }, code: "InvalidParameterException"},
		{name: "four dimensions", mutate: func(r *putMetricFilterRequest) {
			r.FilterPattern = strPtr("[a, b, c, d]")
			r.MetricTransformations[0].Dimensions = map[string]string{"A": "$a", "B": "$b", "C": "$c", "D": "$d"}
		}, code: "InvalidParameterException"},
		{name: "dimensions with defaultValue", mutate: func(r *putMetricFilterRequest) {
			r.FilterPattern = strPtr("[a, b]")
			r.MetricTransformations[0].Dimensions = map[string]string{"A": "$a"}
			r.MetricTransformations[0].DefaultValue = &zero
		}, code: "InvalidParameterException"},
		{name: "dimensions on a text pattern", mutate: func(r *putMetricFilterRequest) {
			r.MetricTransformations[0].Dimensions = map[string]string{"A": "$a"}
		}, code: "InvalidParameterException"},
		{name: "dimension value that is not a field reference", mutate: func(r *putMetricFilterRequest) {
			r.FilterPattern = strPtr("[a, b]")
			r.MetricTransformations[0].Dimensions = map[string]string{"A": "literal"}
		}, code: "InvalidParameterException"},
		{name: "empty dimension name", mutate: func(r *putMetricFilterRequest) {
			r.FilterPattern = strPtr("[a, b]")
			r.MetricTransformations[0].Dimensions = map[string]string{"": "$a"}
		}, code: "InvalidParameterException"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an otherwise valid request with one field broken
			req := valid()
			tc.mutate(req)

			// When: it is put
			_, aerr := svc.handler.putMetricFilterTyped(context.Background(), req)

			// Then: it is refused with the modeled error and nothing is stored
			assertAWSError(t, aerr, tc.code)
			if resp := describeFilters(t, svc, &describeMetricFiltersRequest{}); len(resp.MetricFilters) != 0 {
				t.Fatalf("a rejected request stored a filter: %+v", resp.MetricFilters)
			}
		})
	}
}

func TestPutMetricFilter_validRequestIsAccepted(t *testing.T) {
	// Given: a group, and the AWS reference request: a space-delimited
	// pattern, a field metricValue, two dimensions and a unit
	svc, _, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "my-log-group")

	// When: the filter is put
	putFilter(t, svc, "my-log-group", "my-metric-filter", "[ip, identity, user_id, timestamp, request, status_code, size]",
		[]metricTransformationWire{{
			MetricName: "Volume", MetricNamespace: "MyApp", MetricValue: "$size",
			Dimensions: map[string]string{"Request": "$request", "UserId": "$user_id"}, Unit: "Count",
		}})

	// Then: DescribeMetricFilters reports it whole, with the creation time
	resp := describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "my-log-group"})
	require.Len(t, resp.MetricFilters, 1)
	got := resp.MetricFilters[0]
	assert.Equal(t, "my-metric-filter", got.FilterName)
	assert.Equal(t, "my-log-group", got.LogGroupName)
	assert.Equal(t, "[ip, identity, user_id, timestamp, request, status_code, size]", got.FilterPattern)
	assert.Equal(t, mock.Now().UnixMilli(), got.CreationTime)
	require.Len(t, got.MetricTransformations, 1)
	assert.Equal(t, metricTransformationWire{
		MetricName: "Volume", MetricNamespace: "MyApp", MetricValue: "$size",
		Dimensions: map[string]string{"Request": "$request", "UserId": "$user_id"}, Unit: "Count",
	}, got.MetricTransformations[0])
}

func TestPutMetricFilter_emptyPatternMatchesEverything(t *testing.T) {
	// Given: a group
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")

	// When: a filter with the empty pattern is put (required, but may be "")
	putFilter(t, svc, "g", "all", "", countTransformation("App", "Lines"))

	// Then: it is stored with the empty pattern
	resp := describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "g"})
	require.Len(t, resp.MetricFilters, 1)
	assert.Equal(t, "", resp.MetricFilters[0].FilterPattern)
}

func TestPutMetricFilter_sameNameReplaces(t *testing.T) {
	// Given: a filter
	svc, _, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	putFilter(t, svc, "g", "f", "ERROR", countTransformation("App", "Errors"))
	created := mock.Now().UnixMilli()
	mock.Add(time.Minute)

	// When: the same name is put with a new pattern and metric
	putFilter(t, svc, "g", "f", "WARN", countTransformation("App", "Warnings"))

	// Then: there is still one filter, carrying the new definition and the
	// original creation time
	resp := describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "g"})
	require.Len(t, resp.MetricFilters, 1)
	assert.Equal(t, "WARN", resp.MetricFilters[0].FilterPattern)
	assert.Equal(t, "Warnings", resp.MetricFilters[0].MetricTransformations[0].MetricName)
	assert.Equal(t, created, resp.MetricFilters[0].CreationTime)
}

func TestPutMetricFilter_perGroupLimit(t *testing.T) {
	// Given: a group at the 100-filter quota
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateGroup(t, svc, "other")
	for i := range metricFiltersPerGroupMax {
		putFilter(t, svc, "g", fmt.Sprintf("f-%03d", i), "ERROR", countTransformation("App", "Errors"))
	}

	// When: a 101st name is put
	_, aerr := svc.handler.putMetricFilterTyped(context.Background(), &putMetricFilterRequest{
		LogGroupName: "g", FilterName: "one-too-many", FilterPattern: strPtr("ERROR"), MetricTransformations: countTransformation("App", "Errors"),
	})

	// Then: it is refused, while a replace and another group are not
	assertAWSError(t, aerr, "LimitExceededException")
	putFilter(t, svc, "g", "f-000", "WARN", countTransformation("App", "Warnings"))
	putFilter(t, svc, "other", "f", "ERROR", countTransformation("App", "Errors"))
}

// ---- DescribeMetricFilters --------------------------------------------------

func TestDescribeMetricFilters_filtersAndSorts(t *testing.T) {
	// Given: filters across two groups, put out of name order
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "a")
	mustCreateGroup(t, svc, "b")
	putFilter(t, svc, "a", "zeta", "ERROR", countTransformation("App", "Errors"))
	putFilter(t, svc, "a", "alpha", "WARN", countTransformation("App", "Warnings"))
	putFilter(t, svc, "a", "beta", "INFO", countTransformation("Other", "Errors"))
	putFilter(t, svc, "b", "alpha-b", "ERROR", countTransformation("App", "Errors"))

	// When / Then: each documented filter narrows the ASCII-sorted list
	assert.Equal(t, []string{"alpha", "alpha-b", "beta", "zeta"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{})))
	assert.Equal(t, []string{"alpha", "beta", "zeta"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "a"})))
	assert.Equal(t, []string{"alpha"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "a", FilterNamePrefix: "al"})))
	// filterNamePrefix counts only alongside logGroupName (API docs).
	assert.Equal(t, []string{"alpha", "alpha-b", "beta", "zeta"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{FilterNamePrefix: "al"})))
	assert.Equal(t, []string{"alpha-b", "zeta"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{MetricName: "Errors", MetricNamespace: "App"})))
	assert.Equal(t, []string{"beta"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{MetricName: "Errors", MetricNamespace: "Other"})))
}

func TestDescribeMetricFilters_metricNameRequiresNamespace(t *testing.T) {
	svc, _, _ := newMetricFilterService(t)
	for _, req := range []*describeMetricFiltersRequest{{MetricName: "Errors"}, {MetricNamespace: "App"}} {
		_, aerr := svc.handler.describeMetricFiltersTyped(context.Background(), req)
		assertAWSError(t, aerr, "InvalidParameterException")
	}
}

func TestDescribeMetricFilters_unknownLogGroup(t *testing.T) {
	svc, _, _ := newMetricFilterService(t)
	_, aerr := svc.handler.describeMetricFiltersTyped(context.Background(), &describeMetricFiltersRequest{LogGroupName: "nope"})
	assertAWSError(t, aerr, "ResourceNotFoundException")
}

func TestDescribeMetricFilters_pagination(t *testing.T) {
	// Given: 120 filters — more than the 50-item maximum page
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateGroup(t, svc, "h")
	for i := range 120 {
		group := "g"
		if i%2 == 1 {
			group = "h"
		}
		putFilter(t, svc, group, fmt.Sprintf("f-%03d", i), "ERROR", countTransformation("App", "Errors"))
	}

	// When: the list is walked with the default limit, then with limit 7
	var walked []string
	token := ""
	pages := 0
	for {
		resp := describeFilters(t, svc, &describeMetricFiltersRequest{NextToken: token})
		pages++
		if pages == 1 {
			assert.Len(t, resp.MetricFilters, describeDefaultLimit)
		}
		walked = append(walked, filterNames(resp)...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	small := describeFilters(t, svc, &describeMetricFiltersRequest{Limit: 7})

	// Then: every filter appears exactly once, in order, across 3 pages
	assert.Equal(t, 3, pages)
	require.Len(t, walked, 120)
	for i, name := range walked {
		assert.Equal(t, fmt.Sprintf("f-%03d", i), name)
	}
	assert.Len(t, small.MetricFilters, 7)
	assert.NotEmpty(t, small.NextToken)

	// And: a limit above the maximum is capped, and a bad token refused
	assert.Len(t, describeFilters(t, svc, &describeMetricFiltersRequest{Limit: 500}).MetricFilters, describeMaxLimit)
	_, aerr := svc.handler.describeMetricFiltersTyped(context.Background(), &describeMetricFiltersRequest{NextToken: "not-a-token"})
	assertAWSError(t, aerr, "InvalidParameterException")
}

// ---- DeleteMetricFilter -----------------------------------------------------

func TestDeleteMetricFilter_removesOnlyTheNamedFilter(t *testing.T) {
	// Given: two filters
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	putFilter(t, svc, "g", "keep", "ERROR", countTransformation("App", "Errors"))
	putFilter(t, svc, "g", "drop", "WARN", countTransformation("App", "Warnings"))

	// When: one is deleted
	_, aerr := svc.handler.deleteMetricFilterTyped(context.Background(), &deleteMetricFilterRequest{LogGroupName: "g", FilterName: "drop"})
	require.Nil(t, aerr)

	// Then: only the other remains, and a second delete is not found
	assert.Equal(t, []string{"keep"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "g"})))
	_, aerr = svc.handler.deleteMetricFilterTyped(context.Background(), &deleteMetricFilterRequest{LogGroupName: "g", FilterName: "drop"})
	assertAWSError(t, aerr, "ResourceNotFoundException")
}

func TestDeleteMetricFilter_validation(t *testing.T) {
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	for _, tc := range []struct {
		req  *deleteMetricFilterRequest
		code string
	}{
		{&deleteMetricFilterRequest{FilterName: "f"}, "InvalidParameterException"},
		{&deleteMetricFilterRequest{LogGroupName: "g"}, "InvalidParameterException"},
		{&deleteMetricFilterRequest{LogGroupName: "nope", FilterName: "f"}, "ResourceNotFoundException"},
	} {
		_, aerr := svc.handler.deleteMetricFilterTyped(context.Background(), tc.req)
		assertAWSError(t, aerr, tc.code)
	}
}

func TestDeleteLogGroup_deletesItsMetricFilters(t *testing.T) {
	// Given: filters on two groups
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "gone")
	mustCreateGroup(t, svc, "stays")
	putFilter(t, svc, "gone", "f1", "ERROR", countTransformation("App", "Errors"))
	putFilter(t, svc, "gone", "f2", "WARN", countTransformation("App", "Warnings"))
	putFilter(t, svc, "stays", "f3", "ERROR", countTransformation("App", "Errors"))

	// When: one group is deleted
	_, aerr := svc.handler.deleteLogGroupTyped(context.Background(), &deleteLogGroupRequest{LogGroupName: "gone"})
	require.Nil(t, aerr)

	// Then: its filters are gone from the region-wide listing too
	assert.Equal(t, []string{"f3"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{})))
}

// ---- TestMetricFilter -------------------------------------------------------

func TestTestMetricFilter_textPattern(t *testing.T) {
	// AWS's sixth reference example: eventNumber is zero-based and
	// extractedValues is an empty object for a text pattern.
	svc, _, _ := newMetricFilterService(t)
	resp, aerr := svc.handler.testMetricFilterTyped(context.Background(), &testMetricFilterRequest{
		FilterPattern: strPtr(`"[ERROR]"`),
		LogEventMessages: []string{
			"02 May 2014 00:34:12,525 [INFO] Starting the application",
			"02 May 2014 00:35:14,245 [DEBUG] Database connection established",
			"02 May 2014 00:34:14,663 [INFO] Executing SQL Query",
			"02 May 2014 00:34:16,142 [ERROR] Unhanded exception: InvalidQueryException",
			"02 May 2014 00:34:16,224 [ERROR] Terminating the application",
		},
	})
	require.Nil(t, aerr)
	assert.Equal(t, []metricFilterMatchRecord{
		{EventNumber: 3, EventMessage: "02 May 2014 00:34:16,142 [ERROR] Unhanded exception: InvalidQueryException", ExtractedValues: map[string]string{}},
		{EventNumber: 4, EventMessage: "02 May 2014 00:34:16,224 [ERROR] Terminating the application", ExtractedValues: map[string]string{}},
	}, resp.Matches)
}

func TestTestMetricFilter_spaceDelimitedPattern(t *testing.T) {
	// AWS's fourth reference example, constrained columns included.
	svc, _, _ := newMetricFilterService(t)
	resp, aerr := svc.handler.testMetricFilterTyped(context.Background(), &testMetricFilterRequest{
		FilterPattern: strPtr("[..., status_code=200, size]"),
		LogEventMessages: []string{
			`127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`,
			`127.0.0.1 - frank [10/Oct/2000:13:35:22 -0700] "GET /apache_pb.gif HTTP/1.0" 500 5324`,
			`127.0.0.1 - frank [10/Oct/2000:13:50:35 -0700] "GET /apache_pb.gif HTTP/1.0" 200 4355`,
		},
	})
	require.Nil(t, aerr)
	require.Len(t, resp.Matches, 2)
	assert.Equal(t, int64(0), resp.Matches[0].EventNumber)
	assert.Equal(t, int64(2), resp.Matches[1].EventNumber)
	assert.Equal(t, map[string]string{
		"$status_code": "200", "$size": "1534",
		"$1": "127.0.0.1", "$2": "-", "$3": "frank", "$4": "10/Oct/2000:13:25:15 -0700", "$5": "GET /apache_pb.gif HTTP/1.0",
	}, resp.Matches[0].ExtractedValues)
}

func TestTestMetricFilter_jsonPattern(t *testing.T) {
	svc, _, _ := newMetricFilterService(t)
	resp, aerr := svc.handler.testMetricFilterTyped(context.Background(), &testMetricFilterRequest{
		FilterPattern:    strPtr(`{ $.level = "ERROR" && $.latency > 100 }`),
		LogEventMessages: []string{`{"level":"ERROR","latency":250}`, `{"level":"ERROR","latency":5}`, `plain text`},
	})
	require.Nil(t, aerr)
	assert.Equal(t, []metricFilterMatchRecord{
		{EventNumber: 0, EventMessage: `{"level":"ERROR","latency":250}`, ExtractedValues: map[string]string{"$.level": "ERROR", "$.latency": "250"}},
	}, resp.Matches)
}

func TestTestMetricFilter_noMatchesIsAnEmptyList(t *testing.T) {
	svc, _, _ := newMetricFilterService(t)
	resp, aerr := svc.handler.testMetricFilterTyped(context.Background(), &testMetricFilterRequest{
		FilterPattern: strPtr("ERROR"), LogEventMessages: []string{"all fine"},
	})
	require.Nil(t, aerr)
	assert.NotNil(t, resp.Matches)
	assert.Empty(t, resp.Matches)
}

func TestTestMetricFilter_validation(t *testing.T) {
	svc, _, _ := newMetricFilterService(t)
	tooMany := make([]string, testMetricFilterMessagesMax+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	for name, req := range map[string]*testMetricFilterRequest{
		"missing pattern":   {LogEventMessages: []string{"x"}},
		"no messages":       {FilterPattern: strPtr("x")},
		"too many messages": {FilterPattern: strPtr("x"), LogEventMessages: tooMany},
		"empty message":     {FilterPattern: strPtr("x"), LogEventMessages: []string{""}},
		"bad pattern":       {FilterPattern: strPtr("{ $.a }"), LogEventMessages: []string{"x"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, aerr := svc.handler.testMetricFilterTyped(context.Background(), req)
			assertAWSError(t, aerr, "InvalidParameterException")
		})
	}
}

// ---- Ingest: log events become metric datapoints ----------------------------

func putEvents(t *testing.T, svc *Service, group, stream string, at time.Time, messages ...string) {
	t.Helper()
	inputs := make([]logEventInput, 0, len(messages))
	for _, m := range messages {
		inputs = append(inputs, logEventInput{Timestamp: at.UnixMilli(), Message: m})
	}
	if _, aerr := svc.handler.putLogEventsTyped(context.Background(), &putLogEventsRequest{LogGroupName: group, LogStreamName: stream, LogEvents: inputs}); aerr != nil {
		t.Fatalf("PutLogEvents: %v", aerr)
	}
}

func TestMetricFilter_putLogEventsPublishesCountDatapoints(t *testing.T) {
	// Given: a text-pattern count filter
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	putFilter(t, svc, "g", "errors", "ERROR", countTransformation("App", "Errors"))

	// When: a batch with two matching and one non-matching event arrives,
	// timestamped a minute before now
	at := mock.Now().Add(-time.Minute)
	putEvents(t, svc, "g", "s", at, "ERROR one", "INFO fine", "ERROR two")

	// Then: one observation per match, valued 1, at the event's own time,
	// unit None, no dimensions
	got := rec.observations()
	require.Len(t, got, 2)
	for _, o := range got {
		assert.Equal(t, metrics.Observation{
			Namespace: "App", Name: "Errors", Timestamp: at.UTC(), Unit: "None", Value: 1,
		}, o)
	}
}

func TestMetricFilter_lambdaLogWriterPathPublishes(t *testing.T) {
	// Given: a filter, and the events.LogWriter Lambda writes through
	svc, rec, mock := newMetricFilterService(t)
	lw := svc.LogWriter()
	require.NoError(t, lw.EnsureLogStream(context.Background(), "/aws/lambda/fn", "2026/09/14/[$LATEST]abc"))
	putFilter(t, svc, "/aws/lambda/fn", "task-timeouts", "Task timed out", countTransformation("Custom/Lambda", "Timeouts"))

	// When: Lambda writes a batch containing a timeout line
	at := mock.Now().Add(-time.Minute)
	err := lw.WriteLogEvents(context.Background(), "/aws/lambda/fn", "2026/09/14/[$LATEST]abc", []events.LogEntry{
		{Timestamp: at.UnixMilli(), Message: "START RequestId: 1"},
		{Timestamp: at.UnixMilli(), Message: "2026-09-14T12:00:00Z 1 Task timed out after 3.00 seconds"},
	})
	require.NoError(t, err)

	// Then: the filter published for it — the Lambda path goes through the
	// same appendEvents hook, not the events bus PutLogEvents publishes on
	got := rec.observations()
	require.Len(t, got, 1)
	assert.Equal(t, "Custom/Lambda", got[0].Namespace)
	assert.Equal(t, "Timeouts", got[0].Name)
	assert.Equal(t, at.UTC(), got[0].Timestamp)
}

func TestMetricFilter_fieldValueDimensionsAndUnit(t *testing.T) {
	// Given: the AWS reference filter — bytes from $size, dimensioned by
	// $request and $user_id, in Bytes
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	putFilter(t, svc, "g", "volume", "[ip, identity, user_id, timestamp, request, status_code, size]",
		[]metricTransformationWire{{
			MetricName: "Volume", MetricNamespace: "MyApp", MetricValue: "$size",
			Dimensions: map[string]string{"Request": "$request", "UserId": "$user_id"}, Unit: "Bytes",
		}})

	// When: an access line arrives
	at := mock.Now().Add(-time.Minute)
	putEvents(t, svc, "g", "s", at, `127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`)

	// Then: the datapoint carries the extracted value, both dimensions
	// (delimiters stripped) and the unit
	got := rec.observations()
	require.Len(t, got, 1)
	assert.Equal(t, 1534.0, got[0].Value)
	assert.Equal(t, "Bytes", got[0].Unit)
	assert.ElementsMatch(t, []metrics.Dimension{
		{Name: "Request", Value: "GET /apache_pb.gif HTTP/1.0"},
		{Name: "UserId", Value: "frank"},
	}, got[0].Dimensions)
}

func TestMetricFilter_jsonFieldValue(t *testing.T) {
	// Given: a JSON filter whose value and dimension come from properties
	// the pattern itself never mentions
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	putFilter(t, svc, "g", "latency", `{ $.level = "INFO" }`,
		[]metricTransformationWire{{
			MetricName: "Latency", MetricNamespace: "App", MetricValue: "$.timing.ms",
			Dimensions: map[string]string{"Route": "$.route"}, Unit: "Milliseconds",
		}})

	// When: matching events arrive — one numeric, one with a non-numeric
	// value, one missing the value, one missing the dimension
	at := mock.Now().Add(-time.Minute)
	putEvents(t, svc, "g", "s", at,
		`{"level":"INFO","route":"/a","timing":{"ms":12.5}}`,
		`{"level":"INFO","route":"/a","timing":{"ms":"slow"}}`,
		`{"level":"INFO","route":"/a"}`,
		`{"level":"INFO","timing":{"ms":7}}`,
	)

	// Then: the non-numeric and missing values are skipped; the missing
	// dimension is left off its datapoint rather than dropping it
	got := rec.observations()
	require.Len(t, got, 2)
	assert.Equal(t, 12.5, got[0].Value)
	assert.Equal(t, []metrics.Dimension{{Name: "Route", Value: "/a"}}, got[0].Dimensions)
	assert.Equal(t, 7.0, got[1].Value)
	assert.Empty(t, got[1].Dimensions)
	assert.Equal(t, "Milliseconds", got[1].Unit)
}

func TestMetricFilter_defaultValueOncePerPeriodOfIngestion(t *testing.T) {
	// Given: a count filter with defaultValue 0
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	zero := 0.0
	putFilter(t, svc, "g", "errors", "ERROR", []metricTransformationWire{{
		MetricName: "Errors", MetricNamespace: "App", MetricValue: "1", DefaultValue: &zero,
	}})

	// When: minute 1 sees only non-matching events across two batches,
	// minute 2 sees one batch with a miss BEFORE its match, a later batch
	// with only a miss, and minute 3 sees nothing at all
	minute1 := mock.Now().Add(-3 * time.Minute).Truncate(time.Minute)
	minute2 := minute1.Add(time.Minute)
	putEvents(t, svc, "g", "s", minute1, "INFO a", "INFO b")
	putEvents(t, svc, "g", "s", minute1.Add(30*time.Second), "INFO c")
	putEvents(t, svc, "g", "s", minute2, "INFO before", "ERROR boom")
	putEvents(t, svc, "g", "s", minute2.Add(10*time.Second), "INFO d")

	// Then: minute 1 gets the default exactly once (for a period that
	// ingested but never matched), minute 2 gets the match and no default —
	// the batch is decided as a whole, so the miss ahead of the match does
	// not report one — and the silent minute 3 gets nothing
	got := rec.observations()
	require.Len(t, got, 2)
	assert.Equal(t, metrics.Observation{Namespace: "App", Name: "Errors", Timestamp: minute1.UTC(), Unit: "None", Value: 0}, got[0])
	assert.Equal(t, metrics.Observation{Namespace: "App", Name: "Errors", Timestamp: minute2.UTC(), Unit: "None", Value: 1}, got[1])
}

func TestMetricFilter_defaultValueDoesNotDiluteLambdaInvocationBatch(t *testing.T) {
	// Given: the CDK-idiomatic latency filter — a JSON value with
	// defaultValue 0 — and the events.LogWriter Lambda writes through
	svc, rec, mock := newMetricFilterService(t)
	lw := svc.LogWriter()
	require.NoError(t, lw.EnsureLogStream(context.Background(), "/aws/lambda/fn", "2026/09/14/[$LATEST]abc"))
	zero := 0.0
	putFilter(t, svc, "/aws/lambda/fn", "latency", "{ $.latency >= 0 }", []metricTransformationWire{{
		MetricName: "Latency", MetricNamespace: "App", MetricValue: "$.latency", DefaultValue: &zero,
	}})

	// When: one invocation's batch arrives — START, the application line,
	// END — with the non-matching START ahead of the match
	at := mock.Now().Add(-time.Minute)
	require.NoError(t, lw.WriteLogEvents(context.Background(), "/aws/lambda/fn", "2026/09/14/[$LATEST]abc", []events.LogEntry{
		{Timestamp: at.UnixMilli(), Message: "START RequestId: 1 Version: $LATEST"},
		{Timestamp: at.UnixMilli(), Message: `{"latency":800}`},
		{Timestamp: at.UnixMilli(), Message: "END RequestId: 1"},
	}))

	// Then: the minute carries the one real datapoint and no default, so
	// Average is 800 as on AWS rather than 400
	got := rec.observations()
	require.Len(t, got, 1)
	assert.Equal(t, 800.0, got[0].Value)
}

func TestMetricFilter_deletedFilterStopsPublishing(t *testing.T) {
	// Given: a filter that has already published once (so it is cached)
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	putFilter(t, svc, "g", "errors", "ERROR", countTransformation("App", "Errors"))
	at := mock.Now().Add(-time.Minute)
	putEvents(t, svc, "g", "s", at, "ERROR one")
	require.Len(t, rec.observations(), 1)

	// When: the filter is deleted and another matching event arrives
	_, aerr := svc.handler.deleteMetricFilterTyped(context.Background(), &deleteMetricFilterRequest{LogGroupName: "g", FilterName: "errors"})
	require.Nil(t, aerr)
	putEvents(t, svc, "g", "s", at, "ERROR two")

	// Then: nothing more is published — the compiled cache was invalidated
	assert.Len(t, rec.observations(), 1)

	// And: putting a replacement takes effect on the next batch
	putFilter(t, svc, "g", "errors", "ERROR", countTransformation("App", "Errors2"))
	putEvents(t, svc, "g", "s", at, "ERROR three")
	got := rec.observations()
	require.Len(t, got, 2)
	assert.Equal(t, "Errors2", got[1].Name)
}

func TestMetricFilter_withoutRecorderStoresButPublishesNothing(t *testing.T) {
	// Given: a service that never had InitMetrics — collection disabled
	mock := clock.NewMock()
	svc := New(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, state.NewMemoryStore(), zap.NewNop(), mock)
	t.Cleanup(func() { svc.Stop(context.Background()) })
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")

	// When: a filter is put, described and an event matches it
	putFilter(t, svc, "g", "errors", "ERROR", countTransformation("App", "Errors"))
	putEvents(t, svc, "g", "s", mock.Now(), "ERROR one")

	// Then: the filter is there to describe, and the write succeeded
	assert.Equal(t, []string{"errors"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "g"})))
}

func TestMetricFilter_malformedPersistedRecordIsSkipped(t *testing.T) {
	// Given: one good filter and one corrupt record in the same group
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	putFilter(t, svc, "g", "good", "ERROR", countTransformation("App", "Errors"))
	require.NoError(t, svc.store.Set(context.Background(), nsMetricFilters, "us-east-1/g:bad", "{not json"))

	// When: the group is described, the bad record is fetched by name, and
	// an event is ingested
	resp := describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "g"})
	_, aerr := svc.handler.deleteMetricFilterTyped(context.Background(), &deleteMetricFilterRequest{LogGroupName: "g", FilterName: "bad"})
	putEvents(t, svc, "g", "s", mock.Now(), "ERROR one")

	// Then: the good filter is listed and publishes; the bad one reads as
	// not-found, never as a 500
	assert.Equal(t, []string{"good"}, filterNames(resp))
	assertAWSError(t, aerr, "ResourceNotFoundException")
	assert.Len(t, rec.observations(), 1)

	// And: deleting the group removes the undecodable record too — the
	// cascade deletes by key, so a record no read can decode does not outlive
	// its group
	_, aerr = svc.handler.deleteLogGroupTyped(context.Background(), &deleteLogGroupRequest{LogGroupName: "g"})
	require.Nil(t, aerr)
	left, err := svc.store.Scan(context.Background(), nsMetricFilters, "us-east-1/g:")
	require.NoError(t, err)
	assert.Empty(t, left)
}

func TestMetricFilter_badPersistedFieldReferenceOnTextPatternIsSkipped(t *testing.T) {
	// Given: a persisted record PutMetricFilter would refuse — a text
	// pattern paired with a $ metricValue and a dimension — beside a good one
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	putFilter(t, svc, "g", "good", "ERROR", countTransformation("App", "Errors"))
	bad := `{"name":"bad","log_group_name":"g","filter_pattern":"ERROR","creation_time":1,` +
		`"metric_transformations":[{"metric_name":"Bytes","metric_namespace":"App","metric_value":"$size","dimensions":{"IP":"$ip"}}]}`
	require.NoError(t, svc.store.Set(context.Background(), nsMetricFilters, "us-east-1/g:bad", bad))

	// When: a matching event is ingested
	putEvents(t, svc, "g", "s", mock.Now(), "ERROR one")

	// Then: the write neither panics nor loses the good filter's datapoint
	got := rec.observations()
	require.Len(t, got, 1)
	assert.Equal(t, "Errors", got[0].Name)
}

func TestMetricFilter_keysDoNotCollideAcrossSlashes(t *testing.T) {
	// Given: group "a" with filter "b/c", and group "a/b" with filter "c" —
	// the same string under a "/" join
	svc, _, _ := newMetricFilterService(t)
	mustCreateGroup(t, svc, "a")
	mustCreateGroup(t, svc, "a/b")
	putFilter(t, svc, "a", "b/c", "ERROR", countTransformation("App", "One"))
	putFilter(t, svc, "a/b", "c", "WARN", countTransformation("App", "Two"))

	// When / Then: each group lists exactly its own filter, and deleting
	// one leaves the other
	assert.Equal(t, []string{"b/c"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "a"})))
	assert.Equal(t, []string{"c"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "a/b"})))
	_, aerr := svc.handler.deleteMetricFilterTyped(context.Background(), &deleteMetricFilterRequest{LogGroupName: "a", FilterName: "b/c"})
	require.Nil(t, aerr)
	assert.Empty(t, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "a"})))
	assert.Equal(t, []string{"c"}, filterNames(describeFilters(t, svc, &describeMetricFiltersRequest{LogGroupName: "a/b"})))
}

func TestMetricFilter_putAndDeleteRaceWithIngest(t *testing.T) {
	// Given: a stream with batches flowing through appendEvents on several
	// goroutines — a Lambda being invoked while cdk deploy runs
	svc, rec, mock := newMetricFilterService(t)
	mustCreateGroup(t, svc, "g")
	mustCreateStream(t, svc, "g", "s")
	store := svc.handler.store
	at := mock.Now().Add(-time.Minute).UnixMilli()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = store.appendEvents(context.Background(), "g", "s", []LogEvent{{Timestamp: at, Message: fmt.Sprintf("ERROR w%d-%d", w, i), IngestionTime: at}})
			}
		}()
	}

	// When: the filter is put while batches are in flight, and then a
	// sentinel batch is appended after the put has returned. The sentinels
	// carry timestamps the background batches never use, which is what
	// tells their datapoints apart from a background batch that loaded the
	// filter set before a put or delete and published after it — in flight
	// across the write, and legitimately so.
	for round := range 20 {
		name := fmt.Sprintf("errors-%d", round)
		putFilter(t, svc, "g", name, "ERROR", []metricTransformationWire{{
			MetricName: name, MetricNamespace: "App", MetricValue: "1",
		}})
		afterPut := at + 10_000
		require.Nil(t, store.appendEvents(context.Background(), "g", "s", []LogEvent{{Timestamp: afterPut, Message: "ERROR sentinel", IngestionTime: at}}))

		// Then: the sentinel published through the new filter — a load
		// racing the put never left a stale set cached
		if countNamedAt(rec, name, afterPut) != 1 {
			t.Fatalf("round %d: filter %s did not publish for an event appended after PutMetricFilter returned", round, name)
		}

		// And symmetrically: after the delete returns, a sentinel batch
		// publishes nothing through it
		_, aerr := svc.handler.deleteMetricFilterTyped(context.Background(), &deleteMetricFilterRequest{LogGroupName: "g", FilterName: name})
		require.Nil(t, aerr)
		afterDelete := at + 20_000
		require.Nil(t, store.appendEvents(context.Background(), "g", "s", []LogEvent{{Timestamp: afterDelete, Message: "ERROR sentinel", IngestionTime: at}}))
		if countNamedAt(rec, name, afterDelete) != 0 {
			t.Fatalf("round %d: filter %s published for an event appended after DeleteMetricFilter returned", round, name)
		}
	}
	close(stop)
	wg.Wait()
}

// countNamedAt is how many observations the recorder holds for one metric
// name at one event timestamp — each round of the race test publishes under
// its own name, and each sentinel at its own time.
func countNamedAt(rec *fakeRecorder, name string, timestampMs int64) int {
	n := 0
	for _, o := range rec.observations() {
		if o.Name == name && o.Timestamp.UnixMilli() == timestampMs {
			n++
		}
	}
	return n
}
