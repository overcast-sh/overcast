// create_validation_test.go — the scenario matrix for CreateCacheCluster and
// CreateReplicationGroup (issue #144): identifier constraints, engine and node
// count rules, the memcached-only placement parameters, the lifecycle statuses
// a caller observes, and the Query XML shapes the two operations return.
//
// Every constraint asserted here is quoted from the pinned ElastiCache model
// (models/aws/VERSION, api-models-aws elasticache-2015-02-02) — the member
// documentation on CreateCacheClusterMessage and CreateReplicationGroupMessage,
// the CacheCluster/ReplicationGroup output shapes, and the awsQueryError codes
// on the fault shapes. No real AWS account was called.
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

// ── XML projections ───────────────────────────────────────────────────────────

type xmlEndpointProjection struct {
	Address string `xml:"Address"`
	Port    int    `xml:"Port"`
}

type xmlCacheNodeProjection struct {
	CacheNodeId              string                 `xml:"CacheNodeId"`
	CacheNodeStatus          string                 `xml:"CacheNodeStatus"`
	CacheNodeCreateTime      string                 `xml:"CacheNodeCreateTime"`
	Endpoint                 *xmlEndpointProjection `xml:"Endpoint"`
	ParameterGroupStatus     string                 `xml:"ParameterGroupStatus"`
	CustomerAvailabilityZone string                 `xml:"CustomerAvailabilityZone"`
}

type xmlCacheClusterProjection struct {
	CacheClusterId            string                 `xml:"CacheClusterId"`
	CacheClusterStatus        string                 `xml:"CacheClusterStatus"`
	CacheClusterCreateTime    string                 `xml:"CacheClusterCreateTime"`
	CacheNodeType             string                 `xml:"CacheNodeType"`
	Engine                    string                 `xml:"Engine"`
	EngineVersion             string                 `xml:"EngineVersion"`
	NumCacheNodes             int                    `xml:"NumCacheNodes"`
	PreferredAvailabilityZone string                 `xml:"PreferredAvailabilityZone"`
	ReplicationGroupId        string                 `xml:"ReplicationGroupId"`
	ARN                       string                 `xml:"ARN"`
	ConfigurationEndpoint     *xmlEndpointProjection `xml:"ConfigurationEndpoint"`
	CacheParameterGroup       *struct {
		CacheParameterGroupName string `xml:"CacheParameterGroupName"`
		ParameterApplyStatus    string `xml:"ParameterApplyStatus"`
	} `xml:"CacheParameterGroup"`
	// CacheParameterGroupNameElement is the invented flat element the shape
	// used to carry. AWS models no such member on CacheCluster; it must be
	// gone.
	CacheParameterGroupNameElement string `xml:"CacheParameterGroupName"`
	CacheNodes                     struct {
		Items []xmlCacheNodeProjection `xml:"CacheNode"`
	} `xml:"CacheNodes"`
}

type xmlReplicationGroupProjection struct {
	ReplicationGroupId         string                 `xml:"ReplicationGroupId"`
	Description                string                 `xml:"Description"`
	Status                     string                 `xml:"Status"`
	ReplicationGroupCreateTime string                 `xml:"ReplicationGroupCreateTime"`
	ARN                        string                 `xml:"ARN"`
	AutomaticFailover          string                 `xml:"AutomaticFailover"`
	MultiAZ                    string                 `xml:"MultiAZ"`
	CacheNodeType              string                 `xml:"CacheNodeType"`
	Engine                     string                 `xml:"Engine"`
	ClusterEnabled             string                 `xml:"ClusterEnabled"`
	ConfigurationEndpoint      *xmlEndpointProjection `xml:"ConfigurationEndpoint"`
	MemberClusters             struct {
		Items []string `xml:"ClusterId"`
	} `xml:"MemberClusters"`
	NodeGroups struct {
		Items []struct {
			NodeGroupId      string                 `xml:"NodeGroupId"`
			Status           string                 `xml:"Status"`
			PrimaryEndpoint  *xmlEndpointProjection `xml:"PrimaryEndpoint"`
			ReaderEndpoint   *xmlEndpointProjection `xml:"ReaderEndpoint"`
			NodeGroupMembers struct {
				Items []struct {
					CacheClusterId string                 `xml:"CacheClusterId"`
					CacheNodeId    string                 `xml:"CacheNodeId"`
					CurrentRole    string                 `xml:"CurrentRole"`
					ReadEndpoint   *xmlEndpointProjection `xml:"ReadEndpoint"`
				} `xml:"NodeGroupMember"`
			} `xml:"NodeGroupMembers"`
		} `xml:"NodeGroup"`
	} `xml:"NodeGroups"`
}

