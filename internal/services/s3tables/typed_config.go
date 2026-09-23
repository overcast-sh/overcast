package s3tables

// The configuration half of the S3 Tables operations: encryption, storage
// class, policies, metrics, maintenance, record expiration, replication and
// tags. Everything here is stored on the bucket or table record and read back;
// nothing acts on it (see the service doc's limitations page).

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── Bucket encryption, storage class, policy, metrics ────────────────────────

type encryptionRequest struct {
	TableBucketARN          string                   `json:"tableBucketARN"`
	EncryptionConfiguration *encryptionConfiguration `json:"encryptionConfiguration"`
}

type encryptionResponse struct {
	EncryptionConfiguration encryptionConfiguration `json:"encryptionConfiguration"`
}

func (s *Service) putTableBucketEncryptionTyped(ctx context.Context, req *encryptionRequest) (any, *protocol.AWSError) {
	if aerr := validateEncryption(req.EncryptionConfiguration); aerr != nil {
		return nil, aerr
	}
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		b.Encryption = *req.EncryptionConfiguration
		return nil
	})
	return nil, aerr
}

func (s *Service) getTableBucketEncryptionTyped(ctx context.Context, req *tableBucketARNRequest) (*encryptionResponse, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	return &encryptionResponse{EncryptionConfiguration: b.Encryption}, nil
}

// deleteTableBucketEncryptionTyped returns the bucket to the default every
// table bucket starts with, SSE-S3 (AES256): table buckets are always
// encrypted, so there is no unencrypted state to return to.
func (s *Service) deleteTableBucketEncryptionTyped(ctx context.Context, req *tableBucketARNRequest) (any, *protocol.AWSError) {
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		b.Encryption = encryptionConfiguration{SSEAlgorithm: sseAES256}
		return nil
	})
	return nil, aerr
}

type storageClassRequest struct {
	TableBucketARN            string                     `json:"tableBucketARN"`
	StorageClassConfiguration *storageClassConfiguration `json:"storageClassConfiguration"`
}

type storageClassResponse struct {
	StorageClassConfiguration storageClassConfiguration `json:"storageClassConfiguration"`
}

// putTableBucketStorageClassTyped changes the class new tables inherit;
// existing tables keep theirs, as on AWS.
func (s *Service) putTableBucketStorageClassTyped(ctx context.Context, req *storageClassRequest) (any, *protocol.AWSError) {
	if aerr := validateStorageClass(req.StorageClassConfiguration); aerr != nil {
		return nil, aerr
	}
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		b.StorageClass = req.StorageClassConfiguration.StorageClass
		return nil
	})
	return nil, aerr
}

func (s *Service) getTableBucketStorageClassTyped(ctx context.Context, req *tableBucketARNRequest) (*storageClassResponse, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	return &storageClassResponse{StorageClassConfiguration: storageClassConfiguration{StorageClass: b.StorageClass}}, nil
}

type bucketPolicyRequest struct {
	TableBucketARN string `json:"tableBucketARN"`
	ResourcePolicy string `json:"resourcePolicy"`
}

type policyResponse struct {
	ResourcePolicy string `json:"resourcePolicy"`
}

// validatePolicy checks a resource policy is a JSON object within the model's
// length bound. Overcast stores policies; it does not evaluate them.
func validatePolicy(policy string) *protocol.AWSError {
	const maxPolicyLength = 20480
	if policy == "" || len(policy) > maxPolicyLength {
		return badRequest("The resource policy must be between 1 and 20480 characters.")
	}
	var doc map[string]any
	if json.Unmarshal([]byte(policy), &doc) != nil {
		return badRequest("The resource policy is not valid JSON.")
	}
	return nil
}

func (s *Service) putTableBucketPolicyTyped(ctx context.Context, req *bucketPolicyRequest) (any, *protocol.AWSError) {
	if aerr := validatePolicy(req.ResourcePolicy); aerr != nil {
		return nil, aerr
	}
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		b.Policy = req.ResourcePolicy
		return nil
	})
	return nil, aerr
}

