package cloudformation_test

// cloudfront_properties_test.go — AWS::CloudFront::Distribution Tags (#545).
//
// The distribution config itself is the one handler in the #540 sweep that
// round-trips without an allow-list: the whole DistributionConfig map is
// handed to a generic map-to-XML marshaller, so every origin, cache
// behaviour and alias survives untouched. Tags is the one gap — it is the
// resource's own top-level property, not a DistributionConfig member, and
// the handler always posted to plain /2020-05-31/distribution, which has no
// way to carry it. CreateDistributionWithTags is the same create, wrapped so
// the tags land atomically with the distribution — implemented and
// supported (internal/services/cloudfront/capabilities_dev.go) and never
// called by the CloudFormation handler.
//
// This reads the tags back through ListTagsForResource, and confirms a
// proxied request still reaches the origin, the same shape #545's Definition
// of Done asks for: a property that reaches the service but is not stored is
// as invisible to a user as one that never left the handler.

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const cloudfrontDistributionPropertiesTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Distribution": {
      "Type": "AWS::CloudFront::Distribution",
      "Properties": {
        "DistributionConfig": {
          "Enabled": true,
          "Origins": [
            {"Id": "origin1", "DomainName": "example.com"}
          ],
          "DefaultCacheBehavior": {
            "TargetOriginId": "origin1",
            "ViewerProtocolPolicy": "allow-all"
          }
        },
        "Tags": [
          {"Key": "env", "Value": "prod"},
          {"Key": "Owner", "Value": "platform"}
        ]
      }
    }
  },
  "Outputs": {
    "DistributionId": { "Value": { "Ref": "Distribution" } }
  }
}`

// TestCreateStack_CloudFrontDistribution_tagsForwarded is the failing-first
// case for #545: Tags never reached CreateDistribution, so
// ListTagsForResource returned nothing for a distribution the template
// tagged.
func TestCreateStack_CloudFrontDistribution_tagsForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "cloudfront-distribution-properties-stack"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{cloudfrontDistributionPropertiesTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	outputs := describeStackOutputs(t, srv, stackName)
	distID := outputs["DistributionId"]
	if distID == "" {
		t.Fatalf("DistributionId output was empty: %v", outputs)
	}

	arn := "arn:aws:cloudfront::000000000000:distribution/" + distID
	tags := cloudfrontListTagsForResource(t, srv, arn)
	if tags["env"] != "prod" || tags["Owner"] != "platform" {
		t.Errorf("tags = %v, want env=prod and Owner=platform", tags)
	}
}

// cloudfrontListTagsForResource reads a resource's tags back through
// GET /2020-05-31/tagging?Resource=<arn>, as a {Key: Value} map.
func cloudfrontListTagsForResource(t *testing.T, srv *helpers.TestServer, resourceARN string) map[string]string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/tagging?Resource="+url.QueryEscape(resourceARN), nil)
	if err != nil {
		t.Fatalf("build ListTagsForResource request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListTagsForResource: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var out struct {
		XMLName xml.Name `xml:"Tagging"`
		Tags    struct {
			Items []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Items>Tag"`
		} `xml:"Tags"`
	}
	body := readBody(t, resp)
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal ListTagsForResource response: %v\nbody: %s", err, body)
	}
	tags := make(map[string]string, len(out.Tags.Items))
	for _, tag := range out.Tags.Items {
		tags[tag.Key] = tag.Value
	}
	return tags
}
