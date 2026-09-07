package ecs

// handler.go — HTTP handlers for the ECS emulator.
// Contains the Handler struct, operation registry, shared helpers, and all
// implemented operation handlers. Stubs live in handler_stubs.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/lifecycle"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Handler holds ECS handler dependencies.
type Handler struct {
	cfg         *config.Config
	store       *ecsStore
	log         *serviceutil.ServiceLogger
	clk         clock.Clock
	bus         *events.Bus
	scheduler   *lifecycle.Scheduler
	ops         map[string]http.HandlerFunc
	typedOp     map[string]op.Operation
	docker      *docker.Client
	dockerReady atomic.Bool
	puller      *docker.ImagePuller
	gc          *docker.GC
	images      docker.ImageResolver
	vpcResolver VPCNetworkResolver
	efsResolver EFSVolumeResolver
	targets     TargetRegistrar
	secrets     SecretsManagerResolver
	parameters  ParameterResolver
	// instances scopes container sweeps to the containers this instance
	// created, so a second Overcast on the same daemon does not treat this
	// one's running tasks as orphans. See docker.LabelInstance.
	instances *serviceutil.InstanceDomain
	// logWriter ships task container output to CloudWatch Logs for containers
	// using the awslogs driver. Nil until InitLogWriter is called.
	logWriter events.LogWriter
	// logPumps holds the running awslogs follower of every container that has
	// one, keyed by Docker ID, so it can be stopped when the container is — a
	// follower reconnects for as long as it lives, and nothing else would ever
	// tell it the container is gone. See stopLogStreaming. logPumpsMu guards
	// the map, not the followers.
	logPumpsMu    sync.Mutex
	logPumps      map[string]*logPump
	seedMu        sync.Mutex      // guards seededRegions
	seededRegions map[string]bool // regions where ensureBuiltinProviders has run

	// serviceLocks serialises the read-modify-write of one service's record —
	// see lockService. serviceLocksMu guards the map itself, not the records.
	serviceLocksMu sync.Mutex
	serviceLocks   map[string]*sync.Mutex

	// taskLocks serialises the read-modify-write of one task's record — see
	// lockTask. Striped rather than keyed, so it needs no initialisation and
	// does not grow with the tasks a service churns through.
	taskLocks [taskLockStripes]sync.Mutex

	// endpoint maps AWS resource URLs and hostnames onto an address task
	// containers can dial. Resolved on first task start — see containerEndpoint.
	endpoint     *containerendpoint.Mapper
	endpointOnce sync.Once

	// debugger owns the debug targets a task's containers are registered
	// with at start (see debugger.go); nil when the debugger is not wired.
	// debugPorts remembers which task holds each definition's port.
	debugger   *debugger.Manager
	debugPorts debugPortOwners
}

// EFSVolumeResolver maps EFS references to the Docker volume backing them.
// Implemented by the EFS service; ok is false in EFS mock mode, while Docker
// is unavailable, or for an unknown resource — the task then runs without
// the mount rather than failing.
type EFSVolumeResolver interface {
	// EFSVolumeForFileSystem resolves a bare file system ID (plain
	// efsVolumeConfiguration; any rootDirectory is applied by the caller).
	EFSVolumeForFileSystem(ctx context.Context, fileSystemID string) (volume string, ok bool)
	// EFSVolumeForAccessPointID resolves an authorizationConfig access point,
	// returning the access point's root directory as the volume subpath.
	EFSVolumeForAccessPointID(ctx context.Context, accessPointID string) (volume, subpath string, ok bool)
}

// TargetRegistrar registers a service's tasks with an ELB target group, which
// is what puts them behind a load balancer. Implemented by the elbv2 service;
// nil when elbv2 is disabled, in which case a service's loadBalancers are
// stored and echoed but nothing is registered.
type TargetRegistrar interface {
	RegisterTarget(ctx context.Context, targetGroupArn, address string, port int) error
	DeregisterTarget(ctx context.Context, targetGroupArn, address string) error
}

// VPCNetworkResolver resolves subnet-backed ECS awsvpc placement against EC2.
type VPCNetworkResolver interface {
	VpcIDForSubnet(ctx context.Context, subnetID string) string
	VPCNetworkStatus(ctx context.Context, vpcID string) string
	DockerNetworkForVpc(ctx context.Context, vpcID string) string
	AllocatePrivateIPForSubnet(ctx context.Context, subnetID string) string
}

