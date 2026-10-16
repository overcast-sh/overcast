package ec2

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/lifecycle"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const ec2XMLNS = "http://ec2.amazonaws.com/doc/2016-11-15/"

// syntheticIPCounter generates incrementing IPs for Elastic IPs, NAT gateways, etc.
var syntheticIPCounter atomic.Uint32

// Handler handles EC2 Query-protocol requests.
type Handler struct {
	cfg         *config.Config
	store       *ec2Store
	log         *serviceutil.ServiceLogger
	clk         clock.Clock
	bus         *events.Bus
	scheduler   *lifecycle.Scheduler
	docker      *docker.Client
	dockerReady atomic.Bool
	vpcStrategy vpcNetworkStrategy
	// instances is the sweep domain a VPC network is stamped with at creation
	// and checked against before the reconcile's orphan pass removes it, so an
	// instance sharing a daemon with another cannot take the other's VPC
	// networks for litter. See docker.LabelInstance.
	instances *serviceutil.InstanceDomain

	// networkReporter carries this instance's identity and the verified state
	// of each per-VPC network into /_overcast/health. Optional; nil reports
	// nothing.
	networkReporter docker.NetworkReporter

	// netProblems records, per VPC ID, a dataplane.VPCNetworkProblem: a VPC
	// whose Docker network could not be brought to the isolation its gateway
	// state calls for, or — under egressProblemKey — whose containers could
	// not be given the egress their route tables call for, or — under
	// unbackedProblemKey — that has no network because the daemon refused to
	// create one. Read by Service.NetworkProblems for the health advisories;
	// cleared when a later flip, move or create succeeds or the VPC is
	// deleted.
	netProblems sync.Map // vpcID, or vpcID+"/egress" -> dataplane.VPCNetworkProblem

	// egressMu serialises egress-network CIDR allocation across VPCs, and
	// nothing else. See reserveEgressCIDR.
	egressMu sync.Mutex

	// reconcileMu serialises the network reconcile passes: the startup one
	// over every region, and the per-region one a placement triggers
	// (ensureRegionReconciled). Two passes interleaving would adopt and
	// create against each other's view of the daemon.
	reconcileMu sync.Mutex
	// reconciledAll is set when a full pass (reconcileNetworks) has covered
	// every region the store held, and cleared when the next one starts —
	// the router runs one on every Docker readiness transition, and a
	// network lost with the daemon is not something an earlier pass can
	// vouch for. While it is set the backstop below is a single load: a VPC
	// created after a full pass was backed against the live daemon by
	// EnsureNetwork and needs no healing.
	reconciledAll atomic.Bool
	// reconciledRegions holds, per region, what the per-region backstop has
	// established since the last full pass started, for when that full pass
	// could not run to the end (the store unreadable at the time): that the
	// region is covered, or that an attempt failed and when the next may be
	// made (regionReconcile). A region absent from both gets a pass on first
	// use, so a VPC the startup pass could not see is still backed before
	// anything is placed in it.
	reconciledRegions sync.Map // region → regionReconcile
	// unbackedRetries holds, per VPC, when healUnbackedVPC last retried it.
	unbackedRetries sync.Map // vpcID → time.Time

	// defaultVPCLocks serialises default-VPC seeding per region, so two
	// concurrent describes cannot both find none and both seed one.
	defaultVPCLocks sync.Map // region → *sync.Mutex

	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation

	// keyMaterialFor mints CreateKeyPair's fingerprint and dummy key
	// material. Real deployments use the crypto/rand-backed default
	// (randomFingerprint/dummyKeyMaterial in handler_keypairs.go); the
	// mutation-parity test (typed_parity_mutations_dev_test.go) overrides it
	// on both of its independently-seeded handlers so the legacy and typed
	// dispatch paths mint byte-identical material, leaving nothing that
	// needs masking.
	keyMaterialFor func() (fingerprint, material string)
}

func newHandler(cfg *config.Config, store state.Store, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{
		cfg:       cfg,
		store:     newEC2Store(store, cfg.Region, cfg.AccountID),
		log:       log,
		clk:       clk,
		scheduler: lifecycle.NewScheduler(clk),
		instances: serviceutil.NewAnchoredInstanceDomain(store, nsInstance, serviceutil.DataDirAnchor(cfg.DataDir)),
	}
	h.keyMaterialFor = func() (string, string) { return randomFingerprint(), dummyKeyMaterial() }
	// The guard wraps whichever strategy is configured: the default VPC's
	// network is the shared data plane, not a per-VPC bridge, and no mapping
	// policy may create, adopt, recreate or remove it.
	h.vpcStrategy = &defaultVPCGuard{
		inner: resolveVPCNetworkStrategy(cfg.EC2VPCNetworkStrategy, h),
		h:     h,
	}
	h.buildOpsMap()
	h.buildTypedOps()
	return h
}

