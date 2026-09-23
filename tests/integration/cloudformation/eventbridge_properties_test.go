package cloudformation_test

// eventbridge_properties_test.go — AWS::Events::Rule and AWS::Events::EventBus
// property threading (#539).
//
// EventBridge came out of the #540 sweep in good shape: PutRule already
// forwarded Description, RoleArn, EventPattern, ScheduleExpression and the
// whole Targets array. Tags was the one property both resource types dropped
// even though TagResource/ListTagsForResource are implemented — the "supported
// but never passed" shape. AWS::Events::EventBus's Description,
// DeadLetterConfig, KmsKeyIdentifier and Policy are the other gap, but the
// EventBridge service itself has no member for the first three on
// CreateEventBus and no PutPermission implementation to apply the fourth, so
// they are reported as unconsumed rather than forwarded to a call that would
// have nowhere to put them.
//
// These read back through ListTagsForResource rather than asserting on the
// request the handler built, the same shape eks_properties_test.go uses: a
// property that reaches the service but is not stored is as invisible to a
// user as one that never left the handler.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// eventsEventBusPropertiesTemplate sets every AWS::Events::EventBus property
// #539 lists, only one of which (Tags) the service can actually accept.
const eventsEventBusPropertiesTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Bus": {
      "Type": "AWS::Events::EventBus",
      "Properties": {
        "Name": "cfn-props-bus",
        "Description": "a custom bus",
        "KmsKeyIdentifier": "alias/my-key",
        "DeadLetterConfig": {"Arn": "arn:aws:sqs:us-east-1:000000000000:dlq"},
        "Policy": "{\"Version\":\"2012-10-17\",\"Statement\":[]}",
        "Tags": [
          {"Key": "env", "Value": "prod"},
          {"Key": "Owner", "Value": "platform"}
        ]
      }
    }
  },
  "Outputs": {
    "BusArn": { "Value": { "Fn::GetAtt": ["Bus", "Arn"] } }
  }
}`

// TestCreateStack_EventsEventBus_tagsForwarded is the failing-first case for
// the EventBus half of #539: Tags never reached CreateEventBus, so
// ListTagsForResource returned nothing for a bus the template tagged.
func TestCreateStack_EventsEventBus_tagsForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "eventbus-properties-stack"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{eventsEventBusPropertiesTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	outputs := describeStackOutputs(t, srv, stackName)
	busARN := outputs["BusArn"]
	if busARN == "" {
		t.Fatalf("BusArn output was empty: %v", outputs)
	}

	tags := eventsListTagsForResource(t, srv, busARN)
	if tags["env"] != "prod" || tags["Owner"] != "platform" {
		t.Errorf("tags = %v, want env=prod and Owner=platform", tags)
	}

	// The four properties the service has nowhere to put are reported rather
	// than dropped in silence — see noteUnconsumedProperties.
	reasons := describeStackResourceReasons(t, srv, stackName)
	for _, name := range []string{"Description", "DeadLetterConfig", "KmsKeyIdentifier", "Policy"} {
		if !strings.Contains(reasons, name) {
			t.Errorf("expected the bus's ResourceStatusReason to name the unapplied %s, got: %s", name, reasons)
		}
	}
}

// eventsRulePropertiesTemplate sets Tags on an AWS::Events::Rule alongside
// the properties the handler already forwarded before #539.
const eventsRulePropertiesTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Rule": {
      "Type": "AWS::Events::Rule",
      "Properties": {
        "Name": "cfn-props-rule",
        "Description": "a rule with tags",
        "EventPattern": {"source": ["com.example.props"]},
        "State": "ENABLED",
        "Tags": [
          {"Key": "env", "Value": "prod"}
        ]
      }
    }
  },
  "Outputs": {
    "RuleArn": { "Value": { "Fn::GetAtt": ["Rule", "Arn"] } }
  }
}`

// TestCreateStack_EventsRule_tagsForwarded is the failing-first case for the
// Rule half of #539: Tags never reached PutRule, so ListTagsForResource
// returned nothing for a rule the template tagged.
func TestCreateStack_EventsRule_tagsForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "eventsrule-properties-stack"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{eventsRulePropertiesTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	outputs := describeStackOutputs(t, srv, stackName)
	ruleARN := outputs["RuleArn"]
	if ruleARN == "" {
		t.Fatalf("RuleArn output was empty: %v", outputs)
	}

	tags := eventsListTagsForResource(t, srv, ruleARN)
	if tags["env"] != "prod" {
		t.Errorf("tags = %v, want env=prod", tags)
	}
}

