package rds

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/lifecycle"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

const rdsXMLNS = "http://rds.amazonaws.com/doc/2014-10-31/"

// Handler handles RDS Query-protocol requests.
type Handler struct {
	cfg               *config.Config
	store             *rdsStore
	log               *serviceutil.ServiceLogger
	clk               clock.Clock
	bus               *events.Bus
	scheduler         *lifecycle.Scheduler
	docker            *docker.Client
	dockerReady       atomic.Bool
	shuttingDown      atomic.Bool
	dockerLifecycleMu sync.Mutex
	dockerWg          sync.WaitGroup
	puller            *docker.ImagePuller
	vpcResolver       VPCNetworkResolver
	// secretsManager is nil until Service.SetSecretsManager wires it, which
	// the router does unconditionally at startup (see EC2/VPC's
	// SetVPCResolver for the same pattern). ManageMasterUserPassword refuses
	// rather than silently skipping secret creation when it is absent — see
	// errManagedPasswordUnavailable in managed_secret.go.
	secretsManager SecretsManagerAccess
	gc             *docker.GC
	ops            map[string]http.HandlerFunc
	typedOp        map[string]op.Operation

	// instances scopes container sweeps to the containers this instance
	// created. Load-bearing for RDS beyond tidiness: an engine container holds
	// the database in its writable layer, so a sweep that misjudges ownership
	// destroys another Overcast's data. See docker.LabelInstance.
	instances *serviceutil.InstanceDomain

	// One writer at a time per record — see locks.go.
	instanceLocks serviceutil.RecordLocks
	clusterLocks  serviceutil.RecordLocks
}

// VPCNetworkResolver resolves DB subnet groups back to EC2 VPC network state.
type VPCNetworkResolver interface {
	VpcIDForSubnet(ctx context.Context, subnetID string) string
	VPCNetworkStatus(ctx context.Context, vpcID string) string
	DockerNetworkForVpc(ctx context.Context, vpcID string) string
}

func newHandler(cfg *config.Config, store *rdsStore, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{
		cfg:       cfg,
		store:     store,
		log:       log,
		clk:       clk,
		scheduler: lifecycle.NewScheduler(clk),
		instances: serviceutil.NewAnchoredInstanceDomain(store.store, nsInstance, serviceutil.DataDirAnchor(cfg.DataDir)),
	}
	h.initOps()
	return h
}

func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		"CreateDBInstance":                   h.CreateDBInstance,
		"DescribeDBInstances":                h.DescribeDBInstances,
		"DeleteDBInstance":                   h.DeleteDBInstance,
		"DescribeDBEngineVersions":           h.DescribeDBEngineVersions,
		"StopDBInstance":                     h.StopDBInstance,
		"StartDBInstance":                    h.StartDBInstance,
		"ModifyDBInstance":                   h.ModifyDBInstance,
		"DescribeEvents":                     h.DescribeEvents,
		"CreateDBSubnetGroup":                h.CreateDBSubnetGroup,
		"DeleteDBSubnetGroup":                h.DeleteDBSubnetGroup,
		"DescribeDBSubnetGroups":             h.DescribeDBSubnetGroups,
		"CreateDBParameterGroup":             h.CreateDBParameterGroup,
		"DeleteDBParameterGroup":             h.DeleteDBParameterGroup,
		"DescribeDBParameterGroups":          h.DescribeDBParameterGroups,
		"DescribeOrderableDBInstanceOptions": h.DescribeOrderableDBInstanceOptions,
		"CreateDBCluster":                    h.CreateDBCluster,
		"DeleteDBCluster":                    h.DeleteDBCluster,
		"DescribeDBClusters":                 h.DescribeDBClusters,
		"ModifyDBCluster":                    h.ModifyDBCluster,
		"StartDBCluster":                     h.StartDBCluster,
		"StopDBCluster":                      h.StopDBCluster,
		"CreateDBSnapshot":                   h.CreateDBSnapshot,
		"DeleteDBSnapshot":                   h.DeleteDBSnapshot,
		"DescribeDBSnapshots":                h.DescribeDBSnapshots,
		"RestoreDBInstanceFromDBSnapshot":    h.RestoreDBInstanceFromDBSnapshot,
		"CreateDBClusterSnapshot":            h.CreateDBClusterSnapshot,
		"DeleteDBClusterSnapshot":            h.DeleteDBClusterSnapshot,
		"DescribeDBClusterSnapshots":         h.DescribeDBClusterSnapshots,
		"RebootDBInstance":                   h.RebootDBInstance,
		"DescribeDBLogFiles":                 h.DescribeDBLogFiles,
		"DownloadDBLogFilePortion":           h.DownloadDBLogFilePortion,
		"AddTagsToResource":                  h.AddTagsToResource,
		"RemoveTagsFromResource":             h.RemoveTagsFromResource,
		"ListTagsForResource":                h.ListTagsForResource,
	}
	h.typedOp = h.typedOps()
}

func (h *Handler) ownsAction(action string) bool {
	_, ok := h.ops[action]
	return ok
}

func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("Action")
	if fn, ok := h.ops[action]; ok {
		fn(w, r)
		return
	}
	protocol.NotImplementedQueryXML(w, r)
}

// invokeTypedAsQuery runs a typed operation on the raw Query dispatch path.
//
// Service.DispatchQuery prefers the typed operation whenever a codec is in
// context and otherwise falls back to h.dispatch, so every operation reachable
// both ways needs one implementation, not two. StopDBInstance, StartDBInstance
// and ModifyDBInstance had two, and they had already drifted: only the raw pair
// honoured `MultiAZ=false`. Registering the raw entry as this adapter keeps
// ownsAction working — the router asks h.ops what RDS claims — while leaving
// exactly one place where the behaviour lives.
func (h *Handler) invokeTypedAsQuery(action string, w http.ResponseWriter, r *http.Request) {
	typed, ok := h.typedOp[action]
	if !ok {
		protocol.NotImplementedQueryXML(w, r)
		return
	}
	typed.Invoke(w, r, codec.QueryXML)
}

// publish emits an event if the bus is wired.
func (h *Handler) publish(r *http.Request, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{Type: t, Payload: payload})
	}
}

// ── Engine defaults ──────────────────────────────────────────────────────────

