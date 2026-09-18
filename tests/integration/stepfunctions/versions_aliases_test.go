// Package stepfunctions_test — state machine versions, aliases and the
// control-plane operations built on them (PublishStateMachineVersion,
// *StateMachineAlias, ValidateStateMachineDefinition, and StartExecution /
// ListExecutions / DescribeExecution with a version or alias ARN).
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// passDefinition is a one-state machine whose output names the revision it
// came from, so a test can tell which definition an execution actually ran.
func passDefinition(result string) string {
	return fmt.Sprintf(`{"StartAt":"P","States":{"P":{"Type":"Pass","Result":%q,"End":true}}}`, result)
}

// ─── PublishStateMachineVersion ───────────────────────────────────────────────

func TestPublishStateMachineVersion_firstVersionIsOne(t *testing.T) {
	// Given: a state machine
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "publish-first", passDefinition("v1"))

	// When: a version is published
	resp := sfnCall(t, srv, "PublishStateMachineVersion", map[string]any{
		"stateMachineArn": smARN,
		"description":     "first",
	})
	defer resp.Body.Close()

	// Then: the version ARN is the state machine ARN qualified with 1
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineVersionArn string  `json:"stateMachineVersionArn"`
		CreationDate           float64 `json:"creationDate"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.StateMachineVersionArn != smARN+":1" {
		t.Errorf("stateMachineVersionArn = %q, want %q", out.StateMachineVersionArn, smARN+":1")
	}
	if out.CreationDate == 0 {
		t.Error("creationDate = 0, want the version's creation time")
	}
}

func TestPublishStateMachineVersion_unchangedRevisionIsIdempotent(t *testing.T) {
	// Given: a state machine with one published version
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "publish-idem", passDefinition("v1"))
	first := publishVersion(t, srv, smARN, "")

	// When: the same revision is published again
	second := publishVersion(t, srv, smARN, "")

	// Then: the existing version comes back rather than a new one
	if second != first {
		t.Errorf("second publish = %q, want the existing %q", second, first)
	}
}

func TestPublishStateMachineVersion_newRevisionGetsNextNumber(t *testing.T) {
	// Given: a published version, then an update to the definition
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "publish-next", passDefinition("v1"))
	publishVersion(t, srv, smARN, "")
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})

	// When: a version is published again
	got := publishVersion(t, srv, smARN, "")

	// Then: it is version 2
	if got != smARN+":2" {
		t.Errorf("version = %q, want %q", got, smARN+":2")
	}
}

func TestPublishStateMachineVersion_revisionIdMismatch(t *testing.T) {
	// Given: a state machine that has been updated since creation
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "publish-conflict", passDefinition("v1"))
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})

	// When: publishing is conditioned on the initial revision
	resp := sfnCall(t, srv, "PublishStateMachineVersion", map[string]any{
		"stateMachineArn": smARN,
		"revisionId":      "INITIAL",
	})
	defer resp.Body.Close()

	// Then: ConflictException, and nothing is published
	helpers.AssertJSONError(t, resp, "ConflictException")
	if versions := listVersionARNs(t, srv, smARN, 0); len(versions) != 0 {
		t.Errorf("versions after refused publish = %v, want none", versions)
	}
}

func TestPublishStateMachineVersion_initialRevisionIdMatchesNewStateMachine(t *testing.T) {
	// Given: a state machine never updated
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "publish-initial", passDefinition("v1"))

	// When: publishing is conditioned on INITIAL
	got := publishVersion(t, srv, smARN, "INITIAL")

	// Then: it publishes version 1
	if got != smARN+":1" {
		t.Errorf("version = %q, want %q", got, smARN+":1")
	}
}

func TestPublishStateMachineVersion_matchingRevisionIdFromUpdate(t *testing.T) {
	// Given: an update that reports its revisionId
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "publish-revision", passDefinition("v1"))
	upd := updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})
	if upd.RevisionID == "" {
		t.Fatal("UpdateStateMachine returned no revisionId")
	}

	// When: publishing is conditioned on that revisionId
	got := publishVersion(t, srv, smARN, upd.RevisionID)

	// Then: it publishes, and DescribeStateMachine reports the same revision
	if got != smARN+":1" {
		t.Errorf("version = %q, want %q", got, smARN+":1")
	}
	described := describeSMRaw(t, srv, smARN)
	if described["revisionId"] != upd.RevisionID {
		t.Errorf("DescribeStateMachine revisionId = %v, want %q", described["revisionId"], upd.RevisionID)
	}
}

func TestPublishStateMachineVersion_stateMachineNotFound(t *testing.T) {
	// Given: no state machines
	srv := helpers.NewTestServer(t)

	// When: publishing a version of a missing state machine
	resp := sfnCall(t, srv, "PublishStateMachineVersion", map[string]any{
		"stateMachineArn": "arn:aws:states:us-east-1:000000000000:stateMachine:missing",
	})
	defer resp.Body.Close()

	// Then: StateMachineDoesNotExist
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "StateMachineDoesNotExist")
}

func TestPublishStateMachineVersionCBOR_success(t *testing.T) {
	// Given: a state machine
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "publish-cbor", passDefinition("v1"))

	// When: a version is published over RPC v2 CBOR
	resp := sfnCBORCall(t, srv, "PublishStateMachineVersion", map[string]any{"stateMachineArn": smARN})
	defer resp.Body.Close()

	// Then: the version ARN comes back
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineVersionArn string `cbor:"stateMachineVersionArn"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode CBOR: %v", err)
	}
	if out.StateMachineVersionArn != smARN+":1" {
		t.Errorf("stateMachineVersionArn = %q, want %q", out.StateMachineVersionArn, smARN+":1")
	}
}