// eventsEventBusStackTagsTemplate is a minimal bus with one resource-level
// tag, used by both the create-merge and update-reconcile stack-tag cases.
const eventsEventBusStackTagsTemplate = `{
  "Resources": {
    "Bus": {
      "Type": "AWS::Events::EventBus",
      "Properties": {
        "Name": "stack-tags-bus",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  }
}`

// TestUpdateStack_EventsEventBus_stackTagChangeReconciles proves a
// stack-tag-only update — nothing in the bus's own properties changes —
// still reaches eventsEventBusHandler.Update rather than being skipped as a
// no-op change (the #1310 shape) and that the reconciliation removes the old
// value rather than leaving it beside the new one, which PutRule/TagResource's
// merge-only semantics cannot do on their own (see eventsReconcileTags).
func TestUpdateStack_EventsEventBus_stackTagChangeReconciles(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "eventbus-stack-tags-update"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {eventsEventBusStackTagsTemplate},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"dev"},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	busARN := "arn:aws:events:us-east-1:000000000000:event-bus/stack-tags-bus"
	if got := eventsListTagsForResource(t, srv, busARN); got["env"] != "dev" || got["owner"] != "resource" {
		t.Fatalf("bus tags after create = %#v, want env=dev and owner=resource merged", got)
	}

	update := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {eventsEventBusStackTagsTemplate},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer update.Body.Close()
	helpers.AssertStatus(t, update, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	if got := eventsListTagsForResource(t, srv, busARN); got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled bus tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}

// eventsRuleStackTagsTemplate is a minimal rule with one resource-level tag,
// used by both the create-merge and update-reconcile stack-tag cases.
const eventsRuleStackTagsTemplate = `{
  "Resources": {
    "Rule": {
      "Type": "AWS::Events::Rule",
      "Properties": {
        "Name": "stack-tags-rule",
        "EventPattern": {"source": ["com.example.stacktags"]},
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  }
}`

// TestUpdateStack_EventsRule_stackTagChangeReconciles is the Rule half of the
// same #1310-shaped guard: PutRule's Tags parameter only merges ("the tags
// you specify are merged with any existing tags"), so without an explicit
// reconcile step a stack-tag-only change would either never reach the rule
// (skipped as a no-op) or add the new value without ever giving up the old.
func TestUpdateStack_EventsRule_stackTagChangeReconciles(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "eventsrule-stack-tags-update"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {eventsRuleStackTagsTemplate},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"dev"},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	ruleARN := describeStackResourceIDs(t, srv, stackName)["Rule"]
	if ruleARN == "" {
		t.Fatalf("Rule physical ID was empty")
	}
	if got := eventsListTagsForResource(t, srv, ruleARN); got["env"] != "dev" || got["owner"] != "resource" {
		t.Fatalf("rule tags after create = %#v, want env=dev and owner=resource merged", got)
	}

	update := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":           {stackName},
		"TemplateBody":        {eventsRuleStackTagsTemplate},
		"Tags.member.1.Key":   {"env"},
		"Tags.member.1.Value": {"prod"},
	})
	defer update.Body.Close()
	helpers.AssertStatus(t, update, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	if got := eventsListTagsForResource(t, srv, ruleARN); got["env"] != "prod" || got["owner"] != "resource" {
		t.Fatalf("reconciled rule tags = %#v, want env=prod (reconciled) and owner=resource (unchanged)", got)
	}
}

// eventsListTagsForResource reads a resource's tags back through
// AWSEvents.ListTagsForResource, as a {Key: Value} map for easy assertion.
func eventsListTagsForResource(t *testing.T, srv *helpers.TestServer, resourceARN string) map[string]string {
	t.Helper()
	resp := awsJSONCall(t, srv, "AWSEvents.", "ListTagsForResource", "application/x-amz-json-1.1", map[string]any{
		"ResourceARN": resourceARN,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	helpers.DecodeJSON(t, resp, &out)
	tags := make(map[string]string, len(out.Tags))
	for _, tag := range out.Tags {
		tags[tag.Key] = tag.Value
	}
	return tags
}
