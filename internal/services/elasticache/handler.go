package elasticache

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/lifecycle"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/serviceutil/readiness"
)

const cacheXMLNS = "http://elasticache.amazonaws.com/doc/2015-02-02/"

var cacheTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidParameterValue",
	InvalidCode:     "InvalidParameterValue",
	ExceededMessage: "Tag list exceeds maximum number of tags allowed.",
}

// Handler handles ElastiCache Query-protocol requests.
type Handler struct {
	cfg         *config.Config
	store       *cacheStore
	log         *serviceutil.ServiceLogger
	clk         clock.Clock
	bus         *events.Bus
	scheduler   *lifecycle.Scheduler
	docker      *docker.Client
	dockerReady atomic.Bool
	puller      *docker.ImagePuller
	gc          *docker.GC
	// vpcResolver maps a cache's VPC onto its Docker network, so a cache placed
	// in a VPC is reachable from the rest of it. nil when EC2 is not enabled,
	// which puts every cache on the default plane.
	vpcResolver      VPCNetworkResolver
	dockerWg         sync.WaitGroup // tracks in-flight container-start goroutines
	dockerLifecycle  sync.Mutex     // closes the recovery Add/Wait boundary at shutdown
	dockerStopping   bool
	dockerRecoveries sync.Map // typed resource key -> in-flight replacement
	typedOp          map[string]op.Operation
	// instances scopes container sweeps to the containers this instance
	// created. A cache is ephemeral, so what this prevents is not lost data but
	// a second Overcast's startup deleting the first's stopped caches out from
	// under it. See docker.LabelInstance.
	instances *serviceutil.InstanceDomain
	// bgCtx is cancelled by Service.Stop so async goroutines and scheduler
	// callbacks can abandon Docker/store work once shutdown begins.
	bgCtx    context.Context
	bgCancel context.CancelFunc

	// One writer at a time per record — see locks.go.
	clusterLocks    serviceutil.RecordLocks
	rgLocks         serviceutil.RecordLocks
	serverlessLocks serviceutil.RecordLocks
}

func newHandler(cfg *config.Config, store *cacheStore, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	bgCtx, bgCancel := context.WithCancel(context.Background())
	h := &Handler{
		cfg:       cfg,
		store:     store,
		log:       log,
		clk:       clk,
		scheduler: lifecycle.NewScheduler(clk),
		bgCtx:     bgCtx,
		bgCancel:  bgCancel,
		instances: serviceutil.NewAnchoredInstanceDomain(store.store, nsInstance, serviceutil.DataDirAnchor(cfg.DataDir)),
	}
	h.typedOp = h.typedOps()
	return h
}

func (h *Handler) publish(r *http.Request, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{Type: t, Payload: payload})
	}
}

// ── Engine defaults ──────────────────────────────────────────────────────────

const (
	defaultRedisVersion     = "7.1"
	defaultRedisPort        = 6379
	defaultMemcachedVersion = "1.6"
	defaultMemcachedPort    = 11211
	defaultValkeyVersion    = "7.2"
	defaultNodeType         = "cache.t3.micro"
)

// cacheEngineImages maps engine → version → Docker image tag.
var cacheEngineImages = map[string]map[string]string{
	"redis": {
		"7.1": "redis:7",
		"7.0": "redis:7",
		"6.x": "redis:6",
	},
	"valkey": {
		"7.2": "valkey/valkey:7",
		"8.0": "valkey/valkey:8",
	},
	"memcached": {
		"1.6": "memcached:1.6",
		"1.5": "memcached:1.5",
	},
}

// enginePort returns the default container port for an engine.
func enginePort(engine string) int {
	if engine == "memcached" {
		return defaultMemcachedPort
	}
	return defaultRedisPort
}

// engineDefaultVersion returns the default version string for an engine.
func engineDefaultVersion(engine string) string {
	switch engine {
	case "memcached":
		return defaultMemcachedVersion
	case "valkey":
		return defaultValkeyVersion
	default:
		return defaultRedisVersion
	}
}

// engineImage returns the Docker image tag for the given engine and version,
// falling back to a sensible default when the exact version is not mapped.
func engineImage(engine, version string) string {
	if byVersion, ok := cacheEngineImages[engine]; ok {
		if img, ok := byVersion[version]; ok {
			return img
		}
	}
	switch engine {
	case "memcached":
		return "memcached:1.6"
	case "valkey":
		return "valkey/valkey:7"
	default:
		return "redis:7"
	}
}

// ── XML response types ───────────────────────────────────────────────────────

type xmlCreateCacheClusterResponse struct {
	XMLName          xml.Name                    `xml:"CreateCacheClusterResponse"`
	Xmlns            string                      `xml:"xmlns,attr"`
	Result           xmlCreateCacheClusterResult `xml:"CreateCacheClusterResult"`
	ResponseMetadata protocol.ResponseMetadata   `xml:"ResponseMetadata"`
}

type xmlCreateCacheClusterResult struct {
	CacheCluster xmlCacheCluster `xml:"CacheCluster"`
}

type xmlDeleteCacheClusterResponse struct {
	XMLName          xml.Name                    `xml:"DeleteCacheClusterResponse"`
	Xmlns            string                      `xml:"xmlns,attr"`
	Result           xmlDeleteCacheClusterResult `xml:"DeleteCacheClusterResult"`
	ResponseMetadata protocol.ResponseMetadata   `xml:"ResponseMetadata"`
}

type xmlDeleteCacheClusterResult struct {
	CacheCluster xmlCacheCluster `xml:"CacheCluster"`
}

