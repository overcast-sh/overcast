// Package elasticache_test contains integration tests for the ElastiCache emulator.
//
// Run: go test ./tests/integration/elasticache/...
package elasticache_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// cacheQuery sends an ElastiCache Query protocol request.
func cacheQuery(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2015-02-02")
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func decodeXML(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, xml.Unmarshal(b, dst), "body: %s", b)
}

func assertQueryXMLError(t *testing.T, resp *http.Response, expectedCode string) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var errResp struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	require.NoError(t, xml.Unmarshal(b, &errResp), "body: %s", b)
	assert.Equal(t, expectedCode, errResp.Error.Code)
}

// ── CreateCacheCluster ────────────────────────────────────────────────────────

func TestCreateCacheCluster_success(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: CreateCacheCluster is called with valid params
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"test-cluster"},
		"Engine":         []string{"redis"},
		"CacheNodeType":  []string{"cache.t3.micro"},
	})

	// Then: 200 with a CacheCluster element
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		XMLName xml.Name `xml:"CreateCacheClusterResponse"`
		Result  struct {
			CacheCluster struct {
				CacheClusterId     string `xml:"CacheClusterId"`
				CacheClusterStatus string `xml:"CacheClusterStatus"`
				Engine             string `xml:"Engine"`
				EngineVersion      string `xml:"EngineVersion"`
				ARN                string `xml:"ARN"`
			} `xml:"CacheCluster"`
		} `xml:"CreateCacheClusterResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "test-cluster", out.Result.CacheCluster.CacheClusterId)
	assert.Equal(t, "creating", out.Result.CacheCluster.CacheClusterStatus)
	assert.Equal(t, "redis", out.Result.CacheCluster.Engine)
	assert.NotEmpty(t, out.Result.CacheCluster.ARN)
	assert.Contains(t, out.Result.CacheCluster.ARN, "test-cluster")
}

func TestCreateCacheCluster_duplicate(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a cluster already exists
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"dup-cluster"},
		"Engine":         []string{"redis"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: CreateCacheCluster is called again with the same ID
	resp = cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"dup-cluster"},
		"Engine":         []string{"redis"},
	})

	// Then: CacheClusterAlreadyExists error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "CacheClusterAlreadyExists")
}

func TestCreateCacheCluster_missing_id(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: no CacheClusterId
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"Engine": []string{"redis"},
	})
	// Then: validation error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

func TestCreateCacheCluster_invalid_engine(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"bad-engine"},
		"Engine":         []string{"mysql"},
	})
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

// ── DescribeCacheClusters ─────────────────────────────────────────────────────

func TestDescribeCacheClusters_all(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: two clusters
	for _, id := range []string{"cluster-a", "cluster-b"} {
		resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
			"CacheClusterId": []string{id},
			"Engine":         []string{"redis"},
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}

	// When: DescribeCacheClusters without filter
	resp := cacheQuery(t, srv, "DescribeCacheClusters", nil)

	// Then: both returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		XMLName xml.Name `xml:"DescribeCacheClustersResponse"`
		Result  struct {
			CacheClusters struct {
				Items []struct {
					CacheClusterId string `xml:"CacheClusterId"`
				} `xml:"CacheCluster"`
			} `xml:"CacheClusters"`
		} `xml:"DescribeCacheClustersResult"`
	}
	decodeXML(t, resp, &out)
	assert.Len(t, out.Result.CacheClusters.Items, 2)
}

func TestDescribeCacheClusters_byID(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a cluster
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"my-cache"},
		"Engine":         []string{"redis"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DescribeCacheClusters with specific ID
	resp = cacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"my-cache"},
	})

	// Then: correct cluster returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			CacheClusters struct {
				Items []struct {
					CacheClusterId string `xml:"CacheClusterId"`
				} `xml:"CacheCluster"`
			} `xml:"CacheClusters"`
		} `xml:"DescribeCacheClustersResult"`
	}
	decodeXML(t, resp, &out)
	require.Len(t, out.Result.CacheClusters.Items, 1)
	assert.Equal(t, "my-cache", out.Result.CacheClusters.Items[0].CacheClusterId)
}

func TestDescribeCacheClusters_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"nonexistent"},
	})
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "CacheClusterNotFound")
}

// ── DeleteCacheCluster ────────────────────────────────────────────────────────

func TestDeleteCacheCluster_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a cluster
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"del-cluster"},
		"Engine":         []string{"redis"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DeleteCacheCluster
	resp = cacheQuery(t, srv, "DeleteCacheCluster", url.Values{
		"CacheClusterId": []string{"del-cluster"},
	})

	// Then: deleting status returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			CacheCluster struct {
				CacheClusterStatus string `xml:"CacheClusterStatus"`
			} `xml:"CacheCluster"`
		} `xml:"DeleteCacheClusterResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "deleting", out.Result.CacheCluster.CacheClusterStatus)
}

