package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/state"
)

// These tests pin #2266: what IAM enforcement tells a caller it denied — the
// principal, action, resource and policy, in AWS's words and with the
// service's own error code — and the actions AWS never denies at all.

const (
	denyAllPolicy   = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`
	sqsOnlyUnitDoc  = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"}]}`
	callerARN       = "arn:aws:iam::000000000000:user/caller"
	callerAccessKey = "caller"
)

// signAsCaller signs r for signingName with callerAccessKey.
func signAsCaller(r *http.Request, signingName string) *http.Request {
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+callerAccessKey+
		"/20260423/us-east-1/"+signingName+"/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	r.Header.Set("X-Amz-Date", "20260423T000000Z")
	return r
}

// enforceSigned sends r, which the caller has signed, through IAMEnforce over
// st with queries as the router's Query dispatch. It reports the response and
// whether the request was served.
func enforceSigned(t *testing.T, st state.Store, queries RequestRouter, r *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	served := false
	h := IAMEnforce(true, st, zap.NewNop(), queries)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, served
}

// callerWith is a store holding callerAccessKey's user with policy inline.
func callerWith(t *testing.T, policy string) state.Store {
	t.Helper()
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, callerAccessKey, []string{policy}, nil)
	return st
}

// queryCall is a parsed, signed Query form, and the route the router serves it
// as.
func queryCall(t *testing.T, service, action, version string) (*http.Request, RequestRouter) {
	t.Helper()
	r := signAsCaller(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action="+action+"&Version="+version)), service)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return r, stubRouter{route: QueryRoute{Service: service, Action: action}, isQuery: true}
}

func TestIAMEnforce_actionsNeedingNoPermissionServedUnderAnExplicitDeny(t *testing.T) {
	for _, action := range []string{"GetCallerIdentity", "GetSessionToken"} {
		t.Run(action, func(t *testing.T) {
			// Given: a principal whose only policy denies everything
			st := callerWith(t, denyAllPolicy)
			r, queries := queryCall(t, "sts", action, "2011-06-15")

			// When: it calls an STS action AWS never authorizes
			rec, served := enforceSigned(t, st, queries, r)

			// Then: the call is served, as on AWS
			if !served {
				t.Fatalf("sts:%s was denied (status %d, body %q); AWS serves it under any policy", action, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIAMEnforce_getAccessKeyInfoIsAuthorized(t *testing.T) {
	// Given: a principal whose only policy denies everything
	st := callerWith(t, denyAllPolicy)
	r, queries := queryCall(t, "sts", "GetAccessKeyInfo", "2011-06-15")

	// When: it calls GetAccessKeyInfo, which AWS documents no exemption for
	rec, served := enforceSigned(t, st, queries, r)

	// Then: it is denied like any other action
	if served || rec.Code != http.StatusForbidden {
		t.Fatalf("served=%v status=%d; want sts:GetAccessKeyInfo denied", served, rec.Code)
	}
}

func TestIAMEnforce_denialMessageNamesThePrincipalActionAndResource(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		request func(*testing.T) (*http.Request, RequestRouter)
		code    string
		message string
	}{
		{
			name:   "awsJson, explicit deny",
			policy: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Effect":"Deny","Action":"sqs:DeleteQueue","Resource":"*"}]}`,
			request: func(*testing.T) (*http.Request, RequestRouter) {
				r := signAsCaller(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"QueueUrl":"http://localhost/000000000000/demo"}`)), "sqs")
				r.Header.Set("Content-Type", "application/x-amz-json-1.0")
				r.Header.Set("X-Amz-Target", "AmazonSQS.DeleteQueue")
				return r, nil
			},
			code:    "AccessDeniedException",
			message: "User: " + callerARN + " is not authorized to perform: sqs:DeleteQueue on resource: arn:aws:sqs:us-east-1:000000000000:demo with an explicit deny in an identity-based policy",
		},
		{
			name:   "awsQuery SNS, implicit deny",
			policy: sqsOnlyUnitDoc,
			request: func(t *testing.T) (*http.Request, RequestRouter) {
				return queryCall(t, "sns", "ListTopics", "2010-03-31")
			},
			code:    "AuthorizationError",
			message: "User: " + callerARN + " is not authorized to perform: sns:ListTopics because no identity-based policy allows the sns:ListTopics action",
		},
		{
			name:   "awsQuery IAM, implicit deny",
			policy: sqsOnlyUnitDoc,
			request: func(t *testing.T) (*http.Request, RequestRouter) {
				return queryCall(t, "iam", "ListUsers", "2010-05-08")
			},
			code:    "AccessDenied",
			message: "User: " + callerARN + " is not authorized to perform: iam:ListUsers because no identity-based policy allows the iam:ListUsers action",
		},
		{
			name:   "ec2Query, implicit deny",
			policy: sqsOnlyUnitDoc,
			request: func(t *testing.T) (*http.Request, RequestRouter) {
				return queryCall(t, "ec2", "DescribeVpcs", "2016-11-15")
			},
			code:    "UnauthorizedOperation",
			message: "You are not authorized to perform this operation. User: " + callerARN + " is not authorized to perform: ec2:DescribeVpcs because no identity-based policy allows the ec2:DescribeVpcs action",
		},
		{
			name:   "restXml S3, implicit deny",
			policy: sqsOnlyUnitDoc,
			request: func(*testing.T) (*http.Request, RequestRouter) {
				return signAsCaller(httptest.NewRequest(http.MethodGet, "/bucket/key", nil), "s3"), nil
			},
			code:    "AccessDenied",
			message: "User: " + callerARN + ` is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::bucket/key" because no identity-based policy allows the s3:GetObject action`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a principal whose policy does not allow the call
			st := callerWith(t, tc.policy)
			r, queries := tc.request(t)

			// When: it makes the call
			rec, served := enforceSigned(t, st, queries, r)

			// Then: the denial carries the service's code and AWS's sentence
			if served {
				t.Fatal("the call was served; want it denied")
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.code) || !strings.Contains(body, xmlOrJSONEscaped(tc.message)) {
				t.Fatalf("body = %s\nwant code %s and message\n  %s", body, tc.code, tc.message)
			}
		})
	}
}

func TestIAMEnforce_denialMessageFollowsEachRequestThroughTheCache(t *testing.T) {
	// Given: one principal, whose compiled policies the middleware caches
	st := callerWith(t, sqsOnlyUnitDoc)
	h := IAMEnforce(true, st, zap.NewNop(), nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, key := range []string{"first", "second"} {
		// When: it is denied two different objects in turn
		r := signAsCaller(httptest.NewRequest(http.MethodGet, "/bucket/"+key, nil), "s3")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		// Then: each denial names its own resource, not the first one cached
		want := `on resource: &#34;arn:aws:s3:::bucket/` + key + `&#34;`
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("denial for %s = %s; want it to name %s", key, rec.Body.String(), want)
		}
	}
}

func TestIAMEnforce_unattributedDenialKeepsTheGenericMessage(t *testing.T) {
	// Given: an access key that names no principal
	st := state.NewMemoryStore()
	r, queries := queryCall(t, "iam", "ListUsers", "2010-05-08")

	// When: it makes a call
	rec, served := enforceSigned(t, st, queries, r)

	// Then: there is no principal or policy to name, so none is invented
	if served || !strings.Contains(rec.Body.String(), iamUnattributedDeniedMessage) {
		t.Fatalf("served=%v body=%s; want the generic denial", served, rec.Body.String())
	}
}

// xmlOrJSONEscaped is message as it appears in an XML body: encoding/xml
// escapes double quotes, which the JSON bodies above do not contain.
func xmlOrJSONEscaped(message string) string {
	return strings.ReplaceAll(message, `"`, "&#34;")
}

func TestIAMEnforce_exemptActionOffTheQueryPathIsAuthorized(t *testing.T) {
	// Given: a principal whose only policy denies everything, and a request
	// the router does not serve as Query: an S3 path whose query string names
	// GetCallerIdentity, signed for sts
	st := callerWith(t, denyAllPolicy)
	r := signAsCaller(httptest.NewRequest(http.MethodPut, "/borrowed-bucket?Action=GetCallerIdentity", nil), "sts")

	// When: it reaches enforcement
	rec, served := enforceSigned(t, st, stubRouter{}, r)

	// Then: STS's exemption does not carry over to whatever the router serves
	if served {
		t.Fatalf("a non-Query request naming GetCallerIdentity was served (status %d); want it authorized and denied", rec.Code)
	}
}

func TestIAMEnforce_exemptActionForAnUnknownAccessKeyIsDenied(t *testing.T) {
	// Given: an access key that names no principal
	st := state.NewMemoryStore()
	r, queries := queryCall(t, "sts", "GetCallerIdentity", "2011-06-15")

	// When: it calls GetCallerIdentity
	_, served := enforceSigned(t, st, queries, r)

	// Then: it stays denied, as AWS refuses a key it does not know
	if served {
		t.Fatal("GetCallerIdentity was served to an unknown access key; want it denied")
	}
}

func TestIAMEnforce_exemptActionServedWhenThePolicyCannotBeRead(t *testing.T) {
	// Given: a principal whose policy does not parse
	st := callerWith(t, `{"Statement":`)
	r, queries := queryCall(t, "sts", "GetCallerIdentity", "2011-06-15")

	// When: it calls GetCallerIdentity
	rec, served := enforceSigned(t, st, queries, r)

	// Then: no policy is consulted, so the unreadable one does not matter
	if !served {
		t.Fatalf("GetCallerIdentity was denied (status %d, body %q); AWS consults no policy for it", rec.Code, rec.Body.String())
	}
}
