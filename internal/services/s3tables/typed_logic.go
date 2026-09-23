package s3tables

// The codec-agnostic implementation of every S3 Tables operation. The REST
// adapters in routes.go lift the @http bindings (path labels, query string,
// body) into these request structs and hand them over; nothing else decides
// behaviour.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// listMaxLimit is the model's range for maxBuckets, maxNamespaces and
// maxTables (1–1000); an omitted value is a full page.
const listMaxLimit = 1000

var listOpts = serviceutil.PaginateOptions{DefaultLimit: listMaxLimit, MaxLimit: listMaxLimit}

// tagCfg: BadRequestException is the model's only client-input error.
var tagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "BadRequestException",
	ExceededMessage: "The number of tags exceeds the limit of 50 tags.",
	InvalidCode:     "BadRequestException",
}

// validateLimit checks a list operation's page size against the model's range.
func validateLimit(v *int, field string) *protocol.AWSError {
	if v == nil {
		return nil
	}
	if *v < 1 || *v > listMaxLimit {
		return validationError("Value '%d' at '%s' failed to satisfy constraint: Member must have value between 1 and %d", *v, field, listMaxLimit)
	}
	return nil
}

func limitOf(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func paginate[T any](items []T, limit *int, token string) (serviceutil.Page[T], *protocol.AWSError) {
	page, err := serviceutil.Paginate(items, limitOf(limit), token, listOpts)
	if errors.Is(err, serviceutil.ErrInvalidPageToken) {
		return page, errBadToken
	}
	return page, nil
}

// ─── Resolution helpers ───────────────────────────────────────────────────────

// resolveBucket loads the table bucket an ARN names, in the caller's region.
// An ARN for another region or account names a bucket this endpoint does not
// hold, so it is not found here — the answer AWS's regional endpoint gives.
func (s *Service) resolveBucket(ctx context.Context, arn string) (*tableBucket, *protocol.AWSError) {
	p, aerr := parseBucketARN(arn)
	if aerr != nil {
		return nil, aerr
	}
	region := s.regionOf(ctx)
	if p.Region != region || p.Account != s.accountID() {
		return nil, errBucketNotFound
	}
	b, found, aerr := s.loadBucket(ctx, region, p.Bucket)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, errBucketNotFound
	}
	return b, nil
}

func (s *Service) resolveNamespace(ctx context.Context, arn, namespace string) (*tableBucket, *namespaceRecord, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, arn)
	if aerr != nil {
		return nil, nil, aerr
	}
	n, found, aerr := s.loadNamespace(ctx, b.Region, b.Name, namespace)
	if aerr != nil {
		return nil, nil, aerr
	}
	if !found {
		return nil, nil, errNamespaceNotFound
	}
	return b, n, nil
}

func (s *Service) resolveTable(ctx context.Context, arn, namespace, name string) (*tableBucket, *tableRecord, *protocol.AWSError) {
	b, _, aerr := s.resolveNamespace(ctx, arn, namespace)
	if aerr != nil {
		return nil, nil, aerr
	}
	t, found, aerr := s.loadTable(ctx, b.Region, b.Name, namespace, name)
	if aerr != nil {
		return nil, nil, aerr
	}
	if !found {
		return nil, nil, errTableNotFound
	}
	return b, t, nil
}

// resolveTableARN loads the table a table ARN names.
func (s *Service) resolveTableARN(ctx context.Context, arn string) (*tableRecord, *protocol.AWSError) {
	p, aerr := parseTableARN(arn)
	if aerr != nil {
		return nil, aerr
	}
	region := s.regionOf(ctx)
	if p.Region != region || p.Account != s.accountID() {
		return nil, errTableNotFound
	}
	t, found, aerr := s.findTableByID(ctx, region, p.Bucket, p.TableID)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, errTableNotFound
	}
	return t, nil
}

// ─── Table buckets ────────────────────────────────────────────────────────────

type createTableBucketRequest struct {
	Name                      string                     `json:"name"`
	EncryptionConfiguration   *encryptionConfiguration   `json:"encryptionConfiguration,omitempty"`
	StorageClassConfiguration *storageClassConfiguration `json:"storageClassConfiguration,omitempty"`
	Tags                      map[string]string          `json:"tags,omitempty"`
}

