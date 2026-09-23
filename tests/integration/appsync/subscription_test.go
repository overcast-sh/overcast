package appsync_test

// subscription_test.go — integration tests for AppSync WebSocket subscriptions.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// wsConnect upgrades an HTTP connection to a WebSocket at the realtime endpoint.
func wsConnect(t *testing.T, srv *helpers.TestServer, apiID string) (*websocket.Conn, context.Context) {
	t.Helper()
	keyID := firstAPIKey(t, srv, apiID)
	return wsConnectWithHeaders(t, srv, apiID, http.Header{"x-api-key": []string{keyID}})
}

func wsConnectWithHeaders(t *testing.T, srv *helpers.TestServer, apiID string, header http.Header) (*websocket.Conn, context.Context) {
	t.Helper()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/_overcast/appsync/apis/" + apiID + "/realtime"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("WebSocket dial: %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return conn, ctx
}

// firstAPIKey returns an existing API key for apiID, creating one if none
// exists yet. CreateGraphqlApi does not auto-create a key as a side effect
// (#62), so fixtures that need one for x-api-key auth create it explicitly,
// same as a real client would.
func firstAPIKey(t *testing.T, srv *helpers.TestServer, apiID string) string {
	t.Helper()
	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/apikeys")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ApiKeys []struct {
			Id string `json:"id"`
		} `json:"apiKeys"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.ApiKeys) > 0 {
		return result.ApiKeys[0].Id
	}

	createResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	var created struct {
		ApiKey struct {
			Id string `json:"id"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, createResp, &created)
	if created.ApiKey.Id == "" {
		t.Fatal("expected CreateApiKey to return an id")
	}
	return created.ApiKey.Id
}

// wsWrite sends a JSON message over the WebSocket.
func wsWrite(t *testing.T, ctx context.Context, conn *websocket.Conn, msg map[string]any) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal ws message: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

// wsRead reads a JSON message from the WebSocket with a timeout.
func wsRead(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal ws message: %v (raw: %s)", err, string(data))
	}
	return msg
}

// ─── Subscription Tests ──────────────────────────────────────────────────────

func TestSubscription_connectAndAck(t *testing.T) {
	// Given: an API exists
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: we connect via WebSocket and send connection_init
	conn, ctx := wsConnect(t, srv, apiID)
	wsWrite(t, ctx, conn, map[string]any{"type": "connection_init"})

	// Then: we receive connection_ack
	ack := wsRead(t, ctx, conn)
	if ack["type"] != "connection_ack" {
		t.Fatalf("expected type=connection_ack, got %q", ack["type"])
	}
	payload, ok := ack["payload"].(map[string]any)
	if !ok {
		t.Fatal("expected payload to be an object")
	}
	if timeout, _ := payload["connectionTimeoutMs"].(float64); timeout != 300000 {
		t.Errorf("expected connectionTimeoutMs=300000, got %v", timeout)
	}
}

func TestSubscription_connectMissingAPIKey(t *testing.T) {
	// Given: an API_KEY-authenticated API exists
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: we connect without realtime authorization headers
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/_overcast/appsync/apis/" + apiID + "/realtime"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, wsURL, nil)

	// Then: the connection is rejected before the WebSocket upgrade
	if err == nil {
		t.Fatal("expected missing x-api-key to reject realtime connection")
	}
}

