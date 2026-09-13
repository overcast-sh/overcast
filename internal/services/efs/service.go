// Package efs provides Amazon Elastic File System (EFS) control-plane
// emulation.
//
// The REST-JSON API is served under the real AWS path prefix /2015-02-01/
// (file systems, mount targets, access points, policies, lifecycle and backup
// configuration, tagging, account preferences) — the only wire EFS answers.
// EFS is restJson1 and the pinned models give it no X-Amz-Target prefix, so
// unlike some JSON services this package has no typed dispatcher sharing
// POST /; CloudFormation's resource handlers reach it the same way any EFS
// SDK call does, over these REST bindings (see #1226).
//
// Emulation is metadata-only: no NFS data plane exists, so file systems are
// not mountable. Resources follow the real lifecycle (creating → available →
// deleting) via the shared lifecycle scheduler — transitions are inline with a
// real clock and observable under a mock clock.
package efs

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/lifecycle"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	serviceName = "efs"
	// apiPrefix is the versioned REST path prefix every EFS operation uses.
	apiPrefix = "/2015-02-01"
)

// Service implements router.Service for EFS.
type Service struct {
	cfg       *config.Config
	store     state.Store
	clk       clock.Clock
	log       *serviceutil.ServiceLogger
	scheduler *lifecycle.Scheduler

	// docker backs live-mode volumes (OVERCAST_EFS_MODE=live); nil until the
	// router's Docker probe succeeds. See live_volumes.go.
	docker      *docker.Client
	dockerReady atomic.Bool
	// puller fetches the root-directory materialization helper image; wired
	// with docker in SetDocker. See live_subdir.go.
	puller *docker.ImagePuller
	// materialized dedupes successful root-directory creations
	// ("volume/subpath" → struct{}) so repeat mounts skip the helper run.
	materialized sync.Map

	// instances resolves the sweep-domain identity stamped into every volume
	// and container this instance creates. See sweepDomain.
	instances *serviceutil.InstanceDomain

	// nfsMu serializes host-port reservations for mount-target NFS exports;
	// nfsWg tracks in-flight export starts so Stop waits for them. See
	// live_nfs.go.
	nfsMu sync.Mutex
	nfsWg sync.WaitGroup
	// reconcileMu prevents startup, reconnect, and live-exit recovery from
	// starting competing export replacements for the same snapshot.
	reconcileMu          sync.Mutex
	reconcileLifecycleMu sync.Mutex
	reconcileStopping    bool
	// skipNFSReadiness short-circuits the NFSv4 readiness probe. Tests that
	// drive a fake Docker daemon have no socket to probe; production never
	// sets it.
	skipNFSReadiness bool

	// subnetZones resolves a subnet's availability zone against EC2. Nil until
	// wired, and it returns empty for a subnet EC2 does not know, in which case
	// the zone is derived from the subnet ID instead.
	subnetZones SubnetZoneResolver

	// vpcResolver maps a mount target's subnet onto its VPC and that VPC onto
	// its Docker network, so an export lands on the plane the VPC's functions
	// and tasks are on. Nil until wired; see live_nfs.go.
	vpcResolver VPCNetworkResolver
}

// SubnetZoneResolver reports the availability zone a subnet was created in.
// Implemented by the EC2 service.
type SubnetZoneResolver interface {
	AvailabilityZoneForSubnet(ctx context.Context, subnetID string) string
}

// VPCNetworkResolver resolves mount-target placement against EC2 VPC network
// state. Implemented by the EC2 service; nil when EC2 is not enabled.
type VPCNetworkResolver interface {
	dataplane.VPCResolver
	// VpcIDForSubnet returns the VPC ID that owns the given subnet.
	VpcIDForSubnet(ctx context.Context, subnetID string) string
}

// SetVPCResolver wires EC2 so a mount target's export joins its subnet's VPC
// network. A mount target is created in a subnet, which on AWS fixes the VPC
// its ENI lives in — `fs-….efs.<region>.amazonaws.com` resolves only from
// inside that VPC. Without this every export sat on the default plane, where
// a function or task held to its VPC's network could not resolve it.
func (s *Service) SetVPCResolver(r VPCNetworkResolver) {
	s.vpcResolver = r
}

// SetSubnetZoneResolver wires EC2 so mount targets land in the zone their
// subnet is actually in. Without it the zone is derived by hashing the subnet
// ID, which puts genuinely different zones in collision with each other and
// rejects the second mount target of a multi-AZ file system — the shape every
// CDK `efs.FileSystem` produces.
func (s *Service) SetSubnetZoneResolver(r SubnetZoneResolver) {
	s.subnetZones = r
}