var defaultEngineVersions = map[string]string{
	"mysql":             "8.0",
	"postgres":          "16.1",
	"mariadb":           "11.4",
	"aurora-mysql":      "3.04",
	"aurora-postgresql": "15.4",
}

var defaultPorts = map[string]int{
	"mysql":             3306,
	"postgres":          5432,
	"mariadb":           3306,
	"aurora-mysql":      3306,
	"aurora-postgresql": 5432,
}

var supportedEngines = map[string]bool{
	"mysql":             true,
	"postgres":          true,
	"mariadb":           true,
	"aurora-mysql":      true,
	"aurora-postgresql": true,
}

// auroraEngines lists engines that require the cluster/instance resource model.
var auroraEngines = map[string]bool{
	"aurora-mysql":      true,
	"aurora-postgresql": true,
}

// engineImages maps engine → version → Docker image tag. The keys are the
// versions DescribeDBEngineVersions advertises; resolveEngineImage matches
// anything else against them, so a real EngineVersion still starts a container.
var engineImages = map[string]map[string]string{
	"mysql": {
		"8.0": "mysql:8.0",
		"8.4": "mysql:8.4",
		"5.7": "mysql:5.7",
	},
	"postgres": {
		"16.1":  "postgres:16",
		"15.5":  "postgres:15",
		"14.11": "postgres:14",
	},
	"mariadb": {
		"11.4":  "mariadb:11",
		"10.11": "mariadb:10.11",
	},
	// Aurora variants use the same Docker images as their underlying engines.
	// aurora-mysql is MySQL-wire-compatible; aurora-postgresql is PostgreSQL-wire-compatible.
	"aurora-mysql": {
		"3.04": "mysql:8.0",
		"4.0":  "mysql:8.4",
		"2.11": "mysql:5.7",
	},
	"aurora-postgresql": {
		"15.4":  "postgres:15",
		"14.11": "postgres:14",
	},
}

// engineEnv describes engine-specific Docker environment variables and ports.
type engineEnv struct {
	PasswordVar   string
	DatabaseVar   string
	UserVar       string // optional: sets the bootstrap superuser name (e.g. POSTGRES_USER)
	ContainerPort int
}

var engineEnvConfig = map[string]engineEnv{
	"mysql":             {PasswordVar: "MYSQL_ROOT_PASSWORD", DatabaseVar: "MYSQL_DATABASE", ContainerPort: 3306},
	"postgres":          {PasswordVar: "POSTGRES_PASSWORD", DatabaseVar: "POSTGRES_DB", UserVar: "POSTGRES_USER", ContainerPort: 5432},
	"mariadb":           {PasswordVar: "MARIADB_ROOT_PASSWORD", DatabaseVar: "MARIADB_DATABASE", ContainerPort: 3306},
	"aurora-mysql":      {PasswordVar: "MYSQL_ROOT_PASSWORD", DatabaseVar: "MYSQL_DATABASE", ContainerPort: 3306},
	"aurora-postgresql": {PasswordVar: "POSTGRES_PASSWORD", DatabaseVar: "POSTGRES_DB", UserVar: "POSTGRES_USER", ContainerPort: 5432},
}

// ── XML response types ───────────────────────────────────────────────────────

type xmlCreateDBInstanceResponse struct {
	XMLName          xml.Name                  `xml:"CreateDBInstanceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlCreateDBInstanceResult `xml:"CreateDBInstanceResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlCreateDBInstanceResult struct {
	DBInstance xmlDBInstance `xml:"DBInstance"`
}

type xmlDeleteDBInstanceResponse struct {
	XMLName          xml.Name                  `xml:"DeleteDBInstanceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlDeleteDBInstanceResult `xml:"DeleteDBInstanceResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlDeleteDBInstanceResult struct {
	DBInstance xmlDBInstance `xml:"DBInstance"`
}

type xmlDescribeDBInstancesResponse struct {
	XMLName          xml.Name                     `xml:"DescribeDBInstancesResponse"`
	Xmlns            string                       `xml:"xmlns,attr"`
	Result           xmlDescribeDBInstancesResult `xml:"DescribeDBInstancesResult"`
	ResponseMetadata protocol.ResponseMetadata    `xml:"ResponseMetadata"`
}

type xmlDescribeDBInstancesResult struct {
	DBInstances xmlDBInstances `xml:"DBInstances"`
}

type xmlDBInstances struct {
	Items []xmlDBInstance `xml:"DBInstance"`
}

