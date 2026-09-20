package elasticache

import (
	"context"
	"encoding/xml"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// ── XML types for replication groups ────────────────────────────────────────

type xmlModifyReplicationGroupResponse struct {
	XMLName          xml.Name                        `xml:"ModifyReplicationGroupResponse"`
	Xmlns            string                          `xml:"xmlns,attr"`
	Result           xmlModifyReplicationGroupResult `xml:"ModifyReplicationGroupResult"`
	ResponseMetadata protocol.ResponseMetadata       `xml:"ResponseMetadata"`
}

type xmlModifyReplicationGroupResult struct {
	ReplicationGroup ecXMLReplicationGroup `xml:"ReplicationGroup"`
}

type xmlDeleteReplicationGroupResponse struct {
	XMLName          xml.Name                        `xml:"DeleteReplicationGroupResponse"`
	Xmlns            string                          `xml:"xmlns,attr"`
	Result           xmlDeleteReplicationGroupResult `xml:"DeleteReplicationGroupResult"`
	ResponseMetadata protocol.ResponseMetadata       `xml:"ResponseMetadata"`
}

type xmlDeleteReplicationGroupResult struct {
	ReplicationGroup ecXMLReplicationGroup `xml:"ReplicationGroup"`
}

type xmlDescribeReplicationGroupsResponse struct {
	XMLName          xml.Name                           `xml:"DescribeReplicationGroupsResponse"`
	Xmlns            string                             `xml:"xmlns,attr"`
	Result           xmlDescribeReplicationGroupsResult `xml:"DescribeReplicationGroupsResult"`
	ResponseMetadata protocol.ResponseMetadata          `xml:"ResponseMetadata"`
}

// The ReplicationGroup body is ecXMLReplicationGroup, shared with the typed
// path — see the note on xmlDescribeCacheClustersResult.
type xmlDescribeReplicationGroupsResult struct {
	ReplicationGroups ecXMLReplicationGroups `xml:"ReplicationGroups"`
}

// ── CreateReplicationGroup ────────────────────────────────────────────────────

// CreateReplicationGroup is the Query form entry point; it decodes the form and
// delegates to createReplicationGroupTyped. See CreateCacheCluster for why the
// two protocols share one implementation rather than two.
func (h *Handler) CreateReplicationGroup(w http.ResponseWriter, r *http.Request) {
	req := &ecCreateReplicationGroupReq{
		ReplicationGroupId:          r.FormValue("ReplicationGroupId"),
		ReplicationGroupDescription: r.FormValue("ReplicationGroupDescription"),
		CacheNodeType:               r.FormValue("CacheNodeType"),
		Engine:                      r.FormValue("Engine"),
		EngineVersion:               r.FormValue("EngineVersion"),
		AutomaticFailoverEnabled:    r.FormValue("AutomaticFailoverEnabled"),
		MultiAZEnabled:              r.FormValue("MultiAZEnabled"),
		SnapshotRetentionLimit:      formInt(r, "SnapshotRetentionLimit", 0),
		PrimaryClusterId:            r.FormValue("PrimaryClusterId"),
		CacheSubnetGroupName:        r.FormValue("CacheSubnetGroupName"),
		Tags:                        formTagList(r),
	}
	resp, aerr := h.createReplicationGroupTyped(r.Context(), req)
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	protocol.WriteQueryXML(w, r, http.StatusOK, resp)
}

// ── DescribeReplicationGroups ────────────────────────────────────────────────

func (h *Handler) DescribeReplicationGroups(w http.ResponseWriter, r *http.Request) {
	filterID := ecCanonicalID(r.FormValue("ReplicationGroupId"))

	if filterID != "" {
		rg, aerr := h.store.getReplicationGroup(r.Context(), filterID)
		if aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
		protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeReplicationGroupsResponse{
			Xmlns: cacheXMLNS,
			Result: xmlDescribeReplicationGroupsResult{
				ReplicationGroups: ecXMLReplicationGroups{Items: []ecXMLReplicationGroup{ecToXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg))}},
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
	items := make([]ecXMLReplicationGroup, 0, len(all))
	for _, rg := range all {
		items = append(items, ecToXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg)))
	}
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeReplicationGroupsResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlDescribeReplicationGroupsResult{ReplicationGroups: ecXMLReplicationGroups{Items: items}},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── DeleteReplicationGroup ────────────────────────────────────────────────────

func (h *Handler) DeleteReplicationGroup(w http.ResponseWriter, r *http.Request) {
	id := ecCanonicalID(r.FormValue("ReplicationGroupId"))
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
		Result:           xmlDeleteReplicationGroupResult{ReplicationGroup: ecToXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg))},
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
	id := ecCanonicalID(r.FormValue("ReplicationGroupId"))
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
		Result:           xmlModifyReplicationGroupResult{ReplicationGroup: ecToXMLReplicationGroup(h.replicationGroupForCaller(r.Context(), rg))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}
