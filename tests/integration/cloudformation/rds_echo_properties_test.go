package cloudformation_test

// rds_echo_properties_test.go — #2133: the CloudFormation half of the
// DBInstance/DBCluster properties that were forwarded (or not) to
// CreateDBInstance/CreateDBCluster but never landed in DescribeDBInstances/
// DescribeDBClusters — StorageEncrypted, KmsKeyId, DeletionProtection,
// BackupRetentionPeriod, PreferredBackupWindow, PreferredMaintenanceWindow,
// AutoMinorVersionUpgrade, Iops, AvailabilityZone,
// EnableIAMDatabaseAuthentication, CACertificateIdentifier,
// MonitoringInterval, EnablePerformanceInsights,
// EnableCloudwatchLogsExports, CopyTagsToSnapshot on the instance side, and
// StorageEncrypted, KmsKeyId, EnableHttpEndpoint,
// ServerlessV2ScalingConfiguration on the cluster side.
//
// A stack deploying either resource with these properties set must see
// DescribeDBInstances/DescribeDBClusters agree with what the template asked
// for.

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// rdsQuery sends an RDS Query-protocol request directly — not through
// cfnQuery, which hardcodes CloudFormation's own API version (2010-05-15)
// onto every request and would overwrite RDS's 2014-10-31. See
// rds_update_test.go's rdsMasterUsernames for the same pattern.
func rdsQuery(t *testing.T, srv *helpers.TestServer, params url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(params.Encode()))
	if err != nil {
		t.Fatalf("build RDS request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("RDS request %s: %v", params.Get("Action"), err)
	}
	return resp
}

func rdsInstanceEchoTemplate(id string) string {
	return fmt.Sprintf(`{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Database": {
      "Type": "AWS::RDS::DBInstance",
      "Properties": {
        "DBInstanceIdentifier": %q,
        "Engine": "mysql",
        "DBInstanceClass": "db.t3.micro",
        "AllocatedStorage": "20",
        "MasterUsername": "dbadmin",
        "MasterUserPassword": "correct-horse-battery",
        "StorageEncrypted": "true",
        "KmsKeyId": "arn:aws:kms:us-east-1:123456789012:key/test-key",
        "DeletionProtection": "false",
        "BackupRetentionPeriod": "14",
        "PreferredBackupWindow": "02:00-03:00",
        "PreferredMaintenanceWindow": "sun:05:00-sun:06:00",
        "AutoMinorVersionUpgrade": "false",
        "Iops": "3000",
        "StorageType": "io1",
        "AvailabilityZone": "us-east-1a",
        "EnableIAMDatabaseAuthentication": "true",
        "CACertificateIdentifier": "rds-ca-rsa2048-g1",
        "MonitoringInterval": "60",
        "EnablePerformanceInsights": "true",
        "EnableCloudwatchLogsExports": ["error", "general"],
        "CopyTagsToSnapshot": "true"
      }
    }
  }
}`, id)
}

type describedEchoInstance struct {
	StorageEncrypted                 bool   `xml:"StorageEncrypted"`
	KmsKeyId                         string `xml:"KmsKeyId"`
	DeletionProtection               bool   `xml:"DeletionProtection"`
	BackupRetentionPeriod            int    `xml:"BackupRetentionPeriod"`
	PreferredBackupWindow            string `xml:"PreferredBackupWindow"`
	PreferredMaintenanceWindow       string `xml:"PreferredMaintenanceWindow"`
	AutoMinorVersionUpgrade          bool   `xml:"AutoMinorVersionUpgrade"`
	Iops                             int    `xml:"Iops"`
	AvailabilityZone                 string `xml:"AvailabilityZone"`
	IAMDatabaseAuthenticationEnabled bool   `xml:"IAMDatabaseAuthenticationEnabled"`
	CACertificateIdentifier          string `xml:"CACertificateIdentifier"`
	MonitoringInterval               int    `xml:"MonitoringInterval"`
	PerformanceInsightsEnabled       bool   `xml:"PerformanceInsightsEnabled"`
	EnabledCloudwatchLogsExports     struct {
		Items []string `xml:"member"`
	} `xml:"EnabledCloudwatchLogsExports"`
	CopyTagsToSnapshot bool `xml:"CopyTagsToSnapshot"`
}

