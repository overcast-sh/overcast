package cloudformation_test

// firehose_destinations_test.go — AWS::KinesisFirehose::DeliveryStream
// forwards the properties CDK/CloudFormation templates set (#535).
// firehoseDeliveryStreamHandler.Create used to build a two-field body
// (DeliveryStreamName, DeliveryStreamType), dropping every destination
// configuration, the Kinesis stream source configuration, the encryption
// configuration and Tags — so a CDK delivery stream provisioned as a
// nameless-destination stream and DescribeDeliveryStream reported no
// destinations, no matter what the template asked for.

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const firehoseExtendedS3Template = `{
  "Resources": {
    "Hose": {
      "Type": "AWS::KinesisFirehose::DeliveryStream",
      "Properties": {
        "DeliveryStreamName": "cfn-s3-hose",
        "ExtendedS3DestinationConfiguration": {
          "BucketARN": "arn:aws:s3:::cfn-firehose-target",
          "RoleARN": "arn:aws:iam::000000000000:role/firehose",
          "Prefix": "firehose/"
        },
        "Tags": [{"Key": "environment", "Value": "development"}]
      }
    }
  }
}`

func TestCreateStack_FirehoseDeliveryStream_destinationAndTagsForwarded(t *testing.T) {
	// Given: a stack carrying a delivery stream with an S3 destination and inline Tags
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {"firehose-s3-forward-stack"},
		"TemplateBody": {firehoseExtendedS3Template},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, "firehose-s3-forward-stack", "CREATE_COMPLETE")

	// Then: DescribeDeliveryStream names the S3 destination from the template
	desc := firehoseJSONCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "cfn-s3-hose",
	})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusOK)
	var out struct {
		DeliveryStreamDescription struct {
			DeliveryStreamType string           `json:"DeliveryStreamType"`
			Destinations       []map[string]any `json:"Destinations"`
		} `json:"DeliveryStreamDescription"`
	}
	helpers.DecodeJSON(t, desc, &out)

	if got := out.DeliveryStreamDescription.DeliveryStreamType; got != "DirectPut" {
		t.Errorf("DeliveryStreamType = %q, want DirectPut", got)
	}
	if len(out.DeliveryStreamDescription.Destinations) != 1 {
		t.Fatalf("Destinations = %#v, want exactly one", out.DeliveryStreamDescription.Destinations)
	}
	dest := out.DeliveryStreamDescription.Destinations[0]
	if _, ok := dest["DestinationId"].(string); !ok {
		t.Errorf("Destination has no DestinationId: %#v", dest)
	}
	s3Desc, ok := dest["ExtendedS3DestinationDescription"].(map[string]any)
	if !ok {
		t.Fatalf("expected ExtendedS3DestinationDescription, got %#v", dest)
	}
	if got := s3Desc["BucketARN"]; got != "arn:aws:s3:::cfn-firehose-target" {
		t.Errorf("BucketARN = %v, want arn:aws:s3:::cfn-firehose-target", got)
	}
	if got := s3Desc["Prefix"]; got != "firehose/" {
		t.Errorf("Prefix = %v, want firehose/", got)
	}

	// And: the inline Tags landed too
	tagsResp := firehoseJSONCall(t, srv, "ListTagsForDeliveryStream", map[string]any{
		"DeliveryStreamName": "cfn-s3-hose",
	})
	defer tagsResp.Body.Close()
	helpers.AssertStatus(t, tagsResp, http.StatusOK)
	var tagsOut struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	helpers.DecodeJSON(t, tagsResp, &tagsOut)
	if len(tagsOut.Tags) != 1 || tagsOut.Tags[0].Key != "environment" || tagsOut.Tags[0].Value != "development" {
		t.Errorf("Tags = %#v, want [{environment development}]", tagsOut.Tags)
	}
}