// ensureBuiltinProviders lazily seeds FARGATE and FARGATE_SPOT capacity
// providers so they are discoverable without explicit creation, matching
// real AWS behaviour. Called on first capacity-provider-touching request.
// The store keys providers per region, so seeding happens once per region
// the caller's request context resolves to — a single global seed would
// leave every other region without the builtin providers.
func (h *Handler) ensureBuiltinProviders(ctx context.Context) {
	region := h.store.region(ctx)
	h.seedMu.Lock()
	defer h.seedMu.Unlock()
	if h.seededRegions == nil {
		h.seededRegions = make(map[string]bool)
	}
	if h.seededRegions[region] {
		return
	}
	h.seededRegions[region] = true
	for _, name := range []string{"FARGATE", "FARGATE_SPOT"} {
		existing, err := h.store.getCapacityProvider(ctx, name)
		if err != nil || existing != nil {
			continue
		}
		cp := &CapacityProvider{
			CapacityProviderArn: h.capacityProviderARN(ctx, name),
			Name:                name,
			Status:              "ACTIVE",
		}
		_ = h.store.putCapacityProvider(ctx, cp)
	}
}

func newHandler(cfg *config.Config, store *ecsStore, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{
		cfg: cfg, store: store, log: log, clk: clk,
		scheduler: lifecycle.NewScheduler(clk),
		instances: serviceutil.NewAnchoredInstanceDomain(store.store, nsInstance, serviceutil.DataDirAnchor(cfg.DataDir)),
	}
	h.initOps()
	return h
}

// decodeJSON decodes the request body into v. Returns false and writes an error if it fails.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "SerializationException",
			Message:    "Failed to deserialize the message: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		})
		return false
	}
	return true
}

// publish emits an event if the bus is wired.
func (h *Handler) publish(r *http.Request, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{Type: t, Payload: payload})
	}
}

// region returns the request's region (from SigV4 / X-Overcast-Region header),
// falling back to the configured default. Used for ARN minting so returned
// ARNs carry the deployment region the SDK actually called.
func (h *Handler) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, h.cfg.Region)
}

// clusterARN builds an ECS cluster ARN.
func (h *Handler) clusterARN(ctx context.Context, name string) string {
	return fmt.Sprintf("arn:aws:ecs:%s:%s:cluster/%s", h.region(ctx), h.cfg.AccountID, name)
}

// taskDefinitionARN builds an ECS task definition ARN.
func (h *Handler) taskDefinitionARN(ctx context.Context, family string, revision int) string {
	return fmt.Sprintf("arn:aws:ecs:%s:%s:task-definition/%s:%d", h.region(ctx), h.cfg.AccountID, family, revision)
}

// extractClusterName extracts the cluster name from an ARN or returns the input as-is.
func extractClusterName(input string) string {
	if strings.HasPrefix(input, "arn:") {
		parts := strings.Split(input, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-1]
		}
	}
	return input
}

// taskARN builds an ECS task ARN.
func (h *Handler) taskARN(ctx context.Context, cluster, taskID string) string {
	return fmt.Sprintf("arn:aws:ecs:%s:%s:task/%s/%s", h.region(ctx), h.cfg.AccountID, cluster, taskID)
}

// containerARN builds an ECS container ARN.
func (h *Handler) containerARN(ctx context.Context, id string) string {
	return fmt.Sprintf("arn:aws:ecs:%s:%s:container/%s", h.region(ctx), h.cfg.AccountID, id)
}

// extractTaskID extracts the task UUID from an ARN or returns the input as-is.
func extractTaskID(input string) string {
	if strings.HasPrefix(input, "arn:") {
		parts := strings.Split(input, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-1]
		}
	}
	return input
}

/*
parseTaskDefRef parses a task definition reference which can be:
- family (latest)
- family:revision
- full ARN (arn:aws:ecs:...:task-definition/family:revision).
*/
func parseTaskDefRef(ref string) (family string, revision int, hasRevision bool) {
	// Strip ARN prefix if present
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.Split(ref, "/")
		if len(parts) >= 2 {
			ref = parts[len(parts)-1]
		}
	}
	// Split family:revision
	if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		fam := ref[:idx]
		if rev, err := strconv.Atoi(ref[idx+1:]); err == nil {
			return fam, rev, true
		}
	}
	return ref, 0, false
}

