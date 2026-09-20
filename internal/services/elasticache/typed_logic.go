package elasticache

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"

	"go.uber.org/zap"
)

// ---- Request types ----

type ecCreateCacheClusterReq struct {
	CacheClusterId             string   `json:"CacheClusterId"`
	Engine                     string   `json:"Engine"`
	EngineVersion              string   `json:"EngineVersion"`
	CacheNodeType              string   `json:"CacheNodeType"`
	NumCacheNodes              int      `json:"NumCacheNodes"`
	ReplicationGroupId         string   `json:"ReplicationGroupId"`
	CacheSubnetGroupName       string   `json:"CacheSubnetGroupName"`
	AZMode                     string   `json:"AZMode"`
	PreferredAvailabilityZone  string   `json:"PreferredAvailabilityZone"`
	PreferredAvailabilityZones []string `json:"PreferredAvailabilityZones"`
	CacheParameterGroupName    string   `json:"CacheParameterGroupName"`
	Tags                       []ecTag  `json:"Tags"`
}

// ecTag is the Query-protocol Tags.Tag.N.Key/Value shape, shared by every
// create operation that accepts tags inline rather than through a separate
// AddTagsToResource call.
type ecTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type ecDescribeCacheClustersReq struct {
	CacheClusterId string `json:"CacheClusterId"`
}

type ecDeleteCacheClusterReq struct {
	CacheClusterId string `json:"CacheClusterId"`
}

type ecCreateReplicationGroupReq struct {
	ReplicationGroupId          string  `json:"ReplicationGroupId"`
	ReplicationGroupDescription string  `json:"ReplicationGroupDescription"`
	CacheNodeType               string  `json:"CacheNodeType"`
	Engine                      string  `json:"Engine"`
	EngineVersion               string  `json:"EngineVersion"`
	AutomaticFailoverEnabled    string  `json:"AutomaticFailoverEnabled"`
	MultiAZEnabled              string  `json:"MultiAZEnabled"`
	SnapshotRetentionLimit      int     `json:"SnapshotRetentionLimit"`
	PrimaryClusterId            string  `json:"PrimaryClusterId"`
	CacheSubnetGroupName        string  `json:"CacheSubnetGroupName"`
	Tags                        []ecTag `json:"Tags"`
}

type ecDescribeReplicationGroupsReq struct {
	ReplicationGroupId string `json:"ReplicationGroupId"`
}

type ecDeleteReplicationGroupReq struct {
	ReplicationGroupId string `json:"ReplicationGroupId"`
}

type ecCreateCacheSubnetGroupReq struct {
	CacheSubnetGroupName        string   `json:"CacheSubnetGroupName"`
	CacheSubnetGroupDescription string   `json:"CacheSubnetGroupDescription"`
	VpcId                       string   `json:"VpcId"`
	SubnetIds                   []string `json:"SubnetIds"`
}

type ecDescribeCacheSubnetGroupsReq struct {
	CacheSubnetGroupName string `json:"CacheSubnetGroupName"`
}

type ecDeleteCacheSubnetGroupReq struct {
	CacheSubnetGroupName string `json:"CacheSubnetGroupName"`
}

type ecCreateCacheParameterGroupReq struct {
	CacheParameterGroupName   string `json:"CacheParameterGroupName"`
	CacheParameterGroupFamily string `json:"CacheParameterGroupFamily"`
	Description               string `json:"Description"`
}

type ecDescribeCacheParameterGroupsReq struct {
	CacheParameterGroupName string `json:"CacheParameterGroupName"`
}

type ecDeleteCacheParameterGroupReq struct {
	CacheParameterGroupName string `json:"CacheParameterGroupName"`
}

type ecDescribeCacheParametersReq struct {
	CacheParameterGroupName string `json:"CacheParameterGroupName"`
	Source                  string `json:"Source"`
	MaxRecords              int    `json:"MaxRecords"`
	Marker                  string `json:"Marker"`
}

type ecModifyCacheClusterReq struct {
	CacheClusterId          string `json:"CacheClusterId"`
	CacheNodeType           string `json:"CacheNodeType"`
	EngineVersion           string `json:"EngineVersion"`
	NumCacheNodes           int    `json:"NumCacheNodes"`
	CacheParameterGroupName string `json:"CacheParameterGroupName"`
}

type ecModifyReplicationGroupReq struct {
	ReplicationGroupId          string `json:"ReplicationGroupId"`
	ReplicationGroupDescription string `json:"ReplicationGroupDescription"`
	CacheNodeType               string `json:"CacheNodeType"`
	AutomaticFailoverEnabled    string `json:"AutomaticFailoverEnabled"`
	MultiAZEnabled              string `json:"MultiAZEnabled"`
	SnapshotRetentionLimit      int    `json:"SnapshotRetentionLimit"`
}

// ---- Response types ----

type ecRespMeta struct {
	RequestId string `xml:"RequestId"`
}

type ecCreateCacheClusterResp struct {
	XMLName struct{}                   `xml:"CreateCacheClusterResponse"`
	Xmlns   string                     `xml:"xmlns,attr"`
	Result  ecCreateCacheClusterResult `xml:"CreateCacheClusterResult"`
	Meta    ecRespMeta                 `xml:"ResponseMetadata"`
}

type ecCreateCacheClusterResult struct {
	CacheCluster ecXMLCacheCluster `xml:"CacheCluster"`
}

type ecDescribeCacheClustersResp struct {
	XMLName struct{}                      `xml:"DescribeCacheClustersResponse"`
	Xmlns   string                        `xml:"xmlns,attr"`
	Result  ecDescribeCacheClustersResult `xml:"DescribeCacheClustersResult"`
	Meta    ecRespMeta                    `xml:"ResponseMetadata"`
}

type ecDescribeCacheClustersResult struct {
	CacheClusters ecXMLCacheClusters `xml:"CacheClusters"`
}

type ecXMLCacheClusters struct {
	Items []ecXMLCacheCluster `xml:"CacheCluster"`
}

// ecXMLCacheCluster is the AWS CacheCluster shape, in the model's own member
// order. Only members ElastiCache actually models appear: the flat
// CacheParameterGroupName element this once carried is not one of them — AWS
// nests it in CacheParameterGroup, alongside the apply status — and a caller
// reading the modelled member got nothing back.
type ecXMLCacheCluster struct {
	CacheClusterId string `xml:"CacheClusterId"`
	// ConfigurationEndpoint is Memcached's auto-discovery endpoint. AWS leaves
	// it null for Valkey and Redis OSS, where the node's own Endpoint is what a
	// client dials — see ecToXMLCacheCluster.
	ConfigurationEndpoint     *ecXMLEndpoint                  `xml:"ConfigurationEndpoint,omitempty"`
	CacheNodeType             string                          `xml:"CacheNodeType"`
	Engine                    string                          `xml:"Engine"`
	EngineVersion             string                          `xml:"EngineVersion"`
	CacheClusterStatus        string                          `xml:"CacheClusterStatus"`
	NumCacheNodes             int                             `xml:"NumCacheNodes"`
	PreferredAvailabilityZone string                          `xml:"PreferredAvailabilityZone,omitempty"`
	CacheClusterCreateTime    string                          `xml:"CacheClusterCreateTime,omitempty"`
	CacheParameterGroup       *ecXMLCacheParameterGroupStatus `xml:"CacheParameterGroup,omitempty"`
	CacheSubnetGroupName      string                          `xml:"CacheSubnetGroupName,omitempty"`
	CacheNodes                ecXMLCacheNodes                 `xml:"CacheNodes"`
	ReplicationGroupId        string                          `xml:"ReplicationGroupId,omitempty"`
	ARN                       string                          `xml:"ARN"`
}

