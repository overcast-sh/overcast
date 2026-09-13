package eks

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

func k3sImageForVersion(version string) string {
	// e.g. "1.31" → "rancher/k3s:v1.31.3-k3s1"
	return "rancher/k3s:v" + version + ".3-k3s1"
}

// launchLiveBootstrap hands cluster's k3s bring-up to its own goroutine,
// tracked on liveWg so Stop can wait for it — unless shutdown has already
// begun, in which case it starts nothing, registers nothing, and returns
// false; what that means for the cluster record is the caller's call.
//
// This is the one place liveWg.Add(1) happens. The Add and the liveStopping
// check sit under liveLifecycleMu, the same mutex Stop sets liveStopping and
// cancels liveCtx under before it ever calls liveWg.Wait, so every Add still
// to come either already finished (strictly before Stop's critical section)
// or is blocked on the lock and will observe liveStopping and refuse instead
// of racing the Wait — sync.WaitGroup's Add-vs-Wait race, the shape #1282
// found in lifecycle.Scheduler. CreateCluster and recoverLiveCluster each
// carried their own copy of this fence until one of them didn't (#1291);
// sharing it here is what keeps a third copy from drifting the same way.
//
// The bootstrap runs on s.liveCtx, not a request's: it outlives the response
// that started it, and Stop needs to be able to end it. It gets its own copy
// of cluster, so callers keep using theirs. onDone, if non-nil, runs as the
// goroutine exits, before its liveWg slot is released.
func (s *Service) launchLiveBootstrap(region string, cluster *Cluster, onDone func()) bool {
	s.liveLifecycleMu.Lock()
	if s.liveStopping {
		s.liveLifecycleMu.Unlock()
		return false
	}
	s.liveWg.Add(1)
	s.liveLifecycleMu.Unlock()
	// Register ownership before the goroutine runs so delete/stop can clean
	// up even if the container launch fails. The container ID is filled in
	// once Docker create+start succeeds. Stop cannot miss this: it blocks on
	// the liveWg slot taken above, which the goroutine releases only after
	// this line and its whole bootstrap have run.
	s.setLiveClusterRuntime(region, cluster.Name, &liveClusterRuntime{})
	own := *cluster
	go func() {
		defer s.liveWg.Done()
		if onDone != nil {
			defer onDone()
		}
		s.bootstrapLiveCluster(middleware.ContextWithRegion(s.liveCtx, region), region, &own)
	}()
	return true
}

// bootstrapLiveCluster is the entry point launchLiveBootstrap runs in its own
// goroutine: the real bootstrap, or the stand-in a test installed in its place.
// See startLiveClusterHook.
func (s *Service) bootstrapLiveCluster(ctx context.Context, region string, cluster *Cluster) {
	if hook := s.startLiveClusterHook; hook != nil {
		hook(ctx, region, cluster)
		return
	}
	s.startLiveCluster(ctx, region, cluster)
}

