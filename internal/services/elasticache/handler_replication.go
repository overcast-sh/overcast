package elasticache

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ── XML types for replication groups ────────────────────────────────────────

type xmlCreateReplicationGroupResponse struct {
	XMLName          xml.Name                        `xml:"CreateReplicationGroupResponse"`
	Xmlns            string                          `xml:"xmlns,attr"`
	Result           xmlCreateReplicationGroupResult `xml:"CreateReplicationGroupResult"`
	ResponseMetadata protocol.ResponseMetadata       `xml:"ResponseMetadata"`
}

type xmlCreateReplicationGroupResult struct {
	ReplicationGroup xmlReplicationGroup `xml:"ReplicationGroup"`
}

type xmlModifyReplicationGroupResponse struct {
	XMLName          xml.Name                        `xml:"ModifyReplicationGroupResponse"`
	Xmlns            string                          `xml:"xmlns,attr"`
	Result           xmlModifyReplicationGroupResult `xml:"ModifyReplicationGroupResult"`
	ResponseMetadata protocol.ResponseMetadata       `xml:"ResponseMetadata"`
}

type xmlModifyReplicationGroupResult struct {
	ReplicationGroup xmlReplicationGroup `xml:"ReplicationGroup"`
}

type xmlDeleteReplicationGroupResponse struct {
	XMLName          xml.Name                        `xml:"DeleteReplicationGroupResponse"`
	Xmlns            string                          `xml:"xmlns,attr"`
	Result           xmlDeleteReplicationGroupResult `xml:"DeleteReplicationGroupResult"`
	ResponseMetadata protocol.ResponseMetadata       `xml:"ResponseMetadata"`
}

type xmlDeleteReplicationGroupResult struct {
	ReplicationGroup xmlReplicationGroup `xml:"ReplicationGroup"`
}

type xmlDescribeReplicationGroupsResponse struct {
	XMLName          xml.Name                           `xml:"DescribeReplicationGroupsResponse"`
	Xmlns            string                             `xml:"xmlns,attr"`
	Result           xmlDescribeReplicationGroupsResult `xml:"DescribeReplicationGroupsResult"`
	ResponseMetadata protocol.ResponseMetadata          `xml:"ResponseMetadata"`
}

type xmlDescribeReplicationGroupsResult struct {
	ReplicationGroups xmlReplicationGroups `xml:"ReplicationGroups"`
}

type xmlReplicationGroups struct {
	Items []xmlReplicationGroup `xml:"ReplicationGroup"`
}

type xmlReplicationGroup struct {
	ReplicationGroupId     string            `xml:"ReplicationGroupId"`
	Description            string            `xml:"Description"`
	Status                 string            `xml:"Status"`
	ARN                    string            `xml:"ARN"`
	AutomaticFailover      string            `xml:"AutomaticFailover"`
	MultiAZ                string            `xml:"MultiAZ"`
	CacheNodeType          string            `xml:"CacheNodeType"`
	Engine                 string            `xml:"Engine,omitempty"`
	SnapshotRetentionLimit int               `xml:"SnapshotRetentionLimit"`
	MemberClusters         xmlMemberClusters `xml:"MemberClusters"`
	ConfigurationEndpoint  *xmlEndpoint      `xml:"ConfigurationEndpoint,omitempty"`
}

type xmlMemberClusters struct {
	Items []xmlClusterIDMember `xml:"ClusterId"`
}

type xmlClusterIDMember struct {
	ClusterId string `xml:",chardata"`
}

// ── CreateReplicationGroup ────────────────────────────────────────────────────

