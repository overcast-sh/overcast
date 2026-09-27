package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// The tests in this file pin #2271: a request the router's REST fallback
// serves is named by the service and operation the fallback serves it as,
// and a query-string Action names an operation only of a service AWS Query
// can address.

func TestRequestIAMOperation_restFallbackNamesTheServedOperation(t *testing.T) {
	// Given: CreateBucket signed for sts, with a query string naming an STS
	// Action and carrying an X-Amz-Target, all of which the fallback ignores
	r := signedRequest(http.MethodPut, "/some-bucket?Action=GetFederationToken&Version=2011-06-15", "sts")
	r.Header.Set("X-Amz-Target", "DynamoDB_20120810.DeleteTable")
	for _, tc := range []struct {
		name string
		rest RESTRoute
		want iamOperation
	}{
		{"served by S3", RESTRoute{Outcome: RESTServedByS3}, iamOperation{service: "s3", action: "s3:CreateBucket", rest: true}},
		{"a modeled binding's 501", RESTRoute{Outcome: RESTNotImplemented, Claim: awsapi.Claim{Service: "cloudwatch-logs"}}, iamOperation{service: "logs", rest: true}},
		{"the caller's own 501", RESTRoute{Outcome: RESTNotImplemented}, iamOperation{service: "sts", rest: true}},
		{"refused", RESTRoute{Outcome: RESTScopeMismatch, Claim: awsapi.Claim{Service: "s3tables"}}, iamOperation{service: "s3tables", rest: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When: the router says its REST fallback serves it
			op, err := requestIAMOperation(httptest.NewRecorder(), r, stubRouter{rest: tc.rest, routed: true})

			// Then: it is named as the fallback serves it
			if err != nil || op != tc.want {
				t.Fatalf("requestIAMOperation = %+v, %v; want %+v", op, err, tc.want)
			}
		})
	}
}

func TestRequestIAMAction_actionOnlyForAServiceQueryAddresses(t *testing.T) {
	form := func(target, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	for _, tc := range []struct {
		name, svc string
		r         *http.Request
		want      string
	}{
		// EKS answers REST-JSON: the path names ListClusters.
		{"REST service", "eks", httptest.NewRequest(http.MethodGet, "/clusters?Action=DeleteCluster", nil), "eks:ListClusters"},
		// SQS serves a Query call to its queue URL by the form's Action.
		{"Query service", "sqs", form("/000000000000/q", "Action=DeleteQueue"), "sqs:DeleteQueue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a request naming an Action
			r := tc.r

			// When: its action is named for the service that serves it
			got := requestIAMAction(r, tc.svc)

			// Then: the Action counts only where AWS Query can address it
			if got != tc.want {
				t.Fatalf("requestIAMAction(%s %s) = %q, want %q", r.Method, r.URL, got, tc.want)
			}
		})
	}
}