type xmlDBInstance struct {
	DBInstanceIdentifier string      `xml:"DBInstanceIdentifier"`
	DBInstanceClass      string      `xml:"DBInstanceClass"`
	Engine               string      `xml:"Engine"`
	EngineVersion        string      `xml:"EngineVersion"`
	DBInstanceStatus     string      `xml:"DBInstanceStatus"`
	MasterUsername       string      `xml:"MasterUsername"`
	DBName               string      `xml:"DBName,omitempty"`
	Endpoint             xmlEndpoint `xml:"Endpoint"`
	AllocatedStorage     int         `xml:"AllocatedStorage"`
	DBInstanceArn        string      `xml:"DBInstanceArn"`
	InstanceCreateTime   string      `xml:"InstanceCreateTime,omitempty"`
	MultiAZ              bool        `xml:"MultiAZ"`
	// Always emitted, never omitted: an SDK reads DBInstance.PubliclyAccessible
	// as a boolean, and an absent element leaves it nil — "unknown" where the
	// instance in fact has an answer.
	PubliclyAccessible  bool   `xml:"PubliclyAccessible"`
	StorageType         string `xml:"StorageType"`
	DBClusterIdentifier string `xml:"DBClusterIdentifier,omitempty"`
	// StorageOperationStatus and StorageOperationPercentProgress report an
	// in-progress storage operation — PITR/snapshot restore, read-replica
	// creation, blue/green deployment, Single-AZ->Multi-AZ conversion, or
	// storage scaling. AWS documents both as present "only while a storage
	// operation is in progress" and absent otherwise
	// (rds-2014-10-31.json#DBInstance). Overcast performs none of these as
	// asynchronous storage operations, so these are never set: the omitted
	// wire shape below is AWS's own resting value, not a gap. See
	// docs/dev/compatibility/services/rds.yaml for the tracked scenario.
	StorageOperationStatus          *string `xml:"StorageOperationStatus,omitempty"`
	StorageOperationPercentProgress *int    `xml:"StorageOperationPercentProgress,omitempty"`
	// MasterUserSecret is present only when ManageMasterUserPassword is set —
	// a pointer because encoding/xml's omitempty does not treat a plain
	// struct as ever "empty", so a value type would emit an always-present
	// empty element. See xmlMasterUserSecretFor.
	MasterUserSecret *xmlMasterUserSecret `xml:"MasterUserSecret,omitempty"`

	// The fields below are #2133's echo-only properties. The ones AWS
	// documents as always returning a value (never omitted, regardless of
	// whether the caller ever set them) are plain, unconditional fields, the
	// same way PubliclyAccessible above is; the rest — AWS's own "present
	// only when relevant" fields — carry `omitempty`.
	StorageEncrypted                 bool   `xml:"StorageEncrypted"`
	KmsKeyId                         string `xml:"KmsKeyId,omitempty"`
	DeletionProtection               bool   `xml:"DeletionProtection"`
	BackupRetentionPeriod            int    `xml:"BackupRetentionPeriod"`
	PreferredBackupWindow            string `xml:"PreferredBackupWindow,omitempty"`
	PreferredMaintenanceWindow       string `xml:"PreferredMaintenanceWindow,omitempty"`
	AutoMinorVersionUpgrade          bool   `xml:"AutoMinorVersionUpgrade"`
	Iops                             int    `xml:"Iops,omitempty"`
	AvailabilityZone                 string `xml:"AvailabilityZone,omitempty"`
	IAMDatabaseAuthenticationEnabled bool   `xml:"IAMDatabaseAuthenticationEnabled"`
	CACertificateIdentifier          string `xml:"CACertificateIdentifier,omitempty"`
	// CertificateDetails mirrors CACertificateIdentifier — see
	// xmlCertificateDetailsFor. Overcast tracks no certificate validity
	// window, so ValidTill is never set.
	CertificateDetails           *xmlCertificateDetails `xml:"CertificateDetails,omitempty"`
	MonitoringInterval           int                    `xml:"MonitoringInterval"`
	PerformanceInsightsEnabled   bool                   `xml:"PerformanceInsightsEnabled"`
	EnabledCloudwatchLogsExports xmlLogTypeList         `xml:"EnabledCloudwatchLogsExports"`
	CopyTagsToSnapshot           bool                   `xml:"CopyTagsToSnapshot"`
}

// xmlCertificateDetails is AWS's CertificateDetails shape
// (rds-2014-10-31.json#CertificateDetails: CAIdentifier, ValidTill).
type xmlCertificateDetails struct {
	CAIdentifier string `xml:"CAIdentifier,omitempty"`
}

// xmlCertificateDetailsFor builds the CertificateDetails element AWS returns
// alongside CACertificateIdentifier, or nil when no CA was named — the same
// present-only-when-relevant shape xmlMasterUserSecretFor uses.
func xmlCertificateDetailsFor(caIdentifier string) *xmlCertificateDetails {
	if caIdentifier == "" {
		return nil
	}
	return &xmlCertificateDetails{CAIdentifier: caIdentifier}
}

// xmlMasterUserSecret is AWS's MasterUserSecret shape
// (rds-2014-10-31.json#MasterUserSecret: KmsKeyId, SecretArn, SecretStatus).
// SecretStatus is always "active": Overcast performs no rotation, so a
// managed secret is never anything else.
type xmlMasterUserSecret struct {
	KmsKeyId     string `xml:"KmsKeyId,omitempty"`
	SecretArn    string `xml:"SecretArn"`
	SecretStatus string `xml:"SecretStatus"`
}

// xmlMasterUserSecretFor builds the MasterUserSecret element for a record
// whose password is managed, or returns nil for one whose is not — the
// pointer that keeps the element off the wire entirely when there is nothing
// to report, exactly as AWS omits it.
func xmlMasterUserSecretFor(secretARN, kmsKeyID string) *xmlMasterUserSecret {
	if secretARN == "" {
		return nil
	}
	return &xmlMasterUserSecret{KmsKeyId: kmsKeyID, SecretArn: secretARN, SecretStatus: "active"}
}

type xmlEndpoint struct {
	Address string `xml:"Address"`
	Port    int    `xml:"Port"`
}

type xmlDescribeDBEngineVersionsResponse struct {
	XMLName          xml.Name                          `xml:"DescribeDBEngineVersionsResponse"`
	Xmlns            string                            `xml:"xmlns,attr"`
	Result           xmlDescribeDBEngineVersionsResult `xml:"DescribeDBEngineVersionsResult"`
	ResponseMetadata protocol.ResponseMetadata         `xml:"ResponseMetadata"`
}

type xmlDescribeDBEngineVersionsResult struct {
	DBEngineVersions xmlDBEngineVersions `xml:"DBEngineVersions"`
}

type xmlDBEngineVersions struct {
	Items []xmlDBEngineVersion `xml:"DBEngineVersion"`
}

type xmlDBEngineVersion struct {
	Engine                     string `xml:"Engine"`
	EngineVersion              string `xml:"EngineVersion"`
	DBParameterGroupFamily     string `xml:"DBParameterGroupFamily"`
	DBEngineDescription        string `xml:"DBEngineDescription"`
	DBEngineVersionDescription string `xml:"DBEngineVersionDescription"`
}

// ── New XML response types ───────────────────────────────────────────────────

type xmlStopDBInstanceResponse struct {
	XMLName          xml.Name                  `xml:"StopDBInstanceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlStopDBInstanceResult   `xml:"StopDBInstanceResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlStopDBInstanceResult struct {
	DBInstance xmlDBInstance `xml:"DBInstance"`
}

type xmlStartDBInstanceResponse struct {
	XMLName          xml.Name                  `xml:"StartDBInstanceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlStartDBInstanceResult  `xml:"StartDBInstanceResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlStartDBInstanceResult struct {
	DBInstance xmlDBInstance `xml:"DBInstance"`
}

