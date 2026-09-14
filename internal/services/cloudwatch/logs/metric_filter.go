package logs

// metric_filter.go — CloudWatch Logs metric filters (#1949): the four
// operations (PutMetricFilter, DescribeMetricFilters, DeleteMetricFilter,
// TestMetricFilter), their validation, and the ingest hook that turns a
// matching log event into a CloudWatch metric datapoint through the shared
// service-metrics recorder (internal/metrics). Persistence is in store.go
// with the rest of the package's state access.
//
// AWS docs:
//   - https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutMetricFilter.html
//   - https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_MetricTransformation.html
//   - https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DescribeMetricFilters.html
//   - https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DeleteMetricFilter.html
//   - https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html
//   - https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/MonitoringLogData.html

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/metrics"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ---- Limits and patterns ----------------------------------------------------
//
// Per AWS docs (API_PutMetricFilter.html, API_MetricTransformation.html and
// the CloudWatch Logs quotas page): filterName is 1–512 characters matching
// [^:*]*; filterPattern is 0–1024; metricTransformations is "Fixed number of
// 1 item"; metricName and metricNamespace are at most 255 characters matching
// [^:*$]*; metricValue is at most 100; a filter carries at most three
// dimensions; and "The maximum number of metric filters that can be
// associated with a log group is 100" (LimitExceededException).
const (
	metricFilterNameMaxLen     = 512
	metricFilterPatternMaxLen  = 1024
	metricFilterMetricMaxLen   = 255
	metricFilterValueMaxLen    = 100
	metricFilterDimensionMax   = 3
	metricFilterDimensionMaxLn = 255
	metricFiltersPerGroupMax   = 100

	// TestMetricFilter: "Array Members: Minimum number of 1 item. Maximum
	// number of 50 items." (API_TestMetricFilter.html).
	testMetricFilterMessagesMax = 50
)

var (
	metricFilterNamePattern   = regexp.MustCompile(`^[^:*]*$`)
	metricFilterMetricPattern = regexp.MustCompile(`^[^:*$]*$`)
)

// metricFilterUnits is MetricTransformation's `unit` enumeration
// (API_MetricTransformation.html, "Valid Values"), which is CloudWatch's
// StandardUnit set. "None" is what an omitted unit means.
var metricFilterUnits = map[string]bool{
	"Seconds": true, "Microseconds": true, "Milliseconds": true,
	"Bytes": true, "Kilobytes": true, "Megabytes": true, "Gigabytes": true, "Terabytes": true,
	"Bits": true, "Kilobits": true, "Megabits": true, "Gigabits": true, "Terabits": true,
	"Percent": true, "Count": true,
	"Bytes/Second": true, "Kilobytes/Second": true, "Megabytes/Second": true, "Gigabytes/Second": true, "Terabytes/Second": true,
	"Bits/Second": true, "Kilobits/Second": true, "Megabits/Second": true, "Gigabits/Second": true, "Terabits/Second": true,
	"Count/Second": true, "None": true,
}

const metricFilterDefaultUnit = "None"

// ---- Persisted types --------------------------------------------------------

// MetricFilter is a stored CloudWatch Logs metric filter.
type MetricFilter struct {
	Name                   string                 `json:"name"`
	LogGroupName           string                 `json:"log_group_name"`
	FilterPattern          string                 `json:"filter_pattern"`
	CreationTime           int64                  `json:"creation_time"` // epoch millis
	MetricTransformations  []MetricTransformation `json:"metric_transformations"`
	ApplyOnTransformedLogs bool                   `json:"apply_on_transformed_logs,omitempty"`
}

// MetricTransformation is how a matching event becomes a datapoint.
type MetricTransformation struct {
	MetricName      string            `json:"metric_name"`
	MetricNamespace string            `json:"metric_namespace"`
	MetricValue     string            `json:"metric_value"`
	DefaultValue    *float64          `json:"default_value,omitempty"`
	Dimensions      map[string]string `json:"dimensions,omitempty"`
	Unit            string            `json:"unit,omitempty"`
}

// ---- Wire types -------------------------------------------------------------

