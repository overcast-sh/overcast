// Package ecs provides emulation of Amazon Elastic Container Service (ECS).
//
// Supported: CreateCluster, DescribeClusters, ListClusters, DeleteCluster,
//
//	RegisterTaskDefinition, DescribeTaskDefinition, ListTaskDefinitions,
//	DeregisterTaskDefinition, RunTask, StopTask, DescribeTasks, ListTasks,
//	CreateService, UpdateService, DeleteService, DescribeServices, ListServices
//
// Unsupported: See docs/services/ecs.md (stub operations return 501).
package ecs

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "ecs"

// awsapiService is ECS's key in the generated AWS model corpus.
// serviceutil.MustAWSService validates it at package initialisation, so a
// key the models do not carry fails immediately rather than silently
// answering every unimplemented operation with a 400.
var awsapiService = serviceutil.MustAWSService(serviceName)

// targetPrefix is the X-Amz-Target prefix for ECS.
const targetPrefix = "AmazonEC2ContainerServiceV20141113."

// Service implements router.Service and router.TargetDispatcher for ECS.
type Service struct {
	handler *Handler
	cfg     *config.Config
	log     *serviceutil.ServiceLogger
}

// New returns a configured ECS Service.
//
// dbg is the router-owned debug-target manager (docs/plans/compute-debugger.md);
// nil leaves the debugger off, which is what every test that does not exercise
// it wants.
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock, dbg *debugger.Manager) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	s := newECSStore(store, cfg.Region)
	h := newHandler(cfg, s, log, clk)
	h.debugger = dbg
	svc := &Service{
		handler: h,
		cfg:     cfg,
		log:     log,
	}
	return svc
}

// SetDocker wires a Docker client into the ECS handler, enabling container
// execution for RunTask/StopTask. Called after Docker availability is confirmed.
func (s *Service) SetDocker(dc *docker.Client) {
	s.handler.docker = dc
	s.handler.puller = docker.NewImagePuller(dc).WithResolver(s.handler.images)
	s.handler.gc = docker.NewGC(dc, s.log.ZapLogger(), s.handler.cfg.ECSKeepContainers, s.handler.instances.Resolve)
	// Every removal this GC performs takes the container's final output first,
	// whichever path scheduled it. The alternative is each teardown remembering
	// to capture, which is how the scheduler path came to lose them.
	s.handler.gc.SetBeforeRemove(s.handler.captureContainerLogsByID)
	s.handler.gc.StartRemoveLoop(context.Background())
	s.handler.gc.Sweep(serviceName) // clean up orphaned containers from previous runs
	s.handler.dockerReady.Store(true)

	// Volumes second, and in the background: task-scoped volumes whose task did
	// not survive the last run are only removable once the container sweep
	// above has released them, and neither should hold up wiring Docker.
	go s.handler.sweepOrphanedTaskVolumes(context.Background())
}

// SetImageResolver wires the registry resolver used when a container image
// names a registry Overcast serves rather than a public one — an ECR image
// built and pushed by CDK, whose reference points at real AWS. Implemented by
// the ECR service.
//
// Wire this before SetDocker: the puller takes the resolver when it is built.
// The router does, and the Docker probe that calls SetDocker starts after.
func (s *Service) SetImageResolver(r docker.ImageResolver) {
	s.handler.images = r
}

// SetVPCResolver wires the EC2 VPC resolver for awsvpc task placement.
func (s *Service) SetVPCResolver(r VPCNetworkResolver) {
	s.handler.vpcResolver = r
}

// SetEFSResolver wires the EFS volume resolver so task containers can mount
// efsVolumeConfiguration-backed volumes in EFS live mode.
func (s *Service) SetEFSResolver(r EFSVolumeResolver) {
	s.handler.efsResolver = r
}