// startLiveCluster creates and starts a k3s control-plane container for the
// given cluster. The caller must have already persisted the cluster record with
// CREATING status. On success the live runtime registry is updated with the
// container ID. On failure the cluster is moved to the terminal FAILED state
// carrying the reason, so a caller waiting on it is answered rather than left
// polling CREATING — see failLiveCluster.
func (s *Service) startLiveCluster(ctx context.Context, region string, cluster *Cluster) {
	if s.docker == nil || s.puller == nil {
		s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure,
			errors.New("EKS live mode requires Docker; no daemon is wired"))
		return
	}

	image := k3sImageForVersion(cluster.Version)
	containerName := "overcast-eks-" + cluster.Name

	// Resolve the plane before the image pull: a VPC that cannot take
	// containers should fail the cluster here rather than silently landing its
	// control plane on the default plane, where nothing inside the VPC could
	// reach it.
	placement, err := s.clusterPlacement(ctx, region, cluster)
	if err != nil {
		s.failLiveCluster(ctx, region, cluster.Name, issueConfigurationConflict, err)
		return
	}

	// Pull first, deduplicated per process lifetime, as every other
	// container-starting service does. Without it the create below is the
	// first thing to touch the image, and a machine that does not already
	// hold it gets 404 "No such image" from the daemon (#892).
	if err := s.puller.Ensure(ctx, image); err != nil {
		s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure, err)
		return
	}

	createReq := &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image:        image,
			Cmd:          []string{"server", "--disable=traefik", "--disable=metrics-server"},
			Labels:       s.instances.ManagedLabels(ctx, serviceName, cluster.Name),
			ExposedPorts: map[string]struct{}{"6443/tcp": {}},
		},
		HostConfig: &docker.HostConfig{AutoRemove: true,
			NetworkMode:  dataplane.Primary(s.cfg),
			Privileged:   true,
			Tmpfs:        map[string]string{"/run": "", "/var/run": ""},
			PortBindings: map[string][]docker.PortBinding{"6443/tcp": {{HostIP: "0.0.0.0"}}},
		},
		NetworkingConfig: dataplane.PrimaryEndpoints(s.cfg),
	}

	containerID, err := s.docker.CreateContainer(ctx, containerName, createReq)
	if err != nil {
		if docker.IsConflict(err) {
			existing, inspectErr := s.docker.GetContainerByName(ctx, containerName)
			if inspectErr != nil || existing == nil {
				s.failLiveCluster(ctx, region, cluster.Name, issueConfigurationConflict,
					fmt.Errorf("container %s already exists and could not be inspected: %w", containerName, err))
				return
			}
			if !existing.HasOvercastLabels(serviceName, cluster.Name) {
				s.failLiveCluster(ctx, region, cluster.Name, issueConfigurationConflict,
					fmt.Errorf("container %s already exists and is not managed by Overcast", containerName))
				return
			}
			if owner := existing.Instance(); owner != "" && owner != s.instances.Resolve(ctx) {
				s.failLiveCluster(ctx, region, cluster.Name, issueConfigurationConflict,
					fmt.Errorf("container %s belongs to another Overcast instance %s", containerName, owner))
				return
			}
			containerID = existing.ID
			// A container that was already there was not created by this
			// bootstrap: it is one left from an earlier run, so it is joined
			// the way restart adoption joins one — control plane included, for
			// a container an older version created elsewhere — and before it
			// is started, when it is not already running.
			s.attachLiveClusterContainer(ctx, cluster.Name, containerID, placement, true)
			if !existing.State.Running {
				if err := s.docker.StartContainer(ctx, containerID); err != nil {
					s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure,
						fmt.Errorf("start existing container after name conflict: %w", err))
					return
				}
			}
		} else {
			s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure, err)
			return
		}
	} else {
		// Join the data plane before starting, as every container-backed
		// service does: k3s pulls images from its first moment running, and
		// under OVERCAST_VPC_EGRESS=routed the route out it needs for that is
		// the egress network the placement names. Started first, it races its
		// own first outbound connection.
		s.attachLiveClusterContainer(ctx, cluster.Name, containerID, placement, false)
		if err := s.docker.StartContainer(ctx, containerID); err != nil {
			_ = s.docker.RemoveContainerForce(containerID)
			s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure, err)
			return
		}
	}

	s.setLiveClusterRuntime(region, cluster.Name, &liveClusterRuntime{containerID: containerID})
	s.pollK3sReady(ctx, region, cluster, containerID)
}

// attachLiveClusterContainer joins a control-plane container to its data
// plane(s) — and, for one adopted rather than created by this process, the
// control plane too — advertising the endpoint names, so a task or function
// running kubectl against it resolves the endpoint the API hands out.
//
// Non-fatal: the published host port still serves the host, and a control
// plane that came up is more use reported ACTIVE with this shortfall in the
// log than FAILED over a name that only sibling containers would have used.
func (s *Service) attachLiveClusterContainer(ctx context.Context, name, containerID string, placement dataplane.Placement, adopted bool) {
	var err error
	if adopted {
		err = dataplane.AttachAdopted(ctx, s.docker, s.cfg, containerID, placement)
	} else {
		err = dataplane.Attach(ctx, s.docker, s.cfg, containerID, placement)
	}
	if err != nil {
		s.log.Warn("eks: cluster container could not join the data plane — "+
			"its endpoint resolves only from the host",
			zap.String("cluster", name), zap.Error(err))
	}
}

