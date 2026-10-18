package lambdaweb_test

// Revision and endpoint lifecycle through the SDK, with the real S3 service
// behind the code source. The shared TestServer does not wire a code source
// (the router owns that), so these tests mount the two services on a small
// server of their own, wired as the router wires them.

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	lwsvc "github.com/overcast-sh/overcast/internal/services/lambdaweb"
	s3svc "github.com/overcast-sh/overcast/internal/services/s3"
	"github.com/overcast-sh/overcast/internal/state"
)

type s3Fixture struct {
	*fixture
	s3 *awss3.Client
}

func newS3Fixture(t *testing.T) *s3Fixture {
	t.Helper()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", DataDir: t.TempDir()}
	store := state.NewMemoryStore()
	clk := clock.New()
	bus := events.NewBus()
	s3s := s3svc.New(cfg, store, zap.NewNop(), clk, bus)
	web := lwsvc.New(cfg, store, zap.NewNop(), clk)
	web.InitCodeSource(lwsvc.NewS3CodeSource(s3s))

	r := chi.NewRouter()
	r.Use(middleware.Region)
	web.RegisterRoutes(r)
	s3s.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		web.StopRevisionWorker(ctx)
		s3s.Stop(ctx)
		bus.Stop()
	})

	creds := credentials.NewStaticCredentialsProvider("test", "test", "")
	return &s3Fixture{
		fixture: &fixture{ctx: context.Background(), web: lambdaweb.New(lambdaweb.Options{
			Region: "us-east-1", Credentials: creds, BaseEndpoint: aws.String(srv.URL), HTTPClient: http.DefaultClient,
		})},
		s3: awss3.New(awss3.Options{
			Region: "us-east-1", Credentials: creds, BaseEndpoint: aws.String(srv.URL), HTTPClient: http.DefaultClient, UsePathStyle: true,
		}),
	}
}

