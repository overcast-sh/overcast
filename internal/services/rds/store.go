package rds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	nsDBInstances     = "rds:instances"
	nsClusters        = "rds:clusters"
	nsPorts           = "rds:ports"
	nsSubnetGroups    = "rds:subnet-groups"
	nsParameterGroups = "rds:parameter-groups"
	nsEvents          = "rds:events"
	nsTags            = "rds:tags"
	// nsInstance holds this instance's sweep-domain identity, the value
	// stamped into docker.LabelInstance on every engine container. See
	// serviceutil.InstanceDomain.
	nsInstance = "rds:instance"
)

// DBInstance represents a stored RDS DB instance.
type DBInstance struct {
	DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
	DBInstanceClass      string `json:"DBInstanceClass"`
	Engine               string `json:"Engine"`
	EngineVersion        string `json:"EngineVersion"`
	DBInstanceStatus     string `json:"DBInstanceStatus"`
	// StoppedByUser distinguishes StopDBInstance from a Docker-driven stop.
	// Reconciliation restores missing runtime for the latter, but must preserve
	// the former across Overcast restarts. Older records decode to false, which
	// recovers instances stranded by the pre-fix shutdown sweep.
	StoppedByUser bool `json:"StoppedByUser,omitempty"`
	// DockerRecoveryPending records desired-state recovery that could not yet
	// complete, most notably when the daemon disappears between its die event
	// and the restart call. A later daemon reconnect may retry only these failed
	// instances rather than restarting every genuinely failed database boot.
	DockerRecoveryPending bool `json:"DockerRecoveryPending,omitempty"`
	// DockerRecoveryAttempts bounds automatic restarts of an engine that keeps
	// exiting. It is cleared only after the recovered engine remains available
	// for a stability period, so a process that briefly opens its port before
	// crashing cannot reset its own circuit breaker.
	DockerRecoveryAttempts int `json:"DockerRecoveryAttempts,omitempty"`
	// DockerRecoveryAvailableAt makes the stability window survive an Overcast
	// restart. The reset timer is process-local, but a later recovery can still
	// tell that the engine stayed available long enough to earn a fresh budget.
	DockerRecoveryAvailableAt time.Time `json:"DockerRecoveryAvailableAt,omitempty"`
	// StatusReason is why the instance is in a failure status. It is
	// deliberately absent from the DescribeDBInstances wire shape: the real
	// DBInstance carries no such field, and StatusInfos is documented as
	// read-replica-only. AWS exposes a failure reason through RDS events, and
	// this is the text those events carry.
	StatusReason       string    `json:"StatusReason,omitempty"`
	MasterUsername     string    `json:"MasterUsername"`
	MasterUserPassword string    `json:"MasterUserPassword,omitempty"`
	DBName             string    `json:"DBName,omitempty"`
	AllocatedStorage   int       `json:"AllocatedStorage"`
	Endpoint           *Endpoint `json:"Endpoint,omitempty"`
	DBInstanceArn      string    `json:"DBInstanceArn"`
	InstanceCreateTime string    `json:"InstanceCreateTime,omitempty"`
	MultiAZ            bool      `json:"MultiAZ"`
	StorageType        string    `json:"StorageType"`
	Port               int       `json:"Port"`
	DockerContainerID  string    `json:"DockerContainerID,omitempty"`
	HostPort           int       `json:"HostPort,omitempty"`
	// DialAddress/DialPort are how *Overcast* reaches the engine container
	// (health checks), which is not what any client is told: see dialTarget and
	// instanceEndpointFor in endpoint.go.
	DialAddress         string `json:"DialAddress,omitempty"`
	DialPort            int    `json:"DialPort,omitempty"`
	DBClusterIdentifier string `json:"DBClusterIdentifier,omitempty"`
	DBSubnetGroupName   string `json:"DBSubnetGroupName,omitempty"`
	VpcID               string `json:"VpcId,omitempty"`
	// PubliclyAccessible is whether the instance is meant to be reachable from
	// outside the VPC it was placed in. CreateDBInstance always writes one and
	// ModifyDBInstance can change it, so on a live record it is never nil.
	//
	// It is a pointer for the records that predate the field: those never
	// answered the question, and decoding them to false would quietly declare
	// every instance now running private — which, once VPC placement is
	// enforced, is a database that stops answering after an upgrade nobody
	// connected to it. Nil therefore means "unstated" and is resolved by the
	// same rule a create would have used. Read it through
	// PubliclyAccessibleOrDefault rather than dereferencing it.
	PubliclyAccessible *bool `json:"PubliclyAccessible,omitempty"`
	// LastLogs is a bounded tail of the container's own output, kept from the
	// moment the container stopped answering. Docker discards a removed
	// container's logs, and the whole reason anyone opens the logs of a
	// database that would not start is to read the lines that explain why —
	// so they are copied onto the record while they still exist. Bounded by
	// maxRetainedLogBytes; LastLogsAt says when the copy was taken.
	LastLogs   string `json:"LastLogs,omitempty"`
	LastLogsAt string `json:"LastLogsAt,omitempty"`

	// ManageMasterUserPassword records whether RDS generated the master
	// password and stores it in a Secrets Manager secret it owns, rather than
	// the caller supplying MasterUserPassword directly. An Aurora member
	// instance inherits this (and the two fields below) from its cluster —
	// see managed_secret.go and createDBInstanceTyped.
	ManageMasterUserPassword bool `json:"ManageMasterUserPassword,omitempty"`
	// MasterUserSecretARN is the ARN of that secret. Empty when the password
	// is not managed.
	MasterUserSecretARN string `json:"MasterUserSecretARN,omitempty"`
	// MasterUserSecretKmsKeyId is the caller-supplied (or AWS-default,
	// recorded-only — Overcast performs no encryption) KMS key ID associated
	// with the managed secret.
	MasterUserSecretKmsKeyId string `json:"MasterUserSecretKmsKeyId,omitempty"`
}

