// selections_golden_test.go pins the wire bytes of the four backup-selection
// operations (#2308). Plan and selection ids are UUIDs and are normalised out
// by normalisePlanIDs, which matches any UUID.
//
// Record: go test ./tests/integration/backup/ -run TestGolden -record
package backup_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func goldenSelection(t *testing.T, srv *helpers.TestServer) (planID, selectionID string) {
	t.Helper()
	planID, _ = createPlan(t, srv, "golden-plan")["BackupPlanId"].(string)
	body := createSelection(t, srv, planID, map[string]any{
		"SelectionName": "golden-selection",
		"IamRoleArn":    testRoleArn,
		"Resources":     []string{"arn:aws:dynamodb:us-east-1:000000000000:table/golden"},
		"ListOfTags": []map[string]any{
			{"ConditionType": "STRINGEQUALS", "ConditionKey": "backup", "ConditionValue": "true"},
		},
	})
	selectionID, _ = body["SelectionId"].(string)
	return planID, selectionID
}

func TestGolden_CreateBackupSelection(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	planID, _ := createPlan(t, srv, "golden-plan")["BackupPlanId"].(string)

	resp := backupDo(t, srv, http.MethodPut, selectionsPath(planID), defaultRegion, map[string]any{
		"BackupSelection": selectionDoc("golden-selection"),
	})
	helpers.GoldenTest(t, backupGoldenDir, "CreateBackupSelection", resp, normalisePlanIDs)
}

func TestGolden_GetBackupSelection(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	planID, selectionID := goldenSelection(t, srv)

	resp := backupDo(t, srv, http.MethodGet, selectionPath(planID, selectionID), defaultRegion, nil)
	helpers.GoldenTest(t, backupGoldenDir, "GetBackupSelection", resp, normalisePlanIDs)
}

func TestGolden_ListBackupSelections(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	planID, _ := goldenSelection(t, srv)

	resp := backupDo(t, srv, http.MethodGet, selectionsPath(planID), defaultRegion, nil)
	helpers.GoldenTest(t, backupGoldenDir, "ListBackupSelections", resp, normalisePlanIDs)
}

func TestGolden_DeleteBackupSelection(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	planID, selectionID := goldenSelection(t, srv)

	resp := backupDo(t, srv, http.MethodDelete, selectionPath(planID, selectionID), defaultRegion, nil)
	helpers.GoldenTest(t, backupGoldenDir, "DeleteBackupSelection", resp, normalisePlanIDs)
}