func (h *Handler) buildOpsMap() {
	h.ops = map[string]http.HandlerFunc{
		"DescribeRegions":                h.DescribeRegions,
		"DescribeAvailabilityZones":      h.DescribeAvailabilityZones,
		"DescribeInstances":              h.DescribeInstances,
		"DescribeInstanceTypes":          h.DescribeInstanceTypes,
		"CreateVpc":                      h.CreateVpc,
		"DescribeVpcs":                   h.DescribeVpcs,
		"DeleteVpc":                      h.DeleteVpc,
		"CreateSubnet":                   h.CreateSubnet,
		"DeleteSubnet":                   h.DeleteSubnet,
		"CreateSecurityGroup":            h.CreateSecurityGroup,
		"DeleteSecurityGroup":            h.DeleteSecurityGroup,
		"AuthorizeSecurityGroupIngress":  h.AuthorizeSecurityGroupIngress,
		"AuthorizeSecurityGroupEgress":   h.AuthorizeSecurityGroupEgress,
		"RevokeSecurityGroupIngress":     h.RevokeSecurityGroupIngress,
		"RevokeSecurityGroupEgress":      h.RevokeSecurityGroupEgress,
		"DescribeSecurityGroups":         h.DescribeSecurityGroups,
		"DescribeSubnets":                h.DescribeSubnets,
		"RunInstances":                   h.RunInstances,
		"TerminateInstances":             h.TerminateInstances,
		"StartInstances":                 h.StartInstances,
		"StopInstances":                  h.StopInstances,
		"DescribeImages":                 h.DescribeImages,
		"CreateKeyPair":                  h.CreateKeyPair,
		"DescribeKeyPairs":               h.DescribeKeyPairs,
		"DeleteKeyPair":                  h.DeleteKeyPair,
		"CreateRouteTable":               h.CreateRouteTable,
		"DescribeRouteTables":            h.DescribeRouteTables,
		"DeleteRouteTable":               h.DeleteRouteTable,
		"CreateRoute":                    h.CreateRoute,
		"DeleteRoute":                    h.DeleteRoute,
		"AssociateRouteTable":            h.AssociateRouteTable,
		"DisassociateRouteTable":         h.DisassociateRouteTable,
		"CreateInternetGateway":          h.CreateInternetGateway,
		"DescribeInternetGateways":       h.DescribeInternetGateways,
		"DeleteInternetGateway":          h.DeleteInternetGateway,
		"AttachInternetGateway":          h.AttachInternetGateway,
		"DetachInternetGateway":          h.DetachInternetGateway,
		"CreateVpcPeeringConnection":     h.CreateVpcPeeringConnection,
		"AcceptVpcPeeringConnection":     h.AcceptVpcPeeringConnection,
		"DescribeVpcPeeringConnections":  h.DescribeVpcPeeringConnections,
		"DeleteVpcPeeringConnection":     h.DeleteVpcPeeringConnection,
		"CreateTags":                     h.CreateTags,
		"DeleteTags":                     h.DeleteTags,
		"DescribeTags":                   h.DescribeTags,
		"AllocateAddress":                h.AllocateAddress,
		"ReleaseAddress":                 h.ReleaseAddress,
		"DescribeAddresses":              h.DescribeAddresses,
		"AssociateAddress":               h.AssociateAddress,
		"DisassociateAddress":            h.DisassociateAddress,
		"CreateNatGateway":               h.CreateNatGateway,
		"DescribeNatGateways":            h.DescribeNatGateways,
		"DeleteNatGateway":               h.DeleteNatGateway,
		"ModifySubnetAttribute":          h.ModifySubnetAttribute,
		"ModifyVpcAttribute":             h.ModifyVpcAttribute,
		"DescribeVpcAttribute":           h.DescribeVpcAttribute,
		"DescribeDhcpOptions":            h.DescribeDhcpOptions,
		"DescribeAccountAttributes":      h.DescribeAccountAttributes,
		"CreateVpnGateway":               h.CreateVpnGateway,
		"AttachVpnGateway":               h.AttachVpnGateway,
		"DescribeVpnGateways":            h.DescribeVpnGateways,
		"DetachVpnGateway":               h.DetachVpnGateway,
		"DeleteVpnGateway":               h.DeleteVpnGateway,
		"CreateNetworkInterface":         h.CreateNetworkInterface,
		"DescribeNetworkInterfaces":      h.DescribeNetworkInterfaces,
		"DeleteNetworkInterface":         h.DeleteNetworkInterface,
		"ModifyInstanceAttribute":        h.ModifyInstanceAttribute,
		"CreateVpcEndpoint":              h.CreateVpcEndpoint,
		"DescribeVpcEndpoints":           h.DescribeVpcEndpoints,
		"DeleteVpcEndpoints":             h.DeleteVpcEndpoints,
		"CreateLaunchTemplate":           h.CreateLaunchTemplate,
		"CreateLaunchTemplateVersion":    h.CreateLaunchTemplateVersion,
		"DescribeLaunchTemplates":        h.DescribeLaunchTemplates,
		"DescribeLaunchTemplateVersions": h.DescribeLaunchTemplateVersions,
		"ModifyLaunchTemplate":           h.ModifyLaunchTemplate,
		"DeleteLaunchTemplate":           h.DeleteLaunchTemplate,
		"DeleteLaunchTemplateVersions":   h.DeleteLaunchTemplateVersions,
	}
}