// ─── CreateStateMachine / UpdateStateMachine publish ─────────────────────────

func TestCreateStateMachine_publishReturnsVersionArn(t *testing.T) {
	// Given: an empty server
	srv := helpers.NewTestServer(t)

	// When: a state machine is created with publish=true
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name":               "create-publish",
		"definition":         passDefinition("v1"),
		"roleArn":            "arn:aws:iam::000000000000:role/r",
		"publish":            true,
		"versionDescription": "initial release",
	})
	defer resp.Body.Close()

	// Then: version 1 is returned and describable with its description
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineArn        string `json:"stateMachineArn"`
		StateMachineVersionArn string `json:"stateMachineVersionArn"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.StateMachineVersionArn != out.StateMachineArn+":1" {
		t.Fatalf("stateMachineVersionArn = %q, want %q", out.StateMachineVersionArn, out.StateMachineArn+":1")
	}
	described := describeSMRaw(t, srv, out.StateMachineVersionArn)
	if described["description"] != "initial release" {
		t.Errorf("version description = %v, want %q", described["description"], "initial release")
	}
	if described["stateMachineArn"] != out.StateMachineVersionArn {
		t.Errorf("described stateMachineArn = %v, want the version ARN", described["stateMachineArn"])
	}
}

func TestCreateStateMachine_versionDescriptionWithoutPublish(t *testing.T) {
	// Given: an empty server
	srv := helpers.NewTestServer(t)

	// When: versionDescription is set but publish is not
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name":               "create-desc-only",
		"definition":         passDefinition("v1"),
		"roleArn":            "arn:aws:iam::000000000000:role/r",
		"versionDescription": "orphan",
	})
	defer resp.Body.Close()

	// Then: ValidationException, and no state machine is created
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
	d := sfnCall(t, srv, "DescribeStateMachine", map[string]any{
		"stateMachineArn": "arn:aws:states:us-east-1:000000000000:stateMachine:create-desc-only",
	})
	defer d.Body.Close()
	helpers.AssertJSONError(t, d, "StateMachineDoesNotExist")
}

func TestUpdateStateMachine_publishReturnsVersionArn(t *testing.T) {
	// Given: a state machine published at version 1
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "update-publish", passDefinition("v1"))
	publishVersion(t, srv, smARN, "")

	// When: it is updated with publish=true
	got := updateSM(t, srv, map[string]any{
		"stateMachineArn":    smARN,
		"definition":         passDefinition("v2"),
		"publish":            true,
		"versionDescription": "second",
	})

	// Then: version 2 is returned alongside a revisionId, and version 1 keeps
	// its own definition
	if got.StateMachineVersionArn != smARN+":2" {
		t.Errorf("stateMachineVersionArn = %q, want %q", got.StateMachineVersionArn, smARN+":2")
	}
	if got.RevisionID == "" {
		t.Error("revisionId empty, want the new revision")
	}
	v1 := describeSMRaw(t, srv, smARN+":1")
	if v1["definition"] != passDefinition("v1") {
		t.Errorf("version 1 definition = %v, want the original", v1["definition"])
	}
	v2 := describeSMRaw(t, srv, smARN+":2")
	if v2["definition"] != passDefinition("v2") || v2["description"] != "second" {
		t.Errorf("version 2 = %v, want the updated definition and description", v2)
	}
	if v2["revisionId"] != got.RevisionID {
		t.Errorf("version 2 revisionId = %v, want %q", v2["revisionId"], got.RevisionID)
	}
}

func TestUpdateStateMachine_versionDescriptionWithoutPublish(t *testing.T) {
	// Given: a state machine
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "update-desc-only", passDefinition("v1"))

	// When: versionDescription is set without publish
	resp := sfnCall(t, srv, "UpdateStateMachine", map[string]any{
		"stateMachineArn":    smARN,
		"definition":         passDefinition("v2"),
		"versionDescription": "orphan",
	})
	defer resp.Body.Close()

	// Then: ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

// ─── ListStateMachineVersions / DeleteStateMachineVersion ─────────────────────

func TestListStateMachineVersions_newestFirstWithPagination(t *testing.T) {
	// Given: three published versions
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "list-versions", passDefinition("v1"))
	for i := 1; i <= 3; i++ {
		if i > 1 {
			updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition(fmt.Sprintf("v%d", i))})
		}
		publishVersion(t, srv, smARN, "")
	}

	// When: we page through them two at a time
	first := listVersionsPage(t, srv, smARN, 2, "")
	second := listVersionsPage(t, srv, smARN, 2, first.NextToken)

	// Then: newest first, split across two pages
	if len(first.Versions) != 2 || first.Versions[0].Arn != smARN+":3" || first.Versions[1].Arn != smARN+":2" {
		t.Errorf("first page = %+v, want versions 3 and 2", first.Versions)
	}
	if first.NextToken == "" {
		t.Fatal("first page has no nextToken")
	}
	if len(second.Versions) != 1 || second.Versions[0].Arn != smARN+":1" || second.NextToken != "" {
		t.Errorf("second page = %+v (next %q), want only version 1", second.Versions, second.NextToken)
	}
}

func TestListStateMachineVersions_invalidToken(t *testing.T) {
	// Given: a state machine
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "list-versions-token", passDefinition("v1"))

	// When: a garbage nextToken is supplied
	resp := sfnCall(t, srv, "ListStateMachineVersions", map[string]any{
		"stateMachineArn": smARN,
		"nextToken":       "not-a-token",
	})
	defer resp.Body.Close()

	// Then: InvalidToken
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidToken")
}

func TestDeleteStateMachineVersion_removesVersion(t *testing.T) {
	// Given: two published versions
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "delete-version", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})
	publishVersion(t, srv, smARN, "")

	// When: version 1 is deleted
	resp := sfnCall(t, srv, "DeleteStateMachineVersion", map[string]any{"stateMachineVersionArn": v1})
	defer resp.Body.Close()

	// Then: it is gone from the list and no longer describable
	helpers.AssertStatus(t, resp, http.StatusOK)
	if got := listVersionARNs(t, srv, smARN, 0); len(got) != 1 || got[0] != smARN+":2" {
		t.Errorf("versions = %v, want only version 2", got)
	}
	d := sfnCall(t, srv, "DescribeStateMachine", map[string]any{"stateMachineArn": v1})
	defer d.Body.Close()
	helpers.AssertJSONError(t, d, "StateMachineDoesNotExist")

	// And: version numbers are never reused
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v3")})
	if got := publishVersion(t, srv, smARN, ""); got != smARN+":3" {
		t.Errorf("next version = %q, want %q", got, smARN+":3")
	}
}

func TestDeleteStateMachineVersion_referencedByAlias(t *testing.T) {
	// Given: a version an alias routes to
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "delete-aliased-version", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	createAlias(t, srv, "PROD", "", routing(v1, 100))

	// When: the version is deleted
	resp := sfnCall(t, srv, "DeleteStateMachineVersion", map[string]any{"stateMachineVersionArn": v1})
	defer resp.Body.Close()

	// Then: ConflictException, and the version remains
	helpers.AssertJSONError(t, resp, "ConflictException")
	if got := listVersionARNs(t, srv, smARN, 0); len(got) != 1 {
		t.Errorf("versions = %v, want version 1 kept", got)
	}
}

// ─── Aliases ──────────────────────────────────────────────────────────────────

func TestCreateStateMachineAlias_describeRoundTrip(t *testing.T) {
	// Given: a published version
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-describe", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")

	// When: an alias is created and described
	aliasARN := createAlias(t, srv, "PROD", "production", routing(v1, 100))
	resp := sfnCall(t, srv, "DescribeStateMachineAlias", map[string]any{"stateMachineAliasArn": aliasARN})
	defer resp.Body.Close()

	// Then: the alias ARN is qualified by name and the configuration echoes
	helpers.AssertStatus(t, resp, http.StatusOK)
	if aliasARN != smARN+":PROD" {
		t.Errorf("alias ARN = %q, want %q", aliasARN, smARN+":PROD")
	}
	var out struct {
		StateMachineAliasArn string  `json:"stateMachineAliasArn"`
		Name                 string  `json:"name"`
		Description          string  `json:"description"`
		CreationDate         float64 `json:"creationDate"`
		UpdateDate           float64 `json:"updateDate"`
		RoutingConfiguration []struct {
			StateMachineVersionArn string `json:"stateMachineVersionArn"`
			Weight                 int    `json:"weight"`
		} `json:"routingConfiguration"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.StateMachineAliasArn != aliasARN || out.Name != "PROD" || out.Description != "production" {
		t.Errorf("alias = %+v", out)
	}
	if len(out.RoutingConfiguration) != 1 || out.RoutingConfiguration[0].StateMachineVersionArn != v1 || out.RoutingConfiguration[0].Weight != 100 {
		t.Errorf("routingConfiguration = %+v", out.RoutingConfiguration)
	}
	if out.CreationDate == 0 || out.UpdateDate != out.CreationDate {
		t.Errorf("creationDate=%v updateDate=%v, want equal and non-zero", out.CreationDate, out.UpdateDate)
	}
}

