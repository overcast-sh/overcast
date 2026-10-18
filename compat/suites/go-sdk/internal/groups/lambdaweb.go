package groups

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// LambdaWeb returns the Lambda Web Functions service group.
func LambdaWeb(c *clients.Clients) ServiceGroup {
	g := &lambdaWebGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"lambda-web-endpoints:ServeEndpointRequest":                    g.ServeEndpointRequest,
			"lambda-web-endpoints:ForwardRequestHeaders":                   g.ForwardRequestHeaders,
			"lambda-web-endpoints:AuthorizationHeader":                     g.AuthorizationHeader,
			"lambda-web-endpoints:LatestRevisionServesNewRevision":         g.LatestRevisionServesNewRevision,
			"lambda-web-endpoints:WeightedRouting":                         g.WeightedRouting,
			"lambda-web-endpoints:IamAuthRefusesUnsignedRequest":           g.IamAuthRefusesUnsignedRequest,
			"lambda-web-endpoints:ImportCodeError":                         g.ImportCodeError,
			"lambda-web-endpoints:ListenTimeout":                           g.ListenTimeout,
			"lambda-web-endpoints:UnknownDomain":                           g.UnknownDomain,
			"lambda-web-endpoints:UnsupportedMethod":                       g.UnsupportedMethod,
			"lambda-web-endpoints:EndpointDisabled":                        g.EndpointDisabled,
			"lambda-web-endpoints:EndpointRateLimit":                       g.EndpointRateLimit,
			"lambda-web-endpoints:RequestTimeout":                          g.RequestTimeout,
			"lambda-web-endpoints:DeletedEndpointDomain":                   g.DeletedEndpointDomain,
			"lambda-web-endpoints:StreamedResponse":                        g.StreamedResponse,
			"lambda-web-functions:CreateWebFunction":                       g.CreateWebFunction,
			"lambda-web-functions:GetWebFunction":                          g.GetWebFunction,
			"lambda-web-functions:ListWebFunctions":                        g.ListWebFunctions,
			"lambda-web-functions:GetWebFunctionRevision":                  g.GetWebFunctionRevision,
			"lambda-web-functions:CreateWebFunctionRevision":               g.CreateWebFunctionRevision,
			"lambda-web-functions:GetWebFunctionEndpointLatestRevision":    g.GetWebFunctionEndpointLatestRevision,
			"lambda-web-functions:ListWebFunctionRevisions":                g.ListWebFunctionRevisions,
			"lambda-web-functions:CreateWebFunctionEndpoint":               g.CreateWebFunctionEndpoint,
			"lambda-web-functions:GetWebFunctionEndpoint":                  g.GetWebFunctionEndpoint,
			"lambda-web-functions:ListWebFunctionEndpoints":                g.ListWebFunctionEndpoints,
			"lambda-web-functions:UpdateWebFunctionEndpoint":               g.UpdateWebFunctionEndpoint,
			"lambda-web-functions:UpdateWebFunctionEndpointClearThrottle":  g.UpdateWebFunctionEndpointClearThrottle,
			"lambda-web-functions:DeleteWebFunctionRevisionServingTraffic": g.DeleteWebFunctionRevisionServingTraffic,
			"lambda-web-functions:TagResource":                             g.TagResource,
			"lambda-web-functions:ListTags":                                g.ListTags,
			"lambda-web-functions:UntagResource":                           g.UntagResource,
			"lambda-web-functions:PutResourcePolicy":                       g.PutResourcePolicy,
			"lambda-web-functions:GetResourcePolicy":                       g.GetResourcePolicy,
			"lambda-web-functions:PutResourcePolicyPublicPrincipal":        g.PutResourcePolicyPublicPrincipal,
			"lambda-web-functions:DeleteResourcePolicy":                    g.DeleteResourcePolicy,
			"lambda-web-functions:GetWebAccountSettings":                   g.GetWebAccountSettings,
			"lambda-web-functions:DeleteWebFunctionEndpoint":               g.DeleteWebFunctionEndpoint,
			"lambda-web-functions:DeleteWebFunctionRevision":               g.DeleteWebFunctionRevision,
			"lambda-web-functions:DeleteWebFunction":                       g.DeleteWebFunction,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"lambda-web-functions": g.setup,
			"lambda-web-endpoints": g.setupEndpoints,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"lambda-web-functions": g.teardown,
			"lambda-web-endpoints": g.teardownEndpoints,
		},
	}
}

type lambdaWebGroup struct{ c *clients.Clients }

func (g *lambdaWebGroup) cl() *lambdaweb.Client { return g.c.LambdaWeb() }

const (
	lambdaWebRoleARN      = "arn:aws:iam::000000000000:role/compat-web-role"
	lambdaWebLiveEndpoint = "live"
	lambdaWebCanary       = "canary"
)

// fnName is the run's web function. Names allow letters, digits, hyphens and
// underscores, up to 64 characters, so the run ID and the group token fit.
func (g *lambdaWebGroup) fnName(t *harness.TestContext) string {
	return fmt.Sprintf("%s-lambda-web-functions", t.RunID)
}

// codeBucket is the group's versioned bucket holding the revisions' packages.
// CreateWebFunctionRevision checks that the bucket is versioned and the object
// exists, so the control-plane tests upload a real (if trivial) zip first.
func (g *lambdaWebGroup) codeBucket(t *harness.TestContext) string {
	return fmt.Sprintf("%s-lambda-web-functions", t.RunID)
}

// lambdaWebZip builds an in-memory zip of the given files.
func lambdaWebZip(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// seedBucket creates a versioned bucket and uploads each key as a zip holding
// the given files.
func (g *lambdaWebGroup) seedBucket(ctx context.Context, bucket string, keys []string, files map[string]string) error {
	cl := g.c.S3()
	if _, err := cl.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		return fmt.Errorf("CreateBucket %s: %w", bucket, err)
	}
	if _, err := cl.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
	}); err != nil {
		return fmt.Errorf("PutBucketVersioning %s: %w", bucket, err)
	}
	body, err := lambdaWebZip(files)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := cl.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		}); err != nil {
			return fmt.Errorf("PutObject %s/%s: %w", bucket, key, err)
		}
	}
	return nil
}

func (g *lambdaWebGroup) setup(ctx context.Context, t *harness.TestContext) error {
	return g.seedBucket(ctx, g.codeBucket(t), []string{"app-v1.zip", "app-v2.zip"},
		map[string]string{"index.js": "// compat control-plane package\n"})
}

// lambdaWebEventually polls check until it reports done, fails, or the
// deadline passes. Revisions and endpoints settle asynchronously, as they do
// on AWS, so a test waits the way the SDK waiters would.
func lambdaWebEventually(what string, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		done, err := check()
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: not reached within %v", what, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitRevisionActive polls GetWebFunctionRevision until the revision is Active.
func (g *lambdaWebGroup) waitRevisionActive(ctx context.Context, fn, id string) error {
	return lambdaWebEventually("revision "+id+" to become Active", 30*time.Second, func() (bool, error) {
		out, err := g.cl().GetWebFunctionRevision(ctx, &lambdaweb.GetWebFunctionRevisionInput{
			FunctionName: aws.String(fn), RevisionId: aws.String(id),
		})
		if err != nil {
			return false, err
		}
		switch out.State {
		case types.RevisionStateActive:
			return true, nil
		case types.RevisionStateFailed:
			return false, fmt.Errorf("revision %s is Failed: %+v", id, out.Errors)
		}
		return false, nil
	})
}

// revisionConfig is a revision pointing at a code zip in the group's bucket.
func (g *lambdaWebGroup) revisionConfig(t *harness.TestContext, key string) *types.RevisionConfig {
	return &types.RevisionConfig{
		BuildConfig: &types.BuildConfig{
			CodeConfig:    &types.CodeConfig{S3Object: &types.S3Object{Bucket: aws.String(g.codeBucket(t)), Key: aws.String(key)}},
			RuntimeConfig: &types.RuntimeConfig{Runtime: aws.String("nodejs24.x")},
		},
		ServiceConfig: &types.ServiceConfig{ExecutionRoleArn: aws.String(lambdaWebRoleARN)},
	}
}

func (g *lambdaWebGroup) functionARN(t *harness.TestContext) (string, error) {
	arn := t.GetString("lwFunctionARN")
	if arn == "" {
		return "", fmt.Errorf("no function ARN from CreateWebFunction")
	}
	return arn, nil
}

func (g *lambdaWebGroup) revisionIDs(t *harness.TestContext) (first, second string, err error) {
	first, second = t.GetString("lwRevision1"), t.GetString("lwRevision2")
	if first == "" || second == "" {
		return "", "", fmt.Errorf("missing revision IDs (first=%q second=%q)", first, second)
	}
	return first, second, nil
}

// errorCode returns the modeled error code an SDK call failed with, and the
// HTTP status the response carried.
func lambdaWebErrorCode(err error) (string, int) {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return "", 0
	}
	status := 0
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		status = respErr.HTTPStatusCode()
	}
	return apiErr.ErrorCode(), status
}

