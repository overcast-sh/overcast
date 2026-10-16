package cloudformation

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// captureRouter records every request the provisioner dispatches and replies
// with a minimal CreateFunction/UpdateFunctionCode response. Update sends the
// code and the configuration as separate requests, so keeping only the last one
// would silently assert against the wrong body.
type captureRouter struct {
	requests     []capturedRequest
	failPath     string
	failed       bool
	failPaths    map[string]int
	pathRequests map[string]int
	failAt       map[string]map[int]int
}

type capturedRequest struct {
	method string
	path   string
	query  string
	body   map[string]any
}

func (c *captureRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	c.requests = append(c.requests, capturedRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: body})
	if c.pathRequests == nil {
		c.pathRequests = make(map[string]int)
	}
	c.pathRequests[r.URL.Path]++
	if status := c.failAt[r.URL.Path][c.pathRequests[r.URL.Path]]; status != 0 {
		http.Error(w, "injected compensation failure", status)
		return
	}
	if c.failPaths[r.URL.Path] > 0 {
		c.failPaths[r.URL.Path]--
		http.Error(w, "injected failure", http.StatusInternalServerError)
		return
	}
	if r.URL.Path == c.failPath && !c.failed {
		c.failed = true
		http.Error(w, "injected failure", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"FunctionArn": "arn:aws:lambda:us-east-1:000000000000:function:Stack-Function",
	})
}

func TestLambdaProvisionerUpdateDispatchesOnlyChangedPhases(t *testing.T) {
	const name = "Stack-Function"
	base := map[string]any{
		"Runtime": "nodejs22.x", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/r",
		"Description": "same", "Code": map[string]any{"ZipFile": "exports.handler=()=>1"}, "ReservedConcurrentExecutions": 1,
		"Tags": []any{map[string]any{"Key": "stage", "Value": "old"}},
	}
	tests := []struct {
		name      string
		mutate    func(map[string]any)
		wantPaths []string
	}{
		{name: "no-op update", mutate: func(map[string]any) {}, wantPaths: nil},
		{name: "configuration only", mutate: func(props map[string]any) { props["Description"] = "new" }, wantPaths: []string{"/2015-03-31/functions/" + name + "/configuration"}},
		{name: "code only", mutate: func(props map[string]any) { props["Code"] = map[string]any{"ZipFile": "exports.handler=()=>2"} }, wantPaths: []string{"/2015-03-31/functions/" + name + "/code"}},
		{name: "concurrency only", mutate: func(props map[string]any) { props["ReservedConcurrentExecutions"] = 2 }, wantPaths: []string{"/2017-10-31/functions/" + name + "/concurrency"}},
		{name: "tags only", mutate: func(props map[string]any) { props["Tags"] = []any{map[string]any{"Key": "stage", "Value": "new"}} }, wantPaths: []string{"/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:function:Stack-Function"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a function whose template changes only the named phase.
			oldProps := cloneProperties(t, base)
			newProps := cloneProperties(t, base)
			tc.mutate(newProps)
			router := &captureRouter{}

			// When: CloudFormation applies the update.
			_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, name, newProps, oldProps, &resolveContext{
				Region: "us-east-1", AccountID: "000000000000",
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}

			// Then: unrelated Lambda APIs are not called.
			if got := capturedPaths(router.requests); !slices.Equal(got, tc.wantPaths) {
				t.Fatalf("request paths = %v, want %v", got, tc.wantPaths)
			}
		})
	}
}

func TestLambdaProvisionerUpdateRemovesTags(t *testing.T) {
	const name = "Stack-Function"
	oldProps := map[string]any{
		"Tags": []any{map[string]any{"Key": "stage", "Value": "old"}},
	}
	router := &captureRouter{}

	_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, name, map[string]any{}, oldProps, &resolveContext{
		Region: "us-east-1", AccountID: "000000000000",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(router.requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(router.requests))
	}
	request := router.requests[0]
	if request.method != http.MethodDelete || request.path != "/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:function:Stack-Function" || request.query != "tagKeys=stage" {
		t.Fatalf("request = %s %s?%s, want DELETE tag resource with stage key", request.method, request.path, request.query)
	}
}