func createdCluster(t *testing.T, resp *http.Response) xmlCacheClusterProjection {
	t.Helper()
	var out struct {
		XMLName xml.Name `xml:"CreateCacheClusterResponse"`
		Result  struct {
			CacheCluster xmlCacheClusterProjection `xml:"CacheCluster"`
		} `xml:"CreateCacheClusterResult"`
	}
	decodeXML(t, resp, &out)
	return out.Result.CacheCluster
}

func describedClusters(t *testing.T, resp *http.Response) []xmlCacheClusterProjection {
	t.Helper()
	var out struct {
		Result struct {
			CacheClusters struct {
				Items []xmlCacheClusterProjection `xml:"CacheCluster"`
			} `xml:"CacheClusters"`
		} `xml:"DescribeCacheClustersResult"`
	}
	decodeXML(t, resp, &out)
	return out.Result.CacheClusters.Items
}

func createdGroup(t *testing.T, resp *http.Response) xmlReplicationGroupProjection {
	t.Helper()
	var out struct {
		Result struct {
			ReplicationGroup xmlReplicationGroupProjection `xml:"ReplicationGroup"`
		} `xml:"CreateReplicationGroupResult"`
	}
	decodeXML(t, resp, &out)
	return out.Result.ReplicationGroup
}

func describedGroups(t *testing.T, resp *http.Response) []xmlReplicationGroupProjection {
	t.Helper()
	var out struct {
		Result struct {
			ReplicationGroups struct {
				Items []xmlReplicationGroupProjection `xml:"ReplicationGroup"`
			} `xml:"ReplicationGroups"`
		} `xml:"DescribeReplicationGroupsResult"`
	}
	decodeXML(t, resp, &out)
	return out.Result.ReplicationGroups.Items
}

// ── CacheClusterId constraints ────────────────────────────────────────────────
//
// "A name must contain from 1 to 50 alphanumeric characters or hyphens. The
// first character must be a letter. A name cannot end with a hyphen or contain
// two consecutive hyphens."

func TestCreateCacheCluster_identifierConstraints(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"leading digit", "1cache"},
		{"leading hyphen", "-cache"},
		{"trailing hyphen", "cache-"},
		{"consecutive hyphens", "my--cache"},
		{"underscore", "my_cache"},
		{"dot", "my.cache"},
		{"space", "my cache"},
		{"too long", "c" + strings.Repeat("a", 50)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the ElastiCache service
			srv := helpers.NewTestServer(t)

			// When: CreateCacheCluster names a cluster AWS would refuse
			resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
				"CacheClusterId": []string{tc.id},
				"Engine":         []string{"redis"},
			})
			defer resp.Body.Close()

			// Then: InvalidParameterValue
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertQueryXMLError(t, resp, "InvalidParameterValue")
		})
	}
}

func TestCreateCacheCluster_identifierAtMaximumLength(t *testing.T) {
	// Given: an identifier of exactly 50 characters — the documented maximum
	srv := helpers.NewTestServer(t)
	id := "c" + strings.Repeat("a", 49)

	// When: CreateCacheCluster is called with it
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{id},
		"Engine":         []string{"redis"},
	})
	defer resp.Body.Close()

	// Then: accepted
	helpers.AssertStatus(t, resp, http.StatusOK)
	assert.Equal(t, id, createdCluster(t, resp).CacheClusterId)
}

