package shield_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// shieldTagPair mirrors the Shield Tag structure: Tags are sent and returned
// as a LIST of {Key,Value} structs
// (https://docs.aws.amazon.com/waf/latest/DDOSAPIReference/API_Tag.html),
// never as a JSON object.
type shieldTagPair struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// createTaggedProtection creates a protection and returns its ARN in the
// protection/<id> shape TagResource/UntagResource/ListTagsForResource expect.
func createTaggedProtection(t *testing.T, srv *helpers.TestServer, name, resourceArn string) string {
	t.Helper()
	id := createTestProtection(t, srv, name, resourceArn)
	return protectionARN(id)
}

// ─── TagResource ─────────────────────────────────────────────────────────────

func TestTagResource_success(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	arn := createTaggedProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")

	// When: TagResource is called with two tags
	resp := shieldCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": arn,
		"Tags": []shieldTagPair{
			{Key: "env", Value: "prod"},
			{Key: "team", Value: "security"},
		},
	})
	defer resp.Body.Close()

	// Then: 200 with an empty body
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	// And: the tags round-trip through ListTagsForResource, as a list of
	// Key/Value structs
	listResp := shieldCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list struct {
		Tags []shieldTagPair `json:"Tags"`
	}
	helpers.DecodeJSON(t, listResp, &list)
	got := map[string]string{}
	for _, p := range list.Tags {
		got[p.Key] = p.Value
	}
	if got["env"] != "prod" || got["team"] != "security" {
		t.Errorf("Tags: got %v, want env=prod team=security", got)
	}
}

func TestTagResource_missingResourceARN(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: TagResource is called without ResourceARN
	resp := shieldCall(t, srv, "TagResource", map[string]any{
		"Tags": []shieldTagPair{{Key: "env", Value: "prod"}},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestTagResource_malformedResourceARN(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: TagResource is called with an ARN that is not a protection ARN
	resp := shieldCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc",
		"Tags":        []shieldTagPair{{Key: "env", Value: "prod"}},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException — Overcast's tag surface only resolves
	// protection ARNs (arn:...:protection/<id>), not arbitrary resource ARNs
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestTagResource_unknownProtection(t *testing.T) {
	// Given: a syntactically valid protection ARN naming no protection
	srv := helpers.NewTestServer(t)

	// When: TagResource is called
	resp := shieldCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": protectionARN("33333333-3333-3333-3333-333333333333"),
		"Tags":        []shieldTagPair{{Key: "env", Value: "prod"}},
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	assertResourceNotFound(t, resp)
}

func TestTagResource_reservedKeyPrefixRejected(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	arn := createTaggedProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")

	// When: TagResource is called with a reserved aws: key prefix
	resp := shieldCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": arn,
		"Tags":        []shieldTagPair{{Key: "aws:reserved", Value: "x"}},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// TestTagResource_tagLimitEnforced pins the 50-tags-per-resource limit that
// the Tag documentation states in prose ("you can specify one or more tags
// to add to each AWS resource, up to 50 tags for a resource" —
// https://docs.aws.amazon.com/waf/latest/DDOSAPIReference/API_Tag.html),
// distinct from the Tags array's own 200-item wire cap.
func TestTagResource_tagLimitEnforced(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	arn := createTaggedProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")

	// When: TagResource is called with 51 tags
	tags := make([]shieldTagPair, 0, 51)
	for i := 0; i < 51; i++ {
		tags = append(tags, shieldTagPair{Key: "k" + strconv.Itoa(i), Value: "v"})
	}
	resp := shieldCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": arn,
		"Tags":        tags,
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

// ─── UntagResource ───────────────────────────────────────────────────────────

func TestUntagResource_removesKey(t *testing.T) {
	// Given: a protection tagged with two keys
	srv := helpers.NewTestServer(t)
	arn := createTaggedProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")
	tagResp := shieldCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": arn,
		"Tags":        []shieldTagPair{{Key: "env", Value: "prod"}, {Key: "team", Value: "sec"}},
	})
	tagResp.Body.Close()
	helpers.AssertStatus(t, tagResp, http.StatusOK)

	// When: one key is removed
	resp := shieldCall(t, srv, "UntagResource", map[string]any{
		"ResourceARN": arn,
		"TagKeys":     []string{"env"},
	})
	defer resp.Body.Close()

	// Then: 200, and only the remaining tag is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	listResp := shieldCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer listResp.Body.Close()
	var list struct {
		Tags []shieldTagPair `json:"Tags"`
	}
	helpers.DecodeJSON(t, listResp, &list)
	if len(list.Tags) != 1 || list.Tags[0].Key != "team" {
		t.Errorf("Tags after untag: got %v, want only team", list.Tags)
	}
}

func TestUntagResource_unknownProtection(t *testing.T) {
	// Given: a syntactically valid protection ARN naming no protection
	srv := helpers.NewTestServer(t)

	// When: UntagResource is called
	resp := shieldCall(t, srv, "UntagResource", map[string]any{
		"ResourceARN": protectionARN("44444444-4444-4444-4444-444444444444"),
		"TagKeys":     []string{"env"},
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	assertResourceNotFound(t, resp)
}

// ─── ListTagsForResource ─────────────────────────────────────────────────────

func TestListTagsForResource_emptyForUntaggedProtection(t *testing.T) {
	// Given: a protection with no tags
	srv := helpers.NewTestServer(t)
	arn := createTaggedProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")

	// When: ListTagsForResource is called
	resp := shieldCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer resp.Body.Close()

	// Then: an empty Tags list, not an error
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Tags []shieldTagPair `json:"Tags"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Tags) != 0 {
		t.Errorf("expected no tags, got %v", out.Tags)
	}
}

func TestListTagsForResource_unknownProtection(t *testing.T) {
	// Given: a syntactically valid protection ARN naming no protection
	srv := helpers.NewTestServer(t)

	// When: ListTagsForResource is called
	resp := shieldCall(t, srv, "ListTagsForResource", map[string]any{
		"ResourceARN": protectionARN("55555555-5555-5555-5555-555555555555"),
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	assertResourceNotFound(t, resp)
}

// ─── Delete cascade ──────────────────────────────────────────────────────────

// TestDeleteProtection_cascadesTags confirms that deleting a protection also
// removes its tags: tags are stored inline on the protection record
// (protectionRecord.Tags), so deleting the record deletes the tags with it,
// and any later tag operation against the same ARN reports the protection
// itself as not found.
func TestDeleteProtection_cascadesTags(t *testing.T) {
	// Given: a tagged protection
	srv := helpers.NewTestServer(t)
	id := createTestProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")
	arn := protectionARN(id)
	tagResp := shieldCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": arn,
		"Tags":        []shieldTagPair{{Key: "env", Value: "prod"}},
	})
	tagResp.Body.Close()
	helpers.AssertStatus(t, tagResp, http.StatusOK)

	// When: the protection is deleted
	delResp := shieldCall(t, srv, "DeleteProtection", map[string]any{"ProtectionId": id})
	delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Then: ListTagsForResource on the same ARN now reports the protection
	// as not found, rather than an empty (or the old) tag set
	listResp := shieldCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer listResp.Body.Close()
	assertResourceNotFound(t, listResp)
}
