package elasticache

import (
	"context"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ── Docker container event handlers ──────────────────────────────────────────
//
// The event payloads only carry a resource ID — not the region the resource
// was stored under — so these handlers locate the resource with a
// cross-region scan and pin that region on the context for store writes.

// Every ElastiCache resource ID is a name the caller chose — a cache cluster
// ID, a replication group ID, a serverless cache name — and nothing keeps
// those unique across the Overcasts sharing a Docker daemon. Two of them can
// each hold a cluster called "sessions", and each one's container carries
// overcast.resource-id=sessions, so matching on that alone lets one mark the
// other's live cache stopped, or adopt it and later stop and delete it. Every
// match below therefore goes through h.instances, which settles ownership by
// the identity of the store that created the container and falls back to the
// record's own note of the container ID — see InstanceDomain.ContainerIsOurs.

// handleContainerEvent processes DockerContainerDied and DockerContainerStopped.
// Handles cache clusters (plain resource ID), replication groups ("rg:" prefix),
// and serverless caches ("serverless:" prefix).
func (h *Handler) handleContainerEvent(_ context.Context, e events.Event) {
	p, ok := e.Payload.(events.DockerContainerPayload)
	if !ok || p.Service != serviceName || !h.dockerReady.Load() {
		return
	}

	if rgID, isRG := parseRGResourceID(p.ResourceID); isRG {
		rg, region, found, err := serviceutil.FindRegioned[ReplicationGroup](h.bgCtx, h.store.store, nsReplication, rgID, h.store.defaultRegion)
		if err != nil || !found {
			return
		}
		ctx := middleware.ContextWithRegion(h.bgCtx, region)
		log := h.log.WithRecorder(ctx)
		if !h.instances.ContainerIsOurs(ctx, p.ContainerID, p.Instance, rg.DockerContainerID) {
			// Another Overcast's cache exiting is its business. Marking this
			// group stopped over it takes down a record whose own container
			// never went anywhere.
			return
		}
		switch rg.Status {
		case "available", "starting", "creating", "modifying", statusCreateFailed:
			h.transitionReplicationGroup(ctx, rgID, "modifying", "available")
			h.recoverReplicationGroup(region, rgID)
			log.Info("replication group container stopped; replacement scheduled",
				zap.String("rg", rgID), zap.String("action", p.Action))
		}
		return
	}
	if name, isServerless := parseServerlessResourceID(p.ResourceID); isServerless {
		cache, region, found, err := serviceutil.FindRegioned[ServerlessCache](h.bgCtx, h.store.store, nsServerless, name, h.store.defaultRegion)
		if err != nil || !found {
			return
		}
		ctx := middleware.ContextWithRegion(h.bgCtx, region)
		log := h.log.WithRecorder(ctx)
		if !h.instances.ContainerIsOurs(ctx, p.ContainerID, p.Instance, cache.DockerContainerID) {
			return
		}
		switch cache.Status {
		case "available", "starting", "creating", "modifying", statusCreateFailed:
			h.transitionServerlessCache(ctx, name, "modifying", "available")
			h.recoverServerlessCache(region, name)
			log.Info("serverless cache container stopped; replacement scheduled",
				zap.String("cache", name), zap.String("action", p.Action))
		}
		return
	}

	cluster, region, found, err := serviceutil.FindRegioned[CacheCluster](h.bgCtx, h.store.store, nsClusters, p.ResourceID, h.store.defaultRegion)
	if err != nil || !found {
		return
	}
	ctx := middleware.ContextWithRegion(h.bgCtx, region)
	log := h.log.WithRecorder(ctx)
	if !h.instances.ContainerIsOurs(ctx, p.ContainerID, p.Instance, cluster.DockerContainerID) {
		return
	}
	switch cluster.CacheClusterStatus {
	case "available", "starting", "creating", "modifying", statusClusterUnreachable:
		h.transitionCacheCluster(ctx, p.ResourceID, "modifying", "available")
		h.recoverCacheCluster(region, p.ResourceID)
		log.Info("cache cluster container stopped; replacement scheduled",
			zap.String("cluster", p.ResourceID), zap.String("action", p.Action))
	}
}

// handleContainerStarted processes DockerContainerStarted events. Handles both
// cache clusters and replication groups.
func (h *Handler) handleContainerStarted(_ context.Context, e events.Event) {
	p, ok := e.Payload.(events.DockerContainerPayload)
	if !ok || p.Service != serviceName {
		return
	}

	if rgID, isRG := parseRGResourceID(p.ResourceID); isRG {
		rg, region, found, err := serviceutil.FindRegioned[ReplicationGroup](h.bgCtx, h.store.store, nsReplication, rgID, h.store.defaultRegion)
		if err != nil || !found || rg.ConfigurationEndpoint == nil {
			return
		}
		if !h.instances.ContainerIsOurs(middleware.ContextWithRegion(h.bgCtx, region), p.ContainerID, p.Instance, rg.DockerContainerID) {
			// Health-checking another Overcast's container would report this
			// group available on an endpoint it does not own.
			return
		}
		// The terminal failure status is in the set so a container that comes
		// back recovers the record: the fresh watch is entitled to promote a
		// create-failed group to available again. A failure nothing could ever
		// undo would trade one wrong answer for another.
		switch rg.Status {
		case "stopped", "starting", "creating", statusCreateFailed:
			h.scheduleGroupHealthCheck(region, rgID, rg)
		}
		return
	}
	if name, isServerless := parseServerlessResourceID(p.ResourceID); isServerless {
		cache, region, found, err := serviceutil.FindRegioned[ServerlessCache](h.bgCtx, h.store.store, nsServerless, name, h.store.defaultRegion)
		if err != nil || !found || cache.Endpoint == nil {
			return
		}
		if !h.instances.ContainerIsOurs(middleware.ContextWithRegion(h.bgCtx, region), p.ContainerID, p.Instance, cache.DockerContainerID) {
			return
		}
		switch cache.Status {
		case "stopped", "starting", "creating", statusCreateFailed:
			h.scheduleServerlessCacheHealthCheck(region, name, cache)
		}
		return
	}

	cluster, region, found, err := serviceutil.FindRegioned[CacheCluster](h.bgCtx, h.store.store, nsClusters, p.ResourceID, h.store.defaultRegion)
	if err != nil || !found {
		return
	}
	if !h.instances.ContainerIsOurs(middleware.ContextWithRegion(h.bgCtx, region), p.ContainerID, p.Instance, cluster.DockerContainerID) {
		return
	}
	switch cluster.CacheClusterStatus {
	case "stopped", "starting", "creating", statusClusterUnreachable:
		h.scheduleClusterHealthCheck(region, cluster.CacheClusterId, cluster)
	}
}

// reconcileContainers is called once at startup after Docker becomes available.
// It compares live container state against stored clusters and replication groups
// and corrects any status drift (e.g. containers that exited while Overcast was
// not running). Resources are listed across all regions — the store keys them
// per region, and reconciliation must cover every region, not just the default.
func (h *Handler) reconcileContainers(ctx context.Context, containers []docker.ContainerSummary) {
	log := h.log.WithRecorder(ctx)
	// One resource ID can name several containers — a cache called "sessions"
	// under each Overcast sharing this daemon — so they are collected rather
	// than overwritten, and OwnContainer picks this instance's out of them
	// below. Keyed to a single container, the index kept whichever the daemon
	// listed last, so a neighbour's could decide the state of a record whose
	// own container was running all along.
	byResource := docker.ContainersByResource(containers)

	// Reconcile cache clusters.
	clusters, err := serviceutil.ScanRegions[CacheCluster](ctx, h.store.store, nsClusters, h.store.defaultRegion)
	if err != nil {
		log.Warn("reconcile: failed to list cache clusters", zap.Error(err))
	} else {
		for _, rc := range clusters {
			cluster := rc.Value
			rctx := middleware.ContextWithRegion(ctx, rc.Region)
			c := h.instances.OwnContainer(rctx, byResource[cluster.CacheClusterId], cluster.DockerContainerID)
			switch {
			case c == nil:
				if cacheRuntimeExpected(cluster.CacheClusterStatus) {
					h.transitionCacheCluster(rctx, cluster.CacheClusterId, "modifying", "available", "stopped")
					h.recoverCacheCluster(rc.Region, cluster.CacheClusterId)
					log.Info("reconcile: container missing; replacement scheduled",
						zap.String("cluster", cluster.CacheClusterId))
				}
			case c.State == "running":
				// The Docker inspect stays outside the record's lock; only the
				// endpoint it decides is written back, onto a fresh read.
				cluster.DockerContainerID = c.ID
				h.setContainerDialTarget(rctx, cluster)
				if _, aerr := h.mutateCacheCluster(rctx, cluster.CacheClusterId, func(stored *CacheCluster) *protocol.AWSError {
					stored.DockerContainerID = cluster.DockerContainerID
					stored.DialAddress, stored.DialPort = cluster.DialAddress, cluster.DialPort
					return nil
				}); aerr != nil {
					log.Warn("reconcile: persist cluster endpoint",
						zap.String("cluster", cluster.CacheClusterId), zap.String("error", aerr.Message))
				}
				if cacheRuntimeExpected(cluster.CacheClusterStatus) {
					h.scheduleClusterHealthCheck(rc.Region, cluster.CacheClusterId, cluster)
					log.Info("reconcile: container running — scheduling health check",
						zap.String("cluster", cluster.CacheClusterId))
				}
			default:
				if cacheRuntimeExpected(cluster.CacheClusterStatus) {
					h.transitionCacheCluster(rctx, cluster.CacheClusterId, "modifying", "available", "stopped")
					h.recoverCacheCluster(rc.Region, cluster.CacheClusterId)
					log.Info("reconcile: container not running; replacement scheduled",
						zap.String("cluster", cluster.CacheClusterId),
						zap.String("containerState", c.State))
				}
			}
		}
	}

	// Reconcile replication groups.
	rgs, err := serviceutil.ScanRegions[ReplicationGroup](ctx, h.store.store, nsReplication, h.store.defaultRegion)
	if err != nil {
		log.Warn("reconcile: failed to list replication groups", zap.Error(err))
	} else {
		for _, rr := range rgs {
			rg := rr.Value
			rctx := middleware.ContextWithRegion(ctx, rr.Region)
			resourceLabel := "rg:" + rg.ReplicationGroupId
			c := h.instances.OwnContainer(rctx, byResource[resourceLabel], rg.DockerContainerID)
			switch {
			case c == nil:
				if cacheRuntimeExpected(rg.Status) {
					h.transitionReplicationGroup(rctx, rg.ReplicationGroupId, "modifying", "available", "stopped")
					h.recoverReplicationGroup(rr.Region, rg.ReplicationGroupId)
					log.Info("reconcile: RG container missing; replacement scheduled",
						zap.String("rg", rg.ReplicationGroupId))
				}
			case c.State == "running":
				rg.DockerContainerID = c.ID
				h.setReplicationGroupDialTarget(rctx, rg)
				if _, aerr := h.mutateReplicationGroup(rctx, rg.ReplicationGroupId, func(stored *ReplicationGroup) *protocol.AWSError {
					stored.DockerContainerID = rg.DockerContainerID
					stored.DialAddress, stored.DialPort = rg.DialAddress, rg.DialPort
					return nil
				}); aerr != nil {
					log.Warn("reconcile: persist RG endpoint",
						zap.String("rg", rg.ReplicationGroupId), zap.String("error", aerr.Message))
				}
				if cacheRuntimeExpected(rg.Status) {
					h.scheduleGroupHealthCheck(rr.Region, rg.ReplicationGroupId, rg)
					log.Info("reconcile: RG container running — scheduling health check",
						zap.String("rg", rg.ReplicationGroupId))
				}
			default:
				if cacheRuntimeExpected(rg.Status) {
					h.transitionReplicationGroup(rctx, rg.ReplicationGroupId, "modifying", "available", "stopped")
					h.recoverReplicationGroup(rr.Region, rg.ReplicationGroupId)
					log.Info("reconcile: RG container not running; replacement scheduled",
						zap.String("rg", rg.ReplicationGroupId),
						zap.String("containerState", c.State))
				}
			}
		}
	}

	serverless, err := serviceutil.ScanRegions[ServerlessCache](ctx, h.store.store, nsServerless, h.store.defaultRegion)
	if err != nil {
		log.Warn("reconcile: failed to list serverless caches", zap.Error(err))
		return
	}
	for _, rs := range serverless {
		cache := rs.Value
		rctx := middleware.ContextWithRegion(ctx, rs.Region)
		resourceLabel := "serverless:" + cache.ServerlessCacheName
		c := h.instances.OwnContainer(rctx, byResource[resourceLabel], cache.DockerContainerID)
		switch {
		case c == nil:
			if cacheRuntimeExpected(cache.Status) {
				h.transitionServerlessCache(rctx, cache.ServerlessCacheName, "modifying", "available", "stopped")
				h.recoverServerlessCache(rs.Region, cache.ServerlessCacheName)
				log.Info("reconcile: serverless cache container missing; replacement scheduled",
					zap.String("cache", cache.ServerlessCacheName))
			}
		case c.State == "running":
			cache.DockerContainerID = c.ID
			h.setServerlessDialTarget(rctx, cache)
			if _, aerr := h.mutateServerlessCache(rctx, cache.ServerlessCacheName, func(stored *ServerlessCache) *protocol.AWSError {
				stored.DockerContainerID = cache.DockerContainerID
				stored.DialAddress, stored.DialPort = cache.DialAddress, cache.DialPort
				return nil
			}); aerr != nil {
				log.Warn("reconcile: persist serverless cache endpoint",
					zap.String("cache", cache.ServerlessCacheName), zap.String("error", aerr.Message))
			}
			if cacheRuntimeExpected(cache.Status) {
				h.scheduleServerlessCacheHealthCheck(rs.Region, cache.ServerlessCacheName, cache)
				log.Info("reconcile: serverless cache container running — scheduling health check",
					zap.String("cache", cache.ServerlessCacheName))
			}
		default:
			if cacheRuntimeExpected(cache.Status) {
				h.transitionServerlessCache(rctx, cache.ServerlessCacheName, "modifying", "available", "stopped")
				h.recoverServerlessCache(rs.Region, cache.ServerlessCacheName)
				log.Info("reconcile: serverless cache container not running; replacement scheduled",
					zap.String("cache", cache.ServerlessCacheName),
					zap.String("containerState", c.State))
			}
		}
	}
}

func (h *Handler) runDockerRecovery(key string, recover func()) {
	h.dockerLifecycle.Lock()
	defer h.dockerLifecycle.Unlock()
	if h.dockerStopping || !h.dockerReady.Load() {
		return
	}
	if _, loaded := h.dockerRecoveries.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	h.dockerWg.Add(1)
	go func() {
		defer h.dockerWg.Done()
		defer h.dockerRecoveries.Delete(key)
		recover()
	}()
}

func cacheRuntimeExpected(status string) bool {
	switch status {
	case "available", "creating", "starting", "modifying", "stopped",
		// A record settled in its terminal readiness failure still expects a
		// runtime — the runtime that never came up is the whole reason it is
		// there — so reconciliation keeps replacing and re-checking it, and a
		// container that does come up promotes it out again.
		statusClusterUnreachable, statusCreateFailed:
		return true
	default:
		return false
	}
}

func (h *Handler) recoverCacheCluster(region, id string) {
	h.runDockerRecovery("cluster/"+region+"/"+id, func() {
		ctx := middleware.ContextWithRegion(h.bgCtx, region)
		cluster, aerr := h.store.getCacheCluster(ctx, id)
		if aerr != nil || cluster == nil || cluster.CacheClusterStatus == "deleting" {
			return
		}
		if err := h.startCacheContainer(ctx, cluster); err != nil {
			h.log.Warn("ElastiCache: replace cache cluster container", zap.String("cluster", id), zap.Error(err))
			return
		}
		if _, aerr := h.mutateCacheCluster(ctx, id, func(stored *CacheCluster) *protocol.AWSError {
			if stored.CacheClusterStatus == "deleting" {
				return errRecordMovedOn
			}
			stored.DockerContainerID = cluster.DockerContainerID
			stored.HostPort = cluster.HostPort
			stored.DialAddress, stored.DialPort = cluster.DialAddress, cluster.DialPort
			return nil
		}); aerr != nil {
			h.teardownOrphanedContainer(ctx, "cache cluster", id, cluster.DockerContainerID, cluster.HostPort)
			return
		}
		h.scheduleClusterHealthCheck(region, id, cluster)
	})
}

func (h *Handler) recoverReplicationGroup(region, id string) {
	h.runDockerRecovery("replication-group/"+region+"/"+id, func() {
		ctx := middleware.ContextWithRegion(h.bgCtx, region)
		group, aerr := h.store.getReplicationGroup(ctx, id)
		if aerr != nil || group == nil || group.Status == "deleting" {
			return
		}
		if err := h.startReplicationGroupContainer(ctx, group); err != nil {
			h.log.Warn("ElastiCache: replace replication group container", zap.String("rg", id), zap.Error(err))
			return
		}
		if _, aerr := h.mutateReplicationGroup(ctx, id, func(stored *ReplicationGroup) *protocol.AWSError {
			if stored.Status == "deleting" {
				return errRecordMovedOn
			}
			stored.DockerContainerID = group.DockerContainerID
			stored.HostPort = group.HostPort
			stored.DialAddress, stored.DialPort = group.DialAddress, group.DialPort
			return nil
		}); aerr != nil {
			h.teardownOrphanedContainer(ctx, "replication group", id, group.DockerContainerID, group.HostPort)
			return
		}
		h.scheduleGroupHealthCheck(region, id, group)
	})
}

func (h *Handler) recoverServerlessCache(region, name string) {
	h.runDockerRecovery("serverless/"+region+"/"+name, func() {
		ctx := middleware.ContextWithRegion(h.bgCtx, region)
		cache, aerr := h.store.getServerlessCache(ctx, name)
		if aerr != nil || cache == nil || cache.Status == "deleting" {
			return
		}
		if err := h.startServerlessCacheContainer(ctx, cache); err != nil {
			h.log.Warn("ElastiCache: replace serverless cache container", zap.String("cache", name), zap.Error(err))
			return
		}
		if _, aerr := h.mutateServerlessCache(ctx, name, func(stored *ServerlessCache) *protocol.AWSError {
			if stored.Status == "deleting" {
				return errRecordMovedOn
			}
			stored.DockerContainerID = cache.DockerContainerID
			stored.HostPort = cache.HostPort
			stored.DialAddress, stored.DialPort = cache.DialAddress, cache.DialPort
			return nil
		}); aerr != nil {
			h.teardownOrphanedContainer(ctx, "serverless cache", name, cache.DockerContainerID, cache.HostPort)
			return
		}
		h.scheduleServerlessCacheHealthCheck(region, name, cache)
	})
}

// parseRGResourceID returns (rgID, true) when the resource label has the "rg:" prefix.
func parseRGResourceID(resourceID string) (string, bool) {
	if strings.HasPrefix(resourceID, "rg:") {
		return strings.TrimPrefix(resourceID, "rg:"), true
	}
	return "", false
}

func parseServerlessResourceID(resourceID string) (string, bool) {
	if strings.HasPrefix(resourceID, "serverless:") {
		return strings.TrimPrefix(resourceID, "serverless:"), true
	}
	return "", false
}