func TestCreateStateMachineAlias_invalidRouting(t *testing.T) {
	cases := []struct {
		name    string
		routing func(v1, v2 string) []map[string]any
	}{
		{name: "weights do not sum to 100", routing: func(v1, v2 string) []map[string]any {
			return append(routing(v1, 50), routing(v2, 40)...)
		}},
		{name: "same version twice", routing: func(v1, _ string) []map[string]any {
			return append(routing(v1, 50), routing(v1, 50)...)
		}},
		{name: "three entries", routing: func(v1, v2 string) []map[string]any {
			return append(append(routing(v1, 50), routing(v2, 50)...), routing(v1, 0)...)
		}},
		{name: "empty", routing: func(_, _ string) []map[string]any { return []map[string]any{} }},
		{name: "weight above 100", routing: func(v1, _ string) []map[string]any { return routing(v1, 101) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: two published versions
			srv := helpers.NewTestServer(t)
			smARN := createSM(t, srv, "alias-invalid", passDefinition("v1"))
			v1 := publishVersion(t, srv, smARN, "")
			updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})
			v2 := publishVersion(t, srv, smARN, "")

			// When: an alias is created with an invalid routing configuration
			resp := sfnCall(t, srv, "CreateStateMachineAlias", map[string]any{
				"name":                 "BAD",
				"routingConfiguration": tc.routing(v1, v2),
			})
			defer resp.Body.Close()

			// Then: ValidationException
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "ValidationException")
		})
	}
}

