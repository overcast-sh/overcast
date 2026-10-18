package cloudformation

// AWS::Lambda::WebFunction, WebFunctionRevision and WebFunctionEndpoint — thin
// handlers over the lambda-web REST-JSON API (/2025-03-07/…).
//
// Ref is the ARN for all three types, which is what the CloudFormation
// reference pages give: the web function's ARN, the revision's ARN and the
// endpoint's ARN. A template's FunctionName may be the name or that ARN, since
// Ref on a WebFunction yields the ARN and the API accepts either.
//
// AWS::Lambda::WebFunction creates the container only, "with no revisions and
// no endpoints"; a revision and an endpoint are their own resources. The
// endpoint has no AutoDeploymentMode property, so a CloudFormation endpoint
// always routes by RevisionWeights (the API's default, Disabled).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
)

const (
	cfnLambdaWebFunction = "AWS::Lambda::WebFunction"
	cfnLambdaWebRevision = "AWS::Lambda::WebFunctionRevision"
	cfnLambdaWebEndpoint = "AWS::Lambda::WebFunctionEndpoint"

	lambdaWebAPI = "/2025-03-07"
)

// lambdaWebCall dispatches one lambda-web operation, signed for "lambda" as
// the service is, and decodes its response into out when out is non-nil.
func lambdaWebCall(ctx context.Context, router http.Handler, region, method, path, op string, body, out any) error {
	return signedRESTJSON(ctx, router, "lambda", region, method, path, op, body, out)
}

func lambdaWebRequest(ctx context.Context, router http.Handler, region, method, path string) (*httptest.ResponseRecorder, error) {
	return restCall("lambda", region, method, path, "", nil, scopedAuthHeader("lambda", region)).do(ctx, router)
}

// lambdaWebFunctionName reads a function's name from the name or ARN a template
// holds.
func lambdaWebFunctionName(v any) string {
	s, _ := v.(string)
	if _, after, ok := strings.Cut(s, ":web-function/"); ok {
		name, _, _ := strings.Cut(after, "/")
		return name
	}
	return s
}

// lambdaWebChildName is the last segment of an endpoint or revision ARN, and
// the function name the one before its kind segment.
func lambdaWebChild(arn, kind string) (function, child string, ok bool) {
	_, after, found := strings.Cut(arn, ":web-function/")
	if !found {
		return "", "", false
	}
	parts := strings.Split(after, "/")
	if len(parts) != 3 || parts[1] != kind {
		return "", "", false
	}
	return parts[0], parts[2], true
}

func lambdaWebFunctionPath(name string, rest ...string) string {
	p := lambdaWebAPI + "/web-functions/" + url.PathEscape(name)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// lambdaWebAPIKeys lower-cases the first letter of every key in a CloudFormation
// object, down to its leaves, except under the keys named in opaque: a map the
// user keys themselves (EnvironmentVariables) is data, not a shape.
func lambdaWebAPIKeys(v any, opaque ...string) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			key := toLowerCamelCase(k)
			skip := false
			for _, o := range opaque {
				if k == o {
					skip = true
				}
			}
			if skip {
				out[key] = item
			} else {
				out[key] = lambdaWebAPIKeys(item, opaque...)
			}
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = lambdaWebAPIKeys(item, opaque...)
		}
		return out
	default:
		return v
	}
}

// ── Web function ─────────────────────────────────────────────────────────────

type lambdaWebFunctionHandler struct{}

func (h *lambdaWebFunctionHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{"functionName": props["FunctionName"]}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	var out struct {
		FunctionARN string `json:"functionArn"`
		State       string `json:"state"`
		StateReason string `json:"stateReason"`
		CreatedAt   string `json:"createdAt"`
		UpdatedAt   string `json:"updatedAt"`
	}
	if err := lambdaWebCall(ctx, router, rCtx.Region, http.MethodPut, lambdaWebAPI+"/web-functions", "CreateWebFunction", body, &out); err != nil {
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, cfnLambdaWebFunction, props, "FunctionName", "Tags")
	return out.FunctionARN, map[string]string{
		"FunctionArn": out.FunctionARN, "State": out.State, "StateReason": out.StateReason,
		"CreatedAt": out.CreatedAt, "UpdatedAt": out.UpdatedAt,
	}, nil
}