func TestDeleteCacheCluster_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "DeleteCacheCluster", url.Values{
		"CacheClusterId": []string{"no-such"},
	})
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "CacheClusterNotFound")
}

// ── ReplicationGroup ──────────────────────────────────────────────────────────

func TestCreateReplicationGroup_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"test-rg"},
		"ReplicationGroupDescription": []string{"test replication group"},
		"CacheNodeType":               []string{"cache.t3.micro"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			ReplicationGroup struct {
				ReplicationGroupId string `xml:"ReplicationGroupId"`
				Status             string `xml:"Status"`
				ARN                string `xml:"ARN"`
			} `xml:"ReplicationGroup"`
		} `xml:"CreateReplicationGroupResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "test-rg", out.Result.ReplicationGroup.ReplicationGroupId)
	assert.Equal(t, "creating", out.Result.ReplicationGroup.Status)
	assert.Contains(t, out.Result.ReplicationGroup.ARN, "test-rg")
}

// TestCreateReplicationGroup_tagsAppliedAtCreate: Tags passed to
// CreateReplicationGroup (issue #1196, Axis B) must be stored and readable
// via ListTagsForResource immediately, without a follow-up
// AddTagsToResource call.
func TestCreateReplicationGroup_tagsAppliedAtCreate(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"tags-at-create-rg"},
		"ReplicationGroupDescription": []string{"test"},
		"Tags.Tag.1.Key":              []string{"env"},
		"Tags.Tag.1.Value":            []string{"prod"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	list := cacheQuery(t, srv, "ListTagsForResource", url.Values{
		"ResourceName": []string{"arn:aws:elasticache:us-east-1:000000000000:replicationgroup:tags-at-create-rg"},
	})
	defer list.Body.Close()
	helpers.AssertStatus(t, list, http.StatusOK)
	var out struct {
		Result struct {
			TagList struct {
				Tags []struct {
					Key   string `xml:"Key"`
					Value string `xml:"Value"`
				} `xml:"Tag"`
			} `xml:"TagList"`
		} `xml:"ListTagsForResourceResult"`
	}
	decodeXML(t, list, &out)
	if len(out.Result.TagList.Tags) != 1 || out.Result.TagList.Tags[0].Key != "env" || out.Result.TagList.Tags[0].Value != "prod" {
		t.Errorf("Tags: got %v, want env=prod", out.Result.TagList.Tags)
	}
}

// TestCreateReplicationGroup_invalidTagRejected: an invalid tag passed to
// CreateReplicationGroup must be rejected before the group is created.
func TestCreateReplicationGroup_invalidTagRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"tags-rejected-rg"},
		"ReplicationGroupDescription": []string{"test"},
		"Tags.Tag.1.Key":              []string{"aws:reserved"},
		"Tags.Tag.1.Value":            []string{"x"},
	})
	defer resp.Body.Close()
	assertQueryXMLError(t, resp, "InvalidParameterValue")

	desc := cacheQuery(t, srv, "DescribeReplicationGroups", url.Values{
		"ReplicationGroupId": []string{"tags-rejected-rg"},
	})
	defer desc.Body.Close()
	assertQueryXMLError(t, desc, "ReplicationGroupNotFoundFault")
}

func TestCreateCacheCluster_valkey(t *testing.T) {
	// Given: valkey is a valid ElastiCache engine (same Redis-compatible protocol)
	srv := helpers.NewTestServer(t)

	// When: CreateCacheCluster is called with Engine=valkey
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"valkey-cluster"},
		"Engine":         []string{"valkey"},
		"CacheNodeType":  []string{"cache.t3.micro"},
	})

	// Then: 200 with a CacheCluster element showing valkey engine
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			CacheCluster struct {
				CacheClusterId string `xml:"CacheClusterId"`
				Engine         string `xml:"Engine"`
				EngineVersion  string `xml:"EngineVersion"`
			} `xml:"CacheCluster"`
		} `xml:"CreateCacheClusterResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "valkey-cluster", out.Result.CacheCluster.CacheClusterId)
	assert.Equal(t, "valkey", out.Result.CacheCluster.Engine)
	assert.NotEmpty(t, out.Result.CacheCluster.EngineVersion)
}