type ecXMLCacheParameterGroupStatus struct {
	CacheParameterGroupName string                    `xml:"CacheParameterGroupName"`
	ParameterApplyStatus    string                    `xml:"ParameterApplyStatus"`
	CacheNodeIdsToReboot    ecXMLCacheNodeIdsToReboot `xml:"CacheNodeIdsToReboot"`
}

type ecXMLCacheNodeIdsToReboot struct {
	Items []string `xml:"CacheNodeId"`
}

type ecXMLCacheNodes struct {
	Items []ecXMLCacheNode `xml:"CacheNode"`
}

type ecXMLCacheNode struct {
	CacheNodeId              string         `xml:"CacheNodeId"`
	CacheNodeStatus          string         `xml:"CacheNodeStatus"`
	CacheNodeCreateTime      string         `xml:"CacheNodeCreateTime,omitempty"`
	Endpoint                 *ecXMLEndpoint `xml:"Endpoint,omitempty"`
	ParameterGroupStatus     string         `xml:"ParameterGroupStatus"`
	CustomerAvailabilityZone string         `xml:"CustomerAvailabilityZone,omitempty"`
}

type ecXMLEndpoint struct {
	Address string `xml:"Address"`
	Port    int    `xml:"Port"`
}

type ecDeleteCacheClusterResp struct {
	XMLName struct{}                   `xml:"DeleteCacheClusterResponse"`
	Xmlns   string                     `xml:"xmlns,attr"`
	Result  ecDeleteCacheClusterResult `xml:"DeleteCacheClusterResult"`
	Meta    ecRespMeta                 `xml:"ResponseMetadata"`
}

type ecDeleteCacheClusterResult struct {
	CacheCluster ecXMLCacheCluster `xml:"CacheCluster"`
}

type ecModifyCacheClusterResp struct {
	XMLName struct{}                   `xml:"ModifyCacheClusterResponse"`
	Xmlns   string                     `xml:"xmlns,attr"`
	Result  ecModifyCacheClusterResult `xml:"ModifyCacheClusterResult"`
	Meta    ecRespMeta                 `xml:"ResponseMetadata"`
}

type ecModifyCacheClusterResult struct {
	CacheCluster ecXMLCacheCluster `xml:"CacheCluster"`
}

type ecCreateReplicationGroupResp struct {
	XMLName struct{}                       `xml:"CreateReplicationGroupResponse"`
	Xmlns   string                         `xml:"xmlns,attr"`
	Result  ecCreateReplicationGroupResult `xml:"CreateReplicationGroupResult"`
	Meta    ecRespMeta                     `xml:"ResponseMetadata"`
}

type ecCreateReplicationGroupResult struct {
	ReplicationGroup ecXMLReplicationGroup `xml:"ReplicationGroup"`
}

// ecXMLReplicationGroup is the AWS ReplicationGroup shape, in the model's own
// member order. Overcast starts one primary and no shards, so every group here
// is cluster-mode-disabled: AWS leaves ConfigurationEndpoint null for those and
// puts the address on the node group's PrimaryEndpoint instead.
type ecXMLReplicationGroup struct {
	ReplicationGroupId         string              `xml:"ReplicationGroupId"`
	Description                string              `xml:"Description"`
	Status                     string              `xml:"Status"`
	MemberClusters             ecXMLMemberClusters `xml:"MemberClusters"`
	NodeGroups                 ecXMLNodeGroups     `xml:"NodeGroups"`
	AutomaticFailover          string              `xml:"AutomaticFailover"`
	MultiAZ                    string              `xml:"MultiAZ"`
	ConfigurationEndpoint      *ecXMLEndpoint      `xml:"ConfigurationEndpoint,omitempty"`
	SnapshotRetentionLimit     int                 `xml:"SnapshotRetentionLimit"`
	ClusterEnabled             bool                `xml:"ClusterEnabled"`
	CacheNodeType              string              `xml:"CacheNodeType"`
	ARN                        string              `xml:"ARN"`
	ReplicationGroupCreateTime string              `xml:"ReplicationGroupCreateTime,omitempty"`
	Engine                     string              `xml:"Engine,omitempty"`
}

type ecXMLNodeGroups struct {
	Items []ecXMLNodeGroup `xml:"NodeGroup"`
}

type ecXMLNodeGroup struct {
	NodeGroupId      string                `xml:"NodeGroupId"`
	Status           string                `xml:"Status"`
	PrimaryEndpoint  *ecXMLEndpoint        `xml:"PrimaryEndpoint,omitempty"`
	ReaderEndpoint   *ecXMLEndpoint        `xml:"ReaderEndpoint,omitempty"`
	NodeGroupMembers ecXMLNodeGroupMembers `xml:"NodeGroupMembers"`
}

type ecXMLNodeGroupMembers struct {
	Items []ecXMLNodeGroupMember `xml:"NodeGroupMember"`
}

type ecXMLNodeGroupMember struct {
	CacheClusterId            string         `xml:"CacheClusterId"`
	CacheNodeId               string         `xml:"CacheNodeId"`
	ReadEndpoint              *ecXMLEndpoint `xml:"ReadEndpoint,omitempty"`
	PreferredAvailabilityZone string         `xml:"PreferredAvailabilityZone,omitempty"`
	CurrentRole               string         `xml:"CurrentRole"`
}

type ecXMLMemberClusters struct {
	Items []ecXMLClusterIDMember `xml:"ClusterId"`
}

type ecXMLClusterIDMember struct {
	ClusterId string `xml:",chardata"`
}

type ecDescribeReplicationGroupsResp struct {
	XMLName struct{}                          `xml:"DescribeReplicationGroupsResponse"`
	Xmlns   string                            `xml:"xmlns,attr"`
	Result  ecDescribeReplicationGroupsResult `xml:"DescribeReplicationGroupsResult"`
	Meta    ecRespMeta                        `xml:"ResponseMetadata"`
}

type ecDescribeReplicationGroupsResult struct {
	ReplicationGroups ecXMLReplicationGroups `xml:"ReplicationGroups"`
}

type ecXMLReplicationGroups struct {
	Items []ecXMLReplicationGroup `xml:"ReplicationGroup"`
}

type ecDeleteReplicationGroupResp struct {
	XMLName struct{}                       `xml:"DeleteReplicationGroupResponse"`
	Xmlns   string                         `xml:"xmlns,attr"`
	Result  ecDeleteReplicationGroupResult `xml:"DeleteReplicationGroupResult"`
	Meta    ecRespMeta                     `xml:"ResponseMetadata"`
}

type ecDeleteReplicationGroupResult struct {
	ReplicationGroup ecXMLReplicationGroup `xml:"ReplicationGroup"`
}

type ecModifyReplicationGroupResp struct {
	XMLName struct{}                       `xml:"ModifyReplicationGroupResponse"`
	Xmlns   string                         `xml:"xmlns,attr"`
	Result  ecModifyReplicationGroupResult `xml:"ModifyReplicationGroupResult"`
	Meta    ecRespMeta                     `xml:"ResponseMetadata"`
}

