package lambdaweb_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb/types"
	smithy "github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const roleARN = "arn:aws:iam::000000000000:role/web-role"

func client(srv *helpers.TestServer) *lambdaweb.Client {
	return lambdaweb.New(lambdaweb.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

// apiErrorCode returns the modeled error code an SDK call failed with.
func apiErrorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not an API error: %v", err)
	}
	return apiErr.ErrorCode()
}

type fixture struct {
	ctx context.Context
	web *lambdaweb.Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{ctx: context.Background(), web: client(helpers.NewTestServer(t))}
}

func revisionConfig(key string) *types.RevisionConfig {
	return &types.RevisionConfig{
		BuildConfig: &types.BuildConfig{
			CodeConfig:    &types.CodeConfig{S3Object: &types.S3Object{Bucket: aws.String("code-bucket"), Key: aws.String(key)}},
			RuntimeConfig: &types.RuntimeConfig{Runtime: aws.String("nodejs24.x")},
		},
		ServiceConfig: &types.ServiceConfig{ExecutionRoleArn: aws.String(roleARN)},
	}
}

func homeEndpoint(name string, mode types.AutoDeploymentMode) *types.EndpointConfig {
	return &types.EndpointConfig{
		EndpointName: aws.String(name), EndpointType: types.EndpointTypeHomeRegion,
		AuthType: types.AuthTypeApplicationManaged, AutoDeploymentMode: mode,
	}
}

// createApp creates a web function with a first revision and a LatestRevision
// home endpoint, the shape `aws lambda-web deploy` makes.
func (f *fixture) createApp(t *testing.T, name string) *lambdaweb.CreateWebFunctionOutput {
	t.Helper()
	out, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{
		FunctionName:   aws.String(name),
		RevisionConfig: revisionConfig("app-v1.zip"),
		EndpointConfig: homeEndpoint("dev", types.AutoDeploymentModeLatestRevision),
	})
	if err != nil {
		t.Fatalf("CreateWebFunction: %v", err)
	}
	return out
}

func (f *fixture) newRevision(t *testing.T, name, key string) string {
	t.Helper()
	cfg := revisionConfig(key)
	out, err := f.web.CreateWebFunctionRevision(f.ctx, &lambdaweb.CreateWebFunctionRevisionInput{
		FunctionName: aws.String(name), BuildConfig: cfg.BuildConfig, ServiceConfig: cfg.ServiceConfig,
	})
	if err != nil {
		t.Fatalf("CreateWebFunctionRevision: %v", err)
	}
	return aws.ToString(out.RevisionId)
}

func weights(pairs ...any) []types.RevisionWeight {
	var out []types.RevisionWeight
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, types.RevisionWeight{RevisionId: aws.String(pairs[i].(string)), Weight: aws.Int32(int32(pairs[i+1].(int)))})
	}
	return out
}

// ─── Web functions ────────────────────────────────────────────────────────────

func TestCreateWebFunction_withRevisionAndEndpoint(t *testing.T) {
	// Given: a server
	f := newFixture(t)

	// When: a web function is created with its first revision and endpoint
	out := f.createApp(t, "my-web-app")

	// Then: the function, revision and endpoint answer as AWS's create does
	if aws.ToString(out.FunctionArn) != "arn:aws:lambda:us-east-1:000000000000:web-function/my-web-app" {
		t.Errorf("FunctionArn = %q", aws.ToString(out.FunctionArn))
	}
	if out.State != types.FunctionStatePending {
		t.Errorf("State = %s, want Pending", out.State)
	}
	if out.Revision == nil || !strings.HasPrefix(aws.ToString(out.Revision.RevisionId), "rev-") {
		t.Fatalf("Revision = %+v", out.Revision)
	}
	if out.Endpoint == nil || out.Endpoint.EndpointType != types.EndpointTypeHomeRegion || out.Endpoint.AutoDeploymentMode != types.AutoDeploymentModeLatestRevision {
		t.Fatalf("Endpoint = %+v", out.Endpoint)
	}

	// And: the endpoint's domain follows AWS's regional form
	domain := regexp.MustCompile(`^[a-z]{16}\.[a-z][0-9][a-z]\.lambda-web\.us-east-1\.on\.aws$`)
	if !domain.MatchString(aws.ToString(out.Endpoint.DomainName)) {
		t.Errorf("DomainName = %q", aws.ToString(out.Endpoint.DomainName))
	}

	// And: read back, everything is Active and the endpoint serves the revision
	fn, err := f.web.GetWebFunction(f.ctx, &lambdaweb.GetWebFunctionInput{FunctionName: aws.String("my-web-app")})
	if err != nil || fn.State != types.FunctionStateActive {
		t.Fatalf("GetWebFunction = %+v, %v", fn, err)
	}
	ep, err := f.web.GetWebFunctionEndpoint(f.ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("my-web-app"), EndpointName: aws.String("dev")})
	if err != nil {
		t.Fatalf("GetWebFunctionEndpoint: %v", err)
	}
	if ep.State != types.EndpointStateActive || ep.UpdateStatus != types.EndpointUpdateStatusSuccessful {
		t.Errorf("endpoint state = %s / %s", ep.State, ep.UpdateStatus)
	}
	if len(ep.RevisionWeights) != 1 || aws.ToString(ep.RevisionWeights[0].RevisionId) != aws.ToString(out.Revision.RevisionId) || aws.ToInt32(ep.RevisionWeights[0].Weight) != 100 {
		t.Errorf("RevisionWeights = %+v", ep.RevisionWeights)
	}
	if len(ep.RegionalEndpoints) != 1 || ep.RegionalEndpoints["us-east-1"].DomainName == nil {
		t.Errorf("RegionalEndpoints = %+v", ep.RegionalEndpoints)
	}
}

