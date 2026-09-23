package rds

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── Request types ──────────────────────────────────────────────────────────

type createDBInstanceReq struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
	Engine               string `json:"Engine"`
	MasterUsername       string `json:"MasterUsername"`
	MasterUserPassword   string `json:"MasterUserPassword"`
	DBInstanceClass      string `json:"DBInstanceClass"`
	EngineVersion        string `json:"EngineVersion"`
	AllocatedStorage     int    `json:"AllocatedStorage"`
	Port                 int    `json:"Port"`
	StorageType          string `json:"StorageType"`
	MultiAZ              bool   `json:"MultiAZ"`
	DBName               string `json:"DBName"`
	DBClusterIdentifier  string `json:"DBClusterIdentifier"`
	DBSubnetGroupName    string `json:"DBSubnetGroupName"`
	// PubliclyAccessible is a pointer because AWS's default for it is not
	// false, it is "it depends" — see defaultPubliclyAccessible. A plain bool
	// would make an omitted parameter indistinguishable from
	// `PubliclyAccessible=false` and so make the default unreachable.
	PubliclyAccessible *bool                 `json:"PubliclyAccessible"`
	Tags               []serviceutil.TagPair `json:"Tags"`
	// ManageMasterUserPassword and MasterUserSecretKmsKeyId are the
	// AWS-managed-secret path — see managed_secret.go. ManageMasterUserPassword
	// is a pointer for the same reason PubliclyAccessible is: "absent" and
	// "false" have to read as different requests, or a template that sets it
	// false explicitly could never be told apart from one that never
	// mentioned it.
	ManageMasterUserPassword *bool  `json:"ManageMasterUserPassword"`
	MasterUserSecretKmsKeyId string `json:"MasterUserSecretKmsKeyId"`
	// AdditionalStorageVolumes is decoded only to detect that the caller sent
	// it — see the rejection in createDBInstanceTyped. Its member fields are
	// never read.
	AdditionalStorageVolumes []additionalStorageVolumeReq `json:"AdditionalStorageVolumes"`
}

// additionalStorageVolumeReq mirrors AWS's AdditionalStorageVolume request
// shape (rds-2014-10-31.json#AdditionalStorageVolume). AWS documents it as
// "supported for RDS for Oracle and RDS for SQL Server DB instances only",
// and Overcast emulates neither engine — a commercial licence rules out
// Oracle entirely, and SQL Server is simply not yet implemented (see
// docs/services/rds/limitations.md). Every engine createDBInstanceTyped can
// actually create is therefore one AWS itself would refuse this parameter
// for, so it is rejected outright rather than silently accepted and echoed
// back for a container that never runs.
type additionalStorageVolumeReq struct {
	VolumeName          string `json:"VolumeName"`
	AllocatedStorage    *int   `json:"AllocatedStorage"`
	IOPS                *int   `json:"IOPS"`
	MaxAllocatedStorage *int   `json:"MaxAllocatedStorage"`
	StorageThroughput   *int   `json:"StorageThroughput"`
	StorageType         string `json:"StorageType"`
}

type describeDBInstancesReq struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
}

type deleteDBInstanceReq struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
}

type describeDBEngineVersionsReq struct {
	Engine string `json:"Engine"`
}

type stopDBInstanceReq struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
}

type startDBInstanceReq struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
}

type modifyDBInstanceReq struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
	DBInstanceClass      string `json:"DBInstanceClass"`
	AllocatedStorage     int    `json:"AllocatedStorage"`
	EngineVersion        string `json:"EngineVersion"`
	// MultiAZ is a pointer because "absent" and "false" are different
	// requests: absent leaves the instance alone, `MultiAZ=false` turns
	// Multi-AZ off. A plain bool cannot tell them apart, which is why this
	// operation used to ignore a request to disable Multi-AZ.
	MultiAZ *bool `json:"MultiAZ"`
	// PubliclyAccessible is a pointer for the same reason, and doubly so: this
	// is the parameter someone sends to take a database back off the network,
	// and `PubliclyAccessible=false` being read as "not sent" would leave them
	// no way to do it.
	PubliclyAccessible *bool  `json:"PubliclyAccessible"`
	StorageType        string `json:"StorageType"`
	// MasterUserPassword rotates the master password. Unlike every other field
	// here it is not applied to the record alone — see password.go.
	MasterUserPassword string `json:"MasterUserPassword"`
	// ManageMasterUserPassword toggles the managed-secret path on or off an
	// existing instance. A pointer for the reason MultiAZ is: "absent",
	// "true" and "false" are three different requests, and only a pointer
	// tells "absent" apart from "false" — the one that turns management off.
	ManageMasterUserPassword *bool  `json:"ManageMasterUserPassword"`
	MasterUserSecretKmsKeyId string `json:"MasterUserSecretKmsKeyId"`
}

type createDBSubnetGroupReq struct {
	DBSubnetGroupName        string                `json:"DBSubnetGroupName"`
	DBSubnetGroupDescription string                `json:"DBSubnetGroupDescription"`
	SubnetIds                []string              `json:"SubnetIds"`
	VpcId                    string                `json:"VpcId"`
	Tags                     []serviceutil.TagPair `json:"Tags"`
}

type deleteDBSubnetGroupReq struct {
	DBSubnetGroupName string `json:"DBSubnetGroupName"`
}

type describeDBSubnetGroupsReq struct {
	DBSubnetGroupName string `json:"DBSubnetGroupName"`
}

type createDBParameterGroupReq struct {
	DBParameterGroupName   string `json:"DBParameterGroupName"`
	DBParameterGroupFamily string `json:"DBParameterGroupFamily"`
	Description            string `json:"Description"`
}

type deleteDBParameterGroupReq struct {
	DBParameterGroupName string `json:"DBParameterGroupName"`
}

type describeDBParameterGroupsReq struct {
	DBParameterGroupName string `json:"DBParameterGroupName"`
}

type describeOrderableDBInstanceOptionsReq struct {
	Engine string `json:"Engine"`
}

// createDBClusterReq accepts the same settings ModifyDBCluster can change.
// What can be updated has to be creatable too: with Port and the rest readable
// only on the modify path, a template that set them at create and never
// touched them again produced a cluster that had never had them applied, and
// made the first no-op update look like a change.
type createDBClusterReq struct {
	DBClusterIdentifier               string                             `json:"DBClusterIdentifier"`
	Engine                            string                             `json:"Engine"`
	MasterUsername                    string                             `json:"MasterUsername"`
	MasterUserPassword                string                             `json:"MasterUserPassword"`
	EngineVersion                     string                             `json:"EngineVersion"`
	StorageType                       string                             `json:"StorageType"`
	MultiAZ                           bool                               `json:"MultiAZ"`
	DatabaseName                      string                             `json:"DatabaseName"`
	Port                              int                                `json:"Port"`
	DBSubnetGroupName                 string                             `json:"DBSubnetGroupName"`
	PreferredBackupWindow             string                             `json:"PreferredBackupWindow"`
	PreferredMaintenanceWindow        string                             `json:"PreferredMaintenanceWindow"`
	DBClusterParameterGroupName       string                             `json:"DBClusterParameterGroupName"`
	VpcSecurityGroupIds               []string                           `json:"VpcSecurityGroupIds"`
	BackupRetentionPeriod             *int                               `json:"BackupRetentionPeriod"`
	DeletionProtection                *bool                              `json:"DeletionProtection"`
	EnableCloudwatchLogsExports       []string                           `json:"EnableCloudwatchLogsExports"`
	CloudwatchLogsExportConfiguration *cloudwatchLogsExportConfiguration `json:"CloudwatchLogsExportConfiguration"`
	Tags                              []serviceutil.TagPair              `json:"Tags"`
	// ManageMasterUserPassword and MasterUserSecretKmsKeyId — see
	// createDBInstanceReq's fields of the same name.
	ManageMasterUserPassword *bool  `json:"ManageMasterUserPassword"`
	MasterUserSecretKmsKeyId string `json:"MasterUserSecretKmsKeyId"`
}

type describeDBClustersReq struct {
	DBClusterIdentifier string `json:"DBClusterIdentifier"`
}

type deleteDBClusterReq struct {
	DBClusterIdentifier string `json:"DBClusterIdentifier"`
}

