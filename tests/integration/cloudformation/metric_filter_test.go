package cloudformation_test

// metric_filter_test.go — AWS::Logs::MetricFilter (#1949): create, in-place
// update, replacement on a renamed filter, and delete, each verified through
// the Logs API the handler dispatches to.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

type describedMetricFilter struct {
	FilterName            string `json:"filterName"`
	FilterPattern         string `json:"filterPattern"`
	LogGroupName          string `json:"logGroupName"`
	MetricTransformations []struct {
		MetricName      string            `json:"metricName"`
		MetricNamespace string            `json:"metricNamespace"`
		MetricValue     string            `json:"metricValue"`
		DefaultValue    *float64          `json:"defaultValue"`
		Dimensions      map[string]string `json:"dimensions"`
		Unit            string            `json:"unit"`
	} `json:"metricTransformations"`
}

func describeMetricFilters(t *testing.T, srv *helpers.TestServer, logGroupName string) []describedMetricFilter {
	t.Helper()
	body, err := json.Marshal(map[string]any{"logGroupName": logGroupName})
	if err != nil {
		t.Fatalf("marshal DescribeMetricFilters: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build DescribeMetricFilters request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Logs_20140328.DescribeMetricFilters")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DescribeMetricFilters: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		MetricFilters []describedMetricFilter `json:"metricFilters"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return out.MetricFilters
}

const metricFilterStackTemplate = `{
  "Resources": {
    "LogGroup": {
      "Type": "AWS::Logs::LogGroup",
      "Properties": {"LogGroupName": "/cloudformation/metric-filter"}
    },
    "Filter": {
      "Type": "AWS::Logs::MetricFilter",
      "Properties": {
        "LogGroupName": {"Ref": "LogGroup"},
        "FilterName": "%s",
        "FilterPattern": "%s",
        "MetricTransformations": [{
          "MetricName": "Volume",
          "MetricNamespace": "MyApp",
          "MetricValue": "$size",
          "Unit": "Bytes",
          "Dimensions": [{"Key": "IP", "Value": "$ip"}]
        }]
      }
    }
  },
  "Outputs": {
    "FilterRef": {"Value": {"Ref": "Filter"}}
  }
}`

func TestCreateStack_MetricFilter(t *testing.T) {
	// Given: a stack with a log group and a dimensioned metric filter on it
	srv := helpers.NewTestServer(t)
	const stackName = "metric-filter-stack"
	template := strings.Replace(strings.Replace(metricFilterStackTemplate, "%s", "bytes", 1), "%s", "[ip, id, user, timestamp, request, status_code, size]", 1)

	// When: the stack is created
	createResp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {template}})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	// Then: the filter exists on the group with every property translated,
	// and Ref is the filter name
	filters := describeMetricFilters(t, srv, "/cloudformation/metric-filter")
	if len(filters) != 1 {
		t.Fatalf("metricFilters = %+v, want one", filters)
	}
	got := filters[0]
	if got.FilterName != "bytes" || got.FilterPattern != "[ip, id, user, timestamp, request, status_code, size]" {
		t.Fatalf("metricFilters[0] = %+v", got)
	}
	tr := got.MetricTransformations[0]
	if tr.MetricName != "Volume" || tr.MetricNamespace != "MyApp" || tr.MetricValue != "$size" || tr.Unit != "Bytes" || tr.Dimensions["IP"] != "$ip" {
		t.Fatalf("metricTransformations[0] = %+v", tr)
	}
	if physID := stackResourcePhysicalID(t, srv, stackName, "Filter"); physID != "bytes" {
		t.Fatalf("Filter physical ID = %q, want the filter name", physID)
	}
	outputs := cfnQuery(t, srv, "DescribeStacks", url.Values{"StackName": {stackName}})
	defer outputs.Body.Close()
	if body := string(readBody(t, outputs)); !strings.Contains(body, "<OutputValue>bytes</OutputValue>") {
		t.Fatalf("Ref output missing from DescribeStacks: %s", body)
	}

	// When: the pattern changes, the filter is updated in place
	updated := strings.Replace(strings.Replace(metricFilterStackTemplate, "%s", "bytes", 1), "%s", "[ip, id, user, timestamp, request, status_code = 200, size]", 1)
	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{"StackName": {stackName}, "TemplateBody": {updated}})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	// Then: the same filter carries the new pattern
	filters = describeMetricFilters(t, srv, "/cloudformation/metric-filter")
	if len(filters) != 1 || filters[0].FilterName != "bytes" || filters[0].FilterPattern != "[ip, id, user, timestamp, request, status_code = 200, size]" {
		t.Fatalf("metricFilters after update = %+v", filters)
	}

	// When: the filter is renamed, it is replaced — and the old one removed
	renamed := strings.Replace(strings.Replace(metricFilterStackTemplate, "%s", "bytes-v2", 1), "%s", "[ip, id, user, timestamp, request, status_code = 200, size]", 1)
	renameResp := cfnQuery(t, srv, "UpdateStack", url.Values{"StackName": {stackName}, "TemplateBody": {renamed}})
	defer renameResp.Body.Close()
	helpers.AssertStatus(t, renameResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	filters = describeMetricFilters(t, srv, "/cloudformation/metric-filter")
	if len(filters) != 1 || filters[0].FilterName != "bytes-v2" {
		t.Fatalf("metricFilters after rename = %+v", filters)
	}
	if physID := stackResourcePhysicalID(t, srv, stackName, "Filter"); physID != "bytes-v2" {
		t.Fatalf("Filter physical ID after rename = %q", physID)
	}

	// When: the stack is deleted
	deleteResp := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": {stackName}})
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "DELETE_COMPLETE")

	// Then: the log group, and with it the filter, is gone
	if groups := describeLogGroups(t, srv, "/cloudformation/metric-filter"); len(groups) != 0 {
		t.Fatalf("log group survived stack deletion: %+v", groups)
	}
}

func TestCreateStack_MetricFilterInvalidTransformationRollsBack(t *testing.T) {
	// Given: a template whose transformation the Logs service refuses —
	// dimensions and a default value together
	srv := helpers.NewTestServer(t)
	const stackName = "metric-filter-invalid-stack"
	const template = `{
  "Resources": {
    "LogGroup": {"Type": "AWS::Logs::LogGroup", "Properties": {"LogGroupName": "/cloudformation/metric-filter-invalid"}},
    "Filter": {
      "Type": "AWS::Logs::MetricFilter",
      "Properties": {
        "LogGroupName": {"Ref": "LogGroup"},
        "FilterPattern": "[a, b]",
        "MetricTransformations": [{
          "MetricName": "Count", "MetricNamespace": "App", "MetricValue": "1",
          "DefaultValue": "0",
          "Dimensions": [{"Key": "A", "Value": "$a"}]
        }]
      }
    }
  }
}`

	// When: the stack is created
	createResp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {template}})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)

	// Then: the service's own validation fails the resource and the stack
	// rolls back, rather than a filter that could never publish landing
	waitForStackStatus(t, srv, stackName, "ROLLBACK_COMPLETE")
}
