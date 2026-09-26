package iam_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The tests in this file pin #2229: with IAM enforcement on, an AWS Query call
// is authorised as the operation the router serves it as, named by its Action
// and the service that owns it, and never as its SigV4 credential scope. The
// two disagree whenever a caller signs for one service and names another's
// Action, which every Query service answers on the same POST / and GET /.

// s3OnlyPolicy allows everything in S3 and nothing else.
const s3OnlyPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`

// queryScopeCase is one Query service's read-only operation, and the names AWS
// gives it: the signing name its SDK signs with and the IAM action a policy
// grants it by.
type queryScopeCase struct {
	name        string
	version     string
	action      string
	signingName string
	iamAction   string
}

// queryScopeCases covers every Query service Overcast serves on the root
// listener. Each operation succeeds on an empty emulator, so a pass is a 200.
var queryScopeCases = []queryScopeCase{
	{"IAM", "2010-05-08", "ListUsers", "iam", "iam:ListUsers"},
	{"STS", "2011-06-15", "GetCallerIdentity", "sts", "sts:GetCallerIdentity"},
	{"SQS", "2012-11-05", "ListQueues", "sqs", "sqs:ListQueues"},
	{"SNS", "2010-03-31", "ListTopics", "sns", "sns:ListTopics"},
	{"CloudFormation", "2010-05-15", "ListStacks", "cloudformation", "cloudformation:ListStacks"},
	{"EC2", "2016-11-15", "DescribeVpcs", "ec2", "ec2:DescribeVpcs"},
	{"RDS", "2014-10-31", "DescribeDBInstances", "rds", "rds:DescribeDBInstances"},
	{"ElastiCache", "2015-02-02", "DescribeCacheClusters", "elasticache", "elasticache:DescribeCacheClusters"},
	{"ELBv2", "2015-12-01", "DescribeLoadBalancers", "elasticloadbalancing", "elasticloadbalancing:DescribeLoadBalancers"},
	{"CloudWatch", "2010-08-01", "ListMetrics", "monitoring", "cloudwatch:ListMetrics"},
	{"SES v1", "2010-12-01", "ListIdentities", "ses", "ses:ListIdentities"},
	{"AutoScaling", "2011-01-01", "DescribeAutoScalingGroups", "autoscaling", "autoscaling:DescribeAutoScalingGroups"},
}

func sigV4Auth(accessKey, signingName string) string {
	return "AWS4-HMAC-SHA256 Credential=" + accessKey + "/20260423/us-east-1/" + signingName +
		"/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc"
}

// queryPost sends a Query call as a form-encoded POST /, as every SDK does.
func queryPost(t *testing.T, srv *helpers.TestServer, vals url.Values, auth string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatalf("build query POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doSigned(t, req, auth)
}

// queryGet sends a Query call as GET /?Action=..., which the router serves
// the same way (queryGetMiddleware).
func queryGet(t *testing.T, srv *helpers.TestServer, vals url.Values, auth string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/?"+vals.Encode(), nil)
	if err != nil {
		t.Fatalf("build query GET: %v", err)
	}
	return doSigned(t, req, auth)
}

func doSigned(t *testing.T, req *http.Request, auth string) *http.Response {
	t.Helper()
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Amz-Date", "20260423T000000Z")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", req.Method, req.URL.Path, err)
	}
	return resp
}

func queryValues(action, version string) url.Values {
	vals := url.Values{"Action": {action}}
	if version != "" {
		vals.Set("Version", version)
	}
	return vals
}

type queryTransport struct {
	name string
	send func(*testing.T, *helpers.TestServer, url.Values, string) *http.Response
}

var queryTransports = []queryTransport{{"POST", queryPost}, {"GET", queryGet}}

func TestIAMEnforceQueryScope_s3OnlyPrincipalSignedForS3(t *testing.T) {
	for _, tc := range queryScopeCases {
		for _, tr := range queryTransports {
			t.Run(tc.name+"/"+tr.name, func(t *testing.T) {
				// Given: enforcement on, and a principal allowed s3:* and nothing else
				srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
				seedIAMPrincipal(t, srv, "s3-only", s3OnlyPolicy)

				// When: it calls another service's Query Action, signed for s3
				resp := tr.send(t, srv, queryValues(tc.action, tc.version), sigV4Auth("s3-only", "s3"))
				defer resp.Body.Close()

				// Then: it is authorised as the served operation, and denied in
				// that service's Query envelope
				helpers.AssertQueryXMLError(t, resp, "AccessDenied")
			})
		}
	}
}

func TestIAMEnforceQueryScope_servedActionAllowedWhateverTheScope(t *testing.T) {
	for _, tc := range queryScopeCases {
		for _, signingName := range []string{tc.signingName, "s3"} {
			t.Run(tc.name+"/signed-"+signingName, func(t *testing.T) {
				// Given: a principal allowed exactly the operation's IAM action
				srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
				seedIAMPrincipal(t, srv, "caller", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"`+tc.iamAction+`","Resource":"*"}]}`)

				// When: it calls that operation, signed correctly or for s3
				resp := queryPost(t, srv, queryValues(tc.action, tc.version), sigV4Auth("caller", signingName))
				defer resp.Body.Close()

				// Then: the served operation is what was authorised, so it runs
				helpers.AssertStatus(t, resp, http.StatusOK)
			})
		}
	}
}