// cloudwatchLogsExportConfiguration is ModifyDBCluster's nested log-export
// parameter. AWS spells the request and the response differently: this goes
// in, EnabledCloudwatchLogsExports comes back.
type cloudwatchLogsExportConfiguration struct {
	EnableLogTypes  []string `json:"EnableLogTypes"`
	DisableLogTypes []string `json:"DisableLogTypes"`
}

// modifyDBClusterReq carried DBClusterIdentifier and EngineVersion, and the
// CloudFormation handler for AWS::RDS::DBCluster sent nine other parameters
// that decoded into nothing at all — so a stack update that changed any of
// them reported UPDATE_COMPLETE having changed nothing.
//
// BackupRetentionPeriod and DeletionProtection are pointers for the reason
// modifyDBInstanceReq.MultiAZ is: "absent" and "zero" are different requests.
// `BackupRetentionPeriod=0` turns automated backups off and
// `DeletionProtection=false` turns protection off, and a plain int or bool
// cannot tell either of those from a parameter that was not sent.
type modifyDBClusterReq struct {
	DBClusterIdentifier               string                             `json:"DBClusterIdentifier"`
	EngineVersion                     string                             `json:"EngineVersion"`
	MasterUserPassword                string                             `json:"MasterUserPassword"`
	Port                              int                                `json:"Port"`
	PreferredBackupWindow             string                             `json:"PreferredBackupWindow"`
	PreferredMaintenanceWindow        string                             `json:"PreferredMaintenanceWindow"`
	DBClusterParameterGroupName       string                             `json:"DBClusterParameterGroupName"`
	VpcSecurityGroupIds               []string                           `json:"VpcSecurityGroupIds"`
	BackupRetentionPeriod             *int                               `json:"BackupRetentionPeriod"`
	DeletionProtection                *bool                              `json:"DeletionProtection"`
	CloudwatchLogsExportConfiguration *cloudwatchLogsExportConfiguration `json:"CloudwatchLogsExportConfiguration"`
	// ManageMasterUserPassword and MasterUserSecretKmsKeyId — see
	// modifyDBInstanceReq's fields of the same name.
	ManageMasterUserPassword *bool  `json:"ManageMasterUserPassword"`
	MasterUserSecretKmsKeyId string `json:"MasterUserSecretKmsKeyId"`
}

type startDBClusterReq struct {
	DBClusterIdentifier string `json:"DBClusterIdentifier"`
}

type stopDBClusterReq struct {
	DBClusterIdentifier string `json:"DBClusterIdentifier"`
}

// ─── Typed handler functions ────────────────────────────────────────────────

// --- CreateDBInstance ---