// expectError fails unless err is the named API error.
func lambdaWebExpectError(what string, err error, code string) error {
	if err == nil {
		return fmt.Errorf("%s: expected %s, got success", what, code)
	}
	got, _ := lambdaWebErrorCode(err)
	if got != code {
		return fmt.Errorf("%s: expected %s, got %q (%v)", what, code, got, err)
	}
	return nil
}

func lambdaWebPolicy(functionARN, principal string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":%s,`+
		`"Action":"lambda:InvokeWebFunctionEndpoint","Resource":"%s/endpoint/%s"}]}`,
		principal, functionARN, lambdaWebLiveEndpoint)
}

func (g *lambdaWebGroup) teardown(ctx context.Context, t *harness.TestContext) error {
	// Deleting the function removes its revisions, endpoints, tags and policy,
	// but the group says so explicitly rather than relying on the cascade.
	name := g.fnName(t)
	arn := t.GetString("lwFunctionARN")
	if arn != "" {
		g.cl().DeleteResourcePolicy(ctx, &lambdaweb.DeleteResourcePolicyInput{ResourceArn: aws.String(arn)}) //nolint:errcheck
	}
	for _, ep := range []string{lambdaWebCanary, lambdaWebLiveEndpoint} {
		g.cl().DeleteWebFunctionEndpoint(ctx, &lambdaweb.DeleteWebFunctionEndpointInput{ //nolint:errcheck
			FunctionName: aws.String(name), EndpointName: aws.String(ep),
		})
	}
	for _, key := range []string{"lwRevision1", "lwRevision2"} {
		if id := t.GetString(key); id != "" {
			g.cl().DeleteWebFunctionRevision(ctx, &lambdaweb.DeleteWebFunctionRevisionInput{ //nolint:errcheck
				FunctionName: aws.String(name), RevisionId: aws.String(id),
			})
		}
	}
	g.cl().DeleteWebFunction(ctx, &lambdaweb.DeleteWebFunctionInput{FunctionName: aws.String(name)}) //nolint:errcheck
	(&s3Group{c: g.c}).emptyAndDeleteBucket(ctx, g.codeBucket(t))
	return nil
}

func (g *lambdaWebGroup) CreateWebFunction(ctx context.Context, t *harness.TestContext) error {
	name := g.fnName(t)
	out, err := g.cl().CreateWebFunction(ctx, &lambdaweb.CreateWebFunctionInput{
		FunctionName:   aws.String(name),
		RevisionConfig: g.revisionConfig(t, "app-v1.zip"),
		EndpointConfig: &types.EndpointConfig{
			EndpointName:       aws.String(lambdaWebLiveEndpoint),
			EndpointType:       types.EndpointTypeHomeRegion,
			AuthType:           types.AuthTypeApplicationManaged,
			AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
		},
	})
	if err != nil {
		return err
	}
	arn := aws.ToString(out.FunctionArn)
	if !strings.HasPrefix(arn, "arn:aws:lambda:") || !strings.HasSuffix(arn, ":web-function/"+name) {
		return fmt.Errorf("CreateWebFunction: unexpected FunctionArn %q", arn)
	}
	if aws.ToString(out.FunctionName) != name {
		return fmt.Errorf("CreateWebFunction: FunctionName = %q, want %q", aws.ToString(out.FunctionName), name)
	}
	if out.Revision == nil || aws.ToString(out.Revision.RevisionId) == "" {
		return fmt.Errorf("CreateWebFunction: no first revision in the response")
	}
	if out.Endpoint == nil || aws.ToString(out.Endpoint.EndpointName) != lambdaWebLiveEndpoint ||
		out.Endpoint.AutoDeploymentMode != types.AutoDeploymentModeLatestRevision ||
		aws.ToString(out.Endpoint.DomainName) == "" {
		return fmt.Errorf("CreateWebFunction: endpoint = %+v", out.Endpoint)
	}
	t.Set("lwFunctionARN", arn)
	t.Set("lwRevision1", aws.ToString(out.Revision.RevisionId))
	return nil
}

func (g *lambdaWebGroup) GetWebFunction(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	out, err := g.cl().GetWebFunction(ctx, &lambdaweb.GetWebFunctionInput{FunctionName: aws.String(g.fnName(t))})
	if err != nil {
		return err
	}
	if aws.ToString(out.FunctionArn) != arn || aws.ToString(out.FunctionName) != g.fnName(t) {
		return fmt.Errorf("GetWebFunction: got %q / %q", aws.ToString(out.FunctionArn), aws.ToString(out.FunctionName))
	}
	if out.State != types.FunctionStateActive {
		return fmt.Errorf("GetWebFunction: State = %q, want Active", out.State)
	}
	return nil
}

func (g *lambdaWebGroup) ListWebFunctions(ctx context.Context, t *harness.TestContext) error {
	name := g.fnName(t)
	var token *string
	for {
		out, err := g.cl().ListWebFunctions(ctx, &lambdaweb.ListWebFunctionsInput{NextToken: token})
		if err != nil {
			return err
		}
		for _, f := range out.Functions {
			if aws.ToString(f.FunctionName) == name {
				if aws.ToString(f.FunctionArn) != t.GetString("lwFunctionARN") {
					return fmt.Errorf("ListWebFunctions: %q listed with ARN %q", name, aws.ToString(f.FunctionArn))
				}
				return nil
			}
		}
		if out.NextToken == nil {
			return fmt.Errorf("ListWebFunctions: %q not listed", name)
		}
		token = out.NextToken
	}
}

func (g *lambdaWebGroup) GetWebFunctionRevision(ctx context.Context, t *harness.TestContext) error {
	first := t.GetString("lwRevision1")
	if first == "" {
		return fmt.Errorf("no revision from CreateWebFunction")
	}
	// A revision is Pending while its package is fetched and checked.
	if err := g.waitRevisionActive(ctx, g.fnName(t), first); err != nil {
		return err
	}
	out, err := g.cl().GetWebFunctionRevision(ctx, &lambdaweb.GetWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)), RevisionId: aws.String(first),
	})
	if err != nil {
		return err
	}
	if aws.ToString(out.RevisionId) != first || out.State != types.RevisionStateActive {
		return fmt.Errorf("GetWebFunctionRevision: got %q in state %q", aws.ToString(out.RevisionId), out.State)
	}
	if !strings.HasSuffix(aws.ToString(out.RevisionArn), "/revision/"+first) {
		return fmt.Errorf("GetWebFunctionRevision: RevisionArn = %q", aws.ToString(out.RevisionArn))
	}
	// Settings the call did not give come back as AWS's documented defaults.
	if out.ServiceConfig == nil || aws.ToInt32(out.ServiceConfig.TimeoutSeconds) != 30 ||
		aws.ToInt32(out.ServiceConfig.MaxConcurrencyPerEnvironment) != 64 {
		return fmt.Errorf("GetWebFunctionRevision: ServiceConfig = %+v, want timeout 30 and concurrency 64", out.ServiceConfig)
	}
	if aws.ToString(out.ServiceConfig.ExecutionRoleArn) != lambdaWebRoleARN {
		return fmt.Errorf("GetWebFunctionRevision: ExecutionRoleArn = %q", aws.ToString(out.ServiceConfig.ExecutionRoleArn))
	}
	if out.BuildConfig == nil || out.BuildConfig.CodeConfig == nil || out.BuildConfig.CodeConfig.S3Object == nil ||
		aws.ToString(out.BuildConfig.CodeConfig.S3Object.Key) != "app-v1.zip" {
		return fmt.Errorf("GetWebFunctionRevision: BuildConfig = %+v", out.BuildConfig)
	}
	return nil
}

func (g *lambdaWebGroup) CreateWebFunctionRevision(ctx context.Context, t *harness.TestContext) error {
	cfg := g.revisionConfig(t, "app-v2.zip")
	out, err := g.cl().CreateWebFunctionRevision(ctx, &lambdaweb.CreateWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)),
		Description:  aws.String("second revision"),
		BuildConfig:  cfg.BuildConfig,
		ServiceConfig: &types.ServiceConfig{
			ExecutionRoleArn:     aws.String(lambdaWebRoleARN),
			EnvironmentVariables: map[string]string{"MODE": "prod"},
		},
	})
	if err != nil {
		return err
	}
	id := aws.ToString(out.RevisionId)
	if id == "" || id == t.GetString("lwRevision1") {
		return fmt.Errorf("CreateWebFunctionRevision: RevisionId = %q is empty or the first revision's", id)
	}
	t.Set("lwRevision2", id)
	if err := g.waitRevisionActive(ctx, g.fnName(t), id); err != nil {
		return err
	}

	got, err := g.cl().GetWebFunctionRevision(ctx, &lambdaweb.GetWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)), RevisionId: aws.String(id),
	})
	if err != nil {
		return fmt.Errorf("GetWebFunctionRevision after create: %w", err)
	}
	if aws.ToString(got.Description) != "second revision" || got.ServiceConfig == nil ||
		got.ServiceConfig.EnvironmentVariables["MODE"] != "prod" ||
		aws.ToString(got.BuildConfig.CodeConfig.S3Object.Key) != "app-v2.zip" {
		return fmt.Errorf("GetWebFunctionRevision after create: %+v", got)
	}

	// The platform owns AWS_REGION and friends: a revision cannot set them.
	_, err = g.cl().CreateWebFunctionRevision(ctx, &lambdaweb.CreateWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)),
		BuildConfig:  cfg.BuildConfig,
		ServiceConfig: &types.ServiceConfig{
			ExecutionRoleArn:     aws.String(lambdaWebRoleARN),
			EnvironmentVariables: map[string]string{"AWS_REGION": "eu-west-1"},
		},
	})
	return lambdaWebExpectError("CreateWebFunctionRevision with a reserved environment variable", err, "ValidationException")
}

