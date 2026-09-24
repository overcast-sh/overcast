package rds

// handler_instance_echo_properties_test.go — #2133: DBInstance properties
// that arrived from CreateDBInstance/ModifyDBInstance (and CloudFormation)
// but were never stored, so DescribeDBInstances never echoed them back —
// StorageEncrypted, KmsKeyId, DeletionProtection, BackupRetentionPeriod,
// PreferredBackupWindow, PreferredMaintenanceWindow, AutoMinorVersionUpgrade,
// Iops, AvailabilityZone, EnableIAMDatabaseAuthentication,
// CACertificateIdentifier, MonitoringInterval, PerformanceInsightsEnabled,
// EnableCloudwatchLogsExports and CopyTagsToSnapshot.

import (
	"context"
	"strings"
	"testing"
)

func seedInstance(t *testing.T, h *Handler, id string, req *createDBInstanceReq) {
	t.Helper()
	if req.DBInstanceIdentifier == "" {
		req.DBInstanceIdentifier = id
	}
	if req.Engine == "" {
		req.Engine = "mysql"
	}
	if req.MasterUsername == "" {
		req.MasterUsername = "admin"
	}
	if req.MasterUserPassword == "" {
		req.MasterUserPassword = "password123"
	}
	if _, aerr := h.createDBInstanceTyped(context.Background(), req); aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}
}

// Every one of #2133's properties must survive the round trip from
// CreateDBInstance into the stored record and back out through
// DescribeDBInstances/CreateDBInstance's own response.
func TestCreateDBInstance_echoesEveryProperty(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()

	resp, aerr := h.createDBInstanceTyped(ctx, &createDBInstanceReq{
		DBInstanceIdentifier:            "echo",
		Engine:                          "mysql",
		MasterUsername:                  "admin",
		MasterUserPassword:              "password123",
		StorageEncrypted:                boolPtr(true),
		KmsKeyId:                        "arn:aws:kms:us-east-1:123456789012:key/abc",
		DeletionProtection:              boolPtr(true),
		BackupRetentionPeriod:           intPtr(14),
		PreferredBackupWindow:           "02:00-03:00",
		PreferredMaintenanceWindow:      "sun:05:00-sun:06:00",
		AutoMinorVersionUpgrade:         boolPtr(false),
		Iops:                            3000,
		AvailabilityZone:                "us-east-1a",
		EnableIAMDatabaseAuthentication: boolPtr(true),
		CACertificateIdentifier:         "rds-ca-rsa2048-g1",
		MonitoringInterval:              60,
		EnablePerformanceInsights:       boolPtr(true),
		EnableCloudwatchLogsExports:     []string{"error", "general"},
		CopyTagsToSnapshot:              boolPtr(true),
	})
	if aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	inst := resp.Result.DBInstance
	if !inst.StorageEncrypted {
		t.Error("StorageEncrypted = false, want true")
	}
	if inst.KmsKeyId != "arn:aws:kms:us-east-1:123456789012:key/abc" {
		t.Errorf("KmsKeyId = %q, want the given key", inst.KmsKeyId)
	}
	if !inst.DeletionProtection {
		t.Error("DeletionProtection = false, want true")
	}
	if inst.BackupRetentionPeriod != 14 {
		t.Errorf("BackupRetentionPeriod = %d, want 14", inst.BackupRetentionPeriod)
	}
	if inst.PreferredBackupWindow != "02:00-03:00" {
		t.Errorf("PreferredBackupWindow = %q, want %q", inst.PreferredBackupWindow, "02:00-03:00")
	}
	if inst.PreferredMaintenanceWindow != "sun:05:00-sun:06:00" {
		t.Errorf("PreferredMaintenanceWindow = %q, want %q", inst.PreferredMaintenanceWindow, "sun:05:00-sun:06:00")
	}
	if inst.AutoMinorVersionUpgrade {
		t.Error("AutoMinorVersionUpgrade = true, want false (explicit)")
	}
	if inst.Iops != 3000 {
		t.Errorf("Iops = %d, want 3000", inst.Iops)
	}
	if inst.AvailabilityZone != "us-east-1a" {
		t.Errorf("AvailabilityZone = %q, want %q", inst.AvailabilityZone, "us-east-1a")
	}
	if !inst.IAMDatabaseAuthenticationEnabled {
		t.Error("IAMDatabaseAuthenticationEnabled = false, want true")
	}
	if inst.CACertificateIdentifier != "rds-ca-rsa2048-g1" {
		t.Errorf("CACertificateIdentifier = %q, want %q", inst.CACertificateIdentifier, "rds-ca-rsa2048-g1")
	}
	if inst.CertificateDetails == nil || inst.CertificateDetails.CAIdentifier != "rds-ca-rsa2048-g1" {
		t.Errorf("CertificateDetails = %+v, want CAIdentifier %q", inst.CertificateDetails, "rds-ca-rsa2048-g1")
	}
	if inst.MonitoringInterval != 60 {
		t.Errorf("MonitoringInterval = %d, want 60", inst.MonitoringInterval)
	}
	if !inst.PerformanceInsightsEnabled {
		t.Error("PerformanceInsightsEnabled = false, want true")
	}
	if strings.Join(inst.EnabledCloudwatchLogsExports.Items, ",") != "error,general" {
		t.Errorf("EnabledCloudwatchLogsExports = %v, want [error general]", inst.EnabledCloudwatchLogsExports.Items)
	}
	if !inst.CopyTagsToSnapshot {
		t.Error("CopyTagsToSnapshot = false, want true")
	}

	// And DescribeDBInstances must agree with what CreateDBInstance returned.
	desc, aerr := h.describeDBInstancesTyped(ctx, &describeDBInstancesReq{DBInstanceIdentifier: "echo"})
	if aerr != nil {
		t.Fatalf("DescribeDBInstances: %s", aerr.Message)
	}
	got := desc.Result.DBInstances.Items[0]
	if got.BackupRetentionPeriod != 14 || got.Iops != 3000 || !got.StorageEncrypted {
		t.Errorf("DescribeDBInstances disagrees with CreateDBInstance: %+v", got)
	}
}