func (h *Handler) createDBInstanceTyped(ctx context.Context, req *createDBInstanceReq) (*xmlCreateDBInstanceResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBInstanceIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBInstanceIdentifier is required")
	}
	if aerr := validateDBIdentifier("DBInstanceIdentifier", id); aerr != nil {
		return nil, aerr
	}

	engine := req.Engine
	if engine == "" {
		return nil, errInvalidParameterValue("Engine is required")
	}
	if !supportedEngines[engine] {
		return nil, errInvalidParameterValue("Engine must be one of: mysql, postgres, mariadb, aurora-mysql, aurora-postgresql")
	}

	// AdditionalStorageVolumes is Oracle/SQL Server only on AWS, and engine is
	// already known-supported at this point — never Oracle or SQL Server (see
	// additionalStorageVolumeReq) — so any value here names a combination AWS
	// itself would refuse.
	if len(req.AdditionalStorageVolumes) > 0 {
		return nil, errInvalidParameterCombination("Additional storage volumes are supported for RDS for Oracle and RDS for SQL Server DB instances only.")
	}

	// Aurora member settings belong to the cluster. CreateDBInstance accepts
	// these fields in its shared request shape, but AWS documents them as not
	// applying to Aurora members; they must not override the cluster data plane.
	clusterID := req.DBClusterIdentifier
	masterUser := req.MasterUsername
	masterPass := req.MasterUserPassword
	engineVersion := req.EngineVersion
	port := req.Port
	dbName := req.DBName
	dbSubnetGroupName := req.DBSubnetGroupName
	// manageSecret, secretARN and secretKmsKeyId are managed_secret.go's
	// ManageMasterUserPassword state. For an Aurora member they are inherited
	// from the cluster below, exactly like masterUser/masterPass/etc — AWS
	// mints one secret per cluster, never one per member.
	manageSecret := false
	secretARN := ""
	secretKmsKeyId := ""
	if clusterID != "" {
		cluster, aerr := h.store.getDBCluster(ctx, clusterID)
		if aerr != nil {
			return nil, aerr
		}
		if cluster.Engine != engine {
			return nil, errInvalidParameterCombination("The DB instance engine must match the DB cluster engine.")
		}

		// AWS manages these settings at Aurora cluster scope. Instance-level
		// values don't apply, so they must never override the data plane.
		masterUser = cluster.MasterUsername
		masterPass = cluster.MasterUserPassword
		engineVersion = cluster.EngineVersion
		port = cluster.Port
		dbName = cluster.DatabaseName
		dbSubnetGroupName = cluster.DBSubnetGroupName
		manageSecret = cluster.ManageMasterUserPassword
		secretARN = cluster.MasterUserSecretARN
		secretKmsKeyId = cluster.MasterUserSecretKmsKeyId
	} else {
		manageSecret = req.ManageMasterUserPassword != nil && *req.ManageMasterUserPassword
		secretKmsKeyId = req.MasterUserSecretKmsKeyId
	}

	if clusterID == "" && req.Port != 0 {
		if aerr := validateDBPort(req.Port); aerr != nil {
			return nil, aerr
		}
	}

	if masterUser == "" {
		return nil, errInvalidParameterValue("MasterUsername is required")
	}
	if clusterID == "" {
		if aerr := validateMasterUsername(engine, masterUser, false); aerr != nil {
			return nil, aerr
		}
	}

	if clusterID == "" {
		if manageSecret && masterPass != "" {
			return nil, errInvalidParameterCombination(
				"You can't specify MasterUserPassword and set ManageMasterUserPassword to true at the same time.")
		}
		if !manageSecret {
			if masterPass == "" {
				return nil, errInvalidParameterValue("MasterUserPassword is required")
			}
			if aerr := validateMasterUserPassword(engine, masterPass); aerr != nil {
				return nil, aerr
			}
		}
	} else if masterPass == "" {
		return nil, errInvalidParameterValue("MasterUserPassword is required")
	}
	if clusterID == "" {
		if aerr := validateInitialDatabaseName(engine, dbName); aerr != nil {
			return nil, aerr
		}
	}

	if _, aerr := h.store.getDBInstance(ctx, id); aerr == nil {
		return nil, errDBInstanceAlreadyExists(id)
	}

	// Validated before the instance is written (#1196) — a rejected create
	// leaves no instance behind.
	incomingTags := serviceutil.TagsFromList(req.Tags)
	if aerr := serviceutil.ValidateTags(rdsTagCfg, incomingTags); aerr != nil {
		return nil, aerr
	}

	// Generated, and its Secrets Manager secret created, only now: after
	// every check that would otherwise reject the create. A standalone
	// instance only — an Aurora member already inherited manageSecret and
	// secretARN from its cluster above, and must not mint a second secret.
	if clusterID == "" && manageSecret {
		generated, arn, aerr := h.createManagedMasterSecret(ctx, engine, "db", id, masterUser, secretKmsKeyId)
		if aerr != nil {
			return nil, aerr
		}
		masterPass = generated
		secretARN = arn
	}

	instanceClass := req.DBInstanceClass
	if instanceClass == "" {
		instanceClass = "db.t3.micro"
	}

	if engineVersion == "" {
		engineVersion = defaultEngineVersions[engine]
	}

	allocatedStorage := req.AllocatedStorage
	if allocatedStorage == 0 {
		allocatedStorage = 20
	}

	if port == 0 {
		port = defaultPorts[engine]
	}

	storageType := req.StorageType
	if storageType == "" {
		storageType = "gp2"
	}

	multiAZ := req.MultiAZ
	vpcID := ""

	// Resolved once, here, so the record always carries an explicit answer and
	// nothing downstream has to re-derive it. It follows the *effective* subnet
	// group, inherited or not: an Aurora instance in a cluster with a subnet
	// group is as private as a standalone instance in the same group.
	publiclyAccessible := defaultPubliclyAccessible(engine, dbSubnetGroupName)
	if req.PubliclyAccessible != nil {
		publiclyAccessible = *req.PubliclyAccessible
	}
	if dbSubnetGroupName != "" {
		sg, aerr := h.store.getDBSubnetGroup(ctx, dbSubnetGroupName)
		if aerr != nil {
			return nil, aerr
		}
		vpcID = sg.VpcId
		if h.vpcResolver != nil && vpcID != "" {
			switch status := h.vpcResolver.VPCNetworkStatus(ctx, vpcID); status {
			case "", "ok", "shared", "remapped":
			case "conflict", "unbacked":
				return nil, &protocol.AWSError{
					Code: "InvalidVPCNetworkStateFault", Message: "VPC '" + vpcID + "' is not launchable for DB instances (network status=" + status + ").",
					HTTPStatus: http.StatusBadRequest,
				}
			default:
				return nil, &protocol.AWSError{
					Code: "InvalidVPCNetworkStateFault", Message: "VPC '" + vpcID + "' is not launchable for DB instances (network status=" + status + ").",
					HTTPStatus: http.StatusBadRequest,
				}
			}
		}
	}

	region := h.store.region(ctx)
	arn := protocol.ARN(region, h.cfg.AccountID, "rds", "db:"+id)
	now := h.clk.Now().UTC().Format(time.RFC3339)

	// Stored canonical, re-minted for whoever reads it — see endpoint.go.
	endpoint := &Endpoint{
		Address: instanceEndpointHostname(id, region, h.externalHostname()),
		Port:    port,
	}

	inst := &DBInstance{
		DBInstanceIdentifier: id,
		DBInstanceClass:      instanceClass,
		Engine:               engine,
		EngineVersion:        engineVersion,
		DBInstanceStatus:     "creating",
		MasterUsername:       masterUser,
		MasterUserPassword:   masterPass,
		DBName:               dbName,
		AllocatedStorage:     allocatedStorage,
		Endpoint:             endpoint,
		DBInstanceArn:        arn,
		InstanceCreateTime:   now,
		MultiAZ:              multiAZ,
		StorageType:          storageType,
		Port:                 port,
		DBClusterIdentifier:  clusterID,
		DBSubnetGroupName:    dbSubnetGroupName,
		VpcID:                vpcID,
		PubliclyAccessible:   &publiclyAccessible,

		ManageMasterUserPassword: manageSecret,
		MasterUserSecretARN:      secretARN,
		MasterUserSecretKmsKeyId: secretKmsKeyId,
	}

	if aerr := h.store.putDBInstance(ctx, inst); aerr != nil {
		return nil, aerr
	}
	if len(incomingTags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(ctx, h.store.tags(), arn, incomingTags, rdsTagCfg); aerr != nil {
			return nil, aerr
		}
	}

	// Before the container starts, not after: the alias set the container is
	// attached with depends on whether this instance is its cluster's writer,
	// and that is what membership records. startDBContainer can infer a first
	// member's role without it, but only registering it first makes the answer
	// certain.
	if clusterID != "" {
		h.addInstanceToCluster(ctx, clusterID, id)
	}

	launchingContainer := h.dockerReady.Load()
	if launchingContainer {
		if image, _, ok := resolveEngineImage(inst.Engine, inst.EngineVersion); ok && h.puller != nil {
			h.puller.Prewarm(image)
		}
		h.launchDBContainerAsync(ctx, id)
	}

	// Only a metadata-only instance is available the moment it exists. When a
	// container is coming up, the health check owns the transition — it is the
	// only thing that knows whether the engine answers.
	//
	// This used to run unconditionally, "so the instance is immediately usable
	// via the API". It made the status meaningless on the path most people
	// use: against a real MySQL container the instance reported available 0.9s
	// after CreateDBInstance and did not accept a connection for another 27s,
	// and because the health check only promotes creating/starting it found
	// the instance already available and did nothing — so a create whose
	// container never came up stayed available forever.
	if !launchingContainer {
		instID := id
		h.scheduler.AfterScoped(region, instID, "available", 0, func(ctx context.Context) {
			h.transitionInstance(ctx, instID, "creating", "available")
		})
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.RDSInstanceCreated, Time: h.clk.Now(), Source: "rds", Payload: events.ResourcePayload{Name: id}})
	}
	h.recordInstanceEvent(ctx, id, "DB instance created.", "creation")

	return &xmlCreateDBInstanceResponse{
		Xmlns: rdsXMLNS,
		Result: xmlCreateDBInstanceResult{
			DBInstance: h.toXMLDBInstance(ctx, inst),
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DescribeDBInstances ---

func (h *Handler) describeDBInstancesTyped(ctx context.Context, req *describeDBInstancesReq) (*xmlDescribeDBInstancesResponse, *protocol.AWSError) {
	filterID := normalizeDBIdentifier(req.DBInstanceIdentifier)

	if filterID != "" {
		inst, aerr := h.store.getDBInstance(ctx, filterID)
		if aerr != nil {
			return nil, aerr
		}
		return &xmlDescribeDBInstancesResponse{
			Xmlns: rdsXMLNS,
			Result: xmlDescribeDBInstancesResult{
				DBInstances: xmlDBInstances{Items: []xmlDBInstance{h.toXMLDBInstance(ctx, inst)}},
			},
			ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
		}, nil
	}

	all, aerr := h.store.listDBInstances(ctx)
	if aerr != nil {
		return nil, aerr
	}

	items := make([]xmlDBInstance, 0, len(all))
	for _, inst := range all {
		items = append(items, h.toXMLDBInstance(ctx, inst))
	}

	return &xmlDescribeDBInstancesResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeDBInstancesResult{
			DBInstances: xmlDBInstances{Items: items},
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DeleteDBInstance ---

func (h *Handler) deleteDBInstanceTyped(ctx context.Context, req *deleteDBInstanceReq) (*xmlDeleteDBInstanceResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBInstanceIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBInstanceIdentifier is required")
	}

	var containerID string
	var hostPort int
	inst, aerr := h.mutateInstance(ctx, id, func(inst *DBInstance) *protocol.AWSError {
		containerID = inst.DockerContainerID
		hostPort = inst.HostPort
		inst.DBInstanceStatus = "deleting"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.RDSInstanceDeleted, Time: h.clk.Now(), Source: "rds", Payload: events.ResourcePayload{Name: id}})
	}
	h.recordInstanceEvent(ctx, id, "DB instance deleted.", "deletion")

	// Only a standalone instance owns its managed secret — an Aurora member
	// only ever inherited its cluster's, and deleting one member must not
	// take out the secret the rest of the cluster (and DeleteDBCluster) still
	// need. Best-effort: see deleteManagedSecretBestEffort.
	if inst.DBClusterIdentifier == "" && inst.MasterUserSecretARN != "" {
		h.deleteManagedSecretBestEffort(ctx, inst.MasterUserSecretARN)
	}

	resp := &xmlDeleteDBInstanceResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDeleteDBInstanceResult{
			DBInstance: h.toXMLDBInstance(ctx, inst),
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}

	region := h.store.region(ctx)
	h.scheduler.CancelScoped(region, id, "health")

	// Stop the container immediately (async, non-blocking).
	if h.gc != nil && containerID != "" {
		h.gc.StopNow(containerID)
		h.gc.ScheduleRemove(containerID)
	}
	if hostPort > 0 {
		if aerr := h.store.releasePort(ctx, hostPort); aerr != nil {
			h.log.Warn("RDS cleanup: release port", zap.String("instance", id), zap.Error(aerr))
		}
	}

	h.scheduler.AfterScoped(region, id, "delete", 50*time.Millisecond, func(bgCtx context.Context) {
		if aerr := h.store.deleteDBInstance(bgCtx, id); aerr != nil {
			h.log.Warn("failed to delete RDS instance record", zap.String("instance", id), zap.Error(aerr))
		}
	})

	// The member list loses the instance the same way the instance list does,
	// and here rather than in the deferred record delete above: the container
	// has just been stopped, so a cluster that went on calling it the writer
	// would be advertising a dead engine for as long as the deletion took.
	//
	// AWS keeps the member listed while the instance is still "deleting" and
	// drops it when the instance goes. Overcast's window between the two is a
	// scheduler tick, and closing it early is the side to err on: what a caller
	// can observe in between is a cluster that names a live writer instead of
	// one that names a stopped container.
	if clusterID := inst.DBClusterIdentifier; clusterID != "" {
		if promoted := h.removeInstanceFromCluster(ctx, clusterID, id); promoted != "" {
			h.recordInstanceEvent(ctx, promoted, "Promoted to cluster writer following the deletion of "+id+".", "failover")
			// Docker work — off the request path. The cluster endpoint aliases
			// have to move onto the new writer's container, and that is a
			// detach and re-attach per network, not a store write.
			h.scheduler.AfterScoped(region, promoted, "promote", 0, func(bgCtx context.Context) {
				h.adoptClusterEndpoints(bgCtx, promoted)
			})
		}
	}

	return resp, nil
}

// --- DescribeDBEngineVersions ---

func (h *Handler) describeDBEngineVersionsTyped(ctx context.Context, req *describeDBEngineVersionsReq) (*xmlDescribeDBEngineVersionsResponse, *protocol.AWSError) {
	filterEngine := req.Engine

	items := make([]xmlDBEngineVersion, 0, len(allEngineVersions))
	for _, ev := range allEngineVersions {
		if filterEngine != "" && ev.Engine != filterEngine {
			continue
		}
		items = append(items, xmlDBEngineVersion(ev))
	}

	return &xmlDescribeDBEngineVersionsResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeDBEngineVersionsResult{
			DBEngineVersions: xmlDBEngineVersions{Items: items},
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- StopDBInstance ---

func (h *Handler) stopDBInstanceTyped(ctx context.Context, req *stopDBInstanceReq) (*xmlStopDBInstanceResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBInstanceIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBInstanceIdentifier is required")
	}

	inst, aerr := h.mutateInstance(ctx, id, func(inst *DBInstance) *protocol.AWSError {
		if inst.DBInstanceStatus != "available" {
			return errInvalidDBInstanceState(id, "must be available to stop")
		}
		inst.DBInstanceStatus = "stopping"
		inst.StoppedByUser = true
		inst.DockerRecoveryPending = false
		inst.DockerRecoveryAttempts = 0
		inst.DockerRecoveryAvailableAt = time.Time{}
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}

	if h.dockerReady.Load() && inst.DockerContainerID != "" {
		h.stopInstanceContainerAsync(ctx, id, inst.DockerContainerID)
	} else {
		instID := id
		region := h.store.region(ctx)
		h.scheduler.AfterScoped(region, instID, "stopped", 0, func(ctx context.Context) {
			h.completeInstanceStop(ctx, instID)
		})
	}

	return &xmlStopDBInstanceResponse{
		Xmlns:            rdsXMLNS,
		Result:           xmlStopDBInstanceResult{DBInstance: h.toXMLDBInstance(ctx, inst)},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

func (h *Handler) completeInstanceStop(ctx context.Context, instanceID string) {
	if _, aerr := h.mutateInstance(ctx, instanceID, func(inst *DBInstance) *protocol.AWSError {
		if inst.DBInstanceStatus != "stopping" {
			return errInstanceMovedOn
		}
		inst.DBInstanceStatus = "stopped"
		return nil
	}); aerr != nil {
		if aerr != errInstanceMovedOn {
			h.log.Warn("RDS: persist stopped instance", zap.String("instance", instanceID), zap.String("error", aerr.Message))
		}
		return
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.RDSInstanceStopped, Time: h.clk.Now(), Source: "rds", Payload: events.ResourcePayload{Name: instanceID}})
	}
	// RDS-EVENT-0087.
	h.recordInstanceEvent(ctx, instanceID, "DB instance stopped.", "notification")
}

// --- StartDBInstance ---

func (h *Handler) startDBInstanceTyped(ctx context.Context, req *startDBInstanceReq) (*xmlStartDBInstanceResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBInstanceIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBInstanceIdentifier is required")
	}

	inst, aerr := h.mutateInstance(ctx, id, func(inst *DBInstance) *protocol.AWSError {
		if inst.DBInstanceStatus != "stopped" {
			return errInvalidDBInstanceState(id, "must be stopped to start")
		}
		inst.DBInstanceStatus = "starting"
		inst.StoppedByUser = false
		inst.DockerRecoveryPending = false
		inst.DockerRecoveryAttempts = 0
		inst.DockerRecoveryAvailableAt = time.Time{}
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}

	region := h.store.region(ctx)
	if h.dockerReady.Load() && inst.DockerContainerID != "" {
		// Off the request path: a container Docker no longer has is rebuilt,
		// which may pull an image. The instance settles to available or failed
		// on its own — AWS answers StartDBInstance 200 with "starting" and
		// surfaces a start failure through the status, not through this call.
		h.startInstanceContainerAsync(ctx, id)
	} else {
		instID2 := id
		h.scheduler.AfterScoped(region, instID2, "available", 0, func(ctx context.Context) {
			h.transitionInstance(ctx, instID2, "starting", "available")
		})
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.RDSInstanceStarted, Time: h.clk.Now(), Source: "rds", Payload: events.ResourcePayload{Name: id}})
	}
	// RDS-EVENT-0088.
	h.recordInstanceEvent(ctx, id, "DB instance started.", "notification")

	return &xmlStartDBInstanceResponse{
		Xmlns:            rdsXMLNS,
		Result:           xmlStartDBInstanceResult{DBInstance: h.toXMLDBInstance(ctx, inst)},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- ModifyDBInstance ---

// modifyDBInstanceMasterSecret resolves ModifyDBInstance's MasterUserPassword
// and ManageMasterUserPassword into the single password change (if any) that
// has to reach the running engine, and — only when ManageMasterUserPassword
// itself is changing — the record's new managed-secret state.
//
// manageAfter is nil when management is not changing (an ordinary password
// rotation, or no password-related change at all), which tells the caller to
// leave the record's existing ManageMasterUserPassword/MasterUserSecret*
// fields exactly as they are.
func (h *Handler) modifyDBInstanceMasterSecret(ctx context.Context, id string, req *modifyDBInstanceReq) (passwordToApply string, manageAfter *bool, secretARN, kmsKeyID string, aerr *protocol.AWSError) {
	current, aerr := h.store.getDBInstance(ctx, id)
	if aerr != nil {
		return "", nil, "", "", aerr
	}

	turningOn := req.ManageMasterUserPassword != nil && *req.ManageMasterUserPassword && !current.ManageMasterUserPassword
	turningOff := req.ManageMasterUserPassword != nil && !*req.ManageMasterUserPassword && current.ManageMasterUserPassword

	switch {
	case turningOn:
		// RDS is about to generate the password itself, so the caller must
		// not also be supplying one — the same combination CreateDBInstance
		// refuses.
		if req.MasterUserPassword != "" {
			return "", nil, "", "", errInvalidParameterCombination(
				"You can't specify MasterUserPassword when you set ManageMasterUserPassword to true.")
		}
		generated, arn, gerr := h.createManagedMasterSecret(ctx, current.Engine, "db", id, current.MasterUsername, req.MasterUserSecretKmsKeyId)
		if gerr != nil {
			return "", nil, "", "", gerr
		}
		if aerr := h.changeMasterPassword(ctx, current, generated); aerr != nil {
			return "", nil, "", "", aerr
		}
		managed := true
		return generated, &managed, arn, req.MasterUserSecretKmsKeyId, nil

	case turningOff:
		// AWS requires the caller to supply the password they are taking
		// ownership of — there is no other way for the instance to end this
		// call with a password anyone but RDS itself knows.
		if req.MasterUserPassword == "" {
			return "", nil, "", "", errInvalidParameterValue(
				"MasterUserPassword is required when you set ManageMasterUserPassword to false.")
		}
		if aerr := validateMasterUserPassword(current.Engine, req.MasterUserPassword); aerr != nil {
			return "", nil, "", "", aerr
		}
		if req.MasterUserPassword != current.MasterUserPassword {
			if aerr := h.changeMasterPassword(ctx, current, req.MasterUserPassword); aerr != nil {
				return "", nil, "", "", aerr
			}
		}
		// The secret is deleted once the record stops pointing at it — see
		// modifyDBInstanceTyped. AWS: "Amazon RDS deletes the secret and uses
		// the new password for the master user specified by MasterUserPassword."
		managed := false
		return req.MasterUserPassword, &managed, "", "", nil

	default:
		// No management transition: an ordinary password change, or none.
		if req.MasterUserPassword == "" {
			return "", nil, "", "", nil
		}
		if current.ManageMasterUserPassword && (req.ManageMasterUserPassword == nil || *req.ManageMasterUserPassword) {
			return "", nil, "", "", errInvalidParameterCombination(
				"You can't specify MasterUserPassword while the master password is managed by Amazon RDS. " +
					"Set ManageMasterUserPassword to false to take ownership of it.")
		}
		if req.MasterUserPassword == current.MasterUserPassword {
			return "", nil, "", "", nil
		}
		if aerr := h.changeMasterPassword(ctx, current, req.MasterUserPassword); aerr != nil {
			return "", nil, "", "", aerr
		}
		return req.MasterUserPassword, nil, "", "", nil
	}
}

func (h *Handler) modifyDBInstanceTyped(ctx context.Context, req *modifyDBInstanceReq) (*xmlModifyDBInstanceResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBInstanceIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBInstanceIdentifier is required")
	}

	// The password (and any ManageMasterUserPassword transition) goes first,
	// and nothing else is applied unless it lands: a modification that
	// half-succeeded is harder to reason about than one that was refused
	// outright, and the caller can retry either way.
	//
	// It runs against the engine in the container, which is far too slow to
	// hold a record lock across, so it happens here on a snapshot — the record
	// it reads only decides whether the password is really changing and how to
	// reach the container. The new value is written with everything else below.
	passwordToApply, manageAfter, secretARNAfter, kmsKeyIDAfter, mErr := h.modifyDBInstanceMasterSecret(ctx, id, req)
	if mErr != nil {
		return nil, mErr
	}

	var settledStatus string
	var placementChanged bool
	var retiredSecretARN string
	inst, aerr := h.mutateInstance(ctx, id, func(inst *DBInstance) *protocol.AWSError {
		if passwordToApply != "" {
			inst.MasterUserPassword = passwordToApply
		}
		if manageAfter != nil {
			if !*manageAfter {
				retiredSecretARN = inst.MasterUserSecretARN
			}
			inst.ManageMasterUserPassword = *manageAfter
			inst.MasterUserSecretARN = secretARNAfter
			inst.MasterUserSecretKmsKeyId = kmsKeyIDAfter
		}
		if req.DBInstanceClass != "" {
			inst.DBInstanceClass = req.DBInstanceClass
		}
		if req.AllocatedStorage != 0 {
			inst.AllocatedStorage = req.AllocatedStorage
		}
		if req.EngineVersion != "" {
			if req.EngineVersion != inst.EngineVersion {
				h.log.Warn("EngineVersion change requested — restart would be needed in production",
					zap.String("instance", id), zap.String("from", inst.EngineVersion), zap.String("to", req.EngineVersion))
			}
			inst.EngineVersion = req.EngineVersion
		}
		if req.MultiAZ != nil {
			inst.MultiAZ = *req.MultiAZ
		}
		if req.PubliclyAccessible != nil {
			// Copied, not aliased: a record must not share a pointer with the
			// request that set it.
			pa := *req.PubliclyAccessible
			placementChanged = pa != inst.PubliclyAccessibleOrDefault()
			inst.PubliclyAccessible = &pa
		}
		if req.StorageType != "" {
			inst.StorageType = req.StorageType
		}

		settledStatus = inst.DBInstanceStatus
		if settledStatus == "" {
			settledStatus = "available"
		}
		inst.DBInstanceStatus = "modifying"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	// Turning management off deletes the secret, as AWS documents for
	// ModifyDBInstance — only once the record no longer points at it.
	h.deleteManagedSecretBestEffort(ctx, retiredSecretARN)

	// The wiring follows the record: a flag that only changed the response
	// would leave a "public" instance unreachable from outside its VPC.
	if placementChanged {
		h.reattachInstance(ctx, inst)
	}

	instID := id
	region := h.store.region(ctx)
	h.scheduler.AfterScoped(region, instID, "modified", 500*time.Millisecond, func(ctx context.Context) {
		h.transitionInstance(ctx, instID, "modifying", settledStatus)
	})

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.RDSInstanceModified, Time: h.clk.Now(), Source: "rds", Payload: events.ResourcePayload{Name: id}})
	}

	return &xmlModifyDBInstanceResponse{
		Xmlns:            rdsXMLNS,
		Result:           xmlModifyDBInstanceResult{DBInstance: h.toXMLDBInstance(ctx, inst)},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- CreateDBSubnetGroup ---

func (h *Handler) createDBSubnetGroupTyped(ctx context.Context, req *createDBSubnetGroupReq) (*xmlCreateDBSubnetGroupResponse, *protocol.AWSError) {
	name := req.DBSubnetGroupName
	if name == "" {
		return nil, errInvalidParameterValue("DBSubnetGroupName is required")
	}

	description := req.DBSubnetGroupDescription
	if description == "" {
		return nil, errInvalidParameterValue("DBSubnetGroupDescription is required")
	}

	if _, aerr := h.store.getDBSubnetGroup(ctx, name); aerr == nil {
		return nil, errDBSubnetGroupAlreadyExists(name)
	}

	subnetIds := req.SubnetIds
	if len(subnetIds) == 0 {
		return nil, errInvalidParameterValue("At least one SubnetId is required")
	}

	// Validated before the subnet group is written (#1196) — a rejected
	// create leaves no subnet group behind.
	incomingTags := serviceutil.TagsFromList(req.Tags)
	if aerr := serviceutil.ValidateTags(rdsTagCfg, incomingTags); aerr != nil {
		return nil, aerr
	}

	vpcId := req.VpcId
	if vpcId == "" && h.vpcResolver != nil && len(subnetIds) > 0 {
		vpcId = h.vpcResolver.VpcIDForSubnet(ctx, subnetIds[0])
	}
	if vpcId == "" {
		vpcId = "vpc-00000000"
	}

	region := h.store.region(ctx)
	arn := protocol.ARN(region, h.cfg.AccountID, "rds", "subgrp:"+name)

	sg := &DBSubnetGroup{
		DBSubnetGroupName:        name,
		DBSubnetGroupDescription: description,
		DBSubnetGroupArn:         arn,
		VpcId:                    vpcId,
		SubnetIds:                subnetIds,
		Status:                   "Complete",
	}

	if aerr := h.store.putDBSubnetGroup(ctx, sg); aerr != nil {
		return nil, aerr
	}
	if len(incomingTags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(ctx, h.store.tags(), arn, incomingTags, rdsTagCfg); aerr != nil {
			return nil, aerr
		}
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.RDSSubnetGroupCreated, Time: h.clk.Now(), Source: "rds", Payload: events.ResourcePayload{Name: name}})
	}

	return &xmlCreateDBSubnetGroupResponse{
		Xmlns:            rdsXMLNS,
		Result:           xmlCreateDBSubnetGroupResult{DBSubnetGroup: toXMLDBSubnetGroup(sg)},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DeleteDBSubnetGroup ---

func (h *Handler) deleteDBSubnetGroupTyped(ctx context.Context, req *deleteDBSubnetGroupReq) (*xmlDeleteDBSubnetGroupResponse, *protocol.AWSError) {
	name := req.DBSubnetGroupName
	if name == "" {
		return nil, errInvalidParameterValue("DBSubnetGroupName is required")
	}

	if _, aerr := h.store.getDBSubnetGroup(ctx, name); aerr != nil {
		return nil, aerr
	}

	if aerr := h.store.deleteDBSubnetGroup(ctx, name); aerr != nil {
		return nil, aerr
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.RDSSubnetGroupDeleted, Time: h.clk.Now(), Source: "rds", Payload: events.ResourcePayload{Name: name}})
	}

	return &xmlDeleteDBSubnetGroupResponse{
		Xmlns:            rdsXMLNS,
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DescribeDBSubnetGroups ---

func (h *Handler) describeDBSubnetGroupsTyped(ctx context.Context, req *describeDBSubnetGroupsReq) (*xmlDescribeDBSubnetGroupsResponse, *protocol.AWSError) {
	filterName := req.DBSubnetGroupName

	if filterName != "" {
		sg, aerr := h.store.getDBSubnetGroup(ctx, filterName)
		if aerr != nil {
			return nil, aerr
		}
		return &xmlDescribeDBSubnetGroupsResponse{
			Xmlns: rdsXMLNS,
			Result: xmlDescribeDBSubnetGroupsResult{
				DBSubnetGroups: xmlDBSubnetGroups{Items: []xmlDBSubnetGroup{toXMLDBSubnetGroup(sg)}},
			},
			ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
		}, nil
	}

	all, aerr := h.store.listDBSubnetGroups(ctx)
	if aerr != nil {
		return nil, aerr
	}

	items := make([]xmlDBSubnetGroup, 0, len(all))
	for _, sg := range all {
		items = append(items, toXMLDBSubnetGroup(sg))
	}

	return &xmlDescribeDBSubnetGroupsResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeDBSubnetGroupsResult{
			DBSubnetGroups: xmlDBSubnetGroups{Items: items},
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- CreateDBParameterGroup ---

func (h *Handler) createDBParameterGroupTyped(ctx context.Context, req *createDBParameterGroupReq) (*xmlCreateDBParameterGroupResponse, *protocol.AWSError) {
	name := req.DBParameterGroupName
	if name == "" {
		return nil, errInvalidParameterValue("DBParameterGroupName is required")
	}

	family := req.DBParameterGroupFamily
	if family == "" {
		return nil, errInvalidParameterValue("DBParameterGroupFamily is required")
	}
	if !isSupportedParameterGroupFamily(family) {
		return nil, errInvalidParameterValue("Invalid DB parameter group family: " + family)
	}

	if _, aerr := h.store.getDBParameterGroup(ctx, name); aerr == nil {
		return nil, errDBParameterGroupAlreadyExists(name)
	}

	region := h.store.region(ctx)
	arn := protocol.ARN(region, h.cfg.AccountID, "rds", "pg:"+name)

	pg := &DBParameterGroup{
		DBParameterGroupName:   name,
		DBParameterGroupFamily: family,
		Description:            req.Description,
		DBParameterGroupArn:    arn,
	}

	if aerr := h.store.putDBParameterGroup(ctx, pg); aerr != nil {
		return nil, aerr
	}

	return &xmlCreateDBParameterGroupResponse{
		Xmlns:            rdsXMLNS,
		Result:           xmlCreateDBParameterGroupResult{DBParameterGroup: toXMLDBParameterGroup(pg)},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DeleteDBParameterGroup ---

func (h *Handler) deleteDBParameterGroupTyped(ctx context.Context, req *deleteDBParameterGroupReq) (*xmlDeleteDBParameterGroupResponse, *protocol.AWSError) {
	name := req.DBParameterGroupName
	if name == "" {
		return nil, errInvalidParameterValue("DBParameterGroupName is required")
	}

	if _, aerr := h.store.getDBParameterGroup(ctx, name); aerr != nil {
		return nil, aerr
	}

	if aerr := h.store.deleteDBParameterGroup(ctx, name); aerr != nil {
		return nil, aerr
	}

	return &xmlDeleteDBParameterGroupResponse{
		Xmlns:            rdsXMLNS,
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DescribeDBParameterGroups ---

func (h *Handler) describeDBParameterGroupsTyped(ctx context.Context, req *describeDBParameterGroupsReq) (*xmlDescribeDBParameterGroupsResponse, *protocol.AWSError) {
	filterName := req.DBParameterGroupName

	if filterName != "" {
		pg, aerr := h.store.getDBParameterGroup(ctx, filterName)
		if aerr != nil {
			return nil, aerr
		}
		return &xmlDescribeDBParameterGroupsResponse{
			Xmlns: rdsXMLNS,
			Result: xmlDescribeDBParameterGroupsResult{
				DBParameterGroups: xmlDBParameterGroups{Items: []xmlDBParameterGroup{toXMLDBParameterGroup(pg)}},
			},
			ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
		}, nil
	}

	all, aerr := h.store.listDBParameterGroups(ctx)
	if aerr != nil {
		return nil, aerr
	}

	items := make([]xmlDBParameterGroup, 0, len(all))
	for _, pg := range all {
		items = append(items, toXMLDBParameterGroup(pg))
	}

	return &xmlDescribeDBParameterGroupsResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeDBParameterGroupsResult{
			DBParameterGroups: xmlDBParameterGroups{Items: items},
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DescribeOrderableDBInstanceOptions ---

func (h *Handler) describeOrderableDBInstanceOptionsTyped(ctx context.Context, req *describeOrderableDBInstanceOptionsReq) (*xmlDescribeOrderableDBInstanceOptionsResponse, *protocol.AWSError) {
	engine := req.Engine
	if engine == "" {
		return nil, errInvalidParameterValue("Engine is required")
	}

	items := make([]xmlOrderableDBInstanceOption, 0)
	for _, opt := range allOrderableOptions {
		if opt.Engine != engine {
			continue
		}
		items = append(items, xmlOrderableDBInstanceOption(opt))
	}

	return &xmlDescribeOrderableDBInstanceOptionsResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeOrderableDBInstanceOptionsResult{
			OrderableDBInstanceOptions: xmlOrderableDBInstanceOptions{Items: items},
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- CreateDBCluster ---

func (h *Handler) createDBClusterTyped(ctx context.Context, req *createDBClusterReq) (*xmlCreateDBClusterResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBClusterIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBClusterIdentifier is required")
	}
	if aerr := validateDBIdentifier("DBClusterIdentifier", id); aerr != nil {
		return nil, aerr
	}

	engine := req.Engine
	if !auroraEngines[engine] {
		return nil, errInvalidParameterValue("Engine must be one of: aurora-mysql, aurora-postgresql")
	}

	if req.MasterUsername == "" {
		return nil, errInvalidParameterValue("MasterUsername is required")
	}
	if aerr := validateMasterUsername(engine, req.MasterUsername, true); aerr != nil {
		return nil, aerr
	}

	manageSecret := req.ManageMasterUserPassword != nil && *req.ManageMasterUserPassword
	masterPass := req.MasterUserPassword
	if manageSecret && masterPass != "" {
		return nil, errInvalidParameterCombination(
			"You can't specify MasterUserPassword and set ManageMasterUserPassword to true at the same time.")
	}
	if !manageSecret {
		if masterPass == "" {
			return nil, errInvalidParameterValue("MasterUserPassword is required")
		}
		if aerr := validateMasterUserPassword(engine, masterPass); aerr != nil {
			return nil, aerr
		}
	}
	if aerr := validateInitialDatabaseName(engine, req.DatabaseName); aerr != nil {
		return nil, aerr
	}
	// Port 0 is "not sent" over the Query protocol, so only a value the caller
	// actually chose is range-checked; the engine default fills the rest in.
	if req.Port != 0 {
		if aerr := validateDBPort(req.Port); aerr != nil {
			return nil, aerr
		}
	}
	backupRetention := clusterBackupRetentionDefault
	if req.BackupRetentionPeriod != nil {
		if aerr := validateClusterBackupRetentionPeriod(*req.BackupRetentionPeriod); aerr != nil {
			return nil, aerr
		}
		backupRetention = *req.BackupRetentionPeriod
	}
	// AWS: "Must match the name of an existing DB subnet group." Accepting a
	// name nothing backs is the divergence that provisions here and fails in
	// the account.
	if req.DBSubnetGroupName != "" {
		if _, aerr := h.store.getDBSubnetGroup(ctx, req.DBSubnetGroupName); aerr != nil {
			return nil, aerr
		}
	}

	if _, aerr := h.store.getDBCluster(ctx, id); aerr == nil {
		return nil, errDBClusterAlreadyExists(id)
	}

	// Validated before the cluster is written (#1196) — a rejected create
	// leaves no cluster behind.
	incomingTags := serviceutil.TagsFromList(req.Tags)
	if aerr := serviceutil.ValidateTags(rdsTagCfg, incomingTags); aerr != nil {
		return nil, aerr
	}

	// Generated, and its Secrets Manager secret created, only now: after
	// every check that would otherwise reject the create.
	secretARN := ""
	if manageSecret {
		generated, arn, aerr := h.createManagedMasterSecret(ctx, engine, "cluster", id, req.MasterUsername, req.MasterUserSecretKmsKeyId)
		if aerr != nil {
			return nil, aerr
		}
		masterPass = generated
		secretARN = arn
	}

	engineVersion := req.EngineVersion
	if engineVersion == "" {
		engineVersion = defaultEngineVersions[engine]
	}

	storageType := req.StorageType
	if storageType == "" {
		storageType = "aurora"
	}

	region := h.store.region(ctx)
	arn := protocol.ARN(region, h.cfg.AccountID, "rds", "cluster:"+id)
	now := h.clk.Now().UTC().Format(time.RFC3339)

	port := req.Port
	if port == 0 {
		port = defaultPorts[engine]
	}

	logExports := req.EnableCloudwatchLogsExports
	if cfg := req.CloudwatchLogsExportConfiguration; cfg != nil {
		logExports = applyLogExportConfiguration(logExports, cfg)
	}

	cluster := &DBCluster{
		DBClusterIdentifier: id,
		DBClusterArn:        arn,
		Engine:              engine,
		EngineVersion:       engineVersion,
		Status:              "creating",
		MasterUsername:      req.MasterUsername,
		MasterUserPassword:  masterPass,
		DatabaseName:        req.DatabaseName,
		Port:                port,
		// Built through the same helper the alias set and the wire response
		// use. Spelling the roles inline here is how the stored record and the
		// names actually registered in DNS were free to drift apart.
		Endpoint:                     clusterEndpointHostname(id, clusterRoleWriter, region, h.cfg.ExternalHostname()),
		ReaderEndpoint:               clusterEndpointHostname(id, clusterRoleReader, region, h.cfg.ExternalHostname()),
		MultiAZ:                      req.MultiAZ,
		StorageType:                  storageType,
		ClusterCreateTime:            now,
		DBClusterMembers:             []DBClusterMember{},
		DBSubnetGroupName:            req.DBSubnetGroupName,
		PreferredBackupWindow:        req.PreferredBackupWindow,
		PreferredMaintenanceWindow:   req.PreferredMaintenanceWindow,
		DBClusterParameterGroup:      req.DBClusterParameterGroupName,
		VpcSecurityGroupIds:          req.VpcSecurityGroupIds,
		EnabledCloudwatchLogsExports: logExports,

		ManageMasterUserPassword: manageSecret,
		MasterUserSecretARN:      secretARN,
		MasterUserSecretKmsKeyId: req.MasterUserSecretKmsKeyId,
	}
	cluster.BackupRetentionPeriod = backupRetention
	if req.DeletionProtection != nil {
		cluster.DeletionProtection = *req.DeletionProtection
	}

	if aerr := h.store.putDBCluster(ctx, cluster); aerr != nil {
		return nil, aerr
	}
	if len(incomingTags) > 0 {
		if _, aerr := serviceutil.ApplyStoreTags(ctx, h.store.tags(), arn, incomingTags, rdsTagCfg); aerr != nil {
			return nil, aerr
		}
	}

	clID := id
	h.scheduler.AfterScoped(region, clID, "available", 500*time.Millisecond, func(ctx context.Context) {
		h.transitionCluster(ctx, clID, "creating", "available")
	})

	return &xmlCreateDBClusterResponse{
		Xmlns: rdsXMLNS,
		Result: xmlCreateDBClusterResult{
			DBCluster: h.toXMLDBCluster(ctx, cluster),
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DescribeDBClusters ---

func (h *Handler) describeDBClustersTyped(ctx context.Context, req *describeDBClustersReq) (*xmlDescribeDBClustersResponse, *protocol.AWSError) {
	filterID := normalizeDBIdentifier(req.DBClusterIdentifier)

	if filterID != "" {
		cluster, aerr := h.store.getDBCluster(ctx, filterID)
		if aerr != nil {
			return nil, aerr
		}
		return &xmlDescribeDBClustersResponse{
			Xmlns: rdsXMLNS,
			Result: xmlDescribeDBClustersResult{
				DBClusters: xmlDBClusters{Items: []xmlDBCluster{h.toXMLDBCluster(ctx, cluster)}},
			},
			ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
		}, nil
	}

	all, aerr := h.store.listDBClusters(ctx)
	if aerr != nil {
		return nil, aerr
	}

	items := make([]xmlDBCluster, 0, len(all))
	for _, c := range all {
		items = append(items, h.toXMLDBCluster(ctx, c))
	}

	return &xmlDescribeDBClustersResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDescribeDBClustersResult{
			DBClusters: xmlDBClusters{Items: items},
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- DeleteDBCluster ---

func (h *Handler) deleteDBClusterTyped(ctx context.Context, req *deleteDBClusterReq) (*xmlDeleteDBClusterResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBClusterIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBClusterIdentifier is required")
	}

	cluster, aerr := h.mutateCluster(ctx, id, func(cluster *DBCluster) *protocol.AWSError {
		// AWS refuses outright rather than deleting and reporting the flag
		// afterwards, and a flag that is recorded but not enforced is worse
		// than one that was never recorded: the console shows the cluster as
		// protected while a stack teardown removes it anyway. Refusing inside
		// the mutation leaves the record untouched.
		if cluster.DeletionProtection {
			return &protocol.AWSError{
				Code:       "InvalidParameterCombination",
				Message:    "Cannot delete protected DB Cluster, please disable deletion protection and try again.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		cluster.Status = "deleting"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}

	resp := &xmlDeleteDBClusterResponse{
		Xmlns: rdsXMLNS,
		Result: xmlDeleteDBClusterResult{
			DBCluster: h.toXMLDBCluster(ctx, cluster),
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}

	// The cluster owns this secret outright — no member instance ever mints
	// its own — so it is deleted here, once, rather than by whichever member
	// happens to be deleted first (deleteDBInstanceTyped explicitly leaves a
	// member's inherited secret alone for the same reason).
	if cluster.MasterUserSecretARN != "" {
		h.deleteManagedSecretBestEffort(ctx, cluster.MasterUserSecretARN)
	}

	clID := id
	region := h.store.region(ctx)
	h.scheduler.AfterScoped(region, clID, "delete", 50*time.Millisecond, func(ctx context.Context) {
		if aerr := h.store.deleteDBCluster(ctx, clID); aerr != nil {
			h.log.Warn("failed to delete RDS cluster record",
				zap.String("cluster", clID), zap.Error(aerr))
		}
	})

	return resp, nil
}

// --- ModifyDBCluster ---

// modifyDBClusterMasterSecret is modifyDBInstanceMasterSecret for a cluster —
// see that function for the shape and the reasoning behind it. The only
// difference is the password change itself: changeClusterMasterPassword
// rather than changeMasterPassword, since a cluster has no engine of its own
// and rotates the password across every member.
func (h *Handler) modifyDBClusterMasterSecret(ctx context.Context, id string, req *modifyDBClusterReq) (passwordToApply string, manageAfter *bool, secretARN, kmsKeyID string, aerr *protocol.AWSError) {
	current, aerr := h.store.getDBCluster(ctx, id)
	if aerr != nil {
		return "", nil, "", "", aerr
	}

	turningOn := req.ManageMasterUserPassword != nil && *req.ManageMasterUserPassword && !current.ManageMasterUserPassword
	turningOff := req.ManageMasterUserPassword != nil && !*req.ManageMasterUserPassword && current.ManageMasterUserPassword

	switch {
	case turningOn:
		if req.MasterUserPassword != "" {
			return "", nil, "", "", errInvalidParameterCombination(
				"You can't specify MasterUserPassword when you set ManageMasterUserPassword to true.")
		}
		generated, arn, gerr := h.createManagedMasterSecret(ctx, current.Engine, "cluster", id, current.MasterUsername, req.MasterUserSecretKmsKeyId)
		if gerr != nil {
			return "", nil, "", "", gerr
		}
		if aerr := h.changeClusterMasterPassword(ctx, current, generated); aerr != nil {
			return "", nil, "", "", aerr
		}
		managed := true
		return generated, &managed, arn, req.MasterUserSecretKmsKeyId, nil

	case turningOff:
		if req.MasterUserPassword == "" {
			return "", nil, "", "", errInvalidParameterValue(
				"MasterUserPassword is required when you set ManageMasterUserPassword to false.")
		}
		if aerr := validateMasterUserPassword(current.Engine, req.MasterUserPassword); aerr != nil {
			return "", nil, "", "", aerr
		}
		if req.MasterUserPassword != current.MasterUserPassword {
			if aerr := h.changeClusterMasterPassword(ctx, current, req.MasterUserPassword); aerr != nil {
				return "", nil, "", "", aerr
			}
		}
		// The secret is deleted once the record stops pointing at it — see
		// modifyDBClusterTyped.
		managed := false
		return req.MasterUserPassword, &managed, "", "", nil

	default:
		if req.MasterUserPassword == "" {
			return "", nil, "", "", nil
		}
		if current.ManageMasterUserPassword && (req.ManageMasterUserPassword == nil || *req.ManageMasterUserPassword) {
			return "", nil, "", "", errInvalidParameterCombination(
				"You can't specify MasterUserPassword while the master password is managed by Amazon RDS. " +
					"Set ManageMasterUserPassword to false to take ownership of it.")
		}
		if req.MasterUserPassword == current.MasterUserPassword {
			return "", nil, "", "", nil
		}
		if aerr := h.changeClusterMasterPassword(ctx, current, req.MasterUserPassword); aerr != nil {
			return "", nil, "", "", aerr
		}
		return req.MasterUserPassword, nil, "", "", nil
	}
}

func (h *Handler) modifyDBClusterTyped(ctx context.Context, req *modifyDBClusterReq) (*xmlModifyDBClusterResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBClusterIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBClusterIdentifier is required")
	}
	// The same bounds create enforces. Without them a cluster could be created
	// at a legal retention and then modified to one AWS would refuse, which
	// makes the create-time check theatre.
	if req.Port != 0 {
		if aerr := validateDBPort(req.Port); aerr != nil {
			return nil, aerr
		}
	}
	if req.BackupRetentionPeriod != nil {
		if aerr := validateClusterBackupRetentionPeriod(*req.BackupRetentionPeriod); aerr != nil {
			return nil, aerr
		}
	}

	// The password (and any ManageMasterUserPassword transition) goes first,
	// and nothing else is applied unless it lands — the same discipline
	// ModifyDBInstance follows, and for the same reasons. It runs here rather
	// than inside the mutation below on two counts: it reaches an engine in a
	// container, which is far too slow to hold a record lock across, and it
	// takes each member's instance lock, which must never nest inside the
	// cluster's (see locks.go).
	passwordToApply, manageAfter, secretARNAfter, kmsKeyIDAfter, mErr := h.modifyDBClusterMasterSecret(ctx, id, req)
	if mErr != nil {
		return nil, mErr
	}

	var retiredSecretARN string
	cluster, aerr := h.mutateCluster(ctx, id, func(cluster *DBCluster) *protocol.AWSError {
		if passwordToApply != "" {
			cluster.MasterUserPassword = passwordToApply
		}
		if manageAfter != nil {
			if !*manageAfter {
				retiredSecretARN = cluster.MasterUserSecretARN
			}
			cluster.ManageMasterUserPassword = *manageAfter
			cluster.MasterUserSecretARN = secretARNAfter
			cluster.MasterUserSecretKmsKeyId = kmsKeyIDAfter
		}
		if req.EngineVersion != "" {
			cluster.EngineVersion = req.EngineVersion
		}
		if req.Port != 0 {
			cluster.Port = req.Port
		}
		if req.PreferredBackupWindow != "" {
			cluster.PreferredBackupWindow = req.PreferredBackupWindow
		}
		if req.PreferredMaintenanceWindow != "" {
			cluster.PreferredMaintenanceWindow = req.PreferredMaintenanceWindow
		}
		if req.DBClusterParameterGroupName != "" {
			cluster.DBClusterParameterGroup = req.DBClusterParameterGroupName
		}
		if len(req.VpcSecurityGroupIds) > 0 {
			cluster.VpcSecurityGroupIds = req.VpcSecurityGroupIds
		}
		if req.BackupRetentionPeriod != nil {
			cluster.BackupRetentionPeriod = *req.BackupRetentionPeriod
		}
		if req.DeletionProtection != nil {
			cluster.DeletionProtection = *req.DeletionProtection
		}
		if cfg := req.CloudwatchLogsExportConfiguration; cfg != nil {
			cluster.EnabledCloudwatchLogsExports = applyLogExportConfiguration(
				cluster.EnabledCloudwatchLogsExports, cfg)
		}
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	// As ModifyDBInstance: turning management off deletes the secret.
	h.deleteManagedSecretBestEffort(ctx, retiredSecretARN)

	return &xmlModifyDBClusterResponse{
		Xmlns: rdsXMLNS,
		Result: xmlCreateDBClusterResult{
			DBCluster: h.toXMLDBCluster(ctx, cluster),
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// applyLogExportConfiguration folds an enable/disable request into the log
// types already exported. AWS's parameter is a delta, not a replacement: a
// request that enables "audit" does not turn "error" off.
func applyLogExportConfiguration(current []string, cfg *cloudwatchLogsExportConfiguration) []string {
	enabled := make(map[string]bool, len(current))
	order := append([]string(nil), current...)
	for _, t := range current {
		enabled[t] = true
	}
	for _, t := range cfg.EnableLogTypes {
		if !enabled[t] {
			enabled[t] = true
			order = append(order, t)
		}
	}
	for _, t := range cfg.DisableLogTypes {
		delete(enabled, t)
	}

	out := make([]string, 0, len(order))
	for _, t := range order {
		if enabled[t] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- StartDBCluster ---

func (h *Handler) startDBClusterTyped(ctx context.Context, req *startDBClusterReq) (*xmlStartDBClusterResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBClusterIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBClusterIdentifier is required")
	}

	cluster, aerr := h.mutateCluster(ctx, id, func(cluster *DBCluster) *protocol.AWSError {
		if cluster.Status != "stopped" {
			return &protocol.AWSError{
				Code: "InvalidDBClusterStateFault", Message: "Cluster " + id + " is not in a stopped state.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		cluster.Status = "starting"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}

	clID := id
	region := h.store.region(ctx)
	h.scheduler.AfterScoped(region, clID, "start", 500*time.Millisecond, func(ctx context.Context) {
		h.transitionCluster(ctx, clID, "starting", "available")
	})

	return &xmlStartDBClusterResponse{
		Xmlns: rdsXMLNS,
		Result: xmlCreateDBClusterResult{
			DBCluster: h.toXMLDBCluster(ctx, cluster),
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}

// --- StopDBCluster ---

func (h *Handler) stopDBClusterTyped(ctx context.Context, req *stopDBClusterReq) (*xmlStopDBClusterResponse, *protocol.AWSError) {
	id := normalizeDBIdentifier(req.DBClusterIdentifier)
	if id == "" {
		return nil, errInvalidParameterValue("DBClusterIdentifier is required")
	}

	cluster, aerr := h.mutateCluster(ctx, id, func(cluster *DBCluster) *protocol.AWSError {
		if cluster.Status != "available" {
			return &protocol.AWSError{
				Code: "InvalidDBClusterStateFault", Message: "Cluster " + id + " is not in an available state.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		cluster.Status = "stopping"
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}

	clID := id
	region := h.store.region(ctx)
	h.scheduler.AfterScoped(region, clID, "stop", 500*time.Millisecond, func(ctx context.Context) {
		h.transitionCluster(ctx, clID, "stopping", "stopped")
	})

	return &xmlStopDBClusterResponse{
		Xmlns: rdsXMLNS,
		Result: xmlCreateDBClusterResult{
			DBCluster: h.toXMLDBCluster(ctx, cluster),
		},
		ResponseMetadata: protocol.ResponseMetadata{RequestID: protocol.RequestIDFromContext(ctx)},
	}, nil
}