func TestLambdaProvisionerUpdateCompensatesPartialTagFailure(t *testing.T) {
	const tagPath = "/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:function:Stack-Function"
	oldProps := map[string]any{"Tags": []any{
		map[string]any{"Key": "stage", "Value": "old"},
		map[string]any{"Key": "removed", "Value": "restore"},
	}}
	newProps := map[string]any{"Tags": []any{
		map[string]any{"Key": "stage", "Value": "new"},
		map[string]any{"Key": "added", "Value": "temporary"},
	}}
	router := &captureRouter{failAt: map[string]map[int]int{tagPath: {2: http.StatusInternalServerError}}}

	_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, "Stack-Function", newProps, oldProps, &resolveContext{
		Region: "us-east-1", AccountID: "000000000000",
	})
	if err == nil {
		t.Fatal("Update error = nil, want UntagResource failure")
	}
	if len(router.requests) != 4 {
		t.Fatalf("requests = %d, want 4", len(router.requests))
	}
	if router.requests[0].method != http.MethodPost || router.requests[1].method != http.MethodDelete || router.requests[2].method != http.MethodPost || router.requests[3].method != http.MethodDelete {
		t.Fatalf("request methods = %v", []string{router.requests[0].method, router.requests[1].method, router.requests[2].method, router.requests[3].method})
	}
	restored := router.requests[2].body["Tags"].(map[string]any)
	if restored["stage"] != "old" || restored["removed"] != "restore" || router.requests[3].query != "tagKeys=added" {
		t.Fatalf("compensation = tags %#v, query %q", restored, router.requests[3].query)
	}
}

func TestLambdaProvisionerAuxiliarySupportCheckFailsWithoutProbingService(t *testing.T) {
	router := &captureRouter{}
	_, _, err := (&lambdaFunctionHandler{}).Create(context.Background(), router, nil, map[string]any{
		"FunctionName":            "auxiliary-function",
		"RuntimeManagementConfig": map[string]any{"UpdateRuntimeOn": "Auto"},
	}, &resolveContext{Region: "us-east-1"})
	if err == nil {
		t.Fatal("Create error = nil, want unsupported auxiliary property")
	}
	if len(router.requests) != 0 {
		t.Fatalf("requests = %#v, want no speculative service probe", router.requests)
	}
}

func TestLambdaProvisionerUpdateTreatsFunctionNameRemovalAsReplacement(t *testing.T) {
	_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), &captureRouter{}, nil, "named-function",
		map[string]any{}, map[string]any{"FunctionName": "named-function"}, &resolveContext{Region: "us-east-1"})
	if !errors.Is(err, errReplacementRequired) {
		t.Fatalf("Update error = %v, want errReplacementRequired", err)
	}
}

func TestLambdaProvisionerUpdateRejectsRequiredPropertyRemoval(t *testing.T) {
	tests := []struct {
		name     string
		property string
		oldProps map[string]any
		newProps map[string]any
	}{
		{name: "role", property: "Role", oldProps: map[string]any{"Role": "arn:aws:iam::000000000000:role/r"}, newProps: map[string]any{}},
		{name: "code", property: "Code", oldProps: map[string]any{"Code": map[string]any{"ZipFile": "old"}}, newProps: map[string]any{}},
		{name: "zip runtime", property: "Runtime", oldProps: map[string]any{"PackageType": "Zip", "Runtime": "nodejs22.x"}, newProps: map[string]any{"PackageType": "Zip"}},
		{name: "zip handler", property: "Handler", oldProps: map[string]any{"PackageType": "Zip", "Handler": "index.handler"}, newProps: map[string]any{"PackageType": "Zip"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := &captureRouter{}
			_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, "function", tc.newProps, tc.oldProps, &resolveContext{Region: "us-east-1"})
			var terminal updateFailure
			if !errors.As(err, &terminal) || !strings.Contains(err.Error(), tc.property) {
				t.Fatalf("Update error = %v, want terminal missing-%s failure", err, tc.property)
			}
			if terminal.dirty {
				t.Fatal("validation-only failure marked resource dirty")
			}
			if len(router.requests) != 0 {
				t.Fatalf("requests = %#v, want validation before service mutation", router.requests)
			}
		})
	}
}

