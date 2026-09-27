package iam_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The tests in this file pin #2283: with IAM enforcement on, a request a
// service's own route serves, or that /tags hands to the service its
// resource ARN names, is authorised as the operation that service serves,
// whatever service its credential scope names. An operation a service's
// route serves that cannot be named is authorised as every action of the
// service, never let through.

// signedRequest sends method path signed for signingName by accessKey.
func signedRequest(t *testing.T, srv *helpers.TestServer, method, path, accessKey, signingName string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	return doSigned(t, req, sigV4Auth(accessKey, signingName))
}

// assertDeniedAs checks resp is a 403 denial of action.
func assertDeniedAs(t *testing.T, resp *http.Response, action string) {
	t.Helper()
	body := helpers.ReadBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "not authorized to perform: "+action+" ") {
		t.Fatalf("status %d, body %q; want a denial of %s", resp.StatusCode, body, action)
	}
}

// assertNotDenied checks enforcement let resp's request through to the
// service, whatever the service then answered.
func assertNotDenied(t *testing.T, resp *http.Response) {
	t.Helper()
	body := helpers.ReadBody(t, resp)
	if resp.StatusCode == http.StatusForbidden || strings.Contains(body, "not authorized to perform") {
		t.Fatalf("status %d, body %q; want the request served", resp.StatusCode, body)
	}
}

func TestIAMEnforceServiceRoute_scopeSpoofAuthorisedAsTheServedOperation(t *testing.T) {
	for _, path := range []string{"/clusters", "/clusters?Action=GetCallerIdentity"} {
		t.Run(path, func(t *testing.T) {
			// Given: a principal allowed sts:* and nothing in EKS
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedIAMPrincipal(t, srv, "sts-only", stsOnlyPolicy)

			// When: it calls EKS's ListClusters route signed for sts
			resp := signedRequest(t, srv, http.MethodGet, path, "sts-only", "sts")

			// Then: it is authorised as the ListClusters EKS serves, and denied
			assertDeniedAs(t, resp, "eks:ListClusters")
		})
	}
}

func TestIAMEnforceServiceRoute_servedOperationAllowedWhateverTheScope(t *testing.T) {
	// Given: a principal allowed eks:ListClusters and nothing else
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "lister", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"eks:ListClusters","Resource":"*"}]}`)

	// When: it lists clusters signed for sts
	resp := signedRequest(t, srv, http.MethodGet, "/clusters", "lister", "sts")
	defer resp.Body.Close()

	// Then: the ListClusters it is allowed is what is served
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforceServiceRoute_tagsAuthorisedAsTheARNsService(t *testing.T) {
	for _, tc := range []struct {
		name, arn, action string
	}{
		{"EKS", "arn:aws:eks:us-east-1:000000000000:cluster/c", "eks:ListTagsForResource"},
		// Served by API Gateway's tag store, which AppRegistry shares.
		{"AppRegistry", "arn:aws:servicecatalog:us-east-1:000000000000:/applications/a", "servicecatalog:ListTagsForResource"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a principal allowed sts:* and nothing else
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedIAMPrincipal(t, srv, "sts-only", stsOnlyPolicy)

			// When: it lists the ARN's tags signed for sts
			resp := signedRequest(t, srv, http.MethodGet, "/tags/"+url.PathEscape(tc.arn), "sts-only", "sts")

			// Then: it is authorised as the ARN's service's tag operation
			assertDeniedAs(t, resp, tc.action)
		})
	}
}

func TestIAMEnforceServiceRoute_tagsOperationAllowedWhateverTheScope(t *testing.T) {
	// Given: a principal allowed eks:ListTagsForResource and nothing else
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "tagger", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"eks:ListTagsForResource","Resource":"*"}]}`)

	// When: it lists an EKS cluster's tags signed for sts
	resp := signedRequest(t, srv, http.MethodGet, "/tags/"+url.PathEscape("arn:aws:eks:us-east-1:000000000000:cluster/c"), "tagger", "sts")

	// Then: enforcement lets EKS answer it
	assertNotDenied(t, resp)
}

func TestIAMEnforceServiceRoute_unnamedOperationNeedsTheWholeService(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		denied       bool
	}{
		{"allowed one operation", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`, true},
		{"allowed the service", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a principal allowed some of SQS
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedIAMPrincipal(t, srv, "caller", tc.policy)

			// When: it posts to a queue URL, which SQS's own route serves,
			// naming no operation
			resp := signedRequest(t, srv, http.MethodPost, "/000000000000/q", "caller", "sqs")

			// Then: it is authorised as every SQS action
			if tc.denied {
				assertDeniedAs(t, resp, "sqs:*")
				return
			}
			assertNotDenied(t, resp)
		})
	}
}

func TestIAMEnforceServiceRoute_icebergCallsAuthorisedAsAWSDocumentsThem(t *testing.T) {
	prefix := "/iceberg/v1/" + url.PathEscape("arn:aws:s3tables:us-east-1:000000000000:bucket/b")
	for _, tc := range []struct {
		name, method, path, allowed, deniedAction string
	}{
		{"listNamespaces allowed", http.MethodGet, prefix + "/namespaces", "s3tables:ListNamespaces", ""},
		{"listNamespaces denied", http.MethodGet, prefix + "/namespaces", "s3tables:ListTables", "s3tables:ListNamespaces"},
		{"loadTable needs its data too", http.MethodGet, prefix + "/namespaces/ns/tables/t", "s3tables:GetTableMetadataLocation", "s3tables:GetTableData"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a principal allowed one S3 Tables action
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedIAMPrincipal(t, srv, "catalog", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"`+tc.allowed+`","Resource":"*"}]}`)

			// When: it calls the Iceberg REST catalog
			resp := signedRequest(t, srv, tc.method, tc.path, "catalog", "s3tables")

			// Then: it is authorised as the actions AWS lists for the call
			if tc.deniedAction != "" {
				assertDeniedAs(t, resp, tc.deniedAction)
				return
			}
			assertNotDenied(t, resp)
		})
	}
}

func TestIAMEnforceServiceRoute_apiGatewayInvocationLeftToAPIGateway(t *testing.T) {
	// Given: a principal allowed to invoke APIs and nothing in API Gateway
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "invoker", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"execute-api:Invoke","Resource":"*"}]}`)

	// When: it invokes a REST API through API Gateway's own invocation route
	resp := signedRequest(t, srv, http.MethodGet, "/restapis/abc123/prod/_user_request_/pets", "invoker", "execute-api")

	// Then: enforcement does not gate the invocation (#2291), and API Gateway
	// answers for the API it does not have with its own {"message":"Forbidden"}
	body := helpers.ReadBody(t, resp)
	if strings.Contains(body, "not authorized to perform") || !strings.Contains(body, `"message":"Forbidden"`) {
		t.Fatalf("status %d, body %q; want API Gateway's own answer", resp.StatusCode, body)
	}
}
