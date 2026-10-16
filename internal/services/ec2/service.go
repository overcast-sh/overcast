// Package ec2 provides emulation of Amazon EC2 and VPC operations.
//
// Implemented: DescribeRegions, DescribeAvailabilityZones, DescribeInstances,
// DescribeInstanceTypes, RunInstances, TerminateInstances, StartInstances,
// StopInstances, CreateVpc, DescribeVpcs, DeleteVpc,
// CreateSubnet, DeleteSubnet, DescribeSubnets,
// CreateSecurityGroup, DeleteSecurityGroup, DescribeSecurityGroups,
// AuthorizeSecurityGroupIngress, AuthorizeSecurityGroupEgress,
// RevokeSecurityGroupIngress, RevokeSecurityGroupEgress,
// DescribeImages, CreateKeyPair, DescribeKeyPairs, DeleteKeyPair,
// CreateRouteTable, DescribeRouteTables, DeleteRouteTable, CreateRoute,
// AssociateRouteTable, DisassociateRouteTable,
// CreateInternetGateway, DescribeInternetGateways, DeleteInternetGateway,
// AttachInternetGateway, DetachInternetGateway,
// CreateVpnGateway, AttachVpnGateway, DescribeVpnGateways,
// DetachVpnGateway, DeleteVpnGateway,
// CreateVpcPeeringConnection, AcceptVpcPeeringConnection,
// DescribeVpcPeeringConnections, DeleteVpcPeeringConnection.
// All other operations return HTTP 501 Not Implemented.
package ec2

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "ec2"

// Service implements router.Service and router.QueryDispatcher for EC2 and VPC.
// Uses the AWS Query protocol (form-encoded POST, XML responses) and
// identifies itself by the API version "2016-11-15".
type Service struct {
	handler *Handler
	log     *serviceutil.ServiceLogger
}

// New returns a configured EC2 Service.
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	return &Service{
		handler: newHandler(cfg, store, log, clk),
		log:     log,
	}
}

// InitBus wires the event bus for VPC/subnet/security group lifecycle events.
func (s *Service) InitBus(bus *events.Bus) {
	s.handler.bus = bus
}

// SetDocker wires a Docker client for VPC network management. EC2 manages
// Docker networks (one per VPC) rather than containers.
func (s *Service) SetDocker(dc *docker.Client) {
	s.handler.docker = dc
	s.handler.dockerReady.Store(true)
}

// SetNetworkReporter wires the health tracker, so the networks EC2 creates on
// demand — and the identity that says whose they are — are reported beside the
// two planes the Docker probe ensures at startup.
func (s *Service) SetNetworkReporter(r docker.NetworkReporter) {
	s.handler.SetNetworkReporter(r)
}

// ReconcileNetworks satisfies router.NetworkReconciler. Called once after
// Docker becomes available to sync stored VPC state against actual Docker
// networks (recreate missing networks, update stale Docker network IDs).
func (s *Service) ReconcileNetworks(ctx context.Context, networks []docker.NetworkSummary) {
	s.handler.reconcileNetworks(ctx, networks)
}

// VpcIDForSubnet returns the VPC ID that owns the given subnet.
// Returns empty string if the subnet is not found.
func (s *Service) VpcIDForSubnet(ctx context.Context, subnetID string) string {
	sub, aerr := s.handler.store.getSubnet(ctx, subnetID)
	if aerr != nil {
		return ""
	}
	return sub.VpcID
}

// AvailabilityZoneForSubnet returns the availability zone a subnet was created
// in. Returns empty string when the subnet is not an EC2 one, which lets a
// caller fall back to whatever it does for synthetic subnet IDs.
func (s *Service) AvailabilityZoneForSubnet(ctx context.Context, subnetID string) string {
	sub, aerr := s.handler.store.getSubnet(ctx, subnetID)
	if aerr != nil {
		return ""
	}
	return sub.AvailabilityZone
}

// DockerNetworkForVpc returns the Docker network ID for the given VPC.
// Returns empty string if the VPC is not found or has no Docker network.
//
// The region is reconciled first if no pass has covered it since start, so
// the answer names a network the daemon has rather than one a restart lost.
func (s *Service) DockerNetworkForVpc(ctx context.Context, vpcID string) string {
	s.handler.ensureRegionReconciled(ctx)
	vpc, aerr := s.handler.store.getVPC(ctx, vpcID)
	if aerr != nil {
		return ""
	}
	return vpc.DockerNetworkID
}