type xmlDescribeCacheClustersResponse struct {
	XMLName          xml.Name                       `xml:"DescribeCacheClustersResponse"`
	Xmlns            string                         `xml:"xmlns,attr"`
	Result           xmlDescribeCacheClustersResult `xml:"DescribeCacheClustersResult"`
	ResponseMetadata protocol.ResponseMetadata      `xml:"ResponseMetadata"`
}

type xmlDescribeCacheClustersResult struct {
	CacheClusters xmlCacheClusters `xml:"CacheClusters"`
}

type xmlCacheClusters struct {
	Items []xmlCacheCluster `xml:"CacheCluster"`
}

type xmlCacheCluster struct {
	CacheClusterId            string       `xml:"CacheClusterId"`
	CacheClusterStatus        string       `xml:"CacheClusterStatus"`
	CacheNodeType             string       `xml:"CacheNodeType"`
	Engine                    string       `xml:"Engine"`
	EngineVersion             string       `xml:"EngineVersion"`
	NumCacheNodes             int          `xml:"NumCacheNodes"`
	PreferredAvailabilityZone string       `xml:"PreferredAvailabilityZone,omitempty"`
	CacheSubnetGroupName      string       `xml:"CacheSubnetGroupName,omitempty"`
	ReplicationGroupId        string       `xml:"ReplicationGroupId,omitempty"`
	CacheParameterGroupName   string       `xml:"CacheParameterGroupName,omitempty"`
	ARN                       string       `xml:"ARN"`
	ConfigurationEndpoint     *xmlEndpoint `xml:"ConfigurationEndpoint,omitempty"`
}

type xmlModifyCacheClusterResponse struct {
	XMLName          xml.Name                    `xml:"ModifyCacheClusterResponse"`
	Xmlns            string                      `xml:"xmlns,attr"`
	Result           xmlModifyCacheClusterResult `xml:"ModifyCacheClusterResult"`
	ResponseMetadata protocol.ResponseMetadata   `xml:"ResponseMetadata"`
}

type xmlModifyCacheClusterResult struct {
	CacheCluster xmlCacheCluster `xml:"CacheCluster"`
}

type xmlEndpoint struct {
	Address string `xml:"Address"`
	Port    int    `xml:"Port"`
}

// ── CreateCacheCluster ───────────────────────────────────────────────────────