func (h *lambdaWebFunctionHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if lambdaWebFunctionName(props["FunctionName"]) != lambdaWebFunctionName(oldProps["FunctionName"]) {
		return "", nil, errReplacementRequired
	}
	added, removed := tagDelta(mergeResourceTags(rCtx.StackTags, props["Tags"]), mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"]))
	path := lambdaWebAPI + "/tags/" + url.PathEscape(physicalID)
	if len(added) > 0 {
		if err := lambdaWebCall(ctx, router, rCtx.Region, http.MethodPost, path, "TagResource", map[string]any{"Tags": added}, nil); err != nil {
			return "", nil, err
		}
	}
	if len(removed) > 0 {
		q := url.Values{"tagKeys": removed}
		if err := lambdaWebCall(ctx, router, rCtx.Region, http.MethodDelete, path+"?"+q.Encode(), "UntagResource", nil, nil); err != nil {
			return "", nil, err
		}
	}
	return physicalID, map[string]string{"FunctionArn": physicalID}, nil
}

func (h *lambdaWebFunctionHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	name := lambdaWebFunctionName(physicalID)
	rec, err := lambdaWebRequest(ctx, router, rCtx.Region, http.MethodDelete, lambdaWebFunctionPath(name))
	return teardownError("DeleteWebFunction", rec, err)
}

// ── Revision ─────────────────────────────────────────────────────────────────

type lambdaWebRevisionHandler struct{}

func (h *lambdaWebRevisionHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	fn := lambdaWebFunctionName(props["FunctionName"])
	body := map[string]any{
		"buildConfig":   lambdaWebAPIKeys(props["BuildConfig"]),
		"serviceConfig": lambdaWebAPIKeys(props["ServiceConfig"], "EnvironmentVariables"),
	}
	if v, ok := props["Description"]; ok && v != nil {
		body["description"] = v
	}
	if v, ok := props["KmsKeyArn"]; ok && v != nil {
		body["kmsKeyArn"] = v
	}
	var out struct {
		FunctionARN string `json:"functionArn"`
		RevisionARN string `json:"revisionArn"`
		RevisionID  string `json:"revisionId"`
		State       string `json:"state"`
		StateReason string `json:"stateReason"`
		CreatedAt   string `json:"createdAt"`
	}
	if err := lambdaWebCall(ctx, router, rCtx.Region, http.MethodPost, lambdaWebFunctionPath(fn, "revisions"), "CreateWebFunctionRevision", body, &out); err != nil {
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, cfnLambdaWebRevision, props, "FunctionName", "BuildConfig", "ServiceConfig", "Description", "KmsKeyArn")
	// CloudFormation waits for a resource to stabilise, and an endpoint in the
	// same stack can route only to an Active revision: wait out Pending.
	if err := waitLambdaWebRevisionActive(ctx, router, rCtx.Region, fn, out.RevisionID); err != nil {
		return "", nil, err
	}
	return out.RevisionARN, map[string]string{
		"FunctionArn": out.FunctionARN, "RevisionArn": out.RevisionARN, "RevisionId": out.RevisionID,
		"State": "Active", "StateReason": out.StateReason, "CreatedAt": out.CreatedAt,
	}, nil
}

// waitLambdaWebRevisionActive polls the revision until it is Active, failing
// with its stateReason if it becomes Failed. The wait is bounded by ctx and by
// a minute, which is far beyond what an in-process package check takes.
func waitLambdaWebRevisionActive(ctx context.Context, router http.Handler, region, fn, id string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for delay := time.Millisecond; ; delay = min(delay*2, 100*time.Millisecond) {
		var got struct {
			State       string `json:"state"`
			StateReason string `json:"stateReason"`
		}
		if err := lambdaWebCall(ctx, router, region, http.MethodGet, lambdaWebFunctionPath(fn, "revisions", url.PathEscape(id)), "GetWebFunctionRevision", nil, &got); err != nil {
			return err
		}
		switch got.State {
		case "Active":
			return nil
		case "Failed":
			return fmt.Errorf("revision %s failed: %s", id, got.StateReason)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("revision %s did not become Active: %w", id, ctx.Err())
		case <-time.After(delay):
		}
	}
}

// Update: every revision property is immutable, so any change replaces the
// revision. The resource has no in-place update to offer.
func (h *lambdaWebRevisionHandler) Update(_ context.Context, _ http.Handler, _ *config.Config, _ string, _, _ map[string]any, _ *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

func (h *lambdaWebRevisionHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	fn, id, ok := lambdaWebChild(physicalID, "revision")
	if !ok {
		return nil
	}
	rec, err := lambdaWebRequest(ctx, router, rCtx.Region, http.MethodDelete, lambdaWebFunctionPath(fn, "revisions", url.PathEscape(id)))
	return teardownError("DeleteWebFunctionRevision", rec, err)
}

// ── Endpoint ─────────────────────────────────────────────────────────────────

type lambdaWebEndpointHandler struct{}

func (h *lambdaWebEndpointHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	fn := lambdaWebFunctionName(props["FunctionName"])
	body := map[string]any{
		"endpointName": props["EndpointName"], "endpointType": props["EndpointType"], "authType": props["AuthType"],
	}
	for prop, member := range map[string]string{"Description": "description", "Regions": "regions"} {
		if v, ok := props[prop]; ok && v != nil {
			body[member] = v
		}
	}
	for _, prop := range []string{"RevisionWeights", "ScalingConfig", "ThrottleConfig"} {
		if v, ok := props[prop]; ok && v != nil {
			body[toLowerCamelCase(prop)] = lambdaWebAPIKeys(v)
		}
	}
	var out struct {
		FunctionARN        string `json:"functionArn"`
		EndpointARN        string `json:"endpointArn"`
		DomainName         string `json:"domainName"`
		State              string `json:"state"`
		StateReason        string `json:"stateReason"`
		UpdateStatus       string `json:"updateStatus"`
		UpdateStatusReason string `json:"updateStatusReason"`
		CreatedAt          string `json:"createdAt"`
		UpdatedAt          string `json:"updatedAt"`
	}
	if err := lambdaWebCall(ctx, router, rCtx.Region, http.MethodPut, lambdaWebFunctionPath(fn, "endpoints"), "CreateWebFunctionEndpoint", body, &out); err != nil {
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, cfnLambdaWebEndpoint, props, "FunctionName", "EndpointName", "EndpointType", "AuthType",
		"Description", "Regions", "RevisionWeights", "ScalingConfig", "ThrottleConfig")
	return out.EndpointARN, lambdaWebEndpointAttrs(out.FunctionARN, out.EndpointARN, out.DomainName, out.State, out.StateReason,
		out.UpdateStatus, out.UpdateStatusReason, out.CreatedAt, out.UpdatedAt), nil
}

func lambdaWebEndpointAttrs(functionARN, endpointARN, domain, state, reason, updateStatus, updateReason, createdAt, updatedAt string) map[string]string {
	return map[string]string{
		"FunctionArn": functionARN, "EndpointArn": endpointARN, "DomainName": domain, "State": state, "StateReason": reason,
		"UpdateStatus": updateStatus, "UpdateStatusReason": updateReason, "CreatedAt": createdAt, "UpdatedAt": updatedAt,
	}
}

// Update applies the properties CloudFormation updates without interruption:
// AuthType, Description, RevisionWeights, ScalingConfig and ThrottleConfig.
// The name, type, function and Regions replace the endpoint.
func (h *lambdaWebEndpointHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, immutable := range []string{"EndpointName", "EndpointType", "Regions"} {
		if s3tablesPropChanged(props, oldProps, immutable) {
			return "", nil, errReplacementRequired
		}
	}
	if lambdaWebFunctionName(props["FunctionName"]) != lambdaWebFunctionName(oldProps["FunctionName"]) {
		return "", nil, errReplacementRequired
	}
	fn, name, ok := lambdaWebChild(physicalID, "endpoint")
	if !ok {
		return "", nil, errReplacementRequired
	}
	body := map[string]any{"authType": props["AuthType"]}
	if v, ok := props["Description"]; ok && v != nil {
		body["description"] = v
	} else if oldProps["Description"] != nil {
		body["description"] = ""
	}
	if v, ok := props["RevisionWeights"]; ok && v != nil {
		body["revisionWeights"] = lambdaWebAPIKeys(v)
	}
	// A config removed from the template is cleared with an empty object.
	for _, prop := range []string{"ScalingConfig", "ThrottleConfig"} {
		if v, ok := props[prop]; ok && v != nil {
			body[toLowerCamelCase(prop)] = lambdaWebAPIKeys(v)
		} else if oldProps[prop] != nil {
			body[toLowerCamelCase(prop)] = map[string]any{}
		}
	}
	var out struct {
		FunctionARN        string `json:"functionArn"`
		EndpointARN        string `json:"endpointArn"`
		DomainName         string `json:"domainName"`
		State              string `json:"state"`
		StateReason        string `json:"stateReason"`
		UpdateStatus       string `json:"updateStatus"`
		UpdateStatusReason string `json:"updateStatusReason"`
		CreatedAt          string `json:"createdAt"`
		UpdatedAt          string `json:"updatedAt"`
	}
	if err := lambdaWebCall(ctx, router, rCtx.Region, http.MethodPatch, lambdaWebFunctionPath(fn, "endpoints", url.PathEscape(name)), "UpdateWebFunctionEndpoint", body, &out); err != nil {
		return "", nil, err
	}
	return physicalID, lambdaWebEndpointAttrs(out.FunctionARN, out.EndpointARN, out.DomainName, out.State, out.StateReason,
		out.UpdateStatus, out.UpdateStatusReason, out.CreatedAt, out.UpdatedAt), nil
}

func (h *lambdaWebEndpointHandler) Delete(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, rCtx *resolveContext) error {
	fn, name, ok := lambdaWebChild(physicalID, "endpoint")
	if !ok {
		return nil
	}
	rec, err := lambdaWebRequest(ctx, router, rCtx.Region, http.MethodDelete, lambdaWebFunctionPath(fn, "endpoints", url.PathEscape(name)))
	return teardownError("DeleteWebFunctionEndpoint", rec, err)
}