// PubliclyAccessibleOrDefault reports whether the instance is meant to be
// reachable from outside its VPC, deciding an unstated one exactly as
// CreateDBInstance would have. Every reader — the wire, and anything that
// decides where a container is attached — goes through here, so a stored nil
// cannot mean one thing in a response and another in placement.
func (i *DBInstance) PubliclyAccessibleOrDefault() bool {
	if i.PubliclyAccessible != nil {
		return *i.PubliclyAccessible
	}
	return defaultPubliclyAccessible(i.Engine, i.DBSubnetGroupName)
}

// defaultPubliclyAccessible decides a CreateDBInstance that did not say.
//
// AWS writes the rule in terms of internet gateways. An instance created
// without a DBSubnetGroupName goes into the region's default VPC and is public
// if that VPC has a gateway attached; an instance created into a named subnet
// group is public only if those subnets sit in a VPC that has one.
//
// For non-Aurora RDS instances, the first half resolves to "public" here:
// Overcast seeds its default VPC with an internet gateway and a default route
// through it (internal/services/ec2/default_vpc.go), exactly as AWS does.
// Aurora is the documented exception and defaults to private when no subnet
// group is specified.
//
// The second half is not answerable from RDS: nothing here can see whether a
// subnet group's VPC has a gateway, and VPCNetworkResolver does not offer it.
// So a named subnet group takes AWS's other documented outcome, private. That
// is the commoner shape on AWS — a subnet group is usually built over private
// subnets — and it is the safer half to be wrong about, because being wrong
// is visible (the database does not answer) and the fix is the field itself.
func defaultPubliclyAccessible(engine, dbSubnetGroupName string) bool {
	return !auroraEngines[engine] && dbSubnetGroupName == ""
}

// DBEvent is a stored RDS event — the channel AWS provides for "why did that
// happen to my database". DescribeEvents renders these; nothing else does.
type DBEvent struct {
	SourceIdentifier string    `json:"SourceIdentifier"`
	SourceType       string    `json:"SourceType"`
	SourceArn        string    `json:"SourceArn,omitempty"`
	Message          string    `json:"Message"`
	EventCategories  []string  `json:"EventCategories,omitempty"`
	Date             time.Time `json:"Date"`
}

