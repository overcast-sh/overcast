package lambda_test

// get_function_qualifier_test.go — GetFunction and GetFunctionConfiguration's
// Qualifier parameter (#1960). AWS: "Specify a version or alias to get
// details about a published version of the function."
// https://docs.aws.amazon.com/lambda/latest/api/API_GetFunction.html
// https://docs.aws.amazon.com/lambda/latest/api/API_GetFunctionConfiguration.html

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestGetFunction_qualifierNumericReturnsThatVersion(t *testing.T) {
	// Given: a function with one published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-fn-qualified")
	version := publishLambdaVersion(t, srv, "get-fn-qualified")

	// When: GetFunction is called with ?Qualifier=<version>.
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-fn-qualified")+"?Qualifier="+version, nil)
	defer resp.Body.Close()

	// Then: the version's own configuration comes back, not $LATEST.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got getFunctionResponse
	decodeJSON(t, resp, &got)
	if got.Configuration.Version != version {
		t.Errorf("Configuration.Version = %q, want %q", got.Configuration.Version, version)
	}
	if !strings.HasSuffix(got.Configuration.FunctionArn, ":"+version) {
		t.Errorf("FunctionArn = %q, want suffix %q", got.Configuration.FunctionArn, ":"+version)
	}
}

func TestGetFunction_qualifierDollarLatestExplicit(t *testing.T) {
	// Given: a function with a published version, so $LATEST and 1 both exist.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-fn-latest-explicit")
	publishLambdaVersion(t, srv, "get-fn-latest-explicit")

	// When: GetFunction is called with ?Qualifier=$LATEST.
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-fn-latest-explicit")+"?Qualifier=%24LATEST", nil)
	defer resp.Body.Close()

	// Then: it reports $LATEST, same as an unqualified call.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got getFunctionResponse
	decodeJSON(t, resp, &got)
	if got.Configuration.Version != "$LATEST" {
		t.Errorf("Configuration.Version = %q, want %q", got.Configuration.Version, "$LATEST")
	}
}

func TestGetFunction_qualifierUnknownVersionReturns404(t *testing.T) {
	// Given: a function with no version 99.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-fn-unknown-qualifier")

	// When: GetFunction is called with ?Qualifier=99.
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-fn-unknown-qualifier")+"?Qualifier=99", nil)
	defer resp.Body.Close()

	// Then: AWS's ResourceNotFoundException, not $LATEST.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestGetFunction_qualifierImageCodeReflectsThatVersion(t *testing.T) {
	// Given: an Image function whose code changes after version 1 is published.
	srv := helpers.NewTestServer(t)
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "get-fn-image-qualifier",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{ImageUri: "000000000000.dkr.ecr.us-east-1.amazonaws.com/fn:v1"},
	})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()
	version := publishLambdaVersion(t, srv, "get-fn-image-qualifier")

	updateResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/get-fn-image-qualifier/code"), map[string]any{
		"ImageUri": "000000000000.dkr.ecr.us-east-1.amazonaws.com/fn:v2",
	})
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	updateResp.Body.Close()

	// When: GetFunction is called unqualified and with ?Qualifier=<version>.
	latestResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-fn-image-qualifier"), nil)
	defer latestResp.Body.Close()
	var latest getFunctionResponse
	decodeJSON(t, latestResp, &latest)

	qualifiedResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-fn-image-qualifier")+"?Qualifier="+version, nil)
	defer qualifiedResp.Body.Close()
	var qualified getFunctionResponse
	decodeJSON(t, qualifiedResp, &qualified)

	// Then: each Code block names its own version's image.
	if latest.Code == nil || latest.Code.Location != "000000000000.dkr.ecr.us-east-1.amazonaws.com/fn:v2" {
		t.Errorf("$LATEST Code.Location = %+v, want v2", latest.Code)
	}
	if qualified.Code == nil || qualified.Code.Location != "000000000000.dkr.ecr.us-east-1.amazonaws.com/fn:v1" {
		t.Errorf("version %s Code.Location = %+v, want v1", version, qualified.Code)
	}
}

func TestGetFunctionConfiguration_qualifierNumericReturnsThatVersion(t *testing.T) {
	// Given: a function with one published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-cfg-qualified")
	version := publishLambdaVersion(t, srv, "get-cfg-qualified")

	// When: GetFunctionConfiguration is called with ?Qualifier=<version>.
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-cfg-qualified/configuration")+"?Qualifier="+version, nil)
	defer resp.Body.Close()

	// Then: the version's own configuration comes back.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got functionConfiguration
	decodeJSON(t, resp, &got)
	if got.Version != version {
		t.Errorf("Version = %q, want %q", got.Version, version)
	}
}

func TestGetFunctionConfiguration_qualifierAliasResolvesToItsVersion(t *testing.T) {
	// Given: an alias pointing at a published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-cfg-alias")
	version := publishLambdaVersion(t, srv, "get-cfg-alias")
	createLambdaAlias(t, srv, "get-cfg-alias", "live", version)

	// When: GetFunctionConfiguration is called with ?Qualifier=live.
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-cfg-alias/configuration")+"?Qualifier=live", nil)
	defer resp.Body.Close()

	// Then: the aliased version's configuration comes back.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got functionConfiguration
	decodeJSON(t, resp, &got)
	if got.Version != version {
		t.Errorf("Version = %q, want %q (resolved through alias \"live\")", got.Version, version)
	}
}

func TestGetFunctionConfiguration_qualifierUnknownAliasReturns404(t *testing.T) {
	// Given: a function with no alias named "missing".
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-cfg-unknown-alias")

	// When: GetFunctionConfiguration is called with ?Qualifier=missing.
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-cfg-unknown-alias/configuration")+"?Qualifier=missing", nil)
	defer resp.Body.Close()

	// Then: AWS's ResourceNotFoundException.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestGetFunctionConfiguration_qualifierEmbeddedInNameWins(t *testing.T) {
	// Given: a function with one published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-cfg-embedded")
	version := publishLambdaVersion(t, srv, "get-cfg-embedded")

	// When: the qualifier is embedded in the path instead of the query string.
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-cfg-embedded:"+version+"/configuration"), nil)
	defer resp.Body.Close()

	// Then: it resolves the same way an explicit Qualifier would.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got functionConfiguration
	decodeJSON(t, resp, &got)
	if got.Version != version {
		t.Errorf("Version = %q, want %q", got.Version, version)
	}
}
