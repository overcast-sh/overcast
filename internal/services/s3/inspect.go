package s3

// Metadata-only accessors for internal callers that must decide about an object
// without reading its body — Lambda Web Functions pins a revision to an object
// version at CreateWebFunctionRevision and only fetches the bytes later.

import (
	"context"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ObjectHead is what HeadObjectVersion reports about one object version.
type ObjectHead struct {
	// VersionID is the version's id as S3 reports it ("null" for a null
	// version). It is the id GetObjectBytes accepts to read exactly this version.
	VersionID string
	// Size is the object's length in bytes; zero for a delete marker.
	Size int64
	// DeleteMarker is true when the addressed version is a delete marker: the
	// key reads as absent, and there are no bytes to fetch.
	DeleteMarker bool
}

// BucketVersioning returns the bucket's versioning status — "Enabled",
// "Suspended", or "" for a bucket that has never been versioned — and
// NoSuchBucket when the bucket does not exist.
func (s *Service) BucketVersioning(ctx context.Context, bucket string) (string, *protocol.AWSError) {
	b, aerr := s.handler.store.getBucket(ctx, bucket)
	if aerr != nil {
		return "", aerr
	}
	return b.VersioningStatus, nil
}

// HeadObjectVersion is HeadObject for an internal caller: it reads metadata
// only. An empty versionID addresses the key's current version; otherwise it
// addresses exactly that version. NoSuchBucket, NoSuchKey and NoSuchVersion
// are the errors HeadObject answers with over HTTP. A delete marker is not an
// error here — the caller learns of it from ObjectHead.DeleteMarker.
func (s *Service) HeadObjectVersion(ctx context.Context, bucket, key, versionID string) (ObjectHead, *protocol.AWSError) {
	var obj *Object
	var aerr *protocol.AWSError
	if versionID == "" {
		if _, aerr = s.handler.store.getBucket(ctx, bucket); aerr != nil {
			return ObjectHead{}, aerr
		}
		obj, aerr = s.handler.store.getObjectMeta(ctx, bucket, key)
	} else {
		obj, aerr = s.handler.resolveVersion(ctx, bucket, key, versionID)
	}
	if aerr != nil {
		return ObjectHead{}, aerr
	}
	return ObjectHead{VersionID: obj.wireVersionID(), Size: obj.ContentLength, DeleteMarker: obj.DeleteMarker}, nil
}
