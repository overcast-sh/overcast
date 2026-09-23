package sqs_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The system map's queue node shows the queue's message counts. Messages live
// in SQS's dedicated message backend, not the generic kv store, so this pins
// the map to the same counts GetQueueAttributes reports.

// Given: a queue holding two messages, one of which has been received.
// When: the topology is fetched.
// Then: the queue node reports one visible and one in-flight message.
func TestTopology_queueNodeReportsMessageCounts(t *testing.T) {
	srv := helpers.NewTestServer(t)
	call := func(action string, body map[string]any) map[string]any {
		t.Helper()
		resp := sqsCallWithHeaders(t, srv, action, body, nil)
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	queueURL, _ := call("CreateQueue", map[string]any{"QueueName": "map-counts"})["QueueUrl"].(string)
	call("SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "one"})
	call("SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "two"})
	call("ReceiveMessage", map[string]any{"QueueUrl": queueURL, "MaxNumberOfMessages": 1, "VisibilityTimeout": 300})

	resp, err := http.Get(srv.URL + "/_overcast/topology")
	if err != nil {
		t.Fatalf("GET /_overcast/topology: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var topo struct {
		Nodes []struct {
			ID                                    string `json:"id"`
			ApproximateNumberOfMessages           *int   `json:"approximateNumberOfMessages"`
			ApproximateNumberOfMessagesNotVisible *int   `json:"approximateNumberOfMessagesNotVisible"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&topo); err != nil {
		t.Fatalf("decode topology: %v", err)
	}

	wantID := srv.Config.Region + "::sqs::map-counts"
	for _, n := range topo.Nodes {
		if n.ID != wantID {
			continue
		}
		if n.ApproximateNumberOfMessages == nil || *n.ApproximateNumberOfMessages != 1 {
			t.Errorf("visible messages: got %v, want 1", deref(n.ApproximateNumberOfMessages))
		}
		if n.ApproximateNumberOfMessagesNotVisible == nil || *n.ApproximateNumberOfMessagesNotVisible != 1 {
			t.Errorf("in-flight messages: got %v, want 1", deref(n.ApproximateNumberOfMessagesNotVisible))
		}
		return
	}
	t.Fatalf("no queue node %q in topology: %+v", wantID, topo.Nodes)
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