func (h *Handler) buildTypedOps() {
	h.typedOp = h.typedOps()
}

// ── DescribeRegions ──────────────────────────────────────────────────────────

type xmlDescribeRegionsResponse struct {
	XMLName    xml.Name    `xml:"DescribeRegionsResponse"`
	Xmlns      string      `xml:"xmlns,attr"`
	RequestID  string      `xml:"requestId"`
	RegionInfo []xmlRegion `xml:"regionInfo>item"`
}

type xmlRegion struct {
	RegionName     string `xml:"regionName"`
	RegionEndpoint string `xml:"regionEndpoint"`
	OptInStatus    string `xml:"optInStatus"`
}

// DescribeRegions returns a hardcoded list of AWS regions.
func (h *Handler) DescribeRegions(w http.ResponseWriter, r *http.Request) {
	resp, aerr := h.describeRegions(r.Context(), unselectedQuery(r))
	writeDescribe(w, r, resp, aerr)
}

func (h *Handler) describeRegions(ctx context.Context, q describeQuery) (*xmlDescribeRegionsResponse, *protocol.AWSError) {
	filters, aerr := regionFilters.parse(q.filters)
	if aerr != nil {
		return nil, aerr
	}
	all := []xmlRegion{
		{RegionName: "us-east-1", RegionEndpoint: "ec2.us-east-1.amazonaws.com", OptInStatus: "opt-in-not-required"},
		{RegionName: "us-east-2", RegionEndpoint: "ec2.us-east-2.amazonaws.com", OptInStatus: "opt-in-not-required"},
		{RegionName: "us-west-1", RegionEndpoint: "ec2.us-west-1.amazonaws.com", OptInStatus: "opt-in-not-required"},
		{RegionName: "us-west-2", RegionEndpoint: "ec2.us-west-2.amazonaws.com", OptInStatus: "opt-in-not-required"},
		{RegionName: "eu-west-1", RegionEndpoint: "ec2.eu-west-1.amazonaws.com", OptInStatus: "opt-in-not-required"},
		{RegionName: "eu-central-1", RegionEndpoint: "ec2.eu-central-1.amazonaws.com", OptInStatus: "opt-in-not-required"},
		{RegionName: "ap-southeast-1", RegionEndpoint: "ec2.ap-southeast-1.amazonaws.com", OptInStatus: "opt-in-not-required"},
		{RegionName: "ap-northeast-1", RegionEndpoint: "ec2.ap-northeast-1.amazonaws.com", OptInStatus: "opt-in-not-required"},
	}
	regions := make([]xmlRegion, 0, len(all))
	for _, region := range all {
		if filters.matches(region) {
			regions = append(regions, region)
		}
	}
	return &xmlDescribeRegionsResponse{
		Xmlns:      ec2XMLNS,
		RequestID:  protocol.RequestIDFromContext(ctx),
		RegionInfo: regions,
	}, nil
}

// ── DescribeAvailabilityZones ────────────────────────────────────────────────

type xmlDescribeAZsResponse struct {
	XMLName              xml.Name `xml:"DescribeAvailabilityZonesResponse"`
	Xmlns                string   `xml:"xmlns,attr"`
	RequestID            string   `xml:"requestId"`
	AvailabilityZoneInfo []xmlAZ  `xml:"availabilityZoneInfo>item"`
}

type xmlAZ struct {
	ZoneName   string `xml:"zoneName"`
	ZoneState  string `xml:"zoneState"`
	RegionName string `xml:"regionName"`
}

// DescribeAvailabilityZones returns the AZs for the configured region.
func (h *Handler) DescribeAvailabilityZones(w http.ResponseWriter, r *http.Request) {
	resp, aerr := h.describeAvailabilityZones(r.Context(), unselectedQuery(r))
	writeDescribe(w, r, resp, aerr)
}