// adoptLiveClusterContainer records containerID as cluster's running control
// plane and puts it where a fresh bootstrap would have: on its VPC's network
// or the default plane, carrying its endpoint names, and on the control plane
// — which a container created by an older version may never have joined.
// Recording the ID alone left an adopted control plane reachable by nothing
// but the published host port until it was next recreated.
func (s *Service) adoptLiveClusterContainer(ctx context.Context, region string, cluster *Cluster, containerID string) {
	s.setLiveClusterRuntime(region, cluster.Name, &liveClusterRuntime{containerID: containerID})
	s.attachAdoptedLiveCluster(ctx, region, cluster, containerID)
}

// attachAdoptedLiveCluster is the placement half of adoptLiveClusterContainer,
// for the describe-time reconcile that records the runtime first and reads
// the container's port bindings before it can be sure it has one to adopt.
func (s *Service) attachAdoptedLiveCluster(ctx context.Context, region string, cluster *Cluster, containerID string) {
	placement, err := s.clusterPlacement(ctx, region, cluster)
	if err != nil {
		s.log.Warn("eks: adopted cluster container could not be placed in its VPC — "+
			"its endpoint resolves only from the host",
			zap.String("cluster", cluster.Name), zap.Error(err))
		return
	}
	s.attachLiveClusterContainer(ctx, cluster.Name, containerID, placement, true)
}

// clusterEndpointAliases is the set of DNS names a k3s control plane answers to
// on the data plane. AWS serves one at
// `{hash}.{xx}.{region}.eks.amazonaws.com`; Overcast keys it on the cluster
// name, which is what a caller actually has.
//
// region is the record's, not the configured default — a cluster created in
// another region is named for that one.
func (s *Service) clusterEndpointAliases(region, name string) []string {
	if name == "" || region == "" {
		return nil
	}
	return dataplane.Hostnames(s.cfg, func(base string) string {
		return clusterEndpointHostname(name, region, base)
	})
}

// clusterEndpointPublicAccess reads resourcesVpcConfig.endpointPublicAccess,
// AWS's default of true standing in when the caller did not say. Anything
// that is not a bool reads as the default rather than as an error: a
// malformed field should leave the cluster reachable, not refuse to start it.
func clusterEndpointPublicAccess(vpcConfig map[string]any) bool {
	if v, ok := vpcConfig["endpointPublicAccess"].(bool); ok {
		return v
	}
	return true
}

// clusterPlacement is the Placement cluster's control-plane container takes:
// its VPC and subnets resolved from resourcesVpcConfig, its endpoint aliases,
// and whether its API endpoint is public. One function for the fresh
// bootstrap and every adoption path, so an adopted container cannot land
// somewhere a freshly started one would not.
func (s *Service) clusterPlacement(ctx context.Context, region string, cluster *Cluster) (dataplane.Placement, error) {
	return s.placementFor(ctx, s.vpcForCluster(ctx, cluster), clusterSubnetIDs(cluster.ResourcesVPCConfig),
		s.clusterEndpointAliases(region, cluster.Name), clusterEndpointPublicAccess(cluster.ResourcesVPCConfig))
}

// vpcForCluster returns the VPC an EKS control plane belongs in, or "" for the
// default data plane.
//
// A cluster is created with resourcesVpcConfig.subnetIds and its control plane
// lives in the VPC those subnets belong to, exactly as an RDS instance lives in
// its subnet group's. Overcast stored the config verbatim and never resolved
// it, so the k3s container landed on the default plane while a function or task
// created *in* the VPC landed on the VPC's — which only worked while a
// VPC-placed resource also kept the default plane.
//
// A cluster with no resourcesVpcConfig, no subnets, or subnets EC2 has no
// record of still resolves to "". That is today's behaviour and a working
// cluster, so it must not become an error.
func (s *Service) vpcForCluster(ctx context.Context, cluster *Cluster) string {
	if cluster == nil {
		return ""
	}
	// AWS only *returns* vpcId in resourcesVpcConfig, but a caller that sends
	// one is naming the answer the subnets would have resolved to.
	if vpcID, ok := cluster.ResourcesVPCConfig["vpcId"].(string); ok && vpcID != "" {
		return vpcID
	}
	return s.vpcForSubnets(ctx, clusterSubnetIDs(cluster.ResourcesVPCConfig))
}