// CreateCacheCluster creates a new cache cluster and (when Docker is available)
// starts a real Redis container. The cluster transitions to "available" once
// the TCP health check succeeds, matching AWS semantics.
func (h *Handler) CreateCacheCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CacheClusterId")
	if id == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("CacheClusterId is required"))
		return
	}

	// Duplicate check.
	if _, aerr := h.store.getCacheCluster(r.Context(), id); aerr == nil {
		protocol.WriteQueryXMLError(w, r, errClusterAlreadyExists(id))
		return
	}

	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "redis"
	}
	if engine != "redis" && engine != "memcached" && engine != "valkey" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("Engine must be redis, valkey, or memcached"))
		return
	}

	engineVersion := r.FormValue("EngineVersion")
	if engineVersion == "" {
		engineVersion = engineDefaultVersion(engine)
	}

	nodeType := r.FormValue("CacheNodeType")
	if nodeType == "" {
		nodeType = defaultNodeType
	}

	numNodes := formInt(r, "NumCacheNodes", 1)
	replicationGroupID := r.FormValue("ReplicationGroupId")
	subnetGroupName := r.FormValue("CacheSubnetGroupName")
	if aerr := h.requireCacheSubnetGroup(r.Context(), subnetGroupName); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	az := r.FormValue("PreferredAvailabilityZone")
	parameterGroupName := r.FormValue("CacheParameterGroupName")

	region := h.store.region(r.Context())
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:cluster:%s", region, h.cfg.AccountID, id)

	endpoint := &ClusterEndpoint{
		Address: fmt.Sprintf("%s.%s.cfg.%s", id, region, h.cfg.ExternalHostname()),
		Port:    enginePort(engine),
	}

	cluster := &CacheCluster{
		CacheClusterId:            id,
		CacheClusterStatus:        "creating",
		CacheNodeType:             nodeType,
		Engine:                    engine,
		EngineVersion:             engineVersion,
		NumCacheNodes:             numNodes,
		PreferredAvailabilityZone: az,
		CacheSubnetGroupName:      subnetGroupName,
		ReplicationGroupId:        replicationGroupID,
		CacheParameterGroupName:   parameterGroupName,
		ARN:                       arn,
		ConfigurationEndpoint:     endpoint,
	}

	// Create-time tags are validated before anything is written, so a
	// rejected tag set fails the create rather than leaving a cluster that
	// exists with the tags the caller asked for missing. Mirrors the
	// serverless-cache create path (handler_serverless.go).
	tags := formTags(r)
	if aerr := serviceutil.ValidateTags(cacheTagCfg, tags); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	if aerr := h.store.putCacheCluster(r.Context(), cluster); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	if len(tags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(r.Context(), h.store.tags(), arn, tags, cacheTagCfg); aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
	}

	clusterID := id
	if h.dockerReady.Load() {
		// Docker is available — start a real container. The health check will
		// transition to "available" once the process is listening. We do NOT
		// run the metadata transition here; that would mark the cluster "available"
		// before the container is ready, making the health check a no-op.
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
				// Not a fall back to metadata-only: Docker was wired when the
				// cluster was created, so a container that cannot be built is
				// a real failure. Leaving the cluster in "creating" parks it in
				// a state nothing will ever change — the readiness watch that
				// owns that transition is scheduled below, after the start
				// succeeds, so a failure here leaves nothing behind at all.
				h.failCacheCluster(bgCtx, clusterID, fmt.Sprintf("the cache container could not be created: %v", err))
				return
			}
			// The start took real time; the cluster may have been deleted
			// meanwhile. Delete could not stop this container — its ID was not
			// persisted yet — so the start goroutine owns the teardown. Merge
			// the container fields into a fresh read rather than persisting the
			// pre-start snapshot, which would put the cluster back to
			// "creating" and leave the delete undone. Same as the typed path.
			if _, aerr := h.mutateCacheCluster(bgCtx, clusterID, func(stored *CacheCluster) *protocol.AWSError {
				if stored.CacheClusterStatus == "deleting" {
					return errRecordMovedOn
				}
				stored.DockerContainerID = got.DockerContainerID
				stored.HostPort = got.HostPort
				stored.DialAddress, stored.DialPort = got.DialAddress, got.DialPort
				return nil
			}); aerr != nil {
				if aerr != errRecordMovedOn {
					h.log.Warn("ElastiCache: persist post-start cluster",
						zap.String("cluster", clusterID), zap.String("error", aerr.Message))
				}
				h.teardownOrphanedContainer(bgCtx, "cache cluster", clusterID, got.DockerContainerID, got.HostPort)
				return
			}
			h.scheduleClusterHealthCheck(region, clusterID, got)
		}()
	} else {
		// No container is coming, so nothing else will ever move this cluster
		// out of "creating". See settleCacheClusterWithoutRuntime.
		h.settleCacheClusterWithoutRuntime(region, clusterID)
	}

	h.publish(r, events.ElastiCacheClusterCreated, events.ResourcePayload{Name: id, ARN: arn})

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlCreateCacheClusterResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlCreateCacheClusterResult{CacheCluster: toXMLCacheCluster(h.cacheClusterForCaller(r.Context(), cluster))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── DescribeCacheClusters ────────────────────────────────────────────────────

func (h *Handler) DescribeCacheClusters(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("CacheClusterId")

	if filterID != "" {
		cluster, aerr := h.store.getCacheCluster(r.Context(), filterID)
		if aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
		docker.SetBackingHeaders(w, h.dockerReady.Load(), docker.ContainerHealthUnknown)
		protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeCacheClustersResponse{
			Xmlns: cacheXMLNS,
			Result: xmlDescribeCacheClustersResult{
				CacheClusters: xmlCacheClusters{Items: []xmlCacheCluster{toXMLCacheCluster(h.cacheClusterForCaller(r.Context(), cluster))}},
			},
			ResponseMetadata: protocol.QueryResponseMetadata(r),
		})
		return
	}

	all, aerr := h.store.listCacheClusters(r.Context())
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	items := make([]xmlCacheCluster, 0, len(all))
	for _, c := range all {
		items = append(items, toXMLCacheCluster(h.cacheClusterForCaller(r.Context(), c)))
	}
	docker.SetBackingHeaders(w, h.dockerReady.Load(), docker.ContainerHealthUnknown)
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeCacheClustersResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlDescribeCacheClustersResult{CacheClusters: xmlCacheClusters{Items: items}},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── DeleteCacheCluster ───────────────────────────────────────────────────────

func (h *Handler) DeleteCacheCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CacheClusterId")
	if id == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("CacheClusterId is required"))
		return
	}

	var containerID string
	var hostPort int
	cluster, aerr := h.mutateCacheCluster(r.Context(), id, func(cluster *CacheCluster) *protocol.AWSError {
		containerID = cluster.DockerContainerID
		hostPort = cluster.HostPort
		cluster.CacheClusterStatus = "deleting"
		return nil
	})
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	h.publish(r, events.ElastiCacheClusterDeleted, events.ResourcePayload{Name: id, ARN: cluster.ARN})

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDeleteCacheClusterResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlDeleteCacheClusterResult{CacheCluster: toXMLCacheCluster(h.cacheClusterForCaller(r.Context(), cluster))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})

	h.scheduler.CancelScoped(h.store.region(r.Context()), id, "health")

	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		_ = h.store.releasePort(r.Context(), hostPort) //nolint:errcheck
	}

	h.scheduler.AfterScoped(h.store.region(r.Context()), id, "delete", 50*time.Millisecond, func(ctx context.Context) {
		if aerr := h.store.deleteCacheCluster(ctx, id); aerr != nil {
			h.log.Warn("failed to delete cache cluster record", zap.String("cluster", id), zap.Error(aerr))
		}
	})
}

// ── Tagging ──────────────────────────────────────────────────────────────────

// ── Tagging XML types ─────────────────────────────────────────────────────────

type xmlTag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type xmlTagList struct {
	Items []xmlTag `xml:"Tag"`
}

type xmlAddTagsResponse struct {
	XMLName          xml.Name                  `xml:"AddTagsToResourceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlAddTagsResult          `xml:"AddTagsToResourceResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlAddTagsResult struct {
	TagList xmlTagList `xml:"TagList"`
}

type xmlListTagsResponse struct {
	XMLName          xml.Name                  `xml:"ListTagsForResourceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlListTagsResult         `xml:"ListTagsForResourceResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlListTagsResult struct {
	TagList xmlTagList `xml:"TagList"`
}

