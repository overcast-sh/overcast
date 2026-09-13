package elasticache

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/serviceutil/readiness"
)

type xmlCreateServerlessCacheResponse struct {
	XMLName          xml.Name                       `xml:"CreateServerlessCacheResponse"`
	Xmlns            string                         `xml:"xmlns,attr"`
	Result           xmlCreateServerlessCacheResult `xml:"CreateServerlessCacheResult"`
	ResponseMetadata protocol.ResponseMetadata      `xml:"ResponseMetadata"`
}

type xmlCreateServerlessCacheResult struct {
	ServerlessCache xmlServerlessCache `xml:"ServerlessCache"`
}

type xmlDescribeServerlessCachesResponse struct {
	XMLName          xml.Name                          `xml:"DescribeServerlessCachesResponse"`
	Xmlns            string                            `xml:"xmlns,attr"`
	Result           xmlDescribeServerlessCachesResult `xml:"DescribeServerlessCachesResult"`
	ResponseMetadata protocol.ResponseMetadata         `xml:"ResponseMetadata"`
}

type xmlDescribeServerlessCachesResult struct {
	NextToken        string              `xml:"NextToken,omitempty"`
	ServerlessCaches xmlServerlessCaches `xml:"ServerlessCaches"`
}

type xmlServerlessCaches struct {
	Items []xmlServerlessCache `xml:"ServerlessCache"`
}

type xmlDeleteServerlessCacheResponse struct {
	XMLName          xml.Name                       `xml:"DeleteServerlessCacheResponse"`
	Xmlns            string                         `xml:"xmlns,attr"`
	Result           xmlDeleteServerlessCacheResult `xml:"DeleteServerlessCacheResult"`
	ResponseMetadata protocol.ResponseMetadata      `xml:"ResponseMetadata"`
}

type xmlDeleteServerlessCacheResult struct {
	ServerlessCache xmlServerlessCache `xml:"ServerlessCache"`
}

type xmlModifyServerlessCacheResponse struct {
	XMLName          xml.Name                       `xml:"ModifyServerlessCacheResponse"`
	Xmlns            string                         `xml:"xmlns,attr"`
	Result           xmlModifyServerlessCacheResult `xml:"ModifyServerlessCacheResult"`
	ResponseMetadata protocol.ResponseMetadata      `xml:"ResponseMetadata"`
}

type xmlModifyServerlessCacheResult struct {
	ServerlessCache xmlServerlessCache `xml:"ServerlessCache"`
}

type xmlServerlessCache struct {
	ServerlessCacheName string               `xml:"ServerlessCacheName"`
	Description         string               `xml:"Description"`
	Status              string               `xml:"Status"`
	Engine              string               `xml:"Engine"`
	MajorEngineVersion  string               `xml:"MajorEngineVersion"`
	FullEngineVersion   string               `xml:"FullEngineVersion"`
	CacheUsageLimits    *xmlCacheUsageLimits `xml:"CacheUsageLimits,omitempty"`
	SubnetIds           xmlStringSet         `xml:"SubnetIds,omitempty"`
	SecurityGroupIds    xmlStringSet         `xml:"SecurityGroupIds,omitempty"`
	Endpoint            *xmlEndpoint         `xml:"Endpoint,omitempty"`
	ReaderEndpoint      *xmlEndpoint         `xml:"ReaderEndpoint,omitempty"`
	ARN                 string               `xml:"ARN"`
	SnapshotRetention   int                  `xml:"SnapshotRetentionLimit"`
	DailySnapshotTime   string               `xml:"DailySnapshotTime,omitempty"`
	NetworkType         string               `xml:"NetworkType,omitempty"`
	UserGroupId         string               `xml:"UserGroupId,omitempty"`
	KmsKeyId            string               `xml:"KmsKeyId,omitempty"`
	CreateTime          string               `xml:"CreateTime,omitempty"`
}

type xmlStringSet struct {
	Items []string `xml:"member"`
}