// vpcForSubnets resolves the first subnet that names a VPC, the same way RDS
// and ElastiCache fill their own VPC field in.
func (s *Service) vpcForSubnets(ctx context.Context, subnetIDs []string) string {
	if s.vpcResolver == nil {
		return ""
	}
	for _, id := range subnetIDs {
		if vpcID := s.vpcResolver.VpcIDForSubnet(ctx, id); vpcID != "" {
			return vpcID
		}
	}
	return ""
}

// clusterSubnetIDs reads subnetIds out of a stored resourcesVpcConfig.
//
// The record keeps the free-form map the API supplied, so the list arrives as
// []any of strings after a JSON round-trip and as []string when a caller built
// the record in process. Anything else is read as no subnets rather than an
// error: a malformed field should leave the cluster on the default plane, not
// refuse to start it.
func clusterSubnetIDs(vpcConfig map[string]any) []string {
	switch v := vpcConfig["subnetIds"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if id, ok := item.(string); ok && id != "" {
				out = append(out, id)
			}
		}
		return out
	default:
		return nil
	}
}

// placementFor turns a resolved VPC into the Placement the control-plane
// container should take, carrying its endpoint aliases onto whichever plane it
// lands on.
//
// public is resourcesVpcConfig.endpointPublicAccess. On AWS it is what makes
// the API server reachable from outside the VPC, and here it keeps a
// VPC-placed control plane on the default plane as well as its VPC's network
// — the same AWS-spelled escape hatch RDS's PubliclyAccessible and ECS's
// assignPublicIp are, so a cluster whose endpoint is private is held to its
// VPC exactly as its consumers are.
func (s *Service) placementFor(ctx context.Context, vpcID string, subnetIDs, aliases []string, public bool) (dataplane.Placement, error) {
	var resolver dataplane.VPCResolver
	if s.vpcResolver != nil {
		resolver = s.vpcResolver
	}
	// The subnets go with the VPC: under OVERCAST_VPC_EGRESS=routed their
	// route tables decide whether the control plane gets a route out — which
	// a k3s that has to pull images needs.
	placement, err := dataplane.PlaceInSubnets(ctx, resolver, vpcID, subnetIDs)
	if err != nil {
		return placement, err
	}
	placement.Aliases = aliases
	placement.Public = public
	return placement, nil
}

// pollK3sReady polls the k3s API server host-mapped port until it responds,
// then marks the cluster ACTIVE with the real endpoint URL. It is called
// synchronously at the end of startLiveCluster, which itself runs as a
// background goroutine, so the blocking poll does not delay API responses.
func (s *Service) pollK3sReady(ctx context.Context, region string, cluster *Cluster, containerID string) {
	const pollInterval = 2 * time.Second
	const pollTimeout = 5 * time.Minute

	inspect, err := s.docker.InspectContainer(ctx, containerID)
	if err != nil {
		s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure,
			fmt.Errorf("inspect the control-plane container: %w", err))
		return
	}

	hostPort := ""
	if bindings, ok := inspect.NetworkSettings.Ports["6443/tcp"]; ok && len(bindings) > 0 {
		hostPort = bindings[0].HostPort
	}
	if hostPort == "" {
		s.failLiveCluster(ctx, region, cluster.Name, issueInternalFailure,
			errors.New("the control-plane container published no host port for 6443/tcp"))
		return
	}

	clusterEndpoint := "https://" + s.cfg.ExternalHostname() + ":" + hostPort
	readyzURL := s.readyzURL(ctx, containerID, hostPort)

	//nolint:gosec // intentional: local dev only, k3s uses self-signed cert
	tlsClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	deadline := s.clk.Now().Add(pollTimeout)
	for s.clk.Now().Before(deadline) {
		resp, err := tlsClient.Get(readyzURL) //nolint:noctx // best-effort background poll
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				caData, caErr := s.extractK3sCAData(ctx, containerID)
				if caErr != nil {
					s.log.Warn("pollK3sReady: failed to read k3s certificate authority data",
						zap.String("cluster", cluster.Name), zap.Error(caErr))
				}
				// API server is reachable — mark cluster ACTIVE.
				s.setClusterActiveWithEndpoint(ctx, region, cluster.Name, clusterEndpoint, caData)
				return
			}
		}
		select {
		case <-ctx.Done():
			// Shutdown. Returning quietly would leave the record CREATING for
			// a bootstrap nothing will resume — see failLiveCluster.
			s.failLiveCluster(ctx, region, cluster.Name, issueClusterUnreachable, ctx.Err())
			return
		case <-s.clk.After(pollInterval):
		}
	}
	s.failLiveCluster(ctx, region, cluster.Name, issueClusterUnreachable,
		fmt.Errorf("the k3s API server did not answer %s within %s", readyzURL, pollTimeout))
}

