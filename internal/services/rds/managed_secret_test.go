package rds

// managed_secret_test.go — ManageMasterUserPassword: CreateDBInstance,
// CreateDBCluster, ModifyDBInstance and ModifyDBCluster generating a password
// and creating/removing the Secrets Manager secret behind it, in-process,
// through the SecretsManagerAccess seam rather than a real Secrets Manager
// service. See tests/integration/rds for the end-to-end path through a real
// Secrets Manager GetSecretValue.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// stubSecretsManager is an in-memory SecretsManagerAccess good enough to
// exercise RDS's side of the seam without a real Secrets Manager service.
type stubSecretsManager struct {
	mu      sync.Mutex
	secrets map[string]string // ARN -> SecretString
	created []string          // secret names, in call order
	deleted []string          // secret IDs passed to DeleteManagedSecret

	failCreate *protocol.AWSError
}

func newStubSecretsManager() *stubSecretsManager {
	return &stubSecretsManager{secrets: map[string]string{}}
}

func (s *stubSecretsManager) CreateManagedSecret(_ context.Context, name, _ string, secretString string) (string, *protocol.AWSError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCreate != nil {
		return "", s.failCreate
	}
	arn := "arn:aws:secretsmanager:us-east-1:123456789012:secret:" + name + "-a1b2c3"
	s.secrets[arn] = secretString
	s.created = append(s.created, name)
	return arn, nil
}

func (s *stubSecretsManager) DeleteManagedSecret(_ context.Context, secretID string) *protocol.AWSError {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, secretID)
	s.deleted = append(s.deleted, secretID)
	return nil
}

func newManagedSecretTestHandler(t *testing.T) (*Handler, *stubSecretsManager) {
	t.Helper()
	h := newClusterTestHandler(t)
	sm := newStubSecretsManager()
	h.secretsManager = sm
	return h, sm
}

// ── CreateDBInstance ─────────────────────────────────────────────────────────

func TestCreateDBInstance_managedMasterPassword(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	manage := true

	resp, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier:     "managed-instance",
		Engine:                   "mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	})
	if aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	sec := resp.Result.DBInstance.MasterUserSecret
	if sec == nil {
		t.Fatal("CreateDBInstance response carries no MasterUserSecret for a managed instance")
	}
	if sec.SecretArn == "" {
		t.Error("MasterUserSecret.SecretArn is empty")
	}
	if sec.SecretStatus != "active" {
		t.Errorf("MasterUserSecret.SecretStatus = %q, want \"active\"", sec.SecretStatus)
	}

	inst, aerr := h.store.getDBInstance(context.Background(), "managed-instance")
	if aerr != nil {
		t.Fatalf("getDBInstance: %s", aerr.Message)
	}
	if !inst.ManageMasterUserPassword {
		t.Error("stored record does not record ManageMasterUserPassword")
	}
	if inst.MasterUserSecretARN != sec.SecretArn {
		t.Errorf("stored MasterUserSecretARN = %q, want %q", inst.MasterUserSecretARN, sec.SecretArn)
	}
	if inst.MasterUserPassword == "" {
		t.Error("no password was generated and stored — the container has nothing to boot with")
	}
	if aerr := validateMasterUserPassword("mysql", inst.MasterUserPassword); aerr != nil {
		t.Errorf("generated password fails RDS's own validation: %s", aerr.Message)
	}

	if len(sm.created) != 1 {
		t.Fatalf("CreateManagedSecret called %d times, want 1: %v", len(sm.created), sm.created)
	}
	if !strings.HasPrefix(sm.created[0], "rds!db-") {
		t.Errorf("secret name %q does not follow AWS's rds!db-<uuid> scheme", sm.created[0])
	}
	// json.Marshal HTML-escapes characters like '&', '<', '>' by default, so a
	// generated password containing one of those must be compared decoded
	// rather than as a raw substring of the JSON payload.
	var payload managedSecretJSON
	if err := json.Unmarshal([]byte(sm.secrets[sec.SecretArn]), &payload); err != nil {
		t.Fatalf("secret payload is not valid JSON: %v (%q)", err, sm.secrets[sec.SecretArn])
	}
	if payload.Password != inst.MasterUserPassword {
		t.Errorf("secret payload password = %q, want the generated password %q", payload.Password, inst.MasterUserPassword)
	}
	if payload.Username != "admin" {
		t.Errorf("secret payload username = %q, want %q", payload.Username, "admin")
	}
}