// metricTransformationWire is AWS's MetricTransformation shape, used both in
// PutMetricFilter's request and in DescribeMetricFilters' response.
type metricTransformationWire struct {
	MetricName      string            `json:"metricName" cbor:"metricName"`
	MetricNamespace string            `json:"metricNamespace" cbor:"metricNamespace"`
	MetricValue     string            `json:"metricValue" cbor:"metricValue"`
	DefaultValue    *float64          `json:"defaultValue,omitempty" cbor:"defaultValue,omitempty"`
	Dimensions      map[string]string `json:"dimensions,omitempty" cbor:"dimensions,omitempty"`
	Unit            string            `json:"unit,omitempty" cbor:"unit,omitempty"`
}

type putMetricFilterRequest struct {
	LogGroupName string `json:"logGroupName" cbor:"logGroupName"`
	FilterName   string `json:"filterName" cbor:"filterName"`
	// FilterPattern is required but may legitimately be empty (match
	// everything), so presence rather than emptiness is what is checked.
	FilterPattern          *string                    `json:"filterPattern,omitempty" cbor:"filterPattern,omitempty"`
	MetricTransformations  []metricTransformationWire `json:"metricTransformations" cbor:"metricTransformations"`
	ApplyOnTransformedLogs bool                       `json:"applyOnTransformedLogs,omitempty" cbor:"applyOnTransformedLogs,omitempty"`
}

type describeMetricFiltersRequest struct {
	LogGroupName     string `json:"logGroupName,omitempty" cbor:"logGroupName,omitempty"`
	FilterNamePrefix string `json:"filterNamePrefix,omitempty" cbor:"filterNamePrefix,omitempty"`
	MetricName       string `json:"metricName,omitempty" cbor:"metricName,omitempty"`
	MetricNamespace  string `json:"metricNamespace,omitempty" cbor:"metricNamespace,omitempty"`
	Limit            int    `json:"limit,omitempty" cbor:"limit,omitempty"`
	NextToken        string `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

type metricFilterResponse struct {
	FilterName             string                     `json:"filterName" cbor:"filterName"`
	FilterPattern          string                     `json:"filterPattern" cbor:"filterPattern"`
	LogGroupName           string                     `json:"logGroupName" cbor:"logGroupName"`
	CreationTime           int64                      `json:"creationTime" cbor:"creationTime"`
	MetricTransformations  []metricTransformationWire `json:"metricTransformations" cbor:"metricTransformations"`
	ApplyOnTransformedLogs bool                       `json:"applyOnTransformedLogs,omitempty" cbor:"applyOnTransformedLogs,omitempty"`
}

type describeMetricFiltersResponse struct {
	MetricFilters []metricFilterResponse `json:"metricFilters" cbor:"metricFilters"`
	NextToken     string                 `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

type deleteMetricFilterRequest struct {
	LogGroupName string `json:"logGroupName" cbor:"logGroupName"`
	FilterName   string `json:"filterName" cbor:"filterName"`
}

type testMetricFilterRequest struct {
	FilterPattern    *string  `json:"filterPattern,omitempty" cbor:"filterPattern,omitempty"`
	LogEventMessages []string `json:"logEventMessages" cbor:"logEventMessages"`
}

// metricFilterMatchRecord is AWS's MetricFilterMatchRecord. eventNumber is the
// message's zero-based index in logEventMessages, and extractedValues is
// always present — `{}` for a text pattern — both per the reference examples
// (API_TestMetricFilter.html).
type metricFilterMatchRecord struct {
	EventNumber     int64             `json:"eventNumber" cbor:"eventNumber"`
	EventMessage    string            `json:"eventMessage" cbor:"eventMessage"`
	ExtractedValues map[string]string `json:"extractedValues" cbor:"extractedValues"`
}

type testMetricFilterResponse struct {
	Matches []metricFilterMatchRecord `json:"matches" cbor:"matches"`
}

// ---- Errors -----------------------------------------------------------------

func errMetricFilterNotFound() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "The specified metric filter does not exist.",
		HTTPStatus: http.StatusBadRequest,
	}
}

func errMetricFilterLimitExceeded() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "LimitExceededException",
		Message:    fmt.Sprintf("Resource limit exceeded: a log group can have at most %d metric filters.", metricFiltersPerGroupMax),
		HTTPStatus: http.StatusBadRequest,
	}
}

// ---- Validation -------------------------------------------------------------