// k3sAPIPort is the port the control plane listens on inside its container.
const k3sAPIPort = "6443"

// readyzURL is the readiness URL *Overcast itself* dials: the container's own
// address on 6443 when Overcast runs beside it, else loopback and the
// published port.
//
// Not the endpoint a client is handed — DescribeCluster still answers with the
// published host port. Overcast in a container cannot reach that port: inside
// its own network namespace 127.0.0.1 is itself, so the probe was refused and
// a perfectly healthy control plane never left CREATING. This is the lookup
// RDS, ElastiCache, MSK and EFS already health-check through.
func (s *Service) readyzURL(ctx context.Context, containerID, hostPort string) string {
	if addr := dataplane.ContainerAddr(ctx, s.docker, s.cfg, containerID); addr != "" {
		return "https://" + addr + ":" + k3sAPIPort + "/readyz"
	}
	return "https://127.0.0.1:" + hostPort + "/readyz"
}

// AWS ClusterIssueCode values DescribeCluster's health.issues carry. Overcast
// uses the three that a local control plane can actually run into.
const (
	issueInternalFailure       = "InternalFailure"
	issueConfigurationConflict = "ConfigurationConflict"
	issueClusterUnreachable    = "ClusterUnreachable"
)

// failLiveCluster moves a cluster that could not be brought up to FAILED and
// records why in the health.issues DescribeCluster returns.
//
// The reason used to exist only as a warn log line, which no API caller reads:
// the cluster kept reporting CREATING, so `aws eks wait cluster-active` — and
// any other waiter — spun until it gave up with nothing to say. FAILED is
// terminal for that waiter, and the issue message carries the daemon's own
// words.
//
// This was the first service to take that decision; it is now the decision
// every container-starting service takes, through serviceutil/readiness, which
// owns the retry policy and the exhaustion outcome while each service supplies
// its own probe. ElastiCache (#881), MSK, RDS and EFS all settle terminally
// there and carry the reason with them.
//
// EKS itself is deliberately not on that helper. Its poll is a synchronous loop
// inside a goroutine that startLiveCluster already detached, with no
// lifecycle.Scheduler anywhere in it; moving it across would rewrite the
// concurrency model of a bootstrap that also extracts CA data and mints an
// endpoint, to arrive at the behaviour it already has. The policy is shared;
// this is the one place the shape of the caller was the reason not to.
//
// Re-reading before writing avoids clobbering a concurrent mutation, the same
// way setClusterActiveWithEndpoint does — a DeleteCluster that already removed
// the record leaves nothing to fail.
func (s *Service) failLiveCluster(ctx context.Context, region, name, code string, reason error) {
	// A bootstrap that shutdown cancelled did not fail on its own merits, but
	// the record still needs a terminal answer: nothing resumes a bootstrap
	// across a restart, and Stop removes the container it had got as far as
	// starting, so CREATING would again be a status no one is coming to
	// change. The daemon's words here would only be "context canceled".
	if errors.Is(reason, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		code = issueInternalFailure
		reason = errors.New("Overcast shut down while the cluster was still starting")
	}

	s.log.Warn("eks: cluster could not be started — marking it FAILED",
		zap.String("cluster", name), zap.String("code", code), zap.Error(reason))

	// Detached: on the shutdown path ctx is cancelled, and it is the reason
	// this is being written. Reading and writing through it would drop the
	// record update on any store that honours cancellation.
	ctx = context.WithoutCancel(ctx)

	existing, found, err := s.getCluster(ctx, region, name)
	if err != nil || !found {
		return
	}
	existing.Status = "FAILED"
	existing.Health = map[string]any{
		"issues": []map[string]any{{
			"code":        code,
			"message":     reason.Error(),
			"resourceIds": []string{name},
		}},
	}
	if err := s.putCluster(ctx, region, existing); err != nil {
		s.log.Warn("failLiveCluster: failed to persist",
			zap.String("cluster", name), zap.Error(err))
	}
}