// zoneForSubnet resolves a subnet's zone, preferring EC2's answer and falling
// back to the subnet-ID hash for synthetic subnets EC2 never created.
func (s *Service) zoneForSubnet(ctx context.Context, region, subnetID string) (name, id string) {
	if s.subnetZones != nil {
		if az := s.subnetZones.AvailabilityZoneForSubnet(ctx, subnetID); az != "" {
			return az, azIDForName(region, az)
		}
	}
	return azForSubnet(region, subnetID)
}

// New returns a configured EFS service. Pure field assignment — no store
// reads or I/O (startup-budget rule).
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	return &Service{
		cfg:       cfg,
		store:     st,
		clk:       clk,
		log:       serviceutil.NewServiceLogger(logger, serviceName),
		scheduler: lifecycle.NewScheduler(clk),
		instances: serviceutil.NewAnchoredInstanceDomain(st, nsInstance, serviceutil.DataDirAnchor(cfg.DataDir)),
	}
}

func (s *Service) Name() string { return serviceName }

// InitBus wires live export-container recovery. Startup and watcher reconnect
// gaps are handled through ReconcileContainers; this path handles a death the
// watcher observes normally.
func (s *Service) InitBus(bus *events.Bus) {
	bus.Subscribe(events.DockerContainerDied, s.handleExportContainerDied)
}

// PathPrefixes satisfies router.PathPrefixService.
func (s *Service) PathPrefixes() []string { return []string{apiPrefix} }

// Stop satisfies router.Stopper: cancels pending lifecycle transitions and
// waits for in-flight callbacks, including NFS export starts.
func (s *Service) Stop(ctx context.Context) {
	s.scheduler.Stop(ctx)
	s.reconcileLifecycleMu.Lock()
	s.reconcileStopping = true
	s.dockerReady.Store(false)
	s.reconcileLifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.nfsWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// RegisterRoutes mounts the EFS REST-JSON API under /2015-02-01, mirroring the
// real AWS HTTP bindings. Routes are registered as absolute paths (not a
// chi.Route sub-router) so that modeled-but-unimplemented EFS paths (e.g.
// replication configuration) fall through to the router's generated 501
// fallback instead of a subrouter's bare 404.
func (s *Service) RegisterRoutes(r chi.Router) {
	// File systems
	r.Post(apiPrefix+"/file-systems", s.restCreateFileSystem)
	r.Get(apiPrefix+"/file-systems", s.restDescribeFileSystems)
	r.Put(apiPrefix+"/file-systems/{FileSystemId}", s.restUpdateFileSystem)
	r.Delete(apiPrefix+"/file-systems/{FileSystemId}", s.restDeleteFileSystem)
	r.Put(apiPrefix+"/file-systems/{FileSystemId}/protection", s.restUpdateFileSystemProtection)
	// File-system policy
	r.Put(apiPrefix+"/file-systems/{FileSystemId}/policy", s.restPutFileSystemPolicy)
	r.Get(apiPrefix+"/file-systems/{FileSystemId}/policy", s.restDescribeFileSystemPolicy)
	r.Delete(apiPrefix+"/file-systems/{FileSystemId}/policy", s.restDeleteFileSystemPolicy)
	// Lifecycle configuration
	r.Put(apiPrefix+"/file-systems/{FileSystemId}/lifecycle-configuration", s.restPutLifecycleConfiguration)
	r.Get(apiPrefix+"/file-systems/{FileSystemId}/lifecycle-configuration", s.restDescribeLifecycleConfiguration)
	// Backup policy
	r.Put(apiPrefix+"/file-systems/{FileSystemId}/backup-policy", s.restPutBackupPolicy)
	r.Get(apiPrefix+"/file-systems/{FileSystemId}/backup-policy", s.restDescribeBackupPolicy)
	// Mount targets
	r.Post(apiPrefix+"/mount-targets", s.restCreateMountTarget)
	r.Get(apiPrefix+"/mount-targets", s.restDescribeMountTargets)
	r.Delete(apiPrefix+"/mount-targets/{MountTargetId}", s.restDeleteMountTarget)
	r.Get(apiPrefix+"/mount-targets/{MountTargetId}/security-groups", s.restDescribeMountTargetSecurityGroups)
	r.Put(apiPrefix+"/mount-targets/{MountTargetId}/security-groups", s.restModifyMountTargetSecurityGroups)
	// Access points
	r.Post(apiPrefix+"/access-points", s.restCreateAccessPoint)
	r.Get(apiPrefix+"/access-points", s.restDescribeAccessPoints)
	r.Delete(apiPrefix+"/access-points/{AccessPointId}", s.restDeleteAccessPoint)
	// Tagging
	r.Post(apiPrefix+"/resource-tags/{ResourceId}", s.restTagResource)
	r.Delete(apiPrefix+"/resource-tags/{ResourceId}", s.restUntagResource)
	r.Get(apiPrefix+"/resource-tags/{ResourceId}", s.restListTagsForResource)
	// Legacy tagging. DescribeTags' modeled URI carries a trailing slash;
	// register both forms so hand-written clients work too.
	r.Post(apiPrefix+"/create-tags/{FileSystemId}", s.restCreateTags)
	r.Post(apiPrefix+"/delete-tags/{FileSystemId}", s.restDeleteTags)
	r.Get(apiPrefix+"/tags/{FileSystemId}", s.restDescribeTags)
	r.Get(apiPrefix+"/tags/{FileSystemId}/", s.restDescribeTags)
	// Account preferences
	r.Get(apiPrefix+"/account-preferences", s.restDescribeAccountPreferences)
	r.Put(apiPrefix+"/account-preferences", s.restPutAccountPreferences)
}

// ─── REST adapters ────────────────────────────────────────────────────────────
//
// Each adapter binds URI/query/body members onto the shared typed request
// struct, invokes the same core handler the typed dispatcher uses, and writes
// the operation's documented success status. Business logic lives only in
// typed_logic.go.

// writeRESTResult writes the shared (out, aerr) handler result with the
// operation's REST success status. A nil out writes an empty body (AWS's 204
// responses).
func writeRESTResult(w http.ResponseWriter, r *http.Request, status int, out any, aerr *protocol.AWSError) {
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if out == nil {
		// Empty-body success (AWS's 200/204 void responses). Drain the request
		// body so the HTTP/1.1 connection can be reused by SDK clients.
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
		}
		w.WriteHeader(status)
		return
	}
	protocol.WriteJSON(w, r, status, out)
}

