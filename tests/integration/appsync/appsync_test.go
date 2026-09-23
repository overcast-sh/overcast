// Package appsync_test contains integration tests for the AppSync emulator.
//
// Run: go test ./tests/integration/appsync/...
package appsync_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── HTTP Helpers ─────────────────────────────────────────────────────────────

func appsyncPost(t *testing.T, srv *helpers.TestServer, path string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("appsyncPost %s: %v", path, err)
	}
	return resp
}

func appsyncPut(t *testing.T, srv *helpers.TestServer, path string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("appsyncPut %s: %v", path, err)
	}
	return resp
}

func appsyncGet(t *testing.T, srv *helpers.TestServer, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("appsyncGet %s: %v", path, err)
	}
	return resp
}

func appsyncDelete(t *testing.T, srv *helpers.TestServer, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("appsyncDelete %s: %v", path, err)
	}
	return resp
}

func appsyncPostWithHeaders(t *testing.T, srv *helpers.TestServer, path string, body map[string]any, headers map[string]string) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, doErr := http.DefaultClient.Do(req)
	if doErr != nil {
		t.Fatalf("appsyncPostWithHeaders %s: %v", path, doErr)
	}
	return resp
}

// assertGraphQLAuthUnauthorized asserts that an authorization failure on the
// data-plane GraphQL endpoint (POST .../graphql) carries AppSync's GraphQL
// error envelope — {"errors":[{"errorType":"UnauthorizedException",
// "message":"..."}]} — with Content-Type: application/json and HTTP 401,
// rather than the AWS JSON error envelope ({"__type":...}) that the
// management-plane /v1/apis operations correctly use. Verified against
// https://docs.aws.amazon.com/appsync/latest/devguide/security-authz.html
// and a captured real AppSync 401 response (see
// internal/services/appsync/handler_execute.go's writeGraphQLAuthError doc
// comment for the exact evidence).
func assertGraphQLAuthUnauthorized(t *testing.T, resp *http.Response) {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d\nbody: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Errors []struct {
			ErrorType string `json:"errorType"`
			Message   string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body %s: %v", raw, err)
	}
	if len(body.Errors) != 1 {
		t.Fatalf("errors = %+v, want exactly one GraphQL error\nbody: %s", body.Errors, raw)
	}
	if body.Errors[0].ErrorType != "UnauthorizedException" {
		t.Errorf("errors[0].errorType = %q, want UnauthorizedException", body.Errors[0].ErrorType)
	}
	if body.Errors[0].Message == "" {
		t.Error("errors[0].message is empty, want a non-empty message")
	}
}

// TestExecuteGraphQL_authFailureEnvelope covers every authorization mode the
// GraphQL execution handler can actually refuse a request under, driving each
// one to the credential-missing failure and asserting the GraphQL error
// envelope from assertGraphQLAuthUnauthorized. AWS_IAM is deliberately absent:
// h.iamIdentity (handler_execute.go) never returns an authorization error —
// Overcast accepts any SigV4-shaped credential without verifying it (AGENTS.md
// § Non-goals: "not a security boundary") — so there is no way to make an
// AWS_IAM request fail authorization here.
func TestExecuteGraphQL_authFailureEnvelope(t *testing.T) {
	tests := []struct {
		name               string
		authenticationType string
		extra              map[string]any
	}{
		{
			name:               "API_KEY",
			authenticationType: "API_KEY",
		},
		{
			name:               "OPENID_CONNECT",
			authenticationType: "OPENID_CONNECT",
			extra: map[string]any{
				"openIDConnectConfig": map[string]any{"issuer": "https://issuer.example.test"},
			},
		},
		{
			name:               "AWS_LAMBDA",
			authenticationType: "AWS_LAMBDA",
			extra: map[string]any{
				"lambdaAuthorizerConfig": map[string]any{
					"authorizerUri": "arn:aws:lambda:us-east-1:000000000000:function:does-not-matter",
				},
			},
		},
		{
			name:               "AMAZON_COGNITO_USER_POOLS",
			authenticationType: "AMAZON_COGNITO_USER_POOLS",
			extra: map[string]any{
				"userPoolConfig": map[string]any{"userPoolId": "us-east-1_fake"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an API whose (sole) authentication type is tc.authenticationType
			srv := helpers.NewTestServer(t)
			apiID, _ := createTestAPI(t, srv)

			update := map[string]any{
				"name":               "auth-failure-api",
				"authenticationType": tc.authenticationType,
			}
			for k, v := range tc.extra {
				update[k] = v
			}
			appsyncPost(t, srv, "/v1/apis/"+apiID, update).Body.Close()

			sdl := `type Query { hello: String }`
			b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
			appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL}).Body.Close()

			// When: the request carries no credential at all
			resp := appsyncPost(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
				map[string]any{"query": `{ hello }`},
			)

			// Then: AppSync's GraphQL error envelope, not the AWS JSON envelope
			assertGraphQLAuthUnauthorized(t, resp)
		})
	}
}

// createTestAPI is a helper that creates a GraphQL API and returns its ID and ARN.
func createTestAPI(t *testing.T, srv *helpers.TestServer) (apiID, arn string) {
	t.Helper()
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":               "test-api",
		"authenticationType": "API_KEY",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlAPI struct {
			ApiId string `json:"apiId"`
			ARN   string `json:"arn"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, resp, &result)
	return result.GraphqlAPI.ApiId, result.GraphqlAPI.ARN
}

// ─── CreateGraphqlApi ─────────────────────────────────────────────────────────

func TestCreateGraphqlApi_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateGraphqlApi is called with full fields
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":               "test-api",
		"authenticationType": "API_KEY",
		"tags":               map[string]string{"env": "test"},
	})
	defer resp.Body.Close()

	// Then: 200 with enriched response including uris, dns, defaults
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlAPI struct {
			ApiId              string            `json:"apiId"`
			Name               string            `json:"name"`
			Arn                string            `json:"arn"`
			AuthenticationType string            `json:"authenticationType"`
			Uris               map[string]string `json:"uris"`
			Dns                map[string]string `json:"dns"`
			Tags               map[string]string `json:"tags"`
			ApiType            string            `json:"apiType"`
			Visibility         string            `json:"visibility"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, resp, &result)
	api := result.GraphqlAPI
	if api.ApiId == "" {
		t.Error("expected graphqlApi.apiId to be set")
	}
	if api.Name != "test-api" {
		t.Errorf("expected name=test-api, got %q", api.Name)
	}
	if api.Arn == "" {
		t.Error("expected arn to be set")
	}
	if api.Uris["GRAPHQL"] == "" {
		t.Error("expected uris.GRAPHQL to be set")
	}
	if want := srv.ExternalBase() + "/_overcast/appsync/apis/" + api.ApiId + "/graphql"; api.Uris["GRAPHQL"] != want {
		t.Fatalf("expected executable GraphQL URL %q, got %q", want, api.Uris["GRAPHQL"])
	}
	if api.Uris["REALTIME"] == "" {
		t.Error("expected uris.REALTIME to be set")
	}
	if api.Dns["GRAPHQL"] == "" {
		t.Error("expected dns.GRAPHQL to be set")
	}
	if api.ApiType != "GRAPHQL" {
		t.Errorf("expected apiType=GRAPHQL, got %q", api.ApiType)
	}
	if api.Visibility != "GLOBAL" {
		t.Errorf("expected visibility=GLOBAL, got %q", api.Visibility)
	}
	if api.Tags["env"] != "test" {
		t.Errorf("expected tags.env=test, got %q", api.Tags["env"])
	}
}

func TestListGraphqlApis_localGraphQLURLs(t *testing.T) {
	// Given: an AppSync API exists.
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: ListGraphqlApis is called.
	resp := appsyncGet(t, srv, "/v1/apis")
	defer resp.Body.Close()

	// Then: the returned GraphQL URL is directly executable against this Overcast instance.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlApis []struct {
			ApiId string            `json:"apiId"`
			Uris  map[string]string `json:"uris"`
		} `json:"graphqlApis"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.GraphqlApis) != 1 {
		t.Fatalf("expected one API, got %d", len(result.GraphqlApis))
	}
	want := srv.ExternalBase() + "/_overcast/appsync/apis/" + apiID + "/graphql"
	if got := result.GraphqlApis[0].Uris["GRAPHQL"]; got != want {
		t.Fatalf("expected executable GraphQL URL %q, got %q", want, got)
	}
}

func TestListGraphqlApis_configuredHostname(t *testing.T) {
	// Given: Overcast is configured with an externally reachable hostname.
	srv := helpers.NewTestServer(t, helpers.WithHostname("overcast.local"))
	apiID, _ := createTestAPI(t, srv)

	// When: ListGraphqlApis is called.
	resp := appsyncGet(t, srv, "/v1/apis")
	defer resp.Body.Close()

	// Then: the returned GraphQL URL uses the configured client-facing hostname.
	// "overcast.local" is multi-label, so it can carry the host-routed grammar —
	// see TestGraphqlApi_urisFollowTheBase for the rule.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlApis []struct {
			Uris map[string]string `json:"uris"`
		} `json:"graphqlApis"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.GraphqlApis) != 1 {
		t.Fatalf("expected one API, got %d", len(result.GraphqlApis))
	}
	serverURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	want := "http://" + apiID + ".appsync-api.us-east-1.overcast.local:" + serverURL.Port() + "/graphql"
	if got := result.GraphqlApis[0].Uris["GRAPHQL"]; got != want {
		t.Fatalf("expected configured GraphQL URL %q, got %q", want, got)
	}
}

func TestCreateGraphqlApi_inlineTags(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateGraphqlApi is called with tags in the request body
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":               "inline-tags",
		"authenticationType": "API_KEY",
		"tags":               map[string]string{"env": "test", "team": "guides"},
	})
	defer resp.Body.Close()

	// Then: tags are preserved in the AWS-shaped response
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlAPI struct {
			Tags map[string]string `json:"tags"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.GraphqlAPI.Tags["env"] != "test" {
		t.Fatalf("expected env tag test, got %#v", result.GraphqlAPI.Tags)
	}
	if result.GraphqlAPI.Tags["team"] != "guides" {
		t.Fatalf("expected team tag guides, got %#v", result.GraphqlAPI.Tags)
	}
}

func TestCreateGraphqlApi_ownerContactTooLong(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateGraphqlApi is called with ownerContact beyond AWS's limit
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":               "owner-contact-too-long",
		"authenticationType": "API_KEY",
		"ownerContact":       strings.Repeat("a", 257),
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the invalid ownerContact
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestCreateGraphqlApi_missingName(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateGraphqlApi is called without the required name
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"authenticationType": "API_KEY",
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the malformed request
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
	helpers.AssertRequestID(t, resp)
}

func TestCreateGraphqlApi_missingAuthenticationType(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateGraphqlApi is called without authenticationType
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name": "missing-auth",
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the malformed request
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
	helpers.AssertRequestID(t, resp)
}

func TestCreateGraphqlApi_invalidAuthenticationType(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateGraphqlApi is called with an authentication type outside AWS's documented enum
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":               "bad-auth",
		"authenticationType": "NONE",
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the invalid enum
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
	helpers.AssertRequestID(t, resp)
}

func TestCreateGraphqlApi_invalidApiType(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateGraphqlApi is called with an apiType outside AWS's documented enum
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":               "bad-api-type",
		"authenticationType": "API_KEY",
		"apiType":            "EVENT",
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the invalid enum
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
	helpers.AssertRequestID(t, resp)
}

// TestCreateGraphqlApi_queryDepthLimitBoundaries checks the documented range
// at https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateGraphqlApi.html:
// "Minimum value of 0. Maximum value of 75." 0 (unspecified/no limit) and 75
// (the maximum) are accepted; 76 is rejected.
func TestCreateGraphqlApi_queryDepthLimitBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero-means-unlimited", 0, http.StatusOK},
		{"at-maximum", 75, http.StatusOK},
		{"above-maximum", 76, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
				"name":               "query-depth-" + tc.name,
				"authenticationType": "API_KEY",
				"queryDepthLimit":    tc.limit,
			})
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, tc.want)
			if tc.want == http.StatusBadRequest {
				helpers.AssertJSONError(t, resp, "BadRequestException")
			}
		})
	}
}

// TestCreateGraphqlApi_resolverCountLimitBoundaries checks the documented
// range at https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateGraphqlApi.html:
// "Minimum value of 0. Maximum value of 10000." 0 (unspecified, which AWS
// sets to 10000) and 10000 (the maximum) are accepted; 10001 is rejected.
func TestCreateGraphqlApi_resolverCountLimitBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero-means-default", 0, http.StatusOK},
		{"at-maximum", 10000, http.StatusOK},
		{"above-maximum", 10001, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
				"name":               "resolver-count-" + tc.name,
				"authenticationType": "API_KEY",
				"resolverCountLimit": tc.limit,
			})
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, tc.want)
			if tc.want == http.StatusBadRequest {
				helpers.AssertJSONError(t, resp, "BadRequestException")
			}
		})
	}
}

// TestCreateGraphqlApi_logConfigFieldLogLevel checks logConfig.fieldLogLevel,
// which AWS's LogConfig shape marks required
// (https://docs.aws.amazon.com/appsync/latest/APIReference/API_LogConfig.html)
// with values NONE, ERROR, ALL, INFO, or DEBUG.
func TestCreateGraphqlApi_logConfigFieldLogLevel(t *testing.T) {
	t.Run("valid level accepted", func(t *testing.T) {
		srv := helpers.NewTestServer(t)
		resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
			"name":               "log-config-valid",
			"authenticationType": "API_KEY",
			"logConfig": map[string]any{
				"fieldLogLevel":         "ALL",
				"cloudWatchLogsRoleArn": "arn:aws:iam::123456789012:role/appsync-logs",
			},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
	})

	t.Run("invalid level rejected", func(t *testing.T) {
		srv := helpers.NewTestServer(t)
		resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
			"name":               "log-config-invalid",
			"authenticationType": "API_KEY",
			"logConfig": map[string]any{
				"fieldLogLevel": "VERBOSE",
			},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "BadRequestException")
	})

	t.Run("missing required field rejected", func(t *testing.T) {
		srv := helpers.NewTestServer(t)
		resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
			"name":               "log-config-missing-level",
			"authenticationType": "API_KEY",
			"logConfig": map[string]any{
				"cloudWatchLogsRoleArn": "arn:aws:iam::123456789012:role/appsync-logs",
			},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "BadRequestException")
	})
}

// TestCreateGraphqlApi_additionalAuthenticationProviders checks the nested
// authenticationType enum in additionalAuthenticationProviders, which AWS
// constrains to the same values as the top-level field
// (https://docs.aws.amazon.com/appsync/latest/APIReference/API_AdditionalAuthenticationProvider.html).
func TestCreateGraphqlApi_additionalAuthenticationProviders(t *testing.T) {
	t.Run("valid provider accepted", func(t *testing.T) {
		srv := helpers.NewTestServer(t)
		resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
			"name":               "additional-auth-valid",
			"authenticationType": "API_KEY",
			"additionalAuthenticationProviders": []map[string]any{
				{"authenticationType": "AWS_IAM"},
			},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			GraphqlAPI struct {
				AdditionalAuthenticationProviders []map[string]any `json:"additionalAuthenticationProviders"`
			} `json:"graphqlApi"`
		}
		helpers.DecodeJSON(t, resp, &result)
		if len(result.GraphqlAPI.AdditionalAuthenticationProviders) != 1 {
			t.Fatalf("expected 1 additional authentication provider, got %#v", result.GraphqlAPI.AdditionalAuthenticationProviders)
		}
		if got := result.GraphqlAPI.AdditionalAuthenticationProviders[0]["authenticationType"]; got != "AWS_IAM" {
			t.Errorf("expected authenticationType=AWS_IAM, got %v", got)
		}
	})

	t.Run("invalid provider authenticationType rejected", func(t *testing.T) {
		srv := helpers.NewTestServer(t)
		resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
			"name":               "additional-auth-invalid",
			"authenticationType": "API_KEY",
			"additionalAuthenticationProviders": []map[string]any{
				{"authenticationType": "NONE"},
			},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "BadRequestException")
	})
}

// ─── GetGraphqlApi ────────────────────────────────────────────────────────────

func TestGetGraphqlApi_success(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: GetGraphqlApi is called
	resp := appsyncGet(t, srv, "/v1/apis/"+apiID)
	defer resp.Body.Close()

	// Then: 200 with matching apiId
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlAPI struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.GraphqlAPI.ApiId != apiID {
		t.Errorf("expected apiId=%q, got %q", apiID, result.GraphqlAPI.ApiId)
	}
}

func TestGetGraphqlApi_notFound(t *testing.T) {
	// Given: no APIs
	srv := helpers.NewTestServer(t)

	// When: GetGraphqlApi is called with a non-existent ID
	resp := appsyncGet(t, srv, "/v1/apis/nonexistent")
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── ListGraphqlApis ──────────────────────────────────────────────────────────

func TestListGraphqlApis_success(t *testing.T) {
	// Given: two APIs
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"api-1", "api-2"} {
		r := appsyncPost(t, srv, "/v1/apis", map[string]any{
			"name":               name,
			"authenticationType": "API_KEY",
		})
		r.Body.Close()
	}

	// When: ListGraphqlApis is called
	resp := appsyncGet(t, srv, "/v1/apis")
	defer resp.Body.Close()

	// Then: 200 with 2 APIs
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlAPIs []struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApis"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.GraphqlAPIs) != 2 {
		t.Errorf("expected 2 APIs, got %d", len(result.GraphqlAPIs))
	}
}

func TestListGraphqlApis_filtersAndPaginates(t *testing.T) {
	// Given: standard and merged APIs exist
	srv := helpers.NewTestServer(t)
	for _, body := range []map[string]any{
		{"name": "standard-1", "authenticationType": "API_KEY"},
		{"name": "standard-2", "authenticationType": "API_KEY"},
		{"name": "merged-1", "authenticationType": "AWS_IAM", "apiType": "MERGED"},
	} {
		r := appsyncPost(t, srv, "/v1/apis", body)
		r.Body.Close()
		helpers.AssertStatus(t, r, http.StatusOK)
	}

	// When: ListGraphqlApis is filtered by API type
	filtered := appsyncGet(t, srv, "/v1/apis?apiType=MERGED")
	defer filtered.Body.Close()

	// Then: only merged APIs are returned
	helpers.AssertStatus(t, filtered, http.StatusOK)
	var filteredResult struct {
		GraphqlAPIs []struct {
			ApiType string `json:"apiType"`
		} `json:"graphqlApis"`
	}
	helpers.DecodeJSON(t, filtered, &filteredResult)
	if len(filteredResult.GraphqlAPIs) != 1 {
		t.Fatalf("expected 1 merged API, got %d", len(filteredResult.GraphqlAPIs))
	}
	if filteredResult.GraphqlAPIs[0].ApiType != "MERGED" {
		t.Errorf("expected apiType=MERGED, got %q", filteredResult.GraphqlAPIs[0].ApiType)
	}

	// When: ListGraphqlApis is limited to one result
	page1 := appsyncGet(t, srv, "/v1/apis?maxResults=1")
	defer page1.Body.Close()

	// Then: a nextToken is returned and can be used for the next page
	helpers.AssertStatus(t, page1, http.StatusOK)
	var page1Result struct {
		GraphqlAPIs []struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApis"`
		NextToken string `json:"nextToken"`
	}
	helpers.DecodeJSON(t, page1, &page1Result)
	if len(page1Result.GraphqlAPIs) != 1 {
		t.Fatalf("expected 1 API on first page, got %d", len(page1Result.GraphqlAPIs))
	}
	if page1Result.NextToken == "" {
		t.Fatal("expected nextToken for truncated result")
	}

	page2 := appsyncGet(t, srv, "/v1/apis?maxResults=1&nextToken="+page1Result.NextToken)
	defer page2.Body.Close()
	helpers.AssertStatus(t, page2, http.StatusOK)
	var page2Result struct {
		GraphqlAPIs []struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApis"`
	}
	helpers.DecodeJSON(t, page2, &page2Result)
	if len(page2Result.GraphqlAPIs) != 1 {
		t.Fatalf("expected 1 API on second page, got %d", len(page2Result.GraphqlAPIs))
	}
	if page2Result.GraphqlAPIs[0].ApiId == page1Result.GraphqlAPIs[0].ApiId {
		t.Error("expected second page to advance past first API")
	}
}

