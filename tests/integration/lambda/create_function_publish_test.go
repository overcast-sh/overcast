package lambda_test

// create_function_publish_test.go — CreateFunction with Publish=true.
//
// AWS: "Use the Publish parameter to create version 1 of your function from
// its initial configuration." The response is the published version's
// FunctionConfiguration — Version "1" and the qualified FunctionArn — while
// $LATEST exists alongside it, exactly as if PublishVersion had followed the
// create. https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunction.html

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// publishedConfiguration is the slice of FunctionConfiguration these tests
// read: the version identity plus the members a snapshot must carry over.
type publishedConfiguration struct {
	FunctionName     string               `json:"FunctionName"`
	FunctionArn      string               `json:"FunctionArn"`
	Version          string               `json:"Version"`
	Description      string               `json:"Description"`
	Runtime          string               `json:"Runtime"`
	Handler          string               `json:"Handler"`
	Role             string               `json:"Role"`
	Timeout          int                  `json:"Timeout"`
	MemorySize       int                  `json:"MemorySize"`
	RevisionId       string               `json:"RevisionId"`
	CodeSha256       string               `json:"CodeSha256"`
	State            string               `json:"State"`
	LastUpdateStatus string               `json:"LastUpdateStatus"`
	Environment      *functionEnvironment `json:"Environment"`
	TracingConfig    *struct {
		Mode string `json:"Mode"`
	} `json:"TracingConfig"`
}

const publishTestRole = "arn:aws:iam::000000000000:role/lambda-role"

// publishOnCreateRequest is a CreateFunction body with every member a version
// snapshot has to preserve, plus whatever the caller adds.
func publishOnCreateRequest(name string, extra map[string]any) map[string]any {
	body := map[string]any{
		"FunctionName": name, "Runtime": "python3.12", "Handler": "app.handler",
		"Role":          publishTestRole,
		"Description":   "published on create",
		"Timeout":       42,
		"MemorySize":    512,
		"Environment":   map[string]any{"Variables": map[string]string{"STAGE": "v1"}},
		"TracingConfig": map[string]any{"Mode": "Active"},
		"Code":          map[string]any{"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA=="},
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func listVersionNumbers(t *testing.T, srv *helpers.TestServer, name string) []string {
	t.Helper()
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/"+name+"/versions"), nil)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out listVersionsResponse
	decodeJSON(t, resp, &out)
	versions := make([]string, 0, len(out.Versions))
	for _, v := range out.Versions {
		versions = append(versions, v.Version)
	}
	return versions
}

func getLatestConfiguration(t *testing.T, srv *helpers.TestServer, name string) publishedConfiguration {
	t.Helper()
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/"+name+"/configuration"), nil)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg publishedConfiguration
	decodeJSON(t, resp, &cfg)
	return cfg
}

func TestCreateFunction_publishTrue_returnsPublishedVersionOne(t *testing.T) {
	// Given: a fresh server.
	srv := helpers.NewTestServer(t)

	// When: a function is created with Publish=true.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), publishOnCreateRequest("publish-fn", map[string]any{"Publish": true}))
	defer resp.Body.Close()

	// Then: 201 with the published version's configuration, not $LATEST's.
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created publishedConfiguration
	decodeJSON(t, resp, &created)
	if created.Version != "1" {
		t.Errorf("Version = %q, want \"1\"", created.Version)
	}
	if !strings.HasSuffix(created.FunctionArn, ":function:publish-fn:1") {
		t.Errorf("FunctionArn = %q, want the qualified ARN ending in :1", created.FunctionArn)
	}
	if created.FunctionName != "publish-fn" {
		t.Errorf("FunctionName = %q, want publish-fn", created.FunctionName)
	}
	if created.State != "Active" || created.LastUpdateStatus != "Successful" {
		t.Errorf("State/LastUpdateStatus = %q/%q, want Active/Successful: a published version is finished by definition", created.State, created.LastUpdateStatus)
	}
	if created.RevisionId == "" || created.CodeSha256 == "" {
		t.Errorf("RevisionId %q and CodeSha256 %q must both be set on the version", created.RevisionId, created.CodeSha256)
	}

	// And: the snapshot carries the configuration the create asked for.
	if created.Runtime != "python3.12" || created.Handler != "app.handler" || created.Role != publishTestRole {
		t.Errorf("Runtime/Handler/Role = %q/%q/%q, want python3.12/app.handler/%s", created.Runtime, created.Handler, created.Role, publishTestRole)
	}
	if created.Description != "published on create" || created.Timeout != 42 || created.MemorySize != 512 {
		t.Errorf("Description/Timeout/MemorySize = %q/%d/%d, want \"published on create\"/42/512", created.Description, created.Timeout, created.MemorySize)
	}
	if created.Environment == nil || created.Environment.Variables["STAGE"] != "v1" {
		t.Errorf("Environment = %+v, want STAGE=v1", created.Environment)
	}
	if created.TracingConfig == nil || created.TracingConfig.Mode != "Active" {
		t.Errorf("TracingConfig = %+v, want Mode Active", created.TracingConfig)
	}
}

func TestCreateFunction_publishTrue_latestStaysReadable(t *testing.T) {
	// Given: a function created with Publish=true.
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), publishOnCreateRequest("publish-latest-fn", map[string]any{"Publish": true}))
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var version publishedConfiguration
	decodeJSON(t, resp, &version)

	// When: the unqualified function is read back.
	latest := getLatestConfiguration(t, srv, "publish-latest-fn")

	// Then: $LATEST is still there, unqualified, and shares the snapshot's code.
	if latest.Version != "$LATEST" {
		t.Errorf("GetFunctionConfiguration Version = %q, want $LATEST", latest.Version)
	}
	if !strings.HasSuffix(latest.FunctionArn, ":function:publish-latest-fn") {
		t.Errorf("GetFunctionConfiguration FunctionArn = %q, want the unqualified ARN", latest.FunctionArn)
	}
	if latest.CodeSha256 != version.CodeSha256 {
		t.Errorf("$LATEST CodeSha256 = %q, version 1 CodeSha256 = %q; both must describe the same package", latest.CodeSha256, version.CodeSha256)
	}
	if latest.State != "Active" {
		t.Errorf("$LATEST State = %q, want Active", latest.State)
	}
	if latest.Description != version.Description || latest.MemorySize != version.MemorySize {
		t.Errorf("$LATEST Description/MemorySize = %q/%d, version 1 = %q/%d; the snapshot must not diverge from what was created", latest.Description, latest.MemorySize, version.Description, version.MemorySize)
	}
}

