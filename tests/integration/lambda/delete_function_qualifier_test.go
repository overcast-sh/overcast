package lambda_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// publishLambdaVersion publishes a version and returns its number as a string.
func publishLambdaVersion(t *testing.T, srv *helpers.TestServer, function string) string {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/"+function+"/versions"), publishVersionReq{})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var version versionConfiguration
	decodeJSON(t, resp, &version)
	return version.Version
}

func createLambdaAlias(t *testing.T, srv *helpers.TestServer, function, alias, version string) {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/"+function+"/aliases"), createAliasReq{
		Name: alias, FunctionVersion: version,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
}

func addQualifiedPermission(t *testing.T, srv *helpers.TestServer, function, qualifier, statementID string) {
	t.Helper()
	path := lambdaURL(srv, "/functions/"+function+"/policy")
	if qualifier != "" {
		path += "?Qualifier=" + qualifier
	}
	resp := doJSON(t, http.MethodPost, path, map[string]any{
		"StatementId": statementID, "Action": "lambda:InvokeFunction", "Principal": "sns.amazonaws.com",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
}

// listPublishedVersions returns the numbered versions ListVersionsByFunction
// reports, dropping the $LATEST entry AWS always includes first.
func listPublishedVersions(t *testing.T, srv *helpers.TestServer, function string) []string {
	t.Helper()
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/"+function+"/versions"), nil)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var list struct {
		Versions []versionConfiguration `json:"Versions"`
	}
	decodeJSON(t, resp, &list)
	out := make([]string, 0, len(list.Versions))
	for _, v := range list.Versions {
		if v.Version == "$LATEST" {
			continue
		}
		out = append(out, v.Version)
	}
	return out
}

func deleteFunctionQualified(t *testing.T, srv *helpers.TestServer, function, qualifier string) *http.Response {
	t.Helper()
	path := lambdaURL(srv, "/functions/"+function)
	if qualifier != "" {
		path += "?Qualifier=" + qualifier
	}
	return doJSON(t, http.MethodDelete, path, nil)
}

// AWS: "To delete a specific function version, use the Qualifier parameter.
// Otherwise, all versions and aliases are deleted."
// https://docs.aws.amazon.com/lambda/latest/api/API_DeleteFunction.html
func TestDeleteFunction_QualifierDeletesOnlyThatVersion(t *testing.T) {
	// Given: a function with two published versions, each carrying a policy.
	// A code change separates the two publishes — PublishVersion no longer
	// allocates a new number when nothing changed since the last one.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "qualified-delete-fn")
	first := publishLambdaVersion(t, srv, "qualified-delete-fn")
	updateLambdaCode(t, srv, "qualified-delete-fn", []byte("second-zip-bytes"))
	second := publishLambdaVersion(t, srv, "qualified-delete-fn")
	addQualifiedPermission(t, srv, "qualified-delete-fn", first, "on-first")
	addQualifiedPermission(t, srv, "qualified-delete-fn", second, "on-second")
	addQualifiedPermission(t, srv, "qualified-delete-fn", "", "on-unqualified")

	// When: only the first version is deleted.
	resp := deleteFunctionQualified(t, srv, "qualified-delete-fn", first)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// Then: the base function, $LATEST and the other version survive.
	getResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/qualified-delete-fn"), nil)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	versions := listPublishedVersions(t, srv, "qualified-delete-fn")
	for _, v := range versions {
		if v == first {
			t.Fatalf("deleted version %s still listed: %v", first, versions)
		}
	}
	foundSecond := false
	for _, v := range versions {
		if v == second {
			foundSecond = true
		}
	}
	if !foundSecond {
		t.Fatalf("surviving version %s missing from %v", second, versions)
	}

	// And: only the deleted version's qualified policy is gone.
	goneResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/qualified-delete-fn/policy")+"?Qualifier="+first, nil)
	defer goneResp.Body.Close()
	helpers.AssertStatus(t, goneResp, http.StatusNotFound)
	helpers.AssertJSONError(t, goneResp, "ResourceNotFoundException")

	keptResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/qualified-delete-fn/policy")+"?Qualifier="+second, nil)
	defer keptResp.Body.Close()
	helpers.AssertStatus(t, keptResp, http.StatusOK)

	unqualifiedResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/qualified-delete-fn/policy"), nil)
	defer unqualifiedResp.Body.Close()
	helpers.AssertStatus(t, unqualifiedResp, http.StatusOK)
}

// A qualified delete must not consume the version number pool or the function
// record, so publishing continues from where it left off.
func TestDeleteFunction_QualifierLeavesTheFunctionPublishable(t *testing.T) {
	// Given: a function with one published version that is then deleted.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "qualified-delete-republish")
	first := publishLambdaVersion(t, srv, "qualified-delete-republish")
	resp := deleteFunctionQualified(t, srv, "qualified-delete-republish", first)
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// When: a new version is published.
	next := publishLambdaVersion(t, srv, "qualified-delete-republish")

	// Then: AWS never reuses a version number.
	if next == first {
		t.Fatalf("version number %s was reused after deletion", next)
	}
}

// AWS: "You can't delete a version that an alias references."
func TestDeleteFunction_QualifierRefusesAliasReferencedVersion(t *testing.T) {
	// Given: an alias pointing at a published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "alias-referenced-fn")
	version := publishLambdaVersion(t, srv, "alias-referenced-fn")
	createLambdaAlias(t, srv, "alias-referenced-fn", "live", version)

	// When: that version is deleted by number.
	resp := deleteFunctionQualified(t, srv, "alias-referenced-fn", version)
	defer resp.Body.Close()

	// Then: AWS refuses with a conflict naming the alias.
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertRequestID(t, resp)
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, "ResourceConflictException") ||
		!strings.Contains(body, "Unable to delete version because the following aliases reference it: [live]") {
		t.Fatalf("unexpected conflict body: %s", body)
	}

	// And: the version survives.
	if versions := listPublishedVersions(t, srv, "alias-referenced-fn"); len(versions) != 1 {
		t.Fatalf("versions after refused delete = %v, want the version to survive", versions)
	}
}