type xmlCacheUsageLimits struct {
	DataStorage   *xmlDataStorageLimit `xml:"DataStorage,omitempty"`
	ECPUPerSecond *xmlECPULimit        `xml:"ECPUPerSecond,omitempty"`
}

type xmlDataStorageLimit struct {
	Maximum int    `xml:"Maximum"`
	Unit    string `xml:"Unit"`
}

type xmlECPULimit struct {
	Maximum int `xml:"Maximum"`
}

func (h *Handler) CreateServerlessCache(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.FormValue("ServerlessCacheName"))
	if name == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ServerlessCacheName is required"))
		return
	}

	if _, aerr := h.store.getServerlessCache(r.Context(), name); aerr == nil {
		protocol.WriteQueryXMLError(w, r, errServerlessCacheAlreadyExists(name))
		return
	}

	engine := r.FormValue("Engine")
	if engine == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("Engine is required"))
		return
	}
	if engine != "redis" && engine != "memcached" && engine != "valkey" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("Engine must be redis, valkey, or memcached"))
		return
	}

	major := r.FormValue("MajorEngineVersion")
	fullVersion := engineDefaultVersion(engine)
	if major == "" {
		major = fullVersion
	}

	region := h.store.region(r.Context())
	arn := fmt.Sprintf("arn:aws:elasticache:%s:%s:serverlesscache:%s", region, h.cfg.AccountID, name)
	endpoint := &ClusterEndpoint{Address: fmt.Sprintf("%s.%s.serverless.%s", name, region, h.cfg.ExternalHostname()), Port: enginePort(engine)}

	cache := &ServerlessCache{
		ServerlessCacheName:   name,
		Description:           r.FormValue("Description"),
		Status:                "creating",
		Engine:                engine,
		MajorEngineVersion:    major,
		FullEngineVersion:     fullVersion,
		CacheUsageLimits:      formCacheUsageLimits(r),
		ARN:                   arn,
		CreateTime:            h.clk.Now().UTC().Format(time.RFC3339Nano),
		Endpoint:              endpoint,
		ReaderEndpoint:        endpoint,
		SubnetIds:             formStringList(r, "SubnetIds.SubnetId"),
		SecurityGroupIds:      formStringList(r, "SecurityGroupIds.SecurityGroupId"),
		SnapshotArnsToRestore: formStringList(r, "SnapshotArnsToRestore.SnapshotArn"),
		SnapshotRetention:     formInt(r, "SnapshotRetentionLimit", 0),
		DailySnapshotTime:     r.FormValue("DailySnapshotTime"),
		NetworkType:           r.FormValue("NetworkType"),
		UserGroupId:           r.FormValue("UserGroupId"),
		KmsKeyId:              r.FormValue("KmsKeyId"),
	}
	if cache.NetworkType == "" {
		cache.NetworkType = "ipv4"
	}

	// Create-time tags are validated before anything is written, so a rejected
	// tag set fails the create rather than leaving a cache that exists with the
	// tags the caller asked for missing.
	tags := formTags(r)
	if aerr := serviceutil.ValidateTags(cacheTagCfg, tags); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	if aerr := h.store.putServerlessCache(r.Context(), cache); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	if len(tags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(r.Context(), h.store.tags(), arn, tags, cacheTagCfg); aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
	}

	if h.dockerReady.Load() {
		if h.puller != nil {
			h.puller.Prewarm(engineImage(engine, fullVersion))
		}
		h.dockerWg.Add(1)
		go func(cacheName string) {
			defer h.dockerWg.Done()
			bgCtx := middleware.ContextWithRegion(h.bgCtx, region)
			got, aerr := h.store.getServerlessCache(bgCtx, cacheName)
			if aerr != nil || got == nil {
				return
			}
			if err := h.startServerlessCacheContainer(bgCtx, got); err != nil {
				h.failServerlessCache(bgCtx, cacheName, fmt.Sprintf("the cache container could not be created: %v", err))
				return
			}
			// The start took real time; the cache may have been deleted
			// meanwhile, and the delete could not stop this container — its ID
			// was not persisted yet — so the start goroutine owns the teardown.
			// Merge the container fields into a fresh read rather than
			// persisting the pre-start snapshot.
			if _, aerr := h.mutateServerlessCache(bgCtx, cacheName, func(stored *ServerlessCache) *protocol.AWSError {
				if stored.Status == "deleting" {
					return errRecordMovedOn
				}
				stored.DockerContainerID = got.DockerContainerID
				stored.HostPort = got.HostPort
				stored.DialAddress, stored.DialPort = got.DialAddress, got.DialPort
				return nil
			}); aerr != nil {
				if aerr != errRecordMovedOn {
					h.log.Warn("ElastiCache: persist post-start serverless cache",
						zap.String("cache", cacheName), zap.String("error", aerr.Message))
				}
				h.teardownOrphanedContainer(bgCtx, "serverless cache", cacheName, got.DockerContainerID, got.HostPort)
				return
			}
			h.scheduleServerlessCacheHealthCheck(region, cacheName, got)
		}(name)
	} else {
		// No container is coming, so nothing else will ever move this cache out
		// of "creating". See settleServerlessCacheWithoutRuntime.
		h.settleServerlessCacheWithoutRuntime(region, name)
	}

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlCreateServerlessCacheResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlCreateServerlessCacheResult{ServerlessCache: toXMLServerlessCache(h.serverlessCacheForCaller(r.Context(), cache))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

func (h *Handler) DescribeServerlessCaches(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.FormValue("ServerlessCacheName"))
	if name != "" {
		cache, aerr := h.store.getServerlessCache(r.Context(), name)
		if aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
		protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeServerlessCachesResponse{
			Xmlns: cacheXMLNS,
			Result: xmlDescribeServerlessCachesResult{
				ServerlessCaches: xmlServerlessCaches{Items: []xmlServerlessCache{toXMLServerlessCache(h.serverlessCacheForCaller(r.Context(), cache))}},
			},
			ResponseMetadata: protocol.QueryResponseMetadata(r),
		})
		return
	}

	caches, aerr := h.store.listServerlessCaches(r.Context())
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	sort.Slice(caches, func(i, j int) bool {
		return caches[i].ServerlessCacheName < caches[j].ServerlessCacheName
	})
	start := 0
	if token := r.FormValue("NextToken"); token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil || parsed < 0 || parsed > len(caches) {
			protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("NextToken is invalid"))
			return
		}
		start = parsed
	}
	maxResults := formInt(r, "MaxResults", 50)
	if maxResults <= 0 {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("MaxResults must be greater than zero"))
		return
	}
	end := start + maxResults
	nextToken := ""
	if end < len(caches) {
		nextToken = strconv.Itoa(end)
	} else {
		end = len(caches)
	}
	items := make([]xmlServerlessCache, 0, end-start)
	for _, cache := range caches[start:end] {
		items = append(items, toXMLServerlessCache(h.serverlessCacheForCaller(r.Context(), cache)))
	}
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeServerlessCachesResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlDescribeServerlessCachesResult{NextToken: nextToken, ServerlessCaches: xmlServerlessCaches{Items: items}},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