func TestCreateCacheCluster_identifierStoredLowercase(t *testing.T) {
	// Given: "This parameter is stored as a lowercase string."
	srv := helpers.NewTestServer(t)

	// When: a mixed-case identifier is used
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"MyMixedCase"},
		"Engine":         []string{"redis"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the id and its ARN come back lowercased
	created := createdCluster(t, resp)
	assert.Equal(t, "mymixedcase", created.CacheClusterId)
	assert.Contains(t, created.ARN, ":cluster:mymixedcase")

	// And: the cluster is addressable under either spelling
	byOriginal := cacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"MyMixedCase"},
	})
	defer byOriginal.Body.Close()
	helpers.AssertStatus(t, byOriginal, http.StatusOK)
	require.Len(t, describedClusters(t, byOriginal), 1)

	byCanonical := cacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"mymixedcase"},
	})
	defer byCanonical.Body.Close()
	helpers.AssertStatus(t, byCanonical, http.StatusOK)
	require.Len(t, describedClusters(t, byCanonical), 1)
}

// ── NumCacheNodes ─────────────────────────────────────────────────────────────
//
// "For clusters running Valkey or Redis OSS, this value must be 1. For clusters
// running Memcached, this value must be between 1 and 40."

func TestCreateCacheCluster_redisNodeCountMustBeOne(t *testing.T) {
	for _, engine := range []string{"redis", "valkey"} {
		t.Run(engine, func(t *testing.T) {
			// Given: the ElastiCache service
			srv := helpers.NewTestServer(t)

			// When: a Redis-protocol cluster asks for more than one node
			resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
				"CacheClusterId": []string{"multi-node"},
				"Engine":         []string{engine},
				"NumCacheNodes":  []string{"2"},
			})
			defer resp.Body.Close()

			// Then: InvalidParameterValue
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertQueryXMLError(t, resp, "InvalidParameterValue")
		})
	}
}

func TestCreateCacheCluster_memcachedNodeCountRange(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: a Memcached cluster asks for 41 nodes
	tooMany := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"too-many"},
		"Engine":         []string{"memcached"},
		"NumCacheNodes":  []string{"41"},
	})
	defer tooMany.Body.Close()

	// Then: InvalidParameterValue
	helpers.AssertStatus(t, tooMany, http.StatusBadRequest)
	assertQueryXMLError(t, tooMany, "InvalidParameterValue")

	// And: 40 is accepted, with the count echoed and a node per position
	ok := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"forty"},
		"Engine":         []string{"memcached"},
		"NumCacheNodes":  []string{"40"},
	})
	defer ok.Body.Close()
	helpers.AssertStatus(t, ok, http.StatusOK)
	created := createdCluster(t, ok)
	assert.Equal(t, 40, created.NumCacheNodes)
	assert.Len(t, created.CacheNodes.Items, 40)
}

// ── AZMode and PreferredAvailabilityZones ─────────────────────────────────────
//
// AZMode: "single-az | cross-az", and "This parameter is only supported for
// Memcached clusters." PreferredAvailabilityZones: "This option is only
// supported on Memcached... The number of Availability Zones listed must equal
// the value of NumCacheNodes."

func TestCreateCacheCluster_azModeInvalidValue(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: AZMode is not one of the two modelled enum values
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"az-bad"},
		"Engine":         []string{"memcached"},
		"AZMode":         []string{"multi-az"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterValue
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

func TestCreateCacheCluster_azModeRejectedForRedis(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: AZMode is supplied for a Redis cluster
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"az-redis"},
		"Engine":         []string{"redis"},
		"AZMode":         []string{"cross-az"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterCombination
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterCombination")
}

func TestCreateCacheCluster_preferredAvailabilityZonesRejectedForRedis(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: PreferredAvailabilityZones is supplied for a Redis cluster
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"paz-redis"},
		"Engine":         []string{"redis"},
		"PreferredAvailabilityZones.PreferredAvailabilityZone.1": []string{"us-east-1a"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterCombination
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterCombination")
}

func TestCreateCacheCluster_preferredAvailabilityZonesMustMatchNodeCount(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: two zones are listed for a three-node Memcached cluster
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"paz-mismatch"},
		"Engine":         []string{"memcached"},
		"NumCacheNodes":  []string{"3"},
		"PreferredAvailabilityZones.PreferredAvailabilityZone.1": []string{"us-east-1a"},
		"PreferredAvailabilityZones.PreferredAvailabilityZone.2": []string{"us-east-1b"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterCombination
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterCombination")
}

func TestCreateCacheCluster_preferredAvailabilityZonesPlaceNodes(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: a two-node Memcached cluster lists a zone per node
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"paz-ok"},
		"Engine":         []string{"memcached"},
		"AZMode":         []string{"cross-az"},
		"NumCacheNodes":  []string{"2"},
		"PreferredAvailabilityZones.PreferredAvailabilityZone.1": []string{"us-east-1a"},
		"PreferredAvailabilityZones.PreferredAvailabilityZone.2": []string{"us-east-1b"},
	})
	defer resp.Body.Close()

	// Then: each node carries its own CustomerAvailabilityZone, and the
	// cluster reports the literal "Multiple" AWS documents for nodes that are
	// not all in one zone
	helpers.AssertStatus(t, resp, http.StatusOK)
	created := createdCluster(t, resp)
	require.Len(t, created.CacheNodes.Items, 2)
	assert.Equal(t, "us-east-1a", created.CacheNodes.Items[0].CustomerAvailabilityZone)
	assert.Equal(t, "us-east-1b", created.CacheNodes.Items[1].CustomerAvailabilityZone)
	assert.Equal(t, "Multiple", created.PreferredAvailabilityZone)
}