func TestCreateStateMachineAlias_numericNameRejected(t *testing.T) {
	// Given: a published version
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-numeric", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")

	// When: an alias named like a version number is created
	resp := sfnCall(t, srv, "CreateStateMachineAlias", map[string]any{
		"name":                 "123",
		"routingConfiguration": routing(v1, 100),
	})
	defer resp.Body.Close()

	// Then: InvalidName — it would collide with a version ARN
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidName")
}

func TestCreateStateMachineAlias_unknownVersion(t *testing.T) {
	// Given: a state machine with no versions
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-unknown-version", passDefinition("v1"))

	// When: an alias points at a version that was never published
	resp := sfnCall(t, srv, "CreateStateMachineAlias", map[string]any{
		"name":                 "PROD",
		"routingConfiguration": routing(smARN+":7", 100),
	})
	defer resp.Body.Close()

	// Then: ResourceNotFound
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFound")
}

func TestCreateStateMachineAlias_idempotentAndConflict(t *testing.T) {
	// Given: an alias
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-idem", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	aliasARN := createAlias(t, srv, "PROD", "d", routing(v1, 100))

	// When: the identical request is repeated
	again := createAlias(t, srv, "PROD", "d", routing(v1, 100))

	// Then: the same alias comes back
	if again != aliasARN {
		t.Errorf("repeat create = %q, want %q", again, aliasARN)
	}

	// When: the same name is created with a different configuration
	resp := sfnCall(t, srv, "CreateStateMachineAlias", map[string]any{
		"name":                 "PROD",
		"description":          "different",
		"routingConfiguration": routing(v1, 100),
	})
	defer resp.Body.Close()

	// Then: ConflictException
	helpers.AssertJSONError(t, resp, "ConflictException")
}

func TestUpdateStateMachineAlias_changesRouting(t *testing.T) {
	// Given: an alias on version 1 and a version 2
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-update", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})
	v2 := publishVersion(t, srv, smARN, "")
	aliasARN := createAlias(t, srv, "PROD", "", routing(v1, 100))

	// When: the alias is moved to split between both versions
	resp := sfnCall(t, srv, "UpdateStateMachineAlias", map[string]any{
		"stateMachineAliasArn": aliasARN,
		"description":          "canary",
		"routingConfiguration": append(routing(v1, 90), routing(v2, 10)...),
	})
	defer resp.Body.Close()

	// Then: DescribeStateMachineAlias reflects the change
	helpers.AssertStatus(t, resp, http.StatusOK)
	var upd struct {
		UpdateDate float64 `json:"updateDate"`
	}
	helpers.DecodeJSON(t, resp, &upd)
	if upd.UpdateDate == 0 {
		t.Error("updateDate = 0")
	}
	d := sfnCall(t, srv, "DescribeStateMachineAlias", map[string]any{"stateMachineAliasArn": aliasARN})
	defer d.Body.Close()
	var out struct {
		Description          string           `json:"description"`
		RoutingConfiguration []map[string]any `json:"routingConfiguration"`
	}
	helpers.DecodeJSON(t, d, &out)
	if out.Description != "canary" || len(out.RoutingConfiguration) != 2 {
		t.Errorf("alias after update = %+v", out)
	}
}