type ecModifyReplicationGroupResult struct {
	ReplicationGroup ecXMLReplicationGroup `xml:"ReplicationGroup"`
}

type ecCreateCacheSubnetGroupResp struct {
	XMLName struct{}                       `xml:"CreateCacheSubnetGroupResponse"`
	Xmlns   string                         `xml:"xmlns,attr"`
	Result  ecCreateCacheSubnetGroupResult `xml:"CreateCacheSubnetGroupResult"`
	Meta    ecRespMeta                     `xml:"ResponseMetadata"`
}

type ecCreateCacheSubnetGroupResult struct {
	CacheSubnetGroup ecXMLCacheSubnetGroup `xml:"CacheSubnetGroup"`
}

type ecXMLCacheSubnetGroup struct {
	CacheSubnetGroupName        string       `xml:"CacheSubnetGroupName"`
	CacheSubnetGroupDescription string       `xml:"CacheSubnetGroupDescription"`
	ARN                         string       `xml:"ARN"`
	VpcId                       string       `xml:"VpcId"`
	Subnets                     ecXMLSubnets `xml:"Subnets"`
}

type ecXMLSubnets struct {
	Items []ecXMLSubnet `xml:"Subnet"`
}

type ecXMLSubnet struct {
	SubnetIdentifier string `xml:"SubnetIdentifier"`
}

type ecDescribeCacheSubnetGroupsResp struct {
	XMLName struct{}                          `xml:"DescribeCacheSubnetGroupsResponse"`
	Xmlns   string                            `xml:"xmlns,attr"`
	Result  ecDescribeCacheSubnetGroupsResult `xml:"DescribeCacheSubnetGroupsResult"`
	Meta    ecRespMeta                        `xml:"ResponseMetadata"`
}

type ecDescribeCacheSubnetGroupsResult struct {
	CacheSubnetGroups ecXMLCacheSubnetGroups `xml:"CacheSubnetGroups"`
}

type ecXMLCacheSubnetGroups struct {
	Items []ecXMLCacheSubnetGroup `xml:"CacheSubnetGroup"`
}

type ecDeleteCacheSubnetGroupResp struct {
	XMLName struct{}   `xml:"DeleteCacheSubnetGroupResponse"`
	Xmlns   string     `xml:"xmlns,attr"`
	Meta    ecRespMeta `xml:"ResponseMetadata"`
}

type ecCreateCacheParameterGroupResp struct {
	XMLName struct{}                          `xml:"CreateCacheParameterGroupResponse"`
	Xmlns   string                            `xml:"xmlns,attr"`
	Result  ecCreateCacheParameterGroupResult `xml:"CreateCacheParameterGroupResult"`
	Meta    ecRespMeta                        `xml:"ResponseMetadata"`
}

type ecCreateCacheParameterGroupResult struct {
	CacheParameterGroup ecXMLCacheParameterGroup `xml:"CacheParameterGroup"`
}

type ecXMLCacheParameterGroup struct {
	CacheParameterGroupName   string `xml:"CacheParameterGroupName"`
	CacheParameterGroupFamily string `xml:"CacheParameterGroupFamily"`
	Description               string `xml:"Description"`
	ARN                       string `xml:"ARN"`
}

type ecDescribeCacheParameterGroupsResp struct {
	XMLName struct{}                             `xml:"DescribeCacheParameterGroupsResponse"`
	Xmlns   string                               `xml:"xmlns,attr"`
	Result  ecDescribeCacheParameterGroupsResult `xml:"DescribeCacheParameterGroupsResult"`
	Meta    ecRespMeta                           `xml:"ResponseMetadata"`
}

type ecDescribeCacheParameterGroupsResult struct {
	CacheParameterGroups ecXMLCacheParameterGroups `xml:"CacheParameterGroups"`
}

type ecXMLCacheParameterGroups struct {
	Items []ecXMLCacheParameterGroup `xml:"CacheParameterGroup"`
}

type ecDeleteCacheParameterGroupResp struct {
	XMLName struct{}   `xml:"DeleteCacheParameterGroupResponse"`
	Xmlns   string     `xml:"xmlns,attr"`
	Meta    ecRespMeta `xml:"ResponseMetadata"`
}

type ecDescribeCacheParametersResp struct {
	XMLName struct{}                        `xml:"DescribeCacheParametersResponse"`
	Xmlns   string                          `xml:"xmlns,attr"`
	Result  ecDescribeCacheParametersResult `xml:"DescribeCacheParametersResult"`
	Meta    ecRespMeta                      `xml:"ResponseMetadata"`
}

type ecDescribeCacheParametersResult struct {
	Parameters ecXMLCacheParameterList `xml:"Parameters"`
	Marker     string                  `xml:"Marker"`
}

type ecXMLCacheParameterList struct {
	Items []ecXMLCacheParameter `xml:"Parameter"`
}

type ecXMLCacheParameter struct {
	ParameterName        string `xml:"ParameterName"`
	ParameterValue       string `xml:"ParameterValue"`
	Description          string `xml:"Description"`
	Source               string `xml:"Source"`
	DataType             string `xml:"DataType"`
	AllowedValues        string `xml:"AllowedValues,omitempty"`
	IsModifiable         bool   `xml:"IsModifiable"`
	MinimumEngineVersion string `xml:"MinimumEngineVersion"`
	ChangeType           string `xml:"ChangeType"`
}

// ---- Helpers ----

func ecMetaFromCtx(ctx context.Context) ecRespMeta {
	return ecRespMeta{RequestId: protocol.RequestIDFromContext(ctx)}
}

// ecTagsToMap converts the decoded Tags.Tag.N.Key/Value list into the map
// shape serviceutil's tag store takes. A tag with an empty key is dropped
// rather than stored, matching formTags' behaviour in the legacy raw path.
func ecTagsToMap(tags []ecTag) map[string]string {
	out := map[string]string{}
	for _, t := range tags {
		if t.Key == "" {
			continue
		}
		out[t.Key] = t.Value
	}
	return out
}

func ecToXMLCacheCluster(c *CacheCluster) ecXMLCacheCluster {
	out := ecXMLCacheCluster{
		CacheClusterId:            c.CacheClusterId,
		CacheClusterStatus:        c.CacheClusterStatus,
		CacheNodeType:             c.CacheNodeType,
		Engine:                    c.Engine,
		EngineVersion:             c.EngineVersion,
		NumCacheNodes:             c.NumCacheNodes,
		PreferredAvailabilityZone: ecPreferredAvailabilityZone(c),
		CacheClusterCreateTime:    c.CacheClusterCreateTime,
		CacheSubnetGroupName:      c.CacheSubnetGroupName,
		ReplicationGroupId:        c.ReplicationGroupId,
		ARN:                       c.ARN,
		CacheNodes:                ecXMLCacheNodes{Items: ecCacheNodes(c)},
	}
	if c.CacheParameterGroupName != "" {
		// AWS has no flat CacheParameterGroupName on CacheCluster: the name
		// arrives nested with the status of applying it, which is always
		// in-sync here because Overcast records a parameter group and never
		// pushes it into the engine.
		out.CacheParameterGroup = &ecXMLCacheParameterGroupStatus{
			CacheParameterGroupName: c.CacheParameterGroupName,
			ParameterApplyStatus:    "in-sync",
		}
	}
	// Memcached alone gets a ConfigurationEndpoint — it is the auto-discovery
	// endpoint, and AWS leaves it null on a Valkey or Redis OSS cluster, where
	// the single node's own Endpoint (above) is the address. Filling both in
	// would deploy here and return nothing on AWS, which is the direction of
	// divergence that costs a production incident rather than a local one.
	if c.Engine == "memcached" && c.ConfigurationEndpoint != nil {
		out.ConfigurationEndpoint = ecEndpoint(c.ConfigurationEndpoint)
	}
	return out
}