type xmlModifyDBInstanceResponse struct {
	XMLName          xml.Name                  `xml:"ModifyDBInstanceResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	Result           xmlModifyDBInstanceResult `xml:"ModifyDBInstanceResult"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlModifyDBInstanceResult struct {
	DBInstance xmlDBInstance `xml:"DBInstance"`
}

type xmlCreateDBSubnetGroupResponse struct {
	XMLName          xml.Name                     `xml:"CreateDBSubnetGroupResponse"`
	Xmlns            string                       `xml:"xmlns,attr"`
	Result           xmlCreateDBSubnetGroupResult `xml:"CreateDBSubnetGroupResult"`
	ResponseMetadata protocol.ResponseMetadata    `xml:"ResponseMetadata"`
}

type xmlCreateDBSubnetGroupResult struct {
	DBSubnetGroup xmlDBSubnetGroup `xml:"DBSubnetGroup"`
}

type xmlDeleteDBSubnetGroupResponse struct {
	XMLName          xml.Name                  `xml:"DeleteDBSubnetGroupResponse"`
	Xmlns            string                    `xml:"xmlns,attr"`
	ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
}

type xmlDescribeDBSubnetGroupsResponse struct {
	XMLName          xml.Name                        `xml:"DescribeDBSubnetGroupsResponse"`
	Xmlns            string                          `xml:"xmlns,attr"`
	Result           xmlDescribeDBSubnetGroupsResult `xml:"DescribeDBSubnetGroupsResult"`
	ResponseMetadata protocol.ResponseMetadata       `xml:"ResponseMetadata"`
}

type xmlDescribeDBSubnetGroupsResult struct {
	DBSubnetGroups xmlDBSubnetGroups `xml:"DBSubnetGroups"`
}

type xmlDBSubnetGroups struct {
	Items []xmlDBSubnetGroup `xml:"DBSubnetGroup"`
}

type xmlDBSubnetGroup struct {
	DBSubnetGroupName        string     `xml:"DBSubnetGroupName"`
	DBSubnetGroupDescription string     `xml:"DBSubnetGroupDescription"`
	DBSubnetGroupArn         string     `xml:"DBSubnetGroupArn"`
	VpcId                    string     `xml:"VpcId"`
	SubnetIds                xmlSubnets `xml:"Subnets"`
	Status                   string     `xml:"SubnetGroupStatus"`
}

type xmlSubnets struct {
	Items []xmlSubnet `xml:"Subnet"`
}

type xmlSubnet struct {
	SubnetIdentifier string `xml:"SubnetIdentifier"`
}

// ── DescribeDBInstances ──────────────────────────────────────────────────────

// DescribeDBInstances returns DB instances, optionally filtered by identifier.
func (h *Handler) DescribeDBInstances(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("DBInstanceIdentifier")

	if filterID != "" {
		inst, aerr := h.store.getDBInstance(r.Context(), filterID)
		if aerr != nil {
			protocol.WriteQueryXMLError(w, r, aerr)
			return
		}
		docker.SetBackingHeaders(w, h.dockerReady.Load(), docker.ContainerHealthUnknown)
		protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeDBInstancesResponse{
			Xmlns: rdsXMLNS,
			Result: xmlDescribeDBInstancesResult{
				DBInstances: xmlDBInstances{Items: []xmlDBInstance{h.toXMLDBInstance(r.Context(), inst)}},
			},
			ResponseMetadata: protocol.QueryResponseMetadata(r),
		})
		return
	}

	all, aerr := h.store.listDBInstances(r.Context())
	if aerr != nil {
		protocol.WriteQueryXMLError(w, r, aerr)
		return
	}

	items := make([]xmlDBInstance, 0, len(all))
	for _, inst := range all {
		items = append(items, h.toXMLDBInstance(r.Context(), inst))
	}

	docker.SetBackingHeaders(w, h.dockerReady.Load(), docker.ContainerHealthUnknown)
	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeDBInstancesResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeDBInstancesResult{
			DBInstances: xmlDBInstances{Items: items},
		},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── DescribeDBEngineVersions ─────────────────────────────────────────────────

type engineVersionEntry struct {
	Engine                     string
	EngineVersion              string
	DBParameterGroupFamily     string
	DBEngineDescription        string
	DBEngineVersionDescription string
}

var allEngineVersions = []engineVersionEntry{
	{"mysql", "8.4", "mysql8.4", "MySQL Community Edition", "MySQL 8.4"},
	{"mysql", "8.0", "mysql8.0", "MySQL Community Edition", "MySQL 8.0"},
	{"mysql", "5.7", "mysql5.7", "MySQL Community Edition", "MySQL 5.7"},
	{"postgres", "16.1", "postgres16", "PostgreSQL", "PostgreSQL 16.1"},
	{"postgres", "15.5", "postgres15", "PostgreSQL", "PostgreSQL 15.5"},
	{"postgres", "14.11", "postgres14", "PostgreSQL", "PostgreSQL 14.11"},
	{"mariadb", "11.4", "mariadb11.4", "MariaDB Community Edition", "MariaDB 11.4"},
	{"mariadb", "10.11", "mariadb10.11", "MariaDB Community Edition", "MariaDB 10.11"},
	{"aurora-mysql", "4.0", "aurora-mysql8.4", "Aurora MySQL", "Aurora MySQL 4.0"},
	{"aurora-mysql", "3.04", "aurora-mysql8.0", "Aurora MySQL", "Aurora MySQL 3.04"},
	{"aurora-mysql", "2.11", "aurora-mysql5.7", "Aurora MySQL", "Aurora MySQL 2.11"},
	{"aurora-postgresql", "15.4", "aurora-postgresql15", "Aurora PostgreSQL", "Aurora PostgreSQL 15.4"},
	{"aurora-postgresql", "14.11", "aurora-postgresql14", "Aurora PostgreSQL", "Aurora PostgreSQL 14.11"},
}

// DescribeDBEngineVersions returns the supported engine versions.
func (h *Handler) DescribeDBEngineVersions(w http.ResponseWriter, r *http.Request) {
	filterEngine := r.FormValue("Engine")

	items := make([]xmlDBEngineVersion, 0, len(allEngineVersions))
	for _, ev := range allEngineVersions {
		if filterEngine != "" && ev.Engine != filterEngine {
			continue
		}
		items = append(items, xmlDBEngineVersion(ev))
	}

	protocol.WriteQueryXML(w, r, http.StatusOK, &xmlDescribeDBEngineVersionsResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeDBEngineVersionsResult{
			DBEngineVersions: xmlDBEngineVersions{Items: items},
		},
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// toXMLDBInstance converts a stored DBInstance to the XML response type.
// MasterUserPassword is intentionally omitted (AWS never returns it).
//
// The endpoint is re-minted for this caller rather than replayed from the
// record: the stored form is canonical, and which name and port are dialable
// depends on who is asking. Every response path goes through here, so the two
// dispatch paths (Query XML and typed) cannot disagree.
func (h *Handler) toXMLDBInstance(ctx context.Context, inst *DBInstance) xmlDBInstance {
	var ep xmlEndpoint
	if inst.Endpoint != nil {
		address, port := h.instanceEndpointFor(ctx, inst)
		ep = xmlEndpoint{Address: address, Port: port}
	}
	return xmlDBInstance{
		DBInstanceIdentifier: inst.DBInstanceIdentifier,
		DBInstanceClass:      inst.DBInstanceClass,
		Engine:               inst.Engine,
		EngineVersion:        inst.EngineVersion,
		DBInstanceStatus:     inst.DBInstanceStatus,
		MasterUsername:       inst.MasterUsername,
		DBName:               inst.DBName,
		Endpoint:             ep,
		AllocatedStorage:     inst.AllocatedStorage,
		DBInstanceArn:        inst.DBInstanceArn,
		InstanceCreateTime:   inst.InstanceCreateTime,
		MultiAZ:              inst.MultiAZ,
		PubliclyAccessible:   inst.PubliclyAccessibleOrDefault(),
		StorageType:          inst.StorageType,
		DBClusterIdentifier:  inst.DBClusterIdentifier,
		MasterUserSecret:     xmlMasterUserSecretFor(inst.MasterUserSecretARN, inst.MasterUserSecretKmsKeyId),

		StorageEncrypted:                 inst.StorageEncrypted,
		KmsKeyId:                         inst.KmsKeyId,
		DeletionProtection:               inst.DeletionProtection,
		BackupRetentionPeriod:            inst.BackupRetentionPeriodOrDefault(),
		PreferredBackupWindow:            inst.PreferredBackupWindow,
		PreferredMaintenanceWindow:       inst.PreferredMaintenanceWindow,
		AutoMinorVersionUpgrade:          inst.AutoMinorVersionUpgradeOrDefault(),
		Iops:                             inst.Iops,
		AvailabilityZone:                 inst.AvailabilityZone,
		IAMDatabaseAuthenticationEnabled: inst.EnableIAMDatabaseAuthentication,
		CACertificateIdentifier:          inst.CACertificateIdentifier,
		CertificateDetails:               xmlCertificateDetailsFor(inst.CACertificateIdentifier),
		MonitoringInterval:               inst.MonitoringInterval,
		PerformanceInsightsEnabled:       inst.PerformanceInsightsEnabled,
		EnabledCloudwatchLogsExports:     xmlLogTypeList{Items: inst.EnabledCloudwatchLogsExports},
		CopyTagsToSnapshot:               inst.CopyTagsToSnapshot,
	}
}

// toXMLDBSubnetGroup converts a stored DBSubnetGroup to the XML response type.
func toXMLDBSubnetGroup(sg *DBSubnetGroup) xmlDBSubnetGroup {
	subnets := make([]xmlSubnet, 0, len(sg.SubnetIds))
	for _, id := range sg.SubnetIds {
		subnets = append(subnets, xmlSubnet{SubnetIdentifier: id})
	}
	return xmlDBSubnetGroup{
		DBSubnetGroupName:        sg.DBSubnetGroupName,
		DBSubnetGroupDescription: sg.DBSubnetGroupDescription,
		DBSubnetGroupArn:         sg.DBSubnetGroupArn,
		VpcId:                    sg.VpcId,
		SubnetIds:                xmlSubnets{Items: subnets},
		Status:                   sg.Status,
	}
}

// ── Cluster helpers ───────────────────────────────────────────────────────────

// addInstanceToCluster atomically adds a DB instance as a member of the given
// Aurora cluster. The first instance added becomes the cluster writer.
// Errors are logged and not surfaced — the instance creation response has
// already been committed at this point.
func (h *Handler) addInstanceToCluster(ctx context.Context, clusterID, instanceID string) {
	// Atomically: two instances created together each read the member list, and
	// without the lock the second write drops the first's member — and makes a
	// second writer, having seen an empty list.
	if _, aerr := h.mutateCluster(ctx, clusterID, func(cluster *DBCluster) *protocol.AWSError {
		isWriter := len(cluster.DBClusterMembers) == 0
		cluster.DBClusterMembers = append(cluster.DBClusterMembers, DBClusterMember{
			DBInstanceIdentifier:          instanceID,
			IsClusterWriter:               isWriter,
			DBClusterParameterGroupStatus: "in-sync",
			PromotionTier:                 1,
		})
		return nil
	}); aerr != nil {
		h.log.Warn("addInstanceToCluster: failed to update cluster",
			zap.String("cluster", clusterID), zap.String("error", aerr.Message))
	}
}

// removeInstanceFromCluster is addInstanceToCluster's counterpart: it drops
// instanceID from clusterID's member list and, when the member being dropped
// was the writer, promotes a survivor in its place.
//
// AWS treats losing the writer as a failover — "If the DB cluster has one or
// more Aurora Replicas, then an Aurora Replica is promoted to the primary
// instance during a failure event" — and picks by promotion tier: 0 is the
// highest priority and 15 the lowest, with ties inside a tier going to the
// largest instance and then to "an arbitrary replica in the same promotion
// tier".
// https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/Concepts.AuroraHighAvailability.html
//
// Overcast honours the tier and settles the rest of that tie on the oldest
// surviving member, which is the head of the list because addInstanceToCluster
// appends in creation order. Size is not a factor: every member runs the same
// container whatever its DBInstanceClass. AWS's own answer at that point is
// explicitly arbitrary, and a reproducible one is worth more locally — a test
// that deletes a writer should not have to guess which replica it can dial
// afterwards.
//
// Returns the identifier of the promoted instance, or "" when nothing was
// promoted: the member was not the writer, was not a member at all, or was the
// last one standing. A cluster with no members has no writer, which is the
// honest answer and the one clusterEndpointsFor is already written to expect.
//
// Errors are logged rather than surfaced, for addInstanceToCluster's reason —
// the caller's response is committed by the time this runs.
func (h *Handler) removeInstanceFromCluster(ctx context.Context, clusterID, instanceID string) (promoted string) {
	if clusterID == "" {
		return ""
	}
	// Under the cluster lock for addInstanceToCluster's reason, and one more:
	// the read-modify-write that promotes a survivor must not interleave with
	// the one that admits a new member, or a cluster briefly ends up with two
	// writers — and both of them claim the cluster endpoint aliases.
	if _, aerr := h.mutateCluster(ctx, clusterID, func(cluster *DBCluster) *protocol.AWSError {
		idx := slices.IndexFunc(cluster.DBClusterMembers, func(m DBClusterMember) bool {
			return m.DBInstanceIdentifier == instanceID
		})
		if idx < 0 {
			return nil
		}
		lostWriter := cluster.DBClusterMembers[idx].IsClusterWriter
		cluster.DBClusterMembers = slices.Delete(cluster.DBClusterMembers, idx, idx+1)
		if !lostWriter {
			return nil
		}
		// Flip the flag explicitly rather than leaving the role to be inferred.
		// isClusterWriter treats an instance it cannot find as the writer when
		// the cluster has none — that is deliberate, and it covers the window
		// during creation before the first member is recorded, but it would
		// hand the cluster endpoints to whichever unlisted instance asked first
		// if a promotion left the seat empty.
		if w := electWriter(cluster.DBClusterMembers); w >= 0 {
			cluster.DBClusterMembers[w].IsClusterWriter = true
			promoted = cluster.DBClusterMembers[w].DBInstanceIdentifier
		}
		return nil
	}); aerr != nil {
		h.log.Warn("removeInstanceFromCluster: failed to update cluster",
			zap.String("cluster", clusterID),
			zap.String("instance", instanceID),
			zap.String("error", aerr.Message))
		return ""
	}
	return promoted
}

// electWriter is the index of the member to promote — the lowest promotion
// tier, the earliest-created member breaking the tie — or -1 when the cluster
// has no members left to promote.
func electWriter(members []DBClusterMember) int {
	best := -1
	for i, m := range members {
		if best < 0 || m.PromotionTier < members[best].PromotionTier {
			best = i
		}
	}
	return best
}

// ── Docker helpers ───────────────────────────────────────────────────────────

// setContainerDialTarget inspects the container after start and records how
// *Overcast* reaches it: the container's IP on the RDS Docker network when
// Overcast runs inside a container, otherwise 127.0.0.1 with the host port
// binding.
//
// It deliberately leaves Endpoint alone. Endpoint is what clients are told, and
// a raw address is dialable by exactly one party — the container IP means
// nothing on the host, and 127.0.0.1 means "yourself" inside a sibling
// container, which is how an ECS task ends up connecting to itself. The
// endpoint stays the AWS-shaped hostname and is rendered per caller on the way
// out (endpoint.go); this pair is for health checks only.
func (h *Handler) setContainerDialTarget(ctx context.Context, inst *DBInstance, ecfg engineEnv) {
	if addr := dataplane.ContainerAddr(ctx, h.docker, h.cfg, inst.DockerContainerID); addr != "" {
		inst.DialAddress = addr
		inst.DialPort = ecfg.ContainerPort
		return
	}

	// Native mode — use host port binding on localhost.
	inst.DialAddress = "127.0.0.1"
	inst.DialPort = inst.HostPort
}

// startDBContainer creates (or reuses) and starts a Docker container for the
// given DB instance. Updates inst.DockerContainerID, inst.HostPort and the
// health-check dial target in place.
//
// If a container with the expected name already exists (e.g. overcast was
// restarted while containers were kept running), we reuse it rather than
// failing or creating a duplicate. The existing container's host port binding
// is read from the inspect response so the stored port stays accurate.
func (h *Handler) startDBContainer(ctx context.Context, inst *DBInstance) error {
	// Resolve Docker image.
	image, matched, ok := resolveEngineImage(inst.Engine, inst.EngineVersion)
	if !ok {
		return fmt.Errorf("no image for engine %q version %q", inst.Engine, inst.EngineVersion)
	}
	if matched != inst.EngineVersion {
		h.log.Info("RDS: engine version has no image of its own, using the nearest one",
			zap.String("instance", inst.DBInstanceIdentifier),
			zap.String("engine", inst.Engine),
			zap.String("version", inst.EngineVersion),
			zap.String("served_by", matched),
			zap.String("image", image))
	}

	containerName := "overcast-rds-" + inst.DBInstanceIdentifier
	ecfg := engineEnvConfig[inst.Engine]
	containerPort := fmt.Sprintf("%d/tcp", ecfg.ContainerPort)

	// The DNS names this container must answer to, on every network a caller
	// might reach it from. Same set for each attachment below.
	//
	// An Aurora writer answers to its cluster's endpoints as well as its own:
	// the cluster endpoint is the name CDK hands an application, and it points
	// at no container but this one. See containerAliases.
	aliases := h.containerAliases(ctx, h.store.region(ctx), inst)

	// Check whether a container with this name already exists — this happens
	// after an overcast restart when RDSKeepContainers=true (or the process
	// was killed before cleanup completed).
	// We verify overcast labels before reusing to avoid accidentally attaching
	// to a user-created container that happens to share the same name.
	if existing, err := h.docker.GetContainerByName(ctx, containerName); err == nil && existing != nil {
		if !existing.HasOvercastLabels("rds", inst.DBInstanceIdentifier) {
			return fmt.Errorf("container %q exists but is not an overcast-managed RDS container for instance %q — refusing to reuse",
				containerName, inst.DBInstanceIdentifier)
		}
		// The labels above prove it is an RDS container for this instance's
		// name; they do not prove it is *this* Overcast's. Container names are
		// unique per daemon and the name is derived from the identifier, so
		// two Overcasts sharing a daemon collide here — and reusing what the
		// other one created hands both of them the same database, then lets
		// this one stop and delete it. Refusing says the same thing the daemon
		// would say about the name, early enough to name the real reason.
		if owner := existing.Instance(); owner != "" && owner != h.instances.Resolve(ctx) {
			return fmt.Errorf("container %q was created by another Overcast instance (%s=%s) — refusing to reuse it for DB instance %q; "+
				"two Overcasts sharing a Docker daemon cannot both run a DB instance of that name",
				containerName, docker.LabelInstance, owner, inst.DBInstanceIdentifier)
		}

		h.log.Info("RDS: reusing existing container",
			zap.String("instance", inst.DBInstanceIdentifier),
			zap.String("container", existing.ID),
			zap.String("state", existing.State.Status))

		// Recover the host port from the existing bindings.
		hostPort := 0
		if bindings, ok := existing.NetworkSettings.Ports[containerPort]; ok && len(bindings) > 0 {
			if p, err := strconv.Atoi(bindings[0].HostPort); err == nil {
				hostPort = p
			}
		}

		// If we can't read the port (shouldn't happen) allocate a new one.
		if hostPort == 0 {
			portBase := h.cfg.RDSPortBase
			if portBase == 0 {
				portBase = 33060
			}
			if hp, aerr := h.store.allocatePort(ctx, inst.DBInstanceIdentifier, portBase); aerr == nil {
				hostPort = hp
			}
		} else {
			// Record port in the port-allocation namespace so it's not reused.
			h.store.allocatePortFixed(ctx, inst.DBInstanceIdentifier, hostPort) //nolint:errcheck
		}

		// Ensure the container is running.
		if !existing.State.Running {
			if err := h.docker.StartContainer(ctx, existing.ID); err != nil {
				return fmt.Errorf("start existing container: %w", err)
			}
		}

		inst.DockerContainerID = existing.ID
		inst.HostPort = hostPort
		// Re-attach: a container adopted from an earlier run may predate the
		// current plane layout, or have been minted under a hostname the
		// configuration no longer lists. Attaching is idempotent.
		if placement, err := h.instancePlacement(ctx, inst, aliases); err != nil {
			h.log.Warn("RDS: reused container could not be placed in its VPC",
				zap.String("instance", inst.DBInstanceIdentifier), zap.Error(err))
		} else {
			if err := dataplane.AttachAdopted(ctx, h.docker, h.cfg, existing.ID, placement); err != nil {
				h.log.Warn("RDS: reused container could not join the data plane — "+
					"its endpoint name will not resolve for sibling containers",
					zap.String("instance", inst.DBInstanceIdentifier), zap.Error(err))
			}
		}
		h.setContainerDialTarget(ctx, inst, ecfg)
		return nil
	}

	// No existing container — allocate a port and create a fresh one.
	portBase := h.cfg.RDSPortBase
	if portBase == 0 {
		portBase = 33060
	}
	hostPort, aerr := h.store.allocatePort(ctx, inst.DBInstanceIdentifier, portBase)
	if aerr != nil {
		return fmt.Errorf("allocate port: %s", aerr.Message)
	}

	// Pull image (deduplicated per process lifetime).
	if err := h.puller.Ensure(ctx, image); err != nil {
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("pull image: %w", err)
	}

	// Resolve the plane before anything is created: a VPC that cannot take
	// containers should fail here, not after an image pull and a port
	// reservation that then have to be unwound.
	placement, err := h.instancePlacement(ctx, inst, aliases)
	if err != nil {
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("RDS %s: %w", inst.DBInstanceIdentifier, err)
	}

	// Build engine-specific env vars. MySQL 8 gets an entrypoint-safe temporary
	// root password; a sourced init fragment installs the exact requested
	// credential before the engine is exposed.
	entrypointPassword := inst.MasterUserPassword
	if usesMySQL8CredentialBootstrap(inst.Engine, image) || ecfg.UserVar != "" {
		entrypointPassword = credentialBootstrapPassword(inst.MasterUserPassword)
	}
	env := []string{ecfg.PasswordVar + "=" + entrypointPassword}
	if ecfg.UserVar != "" {
		env = append(env, ecfg.UserVar+"="+postgresBootstrapUser, ecfg.DatabaseVar+"=postgres")
	} else if inst.DBName != "" {
		env = append(env, ecfg.DatabaseVar+"="+inst.DBName)
	}

	req := &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image:        image,
			Env:          env,
			ExposedPorts: map[string]struct{}{containerPort: {}},
			Labels:       h.instances.ManagedLabels(ctx, serviceName, inst.DBInstanceIdentifier),
		},
		// No AutoRemove: StopDBInstance stops this container on purpose and keeps
		// its ID so StartDBInstance can bring it back, which AutoRemove makes
		// impossible by deleting it the moment it exits. DeleteDBInstance owns
		// removal instead, via the GC.
		HostConfig: &docker.HostConfig{
			NetworkMode: dataplane.Primary(h.cfg),
			PortBindings: map[string][]docker.PortBinding{
				containerPort: {{HostIP: "0.0.0.0", HostPort: strconv.Itoa(hostPort)}},
			},
		},
		NetworkingConfig: dataplane.PrimaryEndpoints(h.cfg),
	}

	// The puller retries once when the image was removed behind our back
	// (docker rmi after the recorded pull) instead of failing until restart.
	containerID, err := h.puller.CreateContainerWithRetry(ctx, containerName, req)
	if err != nil {
		// A conflict means the name appeared between our GetContainerByName check
		// and CreateContainer — race condition. Retry once by inspecting and reusing.
		if docker.IsConflict(err) {
			h.log.Warn("RDS: name conflict on create, retrying reuse",
				zap.String("instance", inst.DBInstanceIdentifier))
			h.store.releasePort(ctx, hostPort) //nolint:errcheck
			return h.startDBContainer(ctx, inst)
		}
		h.store.releasePort(ctx, hostPort) //nolint:errcheck
		return fmt.Errorf("create container: %w", err)
	}

	archive, archiveErr := credentialArchiveForInstance(inst, image)
	if archiveErr != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("build database credential initializer: %w", archiveErr)
	}
	if copyErr := h.docker.CopyToContainer(ctx, containerID, "/docker-entrypoint-initdb.d", archive); copyErr != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("install database credential initializer: %w", copyErr)
	}

	// Join the data plane before starting: the engine is reachable by name from
	// the moment it accepts connections, with no window in which a caller
	// resolves the endpoint and finds nothing there.
	placement.Aliases = aliases
	if err := dataplane.Attach(ctx, h.docker, h.cfg, containerID, placement); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("RDS %s: %w", inst.DBInstanceIdentifier, err)
	}

	if err := h.docker.StartContainer(ctx, containerID); err != nil {
		h.docker.RemoveContainerForce(containerID) //nolint:errcheck
		h.store.releasePort(ctx, hostPort)         //nolint:errcheck
		return fmt.Errorf("start container: %w", err)
	}

	inst.DockerContainerID = containerID
	inst.HostPort = hostPort
	h.setContainerDialTarget(ctx, inst, ecfg)
	return nil
}