func TestLambdaProvisionerUpdateDelegatesEmptyCodeObject(t *testing.T) {
	router := &captureRouter{}
	_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, "function",
		map[string]any{"Code": map[string]any{}},
		map[string]any{"Code": map[string]any{"ZipFile": "old"}},
		&resolveContext{Region: "us-east-1", LogicalID: "Function"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(router.requests) != 1 || router.requests[0].method != http.MethodPut ||
		router.requests[0].path != "/2015-03-31/functions/function/code" || len(router.requests[0].body) != 0 {
		t.Fatalf("requests = %#v, want delegated PUT with empty body object", router.requests)
	}
}

func TestLambdaProvisionerUpdateReconcilesStackOnlyTagChanges(t *testing.T) {
	const tagPath = "/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:function:Stack-Function"
	router := &captureRouter{}
	props := map[string]any{}
	_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, "Stack-Function", props, props, &resolveContext{
		Region: "us-east-1", AccountID: "000000000000",
		StackTags: []Tag{{Key: "stage", Value: "new"}}, PreviousStackTags: []Tag{{Key: "stage", Value: "old"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(router.requests) != 1 || router.requests[0].path != tagPath || router.requests[0].body["Tags"].(map[string]any)["stage"] != "new" {
		t.Fatalf("requests = %#v, want stack tag reconciliation", router.requests)
	}
}

func TestLambdaProvisionerUpdateRollsBackStackOnlyTagChanges(t *testing.T) {
	const (
		tagPath         = "/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:function:Stack-Function"
		concurrencyPath = "/2017-10-31/functions/Stack-Function/concurrency"
	)
	router := &captureRouter{failPath: concurrencyPath}
	oldProps := map[string]any{"ReservedConcurrentExecutions": 1}
	newProps := map[string]any{"ReservedConcurrentExecutions": 2}
	_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, "Stack-Function", newProps, oldProps, &resolveContext{
		Region: "us-east-1", AccountID: "000000000000",
		StackTags: []Tag{{Key: "stage", Value: "new"}}, PreviousStackTags: []Tag{{Key: "stage", Value: "old"}},
	})
	if err == nil {
		t.Fatal("Update error = nil, want concurrency failure")
	}
	if got, want := capturedPaths(router.requests), []string{tagPath, concurrencyPath, tagPath}; !slices.Equal(got, want) {
		t.Fatalf("request paths = %v, want %v", got, want)
	}
	if restored := router.requests[2].body["Tags"].(map[string]any)["stage"]; restored != "old" {
		t.Fatalf("restored stack tag = %v, want old", restored)
	}
}

func TestLambdaResourcePropertiesHashIncludesEffectiveStackTags(t *testing.T) {
	props := map[string]any{"Code": map[string]any{"ZipFile": "same"}}
	for _, resourceType := range []string{"AWS::Lambda::Function", "AWS::Lambda::EventSourceMapping", "AWS::SQS::Queue"} {
		oldHash := hashResourceProperties(resourceType, props, []Tag{{Key: "stage", Value: "old"}})
		newHash := hashResourceProperties(resourceType, props, []Tag{{Key: "stage", Value: "new"}})
		if oldHash == newHash {
			t.Errorf("%s resource hash did not change with propagated stack tags", resourceType)
		}
	}
	// AWS::S3::Bucket forwards no Tags at all and is not part of the
	// effective-stack-tag mechanism (stackTagPropagationResourceTypes in
	// provisioner.go), unlike AWS::DynamoDB::Table, which joined it in #1310
	// after this test was written against it as the "unrelated" example.
	if got, want := hashResourceProperties("AWS::S3::Bucket", props, []Tag{{Key: "stage", Value: "old"}}), hashResourceProperties("AWS::S3::Bucket", props, []Tag{{Key: "stage", Value: "new"}}); got != want {
		t.Fatalf("unrelated resource hash changed with stack tags: %q != %q", got, want)
	}
}

func TestLambdaResourcePropertiesMatchMigratesLegacyHashesWithoutChurn(t *testing.T) {
	props := map[string]any{"Code": map[string]any{"ZipFile": "same"}}
	legacyHash := hashProps(props)
	if !resourcePropertiesMatch(legacyHash, "AWS::Lambda::Function", props,
		[]Tag{{Key: "stage", Value: "old"}}, []Tag{{Key: "stage", Value: "old"}}) {
		t.Fatal("unchanged effective tags did not match legacy Lambda hash")
	}
	if resourcePropertiesMatch(legacyHash, "AWS::Lambda::Function", props,
		[]Tag{{Key: "stage", Value: "new"}}, []Tag{{Key: "stage", Value: "old"}}) {
		t.Fatal("changed effective tags incorrectly matched legacy Lambda hash")
	}
	if resourcePropertiesMatch("", "AWS::Lambda::Function", props,
		[]Tag{{Key: "stage", Value: "new"}}, []Tag{{Key: "stage", Value: "old"}}) {
		t.Fatal("changed effective tags incorrectly matched empty legacy Lambda hash")
	}
}

func cloneProperties(t *testing.T, properties map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(properties)
	if err != nil {
		t.Fatalf("marshal properties: %v", err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	return cloned
}

func TestLambdaProvisionerCreateCleanupFailureRetainsPhysicalIDForRetry(t *testing.T) {
	const name = "partial-function"
	router := &captureRouter{failPaths: map[string]int{
		"/2017-10-31/functions/" + name + "/concurrency": 1,
		"/2015-03-31/functions/" + name:                  1,
	}}
	h := &lambdaFunctionHandler{}
	props := map[string]any{
		"FunctionName": name, "Runtime": "nodejs22.x", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/r",
		"Code": map[string]any{"ZipFile": "exports.handler=()=>1"}, "ReservedConcurrentExecutions": 1,
	}
	physicalID, _, err := h.Create(context.Background(), router, nil, props, &resolveContext{Region: "us-east-1"})
	if err == nil || physicalID != name || !strings.Contains(err.Error(), "PutFunctionConcurrency") || !strings.Contains(err.Error(), "cleanup DeleteFunction") {
		t.Fatalf("Create = physicalID %q, err %v", physicalID, err)
	}
	if err := h.Delete(context.Background(), router, nil, physicalID, &resolveContext{Region: "us-east-1"}); err != nil {
		t.Fatalf("retry Delete: %v", err)
	}
}

func TestLambdaProvisionerDeletePropagatesNonNotFoundFailure(t *testing.T) {
	router := &captureRouter{failPaths: map[string]int{"/2015-03-31/functions/delete-fn": 1}}
	h := &lambdaFunctionHandler{}
	err := h.Delete(context.Background(), router, nil, "delete-fn", &resolveContext{Region: "us-east-1"})
	if err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("Delete error = %v, want injected failure", err)
	}
	if err := h.Delete(context.Background(), router, nil, "delete-fn", &resolveContext{Region: "us-east-1"}); err != nil {
		t.Fatalf("retry Delete: %v", err)
	}
}

func TestLambdaProvisionerUpdateCompensatesCompletedPhasesInReverseOrder(t *testing.T) {
	const name = "Stack-Function"
	configurationPath := "/2015-03-31/functions/" + name + "/configuration"
	codePath := "/2015-03-31/functions/" + name + "/code"
	concurrencyPath := "/2017-10-31/functions/" + name + "/concurrency"
	oldProps := map[string]any{
		"Runtime": "nodejs22.x", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/r",
		"Description": "old", "Code": map[string]any{"ZipFile": "exports.handler=()=>1"}, "ReservedConcurrentExecutions": 1,
	}
	newProps := map[string]any{
		"Runtime": "nodejs22.x", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/r",
		"Description": "new", "Code": map[string]any{"ZipFile": "exports.handler=()=>2"}, "ReservedConcurrentExecutions": 2,
	}

	tests := []struct {
		name      string
		failPath  string
		wantPaths []string
	}{
		{
			name:      "configuration failure does not compensate",
			failPath:  configurationPath,
			wantPaths: []string{configurationPath},
		},
		{
			name:      "code failure compensates configuration",
			failPath:  codePath,
			wantPaths: []string{configurationPath, codePath, configurationPath},
		},
		{
			name:     "concurrency failure compensates code then configuration",
			failPath: concurrencyPath,
			wantPaths: []string{
				configurationPath, codePath, concurrencyPath,
				codePath, configurationPath,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the named Lambda update phase will fail before completing.
			router := &captureRouter{failPath: tc.failPath}

			// When: CloudFormation updates all three mutable phases.
			_, _, err := (&lambdaFunctionHandler{}).Update(
				context.Background(), router, nil, name, newProps, oldProps, &resolveContext{Region: "us-east-1"},
			)

			// Then: only completed phases are restored, in reverse order.
			if err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("Update error = %v, want original injected failure", err)
			}
			if len(router.requests) != len(tc.wantPaths) {
				t.Fatalf("request paths = %v, want %v", capturedPaths(router.requests), tc.wantPaths)
			}
			for i, want := range tc.wantPaths {
				if router.requests[i].path != want {
					t.Errorf("request %d path = %q, want %q", i, router.requests[i].path, want)
				}
			}
			if got := router.requests[0].body["Description"]; got != "new" {
				t.Errorf("forward Description = %v, want new", got)
			}
			if tc.failPath != configurationPath {
				got := router.requests[len(router.requests)-1].body["Description"]
				if got != "old" {
					t.Errorf("restored Description = %v, want old", got)
				}
			}
		})
	}
}

func capturedPaths(requests []capturedRequest) []string {
	paths := make([]string, 0, len(requests))
	for _, request := range requests {
		paths = append(paths, request.path)
	}
	return paths
}

func TestLambdaProvisionerUpdateCompensationFailurePreservesBothErrors(t *testing.T) {
	const (
		name              = "Stack-Function"
		configurationPath = "/2015-03-31/functions/" + name + "/configuration"
		concurrencyPath   = "/2017-10-31/functions/" + name + "/concurrency"
	)
	router := &captureRouter{
		failAt: map[string]map[int]int{
			concurrencyPath:   {1: http.StatusBadRequest},
			configurationPath: {2: http.StatusInternalServerError},
		},
	}
	h := &lambdaFunctionHandler{}
	oldProps := map[string]any{
		"Runtime": "nodejs22.x", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/r",
		"Description": "old", "Code": map[string]any{"ZipFile": "exports.handler=()=>1"}, "ReservedConcurrentExecutions": 1,
	}
	newProps := map[string]any{
		"Runtime": "nodejs22.x", "Handler": "index.handler", "Role": "arn:aws:iam::000000000000:role/r",
		"Description": "new", "Code": map[string]any{"ZipFile": "exports.handler=()=>2"}, "ReservedConcurrentExecutions": 2,
	}

	_, _, err := h.Update(context.Background(), router, nil, name, newProps, oldProps, &resolveContext{Region: "us-east-1"})

	if err == nil {
		t.Fatal("Update error = nil, want original and compensation failures")
	}
	var terminal updateFailure
	if !errors.As(err, &terminal) || !terminal.dirty {
		t.Fatalf("Update error = %v, want dirty terminal updateFailure", err)
	}
	for _, want := range []string{
		"lambda PutFunctionConcurrency",
		"restore Lambda function after failed update",
		"lambda UpdateFunctionConfiguration",
		"injected compensation failure",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Update error = %q, want %q", err, want)
		}
	}
}

func TestLambdaProvisionerUpdateCodeSigningConfigDelta(t *testing.T) {
	const (
		name       = "Stack-Function"
		configPath = "/2020-06-30/functions/Stack-Function/code-signing-config"
	)
	tests := []struct {
		name       string
		oldARN     string
		newARN     string
		wantMethod string
		wantBody   map[string]any
	}{
		{name: "add", newARN: "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-new", wantMethod: http.MethodPut, wantBody: map[string]any{"CodeSigningConfigArn": "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-new"}},
		{name: "change", oldARN: "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-old", newARN: "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-new", wantMethod: http.MethodPut, wantBody: map[string]any{"CodeSigningConfigArn": "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-new"}},
		{name: "remove", oldARN: "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-old", wantMethod: http.MethodDelete, wantBody: map[string]any{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a function whose code-signing association changes.
			oldProps := map[string]any{}
			newProps := map[string]any{}
			if tc.oldARN != "" {
				oldProps["CodeSigningConfigArn"] = tc.oldARN
			}
			if tc.newARN != "" {
				newProps["CodeSigningConfigArn"] = tc.newARN
			}
			router := &captureRouter{}

			// When: CloudFormation updates the function.
			_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, name, newProps, oldProps, &resolveContext{Region: "us-east-1"})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}

			// Then: the matching Lambda association API receives the delta.
			if len(router.requests) != 1 {
				t.Fatalf("requests = %v, want one", capturedPaths(router.requests))
			}
			request := router.requests[0]
			if request.path != configPath || request.method != tc.wantMethod || !reflect.DeepEqual(request.body, tc.wantBody) {
				t.Errorf("request = %s %s %#v, want %s %s %#v", request.method, request.path, request.body, tc.wantMethod, configPath, tc.wantBody)
			}
		})
	}
}

