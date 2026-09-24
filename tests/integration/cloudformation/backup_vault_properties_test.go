package cloudformation_test

// backup_vault_properties_test.go — #1760: AccessPolicy, Notifications and
// LockConfiguration have no PutBackupVaultAccessPolicy/
// PutBackupVaultNotifications/PutBackupVaultLockConfiguration counterpart
// anywhere in internal/services/backup, so they are reported through
// noteUnconsumedProperties rather than silently dropped or invented.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// TestCreateStack_BackupVault_unsupportedPropertiesAreReported asserts that a
// vault using AccessPolicy, Notifications and LockConfiguration still
// deploys (the emulator does not enforce any of the three), and that each
// property is named in the resource's ResourceStatusReason rather than
// silently dropped.
func TestCreateStack_BackupVault_unsupportedPropertiesAreReported(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "backup-vault-props-stack"
	const template = `{
  "Resources": {
    "Vault": {
      "Type": "AWS::Backup::BackupVault",
      "Properties": {
        "BackupVaultName": "cfn-vault-props",
        "AccessPolicy": {"Version": "2012-10-17", "Statement": []},
        "Notifications": {"BackupVaultEvents": ["BACKUP_JOB_COMPLETED"], "SNSTopicArn": "arn:aws:sns:us-east-1:000000000000:topic"},
        "LockConfiguration": {"MinRetentionDays": 7}
      }
    }
  }
}`

	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	status, vault := backupGet(t, srv, "/backup-vaults/cfn-vault-props")
	if status != http.StatusOK {
		t.Fatalf("DescribeBackupVault: HTTP %d: %#v", status, vault)
	}

	reasons := describeStackResourceReasons(t, srv, stackName)
	for _, prop := range []string{"AccessPolicy", "Notifications", "LockConfiguration"} {
		if !strings.Contains(reasons, prop) {
			t.Errorf("expected the vault's ResourceStatusReason to name the unapplied %s, got: %s", prop, reasons)
		}
	}
}
