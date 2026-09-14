package cloudformation

// provisioner_logs_metric_filter_test.go — AWS::Logs::MetricFilter's handler
// (#1949): the shape translation it owns, the replacement rule for its two
// immutable properties, and the properties-aware delete.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// metricFilterRouter records every Logs request the handler dispatches and
// answers each with 200 and an empty body, the way the service does.
func metricFilterRouter(t *testing.T, targets *[]string, bodies *[]map[string]any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*targets = append(*targets, r.Header.Get("X-Amz-Target"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode dispatched body: %v", err)
		}
		*bodies = append(*bodies, body)
		_, _ = w.Write([]byte("{}"))
	})
}

func TestLogsMetricFilterHandlerCreate_translatesTheTemplateShape(t *testing.T) {
	// Given: a recording Logs router
	var targets []string
	var bodies []map[string]any
	router := metricFilterRouter(t, &targets, &bodies)
	h := &logsMetricFilterHandler{}

	// When: a fully specified filter is created, DefaultValue as the string a
	// String-typed Ref produces
	physID, attrs, err := h.Create(context.Background(), router, nil, map[string]any{
		"LogGroupName":  "/app",
		"FilterName":    "bytes",
		"FilterPattern": "[ip, size]",
		"MetricTransformations": []any{map[string]any{
			"MetricName":      "Volume",
			"MetricNamespace": "MyApp",
			"MetricValue":     "$size",
			"Unit":            "Bytes",
			"DefaultValue":    "0",
		}},
	}, &resolveContext{Region: "us-east-1", AccountID: "000000000000"})

	// Then: one PutMetricFilter carries the API's shape, and Ref is the name
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if physID != "bytes" || attrs != nil {
		t.Fatalf("Create returned physID %q attrs %v, want the filter name and no attributes", physID, attrs)
	}
	if !reflect.DeepEqual(targets, []string{"Logs_20140328.PutMetricFilter"}) {
		t.Fatalf("dispatched targets = %v", targets)
	}
	want := map[string]any{
		"logGroupName":  "/app",
		"filterName":    "bytes",
		"filterPattern": "[ip, size]",
		"metricTransformations": []any{map[string]any{
			"metricName": "Volume", "metricNamespace": "MyApp", "metricValue": "$size", "unit": "Bytes", "defaultValue": float64(0),
		}},
	}
	if !reflect.DeepEqual(bodies[0], want) {
		t.Fatalf("PutMetricFilter body = %v, want %v", bodies[0], want)
	}
}

func TestLogsMetricFilterHandlerCreate_foldsDimensionsAndGeneratesName(t *testing.T) {
	// Given: a recording Logs router and a template with no FilterName
	var targets []string
	var bodies []map[string]any
	router := metricFilterRouter(t, &targets, &bodies)
	h := &logsMetricFilterHandler{}

	// When: the filter is created with two dimensions in CloudFormation's
	// list shape
	physID, _, err := h.Create(context.Background(), router, nil, map[string]any{
		"LogGroupName":  "/app",
		"FilterPattern": "[ip, user, size]",
		"MetricTransformations": []any{map[string]any{
			"MetricName": "Volume", "MetricNamespace": "MyApp", "MetricValue": "$size",
			"Dimensions": []any{
				map[string]any{"Key": "IP", "Value": "$ip"},
				map[string]any{"Key": "User", "Value": "$user"},
			},
		}},
	}, &resolveContext{Region: "us-east-1", AccountID: "000000000000", StackName: "stack", LogicalID: "Filter"})

	// Then: the name is generated, and the dimensions reach the API as a map
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if physID == "" || !strings.HasPrefix(physID, "stack-Filter-") {
		t.Fatalf("generated physical ID = %q, want stack-Filter-<random>", physID)
	}
	if bodies[0]["filterName"] != physID {
		t.Fatalf("filterName = %v, want the generated name %q", bodies[0]["filterName"], physID)
	}
	tr := bodies[0]["metricTransformations"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(tr["dimensions"], map[string]any{"IP": "$ip", "User": "$user"}) {
		t.Fatalf("dimensions = %v", tr["dimensions"])
	}
}

func TestLogsMetricFilterHandlerCreate_filterPatternIsRequired(t *testing.T) {
	// Given: a recording Logs router
	var targets []string
	var bodies []map[string]any
	router := metricFilterRouter(t, &targets, &bodies)
	h := &logsMetricFilterHandler{}
	transformations := []any{map[string]any{"MetricName": "M", "MetricNamespace": "N", "MetricValue": "1"}}

	// When: FilterPattern is omitted, and when it is an explicit ""
	_, _, omittedErr := h.Create(context.Background(), router, nil, map[string]any{
		"LogGroupName": "/app", "FilterName": "f", "MetricTransformations": transformations,
	}, &resolveContext{Region: "us-east-1", AccountID: "000000000000"})
	_, _, emptyErr := h.Create(context.Background(), router, nil, map[string]any{
		"LogGroupName": "/app", "FilterName": "f", "FilterPattern": "", "MetricTransformations": transformations,
	}, &resolveContext{Region: "us-east-1", AccountID: "000000000000"})

	// Then: the omission is refused before anything is dispatched — AWS
	// requires the property — while the explicit empty pattern is forwarded
	if omittedErr == nil || !strings.Contains(omittedErr.Error(), "FilterPattern is required") {
		t.Fatalf("Create without FilterPattern: err = %v, want a FilterPattern-is-required refusal", omittedErr)
	}
	if emptyErr != nil {
		t.Fatalf("Create with an empty FilterPattern: %v", emptyErr)
	}
	if len(targets) != 1 || bodies[0]["filterPattern"] != "" {
		t.Fatalf("dispatched %v with %v, want one PutMetricFilter carrying the empty pattern", targets, bodies)
	}
}

func TestLogsMetricFilterHandlerCreate_duplicateDimensionKeyIsRefused(t *testing.T) {
	// Given: a router that must never be reached
	router := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a template with a duplicate dimension key was dispatched")
	})
	h := &logsMetricFilterHandler{}

	// When: the same dimension key appears twice
	_, _, err := h.Create(context.Background(), router, nil, map[string]any{
		"LogGroupName": "/app", "FilterName": "f", "FilterPattern": "[a, b]",
		"MetricTransformations": []any{map[string]any{
			"MetricName": "M", "MetricNamespace": "N", "MetricValue": "1",
			"Dimensions": []any{map[string]any{"Key": "A", "Value": "$a"}, map[string]any{"Key": "A", "Value": "$b"}},
		}},
	}, &resolveContext{Region: "us-east-1", AccountID: "000000000000"})

	// Then: the handler refuses it rather than picking a winner
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("Create error = %v, want a duplicate-key refusal", err)
	}
}