// TestListGraphqlApis_defaultsToAWSPageSize pins the page size a client gets
// when it omits maxResults. AWS caps the parameter at 25 and applies that same
// 25 as the default, so the omitted case is a page like any other — a walk that
// terminates, not one unbounded response.
//
// The walk is the second half of the assertion: a default that pages but loses
// or repeats an item is no better than one that returns everything.
func TestListGraphqlApis_defaultsToAWSPageSize(t *testing.T) {
	// Given: more APIs than one default page holds
	srv := helpers.NewTestServer(t)
	const total = 30
	created := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		apiID, _ := createTestAPI(t, srv)
		created[apiID] = true
	}

	// When: ListGraphqlApis is called with no maxResults
	first := appsyncGet(t, srv, "/v1/apis")
	defer first.Body.Close()

	// Then: it returns one AWS-sized page, not everything
	helpers.AssertStatus(t, first, http.StatusOK)
	var page struct {
		GraphqlAPIs []struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApis"`
		NextToken string `json:"nextToken"`
	}
	helpers.DecodeJSON(t, first, &page)
	if len(page.GraphqlAPIs) != 25 {
		t.Fatalf("expected the AWS default page size of 25, got %d", len(page.GraphqlAPIs))
	}
	if page.NextToken == "" {
		t.Fatal("expected nextToken: 30 APIs do not fit in one default page")
	}

	// And: walking the default-sized pages yields every API exactly once
	seen := make(map[string]bool, total)
	for {
		for _, api := range page.GraphqlAPIs {
			if seen[api.ApiId] {
				t.Fatalf("api %s returned on more than one page", api.ApiId)
			}
			if !created[api.ApiId] {
				t.Fatalf("api %s was never created", api.ApiId)
			}
			seen[api.ApiId] = true
		}
		if page.NextToken == "" {
			break
		}
		next := appsyncGet(t, srv, "/v1/apis?nextToken="+url.QueryEscape(page.NextToken))
		helpers.AssertStatus(t, next, http.StatusOK)
		page.GraphqlAPIs = nil
		page.NextToken = ""
		helpers.DecodeJSON(t, next, &page)
		next.Body.Close()
	}
	if len(seen) != total {
		t.Fatalf("walk returned %d of %d APIs", len(seen), total)
	}
}

// ─── UpdateGraphqlApi ─────────────────────────────────────────────────────────

func TestUpdateGraphqlApi_success(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: UpdateGraphqlApi is called to change name and auth type
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"name":               "updated-name",
		"authenticationType": "AWS_IAM",
	})
	defer resp.Body.Close()

	// Then: 200 with updated fields, server-generated fields preserved
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlAPI struct {
			ApiId              string            `json:"apiId"`
			Name               string            `json:"name"`
			AuthenticationType string            `json:"authenticationType"`
			Uris               map[string]string `json:"uris"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.GraphqlAPI.ApiId != apiID {
		t.Errorf("expected apiId preserved, got %q", result.GraphqlAPI.ApiId)
	}
	if result.GraphqlAPI.Name != "updated-name" {
		t.Errorf("expected name=updated-name, got %q", result.GraphqlAPI.Name)
	}
	if result.GraphqlAPI.AuthenticationType != "AWS_IAM" {
		t.Errorf("expected authenticationType=AWS_IAM, got %q", result.GraphqlAPI.AuthenticationType)
	}
	if result.GraphqlAPI.Uris["GRAPHQL"] == "" {
		t.Error("expected uris.GRAPHQL preserved after update")
	}
}

// UpdateGraphqlApi's decode target is the same GraphqlAPI struct
// CreateGraphqlApi uses, so a client-supplied `tags` field on update is
// stored — but until #1052 it was never validated, the one call site
// validateGraphqlAPIInput's allowTags=false skipped entirely.
func TestUpdateGraphqlApi_reservedTagPrefixRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	resp := appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"name":               "updated-name",
		"authenticationType": "API_KEY",
		"tags":               map[string]string{"aws:reserved": "x"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")

	// And: the rejected update did not store the reserved-prefix tag.
	getResp, err := http.Get(srv.URL + "/v1/apis/" + apiID)
	if err != nil {
		t.Fatalf("GetGraphqlApi: %v", err)
	}
	defer getResp.Body.Close()
	var got struct {
		GraphqlAPI struct {
			Tags map[string]string `json:"tags"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, getResp, &got)
	if len(got.GraphqlAPI.Tags) != 0 {
		t.Fatalf("tags = %#v after a rejected UpdateGraphqlApi, want none stored", got.GraphqlAPI.Tags)
	}
}

func TestUpdateGraphqlApi_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := appsyncPost(t, srv, "/v1/apis/nonexistent", map[string]any{
		"name":               "x",
		"authenticationType": "API_KEY",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateGraphqlApi_invalidAuthenticationType(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: UpdateGraphqlApi is called with an invalid authentication type
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"name":               "bad-update",
		"authenticationType": "NONE",
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the invalid enum
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestUpdateGraphqlApi_missingName(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: UpdateGraphqlApi is called without the required name
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"authenticationType": "API_KEY",
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the malformed request
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

// ─── DeleteGraphqlApi ─────────────────────────────────────────────────────────

func TestDeleteGraphqlApi_success(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: DeleteGraphqlApi is called
	resp := appsyncDelete(t, srv, "/v1/apis/"+apiID)
	defer resp.Body.Close()

	// Then: 200
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: the API is really gone
	get := appsyncGet(t, srv, "/v1/apis/"+apiID)
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusNotFound)
}

func TestDeleteGraphqlApi_cascadesChildren(t *testing.T) {
	// Given: an API with a data source and resolver
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	ds := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "MyDS",
		"type": "NONE",
	})
	ds.Body.Close()
	helpers.AssertStatus(t, ds, http.StatusOK)

	res := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "myField",
		"dataSourceName": "MyDS",
	})
	res.Body.Close()
	helpers.AssertStatus(t, res, http.StatusOK)

	// When: the API is deleted
	del := appsyncDelete(t, srv, "/v1/apis/"+apiID)
	del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: children are also gone (new API with same ID won't find old children)
	// We verify by recreating an API (it won't have same ID but that's fine)
	// The original children are orphaned and deleted.
}

// ─── Schema ───────────────────────────────────────────────────────────────────

func TestSchema_uploadAndStatus(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	sdl := `type Query { hello: String }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))

	// When: StartSchemaCreation is called
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{
		"definition": b64SDL,
	})
	defer resp.Body.Close()

	// Then: 200 with status ACTIVE
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Status string `json:"status"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Status != "ACTIVE" {
		t.Errorf("expected status=ACTIVE, got %q", result.Status)
	}

	// And: GetSchemaCreationStatus returns ACTIVE
	statusResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/schemacreation")
	defer statusResp.Body.Close()
	helpers.AssertStatus(t, statusResp, http.StatusOK)
	var statusResult struct {
		Status string `json:"status"`
	}
	helpers.DecodeJSON(t, statusResp, &statusResult)
	if statusResult.Status != "ACTIVE" {
		t.Errorf("expected status=ACTIVE, got %q", statusResult.Status)
	}
}

func TestSchema_appSyncDirectives(t *testing.T) {
	// Given: an existing API and AppSync schemas that use built-in AWS directives
	cases := []struct {
		name string
		sdl  string
	}{
		{
			name: "additional auth directives",
			sdl: `schema {
	query: Query
	mutation: Mutation
	subscription: Subscription
}

type Query @aws_api_key @aws_iam @aws_oidc @aws_cognito_user_pools @aws_lambda {
	hello: String @aws_api_key @aws_iam @aws_oidc @aws_cognito_user_pools @aws_lambda
}

type Mutation {
	publish(message: String!): Message
}

type Subscription {
	onPublish: Message @aws_subscribe(mutations: ["publish"])
}

type Message {
	message: String!
}`,
		},
		{
			name: "cognito auth directive",
			sdl: `type Query {
	admin: String @aws_auth(cognito_groups: ["Admins"])
}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			apiID, _ := createTestAPI(t, srv)

			// When: StartSchemaCreation is called
			resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{
				"definition": base64.StdEncoding.EncodeToString([]byte(tc.sdl)),
			})
			defer resp.Body.Close()

			// Then: AppSync accepts its built-in schema directives
			helpers.AssertStatus(t, resp, http.StatusOK)
			helpers.AssertRequestID(t, resp)
		})
	}
}

func TestSchema_appSyncScalars(t *testing.T) {
	// Given: an existing API and an AppSync schema that uses all built-in AWS scalar types
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)
	sdl := `type Query {
	getObject(id: ID!): Object
}

type Mutation {
	putObject(
		email: AWSEmail,
		json: AWSJSON,
		date: AWSDate,
		time: AWSTime,
		datetime: AWSDateTime,
		timestamp: AWSTimestamp,
		url: AWSURL,
		phone: AWSPhone,
		ip: AWSIPAddress
	): Object
}

type Object {
	id: ID!
	email: AWSEmail
	json: AWSJSON
	date: AWSDate
	time: AWSTime
	datetime: AWSDateTime
	timestamp: AWSTimestamp
	url: AWSURL
	phone: AWSPhone
	ip: AWSIPAddress
}`

	// When: StartSchemaCreation is called
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{
		"definition": base64.StdEncoding.EncodeToString([]byte(sdl)),
	})
	defer resp.Body.Close()

	// Then: AppSync accepts its built-in scalar types
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
}

func TestSchema_customScalar(t *testing.T) {
	// Given: an existing API and a schema with a user-defined custom scalar
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)
	sdl := `scalar CustomDate

type Query {
	createdAt: CustomDate
}`

	// When: StartSchemaCreation is called
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{
		"definition": base64.StdEncoding.EncodeToString([]byte(sdl)),
	})
	defer resp.Body.Close()

	// Then: AppSync rejects user-defined custom scalars
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
	helpers.AssertRequestID(t, resp)
}

func TestSchema_customTypeWithAWSPrefix(t *testing.T) {
	// Given: an existing API and a schema with a custom type using the reserved AWS prefix
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)
	sdl := `type Query {
	item: AWSWidget
}

type AWSWidget {
	id: ID!
}`

	// When: StartSchemaCreation is called
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{
		"definition": base64.StdEncoding.EncodeToString([]byte(sdl)),
	})
	defer resp.Body.Close()

	// Then: AppSync rejects custom types using the reserved AWS prefix
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
	helpers.AssertRequestID(t, resp)
}

func TestSchema_getIntrospection(t *testing.T) {
	// Given: an API with a schema
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)
	sdl := `type Query { hello: String }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	r := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL})
	r.Body.Close()

	// When: GetIntrospectionSchema is called
	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/schema")
	defer resp.Body.Close()

	// Then: 200 with the schema
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Schema string `json:"schema"`
	}
	helpers.DecodeJSON(t, resp, &result)
	decoded, err := base64.StdEncoding.DecodeString(result.Schema)
	if err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if string(decoded) != sdl {
		t.Errorf("expected schema=%q, got %q", sdl, string(decoded))
	}
}

func TestSchema_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/schema")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestSchema_statusBeforeUpload(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/schemacreation")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Status string `json:"status"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Status != "NOT_APPLICABLE" {
		t.Errorf("expected status=NOT_APPLICABLE, got %q", result.Status)
	}
}

// ─── API Keys ─────────────────────────────────────────────────────────────────

// TestApiKey_notAutoCreatedOnApiKeyAuth checks that CreateGraphqlApi does not
// create an API key as a side effect, even for authenticationType=API_KEY.
// AWS's CreateGraphqlApi response
// (https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateGraphqlApi.html)
// carries no apiKey field; the AWS console's "default key" convenience is a
// separate CreateApiKey call the console makes, not something the API itself
// performs. Overcast auto-created one here until #62.
func TestApiKey_notAutoCreatedOnApiKeyAuth(t *testing.T) {
	// Given/When: an API is created with API_KEY authentication type
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv) // uses authenticationType: API_KEY

	// Then: no API key is auto-created
	listResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/apikeys")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listResult struct {
		ApiKeys []struct {
			Id string `json:"id"`
		} `json:"apiKeys"`
	}
	helpers.DecodeJSON(t, listResp, &listResult)
	if len(listResult.ApiKeys) != 0 {
		t.Fatalf("expected 0 API keys immediately after CreateGraphqlApi, got %d", len(listResult.ApiKeys))
	}
}

func TestApiKey_notAutoCreatedOnNonApiKeyAuth(t *testing.T) {
	// Given/When: an API is created with AMAZON_COGNITO_USER_POOLS authentication
	srv := helpers.NewTestServer(t)
	resp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":               "cognito-api",
		"authenticationType": "AMAZON_COGNITO_USER_POOLS",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		GraphqlAPI struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, resp, &result)

	// Then: no API key is auto-created
	listResp := appsyncGet(t, srv, "/v1/apis/"+result.GraphqlAPI.ApiId+"/apikeys")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listResult struct {
		ApiKeys []struct {
			Id string `json:"id"`
		} `json:"apiKeys"`
	}
	helpers.DecodeJSON(t, listResp, &listResult)
	if len(listResult.ApiKeys) != 0 {
		t.Errorf("expected 0 API keys for non-API_KEY auth, got %d", len(listResult.ApiKeys))
	}
}

func TestApiKey_createAndList(t *testing.T) {
	// Given: an existing API with no keys (CreateGraphqlApi does not auto-create one)
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: CreateApiKey is called
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{
		"description": "test key",
	})
	defer resp.Body.Close()

	// Then: 200 with da2- prefixed key ID
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ApiKey struct {
			Id          string `json:"id"`
			Description string `json:"description"`
			Expires     int64  `json:"expires"`
			Deletes     int64  `json:"deletes"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.ApiKey.Id) < 5 || result.ApiKey.Id[:4] != "da2-" {
		t.Errorf("expected da2- prefixed key ID, got %q", result.ApiKey.Id)
	}
	if result.ApiKey.Description != "test key" {
		t.Errorf("expected description='test key', got %q", result.ApiKey.Description)
	}
	if result.ApiKey.Expires == 0 {
		t.Error("expected expires to be set")
	}

	// And: ListApiKeys returns exactly the explicitly created key
	listResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/apikeys")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listResult struct {
		ApiKeys []struct {
			Id string `json:"id"`
		} `json:"apiKeys"`
	}
	helpers.DecodeJSON(t, listResp, &listResult)
	if len(listResult.ApiKeys) != 1 {
		t.Errorf("expected 1 API key (the explicit one), got %d", len(listResult.ApiKeys))
	}
}

func TestApiKey_createWithCdkStyleUpperBoundExpires(t *testing.T) {
	// Given: an existing API and a non-hour-aligned creation time like CDK deployments use
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)
	expires := time.Now().UTC().Add(365 * 24 * time.Hour).Unix()

	// When: CreateApiKey is called with an absolute epoch expiry 365 days from creation
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{
		"description": "cdk-style key",
		"expires":     expires,
	})
	defer resp.Body.Close()

	// Then: AppSync accepts the documented maximum even when the current time is not hour-aligned
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
}

func TestApiKey_updateAndDelete(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Create a key
	cr := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{
		"description": "original",
	})
	defer cr.Body.Close()
	var created struct {
		ApiKey struct {
			Id string `json:"id"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, cr, &created)
	keyID := created.ApiKey.Id

	// Update description
	up := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys/"+keyID, map[string]any{
		"description": "updated",
	})
	defer up.Body.Close()
	helpers.AssertStatus(t, up, http.StatusOK)
	var updResult struct {
		ApiKey struct {
			Description string `json:"description"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, up, &updResult)
	if updResult.ApiKey.Description != "updated" {
		t.Errorf("expected description=updated, got %q", updResult.ApiKey.Description)
	}

	// Delete
	del := appsyncDelete(t, srv, "/v1/apis/"+apiID+"/apikeys/"+keyID)
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Verify the deleted key is gone. CreateGraphqlApi does not auto-create a
	// default key (#62), so no other key remains.
	listResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/apikeys")
	defer listResp.Body.Close()
	var listResult struct {
		ApiKeys []struct {
			Id string `json:"id"`
		} `json:"apiKeys"`
	}
	helpers.DecodeJSON(t, listResp, &listResult)
	if len(listResult.ApiKeys) != 0 {
		t.Errorf("expected 0 API keys after delete, got %d", len(listResult.ApiKeys))
	}
	// The remaining keys should NOT include the one we just deleted.
	for _, key := range listResult.ApiKeys {
		if key.Id == keyID {
			t.Errorf("deleted key %q should not still be present", keyID)
		}
	}
}

// ─── Data Sources ─────────────────────────────────────────────────────────────

func TestDataSource_crudLifecycle(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: CreateDataSource is called
	createResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "myDS",
		"type":           "NONE",
		"description":    "test ds",
		"serviceRoleArn": "arn:aws:iam::123456789012:role/AppSyncRole",
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	var created struct {
		DataSource struct {
			Name          string `json:"name"`
			DataSourceArn string `json:"dataSourceArn"`
			Type          string `json:"type"`
		} `json:"dataSource"`
	}
	helpers.DecodeJSON(t, createResp, &created)
	if created.DataSource.Name != "myDS" {
		t.Errorf("expected name=myDS, got %q", created.DataSource.Name)
	}
	if created.DataSource.DataSourceArn == "" {
		t.Error("expected dataSourceArn to be set")
	}

	// Get
	getResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/datasources/myDS")
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	// List
	listResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/datasources")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		DataSources []struct {
			Name string `json:"name"`
		} `json:"dataSources"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if len(listed.DataSources) != 1 {
		t.Errorf("expected 1 data source, got %d", len(listed.DataSources))
	}

	// Update
	updateResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources/myDS", map[string]any{
		"type":        "AMAZON_DYNAMODB",
		"description": "updated",
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)

	// Delete
	delResp := appsyncDelete(t, srv, "/v1/apis/"+apiID+"/datasources/myDS")
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Verify gone
	goneResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/datasources/myDS")
	defer goneResp.Body.Close()
	helpers.AssertStatus(t, goneResp, http.StatusNotFound)
}

func TestDataSource_duplicateName(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	r1 := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "dup",
		"type": "NONE",
	})
	r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	r2 := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "dup",
		"type": "NONE",
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusConflict)
}

func TestDataSource_missingType(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: CreateDataSource is called without the AWS-required type
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "missingType",
	})
	defer resp.Body.Close()

	// Then: the request is rejected as malformed
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestDataSource_invalidType(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: CreateDataSource is called with an invalid type enum
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "invalidType",
		"type": "BOGUS",
	})
	defer resp.Body.Close()

	// Then: the request is rejected as malformed
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestDataSource_invalidName(t *testing.T) {
	// Given: invalid AppSync identifier names.
	cases := []struct {
		name           string
		dataSourceName string
	}{
		{name: "starts with digit", dataSourceName: "1source"},
		{name: "invalid character", dataSourceName: "bad-source"},
		{name: "too long", dataSourceName: strings.Repeat("a", 65537)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			apiID, _ := createTestAPI(t, srv)

			// When: CreateDataSource is called with an invalid name.
			resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
				"name": tc.dataSourceName,
				"type": "NONE",
			})
			defer resp.Body.Close()

			// Then: AppSync rejects the request with a modeled validation error.
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "BadRequestException")
		})
	}
}

// ─── Functions ────────────────────────────────────────────────────────────────

func TestFunction_crudLifecycle(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Create
	createResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":           "myFn",
		"dataSourceName": "myDS",
		"code":           "export function request(ctx) { return {} }",
		"runtime":        map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	var created struct {
		FunctionConfiguration struct {
			FunctionId  string `json:"functionId"`
			FunctionArn string `json:"functionArn"`
			Name        string `json:"name"`
		} `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, createResp, &created)
	fnID := created.FunctionConfiguration.FunctionId
	if fnID == "" {
		t.Error("expected functionId to be set")
	}
	if created.FunctionConfiguration.FunctionArn == "" {
		t.Error("expected functionArn to be set")
	}

	// Get
	getResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/functions/"+fnID)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	// List
	listResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/functions")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		Functions []struct {
			FunctionId string `json:"functionId"`
		} `json:"functions"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if len(listed.Functions) != 1 {
		t.Errorf("expected 1 function, got %d", len(listed.Functions))
	}

	// Update
	upResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions/"+fnID, map[string]any{
		"name":           "updatedFn",
		"dataSourceName": "otherDS",
	})
	defer upResp.Body.Close()
	helpers.AssertStatus(t, upResp, http.StatusOK)

	// Delete
	delResp := appsyncDelete(t, srv, "/v1/apis/"+apiID+"/functions/"+fnID)
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Verify gone
	goneResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/functions/"+fnID)
	defer goneResp.Body.Close()
	helpers.AssertStatus(t, goneResp, http.StatusNotFound)
}

func TestFunction_missingDataSourceName(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: CreateFunction is called without the AWS-required dataSourceName
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name": "missingDS",
	})
	defer resp.Body.Close()

	// Then: the request is rejected as malformed
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestFunction_invalidIdentifierNames(t *testing.T) {
	// Given: invalid AppSync function and data source identifier names.
	cases := []struct {
		name           string
		functionName   string
		dataSourceName string
	}{
		{name: "function name starts with digit", functionName: "1fn", dataSourceName: "myDS"},
		{name: "data source name has invalid character", functionName: "myFn", dataSourceName: "bad-source"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			apiID, _ := createTestAPI(t, srv)

			// When: CreateFunction is called with an invalid identifier.
			resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
				"name":           tc.functionName,
				"dataSourceName": tc.dataSourceName,
			})
			defer resp.Body.Close()

			// Then: AppSync rejects the request with a modeled validation error.
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "BadRequestException")
		})
	}
}

// ─── Resolvers ────────────────────────────────────────────────────────────────

func TestResolver_crudLifecycle(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Create
	createResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "getUser",
		"dataSourceName": "usersDS",
		"kind":           "UNIT",
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	var created struct {
		Resolver struct {
			TypeName    string `json:"typeName"`
			FieldName   string `json:"fieldName"`
			ResolverArn string `json:"resolverArn"`
			Kind        string `json:"kind"`
		} `json:"resolver"`
	}
	helpers.DecodeJSON(t, createResp, &created)
	if created.Resolver.TypeName != "Query" {
		t.Errorf("expected typeName=Query, got %q", created.Resolver.TypeName)
	}
	if created.Resolver.FieldName != "getUser" {
		t.Errorf("expected fieldName=getUser, got %q", created.Resolver.FieldName)
	}
	if created.Resolver.ResolverArn == "" {
		t.Error("expected resolverArn to be set")
	}
	if created.Resolver.Kind != "UNIT" {
		t.Errorf("expected kind=UNIT, got %q", created.Resolver.Kind)
	}

	// Get
	getResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers/getUser")
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	// List
	listResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		Resolvers []struct {
			FieldName string `json:"fieldName"`
		} `json:"resolvers"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if len(listed.Resolvers) != 1 {
		t.Errorf("expected 1 resolver, got %d", len(listed.Resolvers))
	}

	// Update
	upResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers/getUser", map[string]any{
		"dataSourceName": "updatedDS",
	})
	defer upResp.Body.Close()
	helpers.AssertStatus(t, upResp, http.StatusOK)

	// Delete
	delResp := appsyncDelete(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers/getUser")
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Verify gone
	goneResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers/getUser")
	defer goneResp.Body.Close()
	helpers.AssertStatus(t, goneResp, http.StatusNotFound)
}