// An alias name as the qualifier names a version the alias references, which is
// the same refusal — DeleteFunction never deletes an alias.
func TestDeleteFunction_QualifierRefusesAliasName(t *testing.T) {
	// Given: an alias pointing at a published version.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "alias-qualifier-fn")
	version := publishLambdaVersion(t, srv, "alias-qualifier-fn")
	createLambdaAlias(t, srv, "alias-qualifier-fn", "live", version)

	// When: the alias name is used as the qualifier.
	resp := deleteFunctionQualified(t, srv, "alias-qualifier-fn", "live")
	defer resp.Body.Close()

	// Then: the request is refused and the alias survives.
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertJSONError(t, resp, "ResourceConflictException")

	aliasResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/alias-qualifier-fn/aliases/live"), nil)
	defer aliasResp.Body.Close()
	helpers.AssertStatus(t, aliasResp, http.StatusOK)
}

func TestDeleteFunction_QualifierNegativeCases(t *testing.T) {
	tests := []struct {
		name      string
		qualifier string
		status    int
		errorCode string
	}{
		{
			name:      "$LATEST cannot be deleted on its own",
			qualifier: "$LATEST",
			status:    http.StatusBadRequest,
			errorCode: "InvalidParameterValueException",
		},
		{
			name:      "unknown version",
			qualifier: "99",
			status:    http.StatusNotFound,
			errorCode: "ResourceNotFoundException",
		},
		{
			name:      "unknown alias",
			qualifier: "nope",
			status:    http.StatusNotFound,
			errorCode: "ResourceNotFoundException",
		},
		{
			name:      "qualifier outside the modeled pattern",
			qualifier: "not%2Fvalid",
			status:    http.StatusBadRequest,
			errorCode: "InvalidParameterValueException",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a function with a published version.
			srv := helpers.NewTestServer(t)
			createFunction(t, srv, "delete-qualifier-negative")
			publishLambdaVersion(t, srv, "delete-qualifier-negative")

			// When: DeleteFunction is called with a rejected qualifier.
			resp := deleteFunctionQualified(t, srv, "delete-qualifier-negative", tc.qualifier)
			defer resp.Body.Close()

			// Then: AWS's status and error code are returned…
			helpers.AssertStatus(t, resp, tc.status)
			helpers.AssertRequestID(t, resp)
			helpers.AssertJSONError(t, resp, tc.errorCode)

			// …and nothing was deleted.
			getResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/delete-qualifier-negative"), nil)
			defer getResp.Body.Close()
			helpers.AssertStatus(t, getResp, http.StatusOK)
			if versions := listPublishedVersions(t, srv, "delete-qualifier-negative"); len(versions) != 1 {
				t.Fatalf("versions after refused delete = %v, want 1", versions)
			}
		})
	}
}

// An unqualified delete keeps its documented behavior: the function, all its
// versions and all its aliases go.
func TestDeleteFunction_UnqualifiedStillDeletesEverything(t *testing.T) {
	// Given: a function with a version and an alias.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "unqualified-delete-fn")
	version := publishLambdaVersion(t, srv, "unqualified-delete-fn")
	createLambdaAlias(t, srv, "unqualified-delete-fn", "live", version)

	// When: DeleteFunction is called without a qualifier.
	resp := deleteFunctionQualified(t, srv, "unqualified-delete-fn", "")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// Then: the function is gone even though an alias referenced a version.
	getResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/unqualified-delete-fn"), nil)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}