// A caller-supplied MasterUserPassword and ManageMasterUserPassword=true
// contradict each other: AWS is being asked to both generate the password
// and accept one from the caller.
func TestCreateDBInstance_managedMasterPasswordRejectsExplicitPassword(t *testing.T) {
	h, _ := newManagedSecretTestHandler(t)
	manage := true

	_, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier:     "conflict-instance",
		Engine:                   "mysql",
		MasterUsername:           "admin",
		MasterUserPassword:       "explicit-password1",
		ManageMasterUserPassword: &manage,
	})
	if aerr == nil {
		t.Fatal("CreateDBInstance accepted MasterUserPassword together with ManageMasterUserPassword=true")
	}
	if aerr.Code != "InvalidParameterCombination" {
		t.Errorf("error code = %q, want InvalidParameterCombination", aerr.Code)
	}
}

// Without ManageMasterUserPassword the usual rule still applies: a password
// is required. This guards against the refactor accidentally making it
// optional for every create.
func TestCreateDBInstance_unmanagedStillRequiresPassword(t *testing.T) {
	h, _ := newManagedSecretTestHandler(t)

	_, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier: "no-password",
		Engine:               "mysql",
		MasterUsername:       "admin",
	})
	if aerr == nil {
		t.Fatal("CreateDBInstance accepted a create with no MasterUserPassword and ManageMasterUserPassword unset")
	}
	if aerr.Code != "InvalidParameterValue" {
		t.Errorf("error code = %q, want InvalidParameterValue", aerr.Code)
	}
}

// An Aurora member must never mint its own secret — AWS creates exactly one
// per cluster.
func TestCreateDBInstance_auroraMemberInheritsClusterSecret(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	manage := true

	if _, aerr := h.createDBClusterTyped(context.Background(), &createDBClusterReq{
		DBClusterIdentifier:      "managed-cluster",
		Engine:                   "aurora-mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	}); aerr != nil {
		t.Fatalf("CreateDBCluster: %s: %s", aerr.Code, aerr.Message)
	}
	if len(sm.created) != 1 {
		t.Fatalf("cluster create called CreateManagedSecret %d times, want 1", len(sm.created))
	}

	resp, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier: "managed-cluster-member",
		Engine:               "aurora-mysql",
		DBClusterIdentifier:  "managed-cluster",
	})
	if aerr != nil {
		t.Fatalf("CreateDBInstance (member): %s: %s", aerr.Code, aerr.Message)
	}

	// No second secret was minted for the member.
	if len(sm.created) != 1 {
		t.Fatalf("adding a cluster member called CreateManagedSecret again: %v", sm.created)
	}

	cluster, aerr := h.store.getDBCluster(context.Background(), "managed-cluster")
	if aerr != nil {
		t.Fatalf("getDBCluster: %s", aerr.Message)
	}
	if resp.Result.DBInstance.MasterUserSecret == nil {
		t.Fatal("member instance response carries no MasterUserSecret")
	}
	if resp.Result.DBInstance.MasterUserSecret.SecretArn != cluster.MasterUserSecretARN {
		t.Errorf("member's MasterUserSecret.SecretArn = %q, want the cluster's %q",
			resp.Result.DBInstance.MasterUserSecret.SecretArn, cluster.MasterUserSecretARN)
	}
}

// ── DeleteDBInstance / DeleteDBCluster ──────────────────────────────────────