// validFargateCPUMemory defines valid CPU (millicores string) → allowed memory ranges (MiB).
// https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-cpu-memory-error.html
var validFargateCPUMemory = map[string][2]int{
	"256":   {512, 2048},
	"512":   {1024, 4096},
	"1024":  {2048, 8192},
	"2048":  {4096, 16384},
	"4096":  {8192, 30720},
	"8192":  {16384, 61440},
	"16384": {32768, 122880},
}

// isFargate returns true if the requiresCompatibilities list includes "FARGATE".
func isFargate(compat []string) bool {
	for _, c := range compat {
		if strings.EqualFold(c, "FARGATE") {
			return true
		}
	}
	return false
}

// validateFargateCPUMemory checks that the cpu/memory combination is valid for Fargate.
func validateFargateCPUMemory(cpu, memory string) error {
	memMiB, err := strconv.Atoi(memory)
	if err != nil {
		return fmt.Errorf("invalid memory value %q: must be a number in MiB", memory)
	}
	allowed, ok := validFargateCPUMemory[cpu]
	if !ok {
		return fmt.Errorf("invalid cpu value %q for FARGATE; valid values: 256, 512, 1024, 2048, 4096, 8192, 16384", cpu)
	}
	if memMiB < allowed[0] || memMiB > allowed[1] {
		return fmt.Errorf("invalid memory value %d MiB for cpu=%s; valid range: %d–%d MiB", memMiB, cpu, allowed[0], allowed[1])
	}
	return nil
}

// ---- Cluster handlers --------------------------------------------------------

// CreateCluster handles AmazonEC2ContainerServiceV20141113.CreateCluster.
func (h *Handler) CreateCluster(w http.ResponseWriter, r *http.Request) {
	// Delegates to createClusterTyped (typed_logic.go) so the legacy
	// JSON1.1 path and the CBOR typed path share one implementation — the
	// legacy copy previously re-implemented this inline and silently
	// ignored tags (#1196).
	var req createClusterRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.createClusterTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"cluster": resp.Cluster}, "application/x-amz-json-1.1")
}