func TestCreateFunction_publishTrue_listVersionsShowsLatestAndOne(t *testing.T) {
	// Given: a function created with Publish=true.
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), publishOnCreateRequest("publish-list-fn", map[string]any{"Publish": true}))
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// When: its versions are listed.
	versions := listVersionNumbers(t, srv, "publish-list-fn")

	// Then: exactly $LATEST and 1, in AWS's order.
	if len(versions) != 2 || versions[0] != "$LATEST" || versions[1] != "1" {
		t.Fatalf("ListVersionsByFunction = %v, want [$LATEST 1]", versions)
	}
}

func TestCreateFunction_publishTrue_nextPublishedVersionIsTwo(t *testing.T) {
	// Given: a function whose version 1 was published on create.
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), publishOnCreateRequest("publish-next-fn", map[string]any{"Publish": true}))
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// And: its code changes — PublishVersion no longer allocates a new number
	// when nothing changed since the version the create just published.
	updateLambdaCode(t, srv, "publish-next-fn", []byte("second-zip-bytes"))

	// When: PublishVersion is called afterwards.
	publish := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/publish-next-fn/versions"), publishVersionReq{})
	defer publish.Body.Close()

	// Then: the counter continued from the version the create allocated.
	helpers.AssertStatus(t, publish, http.StatusCreated)
	var published versionConfiguration
	decodeJSON(t, publish, &published)
	if published.Version != "2" {
		t.Errorf("PublishVersion after a Publish=true create returned Version %q, want \"2\"", published.Version)
	}
}

func TestCreateFunction_publishFalseOrAbsent_publishesNothing(t *testing.T) {
	// Given: a fresh server.
	srv := helpers.NewTestServer(t)

	for name, extra := range map[string]map[string]any{
		"absent":         nil,
		"explicit-false": {"Publish": false},
	} {
		t.Run(name, func(t *testing.T) {
			fn := "no-publish-" + name

			// When: the function is created without asking for a version.
			resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), publishOnCreateRequest(fn, extra))
			defer resp.Body.Close()

			// Then: the response is $LATEST, and no version was published.
			helpers.AssertStatus(t, resp, http.StatusCreated)
			var created publishedConfiguration
			decodeJSON(t, resp, &created)
			if created.Version != "$LATEST" {
				t.Errorf("Version = %q, want $LATEST", created.Version)
			}
			if !strings.HasSuffix(created.FunctionArn, ":function:"+fn) {
				t.Errorf("FunctionArn = %q, want the unqualified ARN", created.FunctionArn)
			}
			if versions := listVersionNumbers(t, srv, fn); len(versions) != 1 || versions[0] != "$LATEST" {
				t.Errorf("ListVersionsByFunction = %v, want [$LATEST]", versions)
			}
		})
	}
}

func TestCreateFunction_publishTrue_duplicateNamePublishesNothing(t *testing.T) {
	// Given: an existing function with no published versions.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "publish-dup-fn")

	// When: a second create of the same name asks to publish.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), publishOnCreateRequest("publish-dup-fn", map[string]any{"Publish": true}))
	defer resp.Body.Close()

	// Then: the create is refused as before, and the existing function did not
	// gain a version — publishing is part of the create, not a side effect of
	// attempting one.
	helpers.AssertStatus(t, resp, http.StatusConflict)
	if versions := listVersionNumbers(t, srv, "publish-dup-fn"); len(versions) != 1 || versions[0] != "$LATEST" {
		t.Errorf("ListVersionsByFunction = %v after a refused create, want [$LATEST]", versions)
	}
}

// PublishTo names the $LATEST.PUBLISHED qualifier of Lambda Managed Instances,
// which Overcast does not emulate; it stays an honest 501 with or without
// Publish alongside it.
func TestCreateFunction_publishToLatestPublished_stillUnsupported(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for name, extra := range map[string]map[string]any{
		"alone":        {"PublishTo": "LATEST_PUBLISHED"},
		"with-publish": {"Publish": true, "PublishTo": "LATEST_PUBLISHED"},
	} {
		t.Run(name, func(t *testing.T) {
			fn := "publish-to-" + name
			resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), publishOnCreateRequest(fn, extra))
			assertLambdaUnsupported(t, resp)
			get := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/"+fn+"/configuration"), nil)
			helpers.AssertStatus(t, get, http.StatusNotFound)
			get.Body.Close()
		})
	}
}