func (s *Service) getTableBucketPolicyTyped(ctx context.Context, req *tableBucketARNRequest) (*policyResponse, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	if b.Policy == "" {
		return nil, errNoBucketPolicy
	}
	return &policyResponse{ResourcePolicy: b.Policy}, nil
}

func (s *Service) deleteTableBucketPolicyTyped(ctx context.Context, req *tableBucketARNRequest) (any, *protocol.AWSError) {
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		b.Policy = ""
		return nil
	})
	return nil, aerr
}

type metricsResponse struct {
	TableBucketARN string `json:"tableBucketARN"`
	ID             string `json:"id,omitempty"`
}

// putTableBucketMetricsConfigurationTyped turns on request metrics for the
// bucket. The operation has no body — enabling is all it does — and Overcast
// publishes no CloudWatch metrics for it; the configuration is recorded and
// echoed.
func (s *Service) putTableBucketMetricsConfigurationTyped(ctx context.Context, req *tableBucketARNRequest) (any, *protocol.AWSError) {
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		if b.MetricsID == "" {
			b.MetricsID = s.newID()
		}
		return nil
	})
	return nil, aerr
}

func (s *Service) getTableBucketMetricsConfigurationTyped(ctx context.Context, req *tableBucketARNRequest) (*metricsResponse, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	if b.MetricsID == "" {
		return nil, errNoMetricsConfig
	}
	return &metricsResponse{TableBucketARN: b.ARN, ID: b.MetricsID}, nil
}

func (s *Service) deleteTableBucketMetricsConfigurationTyped(ctx context.Context, req *tableBucketARNRequest) (any, *protocol.AWSError) {
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		b.MetricsID = ""
		return nil
	})
	return nil, aerr
}

// ─── Maintenance ──────────────────────────────────────────────────────────────

func intPtr(v int) *int { return &v }

// defaultBucketMaintenance is what AWS reports for a bucket nobody has
// configured: unreferenced file removal enabled, 3 and 10 days.
func defaultBucketMaintenance() map[string]maintenanceValue {
	return map[string]maintenanceValue{
		maintUnreferencedFileRemoval: {Status: statusEnabled, Settings: &maintenanceSettings{
			IcebergUnreferencedFileRemoval: &unreferencedFileRemovalSettings{UnreferencedDays: intPtr(3), NonCurrentDays: intPtr(10)},
		}},
	}
}

// defaultTableMaintenance is AWS's documented default for a table:
// compaction to 512 MB files and snapshot management keeping at least one
// snapshot for 120 hours, both enabled.
func defaultTableMaintenance() map[string]maintenanceValue {
	return map[string]maintenanceValue{
		maintCompaction: {Status: statusEnabled, Settings: &maintenanceSettings{
			IcebergCompaction: &compactionSettings{TargetFileSizeMB: intPtr(512), Strategy: "auto"},
		}},
		maintSnapshotManagement: {Status: statusEnabled, Settings: &maintenanceSettings{
			IcebergSnapshotManagement: &snapshotManagementSettings{MinSnapshotsToKeep: intPtr(1), MaxSnapshotAgeHours: intPtr(120)},
		}},
	}
}

func withDefaults(defaults, stored map[string]maintenanceValue) map[string]maintenanceValue {
	out := defaults
	maps.Copy(out, stored)
	return out
}

type putBucketMaintenanceRequest struct {
	TableBucketARN string            `json:"tableBucketARN"`
	Type           string            `json:"type"`
	Value          *maintenanceValue `json:"value"`
}

type bucketMaintenanceResponse struct {
	TableBucketARN string                      `json:"tableBucketARN"`
	Configuration  map[string]maintenanceValue `json:"configuration"`
}

func (s *Service) putTableBucketMaintenanceConfigurationTyped(ctx context.Context, req *putBucketMaintenanceRequest) (any, *protocol.AWSError) {
	if aerr := validateMaintenance(req.Type, req.Value, maintUnreferencedFileRemoval); aerr != nil {
		return nil, aerr
	}
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		if b.Maintenance == nil {
			b.Maintenance = map[string]maintenanceValue{}
		}
		b.Maintenance[req.Type] = *req.Value
		return nil
	})
	return nil, aerr
}