func (h *Handler) region() string {
	if h.cfg != nil && h.cfg.Region != "" {
		return h.cfg.Region
	}
	return "us-east-1"
}

func (h *Handler) externalHostname() string {
	if h.cfg != nil {
		return h.cfg.ExternalHostname()
	}
	return "localhost"
}

// scheduleHealthCheck polls TCP connectivity to the DB container and transitions
// the instance from "creating" (or "starting") to "available" once it responds.
// region is the region the instance is stored under — the callbacks run outside
// any request context, so the store lookup would otherwise hit the default
// region and silently no-op for instances created elsewhere.
// scheduleHealthCheck lives in health.go, along with the failure handling it
// now does instead of declaring every instance available.

// launchDBContainerAsync starts the instance's DB container in the background
// so the image pull and container start do not block the request path. ctx is
// only used to capture the request's region; the work runs on a detached
// context.
//
// The container start takes real time, so the instance may have been stopped
// or deleted by the time it finishes. Persisting the pre-start snapshot would
// overwrite those transitions (StartDBInstance then rejects a "stopped"
// instance whose status reverted), so only the container-owned fields are
// merged into a fresh read.
func (h *Handler) launchDBContainerAsync(ctx context.Context, instID string) {
	region := h.store.region(ctx)
	h.dockerWg.Add(1)
	go func() {
		defer h.dockerWg.Done()
		bgCtx := middleware.ContextWithRegion(context.Background(), region)
		got, aerr := h.store.getDBInstance(bgCtx, instID)
		if aerr != nil || got == nil {
			return
		}
		if err := h.startDBContainer(bgCtx, got); err != nil {
			// Not a fallback to metadata-only. Docker was available when the
			// instance was created, so failing to build its container is a
			// real failure, and leaving the instance sitting in "creating" —
			// or, as it used to, in "available" with nothing behind it — tells
			// the caller nothing. Same treatment as a failed start.
			h.failInstance(bgCtx, instID, fmt.Sprintf("the database container could not be created: %v", err))
			return
		}
		fresh, aerr := h.mutateInstance(bgCtx, instID, func(inst *DBInstance) *protocol.AWSError {
			if inst.DBInstanceStatus == "deleting" {
				return errInstanceMovedOn
			}
			inst.DockerContainerID = got.DockerContainerID
			inst.HostPort = got.HostPort
			inst.Endpoint = got.Endpoint
			// DialAddress/DialPort are how Overcast itself reaches the engine,
			// and dropping them here left the health check falling back to the
			// endpoint name — a DNS record only sibling containers resolve — so
			// on the create path it dialled something it could never reach. The
			// start path's merge has always carried them; see
			// startInstanceContainerAsync.
			inst.DialAddress = got.DialAddress
			inst.DialPort = got.DialPort
			return nil
		})
		if aerr != nil {
			// Deleted, being deleted, or unpersistable: either way the
			// container it started belongs to no record, so it comes down.
			if aerr != errInstanceMovedOn {
				h.log.Warn("RDS: persist post-start instance",
					zap.String("instance", instID), zap.String("error", aerr.Message))
			}
			h.teardownOrphanedContainer(bgCtx, instID, got.DockerContainerID, got.HostPort)
			return
		}
		switch fresh.DBInstanceStatus {
		case "stopping", "stopped":
			// Instance was stopped while the container was starting — bring
			// the container down to match; it restarts on StartDBInstance.
			if h.gc != nil {
				h.gc.StopNow(fresh.DockerContainerID)
			} else {
				_ = h.docker.StopContainer(bgCtx, fresh.DockerContainerID, 10)
			}
		default:
			healthHost, healthPort := dialTarget(fresh)
			h.scheduleHealthCheck(region, instID, healthHost, healthPort)
		}
	}()
}