const firehoseKinesisSourceTemplate = `{
  "Resources": {
    "Hose": {
      "Type": "AWS::KinesisFirehose::DeliveryStream",
      "Properties": {
        "DeliveryStreamName": "cfn-kinesis-source-hose",
        "KinesisStreamSourceConfiguration": {
          "KinesisStreamARN": "arn:aws:kinesis:us-east-1:000000000000:stream/source-stream",
          "RoleARN": "arn:aws:iam::000000000000:role/firehose-source"
        },
        "ExtendedS3DestinationConfiguration": {
          "BucketARN": "arn:aws:s3:::cfn-firehose-target",
          "RoleARN": "arn:aws:iam::000000000000:role/firehose"
        }
      }
    }
  }
}`

// TestCreateStack_FirehoseDeliveryStream_kinesisSourceImpliesType pins #535's
// definition of done: a template that sets KinesisStreamSourceConfiguration
// but not DeliveryStreamType — AWS's own example CloudFormation template for
// this exact shape sets both, but nothing requires a template author to
// follow it — gets a KinesisStreamAsSource stream, not the DirectPut default.
func TestCreateStack_FirehoseDeliveryStream_kinesisSourceImpliesType(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {"firehose-kinesis-source-stack"},
		"TemplateBody": {firehoseKinesisSourceTemplate},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, "firehose-kinesis-source-stack", "CREATE_COMPLETE")

	desc := firehoseJSONCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "cfn-kinesis-source-hose",
	})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusOK)
	var out struct {
		DeliveryStreamDescription struct {
			DeliveryStreamType string `json:"DeliveryStreamType"`
			Source             struct {
				KinesisStreamSourceDescription map[string]any `json:"KinesisStreamSourceDescription"`
			} `json:"Source"`
		} `json:"DeliveryStreamDescription"`
	}
	helpers.DecodeJSON(t, desc, &out)

	if got := out.DeliveryStreamDescription.DeliveryStreamType; got != "KinesisStreamAsSource" {
		t.Errorf("DeliveryStreamType = %q, want KinesisStreamAsSource", got)
	}
	if got := out.DeliveryStreamDescription.Source.KinesisStreamSourceDescription["KinesisStreamARN"]; got != "arn:aws:kinesis:us-east-1:000000000000:stream/source-stream" {
		t.Errorf("Source.KinesisStreamSourceDescription.KinesisStreamARN = %v", got)
	}
}

// TestUpdateStack_FirehoseDeliveryStream_tagsOnlyChangeReconciles pins that a
// Tags-only change goes through TagDeliveryStream/UntagDeliveryStream rather
// than forcing replacement — the one property change Firehose can apply in
// place, since it implements no UpdateDestination.
func TestUpdateStack_FirehoseDeliveryStream_tagsOnlyChangeReconciles(t *testing.T) {
	srv := helpers.NewTestServer(t)
	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {"firehose-update-tags-stack"},
		"TemplateBody": {firehoseExtendedS3Template},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, "firehose-update-tags-stack", "CREATE_COMPLETE")

	updated := strings.Replace(firehoseExtendedS3Template, `"Value": "development"`, `"Value": "production"`, 1)
	update := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {"firehose-update-tags-stack"},
		"TemplateBody": {updated},
	})
	defer update.Body.Close()
	helpers.AssertStatus(t, update, http.StatusOK)
	waitForStackStatus(t, srv, "firehose-update-tags-stack", "UPDATE_COMPLETE")

	tagsResp := firehoseJSONCall(t, srv, "ListTagsForDeliveryStream", map[string]any{
		"DeliveryStreamName": "cfn-s3-hose",
	})
	defer tagsResp.Body.Close()
	helpers.AssertStatus(t, tagsResp, http.StatusOK)
	var tagsOut struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	helpers.DecodeJSON(t, tagsResp, &tagsOut)
	if len(tagsOut.Tags) != 1 || !reflect.DeepEqual(tagsOut.Tags[0].Value, "production") {
		t.Errorf("Tags = %#v, want [{environment production}]", tagsOut.Tags)
	}
}
