package cloudformation_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// get_stack_output_test.go — Fn::GetStackOutput end to end: a consumer in one
// region reading a producer's unexported output in another, the weak
// coupling that lets the producer go while the consumer stands, and the
// failures a dangling reference reports, attributed to the resource that
// made it.

// cfnQueryInRegion is cfnQuery with the request's SigV4 credential scope
// naming a region, which is where the router reads the region from.
func cfnQueryInRegion(t *testing.T, srv *helpers.TestServer, region, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2010-05-15")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(params.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=test/20250101/"+region+"/cloudformation/aws4_request, SignedHeaders=host, Signature=fake")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfnQuery %s in %s: %v", action, region, err)
	}
	return resp
}

func waitForStackStatusInRegion(t *testing.T, srv *helpers.TestServer, region, stackName, wantStatus string) {
	t.Helper()
	helpers.Eventually(t, 30*time.Second, 20*time.Millisecond, func() bool {
		resp := cfnQueryInRegion(t, srv, region, "DescribeStacks", url.Values{"StackName": []string{stackName}})
		defer resp.Body.Close()
		return strings.Contains(string(readBody(t, resp)), wantStatus)
	}, "timed out waiting for "+stackName+" in "+region+" to reach "+wantStatus)
}

func createStackInRegion(t *testing.T, srv *helpers.TestServer, region, name, template string, params url.Values) {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("StackName", name)
	params.Set("TemplateBody", template)
	resp := cfnQueryInRegion(t, srv, region, "CreateStack", params)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// The producer exports nothing: Fn::GetStackOutput reads plain outputs.
const weakProducerTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Topic": { "Type": "AWS::SNS::Topic", "Properties": { "TopicName": "weak-producer-topic" } }
  },
  "Outputs": {
    "TopicName": { "Value": { "Fn::GetAtt": ["Topic", "TopicName"] } }
  }
}`

// The consumer names the producer's region explicitly, and builds the
// producer's name from a parameter — the documented Ref idiom.
const weakConsumerTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Parameters": {
    "ProducerName": { "Type": "String", "Default": "weak-producer" },
    "ProducerRegion": { "Type": "String", "Default": "us-west-2" }
  },
  "Resources": {
    "Queue": {
      "Type": "AWS::SQS::Queue",
      "Properties": {
        "QueueName": {
          "Fn::Join": ["-", ["from", {
            "Fn::GetStackOutput": {
              "StackName": { "Ref": "ProducerName" },
              "OutputName": "TopicName",
              "Region": { "Ref": "ProducerRegion" }
            }
          }]]
        }
      }
    }
  },
  "Outputs": {
    "QueueName": { "Value": { "Fn::GetAtt": ["Queue", "QueueName"] } }
  }
}`

func TestGetStackOutput_readsAnOutputFromAnotherRegion(t *testing.T) {
	// Given: a producer in us-west-2 whose output is not exported
	srv := helpers.NewTestServer(t)
	createStackInRegion(t, srv, "us-west-2", "weak-producer", weakProducerTemplate, nil)
	waitForStackStatusInRegion(t, srv, "us-west-2", "weak-producer", "CREATE_COMPLETE")

	// When: a consumer in us-east-1 reads it with Fn::GetStackOutput
	createStackInRegion(t, srv, "us-east-1", "weak-consumer", weakConsumerTemplate, nil)
	waitForStackStatusInRegion(t, srv, "us-east-1", "weak-consumer", "CREATE_COMPLETE")

	// Then: the producer's value reached the consumer's resource
	resp := cfnQueryInRegion(t, srv, "us-east-1", "DescribeStacks", url.Values{"StackName": []string{"weak-consumer"}})
	defer resp.Body.Close()
	body := string(readBody(t, resp))
	if !strings.Contains(body, "<OutputValue>from-weak-producer-topic</OutputValue>") {
		t.Fatalf("consumer outputs should carry the producer's topic name, got: %s", body)
	}

	// And: nothing was exported or imported — the coupling is weak
	exports := cfnQueryInRegion(t, srv, "us-west-2", "ListExports", nil)
	defer exports.Body.Close()
	if b := string(readBody(t, exports)); strings.Contains(b, "weak-producer-topic") {
		t.Fatalf("a weak reference must not create an export, got: %s", b)
	}

	// And: the producer can be deleted while the consumer stands
	del := cfnQueryInRegion(t, srv, "us-west-2", "DeleteStack", url.Values{"StackName": []string{"weak-producer"}})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	helpers.Eventually(t, 30*time.Second, 20*time.Millisecond, func() bool {
		r := cfnQueryInRegion(t, srv, "us-west-2", "DescribeStacks", url.Values{"StackName": []string{"weak-producer"}})
		defer r.Body.Close()
		return r.StatusCode == http.StatusBadRequest
	}, "producer should be gone")
	still := cfnQueryInRegion(t, srv, "us-east-1", "DescribeStacks", url.Values{"StackName": []string{"weak-consumer"}})
	defer still.Body.Close()
	if b := string(readBody(t, still)); !strings.Contains(b, "CREATE_COMPLETE") {
		t.Fatalf("consumer should be untouched by the producer's deletion, got: %s", b)
	}
}

