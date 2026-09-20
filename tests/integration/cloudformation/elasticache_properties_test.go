package cloudformation_test

// elasticache_properties_test.go — AWS::ElastiCache::CacheCluster and
// AWS::ElastiCache::ReplicationGroup property threading. Shares cfnQuery,
// elasticacheQuery, waitForStackStatus and readBody with
// cloudformation_test.go.
//
// ElastiCache's CacheCluster and ReplicationGroup handlers are Docker-backed
// — CreateCacheCluster and CreateReplicationGroup pull an image and start a
// real redis/valkey/memcached container chosen by the Engine and
// EngineVersion properties (engineImage in
// internal/services/elasticache/handler.go). These tests run without Docker
// and prove the CloudFormation provisioner threads the properties through to
// the stored ElastiCache record — the container-selection proof itself needs
// a live daemon and is left to the Docker-gated suite (see
// tests/integration/elasticache and CI, which runs with Docker present).

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const elastiCacheReplicationGroupTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "ValkeyGroup": {
      "Type": "AWS::ElastiCache::ReplicationGroup",
      "Properties": {
        "ReplicationGroupId": "cfn-rg-valkey",
        "ReplicationGroupDescription": "valkey replication group",
        "Engine": "valkey",
        "EngineVersion": "8.0",
        "CacheNodeType": "cache.t3.micro",
        "SnapshotRetentionLimit": 5,
        "PrimaryClusterId": "cfn-rg-valkey-001"
      }
    }
  }
}`

// TestCreateStack_ElastiCacheReplicationGroup_engineThreaded pins the
// priority-1 gap from issue #543: CreateReplicationGroup's Query request
// dropped Engine and EngineVersion entirely, so engineImage
// (internal/services/elasticache/handler.go) fell through to its
// `redis:7` default regardless of what the template asked for. A group
// declared for valkey silently got a redis container that looked like a
// success — the endpoint was reachable, DescribeReplicationGroups answered
// "available" — while the client spoke the wrong protocol to it.
//
// This does not touch Docker: it proves the provisioner now sends Engine and
// EngineVersion to the service by reading them back off the stored record.
// Which image that engine resolves to, and that a real container answers as
// that engine, is exercised by the ElastiCache service's own Docker-gated
// suite and by CI, which runs with a daemon present.
func TestCreateStack_ElastiCacheReplicationGroup_engineThreaded(t *testing.T) {
	srv := helpers.NewTestServer(t)

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"elasticache-rg-engine-stack"},
		"TemplateBody": []string{elastiCacheReplicationGroupTemplate},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "elasticache-rg-engine-stack", "CREATE_COMPLETE")

	dr := elasticacheQuery(t, srv, "DescribeReplicationGroups", url.Values{
		"ReplicationGroupId": []string{"cfn-rg-valkey"},
	})
	defer dr.Body.Close()
	helpers.AssertStatus(t, dr, http.StatusOK)
	body := string(readBody(t, dr))

	if !strings.Contains(body, "<Engine>valkey</Engine>") {
		t.Fatalf("expected the replication group to have kept its valkey engine, got: %s", body)
	}
	if strings.Contains(body, "<Engine>redis</Engine>") {
		t.Fatalf("replication group fell back to the redis default despite Engine=valkey, got: %s", body)
	}
	if !strings.Contains(body, "<SnapshotRetentionLimit>5</SnapshotRetentionLimit>") {
		t.Fatalf("expected SnapshotRetentionLimit=5 to round-trip from Create, got: %s", body)
	}
	if !strings.Contains(body, "<ClusterId>cfn-rg-valkey-001</ClusterId>") {
		t.Fatalf("expected PrimaryClusterId to register as a member cluster, got: %s", body)
	}
}

const elastiCacheCacheClusterPropertiesTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "PropsCluster": {
      "Type": "AWS::ElastiCache::CacheCluster",
      "Properties": {
        "ClusterName": "cfn-props-cluster",
        "Engine": "memcached",
        "EngineVersion": "1.6",
        "CacheNodeType": "cache.t3.micro",
        "NumCacheNodes": 1,
        "PreferredAvailabilityZone": "us-east-1a",
        "CacheParameterGroupName": "default.memcached1.6",
        "Tags": [{ "Key": "env", "Value": "prod" }]
      }
    }
  },
  "Outputs": {
    "ClusterArn": { "Value": { "Fn::GetAtt": ["PropsCluster", "Arn"] } }
  }
}`

