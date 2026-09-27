package s3

// handler_stubs.go contains every S3 handler that is not yet implemented.
// Each method returns HTTP 501 Not Implemented with x-emulator-unsupported: true.
//
// Convention: when an operation is implemented, move its method body out of this
// file and into handler.go (or handler_<group>.go for large feature groups).
// handler.go is the authoritative inventory of what actually works.
//
// It runs past the usual 400-line budget on purpose: it is a flat inventory
// of one-line stubs, and splitting it would scatter that inventory.

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ---- Bucket GET stubs ------------------------------------------------------

// GetBucketAcl handles GET /{bucket}?acl
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketAcl.html
func (h *Handler) GetBucketAcl(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketCors handles GET /{bucket}?cors
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketCors.html
// Implemented in handler_bucket.go.
func (h *Handler) GetBucketCors(w http.ResponseWriter, r *http.Request) {
	h.getBucketCors(w, r)
}

// GetBucketLifecycleConfiguration handles GET /{bucket}?lifecycle
// Covers both GetBucketLifecycle (deprecated) and GetBucketLifecycleConfiguration.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketLifecycleConfiguration.html
// Implemented in handler_lifecycle.go.
func (h *Handler) GetBucketLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	h.getBucketLifecycleConfiguration(w, r)
}

// GetBucketWebsite handles GET /{bucket}?website
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketWebsite.html
// Implemented in handler_bucket.go.
func (h *Handler) GetBucketWebsite(w http.ResponseWriter, r *http.Request) {
	h.getBucketWebsite(w, r)
}

