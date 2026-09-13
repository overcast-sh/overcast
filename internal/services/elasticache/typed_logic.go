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
	CacheClusterId            string  `json:"CacheClusterId"`
	Engine                    string  `json:"Engine"`
	EngineVersion             string  `json:"EngineVersion"`
	CacheNodeType             string  `json:"CacheNodeType"`
	NumCacheNodes             int     `json:"NumCacheNodes"`
	ReplicationGroupId        string  `json:"ReplicationGroupId"`
	CacheSubnetGroupName      string  `json:"CacheSubnetGroupName"`
	PreferredAvailabilityZone string  `json:"PreferredAvailabilityZone"`
	CacheParameterGroupName   string  `json:"CacheParameterGroupName"`
	Tags                      []ecTag `json:"Tags"`
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

type ecXMLCacheCluster struct {
	CacheClusterId            string         `xml:"CacheClusterId"`
	CacheClusterStatus        string         `xml:"CacheClusterStatus"`
	CacheNodeType             string         `xml:"CacheNodeType"`
	Engine                    string         `xml:"Engine"`
	EngineVersion             string         `xml:"EngineVersion"`
	NumCacheNodes             int            `xml:"NumCacheNodes"`
	PreferredAvailabilityZone string         `xml:"PreferredAvailabilityZone,omitempty"`
	CacheSubnetGroupName      string         `xml:"CacheSubnetGroupName,omitempty"`
	ReplicationGroupId        string         `xml:"ReplicationGroupId,omitempty"`
	CacheParameterGroupName   string         `xml:"CacheParameterGroupName,omitempty"`
	ARN                       string         `xml:"ARN"`
	ConfigurationEndpoint     *ecXMLEndpoint `xml:"ConfigurationEndpoint,omitempty"`
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

type ecXMLReplicationGroup struct {
	ReplicationGroupId     string              `xml:"ReplicationGroupId"`
	Description            string              `xml:"Description"`
	Status                 string              `xml:"Status"`
	ARN                    string              `xml:"ARN"`
	AutomaticFailover      string              `xml:"AutomaticFailover"`
	MultiAZ                string              `xml:"MultiAZ"`
	CacheNodeType          string              `xml:"CacheNodeType"`
	Engine                 string              `xml:"Engine,omitempty"`
	SnapshotRetentionLimit int                 `xml:"SnapshotRetentionLimit"`
	MemberClusters         ecXMLMemberClusters `xml:"MemberClusters"`
	ConfigurationEndpoint  *ecXMLEndpoint      `xml:"ConfigurationEndpoint,omitempty"`
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
		PreferredAvailabilityZone: c.PreferredAvailabilityZone,
		CacheSubnetGroupName:      c.CacheSubnetGroupName,
		ReplicationGroupId:        c.ReplicationGroupId,
		CacheParameterGroupName:   c.CacheParameterGroupName,
		ARN:                       c.ARN,
	}
	if c.ConfigurationEndpoint != nil {
		out.ConfigurationEndpoint = &ecXMLEndpoint{Address: c.ConfigurationEndpoint.Address, Port: c.ConfigurationEndpoint.Port}
	}
	return out
}

