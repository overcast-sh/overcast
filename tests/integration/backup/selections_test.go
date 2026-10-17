// selections_test.go covers the four backup-selection operations (#2308),
// addressed at the bindings the pinned model gives them:
//
//	PUT    /backup/plans/{BackupPlanId}/selections
//	GET    /backup/plans/{BackupPlanId}/selections
//	GET    /backup/plans/{BackupPlanId}/selections/{SelectionId}
//	DELETE /backup/plans/{BackupPlanId}/selections/{SelectionId}
package backup_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const testRoleArn = "arn:aws:iam::000000000000:role/service-role/AWSBackupDefaultServiceRole"

func selectionsPath(planID string) string { return pathPlans + "/" + planID + "/selections" }

func selectionPath(planID, selectionID string) string {
	return selectionsPath(planID) + "/" + selectionID
}

func selectionDoc(name string) map[string]any {
	return map[string]any{
		"SelectionName": name,
		"IamRoleArn":    testRoleArn,
		"Resources":     []string{"*"},
	}
}

func createSelection(t *testing.T, srv *helpers.TestServer, planID string, selection map[string]any) map[string]any {
	t.Helper()
	resp := backupDo(t, srv, http.MethodPut, selectionsPath(planID), defaultRegion, map[string]any{
		"BackupSelection": selection,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	return decodeMap(t, resp)
}

func TestCreateBackupSelection_success(t *testing.T) {
	// Given: a plan
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	// When: CreateBackupSelection is called
	body := createSelection(t, srv, planID, selectionDoc("sel-a"))

	// Then: the modeled CreateBackupSelectionOutput comes back
	if got := body["BackupPlanId"]; got != planID {
		t.Errorf("BackupPlanId = %v, want %s", got, planID)
	}
	if id, _ := body["SelectionId"].(string); id == "" {
		t.Errorf("SelectionId = %#v, want a minted id", body["SelectionId"])
	}
	if _, ok := body["CreationDate"].(float64); !ok {
		t.Errorf("CreationDate = %#v, want a JSON number", body["CreationDate"])
	}
}

func TestCreateBackupSelection_unknownPlan(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := backupDo(t, srv, http.MethodPut, selectionsPath("no-such-plan"), defaultRegion, map[string]any{
		"BackupSelection": selectionDoc("sel-a"),
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestCreateBackupSelection_planIsRegionScoped(t *testing.T) {
	// Given: a plan in us-east-1
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	// When: a selection is created against it from another region
	resp := backupDo(t, srv, http.MethodPut, selectionsPath(planID), "eu-west-1", map[string]any{
		"BackupSelection": selectionDoc("sel-a"),
	})
	defer resp.Body.Close()

	// Then: the plan does not exist there
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestCreateBackupSelection_missingMembers(t *testing.T) {
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	cases := map[string]map[string]any{
		"no BackupSelection": {},
		"no SelectionName":   {"BackupSelection": map[string]any{"IamRoleArn": testRoleArn, "Resources": []string{"*"}}},
		"no IamRoleArn":      {"BackupSelection": map[string]any{"SelectionName": "sel-a", "Resources": []string{"*"}}},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp := backupDo(t, srv, http.MethodPut, selectionsPath(planID), defaultRegion, body)
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "MissingParameterValueException")
		})
	}
}

func TestCreateBackupSelection_invalidValues(t *testing.T) {
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	cases := map[string]map[string]any{
		"name outside the pattern": {"SelectionName": "has space", "IamRoleArn": testRoleArn},
		"name too long": {"SelectionName": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"IamRoleArn": testRoleArn},
		"tag condition type not in the enum": {"SelectionName": "sel-a", "IamRoleArn": testRoleArn,
			"ListOfTags": []map[string]any{{"ConditionType": "STRINGLIKE", "ConditionKey": "k", "ConditionValue": "v"}}},
		"tag condition missing its key": {"SelectionName": "sel-a", "IamRoleArn": testRoleArn,
			"ListOfTags": []map[string]any{{"ConditionType": "STRINGEQUALS", "ConditionValue": "v"}}},
	}
	for name, sel := range cases {
		t.Run(name, func(t *testing.T) {
			resp := backupDo(t, srv, http.MethodPut, selectionsPath(planID), defaultRegion,
				map[string]any{"BackupSelection": sel})
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			if name == "tag condition missing its key" {
				helpers.AssertJSONError(t, resp, "MissingParameterValueException")
				return
			}
			helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
		})
	}
}

func TestCreateBackupSelection_idempotentOnCreatorRequestId(t *testing.T) {
	// Given: a selection created with a CreatorRequestId
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	create := func(sel map[string]any) *http.Response {
		return backupDo(t, srv, http.MethodPut, selectionsPath(planID), defaultRegion, map[string]any{
			"BackupSelection":  sel,
			"CreatorRequestId": "token-1",
		})
	}
	first := create(selectionDoc("sel-a"))
	defer first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)
	firstBody := decodeMap(t, first)

	// When: the identical request is retried
	retry := create(selectionDoc("sel-a"))
	defer retry.Body.Close()

	// Then: it answers the original selection rather than minting a second
	helpers.AssertStatus(t, retry, http.StatusOK)
	if got := decodeMap(t, retry); !reflect.DeepEqual(got, firstBody) {
		t.Errorf("retry = %#v, want %#v", got, firstBody)
	}
	list := backupDo(t, srv, http.MethodGet, selectionsPath(planID), defaultRegion, nil)
	defer list.Body.Close()
	if n := len(decodeMap(t, list)["BackupSelectionsList"].([]any)); n != 1 {
		t.Errorf("listed %d selections, want 1", n)
	}

	// And: the same token carrying a different selection is a conflict
	conflict := create(selectionDoc("sel-b"))
	defer conflict.Body.Close()
	helpers.AssertStatus(t, conflict, http.StatusBadRequest)
	helpers.AssertJSONError(t, conflict, "AlreadyExistsException")
}

func TestGetBackupSelection_roundTripsEveryMember(t *testing.T) {
	// Given: a selection naming every optional member
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	sel := map[string]any{
		"SelectionName": "full.sel_1-x",
		"IamRoleArn":    testRoleArn,
		"Resources":     []string{"arn:aws:dynamodb:us-east-1:000000000000:table/a"},
		"NotResources":  []string{"arn:aws:dynamodb:us-east-1:000000000000:table/b"},
		"ListOfTags": []map[string]any{
			{"ConditionType": "STRINGEQUALS", "ConditionKey": "backup", "ConditionValue": "true"},
		},
		"Conditions": map[string]any{
			"StringEquals":    []map[string]any{{"ConditionKey": "aws:ResourceTag/env", "ConditionValue": "prod"}},
			"StringNotEquals": []map[string]any{{"ConditionKey": "aws:ResourceTag/skip", "ConditionValue": "yes"}},
			"StringLike":      []map[string]any{{"ConditionKey": "aws:ResourceTag/team", "ConditionValue": "a*"}},
			"StringNotLike":   []map[string]any{{"ConditionKey": "aws:ResourceTag/tier", "ConditionValue": "z*"}},
		},
	}
	created := createSelection(t, srv, planID, sel)
	id := created["SelectionId"].(string)

	// When: it is read back
	resp := backupDo(t, srv, http.MethodGet, selectionPath(planID, id), defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the document and the identifying members come back unchanged
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := decodeMap(t, resp)
	if body["SelectionId"] != id || body["BackupPlanId"] != planID {
		t.Errorf("ids = %v / %v, want %s / %s", body["SelectionId"], body["BackupPlanId"], id, planID)
	}
	if body["CreationDate"] != created["CreationDate"] {
		t.Errorf("CreationDate = %v, want %v", body["CreationDate"], created["CreationDate"])
	}
	got, _ := body["BackupSelection"].(map[string]any)
	want := map[string]any{}
	for k, v := range sel {
		want[k] = toJSONValue(t, v)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BackupSelection = %#v\nwant %#v", got, want)
	}
}

func TestGetBackupSelection_carriesCreatorRequestId(t *testing.T) {
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	resp := backupDo(t, srv, http.MethodPut, selectionsPath(planID), defaultRegion, map[string]any{
		"BackupSelection": selectionDoc("sel-a"), "CreatorRequestId": "tok-9",
	})
	defer resp.Body.Close()
	id := decodeMap(t, resp)["SelectionId"].(string)

	get := backupDo(t, srv, http.MethodGet, selectionPath(planID, id), defaultRegion, nil)
	defer get.Body.Close()

	if got := decodeMap(t, get)["CreatorRequestId"]; got != "tok-9" {
		t.Errorf("CreatorRequestId = %v, want tok-9", got)
	}
}

func TestGetBackupSelection_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	t.Run("unknown selection", func(t *testing.T) {
		resp := backupDo(t, srv, http.MethodGet, selectionPath(planID, "11111111-1111-1111-1111-111111111111"), defaultRegion, nil)
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
	})
	t.Run("unknown plan", func(t *testing.T) {
		resp := backupDo(t, srv, http.MethodGet, selectionPath("no-such-plan", "x"), defaultRegion, nil)
		defer resp.Body.Close()
		helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
	})
	t.Run("selection of another plan", func(t *testing.T) {
		other := createPlan(t, srv, "plan-b")["BackupPlanId"].(string)
		id := createSelection(t, srv, other, selectionDoc("sel-a"))["SelectionId"].(string)
		resp := backupDo(t, srv, http.MethodGet, selectionPath(planID, id), defaultRegion, nil)
		defer resp.Body.Close()
		helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
	})
}

func TestListBackupSelections(t *testing.T) {
	// Given: two plans, the first with two selections
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	other := createPlan(t, srv, "plan-b")["BackupPlanId"].(string)
	idB := createSelection(t, srv, planID, selectionDoc("sel-b"))["SelectionId"].(string)
	idA := createSelection(t, srv, planID, selectionDoc("sel-a"))["SelectionId"].(string)
	createSelection(t, srv, other, selectionDoc("sel-other"))

	// When: the first plan's selections are listed
	resp := backupDo(t, srv, http.MethodGet, selectionsPath(planID), defaultRegion, nil)
	defer resp.Body.Close()

	// Then: only its own come back, as BackupSelectionsListMembers
	helpers.AssertStatus(t, resp, http.StatusOK)
	list := decodeMap(t, resp)["BackupSelectionsList"].([]any)
	if len(list) != 2 {
		t.Fatalf("listed %d selections, want 2: %#v", len(list), list)
	}
	first := list[0].(map[string]any)
	if first["SelectionName"] != "sel-a" || first["SelectionId"] != idA || first["BackupPlanId"] != planID ||
		first["IamRoleArn"] != testRoleArn {
		t.Errorf("first member = %#v", first)
	}
	if _, ok := first["CreationDate"].(float64); !ok {
		t.Errorf("CreationDate = %#v, want a JSON number", first["CreationDate"])
	}
	if list[1].(map[string]any)["SelectionId"] != idB {
		t.Errorf("second member = %#v", list[1])
	}
	if _, has := first["Resources"]; has {
		t.Errorf("a list member carries the selection document: %#v", first)
	}
}

func TestListBackupSelections_emptyAndUnknownPlan(t *testing.T) {
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	resp := backupDo(t, srv, http.MethodGet, selectionsPath(planID), defaultRegion, nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	list, ok := decodeMap(t, resp)["BackupSelectionsList"].([]any)
	if !ok || len(list) != 0 {
		t.Errorf("BackupSelectionsList = %#v, want an empty array", list)
	}

	missing := backupDo(t, srv, http.MethodGet, selectionsPath("no-such-plan"), defaultRegion, nil)
	defer missing.Body.Close()
	helpers.AssertJSONError(t, missing, "ResourceNotFoundException")
}

func TestListBackupSelections_pagination(t *testing.T) {
	// Given: three selections
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	for _, n := range []string{"sel-1", "sel-2", "sel-3"} {
		createSelection(t, srv, planID, selectionDoc(n))
	}

	// When: they are listed two at a time, following NextToken
	var names []string
	token := ""
	for pages := 0; pages < 5; pages++ {
		path := selectionsPath(planID) + "?maxResults=2"
		if token != "" {
			path += "&nextToken=" + token
		}
		resp := backupDo(t, srv, http.MethodGet, path, defaultRegion, nil)
		helpers.AssertStatus(t, resp, http.StatusOK)
		body := decodeMap(t, resp)
		resp.Body.Close()
		for _, m := range body["BackupSelectionsList"].([]any) {
			names = append(names, m.(map[string]any)["SelectionName"].(string))
		}
		next, _ := body["NextToken"].(string)
		if next == "" {
			break
		}
		token = next
	}

	// Then: every selection was seen exactly once
	if !reflect.DeepEqual(names, []string{"sel-1", "sel-2", "sel-3"}) {
		t.Errorf("paged names = %v", names)
	}
}

func TestListBackupSelections_invalidPaging(t *testing.T) {
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	for _, q := range []string{"?maxResults=0", "?maxResults=1001", "?nextToken=not-a-token"} {
		resp := backupDo(t, srv, http.MethodGet, selectionsPath(planID)+q, defaultRegion, nil)
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
		resp.Body.Close()
	}
}

func TestDeleteBackupSelection(t *testing.T) {
	// Given: a selection
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	id := createSelection(t, srv, planID, selectionDoc("sel-a"))["SelectionId"].(string)

	// When: it is deleted
	resp := backupDo(t, srv, http.MethodDelete, selectionPath(planID, id), defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the modeled empty 200, and the selection is gone
	helpers.AssertStatus(t, resp, http.StatusOK)
	get := backupDo(t, srv, http.MethodGet, selectionPath(planID, id), defaultRegion, nil)
	defer get.Body.Close()
	helpers.AssertJSONError(t, get, "ResourceNotFoundException")

	// And: deleting it again is not found
	again := backupDo(t, srv, http.MethodDelete, selectionPath(planID, id), defaultRegion, nil)
	defer again.Body.Close()
	helpers.AssertStatus(t, again, http.StatusBadRequest)
	helpers.AssertJSONError(t, again, "ResourceNotFoundException")
}

func TestDeleteBackupSelection_unknownPlan(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := backupDo(t, srv, http.MethodDelete, selectionPath("no-such-plan", "x"), defaultRegion, nil)
	defer resp.Body.Close()

	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDeleteBackupPlan_refusedWhileSelectionsExist(t *testing.T) {
	// Given: a plan with a selection
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)
	selID := createSelection(t, srv, planID, selectionDoc("sel-a"))["SelectionId"].(string)

	// When: the plan is deleted
	resp := backupDo(t, srv, http.MethodDelete, pathPlans+"/"+planID, defaultRegion, nil)
	defer resp.Body.Close()

	// Then: AWS refuses until the selections are gone
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidRequestException")

	// And: once the selection is deleted the plan can go
	del := backupDo(t, srv, http.MethodDelete, selectionPath(planID, selID), defaultRegion, nil)
	del.Body.Close()
	ok := backupDo(t, srv, http.MethodDelete, pathPlans+"/"+planID, defaultRegion, nil)
	defer ok.Body.Close()
	helpers.AssertStatus(t, ok, http.StatusOK)
}

// toJSONValue normalises a Go literal to what json.Unmarshal into any yields.
func toJSONValue(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// The AWS CLI and botocore-generated SDKs bind the collection with a trailing
// slash (compat run: PUT/GET /backup/plans/{id}/selections/).
func TestBackupSelections_trailingSlashCollection(t *testing.T) {
	srv := helpers.NewTestServer(t)
	planID := createPlan(t, srv, "plan-a")["BackupPlanId"].(string)

	put := backupDo(t, srv, http.MethodPut, selectionsPath(planID)+"/", defaultRegion, map[string]any{
		"BackupSelection": selectionDoc("sel-a"),
	})
	defer put.Body.Close()
	helpers.AssertStatus(t, put, http.StatusOK)

	list := backupDo(t, srv, http.MethodGet, selectionsPath(planID)+"/", defaultRegion, nil)
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	if n := len(decodeMap(t, list)["BackupSelectionsList"].([]any)); n != 1 {
		t.Errorf("listed %d selections, want 1", n)
	}
}