// AWS's own documented defaults, verified against the CreateDBInstance API
// reference: AutoMinorVersionUpgrade true, BackupRetentionPeriod 1,
// CopyTagsToSnapshot false, DeletionProtection false,
// EnableIAMDatabaseAuthentication (IAMDatabaseAuthenticationEnabled) false,
// MonitoringInterval 0, PerformanceInsightsEnabled false, StorageEncrypted
// false.
func TestCreateDBInstance_defaultsWhenPropertiesAreUnset(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()

	resp, aerr := h.createDBInstanceTyped(ctx, &createDBInstanceReq{
		DBInstanceIdentifier: "defaults",
		Engine:               "mysql",
		MasterUsername:       "admin",
		MasterUserPassword:   "password123",
	})
	if aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	inst := resp.Result.DBInstance
	if !inst.AutoMinorVersionUpgrade {
		t.Error("AutoMinorVersionUpgrade = false, want true (AWS default)")
	}
	if inst.BackupRetentionPeriod != 1 {
		t.Errorf("BackupRetentionPeriod = %d, want 1 (AWS default)", inst.BackupRetentionPeriod)
	}
	if inst.CopyTagsToSnapshot {
		t.Error("CopyTagsToSnapshot = true, want false (AWS default)")
	}
	if inst.DeletionProtection {
		t.Error("DeletionProtection = true, want false (AWS default)")
	}
	if inst.IAMDatabaseAuthenticationEnabled {
		t.Error("IAMDatabaseAuthenticationEnabled = true, want false (AWS default)")
	}
	if inst.MonitoringInterval != 0 {
		t.Errorf("MonitoringInterval = %d, want 0 (AWS default)", inst.MonitoringInterval)
	}
	if inst.PerformanceInsightsEnabled {
		t.Error("PerformanceInsightsEnabled = true, want false (AWS default)")
	}
	if inst.StorageEncrypted {
		t.Error("StorageEncrypted = true, want false (AWS default)")
	}
}

