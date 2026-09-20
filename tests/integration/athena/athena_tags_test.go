package athena_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const athenaTagWGARN = "arn:aws:athena:us-east-1:000000000000:workgroup/tagged-wg"

func createTagTestWorkGroup(t *testing.T, srv *helpers.TestServer) {
	t.Helper()
	resp := athenaCall(t, srv, "CreateWorkGroup", map[string]any{"Name": "tagged-wg"})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// TestAthenaCreateWorkGroup_tagsAppliedAtCreate: Tags passed to
// CreateWorkGroup (issue #1196, Axis B) must be stored inline and readable
// via ListTagsForResource immediately, without a follow-up TagResource call.
func TestAthenaCreateWorkGroup_tagsAppliedAtCreate(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := athenaCall(t, srv, "CreateWorkGroup", map[string]any{
		"Name": "tagged-wg",
		"Tags": []map[string]string{{"Key": "env", "Value": "prod"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	list := athenaCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": athenaTagWGARN})
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	var out struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	helpers.DecodeJSON(t, list, &out)
	if len(out.Tags) != 1 || out.Tags[0].Key != "env" || out.Tags[0].Value != "prod" {
		t.Errorf("Tags: got %v, want env=prod", out.Tags)
	}
}

// TestAthenaCreateWorkGroup_invalidTagRejected: an over-limit tag set passed
// to CreateWorkGroup must be rejected before the workgroup is created, not
// silently dropped.
func TestAthenaCreateWorkGroup_invalidTagRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := athenaCall(t, srv, "CreateWorkGroup", map[string]any{
		"Name": "tagged-wg",
		"Tags": []map[string]string{{"Key": "aws:reserved", "Value": "x"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidRequestException")

	get := athenaCall(t, srv, "GetWorkGroup", map[string]any{"WorkGroup": "tagged-wg"})
	defer get.Body.Close()
	// 400, not 404: Athena's InvalidRequestException carries no @httpError, so
	// awsJson1_1's client-error default applies (#2009).
	helpers.AssertStatus(t, get, http.StatusBadRequest)
}

// TestAthenaTagResource_invalidTagRejected: reserved aws: tag keys must be
// rejected with Athena's InvalidRequestException, not silently stored.
func TestAthenaTagResource_invalidTagRejected(t *testing.T) {
	// Given: a workgroup
	srv := helpers.NewTestServer(t)
	createTagTestWorkGroup(t, srv)

	// When: TagResource is called with a reserved key
	resp := athenaCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": athenaTagWGARN,
		"Tags":        []map[string]string{{"Key": "aws:reserved", "Value": "x"}},
	})
	defer resp.Body.Close()

	// Then: the tag is rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidRequestException")
}

// TestAthenaTagResource_tooManyTagsRejected: exceeding the 50-tag limit is
// rejected with InvalidRequestException.
func TestAthenaTagResource_tooManyTagsRejected(t *testing.T) {
	// Given: a workgroup
	srv := helpers.NewTestServer(t)
	createTagTestWorkGroup(t, srv)

	tags := make([]map[string]string, 0, 51)
	for i := 0; i < 51; i++ {
		tags = append(tags, map[string]string{"Key": string(rune('a'+i%26)) + string(rune('a'+i/26)), "Value": "v"})
	}

	// When: TagResource is called with 51 tags
	resp := athenaCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": athenaTagWGARN,
		"Tags":        tags,
	})
	defer resp.Body.Close()

	// Then: the request is rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidRequestException")
}

// TestAthenaTagResource_validRoundTrip: valid tags still round trip.
func TestAthenaTagResource_validRoundTrip(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createTagTestWorkGroup(t, srv)

	resp := athenaCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": athenaTagWGARN,
		"Tags":        []map[string]string{{"Key": "env", "Value": "prod"}},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	list := athenaCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": athenaTagWGARN})
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	var out struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	helpers.DecodeJSON(t, list, &out)
	if len(out.Tags) != 1 || out.Tags[0].Key != "env" || out.Tags[0].Value != "prod" {
		t.Errorf("Tags: got %v, want env=prod", out.Tags)
	}
}