func TestCreateCacheCluster_memcached_defaults(t *testing.T) {
	// Given: memcached engine
	srv := helpers.NewTestServer(t)

	// When: CreateCacheCluster is called with Engine=memcached (no version)
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"memcached-cluster"},
		"Engine":         []string{"memcached"},
		"CacheNodeType":  []string{"cache.t3.micro"},
	})

	// Then: 200 with engine version defaulted
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			CacheCluster struct {
				Engine        string `xml:"Engine"`
				EngineVersion string `xml:"EngineVersion"`
			} `xml:"CacheCluster"`
		} `xml:"CreateCacheClusterResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "memcached", out.Result.CacheCluster.Engine)
	assert.NotEmpty(t, out.Result.CacheCluster.EngineVersion)
}

func TestDescribeReplicationGroups_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "DescribeReplicationGroups", url.Values{
		"ReplicationGroupId": []string{"no-rg"},
	})
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "ReplicationGroupNotFoundFault")
}

func TestDeleteReplicationGroup_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"del-rg"},
		"ReplicationGroupDescription": []string{"to delete"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = cacheQuery(t, srv, "DeleteReplicationGroup", url.Values{
		"ReplicationGroupId": []string{"del-rg"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			ReplicationGroup struct {
				Status string `xml:"Status"`
			} `xml:"ReplicationGroup"`
		} `xml:"DeleteReplicationGroupResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "deleting", out.Result.ReplicationGroup.Status)
}

// ── ServerlessCache ──────────────────────────────────────────────────────────

func TestCreateServerlessCache_success(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t, helpers.WithRegion("ap-southeast-2"))

	// When: CreateServerlessCache is called with the required AWS parameters
	resp := cacheQuery(t, srv, "CreateServerlessCache", url.Values{
		"ServerlessCacheName": []string{"test-serverless"},
		"Engine":              []string{"redis"},
		"Description":         []string{"serverless redis"},
		"MajorEngineVersion":  []string{"7"},
	})

	// Then: it returns a ServerlessCache shape with endpoint metadata
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			ServerlessCache struct {
				ServerlessCacheName string `xml:"ServerlessCacheName"`
				Description         string `xml:"Description"`
				Status              string `xml:"Status"`
				Engine              string `xml:"Engine"`
				MajorEngineVersion  string `xml:"MajorEngineVersion"`
				FullEngineVersion   string `xml:"FullEngineVersion"`
				ARN                 string `xml:"ARN"`
				Endpoint            struct {
					Address string `xml:"Address"`
					Port    int    `xml:"Port"`
				} `xml:"Endpoint"`
			} `xml:"ServerlessCache"`
		} `xml:"CreateServerlessCacheResult"`
	}
	decodeXML(t, resp, &out)
	cache := out.Result.ServerlessCache
	assert.Equal(t, "test-serverless", cache.ServerlessCacheName)
	assert.Equal(t, "serverless redis", cache.Description)
	assert.Equal(t, "creating", cache.Status)
	assert.Equal(t, "redis", cache.Engine)
	assert.Equal(t, "7", cache.MajorEngineVersion)
	assert.NotEmpty(t, cache.FullEngineVersion)
	assert.Equal(t, "arn:aws:elasticache:ap-southeast-2:000000000000:serverlesscache:test-serverless", cache.ARN)
	assert.NotEmpty(t, cache.Endpoint.Address)
	assert.Equal(t, 6379, cache.Endpoint.Port)
}

func TestDescribeServerlessCaches_byName(t *testing.T) {
	// Given: a serverless cache exists
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateServerlessCache", url.Values{
		"ServerlessCacheName": []string{"describe-serverless"},
		"Engine":              []string{"redis"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DescribeServerlessCaches filters by name
	resp = cacheQuery(t, srv, "DescribeServerlessCaches", url.Values{
		"ServerlessCacheName": []string{"describe-serverless"},
	})

	// Then: the matching serverless cache is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			ServerlessCaches struct {
				Items []struct {
					ServerlessCacheName string `xml:"ServerlessCacheName"`
					Status              string `xml:"Status"`
					Endpoint            struct {
						Address string `xml:"Address"`
						Port    int    `xml:"Port"`
					} `xml:"Endpoint"`
				} `xml:"ServerlessCache"`
			} `xml:"ServerlessCaches"`
		} `xml:"DescribeServerlessCachesResult"`
	}
	decodeXML(t, resp, &out)
	require.Len(t, out.Result.ServerlessCaches.Items, 1)
	assert.Equal(t, "describe-serverless", out.Result.ServerlessCaches.Items[0].ServerlessCacheName)
	assert.NotEmpty(t, out.Result.ServerlessCaches.Items[0].Status)
	assert.NotEmpty(t, out.Result.ServerlessCaches.Items[0].Endpoint.Address)
	assert.Equal(t, 6379, out.Result.ServerlessCaches.Items[0].Endpoint.Port)
}