func TestCreateCacheCluster_crossAZSpreadsWithoutAnExplicitZoneList(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: a multi-node Memcached cluster asks for cross-az and names no
	// zones, which is AWS picking them itself
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"cross-az-spread"},
		"Engine":         []string{"memcached"},
		"AZMode":         []string{"cross-az"},
		"NumCacheNodes":  []string{"3"},
	})
	defer resp.Body.Close()

	// Then: PreferredAvailabilityZone is the "Multiple" AWS documents for a
	// cluster whose nodes are not all in one zone
	helpers.AssertStatus(t, resp, http.StatusOK)
	assert.Equal(t, "Multiple", createdCluster(t, resp).PreferredAvailabilityZone)
}

func TestCreateCacheCluster_singleAZKeepsTheNamedZone(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: a single-az Memcached cluster names its zone
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId":            []string{"single-az"},
		"Engine":                    []string{"memcached"},
		"AZMode":                    []string{"single-az"},
		"NumCacheNodes":             []string{"2"},
		"PreferredAvailabilityZone": []string{"us-east-1a"},
	})
	defer resp.Body.Close()

	// Then: the zone is reported as itself, not as "Multiple"
	helpers.AssertStatus(t, resp, http.StatusOK)
	created := createdCluster(t, resp)
	assert.Equal(t, "us-east-1a", created.PreferredAvailabilityZone)
	require.Len(t, created.CacheNodes.Items, 2)
	assert.Equal(t, "us-east-1a", created.CacheNodes.Items[1].CustomerAvailabilityZone)
}

// ── ReplicationGroupId on CreateCacheCluster ──────────────────────────────────
//
// "The ID of the replication group to which this cluster should belong... This
// parameter is only valid if the Engine parameter is redis."
// ReplicationGroupNotFoundFault is one of the operation's modelled errors.

func TestCreateCacheCluster_unknownReplicationGroup(t *testing.T) {
	// Given: no replication group by that name
	srv := helpers.NewTestServer(t)

	// When: a cluster asks to join it
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId":     []string{"orphan-replica"},
		"Engine":             []string{"redis"},
		"ReplicationGroupId": []string{"no-such-group"},
	})
	defer resp.Body.Close()

	// Then: ReplicationGroupNotFoundFault, not a cluster pointing at nothing
	assertQueryXMLError(t, resp, "ReplicationGroupNotFoundFault")

	gone := cacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"orphan-replica"},
	})
	defer gone.Body.Close()
	assertQueryXMLError(t, gone, "CacheClusterNotFound")
}