// setClusterActiveWithEndpoint reads the current cluster record and updates it
// to ACTIVE with the given endpoint. Re-reading before writing avoids clobbering
// concurrent mutations (e.g. a concurrent DeleteCluster).
func (s *Service) setClusterActiveWithEndpoint(ctx context.Context, region, name, endpoint, caData string) {
	existing, found, err := s.getCluster(ctx, region, name)
	if err != nil || !found {
		s.log.Warn("setClusterActiveWithEndpoint: cluster not found",
			zap.String("cluster", name))
		return
	}
	existing.Status = "ACTIVE"
	existing.Endpoint = endpoint
	// A cluster that came up has no outstanding issue, even if an earlier
	// attempt recorded one.
	existing.Health = nil
	if strings.TrimSpace(caData) != "" {
		existing.CertificateAuthority = map[string]any{"data": caData}
	}
	if err := s.putCluster(ctx, region, existing); err != nil {
		s.log.Warn("setClusterActiveWithEndpoint: failed to persist",
			zap.String("cluster", name), zap.Error(err))
	}
}

func (s *Service) extractK3sCAData(ctx context.Context, containerID string) (string, error) {
	if s.docker == nil {
		return "", nil
	}

	const k3sConfigPath = "/etc/rancher/k3s/k3s.yaml"
	raw, err := s.docker.CopyFileFromContainer(ctx, containerID, k3sConfigPath)
	if err != nil {
		return "", err
	}

	const caPrefix = "certificate-authority-data:"
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, caPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, caPrefix)), nil
		}
	}

	return "", nil
}

func liveClusterRuntimeKey(region, name string) string {
	return region + "/" + name
}

func (s *Service) setLiveClusterRuntime(region, name string, runtime *liveClusterRuntime) {
	if runtime == nil {
		return
	}
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	s.liveRuntimes[liveClusterRuntimeKey(region, name)] = runtime
}

func (s *Service) getLiveClusterRuntime(region, name string) (*liveClusterRuntime, bool) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	runtime, found := s.liveRuntimes[liveClusterRuntimeKey(region, name)]
	if !found || runtime == nil {
		return nil, false
	}
	copy := *runtime
	return &copy, true
}

func (s *Service) deleteLiveClusterRuntime(region, name string) (*liveClusterRuntime, bool) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	key := liveClusterRuntimeKey(region, name)
	runtime, found := s.liveRuntimes[key]
	if found {
		delete(s.liveRuntimes, key)
	}
	if !found || runtime == nil {
		return nil, false
	}
	copy := *runtime
	return &copy, true
}

func (s *Service) reconcileLiveClusterRuntime(ctx context.Context, region, name string) (*liveClusterRuntime, bool, error) {
	if s.docker == nil {
		return nil, false, nil
	}

	containerName := "overcast-eks-" + name
	inspect, err := s.docker.GetContainerByName(ctx, containerName)
	if err != nil {
		return nil, false, err
	}
	if inspect == nil || !inspect.HasOvercastLabels(serviceName, name) {
		return nil, false, nil
	}

	runtime := &liveClusterRuntime{containerID: inspect.ID}
	s.setLiveClusterRuntime(region, name, runtime)
	return runtime, true, nil
}

func (s *Service) reconcilePersistedLiveClusterRuntimes(ctx context.Context) {
	if s.docker == nil {
		return
	}

	pairs, err := s.store.Scan(ctx, nsClusters, "")
	if err != nil {
		s.log.Warn("failed to scan persisted EKS clusters for runtime reconciliation", zap.Error(err))
		return
	}

	for _, kv := range pairs {
		var cluster Cluster
		if err := json.Unmarshal([]byte(kv.Value), &cluster); err != nil {
			continue
		}
		if s.isMockModeClusterRecord(&cluster) {
			continue
		}

		region, name, ok := eksClusterFromResourceARN(cluster.Arn)
		if !ok {
			continue
		}
		// Refresh runtime identity by managed name even when a cached ID exists;
		// stale non-empty IDs can otherwise leak recreated managed containers.
		if _, _, err := s.reconcileLiveClusterRuntime(ctx, region, name); err != nil {
			s.log.Warn("failed to reconcile persisted EKS live runtime during stop",
				zap.String("cluster", name), zap.Error(err))
		}
	}
}