func TestDeleteServerlessCache_success(t *testing.T) {
	// Given: a serverless cache exists
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateServerlessCache", url.Values{
		"ServerlessCacheName": []string{"delete-serverless"},
		"Engine":              []string{"redis"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DeleteServerlessCache is called
	resp = cacheQuery(t, srv, "DeleteServerlessCache", url.Values{
		"ServerlessCacheName": []string{"delete-serverless"},
	})

	// Then: the serverless cache is marked deleting in the AWS response shape
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			ServerlessCache struct {
				ServerlessCacheName string `xml:"ServerlessCacheName"`
				Status              string `xml:"Status"`
			} `xml:"ServerlessCache"`
		} `xml:"DeleteServerlessCacheResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "delete-serverless", out.Result.ServerlessCache.ServerlessCacheName)
	assert.Equal(t, "deleting", out.Result.ServerlessCache.Status)
}

func TestCreateServerlessCache_metadataFields(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: CreateServerlessCache includes common production metadata
	resp := cacheQuery(t, srv, "CreateServerlessCache", url.Values{
		"ServerlessCacheName":                  []string{"metadata-serverless"},
		"Engine":                               []string{"redis"},
		"CacheUsageLimits.DataStorage.Maximum": []string{"10"},
		"CacheUsageLimits.DataStorage.Unit":    []string{"GB"},
		"CacheUsageLimits.ECPUPerSecond.Maximum": []string{
			"50000",
		},
		"SubnetIds.SubnetId.1":                []string{"subnet-a"},
		"SubnetIds.SubnetId.2":                []string{"subnet-b"},
		"SecurityGroupIds.SecurityGroupId.1":  []string{"sg-a"},
		"SnapshotArnsToRestore.SnapshotArn.1": []string{"arn:aws:elasticache:us-east-1:000000000000:snapshot:seed"},
		"SnapshotRetentionLimit":              []string{"10"},
		"DailySnapshotTime":                   []string{"09:00"},
		"NetworkType":                         []string{"dual_stack"},
		"KmsKeyId":                            []string{"alias/cache-key"},
		"UserGroupId":                         []string{"cache-users"},
	})

	// Then: AWS-documented metadata is serialized in the ServerlessCache shape
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			ServerlessCache struct {
				CacheUsageLimits struct {
					DataStorage struct {
						Maximum int    `xml:"Maximum"`
						Unit    string `xml:"Unit"`
					} `xml:"DataStorage"`
					ECPUPerSecond struct {
						Maximum int `xml:"Maximum"`
					} `xml:"ECPUPerSecond"`
				} `xml:"CacheUsageLimits"`
				SubnetIds struct {
					Items []string `xml:"member"`
				} `xml:"SubnetIds"`
				SecurityGroupIds struct {
					Items []string `xml:"member"`
				} `xml:"SecurityGroupIds"`
				SnapshotArnsToRestore *struct {
					Items []string `xml:"member"`
				} `xml:"SnapshotArnsToRestore"`
				SnapshotRetentionLimit int    `xml:"SnapshotRetentionLimit"`
				DailySnapshotTime      string `xml:"DailySnapshotTime"`
				NetworkType            string `xml:"NetworkType"`
				KmsKeyId               string `xml:"KmsKeyId"`
				UserGroupId            string `xml:"UserGroupId"`
			} `xml:"ServerlessCache"`
		} `xml:"CreateServerlessCacheResult"`
	}
	decodeXML(t, resp, &out)
	cache := out.Result.ServerlessCache
	assert.Equal(t, 10, cache.CacheUsageLimits.DataStorage.Maximum)
	assert.Equal(t, "GB", cache.CacheUsageLimits.DataStorage.Unit)
	assert.Equal(t, 50000, cache.CacheUsageLimits.ECPUPerSecond.Maximum)
	assert.Equal(t, []string{"subnet-a", "subnet-b"}, cache.SubnetIds.Items)
	assert.Equal(t, []string{"sg-a"}, cache.SecurityGroupIds.Items)
	// SnapshotArnsToRestore is a CreateServerlessCacheRequest-only member on
	// real AWS; it does not appear on the ServerlessCache response shape, so
	// it must not round-trip onto the wire here even though the create call
	// accepted it above.
	assert.Nil(t, cache.SnapshotArnsToRestore, "SnapshotArnsToRestore must not be echoed on the ServerlessCache response")
	assert.Equal(t, 10, cache.SnapshotRetentionLimit)
	assert.Equal(t, "09:00", cache.DailySnapshotTime)
	assert.Equal(t, "dual_stack", cache.NetworkType)
	assert.Equal(t, "alias/cache-key", cache.KmsKeyId)
	assert.Equal(t, "cache-users", cache.UserGroupId)
}

func TestDescribeServerlessCaches_pagination(t *testing.T) {
	// Given: three serverless caches exist
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"page-a", "page-b", "page-c"} {
		resp := cacheQuery(t, srv, "CreateServerlessCache", url.Values{
			"ServerlessCacheName": []string{name},
			"Engine":              []string{"redis"},
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}

	// When: DescribeServerlessCaches is called with MaxResults
	resp := cacheQuery(t, srv, "DescribeServerlessCaches", url.Values{"MaxResults": []string{"2"}})

	// Then: the response is paginated with a NextToken
	helpers.AssertStatus(t, resp, http.StatusOK)
	var first struct {
		Result struct {
			NextToken        string `xml:"NextToken"`
			ServerlessCaches struct {
				Items []struct {
					ServerlessCacheName string `xml:"ServerlessCacheName"`
				} `xml:"ServerlessCache"`
			} `xml:"ServerlessCaches"`
		} `xml:"DescribeServerlessCachesResult"`
	}
	decodeXML(t, resp, &first)
	require.Len(t, first.Result.ServerlessCaches.Items, 2)
	assert.NotEmpty(t, first.Result.NextToken)

	// When: the next page is requested
	resp = cacheQuery(t, srv, "DescribeServerlessCaches", url.Values{
		"MaxResults": []string{"2"},
		"NextToken":  []string{first.Result.NextToken},
	})

	// Then: the final item is returned with no further token
	helpers.AssertStatus(t, resp, http.StatusOK)
	var second struct {
		Result struct {
			NextToken        string `xml:"NextToken"`
			ServerlessCaches struct {
				Items []struct {
					ServerlessCacheName string `xml:"ServerlessCacheName"`
				} `xml:"ServerlessCache"`
			} `xml:"ServerlessCaches"`
		} `xml:"DescribeServerlessCachesResult"`
	}
	decodeXML(t, resp, &second)
	require.Len(t, second.Result.ServerlessCaches.Items, 1)
	assert.Empty(t, second.Result.NextToken)
}

func TestModifyServerlessCache_success(t *testing.T) {
	// Given: a serverless cache exists
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateServerlessCache", url.Values{
		"ServerlessCacheName": []string{"modify-serverless"},
		"Engine":              []string{"redis"},
		"Description":         []string{"before"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: ModifyServerlessCache updates mutable metadata
	resp = cacheQuery(t, srv, "ModifyServerlessCache", url.Values{
		"ServerlessCacheName":                    []string{"modify-serverless"},
		"Description":                            []string{"after"},
		"DailySnapshotTime":                      []string{"11:00"},
		"SnapshotRetentionLimit":                 []string{"12"},
		"SecurityGroupIds.SecurityGroupId.1":     []string{"sg-updated"},
		"CacheUsageLimits.DataStorage.Maximum":   []string{"20"},
		"CacheUsageLimits.DataStorage.Unit":      []string{"GB"},
		"CacheUsageLimits.ECPUPerSecond.Maximum": []string{"60000"},
	})

	// Then: modified fields are returned and persisted
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			ServerlessCache struct {
				Description            string `xml:"Description"`
				Status                 string `xml:"Status"`
				DailySnapshotTime      string `xml:"DailySnapshotTime"`
				SnapshotRetentionLimit int    `xml:"SnapshotRetentionLimit"`
				SecurityGroupIds       struct {
					Items []string `xml:"member"`
				} `xml:"SecurityGroupIds"`
				CacheUsageLimits struct {
					DataStorage struct {
						Maximum int `xml:"Maximum"`
					} `xml:"DataStorage"`
					ECPUPerSecond struct {
						Maximum int `xml:"Maximum"`
					} `xml:"ECPUPerSecond"`
				} `xml:"CacheUsageLimits"`
			} `xml:"ServerlessCache"`
		} `xml:"ModifyServerlessCacheResult"`
	}
	decodeXML(t, resp, &out)
	cache := out.Result.ServerlessCache
	assert.Equal(t, "after", cache.Description)
	assert.Equal(t, "modifying", cache.Status)
	assert.Equal(t, "11:00", cache.DailySnapshotTime)
	assert.Equal(t, 12, cache.SnapshotRetentionLimit)
	assert.Equal(t, []string{"sg-updated"}, cache.SecurityGroupIds.Items)
	assert.Equal(t, 20, cache.CacheUsageLimits.DataStorage.Maximum)
	assert.Equal(t, 60000, cache.CacheUsageLimits.ECPUPerSecond.Maximum)
}

func TestDeleteServerlessCache_finalSnapshotName(t *testing.T) {
	// Given: a serverless cache exists
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateServerlessCache", url.Values{
		"ServerlessCacheName": []string{"final-snapshot-serverless"},
		"Engine":              []string{"redis"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DeleteServerlessCache includes FinalSnapshotName
	resp = cacheQuery(t, srv, "DeleteServerlessCache", url.Values{
		"ServerlessCacheName": []string{"final-snapshot-serverless"},
		"FinalSnapshotName":   []string{"final-local-snapshot"},
	})

	// Then: the delete path accepts the documented optional parameter
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ── CacheSubnetGroup ──────────────────────────────────────────────────────────

func TestCreateCacheSubnetGroup_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateCacheSubnetGroup", url.Values{
		"CacheSubnetGroupName":         []string{"my-subnet-group"},
		"CacheSubnetGroupDescription":  []string{"test"},
		"SubnetIds.SubnetIdentifier.1": []string{"subnet-aabbccdd"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			CacheSubnetGroup struct {
				CacheSubnetGroupName string `xml:"CacheSubnetGroupName"`
			} `xml:"CacheSubnetGroup"`
		} `xml:"CreateCacheSubnetGroupResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "my-subnet-group", out.Result.CacheSubnetGroup.CacheSubnetGroupName)
}