type createTableBucketResponse struct {
	ARN string `json:"arn"`
}

// validateCreateOptions checks the optional members CreateTableBucket and
// CreateTable share.
func validateCreateOptions(enc *encryptionConfiguration, sc *storageClassConfiguration, tags map[string]string) *protocol.AWSError {
	if enc != nil {
		if aerr := validateEncryption(enc); aerr != nil {
			return aerr
		}
	}
	if sc != nil {
		if aerr := validateStorageClass(sc); aerr != nil {
			return aerr
		}
	}
	if len(tags) > 0 {
		return serviceutil.ValidateTags(tagCfg, tags)
	}
	return nil
}

func (s *Service) createTableBucketTyped(ctx context.Context, req *createTableBucketRequest) (*createTableBucketResponse, *protocol.AWSError) {
	if aerr := validateTableBucketName(req.Name); aerr != nil {
		return nil, aerr
	}
	if aerr := validateCreateOptions(req.EncryptionConfiguration, req.StorageClassConfiguration, req.Tags); aerr != nil {
		return nil, aerr
	}
	enc := encryptionConfiguration{SSEAlgorithm: sseAES256}
	if req.EncryptionConfiguration != nil {
		enc = *req.EncryptionConfiguration
	}
	storage := storageStandard
	if req.StorageClassConfiguration != nil {
		storage = req.StorageClassConfiguration.StorageClass
	}

	region := s.regionOf(ctx)
	defer s.lock()()
	if _, found, aerr := s.loadBucket(ctx, region, req.Name); aerr != nil {
		return nil, aerr
	} else if found {
		return nil, errBucketExists
	}
	b := &tableBucket{
		Name:           req.Name,
		ARN:            s.bucketARN(region, req.Name),
		Region:         region,
		OwnerAccountID: s.accountID(),
		TableBucketID:  s.newID(),
		CreatedAt:      s.now(),
		Encryption:     enc,
		StorageClass:   storage,
		Tags:           req.Tags,
	}
	if aerr := s.saveBucket(ctx, b); aerr != nil {
		return nil, aerr
	}
	return &createTableBucketResponse{ARN: b.ARN}, nil
}

type tableBucketARNRequest struct {
	TableBucketARN string `json:"tableBucketARN"`
}

type tableBucketSummary struct {
	ARN            string    `json:"arn"`
	Name           string    `json:"name"`
	OwnerAccountID string    `json:"ownerAccountId"`
	CreatedAt      time.Time `json:"createdAt"`
	TableBucketID  string    `json:"tableBucketId,omitempty"`
	Type           string    `json:"type,omitempty"`
}

func bucketSummary(b *tableBucket) tableBucketSummary {
	return tableBucketSummary{
		ARN: b.ARN, Name: b.Name, OwnerAccountID: b.OwnerAccountID,
		CreatedAt: b.CreatedAt.UTC(), TableBucketID: b.TableBucketID, Type: resourceTypeCustomer,
	}
}

func (s *Service) getTableBucketTyped(ctx context.Context, req *tableBucketARNRequest) (*tableBucketSummary, *protocol.AWSError) {
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	out := bucketSummary(b)
	return &out, nil
}

type listTableBucketsRequest struct {
	Prefix            string `json:"prefix,omitempty"`
	ContinuationToken string `json:"continuationToken,omitempty"`
	MaxBuckets        *int   `json:"maxBuckets,omitempty"`
	Type              string `json:"type,omitempty"`
}

type listTableBucketsResponse struct {
	TableBuckets      []tableBucketSummary `json:"tableBuckets"`
	ContinuationToken string               `json:"continuationToken,omitempty"`
}