// The YAML short form, same region, no Region parameter.
const weakConsumerYAML = `
Resources:
  Queue:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: !GetStackOutput
        StackName: weak-producer
        OutputName: TopicName
`

func TestGetStackOutput_shortFormDefaultsToTheStacksOwnRegion(t *testing.T) {
	// Given: a producer in the default region
	srv := helpers.NewTestServer(t)
	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"weak-producer"},
		"TemplateBody": []string{weakProducerTemplate},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "weak-producer", "CREATE_COMPLETE")

	// When: a consumer written in YAML short form reads it
	cr2 := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"weak-consumer-yaml"},
		"TemplateBody": []string{weakConsumerYAML},
	})
	defer cr2.Body.Close()
	helpers.AssertStatus(t, cr2, http.StatusOK)
	waitForStackStatus(t, srv, "weak-consumer-yaml", "CREATE_COMPLETE")

	// Then: the queue was named after the producer's topic
	resp := cfnQuery(t, srv, "DescribeStackResources", url.Values{"StackName": []string{"weak-consumer-yaml"}})
	defer resp.Body.Close()
	if b := string(readBody(t, resp)); !strings.Contains(b, "weak-producer-topic") {
		t.Fatalf("queue should be named from the producer's output, got: %s", b)
	}
}

func TestGetStackOutput_missingStackRollsTheConsumerBack(t *testing.T) {
	// Given: no producer at all
	srv := helpers.NewTestServer(t)

	// When: a consumer references it
	createStackInRegion(t, srv, "us-east-1", "dangling-consumer", weakConsumerTemplate, nil)

	// Then: the create rolls back, and the reason names the stack and region
	waitForStackStatusInRegion(t, srv, "us-east-1", "dangling-consumer", "ROLLBACK_COMPLETE")
	resp := cfnQueryInRegion(t, srv, "us-east-1", "DescribeStackEvents", url.Values{"StackName": []string{"dangling-consumer"}})
	defer resp.Body.Close()
	body := string(readBody(t, resp))
	if !strings.Contains(body, `stack &#34;weak-producer&#34; does not exist in us-west-2`) &&
		!strings.Contains(body, `stack "weak-producer" does not exist in us-west-2`) {
		t.Fatalf("events should say which stack was missing and where, got: %s", body)
	}
}

// Two consumers of the same producer: Stale keeps its properties across the
// update, Fresh changes. Without the resolve-error being taken per resource,
// Stale's failure would be charged to Fresh.
const twoConsumerTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Parameters": { "Suffix": { "Type": "String", "Default": "one" } },
  "Resources": {
    "Stale": {
      "Type": "AWS::SQS::Queue",
      "Properties": {
        "QueueName": { "Fn::GetStackOutput": { "StackName": "weak-producer", "OutputName": "TopicName" } }
      }
    },
    "Fresh": {
      "Type": "AWS::SQS::Queue",
      "Properties": { "QueueName": { "Fn::Sub": "fresh-${Suffix}" } }
    }
  }
}`

var eventMemberPattern = regexp.MustCompile(`(?s)<member>(.*?)</member>`)

func TestGetStackOutput_updateFailsTheResourceThatMadeTheReference(t *testing.T) {
	// Given: a producer, a consumer of it, and then the producer deleted
	srv := helpers.NewTestServer(t)
	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"weak-producer"},
		"TemplateBody": []string{weakProducerTemplate},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "weak-producer", "CREATE_COMPLETE")
	cr2 := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"two-consumers"},
		"TemplateBody": []string{twoConsumerTemplate},
	})
	defer cr2.Body.Close()
	helpers.AssertStatus(t, cr2, http.StatusOK)
	waitForStackStatus(t, srv, "two-consumers", "CREATE_COMPLETE")
	del := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": []string{"weak-producer"}})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	waitForStackStatus(t, srv, "weak-producer", "DELETE_COMPLETE")

	// When: the consumer is updated in a way that changes only Fresh
	up := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":                          []string{"two-consumers"},
		"TemplateBody":                       []string{twoConsumerTemplate},
		"Parameters.member.1.ParameterKey":   []string{"Suffix"},
		"Parameters.member.1.ParameterValue": []string{"two"},
	})
	defer up.Body.Close()
	helpers.AssertStatus(t, up, http.StatusOK)

	// Then: the update rolls back because the producer is gone...
	waitForStackStatus(t, srv, "two-consumers", "UPDATE_ROLLBACK_COMPLETE")

	// ...and the resource that failed is Stale, which made the reference
	resp := cfnQuery(t, srv, "DescribeStackEvents", url.Values{"StackName": []string{"two-consumers"}})
	defer resp.Body.Close()
	body := string(readBody(t, resp))
	var failed []string
	for _, m := range eventMemberPattern.FindAllStringSubmatch(body, -1) {
		if strings.Contains(m[1], "<ResourceStatus>UPDATE_FAILED</ResourceStatus>") {
			if id := regexp.MustCompile(`<LogicalResourceId>([^<]+)</LogicalResourceId>`).FindStringSubmatch(m[1]); id != nil {
				failed = append(failed, id[1])
			}
		}
	}
	if len(failed) != 1 || failed[0] != "Stale" {
		t.Fatalf("UPDATE_FAILED resources = %v, want exactly [Stale]; events: %s", failed, body)
	}
}