func (s *Service) getTableBucketMaintenanceConfigurationTyped(ctx context.Context, req *tableBucketARNRequest) (*bucketMaintenanceResponse, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	return &bucketMaintenanceResponse{TableBucketARN: b.ARN, Configuration: withDefaults(defaultBucketMaintenance(), b.Maintenance)}, nil
}

// ─── Bucket replication ───────────────────────────────────────────────────────

type bucketReplicationRequest struct {
	TableBucketARN string                    `json:"tableBucketARN"`
	VersionToken   string                    `json:"versionToken,omitempty"`
	Configuration  *replicationConfiguration `json:"configuration,omitempty"`
}

type replicationResponse struct {
	VersionToken  string                   `json:"versionToken"`
	Configuration replicationConfiguration `json:"configuration"`
}

type putReplicationResponse struct {
	VersionToken string `json:"versionToken"`
	Status       string `json:"status"`
}

// replicationStatusAccepted is what PutTable(Bucket)Replication reports.
// Unverified against AWS; Overcast replicates nothing.
const replicationStatusAccepted = "Enabled"

// applyReplication guards a replication change with its version token: a
// token that no longer matches the stored one is a ConflictException, the
// optimistic-concurrency answer the model's versionToken exists for.
func applyReplication(current **replicationState, token string, cfg *replicationConfiguration, newToken string) *protocol.AWSError {
	if token != "" && (*current == nil || (*current).VersionToken != token) {
		return conflict("The provided version token does not match the current replication configuration.")
	}
	if cfg == nil {
		*current = nil
		return nil
	}
	*current = &replicationState{Configuration: *cfg, VersionToken: newToken}
	return nil
}

func (s *Service) putTableBucketReplicationTyped(ctx context.Context, req *bucketReplicationRequest) (*putReplicationResponse, *protocol.AWSError) {
	if aerr := validateReplication(req.Configuration); aerr != nil {
		return nil, aerr
	}
	token := s.newVersionToken()
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		return applyReplication(&b.Replication, req.VersionToken, req.Configuration, token)
	})
	if aerr != nil {
		return nil, aerr
	}
	return &putReplicationResponse{VersionToken: token, Status: replicationStatusAccepted}, nil
}

func (s *Service) getTableBucketReplicationTyped(ctx context.Context, req *bucketReplicationRequest) (*replicationResponse, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	if b.Replication == nil {
		return nil, errNoReplicationBkt
	}
	return &replicationResponse{VersionToken: b.Replication.VersionToken, Configuration: b.Replication.Configuration}, nil
}

func (s *Service) deleteTableBucketReplicationTyped(ctx context.Context, req *bucketReplicationRequest) (any, *protocol.AWSError) {
	_, aerr := s.updateBucket(ctx, req.TableBucketARN, func(b *tableBucket) *protocol.AWSError {
		if b.Replication == nil {
			return errNoReplicationBkt
		}
		return applyReplication(&b.Replication, req.VersionToken, nil, "")
	})
	return nil, aerr
}

// ─── Table encryption, storage class, policy ──────────────────────────────────

func (s *Service) getTableEncryptionTyped(ctx context.Context, req *tableRequest) (*encryptionResponse, *protocol.AWSError) {
	_, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	return &encryptionResponse{EncryptionConfiguration: t.Encryption}, nil
}

func (s *Service) getTableStorageClassTyped(ctx context.Context, req *tableRequest) (*storageClassResponse, *protocol.AWSError) {
	_, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	return &storageClassResponse{StorageClassConfiguration: storageClassConfiguration{StorageClass: t.StorageClass}}, nil
}