func ecToXMLReplicationGroup(rg *ReplicationGroup) ecXMLReplicationGroup {
	members := make([]ecXMLClusterIDMember, 0, len(rg.MemberClusters))
	for _, id := range rg.MemberClusters {
		members = append(members, ecXMLClusterIDMember{ClusterId: id})
	}
	out := ecXMLReplicationGroup{
		ReplicationGroupId:     rg.ReplicationGroupId,
		Description:            rg.Description,
		Status:                 rg.Status,
		ARN:                    rg.ARN,
		AutomaticFailover:      rg.AutomaticFailover,
		MultiAZ:                rg.MultiAZ,
		CacheNodeType:          rg.CacheNodeType,
		Engine:                 rg.Engine,
		SnapshotRetentionLimit: rg.SnapshotRetentionLimit,
		MemberClusters:         ecXMLMemberClusters{Items: members},
	}
	if rg.ConfigurationEndpoint != nil {
		out.ConfigurationEndpoint = &ecXMLEndpoint{Address: rg.ConfigurationEndpoint.Address, Port: rg.ConfigurationEndpoint.Port}
	}
	return out
}

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
	if _, aerr := h.store.getCacheCluster(ctx, req.CacheClusterId); aerr == nil {
		return nil, errClusterAlreadyExists(req.CacheClusterId)
	}
	if aerr := h.requireCacheSubnetGroup(ctx, req.CacheSubnetGroupName); aerr != nil {
		return nil, aerr
	}
	engine := req.Engine
	if engine == "" {
		engine = "redis"
	}
	if engine != "redis" && engine != "memcached" && engine != "valkey" {
		return nil, errInvalidParameterValue("Engine must be redis, valkey, or memcached")
	}
	engineVersion := req.EngineVersion
	if engineVersion == "" {
		engineVersion = engineDefaultVersion(engine)
	}
	nodeType := req.CacheNodeType
	if nodeType == "" {
		nodeType = defaultNodeType
	}
	numNodes := req.NumCacheNodes
	if numNodes <= 0 {
		numNodes = 1
	}
	region := h.store.region(ctx)
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:cluster:%s", region, h.cfg.AccountID, req.CacheClusterId)
	endpoint := &ClusterEndpoint{
		Address: fmt.Sprintf("%s.%s.cfg.%s", req.CacheClusterId, region, h.cfg.ExternalHostname()),
		Port:    enginePort(engine),
	}
	cluster := &CacheCluster{
		CacheClusterId:            req.CacheClusterId,
		CacheClusterStatus:        "creating",
		CacheNodeType:             nodeType,
		Engine:                    engine,
		EngineVersion:             engineVersion,
		NumCacheNodes:             numNodes,
		PreferredAvailabilityZone: req.PreferredAvailabilityZone,
		CacheSubnetGroupName:      req.CacheSubnetGroupName,
		ReplicationGroupId:        req.ReplicationGroupId,
		CacheParameterGroupName:   req.CacheParameterGroupName,
		ARN:                       arn,
		ConfigurationEndpoint:     endpoint,
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
	clusterID := req.CacheClusterId
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
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheClusterCreated, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: req.CacheClusterId, ARN: arn}})
	}
	return &ecCreateCacheClusterResp{Xmlns: cacheXMLNS, Result: ecCreateCacheClusterResult{CacheCluster: ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, cluster))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeCacheClustersTyped(ctx context.Context, req *ecDescribeCacheClustersReq) (*ecDescribeCacheClustersResp, *protocol.AWSError) {
	if req.CacheClusterId != "" {
		cluster, aerr := h.store.getCacheCluster(ctx, req.CacheClusterId)
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
	var containerID string
	var hostPort int
	cluster, aerr := h.mutateCacheCluster(ctx, req.CacheClusterId, func(cluster *CacheCluster) *protocol.AWSError {
		containerID = cluster.DockerContainerID
		hostPort = cluster.HostPort
		cluster.CacheClusterStatus = "deleting"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheClusterDeleted, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: req.CacheClusterId, ARN: cluster.ARN}})
	}
	h.scheduler.CancelScoped(h.store.region(ctx), req.CacheClusterId, "health")

	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		_ = h.store.releasePort(ctx, hostPort) //nolint:errcheck
	}

	h.scheduler.AfterScoped(h.store.region(ctx), req.CacheClusterId, "delete", 50*time.Millisecond, func(bgCtx context.Context) {
		if aerr := h.store.deleteCacheCluster(bgCtx, req.CacheClusterId); aerr != nil {
			h.log.Warn("failed to delete cache cluster record", zap.String("cluster", req.CacheClusterId), zap.Error(aerr))
		}
	})
	return &ecDeleteCacheClusterResp{Xmlns: cacheXMLNS, Result: ecDeleteCacheClusterResult{CacheCluster: ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, cluster))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) createReplicationGroupTyped(ctx context.Context, req *ecCreateReplicationGroupReq) (*ecCreateReplicationGroupResp, *protocol.AWSError) {
	if req.ReplicationGroupId == "" {
		return nil, errInvalidParameterValue("ReplicationGroupId is required")
	}
	if _, aerr := h.store.getReplicationGroup(ctx, req.ReplicationGroupId); aerr == nil {
		return nil, errReplicationGroupAlreadyExists(req.ReplicationGroupId)
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
	engineVersion := req.EngineVersion
	if engineVersion == "" {
		engineVersion = engineDefaultVersion(engine)
	}
	region := h.store.region(ctx)
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:replicationgroup:%s", region, h.cfg.AccountID, req.ReplicationGroupId)
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
		Address: fmt.Sprintf("%s.%s.ng.cfg.%s", req.ReplicationGroupId, region, h.cfg.ExternalHostname()),
		Port:    port,
	}
	rg := &ReplicationGroup{
		ReplicationGroupId:     req.ReplicationGroupId,
		Description:            req.ReplicationGroupDescription,
		Status:                 "creating",
		ARN:                    arn,
		AutomaticFailover:      autoFailover,
		MultiAZ:                multiAZ,
		CacheNodeType:          nodeType,
		Engine:                 engine,
		EngineVersion:          engineVersion,
		SnapshotRetentionLimit: snapshotRetention,
		ConfigurationEndpoint:  endpoint,
		CacheSubnetGroupName:   req.CacheSubnetGroupName,
	}
	if req.PrimaryClusterId != "" {
		rg.MemberClusters = []string{req.PrimaryClusterId}
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
	rgID := req.ReplicationGroupId
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
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheReplicationGroupCreated, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: req.ReplicationGroupId, ARN: arn}})
	}
	return &ecCreateReplicationGroupResp{Xmlns: cacheXMLNS, Result: ecCreateReplicationGroupResult{ReplicationGroup: ecToXMLReplicationGroup(h.replicationGroupForCaller(ctx, rg))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeReplicationGroupsTyped(ctx context.Context, req *ecDescribeReplicationGroupsReq) (*ecDescribeReplicationGroupsResp, *protocol.AWSError) {
	if req.ReplicationGroupId != "" {
		rg, aerr := h.store.getReplicationGroup(ctx, req.ReplicationGroupId)
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
	var containerID string
	var hostPort int
	rg, aerr := h.mutateReplicationGroup(ctx, req.ReplicationGroupId, func(rg *ReplicationGroup) *protocol.AWSError {
		containerID = rg.DockerContainerID
		hostPort = rg.HostPort
		rg.Status = "deleting"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheReplicationGroupDeleted, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: req.ReplicationGroupId, ARN: rg.ARN}})
	}
	h.scheduler.CancelScoped(h.store.region(ctx), req.ReplicationGroupId, "rg-health")

	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		_ = h.store.releasePort(ctx, hostPort) //nolint:errcheck
	}

	h.scheduler.AfterScoped(h.store.region(ctx), req.ReplicationGroupId, "rg-delete", 50*time.Millisecond, func(bgCtx context.Context) {
		if aerr := h.store.deleteReplicationGroup(bgCtx, req.ReplicationGroupId); aerr != nil {
			h.log.Warn("failed to delete replication group record", zap.String("rg", req.ReplicationGroupId), zap.Error(aerr))
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
	cluster, aerr := h.mutateCacheCluster(ctx, req.CacheClusterId, func(cluster *CacheCluster) *protocol.AWSError {
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
		h.bus.Publish(ctx, events.Event{Type: events.ElastiCacheClusterModified, Time: h.clk.Now(), Source: "elasticache", Payload: events.ResourcePayload{Name: req.CacheClusterId, ARN: cluster.ARN}})
	}
	id := req.CacheClusterId
	h.scheduler.AfterScoped(h.store.region(ctx), id, "available", 0, func(bgCtx context.Context) {
		h.transitionCacheCluster(bgCtx, id, "available", "modifying")
	})
	return &ecModifyCacheClusterResp{Xmlns: cacheXMLNS, Result: ecModifyCacheClusterResult{CacheCluster: ecToXMLCacheCluster(h.cacheClusterForCaller(ctx, cluster))}, Meta: ecMetaFromCtx(ctx)}, nil
}

func (h *Handler) modifyReplicationGroupTyped(ctx context.Context, req *ecModifyReplicationGroupReq) (*ecModifyReplicationGroupResp, *protocol.AWSError) {
	if req.ReplicationGroupId == "" {
		return nil, errInvalidParameterValue("ReplicationGroupId is required")
	}
	rg, aerr := h.mutateReplicationGroup(ctx, req.ReplicationGroupId, func(rg *ReplicationGroup) *protocol.AWSError {
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
	id := req.ReplicationGroupId
	h.scheduler.AfterScoped(h.store.region(ctx), id, "rg-available", 0, func(bgCtx context.Context) {
		h.transitionReplicationGroup(bgCtx, id, "available", "modifying")
	})
	return &ecModifyReplicationGroupResp{Xmlns: cacheXMLNS, Result: ecModifyReplicationGroupResult{ReplicationGroup: ecToXMLReplicationGroup(h.replicationGroupForCaller(ctx, rg))}, Meta: ecMetaFromCtx(ctx)}, nil
}