func (h *Handler) describeAvailabilityZones(ctx context.Context, q describeQuery) (*xmlDescribeAZsResponse, *protocol.AWSError) {
	filters, aerr := azFilters.parse(q.filters)
	if aerr != nil {
		return nil, aerr
	}
	region := h.cfg.Region
	all := []xmlAZ{
		{ZoneName: region + "a", ZoneState: "available", RegionName: region},
		{ZoneName: region + "b", ZoneState: "available", RegionName: region},
		{ZoneName: region + "c", ZoneState: "available", RegionName: region},
	}
	azs := make([]xmlAZ, 0, len(all))
	for _, az := range all {
		if filters.matches(az) {
			azs = append(azs, az)
		}
	}
	return &xmlDescribeAZsResponse{
		Xmlns:                ec2XMLNS,
		RequestID:            protocol.RequestIDFromContext(ctx),
		AvailabilityZoneInfo: azs,
	}, nil
}

// DescribeInstances is implemented in handler_instances.go.

// ── DescribeInstanceTypes ────────────────────────────────────────────────────

type xmlDescribeInstanceTypesResponse struct {
	XMLName           xml.Name              `xml:"DescribeInstanceTypesResponse"`
	Xmlns             string                `xml:"xmlns,attr"`
	RequestID         string                `xml:"requestId"`
	InstanceTypeItems []xmlInstanceTypeItem `xml:"instanceTypeSet>item"`
}

type xmlInstanceTypeItem struct {
	InstanceType      string        `xml:"instanceType"`
	CurrentGeneration bool          `xml:"currentGeneration"`
	VCpuInfo          xmlVCpuInfo   `xml:"vCpuInfo"`
	MemoryInfo        xmlMemoryInfo `xml:"memoryInfo"`
}

type xmlVCpuInfo struct {
	DefaultVCpus int `xml:"defaultVCpus"`
}

type xmlMemoryInfo struct {
	SizeInMiB int `xml:"sizeInMiB"`
}

// DescribeInstanceTypes returns info for requested instance types.
func (h *Handler) DescribeInstanceTypes(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		protocol.WriteEC2QueryXMLError(w, r, protocol.ErrInvalidArgument("invalid request form encoding"))
		return
	}
	// Every InstanceType.* key counts, not just a contiguous InstanceType.N:
	// this is the one describe that collected its selection by prefix rather
	// than through parseIndexedParam, and narrowing it here would change what
	// the legacy path answers.
	var requested []string
	for k, vals := range r.Form {
		if strings.HasPrefix(k, "InstanceType.") {
			requested = append(requested, vals...)
		}
	}
	resp, aerr := h.describeInstanceTypes(r.Context(), unselectedQuery(r), requested)
	writeDescribe(w, r, resp, aerr)
}

func (h *Handler) describeInstanceTypes(ctx context.Context, q describeQuery, instanceTypes []string) (*xmlDescribeInstanceTypesResponse, *protocol.AWSError) {
	filters, aerr := instanceTypeFilters.parse(q.filters)
	if aerr != nil {
		return nil, aerr
	}
	var items []xmlInstanceTypeItem
	requested := map[string]bool{}
	for _, v := range instanceTypes {
		requested[v] = true
	}
	// If none specified, return empty set.
	if len(requested) == 0 {
		return &xmlDescribeInstanceTypesResponse{
			Xmlns:     ec2XMLNS,
			RequestID: protocol.RequestIDFromContext(ctx),
		}, nil
	}
	// Return info for each requested type.
	typeInfo := map[string]xmlInstanceTypeItem{
		"t3.micro":  {InstanceType: "t3.micro", CurrentGeneration: true, VCpuInfo: xmlVCpuInfo{2}, MemoryInfo: xmlMemoryInfo{1024}},
		"t3.small":  {InstanceType: "t3.small", CurrentGeneration: true, VCpuInfo: xmlVCpuInfo{2}, MemoryInfo: xmlMemoryInfo{2048}},
		"t3.medium": {InstanceType: "t3.medium", CurrentGeneration: true, VCpuInfo: xmlVCpuInfo{2}, MemoryInfo: xmlMemoryInfo{4096}},
		"m5.large":  {InstanceType: "m5.large", CurrentGeneration: true, VCpuInfo: xmlVCpuInfo{2}, MemoryInfo: xmlMemoryInfo{8192}},
		"m5.xlarge": {InstanceType: "m5.xlarge", CurrentGeneration: true, VCpuInfo: xmlVCpuInfo{4}, MemoryInfo: xmlMemoryInfo{16384}},
	}
	for t := range requested {
		info, ok := typeInfo[t]
		if !ok {
			// Return a generic entry for unknown types.
			info = xmlInstanceTypeItem{
				InstanceType:      t,
				CurrentGeneration: false,
				VCpuInfo:          xmlVCpuInfo{1},
				MemoryInfo:        xmlMemoryInfo{512},
			}
		}
		if filters.matches(info) {
			items = append(items, info)
		}
	}
	return &xmlDescribeInstanceTypesResponse{
		Xmlns:             ec2XMLNS,
		RequestID:         protocol.RequestIDFromContext(ctx),
		InstanceTypeItems: items,
	}, nil
}

