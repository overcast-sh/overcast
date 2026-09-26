package s3tables

// The Iceberg REST catalog's commit: requirements checked and updates applied
// by internal/icebergmeta against the table's current metadata, the result
// written as the next metadata file, and the table pointed at it — all inside
// swapMetadata, the compare-and-swap UpdateTableMetadataLocation uses too.

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// icebergCommitTableRequest is the spec's CommitTableRequest. Its identifier
// repeats the path and is not read.
type icebergCommitTableRequest struct {
	icebergPath
	icebergmeta.CommitRequest
}

var errLocationChange = badRequest("S3 Tables does not let a table's location change.")

func (s *Service) icebergCommitTableTyped(ctx context.Context, req *icebergCommitTableRequest) (*icebergLoadTableResult, *protocol.AWSError) {
	var result *icebergmeta.Metadata
	t, aerr := s.swapMetadata(ctx, req.BucketARN, req.Namespace, req.Table, func(b *tableBucket, n *namespaceRecord, t *tableRecord) (*tableRecord, string, *icebergmeta.Metadata, *protocol.AWSError) {
		next, t, location, aerr := s.proposeCommit(ctx, b, n, t, req)
		result = next
		return t, location, next, aerr
	})
	if aerr != nil {
		return nil, aerr
	}
	return &icebergLoadTableResult{MetadataLocation: t.MetadataLocation, Metadata: result}, nil
}

// proposeCommit applies the commit to the table as it stands under the lock,
// and returns the metadata the table ends up with, the table, and the new
// metadata file (empty when the commit changes nothing, so nothing is
// written). A table that does not exist can only be created, by a commit
// carrying assert-create — the second half of a staged create. A table that
// exists keeps its location.
func (s *Service) proposeCommit(ctx context.Context, b *tableBucket, n *namespaceRecord, t *tableRecord, req *icebergCommitTableRequest) (*icebergmeta.Metadata, *tableRecord, string, *protocol.AWSError) {
	if t == nil && !slices.ContainsFunc(req.Requirements, func(r icebergmeta.Requirement) bool { return r.Type == icebergmeta.AssertCreate }) {
		return nil, nil, "", errTableNotFound
	}
	base, current, aerr := s.currentMetadata(ctx, t)
	if aerr != nil {
		return nil, nil, "", aerr
	}
	next, err := icebergmeta.Commit(base, current, req.CommitRequest, s.now())
	if err != nil {
		return nil, nil, "", icebergmetaError(err)
	}
	if next == base {
		return base, t, "", nil
	}
	location := strings.TrimSuffix(next.Location, "/")
	switch {
	case t == nil:
		if t, aerr = s.newCatalogTable(ctx, b, n, req.Table, location, next.TableUUID); aerr != nil {
			return nil, nil, "", aerr
		}
	case location != t.WarehouseLocation:
		return nil, nil, "", errLocationChange
	}
	raw, aerr := marshalMetadata(next)
	if aerr != nil {
		return nil, nil, "", aerr
	}
	written, aerr := s.writeMetadataFile(ctx, t.WarehouseLocation, icebergmeta.NextMetadataVersion(current), raw)
	return next, t, written, aerr
}

// currentMetadata is the table's metadata and the file it was read from; both
// are empty for a table that does not exist or has no metadata yet.
func (s *Service) currentMetadata(ctx context.Context, t *tableRecord) (*icebergmeta.Metadata, string, *protocol.AWSError) {
	if t == nil || t.MetadataLocation == "" {
		return nil, "", nil
	}
	meta, aerr := s.readMetadata(ctx, t.MetadataLocation)
	return meta, t.MetadataLocation, aerr
}

// tableIDPattern is the table id a table ARN can carry (TableARN's pattern).
var tableIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

// newCatalogTable is the record for a table the catalog creates from metadata
// it did not build — a staged create's commit, or a registered table. Its id
// is the metadata's table-uuid, as for every table the catalog creates, so its
// ARN and its Iceberg identity agree, and its warehouse is the metadata's
// location.
func (s *Service) newCatalogTable(ctx context.Context, b *tableBucket, n *namespaceRecord, name, location, tableUUID string) (*tableRecord, *protocol.AWSError) {
	if aerr := validateTableName(name); aerr != nil {
		return nil, aerr
	}
	if !tableIDPattern.MatchString(tableUUID) {
		return nil, badRequest("The table-uuid " + tableUUID + " cannot identify an S3 Tables table.")
	}
	if _, taken, aerr := s.findTableByID(ctx, b.Region, b.Name, tableUUID); aerr != nil {
		return nil, aerr
	} else if taken {
		return nil, badRequest("The table-uuid " + tableUUID + " belongs to another table.")
	}
	if aerr := s.claimWarehouse(ctx, b, nil, location); aerr != nil {
		return nil, aerr
	}
	t := s.newTableRecord(b, n, &createTableRequest{Name: name})
	t.TableID, t.ARN, t.WarehouseLocation = tableUUID, tableARN(b.ARN, tableUUID), location
	return t, nil
}

// claimWarehouse checks that location can be the warehouse of the table
// owner (nil for one being created) in bucket b: a whole "--table-s3" bucket
// that no other table of b uses. It ensures the bucket exists, since the
// stage-create or the client that chose it may have been against another
// Overcast.
func (s *Service) claimWarehouse(ctx context.Context, b *tableBucket, owner *tableRecord, location string) *protocol.AWSError {
	warehouse, key, ok := serviceutil.SplitS3URI(location)
	if !ok || key != "" || serviceutil.TableWarehouseBucketName(warehouse) != nil {
		return badRequest("A table's location must be a whole --table-s3 warehouse bucket, not " + location + ".")
	}
	tables, aerr := s.listTables(ctx, b.Region, b.Name, "")
	if aerr != nil {
		return aerr
	}
	if slices.ContainsFunc(tables, func(t *tableRecord) bool {
		return t.WarehouseLocation == location && (owner == nil || t.TableID != owner.TableID)
	}) {
		return badRequest("The warehouse " + location + " belongs to another table.")
	}
	if s.ensureWarehouse == nil {
		return nil
	}
	return s.ensureWarehouse(ctx, warehouse, b.Region)
}