func (g *lambdaWebGroup) GetWebFunctionEndpointLatestRevision(ctx context.Context, t *harness.TestContext) error {
	_, second, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	// The LatestRevision endpoint follows the new revision once it is Active.
	var out *lambdaweb.GetWebFunctionEndpointOutput
	if err := lambdaWebEventually("the live endpoint to serve "+second, 30*time.Second, func() (bool, error) {
		var err error
		out, err = g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
			FunctionName: aws.String(g.fnName(t)), EndpointName: aws.String(lambdaWebLiveEndpoint),
		})
		if err != nil {
			return false, err
		}
		return out.State == types.EndpointStateActive && len(out.RevisionWeights) == 1 &&
			aws.ToString(out.RevisionWeights[0].RevisionId) == second, nil
	}); err != nil {
		return err
	}
	if out.AutoDeploymentMode != types.AutoDeploymentModeLatestRevision || aws.ToInt32(out.RevisionWeights[0].Weight) != 100 {
		return fmt.Errorf("GetWebFunctionEndpoint: mode %q RevisionWeights = %+v, want %s at 100", out.AutoDeploymentMode, out.RevisionWeights, second)
	}
	return nil
}

func (g *lambdaWebGroup) ListWebFunctionRevisions(ctx context.Context, t *harness.TestContext) error {
	first, second, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	var order []string
	var token *string
	for {
		out, err := g.cl().ListWebFunctionRevisions(ctx, &lambdaweb.ListWebFunctionRevisionsInput{
			FunctionName: aws.String(g.fnName(t)), NextToken: token,
		})
		if err != nil {
			return err
		}
		for _, r := range out.Revisions {
			order = append(order, aws.ToString(r.RevisionId))
		}
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}
	// Newest first, and the refused revision with a reserved variable left no trace.
	if len(order) != 2 || order[0] != second || order[1] != first {
		return fmt.Errorf("ListWebFunctionRevisions: got %v, want [%s %s] (newest first)", order, second, first)
	}
	return nil
}

func (g *lambdaWebGroup) CreateWebFunctionEndpoint(ctx context.Context, t *harness.TestContext) error {
	first, second, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	// Weights may name only Active revisions.
	for _, id := range []string{first, second} {
		if err := g.waitRevisionActive(ctx, g.fnName(t), id); err != nil {
			return err
		}
	}
	out, err := g.cl().CreateWebFunctionEndpoint(ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName:       aws.String(g.fnName(t)),
		EndpointName:       aws.String(lambdaWebCanary),
		EndpointType:       types.EndpointTypeHomeRegion,
		AuthType:           types.AuthTypeApplicationManaged,
		AutoDeploymentMode: types.AutoDeploymentModeDisabled,
		RevisionWeights: []types.RevisionWeight{
			{RevisionId: aws.String(first), Weight: aws.Int32(90)},
			{RevisionId: aws.String(second), Weight: aws.Int32(10)},
		},
	})
	if err != nil {
		return err
	}
	arn := aws.ToString(out.EndpointArn)
	if !strings.HasSuffix(arn, "/endpoint/"+lambdaWebCanary) || aws.ToString(out.DomainName) == "" {
		return fmt.Errorf("CreateWebFunctionEndpoint: EndpointArn %q DomainName %q", arn, aws.ToString(out.DomainName))
	}
	t.Set("lwCanaryARN", arn)
	return nil
}

func (g *lambdaWebGroup) getCanary(ctx context.Context, t *harness.TestContext) (*lambdaweb.GetWebFunctionEndpointOutput, error) {
	return g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
		FunctionName: aws.String(g.fnName(t)), EndpointName: aws.String(lambdaWebCanary),
	})
}

func (g *lambdaWebGroup) GetWebFunctionEndpoint(ctx context.Context, t *harness.TestContext) error {
	first, second, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	out, err := g.getCanary(ctx, t)
	if err != nil {
		return err
	}
	if aws.ToString(out.EndpointArn) != t.GetString("lwCanaryARN") || out.AutoDeploymentMode != types.AutoDeploymentModeDisabled ||
		out.EndpointType != types.EndpointTypeHomeRegion {
		return fmt.Errorf("GetWebFunctionEndpoint: got %q mode %q type %q", aws.ToString(out.EndpointArn), out.AutoDeploymentMode, out.EndpointType)
	}
	weights := map[string]int32{}
	for _, w := range out.RevisionWeights {
		weights[aws.ToString(w.RevisionId)] = aws.ToInt32(w.Weight)
	}
	if len(weights) != 2 || weights[first] != 90 || weights[second] != 10 {
		return fmt.Errorf("GetWebFunctionEndpoint: RevisionWeights = %v, want %s:90 %s:10", weights, first, second)
	}
	return nil
}

func (g *lambdaWebGroup) ListWebFunctionEndpoints(ctx context.Context, t *harness.TestContext) error {
	seen := map[string]bool{}
	var token *string
	for {
		out, err := g.cl().ListWebFunctionEndpoints(ctx, &lambdaweb.ListWebFunctionEndpointsInput{
			FunctionName: aws.String(g.fnName(t)), NextToken: token,
		})
		if err != nil {
			return err
		}
		for _, e := range out.Endpoints {
			seen[aws.ToString(e.EndpointName)] = true
		}
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}
	if !seen[lambdaWebLiveEndpoint] || !seen[lambdaWebCanary] || len(seen) != 2 {
		return fmt.Errorf("ListWebFunctionEndpoints: got %v, want live and canary", seen)
	}
	return nil
}

func (g *lambdaWebGroup) UpdateWebFunctionEndpoint(ctx context.Context, t *harness.TestContext) error {
	out, err := g.cl().UpdateWebFunctionEndpoint(ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName:   aws.String(g.fnName(t)),
		EndpointName:   aws.String(lambdaWebCanary),
		AuthType:       types.AuthTypeIamAuth,
		ThrottleConfig: &types.ThrottleConfig{RateLimit: aws.Int32(8000)},
	})
	if err != nil {
		return err
	}
	if out.AuthType != types.AuthTypeIamAuth || out.ThrottleConfig == nil || aws.ToInt32(out.ThrottleConfig.RateLimit) != 8000 {
		return fmt.Errorf("UpdateWebFunctionEndpoint: response AuthType %q ThrottleConfig %+v", out.AuthType, out.ThrottleConfig)
	}
	got, err := g.getCanary(ctx, t)
	if err != nil {
		return fmt.Errorf("GetWebFunctionEndpoint after update: %w", err)
	}
	if got.AuthType != types.AuthTypeIamAuth || got.ThrottleConfig == nil || aws.ToInt32(got.ThrottleConfig.RateLimit) != 8000 {
		return fmt.Errorf("GetWebFunctionEndpoint after update: AuthType %q ThrottleConfig %+v", got.AuthType, got.ThrottleConfig)
	}
	// The members the update left out are kept: the traffic split is unchanged.
	if len(got.RevisionWeights) != 2 {
		return fmt.Errorf("GetWebFunctionEndpoint after update: RevisionWeights = %+v", got.RevisionWeights)
	}
	return nil
}

func (g *lambdaWebGroup) UpdateWebFunctionEndpointClearThrottle(ctx context.Context, t *harness.TestContext) error {
	// An empty throttleConfig clears the rate limit; it is not "leave as is".
	if _, err := g.cl().UpdateWebFunctionEndpoint(ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName:   aws.String(g.fnName(t)),
		EndpointName:   aws.String(lambdaWebCanary),
		ThrottleConfig: &types.ThrottleConfig{},
	}); err != nil {
		return err
	}
	got, err := g.getCanary(ctx, t)
	if err != nil {
		return fmt.Errorf("GetWebFunctionEndpoint after clearing: %w", err)
	}
	if got.ThrottleConfig != nil && got.ThrottleConfig.RateLimit != nil {
		return fmt.Errorf("GetWebFunctionEndpoint after clearing: ThrottleConfig = %+v, want none", got.ThrottleConfig)
	}
	if got.AuthType != types.AuthTypeIamAuth {
		return fmt.Errorf("GetWebFunctionEndpoint after clearing: AuthType = %q, the earlier change was lost", got.AuthType)
	}
	return nil
}