// CreateReplicationGroup creates a replication group and (when Docker is available)
// starts a single primary container. The group transitions to "available" once the
// TCP health check succeeds, matching AWS semantics.
func (h *Handler) CreateReplicationGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReplicationGroupId")
	if id == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ReplicationGroupId is required"))
		return
	}

	if _, aerr := h.store.getReplicationGroup(r.Context(), id); aerr == nil {
		protocol.WriteQueryXMLError(w, r, errReplicationGroupAlreadyExists(id))
		return
	}

	description := r.FormValue("ReplicationGroupDescription")
	nodeType := r.FormValue("CacheNodeType")
	if nodeType == "" {
		nodeType = defaultNodeType
	}

	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "redis"
	}
	engineVersion := r.FormValue("EngineVersion")
	if engineVersion == "" {
		engineVersion = engineDefaultVersion(engine)
	}

	region := h.store.region(r.Context())
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:replicationgroup:%s", region, h.cfg.AccountID, id)

	autoFailover := "disabled"
	if r.FormValue("AutomaticFailoverEnabled") == "true" {
		autoFailover = "enabled"
	}
	multiAZ := "disabled"
	if r.FormValue("MultiAZEnabled") == "true" {
		multiAZ = "enabled"
	}
	snapshotRetention := formInt(r, "SnapshotRetentionLimit", 0)

	port := enginePort(engine)
	endpoint := &ClusterEndpoint{
		Address: fmt.Sprintf("%s.%s.ng.cfg.%s", id, region, h.cfg.ExternalHostname()),
		Port:    port,
	}

	if aerr := h.requireCacheSubnetGroup(r.Context(), r.FormValue("CacheSubnetGroupName")); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	rg := &ReplicationGroup{
		ReplicationGroupId:     id,
		Description:            description,
		Status:                 "creating",
		ARN:                    arn,
		AutomaticFailover:      autoFailover,
		MultiAZ:                multiAZ,
		CacheNodeType:          nodeType,
		Engine:                 engine,
		EngineVersion:          engineVersion,
		SnapshotRetentionLimit: snapshotRetention,
		ConfigurationEndpoint:  endpoint,
		CacheSubnetGroupName:   r.FormValue("CacheSubnetGroupName"),
	}

	// If a primary cluster ID was specified, register it as a member.
	if primaryClusterID := r.FormValue("PrimaryClusterId"); primaryClusterID != "" {
		rg.MemberClusters = []string{primaryClusterID}
	}

	// Create-time tags are validated before anything is written, so a
	// rejected tag set fails the create rather than leaving a replication
	// group that exists with the tags the caller asked for missing. Mirrors
	// the cache-cluster and serverless-cache create paths (#1196).
	tags := formTags(r)
	if aerr := serviceutil.ValidateTags(cacheTagCfg, tags); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	if aerr := h.store.putReplicationGroup(r.Context(), rg); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	if len(tags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(r.Context(), h.store.tags(), arn, tags, cacheTagCfg); aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
	}

	rgID := id
	if h.dockerReady.Load() {
		// Docker available — start a real container. Health check sets "available"
		// once it's listening. Skip the metadata transition to avoid marking
		// "available" before the container is ready.
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
			// meanwhile, and the delete could not stop this container — its ID
			// was not persisted yet — so the start goroutine owns the teardown.
			// Merge the container fields into a fresh read rather than
			// persisting the pre-start snapshot. Same as the typed path.
			if _, aerr := h.mutateReplicationGroup(bgCtx, rgID, func(stored *ReplicationGroup) *protocol.AWSError {
				if stored.Status == "deleting" {
					return errRecordMovedOn
				}
				stored.DockerContainerID = got.DockerContainerID
				stored.HostPort = got.HostPort
				stored.DialAddress, stored.DialPort = got.DialAddress, got.DialPort
				return nil
			}); aerr != nil {
				if aerr != errRecordMovedOn {
					h.log.Warn("ElastiCache: persist post-start replication group",
						zap.String("rg", rgID), zap.String("error", aerr.Message))
				}
				h.teardownOrphanedContainer(bgCtx, "replication group", rgID, got.DockerContainerID, got.HostPort)
				return
			}
			h.scheduleGroupHealthCheck(region, rgID, got)
		}()
	} else {
		// No container is coming, so nothing else will ever move this group out
		// of "creating". See settleReplicationGroupWithoutRuntime.
		h.settleReplicationGroupWithoutRuntime(region, rgID)
	}

	h.publish(r, events.ElastiCacheReplicationGroupCreated, events.ResourcePayload{Name: id, ARN: arn})

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlCreateReplicationGroupResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlCreateReplicationGroupResult{ReplicationGroup: toXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── DescribeReplicationGroups ────────────────────────────────────────────────

func (h *Handler) DescribeReplicationGroups(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("ReplicationGroupId")

	if filterID != "" {
		rg, aerr := h.store.getReplicationGroup(r.Context(), filterID)
		if aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
		protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeReplicationGroupsResponse{
			Xmlns: cacheXMLNS,
			Result: xmlDescribeReplicationGroupsResult{
				ReplicationGroups: xmlReplicationGroups{Items: []xmlReplicationGroup{toXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg))}},
			},
			ResponseMetadata: protocol.QueryResponseMetadata(r),
		})
		return
	}

	all, aerr := h.store.listReplicationGroups(r.Context())
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	items := make([]xmlReplicationGroup, 0, len(all))
	for _, rg := range all {
		items = append(items, toXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg)))
	}
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeReplicationGroupsResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlDescribeReplicationGroupsResult{ReplicationGroups: xmlReplicationGroups{Items: items}},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── DeleteReplicationGroup ────────────────────────────────────────────────────