// A record written before this field existed (no BackupRetentionPeriod or
// AutoMinorVersionUpgrade in its persisted JSON at all) must still read back
// as AWS's own default rather than the Go zero value — see
// DBInstance.BackupRetentionPeriodOrDefault / AutoMinorVersionUpgradeOrDefault.
func TestDBInstance_predatingRecordReadsAWSDefaults(t *testing.T) {
	inst := &DBInstance{DBInstanceIdentifier: "old"}
	if got := inst.BackupRetentionPeriodOrDefault(); got != 1 {
		t.Errorf("BackupRetentionPeriodOrDefault() = %d, want 1", got)
	}
	if got := inst.AutoMinorVersionUpgradeOrDefault(); !got {
		t.Error("AutoMinorVersionUpgradeOrDefault() = false, want true")
	}
}

// ModifyDBInstance must apply every property it accepts, the same
// completeness ModifyDBCluster was already held to.
func TestModifyDBInstance_appliesEchoProperties(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()
	seedInstance(t, h, "mod", &createDBInstanceReq{})

	if _, aerr := h.modifyDBInstanceTyped(ctx, &modifyDBInstanceReq{
		DBInstanceIdentifier:            "mod",
		DeletionProtection:              boolPtr(true),
		BackupRetentionPeriod:           intPtr(21),
		PreferredBackupWindow:           "04:00-05:00",
		PreferredMaintenanceWindow:      "mon:03:00-mon:04:00",
		AutoMinorVersionUpgrade:         boolPtr(false),
		Iops:                            5000,
		EnableIAMDatabaseAuthentication: boolPtr(true),
		CACertificateIdentifier:         "rds-ca-rsa2048-g1",
		MonitoringInterval:              30,
		EnablePerformanceInsights:       boolPtr(true),
		CopyTagsToSnapshot:              boolPtr(true),
		CloudwatchLogsExportConfiguration: &cloudwatchLogsExportConfiguration{
			EnableLogTypes: []string{"audit"},
		},
	}); aerr != nil {
		t.Fatalf("ModifyDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	got, aerr := h.store.getDBInstance(ctx, "mod")
	if aerr != nil {
		t.Fatalf("getDBInstance: %s", aerr.Message)
	}
	if !got.DeletionProtection {
		t.Error("DeletionProtection = false, want true")
	}
	if got.BackupRetentionPeriodOrDefault() != 21 {
		t.Errorf("BackupRetentionPeriod = %d, want 21", got.BackupRetentionPeriodOrDefault())
	}
	if got.PreferredBackupWindow != "04:00-05:00" {
		t.Errorf("PreferredBackupWindow = %q, want %q", got.PreferredBackupWindow, "04:00-05:00")
	}
	if got.PreferredMaintenanceWindow != "mon:03:00-mon:04:00" {
		t.Errorf("PreferredMaintenanceWindow = %q, want %q", got.PreferredMaintenanceWindow, "mon:03:00-mon:04:00")
	}
	if got.AutoMinorVersionUpgradeOrDefault() {
		t.Error("AutoMinorVersionUpgrade = true, want false (explicit)")
	}
	if got.Iops != 5000 {
		t.Errorf("Iops = %d, want 5000", got.Iops)
	}
	if !got.EnableIAMDatabaseAuthentication {
		t.Error("EnableIAMDatabaseAuthentication = false, want true")
	}
	if got.CACertificateIdentifier != "rds-ca-rsa2048-g1" {
		t.Errorf("CACertificateIdentifier = %q, want %q", got.CACertificateIdentifier, "rds-ca-rsa2048-g1")
	}
	if got.MonitoringInterval != 30 {
		t.Errorf("MonitoringInterval = %d, want 30", got.MonitoringInterval)
	}
	if !got.PerformanceInsightsEnabled {
		t.Error("PerformanceInsightsEnabled = false, want true")
	}
	if !got.CopyTagsToSnapshot {
		t.Error("CopyTagsToSnapshot = false, want true")
	}
	if strings.Join(got.EnabledCloudwatchLogsExports, ",") != "audit" {
		t.Errorf("EnabledCloudwatchLogsExports = %v, want [audit]", got.EnabledCloudwatchLogsExports)
	}
}

// An absent property leaves the stored value alone, the same discipline
// ModifyDBCluster is held to.
func TestModifyDBInstance_absentPropertiesAreLeftAlone(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()
	seedInstance(t, h, "mod2", &createDBInstanceReq{BackupRetentionPeriod: intPtr(7)})

	if _, aerr := h.modifyDBInstanceTyped(ctx, &modifyDBInstanceReq{
		DBInstanceIdentifier: "mod2",
		DBInstanceClass:      "db.t3.small",
	}); aerr != nil {
		t.Fatalf("ModifyDBInstance: %s", aerr.Message)
	}

	got, _ := h.store.getDBInstance(ctx, "mod2")
	if got.BackupRetentionPeriodOrDefault() != 7 {
		t.Errorf("BackupRetentionPeriod = %d after an unrelated modify, want it left at 7", got.BackupRetentionPeriodOrDefault())
	}
}

// DeletionProtection=true must make DeleteDBInstance fail with the same
// error shape DeleteDBCluster already refuses a protected cluster with.
func TestDeleteDBInstance_refusesWhenDeletionProtectionEnabled(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()
	seedInstance(t, h, "protected", &createDBInstanceReq{DeletionProtection: boolPtr(true)})

	_, aerr := h.deleteDBInstanceTyped(ctx, &deleteDBInstanceReq{DBInstanceIdentifier: "protected"})
	if aerr == nil {
		t.Fatal("DeleteDBInstance succeeded against a DeletionProtection-enabled instance")
	}
	if aerr.Code != "InvalidParameterCombination" {
		t.Errorf("error code = %q, want InvalidParameterCombination", aerr.Code)
	}
	if !strings.Contains(aerr.Message, "Cannot delete protected DB Instance") {
		t.Errorf("error message = %q, want it to name the protected instance", aerr.Message)
	}

	// The instance must still be there afterward — a refused delete leaves
	// the record untouched, the same guarantee deleteDBClusterTyped gives.
	if _, aerr := h.store.getDBInstance(ctx, "protected"); aerr != nil {
		t.Fatalf("instance was removed despite the refused delete: %s", aerr.Message)
	}

	// Disabling protection must then allow the delete to proceed.
	if _, aerr := h.modifyDBInstanceTyped(ctx, &modifyDBInstanceReq{
		DBInstanceIdentifier: "protected",
		DeletionProtection:   boolPtr(false),
	}); aerr != nil {
		t.Fatalf("ModifyDBInstance (disable protection): %s", aerr.Message)
	}
	if _, aerr := h.deleteDBInstanceTyped(ctx, &deleteDBInstanceReq{DBInstanceIdentifier: "protected"}); aerr != nil {
		t.Fatalf("DeleteDBInstance after disabling protection: %s", aerr.Message)
	}
}

// CreateDBInstance's BackupRetentionPeriod is 0-35 (0 disables backups),
// asymmetric from CreateDBCluster's 1-35 — see validateInstanceBackupRetentionPeriod.
func TestCreateDBInstance_backupRetentionPeriodBounds(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()

	// 0 is legal for an instance (disables automated backups) though not for
	// a cluster.
	if _, aerr := h.createDBInstanceTyped(ctx, &createDBInstanceReq{
		DBInstanceIdentifier:  "zero-retention",
		Engine:                "mysql",
		MasterUsername:        "admin",
		MasterUserPassword:    "password123",
		BackupRetentionPeriod: intPtr(0),
	}); aerr != nil {
		t.Fatalf("CreateDBInstance with BackupRetentionPeriod=0: %s", aerr.Message)
	}

	_, aerr := h.createDBInstanceTyped(ctx, &createDBInstanceReq{
		DBInstanceIdentifier:  "bad-retention",
		Engine:                "mysql",
		MasterUsername:        "admin",
		MasterUserPassword:    "password123",
		BackupRetentionPeriod: intPtr(36),
	})
	if aerr == nil {
		t.Fatal("CreateDBInstance accepted BackupRetentionPeriod=36")
	}
	if aerr.Code != "InvalidParameterValue" {
		t.Errorf("error code = %q, want InvalidParameterValue", aerr.Code)
	}
}