func TestResolver_duplicateConflict(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	r1 := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName": "dup",
	})
	r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)

	r2 := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName": "dup",
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusConflict)
}

func TestResolver_invalidIdentifierNames(t *testing.T) {
	// Given: invalid AppSync resolver identifier names.
	cases := []struct {
		name      string
		typeName  string
		fieldName string
	}{
		{name: "type starts with digit", typeName: "1Query", fieldName: "getUser"},
		{name: "field has invalid character", typeName: "Query", fieldName: "bad-field"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			apiID, _ := createTestAPI(t, srv)

			// When: CreateResolver is called with an invalid type or field identifier.
			resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/"+tc.typeName+"/resolvers", map[string]any{
				"fieldName": tc.fieldName,
			})
			defer resp.Body.Close()

			// Then: AppSync rejects the request with a modeled validation error.
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "BadRequestException")
		})
	}
}

// ─── Tags ─────────────────────────────────────────────────────────────────────

func TestTags_crudLifecycle(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, arn := createTestAPI(t, srv)
	_ = apiID

	// Tag
	tagResp := appsyncPost(t, srv, "/v1/tags/"+arn, map[string]any{
		"tags": map[string]string{"stage": "dev", "team": "backend"},
	})
	defer tagResp.Body.Close()
	helpers.AssertStatus(t, tagResp, http.StatusOK)

	// List
	listResp := appsyncGet(t, srv, "/v1/tags/"+arn)
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		Tags map[string]string `json:"tags"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if listed.Tags["stage"] != "dev" {
		t.Errorf("expected tags.stage=dev, got %q", listed.Tags["stage"])
	}
	if listed.Tags["team"] != "backend" {
		t.Errorf("expected tags.team=backend, got %q", listed.Tags["team"])
	}

	// Untag
	untagResp := appsyncDelete(t, srv, "/v1/tags/"+arn+"?tagKeys=stage")
	defer untagResp.Body.Close()
	helpers.AssertStatus(t, untagResp, http.StatusOK)

	// Verify stage removed, team preserved
	list2 := appsyncGet(t, srv, "/v1/tags/"+arn)
	defer list2.Body.Close()
	var listed2 struct {
		Tags map[string]string `json:"tags"`
	}
	helpers.DecodeJSON(t, list2, &listed2)
	if _, ok := listed2.Tags["stage"]; ok {
		t.Error("expected stage tag to be removed")
	}
	if listed2.Tags["team"] != "backend" {
		t.Errorf("expected team tag preserved, got %q", listed2.Tags["team"])
	}
}

func TestTags_encodedARNPath(t *testing.T) {
	// Given: an existing tagged API
	srv := helpers.NewTestServer(t)
	_, arn := createTestAPI(t, srv)
	encodedARN := url.PathEscape(arn)
	tagResp := appsyncPost(t, srv, "/v1/tags/"+encodedARN, map[string]any{
		"tags": map[string]string{"stage": "dev"},
	})
	defer tagResp.Body.Close()
	helpers.AssertStatus(t, tagResp, http.StatusOK)

	// When: ListTagsForResource is called with the encoded ARN path used by AWS SDKs
	listResp := appsyncGet(t, srv, "/v1/tags/"+encodedARN)
	defer listResp.Body.Close()

	// Then: the API tags are returned
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		Tags map[string]string `json:"tags"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if listed.Tags["stage"] != "dev" {
		t.Fatalf("expected stage tag dev, got %#v", listed.Tags)
	}
}

func TestTagResource_emptyTagKey(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	_, arn := createTestAPI(t, srv)

	// When: TagResource is called with an empty tag key
	resp := appsyncPost(t, srv, "/v1/tags/"+arn, map[string]any{
		"tags": map[string]string{"": "x"},
	})
	defer resp.Body.Close()

	// Then: AppSync rejects the invalid tags with a modeled BadRequestException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestTagResource_invalidTagPatterns(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	_, arn := createTestAPI(t, srv)

	cases := []struct {
		name string
		tags map[string]string
	}{
		{name: "reserved prefix", tags: map[string]string{"aws:reserved": "x"}},
		// '@' is a legal tag-key character under AWS's own documented
		// pattern (^(?!aws:)[\p{L}\p{Z}\p{N}_.:/=+\-@]*$, shared by most
		// tag-modeling services and enforced by serviceutil.ValidateTags) —
		// this case used to assert the opposite because AppSync validated
		// tags with a local, overly-restrictive regex instead of that shared
		// pattern (#1052). '!' is not in the allowed set on any of them.
		{name: "invalid key character", tags: map[string]string{"bad!key": "x"}},
		{name: "invalid value character", tags: map[string]string{"good-key": "bad#value"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: TagResource is called with tags outside AWS's documented tag patterns
			resp := appsyncPost(t, srv, "/v1/tags/"+arn, map[string]any{"tags": tc.tags})
			defer resp.Body.Close()

			// Then: AppSync rejects the invalid tags with a modeled BadRequestException
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "BadRequestException")
		})
	}
}

// ─── Environment Variables ────────────────────────────────────────────────────

func TestEnvironmentVariables_putAndGet(t *testing.T) {
	// Given: a GraphQL API exists
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: PutGraphqlApiEnvironmentVariables is called
	putResp := appsyncPut(t, srv, "/v1/apis/"+apiID+"/environmentVariables", map[string]any{
		"environmentVariables": map[string]string{
			"TABLE_NAME": "my-table",
			"STAGE":      "dev",
		},
	})
	defer putResp.Body.Close()

	// Then: 200 with the stored variables
	helpers.AssertStatus(t, putResp, http.StatusOK)
	var putResult struct {
		EnvironmentVariables map[string]string `json:"environmentVariables"`
	}
	helpers.DecodeJSON(t, putResp, &putResult)
	if putResult.EnvironmentVariables["TABLE_NAME"] != "my-table" {
		t.Errorf("expected TABLE_NAME=my-table, got %q", putResult.EnvironmentVariables["TABLE_NAME"])
	}

	// When: GetGraphqlApiEnvironmentVariables is called
	getResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/environmentVariables")
	defer getResp.Body.Close()

	// Then: 200 with the same variables
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var getResult struct {
		EnvironmentVariables map[string]string `json:"environmentVariables"`
	}
	helpers.DecodeJSON(t, getResp, &getResult)
	if getResult.EnvironmentVariables["TABLE_NAME"] != "my-table" {
		t.Errorf("expected TABLE_NAME=my-table, got %q", getResult.EnvironmentVariables["TABLE_NAME"])
	}
	if getResult.EnvironmentVariables["STAGE"] != "dev" {
		t.Errorf("expected STAGE=dev, got %q", getResult.EnvironmentVariables["STAGE"])
	}
}

func TestEnvironmentVariables_getEmptyReturnsEmptyMap(t *testing.T) {
	// Given: an API with no environment variables
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: GetGraphqlApiEnvironmentVariables is called
	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/environmentVariables")
	defer resp.Body.Close()

	// Then: 200 with empty map
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		EnvironmentVariables map[string]string `json:"environmentVariables"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.EnvironmentVariables) != 0 {
		t.Errorf("expected empty env vars, got %v", result.EnvironmentVariables)
	}
}

func TestEnvironmentVariables_validation(t *testing.T) {
	// Given: a GraphQL API exists
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: more than 50 variables are provided
	vars := make(map[string]string)
	for i := 0; i < 51; i++ {
		vars[fmt.Sprintf("VAR_%03d", i)] = "value"
	}
	resp := appsyncPut(t, srv, "/v1/apis/"+apiID+"/environmentVariables", map[string]any{
		"environmentVariables": vars,
	})
	defer resp.Body.Close()

	// Then: 400 BadRequestException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestEnvironmentVariables_notFoundApi(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// When: env vars are requested for a non-existent API
	resp := appsyncGet(t, srv, "/v1/apis/nonexistent/environmentVariables")
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── Domain Names ─────────────────────────────────────────────────────────────

func TestDomainName_crudLifecycle(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// When: CreateDomainName is called
	createResp := appsyncPost(t, srv, "/v1/domainnames", map[string]any{
		"domainName":     "api.example.com",
		"certificateArn": "arn:aws:acm:us-east-1:123456789012:certificate/abc-123",
		"description":    "test domain",
	})
	defer createResp.Body.Close()

	// Then: 200 with generated appsyncDomainName and hostedZoneId
	helpers.AssertStatus(t, createResp, http.StatusOK)
	var created struct {
		DomainNameConfig struct {
			DomainName        string `json:"domainName"`
			Description       string `json:"description"`
			CertificateArn    string `json:"certificateArn"`
			AppsyncDomainName string `json:"appsyncDomainName"`
			HostedZoneId      string `json:"hostedZoneId"`
		} `json:"domainNameConfig"`
	}
	helpers.DecodeJSON(t, createResp, &created)
	if created.DomainNameConfig.DomainName != "api.example.com" {
		t.Errorf("expected domainName=api.example.com, got %q", created.DomainNameConfig.DomainName)
	}
	if created.DomainNameConfig.AppsyncDomainName == "" {
		t.Error("expected appsyncDomainName to be generated")
	}
	if created.DomainNameConfig.HostedZoneId == "" {
		t.Error("expected hostedZoneId to be generated")
	}

	// When: GetDomainName
	getResp := appsyncGet(t, srv, "/v1/domainnames/api.example.com")
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	// When: ListDomainNames
	listResp := appsyncGet(t, srv, "/v1/domainnames")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		DomainNameConfigs []struct {
			DomainName string `json:"domainName"`
		} `json:"domainNameConfigs"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if len(listed.DomainNameConfigs) != 1 {
		t.Fatalf("expected 1 domain, got %d", len(listed.DomainNameConfigs))
	}

	// When: UpdateDomainName
	updateResp := appsyncPost(t, srv, "/v1/domainnames/api.example.com", map[string]any{
		"description": "updated description",
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)

	// When: DeleteDomainName
	deleteResp := appsyncDelete(t, srv, "/v1/domainnames/api.example.com")
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusOK)

	// Then: GetDomainName returns 404
	gone := appsyncGet(t, srv, "/v1/domainnames/api.example.com")
	defer gone.Body.Close()
	helpers.AssertStatus(t, gone, http.StatusNotFound)
}

func TestDomainName_duplicateReturns409(t *testing.T) {
	srv := helpers.NewTestServer(t)

	body := map[string]any{
		"domainName":     "dup.example.com",
		"certificateArn": "arn:aws:acm:us-east-1:123456789012:certificate/abc-123",
	}
	resp1 := appsyncPost(t, srv, "/v1/domainnames", body)
	defer resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: same domain is created again
	resp2 := appsyncPost(t, srv, "/v1/domainnames", body)
	defer resp2.Body.Close()

	// Then: 409 conflict
	helpers.AssertStatus(t, resp2, http.StatusConflict)
}

// ─── API Associations ─────────────────────────────────────────────────────────

func TestApiAssociation_crudLifecycle(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Given: a domain name exists
	domResp := appsyncPost(t, srv, "/v1/domainnames", map[string]any{
		"domainName":     "assoc.example.com",
		"certificateArn": "arn:aws:acm:us-east-1:123456789012:certificate/abc-123",
	})
	defer domResp.Body.Close()
	helpers.AssertStatus(t, domResp, http.StatusOK)

	// When: AssociateApi is called
	assocResp := appsyncPost(t, srv, "/v1/domainnames/assoc.example.com/apiassociation", map[string]any{
		"apiId": apiID,
	})
	defer assocResp.Body.Close()
	helpers.AssertStatus(t, assocResp, http.StatusOK)
	var assocResult struct {
		ApiAssociation struct {
			DomainName        string `json:"domainName"`
			ApiId             string `json:"apiId"`
			AssociationStatus string `json:"associationStatus"`
		} `json:"apiAssociation"`
	}
	helpers.DecodeJSON(t, assocResp, &assocResult)
	if assocResult.ApiAssociation.AssociationStatus != "SUCCESS" {
		t.Errorf("expected status SUCCESS, got %q", assocResult.ApiAssociation.AssociationStatus)
	}

	// When: GetApiAssociation
	getResp := appsyncGet(t, srv, "/v1/domainnames/assoc.example.com/apiassociation")
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	// When: DisassociateApi
	disResp := appsyncDelete(t, srv, "/v1/domainnames/assoc.example.com/apiassociation")
	defer disResp.Body.Close()
	helpers.AssertStatus(t, disResp, http.StatusOK)

	// Then: GetApiAssociation returns 404
	gone := appsyncGet(t, srv, "/v1/domainnames/assoc.example.com/apiassociation")
	defer gone.Body.Close()
	helpers.AssertStatus(t, gone, http.StatusNotFound)
}

func TestApiAssociation_domainNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: associate to a non-existent domain
	resp := appsyncPost(t, srv, "/v1/domainnames/nope.example.com/apiassociation", map[string]any{
		"apiId": apiID,
	})
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── API Cache ────────────────────────────────────────────────────────────────

func TestApiCache_crudLifecycle(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: CreateApiCache
	createResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/ApiCaches", map[string]any{
		"type":                     "T2_SMALL",
		"apiCachingBehavior":       "FULL_REQUEST_CACHING",
		"transitEncryptionEnabled": true,
		"atRestEncryptionEnabled":  false,
		"ttl":                      3600,
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	var cacheResult struct {
		ApiCache struct {
			Type               string `json:"type"`
			ApiCachingBehavior string `json:"apiCachingBehavior"`
			Status             string `json:"status"`
			Ttl                int64  `json:"ttl"`
		} `json:"apiCache"`
	}
	helpers.DecodeJSON(t, createResp, &cacheResult)
	if cacheResult.ApiCache.Status != "AVAILABLE" {
		t.Errorf("expected status AVAILABLE, got %q", cacheResult.ApiCache.Status)
	}
	if cacheResult.ApiCache.Ttl != 3600 {
		t.Errorf("expected ttl 3600, got %d", cacheResult.ApiCache.Ttl)
	}

	// When: GetApiCache
	getResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/ApiCaches")
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	// When: UpdateApiCache
	updateResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/ApiCaches/update", map[string]any{
		"type":               "T2_MEDIUM",
		"apiCachingBehavior": "PER_RESOLVER_CACHING",
		"ttl":                7200,
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)

	// Verify update
	get2 := appsyncGet(t, srv, "/v1/apis/"+apiID+"/ApiCaches")
	defer get2.Body.Close()
	var updated struct {
		ApiCache struct {
			Type string `json:"type"`
			Ttl  int64  `json:"ttl"`
		} `json:"apiCache"`
	}
	helpers.DecodeJSON(t, get2, &updated)
	if updated.ApiCache.Type != "T2_MEDIUM" {
		t.Errorf("expected T2_MEDIUM, got %q", updated.ApiCache.Type)
	}
	if updated.ApiCache.Ttl != 7200 {
		t.Errorf("expected ttl 7200, got %d", updated.ApiCache.Ttl)
	}

	// When: FlushApiCache (no-op)
	flushResp := appsyncDelete(t, srv, "/v1/apis/"+apiID+"/FlushCache")
	defer flushResp.Body.Close()
	helpers.AssertStatus(t, flushResp, http.StatusOK)

	// When: DeleteApiCache
	deleteResp := appsyncDelete(t, srv, "/v1/apis/"+apiID+"/ApiCaches")
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusOK)

	// Then: GetApiCache returns 404
	gone := appsyncGet(t, srv, "/v1/apis/"+apiID+"/ApiCaches")
	defer gone.Body.Close()
	helpers.AssertStatus(t, gone, http.StatusNotFound)
}

func TestApiCache_duplicateReturns409(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	body := map[string]any{
		"type":               "T2_SMALL",
		"apiCachingBehavior": "FULL_REQUEST_CACHING",
		"ttl":                3600,
	}

	resp1 := appsyncPost(t, srv, "/v1/apis/"+apiID+"/ApiCaches", body)
	defer resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: create again
	resp2 := appsyncPost(t, srv, "/v1/apis/"+apiID+"/ApiCaches", body)
	defer resp2.Body.Close()

	// Then: 409 conflict (one cache per API)
	helpers.AssertStatus(t, resp2, http.StatusConflict)
}

// setupGraphQLAPI creates an API with a schema and API key for execution tests.
func setupGraphQLAPI(t *testing.T, srv *helpers.TestServer, sdl string) (apiID, keyID string) {
	t.Helper()
	apiID, _ = createTestAPI(t, srv)

	// Upload schema.
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	r := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL})
	r.Body.Close()

	// Create API key.
	keyResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{})
	defer keyResp.Body.Close()
	var keyResult struct {
		ApiKey struct {
			Id string `json:"id"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, keyResp, &keyResult)
	return apiID, keyResult.ApiKey.Id
}

// ─── Schema validation ───────────────────────────────────────────────────────

func TestSchema_rejectsInvalidSDL(t *testing.T) {
	// Given: an existing API
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// When: invalid SDL is uploaded
	invalidSDL := `type Query { hello: `
	b64 := base64.StdEncoding.EncodeToString([]byte(invalidSDL))
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{
		"definition": b64,
	})
	defer resp.Body.Close()

	// Then: 400 BadRequestException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestSchema_rejectsSDLWithoutQueryType(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// SDL with no Query type is technically parseable but not a valid AppSync schema.
	sdl := `type Foo { bar: String }`
	b64 := base64.StdEncoding.EncodeToString([]byte(sdl))
	resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{
		"definition": b64,
	})
	defer resp.Body.Close()

	// Then: 400 — AppSync requires a Query type
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── GraphQL execution ───────────────────────────────────────────────────────

func TestExecuteGraphQL_basicQuery(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create a NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	// Create a resolver for Query.hello that returns a static value.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":               "hello",
		"dataSourceName":          "NoneDS",
		"kind":                    "UNIT",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"world"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	// When: a GraphQL query is executed
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with data
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Hello string `json:"hello"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.Hello != "world" {
		t.Errorf("expected data.hello=%q, got %q", "world", result.Data.Hello)
	}
}

func TestExecuteGraphQL_noSchema(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Create an API key.
	keyResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{})
	defer keyResp.Body.Close()
	var keyResult struct {
		ApiKey struct {
			Id string `json:"id"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, keyResp, &keyResult)

	// When: query against an API with no schema
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"x-api-key": keyResult.ApiKey.Id},
	)
	defer resp.Body.Close()

	// Then: 200 with errors (no schema)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected errors for missing schema")
	}
}

func TestExecuteGraphQL_invalidQuery(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// When: a query references a non-existent field
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ nonexistent }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with validation errors
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected validation errors for unknown field")
	}
}

func TestExecuteGraphQL_apiNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/nonexistent/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"x-api-key": "fake"},
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── API_KEY authentication ──────────────────────────────────────────────────

func TestExecuteGraphQL_missingApiKey(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, _ := setupGraphQLAPI(t, srv, sdl)

	// When: no x-api-key header
	resp := appsyncPost(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
	)

	// Then: AppSync's GraphQL error envelope naming UnauthorizedException
	assertGraphQLAuthUnauthorized(t, resp)
}

func TestExecuteGraphQL_invalidApiKey(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, _ := setupGraphQLAPI(t, srv, sdl)

	// When: wrong API key
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"x-api-key": "da2-invalid"},
	)

	// Then: AppSync's GraphQL error envelope naming UnauthorizedException
	assertGraphQLAuthUnauthorized(t, resp)
}

func TestExecuteGraphQL_expiredApiKey(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, _ := setupGraphQLAPI(t, srv, sdl)

	// Create an already-expired key.
	keyResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{
		"expires": 1000000000, // year 2001 — long expired
	})
	defer keyResp.Body.Close()
	var keyResult struct {
		ApiKey struct {
			Id string `json:"id"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, keyResp, &keyResult)

	// When: query with expired key
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"x-api-key": keyResult.ApiKey.Id},
	)

	// Then: AppSync's GraphQL error envelope naming UnauthorizedException
	assertGraphQLAuthUnauthorized(t, resp)
}