func TestCreateWebFunction_rejectsInvalidInput(t *testing.T) {
	f := newFixture(t)
	create := func(name string, cfg *types.RevisionConfig, ep *types.EndpointConfig) error {
		_, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{FunctionName: aws.String(name), RevisionConfig: cfg, EndpointConfig: ep})
		return err
	}
	python := revisionConfig("x.zip")
	python.BuildConfig.RuntimeConfig.Runtime = aws.String("python3.13")
	slow := revisionConfig("x.zip")
	slow.ServiceConfig.TimeoutSeconds = aws.Int32(901)
	badRate := homeEndpoint("dev", types.AutoDeploymentModeLatestRevision)
	badRate.ThrottleConfig = &types.ThrottleConfig{RateLimit: aws.Int32(150)}

	for _, tc := range []struct {
		name string
		fn   string
		cfg  *types.RevisionConfig
		ep   *types.EndpointConfig
	}{
		{"name begins with a hyphen", "-app", nil, nil},
		{"name too long", strings.Repeat("a", 65), nil, nil},
		{"non-Node.js runtime", "app", python, nil},
		{"timeout out of range", "app", slow, nil},
		{"rate limit not a supported value", "app", nil, badRate},
		{"LatestRevision on a MultiRegion endpoint", "app", nil, &types.EndpointConfig{
			EndpointName: aws.String("g"), EndpointType: types.EndpointTypeMultiRegion, AuthType: types.AuthTypeIamAuth,
			AutoDeploymentMode: types.AutoDeploymentModeLatestRevision, Regions: []string{"eu-west-1"},
		}},
	} {
		// When / Then: the call is refused
		if code := apiErrorCode(t, create(tc.fn, tc.cfg, tc.ep)); code != "ValidationException" {
			t.Errorf("%s: code = %s", tc.name, code)
		}
	}

	// And: a rejected call leaves no function behind
	list, err := f.web.ListWebFunctions(f.ctx, &lambdaweb.ListWebFunctionsInput{})
	if err != nil || len(list.Functions) != 0 {
		t.Errorf("ListWebFunctions = %+v, %v", list, err)
	}
}

func TestCreateWebFunction_duplicateName(t *testing.T) {
	// Given: an existing web function
	f := newFixture(t)
	f.createApp(t, "dup")

	// When: the name is created again
	_, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{FunctionName: aws.String("dup")})

	// Then: ConflictException
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("code = %s", code)
	}
}

func TestWebFunction_containerOnly(t *testing.T) {
	// Given: a web function created with no revision or endpoint (what the CloudFormation container does)
	f := newFixture(t)
	out, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{
		FunctionName: aws.String("shell"), Tags: map[string]string{"team": "web"},
	})
	if err != nil {
		t.Fatalf("CreateWebFunction: %v", err)
	}

	// Then: it has neither, and carries its tags
	if out.Revision != nil || out.Endpoint != nil || out.Tags["team"] != "web" {
		t.Errorf("CreateWebFunction = %+v", out)
	}
}

func TestGetWebFunction_notFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.web.GetWebFunction(f.ctx, &lambdaweb.GetWebFunctionInput{FunctionName: aws.String("missing")})
	if code := apiErrorCode(t, err); code != "ResourceNotFoundException" {
		t.Errorf("code = %s", code)
	}
}

func TestWebFunction_addressedByARN(t *testing.T) {
	// Given: a web function with an endpoint and a revision
	f := newFixture(t)
	out := f.createApp(t, "by-arn")

	// When: it is read by function, endpoint and revision ARN
	fn, err := f.web.GetWebFunction(f.ctx, &lambdaweb.GetWebFunctionInput{FunctionName: out.FunctionArn})
	if err != nil {
		t.Fatalf("GetWebFunction by ARN: %v", err)
	}
	ep, err := f.web.GetWebFunctionEndpoint(f.ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: out.FunctionArn, EndpointName: out.Endpoint.EndpointArn})
	if err != nil {
		t.Fatalf("GetWebFunctionEndpoint by ARN: %v", err)
	}
	rev, err := f.web.GetWebFunctionRevision(f.ctx, &lambdaweb.GetWebFunctionRevisionInput{FunctionName: aws.String("by-arn"), RevisionId: out.Revision.RevisionArn})
	if err != nil {
		t.Fatalf("GetWebFunctionRevision by ARN: %v", err)
	}

	// Then: each resolves to the same resource
	if aws.ToString(fn.FunctionName) != "by-arn" || aws.ToString(ep.EndpointName) != "dev" || aws.ToString(rev.RevisionId) != aws.ToString(out.Revision.RevisionId) {
		t.Errorf("fn=%+v ep=%+v rev=%+v", fn, ep, rev)
	}
}