// ── CreateVpc ────────────────────────────────────────────────────────────────

type xmlCreateVpcResponse struct {
	XMLName   xml.Name `xml:"CreateVpcResponse"`
	Xmlns     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	Vpc       xmlVpc   `xml:"vpc"`
}

type xmlVpc struct {
	VpcID                   string         `xml:"vpcId"`
	State                   string         `xml:"state"`
	CidrBlock               string         `xml:"cidrBlock"`
	DhcpOptionsID           string         `xml:"dhcpOptionsId"`
	InstanceTenancy         string         `xml:"instanceTenancy"`
	IsDefault               bool           `xml:"isDefault"`
	CidrBlockAssociationSet []xmlCidrAssoc `xml:"cidrBlockAssociationSet>item"`
	TagSet                  []xmlTag       `xml:"tagSet>item,omitempty"`
}

type xmlCidrAssoc struct {
	AssociationID  string       `xml:"associationId"`
	CidrBlock      string       `xml:"cidrBlock"`
	CidrBlockState xmlCidrState `xml:"cidrBlockState"`
}

type xmlCidrState struct {
	State string `xml:"state"`
}

// publish emits an event if the bus is wired.
//
// Takes a context rather than an *http.Request so the typed dispatch path —
// which never has one, only ctx — can call the same lifecycle-event seam the
// legacy handlers do. See #754 wave 2: routing a mutation typed must not mean
// its create/delete goes unpublished on the internal event bus.
func (h *Handler) publish(ctx context.Context, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: t, Payload: payload})
	}
}

func newMainRouteTable(vpcID, cidr string) *RouteTable {
	rtID := fmt.Sprintf("rtb-%s", shortID())
	return &RouteTable{
		RouteTableID: rtID,
		VpcID:        vpcID,
		Routes: []Route{{
			DestinationCidrBlock: cidr,
			GatewayID:            "local",
			Origin:               "CreateRouteTable",
		}},
		Associations: []RouteTableAssociation{{
			AssociationID: fmt.Sprintf("rtbassoc-%s", shortID()),
			RouteTableID:  rtID,
			Main:          true,
		}},
	}
}

func (h *Handler) putVPCWithMainRouteTable(ctx context.Context, vpc *VPC) *protocol.AWSError {
	if aerr := h.store.putVPC(ctx, vpc); aerr != nil {
		return aerr
	}
	if aerr := h.store.putRouteTable(ctx, newMainRouteTable(vpc.VpcID, vpc.CidrBlock)); aerr != nil {
		_ = h.store.deleteVPC(ctx, vpc.VpcID)
		h.forgetVPCNetwork(ctx, vpc)
		return aerr
	}
	return nil
}

func (h *Handler) deleteRouteTablesForVPC(ctx context.Context, vpcID string) *protocol.AWSError {
	rts, aerr := h.store.listRouteTables(ctx)
	if aerr != nil {
		return aerr
	}
	for _, rt := range rts {
		if rt.VpcID != vpcID {
			continue
		}
		if aerr := h.store.deleteRouteTable(ctx, rt.RouteTableID); aerr != nil {
			return aerr
		}
	}
	return nil
}