func TestCreateCacheCluster_joinsExistingReplicationGroup(t *testing.T) {
	// Given: a replication group
	srv := helpers.NewTestServer(t)
	rg := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"join-rg"},
		"ReplicationGroupDescription": []string{"group"},
	})
	defer rg.Body.Close()
	helpers.AssertStatus(t, rg, http.StatusOK)

	// When: a cluster is created as a read replica of it
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId":     []string{"join-replica"},
		"Engine":             []string{"redis"},
		"ReplicationGroupId": []string{"join-rg"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	assert.Equal(t, "join-rg", createdCluster(t, resp).ReplicationGroupId)

	// Then: the group lists it as a member
	desc := cacheQuery(t, srv, "DescribeReplicationGroups", url.Values{
		"ReplicationGroupId": []string{"join-rg"},
	})
	defer desc.Body.Close()
	groups := describedGroups(t, desc)
	require.Len(t, groups, 1)
	assert.Contains(t, groups[0].MemberClusters.Items, "join-replica")
}

func TestCreateCacheCluster_replicationGroupIdRejectedForMemcached(t *testing.T) {
	// Given: a replication group
	srv := helpers.NewTestServer(t)
	rg := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"mc-rg"},
		"ReplicationGroupDescription": []string{"group"},
	})
	defer rg.Body.Close()
	helpers.AssertStatus(t, rg, http.StatusOK)

	// When: a Memcached cluster asks to join it
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId":     []string{"mc-replica"},
		"Engine":             []string{"memcached"},
		"ReplicationGroupId": []string{"mc-rg"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterCombination
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterCombination")
}

// ── CacheCluster XML shape ────────────────────────────────────────────────────

func TestCreateCacheCluster_redisXMLShape(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: a Redis cluster is created
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId":          []string{"shape-redis"},
		"Engine":                  []string{"redis"},
		"CacheParameterGroupName": []string{"default.redis7"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	created := createdCluster(t, resp)

	// Then: the modelled members are present and the invented one is gone.
	// AWS leaves ConfigurationEndpoint null for Redis — the node's own
	// Endpoint is what a client dials.
	assert.NotEmpty(t, created.CacheClusterCreateTime, "CacheClusterCreateTime")
	assert.Nil(t, created.ConfigurationEndpoint, "ConfigurationEndpoint is Memcached-only on AWS")
	require.NotNil(t, created.CacheParameterGroup, "CacheParameterGroup")
	assert.Equal(t, "default.redis7", created.CacheParameterGroup.CacheParameterGroupName)
	assert.Equal(t, "in-sync", created.CacheParameterGroup.ParameterApplyStatus)
	assert.Empty(t, created.CacheParameterGroupNameElement,
		"CacheCluster has no flat CacheParameterGroupName member in the AWS model")

	require.Len(t, created.CacheNodes.Items, 1)
	node := created.CacheNodes.Items[0]
	assert.Equal(t, "0001", node.CacheNodeId)
	assert.Equal(t, "creating", node.CacheNodeStatus)
	assert.NotEmpty(t, node.CacheNodeCreateTime)
	assert.Equal(t, "in-sync", node.ParameterGroupStatus)
	require.NotNil(t, node.Endpoint)
	assert.NotEmpty(t, node.Endpoint.Address)
	assert.Equal(t, 6379, node.Endpoint.Port)
}

func TestCreateCacheCluster_memcachedXMLShape(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: a two-node Memcached cluster is created
	resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"shape-mc"},
		"Engine":         []string{"memcached"},
		"NumCacheNodes":  []string{"2"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	created := createdCluster(t, resp)

	// Then: Memcached does carry a ConfigurationEndpoint, plus one node each
	require.NotNil(t, created.ConfigurationEndpoint, "ConfigurationEndpoint is Memcached's auto-discovery endpoint")
	assert.Equal(t, 11211, created.ConfigurationEndpoint.Port)
	require.Len(t, created.CacheNodes.Items, 2)
	assert.Equal(t, "0001", created.CacheNodes.Items[0].CacheNodeId)
	assert.Equal(t, "0002", created.CacheNodes.Items[1].CacheNodeId)
}

// TestCreateCacheCluster_firstAddressInTheBodyIsTheClusterEndpoint guards a
// coupling that has no compiler behind it: CloudFormation's ElastiCache
// handlers export their endpoint attributes by pulling the first <Address> and
// <Port> out of the create response body, so moving an endpoint element or
// adding another one ahead of it silently changes what a stack's
// RedisEndpoint.Address GetAtt resolves to.
func TestCreateCacheCluster_firstAddressInTheBodyIsTheClusterEndpoint(t *testing.T) {
	for _, tc := range []struct {
		engine string
		port   string
	}{
		{"redis", "6379"},
		{"memcached", "11211"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			// Given: the ElastiCache service
			srv := helpers.NewTestServer(t)

			// When: a cluster is created
			resp := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
				"CacheClusterId": []string{"first-address"},
				"Engine":         []string{tc.engine},
			})
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)
			body := readBodyString(t, resp)

			// Then: the first endpoint in the body is this cluster's own
			assert.Regexp(t,
				`<Address>first-address\.[^<]*</Address><Port>`+tc.port+`</Port>`,
				firstEndpoint(t, body))
		})
	}
}

