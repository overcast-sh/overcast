package events

import (
	"context"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// The in-process S3 accessor: how another service reads, writes, lists and creates
// buckets in the emulated S3 without an HTTP round trip through the router —
// Athena writing query results to its OutputLocation, S3 Tables creating a
// table's warehouse bucket.
//
// The types live here, like the other cross-service contracts in this
// package, so a consuming service can name them without importing
// internal/services/s3. router.go hands a consumer the s3.Service method
// values, which satisfy the func types below; each method goes through the
// same code path as its HTTP operation and returns the same *protocol.AWSError
// (NoSuchBucket, InvalidBucketName, …) that operation would.

// S3PutObjectOptions carries the PutObject request members an internal writer
// may set. The zero value writes an application/octet-stream object with no
// user metadata.
type S3PutObjectOptions struct {
	// ContentType is the object's Content-Type; empty means
	// application/octet-stream, as for an HTTP PutObject that sends none.
	ContentType string
	// Metadata is the object's user metadata, keyed by name without the
	// x-amz-meta- prefix. Names are stored lower-cased, as S3 stores them.
	Metadata map[string]string
}

// S3PutObjectResult is what PutObject reports about the object it stored.
type S3PutObjectResult struct {
	// ETag is the quoted MD5 of the body, as PutObject's ETag header carries it.
	ETag string
	// VersionID is the stored version's id: empty for a bucket that has never
	// been versioned, "null" for a version-suspended one — the value, or the
	// absence, of PutObject's x-amz-version-id header.
	VersionID string
}

// S3ObjectSummary is one key in a listing, the members of ListObjectsV2's
// Contents element an internal reader needs.
type S3ObjectSummary struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// S3ObjectListPage is one page of a listing. NextContinuationToken is empty on
// the last page; otherwise pass it back to read the next one.
type S3ObjectListPage struct {
	Objects               []S3ObjectSummary
	NextContinuationToken string
}

// S3PutObjectFunc stores body at bucket/key, exactly as PutObject would:
// versioning, the ETag and the bucket's event notifications all behave as
// they do for an HTTP write.
type S3PutObjectFunc func(ctx context.Context, bucket, key string, body []byte, opts S3PutObjectOptions) (S3PutObjectResult, *protocol.AWSError)

// S3ListObjectsFunc lists the keys under prefix one page at a time, as
// ListObjectsV2 does without a delimiter. maxKeys <= 0 means ListObjectsV2's
// default page of 1,000, and larger values are capped there.
type S3ListObjectsFunc func(ctx context.Context, bucket, prefix, continuationToken string, maxKeys int) (S3ObjectListPage, *protocol.AWSError)

// S3GetObjectFunc reads an object's body, exactly as GetObject would: an empty
// versionID reads the current version, and a missing bucket or key answers
// NoSuchBucket or NoSuchKey.
type S3GetObjectFunc func(ctx context.Context, bucket, key, versionID string) ([]byte, *protocol.AWSError)

// S3EnsureBucketFunc creates bucket in region unless it already exists, in
// which case it succeeds without touching it. An empty region means the
// emulator's configured region.
type S3EnsureBucketFunc func(ctx context.Context, bucket, region string) *protocol.AWSError