// teardownOrphanedContainer removes a container whose DB instance record was
// deleted while the container was still starting. DeleteDBInstance could not
// stop it — the container ID had not been persisted yet — so the start
// goroutine owns the cleanup, including the port it allocated.
func (h *Handler) teardownOrphanedContainer(ctx context.Context, instanceID, containerID string, hostPort int) {
	h.log.Info("RDS: instance deleted while its container was starting — removing container",
		zap.String("instance", instanceID), zap.String("container", containerID))
	if containerID != "" {
		if h.gc != nil {
			h.gc.StopNow(containerID)
			h.gc.ScheduleRemove(containerID)
		} else if h.docker != nil {
			_ = h.docker.RemoveContainerForce(containerID)
		}
	}
	if hostPort > 0 {
		if aerr := h.store.releasePort(ctx, hostPort); aerr != nil {
			h.log.Warn("RDS cleanup: release port",
				zap.String("instance", instanceID), zap.Int("port", hostPort), zap.Error(aerr))
		}
	}
}

// cleanupDBContainer releases the port reservation for a DB instance that had
// no Docker container (e.g. it was created before Docker was available).
// Docker container stop/remove is handled by the GC — this function is only
// for port cleanup. Kept as a separate function for clarity.
//
//nolint:unused // Kept for explicit Docker cleanup call sites.
func (h *Handler) cleanupDBContainer(ctx context.Context, instanceID, containerID string, hostPort int) {
	if hostPort > 0 {
		if aerr := h.store.releasePort(ctx, hostPort); aerr != nil {
			h.log.Warn("RDS cleanup: release port",
				zap.String("instance", instanceID), zap.Int("port", hostPort), zap.Error(aerr))
		}
	}
}
