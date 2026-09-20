// notfound_status_test.go — the HTTP status each ElastiCache not-found fault
// answers with (issue #1991).
//
// Every status asserted here is read off the pinned ElastiCache model
// (models/aws/VERSION, api-models-aws elasticache-2015-02-02), from the
// httpResponseCode member of each fault shape's aws.protocols#awsQueryError
// trait. No real AWS account was called.
package elasticache_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ── Not-found HTTP status ─────────────────────────────────────────────────────
//
// ElastiCache is a Query-protocol service, so the HTTP status each fault
// answers with is not a house convention: the pinned Smithy model pins it, per
// fault, in the aws.protocols#awsQueryError trait beside the wire code.
// CacheClusterNotFoundFault, ReplicationGroupNotFoundFault and
// CacheParameterGroupNotFoundFault all declare httpResponseCode 404, so a
// missing cache, group or parameter group is 404 rather than the 400 a
// malformed request gets.
//
// The trait also settles the wire code, which is why the three codes disagree
// about the Fault suffix: the model spells two of them without it
// (CacheClusterNotFound, CacheParameterGroupNotFound) and the third with it.
//
// The last two rows are controls rather than part of the change, because the
// status is per fault and not per service: CacheSubnetGroupNotFoundFault
// declares 400 in the same trait and must not be levelled up by a sweep over
// the package, and ServerlessCacheNotFoundFault already answered the 404 it
// declares.
func TestElastiCacheNotFoundFaults_useTheModelledStatus(t *testing.T) {
	cases := []struct {
		name   string
		action string
		params url.Values
		code   string
		status int
	}{
		{
			name:   "DescribeCacheClusters",
			action: "DescribeCacheClusters",
			params: url.Values{"CacheClusterId": []string{"no-such-cluster"}},
			code:   "CacheClusterNotFound",
			status: http.StatusNotFound,
		},
		{
			name:   "DeleteCacheCluster",
			action: "DeleteCacheCluster",
			params: url.Values{"CacheClusterId": []string{"no-such-cluster"}},
			code:   "CacheClusterNotFound",
			status: http.StatusNotFound,
		},
		{
			name:   "ModifyCacheCluster",
			action: "ModifyCacheCluster",
			params: url.Values{
				"CacheClusterId": []string{"no-such-cluster"},
				"CacheNodeType":  []string{"cache.t3.small"},
			},
			code:   "CacheClusterNotFound",
			status: http.StatusNotFound,
		},
		{
			name:   "DescribeReplicationGroups",
			action: "DescribeReplicationGroups",
			params: url.Values{"ReplicationGroupId": []string{"no-such-group"}},
			code:   "ReplicationGroupNotFoundFault",
			status: http.StatusNotFound,
		},
		{
			name:   "DeleteReplicationGroup",
			action: "DeleteReplicationGroup",
			params: url.Values{"ReplicationGroupId": []string{"no-such-group"}},
			code:   "ReplicationGroupNotFoundFault",
			status: http.StatusNotFound,
		},
		{
			name:   "ModifyReplicationGroup",
			action: "ModifyReplicationGroup",
			params: url.Values{
				"ReplicationGroupId":          []string{"no-such-group"},
				"ReplicationGroupDescription": []string{"updated"},
			},
			code:   "ReplicationGroupNotFoundFault",
			status: http.StatusNotFound,
		},
		{
			// The fault CreateCacheCluster raises for a ReplicationGroupId
			// naming a group that is not there: the same fault, so the same
			// status, on an operation that is creating rather than reading.
			name:   "CreateCacheCluster with an unknown ReplicationGroupId",
			action: "CreateCacheCluster",
			params: url.Values{
				"CacheClusterId":     []string{"orphan-cluster"},
				"Engine":             []string{"redis"},
				"ReplicationGroupId": []string{"no-such-group"},
			},
			code:   "ReplicationGroupNotFoundFault",
			status: http.StatusNotFound,
		},
		{
			name:   "DescribeCacheParameterGroups",
			action: "DescribeCacheParameterGroups",
			params: url.Values{"CacheParameterGroupName": []string{"no-such-params"}},
			code:   "CacheParameterGroupNotFound",
			status: http.StatusNotFound,
		},
		{
			name:   "DeleteCacheParameterGroup",
			action: "DeleteCacheParameterGroup",
			params: url.Values{"CacheParameterGroupName": []string{"no-such-params"}},
			code:   "CacheParameterGroupNotFound",
			status: http.StatusNotFound,
		},
		{
			name:   "DescribeCacheParameters",
			action: "DescribeCacheParameters",
			params: url.Values{"CacheParameterGroupName": []string{"no-such-params"}},
			code:   "CacheParameterGroupNotFound",
			status: http.StatusNotFound,
		},
		{
			name:   "DeleteCacheSubnetGroup is modelled at 400",
			action: "DeleteCacheSubnetGroup",
			params: url.Values{"CacheSubnetGroupName": []string{"no-such-subnets"}},
			code:   "CacheSubnetGroupNotFoundFault",
			status: http.StatusBadRequest,
		},
		{
			name:   "DeleteServerlessCache is modelled at 404",
			action: "DeleteServerlessCache",
			params: url.Values{"ServerlessCacheName": []string{"no-such-serverless"}},
			code:   "ServerlessCacheNotFoundFault",
			status: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an ElastiCache service holding none of these resources
			srv := helpers.NewTestServer(t)

			// When: the operation names one that does not exist
			resp := cacheQuery(t, srv, tc.action, tc.params)
			defer resp.Body.Close()

			// Then: the fault answers at the status its awsQueryError trait
			// binds, carrying the trait code in the Query error envelope
			helpers.AssertStatus(t, resp, tc.status)
			assertQueryXMLError(t, resp, tc.code)
		})
	}
}