func TestCreateReplicationGroup_firstAddressInTheBodyIsThePrimaryEndpoint(t *testing.T) {
	// Given: the ElastiCache service — see the cache-cluster test above
	srv := helpers.NewTestServer(t)

	// When: a replication group is created
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"first-address-rg"},
		"ReplicationGroupDescription": []string{"g"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the first endpoint in the body is the node group's primary
	assert.Regexp(t,
		`<Address>first-address-rg\.[^<]*</Address><Port>6379</Port>`,
		firstEndpoint(t, readBodyString(t, resp)))
}

// firstEndpoint returns the Address/Port pair CloudFormation's extractXMLValue
// would find, so a failure prints the pair rather than the whole document.
func firstEndpoint(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "<Address>")
	require.GreaterOrEqual(t, i, 0, "no <Address> in body: %s", body)
	j := strings.Index(body[i:], "</Port>")
	require.GreaterOrEqual(t, j, 0, "no </Port> after the first <Address>: %s", body)
	return body[i : i+j+len("</Port>")]
}

func readBodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// ── CacheCluster lifecycle ────────────────────────────────────────────────────

func TestCreateCacheCluster_lifecycleStatuses(t *testing.T) {
	// Given: a cluster, created without a container runtime
	srv := helpers.NewTestServer(t)
	create := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"lifecycle-cluster"},
		"Engine":         []string{"redis"},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)

	// Then: the create answers "creating", as AWS does
	assert.Equal(t, "creating", createdCluster(t, create).CacheClusterStatus)

	// And: with no container coming, it settles at "available"
	desc := cacheQuery(t, srv, "DescribeCacheClusters", url.Values{
		"CacheClusterId": []string{"lifecycle-cluster"},
	})
	defer desc.Body.Close()
	clusters := describedClusters(t, desc)
	require.Len(t, clusters, 1)
	assert.Equal(t, "available", clusters[0].CacheClusterStatus)
	require.Len(t, clusters[0].CacheNodes.Items, 1)
	assert.Equal(t, "available", clusters[0].CacheNodes.Items[0].CacheNodeStatus,
		"a node's status tracks its cluster's")

	// When: it is deleted
	del := cacheQuery(t, srv, "DeleteCacheCluster", url.Values{
		"CacheClusterId": []string{"lifecycle-cluster"},
	})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)

	// Then: the delete answers "deleting"
	var out struct {
		Result struct {
			CacheCluster xmlCacheClusterProjection `xml:"CacheCluster"`
		} `xml:"DeleteCacheClusterResult"`
	}
	decodeXML(t, del, &out)
	assert.Equal(t, "deleting", out.Result.CacheCluster.CacheClusterStatus)
}

// ── ReplicationGroupId constraints ────────────────────────────────────────────
//
// "A name must contain from 1 to 40 alphanumeric characters or hyphens. The
// first character must be a letter. A name cannot end with a hyphen or contain
// two consecutive hyphens."

func TestCreateReplicationGroup_identifierConstraints(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"leading digit", "1group"},
		{"trailing hyphen", "group-"},
		{"consecutive hyphens", "my--group"},
		{"underscore", "my_group"},
		{"too long", "g" + strings.Repeat("a", 40)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the ElastiCache service
			srv := helpers.NewTestServer(t)

			// When: CreateReplicationGroup names a group AWS would refuse
			resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
				"ReplicationGroupId":          []string{tc.id},
				"ReplicationGroupDescription": []string{"g"},
			})
			defer resp.Body.Close()

			// Then: InvalidParameterValue
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertQueryXMLError(t, resp, "InvalidParameterValue")
		})
	}
}

