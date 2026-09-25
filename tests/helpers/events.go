package helpers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// BusEvent is one event-bus event as the emulator's events endpoints return
// it; Payload is left raw for the test to decode into the shape it expects.
type BusEvent struct {
	Type        string          `json:"type"`
	Source      string          `json:"source"`
	ResourceARN string          `json:"resourceArn"`
	Payload     json.RawMessage `json:"payload"`
}

// EventsOfCall returns the events of type typ published by the SDK call that
// answered with metadata, in order. It fails the test when the response
// carries no request ID the SDK could read; PinRequestID is the way round a
// response that does not.
func EventsOfCall(t *testing.T, srv *TestServer, metadata middleware.Metadata, typ string) []BusEvent {
	t.Helper()
	requestID, ok := awsmiddleware.GetRequestIDMetadata(metadata)
	if !ok || requestID == "" {
		t.Fatal("the response carries no request ID")
	}
	return EventsOfRequest(t, srv, requestID, typ)
}

// PinRequestID is an SDK API option that sends requestID as the call's
// request ID, which Overcast adopts, so a test can look up what the call
// published without reading the ID back off the response.
func PinRequestID(requestID string) func(*middleware.Stack) error {
	return smithyhttp.AddHeaderValue("x-amzn-requestid", requestID)
}

// EventsOfRequest returns the events of type typ that the request with
// requestID published, in order, as GET /_overcast/events/request/{id}
// lists them.
func EventsOfRequest(t *testing.T, srv *TestServer, requestID, typ string) []BusEvent {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/_overcast/events/request/"+url.PathEscape(requestID), nil)
	if err != nil {
		t.Fatalf("build events request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events of request %s: %v", requestID, err)
	}
	defer resp.Body.Close()
	AssertStatus(t, resp, http.StatusOK)
	var all []BusEvent
	DecodeJSON(t, resp, &all)
	var out []BusEvent
	for _, e := range all {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}