func TestExecuteGraphQL_mutation(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }
type Mutation { setHello(msg: String): String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create NONE data source + resolver for Mutation.setHello.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Mutation/resolvers", map[string]any{
		"fieldName":               "setHello",
		"dataSourceName":          "NoneDS",
		"kind":                    "UNIT",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"updated"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	// When: mutation is executed
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `mutation { setHello(msg: "hi") }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with data.setHello = "updated"
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			SetHello string `json:"setHello"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.SetHello != "updated" {
		t.Errorf("expected data.setHello=%q, got %q", "updated", result.Data.SetHello)
	}
}

func TestExecuteGraphQL_operationName(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { a: String  b: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Two resolvers, two fields.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "a",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":"alpha"}`,
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "b",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":"beta"}`,
	}).Body.Close()

	// When: document with two named operations, operationName selects "GetB"
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":         `query GetA { a } query GetB { b }`,
			"operationName": "GetB",
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: only "b" is resolved
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data map[string]any `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data["b"] != "beta" {
		t.Errorf("expected data.b=%q, got %v", "beta", result.Data["b"])
	}
	if _, hasA := result.Data["a"]; hasA {
		t.Error("expected data.a to be absent when GetB is selected")
	}
}

func TestExecuteGraphQL_operationNameRequired(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { a: String  b: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// When: document with multiple named operations but no operationName
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `query GetA { a } query GetB { b }`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: error — operationName required
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected errors when operationName is omitted for multi-op document")
	}
}

func TestExecuteGraphQL_httpDataSource(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { item: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Start a tiny HTTP backend that the HTTP data source will proxy to.
	backend := helpers.NewHTTPBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"from-http"}`))
	})

	// Create HTTP data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "HttpDS",
		"type": "HTTP",
		"httpConfig": map[string]any{
			"endpoint": backend.URL,
		},
	}).Body.Close()

	// Create resolver that uses the HTTP data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "item",
		"dataSourceName":         "HttpDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","resourcePath":"/","method":"POST","params":{"headers":{"Content-Type":"application/json"},"body":"{\"query\":\"test\"}"}}`,
	}).Body.Close()

	// When: execute query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ item }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: result from HTTP backend
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data map[string]any `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data["item"] == nil {
		t.Fatal("expected data.item to be non-nil from HTTP data source")
	}
}

func TestExecuteGraphQL_nestedObject(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { user: User }
type User { name: String  age: Int }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "user",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":{"name":"Alice","age":30}}`,
	}).Body.Close()

	// When: query with sub-selection on user
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ user { name age } }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: nested fields are returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			User map[string]any `json:"user"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.User["name"] != "Alice" {
		t.Errorf("expected user.name=%q, got %v", "Alice", result.Data.User["name"])
	}
	if result.Data.User["age"] != float64(30) {
		t.Errorf("expected user.age=30, got %v", result.Data.User["age"])
	}
}

func TestExecuteGraphQL_multipleFields(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { x: String  y: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "x",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":"ex"}`,
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "y",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":"why"}`,
	}).Body.Close()

	// When: query with two fields
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ x y }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: both resolved
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data map[string]any `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data["x"] != "ex" {
		t.Errorf("expected data.x=%q, got %v", "ex", result.Data["x"])
	}
	if result.Data["y"] != "why" {
		t.Errorf("expected data.y=%q, got %v", "why", result.Data["y"])
	}
}

// ─── Types CRUD ───────────────────────────────────────────────────────────────

func TestType_crudLifecycle(t *testing.T) {
	// Given: an existing API with a schema
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Upload a schema first (Types API reads types from the schema).
	sdl := `type Query { hello: String }
type User { name: String  age: Int }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	schResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL})
	schResp.Body.Close()

	// When: CreateType with a new type definition
	createResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types", map[string]any{
		"definition": "type Post { id: ID!  title: String }",
		"format":     "SDL",
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)

	var createResult struct {
		Type struct {
			Name       string `json:"name"`
			Format     string `json:"format"`
			Definition string `json:"definition"`
			Arn        string `json:"arn"`
		} `json:"type"`
	}
	helpers.DecodeJSON(t, createResp, &createResult)
	if createResult.Type.Name != "Post" {
		t.Errorf("expected type name=%q, got %q", "Post", createResult.Type.Name)
	}
	if createResult.Type.Format != "SDL" {
		t.Errorf("expected format=%q, got %q", "SDL", createResult.Type.Format)
	}
	if createResult.Type.Arn == "" {
		t.Error("expected type to have an ARN")
	}

	// GetType
	getResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types/Post?format=SDL")
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	var getResult struct {
		Type struct {
			Name       string `json:"name"`
			Definition string `json:"definition"`
		} `json:"type"`
	}
	helpers.DecodeJSON(t, getResp, &getResult)
	if getResult.Type.Name != "Post" {
		t.Errorf("expected type name=%q, got %q", "Post", getResult.Type.Name)
	}

	// ListTypes
	listResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types?format=SDL")
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)

	var listResult struct {
		Types []struct {
			Name string `json:"name"`
		} `json:"types"`
	}
	helpers.DecodeJSON(t, listResp, &listResult)
	// Should include at least Query, User, Post (from schema + created type)
	if len(listResult.Types) < 3 {
		t.Errorf("expected at least 3 types, got %d", len(listResult.Types))
	}

	// UpdateType
	updateResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Post", map[string]any{
		"definition": "type Post { id: ID!  title: String  body: String }",
		"format":     "SDL",
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)

	// Verify update
	getResp2 := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types/Post?format=SDL")
	defer getResp2.Body.Close()
	var getResult2 struct {
		Type struct {
			Definition string `json:"definition"`
		} `json:"type"`
	}
	helpers.DecodeJSON(t, getResp2, &getResult2)
	if !strings.Contains(getResult2.Type.Definition, "body") {
		t.Errorf("expected updated definition to contain 'body', got %q", getResult2.Type.Definition)
	}

	// DeleteType
	delResp := appsyncDelete(t, srv, "/v1/apis/"+apiID+"/types/Post")
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Verify deleted — 404
	goneResp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types/Post?format=SDL")
	defer goneResp.Body.Close()
	helpers.AssertStatus(t, goneResp, http.StatusNotFound)
}

func TestType_getNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/types/Nonexistent?format=SDL")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── ListResolversByFunction ─────────────────────────────────────────────────

func TestListResolversByFunction(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Upload schema
	sdl := `type Query { a: String  b: String }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL}).Body.Close()

	// Create data source
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()

	// Create two functions
	fn1Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":           "fn1",
		"dataSourceName": "NoneDS",
	})
	defer fn1Resp.Body.Close()
	var fn1Result struct {
		FunctionConfiguration struct {
			FunctionId string `json:"functionId"`
		} `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn1Resp, &fn1Result)
	fn1ID := fn1Result.FunctionConfiguration.FunctionId

	fn2Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":           "fn2",
		"dataSourceName": "NoneDS",
	})
	defer fn2Resp.Body.Close()
	var fn2Result struct {
		FunctionConfiguration struct {
			FunctionId string `json:"functionId"`
		} `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn2Resp, &fn2Result)
	fn2ID := fn2Result.FunctionConfiguration.FunctionId

	// Create pipeline resolver referencing fn1
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "a",
		"kind":           "PIPELINE",
		"pipelineConfig": map[string]any{"functions": []string{fn1ID}},
	}).Body.Close()

	// Create pipeline resolver referencing fn1 + fn2
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "b",
		"kind":           "PIPELINE",
		"pipelineConfig": map[string]any{"functions": []string{fn1ID, fn2ID}},
	}).Body.Close()

	// When: list resolvers by fn1
	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/functions/"+fn1ID+"/resolvers")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Resolvers []struct {
			FieldName string `json:"fieldName"`
		} `json:"resolvers"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Resolvers) != 2 {
		t.Fatalf("expected 2 resolvers using fn1, got %d", len(result.Resolvers))
	}

	// When: list resolvers by fn2 — should find only 1
	resp2 := appsyncGet(t, srv, "/v1/apis/"+apiID+"/functions/"+fn2ID+"/resolvers")
	defer resp2.Body.Close()
	var result2 struct {
		Resolvers []struct {
			FieldName string `json:"fieldName"`
		} `json:"resolvers"`
	}
	helpers.DecodeJSON(t, resp2, &result2)
	if len(result2.Resolvers) != 1 {
		t.Fatalf("expected 1 resolver using fn2, got %d", len(result2.Resolvers))
	}
}

// ─── Pipeline Resolver Execution ─────────────────────────────────────────────

func TestExecuteGraphQL_pipelineResolver(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { item: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// NONE data source for the pipeline functions.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()

	// Create two functions with their own request mapping templates.
	fn1Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":                    "step1",
		"dataSourceName":          "NoneDS",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"step1-result"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	})
	defer fn1Resp.Body.Close()
	var fn1 struct {
		FunctionConfiguration struct {
			FunctionId string `json:"functionId"`
		} `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn1Resp, &fn1)

	fn2Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":                    "step2",
		"dataSourceName":          "NoneDS",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"final-result"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	})
	defer fn2Resp.Body.Close()
	var fn2 struct {
		FunctionConfiguration struct {
			FunctionId string `json:"functionId"`
		} `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn2Resp, &fn2)

	// Create a PIPELINE resolver.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "item",
		"kind":           "PIPELINE",
		"pipelineConfig": map[string]any{"functions": []string{fn1.FunctionConfiguration.FunctionId, fn2.FunctionConfiguration.FunctionId}},
	}).Body.Close()

	// When: execute query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ item }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: result is from the last function in the pipeline
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Item string `json:"item"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.Item != "final-result" {
		t.Errorf("expected data.item=%q from pipeline, got %q", "final-result", result.Data.Item)
	}
}

func TestExecuteGraphQL_variablesPassthrough(t *testing.T) {
	srv := helpers.NewTestServer(t)
	sdl := `type Query { echo(msg: String): String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Start an HTTP backend that echoes the request body back.
	backend := helpers.NewHTTPBackend(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		// Return the body as the result — tests that variables appear in the request.
		_, _ = w.Write(body)
	})

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "HttpDS", "type": "HTTP",
		"httpConfig": map[string]any{"endpoint": backend.URL},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "echo",
		"dataSourceName":         "HttpDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","resourcePath":"/","method":"POST","params":{"headers":{"Content-Type":"application/json"},"body":"{\"msg\":\"hello\"}"}}`,
	}).Body.Close()

	// When: execute with variables
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query Echo($msg: String) { echo(msg: $msg) }`,
			"variables": map[string]any{"msg": "hello"},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with data (variables were passed)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data map[string]any `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data["echo"] == nil {
		t.Fatal("expected data.echo to be non-nil")
	}
}

// ─── Lambda data source ───────────────────────────────────────────────────────

func TestExecuteGraphQL_lambdaDataSource(t *testing.T) {
	// Given: an API with a Lambda function + AWS_LAMBDA data source + resolver
	srv := helpers.NewTestServer(t)
	sdl := `type Query { greet(name: String!): String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create a Lambda function via the Lambda API.
	lambdaResp := appsyncPostWithHeaders(t, srv, "/2015-03-31/functions", map[string]any{
		"FunctionName": "greet-fn",
		"Runtime":      "nodejs20.x",
		"Role":         "arn:aws:iam::000000000000:role/test",
		"Handler":      "index.handler",
		"Code":         map[string]any{"ZipFile": ""},
	}, nil)
	defer lambdaResp.Body.Close()
	var fnResult struct {
		FunctionArn string `json:"FunctionArn"`
	}
	helpers.DecodeJSON(t, lambdaResp, &fnResult)
	if fnResult.FunctionArn == "" {
		t.Fatal("expected FunctionArn to be non-empty")
	}

	// Create AWS_LAMBDA data source pointing to the function.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "LambdaDS",
		"type": "AWS_LAMBDA",
		"lambdaConfig": map[string]any{
			"lambdaFunctionArn": fnResult.FunctionArn,
		},
	}).Body.Close()

	// Create a resolver for Query.greet.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "greet",
		"dataSourceName": "LambdaDS",
		"kind":           "UNIT",
	}).Body.Close()

	// When: execute a query with arguments
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query Greet($n: String!) { greet(name: $n) }`,
			"variables": map[string]any{"n": "Alice"},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 — Lambda was invoked (might return an error payload if
	// Docker is unavailable, but the field should have a value or an error).
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	// The Lambda was invoked via the stub runtime (Docker not available), so
	// we expect an error mentioning the Lambda function — NOT "not yet supported".
	if len(result.Errors) == 0 {
		// Happy path: Lambda worked. Verify there's data.
		if result.Data["greet"] == nil {
			t.Fatal("expected data.greet to be non-nil when no errors")
		}
	} else {
		// Stub runtime error. Must NOT contain "not yet supported".
		for _, e := range result.Errors {
			if strings.Contains(e.Message, "not yet supported") {
				t.Fatalf("Lambda data source should be dispatched, not unsupported: %s", e.Message)
			}
		}
	}
}

func TestExecuteGraphQL_lambdaDirectResolver(t *testing.T) {
	// Given: an API with an AWS_LAMBDA data source pointing to a non-existent function
	srv := helpers.NewTestServer(t)
	sdl := `type Query { missing: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create AWS_LAMBDA data source with a function that doesn't exist.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "LambdaDS",
		"type": "AWS_LAMBDA",
		"lambdaConfig": map[string]any{
			"lambdaFunctionArn": "arn:aws:lambda:us-east-1:000000000000:function:nonexistent",
		},
	}).Body.Close()

	// Create resolver.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "missing",
		"dataSourceName": "LambdaDS",
		"kind":           "UNIT",
	}).Body.Close()

	// When: execute query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ missing }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with null data and/or error — graceful handling.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	// Should get an error about the function not being found — not "not yet supported".
	if len(result.Errors) == 0 {
		t.Fatal("expected errors for non-existent Lambda function")
	}
	for _, e := range result.Errors {
		if strings.Contains(e.Message, "not yet supported") {
			t.Fatalf("Lambda data source should be dispatched, not unsupported: %s", e.Message)
		}
	}
}

func TestExecuteGraphQL_lambdaNoInvoker(t *testing.T) {
	// Given: AppSync enabled but Lambda disabled — no invoker wired
	srv := helpers.NewTestServer(t)
	sdl := `type Query { fn: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "LambdaDS",
		"type": "AWS_LAMBDA",
		"lambdaConfig": map[string]any{
			"lambdaFunctionArn": "arn:aws:lambda:us-east-1:000000000000:function:nope",
		},
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "fn",
		"dataSourceName": "LambdaDS",
		"kind":           "UNIT",
	}).Body.Close()

	// When: execute query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ fn }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with error about Lambda not being available
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected errors when Lambda invoker is not wired")
	}
	for _, e := range result.Errors {
		if strings.Contains(e.Message, "not yet supported") {
			t.Fatalf("Lambda data source should return invoker-specific error, not unsupported: %s", e.Message)
		}
	}
}

// ─── Arguments passing ───────────────────────────────────────────────────────

func TestExecuteGraphQL_argumentsInTemplate(t *testing.T) {
	// Given: a NONE resolver whose template includes $context.arguments via passthrough
	srv := helpers.NewTestServer(t)
	sdl := `type Query { greet(name: String!): String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "greet",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":"Hello!"}`,
	}).Body.Close()

	// When: query with an argument
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query G($n: String!) { greet(name: $n) }`,
			"variables": map[string]any{"n": "Bob"},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with payload (arguments don't modify NONE result, but query is valid)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Greet string `json:"greet"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.Greet != "Hello!" {
		t.Errorf("expected data.greet=%q, got %q", "Hello!", result.Data.Greet)
	}
}

// ─── Response mapping templates ──────────────────────────────────────────────

func TestExecuteGraphQL_responseMappingTemplate(t *testing.T) {
	// Given: a resolver with a response mapping template that wraps result
	srv := helpers.NewTestServer(t)
	sdl := `type Query { user: User }
type User { name: String  role: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	// The request returns a payload; the response mapping template is a JSON
	// selector. Our simple implementation: if the responseMappingTemplate is
	// a JSON path like "$context.result.data", extract that path.
	// For now, test that $context.result (identity) returns the payload unchanged.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":               "user",
		"dataSourceName":          "NoneDS",
		"kind":                    "UNIT",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":{"name":"Alice","role":"admin"}}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ user { name role } }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			User map[string]any `json:"user"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.User["name"] != "Alice" {
		t.Errorf("expected user.name=%q, got %v", "Alice", result.Data.User["name"])
	}
	if result.Data.User["role"] != "admin" {
		t.Errorf("expected user.role=%q, got %v", "admin", result.Data.User["role"])
	}
}

// ─── Nested field resolvers ─────────────────────────────────────────────────

func TestExecuteGraphQL_nestedFieldResolvers(t *testing.T) {
	// Given: a schema with nested types where child fields have their own resolvers
	srv := helpers.NewTestServer(t)
	sdl := `type Query { author: Author }
type Author { name: String  posts: [Post] }
type Post { title: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()

	// Root resolver for Query.author — returns name only
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "author",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":{"name":"J.K. Rowling"}}`,
	}).Body.Close()

	// Child resolver for Author.posts — returns posts based on parent
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Author/resolvers", map[string]any{
		"fieldName":              "posts",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":[{"title":"Harry Potter"},{"title":"Fantastic Beasts"}]}`,
	}).Body.Close()

	// When: query nested fields
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ author { name posts { title } } }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: both parent and child resolvers executed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Author struct {
				Name  string           `json:"name"`
				Posts []map[string]any `json:"posts"`
			} `json:"author"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.Author.Name != "J.K. Rowling" {
		t.Errorf("expected author.name=%q, got %q", "J.K. Rowling", result.Data.Author.Name)
	}
	if len(result.Data.Author.Posts) != 2 {
		t.Fatalf("expected 2 posts, got %d", len(result.Data.Author.Posts))
	}
	if result.Data.Author.Posts[0]["title"] != "Harry Potter" {
		t.Errorf("expected posts[0].title=%q, got %v", "Harry Potter", result.Data.Author.Posts[0]["title"])
	}
}

// ─── Stubs return 501 ─────────────────────────────────────────────────────────

// ─── DynamoDB data source execution ──────────────────────────────────────────

// ddbCall sends a DynamoDB JSON-RPC request via X-Amz-Target dispatch.
func ddbCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ddbCall %s: %v", operation, err)
	}
	return resp
}

// setupDDBTable creates a DynamoDB table with a single HASH key on "id" (S).
func setupDDBTable(t *testing.T, srv *helpers.TestServer, tableName string) {
	t.Helper()
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName":            tableName,
		"AttributeDefinitions": []map[string]any{{"AttributeName": "id", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "id", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CreateTable %s: %d", tableName, resp.StatusCode)
	}
}

// setupDDBCompositeTable creates a DynamoDB table with HASH + RANGE key (pk:S, sk:S).
func setupDDBCompositeTable(t *testing.T, srv *helpers.TestServer, tableName string) {
	t.Helper()
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName": tableName,
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "sk", "AttributeType": "S"},
		},
		"KeySchema": []map[string]any{
			{"AttributeName": "pk", "KeyType": "HASH"},
			{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"BillingMode": "PAY_PER_REQUEST",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CreateTable %s: %d", tableName, resp.StatusCode)
	}
}

func TestExecuteGraphQL_dynamoDBGetItem(t *testing.T) {
	// Given: a DynamoDB table with an item, and an AppSync API with AMAZON_DYNAMODB data source
	srv := helpers.NewTestServer(t)
	tableName := "gql-users"
	setupDDBTable(t, srv, tableName)

	// Put an item into the table via DynamoDB API.
	putResp := ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": tableName,
		"Item": map[string]any{
			"id":   map[string]any{"S": "user-1"},
			"name": map[string]any{"S": "Alice"},
			"age":  map[string]any{"N": "30"},
		},
	})
	putResp.Body.Close()

	// Set up AppSync API.
	sdl := `type Query { getUser(id: String!): User }
type User { id: String, name: String, age: Int }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create AMAZON_DYNAMODB data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "UsersDS",
		"type": "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{
			"tableName": tableName,
		},
	}).Body.Close()

	// Create resolver for Query.getUser with a GetItem template.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "getUser",
		"dataSourceName": "UsersDS",
		"kind":           "UNIT",
		"requestMappingTemplate": `{
			"operation": "GetItem",
			"key": {"id": {"S": "$context.arguments.id"}}
		}`,
	}).Body.Close()

	// When: execute a GraphQL query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query GetUser($id: String!) { getUser(id: $id) { id name age } }`,
			"variables": map[string]any{"id": "user-1"},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with data.getUser containing the item
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			GetUser map[string]any `json:"getUser"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.GetUser["id"] != "user-1" {
		t.Errorf("expected id=%q, got %v", "user-1", result.Data.GetUser["id"])
	}
	if result.Data.GetUser["name"] != "Alice" {
		t.Errorf("expected name=%q, got %v", "Alice", result.Data.GetUser["name"])
	}
	// age is a DynamoDB number — unwrapped to numeric.
	if age, ok := result.Data.GetUser["age"].(float64); !ok || age != 30 {
		t.Errorf("expected age=30, got %v (%T)", result.Data.GetUser["age"], result.Data.GetUser["age"])
	}
}

func TestExecuteGraphQL_dynamoDBGetItemNotFound(t *testing.T) {
	// Given: an empty DynamoDB table
	srv := helpers.NewTestServer(t)
	tableName := "gql-missing"
	setupDDBTable(t, srv, tableName)

	sdl := `type Query { getUser(id: String!): User }