func TestUpdateStateMachineAlias_nothingToUpdate(t *testing.T) {
	// Given: an alias
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-update-empty", passDefinition("v1"))
	aliasARN := createAlias(t, srv, "PROD", "", routing(publishVersion(t, srv, smARN, ""), 100))

	// When: neither description nor routingConfiguration is supplied
	resp := sfnCall(t, srv, "UpdateStateMachineAlias", map[string]any{"stateMachineAliasArn": aliasARN})
	defer resp.Body.Close()

	// Then: ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestListStateMachineAliases_paginationAndVersionFilter(t *testing.T) {
	// Given: three aliases, two on version 1 and one on version 2
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-list", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})
	v2 := publishVersion(t, srv, smARN, "")
	createAlias(t, srv, "A", "", routing(v1, 100))
	createAlias(t, srv, "B", "", routing(v1, 100))
	createAlias(t, srv, "C", "", routing(v2, 100))

	// When: we page through the state machine's aliases two at a time
	first := listAliasesPage(t, srv, smARN, 2, "")
	second := listAliasesPage(t, srv, smARN, 2, first.NextToken)

	// Then: newest first across two pages
	got := append(first.Aliases, second.Aliases...)
	want := []string{smARN + ":C", smARN + ":B", smARN + ":A"}
	if len(got) != 3 || got[0].Arn != want[0] || got[1].Arn != want[1] || got[2].Arn != want[2] {
		t.Errorf("aliases = %+v, want %v", got, want)
	}
	if first.NextToken == "" || second.NextToken != "" {
		t.Errorf("tokens = %q / %q, want a first-page token only", first.NextToken, second.NextToken)
	}

	// When: the list is filtered by version 2's ARN
	byVersion := listAliasesPage(t, srv, v2, 0, "")

	// Then: only the alias routing to version 2
	if len(byVersion.Aliases) != 1 || byVersion.Aliases[0].Arn != smARN+":C" {
		t.Errorf("aliases of version 2 = %+v, want only C", byVersion.Aliases)
	}
}

func TestDeleteStateMachineAlias_thenNotFound(t *testing.T) {
	// Given: an alias
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "alias-delete", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	aliasARN := createAlias(t, srv, "PROD", "", routing(v1, 100))

	// When: it is deleted
	resp := sfnCall(t, srv, "DeleteStateMachineAlias", map[string]any{"stateMachineAliasArn": aliasARN})
	defer resp.Body.Close()

	// Then: it can no longer be described, and its version can now be deleted
	helpers.AssertStatus(t, resp, http.StatusOK)
	d := sfnCall(t, srv, "DescribeStateMachineAlias", map[string]any{"stateMachineAliasArn": aliasARN})
	defer d.Body.Close()
	helpers.AssertStatus(t, d, http.StatusBadRequest)
	helpers.AssertJSONError(t, d, "ResourceNotFound")
	dv := sfnCall(t, srv, "DeleteStateMachineVersion", map[string]any{"stateMachineVersionArn": v1})
	defer dv.Body.Close()
	helpers.AssertStatus(t, dv, http.StatusOK)
}

// ─── Executions through versions and aliases ─────────────────────────────────

func TestStartExecution_versionArnRunsThatVersion(t *testing.T) {
	// Given: version 1 published, then the state machine moved on
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "exec-version", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})

	// When: an execution is started through the version ARN
	execARN := startExec(t, srv, v1, `{}`)
	waitForTerminal(t, srv, execARN)

	// Then: it ran version 1's definition, under the base state machine name
	got := describeExecVersioned(t, srv, execARN)
	if got.Output != `"v1"` {
		t.Errorf("output = %s, want version 1's result", got.Output)
	}
	if !strings.HasPrefix(execARN, "arn:aws:states:us-east-1:000000000000:execution:exec-version:") {
		t.Errorf("executionArn = %q, want the base state machine name", execARN)
	}
	if got.StateMachineArn != smARN || got.StateMachineVersionArn != v1 || got.StateMachineAliasArn != "" {
		t.Errorf("stateMachineArn=%q versionArn=%q aliasArn=%q", got.StateMachineArn, got.StateMachineVersionArn, got.StateMachineAliasArn)
	}

	// And: DescribeStateMachineForExecution reports the version's definition
	resp := sfnCall(t, srv, "DescribeStateMachineForExecution", map[string]any{"executionArn": execARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var sm struct {
		Definition string `json:"definition"`
	}
	helpers.DecodeJSON(t, resp, &sm)
	if sm.Definition != passDefinition("v1") {
		t.Errorf("DescribeStateMachineForExecution definition = %s, want version 1's", sm.Definition)
	}
}

func TestStartExecution_aliasArnRoutesByWeight(t *testing.T) {
	// Given: an alias sending all traffic to version 2 (version 1 at weight 0)
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "exec-alias", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v2")})
	v2 := publishVersion(t, srv, smARN, "")
	updateSM(t, srv, map[string]any{"stateMachineArn": smARN, "definition": passDefinition("v3")})
	aliasARN := createAlias(t, srv, "PROD", "", append(routing(v1, 0), routing(v2, 100)...))

	// When: an execution is started through the alias
	execARN := startExec(t, srv, aliasARN, `{}`)
	waitForTerminal(t, srv, execARN)

	// Then: version 2 ran, and both the alias and the version are reported
	got := describeExecVersioned(t, srv, execARN)
	if got.Output != `"v2"` {
		t.Errorf("output = %s, want version 2's result", got.Output)
	}
	if got.StateMachineAliasArn != aliasARN || got.StateMachineVersionArn != v2 {
		t.Errorf("aliasArn=%q versionArn=%q, want %q / %q", got.StateMachineAliasArn, got.StateMachineVersionArn, aliasARN, v2)
	}
}

func TestStartExecution_unknownVersionArn(t *testing.T) {
	// Given: a state machine with no versions
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "exec-missing-version", passDefinition("v1"))

	// When: an execution is started against a version that does not exist
	resp := sfnCall(t, srv, "StartExecution", map[string]any{"stateMachineArn": smARN + ":4"})
	defer resp.Body.Close()

	// Then: StateMachineDoesNotExist
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "StateMachineDoesNotExist")
}