func TestDeleteCacheSubnetGroup_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "DeleteCacheSubnetGroup", url.Values{
		"CacheSubnetGroupName": []string{"nonexistent"},
	})
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "CacheSubnetGroupNotFoundFault")
}

// ── Stub operations ────────────────────────────────────────────────────────────

func TestUnimplementedAction_returns501(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "RebootCacheCluster", url.Values{
		"CacheClusterId": []string{"some-cluster"},
	})
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
}

// ── DescribeCacheParameters ───────────────────────────────────────────────────

func TestDescribeCacheParameters_success(t *testing.T) {
	// Given: a redis7 parameter group
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"my-redis7-params"},
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"test"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DescribeCacheParameters is called
	resp = cacheQuery(t, srv, "DescribeCacheParameters", url.Values{
		"CacheParameterGroupName": []string{"my-redis7-params"},
	})

	// Then: 200 with a non-empty parameter list
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		XMLName xml.Name `xml:"DescribeCacheParametersResponse"`
		Result  struct {
			Parameters struct {
				Items []struct {
					ParameterName string `xml:"ParameterName"`
					Source        string `xml:"Source"`
					DataType      string `xml:"DataType"`
					IsModifiable  bool   `xml:"IsModifiable"`
				} `xml:"Parameter"`
			} `xml:"Parameters"`
		} `xml:"DescribeCacheParametersResult"`
	}
	decodeXML(t, resp, &out)
	assert.NotEmpty(t, out.Result.Parameters.Items)
	// Spot-check a well-known redis parameter
	var found bool
	for _, p := range out.Result.Parameters.Items {
		if p.ParameterName == "maxmemory-policy" {
			found = true
			assert.Equal(t, "string", p.DataType)
			assert.NotEmpty(t, p.Source)
		}
	}
	assert.True(t, found, "expected maxmemory-policy in redis7 parameter list")
}