// ecEndpoint renders a stored endpoint, which the caller-facing rewrite in
// endpoint.go has already pointed at whoever is asking.
func ecEndpoint(e *ClusterEndpoint) *ecXMLEndpoint {
	if e == nil {
		return nil
	}
	return &ecXMLEndpoint{Address: e.Address, Port: e.Port}
}

// ecPreferredAvailabilityZone is the cluster's zone, or the literal "Multiple"
// AWS documents for a cluster whose nodes are in different zones — which is
// either an explicit zone list with more than one entry in it, or an AZMode of
// cross-az over more than one node, where AWS picks the zones itself.
func ecPreferredAvailabilityZone(c *CacheCluster) string {
	distinct := map[string]struct{}{}
	for _, z := range c.PreferredAvailabilityZones {
		distinct[z] = struct{}{}
	}
	if len(distinct) > 1 {
		return "Multiple"
	}
	if len(distinct) == 0 && c.AZMode == azModeCross && c.NumCacheNodes > 1 {
		return "Multiple"
	}
	for z := range distinct {
		return z
	}
	return c.PreferredAvailabilityZone
}

// ecCacheNodes renders one CacheNode per node the cluster was created with.
// Overcast runs a single container per cluster, so every node reports that
// container's endpoint and the cluster's own status — there is no per-node
// lifecycle to report separately.
func ecCacheNodes(c *CacheCluster) []ecXMLCacheNode {
	count := c.NumCacheNodes
	if count < 1 {
		count = 1
	}
	nodes := make([]ecXMLCacheNode, 0, count)
	for i := 0; i < count; i++ {
		node := ecXMLCacheNode{
			CacheNodeId:              fmt.Sprintf("%04d", i+1),
			CacheNodeStatus:          c.CacheClusterStatus,
			CacheNodeCreateTime:      c.CacheClusterCreateTime,
			Endpoint:                 ecEndpoint(c.ConfigurationEndpoint),
			ParameterGroupStatus:     "in-sync",
			CustomerAvailabilityZone: c.PreferredAvailabilityZone,
		}
		if i < len(c.PreferredAvailabilityZones) {
			node.CustomerAvailabilityZone = c.PreferredAvailabilityZones[i]
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func ecToXMLReplicationGroup(rg *ReplicationGroup) ecXMLReplicationGroup {
	members := make([]ecXMLClusterIDMember, 0, len(rg.MemberClusters))
	for _, id := range rg.MemberClusters {
		members = append(members, ecXMLClusterIDMember{ClusterId: id})
	}
	return ecXMLReplicationGroup{
		ReplicationGroupId:         rg.ReplicationGroupId,
		Description:                rg.Description,
		Status:                     rg.Status,
		ARN:                        rg.ARN,
		AutomaticFailover:          rg.AutomaticFailover,
		MultiAZ:                    rg.MultiAZ,
		CacheNodeType:              rg.CacheNodeType,
		Engine:                     rg.Engine,
		SnapshotRetentionLimit:     rg.SnapshotRetentionLimit,
		ReplicationGroupCreateTime: rg.ReplicationGroupCreateTime,
		MemberClusters:             ecXMLMemberClusters{Items: members},
		NodeGroups:                 ecXMLNodeGroups{Items: ecNodeGroups(rg)},
		// ClusterEnabled is false and ConfigurationEndpoint stays nil for the
		// same reason: Overcast models one shard, so every group is
		// cluster-mode-disabled. See ecXMLReplicationGroup.
		ClusterEnabled: false,
	}
}

// ecNodeGroups renders the single shard a replication group has here. The
// primary is MemberClusters[0] — ecPrimaryClusterID names it at create time —
// and any later member is a read replica.
func ecNodeGroups(rg *ReplicationGroup) []ecXMLNodeGroup {
	endpoint := ecEndpoint(rg.ConfigurationEndpoint)
	members := make([]ecXMLNodeGroupMember, 0, len(rg.MemberClusters))
	for i, id := range rg.MemberClusters {
		role := "replica"
		if i == 0 {
			role = "primary"
		}
		members = append(members, ecXMLNodeGroupMember{
			CacheClusterId: id,
			CacheNodeId:    "0001",
			ReadEndpoint:   endpoint,
			CurrentRole:    role,
		})
	}
	return []ecXMLNodeGroup{{
		NodeGroupId: "0001",
		Status:      rg.Status,
		// One container serves both roles, so the reader endpoint is the
		// primary's. AWS would give a separate name that load-balances the
		// replicas; there are none to balance.
		PrimaryEndpoint:  endpoint,
		ReaderEndpoint:   endpoint,
		NodeGroupMembers: ecXMLNodeGroupMembers{Items: members},
	}}
}

// ecPrimaryClusterID is the cache cluster AWS creates to hold a replication
// group's primary when the caller named no existing one: the group id with a
// node-index suffix.
func ecPrimaryClusterID(rgID string) string { return rgID + "-001" }

func ecToXMLCacheSubnetGroup(sg *CacheSubnetGroup) ecXMLCacheSubnetGroup {
	subnets := make([]ecXMLSubnet, 0, len(sg.SubnetIds))
	for _, id := range sg.SubnetIds {
		subnets = append(subnets, ecXMLSubnet{SubnetIdentifier: id})
	}
	return ecXMLCacheSubnetGroup{
		CacheSubnetGroupName:        sg.CacheSubnetGroupName,
		CacheSubnetGroupDescription: sg.CacheSubnetGroupDescription,
		ARN:                         sg.ARN,
		VpcId:                       sg.VpcId,
		Subnets:                     ecXMLSubnets{Items: subnets},
	}
}

func ecToXMLCacheParameterGroup(pg *CacheParameterGroup) ecXMLCacheParameterGroup {
	return ecXMLCacheParameterGroup{
		CacheParameterGroupName:   pg.CacheParameterGroupName,
		CacheParameterGroupFamily: pg.CacheParameterGroupFamily,
		Description:               pg.Description,
		ARN:                       pg.ARN,
	}
}

// ---- Typed handler functions ----

func (h *Handler) createCacheClusterTyped(ctx context.Context, req *ecCreateCacheClusterReq) (*ecCreateCacheClusterResp, *protocol.AWSError) {
	if req.CacheClusterId == "" {
		return nil, errInvalidParameterValue("CacheClusterId is required")
	}
	// Canonicalise before anything else keys off the id — the store, the
	// record lock, the scheduler scope and the ARN all have to agree on one
	// spelling. See ecCanonicalID.
	clusterID := ecCanonicalID(req.CacheClusterId)
	if aerr := validateCacheIdentifier("CacheClusterId", clusterID, maxCacheClusterIDLen); aerr != nil {
		return nil, aerr
	}
	if _, aerr := h.store.getCacheCluster(ctx, clusterID); aerr == nil {
		return nil, errClusterAlreadyExists(clusterID)
	}
	if aerr := h.requireCacheSubnetGroup(ctx, req.CacheSubnetGroupName); aerr != nil {
		return nil, aerr
	}
	engine := req.Engine
	if engine == "" {
		engine = "redis"
	}
	if aerr := validateCacheClusterEngine(engine); aerr != nil {
		return nil, aerr
	}
	engineVersion := req.EngineVersion
	if engineVersion == "" {
		engineVersion = engineDefaultVersion(engine)
	}
	nodeType := req.CacheNodeType
	if nodeType == "" {
		nodeType = defaultNodeType
	}
	// A count of zero is an omitted NumCacheNodes rather than a request for no
	// nodes: the Query wire has no way to tell the two apart on an integer,
	// and AWS defaults the omitted case to one node.
	numNodes := req.NumCacheNodes
	if numNodes <= 0 {
		numNodes = 1
	}
	if aerr := validateNumCacheNodes(engine, numNodes); aerr != nil {
		return nil, aerr
	}
	if aerr := validateCachePlacement(engine, req.AZMode, req.PreferredAvailabilityZones, numNodes); aerr != nil {
		return nil, aerr
	}
	// A cluster created into a replication group is one of its read replicas,
	// so the group has to exist first — ReplicationGroupNotFoundFault is one
	// of this operation's modelled errors — and Memcached does not replicate.
	replicationGroupID := ecCanonicalID(req.ReplicationGroupId)
	if replicationGroupID != "" {
		if engine == "memcached" {
			return nil, errInvalidParameterCombination(
				"ReplicationGroupId is only valid for a Valkey or Redis OSS cluster.")
		}
		if _, aerr := h.store.getReplicationGroup(ctx, replicationGroupID); aerr != nil {
			return nil, aerr
		}
	}
	region := h.store.region(ctx)
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:cluster:%s", region, h.cfg.AccountID, clusterID)
	endpoint := &ClusterEndpoint{
		Address: fmt.Sprintf("%s.%s.cfg.%s", clusterID, region, h.cfg.ExternalHostname()),
		Port:    enginePort(engine),
	}
	cluster := &CacheCluster{
		CacheClusterId:             clusterID,
		CacheClusterStatus:         "creating",
		CacheNodeType:              nodeType,
		Engine:                     engine,
		EngineVersion:              engineVersion,
		NumCacheNodes:              numNodes,
		PreferredAvailabilityZone:  req.PreferredAvailabilityZone,
		PreferredAvailabilityZones: req.PreferredAvailabilityZones,
		AZMode:                     req.AZMode,
		CacheClusterCreateTime:     h.clk.Now().UTC().Format(time.RFC3339Nano),
		CacheSubnetGroupName:       req.CacheSubnetGroupName,
		ReplicationGroupId:         replicationGroupID,
		CacheParameterGroupName:    req.CacheParameterGroupName,
		ARN:                        arn,
		ConfigurationEndpoint:      endpoint,
	}
	// Create-time tags are validated before anything is written, so a
	// rejected tag set fails the create rather than leaving a cluster that
	// exists with the tags the caller asked for missing. Mirrors the
	// serverless-cache create path.
	tags := ecTagsToMap(req.Tags)
	if aerr := serviceutil.ValidateTags(cacheTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.putCacheCluster(ctx, cluster); aerr != nil {
		return nil, aerr
	}
	if len(tags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(ctx, h.store.tags(), arn, tags, cacheTagCfg); aerr != nil {
			return nil, aerr
		}
	}
	if replicationGroupID != "" {
		// The group has to list its new replica, or DescribeReplicationGroups
		// answers with a shard that does not mention a cluster pointing at it.
		if _, aerr := h.mutateReplicationGroup(ctx, replicationGroupID, func(rg *ReplicationGroup) *protocol.AWSError {
			for _, existing := range rg.MemberClusters {
				if existing == clusterID {
					return errRecordMovedOn
				}
			}
			rg.MemberClusters = append(rg.MemberClusters, clusterID)
			return nil
		}); aerr != nil && aerr != errRecordMovedOn {
			return nil, aerr
		}
	}
	if h.dockerReady.Load() {
		if h.puller != nil {
			h.puller.Prewarm(engineImage(engine, engineVersion))
		}
		h.dockerWg.Add(1)
		go func() {
			defer h.dockerWg.Done()
			bgCtx := middleware.ContextWithRegion(h.bgCtx, region)
			got, aerr := h.store.getCacheCluster(bgCtx, clusterID)
			if aerr != nil || got == nil {
				return
			}
			if err := h.startCacheContainer(bgCtx, got); err != nil {
				// See the form path in handler.go: a container that cannot be
				// built is a failure to report, not a stall to leave behind.
				h.failCacheCluster(bgCtx, clusterID, fmt.Sprintf("the cache container could not be created: %v", err))
				return
			}
			// The start took real time; the cluster may have been deleted
			// meanwhile. Delete could not stop this container — its ID was
			// not persisted yet — so the start goroutine owns the teardown.
			// Merge the container fields into a fresh read rather than
			// persisting the pre-start snapshot.
			fresh, aerr := h.mutateCacheCluster(bgCtx, clusterID, func(stored *CacheCluster) *protocol.AWSError {
				if stored.CacheClusterStatus == "deleting" {
					return errRecordMovedOn
				}
				stored.DockerContainerID = got.DockerContainerID
				stored.HostPort = got.HostPort
				stored.DialAddress, stored.DialPort = got.DialAddress, got.DialPort
				return nil
			})
			if aerr != nil {
				if aerr != errRecordMovedOn {
					h.log.Warn("ElastiCache: persist post-start cluster",
						zap.String("cluster", clusterID), zap.String("error", aerr.Message))
				}
				h.teardownOrphanedContainer(bgCtx, "cache cluster", clusterID, got.DockerContainerID, got.HostPort)
				return
			}
			h.scheduleClusterHealthCheck(region, clusterID, fresh)
		}()
	} else {
		// No container is coming, so nothing else will ever move this cluster
		// out of "creating". See settleCacheClusterWithoutRuntime.
		h.settleCacheClusterWithoutRuntime(region, clusterID)
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheClusterCreated, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: clusterID, ARN: arn}})
	}
	return &ecCreateCacheClusterResp{Xmlns: cacheXMLNS, Result: ecCreateCacheClusterResult{CacheCluster: ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, cluster))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeCacheClustersTyped(ctx context.Context, req *ecDescribeCacheClustersReq) (*ecDescribeCacheClustersResp, *protocol.AWSError) {
	if req.CacheClusterId != "" {
		cluster, aerr := h.store.getCacheCluster(ctx, ecCanonicalID(req.CacheClusterId))
		if aerr != nil {
			return nil, aerr
		}
		return &ecDescribeCacheClustersResp{Xmlns: cacheXMLNS, Result: ecDescribeCacheClustersResult{CacheClusters: ecXMLCacheClusters{Items: []ecXMLCacheCluster{ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, cluster))}}}, Meta: ecMetaFromCtx(ctx)}, nil
	}
	all, aerr := h.store.listCacheClusters(ctx)
	if aerr != nil {
		return nil, aerr
	}
	items := make([]ecXMLCacheCluster, 0, len(all))
	for _, c := range all {
		items = append(items, ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, c)))
	}
	return &ecDescribeCacheClustersResp{Xmlns: cacheXMLNS, Result: ecDescribeCacheClustersResult{CacheClusters: ecXMLCacheClusters{Items: items}}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) deleteCacheClusterTyped(ctx context.Context, req *ecDeleteCacheClusterReq) (*ecDeleteCacheClusterResp, *protocol.AWSError) {
	if req.CacheClusterId == "" {
		return nil, errInvalidParameterValue("CacheClusterId is required")
	}
	clusterID := ecCanonicalID(req.CacheClusterId)
	var containerID string
	var hostPort int
	cluster, aerr := h.mutateCacheCluster(ctx, clusterID, func(cluster *CacheCluster) *protocol.AWSError {
		containerID = cluster.DockerContainerID
		hostPort = cluster.HostPort
		cluster.CacheClusterStatus = "deleting"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheClusterDeleted, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: clusterID, ARN: cluster.ARN}})
	}
	h.scheduler.CancelScoped(h.store.region(ctx), clusterID, "health")

	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		_ = h.store.releasePort(ctx, hostPort) //nolint:errcheck
	}

	h.scheduler.AfterScoped(h.store.region(ctx), clusterID, "delete", 50*time.Millisecond, func(bgCtx context.Context) {
		if aerr := h.store.deleteCacheCluster(bgCtx, clusterID); aerr != nil {
			h.log.Warn("failed to delete cache cluster record", zap.String("cluster", clusterID), zap.Error(aerr))
		}
	})
	return &ecDeleteCacheClusterResp{Xmlns: cacheXMLNS, Result: ecDeleteCacheClusterResult{CacheCluster: ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, cluster))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) createReplicationGroupTyped(ctx context.Context, req *ecCreateReplicationGroupReq) (*ecCreateReplicationGroupResp, *protocol.AWSError) {
	if req.ReplicationGroupId == "" {
		return nil, errInvalidParameterValue("ReplicationGroupId is required")
	}
	// Canonicalise first — see createCacheClusterTyped. The cap is 40 here,
	// not the cluster's 50.
	rgID := ecCanonicalID(req.ReplicationGroupId)
	if aerr := validateCacheIdentifier("ReplicationGroupId", rgID, maxReplicationGroupIDLen); aerr != nil {
		return nil, aerr
	}
	if _, aerr := h.store.getReplicationGroup(ctx, rgID); aerr == nil {
		return nil, errReplicationGroupAlreadyExists(rgID)
	}
	if aerr := h.requireCacheSubnetGroup(ctx, req.CacheSubnetGroupName); aerr != nil {
		return nil, aerr
	}
	nodeType := req.CacheNodeType
	if nodeType == "" {
		nodeType = defaultNodeType
	}
	engine := req.Engine
	if engine == "" {
		engine = "redis"
	}
	if aerr := validateReplicationGroupEngine(engine); aerr != nil {
		return nil, aerr
	}
	engineVersion := req.EngineVersion
	if engineVersion == "" {
		engineVersion = engineDefaultVersion(engine)
	}
	region := h.store.region(ctx)
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:replicationgroup:%s", region, h.cfg.AccountID, rgID)
	autoFailover := "disabled"
	if req.AutomaticFailoverEnabled == "true" {
		autoFailover = "enabled"
	}
	multiAZ := "disabled"
	if req.MultiAZEnabled == "true" {
		multiAZ = "enabled"
	}
	snapshotRetention := req.SnapshotRetentionLimit
	port := enginePort(engine)
	endpoint := &ClusterEndpoint{
		Address: fmt.Sprintf("%s.%s.ng.cfg.%s", rgID, region, h.cfg.ExternalHostname()),
		Port:    port,
	}
	// A group always has a primary. Naming an existing cluster makes that
	// cluster the primary; naming none makes AWS create one called
	// <group>-001, which is the name MemberClusters and the node group's
	// members carry either way.
	primaryClusterID := ecCanonicalID(req.PrimaryClusterId)
	if primaryClusterID == "" {
		primaryClusterID = ecPrimaryClusterID(rgID)
	}
	rg := &ReplicationGroup{
		ReplicationGroupId:         rgID,
		Description:                req.ReplicationGroupDescription,
		Status:                     "creating",
		ARN:                        arn,
		AutomaticFailover:          autoFailover,
		MultiAZ:                    multiAZ,
		CacheNodeType:              nodeType,
		Engine:                     engine,
		EngineVersion:              engineVersion,
		SnapshotRetentionLimit:     snapshotRetention,
		ReplicationGroupCreateTime: h.clk.Now().UTC().Format(time.RFC3339Nano),
		MemberClusters:             []string{primaryClusterID},
		ConfigurationEndpoint:      endpoint,
		CacheSubnetGroupName:       req.CacheSubnetGroupName,
	}
	// Create-time tags are validated before anything is written, so a
	// rejected tag set fails the create rather than leaving a replication
	// group that exists with the tags the caller asked for missing. Mirrors
	// the cache-cluster and serverless-cache create paths (#1196).
	tags := ecTagsToMap(req.Tags)
	if aerr := serviceutil.ValidateTags(cacheTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.putReplicationGroup(ctx, rg); aerr != nil {
		return nil, aerr
	}
	if len(tags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(ctx, h.store.tags(), arn, tags, cacheTagCfg); aerr != nil {
			return nil, aerr
		}
	}
	if h.dockerReady.Load() {
		if h.puller != nil {
			h.puller.Prewarm(engineImage(engine, engineVersion))
		}
		h.dockerWg.Add(1)
		go func() {
			defer h.dockerWg.Done()
			bgCtx := middleware.ContextWithRegion(h.bgCtx, region)
			got, aerr := h.store.getReplicationGroup(bgCtx, rgID)
			if aerr != nil || got == nil {
				return
			}
			if err := h.startReplicationGroupContainer(bgCtx, got); err != nil {
				h.failReplicationGroup(bgCtx, rgID, fmt.Sprintf("the cache container could not be created: %v", err))
				return
			}
			// The start took real time; the group may have been deleted
			// meanwhile. Delete could not stop this container — its ID was
			// not persisted yet — so the start goroutine owns the teardown,
			// or the container outlives its resource (the compat teardowns
			// hit this window on every run). Merge the container fields into
			// a fresh read rather than persisting the pre-start snapshot.
			fresh, aerr := h.mutateReplicationGroup(bgCtx, rgID, func(stored *ReplicationGroup) *protocol.AWSError {
				if stored.Status == "deleting" {
					return errRecordMovedOn
				}
				stored.DockerContainerID = got.DockerContainerID
				stored.HostPort = got.HostPort
				stored.DialAddress, stored.DialPort = got.DialAddress, got.DialPort
				return nil
			})
			if aerr != nil {
				if aerr != errRecordMovedOn {
					h.log.Warn("ElastiCache: persist post-start replication group",
						zap.String("rg", rgID), zap.String("error", aerr.Message))
				}
				h.teardownOrphanedContainer(bgCtx, "replication group", rgID, got.DockerContainerID, got.HostPort)
				return
			}
			h.scheduleGroupHealthCheck(region, rgID, fresh)
		}()
	} else {
		// No container is coming, so nothing else will ever move this group out
		// of "creating". See settleReplicationGroupWithoutRuntime.
		h.settleReplicationGroupWithoutRuntime(region, rgID)
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheReplicationGroupCreated, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: rgID, ARN: arn}})
	}
	return &ecCreateReplicationGroupResp{Xmlns: cacheXMLNS, Result: ecCreateReplicationGroupResult{ReplicationGroup: ecToXMLReplicationGroup(h.replicationGroupForCaller(ctx, rg))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeReplicationGroupsTyped(ctx context.Context, req *ecDescribeReplicationGroupsReq) (*ecDescribeReplicationGroupsResp, *protocol.AWSError) {
	if req.ReplicationGroupId != "" {
		rg, aerr := h.store.getReplicationGroup(ctx, ecCanonicalID(req.ReplicationGroupId))
		if aerr != nil {
			return nil, aerr
		}
		return &ecDescribeReplicationGroupsResp{Xmlns: cacheXMLNS, Result: ecDescribeReplicationGroupsResult{ReplicationGroups: ecXMLReplicationGroups{Items: []ecXMLReplicationGroup{ecToXMLReplicationGroup(h.replicationGroupForCaller(ctx, rg))}}}, Meta: ecMetaFromCtx(ctx)}, nil
	}
	all, aerr := h.store.listReplicationGroups(ctx)
	if aerr != nil {
		return nil, aerr
	}
	items := make([]ecXMLReplicationGroup, 0, len(all))
	for _, rg := range all {
		items = append(items, ecToXMLReplicationGroup(h.replicationGroupForCaller(ctx, rg)))
	}
	return &ecDescribeReplicationGroupsResp{Xmlns: cacheXMLNS, Result: ecDescribeReplicationGroupsResult{ReplicationGroups: ecXMLReplicationGroups{Items: items}}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) deleteReplicationGroupTyped(ctx context.Context, req *ecDeleteReplicationGroupReq) (*ecDeleteReplicationGroupResp, *protocol.AWSError) {
	if req.ReplicationGroupId == "" {
		return nil, errInvalidParameterValue("ReplicationGroupId is required")
	}
	rgID := ecCanonicalID(req.ReplicationGroupId)
	var containerID string
	var hostPort int
	rg, aerr := h.mutateReplicationGroup(ctx, rgID, func(rg *ReplicationGroup) *protocol.AWSError {
		containerID = rg.DockerContainerID
		hostPort = rg.HostPort
		rg.Status = "deleting"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheReplicationGroupDeleted, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: rgID, ARN: rg.ARN}})
	}
	h.scheduler.CancelScoped(h.store.region(ctx), rgID, "rg-health")

	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		_ = h.store.releasePort(ctx, hostPort) //nolint:errcheck
	}

	h.scheduler.AfterScoped(h.store.region(ctx), rgID, "rg-delete", 50*time.Millisecond, func(bgCtx context.Context) {
		if aerr := h.store.deleteReplicationGroup(bgCtx, rgID); aerr != nil {
			h.log.Warn("failed to delete replication group record", zap.String("rg", rgID), zap.Error(aerr))
		}
	})
	return &ecDeleteReplicationGroupResp{Xmlns: cacheXMLNS, Result: ecDeleteReplicationGroupResult{ReplicationGroup: ecToXMLReplicationGroup(h.replicationGroupForCaller(ctx, rg))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) createCacheSubnetGroupTyped(ctx context.Context, req *ecCreateCacheSubnetGroupReq) (*ecCreateCacheSubnetGroupResp, *protocol.AWSError) {
	if req.CacheSubnetGroupName == "" {
		return nil, errInvalidParameterValue("CacheSubnetGroupName is required")
	}
	if _, aerr := h.store.getCacheSubnetGroup(ctx, req.CacheSubnetGroupName); aerr == nil {
		return nil, errSubnetGroupAlreadyExists(req.CacheSubnetGroupName)
	}
	region := h.store.region(ctx)
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:subnetgroup:%s", region, h.cfg.AccountID, req.CacheSubnetGroupName)
	sg := &CacheSubnetGroup{
		CacheSubnetGroupName:        req.CacheSubnetGroupName,
		CacheSubnetGroupDescription: req.CacheSubnetGroupDescription,
		ARN:                         arn,
		VpcId:                       req.VpcId,
		SubnetIds:                   req.SubnetIds,
	}
	if aerr := h.store.putCacheSubnetGroup(ctx, sg); aerr != nil {
		return nil, aerr
	}
	return &ecCreateCacheSubnetGroupResp{Xmlns: cacheXMLNS, Result: ecCreateCacheSubnetGroupResult{CacheSubnetGroup: ecToXMLCacheSubnetGroup(sg)}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeCacheSubnetGroupsTyped(ctx context.Context, req *ecDescribeCacheSubnetGroupsReq) (*ecDescribeCacheSubnetGroupsResp, *protocol.AWSError) {
	if req.CacheSubnetGroupName != "" {
		sg, aerr := h.store.getCacheSubnetGroup(ctx, req.CacheSubnetGroupName)
		if aerr != nil {
			return nil, aerr
		}
		return &ecDescribeCacheSubnetGroupsResp{Xmlns: cacheXMLNS, Result: ecDescribeCacheSubnetGroupsResult{CacheSubnetGroups: ecXMLCacheSubnetGroups{Items: []ecXMLCacheSubnetGroup{ecToXMLCacheSubnetGroup(sg)}}}, Meta: ecMetaFromCtx(ctx)}, nil
	}
	all, aerr := h.store.listCacheSubnetGroups(ctx)
	if aerr != nil {
		return nil, aerr
	}
	items := make([]ecXMLCacheSubnetGroup, 0, len(all))
	for _, sg := range all {
		items = append(items, ecToXMLCacheSubnetGroup(sg))
	}
	return &ecDescribeCacheSubnetGroupsResp{Xmlns: cacheXMLNS, Result: ecDescribeCacheSubnetGroupsResult{CacheSubnetGroups: ecXMLCacheSubnetGroups{Items: items}}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) deleteCacheSubnetGroupTyped(ctx context.Context, req *ecDeleteCacheSubnetGroupReq) (*ecDeleteCacheSubnetGroupResp, *protocol.AWSError) {
	if req.CacheSubnetGroupName == "" {
		return nil, errInvalidParameterValue("CacheSubnetGroupName is required")
	}
	if _, aerr := h.store.getCacheSubnetGroup(ctx, req.CacheSubnetGroupName); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteCacheSubnetGroup(ctx, req.CacheSubnetGroupName); aerr != nil {
		return nil, aerr
	}
	return &ecDeleteCacheSubnetGroupResp{Xmlns: cacheXMLNS, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) createCacheParameterGroupTyped(ctx context.Context, req *ecCreateCacheParameterGroupReq) (*ecCreateCacheParameterGroupResp, *protocol.AWSError) {
	if req.CacheParameterGroupName == "" {
		return nil, errInvalidParameterValue("CacheParameterGroupName is required")
	}
	if _, aerr := h.store.getCacheParameterGroup(ctx, req.CacheParameterGroupName); aerr == nil {
		return nil, errParameterGroupAlreadyExists(req.CacheParameterGroupName)
	}
	region := h.store.region(ctx)
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:parametergroup:%s", region, h.cfg.AccountID, req.CacheParameterGroupName)
	pg := &CacheParameterGroup{
		CacheParameterGroupName:   req.CacheParameterGroupName,
		CacheParameterGroupFamily: req.CacheParameterGroupFamily,
		Description:               req.Description,
		ARN:                       arn,
	}
	if aerr := h.store.putCacheParameterGroup(ctx, pg); aerr != nil {
		return nil, aerr
	}
	return &ecCreateCacheParameterGroupResp{Xmlns: cacheXMLNS, Result: ecCreateCacheParameterGroupResult{CacheParameterGroup: ecToXMLCacheParameterGroup(pg)}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeCacheParameterGroupsTyped(ctx context.Context, req *ecDescribeCacheParameterGroupsReq) (*ecDescribeCacheParameterGroupsResp, *protocol.AWSError) {
	if req.CacheParameterGroupName != "" {
		pg, aerr := h.store.getCacheParameterGroup(ctx, req.CacheParameterGroupName)
		if aerr != nil {
			return nil, aerr
		}
		return &ecDescribeCacheParameterGroupsResp{Xmlns: cacheXMLNS, Result: ecDescribeCacheParameterGroupsResult{CacheParameterGroups: ecXMLCacheParameterGroups{Items: []ecXMLCacheParameterGroup{ecToXMLCacheParameterGroup(pg)}}}, Meta: ecMetaFromCtx(ctx)}, nil
	}
	all, aerr := h.store.listCacheParameterGroups(ctx)
	if aerr != nil {
		return nil, aerr
	}
	items := make([]ecXMLCacheParameterGroup, 0, len(all))
	for _, pg := range all {
		items = append(items, ecToXMLCacheParameterGroup(pg))
	}
	return &ecDescribeCacheParameterGroupsResp{Xmlns: cacheXMLNS, Result: ecDescribeCacheParameterGroupsResult{CacheParameterGroups: ecXMLCacheParameterGroups{Items: items}}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) deleteCacheParameterGroupTyped(ctx context.Context, req *ecDeleteCacheParameterGroupReq) (*ecDeleteCacheParameterGroupResp, *protocol.AWSError) {
	if req.CacheParameterGroupName == "" {
		return nil, errInvalidParameterValue("CacheParameterGroupName is required")
	}
	if _, aerr := h.store.getCacheParameterGroup(ctx, req.CacheParameterGroupName); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteCacheParameterGroup(ctx, req.CacheParameterGroupName); aerr != nil {
		return nil, aerr
	}
	return &ecDeleteCacheParameterGroupResp{Xmlns: cacheXMLNS, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeCacheParametersTyped(ctx context.Context, req *ecDescribeCacheParametersReq) (*ecDescribeCacheParametersResp, *protocol.AWSError) {
	if req.CacheParameterGroupName == "" {
		return nil, errInvalidParameterValue("CacheParameterGroupName is required")
	}
	pg, aerr := h.store.getCacheParameterGroup(ctx, req.CacheParameterGroupName)
	if aerr != nil {
		return nil, aerr
	}
	source := strings.ToLower(req.Source)
	var params []ecXMLCacheParameter
	if source == "" || source == "system" || source == "engine-default" {
		for _, p := range engineParamsForFamily(pg.CacheParameterGroupFamily) {
			params = append(params, ecXMLCacheParameter{
				ParameterName:        p.name,
				ParameterValue:       p.value,
				Description:          p.description,
				Source:               "system",
				DataType:             p.dataType,
				AllowedValues:        p.allowed,
				IsModifiable:         p.modifiable,
				MinimumEngineVersion: p.minVersion,
				ChangeType:           p.changeType,
			})
		}
	}
	maxRecords := req.MaxRecords
	if maxRecords <= 0 {
		maxRecords = 100
	}
	startIdx := 0
	if req.Marker != "" {
		if n, err := strconv.Atoi(req.Marker); err == nil && n >= 0 {
			startIdx = n
		}
	}
	if startIdx > len(params) {
		startIdx = len(params)
	}
	page := params[startIdx:]
	nextMarker := ""
	if len(page) > maxRecords {
		page = page[:maxRecords]
		nextMarker = strconv.Itoa(startIdx + maxRecords)
	}
	return &ecDescribeCacheParametersResp{Xmlns: cacheXMLNS, Result: ecDescribeCacheParametersResult{
		Parameters: ecXMLCacheParameterList{Items: page},
		Marker:     nextMarker,
	}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) modifyCacheClusterTyped(ctx context.Context, req *ecModifyCacheClusterReq) (*ecModifyCacheClusterResp, *protocol.AWSError) {
	if req.CacheClusterId == "" {
		return nil, errInvalidParameterValue("CacheClusterId is required")
	}
	id := ecCanonicalID(req.CacheClusterId)
	cluster, aerr := h.mutateCacheCluster(ctx, id, func(cluster *CacheCluster) *protocol.AWSError {
		if req.CacheNodeType != "" {
			cluster.CacheNodeType = req.CacheNodeType
		}
		if req.EngineVersion != "" {
			cluster.EngineVersion = req.EngineVersion
		}
		if req.NumCacheNodes > 0 {
			cluster.NumCacheNodes = req.NumCacheNodes
		}
		if req.CacheParameterGroupName != "" {
			cluster.CacheParameterGroupName = req.CacheParameterGroupName
		}
		cluster.CacheClusterStatus = "modifying"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheClusterModified, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: id, ARN: cluster.ARN}})
	}
	h.scheduler.AfterScoped(h.store.region(ctx), id, "available", 0, func(bgCtx context.Context) {
		h.transitionCacheCluster(bgCtx, id, "available", "modifying")
	})
	return &ecModifyCacheClusterResp{Xmlns: cacheXMLNS, Result: ecModifyCacheClusterResult{CacheCluster: ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, cluster))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) modifyReplicationGroupTyped(ctx context.Context, req *ecModifyReplicationGroupReq) (*ecModifyReplicationGroupResp, *protocol.AWSError) {
	if req.ReplicationGroupId == "" {
		return nil, errInvalidParameterValue("ReplicationGroupId is required")
	}
	id := ecCanonicalID(req.ReplicationGroupId)
	rg, aerr := h.mutateReplicationGroup(ctx, id, func(rg *ReplicationGroup) *protocol.AWSError {
		if req.ReplicationGroupDescription != "" {
			rg.Description = req.ReplicationGroupDescription
		}
		if req.CacheNodeType != "" {
			rg.CacheNodeType = req.CacheNodeType
		}
		if req.AutomaticFailoverEnabled != "" {
			if req.AutomaticFailoverEnabled == "true" {
				rg.AutomaticFailover = "enabled"
			} else {
				rg.AutomaticFailover = "disabled"
			}
		}
		if req.MultiAZEnabled != "" {
			if req.MultiAZEnabled == "true" {
				rg.MultiAZ = "enabled"
			} else {
				rg.MultiAZ = "disabled"
			}
		}
		if req.SnapshotRetentionLimit > 0 {
			rg.SnapshotRetentionLimit = req.SnapshotRetentionLimit
		}
		rg.Status = "modifying"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	h.scheduler.AfterScoped(h.store.region(ctx), id, "rg-available", 0, func(bgCtx context.Context) {
		h.transitionReplicationGroup(bgCtx, id, "available", "modifying")
	})
	return &ecModifyReplicationGroupResp{Xmlns: cacheXMLNS, Result: ecModifyReplicationGroupResult{ReplicationGroup: ecToXMLReplicationGroup(h.replicationGroupForCaller(ctx, rg))}, Meta: ecMetaFromCtx(ctx)}, nil
}