// CreateVpc creates a new VPC.
func (h *Handler) CreateVpc(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("CidrBlock")
	if cidr == "" {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "MissingParameter",
			Message:    "CidrBlock is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	tags := parseTagSpecifications(r, "vpc")
	// Create-time tags are checked before anything is created, as on AWS: a
	// rejected tag must fail the call rather than leave a VPC behind (#1196).
	if aerr := validateTagSpecifications(tags); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	vpcID := fmt.Sprintf("vpc-%s", shortID())
	// Every VPC gets a DHCP options set at creation, real AWS's default-set
	// behavior absent an explicit association. Minted once and persisted on
	// the record so DescribeVpcs echoes the same value back (#1277).
	dhcpOptionsID := fmt.Sprintf("dopt-%s", shortID())
	// Matches real AWS: every new VPC has DNS resolution on; DNS hostnames
	// stay off until ModifyVpcAttribute (or a CDK-issued CFN property) turns
	// them on, except for the seeded default VPC (see seedDefaultVPC).
	vpc := &VPC{VpcID: vpcID, CidrBlock: cidr, State: "available", CreateTime: h.clk.Now().UnixMilli(), EnableDnsSupport: true, EnableDnsHostnames: false, DhcpOptionsId: dhcpOptionsID}

	// Strategy decides whether to create a new Docker network or share one
	// from another VPC with the same CIDR. EnsureNetwork mutates vpc in place.
	if aerr := h.vpcStrategy.EnsureNetwork(r.Context(), vpc); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}

	if aerr := h.putVPCWithMainRouteTable(r.Context(), vpc); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	// Create-time tags go to the tag store, the same place CreateTags writes,
	// so a later describe sees both without reading two sources.
	if aerr := h.putResourceTags(r.Context(), vpcID, tags); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	h.publish(r.Context(), events.EC2VpcCreated, events.ResourcePayload{Name: vpcID})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlCreateVpcResponse{
		Xmlns:     ec2XMLNS,
		RequestID: protocol.RequestIDFromContext(r.Context()),
		Vpc: xmlVpc{
			VpcID:           vpcID,
			State:           "available",
			CidrBlock:       cidr,
			DhcpOptionsID:   dhcpOptionsID,
			InstanceTenancy: "default",
			IsDefault:       false,
			CidrBlockAssociationSet: []xmlCidrAssoc{
				{
					AssociationID:  fmt.Sprintf("vpc-cidr-assoc-%s", shortID()),
					CidrBlock:      cidr,
					CidrBlockState: xmlCidrState{State: "associated"},
				},
			},
			TagSet: xmlTagsOf(tags),
		},
	})
}

// ── DescribeVpcs ─────────────────────────────────────────────────────────────

type xmlDescribeVpcsResponse struct {
	XMLName   xml.Name `xml:"DescribeVpcsResponse"`
	Xmlns     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	VpcSet    []xmlVpc `xml:"vpcSet>item"`
}

// DescribeVpcs returns VPCs, optionally filtered by ID or by isDefault.
//
// The filters are not decoration. CDK's VPC context provider sends them, treats
// the response as already filtered, and fails unless exactly one VPC comes
// back — so an unfiltered response breaks `Vpc.fromLookup` the moment a region
// holds more than one VPC, which seeding a default VPC guarantees.
func (h *Handler) DescribeVpcs(w http.ResponseWriter, r *http.Request) {
	resp, aerr := h.describeVpcs(r.Context(), requestQuery(r, "VpcId"))
	writeDescribe(w, r, resp, aerr)
}

func (h *Handler) describeVpcs(ctx context.Context, q describeQuery) (*xmlDescribeVpcsResponse, *protocol.AWSError) {
	filters, aerr := vpcFilters.parse(q.filters)
	if aerr != nil {
		return nil, aerr
	}
	requested := q.ids

	// Every AWS region has a default VPC. Seeding on first read rather than at
	// startup keeps regions nobody touches free of records.
	h.ensureDefaultVPCQuietly(ctx)

	vpcs, aerr := h.store.listVPCs(ctx)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := resolveIDs(vpcIDScope, requested, vpcs, func(v *VPC) string { return v.VpcID }); aerr != nil {
		return nil, aerr
	}

	tags, aerr := h.tagViewFor(ctx, q.filters, true)
	if aerr != nil {
		return nil, aerr
	}

	items := make([]xmlVpc, 0, len(vpcs))
	for _, v := range vpcs {
		if !requested.has(v.VpcID) || !filters.matches(v) {
			continue
		}
		ns := v.NetworkStatus
		if ns == "" {
			ns = vpcNetworkStatusOK
		}
		// The network-status tag is Overcast's own diagnostic, not the caller's,
		// so it is merged in rather than replacing what they set.
		vpcTags, ok := tags.keepWith(v.VpcID, Tag{Key: vpcNetworkStatusTag, Value: ns})
		if !ok {
			continue
		}
		items = append(items, xmlVpc{
			VpcID:           v.VpcID,
			State:           v.State,
			CidrBlock:       v.CidrBlock,
			DhcpOptionsID:   v.DhcpOptionsId,
			InstanceTenancy: "default",
			IsDefault:       v.IsDefault,
			TagSet:          xmlTagsOf(vpcTags),
		})
	}
	return &xmlDescribeVpcsResponse{
		Xmlns:     ec2XMLNS,
		RequestID: protocol.RequestIDFromContext(ctx),
		VpcSet:    items,
	}, nil
}