func TestDescribeCacheParameters_memcached(t *testing.T) {
	// Given: a memcached1.6 parameter group
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"my-memcached-params"},
		"CacheParameterGroupFamily": []string{"memcached1.6"},
		"Description":               []string{"test"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DescribeCacheParameters is called
	resp = cacheQuery(t, srv, "DescribeCacheParameters", url.Values{
		"CacheParameterGroupName": []string{"my-memcached-params"},
	})

	// Then: 200 with memcached-specific parameters
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			Parameters struct {
				Items []struct {
					ParameterName string `xml:"ParameterName"`
				} `xml:"Parameter"`
			} `xml:"Parameters"`
		} `xml:"DescribeCacheParametersResult"`
	}
	decodeXML(t, resp, &out)
	assert.NotEmpty(t, out.Result.Parameters.Items)
	var found bool
	for _, p := range out.Result.Parameters.Items {
		if p.ParameterName == "max_item_size" {
			found = true
		}
	}
	assert.True(t, found, "expected max_item_size in memcached parameter list")
}

func TestDescribeCacheParameters_sourceFilter_user_returnsEmpty(t *testing.T) {
	// Given: a parameter group (no user-modified params in an emulator)
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"filter-test"},
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"test"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DescribeCacheParameters with Source=user
	resp = cacheQuery(t, srv, "DescribeCacheParameters", url.Values{
		"CacheParameterGroupName": []string{"filter-test"},
		"Source":                  []string{"user"},
	})

	// Then: empty list (no user-modified params)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			Parameters struct {
				Items []struct{} `xml:"Parameter"`
			} `xml:"Parameters"`
		} `xml:"DescribeCacheParametersResult"`
	}
	decodeXML(t, resp, &out)
	assert.Empty(t, out.Result.Parameters.Items)
}

func TestDescribeCacheParameters_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "DescribeCacheParameters", url.Values{
		"CacheParameterGroupName": []string{"no-such-group"},
	})
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "CacheParameterGroupNotFound")
}