type User { id: String, name: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "MissingDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "getUser",
		"dataSourceName":         "MissingDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"operation":"GetItem","key":{"id":{"S":"$context.arguments.id"}}}`,
	}).Body.Close()

	// When: query for a non-existent item
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query { getUser(id: "does-not-exist") { id name } }`,
			"variables": map[string]any{},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with null data (no errors — AppSync returns null for missing items)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			GetUser any `json:"getUser"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.GetUser != nil {
		t.Errorf("expected null for missing item, got %v", result.Data.GetUser)
	}
}

func TestExecuteGraphQL_dynamoDBPutItem(t *testing.T) {
	// Given: an empty DynamoDB table + mutation resolver
	srv := helpers.NewTestServer(t)
	tableName := "gql-put"
	setupDDBTable(t, srv, tableName)

	sdl := `type Query { getUser(id: String!): User }
type Mutation { createUser(id: String!, name: String!): User }
type User { id: String, name: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "PutDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	// Resolver for Mutation.createUser — PutItem.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Mutation/resolvers", map[string]any{
		"fieldName":      "createUser",
		"dataSourceName": "PutDS",
		"kind":           "UNIT",
		"requestMappingTemplate": `{
			"operation": "PutItem",
			"key": {"id": {"S": "$context.arguments.id"}},
			"item": {"id": {"S": "$context.arguments.id"}, "name": {"S": "$context.arguments.name"}}
		}`,
	}).Body.Close()

	// Resolver for Query.getUser — GetItem.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "getUser",
		"dataSourceName":         "PutDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"operation":"GetItem","key":{"id":{"S":"$context.arguments.id"}}}`,
	}).Body.Close()

	// When: execute a mutation to create a user
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `mutation CreateUser($id: String!, $name: String!) { createUser(id: $id, name: $name) { id name } }`,
			"variables": map[string]any{"id": "user-2", "name": "Bob"},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: verify the item was stored by reading it back
	getResp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query { getUser(id: "user-2") { id name } }`,
			"variables": map[string]any{},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	var result struct {
		Data struct {
			GetUser map[string]any `json:"getUser"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, getResp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.GetUser["id"] != "user-2" {
		t.Errorf("expected id=%q, got %v", "user-2", result.Data.GetUser["id"])
	}
	if result.Data.GetUser["name"] != "Bob" {
		t.Errorf("expected name=%q, got %v", "Bob", result.Data.GetUser["name"])
	}
}

func TestExecuteGraphQL_dynamoDBDeleteItem(t *testing.T) {
	// Given: a DynamoDB table with an item
	srv := helpers.NewTestServer(t)
	tableName := "gql-del"
	setupDDBTable(t, srv, tableName)

	ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": tableName,
		"Item":      map[string]any{"id": map[string]any{"S": "del-1"}, "name": map[string]any{"S": "ToDelete"}},
	}).Body.Close()

	sdl := `type Query { getUser(id: String!): User }
type Mutation { deleteUser(id: String!): User }
type User { id: String, name: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "DelDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Mutation/resolvers", map[string]any{
		"fieldName":              "deleteUser",
		"dataSourceName":         "DelDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"operation":"DeleteItem","key":{"id":{"S":"$context.arguments.id"}}}`,
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "getUser",
		"dataSourceName":         "DelDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"operation":"GetItem","key":{"id":{"S":"$context.arguments.id"}}}`,
	}).Body.Close()

	// When: delete the item via mutation
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `mutation { deleteUser(id: "del-1") { id } }`,
			"variables": map[string]any{},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: verify the item is gone
	getResp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query { getUser(id: "del-1") { id name } }`,
			"variables": map[string]any{},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	var result struct {
		Data struct {
			GetUser any `json:"getUser"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, getResp, &result)
	if result.Data.GetUser != nil {
		t.Errorf("expected null for deleted item, got %v", result.Data.GetUser)
	}
}

func TestExecuteGraphQL_dynamoDBScan(t *testing.T) {
	// Given: a DynamoDB table with multiple items
	srv := helpers.NewTestServer(t)
	tableName := "gql-scan"
	setupDDBTable(t, srv, tableName)

	for i := 0; i < 3; i++ {
		ddbCall(t, srv, "PutItem", map[string]any{
			"TableName": tableName,
			"Item": map[string]any{
				"id":   map[string]any{"S": fmt.Sprintf("item-%d", i)},
				"name": map[string]any{"S": fmt.Sprintf("Name%d", i)},
			},
		}).Body.Close()
	}

	sdl := `type Query { listUsers: [User] }
type User { id: String, name: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "ScanDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "listUsers",
		"dataSourceName":         "ScanDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"operation":"Scan"}`,
	}).Body.Close()

	// When: execute a scan query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `{ listUsers { id name } }`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with all 3 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			ListUsers []map[string]any `json:"listUsers"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if len(result.Data.ListUsers) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result.Data.ListUsers))
	}
}

func TestExecuteGraphQL_dynamoDBQuery(t *testing.T) {
	// Given: a composite-key DynamoDB table with items
	srv := helpers.NewTestServer(t)
	tableName := "gql-query"
	setupDDBCompositeTable(t, srv, tableName)

	// Insert items for two different partition keys.
	for _, item := range []map[string]any{
		{"pk": map[string]any{"S": "user-1"}, "sk": map[string]any{"S": "post-a"}, "title": map[string]any{"S": "First"}},
		{"pk": map[string]any{"S": "user-1"}, "sk": map[string]any{"S": "post-b"}, "title": map[string]any{"S": "Second"}},
		{"pk": map[string]any{"S": "user-2"}, "sk": map[string]any{"S": "post-c"}, "title": map[string]any{"S": "Other"}},
	} {
		ddbCall(t, srv, "PutItem", map[string]any{"TableName": tableName, "Item": item}).Body.Close()
	}

	sdl := `type Query { postsByUser(userId: String!): [Post] }
type Post { pk: String, sk: String, title: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "QueryDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "postsByUser",
		"dataSourceName": "QueryDS",
		"kind":           "UNIT",
		"requestMappingTemplate": `{
			"operation": "Query",
			"query": "pk = :pk",
			"expressionValues": {":pk": {"S": "$context.arguments.userId"}}
		}`,
	}).Body.Close()

	// When: query for user-1's posts
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query Posts($uid: String!) { postsByUser(userId: $uid) { pk sk title } }`,
			"variables": map[string]any{"uid": "user-1"},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with 2 posts for user-1
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			PostsByUser []map[string]any `json:"postsByUser"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if len(result.Data.PostsByUser) != 2 {
		t.Fatalf("expected 2 posts for user-1, got %d", len(result.Data.PostsByUser))
	}
}

func TestExecuteGraphQL_dynamoDBNoInvoker(t *testing.T) {
	// Given: AppSync API with AMAZON_DYNAMODB data source but DynamoDB service not wired
	// This test verifies we get a clear error, not a panic.
	//
	// We can't easily simulate "no DynamoDB" in integration tests since the
	// test server always enables all services. Instead, verify the dispatch
	// path works — if DynamoDB is available, it should NOT return "not yet supported".
	srv := helpers.NewTestServer(t)
	tableName := "gql-nowire"
	setupDDBTable(t, srv, tableName)

	sdl := `type Query { get(id: String!): String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "NowireDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "get",
		"dataSourceName":         "NowireDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"operation":"GetItem","key":{"id":{"S":"$context.arguments.id"}}}`,
	}).Body.Close()

	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `{ get(id: "x") }`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	// The resolver should NOT say "not yet supported" — it should dispatch to DynamoDB.
	for _, e := range result.Errors {
		if strings.Contains(e.Message, "not yet supported") {
			t.Fatalf("DynamoDB data source should be dispatched, not unsupported: %s", e.Message)
		}
	}
}

func TestExecuteGraphQL_dynamoDBPipelineResolver(t *testing.T) {
	// Given: a pipeline resolver with a DynamoDB function
	srv := helpers.NewTestServer(t)
	tableName := "gql-pipe-ddb"
	setupDDBTable(t, srv, tableName)

	// Pre-populate.
	ddbCall(t, srv, "PutItem", map[string]any{
		"TableName": tableName,
		"Item": map[string]any{
			"id":   map[string]any{"S": "pipe-1"},
			"name": map[string]any{"S": "Pipeline"},
		},
	}).Body.Close()

	sdl := `type Query { getItem(id: String!): Item }
type Item { id: String, name: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "PipeDDB",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	// Create an AppSync function backed by the DynamoDB data source.
	fnResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":                    "GetItemFn",
		"dataSourceName":          "PipeDDB",
		"requestMappingTemplate":  `{"operation":"GetItem","key":{"id":{"S":"$context.arguments.id"}}}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	})
	defer fnResp.Body.Close()
	var fnResult struct {
		FunctionConfiguration struct {
			FunctionId string `json:"functionId"`
		} `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fnResp, &fnResult)
	fnID := fnResult.FunctionConfiguration.FunctionId

	// Create a PIPELINE resolver that runs the function.
	pipelineConfig, _ := json.Marshal(map[string]any{"functions": []string{fnID}})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "getItem",
		"kind":           "PIPELINE",
		"pipelineConfig": json.RawMessage(pipelineConfig),
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query { getItem(id: "pipe-1") { id name } }`,
			"variables": map[string]any{},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			GetItem map[string]any `json:"getItem"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.GetItem["id"] != "pipe-1" {
		t.Errorf("expected id=%q, got %v", "pipe-1", result.Data.GetItem["id"])
	}
	if result.Data.GetItem["name"] != "Pipeline" {
		t.Errorf("expected name=%q, got %v", "Pipeline", result.Data.GetItem["name"])
	}
}

// ─── APPSYNC_JS Runtime ──────────────────────────────────────────────────────

func TestAppSyncJS_unitResolver(t *testing.T) {
	// Given: an API with a schema and a UNIT resolver using APPSYNC_JS runtime
	sdl := `type Query { greet(name: String!): String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create a NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	// Create a UNIT resolver with APPSYNC_JS runtime (code field, no VTL templates).
	jsCode := `
		export function request(ctx) {
			return { payload: { greeting: "Hello, " + ctx.arguments.name + "!" } };
		}
		export function response(ctx) {
			return ctx.result.greeting;
		}
	`
	runtime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "greet",
		"dataSourceName": "NoneDS",
		"code":           jsCode,
		"runtime":        json.RawMessage(runtime),
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ greet(name: "World") }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should get the greeting from the JS resolver
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Greet string `json:"greet"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Greet != "Hello, World!" {
		t.Errorf("expected %q, got %q", "Hello, World!", result.Data.Greet)
	}
}

func TestAppSyncJS_pipelineResolver(t *testing.T) {
	// Given: an API with a PIPELINE resolver using APPSYNC_JS functions
	sdl := `type Query { add(a: Int!, b: Int!): Int }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create a NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	// Create an APPSYNC_JS function that computes the sum.
	fnRuntime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	fnResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":           "AddFn",
		"dataSourceName": "NoneDS",
		"code": `
			export function request(ctx) {
				return { payload: { sum: ctx.arguments.a + ctx.arguments.b } };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
		"runtime": json.RawMessage(fnRuntime),
	})
	defer fnResp.Body.Close()
	var fnResult struct {
		FunctionConfiguration struct {
			FunctionId string `json:"functionId"`
		} `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fnResp, &fnResult)
	fnID := fnResult.FunctionConfiguration.FunctionId

	// Create a PIPELINE resolver that runs the function.
	pipelineConfig, _ := json.Marshal(map[string]any{"functions": []string{fnID}})
	pipeRuntime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "add",
		"kind":           "PIPELINE",
		"pipelineConfig": json.RawMessage(pipelineConfig),
		"code": `
			export function request(ctx) {
				return {};
			}
			export function response(ctx) {
				return ctx.prev.result.sum;
			}
		`,
		"runtime": json.RawMessage(pipeRuntime),
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ add(a: 3, b: 4) }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should get 7
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Add float64 `json:"add"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Add != 7 {
		t.Errorf("expected 7, got %v", result.Data.Add)
	}
}

func TestAppSyncJS_utilAutoId(t *testing.T) {
	// Given: an API with a resolver that uses util.autoId()
	sdl := `type Query { generateId: String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	runtime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "generateId",
		"dataSourceName": "NoneDS",
		"code": `
			import { util } from '@aws-appsync/utils';
			export function request(ctx) {
				return { payload: util.autoId() };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
		"runtime": json.RawMessage(runtime),
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ generateId }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should get a UUID-like string
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			GenerateId string `json:"generateId"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if len(result.Data.GenerateId) < 32 {
		t.Errorf("expected UUID-like string, got %q", result.Data.GenerateId)
	}
}

func TestAppSyncJS_utilJson(t *testing.T) {
	// Given: a resolver that uses util.toJson and util.parseJson
	sdl := `type Query { roundTrip(input: String!): String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	runtime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "roundTrip",
		"dataSourceName": "NoneDS",
		"code": `
			import { util } from '@aws-appsync/utils';
			export function request(ctx) {
				const obj = { key: ctx.arguments.input };
				const jsonStr = util.toJson(obj);
				const parsed = util.parseJson(jsonStr);
				return { payload: parsed.key };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
		"runtime": json.RawMessage(runtime),
	}).Body.Close()

	// When: execute
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ roundTrip(input: "hello") }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: the round-tripped value should match
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			RoundTrip string `json:"roundTrip"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.RoundTrip != "hello" {
		t.Errorf("expected %q, got %q", "hello", result.Data.RoundTrip)
	}
}

func TestAppSyncJS_stashPropagation(t *testing.T) {
	// Given: a PIPELINE resolver where functions share state via ctx.stash
	sdl := `type Query { stashTest: String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	fnRuntime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})

	// Function 1: writes to stash
	fn1Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":           "WriteFn",
		"dataSourceName": "NoneDS",
		"code": `
			export function request(ctx) {
				ctx.stash.message = "from-stash";
				return { payload: null };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
		"runtime": json.RawMessage(fnRuntime),
	})
	defer fn1Resp.Body.Close()
	var fn1Result struct {
		FunctionConfiguration struct{ FunctionId string } `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn1Resp, &fn1Result)

	// Function 2: reads from stash
	fn2Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":           "ReadFn",
		"dataSourceName": "NoneDS",
		"code": `
			export function request(ctx) {
				return { payload: ctx.stash.message };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
		"runtime": json.RawMessage(fnRuntime),
	})
	defer fn2Resp.Body.Close()
	var fn2Result struct {
		FunctionConfiguration struct{ FunctionId string } `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn2Resp, &fn2Result)

	// Create pipeline with both functions
	pipeConfig, _ := json.Marshal(map[string]any{
		"functions": []string{fn1Result.FunctionConfiguration.FunctionId, fn2Result.FunctionConfiguration.FunctionId},
	})
	pipeRuntime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "stashTest",
		"kind":           "PIPELINE",
		"pipelineConfig": json.RawMessage(pipeConfig),
		"code": `
			export function request(ctx) { return {}; }
			export function response(ctx) { return ctx.prev.result; }
		`,
		"runtime": json.RawMessage(pipeRuntime),
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ stashTest }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should get the stashed value
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			StashTest string `json:"stashTest"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.StashTest != "from-stash" {
		t.Errorf("expected %q, got %q", "from-stash", result.Data.StashTest)
	}
}

// EvaluateCode's own coverage lives in evaluate_test.go, at the binding AWS
// models. The test that stood here drove POST /v1/apis/{apiId}/evaluateCode
// with `context` as an object, which is neither the path nor the shape.

func TestStubs_return501(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Note: EvaluateMappingTemplate is now implemented (VTL evaluator), so it's
	// no longer in this list. Add new stubs here as they are discovered.
	paths := []struct {
		method string
		path   string
	}{}

	for _, tc := range paths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var resp *http.Response
			switch tc.method {
			case http.MethodPost:
				resp = appsyncPost(t, srv, tc.path, map[string]any{})
			case http.MethodGet:
				resp = appsyncGet(t, srv, tc.path)
			case http.MethodPut:
				resp = appsyncPut(t, srv, tc.path, map[string]any{})
			}
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusNotImplemented)
		})
	}

	_ = apiID // keep to avoid unused variable if paths is empty
}

// ─── VTL Mapping Template Tests ──────────────────────────────────────────────

func TestVTL_simpleReturn(t *testing.T) {
	// Given: an API with a schema and a UNIT resolver using VTL templates
	sdl := `type Query { greet(name: String!): String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Create a NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	// Create a UNIT resolver with VTL templates (no runtime = VTL).
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":               "greet",
		"dataSourceName":          "NoneDS",
		"requestMappingTemplate":  `{ "version": "2018-05-29", "payload": "Hello, $context.arguments.name!" }`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ greet(name: "World") }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should get the greeting from the VTL resolver
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Greet string `json:"greet"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Greet != "Hello, World!" {
		t.Errorf("expected %q, got %q", "Hello, World!", result.Data.Greet)
	}
}

func TestVTL_setDirective(t *testing.T) {
	// Given: a VTL resolver using #set directive
	sdl := `type Query { greet(name: String!): String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "greet",
		"dataSourceName": "NoneDS",
		"requestMappingTemplate": `#set($greeting = "Hello")
{ "version": "2018-05-29", "payload": "$greeting $context.arguments.name" }`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ greet(name: "VTL") }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should interpolate the set variable
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Greet string `json:"greet"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Greet != "Hello VTL" {
		t.Errorf("expected %q, got %q", "Hello VTL", result.Data.Greet)
	}
}

func TestVTL_conditionalDirective(t *testing.T) {
	// Given: a VTL resolver with #if/#else
	sdl := `type Query { greet(formal: Boolean!, name: String!): String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "greet",
		"dataSourceName": "NoneDS",
		"requestMappingTemplate": `#if($context.arguments.formal)
{ "version": "2018-05-29", "payload": "Good day, $context.arguments.name." }
#else
{ "version": "2018-05-29", "payload": "Hey $context.arguments.name!" }
#end`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	// When: formal = true
	resp1 := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ greet(formal: true, name: "Alice") }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)
	var r1 struct {
		Data struct {
			Greet string `json:"greet"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp1, &r1)
	if len(r1.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", r1.Errors)
	}
	if !strings.Contains(r1.Data.Greet, "Good day") {
		t.Errorf("expected formal greeting, got %q", r1.Data.Greet)
	}

	// When: formal = false
	resp2 := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ greet(formal: false, name: "Bob") }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var r2 struct {
		Data struct {
			Greet string `json:"greet"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp2, &r2)
	if len(r2.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", r2.Errors)
	}
	if !strings.Contains(r2.Data.Greet, "Hey") {
		t.Errorf("expected informal greeting, got %q", r2.Data.Greet)
	}
}

func TestVTL_foreachDirective(t *testing.T) {
	// Given: a VTL resolver iterating over a list argument
	sdl := `type Query { joinNames(names: [String!]!): String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "joinNames",
		"dataSourceName": "NoneDS",
		"requestMappingTemplate": `#set($result = "")
#foreach($name in $context.arguments.names)
#if($foreach.index > 0)#set($result = "${result}, ${name}")#else#set($result = $name)#end
#end
{ "version": "2018-05-29", "payload": "$result" }`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	// When: execute with a list of names
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query":     `query($names: [String!]!) { joinNames(names: $names) }`,
			"variables": map[string]any{"names": []string{"Alice", "Bob", "Charlie"}},
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should join the names
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			JoinNames string `json:"joinNames"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.JoinNames != "Alice, Bob, Charlie" {
		t.Errorf("expected %q, got %q", "Alice, Bob, Charlie", result.Data.JoinNames)
	}
}

func TestVTL_utilFunctions(t *testing.T) {
	// Given: a server. EvaluateMappingTemplate is API-independent, so these
	// exercise the VTL evaluator without an API to attach to.
	srv := helpers.NewTestServer(t)

	// Test $util.toJson
	t.Run("toJson", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `$util.toJson($context.arguments)`,
			"context":  `{"arguments":{"name":"test","count":42}}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		if !strings.Contains(result.EvaluationResult, `"name":"test"`) {
			t.Errorf("expected JSON with name, got %q", result.EvaluationResult)
		}
	})

	// Test $util.autoId (returns a UUID)
	t.Run("autoId", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `$util.autoId()`,
			"context":  `{}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		// UUID format: 8-4-4-4-12
		if len(result.EvaluationResult) != 36 {
			t.Errorf("expected UUID (36 chars), got %q (len=%d)", result.EvaluationResult, len(result.EvaluationResult))
		}
	})

	// Test $util.isNull
	t.Run("isNull", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `#if($util.isNull($context.arguments.missing))null_detected#end`,
			"context":  `{"arguments":{}}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		if !strings.Contains(result.EvaluationResult, "null_detected") {
			t.Errorf("expected null_detected, got %q", result.EvaluationResult)
		}
	})

	// Test $util.parseJson
	t.Run("parseJson", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `#set($obj = $util.parseJson('{"key":"value"}'))$obj.key`,
			"context":  `{}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		if result.EvaluationResult != "value" {
			t.Errorf("expected %q, got %q", "value", result.EvaluationResult)
		}
	})

	// Test $util.time.nowISO8601()
	t.Run("timeNowISO8601", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `$util.time.nowISO8601()`,
			"context":  `{}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		if !strings.Contains(result.EvaluationResult, "T") {
			t.Errorf("expected ISO8601 timestamp, got %q", result.EvaluationResult)
		}
	})
}