// DescribeClusters handles AmazonEC2ContainerServiceV20141113.DescribeClusters.
func (h *Handler) DescribeClusters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Clusters []string `json:"clusters"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	type failure struct {
		Arn    string `json:"arn"`
		Reason string `json:"reason"`
	}
	found := make([]Cluster, 0, len(req.Clusters))
	failures := make([]failure, 0)
	for _, ref := range req.Clusters {
		name := extractClusterName(ref)
		c, aerr := h.store.getCluster(r.Context(), name)
		if aerr != nil {
			failures = append(failures, failure{
				Arn:    h.clusterARN(r.Context(), name),
				Reason: "MISSING",
			})
			continue
		}
		if aerr := h.refreshClusterCounts(r.Context(), c); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		found = append(found, *c)
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"clusters": found,
		"failures": failures,
	}, "application/x-amz-json-1.1")
}

// refreshClusterCounts derives the summary fields AWS exposes from the current
// resource records instead of relying on counters captured when the cluster was
// created. Fargate tasks count as cluster tasks but never as container instances.
func (h *Handler) refreshClusterCounts(ctx context.Context, cluster *Cluster) *protocol.AWSError {
	tasks, aerr := h.store.listTasks(ctx, cluster.ClusterName)
	if aerr != nil {
		return aerr
	}
	services, aerr := h.store.listServices(ctx, cluster.ClusterName)
	if aerr != nil {
		return aerr
	}
	instances, aerr := h.store.listContainerInstances(ctx, cluster.ClusterName)
	if aerr != nil {
		return aerr
	}

	cluster.RunningTasksCount = 0
	cluster.PendingTasksCount = 0
	for _, task := range tasks {
		switch task.LastStatus {
		case "RUNNING":
			cluster.RunningTasksCount++
		case "PENDING":
			cluster.PendingTasksCount++
		}
	}
	cluster.ActiveServicesCount = 0
	for _, service := range services {
		if service.Status == "ACTIVE" {
			cluster.ActiveServicesCount++
		}
	}
	cluster.RegisteredContainerInstancesCount = 0
	for _, instance := range instances {
		if instance.Status == "ACTIVE" || instance.Status == "DRAINING" {
			cluster.RegisteredContainerInstancesCount++
		}
	}
	return nil
}

// ListClusters handles AmazonEC2ContainerServiceV20141113.ListClusters.
func (h *Handler) ListClusters(w http.ResponseWriter, r *http.Request) {
	// Consume request body (may be empty or {})
	var req json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&req)

	clusters, aerr := h.store.listClusters(r.Context())
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	arns := make([]string, 0, len(clusters))
	for _, c := range clusters {
		arns = append(arns, c.ClusterArn)
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"clusterArns": arns}, "application/x-amz-json-1.1")
}

// DeleteCluster handles AmazonEC2ContainerServiceV20141113.DeleteCluster.
func (h *Handler) DeleteCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name := extractClusterName(req.Cluster)
	c, aerr := h.store.getCluster(r.Context(), name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	c.Status = "INACTIVE"
	if aerr := h.store.deleteCluster(r.Context(), name); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	h.publish(r, events.ECSClusterDeleted, events.ResourcePayload{Name: name})
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"cluster": c}, "application/x-amz-json-1.1")
}

// ---- Task definition handlers -------------------------------------------------

// RegisterTaskDefinition handles AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition.
func (h *Handler) RegisterTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Family                  string                `json:"family"`
		ContainerDefinitions    []ContainerDefinition `json:"containerDefinitions"`
		NetworkMode             string                `json:"networkMode"`
		RequiresCompatibilities []string              `json:"requiresCompatibilities"`
		Cpu                     string                `json:"cpu"`
		Memory                  string                `json:"memory"`
		Volumes                 []TaskVolume          `json:"volumes"`
		TaskRoleArn             string                `json:"taskRoleArn"`
		ExecutionRoleArn        string                `json:"executionRoleArn"`
		RuntimePlatform         *RuntimePlatform      `json:"runtimePlatform"`
		EphemeralStorage        *EphemeralStorage     `json:"ephemeralStorage"`
		PidMode                 string                `json:"pidMode"`
		IpcMode                 string                `json:"ipcMode"`
		Tags                    []Tag                 `json:"tags"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Family == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ClientException",
			Message:    "Family is required.",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if aerr := validateTaskVolumes(req.Volumes, req.ContainerDefinitions, isFargate(req.RequiresCompatibilities)); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// Validate Fargate constraints.
	if isFargate(req.RequiresCompatibilities) {
		if req.NetworkMode != "" && req.NetworkMode != "awsvpc" {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "ClientException",
				Message:    "FARGATE requires networkMode to be 'awsvpc'.",
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		if req.Cpu == "" || req.Memory == "" {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "ClientException",
				Message:    "FARGATE requires both cpu and memory to be specified at the task level.",
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		if err := validateFargateCPUMemory(req.Cpu, req.Memory); err != nil {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "ClientException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		// Default networkMode to awsvpc when not specified for Fargate.
		if req.NetworkMode == "" {
			req.NetworkMode = "awsvpc"
		}
	}

	rev, aerr := h.store.nextRevision(r.Context(), req.Family)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	td := &TaskDefinition{
		TaskDefinitionArn:       h.taskDefinitionARN(r.Context(), req.Family, rev),
		Family:                  req.Family,
		Revision:                rev,
		Status:                  "ACTIVE",
		NetworkMode:             req.NetworkMode,
		RequiresCompatibilities: req.RequiresCompatibilities,
		Cpu:                     req.Cpu,
		Memory:                  req.Memory,
		ContainerDefinitions:    req.ContainerDefinitions,
		Volumes:                 req.Volumes,
		TaskRoleArn:             req.TaskRoleArn,
		ExecutionRoleArn:        req.ExecutionRoleArn,
		RuntimePlatform:         req.RuntimePlatform,
		EphemeralStorage:        req.EphemeralStorage,
		PidMode:                 req.PidMode,
		IpcMode:                 req.IpcMode,
	}
	if aerr := h.store.putTaskDefinition(r.Context(), td); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if aerr := h.storeTaskDefinitionTags(r.Context(), td.TaskDefinitionArn, req.Tags); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	h.prewarmTaskImages(td)
	h.publish(r, events.ECSTaskDefinitionRegistered, events.ResourcePayload{Name: req.Family})
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"taskDefinition": td}, "application/x-amz-json-1.1")
}

// prewarmTaskImages pulls everything a task definition will need to start, in
// the background, so the first RunTask does not pay the cold-pull cost on the
// request path. The sync.Once inside the puller coalesces each one with any
// concurrent RunTask pull.
//
// Both RegisterTaskDefinition paths — the JSON one and the typed one — come
// through here, so a definition registered over CBOR is prewarmed like one
// registered over JSON.
func (h *Handler) prewarmTaskImages(td *TaskDefinition) {
	if h.puller == nil {
		return
	}
	for _, c := range td.ContainerDefinitions {
		h.puller.Prewarm(c.Image)
	}
	// An awsvpc task needs one more image than it declares: the one its network
	// namespace container runs from. Left to the launch path it would be a cold
	// pull on the very first task, before any application container is created.
	if taskSharesNetworkNamespace(td, "") {
		h.puller.Prewarm(docker.UtilityImage)
	}
}