func TestDeleteWebFunction_removesItsRevisionsAndEndpoints(t *testing.T) {
	// Given: a web function with a revision, an endpoint, tags and a policy
	f := newFixture(t)
	out := f.createApp(t, "doomed")
	if _, err := f.web.TagResource(f.ctx, &lambdaweb.TagResourceInput{Resource: out.FunctionArn, Tags: map[string]string{"k": "v"}}); err != nil {
		t.Fatalf("TagResource: %v", err)
	}

	// When: the function is deleted
	if _, err := f.web.DeleteWebFunction(f.ctx, &lambdaweb.DeleteWebFunctionInput{FunctionName: aws.String("doomed")}); err != nil {
		t.Fatalf("DeleteWebFunction: %v", err)
	}

	// Then: it and its children are gone, and the name is reusable without inheriting them
	if _, err := f.web.GetWebFunctionEndpoint(f.ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("doomed"), EndpointName: aws.String("dev")}); apiErrorCode(t, err) != "ResourceNotFoundException" {
		t.Errorf("endpoint survived: %v", err)
	}
	if _, err := f.web.CreateWebFunction(f.ctx, &lambdaweb.CreateWebFunctionInput{FunctionName: aws.String("doomed")}); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	tags, err := f.web.ListTags(f.ctx, &lambdaweb.ListTagsInput{Resource: out.FunctionArn})
	if err != nil || len(tags.Tags) != 0 {
		t.Errorf("ListTags after recreate = %+v, %v", tags, err)
	}
	revs, err := f.web.ListWebFunctionRevisions(f.ctx, &lambdaweb.ListWebFunctionRevisionsInput{FunctionName: aws.String("doomed")})
	if err != nil || len(revs.Revisions) != 0 {
		t.Errorf("revisions after recreate = %+v, %v", revs, err)
	}
}