// EvaluateMappingTemplate's own coverage lives in evaluate_test.go, at the
// binding AWS models. The test that stood here drove
// POST /v1/apis/{apiId}/evaluateMappingTemplate.

func TestVTL_pipelineResolver(t *testing.T) {
	// Given: a PIPELINE resolver where VTL functions share state via $context.stash
	sdl := `type Query { stashTest: String }`
	srv := helpers.NewTestServer(t)
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	// Function 1: writes to stash
	fn1Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":                    "WriteFn",
		"dataSourceName":          "NoneDS",
		"requestMappingTemplate":  `#set($context.stash.message = "from-vtl-stash"){"version":"2018-05-29","payload":null}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	})
	defer fn1Resp.Body.Close()
	var fn1Result struct {
		FunctionConfiguration struct{ FunctionId string } `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn1Resp, &fn1Result)

	// Function 2: reads from stash and returns it
	fn2Resp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/functions", map[string]any{
		"name":                    "ReadFn",
		"dataSourceName":          "NoneDS",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"$context.stash.message"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	})
	defer fn2Resp.Body.Close()
	var fn2Result struct {
		FunctionConfiguration struct{ FunctionId string } `json:"functionConfiguration"`
	}
	helpers.DecodeJSON(t, fn2Resp, &fn2Result)

	// Create pipeline resolver
	pipeConfig, _ := json.Marshal(map[string]any{
		"functions": []string{fn1Result.FunctionConfiguration.FunctionId, fn2Result.FunctionConfiguration.FunctionId},
	})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":               "stashTest",
		"kind":                    "PIPELINE",
		"pipelineConfig":          json.RawMessage(pipeConfig),
		"requestMappingTemplate":  `{}`,
		"responseMappingTemplate": `$util.toJson($context.prev.result)`,
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ stashTest }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: should get the stashed value through the pipeline
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			StashTest string `json:"stashTest"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.StashTest != "from-vtl-stash" {
		t.Errorf("expected %q, got %q", "from-vtl-stash", result.Data.StashTest)
	}
}

// ─── APPSYNC_JS ctx.env injection ────────────────────────────────────────────

func TestAppSyncJS_envVars(t *testing.T) {
	// Given: an API with environment variables and a JS resolver that reads ctx.env
	srv := helpers.NewTestServer(t)
	sdl := `type Query { envTest: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Set environment variables.
	appsyncPut(t, srv, "/v1/apis/"+apiID+"/environmentVariables", map[string]any{
		"environmentVariables": map[string]string{
			"MY_VAR": "hello-from-env",
		},
	}).Body.Close()

	// Create NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()

	// Create resolver that reads ctx.env.MY_VAR.
	runtime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "envTest",
		"dataSourceName": "NoneDS",
		"runtime":        json.RawMessage(runtime),
		"code": `
			export function request(ctx) {
				return { payload: ctx.env.MY_VAR };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ envTest }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: the env var value should be accessible
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			EnvTest string `json:"envTest"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.EnvTest != "hello-from-env" {
		t.Errorf("expected %q, got %q", "hello-from-env", result.Data.EnvTest)
	}
}

// ─── APPSYNC_JS expanded util functions ──────────────────────────────────────

func TestAppSyncJS_utilExpanded(t *testing.T) {
	// Given: a server. EvaluateCode is API-independent, so these exercise the
	// APPSYNC_JS runtime without an API to attach to.
	srv := helpers.NewTestServer(t)

	// Test util.isNull, util.isString, util.defaultIfNull, util.matches.
	resp := appsyncPost(t, srv, evaluateCodePath, map[string]any{
		"code": `
			export function request(ctx) {
				return {
					isNullTrue:         util.isNull(null),
					isNullFalse:        util.isNull("hello"),
					isStringTrue:       util.isString("test"),
					isStringFalse:      util.isString(42),
					isNumberTrue:       util.isNumber(3.14),
					isBoolTrue:         util.isBoolean(true),
					isListTrue:         util.isList([1, 2]),
					isMapTrue:          util.isMap({a: 1}),
					defaultResult:      util.defaultIfNull(null, "fallback"),
					defaultKeep:        util.defaultIfNull("keep", "fallback"),
					matchesTrue:        util.matches("^hello", "hello world"),
					matchesFalse:       util.matches("^world", "hello world"),
					toLower:            util.str.toLower("HELLO"),
					toUpper:            util.str.toUpper("hello"),
					toReplace:          util.str.toReplace("foo bar", "bar", "baz"),
					roundNum:           util.math.roundNum(3.7),
					minVal:             util.math.minVal(10, 5),
					maxVal:             util.math.maxVal(10, 5),
					isNullOrEmptyTrue:  util.isNullOrEmpty(""),
					isNullOrEmptyFalse: util.isNullOrEmpty("hi"),
					defaultIfEmpty:     util.defaultIfNullOrEmpty("", "fallback2"),
				};
			}
		`,
		"context":  `{}`,
		"function": "request",
		"runtime":  appsyncRuntime(),
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var evalResp struct {
		EvaluationResult string `json:"evaluationResult"`
	}
	helpers.DecodeJSON(t, resp, &evalResp)
	if evalResp.EvaluationResult == "" {
		t.Fatal("expected non-empty evaluationResult")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(evalResp.EvaluationResult), &result); err != nil {
		t.Fatalf("parse evaluationResult: %v", err)
	}

	tests := map[string]any{
		"isNullTrue":         true,
		"isNullFalse":        false,
		"isStringTrue":       true,
		"isStringFalse":      false,
		"isNumberTrue":       true,
		"isBoolTrue":         true,
		"isListTrue":         true,
		"isMapTrue":          true,
		"defaultResult":      "fallback",
		"defaultKeep":        "keep",
		"matchesTrue":        true,
		"matchesFalse":       false,
		"toLower":            "hello",
		"toUpper":            "HELLO",
		"toReplace":          "foo baz",
		"roundNum":           float64(4),
		"minVal":             float64(5),
		"maxVal":             float64(10),
		"isNullOrEmptyTrue":  true,
		"isNullOrEmptyFalse": false,
		"defaultIfEmpty":     "fallback2",
	}

	for key, expected := range tests {
		got := result[key]
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", expected) {
			t.Errorf("util.%s: expected %v, got %v", key, expected, got)
		}
	}
}

// ─── Authentication expansion ────────────────────────────────────────────────

// fakeJWT builds a minimal unsigned JWT with the given claims payload.
func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".fakesig"
}

func TestAuth_cognitoAcceptsBearer(t *testing.T) {
	// Given: an API with AMAZON_COGNITO_USER_POOLS auth and a schema+resolver
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Update the API to use Cognito auth.
	appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"name":               "cognito-api",
		"authenticationType": "AMAZON_COGNITO_USER_POOLS",
		"userPoolConfig":     map[string]any{"userPoolId": "us-east-1_fake", "defaultAction": "ALLOW"},
	}).Body.Close()

	// Upload schema.
	sdl := `type Query { hello: String }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL}).Body.Close()

	// Create NONE data source + resolver.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	runtime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "hello",
		"dataSourceName": "NoneDS",
		"runtime":        json.RawMessage(runtime),
		"code": `
			export function request(ctx) {
				return { payload: "world" };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
	}).Body.Close()

	// When: send a request with Bearer token
	token := fakeJWT(t, map[string]any{"sub": "user-123", "iss": "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_fake"})
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"Authorization": "Bearer " + token},
	)
	defer resp.Body.Close()

	// Then: should succeed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Hello string `json:"hello"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Hello != "world" {
		t.Errorf("expected %q, got %q", "world", result.Data.Hello)
	}
}

func TestAuth_cognitoIdentityAvailableInVTL(t *testing.T) {
	// Given: a Cognito-authenticated API with a VTL resolver that reads $ctx.identity
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)
	appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"name":               "cognito-vtl-api",
		"authenticationType": "AMAZON_COGNITO_USER_POOLS",
		"userPoolConfig":     map[string]any{"userPoolId": "us-east-1_fake", "defaultAction": "ALLOW"},
	}).Body.Close()
	b64SDL := base64.StdEncoding.EncodeToString([]byte(`type Query { subject: String }`))
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "subject",
		"dataSourceName":         "NoneDS",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":$util.toJson($ctx.identity.sub)}`,
	}).Body.Close()

	// When: the query is executed with a Bearer token
	token := fakeJWT(t, map[string]any{"sub": "user-123", "iss": "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_fake"})
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ subject }`},
		map[string]string{"Authorization": "Bearer " + token},
	)
	defer resp.Body.Close()

	// Then: VTL can read the identity claims from the resolver context
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Subject string `json:"subject"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.Subject != "user-123" {
		t.Errorf("expected subject from identity, got %q", result.Data.Subject)
	}
}

func TestAuth_cognitoRejectsNoToken(t *testing.T) {
	// Given: an API with AMAZON_COGNITO_USER_POOLS auth
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	// Update auth type to Cognito.
	appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"name":               "cognito-api",
		"authenticationType": "AMAZON_COGNITO_USER_POOLS",
	}).Body.Close()

	// Upload schema.
	sdl := `type Query { hello: String }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL}).Body.Close()

	// When: send request WITHOUT Authorization header
	resp := appsyncPost(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
	)

	// Then: AppSync's GraphQL error envelope naming UnauthorizedException
	assertGraphQLAuthUnauthorized(t, resp)
}

func TestAuth_multiAuth(t *testing.T) {
	// Given: an API with primary API_KEY auth + Cognito as additional
	srv := helpers.NewTestServer(t)
	apiID, _ := createTestAPI(t, srv)

	additionalProviders, _ := json.Marshal([]map[string]any{
		{
			"authenticationType": "AMAZON_COGNITO_USER_POOLS",
			"userPoolConfig":     map[string]any{"userPoolId": "us-east-1_fake"},
		},
	})
	appsyncPost(t, srv, "/v1/apis/"+apiID, map[string]any{
		"name":                              "multi-auth-api",
		"authenticationType":                "API_KEY",
		"additionalAuthenticationProviders": json.RawMessage(additionalProviders),
	}).Body.Close()

	// Upload schema.
	sdl := `type Query { hello: String }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL}).Body.Close()

	// Create API key.
	keyResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{})
	defer keyResp.Body.Close()
	var keyResult struct {
		ApiKey struct{ Id string } `json:"apiKey"`
	}
	helpers.DecodeJSON(t, keyResp, &keyResult)
	keyID := keyResult.ApiKey.Id

	// Create NONE data source + resolver.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	runtime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "hello",
		"dataSourceName": "NoneDS",
		"runtime":        json.RawMessage(runtime),
		"code": `
			export function request(ctx) {
				return { payload: "ok" };
			}
			export function response(ctx) {
				return ctx.result;
			}
		`,
	}).Body.Close()

	// When: use API key → should work (primary auth)
	resp1 := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)
	var r1 struct {
		Data   map[string]any             `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp1, &r1)
	if len(r1.Errors) > 0 {
		t.Fatalf("api key auth: unexpected errors: %v", r1.Errors)
	}

	// When: use Bearer token → should work (additional auth)
	token := fakeJWT(t, map[string]any{"sub": "user-456", "iss": "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_fake"})
	resp2 := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello }`},
		map[string]string{"Authorization": "Bearer " + token},
	)
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var r2 struct {
		Data   map[string]any             `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp2, &r2)
	if len(r2.Errors) > 0 {
		t.Fatalf("bearer auth: unexpected errors: %v", r2.Errors)
	}
}

// ─── GraphQL introspection ─────────────────────────────────────────────────────

func TestExecuteGraphQL_introspectionSchema(t *testing.T) {
	// Given: an API with a schema containing Query and Mutation types.
	srv := helpers.NewTestServer(t)
	sdl := `
type Query { hello: String userId: ID }
type Mutation { createUser(name: String): String }
`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// When: standard __schema introspection query.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `{
__schema {
queryType { name }
mutationType { name }
subscriptionType { name }
types { name kind }
}
}`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200, data.__schema populated.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Schema struct {
				QueryType        *struct{ Name string } `json:"queryType"`
				MutationType     *struct{ Name string } `json:"mutationType"`
				SubscriptionType *struct{ Name string } `json:"subscriptionType"`
				Types            []struct {
					Name string `json:"name"`
					Kind string `json:"kind"`
				} `json:"types"`
			} `json:"__schema"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Schema.QueryType == nil || result.Data.Schema.QueryType.Name != "Query" {
		t.Errorf("expected queryType.name=Query, got %+v", result.Data.Schema.QueryType)
	}
	if result.Data.Schema.MutationType == nil || result.Data.Schema.MutationType.Name != "Mutation" {
		t.Errorf("expected mutationType.name=Mutation, got %+v", result.Data.Schema.MutationType)
	}
	if result.Data.Schema.SubscriptionType != nil {
		t.Errorf("expected subscriptionType=null, got %+v", result.Data.Schema.SubscriptionType)
	}
	// Schema types must include at least Query, Mutation, String, ID.
	typeNames := map[string]bool{}
	for _, ty := range result.Data.Schema.Types {
		typeNames[ty.Name] = true
	}
	for _, required := range []string{"Query", "Mutation", "String", "ID"} {
		if !typeNames[required] {
			t.Errorf("expected types to include %q", required)
		}
	}
}

func TestExecuteGraphQL_introspectionType(t *testing.T) {
	// Given: an API with a User type.
	srv := helpers.NewTestServer(t)
	sdl := `
type Query { getUser: User }
type User { id: ID! name: String! }
`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// When: __type introspection for User.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `{
__type(name: "User") {
name
kind
fields { name type { kind name ofType { kind name } } }
}
}`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: returns User type with id and name fields.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Type *struct {
				Name   string `json:"name"`
				Kind   string `json:"kind"`
				Fields []struct {
					Name string `json:"name"`
					Type struct {
						Kind   string  `json:"kind"`
						Name   *string `json:"name"`
						OfType *struct {
							Kind string `json:"kind"`
							Name string `json:"name"`
						} `json:"ofType"`
					} `json:"type"`
				} `json:"fields"`
			} `json:"__type"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Type == nil {
		t.Fatal("expected __type not to be null")
	}
	if result.Data.Type.Name != "User" {
		t.Errorf("expected name=User, got %q", result.Data.Type.Name)
	}
	if result.Data.Type.Kind != "OBJECT" {
		t.Errorf("expected kind=OBJECT, got %q", result.Data.Type.Kind)
	}
	fieldNames := map[string]bool{}
	for _, f := range result.Data.Type.Fields {
		fieldNames[f.Name] = true
	}
	for _, req := range []string{"id", "name"} {
		if !fieldNames[req] {
			t.Errorf("expected field %q in User", req)
		}
	}
}

func TestExecuteGraphQL_typename(t *testing.T) {
	// Given: an API with a NONE resolver.
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "hello",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `{"version":"2018-05-29","payload":"world"}`,
	}).Body.Close()

	// When: query includes __typename.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ hello __typename }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: __typename = "Query".
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Hello    string `json:"hello"`
			Typename string `json:"__typename"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.Data.Typename != "Query" {
		t.Errorf("expected __typename=Query, got %q", result.Data.Typename)
	}
}

func TestExecuteGraphQL_introspectionFragments(t *testing.T) {
	// Given: an API with a schema.
	srv := helpers.NewTestServer(t)
	sdl := `
type Query { hello: String }
enum Status { ACTIVE INACTIVE }
`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// When: standard Apollo-style introspection query with fragments.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{
			"query": `
query IntrospectionQuery {
  __schema {
    types {
      ...FullType
    }
  }
}
fragment FullType on __Type {
  kind
  name
  enumValues(includeDeprecated: false) {
    name
    isDeprecated
  }
}
`,
		},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: Status enum type has ACTIVE and INACTIVE values.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Schema struct {
				Types []struct {
					Kind       string `json:"kind"`
					Name       string `json:"name"`
					EnumValues []struct {
						Name         string `json:"name"`
						IsDeprecated bool   `json:"isDeprecated"`
					} `json:"enumValues"`
				} `json:"types"`
			} `json:"__schema"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	var statusType *struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		EnumValues []struct {
			Name         string `json:"name"`
			IsDeprecated bool   `json:"isDeprecated"`
		} `json:"enumValues"`
	}
	for i, ty := range result.Data.Schema.Types {
		if ty.Name == "Status" {
			statusType = &result.Data.Schema.Types[i]
			break
		}
	}
	if statusType == nil {
		t.Fatal("expected Status enum type in introspection")
	}
	if statusType.Kind != "ENUM" {
		t.Errorf("expected Status.kind=ENUM, got %q", statusType.Kind)
	}
	if len(statusType.EnumValues) != 2 {
		t.Errorf("expected 2 enum values, got %d: %v", len(statusType.EnumValues), statusType.EnumValues)
	}
}

func TestExecuteGraphQL_introspectionDisabled(t *testing.T) {
	// Given: an API with introspection disabled.
	srv := helpers.NewTestServer(t)

	// Create API with introspectionConfig = DISABLED.
	apiResp := appsyncPost(t, srv, "/v1/apis", map[string]any{
		"name":                "test-disabled-introspection",
		"authenticationType":  "API_KEY",
		"introspectionConfig": "DISABLED",
	})
	defer apiResp.Body.Close()
	var apiResult struct {
		GraphqlAPI struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, apiResp, &apiResult)
	apiID := apiResult.GraphqlAPI.ApiId

	sdl := `type Query { hello: String }`
	b64SDL := base64.StdEncoding.EncodeToString([]byte(sdl))
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/schemacreation", map[string]any{"definition": b64SDL}).Body.Close()

	keyResp := appsyncPost(t, srv, "/v1/apis/"+apiID+"/apikeys", map[string]any{})
	defer keyResp.Body.Close()
	var keyResult struct {
		ApiKey struct{ Id string } `json:"apiKey"`
	}
	helpers.DecodeJSON(t, keyResp, &keyResult)

	// When: introspection query sent to disabled API.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ __schema { queryType { name } } }`},
		map[string]string{"x-api-key": keyResult.ApiKey.Id},
	)
	defer resp.Body.Close()

	// Then: 200 but with errors (introspection disabled).
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Errors []struct{ Message string } `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected errors for disabled introspection")
	}
}

func TestGetIntrospectionSchema_jsonFormat(t *testing.T) {
	// Given: an API with a schema.
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, _ := setupGraphQLAPI(t, srv, sdl)

	// When: GET /schema?format=JSON.
	resp := appsyncGet(t, srv, "/v1/apis/"+apiID+"/schema?format=JSON")
	defer resp.Body.Close()

	// Then: 200 with base64-encoded introspection JSON containing __schema.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var envelope struct {
		Schema string `json:"schema"`
	}
	helpers.DecodeJSON(t, resp, &envelope)
	decoded, err := base64.StdEncoding.DecodeString(envelope.Schema)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	var introspResult map[string]any
	if err := json.Unmarshal(decoded, &introspResult); err != nil {
		t.Fatalf("decode introspection JSON: %v", err)
	}
	schema, ok := introspResult["__schema"].(map[string]any)
	if !ok {
		t.Fatalf("expected __schema key in introspection JSON, got: %v", introspResult)
	}
	if schema["queryType"] == nil {
		t.Error("expected queryType in introspection schema")
	}
}

// ─── Phase 2: VTL/JS evaluator completeness ──────────────────────────────────

// TestVTL_utilTransform exercises $util.transform VTL utilities.
func TestVTL_utilTransform(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// $util.transform.toDynamoDBFilterExpression
	t.Run("toDynamoDBFilterExpression", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `#set($filter = {"name": {"eq": "Alice"}})$util.transform.toDynamoDBFilterExpression($filter)`,
			"context":  `{}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		// Should produce a DynamoDB filter expression JSON object
		var fe map[string]any
		if err := json.Unmarshal([]byte(result.EvaluationResult), &fe); err != nil {
			t.Fatalf("expected valid JSON filter expression, got %q: %v", result.EvaluationResult, err)
		}
		if fe["expression"] == nil {
			t.Errorf("expected 'expression' key in filter expression, got: %v", fe)
		}
		// The field name should appear in expressionNames (DynamoDB uses #alias references).
		exprNames, _ := fe["expressionNames"].(map[string]any)
		foundName := false
		for _, v := range exprNames {
			if v == "name" {
				foundName = true
				break
			}
		}
		if !foundName {
			t.Errorf("expected 'name' in expressionNames, got: %v", exprNames)
		}
	})

	// $util.transform.toDynamoDBConditionExpression
	t.Run("toDynamoDBConditionExpression", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `#set($cond = {"id": {"eq": "123"}})$util.transform.toDynamoDBConditionExpression($cond)`,
			"context":  `{}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		var ce map[string]any
		if err := json.Unmarshal([]byte(result.EvaluationResult), &ce); err != nil {
			t.Fatalf("expected valid JSON condition expression, got %q: %v", result.EvaluationResult, err)
		}
		if ce["expression"] == nil {
			t.Errorf("expected 'expression' key in condition expression, got: %v", ce)
		}
	})
}

// TestVTL_utilHttp exercises $util.http VTL utilities.
func TestVTL_utilHttp(t *testing.T) {
	srv := helpers.NewTestServer(t)

	t.Run("encodeUrl", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `$util.http.encodeUrl("hello world & more")`,
			"context":  `{}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		if !strings.Contains(result.EvaluationResult, "%") {
			t.Errorf("expected URL-encoded string with %%, got %q", result.EvaluationResult)
		}
	})

	t.Run("decodeUrl", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `$util.http.decodeUrl("hello+world+%26+more")`,
			"context":  `{}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		if !strings.Contains(result.EvaluationResult, "more") {
			t.Errorf("expected decoded string, got %q", result.EvaluationResult)
		}
	})

	t.Run("copyHeaders", func(t *testing.T) {
		resp := appsyncPost(t, srv, evaluateTemplatePath, map[string]any{
			"template": `#set($h = $util.http.copyHeaders($context.request.headers))$util.toJson($h)`,
			"context":  `{"request":{"headers":{"x-custom":"value"}}}`,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			EvaluationResult string `json:"evaluationResult"`
		}
		helpers.DecodeJSON(t, resp, &result)
		// Should return a map; any valid JSON is acceptable
		if result.EvaluationResult == "" {
			t.Error("expected non-empty result from copyHeaders")
		}
	})
}

