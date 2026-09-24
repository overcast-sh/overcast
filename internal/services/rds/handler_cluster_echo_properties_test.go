package rds

// handler_cluster_echo_properties_test.go — #2133's remaining DBCluster
// properties: ServerlessV2ScalingConfiguration, EnableHttpEndpoint,
// StorageEncrypted and KmsKeyId.

import (
	"context"
	"testing"
)

func TestCreateDBCluster_echoesEveryProperty(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()

	resp, aerr := h.createDBClusterTyped(ctx, &createDBClusterReq{
		DBClusterIdentifier: "echo-cl",
		Engine:              "aurora-mysql",
		MasterUsername:      "admin",
		MasterUserPassword:  "password123",
		StorageEncrypted:    boolPtr(true),
		KmsKeyId:            "arn:aws:kms:us-east-1:123456789012:key/abc",
		EnableHttpEndpoint:  boolPtr(true),
		ServerlessV2ScalingConfiguration: &serverlessV2ScalingConfigurationReq{
			MinCapacity: float64Ptr(0.5),
			MaxCapacity: float64Ptr(4),
		},
	})
	if aerr != nil {
		t.Fatalf("CreateDBCluster: %s: %s", aerr.Code, aerr.Message)
	}

	c := resp.Result.DBCluster
	if !c.StorageEncrypted {
		t.Error("StorageEncrypted = false, want true")
	}
	if c.KmsKeyId != "arn:aws:kms:us-east-1:123456789012:key/abc" {
		t.Errorf("KmsKeyId = %q, want the given key", c.KmsKeyId)
	}
	if !c.HttpEndpointEnabled {
		t.Error("HttpEndpointEnabled = false, want true")
	}
	if c.ServerlessV2ScalingConfiguration == nil {
		t.Fatal("ServerlessV2ScalingConfiguration is nil, want it set")
	}
	if c.ServerlessV2ScalingConfiguration.MinCapacity != 0.5 {
		t.Errorf("MinCapacity = %v, want 0.5", c.ServerlessV2ScalingConfiguration.MinCapacity)
	}
	if c.ServerlessV2ScalingConfiguration.MaxCapacity != 4 {
		t.Errorf("MaxCapacity = %v, want 4", c.ServerlessV2ScalingConfiguration.MaxCapacity)
	}

	desc, aerr := h.describeDBClustersTyped(ctx, &describeDBClustersReq{DBClusterIdentifier: "echo-cl"})
	if aerr != nil {
		t.Fatalf("DescribeDBClusters: %s", aerr.Message)
	}
	got := desc.Result.DBClusters.Items[0]
	if !got.StorageEncrypted || got.KmsKeyId == "" || !got.HttpEndpointEnabled || got.ServerlessV2ScalingConfiguration == nil {
		t.Errorf("DescribeDBClusters disagrees with CreateDBCluster: %+v", got)
	}
}

// A cluster that never mentions ServerlessV2ScalingConfiguration must not
// carry the element at all — a zero-value struct would falsely claim a
// MinCapacity/MaxCapacity of 0, a floor no cluster can actually run at.
func TestCreateDBCluster_serverlessV2ScalingConfigurationOmittedWhenUnset(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()

	resp, aerr := h.createDBClusterTyped(ctx, &createDBClusterReq{
		DBClusterIdentifier: "no-serverless",
		Engine:              "aurora-mysql",
		MasterUsername:      "admin",
		MasterUserPassword:  "password123",
	})
	if aerr != nil {
		t.Fatalf("CreateDBCluster: %s: %s", aerr.Code, aerr.Message)
	}
	if resp.Result.DBCluster.ServerlessV2ScalingConfiguration != nil {
		t.Errorf("ServerlessV2ScalingConfiguration = %+v, want nil", resp.Result.DBCluster.ServerlessV2ScalingConfiguration)
	}
	if resp.Result.DBCluster.StorageEncrypted {
		t.Error("StorageEncrypted = true, want false (AWS default)")
	}
}

func TestModifyDBCluster_appliesServerlessAndHttpEndpoint(t *testing.T) {
	h := newClusterTestHandler(t)
	ctx := context.Background()
	seedCluster(t, h, "mod-cl")

	if _, aerr := h.modifyDBClusterTyped(ctx, &modifyDBClusterReq{
		DBClusterIdentifier: "mod-cl",
		EnableHttpEndpoint:  boolPtr(true),
		ServerlessV2ScalingConfiguration: &serverlessV2ScalingConfigurationReq{
			MinCapacity: float64Ptr(1),
			MaxCapacity: float64Ptr(8),
		},
	}); aerr != nil {
		t.Fatalf("ModifyDBCluster: %s: %s", aerr.Code, aerr.Message)
	}

	got, aerr := h.store.getDBCluster(ctx, "mod-cl")
	if aerr != nil {
		t.Fatalf("getDBCluster: %s", aerr.Message)
	}
	if !got.HttpEndpointEnabled {
		t.Error("HttpEndpointEnabled = false, want true")
	}
	if got.ServerlessV2ScalingConfiguration == nil || got.ServerlessV2ScalingConfiguration.MaxCapacity != 8 {
		t.Errorf("ServerlessV2ScalingConfiguration = %+v, want MaxCapacity 8", got.ServerlessV2ScalingConfiguration)
	}
}

func float64Ptr(v float64) *float64 { return &v }