func TestListWebFunctions_paginatesAndFilters(t *testing.T) {
	// Given: three web functions
	f := newFixture(t)
	for _, n := range []string{"alpha", "beta", "gamma"} {
		f.createApp(t, n)
	}

	// When: listed two at a time
	first, err := f.web.ListWebFunctions(f.ctx, &lambdaweb.ListWebFunctionsInput{MaxResults: aws.Int32(2)})
	if err != nil {
		t.Fatalf("ListWebFunctions: %v", err)
	}
	if len(first.Functions) != 2 || first.NextToken == nil {
		t.Fatalf("first page = %+v", first)
	}
	if !regexp.MustCompile(`^[a-f0-9-]{36}$`).MatchString(aws.ToString(first.NextToken)) {
		t.Errorf("NextToken %q breaks the model pattern", aws.ToString(first.NextToken))
	}
	second, err := f.web.ListWebFunctions(f.ctx, &lambdaweb.ListWebFunctionsInput{MaxResults: aws.Int32(2), NextToken: first.NextToken})
	if err != nil || len(second.Functions) != 1 || second.NextToken != nil {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	if aws.ToString(second.Functions[0].FunctionName) != "gamma" {
		t.Errorf("second page = %s", aws.ToString(second.Functions[0].FunctionName))
	}

	// And: a state filter that nothing matches returns nothing, an unknown one is refused
	none, err := f.web.ListWebFunctions(f.ctx, &lambdaweb.ListWebFunctionsInput{Filters: []types.Filter{{Name: aws.String("state"), Values: []string{"Failed"}}}})
	if err != nil || len(none.Functions) != 0 {
		t.Errorf("Failed filter = %+v, %v", none, err)
	}
	_, err = f.web.ListWebFunctions(f.ctx, &lambdaweb.ListWebFunctionsInput{Filters: []types.Filter{{Name: aws.String("runtime"), Values: []string{"x"}}}})
	if code := apiErrorCode(t, err); code != "ValidationException" {
		t.Errorf("unknown filter code = %s", code)
	}
}

func TestGetWebAccountSettings(t *testing.T) {
	// Given: one web function
	f := newFixture(t)
	f.createApp(t, "counted")

	// When: account settings are read
	out, err := f.web.GetWebAccountSettings(f.ctx, &lambdaweb.GetWebAccountSettingsInput{})
	if err != nil {
		t.Fatalf("GetWebAccountSettings: %v", err)
	}

	// Then: the documented quotas and the usage are reported
	q := out.AccountQuotas
	if aws.ToInt32(q.MaxTotalArmVCpus) != 2000 || aws.ToInt32(q.MaxTotalRateLimit) != 100000 ||
		aws.ToInt32(q.MaxRevisionsPerFunction) != 50 || aws.ToInt32(q.MaxEndpointsPerFunction) != 10 {
		t.Errorf("AccountQuotas = %+v", q)
	}
	if aws.ToInt32(out.AccountUsage.FunctionCount) != 1 {
		t.Errorf("FunctionCount = %d", aws.ToInt32(out.AccountUsage.FunctionCount))
	}
}

// ─── Revisions ────────────────────────────────────────────────────────────────

func TestCreateWebFunctionRevision_defaultsAndImmutability(t *testing.T) {
	// Given: a web function
	f := newFixture(t)
	f.createApp(t, "revs")

	// When: a revision is created with only the required settings
	cfg := revisionConfig("app-v2.zip")
	cfg.ServiceConfig.EnvironmentVariables = map[string]string{"MODE": "prod"}
	out, err := f.web.CreateWebFunctionRevision(f.ctx, &lambdaweb.CreateWebFunctionRevisionInput{
		FunctionName: aws.String("revs"), Description: aws.String("second"), BuildConfig: cfg.BuildConfig, ServiceConfig: cfg.ServiceConfig,
	})
	if err != nil {
		t.Fatalf("CreateWebFunctionRevision: %v", err)
	}

	// Then: it is Pending in the create response and carries the documented defaults
	if out.State != types.RevisionStatePending {
		t.Errorf("State = %s", out.State)
	}
	if aws.ToInt32(out.ServiceConfig.TimeoutSeconds) != 30 || aws.ToInt32(out.ServiceConfig.MaxConcurrencyPerEnvironment) != 64 {
		t.Errorf("ServiceConfig defaults = %+v", out.ServiceConfig)
	}
	if !strings.HasSuffix(aws.ToString(out.RevisionArn), "/revision/"+aws.ToString(out.RevisionId)) {
		t.Errorf("RevisionArn = %q", aws.ToString(out.RevisionArn))
	}

	// And: Active when read back, with its settings intact
	got, err := f.web.GetWebFunctionRevision(f.ctx, &lambdaweb.GetWebFunctionRevisionInput{FunctionName: aws.String("revs"), RevisionId: out.RevisionId})
	if err != nil {
		t.Fatalf("GetWebFunctionRevision: %v", err)
	}
	if got.State != types.RevisionStateActive || aws.ToString(got.Description) != "second" ||
		got.ServiceConfig.EnvironmentVariables["MODE"] != "prod" ||
		aws.ToString(got.BuildConfig.CodeConfig.S3Object.Key) != "app-v2.zip" {
		t.Errorf("GetWebFunctionRevision = %+v", got)
	}
}

func TestCreateWebFunctionRevision_routesLatestRevisionEndpoints(t *testing.T) {
	// Given: a LatestRevision endpoint serving revision 1
	f := newFixture(t)
	f.createApp(t, "latest")

	// When: a second revision is created
	second := f.newRevision(t, "latest", "app-v2.zip")

	// Then: the endpoint serves it
	ep, err := f.web.GetWebFunctionEndpoint(f.ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("latest"), EndpointName: aws.String("dev")})
	if err != nil {
		t.Fatalf("GetWebFunctionEndpoint: %v", err)
	}
	if len(ep.RevisionWeights) != 1 || aws.ToString(ep.RevisionWeights[0].RevisionId) != second {
		t.Errorf("RevisionWeights = %+v, want %s", ep.RevisionWeights, second)
	}
}

func TestDeleteWebFunctionRevision_refusedWhileServingTraffic(t *testing.T) {
	// Given: a Disabled endpoint serving a canary split across two revisions
	f := newFixture(t)
	first := aws.ToString(f.createApp(t, "canary").Revision.RevisionId)
	second := f.newRevision(t, "canary", "app-v2.zip")
	if _, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("canary"), EndpointName: aws.String("dev"),
		AutoDeploymentMode: types.AutoDeploymentModeDisabled, RevisionWeights: weights(first, 90, second, 10),
	}); err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}

	// When: a revision the endpoint routes to is deleted
	_, err := f.web.DeleteWebFunctionRevision(f.ctx, &lambdaweb.DeleteWebFunctionRevisionInput{FunctionName: aws.String("canary"), RevisionId: aws.String(second)})

	// Then: it is refused
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("code = %s", code)
	}

	// And: once all traffic moves to the first, the second can go
	if _, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("canary"), EndpointName: aws.String("dev"), RevisionWeights: weights(first, 100),
	}); err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}
	if _, err := f.web.DeleteWebFunctionRevision(f.ctx, &lambdaweb.DeleteWebFunctionRevisionInput{FunctionName: aws.String("canary"), RevisionId: aws.String(second)}); err != nil {
		t.Fatalf("DeleteWebFunctionRevision: %v", err)
	}
	if _, err := f.web.GetWebFunctionRevision(f.ctx, &lambdaweb.GetWebFunctionRevisionInput{FunctionName: aws.String("canary"), RevisionId: aws.String(second)}); apiErrorCode(t, err) != "ResourceNotFoundException" {
		t.Errorf("revision survived deletion: %v", err)
	}
}