// Every DBInstance property CreateDBInstance accepts must reach
// DescribeDBInstances after a stack deploy.
func TestCreateStack_rdsInstanceEchoesAllProperties(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "rds-echo-instance-stack"
	const instanceID = "echo-instance"

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{rdsInstanceEchoTemplate(instanceID)},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	dr := rdsQuery(t, srv, url.Values{
		"Action":               []string{"DescribeDBInstances"},
		"Version":              []string{"2014-10-31"},
		"DBInstanceIdentifier": []string{instanceID},
	})
	defer dr.Body.Close()
	helpers.AssertStatus(t, dr, http.StatusOK)
	body := readBody(t, dr)

	var result struct {
		Instances []describedEchoInstance `xml:"DescribeDBInstancesResult>DBInstances>DBInstance"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal DescribeDBInstancesResponse: %v\nbody: %s", err, body)
	}
	if len(result.Instances) != 1 {
		t.Fatalf("expected exactly one DB instance, got %d: %s", len(result.Instances), body)
	}
	got := result.Instances[0]

	if !got.StorageEncrypted {
		t.Error("StorageEncrypted = false, want true")
	}
	if got.KmsKeyId != "arn:aws:kms:us-east-1:123456789012:key/test-key" {
		t.Errorf("KmsKeyId = %q, want the templated key", got.KmsKeyId)
	}
	if got.DeletionProtection {
		t.Error("DeletionProtection = true, want false")
	}
	if got.BackupRetentionPeriod != 14 {
		t.Errorf("BackupRetentionPeriod = %d, want 14", got.BackupRetentionPeriod)
	}
	if got.PreferredBackupWindow != "02:00-03:00" {
		t.Errorf("PreferredBackupWindow = %q, want %q", got.PreferredBackupWindow, "02:00-03:00")
	}
	if got.PreferredMaintenanceWindow != "sun:05:00-sun:06:00" {
		t.Errorf("PreferredMaintenanceWindow = %q, want %q", got.PreferredMaintenanceWindow, "sun:05:00-sun:06:00")
	}
	if got.AutoMinorVersionUpgrade {
		t.Error("AutoMinorVersionUpgrade = true, want false (explicit)")
	}
	if got.Iops != 3000 {
		t.Errorf("Iops = %d, want 3000", got.Iops)
	}
	if got.AvailabilityZone != "us-east-1a" {
		t.Errorf("AvailabilityZone = %q, want %q", got.AvailabilityZone, "us-east-1a")
	}
	if !got.IAMDatabaseAuthenticationEnabled {
		t.Error("IAMDatabaseAuthenticationEnabled = false, want true")
	}
	if got.CACertificateIdentifier != "rds-ca-rsa2048-g1" {
		t.Errorf("CACertificateIdentifier = %q, want %q", got.CACertificateIdentifier, "rds-ca-rsa2048-g1")
	}
	if got.MonitoringInterval != 60 {
		t.Errorf("MonitoringInterval = %d, want 60", got.MonitoringInterval)
	}
	if !got.PerformanceInsightsEnabled {
		t.Error("PerformanceInsightsEnabled = false, want true")
	}
	if len(got.EnabledCloudwatchLogsExports.Items) != 2 {
		t.Errorf("EnabledCloudwatchLogsExports = %v, want [error general]", got.EnabledCloudwatchLogsExports.Items)
	}
	if !got.CopyTagsToSnapshot {
		t.Error("CopyTagsToSnapshot = false, want true")
	}
}

func rdsClusterEchoTemplate(id string) string {
	return fmt.Sprintf(`{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Cluster": {
      "Type": "AWS::RDS::DBCluster",
      "Properties": {
        "DBClusterIdentifier": %q,
        "Engine": "aurora-mysql",
        "MasterUsername": "dbadmin",
        "MasterUserPassword": "correct-horse-battery",
        "StorageEncrypted": "true",
        "KmsKeyId": "arn:aws:kms:us-east-1:123456789012:key/test-key",
        "EnableHttpEndpoint": "true",
        "ServerlessV2ScalingConfiguration": {
          "MinCapacity": 0.5,
          "MaxCapacity": 4
        }
      }
    }
  }
}`, id)
}

type describedEchoCluster struct {
	StorageEncrypted                 bool   `xml:"StorageEncrypted"`
	KmsKeyId                         string `xml:"KmsKeyId"`
	HttpEndpointEnabled              bool   `xml:"HttpEndpointEnabled"`
	ServerlessV2ScalingConfiguration *struct {
		MinCapacity float64 `xml:"MinCapacity"`
		MaxCapacity float64 `xml:"MaxCapacity"`
	} `xml:"ServerlessV2ScalingConfiguration"`
}

// Every DBCluster property CreateDBCluster accepts must reach
// DescribeDBClusters after a stack deploy.
func TestCreateStack_rdsClusterEchoesAllProperties(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "rds-echo-cluster-stack"
	const clusterID = "echo-cluster"

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{rdsClusterEchoTemplate(clusterID)},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	dr := rdsQuery(t, srv, url.Values{
		"Action":              []string{"DescribeDBClusters"},
		"Version":             []string{"2014-10-31"},
		"DBClusterIdentifier": []string{clusterID},
	})
	defer dr.Body.Close()
	helpers.AssertStatus(t, dr, http.StatusOK)
	body := readBody(t, dr)

	var result struct {
		Clusters []describedEchoCluster `xml:"DescribeDBClustersResult>DBClusters>DBCluster"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal DescribeDBClustersResponse: %v\nbody: %s", err, body)
	}
	if len(result.Clusters) != 1 {
		t.Fatalf("expected exactly one DB cluster, got %d: %s", len(result.Clusters), body)
	}
	got := result.Clusters[0]

	if !got.StorageEncrypted {
		t.Error("StorageEncrypted = false, want true")
	}
	if got.KmsKeyId != "arn:aws:kms:us-east-1:123456789012:key/test-key" {
		t.Errorf("KmsKeyId = %q, want the templated key", got.KmsKeyId)
	}
	if !got.HttpEndpointEnabled {
		t.Error("HttpEndpointEnabled = false, want true")
	}
	if got.ServerlessV2ScalingConfiguration == nil {
		t.Fatal("ServerlessV2ScalingConfiguration is nil, want it set")
	}
	if got.ServerlessV2ScalingConfiguration.MinCapacity != 0.5 {
		t.Errorf("MinCapacity = %v, want 0.5", got.ServerlessV2ScalingConfiguration.MinCapacity)
	}
	if got.ServerlessV2ScalingConfiguration.MaxCapacity != 4 {
		t.Errorf("MaxCapacity = %v, want 4", got.ServerlessV2ScalingConfiguration.MaxCapacity)
	}
}