func (f *s3Fixture) bucket(t *testing.T, name string, versioned bool) {
	t.Helper()
	if _, err := f.s3.CreateBucket(f.ctx, &awss3.CreateBucketInput{Bucket: aws.String(name)}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if versioned {
		if _, err := f.s3.PutBucketVersioning(f.ctx, &awss3.PutBucketVersioningInput{
			Bucket: aws.String(name), VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
		}); err != nil {
			t.Fatalf("PutBucketVersioning: %v", err)
		}
	}
}

func (f *s3Fixture) upload(t *testing.T, bucket, key string, body []byte) string {
	t.Helper()
	out, err := f.s3.PutObject(f.ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	return aws.ToString(out.VersionId)
}

func zipOf(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func cfgFor(bucket, key, versionID string) *types.RevisionConfig {
	c := revisionConfig(key)
	c.BuildConfig.CodeConfig.S3Object.Bucket = aws.String(bucket)
	if versionID != "" {
		c.BuildConfig.CodeConfig.S3Object.VersionId = aws.String(versionID)
	}
	return c
}

func (f *s3Fixture) createRevision(t *testing.T, fn string, cfg *types.RevisionConfig) (*lambdaweb.CreateWebFunctionRevisionOutput, error) {
	t.Helper()
	return f.web.CreateWebFunctionRevision(f.ctx, &lambdaweb.CreateWebFunctionRevisionInput{FunctionName: aws.String(fn), BuildConfig: cfg.BuildConfig, ServiceConfig: cfg.ServiceConfig})
}

func (f *s3Fixture) waitRevision(t *testing.T, fn, id string, want types.RevisionState) *lambdaweb.GetWebFunctionRevisionOutput {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := f.web.GetWebFunctionRevision(f.ctx, &lambdaweb.GetWebFunctionRevisionInput{FunctionName: aws.String(fn), RevisionId: aws.String(id)})
		if err != nil {
			t.Fatalf("GetWebFunctionRevision: %v", err)
		}
		if got.State == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("revision %s is %s, want %s", id, got.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCreateWebFunctionRevision_pinsTheS3VersionAndActivates(t *testing.T) {
	// Given: a versioned bucket holding two uploads of the same package key
	f := newS3Fixture(t)
	f.bucket(t, "code-bucket", true)
	first := f.upload(t, "code-bucket", "app.zip", zipOf(t, map[string]string{"index.js": "// one"}))
	second := f.upload(t, "code-bucket", "app.zip", zipOf(t, map[string]string{"index.js": "// two"}))
	if _, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{FunctionName: aws.String("pinned")}); err != nil {
		t.Fatalf("CreateWebFunction: %v", err)
	}

	// When: one revision omits the version and another names the first
	latest, err := f.createRevision(t, "pinned", cfgFor("code-bucket", "app.zip", ""))
	if err != nil {
		t.Fatalf("CreateWebFunctionRevision: %v", err)
	}
	older, err := f.createRevision(t, "pinned", cfgFor("code-bucket", "app.zip", first))
	if err != nil {
		t.Fatalf("CreateWebFunctionRevision: %v", err)
	}

	// Then: each is created Pending, pinned to its version, and becomes Active
	if latest.State != types.RevisionStatePending || older.State != types.RevisionStatePending {
		t.Errorf("create states = %s, %s", latest.State, older.State)
	}
	if got := aws.ToString(latest.BuildConfig.CodeConfig.S3Object.VersionId); got != second {
		t.Errorf("latest revision pinned %q, want %q", got, second)
	}
	if got := aws.ToString(older.BuildConfig.CodeConfig.S3Object.VersionId); got != first {
		t.Errorf("older revision pinned %q, want %q", got, first)
	}
	f.waitRevision(t, "pinned", aws.ToString(latest.RevisionId), types.RevisionStateActive)
	f.waitRevision(t, "pinned", aws.ToString(older.RevisionId), types.RevisionStateActive)

	// And: a later upload does not move the pin
	f.upload(t, "code-bucket", "app.zip", zipOf(t, map[string]string{"index.js": "// three"}))
	got := f.waitRevision(t, "pinned", aws.ToString(latest.RevisionId), types.RevisionStateActive)
	if aws.ToString(got.BuildConfig.CodeConfig.S3Object.VersionId) != second {
		t.Errorf("pin moved to %q", aws.ToString(got.BuildConfig.CodeConfig.S3Object.VersionId))
	}

	// And: the list is newest first
	list, err := f.web.ListWebFunctionRevisions(f.ctx, &lambdaweb.ListWebFunctionRevisionsInput{FunctionName: aws.String("pinned")})
	if err != nil || len(list.Revisions) != 2 || aws.ToString(list.Revisions[0].RevisionId) != aws.ToString(older.RevisionId) {
		t.Errorf("ListWebFunctionRevisions = %+v, %v; want %s first", list, err, aws.ToString(older.RevisionId))
	}
}

func TestCreateWebFunctionRevision_refusesPackagesS3CannotServe(t *testing.T) {
	// Given: a versioned bucket, an unversioned one, and a deleted object
	f := newS3Fixture(t)
	f.bucket(t, "versioned", true)
	f.bucket(t, "plain", false)
	f.upload(t, "versioned", "app.zip", zipOf(t, map[string]string{"index.js": "x"}))
	f.upload(t, "plain", "app.zip", zipOf(t, map[string]string{"index.js": "x"}))
	f.upload(t, "versioned", "gone.zip", zipOf(t, map[string]string{"index.js": "x"}))
	if _, err := f.s3.DeleteObject(f.ctx, &awss3.DeleteObjectInput{Bucket: aws.String("versioned"), Key: aws.String("gone.zip")}); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{FunctionName: aws.String("refused")}); err != nil {
		t.Fatalf("CreateWebFunction: %v", err)
	}

	cases := map[string]*types.RevisionConfig{
		"missing bucket":             cfgFor("absent-bucket", "app.zip", ""),
		"unversioned bucket":         cfgFor("plain", "app.zip", ""),
		"missing key":                cfgFor("versioned", "nope.zip", ""),
		"missing version":            cfgFor("versioned", "app.zip", "no-such-version"),
		"key behind a delete marker": cfgFor("versioned", "gone.zip", ""),
	}
	for name, cfg := range cases {
		// When: a revision names the unusable package
		_, err := f.createRevision(t, "refused", cfg)

		// Then: the request fails with a ValidationException and no revision exists
		if code := apiErrorCode(t, err); code != "ValidationException" {
			t.Errorf("%s: code = %s", name, code)
		}
	}
	list, err := f.web.ListWebFunctionRevisions(f.ctx, &lambdaweb.ListWebFunctionRevisionsInput{FunctionName: aws.String("refused")})
	if err != nil || len(list.Revisions) != 0 {
		t.Errorf("revisions after refusals = %+v, %v", list, err)
	}
}

func TestCreateWebFunctionRevision_corruptPackageFails(t *testing.T) {
	// Given: a function and an object that is not a zip
	f := newS3Fixture(t)
	f.bucket(t, "code-bucket", true)
	f.upload(t, "code-bucket", "bad.zip", []byte("definitely not a zip"))
	f.upload(t, "code-bucket", "slip.zip", zipOf(t, map[string]string{"../../escape.js": "x"}))
	if _, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{FunctionName: aws.String("broken")}); err != nil {
		t.Fatalf("CreateWebFunction: %v", err)
	}

	for _, key := range []string{"bad.zip", "slip.zip"} {
		// When: a revision is created from it
		rev, err := f.createRevision(t, "broken", cfgFor("code-bucket", key, ""))
		if err != nil {
			t.Fatalf("CreateWebFunctionRevision(%s): %v", key, err)
		}

		// Then: it ends Failed, with the reason in stateReason and errors
		got := f.waitRevision(t, "broken", aws.ToString(rev.RevisionId), types.RevisionStateFailed)
		if aws.ToString(got.StateReason) == "" || len(got.Errors) != 1 ||
			aws.ToString(got.Errors[0].Attribute) != "buildConfig.codeConfig.s3Object" || aws.ToString(got.Errors[0].ErrorCode) == "" {
			t.Errorf("%s: StateReason = %q, Errors = %+v", key, aws.ToString(got.StateReason), got.Errors)
		}
	}

	// And: a Failed revision can't be routed to
	list, _ := f.web.ListWebFunctionRevisions(f.ctx, &lambdaweb.ListWebFunctionRevisionsInput{
		FunctionName: aws.String("broken"), Filters: []types.Filter{{Name: aws.String("state"), Values: []string{"Failed"}}},
	})
	if list == nil || len(list.Revisions) != 2 {
		t.Fatalf("Failed revisions = %+v", list)
	}
	_, err := f.web.CreateWebFunctionEndpoint(f.ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName: aws.String("broken"), EndpointName: aws.String("e"), EndpointType: types.EndpointTypeHomeRegion,
		AuthType: types.AuthTypeApplicationManaged, RevisionWeights: weights(aws.ToString(list.Revisions[0].RevisionId), 100),
	})
	if code := apiErrorCode(t, err); code != "ValidationException" {
		t.Errorf("weights on a Failed revision: code = %s", code)
	}
}

func TestCreateWebFunction_latestRevisionEndpointFollowsTheRevisionOnceActive(t *testing.T) {
	// Given: a package in a versioned bucket
	f := newS3Fixture(t)
	f.bucket(t, "code-bucket", true)
	f.upload(t, "code-bucket", "app-v1.zip", zipOf(t, map[string]string{"index.js": "x"}))

	// When: a function is created with a revision and a LatestRevision endpoint
	out, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{
		FunctionName: aws.String("follow"), RevisionConfig: revisionConfig("app-v1.zip"), EndpointConfig: homeEndpoint("dev", types.AutoDeploymentModeLatestRevision),
	})
	if err != nil {
		t.Fatalf("CreateWebFunction: %v", err)
	}
	rev := aws.ToString(out.Revision.RevisionId)

	// Then: the create reports Pending, and the endpoint reaches Active/Successful serving the revision
	if out.Revision.State != types.RevisionStatePending || out.Endpoint.State != types.EndpointStatePending {
		t.Errorf("create states = %s, %s", out.Revision.State, out.Endpoint.State)
	}
	f.waitRevision(t, "follow", rev, types.RevisionStateActive)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ep, err := f.web.GetWebFunctionEndpoint(f.ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("follow"), EndpointName: aws.String("dev")})
		if err != nil {
			t.Fatalf("GetWebFunctionEndpoint: %v", err)
		}
		if len(ep.RevisionWeights) == 1 && aws.ToString(ep.RevisionWeights[0].RevisionId) == rev && ep.State == types.EndpointStateActive && ep.UpdateStatus == types.EndpointUpdateStatusSuccessful {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("endpoint = %s / %s with weights %+v", ep.State, ep.UpdateStatus, ep.RevisionWeights)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCreateWebFunctionRevision_rejectsReservedEnvironmentVariables(t *testing.T) {
	// Given: a function
	f := newFixture(t)
	f.createApp(t, "env")
	f.code(t, "v2.zip")
	f.code(t, "v1.zip")

	for _, name := range []string{"AWS_REGION", "AWS_LAMBDA_HTTP_ENDPOINT", "AWS_SESSION_TOKEN", "AWS_LAMBDA_METADATA_TOKEN"} {
		// When: a revision sets a reserved variable
		cfg := revisionConfig("v2.zip")
		cfg.ServiceConfig.EnvironmentVariables = map[string]string{name: "x"}
		_, err := f.web.CreateWebFunctionRevision(f.ctx, &lambdaweb.CreateWebFunctionRevisionInput{FunctionName: aws.String("env"), BuildConfig: cfg.BuildConfig, ServiceConfig: cfg.ServiceConfig})

		// Then: it is refused
		if code := apiErrorCode(t, err); code != "ValidationException" {
			t.Errorf("%s: code = %s", name, code)
		}
	}

	// And: CreateWebFunction refuses it too
	cfg := revisionConfig("v1.zip")
	cfg.ServiceConfig.EnvironmentVariables = map[string]string{"AWS_ACCESS_KEY_ID": "x"}
	_, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{FunctionName: aws.String("env2"), RevisionConfig: cfg})
	if code := apiErrorCode(t, err); code != "ValidationException" || !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") {
		t.Errorf("CreateWebFunction: %v", err)
	}
}