// VPCNetworkStatus returns the current network status for the given VPC.
// Empty string means the VPC was not found. As for DockerNetworkForVpc, the
// region is reconciled first when no pass has covered it since start.
func (s *Service) VPCNetworkStatus(ctx context.Context, vpcID string) string {
	s.handler.ensureRegionReconciled(ctx)
	vpc, aerr := s.handler.store.getVPC(ctx, vpcID)
	if aerr != nil {
		return ""
	}
	if vpc.NetworkStatus == "" {
		return vpcNetworkStatusOK
	}
	// A VPC whose network create was refused earlier is retried here, where
	// the answer is about to refuse a launch, rather than staying unbacked
	// until Docker next reappears.
	if vpc.NetworkStatus == vpcNetworkStatusUnbacked {
		return s.handler.healUnbackedVPC(ctx, vpcID)
	}
	return vpc.NetworkStatus
}

// NetworkProblems reports the VPCs whose Docker network could not be brought
// to the isolation their internet-gateway state calls for, or whose
// containers could not be given the egress their route tables call for
// (OVERCAST_VPC_EGRESS=routed), ordered by VPC ID. The router renders them as
// a health advisory.
func (s *Service) NetworkProblems() []dataplane.VPCNetworkProblem {
	return s.handler.networkProblems()
}

// EgressNetworkForSubnets satisfies dataplane.SubnetPlacer: under
// OVERCAST_VPC_EGRESS=routed, the routable network a container placed in
// these subnets of vpcID takes its default route from, created on demand —
// or "" when none of the subnets' route tables grants a route out, and in
// every other mode. See vpc_egress.go.
func (s *Service) EgressNetworkForSubnets(ctx context.Context, vpcID string, subnetIDs []string) (string, error) {
	return s.handler.egressNetworkForSubnets(ctx, vpcID, subnetIDs)
}

// RecordPlacement satisfies dataplane.PlacementRecorder: which subnets a
// container was placed in, so a later route-table change can move it on or
// off its VPC's egress network. A no-op outside `routed`.
func (s *Service) RecordPlacement(ctx context.Context, containerID string, p dataplane.Placement) {
	s.handler.recordPlacement(ctx, containerID, p)
}

// AllocatePrivateIPForSubnet returns the API-visible private IP for the given
// subnet, applying remapped translation when needed. Empty string means the
// subnet could not be resolved.
func (s *Service) AllocatePrivateIPForSubnet(ctx context.Context, subnetID string) string {
	apiIP, _, _ := s.handler.allocatePrivateIPForSubnet(ctx, subnetID)
	return apiIP
}

// Name satisfies router.Service.
func (s *Service) Name() string { return serviceName }

// RegisterRoutes satisfies router.Service. EC2 has no path-routed endpoints.
func (s *Service) RegisterRoutes(_ chi.Router) {}

// OwnsVersion satisfies router.QueryVersionOwner.
func (s *Service) OwnsVersion(version string) bool { return version == awsapi.VersionEC2 }

// Stop cancels pending lifecycle transitions (e.g. instance state changes).
func (s *Service) Stop(ctx context.Context) {
	s.handler.scheduler.Stop(ctx)
}

// ec2ErrorCodec renders a typed operation's errors in EC2's own XML error
// shape.
//
// EC2's error envelope is not the generic Query one. AWS documents EC2 as
// `<Response><Errors><Error><Code/><Message/></Error></Errors><RequestID/>`,
// a plural `<Errors>` wrapper the other Query services do not have, and the
// generic codec's WriteError emits SNS's single `<Error><Type/>…` instead. The
// wrapper existed when the typed branch was first wired, was deleted with it to
// keep the unused check quiet, and is restored here from that commit rather
// than rewritten — an error shape is the half of a response nobody notices
// until a client's error handling stops matching.
type ec2ErrorCodec struct {
	codec.Codec
}

func (c ec2ErrorCodec) WriteError(w http.ResponseWriter, r *http.Request, aerr *protocol.AWSError) {
	protocol.WriteEC2QueryXMLError(w, r, aerr)
}

// DispatchQuery satisfies router.QueryDispatcher; routes to the correct handler.
//
// EC2 answers from the typed operation registry for the operations named in
// ec2TypedOps, and from the legacy handler map for the rest. The allow-list is
// deliberately the positive one — see its comment for why, and #754 for what
// dispatching the whole registry at once cost the first time it was tried.
func (s *Service) DispatchQuery(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		protocol.WriteEC2QueryXMLError(w, r, protocol.ErrInvalidArgument("invalid request form encoding"))
		return
	}
	if c, opName := codec.FromContext(r.Context()); c != nil && ec2TypedOps[opName] {
		if !serviceutil.AllowProtocolDrift(s.handler.cfg, s.log, opName, c, s.SupportedProtocols()) {
			protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
				Code: "UnsupportedProtocol", Message: "EC2 does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		if typed, ok := s.handler.typedOp[opName]; ok {
			typed.Invoke(w, r, ec2ErrorCodec{Codec: c})
			return
		}
	}
	action := r.FormValue("Action")
	if handler, ok := s.handler.ops[action]; ok {
		handler(w, r)
		return
	}
	protocol.NotImplementedEC2QueryXML(w, r)
}
