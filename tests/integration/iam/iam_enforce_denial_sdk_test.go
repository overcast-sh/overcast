package iam_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	cfg := aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("sqs-only", "secret", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	}

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