// DescribeTaskDefinition handles AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition.
func (h *Handler) DescribeTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskDefinition string `json:"taskDefinition"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TaskDefinition == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ClientException",
			Message:    "taskDefinition is required.",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	family, revision, hasRevision := parseTaskDefRef(req.TaskDefinition)
	var td *TaskDefinition
	var aerr *protocol.AWSError
	if hasRevision {
		td, aerr = h.store.getTaskDefinition(r.Context(), family, revision)
	} else {
		td, aerr = h.store.getLatestTaskDefinition(r.Context(), family)
	}
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"taskDefinition": td}, "application/x-amz-json-1.1")
}

// ListTaskDefinitions handles AmazonEC2ContainerServiceV20141113.ListTaskDefinitions.
func (h *Handler) ListTaskDefinitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FamilyPrefix string `json:"familyPrefix"`
	}
	// Body may be empty
	_ = json.NewDecoder(r.Body).Decode(&req)

	var defs []TaskDefinition
	var aerr *protocol.AWSError
	if req.FamilyPrefix != "" {
		defs, aerr = h.store.listTaskDefinitionsByFamily(r.Context(), req.FamilyPrefix)
	} else {
		defs, aerr = h.store.listTaskDefinitions(r.Context())
	}
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	arns := make([]string, 0, len(defs))
	for _, td := range defs {
		arns = append(arns, td.TaskDefinitionArn)
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"taskDefinitionArns": arns}, "application/x-amz-json-1.1")
}

// DeregisterTaskDefinition handles AmazonEC2ContainerServiceV20141113.DeregisterTaskDefinition.
func (h *Handler) DeregisterTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskDefinition string `json:"taskDefinition"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TaskDefinition == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ClientException",
			Message:    "taskDefinition is required.",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	family, revision, hasRevision := parseTaskDefRef(req.TaskDefinition)
	if !hasRevision {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ClientException",
			Message:    "taskDefinition must include a revision (family:revision).",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	td, aerr := h.store.getTaskDefinition(r.Context(), family, revision)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	td.Status = "INACTIVE"
	if aerr := h.store.putTaskDefinition(r.Context(), td); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	h.publish(r, events.ECSTaskDefinitionDeregistered, events.ResourcePayload{Name: family})
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"taskDefinition": td}, "application/x-amz-json-1.1")
}

// ListTaskDefinitionFamilies handles AmazonEC2ContainerServiceV20141113.ListTaskDefinitionFamilies.
func (h *Handler) ListTaskDefinitionFamilies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FamilyPrefix string `json:"familyPrefix"`
		Status       string `json:"status"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	families, aerr := h.store.listTaskDefinitionFamilies(r.Context())
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	filtered := make([]string, 0, len(families))
	for _, f := range families {
		if req.FamilyPrefix != "" && !strings.HasPrefix(f, req.FamilyPrefix) {
			continue
		}
		filtered = append(filtered, f)
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"families": filtered}, "application/x-amz-json-1.1")
}

// ---- Cluster update handlers --------------------------------------------------

// UpdateCluster handles AmazonEC2ContainerServiceV20141113.UpdateCluster.
func (h *Handler) UpdateCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster       string `json:"cluster"`
		Configuration *struct {
			ExecuteCommandConfiguration *struct {
				Logging string `json:"logging"`
			} `json:"executeCommandConfiguration"`
		} `json:"configuration"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name := extractClusterName(req.Cluster)
	c, aerr := h.store.getCluster(r.Context(), name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	// Configuration is metadata-only — store it and return the cluster.
	if aerr := h.store.putCluster(r.Context(), c); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"cluster": c}, "application/x-amz-json-1.1")
}

// UpdateClusterSettings handles AmazonEC2ContainerServiceV20141113.UpdateClusterSettings.
func (h *Handler) UpdateClusterSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster  string `json:"cluster"`
		Settings []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"settings"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name := extractClusterName(req.Cluster)
	c, aerr := h.store.getCluster(r.Context(), name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	// Settings are metadata-only — acknowledge and return the cluster.
	if aerr := h.store.putCluster(r.Context(), c); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"cluster": c}, "application/x-amz-json-1.1")
}