type xmlRemoveTagsResponse struct {
	XMLName          xml.Name                  `xml:"RemoveTagsFromResourceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

// ── Tagging handlers ──────────────────────────────────────────────────────────

func (h *Handler) AddTagsToResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	if arn == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ResourceName is required"))
		return
	}
	incoming := map[string]string{}
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Tags.Tag.%d.Key", i))
		if key == "" {
			break
		}
		incoming[key] = r.FormValue(fmt.Sprintf("Tags.Tag.%d.Value", i))
	}
	tags, aerr := serviceutil.ApplyStoreTags(r.Context(), h.store.tags(), arn, incoming, cacheTagCfg)
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	items := serviceutil.TagElements(tags, func(k, v string) xmlTag {
		return xmlTag{Key: k, Value: v}
	})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlAddTagsResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlAddTagsResult{TagList: xmlTagList{Items: items}},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	if arn == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ResourceName is required"))
		return
	}
	tags, aerr := h.store.tags().Load(r.Context(), arn)
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	items := serviceutil.TagElements(tags, func(k, v string) xmlTag {
		return xmlTag{Key: k, Value: v}
	})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlListTagsResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlListTagsResult{TagList: xmlTagList{Items: items}},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

func (h *Handler) RemoveTagsFromResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	if arn == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ResourceName is required"))
		return
	}
	var keys []string
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
		if key == "" {
			break
		}
		keys = append(keys, key)
	}
	if _, aerr := serviceutil.RemoveStoreTags(r.Context(), h.store.tags(), arn, keys); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlRemoveTagsResponse{
		Xmlns:            cacheXMLNS,
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── Docker helpers ───────────────────────────────────────────────────────────

// startCacheContainer creates (or reuses) and starts a Docker container for the
// given cache cluster. Supports redis, valkey, and memcached engines. Updates
// c.DockerContainerID, c.HostPort and the health-check dial target in place.
// Follows the same reuse-on-restart semantics as RDS.
func (h *Handler) startCacheContainer(ctx context.Context, c *CacheCluster) error {
	image := engineImage(c.Engine, c.EngineVersion)
	port := enginePort(c.Engine)

	containerName := "overcast-elasticache-" + c.CacheClusterId
	containerPort := fmt.Sprintf("%d/tcp", port)

	// Check for an existing container (post-restart reuse).
	if existing, err := h.docker.GetContainerByName(ctx, containerName); err == nil && existing != nil {
		if !existing.HasOvercastLabels(serviceName, c.CacheClusterId) {
			return fmt.Errorf("container %q exists but is not an overcast-managed ElastiCache container — refusing to reuse", containerName)
		}
		// The labels above prove it is an ElastiCache container for this
		// cluster's name; they do not prove it is *this* Overcast's. Container
		// names are unique per daemon and the name is derived from the cluster
		// ID, so two Overcasts sharing a daemon collide here — and reusing what
		// the other created hands both of them one redis, which this one will
		// later stop and delete on the strength of its own record. Scoping
		// reconciliation alone would not have held: a record whose container is
		// gone rebuilds, and the rebuild comes straight back through here.
		if owner := existing.Instance(); owner != "" && owner != h.instances.Resolve(ctx) {
			return fmt.Errorf("container %q was created by another Overcast instance (%s=%s) — refusing to reuse it for cache cluster %q; "+
				"two Overcasts sharing a Docker daemon cannot both run a cache cluster of that name",
				containerName, docker.LabelInstance, owner, c.CacheClusterId)
		}
		h.log.Info("ElastiCache: reusing existing container",
			zap.String("cluster", c.CacheClusterId),
			zap.String("container", existing.ID),
			zap.String("state", existing.State.Status))

		hostPort := 0
		if bindings, ok := existing.NetworkSettings.Ports[containerPort]; ok && len(bindings) > 0 {
			if p, err := strconv.Atoi(bindings[0].HostPort); err == nil {
				hostPort = p
			}
		}
		if hostPort == 0 {
			portBase := h.portBase()
			if hp, aerr := h.store.allocatePort(ctx, c.CacheClusterId, portBase); aerr == nil {
				hostPort = hp
			}
		} else {
			h.store.allocatePortFixed(ctx, c.CacheClusterId, hostPort) //nolint:errcheck
		}
		if !existing.State.Running {
			if err := h.docker.StartContainer(ctx, existing.ID); err != nil {
				return fmt.Errorf("start existing container: %w", err)
			}
		}
		c.DockerContainerID = existing.ID
		c.HostPort = hostPort
		// Re-attach: a container adopted from an earlier run predates the
		// current alias set. Attaching is idempotent.
		if err := h.attachAdoptedToDataPlane(ctx, existing.ID,
			h.vpcForSubnetGroup(ctx, c.CacheSubnetGroupName), h.subnetGroupSubnets(ctx, c.CacheSubnetGroupName),
			h.clusterEndpointAliases(c)); err != nil {
			h.log.Warn("ElastiCache: reused container could not join the data plane — "+
				"its endpoint name will not resolve for sibling containers",
				zap.String("cluster", c.CacheClusterId), zap.Error(err))
		}
		h.setContainerDialTarget(ctx, c)
		return nil
	}

	// Allocate a host port.
	hostPort, aerr := h.store.allocatePort(ctx, c.CacheClusterId, h.portBase())
	if aerr != nil {
		return fmt.Errorf("allocate port: %s", aerr.Message)
	}

	// Pull image (deduplicated per process lifetime).
	if err := h.puller.Ensure(ctx, image); err != nil {
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("pull image: %w", err)
	}

	req := &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image:        image,
			ExposedPorts: map[string]struct{}{containerPort: {}},
			Labels:       h.instances.ManagedLabels(ctx, serviceName, c.CacheClusterId),
		},
		HostConfig: &docker.HostConfig{AutoRemove: true,
			NetworkMode: dataplane.Primary(h.cfg),
			PortBindings: map[string][]docker.PortBinding{
				containerPort: {{HostIP: "0.0.0.0", HostPort: strconv.Itoa(hostPort)}},
			},
		},
		NetworkingConfig: dataplane.PrimaryEndpoints(h.cfg),
	}

	containerID, err := h.docker.CreateContainer(ctx, containerName, req)
	if err != nil {
		if docker.IsConflict(err) {
			h.log.Warn("ElastiCache: name conflict on create, retrying reuse",
				zap.String("cluster", c.CacheClusterId))
			h.store.releasePort(ctx, hostPort) //nolint:errcheck
			return h.startCacheContainer(ctx, c)
		}
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("create container: %w", err)
	}

	// Join the data plane before starting, so the node answers to its endpoint
	// names from the moment it accepts connections. Every name, on the plane
	// every consumer shares — an ECS task included, which is what #872 was.
	if err := h.attachToDataPlane(ctx, containerID, h.vpcForSubnetGroup(ctx, c.CacheSubnetGroupName),
		h.subnetGroupSubnets(ctx, c.CacheSubnetGroupName), h.clusterEndpointAliases(c)); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("ElastiCache %s: %w", c.CacheClusterId, err)
	}

	if err := h.docker.StartContainer(ctx, containerID); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("start container: %w", err)
	}

	c.DockerContainerID = containerID
	c.HostPort = hostPort
	h.setContainerDialTarget(ctx, c)
	return nil
}

// attachToDataPlane joins a cache container to the plane its consumers are on,
// advertising every name the API could hand out for it.
//
// A cache Overcast cannot place in a VPC — no subnet group, no subnets, or no
// EC2 service to ask — lands on the default plane. That is the same plane
// everything else without a VPC is on, so it stays reachable either way.
func (h *Handler) attachToDataPlane(ctx context.Context, containerID, vpcID string, subnetIDs, aliases []string) error {
	placement, err := h.placementFor(ctx, vpcID, subnetIDs, aliases)
	if err != nil {
		return err
	}
	return dataplane.Attach(ctx, h.docker, h.cfg, containerID, placement)
}

// attachAdoptedToDataPlane is attachToDataPlane for a container reused after a
// restart, which may predate the current plane layout entirely.
func (h *Handler) attachAdoptedToDataPlane(ctx context.Context, containerID, vpcID string, subnetIDs, aliases []string) error {
	placement, err := h.placementFor(ctx, vpcID, subnetIDs, aliases)
	if err != nil {
		return err
	}
	return dataplane.AttachAdopted(ctx, h.docker, h.cfg, containerID, placement)
}

// placementFor turns a resolved VPC and the subnets the cache named into the
// Placement its container should take. The subnets matter under
// OVERCAST_VPC_EGRESS=routed, where their route tables decide the route out.
func (h *Handler) placementFor(ctx context.Context, vpcID string, subnetIDs, aliases []string) (dataplane.Placement, error) {
	var resolver dataplane.VPCResolver
	if h.vpcResolver != nil {
		resolver = h.vpcResolver
	}
	placement, err := dataplane.PlaceInSubnets(ctx, resolver, vpcID, subnetIDs)
	if err != nil {
		return placement, err
	}
	placement.Aliases = aliases
	return placement, nil
}

// subnetGroupSubnets returns the subnets a cache subnet group names, or nil
// for no group or one Overcast has no record of.
func (h *Handler) subnetGroupSubnets(ctx context.Context, name string) []string {
	if name == "" {
		return nil
	}
	sg, aerr := h.store.getCacheSubnetGroup(ctx, name)
	if aerr != nil || sg == nil {
		return nil
	}
	return sg.SubnetIds
}

// setContainerDialTarget records how Overcast reaches the cluster's container
// for its health check. It deliberately leaves ConfigurationEndpoint alone:
// that is what callers are told, and it is rendered per caller on the way out
// (endpoint.go).
func (h *Handler) setContainerDialTarget(ctx context.Context, c *CacheCluster) {
	c.DialAddress, c.DialPort = h.containerDialTarget(ctx, c.DockerContainerID, c.Engine, c.HostPort)
}

// setReplicationGroupDialTarget is setContainerDialTarget for a replication
// group.
func (h *Handler) setReplicationGroupDialTarget(ctx context.Context, rg *ReplicationGroup) {
	rg.DialAddress, rg.DialPort = h.containerDialTarget(ctx, rg.DockerContainerID, rg.Engine, rg.HostPort)
}

// scheduleClusterHealthCheck is scheduleHealthCheck aimed at c's dial target.
func (h *Handler) scheduleClusterHealthCheck(region, clusterID string, c *CacheCluster) {
	host, port := c.dialTarget()
	h.scheduleHealthCheck(region, clusterID, host, port)
}

// scheduleGroupHealthCheck is scheduleReplicationGroupHealthCheck aimed at
// rg's dial target.
func (h *Handler) scheduleGroupHealthCheck(region, rgID string, rg *ReplicationGroup) {
	host, port := rg.dialTarget()
	h.scheduleReplicationGroupHealthCheck(region, rgID, host, port)
}

// cleanupCacheContainer releases the port reservation for a cache cluster.
// Docker container stop/remove is handled by the GC.
//
//nolint:unused // Kept for explicit Docker cleanup call sites.
func (h *Handler) cleanupCacheContainer(ctx context.Context, clusterID, containerID string, hostPort int) {
	if hostPort > 0 {
		if aerr := h.store.releasePort(ctx, hostPort); aerr != nil {
			h.log.Warn("ElastiCache cleanup: release port",
				zap.String("cluster", clusterID), zap.Int("port", hostPort), zap.Error(aerr))
		}
	}
}

// scheduleHealthCheck waits for the cache engine to answer its own protocol and
// transitions the cluster to "available", or to a terminal failure carrying why
// when it never does. region is the region the cluster is stored under — the
// callbacks run outside any request context.
func (h *Handler) scheduleHealthCheck(region, clusterID, host string, port int) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	readiness.Watch{
		Scheduler:  h.scheduler,
		Clock:      h.clk,
		Region:     region,
		ResourceID: clusterID,
		Transition: "health",
		Subject:    "the cache engine at " + addr,
		Policy:     cacheReadiness,
		Probe: func(ctx context.Context) readiness.Result {
			got, aerr := h.store.getCacheCluster(ctx, clusterID)
			if aerr != nil || got == nil {
				return readiness.Abandoned()
			}
			if !statusIn(got.CacheClusterStatus, cacheClusterAwaiting) {
				// Stopped, deleted or modified while starting — not ours.
				return readiness.Abandoned()
			}
			if err := probeEngine(ctx, got.Engine, addr); err != nil {
				return readiness.Retry(err)
			}
			return readiness.Answered()
		},
		OnReady: func(ctx context.Context) {
			h.transitionCacheCluster(ctx, clusterID, "available", cacheClusterAwaiting...)
		},
		OnFailed: func(ctx context.Context, reason string) {
			h.failCacheCluster(ctx, clusterID, reason)
		},
	}.Start()
}

// teardownOrphanedContainer removes a container whose resource record was
// deleted while the container was still starting. The delete path could not
// stop it — the container ID had not been persisted yet — so the start
// goroutine owns the cleanup, including the port it allocated. Mirrors the
// RDS fix for the same race (#412); the compat teardowns create-and-delete
// fast enough to hit this window on every run.
func (h *Handler) teardownOrphanedContainer(ctx context.Context, kind, id, containerID string, hostPort int) {
	h.log.Info("ElastiCache: "+kind+" deleted while its container was starting — removing container",
		zap.String("id", id), zap.String("container", containerID))
	if containerID != "" {
		if h.gc != nil {
			h.gc.StopNow(containerID)
			h.gc.ScheduleRemove(containerID)
		} else {
			_ = h.docker.StopContainer(ctx, containerID, 10)     //nolint:errcheck
			_ = h.docker.RemoveContainer(ctx, containerID, true) //nolint:errcheck
		}
	}
	if hostPort > 0 {
		_ = h.store.releasePort(ctx, hostPort) //nolint:errcheck
	}
}

// startReplicationGroupContainer creates (or reuses) and starts a single Docker
// container for the given replication group (primary node). Updates rg.DockerContainerID,
// rg.HostPort and the health-check dial target in place.
func (h *Handler) startReplicationGroupContainer(ctx context.Context, rg *ReplicationGroup) error {
	image := engineImage(rg.Engine, rg.EngineVersion)
	port := enginePort(rg.Engine)

	containerName := "overcast-elasticache-rg-" + rg.ReplicationGroupId
	containerPort := fmt.Sprintf("%d/tcp", port)
	resourceLabel := "rg:" + rg.ReplicationGroupId

	if existing, err := h.docker.GetContainerByName(ctx, containerName); err == nil && existing != nil {
		if !existing.HasOvercastLabels(serviceName, resourceLabel) {
			return fmt.Errorf("container %q exists but is not an overcast-managed replication group container — refusing to reuse", containerName)
		}
		// Somebody else's container for a group of the same name — see the
		// cache-cluster reuse path for why the labels above do not settle it.
		if owner := existing.Instance(); owner != "" && owner != h.instances.Resolve(ctx) {
			return fmt.Errorf("container %q was created by another Overcast instance (%s=%s) — refusing to reuse it for replication group %q; "+
				"two Overcasts sharing a Docker daemon cannot both run a replication group of that name",
				containerName, docker.LabelInstance, owner, rg.ReplicationGroupId)
		}
		h.log.Info("ElastiCache: reusing existing replication group container",
			zap.String("rg", rg.ReplicationGroupId),
			zap.String("container", existing.ID))

		hostPort := 0
		if bindings, ok := existing.NetworkSettings.Ports[containerPort]; ok && len(bindings) > 0 {
			if p, err := strconv.Atoi(bindings[0].HostPort); err == nil {
				hostPort = p
			}
		}
		if hostPort == 0 {
			if hp, aerr := h.store.allocatePort(ctx, resourceLabel, h.portBase()); aerr == nil {
				hostPort = hp
			}
		} else {
			h.store.allocatePortFixed(ctx, resourceLabel, hostPort) //nolint:errcheck
		}
		if !existing.State.Running {
			if err := h.docker.StartContainer(ctx, existing.ID); err != nil {
				return fmt.Errorf("start existing container: %w", err)
			}
		}
		rg.DockerContainerID = existing.ID
		rg.HostPort = hostPort
		// Re-attach with the group's own placement, as the cache-cluster path
		// does: an adopted container predates the current alias set and may
		// predate VPC placement entirely, and one rejoined without its VPC
		// lands on the default plane where the group's own consumers cannot
		// see it.
		if err := h.attachAdoptedToDataPlane(ctx, existing.ID,
			h.vpcForSubnetGroup(ctx, rg.CacheSubnetGroupName), h.subnetGroupSubnets(ctx, rg.CacheSubnetGroupName),
			h.replicationGroupEndpointAliases(rg)); err != nil {
			h.log.Warn("ElastiCache: reused container could not join the data plane — "+
				"its endpoint name will not resolve for sibling containers",
				zap.String("rg", rg.ReplicationGroupId), zap.Error(err))
		}
		h.setReplicationGroupDialTarget(ctx, rg)
		return nil
	}

	hostPort, aerr := h.store.allocatePort(ctx, resourceLabel, h.portBase())
	if aerr != nil {
		return fmt.Errorf("allocate port: %s", aerr.Message)
	}

	if err := h.puller.Ensure(ctx, image); err != nil {
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("pull image: %w", err)
	}

	req := &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image:        image,
			ExposedPorts: map[string]struct{}{containerPort: {}},
			Labels:       h.instances.ManagedLabels(ctx, serviceName, resourceLabel),
		},
		HostConfig: &docker.HostConfig{AutoRemove: true,
			NetworkMode: dataplane.Primary(h.cfg),
			PortBindings: map[string][]docker.PortBinding{
				containerPort: {{HostIP: "0.0.0.0", HostPort: strconv.Itoa(hostPort)}},
			},
		},
		NetworkingConfig: dataplane.PrimaryEndpoints(h.cfg),
	}

	containerID, err := h.docker.CreateContainer(ctx, containerName, req)
	if err != nil {
		if docker.IsConflict(err) {
			h.store.releasePort(ctx, hostPort) //nolint:errcheck
			return h.startReplicationGroupContainer(ctx, rg)
		}
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("create container: %w", err)
	}

	// Placed in the VPC its subnet group belongs to, the same way a cache
	// cluster is. A group created without one resolves to no VPC and stays on
	// the default plane, which is where AWS puts a cache outside a VPC too.
	if err := h.attachToDataPlane(ctx, containerID,
		h.vpcForSubnetGroup(ctx, rg.CacheSubnetGroupName), h.subnetGroupSubnets(ctx, rg.CacheSubnetGroupName),
		h.replicationGroupEndpointAliases(rg)); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("ElastiCache %s: %w", rg.ReplicationGroupId, err)
	}

	if err := h.docker.StartContainer(ctx, containerID); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("start container: %w", err)
	}

	rg.DockerContainerID = containerID
	rg.HostPort = hostPort
	h.setReplicationGroupDialTarget(ctx, rg)
	return nil
}

// Endpoint hostnames. A cache endpoint is a data-plane name: it points at the
// engine container, not at Overcast. AWS's shapes are
// `{cluster}.{hash}.cfg.{region}.cache.amazonaws.com` for a cluster and
// `{group}.{hash}.ng.{region}.cache.amazonaws.com` for a replication group;
// Overcast mints the same grammar with the account-specific hash dropped, on
// each base it could hand a caller. See dataplane.Hostnames for why the whole
// set is registered rather than the configured name alone.

func clusterEndpointHostname(id, region, base string) string {
	return fmt.Sprintf("%s.%s.cfg.%s", id, region, base)
}

func replicationGroupEndpointHostname(id, region, base string) string {
	return fmt.Sprintf("%s.%s.ng.cfg.%s", id, region, base)
}

func (h *Handler) clusterEndpointAliases(c *CacheCluster) []string {
	if c == nil || c.CacheClusterId == "" {
		return nil
	}
	return dataplane.Hostnames(h.cfg, func(base string) string {
		return clusterEndpointHostname(c.CacheClusterId, h.region(), base)
	}, advertisedAddress(c.ConfigurationEndpoint))
}

func (h *Handler) replicationGroupEndpointAliases(rg *ReplicationGroup) []string {
	if rg == nil || rg.ReplicationGroupId == "" {
		return nil
	}
	return dataplane.Hostnames(h.cfg, func(base string) string {
		return replicationGroupEndpointHostname(rg.ReplicationGroupId, h.region(), base)
	}, advertisedAddress(rg.ConfigurationEndpoint))
}

// advertisedAddress is whatever a stored record already hands out, so a
// container keeps answering to a name minted before the current configuration.
func advertisedAddress(ep *ClusterEndpoint) string {
	if ep == nil {
		return ""
	}
	return ep.Address
}

// requireCacheSubnetGroup refuses a create that names a subnet group Overcast
// has no record of, with AWS's CacheSubnetGroupNotFoundFault.
//
// It used to be accepted and ignored, which put the cache on the default plane
// with nothing said. That is the worst place for a typo to land: the cache
// runs, DescribeCacheClusters echoes the name that was asked for, and the
// first sign anything is wrong is a consumer in the intended VPC that cannot
// resolve the endpoint. An empty name is fine — that is the default VPC, on
// AWS and here.
func (h *Handler) requireCacheSubnetGroup(ctx context.Context, name string) *protocol.AWSError {
	if name == "" {
		return nil
	}
	if _, aerr := h.store.getCacheSubnetGroup(ctx, name); aerr != nil {
		return errSubnetGroupNotFound(name)
	}
	return nil
}

// vpcForSubnetGroup returns the VPC a cache subnet group belongs to, or "" for
// a cache that named no group or named one Overcast has no record of. Both
// mean the default plane rather than an error: a cache with nowhere better to
// be is still a working cache.
//
// A subnet group whose own VpcId was never populated is resolved through its
// subnets, which is how RDS fills the same field in.
func (h *Handler) vpcForSubnetGroup(ctx context.Context, name string) string {
	if name == "" {
		return ""
	}
	sg, aerr := h.store.getCacheSubnetGroup(ctx, name)
	if aerr != nil || sg == nil {
		return ""
	}
	if sg.VpcId != "" {
		return sg.VpcId
	}
	return h.vpcForSubnets(ctx, sg.SubnetIds)
}

// vpcForSubnets resolves the first subnet that names a VPC. A serverless cache
// carries subnet IDs directly rather than through a group.
func (h *Handler) vpcForSubnets(ctx context.Context, subnetIDs []string) string {
	if h.vpcResolver == nil {
		return ""
	}
	for _, id := range subnetIDs {
		if vpcID := h.vpcResolver.VpcIDForSubnet(ctx, id); vpcID != "" {
			return vpcID
		}
	}
	return ""
}

func (h *Handler) region() string {
	if h.cfg != nil && h.cfg.Region != "" {
		return h.cfg.Region
	}
	return "us-east-1"
}

// cleanupReplicationGroupContainer releases the port for a replication group container.
// Docker container stop/remove is handled by the GC.
//
//nolint:unused // Kept for explicit Docker cleanup call sites.
func (h *Handler) cleanupReplicationGroupContainer(ctx context.Context, rgID, containerID string, hostPort int) {
	if hostPort > 0 {
		if aerr := h.store.releasePort(ctx, hostPort); aerr != nil {
			h.log.Warn("ElastiCache cleanup: release RG port",
				zap.String("rg", rgID), zap.Int("port", hostPort), zap.Error(aerr))
		}
	}
}

// scheduleReplicationGroupHealthCheck waits for the group's primary node to
// answer and transitions the group to "available", or to "create-failed"
// carrying why when it never does.
//
// The exhaustion branch used to transition to "available" (#881). Thirty failed
// dials in a row were treated as grounds for reporting a working replication
// group, so DescribeReplicationGroups answered "available" for a group with
// nothing behind its configuration endpoint — and unlike every other instance
// of this bug, it did not even need a container to have existed: a group whose
// start never got as far as Docker took the same path.
//
// region is the region the replication group is stored under.
func (h *Handler) scheduleReplicationGroupHealthCheck(region, rgID, host string, port int) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	readiness.Watch{
		Scheduler:  h.scheduler,
		Clock:      h.clk,
		Region:     region,
		ResourceID: rgID,
		Transition: "rg-health",
		Subject:    "the primary node at " + addr,
		Policy:     cacheReadiness,
		Probe: func(ctx context.Context) readiness.Result {
			got, aerr := h.store.getReplicationGroup(ctx, rgID)
			if aerr != nil || got == nil {
				return readiness.Abandoned()
			}
			if !statusIn(got.Status, replicationGroupAwaiting) {
				return readiness.Abandoned()
			}
			if err := probeEngine(ctx, got.Engine, addr); err != nil {
				return readiness.Retry(err)
			}
			return readiness.Answered()
		},
		OnReady: func(ctx context.Context) {
			h.transitionReplicationGroup(ctx, rgID, "available", replicationGroupAwaiting...)
		},
		OnFailed: func(ctx context.Context, reason string) {
			h.failReplicationGroup(ctx, rgID, reason)
		},
	}.Start()
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func (h *Handler) portBase() int {
	if h.cfg.ElastiCachePortBase > 0 {
		return h.cfg.ElastiCachePortBase
	}
	return 63790
}

// VPCNetworkResolver resolves cache placement against EC2 VPC network state.
// Implemented by the EC2 service; nil when EC2 is not enabled.
type VPCNetworkResolver interface {
	dataplane.VPCResolver
	// VpcIDForSubnet returns the VPC ID that owns the given subnet.
	VpcIDForSubnet(ctx context.Context, subnetID string) string
}

// ── ModifyCacheCluster ───────────────────────────────────────────────────────

func (h *Handler) ModifyCacheCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CacheClusterId")
	if id == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("CacheClusterId is required"))
		return
	}

	cluster, aerr := h.store.getCacheCluster(r.Context(), id)
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	if v := r.FormValue("CacheNodeType"); v != "" {
		cluster.CacheNodeType = v
	}
	if v := r.FormValue("EngineVersion"); v != "" {
		cluster.EngineVersion = v
	}
	if v := r.FormValue("NumCacheNodes"); v != "" {
		cluster.NumCacheNodes = formInt(r, "NumCacheNodes", cluster.NumCacheNodes)
	}
	if v := r.FormValue("CacheParameterGroupName"); v != "" {
		cluster.CacheParameterGroupName = v
	}

	cluster.CacheClusterStatus = "modifying"
	if aerr := h.store.putCacheCluster(r.Context(), cluster); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	h.publish(r, events.ElastiCacheClusterModified, events.ResourcePayload{Name: id, ARN: cluster.ARN})

	// Schedule transition back to available.
	h.scheduler.AfterScoped(h.store.region(r.Context()), id, "available", 0, func(ctx context.Context) {
		got, aerr := h.store.getCacheCluster(ctx, id)
		if aerr != nil {
			return
		}
		if got.CacheClusterStatus == "modifying" {
			got.CacheClusterStatus = "available"
			h.store.putCacheCluster(ctx, got) //nolint:errcheck
		}
	})

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlModifyCacheClusterResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlModifyCacheClusterResult{CacheCluster: toXMLCacheCluster(h.cacheClusterForCaller(r.Context(), cluster))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

func toXMLCacheCluster(c *CacheCluster) xmlCacheCluster {
	out := xmlCacheCluster{
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
		out.ConfigurationEndpoint = &xmlEndpoint{
			Address: c.ConfigurationEndpoint.Address,
			Port:    c.ConfigurationEndpoint.Port,
		}
	}
	return out
}

func formInt(r *http.Request, key string, def int) int {
	v := r.FormValue(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
