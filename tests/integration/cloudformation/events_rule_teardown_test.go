package cloudformation_test

// events_rule_teardown_test.go — deleting a stack whose AWS::Events::Rule has
// targets.
//
// EventBridge refuses DeleteRule while a rule still has targets ("Before you
// can delete the rule, you must remove all targets, using RemoveTargets" —
// API_DeleteRule), and Force is documented as the managed-rule escape only, so
// it is not a way around that. Real CloudFormation deletes such a rule anyway,
// because its resource provider removes the targets it attached first.
//
// This is the guard for that ordering: the handler's Create attaches targets,
// so its Delete has to take them off again. It passed before EventBridge
// enforced the rule and has to keep passing afterwards.

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const eventsRuleTeardownTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Queue": {
      "Type": "AWS::SQS::Queue",
      "Properties": { "QueueName": "rule-teardown-queue" }
    },
    "Bus": {
      "Type": "AWS::Events::EventBus",
      "Properties": { "Name": "rule-teardown-bus" }
    },
    "Rule": {
      "Type": "AWS::Events::Rule",
      "Properties": {
        "Name": "rule-teardown-rule",
        "EventBusName": {"Ref": "Bus"},
        "EventPattern": {"source": ["com.example.teardown"]},
        "State": "ENABLED",
        "Targets": [{"Id": "queue", "Arn": {"Fn::GetAtt": ["Queue", "Arn"]}}]
      }
    }
  }
}`

func TestDeleteStack_eventsRuleWithTargetsIsRemovedTargetsFirst(t *testing.T) {
	// Given: a stack whose rule carries a target.
	srv := helpers.NewTestServer(t)
	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"events-rule-teardown-stack"},
		"TemplateBody": []string{eventsRuleTeardownTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, "events-rule-teardown-stack", "CREATE_COMPLETE")

	targets := listEventsRuleTargets(t, srv, "rule-teardown-bus", "rule-teardown-rule")
	if len(targets) != 1 {
		t.Fatalf("setup: rule has %d targets, want the one the stack attached", len(targets))
	}

	// When: the stack is deleted.
	del := cfnQuery(t, srv, "DeleteStack", url.Values{
		"StackName": []string{"events-rule-teardown-stack"},
	})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	waitForStackStatus(t, srv, "events-rule-teardown-stack", "DELETE_COMPLETE")

	// Then: the delete completed rather than stalling on EventBridge's
	// targets-attached refusal, and both the targets and the rule are gone.
	if remaining := listEventsRuleTargets(t, srv, "rule-teardown-bus", "rule-teardown-rule"); len(remaining) != 0 {
		t.Fatalf("stack delete left %d target(s) behind: %#v", len(remaining), remaining)
	}
	describe := awsJSONCall(t, srv, "AWSEvents.", "DescribeRule", "application/x-amz-json-1.1", map[string]any{
		"Name":         "rule-teardown-rule",
		"EventBusName": "rule-teardown-bus",
	})
	defer describe.Body.Close()
	if describe.StatusCode != http.StatusBadRequest {
		t.Fatalf("DescribeRule after the stack delete = %d, want 400 for a deleted rule", describe.StatusCode)
	}
}

// listEventsRuleTargets returns the targets currently attached to a rule.
func listEventsRuleTargets(t *testing.T, srv *helpers.TestServer, bus, rule string) []map[string]any {
	t.Helper()
	resp := awsJSONCall(t, srv, "AWSEvents.", "ListTargetsByRule", "application/x-amz-json-1.1", map[string]any{
		"Rule":         rule,
		"EventBusName": bus,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Targets []map[string]any `json:"Targets"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return out.Targets
}