func (s *Service) listTableBucketsTyped(ctx context.Context, req *listTableBucketsRequest) (*listTableBucketsResponse, *protocol.AWSError) {
	if aerr := validateLimit(req.MaxBuckets, "maxBuckets"); aerr != nil {
		return nil, aerr
	}
	if req.Type != "" && req.Type != resourceTypeCustomer && req.Type != resourceTypeAWS {
		return nil, enumError("type", req.Type, resourceTypeCustomer, resourceTypeAWS)
	}
	buckets, aerr := s.listBuckets(ctx, s.regionOf(ctx))
	if aerr != nil {
		return nil, aerr
	}
	summaries := make([]tableBucketSummary, 0, len(buckets))
	for _, b := range buckets {
		// Every bucket a caller can create is a customer bucket; AWS-managed
		// ("aws") buckets are never created here.
		if req.Type == resourceTypeAWS || !strings.HasPrefix(b.Name, req.Prefix) {
			continue
		}
		summaries = append(summaries, bucketSummary(b))
	}
	page, aerr := paginate(summaries, req.MaxBuckets, req.ContinuationToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listTableBucketsResponse{TableBuckets: page.Items, ContinuationToken: page.NextToken}, nil
}

func (s *Service) deleteTableBucketTyped(ctx context.Context, req *tableBucketARNRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	nonEmpty, aerr := hasAny(ctx, s, nsNamespaces, namespaceKey(b.Region, b.Name, ""))
	if aerr != nil {
		return nil, aerr
	}
	if nonEmpty {
		return nil, errBucketNotEmpty
	}
	return nil, deleteRecord(ctx, s, nsBuckets, bucketKey(b.Region, b.Name))
}

// updateBucket runs a read-modify-write of one table bucket under the
// service's write lock.
func (s *Service) updateBucket(ctx context.Context, arn string, mutate func(*tableBucket) *protocol.AWSError) (*tableBucket, *protocol.AWSError) {
	return readModifyWrite(s, func() (*tableBucket, *protocol.AWSError) { return s.resolveBucket(ctx, arn) }, mutate,
		func(b *tableBucket) *protocol.AWSError { return s.saveBucket(ctx, b) })
}

// readModifyWrite resolves a record, applies mutate and saves the result, all
// under the service's write lock, so the resolve-check-write is one step.
func readModifyWrite[T any](s *Service, resolve func() (*T, *protocol.AWSError), mutate, save func(*T) *protocol.AWSError) (*T, *protocol.AWSError) {
	defer s.lock()()
	v, aerr := resolve()
	if aerr != nil {
		return nil, aerr
	}
	if aerr := mutate(v); aerr != nil {
		return nil, aerr
	}
	return v, save(v)
}

// ─── Namespaces ───────────────────────────────────────────────────────────────

type createNamespaceRequest struct {
	TableBucketARN string   `json:"tableBucketARN"`
	Namespace      []string `json:"namespace"`
}

type createNamespaceResponse struct {
	TableBucketARN string   `json:"tableBucketARN"`
	Namespace      []string `json:"namespace"`
}

func (s *Service) createNamespaceTyped(ctx context.Context, req *createNamespaceRequest) (*createNamespaceResponse, *protocol.AWSError) {
	if len(req.Namespace) != 1 {
		return nil, validationError("Value at 'namespace' failed to satisfy constraint: Member must have length less than or equal to 1")
	}
	name := req.Namespace[0]
	if aerr := validateNamespaceName(name); aerr != nil {
		return nil, aerr
	}
	defer s.lock()()
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	if _, found, aerr := s.loadNamespace(ctx, b.Region, b.Name, name); aerr != nil {
		return nil, aerr
	} else if found {
		return nil, errNamespaceExists
	}
	n := &namespaceRecord{
		Name: name, Bucket: b.Name, NamespaceID: s.newID(), CreatedAt: s.now(),
		CreatedBy: s.accountID(), OwnerAccountID: s.accountID(),
	}
	if aerr := s.saveNamespace(ctx, b.Region, n); aerr != nil {
		return nil, aerr
	}
	return &createNamespaceResponse{TableBucketARN: b.ARN, Namespace: []string{name}}, nil
}

type namespaceRequest struct {
	TableBucketARN string `json:"tableBucketARN"`
	Namespace      string `json:"namespace"`
}

type namespaceSummary struct {
	Namespace      []string  `json:"namespace"`
	CreatedAt      time.Time `json:"createdAt"`
	CreatedBy      string    `json:"createdBy"`
	OwnerAccountID string    `json:"ownerAccountId"`
	NamespaceID    string    `json:"namespaceId,omitempty"`
	TableBucketID  string    `json:"tableBucketId,omitempty"`
}

func nsSummary(b *tableBucket, n *namespaceRecord) namespaceSummary {
	return namespaceSummary{
		Namespace: []string{n.Name}, CreatedAt: n.CreatedAt.UTC(), CreatedBy: n.CreatedBy,
		OwnerAccountID: n.OwnerAccountID, NamespaceID: n.NamespaceID, TableBucketID: b.TableBucketID,
	}
}

func (s *Service) getNamespaceTyped(ctx context.Context, req *namespaceRequest) (*namespaceSummary, *protocol.AWSError) {
	b, n, aerr := s.resolveNamespace(ctx, req.TableBucketARN, req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	out := nsSummary(b, n)
	return &out, nil
}

type listNamespacesRequest struct {
	TableBucketARN    string `json:"tableBucketARN"`
	Prefix            string `json:"prefix,omitempty"`
	ContinuationToken string `json:"continuationToken,omitempty"`
	MaxNamespaces     *int   `json:"maxNamespaces,omitempty"`
}

type listNamespacesResponse struct {
	Namespaces        []namespaceSummary `json:"namespaces"`
	ContinuationToken string             `json:"continuationToken,omitempty"`
}

func (s *Service) listNamespacesTyped(ctx context.Context, req *listNamespacesRequest) (*listNamespacesResponse, *protocol.AWSError) {
	if aerr := validateLimit(req.MaxNamespaces, "maxNamespaces"); aerr != nil {
		return nil, aerr
	}
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	namespaces, aerr := s.listNamespaces(ctx, b.Region, b.Name)
	if aerr != nil {
		return nil, aerr
	}
	summaries := make([]namespaceSummary, 0, len(namespaces))
	for _, n := range namespaces {
		if strings.HasPrefix(n.Name, req.Prefix) {
			summaries = append(summaries, nsSummary(b, n))
		}
	}
	page, aerr := paginate(summaries, req.MaxNamespaces, req.ContinuationToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listNamespacesResponse{Namespaces: page.Items, ContinuationToken: page.NextToken}, nil
}

func (s *Service) deleteNamespaceTyped(ctx context.Context, req *namespaceRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	b, n, aerr := s.resolveNamespace(ctx, req.TableBucketARN, req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	nonEmpty, aerr := hasAny(ctx, s, nsTables, namespaceKey(b.Region, b.Name, n.Name)+"/")
	if aerr != nil {
		return nil, aerr
	}
	if nonEmpty {
		return nil, errNamespaceNotEmpty
	}
	return nil, deleteRecord(ctx, s, nsNamespaces, namespaceKey(b.Region, b.Name, n.Name))
}

// ─── Tables ───────────────────────────────────────────────────────────────────

// icebergSchemaField is SchemaField.
type icebergSchemaField struct {
	ID       *int   `json:"id,omitempty"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
}

type icebergSchema struct {
	Fields []icebergSchemaField `json:"fields"`
}

type icebergPartitionField struct {
	SourceID  int    `json:"source-id"`
	Transform string `json:"transform"`
	Name      string `json:"name"`
	FieldID   int    `json:"field-id,omitempty"`
}

type icebergPartitionSpec struct {
	Fields []icebergPartitionField `json:"fields"`
	SpecID *int                    `json:"spec-id,omitempty"`
}

type icebergSortField struct {
	SourceID  int    `json:"source-id"`
	Transform string `json:"transform"`
	Direction string `json:"direction"`
	NullOrder string `json:"null-order"`
}

type icebergSortOrder struct {
	OrderID int                `json:"order-id"`
	Fields  []icebergSortField `json:"fields"`
}

type icebergMetadata struct {
	Schema        *icebergSchema        `json:"schema,omitempty"`
	SchemaV2      json.RawMessage       `json:"schemaV2,omitempty"`
	PartitionSpec *icebergPartitionSpec `json:"partitionSpec,omitempty"`
	WriteOrder    *icebergSortOrder     `json:"writeOrder,omitempty"`
	Properties    map[string]string     `json:"properties,omitempty"`
}

type tableMetadata struct {
	Iceberg *icebergMetadata `json:"iceberg,omitempty"`
}

type createTableRequest struct {
	TableBucketARN            string                     `json:"tableBucketARN"`
	Namespace                 string                     `json:"namespace"`
	Name                      string                     `json:"name"`
	Format                    string                     `json:"format"`
	Metadata                  *tableMetadata             `json:"metadata,omitempty"`
	EncryptionConfiguration   *encryptionConfiguration   `json:"encryptionConfiguration,omitempty"`
	StorageClassConfiguration *storageClassConfiguration `json:"storageClassConfiguration,omitempty"`
	Tags                      map[string]string          `json:"tags,omitempty"`
}

type createTableResponse struct {
	TableARN     string `json:"tableARN"`
	VersionToken string `json:"versionToken"`
}

func (s *Service) createTableTyped(ctx context.Context, req *createTableRequest) (*createTableResponse, *protocol.AWSError) {
	iceberg, aerr := validateCreateTable(req)
	if aerr != nil {
		return nil, aerr
	}

	defer s.lock()()
	b, n, aerr := s.resolveNamespace(ctx, req.TableBucketARN, req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	if _, found, aerr := s.loadTable(ctx, b.Region, b.Name, n.Name, req.Name); aerr != nil {
		return nil, aerr
	} else if found {
		return nil, errTableExists
	}

	t := s.newTableRecord(b, n, req)
	// The metadata document is built before anything is created, so a schema
	// Iceberg would refuse leaves no warehouse bucket behind.
	var metadataJSON []byte
	if iceberg != nil {
		if metadataJSON, aerr = buildInitialMetadata(t.TableID, t.WarehouseLocation, t.CreatedAt, iceberg); aerr != nil {
			return nil, aerr
		}
	}
	if aerr := s.createWarehouse(ctx, t, metadataJSON); aerr != nil {
		return nil, aerr
	}
	if aerr := s.saveTable(ctx, t); aerr != nil {
		return nil, aerr
	}
	return &createTableResponse{TableARN: t.ARN, VersionToken: t.VersionToken}, nil
}

// validateCreateTable checks everything CreateTable can check without state,
// and returns metadata.iceberg when the caller supplied one.
func validateCreateTable(req *createTableRequest) (*icebergMetadata, *protocol.AWSError) {
	if aerr := validateTableName(req.Name); aerr != nil {
		return nil, aerr
	}
	if req.Format != formatIceberg {
		return nil, enumError("format", req.Format, formatIceberg)
	}
	if aerr := validateCreateOptions(req.EncryptionConfiguration, req.StorageClassConfiguration, req.Tags); aerr != nil {
		return nil, aerr
	}
	if req.Metadata == nil || req.Metadata.Iceberg == nil {
		return nil, nil
	}
	iceberg := req.Metadata.Iceberg
	if len(iceberg.SchemaV2) > 0 {
		return nil, notImplemented("CreateTable with metadata.iceberg.schemaV2 is not emulated; declare the columns with metadata.iceberg.schema.")
	}
	if iceberg.Schema == nil {
		return nil, badRequest("metadata.iceberg.schema is required.")
	}
	return iceberg, nil
}

// newTableRecord is a new table in namespace n of bucket b, with a fresh id,
// version token and warehouse location. It inherits the bucket's encryption
// and storage class unless the request sets its own.
func (s *Service) newTableRecord(b *tableBucket, n *namespaceRecord, req *createTableRequest) *tableRecord {
	now := s.now()
	tableID := s.newID()
	t := &tableRecord{
		Name: req.Name, Namespace: n.Name, NamespaceID: n.NamespaceID,
		Bucket: b.Name, BucketARN: b.ARN, TableBucketID: b.TableBucketID, Region: b.Region,
		TableID: tableID, ARN: tableARN(b.ARN, tableID), Format: formatIceberg,
		VersionToken: s.newVersionToken(), WarehouseLocation: "s3://" + s.newWarehouseBucketName(),
		CreatedAt: now, CreatedBy: s.accountID(), ModifiedAt: now, ModifiedBy: s.accountID(),
		OwnerAccountID: s.accountID(), Encryption: b.Encryption, StorageClass: b.StorageClass,
		Tags: req.Tags,
	}
	if req.EncryptionConfiguration != nil {
		t.Encryption = *req.EncryptionConfiguration
	}
	if req.StorageClassConfiguration != nil {
		t.StorageClass = req.StorageClassConfiguration.StorageClass
	}
	return t
}

// createWarehouse creates the table's warehouse bucket through the S3
// accessor and, when there is one, writes its first metadata file there and
// points the table at it.
func (s *Service) createWarehouse(ctx context.Context, t *tableRecord, metadataJSON []byte) *protocol.AWSError {
	warehouse := strings.TrimPrefix(t.WarehouseLocation, "s3://")
	if s.ensureWarehouse != nil {
		if aerr := s.ensureWarehouse(ctx, warehouse, t.Region); aerr != nil {
			return aerr
		}
	}
	if metadataJSON == nil || s.putObject == nil {
		return nil
	}
	key := icebergmeta.MetadataPath(0, s.newID())
	if _, aerr := s.putObject(ctx, warehouse, key, metadataJSON, s3PutJSON); aerr != nil {
		return aerr
	}
	t.MetadataLocation = t.WarehouseLocation + "/" + key
	return nil
}

type getTableRequest struct {
	TableBucketARN string `json:"tableBucketARN,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	Name           string `json:"name,omitempty"`
	TableARN       string `json:"tableArn,omitempty"`
}

type getTableResponse struct {
	Name              string    `json:"name"`
	Type              string    `json:"type"`
	TableARN          string    `json:"tableARN"`
	Namespace         []string  `json:"namespace"`
	NamespaceID       string    `json:"namespaceId,omitempty"`
	VersionToken      string    `json:"versionToken"`
	MetadataLocation  string    `json:"metadataLocation,omitempty"`
	WarehouseLocation string    `json:"warehouseLocation"`
	CreatedAt         time.Time `json:"createdAt"`
	CreatedBy         string    `json:"createdBy"`
	ModifiedAt        time.Time `json:"modifiedAt"`
	ModifiedBy        string    `json:"modifiedBy"`
	OwnerAccountID    string    `json:"ownerAccountId"`
	Format            string    `json:"format"`
	TableBucketID     string    `json:"tableBucketId,omitempty"`
}

// getTableTyped addresses the table either by its ARN or by bucket, namespace
// and name; the model makes every member optional, so a request naming
// neither is refused here.
func (s *Service) getTableTyped(ctx context.Context, req *getTableRequest) (*getTableResponse, *protocol.AWSError) {
	var t *tableRecord
	var aerr *protocol.AWSError
	switch {
	case req.TableARN != "":
		t, aerr = s.resolveTableARN(ctx, req.TableARN)
	case req.TableBucketARN != "" && req.Namespace != "" && req.Name != "":
		_, t, aerr = s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	default:
		aerr = badRequest("Specify either tableArn, or tableBucketARN, namespace and name.")
	}
	if aerr != nil {
		return nil, aerr
	}
	return &getTableResponse{
		Name: t.Name, Type: resourceTypeCustomer, TableARN: t.ARN, Namespace: []string{t.Namespace},
		NamespaceID: t.NamespaceID, VersionToken: t.VersionToken, MetadataLocation: t.MetadataLocation,
		WarehouseLocation: t.WarehouseLocation, CreatedAt: t.CreatedAt.UTC(), CreatedBy: t.CreatedBy,
		ModifiedAt: t.ModifiedAt.UTC(), ModifiedBy: t.ModifiedBy, OwnerAccountID: t.OwnerAccountID,
		Format: t.Format, TableBucketID: t.TableBucketID,
	}, nil
}

type listTablesRequest struct {
	TableBucketARN    string `json:"tableBucketARN"`
	Namespace         string `json:"namespace,omitempty"`
	Prefix            string `json:"prefix,omitempty"`
	ContinuationToken string `json:"continuationToken,omitempty"`
	MaxTables         *int   `json:"maxTables,omitempty"`
}

type tableSummary struct {
	Namespace     []string  `json:"namespace"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	TableARN      string    `json:"tableARN"`
	CreatedAt     time.Time `json:"createdAt"`
	ModifiedAt    time.Time `json:"modifiedAt"`
	NamespaceID   string    `json:"namespaceId,omitempty"`
	TableBucketID string    `json:"tableBucketId,omitempty"`
}

type listTablesResponse struct {
	Tables            []tableSummary `json:"tables"`
	ContinuationToken string         `json:"continuationToken,omitempty"`
}

func (s *Service) listTablesTyped(ctx context.Context, req *listTablesRequest) (*listTablesResponse, *protocol.AWSError) {
	if aerr := validateLimit(req.MaxTables, "maxTables"); aerr != nil {
		return nil, aerr
	}
	b, aerr := s.resolveBucket(ctx, req.TableBucketARN)
	if aerr != nil {
		return nil, aerr
	}
	if req.Namespace != "" {
		if _, found, aerr := s.loadNamespace(ctx, b.Region, b.Name, req.Namespace); aerr != nil {
			return nil, aerr
		} else if !found {
			return nil, errNamespaceNotFound
		}
	}
	tables, aerr := s.listTables(ctx, b.Region, b.Name, req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	summaries := make([]tableSummary, 0, len(tables))
	for _, t := range tables {
		if !strings.HasPrefix(t.Name, req.Prefix) {
			continue
		}
		summaries = append(summaries, tableSummary{
			Namespace: []string{t.Namespace}, Name: t.Name, Type: resourceTypeCustomer, TableARN: t.ARN,
			CreatedAt: t.CreatedAt.UTC(), ModifiedAt: t.ModifiedAt.UTC(),
			NamespaceID: t.NamespaceID, TableBucketID: t.TableBucketID,
		})
	}
	page, aerr := paginate(summaries, req.MaxTables, req.ContinuationToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listTablesResponse{Tables: page.Items, ContinuationToken: page.NextToken}, nil
}

type tableRequest struct {
	TableBucketARN string `json:"tableBucketARN"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	VersionToken   string `json:"versionToken,omitempty"`
}

// deleteTableTyped removes the table from the catalog. Its warehouse bucket
// and whatever the engine wrote there stay in S3: AWS deletes a table's data
// asynchronously, and Overcast does not delete it at all (see limitations).
func (s *Service) deleteTableTyped(ctx context.Context, req *tableRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	_, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	if req.VersionToken != "" && req.VersionToken != t.VersionToken {
		return nil, errVersionMismatch
	}
	return nil, s.deleteTableRecord(ctx, t)
}

// updateTable runs a read-modify-write of one table under the write lock.
func (s *Service) updateTable(ctx context.Context, arn, namespace, name string, mutate func(*tableRecord) *protocol.AWSError) (*tableRecord, *protocol.AWSError) {
	return readModifyWrite(s, func() (*tableRecord, *protocol.AWSError) {
		_, t, aerr := s.resolveTable(ctx, arn, namespace, name)
		return t, aerr
	}, mutate, func(t *tableRecord) *protocol.AWSError { return s.saveTable(ctx, t) })
}

// updateTableByARN is updateTable for the operations that address a table by
// its ARN.
func (s *Service) updateTableByARN(ctx context.Context, arn string, mutate func(*tableRecord) *protocol.AWSError) (*tableRecord, *protocol.AWSError) {
	return readModifyWrite(s, func() (*tableRecord, *protocol.AWSError) { return s.resolveTableARN(ctx, arn) }, mutate,
		func(t *tableRecord) *protocol.AWSError { return s.saveTable(ctx, t) })
}

type renameTableRequest struct {
	TableBucketARN   string `json:"tableBucketARN"`
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	NewNamespaceName string `json:"newNamespaceName,omitempty"`
	NewName          string `json:"newName,omitempty"`
	VersionToken     string `json:"versionToken,omitempty"`
}

// renameTableTyped moves a table to a new name, a new namespace, or both. The
// ARN names the table by id, so it is unchanged; so is the version token,
// which tracks the table's metadata rather than its catalog entry.
func (s *Service) renameTableTyped(ctx context.Context, req *renameTableRequest) (any, *protocol.AWSError) {
	if req.NewName == "" && req.NewNamespaceName == "" {
		return nil, errNothingToRename
	}
	if req.NewName != "" {
		if aerr := validateTableName(req.NewName); aerr != nil {
			return nil, aerr
		}
	}
	if req.NewNamespaceName != "" {
		if aerr := validateNamespaceName(req.NewNamespaceName); aerr != nil {
			return nil, aerr
		}
	}
	defer s.lock()()
	b, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	if req.VersionToken != "" && req.VersionToken != t.VersionToken {
		return nil, errVersionMismatch
	}
	destNS, destName := t.Namespace, t.Name
	if req.NewNamespaceName != "" {
		destNS = req.NewNamespaceName
	}
	if req.NewName != "" {
		destName = req.NewName
	}
	if destNS == t.Namespace && destName == t.Name {
		return nil, nil
	}
	n, found, aerr := s.loadNamespace(ctx, b.Region, b.Name, destNS)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, errDestNamespace
	}
	if _, exists, aerr := s.loadTable(ctx, b.Region, b.Name, destNS, destName); aerr != nil {
		return nil, aerr
	} else if exists {
		return nil, errTableExists
	}
	// The new key is written before the old one goes, so a failed write loses
	// nothing and a reader never finds the table missing in between.
	old := *t
	t.Namespace, t.NamespaceID, t.Name = destNS, n.NamespaceID, destName
	t.ModifiedAt, t.ModifiedBy = s.now(), s.accountID()
	if aerr := s.saveTable(ctx, t); aerr != nil {
		return nil, aerr
	}
	return nil, s.deleteTableRecord(ctx, &old)
}

// ─── Metadata location ────────────────────────────────────────────────────────

type metadataLocationResponse struct {
	VersionToken      string `json:"versionToken"`
	MetadataLocation  string `json:"metadataLocation,omitempty"`
	WarehouseLocation string `json:"warehouseLocation"`
}

func (s *Service) getTableMetadataLocationTyped(ctx context.Context, req *tableRequest) (*metadataLocationResponse, *protocol.AWSError) {
	_, t, aerr := s.resolveTable(ctx, req.TableBucketARN, req.Namespace, req.Name)
	if aerr != nil {
		return nil, aerr
	}
	return &metadataLocationResponse{VersionToken: t.VersionToken, MetadataLocation: t.MetadataLocation, WarehouseLocation: t.WarehouseLocation}, nil
}

type updateMetadataLocationRequest struct {
	TableBucketARN   string `json:"tableBucketARN"`
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	VersionToken     string `json:"versionToken"`
	MetadataLocation string `json:"metadataLocation"`
}

type updateMetadataLocationResponse struct {
	Name             string   `json:"name"`
	TableARN         string   `json:"tableARN"`
	Namespace        []string `json:"namespace"`
	VersionToken     string   `json:"versionToken"`
	MetadataLocation string   `json:"metadataLocation"`
}

// updateTableMetadataLocationTyped is an Iceberg commit's compare-and-swap:
// the new pointer is accepted only from a caller holding the current version
// token, and every accepted swap issues a new one, so of two writers racing
// from the same token exactly one wins and the other gets ConflictException.
// The new location must lie inside the table's warehouse.
func (s *Service) updateTableMetadataLocationTyped(ctx context.Context, req *updateMetadataLocationRequest) (*updateMetadataLocationResponse, *protocol.AWSError) {
	if req.VersionToken == "" {
		return nil, badRequest("versionToken is required.")
	}
	if req.MetadataLocation == "" || len(req.MetadataLocation) > 2048 {
		return nil, errBadMetadataLoc
	}
	t, aerr := s.updateTable(ctx, req.TableBucketARN, req.Namespace, req.Name, func(t *tableRecord) *protocol.AWSError {
		if !strings.HasPrefix(req.MetadataLocation, t.WarehouseLocation+"/") {
			return errBadMetadataLoc
		}
		if req.VersionToken != t.VersionToken {
			return errVersionMismatch
		}
		t.MetadataLocation = req.MetadataLocation
		t.VersionToken = s.newVersionToken()
		t.ModifiedAt, t.ModifiedBy = s.now(), s.accountID()
		return nil
	})
	if aerr != nil {
		return nil, aerr
	}
	return &updateMetadataLocationResponse{
		Name: t.Name, TableARN: t.ARN, Namespace: []string{t.Namespace},
		VersionToken: t.VersionToken, MetadataLocation: t.MetadataLocation,
	}, nil
}