func (h *Handler) ModifyServerlessCache(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.FormValue("ServerlessCacheName"))
	if name == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ServerlessCacheName is required"))
		return
	}
	cache, aerr := h.store.getServerlessCache(r.Context(), name)
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	if v := r.FormValue("Description"); v != "" {
		cache.Description = v
	}
	if v := r.FormValue("Engine"); v != "" {
		if v != "redis" && v != "memcached" && v != "valkey" {
			protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("Engine must be redis, valkey, or memcached"))
			return
		}
		cache.Engine = v
		cache.FullEngineVersion = engineDefaultVersion(v)
	}
	if v := r.FormValue("MajorEngineVersion"); v != "" {
		cache.MajorEngineVersion = v
	}
	if r.FormValue("CacheUsageLimits.DataStorage.Maximum") != "" || r.FormValue("CacheUsageLimits.DataStorage.Unit") != "" || r.FormValue("CacheUsageLimits.ECPUPerSecond.Maximum") != "" {
		cache.CacheUsageLimits = formCacheUsageLimits(r)
	}
	if r.FormValue("SecurityGroupIds.SecurityGroupId.1") != "" {
		cache.SecurityGroupIds = formStringList(r, "SecurityGroupIds.SecurityGroupId")
	}
	if v := r.FormValue("SnapshotRetentionLimit"); v != "" {
		cache.SnapshotRetention = formInt(r, "SnapshotRetentionLimit", cache.SnapshotRetention)
	}
	if v := r.FormValue("DailySnapshotTime"); v != "" {
		cache.DailySnapshotTime = v
	}
	if v := r.FormValue("UserGroupId"); v != "" {
		cache.UserGroupId = v
	}
	if strings.EqualFold(r.FormValue("RemoveUserGroup"), "true") {
		cache.UserGroupId = ""
	}
	cache.Status = "modifying"
	if aerr := h.store.putServerlessCache(r.Context(), cache); aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}
	h.scheduler.AfterScoped(h.store.region(r.Context()), name, "serverless-available", 0, func(ctx context.Context) {
		h.transitionServerlessCache(ctx, name, "available", "modifying")
	})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlModifyServerlessCacheResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlModifyServerlessCacheResult{ServerlessCache: toXMLServerlessCache(h.serverlessCacheForCaller(r.Context(), cache))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

func (h *Handler) DeleteServerlessCache(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.FormValue("ServerlessCacheName"))
	if name == "" {
		protocol.WriteQueryXMLError(w, r, errInvalidParameterValue("ServerlessCacheName is required"))
		return
	}
	var containerID string
	var hostPort int
	cache, aerr := h.mutateServerlessCache(r.Context(), name, func(cache *ServerlessCache) *protocol.AWSError {
		containerID = cache.DockerContainerID
		hostPort = cache.HostPort
		cache.Status = "deleting"
		return nil
	})
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDeleteServerlessCacheResponse{
		Xmlns:            cacheXMLNS,
		Result:           xmlDeleteServerlessCacheResult{ServerlessCache: toXMLServerlessCache(h.serverlessCacheForCaller(r.Context(), cache))},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})

	h.scheduler.CancelScoped(h.store.region(r.Context()), name, "serverless-health")
	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		_ = h.store.releasePort(r.Context(), hostPort) //nolint:errcheck
	}
	h.scheduler.AfterScoped(h.store.region(r.Context()), name, "serverless-delete", 50*time.Millisecond, func(ctx context.Context) {
		if aerr := h.store.deleteServerlessCache(ctx, name); aerr != nil {
			h.log.Warn("failed to delete serverless cache record", zap.String("cache", name), zap.Error(aerr))
		}
	})
}

