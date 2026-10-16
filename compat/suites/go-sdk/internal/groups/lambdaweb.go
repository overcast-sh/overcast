package groups

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb/types"
	smithy "github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// LambdaWeb returns the Lambda Web Functions service group.
func LambdaWeb(c *clients.Clients) ServiceGroup {
	g := &lambdaWebGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
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
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"lambda-web-functions": g.teardown,
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

// revisionConfig is a revision pointing at a code zip. The package is never
// fetched, so a bucket that exists nowhere is fine.
func (g *lambdaWebGroup) revisionConfig(key string) *types.RevisionConfig {
	return &types.RevisionConfig{
		BuildConfig: &types.BuildConfig{
			CodeConfig:    &types.CodeConfig{S3Object: &types.S3Object{Bucket: aws.String("compat-web-code"), Key: aws.String(key)}},
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
	return nil
}

func (g *lambdaWebGroup) CreateWebFunction(ctx context.Context, t *harness.TestContext) error {
	name := g.fnName(t)
	out, err := g.cl().CreateWebFunction(ctx, &lambdaweb.CreateWebFunctionInput{
		FunctionName:   aws.String(name),
		RevisionConfig: g.revisionConfig("app-v1.zip"),
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
	cfg := g.revisionConfig("app-v2.zip")
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
	return nil
}

func (g *lambdaWebGroup) GetWebFunctionEndpointLatestRevision(ctx context.Context, t *harness.TestContext) error {
	_, second, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	out, err := g.cl().GetWebFunctionEndpoint(ctx, &lambdaweb.GetWebFunctionEndpointInput{
		FunctionName: aws.String(g.fnName(t)), EndpointName: aws.String(lambdaWebLiveEndpoint),
	})
	if err != nil {
		return err
	}
	if out.AutoDeploymentMode != types.AutoDeploymentModeLatestRevision || out.State != types.EndpointStateActive {
		return fmt.Errorf("GetWebFunctionEndpoint: mode %q state %q", out.AutoDeploymentMode, out.State)
	}
	// The LatestRevision endpoint now serves the new revision with all traffic.
	if len(out.RevisionWeights) != 1 || aws.ToString(out.RevisionWeights[0].RevisionId) != second ||
		aws.ToInt32(out.RevisionWeights[0].Weight) != 100 {
		return fmt.Errorf("GetWebFunctionEndpoint: RevisionWeights = %+v, want %s at 100", out.RevisionWeights, second)
	}
	return nil
}

func (g *lambdaWebGroup) ListWebFunctionRevisions(ctx context.Context, t *harness.TestContext) error {
	first, second, err := g.revisionIDs(t)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var token *string
	for {
		out, err := g.cl().ListWebFunctionRevisions(ctx, &lambdaweb.ListWebFunctionRevisionsInput{
			FunctionName: aws.String(g.fnName(t)), NextToken: token,
		})
		if err != nil {
			return err
		}
		for _, r := range out.Revisions {
			seen[aws.ToString(r.RevisionId)] = true
		}
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}
	if !seen[first] || !seen[second] || len(seen) != 2 {
		return fmt.Errorf("ListWebFunctionRevisions: got %v, want exactly %s and %s", seen, first, second)
	}
	return nil
}

func (g *lambdaWebGroup) CreateWebFunctionEndpoint(ctx context.Context, t *harness.TestContext) error {
	first, second, err := g.revisionIDs(t)
	if err != nil {
		return err
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