// validateMetricFilterName checks a filterName against its modeled
// constraints; field names the request member for the message.
func validateMetricFilterName(field, name string) *protocol.AWSError {
	if name == "" {
		return errInvalidParameter(field + " is required")
	}
	if len(name) > metricFilterNameMaxLen {
		return errInvalidParameter(fmt.Sprintf("%s must be at most %d characters", field, metricFilterNameMaxLen))
	}
	if !metricFilterNamePattern.MatchString(name) {
		return errInvalidParameter(field + " must not contain ':' or '*'")
	}
	return nil
}

// compileMetricFilterPattern validates a filterPattern's length and syntax.
func compileMetricFilterPattern(pattern string) (*compiledFilter, *protocol.AWSError) {
	if len(pattern) > metricFilterPatternMaxLen {
		return nil, errInvalidParameter(fmt.Sprintf("filterPattern must be at most %d characters", metricFilterPatternMaxLen))
	}
	f, err := compileFilterPattern(pattern)
	if err != nil {
		return nil, errInvalidParameter("Invalid filter pattern: " + err.Error())
	}
	return f, nil
}

// metricValueRef reports whether a metricValue (or a dimension value) is a
// field reference — `$name` or `$.path` — rather than a literal.
func metricValueRef(v string) bool { return strings.HasPrefix(v, "$") }

// validateMetricTransformation checks one MetricTransformation against the
// constraints AWS documents, plus the three rules that fall out of what the
// transformation can ever produce:
//
//   - metricValue is a number or a field reference. A literal that is not a
//     number could never be published (MonitoringLogData.html: "The numerical
//     value to publish to the metric each time a matching log is found"), so
//     it is refused rather than stored and silently never emitted.
//   - defaultValue and dimensions are exclusive: "If you assign dimensions to
//     a metric created by a metric filter, you can't assign a default value
//     for that metric." (MonitoringLogData.html, Concepts).
//   - dimensions are field references, and only a JSON or space-delimited
//     pattern names fields ("Publishing dimensions with metrics from values
//     in JSON or space-delimited log events", FilterAndPatternSyntax.html),
//     so a dimension on a text pattern is refused — with it accepted, the
//     filter would match and never publish anything.
func validateMetricTransformation(t *metricTransformationWire, pattern *compiledFilter) *protocol.AWSError {
	if t.MetricName == "" {
		return errInvalidParameter("metricTransformations[0].metricName is required")
	}
	if len(t.MetricName) > metricFilterMetricMaxLen || !metricFilterMetricPattern.MatchString(t.MetricName) {
		return errInvalidParameter(fmt.Sprintf("metricName must be at most %d characters and must not contain ':', '*' or '$'", metricFilterMetricMaxLen))
	}
	if t.MetricNamespace == "" {
		return errInvalidParameter("metricTransformations[0].metricNamespace is required")
	}
	if len(t.MetricNamespace) > metricFilterMetricMaxLen || !metricFilterMetricPattern.MatchString(t.MetricNamespace) {
		return errInvalidParameter(fmt.Sprintf("metricNamespace must be at most %d characters and must not contain ':', '*' or '$'", metricFilterMetricMaxLen))
	}
	if t.MetricValue == "" {
		return errInvalidParameter("metricTransformations[0].metricValue is required")
	}
	if len(t.MetricValue) > metricFilterValueMaxLen {
		return errInvalidParameter(fmt.Sprintf("metricValue must be at most %d characters", metricFilterValueMaxLen))
	}
	if !metricValueRef(t.MetricValue) {
		if _, err := strconv.ParseFloat(t.MetricValue, 64); err != nil {
			return errInvalidParameter("metricValue must be a number or a field reference such as $field or $.field")
		}
	} else if !pattern.extractsFields {
		return errInvalidParameter("metricValue can reference a field only with a JSON or space-delimited filter pattern")
	}
	if t.Unit != "" && !metricFilterUnits[t.Unit] {
		return errInvalidParameter("unit is not a valid CloudWatch unit: " + t.Unit)
	}
	if len(t.Dimensions) > metricFilterDimensionMax {
		return errInvalidParameter(fmt.Sprintf("a metric filter can have at most %d dimensions", metricFilterDimensionMax))
	}
	if len(t.Dimensions) > 0 {
		if t.DefaultValue != nil {
			return errInvalidParameter("defaultValue cannot be set on a metric filter that has dimensions")
		}
		if !pattern.extractsFields {
			return errInvalidParameter("dimensions require a JSON or space-delimited filter pattern")
		}
		for key, value := range t.Dimensions {
			if key == "" || len(key) > metricFilterDimensionMaxLn {
				return errInvalidParameter(fmt.Sprintf("dimension names must be 1 to %d characters", metricFilterDimensionMaxLn))
			}
			if !metricValueRef(value) || len(value) > metricFilterDimensionMaxLn {
				return errInvalidParameter(fmt.Sprintf("dimension %q must reference a log event field such as $field or $.field", key))
			}
		}
	}
	return nil
}