// DBCluster represents a stored Aurora DB cluster.
//
// It deliberately carries no PubliclyAccessible. On AWS that field belongs to
// the instance: an Aurora cluster has no network placement of its own, and
// CreateDBCluster only accepts it for Multi-AZ DB clusters, the non-Aurora
// deployment. createDBClusterTyped refuses any engine outside auroraEngines,
// so that shape cannot be created here, and the field stays on the members.
type DBCluster struct {
	DBClusterIdentifier string `json:"DBClusterIdentifier"`
	DBClusterArn        string `json:"DBClusterArn"`
	Engine              string `json:"Engine"`
	EngineVersion       string `json:"EngineVersion"`
	Status              string `json:"Status"`
	MasterUsername      string `json:"MasterUsername"`
	// MasterUserPassword is the password the cluster's members answer to. A
	// cluster has no engine of its own, so this is not what a connection uses
	// — it is what tells a later ModifyDBCluster whether the password is
	// really changing, and it is never returned on the wire.
	MasterUserPassword string            `json:"MasterUserPassword,omitempty"`
	DatabaseName       string            `json:"DatabaseName,omitempty"`
	Port               int               `json:"Port"`
	Endpoint           string            `json:"Endpoint,omitempty"`
	ReaderEndpoint     string            `json:"ReaderEndpoint,omitempty"`
	MultiAZ            bool              `json:"MultiAZ"`
	StorageType        string            `json:"StorageType"`
	ClusterCreateTime  string            `json:"ClusterCreateTime,omitempty"`
	DBClusterMembers   []DBClusterMember `json:"DBClusterMembers,omitempty"`
	DBSubnetGroupName  string            `json:"DBSubnetGroup,omitempty"`

	// Settings ModifyDBCluster can change. They are recorded and reported
	// because that is what the AWS management plane does with them, and
	// because a stack update that changes one has to be able to show that it
	// landed. What sits behind them differs:
	//
	//   - DeletionProtection is enforced: DeleteDBCluster refuses a protected
	//     cluster, as AWS does.
	//   - BackupRetentionPeriod is validated against AWS's documented 1-35 and
	//     defaults to 1, then recorded only; PreferredBackupWindow and
	//     PreferredMaintenanceWindow are recorded only — Overcast takes no
	//     backups and has no maintenance window to schedule anything in.
	//   - DBClusterParameterGroup and VpcSecurityGroupIds are recorded only;
	//     neither engine parameters nor security groups are applied to the
	//     containers behind a cluster.
	//   - EnabledCloudwatchLogsExports is recorded only; no engine log is
	//     shipped to CloudWatch Logs.
	//
	// docs/services/rds.md carries the same list for users.
	BackupRetentionPeriod        int      `json:"BackupRetentionPeriod,omitempty"`
	PreferredBackupWindow        string   `json:"PreferredBackupWindow,omitempty"`
	PreferredMaintenanceWindow   string   `json:"PreferredMaintenanceWindow,omitempty"`
	DBClusterParameterGroup      string   `json:"DBClusterParameterGroup,omitempty"`
	VpcSecurityGroupIds          []string `json:"VpcSecurityGroupIds,omitempty"`
	EnabledCloudwatchLogsExports []string `json:"EnabledCloudwatchLogsExports,omitempty"`
	DeletionProtection           bool     `json:"DeletionProtection,omitempty"`

	// ManageMasterUserPassword, MasterUserSecretARN and MasterUserSecretKmsKeyId
	// are managed_secret.go's ManageMasterUserPassword state, at cluster scope
	// — see the identical fields on DBInstance. A member instance created
	// under this cluster inherits all three rather than owning a secret of
	// its own; AWS mints exactly one secret per Aurora cluster, not one per
	// member.
	ManageMasterUserPassword bool   `json:"ManageMasterUserPassword,omitempty"`
	MasterUserSecretARN      string `json:"MasterUserSecretARN,omitempty"`
	MasterUserSecretKmsKeyId string `json:"MasterUserSecretKmsKeyId,omitempty"`
}