func TestCreateReplicationGroup_identifierLongerThanClusterMaximum(t *testing.T) {
	// Given: 45 characters — legal for a cluster, too long for a group
	srv := helpers.NewTestServer(t)
	id := "g" + strings.Repeat("a", 44)

	// When: CreateReplicationGroup is called with it
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{id},
		"ReplicationGroupDescription": []string{"g"},
	})
	defer resp.Body.Close()

	// Then: InvalidParameterValue — the group cap is 40, not 50
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

func TestCreateReplicationGroup_identifierStoredLowercase(t *testing.T) {
	// Given: "This parameter is stored as a lowercase string."
	srv := helpers.NewTestServer(t)

	// When: a mixed-case identifier is used
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"MixedGroup"},
		"ReplicationGroupDescription": []string{"g"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the id and ARN come back lowercased, and either spelling resolves
	created := createdGroup(t, resp)
	assert.Equal(t, "mixedgroup", created.ReplicationGroupId)
	assert.Contains(t, created.ARN, ":replicationgroup:mixedgroup")

	desc := cacheQuery(t, srv, "DescribeReplicationGroups", url.Values{
		"ReplicationGroupId": []string{"MixedGroup"},
	})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusOK)
	require.Len(t, describedGroups(t, desc), 1)
}

func TestCreateReplicationGroup_duplicate(t *testing.T) {
	// Given: a replication group
	srv := helpers.NewTestServer(t)
	first := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"dup-group"},
		"ReplicationGroupDescription": []string{"g"},
	})
	defer first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)

	// When: the same id is created again
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"dup-group"},
		"ReplicationGroupDescription": []string{"g"},
	})
	defer resp.Body.Close()

	// Then: the wire code is the one the model's awsQueryError trait gives —
	// "ReplicationGroupAlreadyExists", not the shape name with its Fault
	// suffix, which no SDK would match to the modelled exception type
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "ReplicationGroupAlreadyExists")
}

// ── CreateReplicationGroup engine ─────────────────────────────────────────────
//
// "The name of the cache engine to be used for the clusters in this replication
// group. The value must be set to valkey or redis."

func TestCreateReplicationGroup_engineValidation(t *testing.T) {
	for _, engine := range []string{"memcached", "postgres", "Redis "} {
		t.Run(engine, func(t *testing.T) {
			// Given: the ElastiCache service
			srv := helpers.NewTestServer(t)

			// When: an engine that cannot replicate is requested
			resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
				"ReplicationGroupId":          []string{"engine-rg"},
				"ReplicationGroupDescription": []string{"g"},
				"Engine":                      []string{engine},
			})
			defer resp.Body.Close()

			// Then: InvalidParameterValue, rather than a group that silently
			// runs a different engine than the one asked for
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertQueryXMLError(t, resp, "InvalidParameterValue")
		})
	}
}

func TestCreateReplicationGroup_valkeyAccepted(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: Engine is valkey
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"valkey-rg"},
		"ReplicationGroupDescription": []string{"g"},
		"Engine":                      []string{"valkey"},
	})
	defer resp.Body.Close()

	// Then: accepted, and the engine is echoed
	helpers.AssertStatus(t, resp, http.StatusOK)
	assert.Equal(t, "valkey", createdGroup(t, resp).Engine)
}

// ── ReplicationGroup XML shape ────────────────────────────────────────────────

