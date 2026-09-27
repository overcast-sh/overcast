package iam_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The tests in this file pin #2259: an IAM denial reaches an unmodified AWS SDK
// for Go v2 client as that protocol's access-denied error, which it can read,
// rather than as a body it fails to deserialize.

// sqsOnlyPolicy allows one SQS action and nothing the tests below call.
const sqsOnlyPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"}]}`

// deniedSDKCall is one SDK call the sqs-only principal is not allowed, and the
// error AWS answers it with.
type deniedSDKCall struct {
	name   string
	call   func(context.Context, aws.Config) error
	code   string
	status int
}

var deniedSDKCalls = []deniedSDKCall{
	{"athena (awsJson1_1)", func(ctx context.Context, cfg aws.Config) error {
		_, err := athena.NewFromConfig(cfg).ListWorkGroups(ctx, &athena.ListWorkGroupsInput{})
		return err
	}, "AccessDeniedException", http.StatusBadRequest},
	{"glue (awsJson1_1)", func(ctx context.Context, cfg aws.Config) error {
		_, err := glue.NewFromConfig(cfg).GetDatabases(ctx, &glue.GetDatabasesInput{})
		return err
	}, "AccessDeniedException", http.StatusBadRequest},
	{"sts (awsQuery)", func(ctx context.Context, cfg aws.Config) error {
		_, err := sts.NewFromConfig(cfg).AssumeRole(ctx, &sts.AssumeRoleInput{
			RoleArn:         aws.String("arn:aws:iam::000000000000:role/app"),
			RoleSessionName: aws.String("denied"),
		})
		return err
	}, "AccessDenied", http.StatusForbidden},
	{"s3 (restXml)", func(ctx context.Context, cfg aws.Config) error {
		_, err := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }).ListBuckets(ctx, &s3.ListBucketsInput{})
		return err
	}, "AccessDenied", http.StatusForbidden},
}

func TestIAMEnforceDenial_sdkReadsTheErrorCode(t *testing.T) {
	// Given: enforcement on, and a principal allowed none of the calls below
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "sqs-only", sqsOnlyPolicy)
	cfg := sdkConfigFor(srv, "sqs-only")

	for _, tc := range deniedSDKCalls {
		t.Run(tc.name, func(t *testing.T) {
			// When: the principal makes the call through the SDK
			err := tc.call(context.Background(), cfg)

			// Then: the SDK decodes the denial, code and status
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) || apiErr.ErrorCode() != tc.code {
				t.Fatalf("err = %v, want API error %s", err, tc.code)
			}
			var respErr *smithyhttp.ResponseError
			if !errors.As(err, &respErr) || respErr.HTTPStatusCode() != tc.status {
				t.Fatalf("err = %v, want HTTP %d", err, tc.status)
			}
		})
	}
}