func (h *Handler) startServerlessCacheContainer(ctx context.Context, c *ServerlessCache) error {
	image := engineImage(c.Engine, c.FullEngineVersion)
	port := enginePort(c.Engine)
	containerName := "overcast-elasticache-serverless-" + c.ServerlessCacheName
	containerPort := fmt.Sprintf("%d/tcp", port)
	resourceLabel := "serverless:" + c.ServerlessCacheName

	if existing, err := h.docker.GetContainerByName(ctx, containerName); err == nil && existing != nil {
		if !existing.HasOvercastLabels(serviceName, resourceLabel) {
			return fmt.Errorf("container %q exists but is not an overcast-managed serverless cache container — refusing to reuse", containerName)
		}
		// Somebody else's container for a cache of the same name — see the
		// cache-cluster reuse path for why the labels above do not settle it.
		if owner := existing.Instance(); owner != "" && owner != h.instances.Resolve(ctx) {
			return fmt.Errorf("container %q was created by another Overcast instance (%s=%s) — refusing to reuse it for serverless cache %q; "+
				"two Overcasts sharing a Docker daemon cannot both run a serverless cache of that name",
				containerName, docker.LabelInstance, owner, c.ServerlessCacheName)
		}
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
		c.DockerContainerID = existing.ID
		c.HostPort = hostPort
		if err := h.attachAdoptedToDataPlane(ctx, existing.ID,
			h.vpcForSubnets(ctx, c.SubnetIds), c.SubnetIds, h.serverlessEndpointAliases(c)); err != nil {
			h.log.Warn("ElastiCache: reused container could not join the data plane — "+
				"its endpoint name will not resolve for sibling containers",
				zap.String("cache", c.ServerlessCacheName), zap.Error(err))
		}
		h.setServerlessDialTarget(ctx, c)
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
			return h.startServerlessCacheContainer(ctx, c)
		}
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("create container: %w", err)
	}
	// A serverless cache carries subnet IDs rather than a subnet group, so the
	// VPC is resolved from the first of them EC2 can name.
	if err := h.attachToDataPlane(ctx, containerID, h.vpcForSubnets(ctx, c.SubnetIds), c.SubnetIds, h.serverlessEndpointAliases(c)); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("ElastiCache %s: %w", c.ServerlessCacheName, err)
	}
	if err := h.docker.StartContainer(ctx, containerID); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("start container: %w", err)
	}
	c.DockerContainerID = containerID
	c.HostPort = hostPort
	h.setServerlessDialTarget(ctx, c)
	return nil
}