type tablePolicyRequest struct {
	TableBucketARN string `json:"tableBucketARN"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	ResourcePolicy string `json:"resourcePolicy"`
}

func (s *Service) putTablePolicyTyped(ctx context.Context, req *tablePolicyRequest) (any, *protocol.AWSError) {
	if aerr := validatePolicy(req.ResourcePolicy); aerr != nil {
		return nil, aerr
	}
	_, aerr := s.updateTable(ctx, req.TableBucketARN, req.Namespace, req.Name, func(t *tableRecord) *protocol.AWSError {
		t.Policy = req.ResourcePolicy
		return nil
	})
	return nil, aerr
}

func (s *Service) getTablePolicyTyped(ctx context.Context, req *tableRequest) (*policyResponse, *protocol.AWSError) {
	_, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	if t.Policy == "" {
		return nil, errNoTablePolicy
	}
	return &policyResponse{ResourcePolicy: t.Policy}, nil
}

func (s *Service) deleteTablePolicyTyped(ctx context.Context, req *tableRequest) (any, *protocol.AWSError) {
	_, aerr := s.updateTable(ctx, req.TableBucketARN, req.Namespace, req.Name, func(t *tableRecord) *protocol.AWSError {
		t.Policy = ""
		return nil
	})
	return nil, aerr
}

// ─── Table maintenance ────────────────────────────────────────────────────────

type putTableMaintenanceRequest struct {
	TableBucketARN string            `json:"tableBucketARN"`
	Namespace      string            `json:"namespace"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Value          *maintenanceValue `json:"value"`
}

type tableMaintenanceResponse struct {
	TableARN      string                      `json:"tableARN"`
	Configuration map[string]maintenanceValue `json:"configuration"`
}

func (s *Service) putTableMaintenanceConfigurationTyped(ctx context.Context, req *putTableMaintenanceRequest) (any, *protocol.AWSError) {
	if aerr := validateMaintenance(req.Type, req.Value, maintCompaction, maintSnapshotManagement); aerr != nil {
		return nil, aerr
	}
	_, aerr := s.updateTable(ctx, req.TableBucketARN, req.Namespace, req.Name, func(t *tableRecord) *protocol.AWSError {
		if t.Maintenance == nil {
			t.Maintenance = map[string]maintenanceValue{}
		}
		t.Maintenance[req.Type] = *req.Value
		return nil
	})
	return nil, aerr
}

func (s *Service) getTableMaintenanceConfigurationTyped(ctx context.Context, req *tableRequest) (*tableMaintenanceResponse, *protocol.AWSError) {
	_, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	return &tableMaintenanceResponse{TableARN: t.ARN, Configuration: withDefaults(defaultTableMaintenance(), t.Maintenance)}, nil
}

type jobStatusValue struct {
	Status string `json:"status"`
}

type tableMaintenanceJobStatusResponse struct {
	TableARN string                    `json:"tableARN"`
	Status   map[string]jobStatusValue `json:"status"`
}

// jobStatus reports a maintenance job Overcast never runs: Not_Yet_Run while
// its configuration is enabled, Disabled once it is turned off.
func jobStatus(v maintenanceValue) jobStatusValue {
	if v.Status == statusDisabled {
		return jobStatusValue{Status: jobDisabled}
	}
	return jobStatusValue{Status: jobNotYetRun}
}

func (s *Service) getTableMaintenanceJobStatusTyped(ctx context.Context, req *tableRequest) (*tableMaintenanceJobStatusResponse, *protocol.AWSError) {
	b, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	table := withDefaults(defaultTableMaintenance(), t.Maintenance)
	bucket := withDefaults(defaultBucketMaintenance(), b.Maintenance)
	return &tableMaintenanceJobStatusResponse{TableARN: t.ARN, Status: map[string]jobStatusValue{
		maintCompaction:              jobStatus(table[maintCompaction]),
		maintSnapshotManagement:      jobStatus(table[maintSnapshotManagement]),
		maintUnreferencedFileRemoval: jobStatus(bucket[maintUnreferencedFileRemoval]),
	}}, nil
}

// ─── Record expiration ────────────────────────────────────────────────────────

type tableARNRequest struct {
	TableARN     string `json:"tableArn"`
	VersionToken string `json:"versionToken,omitempty"`
}

type putRecordExpirationRequest struct {
	TableARN string                 `json:"tableArn"`
	Value    *recordExpirationValue `json:"value"`
}

type recordExpirationResponse struct {
	Configuration recordExpirationValue `json:"configuration"`
}

type recordExpirationJobStatusResponse struct {
	Status string `json:"status"`
}