// TestCreateStack_ElastiCacheCacheCluster_propertiesThreaded pins the
// "Also dropped" properties issue #543 calls out for CacheCluster:
// PreferredAvailabilityZone and CacheParameterGroupName were stored fields
// the Create path simply never populated, and Tags were accepted by
// AddTagsToResource but never applied at Create despite CDK templates
// supplying them inline.
func TestCreateStack_ElastiCacheCacheCluster_propertiesThreaded(t *testing.T) {
	srv := helpers.NewTestServer(t)

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"elasticache-cluster-props-stack"},
		"TemplateBody": []string{elastiCacheCacheClusterPropertiesTemplate},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "elasticache-cluster-props-stack", "CREATE_COMPLETE")

	dr := elasticacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"cfn-props-cluster"},
	})
	defer dr.Body.Close()
	helpers.AssertStatus(t, dr, http.StatusOK)
	body := string(readBody(t, dr))

	if !strings.Contains(body, "<PreferredAvailabilityZone>us-east-1a</PreferredAvailabilityZone>") {
		t.Fatalf("expected PreferredAvailabilityZone to round-trip from Create, got: %s", body)
	}
	if !strings.Contains(body, "<CacheParameterGroupName>default.memcached1.6</CacheParameterGroupName>") {
		t.Fatalf("expected CacheParameterGroupName to round-trip from Create, got: %s", body)
	}

	tr := elasticacheQuery(t, srv, "ListTagsForResource", url.Values{
		"ResourceName": []string{"arn:aws:elasticache:us-east-1:000000000000:cluster:cfn-props-cluster"},
	})
	defer tr.Body.Close()
	helpers.AssertStatus(t, tr, http.StatusOK)
	tagBody := string(readBody(t, tr))
	if !strings.Contains(tagBody, "<Key>env</Key>") || !strings.Contains(tagBody, "<Value>prod</Value>") {
		t.Fatalf("expected the env=prod tag supplied at Create to be stored, got: %s", tagBody)
	}
}

const elastiCacheTeardownTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "GoneCluster": {
      "Type": "AWS::ElastiCache::CacheCluster",
      "Properties": {
        "ClusterName": "cfn-teardown-cluster",
        "Engine": "redis",
        "CacheNodeType": "cache.t3.micro",
        "NumCacheNodes": 1
      }
    }
  }
}`

// TestDeleteStack_ElastiCacheCacheClusterAlreadyGone pins the teardown half of
// issue #1991. A resource that is already absent must never fail a stack
// delete, and the provisioner decides that from what the service answers:
// resourceAlreadyGone treats HTTP 404, or absence named in the error body, as
// "it is gone". CacheClusterNotFound used to satisfy only the second of those
// and now satisfies both, which is exactly the kind of move that goes
// unnoticed until a stack strands in DELETE_FAILED.
//
// The cluster is removed out of band first so that the stack delete meets the
// not-found answer rather than a successful DeleteCacheCluster.
func TestDeleteStack_ElastiCacheCacheClusterAlreadyGone(t *testing.T) {
	// Given: a stack holding a cache cluster
	srv := helpers.NewTestServer(t)
	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"elasticache-teardown-stack"},
		"TemplateBody": []string{elastiCacheTeardownTemplate},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "elasticache-teardown-stack", "CREATE_COMPLETE")

	// And: the cluster deleted out from under it, so that the stack delete
	// meets the not-found fault
	dc := elasticacheQuery(t, srv, "DeleteCacheCluster", url.Values{
		"CacheClusterId": []string{"cfn-teardown-cluster"},
	})
	defer dc.Body.Close()
	helpers.AssertStatus(t, dc, http.StatusOK)
	helpers.Eventually(t, 30*time.Second, 20*time.Millisecond, func() bool {
		resp := elasticacheQuery(t, srv, "DescribeCacheClusters", url.Values{
			"CacheClusterId": []string{"cfn-teardown-cluster"},
		})
		defer resp.Body.Close()
		return strings.Contains(string(readBody(t, resp)), "CacheClusterNotFound")
	}, "timed out waiting for the cache cluster to be gone")

	// And: the fault it answers with is the 404 its awsQueryError trait binds,
	// which is the signal resourceAlreadyGone reads first
	missing := elasticacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"cfn-teardown-cluster"},
	})
	defer missing.Body.Close()
	helpers.AssertStatus(t, missing, http.StatusNotFound)

	// When: the stack is deleted
	del := cfnQuery(t, srv, "DeleteStack", url.Values{
		"StackName": []string{"elasticache-teardown-stack"},
	})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: the teardown reads the fault as "already gone" and completes,
	// rather than reporting a resource still standing
	waitForStackStatus(t, srv, "elasticache-teardown-stack", "DELETE_COMPLETE")
}