func (h *Handler) serverlessEndpointAliases(c *ServerlessCache) []string {
	if c == nil || c.ServerlessCacheName == "" {
		return nil
	}
	var advertised []string
	if c.Endpoint != nil {
		advertised = append(advertised, c.Endpoint.Address)
	}
	if c.ReaderEndpoint != nil {
		advertised = append(advertised, c.ReaderEndpoint.Address)
	}
	return dataplane.Hostnames(h.cfg, func(base string) string {
		return serverlessEndpointHostname(c.ServerlessCacheName, h.region(), base)
	}, advertised...)
}

// setServerlessDialTarget records how Overcast reaches the cache's container
// for its health check — see setContainerDialTarget. Writer and reader are the
// same container here: there is one node, and a serverless cache's reader
// endpoint is an addressing convenience on AWS rather than a second engine.
func (h *Handler) setServerlessDialTarget(ctx context.Context, c *ServerlessCache) {
	c.DialAddress, c.DialPort = h.containerDialTarget(ctx, c.DockerContainerID, c.Engine, c.HostPort)
}

// scheduleServerlessCacheHealthCheck is scheduleServerlessHealthCheck aimed at
// c's dial target.
func (h *Handler) scheduleServerlessCacheHealthCheck(region, name string, c *ServerlessCache) {
	host, port := c.dialTarget()
	h.scheduleServerlessHealthCheck(region, name, host, port)
}

// serverlessEndpointHostname builds the endpoint name for a serverless cache
// on base: `{name}.{region}.serverless.{base}`.
func serverlessEndpointHostname(name, region, base string) string {
	return fmt.Sprintf("%s.%s.serverless.%s", name, region, base)
}