func TestCreateReplicationGroup_xmlShape(t *testing.T) {
	// Given: the ElastiCache service
	srv := helpers.NewTestServer(t)

	// When: a replication group is created
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"shape-rg"},
		"ReplicationGroupDescription": []string{"shape"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	created := createdGroup(t, resp)

	// Then: the group is cluster-mode-disabled, so AWS leaves
	// ConfigurationEndpoint null and puts the address on the node group's
	// PrimaryEndpoint instead
	assert.Equal(t, "false", created.ClusterEnabled)
	assert.Nil(t, created.ConfigurationEndpoint,
		"ConfigurationEndpoint is cluster-mode-enabled only on AWS")
	assert.NotEmpty(t, created.ReplicationGroupCreateTime)

	// And: a member cluster exists for the primary, named as AWS names it
	assert.Equal(t, []string{"shape-rg-001"}, created.MemberClusters.Items)

	require.Len(t, created.NodeGroups.Items, 1)
	ng := created.NodeGroups.Items[0]
	assert.Equal(t, "0001", ng.NodeGroupId)
	assert.Equal(t, "creating", ng.Status)
	require.NotNil(t, ng.PrimaryEndpoint)
	assert.NotEmpty(t, ng.PrimaryEndpoint.Address)
	assert.Equal(t, 6379, ng.PrimaryEndpoint.Port)
	require.NotNil(t, ng.ReaderEndpoint)
	assert.Equal(t, ng.PrimaryEndpoint.Port, ng.ReaderEndpoint.Port)

	require.Len(t, ng.NodeGroupMembers.Items, 1)
	member := ng.NodeGroupMembers.Items[0]
	assert.Equal(t, "shape-rg-001", member.CacheClusterId)
	assert.Equal(t, "0001", member.CacheNodeId)
	assert.Equal(t, "primary", member.CurrentRole)
	require.NotNil(t, member.ReadEndpoint)
}

func TestCreateReplicationGroup_primaryClusterIdBecomesMember(t *testing.T) {
	// Given: an existing cache cluster
	srv := helpers.NewTestServer(t)
	cluster := cacheQuery(t, srv, "CreateCacheCluster", url.Values{
		"CacheClusterId": []string{"primary-cluster"},
		"Engine":         []string{"redis"},
	})
	defer cluster.Body.Close()
	helpers.AssertStatus(t, cluster, http.StatusOK)

	// When: a group is created naming it as the primary
	resp := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"primary-rg"},
		"ReplicationGroupDescription": []string{"g"},
		"PrimaryClusterId":            []string{"primary-cluster"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: that cluster is the member, not a generated one
	created := createdGroup(t, resp)
	assert.Equal(t, []string{"primary-cluster"}, created.MemberClusters.Items)
	require.Len(t, created.NodeGroups.Items, 1)
	require.Len(t, created.NodeGroups.Items[0].NodeGroupMembers.Items, 1)
	assert.Equal(t, "primary-cluster",
		created.NodeGroups.Items[0].NodeGroupMembers.Items[0].CacheClusterId)
}

// ── ReplicationGroup lifecycle ────────────────────────────────────────────────

func TestCreateReplicationGroup_lifecycleStatuses(t *testing.T) {
	// Given: a replication group, created without a container runtime
	srv := helpers.NewTestServer(t)
	create := cacheQuery(t, srv, "CreateReplicationGroup", url.Values{
		"ReplicationGroupId":          []string{"lifecycle-rg"},
		"ReplicationGroupDescription": []string{"g"},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)

	// Then: the create answers "creating"
	assert.Equal(t, "creating", createdGroup(t, create).Status)

	// And: it settles at "available", node group included
	desc := cacheQuery(t, srv, "DescribeReplicationGroups", url.Values{
		"ReplicationGroupId": []string{"lifecycle-rg"},
	})
	defer desc.Body.Close()
	groups := describedGroups(t, desc)
	require.Len(t, groups, 1)
	assert.Equal(t, "available", groups[0].Status)
	require.Len(t, groups[0].NodeGroups.Items, 1)
	assert.Equal(t, "available", groups[0].NodeGroups.Items[0].Status)

	// When: it is deleted
	del := cacheQuery(t, srv, "DeleteReplicationGroup", url.Values{
		"ReplicationGroupId": []string{"lifecycle-rg"},
	})
	defer del.Body.Close()

	// Then: the delete answers "deleting"
	helpers.AssertStatus(t, del, http.StatusOK)
	var out struct {
		Result struct {
			ReplicationGroup xmlReplicationGroupProjection `xml:"ReplicationGroup"`
		} `xml:"DeleteReplicationGroupResult"`
	}
	decodeXML(t, del, &out)
	assert.Equal(t, "deleting", out.Result.ReplicationGroup.Status)
}