// SetSecretResolvers wires Secrets Manager and SSM so a container definition's
// `secrets` are fetched and injected as environment variables at task start.
func (s *Service) SetSecretResolvers(sm SecretsManagerResolver, ssm ParameterResolver) {
	s.handler.secrets = sm
	s.handler.parameters = ssm
}

// SetTargetRegistrar wires elbv2 so a service's tasks are registered with the
// target groups named in its loadBalancers.
func (s *Service) SetTargetRegistrar(r TargetRegistrar) {
	s.handler.targets = r
}

// InitLogWriter wires CloudWatch Logs so task containers using the awslogs log
// driver have their output shipped there, as they do on ECS. Applies to every
// task regardless of launch type — the driver is a property of the container
// definition, not of Fargate or EC2.
func (s *Service) InitLogWriter(w events.LogWriter) {
	s.handler.logWriter = w
}

// InitBus wires the event bus for ECS lifecycle events.
func (s *Service) InitBus(bus *events.Bus) {
	s.handler.bus = bus
	bus.Subscribe(events.DockerContainerDied, s.handler.handleContainerDied)
}

// ReconcileContainers satisfies router.ContainerReconciler. The central
// Docker supervisor calls it at startup and after watcher reconnects so a
// missed event cannot leave a dead container represented as a running task.
func (s *Service) ReconcileContainers(ctx context.Context, containers []docker.ContainerSummary) {
	s.handler.reconcileContainers(ctx, containers)
}

// Name satisfies router.Service.
func (s *Service) Name() string { return serviceName }

// RegisterRoutes satisfies router.Service. Registers emulator-only endpoints.
func (s *Service) RegisterRoutes(r chi.Router) {
	r.Get("/_overcast/ecs/tasks/{taskArn}/logs/{container}", s.handler.GetTaskContainerLogs)
	r.Get("/_overcast/ecs/clusters/{cluster}/tasks", s.handler.ListClusterTasks)
}

// Stop cancels pending lifecycle transitions and cleans up Docker containers
// via the GC. The GC does a Docker-level sweep so even orphaned containers
// (whose task record was already deleted) are caught and removed.
func (s *Service) Stop(ctx context.Context) {
	s.handler.scheduler.Stop(ctx)

	if s.handler.gc != nil {
		s.handler.gc.DrainAndSweep(ctx, serviceName)
	}
}

// TargetPrefix satisfies router.TargetDispatcher.
func (s *Service) TargetPrefix() string { return targetPrefix }

// Dispatch satisfies router.TargetDispatcher.
func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code:       "UnsupportedProtocol",
				Message:    "ECS does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		if c.Name() != codec.NameRPCv2CBOR {
			s.dispatchLegacy(w, r, opName)
			return
		}
		if typed, ok := s.handler.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		// Same split as dispatchLegacy, in CBOR: a name AWS does not model is
		// InvalidAction here too, not a 501 claiming it is merely unemulated.
		serviceutil.WriteUnhandledOperation(w, r, c, awsapiService, opName, errInvalidAction(opName))
		return
	}

	suffix := r.Header.Get("X-Amz-Target")[len(targetPrefix):]
	s.dispatchLegacy(w, r, suffix)
}

func (s *Service) dispatchLegacy(w http.ResponseWriter, r *http.Request, suffix string) {
	if fn, ok := s.handler.ops[suffix]; ok {
		fn(w, r)
		return
	}
	// A real ECS operation Overcast has not implemented gets an honest 501;
	// InvalidAction stays for a name AWS does not model (#1645). This path
	// serves the JSON families only — Dispatch sends RPC v2 CBOR to the typed
	// operations — so JSON11 writes the bytes WriteJSONError always did.
	serviceutil.WriteUnhandledOperation(w, r, codec.JSON11, awsapiService, suffix, errInvalidAction(suffix))
}

// errInvalidAction is ECS's answer for a target naming no modeled operation.
func errInvalidAction(action string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidAction",
		Message:    "The action " + action + " is not valid for this web service.",
		HTTPStatus: http.StatusBadRequest,
	}
}