func TestDeleteDBInstance_deletesTheManagedSecret(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	manage := true
	if _, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier:     "delete-me",
		Engine:                   "mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	}); aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}
	inst, aerr := h.store.getDBInstance(context.Background(), "delete-me")
	if aerr != nil {
		t.Fatalf("getDBInstance: %s", aerr.Message)
	}

	if _, aerr := h.deleteDBInstanceTyped(context.Background(), &deleteDBInstanceReq{DBInstanceIdentifier: "delete-me"}); aerr != nil {
		t.Fatalf("DeleteDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	sm.mu.Lock()
	deleted := append([]string(nil), sm.deleted...)
	sm.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != inst.MasterUserSecretARN {
		t.Errorf("DeleteManagedSecret calls = %v, want exactly [%q]", deleted, inst.MasterUserSecretARN)
	}
}

// Deleting one cluster member must not delete the cluster's shared secret —
// only DeleteDBCluster owns that.
func TestDeleteDBInstance_clusterMemberDoesNotDeleteTheClusterSecret(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	manage := true
	if _, aerr := h.createDBClusterTyped(context.Background(), &createDBClusterReq{
		DBClusterIdentifier:      "member-delete-cluster",
		Engine:                   "aurora-mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	}); aerr != nil {
		t.Fatalf("CreateDBCluster: %s: %s", aerr.Code, aerr.Message)
	}
	if _, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier: "member-delete-instance",
		Engine:               "aurora-mysql",
		DBClusterIdentifier:  "member-delete-cluster",
	}); aerr != nil {
		t.Fatalf("CreateDBInstance (member): %s: %s", aerr.Code, aerr.Message)
	}

	if _, aerr := h.deleteDBInstanceTyped(context.Background(), &deleteDBInstanceReq{DBInstanceIdentifier: "member-delete-instance"}); aerr != nil {
		t.Fatalf("DeleteDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	sm.mu.Lock()
	deleted := len(sm.deleted)
	sm.mu.Unlock()
	if deleted != 0 {
		t.Errorf("deleting a cluster member called DeleteManagedSecret %d times, want 0", deleted)
	}
}

func TestDeleteDBCluster_deletesTheManagedSecret(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	manage := true
	if _, aerr := h.createDBClusterTyped(context.Background(), &createDBClusterReq{
		DBClusterIdentifier:      "cluster-delete-me",
		Engine:                   "aurora-mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	}); aerr != nil {
		t.Fatalf("CreateDBCluster: %s: %s", aerr.Code, aerr.Message)
	}
	cluster, aerr := h.store.getDBCluster(context.Background(), "cluster-delete-me")
	if aerr != nil {
		t.Fatalf("getDBCluster: %s", aerr.Message)
	}

	if _, aerr := h.deleteDBClusterTyped(context.Background(), &deleteDBClusterReq{DBClusterIdentifier: "cluster-delete-me"}); aerr != nil {
		t.Fatalf("DeleteDBCluster: %s: %s", aerr.Code, aerr.Message)
	}

	sm.mu.Lock()
	deleted := append([]string(nil), sm.deleted...)
	sm.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != cluster.MasterUserSecretARN {
		t.Errorf("DeleteManagedSecret calls = %v, want exactly [%q]", deleted, cluster.MasterUserSecretARN)
	}
}

// ── ModifyDBInstance ─────────────────────────────────────────────────────────

// Turning management on generates a password and a secret, exactly like
// create.
func TestModifyDBInstance_turnManagementOn(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	if _, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier: "modify-on",
		Engine:               "mysql",
		MasterUsername:       "admin",
		MasterUserPassword:   "originalpass1",
	}); aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	manage := true
	resp, aerr := h.modifyDBInstanceTyped(context.Background(), &modifyDBInstanceReq{
		DBInstanceIdentifier:     "modify-on",
		ManageMasterUserPassword: &manage,
	})
	if aerr != nil {
		t.Fatalf("ModifyDBInstance: %s: %s", aerr.Code, aerr.Message)
	}
	if resp.Result.DBInstance.MasterUserSecret == nil {
		t.Fatal("ModifyDBInstance response carries no MasterUserSecret after turning management on")
	}
	if len(sm.created) != 1 {
		t.Fatalf("CreateManagedSecret called %d times, want 1", len(sm.created))
	}

	inst, aerr := h.store.getDBInstance(context.Background(), "modify-on")
	if aerr != nil {
		t.Fatalf("getDBInstance: %s", aerr.Message)
	}
	if inst.MasterUserPassword == "originalpass1" {
		t.Error("turning management on did not generate a new password")
	}
	if !inst.ManageMasterUserPassword || inst.MasterUserSecretARN == "" {
		t.Error("stored record does not reflect the new managed-secret state")
	}
}

// Supplying MasterUserPassword together with ManageMasterUserPassword=true is
// the same contradiction ModifyDBInstance's create-time counterpart refuses.
func TestModifyDBInstance_turnManagementOnRejectsExplicitPassword(t *testing.T) {
	h, _ := newManagedSecretTestHandler(t)
	if _, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier: "modify-on-conflict",
		Engine:               "mysql",
		MasterUsername:       "admin",
		MasterUserPassword:   "originalpass1",
	}); aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	manage := true
	_, aerr := h.modifyDBInstanceTyped(context.Background(), &modifyDBInstanceReq{
		DBInstanceIdentifier:     "modify-on-conflict",
		ManageMasterUserPassword: &manage,
		MasterUserPassword:       "some-other-pass1",
	})
	if aerr == nil {
		t.Fatal("ModifyDBInstance accepted MasterUserPassword together with ManageMasterUserPassword=true")
	}
	if aerr.Code != "InvalidParameterCombination" {
		t.Errorf("error code = %q, want InvalidParameterCombination", aerr.Code)
	}
}

