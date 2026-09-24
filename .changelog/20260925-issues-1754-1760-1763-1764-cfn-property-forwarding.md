* [cloudformation/cloudtrail] AWS::CloudTrail::Trail's `IsLogging` now reaches `StartLogging`/`StopLogging` on create and update.
* [cloudformation/backup] BackupVault's `AccessPolicy`, `Notifications` and `LockConfiguration` are now reported as unapplied instead of dropped silently.
~ [cloudformation/backup] AWS::Backup::BackupPlan updates now apply in place via `UpdateBackupPlan` instead of replacing the plan.
  a changed rule set keeps the same plan ID and ARN, matching the schema's `BackupPlan: Update requires: No interruption`.
* [cloudformation/appregistry] Application tags merge with the stack's tags on create and reconcile via `TagResource`/`UntagResource` on update.
+! [cloudformation/appregistry] AttributeGroup and AttributeGroupAssociation are now provisioned as real resources instead of an unbacked stub.
  migration: a template using either now creates a real resource and can fail if `Application`/`AttributeGroup` is invalid, previously always a no-op success.
~ [cloudformation/ses] AWS::SES::Template updates now apply via `UpdateTemplate` instead of replacing the template.
+! [cloudformation/ses] AWS::SES::EmailIdentity is now provisioned as a real resource instead of an unbacked stub.
  migration: a template using it now creates a real SES v2 identity and can fail if `EmailIdentity` is missing, where it previously always reported success with nothing behind it.
