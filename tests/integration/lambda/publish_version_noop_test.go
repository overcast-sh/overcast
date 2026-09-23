package lambda_test

// publish_version_noop_test.go — PublishVersion's no-op rule and PublishTo
// gate (#1959). AWS: "AWS Lambda doesn't publish a version if the function's
// configuration and code haven't changed since the last version."
// https://docs.aws.amazon.com/lambda/latest/api/API_PublishVersion.html

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestPublishVersion_noChangeReusesTheLatestVersion(t *testing.T) {
	// Given: a function with one published version and no changes since.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "noop-publish-fn")
	first := publishVersionConfig(t, srv, "noop-publish-fn")

	// When: PublishVersion is called again with nothing changed.
	second := publishVersionConfig(t, srv, "noop-publish-fn")

	// Then: the same version comes back — no new number was allocated.
	if second.Version != first.Version {
		t.Errorf("Version = %q, want reused %q", second.Version, first.Version)
	}
	if second.FunctionArn != first.FunctionArn {
		t.Errorf("FunctionArn = %q, want reused %q", second.FunctionArn, first.FunctionArn)
	}
	if second.RevisionId != first.RevisionId {
		t.Errorf("RevisionId = %q, want reused %q (no new snapshot was taken)", second.RevisionId, first.RevisionId)
	}

	// And: no second numbered version exists.
	if versions := listPublishedVersions(t, srv, "noop-publish-fn"); len(versions) != 1 {
		t.Fatalf("published versions = %v, want exactly [%s]", versions, first.Version)
	}
}

func TestPublishVersion_codeChangeCreatesANewVersion(t *testing.T) {
	// Given: a function with one published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "code-change-publish-fn")
	first := publishVersionConfig(t, srv, "code-change-publish-fn")

	// When: the code changes and PublishVersion is called again.
	updateLambdaCode(t, srv, "code-change-publish-fn", []byte("new-zip-bytes"))
	second := publishVersionConfig(t, srv, "code-change-publish-fn")

	// Then: a new version is allocated.
	if second.Version == first.Version {
		t.Errorf("Version = %q, want a new version distinct from %q", second.Version, first.Version)
	}
	if second.CodeSha256 == first.CodeSha256 {
		t.Errorf("CodeSha256 unchanged across a code update: %q", second.CodeSha256)
	}
}

func TestPublishVersion_configurationChangeCreatesANewVersion(t *testing.T) {
	// Given: a function with one published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "config-change-publish-fn")
	first := publishVersionConfig(t, srv, "config-change-publish-fn")

	// When: the configuration changes and PublishVersion is called again.
	updateResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/config-change-publish-fn/configuration"), map[string]any{
		"Description": "a new description",
	})
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	updateResp.Body.Close()
	second := publishVersionConfig(t, srv, "config-change-publish-fn")

	// Then: a new version is allocated.
	if second.Version == first.Version {
		t.Errorf("Version = %q, want a new version distinct from %q", second.Version, first.Version)
	}
}

func TestPublishVersion_publishToReturns501BeforeMutation(t *testing.T) {
	// Given: a created function.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "publish-to-fn")

	// When: PublishVersion is called with PublishTo set.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/publish-to-fn/versions"), map[string]any{
		"PublishTo": "LATEST_PUBLISHED",
	})
	defer resp.Body.Close()

	// Then: 501, and nothing is persisted.
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	helpers.AssertHeader(t, resp, "x-emulator-unsupported", "true")
	helpers.AssertJSONError(t, resp, "NotImplemented")
	if versions := listPublishedVersions(t, srv, "publish-to-fn"); len(versions) != 0 {
		t.Fatalf("published versions after refused PublishTo = %v, want none", versions)
	}
}

func TestPublishVersion_revisionIdMismatchIsPreconditionFailed(t *testing.T) {
	// Given: a created function.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "revision-mismatch-fn")

	// When: PublishVersion is called with a RevisionId that isn't current.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/revision-mismatch-fn/versions"), map[string]any{
		"RevisionId": "not-the-current-revision",
	})
	defer resp.Body.Close()

	// Then: AWS's documented PreconditionFailedException.
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
	helpers.AssertJSONError(t, resp, "PreconditionFailedException")
	if versions := listPublishedVersions(t, srv, "revision-mismatch-fn"); len(versions) != 0 {
		t.Fatalf("published versions after refused publish = %v, want none", versions)
	}
}

func TestPublishVersion_codeSha256MismatchIsRejected(t *testing.T) {
	// Given: a created function.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "codesha-mismatch-fn")

	// When: PublishVersion is called with a CodeSha256 that doesn't match.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/codesha-mismatch-fn/versions"), map[string]any{
		"CodeSha256": "not-the-current-hash",
	})
	defer resp.Body.Close()

	// Then: rejected, and nothing is persisted.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
	if versions := listPublishedVersions(t, srv, "codesha-mismatch-fn"); len(versions) != 0 {
		t.Fatalf("published versions after refused publish = %v, want none", versions)
	}
}

func TestPublishVersion_codeSha256MatchPublishes(t *testing.T) {
	// Given: a created function.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "codesha-match-fn")
	cfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/codesha-match-fn/configuration"), nil)
	var cfg functionConfiguration
	decodeJSON(t, cfgResp, &cfg)
	cfgResp.Body.Close()

	// When: PublishVersion is called with the matching CodeSha256.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/codesha-match-fn/versions"), map[string]any{
		"CodeSha256": cfg.CodeSha256,
	})
	defer resp.Body.Close()

	// Then: it publishes normally.
	helpers.AssertStatus(t, resp, http.StatusCreated)
}

// publishVersionConfig publishes a version and returns its full configuration.
func publishVersionConfig(t *testing.T, srv *helpers.TestServer, function string) versionConfiguration {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/"+function+"/versions"), publishVersionReq{})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	defer resp.Body.Close()
	var ver versionConfiguration
	decodeJSON(t, resp, &ver)
	return ver
}

// updateLambdaCode replaces a function's deployment package, which is enough
// of a code change to make PublishVersion's "nothing changed since the last
// version" comparison see a difference. Shared with tests elsewhere in this
// package that published twice in a row before PublishVersion's no-op rule
// existed and now need a real change between the two calls.
func updateLambdaCode(t *testing.T, srv *helpers.TestServer, function string, zip []byte) {
	t.Helper()
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/"+function+"/code"), map[string]any{
		"ZipFile": zip,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}