func (g *lambdaWebGroup) DeleteWebFunctionRevisionServingTraffic(ctx context.Context, t *harness.TestContext) error {
	first, _, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	// The canary endpoint sends 90% of its traffic to the first revision.
	_, err = g.cl().DeleteWebFunctionRevision(ctx, &lambdaweb.DeleteWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)), RevisionId: aws.String(first),
	})
	if err := lambdaWebExpectError("DeleteWebFunctionRevision while serving traffic", err, "ConflictException"); err != nil {
		return err
	}
	if _, status := lambdaWebErrorCode(err); status != 0 && status != http.StatusConflict {
		return fmt.Errorf("DeleteWebFunctionRevision while serving traffic: HTTP %d, want 409", status)
	}
	// The refused delete left the revision in place.
	if _, err := g.cl().GetWebFunctionRevision(ctx, &lambdaweb.GetWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)), RevisionId: aws.String(first),
	}); err != nil {
		return fmt.Errorf("revision gone after a refused delete: %w", err)
	}
	return nil
}

func (g *lambdaWebGroup) listTags(ctx context.Context, arn string) (map[string]string, error) {
	out, err := g.cl().ListTags(ctx, &lambdaweb.ListTagsInput{Resource: aws.String(arn)})
	if err != nil {
		return nil, err
	}
	return out.Tags, nil
}

func (g *lambdaWebGroup) TagResource(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	if _, err := g.cl().TagResource(ctx, &lambdaweb.TagResourceInput{
		Resource: aws.String(arn), Tags: map[string]string{"team": "web", "stage": "compat"},
	}); err != nil {
		return err
	}
	tags, err := g.listTags(ctx, arn)
	if err != nil {
		return fmt.Errorf("ListTags after TagResource: %w", err)
	}
	if tags["team"] != "web" || tags["stage"] != "compat" {
		return fmt.Errorf("ListTags after TagResource: %v", tags)
	}
	return nil
}

func (g *lambdaWebGroup) ListTags(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	tags, err := g.listTags(ctx, arn)
	if err != nil {
		return err
	}
	if len(tags) != 2 || tags["team"] != "web" || tags["stage"] != "compat" {
		return fmt.Errorf("ListTags: got %v, want team=web and stage=compat", tags)
	}
	return nil
}

func (g *lambdaWebGroup) UntagResource(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	if _, err := g.cl().UntagResource(ctx, &lambdaweb.UntagResourceInput{
		Resource: aws.String(arn), TagKeys: []string{"stage"},
	}); err != nil {
		return err
	}
	tags, err := g.listTags(ctx, arn)
	if err != nil {
		return fmt.Errorf("ListTags after UntagResource: %w", err)
	}
	if _, ok := tags["stage"]; ok || tags["team"] != "web" {
		return fmt.Errorf("ListTags after UntagResource: %v, want only team=web", tags)
	}
	return nil
}

func (g *lambdaWebGroup) PutResourcePolicy(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	out, err := g.cl().PutResourcePolicy(ctx, &lambdaweb.PutResourcePolicyInput{
		ResourceArn: aws.String(arn),
		Policy:      aws.String(lambdaWebPolicy(arn, `{"AWS":"444455556666"}`)),
	})
	if err != nil {
		return err
	}
	if aws.ToString(out.RevisionId) == "" {
		return fmt.Errorf("PutResourcePolicy: no RevisionId")
	}
	t.Set("lwPolicyRevision", aws.ToString(out.RevisionId))
	return nil
}

func (g *lambdaWebGroup) GetResourcePolicy(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	out, err := g.cl().GetResourcePolicy(ctx, &lambdaweb.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	if err != nil {
		return err
	}
	if !strings.Contains(aws.ToString(out.Policy), "444455556666") {
		return fmt.Errorf("GetResourcePolicy: policy does not name the account: %q", aws.ToString(out.Policy))
	}
	if aws.ToString(out.RevisionId) != t.GetString("lwPolicyRevision") {
		return fmt.Errorf("GetResourcePolicy: RevisionId %q, PutResourcePolicy returned %q",
			aws.ToString(out.RevisionId), t.GetString("lwPolicyRevision"))
	}
	return nil
}

func (g *lambdaWebGroup) PutResourcePolicyPublicPrincipal(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	// A wildcard principal with no condition would make the endpoint public.
	_, err = g.cl().PutResourcePolicy(ctx, &lambdaweb.PutResourcePolicyInput{
		ResourceArn: aws.String(arn),
		Policy:      aws.String(lambdaWebPolicy(arn, `"*"`)),
	})
	if err := lambdaWebExpectError("PutResourcePolicy with a wildcard principal", err, "ValidationException"); err != nil {
		return err
	}
	// The refused policy did not replace the stored one.
	out, err := g.cl().GetResourcePolicy(ctx, &lambdaweb.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	if err != nil {
		return fmt.Errorf("GetResourcePolicy after a refused put: %w", err)
	}
	if !strings.Contains(aws.ToString(out.Policy), "444455556666") {
		return fmt.Errorf("GetResourcePolicy after a refused put: policy changed to %q", aws.ToString(out.Policy))
	}
	return nil
}

func (g *lambdaWebGroup) DeleteResourcePolicy(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.functionARN(t)
	if err != nil {
		return err
	}
	if _, err := g.cl().DeleteResourcePolicy(ctx, &lambdaweb.DeleteResourcePolicyInput{ResourceArn: aws.String(arn)}); err != nil {
		return err
	}
	_, err = g.cl().GetResourcePolicy(ctx, &lambdaweb.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	return lambdaWebExpectError("GetResourcePolicy after DeleteResourcePolicy", err, "ResourceNotFoundException")
}

func (g *lambdaWebGroup) GetWebAccountSettings(ctx context.Context, t *harness.TestContext) error {
	out, err := g.cl().GetWebAccountSettings(ctx, &lambdaweb.GetWebAccountSettingsInput{})
	if err != nil {
		return err
	}
	q := out.AccountQuotas
	if q == nil || aws.ToInt32(q.MaxTotalArmVCpus) != 2000 || aws.ToInt32(q.MaxTotalRateLimit) != 100000 ||
		aws.ToInt32(q.MaxRevisionsPerFunction) != 50 || aws.ToInt32(q.MaxEndpointsPerFunction) != 10 {
		return fmt.Errorf("GetWebAccountSettings: AccountQuotas = %+v", q)
	}
	// Other suites' groups may be running their own functions, so at least ours.
	if out.AccountUsage == nil || aws.ToInt32(out.AccountUsage.FunctionCount) < 1 {
		return fmt.Errorf("GetWebAccountSettings: AccountUsage = %+v, want FunctionCount >= 1", out.AccountUsage)
	}
	return nil
}

func (g *lambdaWebGroup) DeleteWebFunctionEndpoint(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.cl().DeleteWebFunctionEndpoint(ctx, &lambdaweb.DeleteWebFunctionEndpointInput{
		FunctionName: aws.String(g.fnName(t)), EndpointName: aws.String(lambdaWebCanary),
	}); err != nil {
		return err
	}
	_, err := g.getCanary(ctx, t)
	return lambdaWebExpectError("GetWebFunctionEndpoint after delete", err, "ResourceNotFoundException")
}

func (g *lambdaWebGroup) DeleteWebFunctionRevision(ctx context.Context, t *harness.TestContext) error {
	first, _, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	// With the canary endpoint gone, only the live endpoint routes anywhere and
	// it serves the second revision, so the first can go.
	if _, err := g.cl().DeleteWebFunctionRevision(ctx, &lambdaweb.DeleteWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)), RevisionId: aws.String(first),
	}); err != nil {
		return err
	}
	_, err = g.cl().GetWebFunctionRevision(ctx, &lambdaweb.GetWebFunctionRevisionInput{
		FunctionName: aws.String(g.fnName(t)), RevisionId: aws.String(first),
	})
	return lambdaWebExpectError("GetWebFunctionRevision after delete", err, "ResourceNotFoundException")
}

func (g *lambdaWebGroup) DeleteWebFunction(ctx context.Context, t *harness.TestContext) error {
	name := g.fnName(t)
	if _, err := g.cl().DeleteWebFunction(ctx, &lambdaweb.DeleteWebFunctionInput{FunctionName: aws.String(name)}); err != nil {
		return err
	}
	_, err := g.cl().GetWebFunction(ctx, &lambdaweb.GetWebFunctionInput{FunctionName: aws.String(name)})
	if err := lambdaWebExpectError("GetWebFunction after delete", err, "ResourceNotFoundException"); err != nil {
		return err
	}
	// The function's endpoints and revisions went with it.
	_, err = g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
		FunctionName: aws.String(name), EndpointName: aws.String(lambdaWebLiveEndpoint),
	})
	return lambdaWebExpectError("GetWebFunctionEndpoint after the function is deleted", err, "ResourceNotFoundException")
}