// denyAllPolicy denies every action on every resource.
const denyAllPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`

// sdkConfigFor is an SDK configuration that calls srv as accessKey.
func sdkConfigFor(srv *helpers.TestServer, accessKey string) aws.Config {
	return aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, "secret", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	}
}

func TestIAMEnforceDenial_getCallerIdentityServedUnderDenyAll(t *testing.T) {
	// Given: enforcement on, and a principal whose only policy denies everything
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "deny-all", denyAllPolicy)

	// When: it asks STS who it is
	out, err := sts.NewFromConfig(sdkConfigFor(srv, "deny-all")).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})

	// Then: AWS needs no permission for that, so it is answered
	if err != nil {
		t.Fatalf("GetCallerIdentity under a Deny-all policy: %v", err)
	}
	if got := aws.ToString(out.Account); got != "000000000000" {
		t.Fatalf("Account = %q, want 000000000000", got)
	}
}

func TestIAMEnforceDenial_snsAuthorizationErrorNamesThePrincipalAndAction(t *testing.T) {
	// Given: enforcement on, and a principal whose only policy denies everything
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "deny-all", denyAllPolicy)

	// When: it lists SNS topics
	_, err := sns.NewFromConfig(sdkConfigFor(srv, "deny-all")).ListTopics(context.Background(), &sns.ListTopicsInput{})

	// Then: the SDK reads SNS's own denial, a 403 AuthorizationError, whose
	// message says who was refused what and why
	var authErr *snstypes.AuthorizationErrorException
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want *types.AuthorizationErrorException", err)
	}
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) || respErr.HTTPStatusCode() != http.StatusForbidden {
		t.Fatalf("err = %v, want HTTP 403", err)
	}
	want := "User: arn:aws:iam::000000000000:user/deny-all is not authorized to perform: sns:ListTopics with an explicit deny in an identity-based policy"
	if got := authErr.ErrorMessage(); got != want {
		t.Fatalf("message = %q\nwant      %q", got, want)
	}
}

func TestIAMEnforceDenial_roleSessionIsNamedByItsAssumedRoleARN(t *testing.T) {
	// Given: enforcement on, and a session assumed through the SDK of a role
	// under a path whose only policy denies everything (#2272)
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	assumed, session := assumeTeamAppRole(t, srv)
	putAppRolePolicy(t, srv, denyAllPolicy)

	// When: the session lists SNS topics
	_, err := sns.NewFromConfig(session).ListTopics(context.Background(), &sns.ListTopicsInput{})

	// Then: the denial names the session as AWS does, by the assumed-role ARN
	// STS returned for it, which leaves the role's path out
	var authErr *snstypes.AuthorizationErrorException
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want *types.AuthorizationErrorException", err)
	}
	const sessionARN = "arn:aws:sts::000000000000:assumed-role/app/alice"
	if got := aws.ToString(assumed.AssumedRoleUser.Arn); got != sessionARN {
		t.Fatalf("AssumedRoleUser.Arn = %q, want %q", got, sessionARN)
	}
	want := "User: " + sessionARN + " is not authorized to perform: sns:ListTopics with an explicit deny in an identity-based policy"
	if got := authErr.ErrorMessage(); got != want {
		t.Fatalf("message = %q\nwant      %q", got, want)
	}
}

func TestIAMEnforceDenial_roleSessionUserIDIsItsAssumedRoleID(t *testing.T) {
	// Given: enforcement on, a session assumed through the SDK, and a role
	// policy allowing sns:ListTopics only to the aws:userid AWS gives that
	// session, the AssumedRoleId STS returned for it
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	assumed, session := assumeTeamAppRole(t, srv)
	putAppRolePolicy(t, srv, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:ListTopics","Resource":"*",`+
		`"Condition":{"StringEquals":{"aws:userid":"`+aws.ToString(assumed.AssumedRoleUser.AssumedRoleId)+`"}}}]}`)

	// When: the session lists SNS topics
	_, err := sns.NewFromConfig(session).ListTopics(context.Background(), &sns.ListTopicsInput{})

	// Then: the condition matched, so the call is served
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
}

// assumeTeamAppRole creates the role "app" under the path /team/, with no
// policy, and assumes it as the session "alice" through the SDK. It returns
// what STS answered and an SDK configuration that calls srv as the session.
// The module has no IAM SDK client, so the role is created over signed Query.
func assumeTeamAppRole(t *testing.T, srv *helpers.TestServer) (*sts.AssumeRoleOutput, aws.Config) {
	t.Helper()
	seedIAMPrincipal(t, srv, "admin", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:CreateRole","iam:PutRolePolicy","sts:AssumeRole"],"Resource":"*"}]}`)
	callIAMAsAdmin(t, srv, url.Values{
		"Action":                   {"CreateRole"},
		"RoleName":                 {"app"},
		"Path":                     {"/team/"},
		"AssumeRolePolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`},
	})
	assumed, err := sts.NewFromConfig(sdkConfigFor(srv, "admin")).AssumeRole(context.Background(), &sts.AssumeRoleInput{
		RoleArn:         aws.String("arn:aws:iam::000000000000:role/team/app"),
		RoleSessionName: aws.String("alice"),
	})
	if err != nil {
		t.Fatalf("AssumeRole: %v", err)
	}
	session := sdkConfigFor(srv, "")
	session.Credentials = credentials.NewStaticCredentialsProvider(
		aws.ToString(assumed.Credentials.AccessKeyId),
		aws.ToString(assumed.Credentials.SecretAccessKey),
		aws.ToString(assumed.Credentials.SessionToken),
	)
	return assumed, session
}

// putAppRolePolicy gives the role "app" policy as its only inline policy.
func putAppRolePolicy(t *testing.T, srv *helpers.TestServer, policy string) {
	t.Helper()
	callIAMAsAdmin(t, srv, url.Values{
		"Action":         {"PutRolePolicy"},
		"RoleName":       {"app"},
		"PolicyName":     {"only"},
		"PolicyDocument": {policy},
	})
}

// callIAMAsAdmin makes the IAM Query call vals as the "admin" access key and
// fails the test unless it succeeds.
func callIAMAsAdmin(t *testing.T, srv *helpers.TestServer, vals url.Values) {
	t.Helper()
	vals.Set("Version", "2010-05-08")
	resp := queryCallWithAuthValues(t, srv, vals,
		"AWS4-HMAC-SHA256 Credential=admin/20260423/us-east-1/iam/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}
