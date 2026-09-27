package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

// The tests in this file pin #2283: a request a service's own route serves
// is named as that service's whatever its credential scope names, and one it
// serves that cannot be named needs every action of the service.

func TestRequestIAMOperation_serviceRouteNamesTheServedOperation(t *testing.T) {
	for _, target := range []string{"/clusters", "/clusters?Action=GetCallerIdentity"} {
		t.Run(target, func(t *testing.T) {
			// Given: EKS's ListClusters signed for sts
			r := signedRequest(http.MethodGet, target, "sts")

			// When: the router says EKS's route serves it
			op, err := requestIAMOperation(httptest.NewRecorder(), r, stubRouter{
				rest:   RESTRoute{Outcome: RESTServedByService, Service: "eks"},
				routed: true,
			})

			// Then: it is EKS's ListClusters
			want := iamOperation{service: "eks", action: "eks:ListClusters"}
			if err != nil || op != want {
				t.Fatalf("requestIAMOperation = %+v, %v; want %+v", op, err, want)
			}
		})
	}
}

func TestServiceRouteIAMOperation_unnamedOperationNeedsTheWholeService(t *testing.T) {
	for _, tc := range []struct {
		name, service, method, target, want string
	}{
		{"queue URL naming no operation", "sqs", http.MethodPost, "/000000000000/q", "sqs:*"},
		{"Iceberg endpoint AWS does not serve", "s3tables", http.MethodPost, "/iceberg/v1/p/namespaces/ns/register", "s3tables:*"},
		{"API Gateway path naming no operation", "apigateway", http.MethodPost, "/restapis/a/b/c", "apigateway:*"},
		{"REST API invocation", "apigateway", http.MethodGet, "/restapis/a/prod/_user_request_/pets", ""},
		{"REST API invocation at its root", "apigateway", http.MethodGet, "/restapis/a/prod/_user_request_/", ""},
		{"not an invocation route", "apigateway", http.MethodGet, "/restapis/a/prod/_user_request_", "apigateway:*"},
		{"HTTP API invocation", "apigateway", http.MethodPost, "/v2/apis/a/stages/prod/pets", ""},
		{"a named operation beneath a stage", "apigateway", http.MethodDelete, "/v2/apis/a/stages/prod/routesettings/r", "apigateway:DeleteRouteSettings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a request the service's own route serves
			r := httptest.NewRequest(tc.method, tc.target, nil)

			// When: it is named as the service's
			op := serviceRouteIAMOperation(r, tc.service)

			// Then: an operation no action names needs the whole service,
			// unless it invokes a deployed API
			if op.action != tc.want {
				t.Fatalf("action = %q, want %q", op.action, tc.want)
			}
		})
	}
}

func TestIcebergIAMActions_areTheActionsAWSLists(t *testing.T) {
	prefix := "/iceberg/v1/" + url.PathEscape("arn:aws:s3tables:us-east-1:000000000000:bucket/b")
	for _, tc := range []struct {
		method, path string
		want         []string
	}{
		{http.MethodGet, "/iceberg/v1/config?warehouse=w", []string{"s3tables:GetTableBucket"}},
		{http.MethodGet, prefix + "/namespaces", []string{"s3tables:ListNamespaces"}},
		{http.MethodHead, prefix + "/namespaces/ns", []string{"s3tables:GetNamespace"}},
		{http.MethodPost, prefix + "/namespaces/ns/tables", []string{"s3tables:CreateTable", "s3tables:PutTableData"}},
		{http.MethodGet, prefix + "/namespaces/ns/tables/t", []string{"s3tables:GetTableMetadataLocation", "s3tables:GetTableData"}},
		{http.MethodPost, prefix + "/namespaces/ns/tables/t", []string{"s3tables:UpdateTableMetadataLocation", "s3tables:PutTableData", "s3tables:GetTableData"}},
		{http.MethodHead, prefix + "/namespaces/ns/tables/t", []string{"s3tables:GetTable"}},
		{http.MethodPost, prefix + "/tables/rename", []string{"s3tables:RenameTable"}},
		// A namespace or table may be named like a path literal.
		{http.MethodDelete, prefix + "/namespaces/tables/tables/rename", []string{"s3tables:DeleteTable"}},
		{http.MethodPost, prefix + "/namespaces/ns/tables/t/metrics", nil},
		{http.MethodGet, "/tables/b/ns/t", nil},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			// Given: an Iceberg REST catalog call
			r := httptest.NewRequest(tc.method, tc.path, nil)

			// When: its actions are named
			got := icebergIAMActions(r)

			// Then: they are the ones AWS's endpoint table lists
			if !slices.Equal(got, tc.want) {
				t.Fatalf("icebergIAMActions = %v, want %v", got, tc.want)
			}
		})
	}
}