func TestListWebFunctionRevisions_newestFirst(t *testing.T) {
	// Given: a function with three revisions created in order
	f := newFixture(t)
	first := aws.ToString(f.createApp(t, "ordered").Revision.RevisionId)
	second := f.newRevision(t, "ordered", "v2.zip")
	third := f.newRevision(t, "ordered", "v3.zip")

	// When: its revisions are listed
	list, err := f.web.ListWebFunctionRevisions(f.ctx, &lambdaweb.ListWebFunctionRevisionsInput{FunctionName: aws.String("ordered")})
	if err != nil {
		t.Fatalf("ListWebFunctionRevisions: %v", err)
	}

	// Then: the newest comes first
	var got []string
	for _, r := range list.Revisions {
		got = append(got, aws.ToString(r.RevisionId))
	}
	if want := []string{third, second, first}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestEndpointWaiters_seeActiveAndSuccessful(t *testing.T) {
	// Given: an endpoint just created and then updated
	f := newFixture(t)
	f.createApp(t, "waited")
	if _, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("waited"), EndpointName: aws.String("dev"), AuthType: types.AuthTypeIamAuth,
	}); err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}

	// When: the SDK waiters the CLI's `wait` commands use run
	in := &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("waited"), EndpointName: aws.String("dev")}
	active := lambdaweb.NewWebFunctionEndpointActiveWaiter(f.web)
	updated := lambdaweb.NewWebFunctionEndpointUpdatedWaiter(f.web)

	// Then: both complete
	if err := active.Wait(f.ctx, in, 5*time.Second); err != nil {
		t.Errorf("web-function-endpoint-active: %v", err)
	}
	if err := updated.Wait(f.ctx, in, 5*time.Second); err != nil {
		t.Errorf("web-function-endpoint-updated: %v", err)
	}
}