func (s *Service) reconcileLiveClusterContainers(ctx context.Context, containers []docker.ContainerSummary) {
	if s.docker == nil || !s.liveModeEnabled() {
		return
	}
	byResource := docker.ContainersByResource(containers)
	pairs, err := s.store.Scan(ctx, nsClusters, "")
	if err != nil {
		s.log.Warn("failed to scan persisted EKS clusters for container reconciliation", zap.Error(err))
		return
	}
	for _, kv := range pairs {
		var cluster Cluster
		if err := json.Unmarshal([]byte(kv.Value), &cluster); err != nil || s.isMockModeClusterRecord(&cluster) {
			continue
		}
		region, name, ok := eksClusterFromResourceARN(cluster.Arn)
		if !ok || (cluster.Status != "ACTIVE" && cluster.Status != "CREATING") {
			continue
		}
		var recordedID string
		if runtime, ok := s.getLiveClusterRuntime(region, name); ok {
			recordedID = runtime.containerID
		}
		rctx := middleware.ContextWithRegion(ctx, region)
		running := s.instances.OwnContainer(rctx, byResource[name], recordedID)
		if running != nil && !strings.EqualFold(running.State, "running") {
			running = nil
		}
		if running != nil {
			s.adoptLiveClusterContainer(rctx, region, &cluster, running.ID)
			continue
		}
		s.recoverLiveCluster(region, &cluster)
	}
}

func (s *Service) handleLiveRuntimeDied(_ context.Context, e events.Event) {
	p, ok := e.Payload.(events.DockerContainerPayload)
	if !ok || p.Service != serviceName || p.ResourceID == "" || !s.liveModeEnabled() {
		return
	}
	cluster, region, found, err := serviceutil.FindRegioned[Cluster](
		context.Background(), s.store, nsClusters, p.ResourceID, s.cfg.Region)
	if err != nil || !found || cluster == nil || (cluster.Status != "ACTIVE" && cluster.Status != "CREATING") {
		return
	}
	if runtime, ok := s.getLiveClusterRuntime(region, p.ResourceID); ok && runtime.containerID != "" && runtime.containerID != p.ContainerID {
		return
	}
	var recordedID string
	if runtime, ok := s.getLiveClusterRuntime(region, p.ResourceID); ok {
		recordedID = runtime.containerID
	}
	if !s.instances.ContainerIsOurs(middleware.ContextWithRegion(context.Background(), region), p.ContainerID, p.Instance, recordedID) {
		return
	}
	s.recoverLiveCluster(region, cluster)
}

func (s *Service) recoverLiveCluster(region string, cluster *Cluster) {
	if cluster == nil {
		return
	}
	key := liveClusterRuntimeKey(region, cluster.Name)
	if _, loaded := s.liveRecoveries.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	// A refused launch (shutdown has begun) gives the key back so the
	// coalescing map does not outlive the recovery it was reserved for;
	// nothing else can launch past this point either, so no bootstrap is
	// lost to the gap between reserving and refusing.
	if !s.launchLiveBootstrap(region, cluster, func() { s.liveRecoveries.Delete(key) }) {
		s.liveRecoveries.Delete(key)
	}
}