func (s *Service) restCreateFileSystem(w http.ResponseWriter, r *http.Request) {
	var req createFileSystemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := s.createFileSystemTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusCreated, outOrNil(out), aerr)
}

func (s *Service) restDescribeFileSystems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := describeFileSystemsRequest{
		MaxItems:      serviceutil.QueryInt(r, "MaxItems", 0),
		Marker:        q.Get("Marker"),
		CreationToken: q.Get("CreationToken"),
		FileSystemId:  q.Get("FileSystemId"),
	}
	out, aerr := s.describeFileSystemsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restUpdateFileSystem(w http.ResponseWriter, r *http.Request) {
	var req updateFileSystemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.FileSystemId = chi.URLParam(r, "FileSystemId")
	out, aerr := s.updateFileSystemTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusAccepted, outOrNil(out), aerr)
}

func (s *Service) restDeleteFileSystem(w http.ResponseWriter, r *http.Request) {
	req := deleteFileSystemRequest{FileSystemId: chi.URLParam(r, "FileSystemId")}
	out, aerr := s.deleteFileSystemTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusNoContent, out, aerr)
}

func (s *Service) restUpdateFileSystemProtection(w http.ResponseWriter, r *http.Request) {
	var req updateFileSystemProtectionRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.FileSystemId = chi.URLParam(r, "FileSystemId")
	out, aerr := s.updateFileSystemProtectionTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restPutFileSystemPolicy(w http.ResponseWriter, r *http.Request) {
	var req putFileSystemPolicyRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.FileSystemId = chi.URLParam(r, "FileSystemId")
	out, aerr := s.putFileSystemPolicyTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDescribeFileSystemPolicy(w http.ResponseWriter, r *http.Request) {
	req := describeFileSystemPolicyRequest{FileSystemId: chi.URLParam(r, "FileSystemId")}
	out, aerr := s.describeFileSystemPolicyTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDeleteFileSystemPolicy(w http.ResponseWriter, r *http.Request) {
	req := describeFileSystemPolicyRequest{FileSystemId: chi.URLParam(r, "FileSystemId")}
	out, aerr := s.deleteFileSystemPolicyTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, out, aerr)
}

func (s *Service) restPutLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	var req putLifecycleConfigurationRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.FileSystemId = chi.URLParam(r, "FileSystemId")
	out, aerr := s.putLifecycleConfigurationTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDescribeLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	req := describeLifecycleConfigurationRequest{FileSystemId: chi.URLParam(r, "FileSystemId")}
	out, aerr := s.describeLifecycleConfigurationTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restPutBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var req putBackupPolicyRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.FileSystemId = chi.URLParam(r, "FileSystemId")
	out, aerr := s.putBackupPolicyTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDescribeBackupPolicy(w http.ResponseWriter, r *http.Request) {
	req := describeBackupPolicyRequest{FileSystemId: chi.URLParam(r, "FileSystemId")}
	out, aerr := s.describeBackupPolicyTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restCreateMountTarget(w http.ResponseWriter, r *http.Request) {
	var req createMountTargetRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := s.createMountTargetTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDescribeMountTargets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := describeMountTargetsRequest{
		MaxItems:      serviceutil.QueryInt(r, "MaxItems", 0),
		Marker:        q.Get("Marker"),
		FileSystemId:  q.Get("FileSystemId"),
		MountTargetId: q.Get("MountTargetId"),
		AccessPointId: q.Get("AccessPointId"),
	}
	out, aerr := s.describeMountTargetsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDeleteMountTarget(w http.ResponseWriter, r *http.Request) {
	req := deleteMountTargetRequest{MountTargetId: chi.URLParam(r, "MountTargetId")}
	out, aerr := s.deleteMountTargetTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusNoContent, out, aerr)
}

func (s *Service) restDescribeMountTargetSecurityGroups(w http.ResponseWriter, r *http.Request) {
	req := mountTargetSecurityGroupsRequest{MountTargetId: chi.URLParam(r, "MountTargetId")}
	out, aerr := s.describeMountTargetSecurityGroupsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restModifyMountTargetSecurityGroups(w http.ResponseWriter, r *http.Request) {
	var req mountTargetSecurityGroupsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.MountTargetId = chi.URLParam(r, "MountTargetId")
	out, aerr := s.modifyMountTargetSecurityGroupsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusNoContent, out, aerr)
}

func (s *Service) restCreateAccessPoint(w http.ResponseWriter, r *http.Request) {
	var req createAccessPointRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := s.createAccessPointTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDescribeAccessPoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := describeAccessPointsRequest{
		MaxResults:    serviceutil.QueryInt(r, "MaxResults", 0),
		NextToken:     q.Get("NextToken"),
		AccessPointId: q.Get("AccessPointId"),
		FileSystemId:  q.Get("FileSystemId"),
	}
	out, aerr := s.describeAccessPointsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDeleteAccessPoint(w http.ResponseWriter, r *http.Request) {
	req := deleteAccessPointRequest{AccessPointId: chi.URLParam(r, "AccessPointId")}
	out, aerr := s.deleteAccessPointTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusNoContent, out, aerr)
}

func (s *Service) restTagResource(w http.ResponseWriter, r *http.Request) {
	var req tagResourceRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.ResourceId = chi.URLParam(r, "ResourceId")
	out, aerr := s.tagResourceTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, out, aerr)
}

func (s *Service) restUntagResource(w http.ResponseWriter, r *http.Request) {
	req := untagResourceRequest{
		ResourceId: chi.URLParam(r, "ResourceId"),
		TagKeys:    r.URL.Query()["tagKeys"],
	}
	out, aerr := s.untagResourceTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, out, aerr)
}

func (s *Service) restListTagsForResource(w http.ResponseWriter, r *http.Request) {
	req := listTagsForResourceRequest{
		ResourceId: chi.URLParam(r, "ResourceId"),
		MaxResults: serviceutil.QueryInt(r, "MaxResults", 0),
		NextToken:  r.URL.Query().Get("NextToken"),
	}
	out, aerr := s.listTagsForResourceTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restCreateTags(w http.ResponseWriter, r *http.Request) {
	var req createTagsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.FileSystemId = chi.URLParam(r, "FileSystemId")
	out, aerr := s.createTagsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusNoContent, out, aerr)
}

func (s *Service) restDeleteTags(w http.ResponseWriter, r *http.Request) {
	var req deleteTagsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	req.FileSystemId = chi.URLParam(r, "FileSystemId")
	out, aerr := s.deleteTagsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusNoContent, out, aerr)
}

func (s *Service) restDescribeTags(w http.ResponseWriter, r *http.Request) {
	req := describeTagsRequest{
		FileSystemId: chi.URLParam(r, "FileSystemId"),
		MaxItems:     serviceutil.QueryInt(r, "MaxItems", 0),
		Marker:       r.URL.Query().Get("Marker"),
	}
	out, aerr := s.describeTagsTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restDescribeAccountPreferences(w http.ResponseWriter, r *http.Request) {
	req := describeAccountPreferencesRequest{
		NextToken:  r.URL.Query().Get("NextToken"),
		MaxResults: serviceutil.QueryInt(r, "MaxResults", 0),
	}
	out, aerr := s.describeAccountPreferencesTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

func (s *Service) restPutAccountPreferences(w http.ResponseWriter, r *http.Request) {
	var req putAccountPreferencesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := s.putAccountPreferencesTyped(r.Context(), &req)
	writeRESTResult(w, r, http.StatusOK, outOrNil(out), aerr)
}

// outOrNil collapses a typed nil pointer into an untyped nil so
// writeRESTResult's nil check works for any response type.
func outOrNil[T any](out *T) any {
	if out == nil {
		return nil
	}
	return out
}
