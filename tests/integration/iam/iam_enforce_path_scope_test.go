package iam_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The tests in this file pin #2271: with IAM enforcement on, a request the
// router serves by its path is authorised as the operation it is served as,
// whatever service its credential scope names and whatever Action its query
// string carries. The router gives a path no service claims to S3, so a
// PUT /bucket signed for sts is S3's CreateBucket, not an STS call.

// stsOnlyPolicy allows everything in STS and nothing else.
const stsOnlyPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:*","Resource":"*"}]}`

// signedPut sends PUT path signed for signingName by accessKey.
func signedPut(t *testing.T, srv *helpers.TestServer, path, accessKey, signingName string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build PUT %s: %v", path, err)
	}
	return doSigned(t, req, sigV4Auth(accessKey, signingName))
}

// assertS3Denied checks resp is S3's AccessDenied for action.
func assertS3Denied(t *testing.T, resp *http.Response, action string) {
	t.Helper()
	body := helpers.ReadBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.HasPrefix(body, "<?xml") || !strings.Contains(body, "<Error><Code>AccessDenied</Code>") {
		t.Fatalf("status %d, body %q; want S3's bare <Error> AccessDenied", resp.StatusCode, body)
	}
	if !strings.Contains(body, "not authorized to perform: "+action+" ") {
		t.Fatalf("denial %q does not name %s", body, action)
	}
}

// assertNoBucket fails if bucket exists. It reads the S3 store directly:
// under enforcement a HeadBucket call would itself be gated.
func assertNoBucket(t *testing.T, srv *helpers.TestServer, bucket string) {
	t.Helper()
	keys, err := srv.Store.List(context.Background(), "s3:buckets", "")
	if err != nil {
		t.Fatalf("list buckets: %v", err)
	}
	for _, key := range keys {
		if strings.HasSuffix(key, bucket) {
			t.Fatalf("bucket %q was created: store key %q", bucket, key)
		}
	}
}

func TestIAMEnforcePathScope_foreignScopeAndActionAuthorisedAsTheServedOperation(t *testing.T) {
	// Buckets named after roots the router shares with other services reach
	// S3 through those services' dispatchers, so each is its own case.
	for _, bucket := range []string{"some-bucket", "tags", "buckets", "iceberg", "applications"} {
		t.Run(bucket, func(t *testing.T) {
			// Given: enforcement on, and a principal allowed sts:* and nothing in S3
			srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
			seedIAMPrincipal(t, srv, "sts-only", stsOnlyPolicy)

			// When: it sends S3 CreateBucket signed for sts, naming an STS
			// Action in the query string
			resp := signedPut(t, srv, "/"+bucket+"?Action=GetFederationToken&Version=2011-06-15&Name=escalated", "sts-only", "sts")

			// Then: it is authorised as the CreateBucket S3 serves, and denied
			// in S3's envelope
			assertS3Denied(t, resp, "s3:CreateBucket")
			assertNoBucket(t, srv, bucket)
		})
	}
}

func TestIAMEnforcePathScope_smithyURIWithoutItsHeaderIsS3(t *testing.T) {
	// Given: a principal allowed sts:* and nothing in S3
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "sts-only", stsOnlyPolicy)

	// When: it starts a multipart upload on the Smithy RPC v2 URI, which is
	// an S3 object key without a Smithy-Protocol header, signed for sts
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/service/a/operation/b?uploads&Action=GetFederationToken", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp := doSigned(t, req, sigV4Auth("sts-only", "sts"))

	// Then: it is authorised as the S3 operation served, CreateMultipartUpload,
	// whose action is s3:PutObject, and denied
	assertS3Denied(t, resp, "s3:PutObject")
}

func TestIAMEnforcePathScope_servedOperationAllowedWhateverTheScope(t *testing.T) {
	// Given: a principal allowed s3:CreateBucket and nothing else
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "creator", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"*"}]}`)

	// When: it sends CreateBucket signed for sts, with an STS Action in the
	// query string
	resp := signedPut(t, srv, "/scoped-bucket?Action=GetFederationToken", "creator", "sts")
	defer resp.Body.Close()

	// Then: the CreateBucket that was authorised is what is served
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestIAMEnforcePathScope_restServiceNotNamedByAQueryAction(t *testing.T) {
	// Given: a principal allowed eks:DeleteCluster and nothing else
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "deleter", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"eks:DeleteCluster","Resource":"*"}]}`)

	// When: it lists clusters, naming DeleteCluster in the query string
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/clusters?Action=DeleteCluster", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp := doSigned(t, req, sigV4Auth("deleter", "eks"))
	body := helpers.ReadBody(t, resp)

	// Then: it is authorised as the ListClusters EKS serves, which the
	// principal is not allowed; EKS answers REST-JSON, which has no Action
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "eks:ListClusters") {
		t.Fatalf("status %d, body %q; want a denial of eks:ListClusters", resp.StatusCode, body)
	}
}