func (s *Service) reconcileReadyLiveCluster(ctx context.Context, region string, cluster *Cluster) *Cluster {
	if cluster == nil || s.docker == nil || s.isMockModeClusterRecord(cluster) {
		return cluster
	}

	caData, _ := cluster.CertificateAuthority["data"].(string)
	if cluster.Status == "ACTIVE" && strings.TrimSpace(cluster.Endpoint) != "" && strings.TrimSpace(caData) != "" {
		return cluster
	}

	var inspect *docker.ContainerInspect
	// adopted marks a container this process did not start and has not yet
	// put on its planes — one found by name after a restart, or behind a
	// stale ID. It is attached once its port bindings say it is the one.
	adopted := false
	runtime, found := s.getLiveClusterRuntime(region, cluster.Name)
	if !found || strings.TrimSpace(runtime.containerID) == "" {
		containerName := "overcast-eks-" + cluster.Name
		var err error
		inspect, err = s.docker.GetContainerByName(ctx, containerName)
		if err != nil {
			s.log.Warn("failed to reconcile EKS live runtime during cluster describe",
				zap.String("cluster", cluster.Name), zap.Error(err))
			return cluster
		}
		if inspect != nil && inspect.HasOvercastLabels(serviceName, cluster.Name) {
			runtime = &liveClusterRuntime{containerID: inspect.ID}
			s.setLiveClusterRuntime(region, cluster.Name, runtime)
			found = true
			adopted = true
		}
	}
	if !found || strings.TrimSpace(runtime.containerID) == "" {
		return cluster
	}

	if inspect == nil {
		var err error
		inspect, err = s.docker.InspectContainer(ctx, runtime.containerID)
		if err != nil {
			if !docker.IsNotFound(err) {
				s.log.Warn("failed to inspect reconciled EKS live runtime during cluster describe",
					zap.String("cluster", cluster.Name), zap.Error(err))
				return cluster
			}
			// Stale container ID — fall back to name-based lookup.
			containerName := "overcast-eks-" + cluster.Name
			nameInspect, nameErr := s.docker.GetContainerByName(ctx, containerName)
			if nameErr != nil {
				s.log.Warn("failed to look up EKS live runtime by name after stale ID during cluster describe",
					zap.String("cluster", cluster.Name), zap.Error(nameErr))
				return cluster
			}
			if nameInspect == nil || !nameInspect.HasOvercastLabels(serviceName, cluster.Name) {
				return cluster
			}
			runtime = &liveClusterRuntime{containerID: nameInspect.ID}
			s.setLiveClusterRuntime(region, cluster.Name, runtime)
			adopted = true
			inspect, err = s.docker.InspectContainer(ctx, runtime.containerID)
			if err != nil {
				s.log.Warn("failed to inspect refreshed EKS live runtime after stale ID recovery",
					zap.String("cluster", cluster.Name), zap.Error(err))
				return cluster
			}
		}
	}

	hostPort := ""
	if bindings, ok := inspect.NetworkSettings.Ports["6443/tcp"]; ok && len(bindings) > 0 {
		hostPort = bindings[0].HostPort
	}
	if hostPort == "" {
		return cluster
	}
	if adopted {
		s.attachAdoptedLiveCluster(ctx, region, cluster, runtime.containerID)
	}

	readyzURL := s.readyzURL(ctx, runtime.containerID, hostPort)
	//nolint:gosec // intentional: local dev only, k3s uses self-signed cert
	tlsClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := tlsClient.Get(readyzURL) //nolint:noctx // best-effort readiness reconciliation
	if err != nil {
		return cluster
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		return cluster
	}

	clusterEndpoint := "https://" + s.cfg.ExternalHostname() + ":" + hostPort
	reconciledCAData, caErr := s.extractK3sCAData(ctx, runtime.containerID)
	if caErr != nil {
		s.log.Warn("failed to read k3s certificate authority data during cluster describe reconciliation",
			zap.String("cluster", cluster.Name), zap.Error(caErr))
	}
	s.setClusterActiveWithEndpoint(ctx, region, cluster.Name, clusterEndpoint, reconciledCAData)

	updated, found, err := s.getCluster(ctx, region, cluster.Name)
	if err != nil || !found {
		return cluster
	}
	return updated
}

func (s *Service) drainLiveClusterRuntimes() []*liveClusterRuntime {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	out := make([]*liveClusterRuntime, 0, len(s.liveRuntimes))
	for key, runtime := range s.liveRuntimes {
		delete(s.liveRuntimes, key)
		if runtime == nil {
			continue
		}
		copy := *runtime
		out = append(out, &copy)
	}
	return out
}

func (s *Service) cleanupLiveClusterRuntime(ctx context.Context, runtime *liveClusterRuntime) error {
	if runtime == nil || strings.TrimSpace(runtime.containerID) == "" || s.docker == nil {
		return nil
	}
	if err := s.docker.StopContainer(ctx, runtime.containerID, 10); err != nil {
		return err
	}
	if err := s.docker.RemoveContainerForce(runtime.containerID); err != nil {
		return err
	}
	return nil
}