// ---- Typed handlers ---------------------------------------------------------

// putMetricFilterTyped creates or replaces a metric filter. Request-shape
// validation runs before the log group is resolved, as this package's other
// validators do; the per-group limit is checked last, and only for a name
// the group does not already carry — a replace never counts against it.
func (h *Handler) putMetricFilterTyped(ctx context.Context, req *putMetricFilterRequest) (*struct{}, *protocol.AWSError) {
	if req.LogGroupName == "" {
		return nil, errInvalidParameter("logGroupName is required")
	}
	if aerr := validateMetricFilterName("filterName", req.FilterName); aerr != nil {
		return nil, aerr
	}
	if req.FilterPattern == nil {
		return nil, errInvalidParameter("filterPattern is required")
	}
	pattern, aerr := compileMetricFilterPattern(*req.FilterPattern)
	if aerr != nil {
		return nil, aerr
	}
	if len(req.MetricTransformations) != 1 {
		return nil, errInvalidParameter("metricTransformations must contain exactly 1 item")
	}
	if aerr := validateMetricTransformation(&req.MetricTransformations[0], pattern); aerr != nil {
		return nil, aerr
	}
	if _, aerr := h.store.getLogGroup(ctx, req.LogGroupName); aerr != nil {
		return nil, aerr
	}

	existing, aerr := h.store.listMetricFilters(ctx, req.LogGroupName)
	if aerr != nil {
		return nil, aerr
	}
	creationTime := h.clk.Now().UnixMilli()
	replacing := false
	for _, f := range existing {
		if f.Name == req.FilterName {
			replacing = true
			creationTime = f.CreationTime
			break
		}
	}
	if !replacing && len(existing) >= metricFiltersPerGroupMax {
		return nil, errMetricFilterLimitExceeded()
	}

	mf := &MetricFilter{
		Name:                   req.FilterName,
		LogGroupName:           req.LogGroupName,
		FilterPattern:          *req.FilterPattern,
		CreationTime:           creationTime,
		MetricTransformations:  []MetricTransformation{metricTransformationFromWire(req.MetricTransformations[0])},
		ApplyOnTransformedLogs: req.ApplyOnTransformedLogs,
	}
	if aerr := h.store.putMetricFilter(ctx, mf); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

// describeMetricFiltersTyped lists metric filters, ASCII-sorted by filter
// name and paged with the package's index-cursor tokens (see
// describeLogGroupsTyped). Per AWS docs (API_DescribeMetricFilters.html):
// filterNamePrefix is honoured "only if you also include the logGroupName
// parameter", and metricName and metricNamespace each require the other.
func (h *Handler) describeMetricFiltersTyped(ctx context.Context, req *describeMetricFiltersRequest) (*describeMetricFiltersResponse, *protocol.AWSError) {
	if (req.MetricName == "") != (req.MetricNamespace == "") {
		return nil, errInvalidParameter("metricName and metricNamespace must be specified together")
	}
	if req.FilterNamePrefix != "" {
		if aerr := validateMetricFilterName("filterNamePrefix", req.FilterNamePrefix); aerr != nil {
			return nil, aerr
		}
	}
	if req.LogGroupName != "" {
		if _, aerr := h.store.getLogGroup(ctx, req.LogGroupName); aerr != nil {
			return nil, aerr
		}
	}
	all, aerr := h.store.listMetricFilters(ctx, req.LogGroupName)
	if aerr != nil {
		return nil, aerr
	}
	filters := make([]*MetricFilter, 0, len(all))
	for _, f := range all {
		if req.LogGroupName != "" && req.FilterNamePrefix != "" && !strings.HasPrefix(f.Name, req.FilterNamePrefix) {
			continue
		}
		if req.MetricName != "" && !f.publishes(req.MetricNamespace, req.MetricName) {
			continue
		}
		filters = append(filters, f)
	}
	page, err := serviceutil.Paginate(filters, req.Limit, req.NextToken, serviceutil.PaginateOptions{
		DefaultLimit: describeDefaultLimit,
		MaxLimit:     describeMaxLimit,
	})
	if err != nil {
		return nil, errInvalidNextToken()
	}
	out := make([]metricFilterResponse, 0, len(page.Items))
	for _, f := range page.Items {
		out = append(out, metricFilterToWire(f))
	}
	return &describeMetricFiltersResponse{MetricFilters: out, NextToken: page.NextToken}, nil
}

func (h *Handler) deleteMetricFilterTyped(ctx context.Context, req *deleteMetricFilterRequest) (*struct{}, *protocol.AWSError) {
	if req.LogGroupName == "" {
		return nil, errInvalidParameter("logGroupName is required")
	}
	if aerr := validateMetricFilterName("filterName", req.FilterName); aerr != nil {
		return nil, aerr
	}
	if _, aerr := h.store.getLogGroup(ctx, req.LogGroupName); aerr != nil {
		return nil, aerr
	}
	if _, aerr := h.store.getMetricFilter(ctx, req.LogGroupName, req.FilterName); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteMetricFilter(ctx, req.LogGroupName, req.FilterName); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

// testMetricFilterTyped runs a pattern over sample messages. It touches no
// state, so it needs no log group.
func (h *Handler) testMetricFilterTyped(_ context.Context, req *testMetricFilterRequest) (*testMetricFilterResponse, *protocol.AWSError) {
	if req.FilterPattern == nil {
		return nil, errInvalidParameter("filterPattern is required")
	}
	if len(req.LogEventMessages) == 0 || len(req.LogEventMessages) > testMetricFilterMessagesMax {
		return nil, errInvalidParameter(fmt.Sprintf("logEventMessages must contain 1 to %d items", testMetricFilterMessagesMax))
	}
	for _, msg := range req.LogEventMessages {
		if msg == "" {
			return nil, errInvalidParameter("logEventMessages must not contain an empty message")
		}
	}
	pattern, aerr := compileMetricFilterPattern(*req.FilterPattern)
	if aerr != nil {
		return nil, aerr
	}
	matches := make([]metricFilterMatchRecord, 0, len(req.LogEventMessages))
	for i, msg := range req.LogEventMessages {
		fields, ok := pattern.eval(msg)
		if !ok {
			continue
		}
		extracted := map[string]string{}
		if fields != nil {
			extracted = fields.named()
		}
		matches = append(matches, metricFilterMatchRecord{EventNumber: int64(i), EventMessage: msg, ExtractedValues: extracted})
	}
	return &testMetricFilterResponse{Matches: matches}, nil
}

// ---- Wire ↔ persisted --------------------------------------------------------

func metricTransformationFromWire(w metricTransformationWire) MetricTransformation {
	t := MetricTransformation{
		MetricName:      w.MetricName,
		MetricNamespace: w.MetricNamespace,
		MetricValue:     w.MetricValue,
		Unit:            w.Unit,
	}
	if w.DefaultValue != nil {
		v := *w.DefaultValue
		t.DefaultValue = &v
	}
	if len(w.Dimensions) > 0 {
		t.Dimensions = make(map[string]string, len(w.Dimensions))
		for k, v := range w.Dimensions {
			t.Dimensions[k] = v
		}
	}
	return t
}

func metricFilterToWire(f *MetricFilter) metricFilterResponse {
	out := metricFilterResponse{
		FilterName:             f.Name,
		FilterPattern:          f.FilterPattern,
		LogGroupName:           f.LogGroupName,
		CreationTime:           f.CreationTime,
		MetricTransformations:  make([]metricTransformationWire, 0, len(f.MetricTransformations)),
		ApplyOnTransformedLogs: f.ApplyOnTransformedLogs,
	}
	for _, t := range f.MetricTransformations {
		// The two structs differ only in their tags; the conversion is the
		// persisted → wire rename.
		out.MetricTransformations = append(out.MetricTransformations, metricTransformationWire(t))
	}
	return out
}

// publishes reports whether any of the filter's transformations targets the
// (namespace, name) pair — DescribeMetricFilters' metricName/metricNamespace
// filter.
func (f *MetricFilter) publishes(namespace, name string) bool {
	for _, t := range f.MetricTransformations {
		if t.MetricNamespace == namespace && t.MetricName == name {
			return true
		}
	}
	return false
}

// ---- Ingest: log events → metric datapoints ---------------------------------
//
// Every accepted event — from PutLogEvents and from the Lambda log writer
// alike, both of which go through logsStore.appendEvents — is run through the
// compiled filters of its log group. The compiled forms are cached per
// (region, group) and invalidated by every write that changes the group's
// filter set, so the hot path never recompiles a pattern or reads the store.
//
// With service metrics disabled (OVERCAST_SERVICE_METRICS=disabled) the
// recorder is nil: filters are still stored, described and tested, and
// nothing is published — documented in docs/services/cloudwatch-logs.md.

// compiledMetricFilter is one metric filter as the ingest hook evaluates it.
type compiledMetricFilter struct {
	name    string
	pattern *compiledFilter

	namespace  string
	metricName string
	unit       string
	// valueRef names the field the value comes from; literal is the value
	// when valueRef is empty.
	valueRef string
	literal  float64
	// dimensions maps a dimension name to the field reference its value
	// comes from.
	dimensions   map[string]string
	defaultValue *float64
	// periods tracks the one-minute periods this filter has already
	// answered for, for defaultValue — see publishMetricFilterObservations.
	periods defaultValuePeriods
}

func compileMetricFilter(f *MetricFilter) (*compiledMetricFilter, error) {
	if len(f.MetricTransformations) != 1 {
		return nil, fmt.Errorf("metric filter %q has %d transformations, want 1", f.Name, len(f.MetricTransformations))
	}
	pattern, err := compileFilterPattern(f.FilterPattern)
	if err != nil {
		return nil, fmt.Errorf("metric filter %q: %w", f.Name, err)
	}
	t := f.MetricTransformations[0]
	c := &compiledMetricFilter{
		name:         f.Name,
		pattern:      pattern,
		namespace:    t.MetricNamespace,
		metricName:   t.MetricName,
		unit:         t.Unit,
		dimensions:   t.Dimensions,
		defaultValue: t.DefaultValue,
	}
	if c.unit == "" {
		c.unit = metricFilterDefaultUnit
	}
	if metricValueRef(t.MetricValue) {
		c.valueRef = t.MetricValue
	} else if c.literal, err = strconv.ParseFloat(t.MetricValue, 64); err != nil {
		return nil, fmt.Errorf("metric filter %q: metricValue %q: %w", f.Name, t.MetricValue, err)
	}
	// PutMetricFilter refuses these pairings (validateMetricTransformation);
	// a persisted record carrying one anyway is malformed, and is skipped
	// here rather than left to dereference the nil field set a text pattern
	// yields on the ingest path.
	if !pattern.extractsFields && (c.valueRef != "" || len(c.dimensions) > 0) {
		return nil, fmt.Errorf("metric filter %q: a text pattern names no fields for its metricValue or dimensions", f.Name)
	}
	return c, nil
}

// observation builds the datapoint one event contributes, or reports false
// when the event contributes nothing:
//
//   - The pattern does not match. The caller then applies defaultValue.
//   - metricValue names a field the event lacks, or one that is not a number.
//     "The numerical value to publish" (MonitoringLogData.html) cannot be
//     produced from it, so the event is skipped — never published as 0.
//
// A dimension whose field the event lacks is left off the datapoint rather
// than dropping it: per the CloudFormation reference for
// AWS::Logs::MetricFilter Dimension, "This dimension will only be published
// for a metric if the value is found in the log event"
// (aws-properties-logs-metricfilter-dimension.html) — it is the dimension
// that is withheld, not the datapoint.
func (c *compiledMetricFilter) observation(e LogEvent) (metrics.Observation, bool) {
	fields, ok := c.pattern.eval(e.Message)
	if !ok {
		return metrics.Observation{}, false
	}
	value := c.literal
	if c.valueRef != "" {
		if fields == nil {
			return metrics.Observation{}, false
		}
		raw, found := fields.lookup(c.valueRef)
		if !found {
			return metrics.Observation{}, false
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return metrics.Observation{}, false
		}
		value = parsed
	}
	var dims []metrics.Dimension
	if len(c.dimensions) > 0 && fields != nil {
		dims = make([]metrics.Dimension, 0, len(c.dimensions))
		for name, ref := range c.dimensions {
			if v, found := fields.lookup(ref); found {
				dims = append(dims, metrics.Dimension{Name: name, Value: v})
			}
		}
	}
	return metrics.Observation{
		Namespace:  c.namespace,
		Name:       c.metricName,
		Dimensions: dims,
		Timestamp:  time.UnixMilli(e.Timestamp).UTC(),
		Unit:       c.unit,
		Value:      value,
	}, true
}

// defaultValuePeriods remembers, per one-minute period, whether a filter has
// already published for it — a match, or the default. It is what gives
// defaultValue its documented per-period meaning (MonitoringLogData.html,
// Concepts): "The value reported to the metric filter during a period when
// logs are ingested but no matching logs are found. ... If no logs are
// ingested during a one-minute period, then no value is reported." The
// default is therefore emitted at most once per period, only for a period
// some ingested event fell in — never for a silent minute — and never for a
// period a matching event has been seen in.
//
// The decision is made per batch, not per event: publishMetricFilterObservations
// evaluates the whole batch first, and a period any event of the batch
// matched gets no default from that batch, whatever order its events came
// in. Lambda writes `START …` before the application line, so deciding per
// event would pair every real datapoint with a spurious default in the same
// minute — an Average of 400 where AWS reports 800 for one invocation
// logging START, {"latency":800}, END. Across batches the tracker below
// applies: a period that already carried a match never gets the default, and
// one that already carried the default keeps it (AWS does not say what it
// does when a match arrives in a later batch of a period the default was
// already reported for; a defaultValue of 0 — the documented use — leaves
// Sum unchanged by it).
type defaultValuePeriods struct {
	mu   sync.Mutex
	seen map[int64]periodState // key: period start, epoch millis
}

type periodState struct {
	matched   bool
	defaulted bool
}

// defaultValuePeriodsMax bounds the tracker per filter. Periods are evicted
// oldest-first once it is exceeded, so a filter over a long-lived group holds
// at most this many minutes of bookkeeping.
const defaultValuePeriodsMax = 256

func periodStart(timestampMs int64) int64 {
	const minuteMs = int64(time.Minute / time.Millisecond)
	return timestampMs - (timestampMs%minuteMs+minuteMs)%minuteMs
}

// noteMatch records that a matching event landed in the period.
func (p *defaultValuePeriods) noteMatch(start int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.set(start, func(s *periodState) { s.matched = true })
}

// noteMiss records that a period ingested only non-matching events in a
// batch and reports whether the default should be published for it — true
// exactly once per period, and never once a match has been seen in it.
func (p *defaultValuePeriods) noteMiss(start int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.seen[start]; s.matched || s.defaulted {
		return false
	}
	p.set(start, func(s *periodState) { s.defaulted = true })
	return true
}

func (p *defaultValuePeriods) set(start int64, mutate func(*periodState)) {
	if p.seen == nil {
		p.seen = make(map[int64]periodState)
	}
	s := p.seen[start]
	mutate(&s)
	p.seen[start] = s
	if len(p.seen) > defaultValuePeriodsMax {
		oldest := start
		for k := range p.seen {
			if k < oldest {
				oldest = k
			}
		}
		delete(p.seen, oldest)
	}
}

// metricFilterCacheKey scopes the compiled-filter cache the way every other
// logs key is scoped: by region, then group.
func metricFilterCacheKey(region, groupName string) string {
	return serviceutil.RegionKey(region, groupName)
}

// compiledMetricFilters returns the compiled filters of a log group, loading
// and compiling them from the store on first use and caching the result —
// an empty slice included, so a group with no filters costs one scan, not one
// per batch. A store error is not cached, so the next batch retries it.
//
// The hit path is a lock-free sync.Map read. A miss loads under
// metricFilterMu, the same lock every writer holds across its store write
// and cache invalidation, so the scan below can never observe a set that a
// write then invalidates before the store here: either the write completed
// first and the scan sees it, or the write waits and its invalidation
// removes what this load stored.
func (s *logsStore) compiledMetricFilters(ctx context.Context, groupName string) []*compiledMetricFilter {
	key := metricFilterCacheKey(s.region(ctx), groupName)
	if v, ok := s.metricFilterCache.Load(key); ok {
		return v.([]*compiledMetricFilter)
	}
	s.metricFilterMu.Lock()
	defer s.metricFilterMu.Unlock()
	if v, ok := s.metricFilterCache.Load(key); ok {
		return v.([]*compiledMetricFilter) // another batch loaded it meanwhile
	}
	stored, aerr := s.listMetricFilters(ctx, groupName)
	if aerr != nil {
		s.logMetricFilterProblem("logs: metric filters could not be loaded", groupName, aerr)
		return nil
	}
	compiled := make([]*compiledMetricFilter, 0, len(stored))
	for _, f := range stored {
		c, err := compileMetricFilter(f)
		if err != nil {
			// PutMetricFilter validated it, so this is a persisted record
			// that has since gone bad: skip it rather than lose the group's
			// other filters (the malformed-persisted-state rule).
			s.logMetricFilterProblem("logs: skipping malformed metric filter", groupName, err)
			continue
		}
		compiled = append(compiled, c)
	}
	s.metricFilterCache.Store(key, compiled)
	return compiled
}

// invalidateMetricFilters drops a group's compiled filters so the next batch
// reloads them. Called, under metricFilterMu, by every write that changes
// the group's filter set.
func (s *logsStore) invalidateMetricFilters(region, groupName string) {
	s.metricFilterCache.Delete(metricFilterCacheKey(region, groupName))
}

// publishMetricFilterObservations runs a group's metric filters over a batch
// of accepted events and records one observation per match — and, for a
// filter with a defaultValue, the default for each one-minute period the
// batch ingested into without matching, at most once per period (see
// defaultValuePeriods). Called from appendEvents after the write buffer's
// lock is released; a recorder failure is logged and never fails the
// caller's write.
func (s *logsStore) publishMetricFilterObservations(ctx context.Context, groupName string, events []LogEvent) {
	if s.metrics == nil {
		return
	}
	filters := s.compiledMetricFilters(ctx, groupName)
	if len(filters) == 0 {
		return
	}
	for _, f := range filters {
		// The whole batch is evaluated before any default is decided, so a
		// period any event of it matched gets no default from it.
		matchedPeriods := map[int64]bool{}
		var missedPeriods []int64
		for _, e := range events {
			obs, ok := f.observation(e)
			period := periodStart(e.Timestamp)
			if ok {
				matchedPeriods[period] = true
				s.observeMetricFilter(ctx, f, obs)
				continue
			}
			if f.defaultValue != nil {
				missedPeriods = append(missedPeriods, period)
			}
		}
		if f.defaultValue == nil {
			continue
		}
		for period := range matchedPeriods {
			f.periods.noteMatch(period)
		}
		for _, period := range missedPeriods {
			if matchedPeriods[period] || !f.periods.noteMiss(period) {
				continue
			}
			s.observeMetricFilter(ctx, f, metrics.Observation{
				Namespace: f.namespace,
				Name:      f.metricName,
				Timestamp: time.UnixMilli(period).UTC(),
				Unit:      f.unit,
				Value:     *f.defaultValue,
			})
		}
	}
}

func (s *logsStore) observeMetricFilter(ctx context.Context, f *compiledMetricFilter, obs metrics.Observation) {
	if err := s.metrics.Observe(ctx, obs); err != nil {
		s.logMetricFilterProblem("logs: metric filter observe failed", f.name, err)
	}
}

// logMetricFilterProblem reports an ingest-side metric filter problem at
// debug level: a metrics-subsystem hiccup must not turn a successful write
// into noise, let alone a failure (the same stance SQS's observeSQSMetric
// takes). The store may have no logger in a unit test.
func (s *logsStore) logMetricFilterProblem(msg, subject string, err error) {
	if s.log == nil {
		return
	}
	s.log.Debug(msg, zap.String("subject", subject), zap.Error(err))
}

// sortMetricFilters orders filters as DescribeMetricFilters reports them —
// "ASCII-sorted by filter name", per the API reference.
func sortMetricFilters(filters []*MetricFilter) {
	sort.Slice(filters, func(i, j int) bool {
		if filters[i].Name != filters[j].Name {
			return filters[i].Name < filters[j].Name
		}
		return filters[i].LogGroupName < filters[j].LogGroupName
	})
}