func TestCreateWebFunctionRevision_cleansUpPastFifty(t *testing.T) {
	// Given: a function holding 50 revisions, the first served by its endpoint
	f := newFixture(t)
	first := aws.ToString(f.createApp(t, "many").Revision.RevisionId)
	if _, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("many"), EndpointName: aws.String("dev"),
		AutoDeploymentMode: types.AutoDeploymentModeDisabled, RevisionWeights: weights(first, 100),
	}); err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}
	var second string
	for i := 2; i <= 50; i++ {
		id := f.newRevision(t, "many", fmt.Sprintf("v%d.zip", i))
		if i == 2 {
			second = id
		}
	}

	// When: a 51st revision is created
	last := f.newRevision(t, "many", "v51.zip")

	// Then: the oldest revision nothing routes to is cleaned up, and the served one is kept
	list, err := f.web.ListWebFunctionRevisions(f.ctx, &lambdaweb.ListWebFunctionRevisionsInput{FunctionName: aws.String("many")})
	if err != nil {
		t.Fatalf("ListWebFunctionRevisions: %v", err)
	}
	var ids []string
	token := list.NextToken
	for _, r := range list.Revisions {
		ids = append(ids, aws.ToString(r.RevisionId))
	}
	for token != nil {
		next, err := f.web.ListWebFunctionRevisions(f.ctx, &lambdaweb.ListWebFunctionRevisionsInput{FunctionName: aws.String("many"), NextToken: token})
		if err != nil {
			t.Fatalf("ListWebFunctionRevisions: %v", err)
		}
		for _, r := range next.Revisions {
			ids = append(ids, aws.ToString(r.RevisionId))
		}
		token = next.NextToken
	}
	has := func(id string) bool {
		for _, v := range ids {
			if v == id {
				return true
			}
		}
		return false
	}
	if len(ids) != 50 || !has(first) || has(second) || !has(last) {
		t.Errorf("revisions = %d (first kept %v, second kept %v, last kept %v)", len(ids), has(first), has(second), has(last))
	}
}

// ─── Endpoints ────────────────────────────────────────────────────────────────

func TestCreateWebFunctionEndpoint_multiRegion(t *testing.T) {
	// Given: a web function with a revision
	f := newFixture(t)
	rev := aws.ToString(f.createApp(t, "global").Revision.RevisionId)

	// When: a MultiRegion endpoint is created without its home Region listed
	out, err := f.web.CreateWebFunctionEndpoint(f.ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName: aws.String("global"), EndpointName: aws.String("world"), EndpointType: types.EndpointTypeMultiRegion,
		AuthType: types.AuthTypeIamAuth, Regions: []string{"eu-west-1", "ap-northeast-1"}, RevisionWeights: weights(rev, 100),
	})
	if err != nil {
		t.Fatalf("CreateWebFunctionEndpoint: %v", err)
	}

	// Then: the home Region is added, the domain is global, and every Region is listed
	if want := []string{"us-east-1", "eu-west-1", "ap-northeast-1"}; strings.Join(out.Regions, ",") != strings.Join(want, ",") {
		t.Errorf("Regions = %v, want %v", out.Regions, want)
	}
	if !regexp.MustCompile(`^[a-z]{11}\.lambda-web\.global\.on\.aws$`).MatchString(aws.ToString(out.DomainName)) {
		t.Errorf("DomainName = %q", aws.ToString(out.DomainName))
	}
	if len(out.RegionalEndpoints) != 3 || out.State != types.EndpointStatePending {
		t.Errorf("RegionalEndpoints = %d, State = %s", len(out.RegionalEndpoints), out.State)
	}
}

func TestCreateWebFunctionEndpoint_perRegionDomainsDiffer(t *testing.T) {
	f := newFixture(t)
	rev := aws.ToString(f.createApp(t, "perregion").Revision.RevisionId)
	out, err := f.web.CreateWebFunctionEndpoint(f.ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName: aws.String("perregion"), EndpointName: aws.String("each"), EndpointType: types.EndpointTypePerRegion,
		AuthType: types.AuthTypeApplicationManaged, Regions: []string{"eu-west-1"}, RevisionWeights: weights(rev, 100),
	})
	if err != nil {
		t.Fatalf("CreateWebFunctionEndpoint: %v", err)
	}
	home, eu := aws.ToString(out.RegionalEndpoints["us-east-1"].DomainName), aws.ToString(out.RegionalEndpoints["eu-west-1"].DomainName)
	if home == "" || eu == "" || home == eu || !strings.HasSuffix(eu, ".lambda-web.eu-west-1.on.aws") {
		t.Errorf("regional domains = %q, %q", home, eu)
	}
}

