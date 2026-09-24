package rds

// handler_aurora.go implements Aurora DB cluster operations.
// Aurora MySQL and Aurora PostgreSQL are emulated using MySQL and PostgreSQL
// Docker images respectively — both engines use the same wire protocol as
// their open-source counterparts.

import (
	"context"
	"encoding/xml"
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ── Aurora XML types ──────────────────────────────────────────────────────────

type xmlCreateDBClusterResponse struct {
	XMLName          xml.Name                  `xml:"CreateDBClusterResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlCreateDBClusterResult  `xml:"CreateDBClusterResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlCreateDBClusterResult struct {
	DBCluster xmlDBCluster `xml:"DBCluster"`
}

// xmlModifyDBClusterResponse is ModifyDBCluster's own envelope. The typed
// implementation used to answer with a CreateDBClusterResponse element, which
// only went unnoticed because the raw Query path — the one an SDK reached
// through — declared the right one locally. Collapsing the two onto the typed
// implementation makes this the single answer, so it has to be the correct one.
type xmlModifyDBClusterResponse struct {
	XMLName          xml.Name                  `xml:"ModifyDBClusterResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlCreateDBClusterResult  `xml:"ModifyDBClusterResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

// xmlStopDBClusterResponse and xmlStartDBClusterResponse are Stop's and Start's
// own envelopes, for the reason given on xmlModifyDBClusterResponse. Both typed
// implementations used to answer with a CreateDBClusterResponse, and the raw
// Query handlers below declared the right element names in local types that the
// typed path never reached — so reading either handler in isolation showed a
// correct answer while the wire carried CreateDBClusterResponse for a stop.
//
// Nothing caught it because the state change was right: the cluster really did
// go to "stopping", so every assertion on the stored record passed. Only a
// client that parses the envelope sees the fault, and until compat grew an
// rds-clusters group no client did.
type xmlStopDBClusterResponse struct {
	XMLName          xml.Name                  `xml:"StopDBClusterResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlCreateDBClusterResult  `xml:"StopDBClusterResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlStartDBClusterResponse struct {
	XMLName          xml.Name                  `xml:"StartDBClusterResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlCreateDBClusterResult  `xml:"StartDBClusterResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlDeleteDBClusterResponse struct {
	XMLName          xml.Name                  `xml:"DeleteDBClusterResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlDeleteDBClusterResult  `xml:"DeleteDBClusterResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlDeleteDBClusterResult struct {
	DBCluster xmlDBCluster `xml:"DBCluster"`
}

type xmlDescribeDBClustersResponse struct {
	XMLName          xml.Name                    `xml:"DescribeDBClustersResponse"`
	Xmlns            string                      `xml:"xmlns,attr"`
	Result           xmlDescribeDBClustersResult `xml:"DescribeDBClustersResult"`
	ResponseMetadata protocol.ResponseMetadata   `xml:"ResponseMetadata"`
}

type xmlDescribeDBClustersResult struct {
	DBClusters xmlDBClusters `xml:"DBClusters"`
}

type xmlDBClusters struct {
	Items []xmlDBCluster `xml:"DBCluster"`
}

// xmlDBCluster is the DBCluster wire shape. MasterUserPassword is deliberately
// absent — AWS never returns it — while the settings ModifyDBCluster changes
// are all present, because an update that cannot be observed is
// indistinguishable from one that was dropped.
//
// Several of them are spelled differently on the way out than on the way in,
// and the request spellings are the ones CloudFormation sends:
// DBClusterParameterGroupName arrives, DBClusterParameterGroup goes back;
// CloudwatchLogsExportConfiguration.EnableLogTypes arrives,
// EnabledCloudwatchLogsExports goes back; VpcSecurityGroupIds arrives, a list
// of VpcSecurityGroupMembership goes back.
type xmlDBCluster struct {
	DBClusterIdentifier          string               `xml:"DBClusterIdentifier"`
	DBClusterArn                 string               `xml:"DBClusterArn"`
	Engine                       string               `xml:"Engine"`
	EngineVersion                string               `xml:"EngineVersion"`
	Status                       string               `xml:"Status"`
	MasterUsername               string               `xml:"MasterUsername"`
	DatabaseName                 string               `xml:"DatabaseName,omitempty"`
	Port                         int                  `xml:"Port"`
	Endpoint                     string               `xml:"Endpoint,omitempty"`
	ReaderEndpoint               string               `xml:"ReaderEndpoint,omitempty"`
	MultiAZ                      bool                 `xml:"MultiAZ"`
	StorageType                  string               `xml:"StorageType"`
	ClusterCreateTime            string               `xml:"ClusterCreateTime,omitempty"`
	DBSubnetGroup                string               `xml:"DBSubnetGroup,omitempty"`
	BackupRetentionPeriod        int                  `xml:"BackupRetentionPeriod"`
	PreferredBackupWindow        string               `xml:"PreferredBackupWindow,omitempty"`
	PreferredMaintenanceWindow   string               `xml:"PreferredMaintenanceWindow,omitempty"`
	DBClusterParameterGroup      string               `xml:"DBClusterParameterGroup,omitempty"`
	DeletionProtection           bool                 `xml:"DeletionProtection"`
	VpcSecurityGroups            xmlVpcSecurityGroups `xml:"VpcSecurityGroups"`
	EnabledCloudwatchLogsExports xmlLogTypeList       `xml:"EnabledCloudwatchLogsExports"`
	DBClusterMembers             xmlDBClusterMembers  `xml:"DBClusterMembers"`
	// MasterUserSecret mirrors xmlDBInstance's field — see
	// xmlMasterUserSecretFor in handler.go.
	MasterUserSecret *xmlMasterUserSecret `xml:"MasterUserSecret,omitempty"`

	// StorageEncrypted and KmsKeyId are #2133's remaining DBCluster
	// echo-only properties — see xmlDBInstance's identical fields in
	// handler.go for the shared reasoning. StorageEncrypted is one of the
	// "AWS always returns a value" fields; KmsKeyId is present only when set.
	StorageEncrypted bool   `xml:"StorageEncrypted"`
	KmsKeyId         string `xml:"KmsKeyId,omitempty"`
	// HttpEndpointEnabled is BooleanOptional on AWS but, like StorageEncrypted,
	// is rendered unconditionally with its resting default (false) rather
	// than modeled as present-only-when-set: Overcast has no "never asked"
	// state distinct from "explicitly disabled" to preserve, and false is
	// both defaults' answer.
	HttpEndpointEnabled bool `xml:"HttpEndpointEnabled"`
	// ServerlessV2ScalingConfiguration, unlike HttpEndpointEnabled, has no
	// meaningful zero value to fall back to — MinCapacity/MaxCapacity of 0
	// would claim a scaling range no Aurora Serverless v2 cluster can run
	// at — so it stays present-only-when-set.
	ServerlessV2ScalingConfiguration *xmlServerlessV2ScalingConfiguration `xml:"ServerlessV2ScalingConfiguration,omitempty"`
}

// xmlServerlessV2ScalingConfiguration is AWS's
// ServerlessV2ScalingConfigurationInfo output shape.
type xmlServerlessV2ScalingConfiguration struct {
	MinCapacity           float64 `xml:"MinCapacity,omitempty"`
	MaxCapacity           float64 `xml:"MaxCapacity,omitempty"`
	SecondsUntilAutoPause int     `xml:"SecondsUntilAutoPause,omitempty"`
}

// xmlServerlessV2ScalingConfigurationFor builds the wire element for a
// cluster's recorded scaling configuration, or nil when none was ever set —
// the same present-only-when-relevant shape xmlMasterUserSecretFor uses.
func xmlServerlessV2ScalingConfigurationFor(cfg *ServerlessV2ScalingConfig) *xmlServerlessV2ScalingConfiguration {
	if cfg == nil {
		return nil
	}
	return &xmlServerlessV2ScalingConfiguration{
		MinCapacity:           cfg.MinCapacity,
		MaxCapacity:           cfg.MaxCapacity,
		SecondsUntilAutoPause: cfg.SecondsUntilAutoPause,
	}
}

// xmlVpcSecurityGroups is AWS's VpcSecurityGroupMembership list. The Status
// field is part of the shape; Overcast does not model attachment progress, so
// a recorded membership is reported as "active".
type xmlVpcSecurityGroups struct {
	Items []xmlVpcSecurityGroupMembership `xml:"VpcSecurityGroupMembership"`
}

type xmlVpcSecurityGroupMembership struct {
	VpcSecurityGroupId string `xml:"VpcSecurityGroupId"`
	Status             string `xml:"Status"`
}

type xmlLogTypeList struct {
	Items []string `xml:"member"`
}

type xmlDBClusterMembers struct {
	Items []xmlDBClusterMember `xml:"DBClusterMember"`
}

type xmlDBClusterMember struct {
	DBInstanceIdentifier          string `xml:"DBInstanceIdentifier"`
	IsClusterWriter               bool   `xml:"IsClusterWriter"`
	DBClusterParameterGroupStatus string `xml:"DBClusterParameterGroupStatus"`
	PromotionTier                 int    `xml:"PromotionTier"`
}

// ── CreateDBCluster ───────────────────────────────────────────────────────────

// CreateDBCluster creates a new Aurora DB cluster. Only aurora-mysql and
// aurora-postgresql engines are accepted. The cluster is a logical resource —
// Docker containers are started when instances are added via CreateDBInstance.
//
// An adapter, for the reason given on ModifyDBCluster: this was a second full
// implementation, and it read none of the settings the typed one accepts, so a
// create arriving on this path would have discarded every one of them —
// including the master password the cluster has to remember in order to know,
// later, whether a rotation is really a change.
func (h *Handler) CreateDBCluster(w http.ResponseWriter, r *http.Request) {
	h.invokeTypedAsQuery("CreateDBCluster", w, r)
}

// ── DescribeDBClusters ────────────────────────────────────────────────────────

// DescribeDBClusters returns Aurora DB clusters, optionally filtered by identifier.
func (h *Handler) DescribeDBClusters(w http.ResponseWriter, r *http.Request) {
	h.invokeTypedAsQuery("DescribeDBClusters", w, r)
}

// ── DeleteDBCluster ───────────────────────────────────────────────────────────

// DeleteDBCluster deletes an Aurora DB cluster. The cluster is marked "deleting"
// immediately and removed asynchronously. Member instances are not automatically
// deleted — callers should delete instances first (matching AWS behaviour).
// An adapter, and necessarily one: the typed implementation refuses a cluster
// with DeletionProtection enabled, and a second implementation that did not
// would make the protection bypassable by choosing a dispatch path.
func (h *Handler) DeleteDBCluster(w http.ResponseWriter, r *http.Request) {
	h.invokeTypedAsQuery("DeleteDBCluster", w, r)
}

// ── ModifyDBCluster ───────────────────────────────────────────────────────────

// ModifyDBCluster updates settings on an Aurora DB cluster.
//
// This was a second full implementation, reachable whenever
// Service.DispatchQuery found no codec in context and fell back to h.dispatch.
// It read exactly one parameter, as the typed one did, so the two agreed by
// accident rather than by construction — and the moment the typed one learned
// to apply the rest of ModifyDBCluster's parameters they would have disagreed
// about every one of them. It is an adapter now, for the same reason the
// instance operations became adapters; the behaviour lives once, in
// typed_logic.go.
func (h *Handler) ModifyDBCluster(w http.ResponseWriter, r *http.Request) {
	h.invokeTypedAsQuery("ModifyDBCluster", w, r)
}

// ── StartDBCluster / StopDBCluster ────────────────────────────────────────────

// StartDBCluster starts a stopped Aurora DB cluster.
//
// An adapter, for the reason given on the other four: this was a second full
// implementation, and the two disagreed about the one thing neither could see.
// The typed one answered in a CreateDBClusterResponse envelope; this one built
// the right envelope from a locally declared type, so whichever you read looked
// correct and only the dispatch path decided which an SDK got.
func (h *Handler) StartDBCluster(w http.ResponseWriter, r *http.Request) {
	h.invokeTypedAsQuery("StartDBCluster", w, r)
}

// StopDBCluster stops a running Aurora DB cluster. An adapter, for the reason
// given on StartDBCluster.
func (h *Handler) StopDBCluster(w http.ResponseWriter, r *http.Request) {
	h.invokeTypedAsQuery("StopDBCluster", w, r)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// toXMLDBCluster converts a stored DBCluster to the XML response type, with the
// cluster endpoints re-minted for this caller — see endpoint.go.
func (h *Handler) toXMLDBCluster(ctx context.Context, c *DBCluster) xmlDBCluster {
	writer, reader, port := h.clusterEndpointsFor(ctx, c)
	members := make([]xmlDBClusterMember, 0, len(c.DBClusterMembers))
	for _, m := range c.DBClusterMembers {
		members = append(members, xmlDBClusterMember(m))
	}
	groups := make([]xmlVpcSecurityGroupMembership, 0, len(c.VpcSecurityGroupIds))
	for _, id := range c.VpcSecurityGroupIds {
		groups = append(groups, xmlVpcSecurityGroupMembership{VpcSecurityGroupId: id, Status: "active"})
	}
	return xmlDBCluster{
		DBClusterIdentifier:          c.DBClusterIdentifier,
		DBClusterArn:                 c.DBClusterArn,
		Engine:                       c.Engine,
		EngineVersion:                c.EngineVersion,
		Status:                       c.Status,
		MasterUsername:               c.MasterUsername,
		DatabaseName:                 c.DatabaseName,
		Port:                         port,
		Endpoint:                     writer,
		ReaderEndpoint:               reader,
		MultiAZ:                      c.MultiAZ,
		StorageType:                  c.StorageType,
		ClusterCreateTime:            c.ClusterCreateTime,
		DBSubnetGroup:                c.DBSubnetGroupName,
		BackupRetentionPeriod:        c.BackupRetentionPeriod,
		PreferredBackupWindow:        c.PreferredBackupWindow,
		PreferredMaintenanceWindow:   c.PreferredMaintenanceWindow,
		DBClusterParameterGroup:      c.DBClusterParameterGroup,
		DeletionProtection:           c.DeletionProtection,
		VpcSecurityGroups:            xmlVpcSecurityGroups{Items: groups},
		EnabledCloudwatchLogsExports: xmlLogTypeList{Items: c.EnabledCloudwatchLogsExports},
		DBClusterMembers:             xmlDBClusterMembers{Items: members},
		MasterUserSecret:             xmlMasterUserSecretFor(c.MasterUserSecretARN, c.MasterUserSecretKmsKeyId),

		StorageEncrypted:                 c.StorageEncrypted,
		KmsKeyId:                         c.KmsKeyId,
		HttpEndpointEnabled:              c.HttpEndpointEnabled,
		ServerlessV2ScalingConfiguration: xmlServerlessV2ScalingConfigurationFor(c.ServerlessV2ScalingConfiguration),
	}
}