func (s *Service) putTableRecordExpirationConfigurationTyped(ctx context.Context, req *putRecordExpirationRequest) (any, *protocol.AWSError) {
	if req.Value == nil {
		return nil, badRequest("value is required.")
	}
	if aerr := validateStatus(req.Value.Status, "value.status"); aerr != nil {
		return nil, aerr
	}
	if req.Value.Settings != nil && req.Value.Settings.Days != nil && *req.Value.Settings.Days < 1 {
		return nil, badRequest("value.settings.days must be a positive integer.")
	}
	_, aerr := s.updateTableByARN(ctx, req.TableARN, func(t *tableRecord) *protocol.AWSError {
		v := *req.Value
		t.RecordExpiration = &v
		return nil
	})
	return nil, aerr
}

func (s *Service) getTableRecordExpirationConfigurationTyped(ctx context.Context, req *tableARNRequest) (*recordExpirationResponse, *protocol.AWSError) {
	t, aerr := s.resolveTableARN(ctx, req.TableARN)
	if aerr != nil {
		return nil, aerr
	}
	cfg := recordExpirationValue{Status: statusDisabled}
	if t.RecordExpiration != nil {
		cfg = *t.RecordExpiration
	}
	return &recordExpirationResponse{Configuration: cfg}, nil
}

// getTableRecordExpirationJobStatusTyped reports that no expiration job has
// run: Overcast never expires records.
func (s *Service) getTableRecordExpirationJobStatusTyped(ctx context.Context, req *tableARNRequest) (*recordExpirationJobStatusResponse, *protocol.AWSError) {
	t, aerr := s.resolveTableARN(ctx, req.TableARN)
	if aerr != nil {
		return nil, aerr
	}
	if t.RecordExpiration != nil && t.RecordExpiration.Status == statusEnabled {
		return &recordExpirationJobStatusResponse{Status: expiryNotYetRun}, nil
	}
	return &recordExpirationJobStatusResponse{Status: expiryDisabled}, nil
}

// ─── Table replication ────────────────────────────────────────────────────────

type tableReplicationRequest struct {
	TableARN      string                    `json:"tableArn"`
	VersionToken  string                    `json:"versionToken,omitempty"`
	Configuration *replicationConfiguration `json:"configuration,omitempty"`
}

func (s *Service) putTableReplicationTyped(ctx context.Context, req *tableReplicationRequest) (*putReplicationResponse, *protocol.AWSError) {
	if aerr := validateReplication(req.Configuration); aerr != nil {
		return nil, aerr
	}
	token := s.newVersionToken()
	_, aerr := s.updateTableByARN(ctx, req.TableARN, func(t *tableRecord) *protocol.AWSError {
		return applyReplication(&t.Replication, req.VersionToken, req.Configuration, token)
	})
	if aerr != nil {
		return nil, aerr
	}
	return &putReplicationResponse{VersionToken: token, Status: replicationStatusAccepted}, nil
}

func (s *Service) getTableReplicationTyped(ctx context.Context, req *tableARNRequest) (*replicationResponse, *protocol.AWSError) {
	t, aerr := s.resolveTableARN(ctx, req.TableARN)
	if aerr != nil {
		return nil, aerr
	}
	if t.Replication == nil {
		return nil, errNoReplicationTable
	}
	return &replicationResponse{VersionToken: t.Replication.VersionToken, Configuration: t.Replication.Configuration}, nil
}

func (s *Service) deleteTableReplicationTyped(ctx context.Context, req *tableARNRequest) (any, *protocol.AWSError) {
	if req.VersionToken == "" {
		return nil, badRequest("versionToken is required.")
	}
	_, aerr := s.updateTableByARN(ctx, req.TableARN, func(t *tableRecord) *protocol.AWSError {
		if t.Replication == nil {
			return errNoReplicationTable
		}
		return applyReplication(&t.Replication, req.VersionToken, nil, "")
	})
	return nil, aerr
}

type replicationDestinationStatus struct {
	ReplicationStatus         string `json:"replicationStatus"`
	DestinationTableBucketARN string `json:"destinationTableBucketArn"`
}

type replicationStatusResponse struct {
	SourceTableARN string                         `json:"sourceTableArn"`
	Destinations   []replicationDestinationStatus `json:"destinations"`
}

