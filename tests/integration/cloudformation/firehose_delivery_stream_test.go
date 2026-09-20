package cloudformation_test

// firehose_delivery_stream_test.go — what Ref returns for
// AWS::KinesisFirehose::DeliveryStream, and what stack deletion leaves behind.
//
// AWS documents Ref as the delivery stream *name*: "When you pass the logical
// ID of this resource to the intrinsic Ref function, Ref returns the delivery
// stream name." Overcast returned the ARN, which is also the resource's
// physical ID — and the physical ID is what the handler hands back as
// DeliveryStreamName on teardown. An ARN is not a legal delivery stream name
// (@length(1, 64), @pattern ^[a-zA-Z0-9_.-]+$), so the delete resolved nothing
// and the stream outlived its stack. The same bug class as the event bus one
// in eventbus_ref_test.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const firehoseDeliveryStreamTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Hose": {
      "Type": "AWS::KinesisFirehose::DeliveryStream",
      "Properties": { "DeliveryStreamName": "ref-probe-hose" }
    }
  },
  "Outputs": {
    "HoseRef": { "Value": { "Ref": "Hose" } },
    "HoseArn": { "Value": { "Fn::GetAtt": ["Hose", "Arn"] } }
  }
}`

func firehoseJSONCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
	t.Helper()
	return awsJSONCall(t, srv, "Firehose_20150804.", action, "application/x-amz-json-1.1", body)
}

func TestCreateStack_firehoseDeliveryStreamRefIsTheNameNotTheArn(t *testing.T) {
	// Given: a stack carrying one named delivery stream
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"firehose-ref-stack"},
		"TemplateBody": []string{firehoseDeliveryStreamTemplate},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, "firehose-ref-stack", "CREATE_COMPLETE")

	// When: the stack's outputs are read
	outputs := describeStackOutputs(t, srv, "firehose-ref-stack")

	// Then: Ref is the delivery stream name, and the ARN is still on GetAtt
	if got := outputs["HoseRef"]; got != "ref-probe-hose" {
		t.Errorf("Ref = %q, want the delivery stream name %q", got, "ref-probe-hose")
	}
	arn := outputs["HoseArn"]
	if !strings.HasSuffix(arn, ":deliverystream/ref-probe-hose") {
		t.Errorf("GetAtt Arn = %q, want it to end in :deliverystream/ref-probe-hose", arn)
	}
	if strings.Count(arn, "arn:aws:firehose:") != 1 {
		t.Errorf("GetAtt Arn = %q, want a single ARN rather than a doubled one", arn)
	}
}

func TestDeleteStack_firehoseDeliveryStreamIsDeletedWithIt(t *testing.T) {
	// Given: a stack carrying one named delivery stream, which really exists
	srv := helpers.NewTestServer(t)
	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"firehose-teardown-stack"},
		"TemplateBody": []string{firehoseDeliveryStreamTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, "firehose-teardown-stack", "CREATE_COMPLETE")

	describe := firehoseJSONCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "ref-probe-hose",
	})
	defer describe.Body.Close()
	helpers.AssertStatus(t, describe, http.StatusOK)

	// When: the stack is deleted
	del := cfnQuery(t, srv, "DeleteStack", url.Values{
		"StackName": []string{"firehose-teardown-stack"},
	})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	waitForStackStatus(t, srv, "firehose-teardown-stack", "DELETE_COMPLETE")

	// Then: the delivery stream went with it, rather than outliving the stack
	gone := firehoseJSONCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "ref-probe-hose",
	})
	defer gone.Body.Close()
	helpers.AssertStatus(t, gone, http.StatusBadRequest)
	helpers.AssertJSONError(t, gone, "ResourceNotFoundException")

	// And it is gone from the listing too, not merely unresolvable by name.
	list := firehoseJSONCall(t, srv, "ListDeliveryStreams", map[string]any{})
	defer list.Body.Close()
	body := readBody(t, list)
	if bytes.Contains(body, []byte("ref-probe-hose")) {
		var listed map[string]any
		_ = json.Unmarshal(body, &listed)
		t.Errorf("ListDeliveryStreams still reports the stream after the stack was deleted: %v", listed)
	}
}
