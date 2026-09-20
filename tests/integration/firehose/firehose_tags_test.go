package firehose_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// fhTag mirrors the Firehose Tag structure: real Firehose sends and returns
// tags as a LIST of {Key,Value} structs, never as a JSON object.
type fhTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// TestCreateDeliveryStream_tagsAppliedAtCreate: Tags passed to
// CreateDeliveryStream (issue #1196, Axis B) must be stored and readable
// immediately, without a follow-up TagDeliveryStream call.
func TestCreateDeliveryStream_tagsAppliedAtCreate(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": "created-tagged",
		"Tags":               []fhTag{{Key: "env", Value: "prod"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	listResp := fhCall(t, srv, "ListTagsForDeliveryStream", map[string]any{
		"DeliveryStreamName": "created-tagged",
	})
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list struct {
		Tags []fhTag `json:"Tags"`
	}
	helpers.DecodeJSON(t, listResp, &list)
	if len(list.Tags) != 1 || list.Tags[0].Key != "env" || list.Tags[0].Value != "prod" {
		t.Errorf("Tags: got %v, want env=prod", list.Tags)
	}
}

// TestCreateDeliveryStream_invalidTagRejected: an invalid tag passed to
// CreateDeliveryStream must be rejected before the stream is created.
func TestCreateDeliveryStream_invalidTagRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": "rejected-stream",
		"Tags":               []fhTag{{Key: "aws:reserved", Value: "x"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")

	desc := fhCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "rejected-stream",
	})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusBadRequest)
	helpers.AssertJSONError(t, desc, "ResourceNotFoundException")
}

// TestTagDeliveryStream_sdkListShape sends the exact JSON shape the Firehose
// SDK produces — Tags as a list of {Key,Value} structs.
func TestTagDeliveryStream_sdkListShape(t *testing.T) {
	// Given: a delivery stream
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "tag-stream")

	// When: TagDeliveryStream is called with the SDK list shape
	resp := fhCall(t, srv, "TagDeliveryStream", map[string]any{
		"DeliveryStreamName": "tag-stream",
		"Tags":               []fhTag{{Key: "env", Value: "prod"}, {Key: "team", Value: "data"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: ListTagsForDeliveryStream returns both tags as a list
	listResp := fhCall(t, srv, "ListTagsForDeliveryStream", map[string]any{
		"DeliveryStreamName": "tag-stream",
	})
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list struct {
		Tags        []fhTag `json:"Tags"`
		HasMoreTags bool    `json:"HasMoreTags"`
	}
	helpers.DecodeJSON(t, listResp, &list)
	got := map[string]string{}
	for _, tag := range list.Tags {
		got[tag.Key] = tag.Value
	}
	if got["env"] != "prod" || got["team"] != "data" {
		t.Errorf("Tags: got %v, want env=prod team=data", got)
	}
}

// TestUntagDeliveryStream_removesKey checks the untag round trip.
func TestUntagDeliveryStream_removesKey(t *testing.T) {
	// Given: a tagged stream
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "untag-stream")
	resp := fhCall(t, srv, "TagDeliveryStream", map[string]any{
		"DeliveryStreamName": "untag-stream",
		"Tags":               []fhTag{{Key: "env", Value: "prod"}, {Key: "team", Value: "data"}},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: one key is removed
	untag := fhCall(t, srv, "UntagDeliveryStream", map[string]any{
		"DeliveryStreamName": "untag-stream",
		"TagKeys":            []string{"env"},
	})
	defer untag.Body.Close()
	helpers.AssertStatus(t, untag, http.StatusOK)

	// Then: only the remaining tag is returned
	listResp := fhCall(t, srv, "ListTagsForDeliveryStream", map[string]any{
		"DeliveryStreamName": "untag-stream",
	})
	defer listResp.Body.Close()
	var list struct {
		Tags []fhTag `json:"Tags"`
	}
	helpers.DecodeJSON(t, listResp, &list)
	if len(list.Tags) != 1 || list.Tags[0].Key != "team" {
		t.Errorf("Tags after untag: got %v, want only team", list.Tags)
	}
}

// TestListTagsForDeliveryStream_untaggedIsEmptyList: an untagged stream must
// serialize "Tags" as [] — a JSON null breaks strict SDK deserializers.
func TestListTagsForDeliveryStream_untaggedIsEmptyList(t *testing.T) {
	// Given: an untagged stream
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "bare-stream")

	// When: tags are listed
	resp := fhCall(t, srv, "ListTagsForDeliveryStream", map[string]any{
		"DeliveryStreamName": "bare-stream",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: Tags is an empty array, not null
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var raw struct {
		Tags json.RawMessage `json:"Tags"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if string(raw.Tags) != "[]" {
		t.Errorf("Tags serialized as %s, want [] (body: %s)", raw.Tags, body)
	}
}
