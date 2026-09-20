// tag_validation_test.go — CloudFront TagResource/
// CreateDistributionWithTags tag validation (#1052).
//
// Both used to store whatever a caller sent without checking AWS's own tag
// constraints — a reserved `aws:` key prefix, or more than 50 tags on one
// distribution, had to be rejected the way real AWS rejects them, and
// neither was.
package cloudfront_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func cfListTagsMap(t *testing.T, srv *helpers.TestServer, arn string) map[string]string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/tagging?Resource="+arn, nil)
	if err != nil {
		t.Fatalf("build ListTagsForResource request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListTagsForResource: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	b := readBody(t, resp)
	var result struct {
		Tags struct {
			Items []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Items>Tag"`
		} `xml:"Tags"`
	}
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal Tagging: %v\nbody: %s", err, b)
	}
	got := make(map[string]string, len(result.Tags.Items))
	for _, tag := range result.Tags.Items {
		got[tag.Key] = tag.Value
	}
	return got
}

func TestTagResource_reservedTagPrefixRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "invalid-tag-resource")

	body := `<?xml version="1.0" encoding="UTF-8"?>
<Tags xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Items><Tag><Key>aws:reserved</Key><Value>x</Value></Tag></Items>
</Tags>`
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/tagging?Operation=Tag&Resource="+dist.ARN,
		bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build TagResource request: %v", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("TagResource: %v", err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertErrorEnvelope(t, readBody(t, resp), "InvalidArgument")

	if got := cfListTagsMap(t, srv, dist.ARN); len(got) != 0 {
		t.Fatalf("tags = %#v after a rejected TagResource, want none stored", got)
	}
}

func TestCreateDistributionWithTags_reservedTagPrefixRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)

	body := distributionConfigWithTagsXML("invalid-tag-create", "aws:reserved", "x")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/2020-05-31/distribution?WithTags", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build CreateDistributionWithTags request: %v", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateDistributionWithTags: %v", err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertErrorEnvelope(t, readBody(t, resp), "InvalidArgument")
}

func TestTagResource_validTagsStillWork(t *testing.T) {
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "valid-tag-resource")
	cfTagResource(t, srv, dist.ARN, "env", "prod")

	if got := cfListTagsMap(t, srv, dist.ARN); got["env"] != "prod" {
		t.Fatalf("tags = %#v, want env=prod", got)
	}
}

// The 50-tag limit is checked against the existing plus incoming set, not
// just what one TagResource call adds.
func TestTagResource_tagLimitEnforcedOnMergedSet(t *testing.T) {
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "tag-limit")

	var items strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&items, "<Tag><Key>k%d</Key><Value>v</Value></Tag>", i)
	}
	seedBody := `<?xml version="1.0" encoding="UTF-8"?>
<Tags xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Items>` + items.String() + `</Items>
</Tags>`
	seedReq, err := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/tagging?Operation=Tag&Resource="+dist.ARN,
		bytes.NewReader([]byte(seedBody)))
	if err != nil {
		t.Fatalf("build seed TagResource request: %v", err)
	}
	seedReq.Header.Set("Content-Type", "application/xml")
	seedResp, err := http.DefaultClient.Do(seedReq)
	if err != nil {
		t.Fatalf("seed TagResource: %v", err)
	}
	defer seedResp.Body.Close()
	helpers.AssertStatus(t, seedResp, http.StatusNoContent)

	body := `<?xml version="1.0" encoding="UTF-8"?>
<Tags xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Items><Tag><Key>one-too-many</Key><Value>x</Value></Tag></Items>
</Tags>`
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/tagging?Operation=Tag&Resource="+dist.ARN,
		bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build TagResource request: %v", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("TagResource: %v", err)
	}
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertErrorEnvelope(t, readBody(t, resp), "InvalidArgument")
}