// DBClusterMember represents one DB instance that belongs to an Aurora cluster.
type DBClusterMember struct {
	DBInstanceIdentifier          string `json:"DBInstanceIdentifier"`
	IsClusterWriter               bool   `json:"IsClusterWriter"`
	DBClusterParameterGroupStatus string `json:"DBClusterParameterGroupStatus"`
	PromotionTier                 int    `json:"PromotionTier"`
}

// Endpoint represents the connection endpoint for a DB instance.
type Endpoint struct {
	Address string `json:"Address"`
	Port    int    `json:"Port"`
}

// DBSubnetGroup represents a stored RDS DB subnet group.
type DBSubnetGroup struct {
	DBSubnetGroupName        string   `json:"DBSubnetGroupName"`
	DBSubnetGroupDescription string   `json:"DBSubnetGroupDescription"`
	DBSubnetGroupArn         string   `json:"DBSubnetGroupArn"`
	VpcId                    string   `json:"VpcId"`
	SubnetIds                []string `json:"SubnetIds"`
	Status                   string   `json:"Status"`
}

// DBParameterGroup represents a stored RDS DB parameter group.
type DBParameterGroup struct {
	DBParameterGroupName   string `json:"dbParameterGroupName"`
	DBParameterGroupFamily string `json:"dbParameterGroupFamily"`
	Description            string `json:"description"`
	DBParameterGroupArn    string `json:"dbParameterGroupArn"`
}

type rdsStore struct {
	mu            sync.Mutex
	store         state.Store
	defaultRegion string
}

func newRDSStore(store state.Store, defaultRegion string) *rdsStore {
	return &rdsStore{store: store, defaultRegion: defaultRegion}
}

func (s *rdsStore) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.defaultRegion)
}

func (s *rdsStore) putDBInstance(ctx context.Context, inst *DBInstance) *protocol.AWSError {
	raw, err := json.Marshal(inst)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsDBInstances, serviceutil.RegionKey(s.region(ctx), normalizeDBIdentifier(inst.DBInstanceIdentifier)), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *rdsStore) getDBInstance(ctx context.Context, id string) (*DBInstance, *protocol.AWSError) {
	raw, ok, err := s.store.Get(ctx, nsDBInstances, serviceutil.RegionKey(s.region(ctx), normalizeDBIdentifier(id)))
	if err != nil || !ok {
		return nil, errDBInstanceNotFound(id)
	}
	var inst DBInstance
	if err := json.Unmarshal([]byte(raw), &inst); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &inst, nil
}

func (s *rdsStore) deleteDBInstance(ctx context.Context, id string) *protocol.AWSError {
	// The record is read for the ARN its tags are keyed by. A record that has
	// already gone has no tags left to key, so its absence is not an error.
	if inst, aerr := s.getDBInstance(ctx, id); aerr == nil {
		if aerr := s.deleteTags(ctx, inst.DBInstanceArn); aerr != nil {
			return aerr
		}
	}
	if err := s.store.Delete(ctx, nsDBInstances, serviceutil.RegionKey(s.region(ctx), normalizeDBIdentifier(id))); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *rdsStore) listDBInstances(ctx context.Context) ([]*DBInstance, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsDBInstances, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	instances := make([]*DBInstance, 0, len(pairs))
	for _, p := range pairs {
		var inst DBInstance
		if err := json.Unmarshal([]byte(p.Value), &inst); err != nil {
			continue
		}
		instances = append(instances, &inst)
	}
	return instances, nil
}

// ── Event store ───────────────────────────────────────────────────────────────