func TestLogsMetricFilterHandlerUpdate_replacesOnNameOrGroupChange(t *testing.T) {
	// Given: a router that must never be reached
	router := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a replacement-requiring update was dispatched in place")
	})
	h := &logsMetricFilterHandler{}
	transformations := []any{map[string]any{"MetricName": "M", "MetricNamespace": "N", "MetricValue": "1"}}
	old := map[string]any{"LogGroupName": "/app", "FilterName": "f", "FilterPattern": "ERROR", "MetricTransformations": transformations}
	for name, props := range map[string]map[string]any{
		"renamed filter":    {"LogGroupName": "/app", "FilterName": "g", "FilterPattern": "ERROR", "MetricTransformations": transformations},
		"changed log group": {"LogGroupName": "/other", "FilterName": "f", "FilterPattern": "ERROR", "MetricTransformations": transformations},
	} {
		t.Run(name, func(t *testing.T) {
			// When: the immutable property changes
			_, _, err := h.Update(context.Background(), router, nil, "f", props, old, &resolveContext{Region: "us-east-1", AccountID: "000000000000"})

			// Then: the provisioner is told to replace the resource
			if !errors.Is(err, errReplacementRequired) {
				t.Fatalf("Update error = %v, want errReplacementRequired", err)
			}
		})
	}
}

func TestLogsMetricFilterHandlerUpdate_putsInPlaceOtherwise(t *testing.T) {
	// Given: a recording Logs router
	var targets []string
	var bodies []map[string]any
	router := metricFilterRouter(t, &targets, &bodies)
	h := &logsMetricFilterHandler{}
	transformations := []any{map[string]any{"MetricName": "M", "MetricNamespace": "N", "MetricValue": "1"}}

	// When: only the pattern changes
	physID, _, err := h.Update(context.Background(), router, nil, "f",
		map[string]any{"LogGroupName": "/app", "FilterName": "f", "FilterPattern": "WARN", "MetricTransformations": transformations},
		map[string]any{"LogGroupName": "/app", "FilterName": "f", "FilterPattern": "ERROR", "MetricTransformations": transformations},
		&resolveContext{Region: "us-east-1", AccountID: "000000000000"})

	// Then: one PutMetricFilter replaces the definition under the same name
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if physID != "f" || !reflect.DeepEqual(targets, []string{"Logs_20140328.PutMetricFilter"}) || bodies[0]["filterPattern"] != "WARN" {
		t.Fatalf("Update dispatched %v with %v, physical ID %q", targets, bodies, physID)
	}
}

func TestLogsMetricFilterHandlerUpdate_serviceRejectionFailsInPlace(t *testing.T) {
	// Given: a Logs router that refuses the new definition
	router := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"__type":"InvalidParameterException"}`, http.StatusBadRequest)
	})
	h := &logsMetricFilterHandler{}
	transformations := []any{map[string]any{"MetricName": "M", "MetricNamespace": "N", "MetricValue": "1"}}

	// When: the update is dispatched
	_, _, err := h.Update(context.Background(), router, nil, "f",
		map[string]any{"LogGroupName": "/app", "FilterName": "f", "FilterPattern": "WARN", "MetricTransformations": transformations},
		map[string]any{"LogGroupName": "/app", "FilterName": "f", "FilterPattern": "ERROR", "MetricTransformations": transformations},
		&resolveContext{Region: "us-east-1", AccountID: "000000000000"})

	// Then: the failure is terminal for the update, never a replacement
	var failed updateFailure
	if !errors.As(err, &failed) {
		t.Fatalf("Update error = %v, want an updateFailure", err)
	}
}

func TestLogsMetricFilterHandlerDeleteWithProperties_usesTheGroupAndToleratesAbsence(t *testing.T) {
	// Given: a Logs router answering that the filter is already gone
	var bodies []map[string]any
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "Logs_20140328.DeleteMetricFilter" {
			t.Errorf("target = %q, want DeleteMetricFilter", r.Header.Get("X-Amz-Target"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		http.Error(w, `{"__type":"ResourceNotFoundException","message":"The specified log group does not exist."}`, http.StatusBadRequest)
	})
	h := &logsMetricFilterHandler{}

	// When: the resource is deleted with its stored properties
	err := h.DeleteWithProperties(context.Background(), router, nil, "f", map[string]any{"LogGroupName": "/app"}, &resolveContext{Region: "us-east-1", AccountID: "000000000000"})

	// Then: the group came from the properties, and absence is not an error
	if err != nil {
		t.Fatalf("DeleteWithProperties: %v", err)
	}
	if len(bodies) != 1 || bodies[0]["logGroupName"] != "/app" || bodies[0]["filterName"] != "f" {
		t.Fatalf("DeleteMetricFilter body = %v", bodies)
	}
}