func (h *Handler) DeleteReplicationGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReplicationGroupId")
	if id == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ReplicationGroupId is required"))
		return
	}

	var containerID string
	var hostPort int
	rg, aerr := h.mutateReplicationGroup(r.Context(), id, func(rg *ReplicationGroup) *protocol.AWSError {
		containerID = rg.DockerContainerID
		hostPort = rg.HostPort
		rg.Status = "deleting"
		return nil
	})
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	h.publish(r, events.ElastiCacheReplicationGroupDeleted, events.ResourcePayload{Name: id, ARN: rg.ARN})

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDeleteReplicationGroupResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlDeleteReplicationGroupResult{ReplicationGroup: toXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})

	h.scheduler.CancelScoped(h.store.region(r.Context()), id, "rg-health")

	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		_ = h.store.releasePort(r.Context(), hostPort) //nolint:errcheck
	}

	h.scheduler.AfterScoped(h.store.region(r.Context()), id, "rg-delete", 50*time.Millisecond, func(ctx context.Context) {
		if aerr := h.store.deleteReplicationGroup(ctx, id); aerr != nil {
			h.log.Warn("failed to delete replication group record", zap.String("rg", id), zap.Error(aerr))
		}
	})
}

// ── ModifyReplicationGroup ────────────────────────────────────────────────────

func (h *Handler) ModifyReplicationGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReplicationGroupId")
	if id == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ReplicationGroupId is required"))
		return
	}

	rg, aerr := h.store.getReplicationGroup(r.Context(), id)
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	if v := r.FormValue("ReplicationGroupDescription"); v != "" {
		rg.Description = v
	}
	if v := r.FormValue("CacheNodeType"); v != "" {
		rg.CacheNodeType = v
	}
	if v := r.FormValue("AutomaticFailoverEnabled"); v != "" {
		if v == "true" {
			rg.AutomaticFailover = "enabled"
		} else {
			rg.AutomaticFailover = "disabled"
		}
	}
	if v := r.FormValue("MultiAZEnabled"); v != "" {
		if v == "true" {
			rg.MultiAZ = "enabled"
		} else {
			rg.MultiAZ = "disabled"
		}
	}
	if v := r.FormValue("SnapshotRetentionLimit"); v != "" {
		rg.SnapshotRetentionLimit = formInt(r, "SnapshotRetentionLimit", rg.SnapshotRetentionLimit)
	}

	rg.Status = "modifying"
	if aerr := h.store.putReplicationGroup(r.Context(), rg); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	// Schedule transition back to available.
	h.scheduler.AfterScoped(h.store.region(r.Context()), id, "rg-available", 0, func(ctx context.Context) {
		h.transitionReplicationGroup(ctx, id, "available", "modifying")
	})

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlModifyReplicationGroupResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlModifyReplicationGroupResult{ReplicationGroup: toXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── Helper ───────────────────────────────────────────────────────────────────

func toXMLReplicationGroup(rg *ReplicationGroup) xmlReplicationGroup {
	members := make([]xmlClusterIDMember, 0, len(rg.MemberClusters))
	for _, id := range rg.MemberClusters {
		members = append(members, xmlClusterIDMember{ClusterId: id})
	}
	out := xmlReplicationGroup{
		ReplicationGroupId:     rg.ReplicationGroupId,
		Description:            rg.Description,
		Status:                 rg.Status,
		ARN:                    rg.ARN,
		AutomaticFailover:      rg.AutomaticFailover,
		MultiAZ:                rg.MultiAZ,
		CacheNodeType:          rg.CacheNodeType,
		Engine:                 rg.Engine,
		SnapshotRetentionLimit: rg.SnapshotRetentionLimit,
		MemberClusters:         xmlMemberClusters{Items: members},
	}
	if rg.ConfigurationEndpoint != nil {
		out.ConfigurationEndpoint = &xmlEndpoint{
			Address: rg.ConfigurationEndpoint.Address,
			Port:    rg.ConfigurationEndpoint.Port,
		}
	}
	return out
}