// Turning management off requires the caller's own password and drops the
// record's pointer to the (left-in-place) secret.
func TestModifyDBInstance_turnManagementOff(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	manage := true
	if _, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier:     "modify-off",
		Engine:                   "mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	}); aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}
	managedSecretARN, aerr := h.store.getDBInstance(context.Background(), "modify-off")
	if aerr != nil {
		t.Fatalf("getDBInstance: %s", aerr.Message)
	}
	oldARN := managedSecretARN.MasterUserSecretARN

	unmanage := false
	resp, aerr := h.modifyDBInstanceTyped(context.Background(), &modifyDBInstanceReq{
		DBInstanceIdentifier:     "modify-off",
		ManageMasterUserPassword: &unmanage,
		MasterUserPassword:       "caller-owned-pass1",
	})
	if aerr != nil {
		t.Fatalf("ModifyDBInstance: %s: %s", aerr.Code, aerr.Message)
	}
	if resp.Result.DBInstance.MasterUserSecret != nil {
		t.Error("response still carries MasterUserSecret after turning management off")
	}

	inst, aerr := h.store.getDBInstance(context.Background(), "modify-off")
	if aerr != nil {
		t.Fatalf("getDBInstance: %s", aerr.Message)
	}
	if inst.ManageMasterUserPassword {
		t.Error("stored record still says the password is managed")
	}
	if inst.MasterUserSecretARN != "" {
		t.Errorf("stored MasterUserSecretARN = %q, want empty after turning management off", inst.MasterUserSecretARN)
	}
	if inst.MasterUserPassword != "caller-owned-pass1" {
		t.Errorf("stored MasterUserPassword = %q, want the caller-supplied value", inst.MasterUserPassword)
	}

	// AWS: "Amazon RDS deletes the secret and uses the new password for the
	// master user specified by MasterUserPassword."
	sm.mu.Lock()
	_, stillThere := sm.secrets[oldARN]
	sm.mu.Unlock()
	if stillThere {
		t.Error("the secret survived; ModifyDBInstance(ManageMasterUserPassword=false) should delete it")
	}
}

// Turning management off without a replacement password leaves the instance
// with no password anyone (except RDS, whose secret is no longer trusted)
// knows — AWS refuses this.
func TestModifyDBInstance_turnManagementOffRequiresPassword(t *testing.T) {
	h, _ := newManagedSecretTestHandler(t)
	manage := true
	if _, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier:     "modify-off-nopass",
		Engine:                   "mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	}); aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	unmanage := false
	_, aerr := h.modifyDBInstanceTyped(context.Background(), &modifyDBInstanceReq{
		DBInstanceIdentifier:     "modify-off-nopass",
		ManageMasterUserPassword: &unmanage,
	})
	if aerr == nil {
		t.Fatal("ModifyDBInstance turned management off with no MasterUserPassword")
	}
	if aerr.Code != "InvalidParameterValue" {
		t.Errorf("error code = %q, want InvalidParameterValue", aerr.Code)
	}
}