func TestStartSyncExecution_versionArn(t *testing.T) {
	// Given: a published EXPRESS state machine that has since changed
	srv := helpers.NewTestServer(t)
	create := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name":       "sync-version",
		"type":       "EXPRESS",
		"definition": passDefinition("v1"),
		"roleArn":    "arn:aws:iam::000000000000:role/r",
		"publish":    true,
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	var created struct {
		StateMachineArn        string `json:"stateMachineArn"`
		StateMachineVersionArn string `json:"stateMachineVersionArn"`
	}
	helpers.DecodeJSON(t, create, &created)
	updateSM(t, srv, map[string]any{"stateMachineArn": created.StateMachineArn, "definition": passDefinition("v2")})

	// When: StartSyncExecution targets the version
	resp := sfnCall(t, srv, "StartSyncExecution", map[string]any{"stateMachineArn": created.StateMachineVersionArn})
	defer resp.Body.Close()

	// Then: version 1 ran and the response names the base state machine
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineArn string `json:"stateMachineArn"`
		Output          string `json:"output"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.Output != `"v1"` || out.StateMachineArn != created.StateMachineArn {
		t.Errorf("output=%s stateMachineArn=%q", out.Output, out.StateMachineArn)
	}
}

func TestListExecutions_versionAndAliasFilters(t *testing.T) {
	// Given: executions started directly, through a version, and through an alias
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "list-exec-filter", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	aliasARN := createAlias(t, srv, "PROD", "", routing(v1, 100))
	direct := startExec(t, srv, smARN, `{}`)
	viaVersion := startExec(t, srv, v1, `{}`)
	viaAlias := startExec(t, srv, aliasARN, `{}`)
	for _, arn := range []string{direct, viaVersion, viaAlias} {
		waitForTerminal(t, srv, arn)
	}

	// When / Then: each filter returns the executions started through it
	if got := listExecutionARNs(t, srv, smARN); len(got) != 3 {
		t.Errorf("base filter = %v, want all three", got)
	}
	// The version filter covers executions that ran the version, whether
	// started through it directly or routed to it by an alias.
	if got := listExecutionARNs(t, srv, v1); len(got) != 2 || !contains(got, viaVersion) || !contains(got, viaAlias) {
		t.Errorf("version filter = %v, want %q and %q", got, viaVersion, viaAlias)
	}
	if got := listExecutionARNs(t, srv, aliasARN); len(got) != 1 || got[0] != viaAlias {
		t.Errorf("alias filter = %v, want only %q", got, viaAlias)
	}
}

// ─── DescribeStateMachine / DeleteStateMachine / ListStateMachines ────────────

func TestDescribeStateMachine_newStateMachineHasNoRevisionId(t *testing.T) {
	// Given: a state machine never updated
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "describe-revision", passDefinition("v1"))

	// When: it is described
	described := describeSMRaw(t, srv, smARN)

	// Then: revisionId is absent, as AWS reports for the initial revision
	if _, ok := described["revisionId"]; ok {
		t.Errorf("revisionId = %v, want it absent before any update", described["revisionId"])
	}
}

func TestDeleteStateMachine_removesVersionsAndAliases(t *testing.T) {
	// Given: a state machine with a version and an alias
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "delete-cascade", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")
	aliasARN := createAlias(t, srv, "PROD", "", routing(v1, 100))

	// When: the state machine is deleted
	resp := sfnCall(t, srv, "DeleteStateMachine", map[string]any{"stateMachineArn": smARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: neither the version nor the alias survives
	d := sfnCall(t, srv, "DescribeStateMachine", map[string]any{"stateMachineArn": v1})
	defer d.Body.Close()
	helpers.AssertJSONError(t, d, "StateMachineDoesNotExist")
	a := sfnCall(t, srv, "DescribeStateMachineAlias", map[string]any{"stateMachineAliasArn": aliasARN})
	defer a.Body.Close()
	helpers.AssertJSONError(t, a, "ResourceNotFound")

	// And: a state machine recreated under the same name starts again at 1
	createSM(t, srv, "delete-cascade", passDefinition("v1"))
	if got := publishVersion(t, srv, smARN, ""); got != smARN+":1" {
		t.Errorf("recreated state machine's first version = %q, want %q", got, smARN+":1")
	}
}

func TestListStateMachines_pagination(t *testing.T) {
	// Given: three state machines
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"page-a", "page-b", "page-c"} {
		createSM(t, srv, name, passDefinition("v1"))
	}

	// When: we page through them two at a time
	var names []string
	token := ""
	pages := 0
	for {
		body := map[string]any{"maxResults": 2}
		if token != "" {
			body["nextToken"] = token
		}
		resp := sfnCall(t, srv, "ListStateMachines", body)
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			StateMachines []struct {
				Name string `json:"name"`
			} `json:"stateMachines"`
			NextToken string `json:"nextToken"`
		}
		helpers.DecodeJSON(t, resp, &out)
		resp.Body.Close()
		pages++
		for _, sm := range out.StateMachines {
			names = append(names, sm.Name)
		}
		if out.NextToken == "" || pages > 3 {
			break
		}
		token = out.NextToken
	}

	// Then: all three come back across two pages
	if pages != 2 || len(names) != 3 {
		t.Errorf("pages=%d names=%v, want 2 pages holding 3 state machines", pages, names)
	}
}

func TestListStateMachines_invalidToken(t *testing.T) {
	// Given: an empty server
	srv := helpers.NewTestServer(t)

	// When: a garbage nextToken is supplied
	resp := sfnCall(t, srv, "ListStateMachines", map[string]any{"nextToken": "garbage"})
	defer resp.Body.Close()

	// Then: InvalidToken
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidToken")
}

// ─── Tags on qualified ARNs ───────────────────────────────────────────────────

func TestTagResource_versionArnRejected(t *testing.T) {
	// Given: a published version
	srv := helpers.NewTestServer(t)
	smARN := createSM(t, srv, "tag-version", passDefinition("v1"))
	v1 := publishVersion(t, srv, smARN, "")

	// When: TagResource targets the version ARN (only state machines and
	// activities are taggable)
	resp := sfnCall(t, srv, "TagResource", map[string]any{
		"resourceArn": v1,
		"tags":        []map[string]string{{"key": "k", "value": "v"}},
	})
	defer resp.Body.Close()

	// Then: InvalidArn
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArn")
}

// ─── ValidateStateMachineDefinition ───────────────────────────────────────────

func TestValidateStateMachineDefinition_validDefinition(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: a valid definition is validated
	resp := sfnCall(t, srv, "ValidateStateMachineDefinition", map[string]any{
		"definition": passDefinition("ok"),
		"type":       "STANDARD",
	})
	defer resp.Body.Close()

	// Then: OK with no diagnostics
	helpers.AssertStatus(t, resp, http.StatusOK)
	out := decodeValidation(t, resp)
	if out.Result != "OK" || len(out.Diagnostics) != 0 || out.Truncated {
		t.Errorf("result = %+v, want OK with no diagnostics", out)
	}
}

func TestValidateStateMachineDefinition_invalidDefinition(t *testing.T) {
	cases := []struct {
		name       string
		definition string
	}{
		{name: "not JSON", definition: `{"StartAt":`},
		{name: "dangling StartAt", definition: `{"StartAt":"Nope","States":{"P":{"Type":"Pass","End":true}}}`},
		{name: "dangling Next", definition: `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"Gone"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a running server
			srv := helpers.NewTestServer(t)

			// When: an invalid definition is validated
			resp := sfnCall(t, srv, "ValidateStateMachineDefinition", map[string]any{"definition": tc.definition})
			defer resp.Body.Close()

			// Then: a 200 carrying FAIL and an ERROR diagnostic — never an
			// error response
			helpers.AssertStatus(t, resp, http.StatusOK)
			out := decodeValidation(t, resp)
			if out.Result != "FAIL" || len(out.Diagnostics) == 0 {
				t.Fatalf("result = %+v, want FAIL with a diagnostic", out)
			}
			d := out.Diagnostics[0]
			if d.Severity != "ERROR" || d.Code == "" || d.Message == "" {
				t.Errorf("diagnostic = %+v, want an ERROR with code and message", d)
			}
		})
	}
}