func TestCreateWebFunctionEndpoint_validation(t *testing.T) {
	f := newFixture(t)
	rev := aws.ToString(f.createApp(t, "rules").Revision.RevisionId)
	create := func(mutate func(*lambdaweb.CreateWebFunctionEndpointInput)) error {
		in := &lambdaweb.CreateWebFunctionEndpointInput{
			FunctionName: aws.String("rules"), EndpointName: aws.String("e1"), EndpointType: types.EndpointTypeHomeRegion,
			AuthType: types.AuthTypeApplicationManaged, RevisionWeights: weights(rev, 100),
		}
		mutate(in)
		_, err := f.web.CreateWebFunctionEndpoint(f.ctx, in)
		return err
	}
	for _, tc := range []struct {
		name   string
		mutate func(*lambdaweb.CreateWebFunctionEndpointInput)
		code   string
	}{
		{"multi-region with only the home region", func(in *lambdaweb.CreateWebFunctionEndpointInput) {
			in.EndpointType, in.Regions = types.EndpointTypeMultiRegion, []string{"us-east-1"}
		}, "ValidationException"},
		{"multi-region without weights", func(in *lambdaweb.CreateWebFunctionEndpointInput) {
			in.EndpointType, in.Regions, in.RevisionWeights = types.EndpointTypeMultiRegion, []string{"eu-west-1"}, nil
		}, "ValidationException"},
		{"home endpoint listing another region", func(in *lambdaweb.CreateWebFunctionEndpointInput) { in.Regions = []string{"eu-west-1"} }, "ValidationException"},
		{"weights not totalling 100", func(in *lambdaweb.CreateWebFunctionEndpointInput) { in.RevisionWeights = weights(rev, 60) }, "ValidationException"},
		{"three revisions", func(in *lambdaweb.CreateWebFunctionEndpointInput) {
			in.RevisionWeights = weights(rev, 50, "rev-aaaaa", 25, "rev-bbbbb", 25)
		}, "ValidationException"},
		{"unknown revision", func(in *lambdaweb.CreateWebFunctionEndpointInput) { in.RevisionWeights = weights("rev-nope0", 100) }, "ResourceNotFoundException"},
		{"weights with LatestRevision", func(in *lambdaweb.CreateWebFunctionEndpointInput) {
			in.AutoDeploymentMode = types.AutoDeploymentModeLatestRevision
		}, "ValidationException"},
		{"name ends with an underscore", func(in *lambdaweb.CreateWebFunctionEndpointInput) { in.EndpointName = aws.String("e1_") }, "ValidationException"},
	} {
		if code := apiErrorCode(t, create(tc.mutate)); code != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.name, code, tc.code)
		}
	}
}

func TestCreateWebFunctionEndpoint_duplicateAndQuota(t *testing.T) {
	// Given: a web function with ten endpoints (the limit)
	f := newFixture(t)
	f.createApp(t, "quota") // endpoint "dev" is the first
	for i := 2; i <= 10; i++ {
		_, err := f.web.CreateWebFunctionEndpoint(f.ctx, &lambdaweb.CreateWebFunctionEndpointInput{
			FunctionName: aws.String("quota"), EndpointName: aws.String(fmt.Sprintf("e%d", i)), EndpointType: types.EndpointTypeHomeRegion,
			AuthType: types.AuthTypeApplicationManaged, AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
		})
		if err != nil {
			t.Fatalf("endpoint %d: %v", i, err)
		}
	}

	// When / Then: an eleventh is a quota error, and a repeated name is a conflict
	_, err := f.web.CreateWebFunctionEndpoint(f.ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName: aws.String("quota"), EndpointName: aws.String("e11"), EndpointType: types.EndpointTypeHomeRegion, AuthType: types.AuthTypeApplicationManaged,
		AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
	})
	if code := apiErrorCode(t, err); code != "ServiceQuotaExceededException" {
		t.Errorf("11th endpoint code = %s", code)
	}
	_, err = f.web.CreateWebFunctionEndpoint(f.ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName: aws.String("quota"), EndpointName: aws.String("e2"), EndpointType: types.EndpointTypeHomeRegion, AuthType: types.AuthTypeApplicationManaged,
		AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
	})
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("duplicate endpoint code = %s", code)
	}
}