// ─── lambda-web-endpoints ────────────────────────────────────────────────────
//
// The data plane, black box: a Node.js server is uploaded to S3, deployed with
// the Lambda Web Functions SDK calls, and then reached with plain HTTP — to the
// Overcast base URL with the endpoint's domainName as the Host header, which is
// how a client reaches an endpoint whose DNS name resolves to Overcast. Every
// test needs Docker (the registry says so) because the package runs in a
// container.

// lweAppSource is a dependency-free Node.js server. "/" answers with what the
// request looked like to the application; "/sleep" outlives any timeout the
// tests set; "/stream" writes two chunks 500 ms apart.
const lweAppSource = `
const http = require('http');
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
http.createServer((req, res) => {
  if (req.url.indexOf('/sleep') === 0) {
    setTimeout(() => res.end('late'), 20000);
    return;
  }
  if (req.url.indexOf('/stream') === 0) {
    res.setHeader('content-type', 'text/plain');
    res.write('one\n');
    setTimeout(() => res.end('two\n'), 500);
    return;
  }
  res.setHeader('x-app', 'echo');
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify({
    version: process.env.APP_VERSION || 'v1',
    method: req.method, url: req.url, host: req.headers.host,
    headers: req.headers,
  }));
}).listen(port, host);
`

const (
	// lweBrokenSource fails at import time, before it can listen.
	lweBrokenSource = "require('./missing');\n"
	// lweSilentSource runs forever and never listens on AWS_LAMBDA_HTTP_ENDPOINT.
	lweSilentSource = "setInterval(function () {}, 1000);\n"

	lweEndpointName = "live"
	lweAppKey       = "app.zip"
	lweBrokenKey    = "broken.zip"
	lweSilentKey    = "silent.zip"
)

// lweClient talks plain HTTP to the data plane. It does not follow redirects,
// reuse connections or negotiate anything the application would not see.
var lweClient = &http.Client{
	Timeout:       90 * time.Second,
	Transport:     &http.Transport{DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (g *lambdaWebGroup) endpointsBucket(t *harness.TestContext) string {
	return fmt.Sprintf("%s-lambda-web-endpoints", t.RunID)
}

func (g *lambdaWebGroup) putZip(ctx context.Context, bucket, key string, files map[string]string) error {
	body, err := lambdaWebZip(files)
	if err != nil {
		return err
	}
	_, err = g.c.S3().PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	if err != nil {
		return fmt.Errorf("PutObject %s/%s: %w", bucket, key, err)
	}
	return nil
}

func (g *lambdaWebGroup) setupEndpoints(ctx context.Context, t *harness.TestContext) error {
	bucket := g.endpointsBucket(t)
	if err := g.seedBucket(ctx, bucket, []string{lweAppKey}, map[string]string{"index.js": lweAppSource}); err != nil {
		return err
	}
	if err := g.putZip(ctx, bucket, lweBrokenKey, map[string]string{"index.js": lweBrokenSource}); err != nil {
		return err
	}
	return g.putZip(ctx, bucket, lweSilentKey, map[string]string{"index.js": lweSilentSource})
}

// track remembers a function name so teardown can remove it even when its test
// failed before cleaning up.
func (g *lambdaWebGroup) track(t *harness.TestContext, fn string) {
	var names []string
	if v, ok := t.Get("lweFunctions"); ok {
		names, _ = v.([]string)
	}
	t.Set("lweFunctions", append(names, fn))
}

// deleteFunction removes a web function the explicit way: its endpoints, then
// its revisions, then the function. Failures are ignored; it is cleanup.
func (g *lambdaWebGroup) deleteFunction(ctx context.Context, fn string) {
	cl := g.cl()
	if eps, err := cl.ListWebFunctionEndpoints(ctx, &lambdaweb.ListWebFunctionEndpointsInput{FunctionName: aws.String(fn)}); err == nil {
		for _, e := range eps.Endpoints {
			cl.DeleteWebFunctionEndpoint(ctx, &lambdaweb.DeleteWebFunctionEndpointInput{ //nolint:errcheck
				FunctionName: aws.String(fn), EndpointName: e.EndpointName,
			})
		}
	}
	if revs, err := cl.ListWebFunctionRevisions(ctx, &lambdaweb.ListWebFunctionRevisionsInput{FunctionName: aws.String(fn)}); err == nil {
		for _, r := range revs.Revisions {
			cl.DeleteWebFunctionRevision(ctx, &lambdaweb.DeleteWebFunctionRevisionInput{ //nolint:errcheck
				FunctionName: aws.String(fn), RevisionId: r.RevisionId,
			})
		}
	}
	cl.DeleteWebFunction(ctx, &lambdaweb.DeleteWebFunctionInput{FunctionName: aws.String(fn)}) //nolint:errcheck
}

func (g *lambdaWebGroup) teardownEndpoints(ctx context.Context, t *harness.TestContext) error {
	if v, ok := t.Get("lweFunctions"); ok {
		names, _ := v.([]string)
		for _, fn := range names {
			g.deleteFunction(ctx, fn)
		}
	}
	(&s3Group{c: g.c}).emptyAndDeleteBucket(ctx, g.endpointsBucket(t))
	return nil
}

// lweApp is one deployed web function with its "live" endpoint.
type lweApp struct {
	g      *lambdaWebGroup
	t      *harness.TestContext
	fn     string
	domain string // the endpoint's domainName: the Host a client sends
	rev    string // the first revision
}

// lweDeploy describes the function a test deploys.
type lweDeploy struct {
	suffix  string
	key     string // S3 key of the package; lweAppKey when empty
	auth    types.AuthType
	env     map[string]string
	timeout *int32
}

func (g *lambdaWebGroup) deploy(ctx context.Context, t *harness.TestContext, d lweDeploy) (*lweApp, error) {
	if d.key == "" {
		d.key = lweAppKey
	}
	if d.auth == "" {
		d.auth = types.AuthTypeApplicationManaged
	}
	fn := fmt.Sprintf("%s-lambda-web-endpoints-%s", t.RunID, d.suffix)
	g.track(t, fn)
	out, err := g.cl().CreateWebFunction(ctx, &lambdaweb.CreateWebFunctionInput{
		FunctionName:   aws.String(fn),
		RevisionConfig: g.lweRevisionConfig(t, d.key, d.env, d.timeout),
		EndpointConfig: &types.EndpointConfig{
			EndpointName:       aws.String(lweEndpointName),
			EndpointType:       types.EndpointTypeHomeRegion,
			AuthType:           d.auth,
			AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("CreateWebFunction %s: %w", fn, err)
	}
	if out.Revision == nil || aws.ToString(out.Revision.RevisionId) == "" || out.Endpoint == nil {
		return nil, fmt.Errorf("CreateWebFunction %s: no revision or endpoint in %+v", fn, out)
	}
	app := &lweApp{g: g, t: t, fn: fn, rev: aws.ToString(out.Revision.RevisionId)}
	if err := g.waitRevisionActive(ctx, fn, app.rev); err != nil {
		return app, err
	}
	ep, err := g.waitEndpointServing(ctx, fn, lweEndpointName, app.rev)
	if err != nil {
		return app, err
	}
	app.domain = aws.ToString(ep.DomainName)
	if app.domain == "" {
		return app, fmt.Errorf("GetWebFunctionEndpoint %s: empty DomainName", fn)
	}
	return app, nil
}

func (g *lambdaWebGroup) lweRevisionConfig(t *harness.TestContext, key string, env map[string]string, timeout *int32) *types.RevisionConfig {
	return &types.RevisionConfig{
		BuildConfig: &types.BuildConfig{
			CodeConfig:    &types.CodeConfig{S3Object: &types.S3Object{Bucket: aws.String(g.endpointsBucket(t)), Key: aws.String(key)}},
			RuntimeConfig: &types.RuntimeConfig{Runtime: aws.String("nodejs24.x")},
		},
		ServiceConfig: &types.ServiceConfig{
			ExecutionRoleArn: aws.String(lambdaWebRoleARN), EnvironmentVariables: env, TimeoutSeconds: timeout,
		},
	}
}

// waitEndpointServing polls until the endpoint is Active and sends all its
// traffic to one revision.
func (g *lambdaWebGroup) waitEndpointServing(ctx context.Context, fn, endpoint, rev string) (*lambdaweb.GetWebFunctionEndpointOutput, error) {
	var out *lambdaweb.GetWebFunctionEndpointOutput
	err := lambdaWebEventually("endpoint "+endpoint+" to serve "+rev, 30*time.Second, func() (bool, error) {
		var err error
		out, err = g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
			FunctionName: aws.String(fn), EndpointName: aws.String(endpoint),
		})
		if err != nil {
			return false, err
		}
		return out.State == types.EndpointStateActive && out.UpdateStatus != types.EndpointUpdateStatusInProgress &&
			len(out.RevisionWeights) == 1 && aws.ToString(out.RevisionWeights[0].RevisionId) == rev, nil
	})
	return out, err
}

// waitUpdated polls until the endpoint's last update is applied and ok accepts it.
func (g *lambdaWebGroup) waitUpdated(ctx context.Context, fn, endpoint string, ok func(*lambdaweb.GetWebFunctionEndpointOutput) bool) error {
	return lambdaWebEventually("endpoint "+endpoint+" update to apply", 30*time.Second, func() (bool, error) {
		out, err := g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
			FunctionName: aws.String(fn), EndpointName: aws.String(endpoint),
		})
		if err != nil {
			return false, err
		}
		if out.UpdateStatus == types.EndpointUpdateStatusFailed {
			return false, fmt.Errorf("update Failed: %s", aws.ToString(out.UpdateStatusReason))
		}
		return out.UpdateStatus != types.EndpointUpdateStatusInProgress && ok(out), nil
	})
}

