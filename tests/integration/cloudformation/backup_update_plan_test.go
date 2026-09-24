package cloudformation_test

// backup_update_plan_test.go — #1760: backupBackupPlanHandler.Update used to
// force replacement for any BackupPlan structure change, even though the
// CloudFormation resource spec marks BackupPlan "Update requires: No
// interruption" and Backup's own UpdateBackupPlan (POST
// /backup/plans/{BackupPlanId}) exists for exactly this. Covers that a rule
// change applies in place — same physical ID/ARN, new rule visible through
// GetBackupPlan — rather than replacing the plan.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func backupPlanTemplate(ruleName, vaultName string) string {
	return `{
  "Resources": {
    "Vault": {
      "Type": "AWS::Backup::BackupVault",
      "Properties": {"BackupVaultName": "` + vaultName + `"}
    },
    "Plan": {
      "Type": "AWS::Backup::BackupPlan",
      "Properties": {
        "BackupPlan": {
          "BackupPlanName": "cfn-update-plan",
          "Rules": [{"RuleName": "` + ruleName + `", "TargetBackupVaultName": "` + vaultName + `"}]
        }
      }
    }
  },
  "Outputs": {
    "PlanArn": {"Value": {"Fn::GetAtt": ["Plan", "BackupPlanArn"]}}
  }
}`
}

func getBackupPlanRuleNames(t *testing.T, srv *helpers.TestServer, planID string) []string {
	t.Helper()
	status, body := backupGet(t, srv, "/backup/plans/"+planID+"/")
	if status != http.StatusOK {
		t.Fatalf("GetBackupPlan %s: HTTP %d: %#v", planID, status, body)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal GetBackupPlan response: %v", err)
	}
	var parsed struct {
		BackupPlan struct {
			Rules []struct {
				RuleName string `json:"RuleName"`
			} `json:"Rules"`
		} `json:"BackupPlan"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("decode GetBackupPlan response: %v", err)
	}
	names := make([]string, 0, len(parsed.BackupPlan.Rules))
	for _, r := range parsed.BackupPlan.Rules {
		names = append(names, r.RuleName)
	}
	return names
}

// TestUpdateStack_BackupPlan_rulesChangeAppliesInPlace asserts that changing
// a backup plan's rules on a stack update calls UpdateBackupPlan rather than
// replacing the plan: the physical ID (ARN) is unchanged, and GetBackupPlan
// reports the new rule.
func TestUpdateStack_BackupPlan_rulesChangeAppliesInPlace(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "backup-update-plan-stack"
	const vaultName = "cfn-update-plan-vault"

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {backupPlanTemplate("daily", vaultName)},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	resourceIDs := describeStackResourceIDs(t, srv, stackName)
	originalPlanArn := resourceIDs["Plan"]
	planID := originalPlanArn[strings.LastIndex(originalPlanArn, ":")+1:]

	if names := getBackupPlanRuleNames(t, srv, planID); len(names) != 1 || names[0] != "daily" {
		t.Fatalf("initial plan rules = %v, want [daily]", names)
	}

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {backupPlanTemplate("weekly", vaultName)},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	newResourceIDs := describeStackResourceIDs(t, srv, stackName)
	if newResourceIDs["Plan"] != originalPlanArn {
		t.Fatalf("Plan physical ID changed from %q to %q on a rules-only update; plan should not have been replaced", originalPlanArn, newResourceIDs["Plan"])
	}

	if names := getBackupPlanRuleNames(t, srv, planID); len(names) != 1 || names[0] != "weekly" {
		t.Fatalf("plan rules after update = %v, want [weekly]", names)
	}
}