// scheduleServerlessHealthCheck waits for the cache engine to answer and
// transitions the serverless cache to "available", or to "create-failed"
// carrying why when it never does. region is the region the cache is stored
// under.
func (h *Handler) scheduleServerlessHealthCheck(region, name, host string, port int) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	readiness.Watch{
		Scheduler:  h.scheduler,
		Clock:      h.clk,
		Region:     region,
		ResourceID: name,
		Transition: "serverless-health",
		Subject:    "the cache engine at " + addr,
		Policy:     cacheReadiness,
		Probe: func(ctx context.Context) readiness.Result {
			got, aerr := h.store.getServerlessCache(ctx, name)
			if aerr != nil || got == nil {
				return readiness.Abandoned()
			}
			if !statusIn(got.Status, serverlessAwaiting) {
				return readiness.Abandoned()
			}
			if err := probeEngine(ctx, got.Engine, addr); err != nil {
				return readiness.Retry(err)
			}
			return readiness.Answered()
		},
		OnReady: func(ctx context.Context) {
			h.transitionServerlessCache(ctx, name, "available", serverlessAwaiting...)
		},
		OnFailed: func(ctx context.Context, reason string) {
			h.failServerlessCache(ctx, name, reason)
		},
	}.Start()
}

func toXMLServerlessCache(c *ServerlessCache) xmlServerlessCache {
	out := xmlServerlessCache{
		ServerlessCacheName: c.ServerlessCacheName,
		Description:         c.Description,
		Status:              c.Status,
		Engine:              c.Engine,
		MajorEngineVersion:  c.MajorEngineVersion,
		FullEngineVersion:   c.FullEngineVersion,
		CacheUsageLimits:    toXMLCacheUsageLimits(c.CacheUsageLimits),
		ARN:                 c.ARN,
		SnapshotRetention:   c.SnapshotRetention,
		DailySnapshotTime:   c.DailySnapshotTime,
		NetworkType:         c.NetworkType,
		UserGroupId:         c.UserGroupId,
		KmsKeyId:            c.KmsKeyId,
		CreateTime:          c.CreateTime,
	}
	if len(c.SubnetIds) > 0 {
		out.SubnetIds = xmlStringSet{Items: c.SubnetIds}
	}
	if len(c.SecurityGroupIds) > 0 {
		out.SecurityGroupIds = xmlStringSet{Items: c.SecurityGroupIds}
	}
	if c.Endpoint != nil {
		out.Endpoint = &xmlEndpoint{Address: c.Endpoint.Address, Port: c.Endpoint.Port}
	}
	if c.ReaderEndpoint != nil {
		out.ReaderEndpoint = &xmlEndpoint{Address: c.ReaderEndpoint.Address, Port: c.ReaderEndpoint.Port}
	}
	return out
}

func toXMLCacheUsageLimits(limits CacheUsageLimits) *xmlCacheUsageLimits {
	out := &xmlCacheUsageLimits{}
	if limits.DataStorage.Maximum > 0 || limits.DataStorage.Unit != "" {
		out.DataStorage = &xmlDataStorageLimit{Maximum: limits.DataStorage.Maximum, Unit: limits.DataStorage.Unit}
	}
	if limits.ECPUPerSecond.Maximum > 0 {
		out.ECPUPerSecond = &xmlECPULimit{Maximum: limits.ECPUPerSecond.Maximum}
	}
	if out.DataStorage == nil && out.ECPUPerSecond == nil {
		return nil
	}
	return out
}

func formStringList(r *http.Request, prefix string) []string {
	out := []string{}
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.%d", prefix, i))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

func formTags(r *http.Request) map[string]string {
	tags := map[string]string{}
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Tags.Tag.%d.Key", i))
		if key == "" {
			break
		}
		tags[key] = r.FormValue(fmt.Sprintf("Tags.Tag.%d.Value", i))
	}
	return tags
}

func formCacheUsageLimits(r *http.Request) CacheUsageLimits {
	return CacheUsageLimits{
		DataStorage: DataStorageLimit{
			Maximum: formInt(r, "CacheUsageLimits.DataStorage.Maximum", 0),
			Unit:    r.FormValue("CacheUsageLimits.DataStorage.Unit"),
		},
		ECPUPerSecond: ECPULimit{
			Maximum: formInt(r, "CacheUsageLimits.ECPUPerSecond.Maximum", 0),
		},
	}
}