func (a *lweApp) close(ctx context.Context) { a.g.deleteFunction(ctx, a.fn) }

// lweResponse is a data-plane response read to the end.
type lweResponse struct {
	status int
	header http.Header
	body   []byte
}

func (r *lweResponse) errorType() string { return r.header.Get("Lambda-Web-Error-Type") }

// lweRaw sends one request to the Overcast base URL with host as the Host
// header. The caller owns the response body.
func lweRaw(ctx context.Context, base, host, method, path string, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return lweClient.Do(req)
}

// lweSend is lweRaw read to the end, with one allowance: a first request can
// arrive while the emulator is still finishing its Docker start-up work, and is
// answered 502 with no lambda-web-error-type. Those are retried for a while;
// every platform error AWS documents carries a type.
func lweSend(ctx context.Context, t *harness.TestContext, host, method, path string, hdr map[string]string) (*lweResponse, error) {
	deadline := time.Now().Add(60 * time.Second)
	for attempt := 0; ; attempt++ {
		resp, err := lweRaw(ctx, t.Endpoint, host, method, path, hdr)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("%s %s: reading the body: %w", method, path, err)
		}
		r := &lweResponse{status: resp.StatusCode, header: resp.Header, body: body}
		if r.status == http.StatusBadGateway && r.errorType() == "" && time.Now().Before(deadline) {
			t.Log(fmt.Sprintf("%s %s: untyped 502 (attempt %d), retrying: %s", method, path, attempt+1, strings.TrimSpace(string(body))))
			time.Sleep(300 * time.Millisecond)
			continue
		}
		return r, nil
	}
}

func (a *lweApp) send(ctx context.Context, method, path string, hdr map[string]string) (*lweResponse, error) {
	return lweSend(ctx, a.t, a.domain, method, path, hdr)
}

// lweEcho is what the application reports about the request it received.
type lweEcho struct {
	Version string         `json:"version"`
	Method  string         `json:"method"`
	URL     string         `json:"url"`
	Host    string         `json:"host"`
	Headers map[string]any `json:"headers"`
}

func (e *lweEcho) header(name string) string {
	v, _ := e.Headers[strings.ToLower(name)].(string)
	return v
}

func (e *lweEcho) has(name string) bool {
	_, ok := e.Headers[strings.ToLower(name)]
	return ok
}

// lweExpect200 parses the echo app's answer, failing with the response when it
// is anything but a 200.
func lweExpect200(what string, r *lweResponse) (*lweEcho, error) {
	if r.status != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d (lambda-web-error-type %q): %s", what, r.status, r.errorType(), strings.TrimSpace(string(r.body)))
	}
	var e lweEcho
	if err := json.Unmarshal(r.body, &e); err != nil {
		return nil, fmt.Errorf("%s: body is not the echo app's JSON: %v: %s", what, err, r.body)
	}
	return &e, nil
}

// lweExpectError checks a platform error response: its status and its
// lambda-web-error-type.
func lweExpectError(what string, r *lweResponse, status int, errType string) error {
	if r.status != status || r.errorType() != errType {
		return fmt.Errorf("%s: got HTTP %d with lambda-web-error-type %q, want %d %q: %s",
			what, r.status, r.errorType(), status, errType, strings.TrimSpace(string(r.body)))
	}
	return nil
}

func lweHostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.ToLower(h)
}

func (g *lambdaWebGroup) ServeEndpointRequest(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "serve"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	resp, err := app.send(ctx, "GET", "/hello/world?x=1&y=%2F", map[string]string{"X-Forwarded-For": "1.2.3.4"})
	if err != nil {
		return err
	}
	echo, err := lweExpect200("GET through the endpoint domain", resp)
	if err != nil {
		return err
	}
	if echo.Method != "GET" || echo.URL != "/hello/world?x=1&y=%2F" {
		return fmt.Errorf("the application saw %s %s, want GET /hello/world?x=1&y=%%2F", echo.Method, echo.URL)
	}
	if lweHostOnly(echo.Host) != lweHostOnly(app.domain) {
		return fmt.Errorf("the application saw Host %q, want the endpoint domain %q", echo.Host, app.domain)
	}
	if got := echo.header("lambda-web-endpoint-name"); got != lweEndpointName {
		return fmt.Errorf("lambda-web-endpoint-name = %q, want %q", got, lweEndpointName)
	}
	reqID := echo.header("lambda-web-request-id")
	if reqID == "" || reqID != resp.header.Get("Lambda-Web-Request-Id") {
		return fmt.Errorf("request id: the application saw %q, the response carries %q", reqID, resp.header.Get("Lambda-Web-Request-Id"))
	}
	if resp.header.Get("Lambda-Web-Logstream") == "" {
		return fmt.Errorf("the response has no lambda-web-logstream header: %v", resp.header)
	}
	if resp.header.Get("X-App") != "echo" {
		return fmt.Errorf("the application's own response header x-app was lost: %v", resp.header)
	}
	if echo.Version != "v1" {
		return fmt.Errorf("version = %q, want v1", echo.Version)
	}
	return nil
}

func (g *lambdaWebGroup) ForwardRequestHeaders(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "headers"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	resp, err := app.send(ctx, "GET", "/", map[string]string{
		"X-Real-IP":                "9.9.9.9",
		"X-HTTP-Method-Override":   "DELETE",
		"X-HTTP-Method":            "DELETE",
		"X-Method-Override":        "DELETE",
		"Proxy-Authorization":      "Basic Zm9vOmJhcg==",
		"Lambda-Web-Endpoint-Name": "spoofed",
		"Lambda-Web-Request-Id":    "spoofed",
		"Lambda-Web-Custom":        "1",
		"X-Forwarded-For":          "1.2.3.4",
		"X-Forwarded-Host":         "original.example.test",
	})
	if err != nil {
		return err
	}
	echo, err := lweExpect200("GET with headers the platform controls", resp)
	if err != nil {
		return err
	}
	for _, name := range []string{"X-Real-IP", "X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override", "Proxy-Authorization", "Lambda-Web-Custom"} {
		if echo.has(name) {
			return fmt.Errorf("the application saw %s = %v, which the platform removes", name, echo.Headers[strings.ToLower(name)])
		}
	}
	if echo.Method != "GET" {
		return fmt.Errorf("the application saw method %s: a method override header took effect", echo.Method)
	}
	if got := echo.header("lambda-web-endpoint-name"); got != lweEndpointName {
		return fmt.Errorf("lambda-web-endpoint-name = %q: a client-supplied value was not replaced (want %q)", got, lweEndpointName)
	}
	reqID := echo.header("lambda-web-request-id")
	if reqID == "" || reqID == "spoofed" || reqID != resp.header.Get("Lambda-Web-Request-Id") {
		return fmt.Errorf("lambda-web-request-id: the application saw %q, the response carries %q", reqID, resp.header.Get("Lambda-Web-Request-Id"))
	}
	xff := echo.header("x-forwarded-for")
	if !strings.HasPrefix(xff, "1.2.3.4, ") || len(xff) <= len("1.2.3.4, ") {
		return fmt.Errorf("x-forwarded-for = %q, want the client address appended to %q", xff, "1.2.3.4")
	}
	if got := echo.header("x-forwarded-host"); got != "original.example.test" {
		return fmt.Errorf("x-forwarded-host = %q, want it forwarded untouched", got)
	}
	return nil
}

func (g *lambdaWebGroup) AuthorizationHeader(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "authz"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	// An application-managed endpoint leaves authentication to the application:
	// a bearer token reaches it.
	resp, err := app.send(ctx, "GET", "/", map[string]string{"Authorization": "Bearer compat-token-123"})
	if err != nil {
		return err
	}
	echo, err := lweExpect200("GET with a bearer token", resp)
	if err != nil {
		return err
	}
	if got := echo.header("authorization"); got != "Bearer compat-token-123" {
		return fmt.Errorf("the application saw Authorization %q, want the bearer token", got)
	}
	// An AWS signature is Lambda's, not the application's, and is removed.
	resp, err = app.send(ctx, "GET", "/", map[string]string{
		"Authorization":        "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260101/us-east-1/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=0123456789abcdef",
		"X-Amz-Date":           "20260101T000000Z",
		"X-Amz-Security-Token": "compat-session-token",
	})
	if err != nil {
		return err
	}
	echo, err = lweExpect200("GET with a SigV4 Authorization", resp)
	if err != nil {
		return err
	}
	for _, name := range []string{"authorization", "x-amz-security-token"} {
		if echo.has(name) {
			return fmt.Errorf("the application saw %s = %v: a SigV4 credential must not reach it", name, echo.Headers[name])
		}
	}
	return nil
}

