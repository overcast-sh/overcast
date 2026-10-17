//go:build dev

package backup

import "github.com/overcast-sh/overcast/internal/capabilities"

// The nine vault and plan rows below were StatusInert before #864 flipped them
// to StatusWIP, and #815 makes them reachable. Neither of those grades is the
// honest one now:
//
//   - StatusInert says an operation "is intentionally accepted but has no side
//     effects". That describes the *tier* — router.TierInert records that
//     nothing runs behind Backup's control plane — not these operations, which
//     really do create, store, list and delete vaults and plans.
//   - StatusPartial is what a served operation gets when "core behaviour works;
//     edge cases or optional fields may differ", which is exactly the state of
//     every operation whose modeled input or output carries a member Overcast
//     drops. Each note below names the member rather than saying "partial".
//
// The three Deletes serve their modeled shapes in full and are
// StatusSupported. Each models an InvalidRequestException for a resource that
// still has contents — recovery points, backup selections, an S3 access point
// to tear down. DeleteBackupPlan produces it while selections remain (#2308);
// the vault and access point Deletes cannot, because Overcast creates no
// recovery points or S3 access points for them to refuse over.
//
// The six access point rows (#1467) are Partial for one reason throughout: an
// access point's vault, resource and S3 access point are all properties of the
// recovery point it was created against, and Overcast runs no backup jobs, so
// there is no recovery point for any of them to be read from. See
// handler_access_points.go.
func init() {
	capabilities.Default.RegisterForService(serviceName,
		capabilities.Capability{Operation: "CreateBackupSelection", Status: capabilities.StatusPartial,
			Notes: "PUT /backup/plans/{BackupPlanId}/selections (#2308); `CreatorRequestId` is idempotent per plan (a replay with a different selection is AlreadyExistsException). `Resources`/`NotResources`/`ListOfTags`/`Conditions` are stored and echoed, never evaluated or validated against real resources or the IAM role; `LimitExceededException` is not enforced and `AlreadyExistsException` carries no `Arn`/`CreatorRequestId` members"},
		capabilities.Capability{Operation: "GetBackupSelection", Status: capabilities.StatusSupported,
			Notes: "GET /backup/plans/{BackupPlanId}/selections/{SelectionId} (#2308); ResourceNotFoundException for an unknown plan or selection"},
		capabilities.Capability{Operation: "ListBackupSelections", Status: capabilities.StatusSupported,
			Notes: "GET /backup/plans/{BackupPlanId}/selections (#2308); `maxResults` and `nextToken` query params; ordered by name then id"},
		capabilities.Capability{Operation: "DeleteBackupSelection", Status: capabilities.StatusSupported,
			Notes: "DELETE /backup/plans/{BackupPlanId}/selections/{SelectionId} (#2308); empty 200, the modeled Unit output"},
		capabilities.Capability{Operation: "CreateBackupVault", Status: capabilities.StatusSupported,
			Notes: "PUT /backup-vaults/{BackupVaultName}; `BackupVaultTags` is applied at create time, in the same store write as the vault (#1195)"},
		capabilities.Capability{Operation: "DeleteBackupVault", Status: capabilities.StatusSupported,
			Notes: "DELETE /backup-vaults/{BackupVaultName}; empty 200, the modeled Unit output"},
		capabilities.Capability{Operation: "DescribeBackupVault", Status: capabilities.StatusPartial,
			Notes: "GET /backup-vaults/{BackupVaultName}; honours `backupVaultAccountId`. Vault lock and MPA approval are not emulated, so `Locked` is always false and the retention members are absent"},
		capabilities.Capability{Operation: "ListBackupVaults", Status: capabilities.StatusPartial,
			Notes: "GET /backup-vaults; `vaultType`, `shared`, `maxResults` and `nextToken` query params. Every vault is a standard unshared vault, so `shared=true` lists none"},
		capabilities.Capability{Operation: "CreateBackupPlan", Status: capabilities.StatusPartial,
			Notes: "PUT /backup/plans; `BackupPlanTags` is applied at create time (#1195); `AdvancedBackupSettings` is not stored or returned"},
		capabilities.Capability{Operation: "DeleteBackupPlan", Status: capabilities.StatusSupported,
			Notes: "DELETE /backup/plans/{BackupPlanId}; returns the modeled id, ARN, VersionId and DeletionDate. InvalidRequestException while the plan still has backup selections (#2308)"},
		capabilities.Capability{Operation: "GetBackupPlan", Status: capabilities.StatusPartial,
			Notes: "GET /backup/plans/{BackupPlanId}; only the current version is kept, so any other `versionId` is not found, and `MaxScheduledRunsPreview` yields no `ScheduledRunsPreview`"},
		capabilities.Capability{Operation: "ListBackupPlans", Status: capabilities.StatusPartial,
			Notes: "GET /backup/plans; `maxResults` and `nextToken` query params. Plans are deleted outright rather than tombstoned, so `includeDeleted` adds nothing"},
		capabilities.Capability{Operation: "UpdateBackupPlan", Status: capabilities.StatusPartial,
			Notes: "POST /backup/plans/{BackupPlanId}; mints a new VersionId per update. Prior versions are not retained and `AdvancedBackupSettings` is not stored"},
		capabilities.Capability{Operation: "CreateBackupAccessPoint", Status: capabilities.StatusPartial,
			Notes: "PUT /backup-access-point/create (#1467); `Tags` is applied at create time. `AccessPointPolicy` is accepted and dropped — no access policy is enforced behind this control plane"},
		capabilities.Capability{Operation: "DeleteBackupAccessPoint", Status: capabilities.StatusSupported,
			Notes: "DELETE /backup-access-point/delete/{AccessPointArn}; empty 204, the modeled Unit output"},
		capabilities.Capability{Operation: "DescribeBackupAccessPoint", Status: capabilities.StatusPartial,
			Notes: "GET /backup-access-point/{AccessPointArn}; returns the stored `AccessPointMetadata` verbatim. No recovery point exists to read `BackupVaultName`, `BackupVaultArn`, `ResourceArn` or `ResourceType` from, and no S3 access point is created, so those members and the metadata's `S3AccessPointArn`/`S3AccessPointAlias` are absent rather than invented"},
		capabilities.Capability{Operation: "ListBackupAccessPoints", Status: capabilities.StatusPartial,
			Notes: "GET /backup-access-point; `MaxResults` and `NextToken` query params. Members read off a recovery point are absent, as on DescribeBackupAccessPoint"},
		capabilities.Capability{Operation: "ListBackupAccessPointsByRecoveryPoint", Status: capabilities.StatusPartial,
			Notes: "POST /backup-access-point/recovery-point/{RecoveryPointArn}; filters on the ARN each access point was created against. No backup job runs, so no recovery point Overcast produced can ever be named here and the list is empty"},
		capabilities.Capability{Operation: "ListBackupAccessPointsByResource", Status: capabilities.StatusPartial,
			Notes: "POST /backup-access-point/resource/{ResourceArn}; an access point's `ResourceArn` is a property of its recovery point, which Overcast never creates, so nothing is ever stored for this to match and the list is empty"},
		capabilities.Capability{Operation: "TagResource", Status: capabilities.StatusSupported,
			Notes: "POST /tags/{ResourceArn}, shared with Pipes/EKS/Scheduler/AppConfig/API Gateway's ARN-dispatched path (#1195). Tags are stored inline on the vault, plan or access point record, so they die with it"},
		capabilities.Capability{Operation: "ListTags", Status: capabilities.StatusSupported,
			Notes: "GET /tags/{ResourceArn} (#1195)"},
		capabilities.Capability{Operation: "UntagResource", Status: capabilities.StatusSupported,
			Notes: "POST /untag/{ResourceArn} — Backup's own path, not another member of the shared /tags dispatcher (#1195)"},
	)
}