// ── DeleteVpc ────────────────────────────────────────────────────────────────

type xmlDeleteVpcResponse struct {
	XMLName   xml.Name `xml:"DeleteVpcResponse"`
	Xmlns     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	Return    bool     `xml:"return"`
}

// DeleteVpc deletes a VPC by ID.
func (h *Handler) DeleteVpc(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "MissingParameter",
			Message:    "VpcId is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	// Fetch VPC before deleting so we can clean up the Docker network.
	vpc, _ := h.store.getVPC(r.Context(), vpcID)
	if vpc != nil {
		if aerr := h.vpcDependencyError(r.Context(), vpcID); aerr != nil {
			protocol.WriteEC2QueryXMLError(w, r, aerr)
			return
		}
	}

	if aerr := h.store.deleteVPC(r.Context(), vpcID); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	if aerr := h.deleteRouteTablesForVPC(r.Context(), vpcID); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}

	// Strategy decides whether to actually tear down the Docker network
	// (shared strategies keep it alive while other VPCs still use it).
	h.forgetVPCNetwork(r.Context(), vpc)

	h.publish(r.Context(), events.EC2VpcDeleted, events.ResourcePayload{Name: vpcID})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDeleteVpcResponse{
		Xmlns:     ec2XMLNS,
		RequestID: protocol.RequestIDFromContext(r.Context()),
		Return:    true,
	})
}

// ── CreateSubnet ─────────────────────────────────────────────────────────────

type xmlCreateSubnetResponse struct {
	XMLName   xml.Name  `xml:"CreateSubnetResponse"`
	Xmlns     string    `xml:"xmlns,attr"`
	RequestID string    `xml:"requestId"`
	Subnet    xmlSubnet `xml:"subnet"`
}

type xmlSubnet struct {
	SubnetID                string        `xml:"subnetId"`
	State                   string        `xml:"state"`
	VpcID                   string        `xml:"vpcId"`
	CidrBlock               string        `xml:"cidrBlock"`
	AvailabilityZone        string        `xml:"availabilityZone"`
	AvailableIPAddressCount int           `xml:"availableIpAddressCount"`
	DefaultForAz            bool          `xml:"defaultForAz"`
	MapPublicIPOnLaunch     bool          `xml:"mapPublicIpOnLaunch"`
	TagSet                  []typedTagXML `xml:"tagSet>item,omitempty"`
}

// CreateSubnet creates a new subnet in a VPC.
func (h *Handler) CreateSubnet(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	cidr := r.FormValue("CidrBlock")
	if vpcID == "" || cidr == "" {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "MissingParameter",
			Message:    "VpcId and CidrBlock are required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	// Validate VPC exists.
	if _, aerr := h.store.getVPC(r.Context(), vpcID); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "InvalidVpcID.NotFound",
			Message:    fmt.Sprintf("The vpc ID '%s' does not exist", vpcID),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	tags := parseTagSpecifications(r, "subnet")
	// Create-time tags are checked before anything is created, as on AWS: a
	// rejected tag must fail the call rather than leave a subnet behind (#1196).
	if aerr := validateTagSpecifications(tags); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	subnetID := fmt.Sprintf("subnet-%s", shortID())
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		az = h.cfg.Region + "a"
	}
	subnet := &Subnet{SubnetID: subnetID, VpcID: vpcID, CidrBlock: cidr, AvailabilityZone: az, State: "available"}
	if aerr := h.store.putSubnet(r.Context(), subnet); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	// Create-time tags go to the tag store, the same place CreateTags writes,
	// so a later describe sees both without reading two sources.
	if aerr := h.putResourceTags(r.Context(), subnetID, tags); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	h.publish(r.Context(), events.EC2SubnetCreated, events.ResourcePayload{Name: subnetID})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlCreateSubnetResponse{
		Xmlns:     ec2XMLNS,
		RequestID: protocol.RequestIDFromContext(r.Context()),
		Subnet: xmlSubnet{
			SubnetID:                subnetID,
			State:                   "available",
			VpcID:                   vpcID,
			CidrBlock:               cidr,
			AvailabilityZone:        az,
			AvailableIPAddressCount: 251,
			DefaultForAz:            false,
			MapPublicIPOnLaunch:     false,
			TagSet:                  typedTagsOf(tags),
		},
	})
}

// ── DeleteSubnet ─────────────────────────────────────────────────────────────

type xmlDeleteSubnetResponse struct {
	XMLName   xml.Name `xml:"DeleteSubnetResponse"`
	Xmlns     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	Return    bool     `xml:"return"`
}

