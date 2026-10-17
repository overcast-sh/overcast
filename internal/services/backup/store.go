package backup

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	nsVaults       = "backup:vaults"
	nsPlans        = "backup:plans"
	nsAccessPoints = "backup:accesspoints"
	nsSelections   = "backup:selections"
)

// vaultRecord is a backup vault as it is persisted. It is deliberately not the
// wire shape: CreationDate is an RFC 3339 string here (what time.Time
// marshals to, and what every already-stored record holds) while restJson1
// puts epoch seconds on the wire. Keeping the two apart is what lets the wire
// encoding be corrected without a migration.
type vaultRecord struct {
	BackupVaultName  string    `json:"BackupVaultName"`
	BackupVaultArn   string    `json:"BackupVaultArn"`
	CreationDate     time.Time `json:"CreationDate"`
	EncryptionKeyArn string    `json:"EncryptionKeyArn,omitempty"`
	CreatorRequestID string    `json:"CreatorRequestId,omitempty"`
	// Tags is stored inline so it dies with the vault record rather than in a
	// side namespace nothing tears down on delete (#1195, #1037's "tags die
	// with their resource" rule). Populated from BackupVaultTags at create
	// time and mutated by TagResource/UntagResource thereafter.
	Tags map[string]string `json:"Tags,omitempty"`
}

// planRecord is a backup plan as it is persisted.
//
// Version is the plan's version counter, rendered on the wire as an opaque
// VersionId. Records written before #815 carry a "VersionId" string instead
// and decode with Version 0, which versionID reads as version 1 — the value
// those records were written with.
type planRecord struct {
	BackupPlanID   string           `json:"BackupPlanId"`
	BackupPlanArn  string           `json:"BackupPlanArn"`
	BackupPlanName string           `json:"BackupPlanName"`
	Rules          []map[string]any `json:"Rules,omitempty"`
	Version        int              `json:"Version"`
	CreationDate   time.Time        `json:"CreationDate"`
	// Tags is stored inline for the same reason as vaultRecord.Tags above:
	// populated from BackupPlanTags at create time (CreateBackupPlan is the
	// only operation that accepts it — UpdateBackupPlan does not model tags),
	// mutated by TagResource/UntagResource thereafter.
	Tags map[string]string `json:"Tags,omitempty"`
}

// accessPointRecord is a backup access point as it is persisted (#1467).
//
// The record holds only what the client supplied. On AWS, BackupVaultName,
// BackupVaultArn, ResourceArn and ResourceType are all read off the recovery
// point the access point was created against; Overcast runs no backup jobs, so
// no recovery point exists for RecoveryPointArn to resolve to and none of the
// four can be derived. They are absent from the wire shape rather than
// invented — see wireAccessPoint in handler_access_points.go.
//
// AccessPointPolicy is deliberately not here: CreateBackupAccessPoint accepts
// one, no operation returns it, and nothing behind this control plane enforces
// an access policy, so persisting it would store data nothing can ever read.
type accessPointRecord struct {
	Name             string            `json:"Name"`
	AccessPointArn   string            `json:"AccessPointArn"`
	RecoveryPointArn string            `json:"RecoveryPointArn"`
	Metadata         map[string]string `json:"AccessPointMetadata,omitempty"`
	CreationTime     time.Time         `json:"CreationTime"`
	// Tags is stored inline for the same reason as vaultRecord.Tags above:
	// populated from CreateBackupAccessPoint's Tags member and mutated by
	// TagResource/UntagResource thereafter.
	Tags map[string]string `json:"Tags,omitempty"`
}

// backupStore is every read and write Backup makes against state.Store.
//
// Keys are region-scoped. AWS states the constraint on CreateBackupVault:
// vault names are unique "per account per region", and a plan's ARN names the
// region it was created in — so two regions may legitimately hold the same
// name and a request must only ever see its own region's. Records written
// before #815 are keyed by bare name and are therefore not read: state that
// predates a key reshape is lost on upgrade, which CONTRIBUTING.md
// § JSON compatibility permits for a local dev tool with no durability
// guarantee, and which costs nothing here because no SDK could ever have
// written one.
type backupStore struct {
	store state.Store
	log   *serviceutil.ServiceLogger
}

func newBackupStore(st state.Store, log *serviceutil.ServiceLogger) *backupStore {
	return &backupStore{store: st, log: log}
}