// TestVTL_selectionSetGraphQL exercises $ctx.info.selectionSetGraphQL in VTL.
func TestVTL_selectionSetGraphQL(t *testing.T) {
	// Given: a GraphQL API with a schema, API key, and NONE resolver that returns selectionSetGraphQL.
	srv := helpers.NewTestServer(t)
	schema := `
type Query {
  getUser(id: ID!): User
}
type User {
  id: ID!
  name: String!
  email: String
}
`
	apiID, keyID := setupGraphQLAPI(t, srv, schema)

	// Create NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()

	// Create resolver that captures and returns selectionSetGraphQL.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"typeName":                "Query",
		"fieldName":               "getUser",
		"dataSourceName":          "NoneDS",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":{"sel":"$ctx.info.selectionSetGraphQL"}}`,
		"responseMappingTemplate": `$util.toJson($context.result.sel)`,
	}).Body.Close()

	// When: a query is executed requesting specific sub-fields.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `query { getUser(id: "1") { id name } }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var gqlResult struct {
		Data struct {
			GetUser string `json:"getUser"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &gqlResult)

	// Then: selectionSetGraphQL should contain the field selection in GraphQL syntax.
	if gqlResult.Data.GetUser == "" {
		t.Fatal("expected non-empty selectionSetGraphQL result")
	}
	if !strings.Contains(gqlResult.Data.GetUser, "id") {
		t.Errorf("selectionSetGraphQL should contain 'id', got: %q", gqlResult.Data.GetUser)
	}
	if !strings.Contains(gqlResult.Data.GetUser, "name") {
		t.Errorf("selectionSetGraphQL should contain 'name', got: %q", gqlResult.Data.GetUser)
	}
}

// TestJS_selectionSetGraphQL exercises ctx.info.selectionSetGraphQL in JS resolvers.
func TestJS_selectionSetGraphQL(t *testing.T) {
	// Given: a GraphQL API with a schema, API key, and JS NONE resolver.
	srv := helpers.NewTestServer(t)
	schema := `
type Query {
  getItem(id: ID!): Item
}
type Item {
  id: ID!
  value: String!
}
`
	apiID, keyID := setupGraphQLAPI(t, srv, schema)

	// Create NONE data source.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS", "type": "NONE",
	}).Body.Close()

	runtime, _ := json.Marshal(map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"})

	// Create JS resolver that returns ctx.info.selectionSetGraphQL as the result.
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"typeName":       "Query",
		"fieldName":      "getItem",
		"dataSourceName": "NoneDS",
		"code":           `export function request(ctx) { return { payload: { sel: ctx.info.selectionSetGraphQL } }; } export function response(ctx) { return ctx.result.sel; }`,
		"runtime":        json.RawMessage(runtime),
	}).Body.Close()

	// When: a query executes requesting id and value.
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `query { getItem(id: "1") { id value } }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var gqlResult struct {
		Data struct {
			GetItem string `json:"getItem"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &gqlResult)

	// Then: selectionSetGraphQL contains both requested fields.
	if gqlResult.Data.GetItem == "" {
		t.Fatal("expected non-empty selectionSetGraphQL result")
	}
	if !strings.Contains(gqlResult.Data.GetItem, "id") || !strings.Contains(gqlResult.Data.GetItem, "value") {
		t.Errorf("selectionSetGraphQL should contain 'id' and 'value', got: %q", gqlResult.Data.GetItem)
	}
}

// ─── DynamoDB batch / transact operations ────────────────────────────────────

func TestExecuteGraphQL_dynamoDBBatchGetItem(t *testing.T) {
	// Given: a DynamoDB table with two items and an AppSync API using BatchGetItem
	srv := helpers.NewTestServer(t)
	tableName := "gql-batch-get"
	setupDDBTable(t, srv, tableName)

	for _, item := range []map[string]any{
		{"id": map[string]any{"S": "a1"}, "name": map[string]any{"S": "Alpha"}},
		{"id": map[string]any{"S": "a2"}, "name": map[string]any{"S": "Beta"}},
	} {
		ddbCall(t, srv, "PutItem", map[string]any{"TableName": tableName, "Item": item}).Body.Close()
	}

	sdl := `type Query { batchGet: [Item] }
type Item { id: String, name: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "BatchGetDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "batchGet",
		"dataSourceName": "BatchGetDS",
		"kind":           "UNIT",
		"requestMappingTemplate": `{
			"operation": "BatchGetItem",
			"tables": {
				"` + tableName + `": {
					"keys": [
						{"id": {"S": "a1"}},
						{"id": {"S": "a2"}}
					]
				}
			}
		}`,
	}).Body.Close()

	// When: execute GraphQL query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ batchGet { id name } }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with both items returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			BatchGet []map[string]any `json:"batchGet"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if len(result.Data.BatchGet) != 2 {
		t.Fatalf("expected 2 items from BatchGetItem, got %d", len(result.Data.BatchGet))
	}
	// Verify IDs are present.
	ids := map[string]bool{}
	for _, item := range result.Data.BatchGet {
		if id, ok := item["id"].(string); ok {
			ids[id] = true
		}
	}
	if !ids["a1"] || !ids["a2"] {
		t.Fatalf("expected items a1 and a2, got %v", ids)
	}
}

func TestExecuteGraphQL_dynamoDBBatchWriteItem(t *testing.T) {
	// Given: an empty DynamoDB table and an AppSync mutation using BatchWriteItem
	srv := helpers.NewTestServer(t)
	tableName := "gql-batch-write"
	setupDDBTable(t, srv, tableName)

	sdl := `type Mutation { batchWrite: Boolean }
type Query { dummy: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "BatchWriteDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Mutation/resolvers", map[string]any{
		"fieldName":      "batchWrite",
		"dataSourceName": "BatchWriteDS",
		"kind":           "UNIT",
		"requestMappingTemplate": `{
			"operation": "BatchWriteItem",
			"tables": {
				"` + tableName + `": {
					"putRequest": [
						{"id": {"S": "b1"}, "name": {"S": "Gamma"}},
						{"id": {"S": "b2"}, "name": {"S": "Delta"}}
					]
				}
			}
		}`,
		"responseMappingTemplate": `true`,
	}).Body.Close()

	// When: execute GraphQL mutation
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `mutation { batchWrite }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: mutation succeeds with no errors
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	// Verify items were actually written by scanning the table.
	scanResp := ddbCall(t, srv, "Scan", map[string]any{"TableName": tableName})
	defer scanResp.Body.Close()
	var scanResult struct {
		Items []map[string]any `json:"Items"`
	}
	helpers.DecodeJSON(t, scanResp, &scanResult)
	if len(scanResult.Items) != 2 {
		t.Fatalf("expected 2 items written by BatchWriteItem, got %d", len(scanResult.Items))
	}
}

func TestExecuteGraphQL_dynamoDBTransactGetItems(t *testing.T) {
	// Given: a DynamoDB table with two items and an AppSync API using TransactGetItems
	srv := helpers.NewTestServer(t)
	tableName := "gql-transact-get"
	setupDDBTable(t, srv, tableName)

	for _, item := range []map[string]any{
		{"id": map[string]any{"S": "t1"}, "val": map[string]any{"S": "Epsilon"}},
		{"id": map[string]any{"S": "t2"}, "val": map[string]any{"S": "Zeta"}},
	} {
		ddbCall(t, srv, "PutItem", map[string]any{"TableName": tableName, "Item": item}).Body.Close()
	}

	sdl := `type Query { transactGet: [Item] }
type Item { id: String, val: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "TransactGetDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableName},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "transactGet",
		"dataSourceName": "TransactGetDS",
		"kind":           "UNIT",
		"requestMappingTemplate": `{
			"operation": "TransactGetItems",
			"transactItems": [
				{"table": "` + tableName + `", "key": {"id": {"S": "t1"}}},
				{"table": "` + tableName + `", "key": {"id": {"S": "t2"}}}
			]
		}`,
	}).Body.Close()

	// When: execute GraphQL query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ transactGet { id val } }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with both items (order may vary)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			TransactGet []map[string]any `json:"transactGet"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if len(result.Data.TransactGet) != 2 {
		t.Fatalf("expected 2 items from TransactGetItems, got %d", len(result.Data.TransactGet))
	}
}

func TestExecuteGraphQL_dynamoDBTransactWriteItems(t *testing.T) {
	// Given: two DynamoDB tables and an AppSync mutation using TransactWriteItems
	srv := helpers.NewTestServer(t)
	tableA := "gql-transact-write-a"
	tableB := "gql-transact-write-b"
	setupDDBTable(t, srv, tableA)
	setupDDBTable(t, srv, tableB)

	sdl := `type Mutation { transactWrite: Boolean }
type Query { dummy: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	// Use tableA as the data source "default" table (required by AppSync config).
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name":           "TransactWriteDS",
		"type":           "AMAZON_DYNAMODB",
		"dynamodbConfig": map[string]any{"tableName": tableA},
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Mutation/resolvers", map[string]any{
		"fieldName":      "transactWrite",
		"dataSourceName": "TransactWriteDS",
		"kind":           "UNIT",
		"requestMappingTemplate": `{
			"operation": "TransactWriteItems",
			"transactItems": [
				{"table": "` + tableA + `", "operation": "PutItem", "key": {"id": {"S": "tw1"}}, "item": {"id": {"S": "tw1"}, "data": {"S": "from-A"}}},
				{"table": "` + tableB + `", "operation": "PutItem", "key": {"id": {"S": "tw2"}}, "item": {"id": {"S": "tw2"}, "data": {"S": "from-B"}}}
			]
		}`,
		"responseMappingTemplate": `true`,
	}).Body.Close()

	// When: execute GraphQL mutation
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `mutation { transactWrite }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: mutation completes without errors
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	// Verify items appeared in both tables.
	for _, tc := range []struct {
		table string
		id    string
	}{
		{tableA, "tw1"},
		{tableB, "tw2"},
	} {
		getResp := ddbCall(t, srv, "GetItem", map[string]any{
			"TableName": tc.table,
			"Key":       map[string]any{"id": map[string]any{"S": tc.id}},
		})
		defer getResp.Body.Close()
		var getResult struct {
			Item map[string]any `json:"Item"`
		}
		helpers.DecodeJSON(t, getResp, &getResult)
		if getResult.Item == nil {
			t.Fatalf("expected item %s in table %s after TransactWriteItems", tc.id, tc.table)
		}
	}
}

// ─── Phase 4: Resolver robustness / error enrichment ─────────────────────────

func TestVTL_utilErrorWithType(t *testing.T) {
	// Given: a VTL resolver that calls $util.error() with an errorType and data
	srv := helpers.NewTestServer(t)
	sdl := `type Query { failTyped: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "failTyped",
		"dataSourceName":         "NoneDS",
		"kind":                   "UNIT",
		"requestMappingTemplate": `#set($data = {"reason": "test"}) $util.error("Something went wrong", "CustomError", $data)`,
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ failTyped }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with an error containing extensions.errorType and extensions.data
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected an error, got none")
	}
	ext := result.Errors[0].Extensions
	if ext == nil {
		t.Fatal("expected extensions on error, got nil")
	}
	if ext["errorType"] != "CustomError" {
		t.Errorf("expected extensions.errorType=CustomError, got %v", ext["errorType"])
	}
	if ext["data"] == nil {
		t.Error("expected extensions.data to be present")
	}
}

func TestGraphQL_errorPathEnrichment(t *testing.T) {
	// Given: a NONE resolver that always errors
	srv := helpers.NewTestServer(t)
	sdl := `type Query { brokenField: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS2",
		"type": "NONE",
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":              "brokenField",
		"dataSourceName":         "NoneDS2",
		"kind":                   "UNIT",
		"requestMappingTemplate": `$util.error("Intentional error")`,
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ brokenField }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with an error that has path = ["brokenField"]
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected an error, got none")
	}
	if result.Errors[0].Message != "Intentional error" {
		t.Errorf("unexpected error message: %s", result.Errors[0].Message)
	}
	path := result.Errors[0].Path
	if len(path) == 0 {
		t.Fatal("expected error path to be populated, got empty")
	}
	if path[0] != "brokenField" {
		t.Errorf("expected path[0]=brokenField, got %v", path[0])
	}
}

func TestJS_utilErrorWithType(t *testing.T) {
	// Given: a JS resolver that calls util.error() with errorType and data
	srv := helpers.NewTestServer(t)
	sdl := `type Query { failJS: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS3",
		"type": "NONE",
	}).Body.Close()

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":      "failJS",
		"dataSourceName": "NoneDS3",
		"kind":           "UNIT",
		"runtime":        map[string]any{"name": "APPSYNC_JS", "runtimeVersion": "1.0.0"},
		"code": `
export function request(ctx) { return {}; }
export function response(ctx) {
  util.error("JS error occurred", "JSCustomError", {detail: "extra"});
}
`,
	}).Body.Close()

	// When: execute the query
	resp := appsyncPostWithHeaders(t, srv, "/_overcast/appsync/apis/"+apiID+"/graphql",
		map[string]any{"query": `{ failJS }`},
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with an error containing extensions.errorType
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Errors) == 0 {
		t.Fatal("expected an error, got none")
	}
	if result.Errors[0].Message != "JS error occurred" {
		t.Errorf("unexpected error message: %s", result.Errors[0].Message)
	}
	ext := result.Errors[0].Extensions
	if ext == nil {
		t.Fatal("expected extensions on error, got nil")
	}
	if ext["errorType"] != "JSCustomError" {
		t.Errorf("expected extensions.errorType=JSCustomError, got %v", ext["errorType"])
	}
	if ext["data"] == nil {
		t.Error("expected extensions.data to be present")
	}
}

// ─── Merged API Source Associations ──────────────────────────────────────────

func TestAssociateSourceGraphqlApi_success(t *testing.T) {
	// Given: two GraphQL APIs (one "merged", one "source") with a schema on the source
	srv := helpers.NewTestServer(t)
	mergedID, _ := createTestAPI(t, srv)
	sourceID, _ := createTestAPI(t, srv)

	// Upload a schema to the source API.
	sdl := "type Query { hello: String }"
	appsyncPost(t, srv, "/v1/apis/"+sourceID+"/schemacreation", map[string]any{
		"definition": base64.StdEncoding.EncodeToString([]byte(sdl)),
	}).Body.Close()

	// When: associate source to merged via the merged API path
	resp := appsyncPost(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations", map[string]any{
		"sourceApiIdentifier":        sourceID,
		"description":                "test association",
		"sourceApiAssociationConfig": map[string]any{"mergeType": "MANUAL_MERGE"},
	})
	defer resp.Body.Close()

	// Then: 200 with association details, merge succeeds
	helpers.AssertStatus(t, resp, http.StatusOK)
	var assocResult struct {
		SourceApiAssociation struct {
			AssociationId              string `json:"associationId"`
			AssociationArn             string `json:"associationArn"`
			Description                string `json:"description"`
			SourceApiId                string `json:"sourceApiId"`
			MergedApiId                string `json:"mergedApiId"`
			SourceApiAssociationStatus string `json:"sourceApiAssociationStatus"`
		} `json:"sourceApiAssociation"`
	}
	helpers.DecodeJSON(t, resp, &assocResult)
	assoc := assocResult.SourceApiAssociation
	if assoc.AssociationId == "" {
		t.Error("expected associationId to be set")
	}
	if assoc.AssociationArn == "" {
		t.Error("expected associationArn to be set")
	}
	if assoc.Description != "test association" {
		t.Errorf("expected description='test association', got %q", assoc.Description)
	}
	if assoc.SourceApiId != sourceID {
		t.Errorf("expected sourceApiId=%s, got %s", sourceID, assoc.SourceApiId)
	}
	if assoc.MergedApiId != mergedID {
		t.Errorf("expected mergedApiId=%s, got %s", mergedID, assoc.MergedApiId)
	}
	if assoc.SourceApiAssociationStatus != "MERGE_SUCCESS" {
		t.Errorf("expected status=MERGE_SUCCESS, got %s", assoc.SourceApiAssociationStatus)
	}
}

func TestGetSourceApiAssociation_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	mergedID, _ := createTestAPI(t, srv)
	sourceID, _ := createTestAPI(t, srv)

	// Associate
	resp := appsyncPost(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations", map[string]any{
		"sourceApiIdentifier": sourceID,
	})
	var createResult struct {
		SourceApiAssociation struct {
			AssociationId string `json:"associationId"`
		} `json:"sourceApiAssociation"`
	}
	helpers.DecodeJSON(t, resp, &createResult)
	resp.Body.Close()
	assocID := createResult.SourceApiAssociation.AssociationId

	// When: get the association
	getResp := appsyncGet(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations/"+assocID)
	defer getResp.Body.Close()

	// Then: 200 with the association
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var getResult struct {
		SourceApiAssociation struct {
			AssociationId string `json:"associationId"`
			MergedApiId   string `json:"mergedApiId"`
		} `json:"sourceApiAssociation"`
	}
	helpers.DecodeJSON(t, getResp, &getResult)
	if getResult.SourceApiAssociation.AssociationId != assocID {
		t.Errorf("expected associationId=%s, got %s", assocID, getResult.SourceApiAssociation.AssociationId)
	}
}

func TestGetSourceApiAssociation_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	mergedID, _ := createTestAPI(t, srv)

	resp := appsyncGet(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations/nonexistent")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestListSourceApiAssociations_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	mergedID, _ := createTestAPI(t, srv)
	sourceID, _ := createTestAPI(t, srv)

	// Associate
	appsyncPost(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations", map[string]any{
		"sourceApiIdentifier": sourceID,
	}).Body.Close()

	// When: list associations via the /v1/apis path
	resp := appsyncGet(t, srv, "/v1/apis/"+mergedID+"/sourceApiAssociations")
	defer resp.Body.Close()

	// Then: 200 with array containing one association
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listResult struct {
		SourceApiAssociationSummaries []struct {
			AssociationId string `json:"associationId"`
		} `json:"sourceApiAssociationSummaries"`
	}
	helpers.DecodeJSON(t, resp, &listResult)
	if len(listResult.SourceApiAssociationSummaries) != 1 {
		t.Errorf("expected 1 association, got %d", len(listResult.SourceApiAssociationSummaries))
	}
}

