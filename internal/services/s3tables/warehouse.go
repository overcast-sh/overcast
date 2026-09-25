package s3tables

// A table's warehouse: reading and writing its Iceberg metadata files through
// the in-process S3 accessor, and the one compare-and-swap that points a
// table at a new metadata file.

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
)

const s3Scheme = "s3://"

// splitS3URI takes "s3://bucket/key" apart; ok is false for anything else.
func splitS3URI(uri string) (bucket, key string, ok bool) {
	rest, found := strings.CutPrefix(uri, s3Scheme)
	if !found {
		return "", "", false
	}
	bucket, key, _ = strings.Cut(rest, "/")
	return bucket, key, bucket != ""
}

// writeMetadataFile writes raw as the version-th metadata file under the
// table location and returns the file's s3:// location.
func (s *Service) writeMetadataFile(ctx context.Context, tableLocation string, version int, raw []byte) (string, *protocol.AWSError) {
	bucket, prefix, ok := splitS3URI(tableLocation)
	if !ok {
		return "", badRequest("The table location " + tableLocation + " is not an s3:// location.")
	}
	key := icebergmeta.MetadataPath(version, s.newID())
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		key = prefix + "/" + key
	}
	if _, aerr := s.putObject(ctx, bucket, key, raw, s3PutJSON); aerr != nil {
		return "", aerr
	}
	return s3Scheme + bucket + "/" + key, nil
}

// readMetadata reads and parses the metadata file at location. A file that is
// missing or is not Iceberg metadata makes the table unreadable, which is
// answered as the table's absence rather than a server fault: the file is the
// caller's data, and nothing Overcast holds is broken.
func (s *Service) readMetadata(ctx context.Context, location string) (*icebergmeta.Metadata, *protocol.AWSError) {
	bucket, key, ok := splitS3URI(location)
	if !ok || key == "" {
		return nil, errUnreadableMetadata(location, "it is not an s3:// object")
	}
	raw, aerr := s.getObject(ctx, bucket, key, "")
	if aerr != nil {
		return nil, errUnreadableMetadata(location, aerr.Message)
	}
	meta, err := icebergmeta.Parse(raw)
	if err != nil {
		return nil, errUnreadableMetadata(location, err.Error())
	}
	return meta, nil
}

func errUnreadableMetadata(location, reason string) *protocol.AWSError {
	return notFound("The table's metadata file " + location + " could not be read: " + reason)
}

// metadataProposal decides where a table's metadata pointer moves, given the
// table as it stands under the service lock — t is nil when the table does
// not exist, and a proposal that creates it returns the new record. An empty
// location means "leave the table as it is". meta is the metadata at
// location when the proposer has parsed it, and nil otherwise.
type metadataProposal func(b *tableBucket, n *namespaceRecord, t *tableRecord) (_ *tableRecord, location string, meta *icebergmeta.Metadata, _ *protocol.AWSError)

// swapMetadata is the compare-and-swap every change to a table's metadata
// goes through: UpdateTableMetadataLocation's and the Iceberg REST catalog's
// commits alike. Under the service lock it resolves the namespace and the
// table, lets propose check the caller's precondition against the table as it
// now is, and points the table at the proposed location with a fresh version
// token. Of two writers racing from the same state exactly one wins; the
// other's proposal sees the winner's table and fails its check.
//
// The swap is published once the lock is released: its event may need the
// new metadata file read, which is no reason to hold up other writers.
func (s *Service) swapMetadata(ctx context.Context, bucketARN, namespace, name string, propose metadataProposal) (*tableRecord, *protocol.AWSError) {
	t, swap, aerr := s.swapLocked(ctx, bucketARN, namespace, name, propose)
	if swap != nil {
		s.publishSwap(ctx, swap)
	}
	return t, aerr
}

// swapLocked is swapMetadata's compare-and-swap, under the service lock. It
// describes the swap it made, or returns a nil swap when it made none.
func (s *Service) swapLocked(ctx context.Context, bucketARN, namespace, name string, propose metadataProposal) (*tableRecord, *metadataSwap, *protocol.AWSError) {
	defer s.lock()()
	b, n, aerr := s.resolveNamespace(ctx, bucketARN, namespace)
	if aerr != nil {
		return nil, nil, aerr
	}
	current, _, aerr := s.loadTable(ctx, b.Region, b.Name, n.Name, name)
	if aerr != nil {
		return nil, nil, aerr
	}
	swap := &metadataSwap{created: current == nil}
	if current != nil {
		swap.previous = current.MetadataLocation
	}
	t, location, meta, aerr := propose(b, n, current)
	if aerr != nil || location == "" {
		return t, nil, aerr
	}
	t.MetadataLocation = location
	t.VersionToken = s.newVersionToken()
	t.ModifiedAt, t.ModifiedBy = s.now(), s.accountID()
	if aerr := s.saveTable(ctx, t); aerr != nil {
		return t, nil, aerr
	}
	swap.table, swap.metadata = *t, meta
	return t, swap, nil
}