func TestDescribeCacheParameters_missingName(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := cacheQuery(t, srv, "DescribeCacheParameters", nil)
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

// ── CreateCacheParameterGroup ─────────────────────────────────────────────────

func TestCreateCacheParameterGroup_success(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: CreateCacheParameterGroup is called with valid params
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"my-param-group"},
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"test group"},
	})

	// Then: 200 with a CacheParameterGroup element
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		XMLName xml.Name `xml:"CreateCacheParameterGroupResponse"`
		Result  struct {
			CacheParameterGroup struct {
				CacheParameterGroupName   string `xml:"CacheParameterGroupName"`
				CacheParameterGroupFamily string `xml:"CacheParameterGroupFamily"`
				Description               string `xml:"Description"`
				ARN                       string `xml:"ARN"`
			} `xml:"CacheParameterGroup"`
		} `xml:"CreateCacheParameterGroupResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "my-param-group", out.Result.CacheParameterGroup.CacheParameterGroupName)
	assert.Equal(t, "redis7", out.Result.CacheParameterGroup.CacheParameterGroupFamily)
	assert.Equal(t, "test group", out.Result.CacheParameterGroup.Description)
	assert.NotEmpty(t, out.Result.CacheParameterGroup.ARN)
	assert.Contains(t, out.Result.CacheParameterGroup.ARN, "my-param-group")
}

func TestCreateCacheParameterGroup_duplicate(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a parameter group already exists
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"dup-group"},
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"first"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: CreateCacheParameterGroup is called again with the same name
	resp = cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"dup-group"},
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"second"},
	})

	// Then: CacheParameterGroupAlreadyExists error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "CacheParameterGroupAlreadyExists")
}

func TestCreateCacheParameterGroup_missing_name(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: no CacheParameterGroupName
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"test"},
	})
	// Then: validation error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

// ── DescribeCacheParameterGroups ──────────────────────────────────────────────

func TestDescribeCacheParameterGroups_all(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: two parameter groups
	for _, name := range []string{"group-a", "group-b"} {
		resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
			"CacheParameterGroupName":   []string{name},
			"CacheParameterGroupFamily": []string{"redis7"},
			"Description":               []string{"test"},
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}

	// When: DescribeCacheParameterGroups without filter
	resp := cacheQuery(t, srv, "DescribeCacheParameterGroups", nil)

	// Then: both returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		XMLName xml.Name `xml:"DescribeCacheParameterGroupsResponse"`
		Result  struct {
			CacheParameterGroups struct {
				Items []struct {
					CacheParameterGroupName string `xml:"CacheParameterGroupName"`
				} `xml:"CacheParameterGroup"`
			} `xml:"CacheParameterGroups"`
		} `xml:"DescribeCacheParameterGroupsResult"`
	}
	decodeXML(t, resp, &out)
	assert.Len(t, out.Result.CacheParameterGroups.Items, 2)
}

func TestDescribeCacheParameterGroups_filter_by_name(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a parameter group
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"specific-group"},
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"test"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DescribeCacheParameterGroups filtered by name
	resp = cacheQuery(t, srv, "DescribeCacheParameterGroups", url.Values{
		"CacheParameterGroupName": []string{"specific-group"},
	})

	// Then: only that group returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			CacheParameterGroups struct {
				Items []struct {
					CacheParameterGroupName   string `xml:"CacheParameterGroupName"`
					CacheParameterGroupFamily string `xml:"CacheParameterGroupFamily"`
				} `xml:"CacheParameterGroup"`
			} `xml:"CacheParameterGroups"`
		} `xml:"DescribeCacheParameterGroupsResult"`
	}
	decodeXML(t, resp, &out)
	assert.Len(t, out.Result.CacheParameterGroups.Items, 1)
	assert.Equal(t, "specific-group", out.Result.CacheParameterGroups.Items[0].CacheParameterGroupName)
	assert.Equal(t, "redis7", out.Result.CacheParameterGroups.Items[0].CacheParameterGroupFamily)
}

func TestDescribeCacheParameterGroups_not_found(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: describe a non-existent group
	resp := cacheQuery(t, srv, "DescribeCacheParameterGroups", url.Values{
		"CacheParameterGroupName": []string{"does-not-exist"},
	})
	// Then: CacheParameterGroupNotFound error
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "CacheParameterGroupNotFound")
}

// ── DeleteCacheParameterGroup ─────────────────────────────────────────────────

func TestDeleteCacheParameterGroup_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a parameter group
	resp := cacheQuery(t, srv, "CreateCacheParameterGroup", url.Values{
		"CacheParameterGroupName":   []string{"to-delete"},
		"CacheParameterGroupFamily": []string{"redis7"},
		"Description":               []string{"test"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: DeleteCacheParameterGroup is called
	resp = cacheQuery(t, srv, "DeleteCacheParameterGroup", url.Values{
		"CacheParameterGroupName": []string{"to-delete"},
	})

	// Then: 200 empty response
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// And: the group is gone
	resp = cacheQuery(t, srv, "DescribeCacheParameterGroups", url.Values{
		"CacheParameterGroupName": []string{"to-delete"},
	})
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "CacheParameterGroupNotFound")
}

func TestDeleteCacheParameterGroup_not_found(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: delete a non-existent group
	resp := cacheQuery(t, srv, "DeleteCacheParameterGroup", url.Values{
		"CacheParameterGroupName": []string{"ghost"},
	})
	// Then: CacheParameterGroupNotFound error
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "CacheParameterGroupNotFound")
}

func TestDeleteCacheParameterGroup_missing_name(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: no CacheParameterGroupName
	resp := cacheQuery(t, srv, "DeleteCacheParameterGroup", nil)
	// Then: validation error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

// ── ModifyCacheCluster ────────────────────────────────────────────────────────

func TestModifyCacheCluster_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a cluster
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"modifiable-cluster"},
		"Engine":         []string{"redis"},
		"CacheNodeType":  []string{"cache.t3.micro"},
		"NumCacheNodes":  []string{"1"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: ModifyCacheCluster updates the node type
	resp = cacheQuery(t, srv, "ModifyCacheCluster", url.Values{
		"CacheClusterId":   []string{"modifiable-cluster"},
		"CacheNodeType":    []string{"cache.t3.small"},
		"ApplyImmediately": []string{"true"},
	})

	// Then: 200 with modifying status and updated node type
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		XMLName xml.Name `xml:"ModifyCacheClusterResponse"`
		Result  struct {
			CacheCluster struct {
				CacheClusterId     string `xml:"CacheClusterId"`
				CacheClusterStatus string `xml:"CacheClusterStatus"`
				CacheNodeType      string `xml:"CacheNodeType"`
			} `xml:"CacheCluster"`
		} `xml:"ModifyCacheClusterResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "modifiable-cluster", out.Result.CacheCluster.CacheClusterId)
	assert.Equal(t, "modifying", out.Result.CacheCluster.CacheClusterStatus)
	assert.Equal(t, "cache.t3.small", out.Result.CacheCluster.CacheNodeType)
}