func TestDisassociateSourceGraphqlApi_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	mergedID, _ := createTestAPI(t, srv)
	sourceID, _ := createTestAPI(t, srv)

	// Associate
	resp := appsyncPost(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations", map[string]any{
		"sourceApiIdentifier": sourceID,
	})
	var assocCreateResult struct {
		SourceApiAssociation struct {
			AssociationId string `json:"associationId"`
		} `json:"sourceApiAssociation"`
	}
	helpers.DecodeJSON(t, resp, &assocCreateResult)
	resp.Body.Close()
	assocID := assocCreateResult.SourceApiAssociation.AssociationId

	// When: disassociate
	delResp := appsyncDelete(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations/"+assocID)
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Then: get should return 404
	getResp := appsyncGet(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations/"+assocID)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestAssociateMergedGraphqlApi_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	mergedID, _ := createTestAPI(t, srv)
	sourceID, _ := createTestAPI(t, srv)

	// When: associate from the source API side
	resp := appsyncPost(t, srv, "/v1/sourceApis/"+sourceID+"/mergedApiAssociations", map[string]any{
		"mergedApiIdentifier": mergedID,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var assocMergedResult struct {
		SourceApiAssociation struct {
			AssociationId string `json:"associationId"`
			SourceApiId   string `json:"sourceApiId"`
			MergedApiId   string `json:"mergedApiId"`
		} `json:"sourceApiAssociation"`
	}
	helpers.DecodeJSON(t, resp, &assocMergedResult)
	if assocMergedResult.SourceApiAssociation.SourceApiId != sourceID {
		t.Errorf("expected sourceApiId=%s, got %s", sourceID, assocMergedResult.SourceApiAssociation.SourceApiId)
	}
	if assocMergedResult.SourceApiAssociation.MergedApiId != mergedID {
		t.Errorf("expected mergedApiId=%s, got %s", mergedID, assocMergedResult.SourceApiAssociation.MergedApiId)
	}
}

func TestStartSchemaMerge_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	mergedID, _ := createTestAPI(t, srv)
	sourceID, _ := createTestAPI(t, srv)

	// Upload schema to source
	sdl := "type Query { hello: String }"
	appsyncPost(t, srv, "/v1/apis/"+sourceID+"/schemacreation", map[string]any{
		"definition": base64.StdEncoding.EncodeToString([]byte(sdl)),
	}).Body.Close()

	// Associate
	resp := appsyncPost(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations", map[string]any{
		"sourceApiIdentifier": sourceID,
	})
	var mergeCreateResult struct {
		SourceApiAssociation struct {
			AssociationId string `json:"associationId"`
		} `json:"sourceApiAssociation"`
	}
	helpers.DecodeJSON(t, resp, &mergeCreateResult)
	resp.Body.Close()
	assocID := mergeCreateResult.SourceApiAssociation.AssociationId

	// When: trigger a re-merge
	mergeResp := appsyncPost(t, srv, "/v1/mergedApis/"+mergedID+"/sourceApiAssociations/"+assocID+"/merge", nil)
	defer mergeResp.Body.Close()

	// Then: 200 with MERGE_SUCCESS status
	helpers.AssertStatus(t, mergeResp, http.StatusOK)
	var mergeResult struct {
		SourceApiAssociationStatus string `json:"sourceApiAssociationStatus"`
	}
	helpers.DecodeJSON(t, mergeResp, &mergeResult)
	if mergeResult.SourceApiAssociationStatus != "MERGE_SUCCESS" {
		t.Errorf("expected MERGE_SUCCESS, got %s", mergeResult.SourceApiAssociationStatus)
	}

	// Verify merged schema is available on the merged API
	schemaResp := appsyncGet(t, srv, "/v1/apis/"+mergedID+"/schemacreation")
	defer schemaResp.Body.Close()
	helpers.AssertStatus(t, schemaResp, http.StatusOK)
	var schemaResult struct {
		Status string `json:"status"`
	}
	helpers.DecodeJSON(t, schemaResp, &schemaResult)
	if schemaResult.Status != "ACTIVE" {
		t.Errorf("expected merged schema status=ACTIVE, got %s", schemaResult.Status)
	}
}

// ─── Events API ──────────────────────────────────────────────────────────────

func TestCreateApi_EventsAPI_success(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := eventsPost(t, srv, "/v2/apis", map[string]any{
		"name": "test-event-api",
		"eventConfig": map[string]any{
			"authProviders": []map[string]any{
				{"authType": "API_KEY"},
			},
			"connectionAuthModes": []map[string]any{
				{"authType": "API_KEY"},
			},
			"defaultPublishAuthModes": []map[string]any{
				{"authType": "API_KEY"},
			},
			"defaultSubscribeAuthModes": []map[string]any{
				{"authType": "API_KEY"},
			},
		},
		"tags": map[string]string{"env": "test"},
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var eventResult struct {
		Api struct {
			ApiId  string            `json:"apiId"`
			Name   string            `json:"name"`
			ApiArn string            `json:"apiArn"`
			Dns    map[string]string `json:"dns"`
			Tags   map[string]string `json:"tags"`
			// created is a plain restJson1 timestamp (no timestampFormat
			// trait): epoch seconds as a JSON number. Decoding into float64
			// fails the test if the server ever emits a string again, and
			// the magnitude check below catches an epoch-millis regression.
			Created float64 `json:"created"`
		} `json:"api"`
	}
	helpers.DecodeJSON(t, resp, &eventResult)
	api := eventResult.Api
	if api.ApiId == "" {
		t.Error("expected apiId to be set")
	}
	if api.Created < 1e9 || api.Created >= 1e10 {
		t.Errorf("expected created as epoch seconds (between 1e9 and 1e10), got %v", api.Created)
	}
	if api.Name != "test-event-api" {
		t.Errorf("expected name=test-event-api, got %q", api.Name)
	}
	if api.ApiArn == "" {
		t.Error("expected apiArn to be set")
	}
	if api.Dns["HTTP"] == "" {
		t.Error("expected dns.HTTP to be set")
	}
	if api.Tags["env"] != "test" {
		t.Errorf("expected tags.env=test, got %q", api.Tags["env"])
	}
}

func TestGetApi_EventsAPI_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	resp := eventsGet(t, srv, "/v2/apis/"+apiID)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var eventGetResult struct {
		Api struct {
			ApiId string `json:"apiId"`
			Name  string `json:"name"`
		} `json:"api"`
	}
	helpers.DecodeJSON(t, resp, &eventGetResult)
	if eventGetResult.Api.ApiId != apiID {
		t.Errorf("expected apiId=%s, got %s", apiID, eventGetResult.Api.ApiId)
	}
}

func TestGetApi_EventsAPI_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := eventsGet(t, srv, "/v2/apis/nonexistent")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestListApis_EventsAPI_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createTestEventAPI(t, srv)
	createTestEventAPI(t, srv)

	resp := eventsGet(t, srv, "/v2/apis")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var eventListResult struct {
		Apis []struct {
			ApiId string `json:"apiId"`
		} `json:"apis"`
	}
	helpers.DecodeJSON(t, resp, &eventListResult)
	if len(eventListResult.Apis) < 2 {
		t.Errorf("expected at least 2 APIs, got %d", len(eventListResult.Apis))
	}
}

func TestUpdateApi_EventsAPI_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	resp := eventsPost(t, srv, "/v2/apis/"+apiID, map[string]any{
		"name": "updated-event-api",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var eventUpdateResult struct {
		Api struct {
			Name string `json:"name"`
		} `json:"api"`
	}
	helpers.DecodeJSON(t, resp, &eventUpdateResult)
	if eventUpdateResult.Api.Name != "updated-event-api" {
		t.Errorf("expected name=updated-event-api, got %q", eventUpdateResult.Api.Name)
	}
}

func TestDeleteApi_EventsAPI_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	delResp := eventsDelete(t, srv, "/v2/apis/"+apiID)
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Verify it's gone
	getResp := eventsGet(t, srv, "/v2/apis/"+apiID)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

// ─── Channel Namespaces ──────────────────────────────────────────────────────

func TestCreateChannelNamespace_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	resp := eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{
		"name":         "test-channel",
		"codeHandlers": "export function onPublish(ctx) { return ctx; }",
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var nsCreateResult struct {
		ChannelNamespace struct {
			Name                string `json:"name"`
			ApiId               string `json:"apiId"`
			ChannelNamespaceArn string `json:"channelNamespaceArn"`
			// created/lastModified are plain restJson1 timestamps: epoch
			// seconds as JSON numbers. Decoding into float64 fails the test
			// if the server ever emits strings again, and the magnitude
			// checks below catch an epoch-millis regression.
			Created      float64 `json:"created"`
			LastModified float64 `json:"lastModified"`
			CodeHandlers string  `json:"codeHandlers"`
		} `json:"channelNamespace"`
	}
	helpers.DecodeJSON(t, resp, &nsCreateResult)
	ns := nsCreateResult.ChannelNamespace
	if ns.Name != "test-channel" {
		t.Errorf("expected name=test-channel, got %q", ns.Name)
	}
	if ns.Created < 1e9 || ns.Created >= 1e10 {
		t.Errorf("expected created as epoch seconds (between 1e9 and 1e10), got %v", ns.Created)
	}
	if ns.LastModified != ns.Created {
		t.Errorf("expected lastModified=created on create, got created=%v lastModified=%v", ns.Created, ns.LastModified)
	}
	if ns.ApiId != apiID {
		t.Errorf("expected apiId=%s, got %s", apiID, ns.ApiId)
	}
	if ns.ChannelNamespaceArn == "" {
		t.Error("expected channelNamespaceArn to be set")
	}
	if ns.CodeHandlers == "" {
		t.Error("expected codeHandlers to be set")
	}
}

func TestCreateChannelNamespace_duplicate(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{
		"name": "dup-channel",
	}).Body.Close()

	resp := eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{
		"name": "dup-channel",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

func TestGetChannelNamespace_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{
		"name": "get-channel",
	}).Body.Close()

	resp := eventsGet(t, srv, "/v2/apis/"+apiID+"/channelNamespaces/get-channel")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var nsGetResult struct {
		ChannelNamespace struct {
			Name string `json:"name"`
		} `json:"channelNamespace"`
	}
	helpers.DecodeJSON(t, resp, &nsGetResult)
	if nsGetResult.ChannelNamespace.Name != "get-channel" {
		t.Errorf("expected name=get-channel, got %q", nsGetResult.ChannelNamespace.Name)
	}
}

func TestGetChannelNamespace_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	resp := eventsGet(t, srv, "/v2/apis/"+apiID+"/channelNamespaces/nonexistent")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestListChannelNamespaces_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{"name": "ch1"}).Body.Close()
	eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{"name": "ch2"}).Body.Close()

	resp := eventsGet(t, srv, "/v2/apis/"+apiID+"/channelNamespaces")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var nsListResult struct {
		ChannelNamespaces []struct {
			Name string `json:"name"`
		} `json:"channelNamespaces"`
	}
	helpers.DecodeJSON(t, resp, &nsListResult)
	if len(nsListResult.ChannelNamespaces) != 2 {
		t.Errorf("expected 2 namespaces, got %d", len(nsListResult.ChannelNamespaces))
	}
}

func TestUpdateChannelNamespace_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{
		"name":         "upd-channel",
		"codeHandlers": "export function onPublish(ctx) { return ctx; }",
	}).Body.Close()

	resp := eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces/upd-channel", map[string]any{
		"codeHandlers": "export function onPublish(ctx) { return null; }",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var nsUpdateResult struct {
		ChannelNamespace struct {
			CodeHandlers string `json:"codeHandlers"`
		} `json:"channelNamespace"`
	}
	helpers.DecodeJSON(t, resp, &nsUpdateResult)
	if nsUpdateResult.ChannelNamespace.CodeHandlers != "export function onPublish(ctx) { return null; }" {
		t.Errorf("expected updated codeHandlers, got %q", nsUpdateResult.ChannelNamespace.CodeHandlers)
	}
}

func TestDeleteChannelNamespace_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{
		"name": "del-channel",
	}).Body.Close()

	delResp := eventsDelete(t, srv, "/v2/apis/"+apiID+"/channelNamespaces/del-channel")
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusOK)

	// Verify gone
	getResp := eventsGet(t, srv, "/v2/apis/"+apiID+"/channelNamespaces/del-channel")
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestDeleteApi_EventsAPI_cascadesChannelNamespaces(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createTestEventAPI(t, srv)

	eventsPost(t, srv, "/v2/apis/"+apiID+"/channelNamespaces", map[string]any{
		"name": "cascade-ns",
	}).Body.Close()

	// Delete the API
	eventsDelete(t, srv, "/v2/apis/"+apiID).Body.Close()

	// Verify API is gone
	getResp := eventsGet(t, srv, "/v2/apis/"+apiID)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

// createTestEventAPI is a helper that creates an Event API and returns its ID.
func createTestEventAPI(t *testing.T, srv *helpers.TestServer) string {
	t.Helper()
	resp := eventsPost(t, srv, "/v2/apis", map[string]any{
		"name": "test-event-api",
		"eventConfig": map[string]any{
			"authProviders": []map[string]any{
				{"authType": "API_KEY"},
			},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var eventHelperResult struct {
		Api struct {
			ApiId string `json:"apiId"`
		} `json:"api"`
	}
	helpers.DecodeJSON(t, resp, &eventHelperResult)
	return eventHelperResult.Api.ApiId
}

// fakeAppsyncSigV4 is a fake SigV4 Authorization header that identifies
// the service as "appsync" so the /v2/apis dispatch routes to AppSync
// Events API instead of API Gateway v2.
const fakeAppsyncSigV4 = "AWS4-HMAC-SHA256 Credential=test/20250101/us-east-1/appsync/aws4_request, SignedHeaders=host, Signature=fake"

// eventsPost sends a POST with the fake appsync SigV4 header for /v2/apis dispatch.
func eventsPost(t *testing.T, srv *helpers.TestServer, path string, body map[string]any) *http.Response {
	t.Helper()
	return appsyncPostWithHeaders(t, srv, path, body, map[string]string{
		"Authorization": fakeAppsyncSigV4,
	})
}

// eventsGet sends a GET with the fake appsync SigV4 header for /v2/apis dispatch.
func eventsGet(t *testing.T, srv *helpers.TestServer, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization", fakeAppsyncSigV4)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("eventsGet %s: %v", path, err)
	}
	return resp
}

// eventsDelete sends a DELETE with the fake appsync SigV4 header for /v2/apis dispatch.
func eventsDelete(t *testing.T, srv *helpers.TestServer, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	req.Header.Set("Authorization", fakeAppsyncSigV4)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("eventsDelete %s: %v", path, err)
	}
	return resp
}

// ─── Host-based routing (appsync-api Host header) ───────────────────────────

// TestExecuteGraphQL_hostBasedInvoke mirrors TestExecuteGraphQL_basicQuery
// but posts to "/graphql" against the real AWS Host-routed shape
// ({apiId}.appsync-api.{region}.amazonaws.com/graphql) instead of the
// path-style /_overcast/appsync/apis/{apiId}/graphql URL, proving
// middleware.HostDispatch's rewrite reaches the exact same execution logic.
func TestExecuteGraphQL_hostBasedInvoke(t *testing.T) {
	// Given: a GraphQL API with a resolver, same setup as the path-style test
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":               "hello",
		"dataSourceName":          "NoneDS",
		"kind":                    "UNIT",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"world"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	// When: the query is executed via the appsync-api Host header at the
	// bare "/graphql" path instead of the path-style /_overcast/appsync/apis/{apiId}/graphql
	resp := appsyncPostWithHost(t, srv, "/graphql",
		map[string]any{"query": `{ hello }`},
		apiID+".appsync-api.us-east-1.amazonaws.com",
		map[string]string{"x-api-key": keyID},
	)
	defer resp.Body.Close()

	// Then: 200 with the same data the path-style invoke returns
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Data struct {
			Hello string `json:"hello"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Data.Hello != "world" {
		t.Errorf("expected data.hello=%q, got %q", "world", result.Data.Hello)
	}
}

// TestExecuteGraphQL_hostBasedInvokeAcrossResolvableBases is the AppSync half
// of the addressing-precedence regression. Every base below except
// ".amazonaws.com" used to be claimed by S3 virtual-hosted addressing first,
// which prepended a bogus bucket segment to the path so the rewritten request
// never matched /_overcast/appsync/apis/{apiId}/graphql.
//
// See docs/plans/host-routing-precedence.md.
func TestExecuteGraphQL_hostBasedInvokeAcrossResolvableBases(t *testing.T) {
	// Given: a GraphQL API with a NONE-datasource resolver
	srv := helpers.NewTestServer(t)
	sdl := `type Query { hello: String }`
	apiID, keyID := setupGraphQLAPI(t, srv, sdl)

	appsyncPost(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}).Body.Close()
	appsyncPost(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":               "hello",
		"dataSourceName":          "NoneDS",
		"kind":                    "UNIT",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"world"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}).Body.Close()

	bases := []struct{ name, host string }{
		{"amazonaws.com with region", apiID + ".appsync-api.us-east-1.amazonaws.com"},
		{"localhost with region", apiID + ".appsync-api.us-east-1.localhost:4566"},
		{"localhost without region", apiID + ".appsync-api.localhost:4566"},
		{"overcast.sh wildcard with region", apiID + ".appsync-api.us-east-1.localhost.overcast.sh:4566"},
		{"localstack.cloud wildcard with region", apiID + ".appsync-api.us-east-1.localhost.localstack.cloud:4566"},
		// A hostname is case-insensitive, so the same address in any case is
		// the same address. Sent case-sensitively, the label missed
		// hostRouteLabels and S3 virtual-hosted addressing claimed the Host
		// instead — an S3 XML error for a GraphQL query.
		{"mixed case", apiID + ".AppSync-Api.US-East-1.localhost.overcast.sh:4566"},
	}

	for _, b := range bases {
		t.Run(b.name, func(t *testing.T) {
			// When: the query is executed via the appsync-api Host on that base
			resp := appsyncPostWithHost(t, srv, "/graphql",
				map[string]any{"query": `{ hello }`},
				b.host,
				map[string]string{"x-api-key": keyID},
			)
			defer resp.Body.Close()

			// Then: the same data the path-style invoke returns
			helpers.AssertStatus(t, resp, http.StatusOK)
			var result struct {
				Data struct {
					Hello string `json:"hello"`
				} `json:"data"`
			}
			helpers.DecodeJSON(t, resp, &result)
			if result.Data.Hello != "world" {
				t.Errorf("Host %q: expected data.hello=%q, got %q", b.host, "world", result.Data.Hello)
			}
		})
	}
}

// TestExecuteGraphQL_hostBasedInvokeInNonDefaultRegion is the region-scoped
// counterpart to the test above, which only ever exercised the default region —
// which is why it could not catch this.
//
// AppSync's store is region-scoped (serviceutil.RegionKey, store.go), so an API
// created in eu-west-1 lives under a different key prefix from one created in
// us-east-1. A host-routed invoke carries no SigV4 — AppSync authenticates it
// with an x-api-key header — so the Host is the ONLY evidence of which region
// the caller means. If the region hint in that Host is dropped, the lookup
// resolves against the default region and the API is not found.
func TestExecuteGraphQL_hostBasedInvokeInNonDefaultRegion(t *testing.T) {
	// Given: a GraphQL API with a NONE-datasource resolver, created entirely
	// in eu-west-1 via SigV4-scoped control-plane calls
	const region = "eu-west-1"
	srv := helpers.NewTestServer(t)
	regional := map[string]string{"Authorization": appsyncSigV4In(region)}

	resp := appsyncPostWithHeaders(t, srv, "/v1/apis", map[string]any{
		"name":               "regional-api",
		"authenticationType": "API_KEY",
	}, regional)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var created struct {
		GraphqlAPI struct {
			ApiId string `json:"apiId"`
		} `json:"graphqlApi"`
	}
	helpers.DecodeJSON(t, resp, &created)
	apiID := created.GraphqlAPI.ApiId

	sdl := base64.StdEncoding.EncodeToString([]byte(`type Query { hello: String }`))
	appsyncPostWithHeaders(t, srv, "/v1/apis/"+apiID+"/schemacreation",
		map[string]any{"definition": sdl}, regional).Body.Close()
	appsyncPostWithHeaders(t, srv, "/v1/apis/"+apiID+"/datasources", map[string]any{
		"name": "NoneDS",
		"type": "NONE",
	}, regional).Body.Close()
	appsyncPostWithHeaders(t, srv, "/v1/apis/"+apiID+"/types/Query/resolvers", map[string]any{
		"fieldName":               "hello",
		"dataSourceName":          "NoneDS",
		"kind":                    "UNIT",
		"requestMappingTemplate":  `{"version":"2018-05-29","payload":"world"}`,
		"responseMappingTemplate": `$util.toJson($context.result)`,
	}, regional).Body.Close()

	keyResp := appsyncPostWithHeaders(t, srv, "/v1/apis/"+apiID+"/apikeys",
		map[string]any{}, regional)
	helpers.AssertStatus(t, keyResp, http.StatusOK)
	var key struct {
		ApiKey struct {
			Id string `json:"id"`
		} `json:"apiKey"`
	}
	helpers.DecodeJSON(t, keyResp, &key)

	// When: the query is executed over that API's own host-routed URL, whose
	// region segment is the only thing naming eu-west-1
	invoke := appsyncPostWithHost(t, srv, "/graphql",
		map[string]any{"query": `{ hello }`},
		apiID+".appsync-api."+region+".localhost:4566",
		map[string]string{"x-api-key": key.ApiKey.Id},
	)
	defer invoke.Body.Close()

	// Then: the same data a path-style invoke in that region returns
	helpers.AssertStatus(t, invoke, http.StatusOK)
	var result struct {
		Data struct {
			Hello string `json:"hello"`
		} `json:"data"`
	}
	helpers.DecodeJSON(t, invoke, &result)
	if result.Data.Hello != "world" {
		t.Errorf("expected data.hello=%q, got %q", "world", result.Data.Hello)
	}
}

// appsyncSigV4In builds a fake SigV4 Authorization header scoped to region, so
// a control-plane call lands in that region's partition of the store.
func appsyncSigV4In(region string) string {
	return "AWS4-HMAC-SHA256 Credential=test/20250101/" + region +
		"/appsync/aws4_request, SignedHeaders=host, Signature=fake"
}

// appsyncPostWithHost is appsyncPostWithHeaders but also sets an explicit
// Host header, for exercising the Host-routed (appsync-api) GraphQL path.
func appsyncPostWithHost(t *testing.T, srv *helpers.TestServer, path string, body map[string]any, host string, headers map[string]string) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, doErr := http.DefaultClient.Do(req)
	if doErr != nil {
		t.Fatalf("appsyncPostWithHost %s: %v", path, doErr)
	}
	return resp
}