func TestIAMEnforceQueryScope_createUserSignedForS3(t *testing.T) {
	for _, tr := range queryTransports {
		t.Run(tr.name, func(t *testing.T) {
			// Given: a principal allowed s3:* and nothing in IAM
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedIAMPrincipal(t, srv, "s3-only", s3OnlyPolicy)

			// When: it sends IAM CreateUser signed for s3
			vals := queryValues("CreateUser", "2010-05-08")
			vals.Set("UserName", "escalated")
			resp := tr.send(t, srv, vals, sigV4Auth("s3-only", "s3"))
			defer resp.Body.Close()

			// Then: it is denied, and no user exists
			helpers.AssertQueryXMLError(t, resp, "AccessDenied")
			assertNoIAMUser(t, srv, "escalated")
		})
	}
}

func TestIAMEnforceQueryScope_actionWithoutVersion(t *testing.T) {
	// Given: a principal allowed s3:* and nothing in IAM
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "s3-only", s3OnlyPolicy)

	// When: it sends CreateUser with no Version, which the router gives to
	// the only service that owns the Action
	vals := url.Values{"Action": {"CreateUser"}, "UserName": {"unversioned"}}
	resp := queryPost(t, srv, vals, sigV4Auth("s3-only", "s3"))
	defer resp.Body.Close()

	// Then: it is authorised as iam:CreateUser and denied
	helpers.AssertQueryXMLError(t, resp, "AccessDenied")
	assertNoIAMUser(t, srv, "unversioned")
}

func TestIAMEnforceQueryScope_actionPastALargeLeadingParameter(t *testing.T) {
	for _, padding := range []struct {
		name string
		size int
	}{{"small", 4 << 10}, {"past the speculative parse", 2 << 20}} {
		t.Run(padding.name, func(t *testing.T) {
			// Given: a principal allowed s3:* and nothing in IAM
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedIAMPrincipal(t, srv, "s3-only", s3OnlyPolicy)

			// When: CreateUser's Action follows a large parameter, signed for s3
			body := "Pad=" + strings.Repeat("a", padding.size) + "&" + url.Values{
				"Action": {"CreateUser"}, "Version": {"2010-05-08"}, "UserName": {"padded"},
			}.Encode()
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(body))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp := doSigned(t, req, sigV4Auth("s3-only", "s3"))
			defer resp.Body.Close()

			// Then: the Action is found where the router finds it, and denied
			helpers.AssertQueryXMLError(t, resp, "AccessDenied")
			assertNoIAMUser(t, srv, "padded")
		})
	}
}