// putEvent stores one event under a caller-supplied, lexicographically sortable
// key so a Scan comes back in chronological order.
func (s *rdsStore) putEvent(ctx context.Context, key string, e *DBEvent) *protocol.AWSError {
	raw, err := json.Marshal(e)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsEvents, serviceutil.RegionKey(s.region(ctx), key), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// listEvents returns the region's events oldest-first. A record that will not
// decode is skipped rather than failing the whole call — one bad payload must
// not take DescribeEvents down with it.
func (s *rdsStore) listEvents(ctx context.Context) ([]*DBEvent, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsEvents, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	events := make([]*DBEvent, 0, len(pairs))
	for _, p := range pairs {
		var e DBEvent
		if err := json.Unmarshal([]byte(p.Value), &e); err != nil {
			continue
		}
		events = append(events, &e)
	}
	return events, nil
}

// pruneEvents drops the region's events that fall outside the retention window
// or past the count cap, keeping the newest. Without it the event log is the
// one part of RDS state that only ever grows.
func (s *rdsStore) pruneEvents(ctx context.Context, cutoff time.Time, keep int) {
	pairs, err := s.store.Scan(ctx, nsEvents, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return
	}
	// Scan is key-ordered and the keys are timestamp-prefixed, so the surplus
	// oldest entries are simply the front of the slice.
	drop := len(pairs) - keep
	for i, p := range pairs {
		expired := false
		var e DBEvent
		if err := json.Unmarshal([]byte(p.Value), &e); err != nil {
			expired = true // undecodable: it can never be served, so it is litter
		} else if e.Date.Before(cutoff) {
			expired = true
		}
		if !expired && i >= drop {
			continue
		}
		_ = s.store.Delete(ctx, nsEvents, p.Key)
	}
}

// ── Errors ────────────────────────────────────────────────────────────────────

func errDBInstanceNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBInstanceNotFound",
		Message:    fmt.Sprintf("DBInstance %s not found.", id),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errDBInstanceAlreadyExists(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBInstanceAlreadyExists",
		Message:    fmt.Sprintf("DB instance already exists: %s", id),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errInvalidParameterValue(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidParameterValue",
		Message:    msg,
		HTTPStatus: http.StatusBadRequest,
	}
}

func errInvalidParameterCombination(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidParameterCombination",
		Message:    msg,
		HTTPStatus: http.StatusBadRequest,
	}
}

func errInvalidDBInstanceState(id, msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidDBInstanceState",
		Message:    fmt.Sprintf("Instance %s is not in a valid state: %s", id, msg),
		HTTPStatus: http.StatusBadRequest,
	}
}

// errMasterPasswordNotApplied reports a master-password change the engine did
// not take. It carries the engine's own words because that is what tells a
// rejected password apart from a container that was not there to be asked, and
// it is a 500 rather than a 400 because AWS accepts this modification — the
// failure is Overcast's, not the caller's.
func errMasterPasswordNotApplied(id, detail string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InternalError",
		Message:    fmt.Sprintf("The master password for DB instance %s was not changed: %s", id, detail),
		HTTPStatus: http.StatusInternalServerError,
	}
}

// errDBSubnetGroupNotFound is a 404, not a 400. AWS documents
// DBSubnetGroupNotFoundFault as HTTP 404 on every operation that raises it —
// CreateDBCluster, CreateDBInstance, DescribeDBSubnetGroups and
// DeleteDBSubnetGroup among them — and an SDK that branches on the status
// rather than the code treats a 400 as a malformed request it should not
// retry or handle as "absent".
func errDBSubnetGroupNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBSubnetGroupNotFoundFault",
		Message:    fmt.Sprintf("DBSubnetGroup '%s' not found.", name),
		HTTPStatus: http.StatusNotFound,
	}
}

func errDBSubnetGroupAlreadyExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBSubnetGroupAlreadyExistsFault",
		Message:    fmt.Sprintf("DB Subnet Group '%s' already exists.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errDBParameterGroupNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBParameterGroupNotFound",
		Message:    fmt.Sprintf("DBParameterGroup '%s' not found.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errDBParameterGroupAlreadyExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBParameterGroupAlreadyExists",
		Message:    fmt.Sprintf("A DB parameter group named '%s' already exists.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errDBClusterNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBClusterNotFoundFault",
		Message:    fmt.Sprintf("DBCluster %s not found.", id),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errDBClusterAlreadyExists(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DBClusterAlreadyExistsFault",
		Message:    fmt.Sprintf("DB cluster already exists: %s", id),
		HTTPStatus: http.StatusBadRequest,
	}
}

// ── Port allocation ───────────────────────────────────────────────────────────

// portAllocationSpan is how many ports above the base allocatePort will
// consider. Named because it is also what says which ports a given base can
// ever produce, which is not only allocatePort's business.
const portAllocationSpan = 1000

// allocatePort scans existing port allocations and claims the first free port
// in [portBase, portBase+portAllocationSpan). Protected by a mutex to prevent
// concurrent callers from claiming the same port.
func (s *rdsStore) allocatePort(ctx context.Context, instanceID string, portBase int) (int, *protocol.AWSError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pairs, err := s.store.Scan(ctx, nsPorts, "")
	if err != nil {
		return 0, protocol.Wrap(protocol.ErrInternalError, err)
	}

	used := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		port, parseErr := strconv.Atoi(p.Key)
		if parseErr == nil {
			used[port] = true
		}
	}

	for port := portBase; port < portBase+portAllocationSpan; port++ {
		if !used[port] {
			if err := s.store.Set(ctx, nsPorts, strconv.Itoa(port), instanceID); err != nil {
				return 0, protocol.Wrap(protocol.ErrInternalError, err)
			}
			return port, nil
		}
	}
	return 0, &protocol.AWSError{
		Code:       "InternalFailure",
		Message:    "no free port available for RDS instance",
		HTTPStatus: http.StatusInternalServerError,
	}
}

func (s *rdsStore) releasePort(ctx context.Context, port int) *protocol.AWSError {
	if err := s.store.Delete(ctx, nsPorts, strconv.Itoa(port)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// allocatePortFixed records a specific port as allocated for an instance.
// Used when reusing an existing container whose host port is already known.
// If the port is already recorded (idempotent), the existing record is overwritten.
func (s *rdsStore) allocatePortFixed(ctx context.Context, instanceID string, port int) *protocol.AWSError {
	if err := s.store.Set(ctx, nsPorts, strconv.Itoa(port), instanceID); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// ── SubnetGroup store ─────────────────────────────────────────────────────────

func (s *rdsStore) putDBSubnetGroup(ctx context.Context, sg *DBSubnetGroup) *protocol.AWSError {
	raw, err := json.Marshal(sg)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsSubnetGroups, serviceutil.RegionKey(s.region(ctx), sg.DBSubnetGroupName), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *rdsStore) getDBSubnetGroup(ctx context.Context, name string) (*DBSubnetGroup, *protocol.AWSError) {
	raw, ok, err := s.store.Get(ctx, nsSubnetGroups, serviceutil.RegionKey(s.region(ctx), name))
	if err != nil || !ok {
		return nil, errDBSubnetGroupNotFound(name)
	}
	var sg DBSubnetGroup
	if err := json.Unmarshal([]byte(raw), &sg); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &sg, nil
}

func (s *rdsStore) listDBSubnetGroups(ctx context.Context) ([]*DBSubnetGroup, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsSubnetGroups, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	groups := make([]*DBSubnetGroup, 0, len(pairs))
	for _, p := range pairs {
		var sg DBSubnetGroup
		if err := json.Unmarshal([]byte(p.Value), &sg); err != nil {
			continue
		}
		groups = append(groups, &sg)
	}
	return groups, nil
}

func (s *rdsStore) deleteDBSubnetGroup(ctx context.Context, name string) *protocol.AWSError {
	if sg, aerr := s.getDBSubnetGroup(ctx, name); aerr == nil {
		if aerr := s.deleteTags(ctx, sg.DBSubnetGroupArn); aerr != nil {
			return aerr
		}
	}
	key := serviceutil.RegionKey(s.region(ctx), name)
	if err := s.store.Delete(ctx, nsSubnetGroups, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// ── ParameterGroup store ──────────────────────────────────────────────────────

func (s *rdsStore) putDBParameterGroup(ctx context.Context, pg *DBParameterGroup) *protocol.AWSError {
	raw, err := json.Marshal(pg)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsParameterGroups, serviceutil.RegionKey(s.region(ctx), pg.DBParameterGroupName), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *rdsStore) getDBParameterGroup(ctx context.Context, name string) (*DBParameterGroup, *protocol.AWSError) {
	raw, ok, err := s.store.Get(ctx, nsParameterGroups, serviceutil.RegionKey(s.region(ctx), name))
	if err != nil || !ok {
		return nil, errDBParameterGroupNotFound(name)
	}
	var pg DBParameterGroup
	if err := json.Unmarshal([]byte(raw), &pg); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &pg, nil
}

func (s *rdsStore) listDBParameterGroups(ctx context.Context) ([]*DBParameterGroup, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsParameterGroups, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	groups := make([]*DBParameterGroup, 0, len(pairs))
	for _, p := range pairs {
		var pg DBParameterGroup
		if err := json.Unmarshal([]byte(p.Value), &pg); err != nil {
			continue
		}
		groups = append(groups, &pg)
	}
	return groups, nil
}

func (s *rdsStore) deleteDBParameterGroup(ctx context.Context, name string) *protocol.AWSError {
	if pg, aerr := s.getDBParameterGroup(ctx, name); aerr == nil {
		if aerr := s.deleteTags(ctx, pg.DBParameterGroupArn); aerr != nil {
			return aerr
		}
	}
	key := serviceutil.RegionKey(s.region(ctx), name)
	if err := s.store.Delete(ctx, nsParameterGroups, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// ── Tags store ────────────────────────────────────────────────────────────────

// tags is the namespace resource tags live in. They are keyed by the resource's
// own ARN, which is the string a caller passes as ResourceName.
func (s *rdsStore) tags() *serviceutil.NSStore {
	return &serviceutil.NSStore{Store: s.store, NS: nsTags}
}

// deleteTags removes the tags of a resource that is going away.
//
// Nothing ties a tag blob to the lifetime of the record it describes, so a
// delete path that forgets this leaves ListTagsForResource answering for a
// resource that no longer exists, and leaves the blob in the store for the rest
// of the session. Every delete path below calls it, so a new one is a visibly
// missing line rather than a silent leak.
//
// An untagged resource is not a special case: deleting a key that was never
// written is a no-op. An empty ARN is — it would name the whole namespace's
// zero key rather than this resource.
func (s *rdsStore) deleteTags(ctx context.Context, arn string) *protocol.AWSError {
	if arn == "" {
		return nil
	}
	return s.tags().Delete(ctx, arn)
}

// ── Cluster store ─────────────────────────────────────────────────────────────

func (s *rdsStore) putDBCluster(ctx context.Context, c *DBCluster) *protocol.AWSError {
	raw, err := json.Marshal(c)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsClusters, serviceutil.RegionKey(s.region(ctx), normalizeDBIdentifier(c.DBClusterIdentifier)), string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *rdsStore) getDBCluster(ctx context.Context, id string) (*DBCluster, *protocol.AWSError) {
	raw, ok, err := s.store.Get(ctx, nsClusters, serviceutil.RegionKey(s.region(ctx), normalizeDBIdentifier(id)))
	if err != nil || !ok {
		return nil, errDBClusterNotFound(id)
	}
	var c DBCluster
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &c, nil
}

func (s *rdsStore) deleteDBCluster(ctx context.Context, id string) *protocol.AWSError {
	if c, aerr := s.getDBCluster(ctx, id); aerr == nil {
		if aerr := s.deleteTags(ctx, c.DBClusterArn); aerr != nil {
			return aerr
		}
	}
	if err := s.store.Delete(ctx, nsClusters, serviceutil.RegionKey(s.region(ctx), normalizeDBIdentifier(id))); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *rdsStore) listDBClusters(ctx context.Context) ([]*DBCluster, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsClusters, serviceutil.RegionKey(s.region(ctx), ""))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	clusters := make([]*DBCluster, 0, len(pairs))
	for _, p := range pairs {
		var c DBCluster
		if err := json.Unmarshal([]byte(p.Value), &c); err != nil {
			continue
		}
		clusters = append(clusters, &c)
	}
	return clusters, nil
}