func TestValidateStateMachineDefinition_malformedRequest(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{name: "missing definition", body: map[string]any{}},
		{name: "unknown severity", body: map[string]any{"definition": passDefinition("x"), "severity": "INFO"}},
		{name: "unknown type", body: map[string]any{"definition": passDefinition("x"), "type": "BATCH"}},
		{name: "maxResults above 100", body: map[string]any{"definition": passDefinition("x"), "maxResults": 101}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a running server
			srv := helpers.NewTestServer(t)

			// When: the request itself breaks the API's constraints
			resp := sfnCall(t, srv, "ValidateStateMachineDefinition", tc.body)
			defer resp.Body.Close()

			// Then: ValidationException
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "ValidationException")
		})
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func publishVersion(t *testing.T, srv *helpers.TestServer, smARN, revisionID string) string {
	t.Helper()
	body := map[string]any{"stateMachineArn": smARN}
	if revisionID != "" {
		body["revisionId"] = revisionID
	}
	resp := sfnCall(t, srv, "PublishStateMachineVersion", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineVersionArn string `json:"stateMachineVersionArn"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return out.StateMachineVersionArn
}

type updateSMResult struct {
	RevisionID             string  `json:"revisionId"`
	StateMachineVersionArn string  `json:"stateMachineVersionArn"`
	UpdateDate             float64 `json:"updateDate"`
}

func updateSM(t *testing.T, srv *helpers.TestServer, body map[string]any) updateSMResult {
	t.Helper()
	resp := sfnCall(t, srv, "UpdateStateMachine", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out updateSMResult
	helpers.DecodeJSON(t, resp, &out)
	return out
}

func describeSMRaw(t *testing.T, srv *helpers.TestServer, arn string) map[string]any {
	t.Helper()
	resp := sfnCall(t, srv, "DescribeStateMachine", map[string]any{"stateMachineArn": arn})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	return out
}

func routing(versionARN string, weight int) []map[string]any {
	return []map[string]any{{"stateMachineVersionArn": versionARN, "weight": weight}}
}

func createAlias(t *testing.T, srv *helpers.TestServer, name, description string, routingConfig []map[string]any) string {
	t.Helper()
	body := map[string]any{"name": name, "routingConfiguration": routingConfig}
	if description != "" {
		body["description"] = description
	}
	resp := sfnCall(t, srv, "CreateStateMachineAlias", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StateMachineAliasArn string `json:"stateMachineAliasArn"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return out.StateMachineAliasArn
}

type versionsPage struct {
	Versions []struct {
		Arn string `json:"stateMachineVersionArn"`
	} `json:"stateMachineVersions"`
	NextToken string `json:"nextToken"`
}

func listVersionsPage(t *testing.T, srv *helpers.TestServer, smARN string, maxResults int, token string) versionsPage {
	t.Helper()
	body := map[string]any{"stateMachineArn": smARN}
	if maxResults > 0 {
		body["maxResults"] = maxResults
	}
	if token != "" {
		body["nextToken"] = token
	}
	resp := sfnCall(t, srv, "ListStateMachineVersions", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out versionsPage
	helpers.DecodeJSON(t, resp, &out)
	return out
}

func listVersionARNs(t *testing.T, srv *helpers.TestServer, smARN string, maxResults int) []string {
	t.Helper()
	page := listVersionsPage(t, srv, smARN, maxResults, "")
	arns := make([]string, 0, len(page.Versions))
	for _, v := range page.Versions {
		arns = append(arns, v.Arn)
	}
	return arns
}

type aliasesPage struct {
	Aliases []struct {
		Arn string `json:"stateMachineAliasArn"`
	} `json:"stateMachineAliases"`
	NextToken string `json:"nextToken"`
}

func listAliasesPage(t *testing.T, srv *helpers.TestServer, arn string, maxResults int, token string) aliasesPage {
	t.Helper()
	body := map[string]any{"stateMachineArn": arn}
	if maxResults > 0 {
		body["maxResults"] = maxResults
	}
	if token != "" {
		body["nextToken"] = token
	}
	resp := sfnCall(t, srv, "ListStateMachineAliases", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out aliasesPage
	helpers.DecodeJSON(t, resp, &out)
	return out
}

type versionedExecution struct {
	StateMachineArn        string `json:"stateMachineArn"`
	StateMachineVersionArn string `json:"stateMachineVersionArn"`
	StateMachineAliasArn   string `json:"stateMachineAliasArn"`
	Output                 string `json:"output"`
}

func describeExecVersioned(t *testing.T, srv *helpers.TestServer, execARN string) versionedExecution {
	t.Helper()
	resp := sfnCall(t, srv, "DescribeExecution", map[string]any{"executionArn": execARN})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out versionedExecution
	helpers.DecodeJSON(t, resp, &out)
	return out
}

func listExecutionARNs(t *testing.T, srv *helpers.TestServer, arn string) []string {
	t.Helper()
	resp := sfnCall(t, srv, "ListExecutions", map[string]any{"stateMachineArn": arn})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Executions []struct {
			ExecutionArn string `json:"executionArn"`
		} `json:"executions"`
	}
	helpers.DecodeJSON(t, resp, &out)
	arns := make([]string, 0, len(out.Executions))
	for _, e := range out.Executions {
		arns = append(arns, e.ExecutionArn)
	}
	return arns
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

type validationResult struct {
	Result      string `json:"result"`
	Truncated   bool   `json:"truncated"`
	Diagnostics []struct {
		Severity string `json:"severity"`
		Code     string `json:"code"`
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"diagnostics"`
}

func decodeValidation(t *testing.T, resp *http.Response) validationResult {
	t.Helper()
	var out validationResult
	helpers.DecodeJSON(t, resp, &out)
	return out
}