func TestLambdaProvisionerUpdateCodeSigningConfigCompensatesLaterFailure(t *testing.T) {
	const (
		name            = "Stack-Function"
		configPath      = "/2020-06-30/functions/Stack-Function/code-signing-config"
		concurrencyPath = "/2017-10-31/functions/Stack-Function/concurrency"
	)
	oldARN := "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-old"
	newARN := "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-new"
	router := &captureRouter{failPath: concurrencyPath}

	// When: a later concurrency phase fails after changing the association.
	_, _, err := (&lambdaFunctionHandler{}).Update(context.Background(), router, nil, name,
		map[string]any{"CodeSigningConfigArn": newARN, "ReservedConcurrentExecutions": 2},
		map[string]any{"CodeSigningConfigArn": oldARN, "ReservedConcurrentExecutions": 1},
		&resolveContext{Region: "us-east-1"},
	)

	// Then: compensation restores the former association before returning.
	if err == nil {
		t.Fatal("Update error = nil, want concurrency failure")
	}
	wantPaths := []string{configPath, concurrencyPath, configPath}
	if got := capturedPaths(router.requests); !slices.Equal(got, wantPaths) {
		t.Fatalf("request paths = %v, want %v", got, wantPaths)
	}
	if got := router.requests[2].body["CodeSigningConfigArn"]; got != oldARN {
		t.Errorf("restored CodeSigningConfigArn = %v, want %q", got, oldARN)
	}
}