func TestIAMEnforceQueryScope_largeBodyServedAsTheAuthorisedAction(t *testing.T) {
	// Given: a principal allowed sts:GetCallerIdentity and nothing else
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "caller", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:GetCallerIdentity","Resource":"*"}]}`)

	// When: its body, past the size the protocol middleware parses, names
	// GetCallerIdentity while the query string names GetSessionToken
	body := "Pad=" + strings.Repeat("a", 2<<20) + "&Action=GetCallerIdentity&Version=2011-06-15"
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/?Action=GetSessionToken", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := doSigned(t, req, sigV4Auth("caller", "sts"))
	defer resp.Body.Close()

	// Then: the operation served is the GetCallerIdentity that was authorised
	helpers.AssertStatus(t, resp, http.StatusOK)
	if got := helpers.ReadBody(t, resp); !strings.Contains(got, "<GetCallerIdentityResult>") {
		t.Fatalf("served a different operation from the one authorised:\n%s", got)
	}
}

func TestIAMEnforceQueryScope_queryStringActionCannotStandInForTheBody(t *testing.T) {
	// Given: a principal allowed iam:ListUsers and nothing else
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "lister", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*"}]}`)

	// When: the query string names ListUsers and the body, which the router
	// dispatches on, names CreateUser
	body := url.Values{"Action": {"CreateUser"}, "Version": {"2010-05-08"}, "UserName": {"body-user"}}.Encode()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/?Action=ListUsers", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := doSigned(t, req, sigV4Auth("lister", "iam"))
	defer resp.Body.Close()

	// Then: it is authorised as the CreateUser that would be served, and denied
	helpers.AssertQueryXMLError(t, resp, "AccessDenied")
	assertNoIAMUser(t, srv, "body-user")
}

func TestIAMEnforceQueryScope_resourceReadWhereTheHandlerReadsIt(t *testing.T) {
	// Given: a principal allowed SQS on one queue only
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "scoped", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"arn:aws:sqs:us-east-1:000000000000:allowed"}]}`)

	// When: the query string names the allowed queue and the body, which SQS
	// acts on, names another
	queueURL := func(name string) string { return srv.URL + "/000000000000/" + name }
	body := url.Values{"Action": {"GetQueueAttributes"}, "Version": {"2012-11-05"}, "QueueUrl": {queueURL("protected")}}.Encode()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/?"+url.Values{"QueueUrl": {queueURL("allowed")}}.Encode(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := doSigned(t, req, sigV4Auth("scoped", "sqs"))
	defer resp.Body.Close()

	// Then: it is authorised against the queue that would be served, and denied
	helpers.AssertQueryXMLError(t, resp, "AccessDenied")
}

func TestIAMEnforceQueryScope_sdkSignedForAnotherService(t *testing.T) {
	// Given: a principal allowed s3:* and nothing in STS
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "s3-only", s3OnlyPolicy)

	// When: an STS client signs GetCallerIdentity for s3
	client := sts.New(sts.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("s3-only", "secret", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	}, sts.WithSigV4SigningName("s3"))
	_, err := client.GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})

	// Then: the SDK sees AccessDenied
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" {
		t.Fatalf("GetCallerIdentity signed for s3: err = %v, want AccessDenied", err)
	}
}

func TestIAMEnforceQueryScope_sdkSignedCorrectly(t *testing.T) {
	// Given: a principal allowed sts:GetCallerIdentity
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "caller", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:GetCallerIdentity","Resource":"*"}]}`)

	// When: an unmodified STS client calls it
	client := sts.New(sts.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("caller", "secret", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
	_, err := client.GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})

	// Then: it succeeds
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
}

// assertNoIAMUser fails if the IAM store holds a user named name. It reads the
// store directly: under enforcement a GetUser call would itself be gated.
func assertNoIAMUser(t *testing.T, srv *helpers.TestServer, name string) {
	t.Helper()
	keys, err := srv.Store.List(context.Background(), "iam:users", "")
	if err != nil {
		t.Fatalf("list iam users: %v", err)
	}
	for _, key := range keys {
		if strings.Contains(key, name) {
			t.Fatalf("IAM user %q was created: store key %q", name, key)
		}
	}
}