func TestModifyCacheCluster_not_found(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: modify a non-existent cluster
	resp := cacheQuery(t, srv, "ModifyCacheCluster", url.Values{
		"CacheClusterId": []string{"ghost"},
		"CacheNodeType":  []string{"cache.t3.small"},
	})
	// Then: CacheClusterNotFound error
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "CacheClusterNotFound")
}

func TestModifyCacheCluster_missing_id(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: no CacheClusterId
	resp := cacheQuery(t, srv, "ModifyCacheCluster", url.Values{
		"CacheNodeType": []string{"cache.t3.small"},
	})
	// Then: validation error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

// ── ModifyReplicationGroup ────────────────────────────────────────────────────

func TestModifyReplicationGroup_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// Given: a replication group
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"modifiable-rg"},
		"ReplicationGroupDescription": []string{"original"},
		"CacheNodeType":               []string{"cache.t3.micro"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: ModifyReplicationGroup updates description and node type
	resp = cacheQuery(t, srv, "ModifyReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"modifiable-rg"},
		"ReplicationGroupDescription": []string{"updated"},
		"CacheNodeType":               []string{"cache.t3.small"},
		"ApplyImmediately":            []string{"true"},
	})

	// Then: 200 with modifying status and updated fields
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		XMLName xml.Name `xml:"ModifyReplicationGroupResponse"`
		Result  struct {
			ReplicationGroup struct {
				ReplicationGroupId string `xml:"ReplicationGroupId"`
				Status             string `xml:"Status"`
				Description        string `xml:"Description"`
				CacheNodeType      string `xml:"CacheNodeType"`
			} `xml:"ReplicationGroup"`
		} `xml:"ModifyReplicationGroupResult"`
	}
	decodeXML(t, resp, &out)
	assert.Equal(t, "modifiable-rg", out.Result.ReplicationGroup.ReplicationGroupId)
	assert.Equal(t, "modifying", out.Result.ReplicationGroup.Status)
	assert.Equal(t, "updated", out.Result.ReplicationGroup.Description)
	assert.Equal(t, "cache.t3.small", out.Result.ReplicationGroup.CacheNodeType)
}

func TestModifyReplicationGroup_not_found(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: modify a non-existent replication group
	resp := cacheQuery(t, srv, "ModifyReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"ghost-rg"},
		"ReplicationGroupDescription": []string{"updated"},
	})
	// Then: ReplicationGroupNotFoundFault error
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "ReplicationGroupNotFoundFault")
}

func TestModifyReplicationGroup_missing_id(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// When: no ReplicationGroupId
	resp := cacheQuery(t, srv, "ModifyReplicationGroup", url.Values{
		"ReplicationGroupDescription": []string{"updated"},
	})
	// Then: validation error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}