func TestUpdateWebFunctionEndpoint_keepsOmittedAndClearsEmpty(t *testing.T) {
	// Given: an endpoint with a description, scaling and throttle config
	f := newFixture(t)
	rev := aws.ToString(f.createApp(t, "tuned").Revision.RevisionId)
	if _, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("tuned"), EndpointName: aws.String("dev"), Description: aws.String("first"),
		ScalingConfig: &types.ScalingConfig{MaxEnvironments: aws.Int32(50)}, ThrottleConfig: &types.ThrottleConfig{RateLimit: aws.Int32(8000)},
	}); err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}

	// When: only the auth type changes
	out, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("tuned"), EndpointName: aws.String("dev"), AuthType: types.AuthTypeIamAuth,
	})
	if err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}

	// Then: everything else is kept
	if out.AuthType != types.AuthTypeIamAuth || aws.ToString(out.Description) != "first" ||
		aws.ToInt32(out.ScalingConfig.MaxEnvironments) != 50 || aws.ToInt32(out.ThrottleConfig.RateLimit) != 8000 {
		t.Errorf("after auth update = %+v", out)
	}
	if len(out.RevisionWeights) != 1 || aws.ToString(out.RevisionWeights[0].RevisionId) != rev {
		t.Errorf("RevisionWeights = %+v", out.RevisionWeights)
	}

	// And: an empty throttleConfig clears the rate limit
	cleared, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("tuned"), EndpointName: aws.String("dev"), ThrottleConfig: &types.ThrottleConfig{},
	})
	if err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}
	if cleared.ThrottleConfig != nil || cleared.ScalingConfig == nil {
		t.Errorf("after clearing = scaling %+v throttle %+v", cleared.ScalingConfig, cleared.ThrottleConfig)
	}
}

func TestUpdateWebFunctionEndpoint_switchingToLatestRevisionServesNewest(t *testing.T) {
	// Given: a Disabled endpoint serving the first of two revisions
	f := newFixture(t)
	first := aws.ToString(f.createApp(t, "switch").Revision.RevisionId)
	second := f.newRevision(t, "switch", "v2.zip")
	if _, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("switch"), EndpointName: aws.String("dev"), AutoDeploymentMode: types.AutoDeploymentModeDisabled, RevisionWeights: weights(first, 100),
	}); err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}

	// When: it switches to LatestRevision
	out, err := f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("switch"), EndpointName: aws.String("dev"), AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
	})
	if err != nil {
		t.Fatalf("UpdateWebFunctionEndpoint: %v", err)
	}

	// Then: it serves the newest revision
	if len(out.RevisionWeights) != 1 || aws.ToString(out.RevisionWeights[0].RevisionId) != second {
		t.Errorf("RevisionWeights = %+v, want %s", out.RevisionWeights, second)
	}

	// And: weights can't be set while in LatestRevision mode
	_, err = f.web.UpdateWebFunctionEndpoint(f.ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String("switch"), EndpointName: aws.String("dev"), RevisionWeights: weights(first, 100),
	})
	if code := apiErrorCode(t, err); code != "ValidationException" {
		t.Errorf("weights under LatestRevision code = %s", code)
	}
}

func TestListWebFunctionEndpoints_filters(t *testing.T) {
	// Given: one IAM-authenticated endpoint beside the application-managed one
	f := newFixture(t)
	f.createApp(t, "filtered")
	if _, err := f.web.CreateWebFunctionEndpoint(f.ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName: aws.String("filtered"), EndpointName: aws.String("private"), EndpointType: types.EndpointTypeHomeRegion,
		AuthType: types.AuthTypeIamAuth, AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
	}); err != nil {
		t.Fatalf("CreateWebFunctionEndpoint: %v", err)
	}

	// When: listed by auth type
	out, err := f.web.ListWebFunctionEndpoints(f.ctx, &lambdaweb.ListWebFunctionEndpointsInput{
		FunctionName: aws.String("filtered"), Filters: []types.Filter{{Name: aws.String("authType"), Values: []string{"IamAuth"}}},
	})

	// Then: only that endpoint is returned
	if err != nil || len(out.Endpoints) != 1 || aws.ToString(out.Endpoints[0].EndpointName) != "private" {
		t.Errorf("ListWebFunctionEndpoints = %+v, %v", out, err)
	}
}

func TestDeleteWebFunctionEndpoint(t *testing.T) {
	f := newFixture(t)
	f.createApp(t, "gone")
	if _, err := f.web.DeleteWebFunctionEndpoint(f.ctx, &lambdaweb.DeleteWebFunctionEndpointInput{FunctionName: aws.String("gone"), EndpointName: aws.String("dev")}); err != nil {
		t.Fatalf("DeleteWebFunctionEndpoint: %v", err)
	}
	_, err := f.web.GetWebFunctionEndpoint(f.ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String("gone"), EndpointName: aws.String("dev")})
	if code := apiErrorCode(t, err); code != "ResourceNotFoundException" {
		t.Errorf("code = %s", code)
	}
}

// ─── Tags ─────────────────────────────────────────────────────────────────────

func TestTags_roundTrip(t *testing.T) {
	// Given: a web function
	f := newFixture(t)
	arn := f.createApp(t, "tagged").FunctionArn

	// When: tags are added, one overwritten and one removed
	if _, err := f.web.TagResource(f.ctx, &lambdaweb.TagResourceInput{Resource: arn, Tags: map[string]string{"a": "1", "b": "2"}}); err != nil {
		t.Fatalf("TagResource: %v", err)
	}
	if _, err := f.web.TagResource(f.ctx, &lambdaweb.TagResourceInput{Resource: arn, Tags: map[string]string{"a": "9"}}); err != nil {
		t.Fatalf("TagResource: %v", err)
	}
	if _, err := f.web.UntagResource(f.ctx, &lambdaweb.UntagResourceInput{Resource: arn, TagKeys: []string{"b"}}); err != nil {
		t.Fatalf("UntagResource: %v", err)
	}

	// Then: ListTags shows the merged result
	out, err := f.web.ListTags(f.ctx, &lambdaweb.ListTagsInput{Resource: arn})
	if err != nil || len(out.Tags) != 1 || out.Tags["a"] != "9" {
		t.Errorf("ListTags = %+v, %v", out, err)
	}
}