func (g *lambdaWebGroup) LatestRevisionServesNewRevision(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "latest", env: map[string]string{"APP_VERSION": "v1"}})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	resp, err := app.send(ctx, "GET", "/", nil)
	if err != nil {
		return err
	}
	if echo, err := lweExpect200("the first revision", resp); err != nil {
		return err
	} else if echo.Version != "v1" {
		return fmt.Errorf("the first revision answered version %q, want v1", echo.Version)
	}

	cfg := g.lweRevisionConfig(t, lweAppKey, map[string]string{"APP_VERSION": "v2"}, nil)
	rev, err := g.cl().CreateWebFunctionRevision(ctx, &lambdaweb.CreateWebFunctionRevisionInput{
		FunctionName: aws.String(app.fn), BuildConfig: cfg.BuildConfig, ServiceConfig: cfg.ServiceConfig,
	})
	if err != nil {
		return fmt.Errorf("CreateWebFunctionRevision: %w", err)
	}
	second := aws.ToString(rev.RevisionId)
	if second == "" || second == app.rev {
		return fmt.Errorf("CreateWebFunctionRevision: RevisionId = %q", second)
	}
	if err := g.waitRevisionActive(ctx, app.fn, second); err != nil {
		return err
	}
	if _, err := g.waitEndpointServing(ctx, app.fn, lweEndpointName, second); err != nil {
		return err
	}
	// Requests reach the new revision (a warm environment of the old one may
	// still answer a request that was already routed).
	return lambdaWebEventually("requests to be served by version v2", 30*time.Second, func() (bool, error) {
		resp, err := app.send(ctx, "GET", "/", nil)
		if err != nil {
			return false, err
		}
		echo, err := lweExpect200("the second revision", resp)
		if err != nil {
			return false, err
		}
		return echo.Version == "v2", nil
	})
}

func (g *lambdaWebGroup) WeightedRouting(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "weights", env: map[string]string{"APP_VERSION": "v1"}})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	cfg := g.lweRevisionConfig(t, lweAppKey, map[string]string{"APP_VERSION": "v2"}, nil)
	rev, err := g.cl().CreateWebFunctionRevision(ctx, &lambdaweb.CreateWebFunctionRevisionInput{
		FunctionName: aws.String(app.fn), BuildConfig: cfg.BuildConfig, ServiceConfig: cfg.ServiceConfig,
	})
	if err != nil {
		return fmt.Errorf("CreateWebFunctionRevision: %w", err)
	}
	second := aws.ToString(rev.RevisionId)
	if err := g.waitRevisionActive(ctx, app.fn, second); err != nil {
		return err
	}
	// Weights run from 1 to 100 per revision, so a canary is a 90/10 split.
	created, err := g.cl().CreateWebFunctionEndpoint(ctx, &lambdaweb.CreateWebFunctionEndpointInput{
		FunctionName:       aws.String(app.fn),
		EndpointName:       aws.String("canary"),
		EndpointType:       types.EndpointTypeHomeRegion,
		AuthType:           types.AuthTypeApplicationManaged,
		AutoDeploymentMode: types.AutoDeploymentModeDisabled,
		RevisionWeights: []types.RevisionWeight{
			{RevisionId: aws.String(app.rev), Weight: aws.Int32(90)},
			{RevisionId: aws.String(second), Weight: aws.Int32(10)},
		},
	})
	if err != nil {
		return fmt.Errorf("CreateWebFunctionEndpoint: %w", err)
	}
	if err := lambdaWebEventually("the canary endpoint to become Active", 30*time.Second, func() (bool, error) {
		out, err := g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
			FunctionName: aws.String(app.fn), EndpointName: aws.String("canary"),
		})
		if err != nil {
			return false, err
		}
		return out.State == types.EndpointStateActive, nil
	}); err != nil {
		return err
	}
	canary := aws.ToString(created.DomainName)
	if canary == "" || canary == app.domain {
		return fmt.Errorf("the canary endpoint's domain is %q (the live endpoint's is %q)", canary, app.domain)
	}

	counts := map[string]int{}
	const requests = 200
	for i := 0; i < requests; i++ {
		resp, err := lweSend(ctx, t, canary, "GET", "/", nil)
		if err != nil {
			return err
		}
		echo, err := lweExpect200(fmt.Sprintf("request %d to the canary endpoint", i+1), resp)
		if err != nil {
			return err
		}
		counts[echo.Version]++
	}
	// 90/10 of 200 requests expects 180/20; the bounds are loose enough that a
	// correct split fails them with probability well under one in a million.
	if counts["v1"] < 120 || counts["v2"] < 2 || counts["v1"]+counts["v2"] != requests {
		return fmt.Errorf("a 90/10 split over %d requests served %v, want both revisions with v1 the clear majority", requests, counts)
	}
	return nil
}

func (g *lambdaWebGroup) IamAuthRefusesUnsignedRequest(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "iam", auth: types.AuthTypeIamAuth})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	// Authorisation failures are a bare 403: AWS sends no lambda-web-error-type.
	resp, err := app.send(ctx, "GET", "/", nil)
	if err != nil {
		return err
	}
	if resp.status != http.StatusForbidden || resp.errorType() != "" {
		return fmt.Errorf("an unsigned request to an IamAuth endpoint: HTTP %d with lambda-web-error-type %q, want a bare 403: %s",
			resp.status, resp.errorType(), strings.TrimSpace(string(resp.body)))
	}

	// The same call to the endpoint once it is ApplicationManaged.
	out, err := g.cl().UpdateWebFunctionEndpoint(ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String(app.fn), EndpointName: aws.String(lweEndpointName), AuthType: types.AuthTypeApplicationManaged,
	})
	if err != nil {
		return fmt.Errorf("UpdateWebFunctionEndpoint: %w", err)
	}
	if out.AuthType != types.AuthTypeApplicationManaged {
		return fmt.Errorf("UpdateWebFunctionEndpoint: AuthType = %q", out.AuthType)
	}
	if err := g.waitUpdated(ctx, app.fn, lweEndpointName, func(e *lambdaweb.GetWebFunctionEndpointOutput) bool {
		return e.AuthType == types.AuthTypeApplicationManaged
	}); err != nil {
		return err
	}
	return lambdaWebEventually("the unsigned request to be served", 20*time.Second, func() (bool, error) {
		resp, err := app.send(ctx, "GET", "/", nil)
		if err != nil {
			return false, err
		}
		if resp.status == http.StatusForbidden {
			return false, nil
		}
		_, err = lweExpect200("an unsigned request to an ApplicationManaged endpoint", resp)
		return err == nil, err
	})
}

func (g *lambdaWebGroup) ImportCodeError(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "import", key: lweBrokenKey})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	resp, err := app.send(ctx, "GET", "/", nil)
	if err != nil {
		return err
	}
	return lweExpectError("a package whose entry file requires a missing module", resp, http.StatusBadGateway, "Runtime.ImportCodeError")
}

// ListenTimeout is the slow one: the platform waits 10 seconds for the server
// to start listening before it gives up, so this test takes about that long.
func (g *lambdaWebGroup) ListenTimeout(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "listen", key: lweSilentKey})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	start := time.Now()
	resp, err := app.send(ctx, "GET", "/", nil)
	if err != nil {
		return err
	}
	if err := lweExpectError("an application that never listens", resp, http.StatusBadGateway, "Application.TimeoutListenError"); err != nil {
		return err
	}
	if waited := time.Since(start); waited < 5*time.Second {
		return fmt.Errorf("the platform gave up after %v; it should wait about 10 seconds for the application to listen", waited)
	}
	return nil
}

func (g *lambdaWebGroup) UnknownDomain(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "unknown"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	// Keep everything but the endpoint's own id, so the host is shaped like an
	// endpoint domain and names nothing.
	labels := strings.SplitN(app.domain, ".", 2)
	if len(labels) != 2 {
		return fmt.Errorf("endpoint domain %q has no id label", app.domain)
	}
	resp, err := lweSend(ctx, t, "nosuchendpoint0."+labels[1], "GET", "/", nil)
	if err != nil {
		return err
	}
	return lweExpectError("a request to an endpoint domain nobody owns", resp, http.StatusNotFound, "Platform.InvalidEndpoint")
}