// getTableReplicationStatusTyped lists the table's replication destinations,
// each still pending: Overcast never replicates. The table's own
// configuration wins over its bucket's, as it does on AWS.
func (s *Service) getTableReplicationStatusTyped(ctx context.Context, req *tableARNRequest) (*replicationStatusResponse, *protocol.AWSError) {
	t, aerr := s.resolveTableARN(ctx, req.TableARN)
	if aerr != nil {
		return nil, aerr
	}
	cfg := t.Replication
	if cfg == nil {
		b, found, aerr := s.loadBucket(ctx, t.Region, t.Bucket)
		if aerr != nil {
			return nil, aerr
		}
		if found {
			cfg = b.Replication
		}
	}
	if cfg == nil {
		return nil, errNoReplicationTable
	}
	out := &replicationStatusResponse{SourceTableARN: t.ARN}
	for _, rule := range cfg.Configuration.Rules {
		for _, d := range rule.Destinations {
			out.Destinations = append(out.Destinations, replicationDestinationStatus{
				ReplicationStatus: replicationPending, DestinationTableBucketARN: d.DestinationTableBucketARN,
			})
		}
	}
	return out, nil
}

// ─── Tags ─────────────────────────────────────────────────────────────────────

type tagResourceRequest struct {
	ResourceARN string            `json:"resourceArn"`
	Tags        map[string]string `json:"tags"`
}

type untagResourceRequest struct {
	ResourceARN string   `json:"resourceArn"`
	TagKeys     []string `json:"tagKeys"`
}

type listTagsRequest struct {
	ResourceARN string `json:"resourceArn"`
}

type listTagsResponse struct {
	Tags map[string]string `json:"tags"`
}

// resourceTags returns the tags of the bucket or table an ARN names.
func (s *Service) resourceTags(ctx context.Context, arn string) (map[string]string, *protocol.AWSError) {
	p, aerr := parseResourceARN(arn)
	if aerr != nil {
		return nil, aerr
	}
	if p.TableID != "" {
		t, aerr := s.resolveTableARN(ctx, arn)
		if aerr != nil {
			return nil, aerr
		}
		return t.Tags, nil
	}
	b, aerr := s.resolveBucket(ctx, arn)
	if aerr != nil {
		return nil, aerr
	}
	return b.Tags, nil
}

// updateResourceTags replaces the tags of the bucket or table an ARN names
// with what edit makes of a copy of them. Tags live on the record, so they go
// when it does.
func (s *Service) updateResourceTags(ctx context.Context, arn string, edit func(map[string]string) (map[string]string, *protocol.AWSError)) *protocol.AWSError {
	p, aerr := parseResourceARN(arn)
	if aerr != nil {
		return aerr
	}
	apply := func(tags *map[string]string) *protocol.AWSError {
		edited, aerr := edit(maps.Clone(*tags))
		if aerr == nil {
			*tags = edited
		}
		return aerr
	}
	if p.TableID != "" {
		_, aerr = s.updateTableByARN(ctx, arn, func(t *tableRecord) *protocol.AWSError { return apply(&t.Tags) })
		return aerr
	}
	_, aerr = s.updateBucket(ctx, arn, func(b *tableBucket) *protocol.AWSError { return apply(&b.Tags) })
	return aerr
}

func (s *Service) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (any, *protocol.AWSError) {
	return nil, s.updateResourceTags(ctx, req.ResourceARN, func(tags map[string]string) (map[string]string, *protocol.AWSError) {
		if tags == nil {
			tags = map[string]string{}
		}
		maps.Copy(tags, req.Tags)
		return tags, serviceutil.ValidateTags(tagCfg, tags)
	})
}

func (s *Service) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (any, *protocol.AWSError) {
	return nil, s.updateResourceTags(ctx, req.ResourceARN, func(tags map[string]string) (map[string]string, *protocol.AWSError) {
		for _, k := range req.TagKeys {
			delete(tags, k)
		}
		return tags, nil
	})
}

func (s *Service) listTagsForResourceTyped(ctx context.Context, req *listTagsRequest) (*listTagsResponse, *protocol.AWSError) {
	tags, aerr := s.resourceTags(ctx, req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	if tags == nil {
		tags = map[string]string{}
	}
	return &listTagsResponse{Tags: tags}, nil
}
