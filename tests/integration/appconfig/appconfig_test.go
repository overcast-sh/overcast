// Package appconfig_test contains integration tests for the AppConfig emulator.
//
// Every request here is sent to the binding the pinned AWS model gives the
// operation, and carries an `appconfig` SigV4 credential scope. Both matter:
// AppConfig and Service Catalog AppRegistry model the same `/applications`
// tree, and the scope is the only thing that tells the two apart on a
// single-endpoint emulator (see internal/router/router.go's /applications
// dispatch).
//
// Run: go test ./tests/integration/appconfig/...
package appconfig_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// appconfigSigV4 is the credential scope an AWS SDK for AppConfig signs with.
const appconfigSigV4 = "AWS4-HMAC-SHA256 Credential=test/20250101/us-east-1/appconfig/aws4_request, SignedHeaders=host, Signature=fake"

// appregistrySigV4 is the scope a Service Catalog AppRegistry SDK signs with —
// AppRegistry's signing name is `servicecatalog`, not `appregistry`.
const appregistrySigV4 = "AWS4-HMAC-SHA256 Credential=test/20250101/us-east-1/servicecatalog/aws4_request, SignedHeaders=host, Signature=fake"

// acDo performs an AppConfig REST-JSON request at its modeled binding.
func acDo(t *testing.T, srv *helpers.TestServer, method, path string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", appconfigSigV4)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// acRaw performs a request with a caller-supplied body and headers, for the
// two operations that bind their content as an httpPayload blob.
func acRaw(t *testing.T, srv *helpers.TestServer, method, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rdr)
	req.Header.Set("Authorization", appconfigSigV4)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func createApplication(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := acDo(t, srv, http.MethodPost, "/applications", map[string]any{"Name": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result struct {
		Id string `json:"Id"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Id == "" {
		t.Fatal("expected Id to be set")
	}
	return result.Id
}

func createEnvironment(t *testing.T, srv *helpers.TestServer, appID, name string) string {
	t.Helper()
	resp := acDo(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/environments", appID), map[string]any{"Name": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result struct {
		Id string `json:"Id"`
	}
	helpers.DecodeJSON(t, resp, &result)
	return result.Id
}

func createProfile(t *testing.T, srv *helpers.TestServer, appID, name string) string {
	t.Helper()
	resp := acDo(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/configurationprofiles", appID),
		map[string]any{"Name": name, "LocationUri": "hosted"})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result struct {
		Id string `json:"Id"`
	}
	helpers.DecodeJSON(t, resp, &result)
	return result.Id
}

// ─── The #854 headline: /applications is not AppRegistry's alone ──────────────

func TestCreateApplication_appconfigScopeIsNotAnsweredByAppRegistry(t *testing.T) {
	// Given: an emulator with both AppConfig and AppRegistry registered, which
	// model the same POST /applications binding.
	srv := helpers.NewTestServer(t)

	// When: the application is created with an appconfig credential scope.
	resp := acDo(t, srv, http.MethodPost, "/applications", map[string]any{
		"Name":        "probe-app",
		"Description": "d",
	})
	defer resp.Body.Close()

	// Then: the answer is AppConfig's, not a Service Catalog AppRegistry
	// resource wrapped in an "application" envelope with a servicecatalog ARN.
	helpers.AssertStatus(t, resp, http.StatusCreated)
	body := helpers.ReadBody(t, resp)
	if strings.Contains(body, "servicecatalog") {
		t.Errorf("appconfig CreateApplication answered with a servicecatalog resource: %s", body)
	}
	if strings.Contains(body, `"application"`) {
		t.Errorf("appconfig CreateApplication answered with AppRegistry's envelope: %s", body)
	}
	var result struct {
		Id          string `json:"Id"`
		Name        string `json:"Name"`
		Description string `json:"Description"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if result.Id == "" || result.Name != "probe-app" || result.Description != "d" {
		t.Errorf("unexpected AppConfig application %+v", result)
	}
}

func TestCreateApplication_appregistryScopeStillReachesAppRegistry(t *testing.T) {
	// Given: an emulator serving both services on /applications.
	srv := helpers.NewTestServer(t)

	// When: the same path is called with AppRegistry's signing name.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/applications",
		bytes.NewReader([]byte(`{"name":"registry-app"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", appregistrySigV4)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /applications: %v", err)
	}
	defer resp.Body.Close()

	// Then: AppRegistry answers, as it always has.
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, "servicecatalog") {
		t.Errorf("appregistry CreateApplication did not answer: %s", body)
	}
}

func TestAppConfig_unimplementedModeledPathAnswersNotImplemented(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")

	// When: a modeled AppConfig operation Overcast does not implement is
	// called (StartDeployment).
	resp := acDo(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/environments/none/deployments", appID), map[string]any{})
	defer resp.Body.Close()

	// Then: the generated registry answers 501 rather than the sub-router
	// answering a bare 404 with no AWS error envelope.
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
}

// ─── Applications ─────────────────────────────────────────────────────────────

func TestCreateApplication_success(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: CreateApplication is called.
	resp := acDo(t, srv, http.MethodPost, "/applications", map[string]any{"Name": "my-app"})
	defer resp.Body.Close()

	// Then: 201 with the modeled Application shape and nothing else — AWS's
	// Application shape is {Id, Name, Description} and carries no ARN.
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var raw map[string]any
	helpers.DecodeJSON(t, resp, &raw)
	if raw["Id"] == "" || raw["Name"] != "my-app" {
		t.Errorf("unexpected application %v", raw)
	}
	for _, unmodeled := range []string{"Arn", "Tags"} {
		if _, present := raw[unmodeled]; present {
			t.Errorf("response carries unmodeled member %q: %v", unmodeled, raw)
		}
	}
}

func TestCreateApplication_missingNameIsRejected(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: CreateApplication is called without the required Name.
	resp := acDo(t, srv, http.MethodPost, "/applications", map[string]any{})
	defer resp.Body.Close()

	// Then: BadRequestException, the modeled error.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestCreateApplication_inlineTagsAreStored(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: CreateApplication carries a Tags map, as the model allows.
	resp := acDo(t, srv, http.MethodPost, "/applications", map[string]any{
		"Name": "tagged-app",
		"Tags": map[string]string{"env": "dev"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created struct {
		Id string `json:"Id"`
	}
	helpers.DecodeJSON(t, resp, &created)

	// Then: ListTagsForResource at its modeled binding returns them.
	arn := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s", created.Id)
	tags := acDo(t, srv, http.MethodGet, "/tags/"+url.PathEscape(arn), nil)
	defer tags.Body.Close()
	helpers.AssertStatus(t, tags, http.StatusOK)
	var result struct {
		Tags map[string]string `json:"Tags"`
	}
	helpers.DecodeJSON(t, tags, &result)
	if result.Tags["env"] != "dev" {
		t.Errorf("expected inline tag env=dev, got %v", result.Tags)
	}
}

func TestGetApplication_success(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	id := createApplication(t, srv, "my-app")

	// When: GetApplication is called.
	resp := acDo(t, srv, http.MethodGet, "/applications/"+id, nil)
	defer resp.Body.Close()

	// Then: 200 with matching Name.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Name string `json:"Name"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Name != "my-app" {
		t.Errorf("expected Name=my-app, got %q", result.Name)
	}
}

func TestGetApplication_unknownIDIsNotFound(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: GetApplication names an application that does not exist.
	resp := acDo(t, srv, http.MethodGet, "/applications/nope", nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestListApplications_success(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	createApplication(t, srv, "my-app")

	// When: ListApplications is called.
	resp := acDo(t, srv, http.MethodGet, "/applications", nil)
	defer resp.Body.Close()

	// Then: 200 with at least one item.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) < 1 {
		t.Error("expected at least 1 application in list")
	}
}

func TestListApplications_paginates(t *testing.T) {
	// Given: three applications.
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"a", "b", "c"} {
		createApplication(t, srv, name)
	}

	// When: the first page is requested with the modeled max_results query
	// parameter.
	resp := acDo(t, srv, http.MethodGet, "/applications?max_results=2", nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var first struct {
		Items     []map[string]any `json:"Items"`
		NextToken string           `json:"NextToken"`
	}
	helpers.DecodeJSON(t, resp, &first)

	// Then: two items come back with a continuation token, and next_token
	// returns the rest.
	if len(first.Items) != 2 || first.NextToken == "" {
		t.Fatalf("expected a truncated first page, got %d items, token %q", len(first.Items), first.NextToken)
	}
	next := acDo(t, srv, http.MethodGet, "/applications?next_token="+url.QueryEscape(first.NextToken), nil)
	defer next.Body.Close()
	helpers.AssertStatus(t, next, http.StatusOK)
	var second struct {
		Items     []map[string]any `json:"Items"`
		NextToken string           `json:"NextToken"`
	}
	helpers.DecodeJSON(t, next, &second)
	if len(second.Items) != 1 || second.NextToken != "" {
		t.Errorf("expected a final page of 1, got %d items, token %q", len(second.Items), second.NextToken)
	}
}

func TestListApplications_invalidTokenIsRejected(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: a garbled continuation token is presented.
	resp := acDo(t, srv, http.MethodGet, "/applications?next_token=not-a-token", nil)
	defer resp.Body.Close()

	// Then: BadRequestException rather than a silent restart from page one.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestUpdateApplication_success(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	id := createApplication(t, srv, "my-app")

	// When: UpdateApplication PATCHes a new description.
	resp := acDo(t, srv, http.MethodPatch, "/applications/"+id,
		map[string]any{"Description": "updated"})
	defer resp.Body.Close()

	// Then: 200 with the new value, and it persists.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Name        string `json:"Name"`
		Description string `json:"Description"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Description != "updated" || result.Name != "my-app" {
		t.Errorf("unexpected update result %+v", result)
	}
}

func TestDeleteApplication_success(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	id := createApplication(t, srv, "my-app")

	// When: DeleteApplication is called.
	del := acDo(t, srv, http.MethodDelete, "/applications/"+id, nil)
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusNoContent)

	// Then: GetApplication returns 404.
	resp := acDo(t, srv, http.MethodGet, "/applications/"+id, nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── Environments ─────────────────────────────────────────────────────────────

func TestCreateEnvironment_success(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")

	// When: CreateEnvironment is called at its modeled binding.
	resp := acDo(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/environments", appID), map[string]any{"Name": "prod"})
	defer resp.Body.Close()

	// Then: 201 with the modeled Environment shape.
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result struct {
		ApplicationId string `json:"ApplicationId"`
		Id            string `json:"Id"`
		Name          string `json:"Name"`
		State         string `json:"State"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ApplicationId != appID || result.Id == "" || result.Name != "prod" {
		t.Errorf("unexpected environment %+v", result)
	}
	if result.State != "READY_FOR_DEPLOYMENT" {
		t.Errorf("expected State=READY_FOR_DEPLOYMENT, got %q", result.State)
	}
}

func TestCreateEnvironment_unknownApplicationIsNotFound(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: an environment is created under an application that does not exist.
	resp := acDo(t, srv, http.MethodPost, "/applications/nope/environments",
		map[string]any{"Name": "prod"})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, not a dangling environment.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestCreateEnvironment_missingNameIsRejected(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")

	// When: CreateEnvironment omits the required Name.
	resp := acDo(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/environments", appID), map[string]any{})
	defer resp.Body.Close()

	// Then: BadRequestException.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestGetEnvironment_success(t *testing.T) {
	// Given: an environment exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	envID := createEnvironment(t, srv, appID, "prod")

	// When: GetEnvironment is called.
	resp := acDo(t, srv, http.MethodGet,
		fmt.Sprintf("/applications/%s/environments/%s", appID, envID), nil)
	defer resp.Body.Close()

	// Then: 200 with matching Name.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Name string `json:"Name"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Name != "prod" {
		t.Errorf("expected Name=prod, got %q", result.Name)
	}
}

func TestListEnvironments_success(t *testing.T) {
	// Given: two environments under one application.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	createEnvironment(t, srv, appID, "dev")
	createEnvironment(t, srv, appID, "prod")

	// When: ListEnvironments is called.
	resp := acDo(t, srv, http.MethodGet,
		fmt.Sprintf("/applications/%s/environments", appID), nil)
	defer resp.Body.Close()

	// Then: both come back.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []struct {
			Name string `json:"Name"`
		} `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) != 2 {
		t.Errorf("expected 2 environments, got %d", len(result.Items))
	}
}

func TestDeleteEnvironment_success(t *testing.T) {
	// Given: an environment exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	envID := createEnvironment(t, srv, appID, "prod")

	// When: DeleteEnvironment is called.
	del := acDo(t, srv, http.MethodDelete,
		fmt.Sprintf("/applications/%s/environments/%s", appID, envID), nil)
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusNoContent)

	// Then: it is gone.
	resp := acDo(t, srv, http.MethodGet,
		fmt.Sprintf("/applications/%s/environments/%s", appID, envID), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── Configuration profiles ───────────────────────────────────────────────────

func TestCreateConfigurationProfile_success(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")

	// When: CreateConfigurationProfile is called at its modeled binding.
	resp := acDo(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/configurationprofiles", appID),
		map[string]any{"Name": "cfg", "LocationUri": "hosted", "Type": "AWS.Freeform"})
	defer resp.Body.Close()

	// Then: 201 with the modeled shape.
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result struct {
		ApplicationId string `json:"ApplicationId"`
		Id            string `json:"Id"`
		Name          string `json:"Name"`
		LocationUri   string `json:"LocationUri"`
		Type          string `json:"Type"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ApplicationId != appID || result.Id == "" || result.Name != "cfg" {
		t.Errorf("unexpected profile %+v", result)
	}
	if result.LocationUri != "hosted" || result.Type != "AWS.Freeform" {
		t.Errorf("unexpected profile %+v", result)
	}
}

func TestCreateConfigurationProfile_missingLocationUriIsRejected(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")

	// When: the required LocationUri is omitted.
	resp := acDo(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/configurationprofiles", appID),
		map[string]any{"Name": "cfg"})
	defer resp.Body.Close()

	// Then: BadRequestException.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestListConfigurationProfiles_filtersByType(t *testing.T) {
	// Given: two profiles of different types.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	for _, spec := range []map[string]any{
		{"Name": "freeform", "LocationUri": "hosted", "Type": "AWS.Freeform"},
		{"Name": "flags", "LocationUri": "hosted", "Type": "AWS.AppConfig.FeatureFlags"},
	} {
		resp := acDo(t, srv, http.MethodPost,
			fmt.Sprintf("/applications/%s/configurationprofiles", appID), spec)
		resp.Body.Close()
	}

	// When: the modeled `type` query filter is applied.
	resp := acDo(t, srv, http.MethodGet,
		fmt.Sprintf("/applications/%s/configurationprofiles?type=AWS.Freeform", appID), nil)
	defer resp.Body.Close()

	// Then: only the matching profile comes back.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []struct {
			Name string `json:"Name"`
			Type string `json:"Type"`
		} `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) != 1 || result.Items[0].Name != "freeform" {
		t.Errorf("expected only the AWS.Freeform profile, got %+v", result.Items)
	}
}

func TestDeleteConfigurationProfile_success(t *testing.T) {
	// Given: a profile exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")

	// When: DeleteConfigurationProfile is called.
	del := acDo(t, srv, http.MethodDelete,
		fmt.Sprintf("/applications/%s/configurationprofiles/%s", appID, profID), nil)
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusNoContent)

	// Then: it is gone.
	resp := acDo(t, srv, http.MethodGet,
		fmt.Sprintf("/applications/%s/configurationprofiles/%s", appID, profID), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── Hosted configuration versions ────────────────────────────────────────────

func TestCreateHostedConfigurationVersion_returnsContentAndModeledHeaders(t *testing.T) {
	// Given: a configuration profile exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")

	// When: content is posted as the httpPayload blob the model binds.
	resp := acRaw(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID),
		[]byte(`{"flag":true}`),
		map[string]string{"Content-Type": "application/json", "Description": "first"})
	defer resp.Body.Close()

	// Then: 201, the content comes back as the body, and the metadata comes
	// back in the headers the model binds it to.
	helpers.AssertStatus(t, resp, http.StatusCreated)
	helpers.AssertHeader(t, resp, "Application-Id", appID)
	helpers.AssertHeader(t, resp, "Configuration-Profile-Id", profID)
	helpers.AssertHeader(t, resp, "Version-Number", "1")
	helpers.AssertHeader(t, resp, "Description", "first")
	helpers.AssertHeader(t, resp, "Content-Type", "application/json")
	if body := helpers.ReadBody(t, resp); body != `{"flag":true}` {
		t.Errorf("expected the raw content as the response payload, got %q", body)
	}
}

func TestCreateHostedConfigurationVersion_incrementsVersionNumber(t *testing.T) {
	// Given: a configuration profile exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")
	path := fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID)

	// When: three versions are stored in turn.
	// Then: each is numbered one higher than the last.
	for want := 1; want <= 3; want++ {
		resp := acRaw(t, srv, http.MethodPost, path, []byte("v"),
			map[string]string{"Content-Type": "text/plain"})
		helpers.AssertStatus(t, resp, http.StatusCreated)
		helpers.AssertHeader(t, resp, "Version-Number", strconv.Itoa(want))
		resp.Body.Close()
	}
}

func TestCreateHostedConfigurationVersion_unknownProfileIsNotFound(t *testing.T) {
	// Given: an application with no configuration profiles.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")

	// When: content is posted against a profile that does not exist.
	resp := acRaw(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/configurationprofiles/nope/hostedconfigurationversions", appID),
		[]byte("x"), map[string]string{"Content-Type": "text/plain"})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestCreateHostedConfigurationVersion_missingContentTypeIsRejected(t *testing.T) {
	// Given: a configuration profile exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")

	// When: the required Content-Type header is absent.
	resp := acRaw(t, srv, http.MethodPost,
		fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID),
		[]byte("x"), nil)
	defer resp.Body.Close()

	// Then: BadRequestException.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "BadRequestException")
}

func TestCreateHostedConfigurationVersion_staleLatestVersionNumberConflicts(t *testing.T) {
	// Given: a profile with one hosted version.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")
	path := fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID)
	first := acRaw(t, srv, http.MethodPost, path, []byte("v1"),
		map[string]string{"Content-Type": "text/plain"})
	first.Body.Close()

	// When: a second version states a Latest-Version-Number that is no longer
	// current — the model's optimistic-concurrency header.
	resp := acRaw(t, srv, http.MethodPost, path, []byte("v2"),
		map[string]string{"Content-Type": "text/plain", "Latest-Version-Number": "0"})
	defer resp.Body.Close()

	// Then: ConflictException.
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertJSONError(t, resp, "ConflictException")
}

func TestGetHostedConfigurationVersion_returnsContent(t *testing.T) {
	// Given: a hosted configuration version exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")
	path := fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID)
	created := acRaw(t, srv, http.MethodPost, path, []byte("hello"),
		map[string]string{"Content-Type": "text/plain", "VersionLabel": "release"})
	created.Body.Close()

	// When: it is fetched by version number.
	resp := acDo(t, srv, http.MethodGet, path+"/1", nil)
	defer resp.Body.Close()

	// Then: the content is the body and the metadata is in the modeled headers.
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Version-Number", "1")
	helpers.AssertHeader(t, resp, "VersionLabel", "release")
	if body := helpers.ReadBody(t, resp); body != "hello" {
		t.Errorf("expected content hello, got %q", body)
	}
}

func TestGetHostedConfigurationVersion_unknownVersionIsNotFound(t *testing.T) {
	// Given: a configuration profile with no hosted versions.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")

	// When: a version number that was never created is requested.
	resp := acDo(t, srv, http.MethodGet,
		fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions/1", appID, profID), nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, matching AWS's documented error for the
	// operation (API_GetHostedConfigurationVersion.html).
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestListHostedConfigurationVersions_filtersByVersionLabel(t *testing.T) {
	// Given: two versions, one labelled.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")
	path := fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID)
	for _, label := range []string{"release", ""} {
		headers := map[string]string{"Content-Type": "text/plain"}
		if label != "" {
			headers["VersionLabel"] = label
		}
		resp := acRaw(t, srv, http.MethodPost, path, []byte("x"), headers)
		resp.Body.Close()
	}

	// When: the modeled version_label filter is applied.
	resp := acDo(t, srv, http.MethodGet, path+"?version_label=release", nil)
	defer resp.Body.Close()

	// Then: only the labelled version comes back, without its content.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []struct {
			VersionNumber int    `json:"VersionNumber"`
			VersionLabel  string `json:"VersionLabel"`
		} `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) != 1 || result.Items[0].VersionLabel != "release" {
		t.Errorf("expected only the labelled version, got %+v", result.Items)
	}
}

// TestListHostedConfigurationVersions_filtersByVersionLabelPrefix pins the
// wildcard behaviour AWS documents for version_label: "This parameter
// supports filtering by prefix using a wildcard, for example 'v2*'. If you
// don't specify an asterisk at the end of the value, only an exact match is
// returned." (API_ListHostedConfigurationVersions.html).
func TestListHostedConfigurationVersions_filtersByVersionLabelPrefix(t *testing.T) {
	// Given: versions labelled v2.1.0, v2.2.0 and v3.0.0.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")
	path := fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID)
	for _, label := range []string{"v2.1.0", "v2.2.0", "v3.0.0"} {
		resp := acRaw(t, srv, http.MethodPost, path, []byte("x"),
			map[string]string{"Content-Type": "text/plain", "VersionLabel": label})
		resp.Body.Close()
	}

	// When: version_label carries a trailing wildcard.
	resp := acDo(t, srv, http.MethodGet, path+"?version_label="+url.QueryEscape("v2*"), nil)
	defer resp.Body.Close()

	// Then: every label with that prefix comes back, and the v3 version is
	// excluded.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []struct {
			VersionLabel string `json:"VersionLabel"`
		} `json:"Items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	got := map[string]bool{}
	for _, item := range result.Items {
		got[item.VersionLabel] = true
	}
	if len(result.Items) != 2 || !got["v2.1.0"] || !got["v2.2.0"] {
		t.Errorf("expected only the v2.* labels for a trailing wildcard, got %+v", result.Items)
	}
}

func TestDeleteHostedConfigurationVersion_success(t *testing.T) {
	// Given: a hosted configuration version exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")
	path := fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions", appID, profID)
	created := acRaw(t, srv, http.MethodPost, path, []byte("x"),
		map[string]string{"Content-Type": "text/plain"})
	created.Body.Close()

	// When: it is deleted.
	del := acDo(t, srv, http.MethodDelete, path+"/1", nil)
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusNoContent)

	// Then: it is gone.
	resp := acDo(t, srv, http.MethodGet, path+"/1", nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestDeleteHostedConfigurationVersion_unknownVersionIsNotFound(t *testing.T) {
	// Given: a configuration profile with no hosted versions.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	profID := createProfile(t, srv, appID, "cfg")

	// When: a version number that was never created is deleted.
	resp := acDo(t, srv, http.MethodDelete,
		fmt.Sprintf("/applications/%s/configurationprofiles/%s/hostedconfigurationversions/1", appID, profID), nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, matching AWS's documented error for the
	// operation (API_DeleteHostedConfigurationVersion.html).
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── Tags ─────────────────────────────────────────────────────────────────────

func TestTagResource_roundTripsAtTheModeledBinding(t *testing.T) {
	// Given: an application exists.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	arn := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s", appID)
	tagPath := "/tags/" + url.PathEscape(arn)

	// When: tags are written at /tags/{ResourceArn}, the modeled binding.
	tag := acDo(t, srv, http.MethodPost, tagPath, map[string]any{
		"Tags": map[string]string{"team": "platform", "env": "dev"},
	})
	defer tag.Body.Close()
	helpers.AssertStatus(t, tag, http.StatusNoContent)

	// Then: AppConfig's own store answers the read, not API Gateway's
	// ARN-keyed fallback.
	list := acDo(t, srv, http.MethodGet, tagPath, nil)
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	var listed struct {
		Tags map[string]string `json:"Tags"`
	}
	helpers.DecodeJSON(t, list, &listed)
	if listed.Tags["team"] != "platform" || listed.Tags["env"] != "dev" {
		t.Errorf("unexpected tags %v", listed.Tags)
	}

	// And: UntagResource removes by the modeled tagKeys query parameter.
	untag := acDo(t, srv, http.MethodDelete, tagPath+"?tagKeys=env", nil)
	defer untag.Body.Close()
	helpers.AssertStatus(t, untag, http.StatusNoContent)

	after := acDo(t, srv, http.MethodGet, tagPath, nil)
	defer after.Body.Close()
	var remaining struct {
		Tags map[string]string `json:"Tags"`
	}
	helpers.DecodeJSON(t, after, &remaining)
	if _, present := remaining.Tags["env"]; present {
		t.Errorf("env tag survived UntagResource: %v", remaining.Tags)
	}
	if remaining.Tags["team"] != "platform" {
		t.Errorf("team tag was lost: %v", remaining.Tags)
	}
}

func TestListTagsForResource_unknownResourceIsNotFound(t *testing.T) {
	// Given: an empty store.
	srv := helpers.NewTestServer(t)

	// When: an AppConfig ARN naming nothing is read.
	arn := "arn:aws:appconfig:us-east-1:000000000000:application/missing"
	resp := acDo(t, srv, http.MethodGet, "/tags/"+url.PathEscape(arn), nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException rather than an empty map from another
	// service's tag store.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDeleteApplication_dropsItsTags(t *testing.T) {
	// Given: a tagged application.
	srv := helpers.NewTestServer(t)
	appID := createApplication(t, srv, "my-app")
	arn := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s", appID)
	tagPath := "/tags/" + url.PathEscape(arn)
	tag := acDo(t, srv, http.MethodPost, tagPath, map[string]any{"Tags": map[string]string{"k": "v"}})
	tag.Body.Close()

	// When: the application is deleted and another is created.
	del := acDo(t, srv, http.MethodDelete, "/applications/"+appID, nil)
	del.Body.Close()

	// Then: the tags went with it — nothing is left for a later resource of
	// the same identity to inherit.
	resp := acDo(t, srv, http.MethodGet, tagPath, nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}