func (g *lambdaWebGroup) UnsupportedMethod(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "method"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	resp, err := app.send(ctx, "TRACE", "/", nil)
	if err != nil {
		return err
	}
	if resp.status != http.StatusBadRequest {
		return fmt.Errorf("TRACE: HTTP %d (lambda-web-error-type %q), want 400: %s", resp.status, resp.errorType(), strings.TrimSpace(string(resp.body)))
	}
	// The endpoint still serves supported methods.
	resp, err = app.send(ctx, "POST", "/", nil)
	if err != nil {
		return err
	}
	echo, err := lweExpect200("POST after a refused TRACE", resp)
	if err != nil {
		return err
	}
	if echo.Method != "POST" {
		return fmt.Errorf("the application saw method %s, want POST", echo.Method)
	}
	return nil
}

func (g *lambdaWebGroup) setRateLimit(ctx context.Context, app *lweApp, limit int32) error {
	out, err := g.cl().UpdateWebFunctionEndpoint(ctx, &lambdaweb.UpdateWebFunctionEndpointInput{
		FunctionName: aws.String(app.fn), EndpointName: aws.String(lweEndpointName),
		ThrottleConfig: &types.ThrottleConfig{RateLimit: aws.Int32(limit)},
	})
	if err != nil {
		return fmt.Errorf("UpdateWebFunctionEndpoint rateLimit %d: %w", limit, err)
	}
	if out.ThrottleConfig == nil || out.ThrottleConfig.RateLimit == nil || aws.ToInt32(out.ThrottleConfig.RateLimit) != limit {
		return fmt.Errorf("UpdateWebFunctionEndpoint: ThrottleConfig = %+v, want rateLimit %d", out.ThrottleConfig, limit)
	}
	return g.waitUpdated(ctx, app.fn, lweEndpointName, func(e *lambdaweb.GetWebFunctionEndpointOutput) bool {
		return e.ThrottleConfig != nil && e.ThrottleConfig.RateLimit != nil && aws.ToInt32(e.ThrottleConfig.RateLimit) == limit
	})
}

func (g *lambdaWebGroup) EndpointDisabled(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "disabled"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	if resp, err := app.send(ctx, "GET", "/", nil); err != nil {
		return err
	} else if _, err := lweExpect200("before disabling", resp); err != nil {
		return err
	}
	// A rate limit of 0 disables the endpoint.
	if err := g.setRateLimit(ctx, app, 0); err != nil {
		return err
	}
	var last *lweResponse
	err = lambdaWebEventually("the endpoint to answer 503", 20*time.Second, func() (bool, error) {
		var err error
		last, err = app.send(ctx, "GET", "/", nil)
		return err == nil && last.status == http.StatusServiceUnavailable, err
	})
	if err != nil {
		if last != nil {
			return fmt.Errorf("%w (last: HTTP %d %q)", err, last.status, last.errorType())
		}
		return err
	}
	return lweExpectError("an endpoint with rateLimit 0", last, http.StatusServiceUnavailable, "Platform.EndpointDisabled")
}

func (g *lambdaWebGroup) EndpointRateLimit(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "throttle"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	// Warm an environment first, so the burst measures the rate limit and not a
	// cold start.
	if resp, err := app.send(ctx, "GET", "/", nil); err != nil {
		return err
	} else if _, err := lweExpect200("the warm-up request", resp); err != nil {
		return err
	}
	if err := g.setRateLimit(ctx, app, 100); err != nil {
		return err
	}

	const burst = 50
	results := make([]*lweResponse, burst)
	errs := make([]error, burst)
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = lweSend(ctx, t, app.domain, "GET", "/", nil)
		}()
	}
	wg.Wait()
	throttled, served := 0, 0
	for i, r := range results {
		if errs[i] != nil {
			return errs[i]
		}
		switch r.status {
		case http.StatusOK:
			served++
		case http.StatusTooManyRequests:
			if err := lweExpectError("a throttled request", r, http.StatusTooManyRequests, "Platform.EndpointRequestThrottle"); err != nil {
				return err
			}
			if r.header.Get("Retry-After") == "" {
				return fmt.Errorf("a throttled request carries no Retry-After header: %v", r.header)
			}
			throttled++
		default:
			return fmt.Errorf("a request in the burst: HTTP %d (lambda-web-error-type %q): %s", r.status, r.errorType(), strings.TrimSpace(string(r.body)))
		}
	}
	if throttled == 0 {
		return fmt.Errorf("%d concurrent requests to an endpoint limited to 100 per second were all served; some should be throttled", burst)
	}
	if served == 0 {
		return fmt.Errorf("all %d requests were throttled; a limit of 100 per second allows a burst through", burst)
	}
	return nil
}

func (g *lambdaWebGroup) RequestTimeout(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "timeout", timeout: aws.Int32(3)})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	rev, err := g.cl().GetWebFunctionRevision(ctx, &lambdaweb.GetWebFunctionRevisionInput{FunctionName: aws.String(app.fn), RevisionId: aws.String(app.rev)})
	if err != nil {
		return err
	}
	if rev.ServiceConfig == nil || aws.ToInt32(rev.ServiceConfig.TimeoutSeconds) != 3 {
		return fmt.Errorf("GetWebFunctionRevision: ServiceConfig = %+v, want timeoutSeconds 3", rev.ServiceConfig)
	}
	// Warm the environment so the wait below is the application's, not a cold start.
	if resp, err := app.send(ctx, "GET", "/", nil); err != nil {
		return err
	} else if _, err := lweExpect200("the warm-up request", resp); err != nil {
		return err
	}
	start := time.Now()
	resp, err := app.send(ctx, "GET", "/sleep", nil)
	if err != nil {
		return err
	}
	if err := lweExpectError("an application that outlives timeoutSeconds", resp, http.StatusGatewayTimeout, "Runtime.RequestTimeout"); err != nil {
		return err
	}
	if waited := time.Since(start); waited < 2*time.Second || waited > 15*time.Second {
		return fmt.Errorf("the request timed out after %v, want about 3 seconds", waited)
	}
	return nil
}

func (g *lambdaWebGroup) DeletedEndpointDomain(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "deleted"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	if resp, err := app.send(ctx, "GET", "/", nil); err != nil {
		return err
	} else if _, err := lweExpect200("before deleting the endpoint", resp); err != nil {
		return err
	}
	if _, err := g.cl().DeleteWebFunctionEndpoint(ctx, &lambdaweb.DeleteWebFunctionEndpointInput{
		FunctionName: aws.String(app.fn), EndpointName: aws.String(lweEndpointName),
	}); err != nil {
		return fmt.Errorf("DeleteWebFunctionEndpoint: %w", err)
	}
	if err := lambdaWebEventually("the deleted endpoint to be gone", 30*time.Second, func() (bool, error) {
		_, err := g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
			FunctionName: aws.String(app.fn), EndpointName: aws.String(lweEndpointName),
		})
		if err == nil {
			return false, nil
		}
		if code, _ := lambdaWebErrorCode(err); code == "ResourceNotFoundException" {
			return true, nil
		}
		return false, err
	}); err != nil {
		return err
	}
	var last *lweResponse
	err = lambdaWebEventually("the deleted endpoint's domain to answer 404", 30*time.Second, func() (bool, error) {
		var err error
		last, err = app.send(ctx, "GET", "/", nil)
		return err == nil && last.status == http.StatusNotFound, err
	})
	if err != nil {
		if last != nil {
			return fmt.Errorf("%w (last: HTTP %d %q)", err, last.status, last.errorType())
		}
		return err
	}
	return lweExpectError("a request to a deleted endpoint's domain", last, http.StatusNotFound, "Platform.InvalidEndpoint")
}

func (g *lambdaWebGroup) StreamedResponse(ctx context.Context, t *harness.TestContext) error {
	app, err := g.deploy(ctx, t, lweDeploy{suffix: "stream"})
	defer func() {
		if app != nil {
			app.close(ctx)
		}
	}()
	if err != nil {
		return err
	}
	// Warm up, so the gap measured below is the application's 500 ms.
	if resp, err := app.send(ctx, "GET", "/", nil); err != nil {
		return err
	} else if _, err := lweExpect200("the warm-up request", resp); err != nil {
		return err
	}
	resp, err := lweRaw(ctx, t.Endpoint, app.domain, "GET", "/stream", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /stream: HTTP %d (lambda-web-error-type %q)", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
	// The application writes "one" and, 500 ms later, "two". A proxy that
	// buffered the response would deliver both together.
	rd := bufio.NewReader(resp.Body)
	first, err := rd.ReadString('\n')
	firstAt := time.Now()
	if err != nil || first != "one\n" {
		return fmt.Errorf("first chunk = %q, %v; want \"one\\n\"", first, err)
	}
	second, err := rd.ReadString('\n')
	secondAt := time.Now()
	if err != nil || second != "two\n" {
		return fmt.Errorf("second chunk = %q, %v; want \"two\\n\"", second, err)
	}
	if gap := secondAt.Sub(firstAt); gap < 300*time.Millisecond {
		return fmt.Errorf("the chunks arrived %v apart: the response was buffered, not streamed (the application waits 500 ms)", gap)
	}
	return nil
}