func TestTags_onlyAFunctionARN(t *testing.T) {
	f := newFixture(t)
	out := f.createApp(t, "arns")
	_, err := f.web.ListTags(f.ctx, &lambdaweb.ListTagsInput{Resource: out.Endpoint.EndpointArn})
	if code := apiErrorCode(t, err); code != "ValidationException" {
		t.Errorf("endpoint ARN code = %s", code)
	}
	_, err = f.web.ListTags(f.ctx, &lambdaweb.ListTagsInput{Resource: aws.String("arn:aws:lambda:us-east-1:000000000000:web-function/absent")})
	if code := apiErrorCode(t, err); code != "ResourceNotFoundException" {
		t.Errorf("absent function code = %s", code)
	}
}

// ─── Resource policy ──────────────────────────────────────────────────────────

func policyFor(arn, principal, extra string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":%s,"Action":"lambda:InvokeWebFunctionEndpoint","Resource":"%s/endpoint/dev"%s}]}`, principal, arn, extra)
}

func TestResourcePolicy_lifecycle(t *testing.T) {
	// Given: a web function
	f := newFixture(t)
	arn := aws.ToString(f.createApp(t, "policy").FunctionArn)

	// When: a policy naming an account is attached
	put, err := f.web.PutResourcePolicy(f.ctx, &lambdaweb.PutResourcePolicyInput{
		ResourceArn: aws.String(arn), Policy: aws.String(policyFor(arn, `{"AWS":"444455556666"}`, "")),
	})
	if err != nil {
		t.Fatalf("PutResourcePolicy: %v", err)
	}

	// Then: it reads back with its revision ID
	got, err := f.web.GetResourcePolicy(f.ctx, &lambdaweb.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	if err != nil || aws.ToString(got.RevisionId) != aws.ToString(put.RevisionId) || !strings.Contains(aws.ToString(got.Policy), "444455556666") {
		t.Fatalf("GetResourcePolicy = %+v, %v", got, err)
	}

	// And: a stale revision ID is a conflict, a current one replaces the policy
	_, err = f.web.PutResourcePolicy(f.ctx, &lambdaweb.PutResourcePolicyInput{
		ResourceArn: aws.String(arn), Policy: aws.String(policyFor(arn, `{"AWS":"111122223333"}`, "")), RevisionId: aws.String("00000000-0000-0000-0000-000000000000"),
	})
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("stale revision code = %s", code)
	}
	next, err := f.web.PutResourcePolicy(f.ctx, &lambdaweb.PutResourcePolicyInput{
		ResourceArn: aws.String(arn), Policy: aws.String(policyFor(arn, `{"AWS":"111122223333"}`, "")), RevisionId: put.RevisionId,
	})
	if err != nil || aws.ToString(next.RevisionId) == aws.ToString(put.RevisionId) {
		t.Fatalf("replacing PutResourcePolicy = %+v, %v", next, err)
	}

	// And: delete removes it
	if _, err := f.web.DeleteResourcePolicy(f.ctx, &lambdaweb.DeleteResourcePolicyInput{ResourceArn: aws.String(arn)}); err != nil {
		t.Fatalf("DeleteResourcePolicy: %v", err)
	}
	_, err = f.web.GetResourcePolicy(f.ctx, &lambdaweb.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	if code := apiErrorCode(t, err); code != "ResourceNotFoundException" {
		t.Errorf("after delete code = %s", code)
	}
}

func TestPutResourcePolicy_rejectsPublicPolicies(t *testing.T) {
	f := newFixture(t)
	arn := aws.ToString(f.createApp(t, "public").FunctionArn)
	put := func(policy string) error {
		_, err := f.web.PutResourcePolicy(f.ctx, &lambdaweb.PutResourcePolicyInput{ResourceArn: aws.String(arn), Policy: aws.String(policy)})
		return err
	}

	// A wildcard principal with no condition, or with an IP condition, is public.
	if code := apiErrorCode(t, put(policyFor(arn, `"*"`, ""))); code != "ValidationException" {
		t.Errorf("bare wildcard code = %s", code)
	}
	if code := apiErrorCode(t, put(policyFor(arn, `{"AWS":"*"}`, `,"Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`))); code != "ValidationException" {
		t.Errorf("source-IP condition code = %s", code)
	}
	// A trusted condition key pinned to a value makes it non-public.
	org := policyFor(arn, `{"AWS":"*"}`, `,"Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-a1b2c3d4e5"}}`)
	if err := put(org); err != nil {
		t.Errorf("org-scoped policy: %v", err)
	}
}