// ─── Generic record access ────────────────────────────────────
//
// All three record types are region-keyed JSON blobs in the generic kv table,
// which both MemoryStore and SQLiteStore serve without a per-service backend —
// no namespace here has earned a dedicated table (CONTRIBUTING § Data earns a
// table). The four operations below are therefore the same four for every
// record type, and are written once rather than copied per type.

func putRecord[T any](ctx context.Context, s *backupStore, ns, region, id string, rec *T) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, ns, serviceutil.RegionKey(region, id), string(raw))
}

// getRecord returns the record called id in region.
//
// A record that cannot be decoded reads as absent rather than as a failure:
// one bad payload must not turn a describe into a 500, and
// ResourceNotFoundException is the answer AWS models for a name the service
// cannot produce (AGENTS.md § malformed persisted state must be isolated).
func getRecord[T any](ctx context.Context, s *backupStore, ns, kind, region, id string) (*T, bool, error) {
	raw, found, err := s.store.Get(ctx, ns, serviceutil.RegionKey(region, id))
	if err != nil || !found {
		return nil, false, err
	}
	var rec T
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		s.log.Warn("skipping undecodable record", zap.String("kind", kind), zap.String("id", id), zap.Error(err))
		return nil, false, nil
	}
	return &rec, true, nil
}

// listRecords returns every record of one kind in region, skipping records
// that cannot be decoded so that one bad payload does not fail the whole
// listing.
func listRecords[T any](ctx context.Context, s *backupStore, ns, kind, region string) ([]T, error) {
	pairs, err := s.store.Scan(ctx, ns, serviceutil.RegionKey(region, ""))
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(pairs))
	for _, kv := range pairs {
		var rec T
		if err := json.Unmarshal([]byte(kv.Value), &rec); err != nil {
			s.log.Warn("skipping undecodable record", zap.String("kind", kind), zap.String("key", kv.Key), zap.Error(err))
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *backupStore) deleteRecord(ctx context.Context, ns, region, id string) error {
	return s.store.Delete(ctx, ns, serviceutil.RegionKey(region, id))
}

// ─── Vaults ───────────────────────────────────────────────────

func (s *backupStore) putVault(ctx context.Context, region string, v *vaultRecord) error {
	return putRecord(ctx, s, nsVaults, region, v.BackupVaultName, v)
}

func (s *backupStore) getVault(ctx context.Context, region, name string) (*vaultRecord, bool, error) {
	return getRecord[vaultRecord](ctx, s, nsVaults, "vault", region, name)
}

func (s *backupStore) listVaults(ctx context.Context, region string) ([]vaultRecord, error) {
	return listRecords[vaultRecord](ctx, s, nsVaults, "vault", region)
}

func (s *backupStore) deleteVault(ctx context.Context, region, name string) error {
	return s.deleteRecord(ctx, nsVaults, region, name)
}

// ─── Plans ────────────────────────────────────────────────────

func (s *backupStore) putPlan(ctx context.Context, region string, p *planRecord) error {
	return putRecord(ctx, s, nsPlans, region, p.BackupPlanID, p)
}

func (s *backupStore) getPlan(ctx context.Context, region, id string) (*planRecord, bool, error) {
	return getRecord[planRecord](ctx, s, nsPlans, "plan", region, id)
}

func (s *backupStore) listPlans(ctx context.Context, region string) ([]planRecord, error) {
	return listRecords[planRecord](ctx, s, nsPlans, "plan", region)
}

func (s *backupStore) deletePlan(ctx context.Context, region, id string) error {
	return s.deleteRecord(ctx, nsPlans, region, id)
}

// ─── Access points ────────────────────────────────────────────
//
// Keyed by Name, which AWS states is unique within an account and Region — the
// same scope vault names carry, and the reason the key is region-scoped.

func (s *backupStore) putAccessPoint(ctx context.Context, region string, a *accessPointRecord) error {
	return putRecord(ctx, s, nsAccessPoints, region, a.Name, a)
}

func (s *backupStore) getAccessPoint(ctx context.Context, region, name string) (*accessPointRecord, bool, error) {
	return getRecord[accessPointRecord](ctx, s, nsAccessPoints, "access point", region, name)
}

func (s *backupStore) listAccessPoints(ctx context.Context, region string) ([]accessPointRecord, error) {
	return listRecords[accessPointRecord](ctx, s, nsAccessPoints, "access point", region)
}

func (s *backupStore) deleteAccessPoint(ctx context.Context, region, name string) error {
	return s.deleteRecord(ctx, nsAccessPoints, region, name)
}