func TestSubscription_subscribeAndReceive(t *testing.T) {
	// Given: an API with a schema that has Query, Mutation, and Subscription types
	srv := helpers.NewTestServer(t)
	sdl := `
		type Query { getTodo(id: ID!): Todo }
		type Mutation { createTodo(title: String!): Todo }
		type Subscription { onCreateTodo: Todo }
		type Todo { id: ID! title: String! }
	`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create a NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	// Create resolver for Mutation.createTodo — returns a static response.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Mutation/resolvers", map[string]any{
		"fieldName":              "createTodo",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":{"id":"todo-1","title":"Test Todo"}}`,
	}).Body.Close()

	// When: client connects via WebSocket and subscribes to onCreateTodo
	conn, ctx := wsConnect(t, srv, apiID)
	wsWrite(t, ctx, conn, map[string]any{"type": "connection_init"})
	ack := wsRead(t, ctx, conn) // connection_ack
	if ack["type"] != "connection_ack" {
		t.Fatalf("expected connection_ack, got %q", ack["type"])
	}

	// Subscribe using AWS's realtime payload.data shape.
	data, err := json.Marshal(map[string]any{
		"query":     `subscription { onCreateTodo { id title } }`,
		"variables": map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal subscription data: %v", err)
	}
	wsWrite(t, ctx, conn, map[string]any{
		"id":   "sub-1",
		"type": "start",
		"payload": map[string]any{
			"data": string(data),
			"extensions": map[string]any{
				"authorization": map[string]any{"x-api-key": keyID},
			},
		},
	})
	startAck := wsRead(t, ctx, conn) // start_ack
	if startAck["type"] != "start_ack" {
		t.Fatalf("expected start_ack, got %q", startAck["type"])
	}
	if startAck["id"] != "sub-1" {
		t.Fatalf("expected id=sub-1, got %q", startAck["id"])
	}

	// Execute a createTodo mutation via HTTP.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `mutation { createTodo(title: "Test Todo") { id title } }`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the subscription receives the mutation result
	dataMsg := wsRead(t, ctx, conn)
	if dataMsg["type"] != "data" {
		t.Fatalf("expected type=data, got %q", dataMsg["type"])
	}
	if dataMsg["id"] != "sub-1" {
		t.Fatalf("expected id=sub-1, got %q", dataMsg["id"])
	}

	// Parse payload.
	var payload struct {
		Data struct {
			OnCreateTodo struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"onCreateTodo"`
		} `json:"data"`
	}
	payloadBytes, _ := json.Marshal(dataMsg["payload"])
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Data.OnCreateTodo.ID != "todo-1" {
		t.Errorf("expected id=todo-1, got %q", payload.Data.OnCreateTodo.ID)
	}
	if payload.Data.OnCreateTodo.Title != "Test Todo" {
		t.Errorf("expected title='Test Todo', got %q", payload.Data.OnCreateTodo.Title)
	}
}

func TestSubscription_unsubscribe(t *testing.T) {
	// Given: an API with mutation and subscription types
	srv := helpers.NewTestServer(t)
	sdl := `
		type Query { getTodo(id: ID!): Todo }
		type Mutation { createTodo(title: String!): Todo }
		type Subscription { onCreateTodo: Todo }
		type Todo { id: ID! title: String! }
	`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create a NONE data source and resolver.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Mutation/resolvers", map[string]any{
		"fieldName":              "createTodo",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":{"id":"todo-1","title":"Test Todo"}}`,
	}).Body.Close()

	// Connect and subscribe.
	conn, ctx := wsConnect(t, srv, apiID)
	wsWrite(t, ctx, conn, map[string]any{"type": "connection_init"})
	wsRead(t, ctx, conn) // connection_ack

	wsWrite(t, ctx, conn, map[string]any{
		"id":   "sub-1",
		"type": "start",
		"payload": map[string]any{
			"query":     `subscription { onCreateTodo { id title } }`,
			"variables": map[string]any{},
		},
	})
	wsRead(t, ctx, conn) // start_ack

	// When: client sends stop
	wsWrite(t, ctx, conn, map[string]any{
		"id":   "sub-1",
		"type": "stop",
	})

	// Then: server sends complete
	complete := wsRead(t, ctx, conn)
	if complete["type"] != "complete" {
		t.Fatalf("expected type=complete, got %q", complete["type"])
	}
	if complete["id"] != "sub-1" {
		t.Fatalf("expected id=sub-1, got %q", complete["id"])
	}

	// And: executing a mutation does NOT push data to the unsubscribed client
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `mutation { createTodo(title: "After Unsub") { id title } }`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Try to read — should timeout (no message expected).
	readCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, _, err := conn.Read(readCtx)
	if err == nil {
		t.Fatal("expected no message after unsubscribe, but got one")
	}
	// Context deadline exceeded is the expected error (no message available).
}

func TestSubscription_apiNotFound(t *testing.T) {
	// Given: no APIs exist
	srv := helpers.NewTestServer(t)

	// When: attempting to connect to a non-existent API's realtime endpoint
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/_overcast/appsync/apis/nonexistent/realtime"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, wsURL, nil)

	// Then: the connection is rejected
	if err == nil {
		t.Fatal("expected error connecting to non-existent API")
	}
}