// DeletionProtection enabled through CloudFormation must block DeleteStack
// exactly as it blocks a direct DeleteDBInstance call.
func TestDeleteStack_rdsInstanceWithDeletionProtectionIsRefused(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "rds-protected-instance-stack"
	const instanceID = "protected-instance"

	template := fmt.Sprintf(`{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Database": {
      "Type": "AWS::RDS::DBInstance",
      "Properties": {
        "DBInstanceIdentifier": %q,
        "Engine": "mysql",
        "DBInstanceClass": "db.t3.micro",
        "AllocatedStorage": "20",
        "MasterUsername": "dbadmin",
        "MasterUserPassword": "correct-horse-battery",
        "DeletionProtection": "true"
      }
    }
  }
}`, instanceID)

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{template},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	dsr := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": []string{stackName}})
	defer dsr.Body.Close()
	helpers.AssertStatus(t, dsr, http.StatusOK)

	got := waitForStackStatusIn(t, srv, stackName, "DELETE_COMPLETE", "DELETE_FAILED")
	if got != "DELETE_FAILED" {
		t.Fatalf("stack status = %s, want DELETE_FAILED for a protected DB instance", got)
	}

	// The instance must still exist: a refused delete must not have removed
	// the record underneath a stack that reports it failed.
	dr := rdsQuery(t, srv, url.Values{
		"Action":               []string{"DescribeDBInstances"},
		"Version":              []string{"2014-10-31"},
		"DBInstanceIdentifier": []string{instanceID},
	})
	defer dr.Body.Close()
	helpers.AssertStatus(t, dr, http.StatusOK)
}