func TestIAMEnforcePathScope_serviceRouteCannotBorrowAnActionNeedingNoPermission(t *testing.T) {
	// Given: a principal whose only policy denies everything
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "deny-all", denyAllPolicy)

	// When: it lists EKS clusters signed for sts, naming GetCallerIdentity,
	// which AWS serves whatever the caller's policies say
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/clusters?Action=GetCallerIdentity", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp := doSigned(t, req, sigV4Auth("deny-all", "sts"))
	defer resp.Body.Close()

	// Then: STS's exemption does not reach the EKS route, and it is refused
	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

func TestIAMEnforcePathScope_presignedURLAuthorisedAsTheServedOperation(t *testing.T) {
	// Given: a principal allowed to write and read objects, and one allowed
	// sts:* only, and an object
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "reader", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:CreateBucket","s3:PutObject","s3:GetObject"],"Resource":"*"}]}`)
	seedIAMPrincipal(t, srv, "sts-only", stsOnlyPolicy)
	ctx := context.Background()
	client := s3.NewFromConfig(sdkConfigFor(srv, "reader"), func(o *s3.Options) { o.UsePathStyle = true })
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("presigned")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("presigned"), Key: aws.String("k"), Body: strings.NewReader("v")}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	for _, tc := range []struct {
		principal string
		want      int
	}{{"reader", http.StatusOK}, {"sts-only", http.StatusForbidden}} {
		t.Run(tc.principal, func(t *testing.T) {
			// When: the principal fetches the object through a presigned URL
			presigner := s3.NewPresignClient(s3.NewFromConfig(sdkConfigFor(srv, tc.principal), func(o *s3.Options) { o.UsePathStyle = true }))
			req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("presigned"), Key: aws.String("k")})
			if err != nil {
				t.Fatalf("PresignGetObject: %v", err)
			}
			resp, err := http.Get(req.URL)
			if err != nil {
				t.Fatalf("GET presigned URL: %v", err)
			}
			defer resp.Body.Close()

			// Then: it is authorised as s3:GetObject
			helpers.AssertStatus(t, resp, tc.want)
		})
	}
}

// withQueryParameter adds key=value to the query string of every request the
// SDK sends, after the SDK has serialized its own.
func withQueryParameter(key, value string) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		return stack.Build.Add(middleware.BuildMiddlewareFunc("AddQueryParameter",
			func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
				if req, ok := in.Request.(*smithyhttp.Request); ok {
					query := req.URL.Query()
					query.Set(key, value)
					req.URL.RawQuery = query.Encode()
				}
				return next.HandleBuild(ctx, in)
			}), middleware.After)
	}
}

func TestIAMEnforcePathScope_sdkCreateBucketSignedForSTS(t *testing.T) {
	// Given: a principal allowed sts:* and nothing in S3, and an S3 client that
	// signs for sts and adds an STS Action to its query string
	srv := helpers.NewTestServer(t, helpers.WithEnforceIAM(true))
	seedIAMPrincipal(t, srv, "sts-only", stsOnlyPolicy)
	client := s3.NewFromConfig(sdkConfigFor(srv, "sts-only"), func(o *s3.Options) {
		o.UsePathStyle = true
		o.APIOptions = append(o.APIOptions, withQueryParameter("Action", "GetFederationToken"))
	}, s3.WithSigV4SigningName("sts"))

	// When: it creates a bucket
	_, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("sdk-bucket")})

	// Then: the SDK reads S3's AccessDenied for s3:CreateBucket, and no bucket
	// exists
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" || !strings.Contains(apiErr.ErrorMessage(), "s3:CreateBucket") {
		t.Fatalf("CreateBucket signed for sts: err = %v, want AccessDenied for s3:CreateBucket", err)
	}
	assertNoBucket(t, srv, "sdk-bucket")
}