// GetBucketLogging handles GET /{bucket}?logging
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketLogging.html
func (h *Handler) GetBucketLogging(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketReplication handles GET /{bucket}?replication
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketReplication.html
func (h *Handler) GetBucketReplication(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketAccelerateConfiguration handles GET /{bucket}?accelerate
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketAccelerateConfiguration.html
func (h *Handler) GetBucketAccelerateConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketRequestPayment handles GET /{bucket}?requestPayment
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketRequestPayment.html
func (h *Handler) GetBucketRequestPayment(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketOwnershipControls handles GET /{bucket}?ownershipControls
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketOwnershipControls.html
func (h *Handler) GetBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetPublicAccessBlock handles GET /{bucket}?publicAccessBlock
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetPublicAccessBlock.html
func (h *Handler) GetPublicAccessBlock(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ListObjects handles GET /{bucket} (legacy v1 listing, no list-type param).
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjects.html
//
// Implemented in handler_bucket.go as ListObjectsV1.
func (h *Handler) ListObjects(w http.ResponseWriter, r *http.Request) {
	h.ListObjectsV1(w, r)
}

// GetBucketAnalyticsConfiguration handles GET /{bucket}?analytics&id=
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketAnalyticsConfiguration.html
func (h *Handler) GetBucketAnalyticsConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ListBucketAnalyticsConfigurations handles GET /{bucket}?analytics
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBucketAnalyticsConfigurations.html
func (h *Handler) ListBucketAnalyticsConfigurations(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketIntelligentTieringConfiguration handles GET /{bucket}?intelligent-tiering&id=
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketIntelligentTieringConfiguration.html
func (h *Handler) GetBucketIntelligentTieringConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ListBucketIntelligentTieringConfigurations handles GET /{bucket}?intelligent-tiering
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBucketIntelligentTieringConfigurations.html
func (h *Handler) ListBucketIntelligentTieringConfigurations(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketInventoryConfiguration handles GET /{bucket}?inventory&id=
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketInventoryConfiguration.html
func (h *Handler) GetBucketInventoryConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ListBucketInventoryConfigurations handles GET /{bucket}?inventory
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBucketInventoryConfigurations.html
func (h *Handler) ListBucketInventoryConfigurations(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketMetricsConfiguration handles GET /{bucket}?metrics&id=
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketMetricsConfiguration.html
func (h *Handler) GetBucketMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ListBucketMetricsConfigurations handles GET /{bucket}?metrics
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBucketMetricsConfigurations.html
func (h *Handler) ListBucketMetricsConfigurations(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetObjectLockConfiguration handles GET /{bucket}?object-lock
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectLockConfiguration.html
func (h *Handler) GetObjectLockConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketAbac handles GET /{bucket}?abac
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketAbac.html
func (h *Handler) GetBucketAbac(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketMetadataConfiguration handles GET /{bucket}?metadataConfiguration
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketMetadataConfiguration.html
func (h *Handler) GetBucketMetadataConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetBucketMetadataTableConfiguration handles GET /{bucket}?metadataTable
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketMetadataTableConfiguration.html
func (h *Handler) GetBucketMetadataTableConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// CreateSession handles GET /{bucket}?session
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateSession.html
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ListDirectoryBuckets handles GET /?directory-buckets
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListDirectoryBuckets.html
func (h *Handler) ListDirectoryBuckets(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ---- Bucket PUT stubs ------------------------------------------------------

// PutBucketAcl handles PUT /{bucket}?acl
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketAcl.html
func (h *Handler) PutBucketAcl(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketCors handles PUT /{bucket}?cors
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketCors.html
// Implemented in handler_bucket.go.
func (h *Handler) PutBucketCors(w http.ResponseWriter, r *http.Request) {
	h.putBucketCors(w, r)
}

// PutBucketLifecycleConfiguration handles PUT /{bucket}?lifecycle
// Covers both PutBucketLifecycle (deprecated) and PutBucketLifecycleConfiguration.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketLifecycleConfiguration.html
// Implemented in handler_lifecycle.go.
func (h *Handler) PutBucketLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	h.putBucketLifecycleConfiguration(w, r)
}

// PutBucketWebsite handles PUT /{bucket}?website
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketWebsite.html
// Implemented in handler_bucket.go.
func (h *Handler) PutBucketWebsite(w http.ResponseWriter, r *http.Request) {
	h.putBucketWebsite(w, r)
}

// PutBucketLogging handles PUT /{bucket}?logging
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketLogging.html
func (h *Handler) PutBucketLogging(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketReplication handles PUT /{bucket}?replication
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketReplication.html
func (h *Handler) PutBucketReplication(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketAccelerateConfiguration handles PUT /{bucket}?accelerate
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketAccelerateConfiguration.html
func (h *Handler) PutBucketAccelerateConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketRequestPayment handles PUT /{bucket}?requestPayment
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketRequestPayment.html
func (h *Handler) PutBucketRequestPayment(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketOwnershipControls handles PUT /{bucket}?ownershipControls
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketOwnershipControls.html
func (h *Handler) PutBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutPublicAccessBlock handles PUT /{bucket}?publicAccessBlock
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutPublicAccessBlock.html
func (h *Handler) PutPublicAccessBlock(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketAnalyticsConfiguration handles PUT /{bucket}?analytics
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketAnalyticsConfiguration.html
func (h *Handler) PutBucketAnalyticsConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketIntelligentTieringConfiguration handles PUT /{bucket}?intelligent-tiering
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketIntelligentTieringConfiguration.html
func (h *Handler) PutBucketIntelligentTieringConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketInventoryConfiguration handles PUT /{bucket}?inventory
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketInventoryConfiguration.html
func (h *Handler) PutBucketInventoryConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketMetricsConfiguration handles PUT /{bucket}?metrics
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketMetricsConfiguration.html
func (h *Handler) PutBucketMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutObjectLockConfiguration handles PUT /{bucket}?object-lock
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectLockConfiguration.html
func (h *Handler) PutObjectLockConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutBucketAbac handles PUT /{bucket}?abac
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketAbac.html
func (h *Handler) PutBucketAbac(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// UpdateBucketMetadataInventoryTableConfiguration handles PUT /{bucket}?metadataInventoryTable
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_UpdateBucketMetadataInventoryTableConfiguration.html
func (h *Handler) UpdateBucketMetadataInventoryTableConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// UpdateBucketMetadataJournalTableConfiguration handles PUT /{bucket}?metadataJournalTable
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_UpdateBucketMetadataJournalTableConfiguration.html
func (h *Handler) UpdateBucketMetadataJournalTableConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// UpdateBucketMetadataAnnotationTableConfiguration handles PUT /{bucket}?metadataAnnotationTable
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_UpdateBucketMetadataAnnotationTableConfiguration.html
func (h *Handler) UpdateBucketMetadataAnnotationTableConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ---- Bucket DELETE stubs ---------------------------------------------------

// DeleteBucketCors handles DELETE /{bucket}?cors
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketCors.html
// Implemented in handler_bucket.go.
func (h *Handler) DeleteBucketCors(w http.ResponseWriter, r *http.Request) {
	h.deleteBucketCors(w, r)
}

// DeleteBucketLifecycle handles DELETE /{bucket}?lifecycle
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketLifecycle.html
// Implemented in handler_lifecycle.go.
func (h *Handler) DeleteBucketLifecycle(w http.ResponseWriter, r *http.Request) {
	h.deleteBucketLifecycle(w, r)
}

// DeleteBucketWebsite handles DELETE /{bucket}?website
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketWebsite.html
// Implemented in handler_bucket.go.
func (h *Handler) DeleteBucketWebsite(w http.ResponseWriter, r *http.Request) {
	h.deleteBucketWebsite(w, r)
}

// DeleteBucketReplication handles DELETE /{bucket}?replication
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketReplication.html
func (h *Handler) DeleteBucketReplication(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketAnalyticsConfiguration handles DELETE /{bucket}?analytics
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketAnalyticsConfiguration.html
func (h *Handler) DeleteBucketAnalyticsConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketIntelligentTieringConfiguration handles DELETE /{bucket}?intelligent-tiering
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketIntelligentTieringConfiguration.html
func (h *Handler) DeleteBucketIntelligentTieringConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketInventoryConfiguration handles DELETE /{bucket}?inventory
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketInventoryConfiguration.html
func (h *Handler) DeleteBucketInventoryConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketMetricsConfiguration handles DELETE /{bucket}?metrics
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketMetricsConfiguration.html
func (h *Handler) DeleteBucketMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketOwnershipControls handles DELETE /{bucket}?ownershipControls
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketOwnershipControls.html
func (h *Handler) DeleteBucketOwnershipControls(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeletePublicAccessBlock handles DELETE /{bucket}?publicAccessBlock
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeletePublicAccessBlock.html
func (h *Handler) DeletePublicAccessBlock(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketMetadataConfiguration handles DELETE /{bucket}?metadataConfiguration
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketMetadataConfiguration.html
func (h *Handler) DeleteBucketMetadataConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// DeleteBucketMetadataTableConfiguration handles DELETE /{bucket}?metadataTable
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteBucketMetadataTableConfiguration.html
func (h *Handler) DeleteBucketMetadataTableConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ---- Bucket POST stubs -----------------------------------------------------

// CreateBucketMetadataConfiguration handles POST /{bucket}?metadataConfiguration
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateBucketMetadataConfiguration.html
func (h *Handler) CreateBucketMetadataConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// CreateBucketMetadataTableConfiguration handles POST /{bucket}?metadataTable
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateBucketMetadataTableConfiguration.html
func (h *Handler) CreateBucketMetadataTableConfiguration(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ---- Object GET stubs ------------------------------------------------------

// GetObjectAcl handles GET /{bucket}/{key}?acl
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAcl.html
func (h *Handler) GetObjectAcl(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetObjectAttributes handles GET /{bucket}/{key}?attributes
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAttributes.html
func (h *Handler) GetObjectAttributes(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetObjectLegalHold handles GET /{bucket}/{key}?legal-hold
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectLegalHold.html
func (h *Handler) GetObjectLegalHold(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetObjectRetention handles GET /{bucket}/{key}?retention
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectRetention.html
func (h *Handler) GetObjectRetention(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetObjectTorrent handles GET /{bucket}/{key}?torrent
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectTorrent.html
func (h *Handler) GetObjectTorrent(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// GetObjectAnnotation handles GET /{bucket}/{key}?annotation&annotationName=
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAnnotation.html
func (h *Handler) GetObjectAnnotation(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ListObjectAnnotations handles GET /{bucket}/{key}?annotation
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectAnnotations.html
func (h *Handler) ListObjectAnnotations(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ---- Object PUT stubs ------------------------------------------------------

// PutObjectAcl handles PUT /{bucket}/{key}?acl
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectAcl.html
func (h *Handler) PutObjectAcl(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutObjectLegalHold handles PUT /{bucket}/{key}?legal-hold
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectLegalHold.html
func (h *Handler) PutObjectLegalHold(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutObjectRetention handles PUT /{bucket}/{key}?retention
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectRetention.html
func (h *Handler) PutObjectRetention(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// RenameObject handles PUT /{bucket}/{key}?renameObject
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_RenameObject.html
func (h *Handler) RenameObject(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// UpdateObjectEncryption handles PUT /{bucket}/{key}?encryption
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_UpdateObjectEncryption.html
func (h *Handler) UpdateObjectEncryption(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// PutObjectAnnotation handles PUT /{bucket}/{key}?annotation
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectAnnotation.html
func (h *Handler) PutObjectAnnotation(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// UploadPartCopy handles PUT /{bucket}/{key}?partNumber=N with x-amz-copy-source header.
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html
func (h *Handler) UploadPartCopy(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ---- Object DELETE stubs ---------------------------------------------------

// DeleteObjectAnnotation handles DELETE /{bucket}/{key}?annotation
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjectAnnotation.html
func (h *Handler) DeleteObjectAnnotation(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// ---- Object POST stubs -----------------------------------------------------

// RestoreObject handles POST /{bucket}/{key}?restore
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_RestoreObject.html
func (h *Handler) RestoreObject(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// SelectObjectContent handles POST /{bucket}/{key}?select&select-type=2
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_SelectObjectContent.html
func (h *Handler) SelectObjectContent(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}

// WriteGetObjectResponse handles POST /WriteGetObjectResponse
// AWS docs: https://docs.aws.amazon.com/AmazonS3/latest/API/API_WriteGetObjectResponse.html
func (h *Handler) WriteGetObjectResponse(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedXML(w, r)
}