// DeleteSubnet deletes a subnet by ID.
func (h *Handler) DeleteSubnet(w http.ResponseWriter, r *http.Request) {
	subnetID := r.FormValue("SubnetId")
	if subnetID == "" {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "MissingParameter",
			Message:    "SubnetId is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if aerr := h.subnetDependencyError(r.Context(), subnetID); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	if aerr := h.store.deleteSubnet(r.Context(), subnetID); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	h.publish(r.Context(), events.EC2SubnetDeleted, events.ResourcePayload{Name: subnetID})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDeleteSubnetResponse{
		Xmlns:     ec2XMLNS,
		RequestID: protocol.RequestIDFromContext(r.Context()),
		Return:    true,
	})
}

// ── CreateSecurityGroup ──────────────────────────────────────────────────────

// xmlCreateSGResponse is the one create response whose tagSet is not nested
// inside the resource it made: the model gives CreateSecurityGroupResult a
// top-level `tagSet` alongside `groupId`, where every other EC2 create puts
// one inside its `<vpc>`/`<subnet>`/`<internetGateway>` element. Overcast
// omitted it on both dispatch paths until create-time tagging was wired
// through the typed one, so a caller that tagged a group at create had to
// issue a DescribeSecurityGroups to see what it had just set.
type xmlCreateSGResponse struct {
	XMLName   xml.Name      `xml:"CreateSecurityGroupResponse"`
	Xmlns     string        `xml:"xmlns,attr"`
	RequestID string        `xml:"requestId"`
	GroupID   string        `xml:"groupId"`
	TagSet    []typedTagXML `xml:"tagSet>item,omitempty"`
}

// CreateSecurityGroup creates a new VPC security group.
func (h *Handler) CreateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	desc := r.FormValue("GroupDescription")
	vpcID := r.FormValue("VpcId")
	if name == "" {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "MissingParameter",
			Message:    "GroupName is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	tags := parseTagSpecifications(r, "security-group")
	// Create-time tags are checked before anything is created, as on AWS: a
	// rejected tag must fail the call rather than leave a security group
	// behind (#1196).
	if aerr := validateTagSpecifications(tags); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	groupID := fmt.Sprintf("sg-%s", shortID())
	sg := &SecurityGroup{
		GroupID:     groupID,
		GroupName:   name,
		Description: desc,
		VpcID:       vpcID,
		IpPermissionsEgress: []IpPermission{{
			IpProtocol: "-1",
			IpRanges:   []IpRange{{CidrIp: "0.0.0.0/0"}},
		}},
	}
	if aerr := h.store.putSecurityGroup(r.Context(), sg); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	// Create-time tags go to the tag store, the same place CreateTags writes,
	// so a later describe sees both without reading two sources.
	if aerr := h.putResourceTags(r.Context(), groupID, tags); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	h.publish(r.Context(), events.EC2SecurityGroupCreated, events.ResourcePayload{Name: name})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlCreateSGResponse{
		Xmlns:     ec2XMLNS,
		RequestID: protocol.RequestIDFromContext(r.Context()),
		GroupID:   groupID,
		TagSet:    typedTagsOf(tags),
	})
}

// ── DeleteSecurityGroup ──────────────────────────────────────────────────────

type xmlDeleteSGResponse struct {
	XMLName   xml.Name `xml:"DeleteSecurityGroupResponse"`
	Xmlns     string   `xml:"xmlns,attr"`
	RequestID string   `xml:"requestId"`
	Return    bool     `xml:"return"`
}

// DeleteSecurityGroup deletes a security group by ID.
func (h *Handler) DeleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.FormValue("GroupId")
	if groupID == "" {
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "MissingParameter",
			Message:    "GroupId is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	sg, aerr := h.store.getSecurityGroup(r.Context(), groupID)
	if aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	if aerr := h.securityGroupDependencyError(r.Context(), sg); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	if aerr := h.store.deleteSecurityGroup(r.Context(), groupID); aerr != nil {
		protocol.WriteEC2QueryXMLError(w, r, aerr)
		return
	}
	h.publish(r.Context(), events.EC2SecurityGroupDeleted, events.ResourcePayload{Name: groupID})
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDeleteSGResponse{
		Xmlns:     ec2XMLNS,
		RequestID: protocol.RequestIDFromContext(r.Context()),
		Return:    true,
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// shortID generates an 8-character hex short ID from a UUID.
func shortID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
}

// longID generates a 17-character hex ID from a UUID, the length AWS uses for
// the resource types introduced after its move to long IDs. Launch templates
// are the one here: AWS's own InvalidLaunchTemplateId.Malformed message spells
// the shape out as lt-xxxxxxxxxxxxxxxxx, and the short form was never valid.
func longID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:17]
}