// While managed, MasterUserPassword cannot be set directly without also
// turning management off.
func TestModifyDBInstance_managedRejectsDirectPasswordWithoutTurningOff(t *testing.T) {
	h, _ := newManagedSecretTestHandler(t)
	manage := true
	if _, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier:     "modify-managed-direct",
		Engine:                   "mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	}); aerr != nil {
		t.Fatalf("CreateDBInstance: %s: %s", aerr.Code, aerr.Message)
	}

	_, aerr := h.modifyDBInstanceTyped(context.Background(), &modifyDBInstanceReq{
		DBInstanceIdentifier: "modify-managed-direct",
		MasterUserPassword:   "sneaky-direct-pass1",
	})
	if aerr == nil {
		t.Fatal("ModifyDBInstance accepted a direct MasterUserPassword on a managed instance")
	}
	if aerr.Code != "InvalidParameterCombination" {
		t.Errorf("error code = %q, want InvalidParameterCombination", aerr.Code)
	}
}

// ── ModifyDBCluster ──────────────────────────────────────────────────────────

func TestModifyDBCluster_turnManagementOnAndOff(t *testing.T) {
	h, sm := newManagedSecretTestHandler(t)
	if _, aerr := h.createDBClusterTyped(context.Background(), &createDBClusterReq{
		DBClusterIdentifier: "cluster-modify",
		Engine:              "aurora-mysql",
		MasterUsername:      "admin",
		MasterUserPassword:  "originalpass1",
	}); aerr != nil {
		t.Fatalf("CreateDBCluster: %s: %s", aerr.Code, aerr.Message)
	}

	manage := true
	resp, aerr := h.modifyDBClusterTyped(context.Background(), &modifyDBClusterReq{
		DBClusterIdentifier:      "cluster-modify",
		ManageMasterUserPassword: &manage,
	})
	if aerr != nil {
		t.Fatalf("ModifyDBCluster (on): %s: %s", aerr.Code, aerr.Message)
	}
	if resp.Result.DBCluster.MasterUserSecret == nil {
		t.Fatal("ModifyDBCluster response carries no MasterUserSecret after turning management on")
	}
	if len(sm.created) != 1 || !strings.HasPrefix(sm.created[0], "rds!cluster-") {
		t.Fatalf("CreateManagedSecret calls = %v, want exactly one rds!cluster-<uuid> secret", sm.created)
	}

	unmanage := false
	resp, aerr = h.modifyDBClusterTyped(context.Background(), &modifyDBClusterReq{
		DBClusterIdentifier:      "cluster-modify",
		ManageMasterUserPassword: &unmanage,
		MasterUserPassword:       "caller-owned-pass1",
	})
	if aerr != nil {
		t.Fatalf("ModifyDBCluster (off): %s: %s", aerr.Code, aerr.Message)
	}
	if resp.Result.DBCluster.MasterUserSecret != nil {
		t.Error("response still carries MasterUserSecret after turning management off")
	}

	cluster, aerr := h.store.getDBCluster(context.Background(), "cluster-modify")
	if aerr != nil {
		t.Fatalf("getDBCluster: %s", aerr.Message)
	}
	if cluster.ManageMasterUserPassword || cluster.MasterUserSecretARN != "" {
		t.Error("stored cluster record still reflects managed state after turning it off")
	}
}

// ── Secrets Manager not wired ───────────────────────────────────────────────

// A handler nothing wired to Secrets Manager refuses a managed-password
// request rather than silently creating an instance with a password nobody
// can retrieve.
func TestCreateDBInstance_managedMasterPasswordWithoutSecretsManagerWired(t *testing.T) {
	h := newClusterTestHandler(t) // no SecretsManagerAccess set
	manage := true

	_, aerr := h.createDBInstanceTyped(context.Background(), &createDBInstanceReq{
		DBInstanceIdentifier:     "unwired",
		Engine:                   "mysql",
		MasterUsername:           "admin",
		ManageMasterUserPassword: &manage,
	})
	if aerr == nil {
		t.Fatal("CreateDBInstance succeeded with ManageMasterUserPassword=true and no Secrets Manager wired")
	}

	if _, getErr := h.store.getDBInstance(context.Background(), "unwired"); getErr == nil {
		t.Error("a rejected managed-password create left an instance record behind")
	}
}