// carryingCode returns the body of the one request that carried a code payload.
func (c *captureRouter) carryingCode(t *testing.T) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, req := range c.requests {
		if _, ok := req.body["ZipFile"]; ok {
			found = append(found, req.body)
			continue
		}
		if code, ok := req.body["Code"].(map[string]any); ok {
			if _, ok := code["ZipFile"]; ok {
				found = append(found, req.body)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one request carrying code, got %d across %d requests",
			len(found), len(c.requests))
	}
	return found[0]
}

// codeZipFrom pulls Code.ZipFile (create) or ZipFile (update) out of a captured
// request and decodes it.
func codeZipFrom(t *testing.T, body map[string]any) []byte {
	t.Helper()
	encoded, ok := body["ZipFile"].(string)
	if !ok {
		code, _ := body["Code"].(map[string]any)
		encoded, ok = code["ZipFile"].(string)
		if !ok {
			t.Fatalf("no ZipFile in request body: %v", body)
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("ZipFile is not valid base64: %v", err)
	}
	return decoded
}

func assertReadableArchive(t *testing.T, payload []byte, wantName, wantContent string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("dispatched code is not a readable zip (%v); this is what fails at invoke time "+
			"with \"build code tar: open zip: zip: not a valid zip file\"", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != wantName {
		var names []string
		for _, f := range zr.File {
			names = append(names, f.Name)
		}
		t.Fatalf("archive contains %v, want exactly [%s]", names, wantName)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("open %s: %v", wantName, err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != wantContent {
		t.Errorf("archived content = %q, want %q", got, wantContent)
	}
}

// The provisioner must package inline template source before handing it to the
// Lambda API — the two Code.ZipFile fields do not mean the same thing.
func TestLambdaProvisionerPackagesInlineCode(t *testing.T) {
	const source = "import json\n\ndef handler(event, context):\n    return 1\n"

	t.Run("on create", func(t *testing.T) {
		router := &captureRouter{}
		h := &lambdaFunctionHandler{}
		props := map[string]any{
			"Runtime": "python3.12",
			"Handler": "index.handler",
			"Role":    "arn:aws:iam::000000000000:role/r",
			"Code":    map[string]any{"ZipFile": source},
		}

		// An unnamed function is named from its logical ID, as the provisioner
		// sets it before dispatching — two unnamed functions in one stack must
		// not end up with the same name.
		name, _, err := h.Create(context.Background(), router, nil, props, &resolveContext{
			StackName: "Stack",
			LogicalID: "Function",
			Region:    "us-east-1",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		// Shaped like CloudFormation's own generated physical ID:
		// {StackName}-{LogicalID}-{RANDOM}. The random part is not predictable,
		// so assert the shape rather than an exact string.
		if !generatedNamePattern("Stack", "Function").MatchString(name) {
			t.Errorf("physical id = %q, want Stack-Function-{RANDOM}", name)
		}
		assertReadableArchive(t, codeZipFrom(t, router.carryingCode(t)), "index.py", source)
	})

	t.Run("on update", func(t *testing.T) {
		router := &captureRouter{}
		h := &lambdaFunctionHandler{}
		props := map[string]any{
			"Runtime": "nodejs22.x",
			"Handler": "index.handler",
			"Code":    map[string]any{"ZipFile": source},
		}

		_, _, err := h.Update(context.Background(), router, nil, "Stack-Function", props, nil, &resolveContext{
			StackName: "Stack",
			Region:    "us-east-1",
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		assertReadableArchive(t, codeZipFrom(t, router.carryingCode(t)), "index.js", source)
	})
}

func TestLambdaProvisionerUpdateRecursiveLoop(t *testing.T) {
	h := &lambdaFunctionHandler{}
	rCtx := &resolveContext{Region: "us-east-1"}
	const path = "/2024-08-31/functions/loop-fn/recursion-config"

	router := &captureRouter{}
	applied, err := h.updateRecursiveLoop(context.Background(), router, "loop-fn", map[string]any{"RecursiveLoop": "Allow"}, map[string]any{}, rCtx)
	if err != nil || !applied {
		t.Fatalf("set: applied=%v err=%v", applied, err)
	}
	if len(router.requests) != 1 || router.requests[0].method != http.MethodPut || router.requests[0].path != path || router.requests[0].body["RecursiveLoop"] != "Allow" {
		t.Fatalf("set requests = %#v", router.requests)
	}

	// Removing the property reverts to AWS's default.
	router = &captureRouter{}
	applied, err = h.updateRecursiveLoop(context.Background(), router, "loop-fn", map[string]any{}, map[string]any{"RecursiveLoop": "Allow"}, rCtx)
	if err != nil || !applied || len(router.requests) != 1 || router.requests[0].body["RecursiveLoop"] != "Terminate" {
		t.Fatalf("remove: applied=%v err=%v requests=%#v", applied, err, router.requests)
	}

	// Unchanged: no call.
	router = &captureRouter{}
	applied, err = h.updateRecursiveLoop(context.Background(), router, "loop-fn", map[string]any{"RecursiveLoop": "Allow"}, map[string]any{"RecursiveLoop": "Allow"}, rCtx)
	if err != nil || applied || len(router.requests) != 0 {
		t.Fatalf("unchanged: applied=%v err=%v requests=%#v", applied, err, router.requests)
	}
}
