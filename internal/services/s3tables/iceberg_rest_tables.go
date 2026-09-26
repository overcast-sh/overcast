package s3tables

// The Iceberg REST catalog's table endpoints. A table is the S3 Tables table
// of the same name, and its metadata is the file its metadata location points
// at in its warehouse bucket.

import (
	"context"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// icebergLoadTableResult is the spec's LoadTableResult, and its
// CommitTableResponse. A staged table has no metadata location yet.
type icebergLoadTableResult struct {
	MetadataLocation string                `json:"metadata-location,omitempty"`
	Metadata         *icebergmeta.Metadata `json:"metadata"`
}

// icebergTableIdentifier is the spec's TableIdentifier.
type icebergTableIdentifier struct {
	Namespace []string `json:"namespace"`
	Name      string   `json:"name"`
}

// ─── Create ───────────────────────────────────────────────────────────────────

// icebergCreateTableRequest is the spec's CreateTableRequest.
type icebergCreateTableRequest struct {
	icebergPath
	Name          string                     `json:"name"`
	Location      string                     `json:"location,omitempty"`
	Schema        *icebergmeta.Schema        `json:"schema"`
	PartitionSpec *icebergmeta.PartitionSpec `json:"partition-spec,omitempty"`
	WriteOrder    *icebergmeta.SortOrder     `json:"write-order,omitempty"`
	StageCreate   bool                       `json:"stage-create,omitempty"`
	Properties    map[string]string          `json:"properties,omitempty"`
}

// createSpec is the table the request describes, with the given identity.
func (req *icebergCreateTableRequest) createSpec(tableUUID, location string) icebergmeta.CreateSpec {
	spec := icebergmeta.CreateSpec{
		TableUUID: tableUUID, Location: location, Fields: req.Schema.Fields,
		IdentifierFieldIDs: req.Schema.IdentifierFieldIDs, Properties: req.Properties,
	}
	if req.PartitionSpec != nil {
		spec.PartitionFields = req.PartitionSpec.Fields
	}
	if req.WriteOrder != nil {
		spec.SortFields = req.WriteOrder.Fields
	}
	return spec
}

func (req *icebergCreateTableRequest) validate() *protocol.AWSError {
	if aerr := validateTableName(req.Name); aerr != nil {
		return aerr
	}
	if req.Schema == nil {
		return badRequest("A table needs a schema.")
	}
	if req.Location != "" {
		return badRequest("S3 Tables chooses a table's location; create the table without one.")
	}
	return nil
}

// icebergCreateTableTyped creates a table with its first metadata file, or — for
// stage-create — only describes the table a later commit will create.
func (s *Service) icebergCreateTableTyped(ctx context.Context, req *icebergCreateTableRequest) (*icebergLoadTableResult, *protocol.AWSError) {
	if aerr := req.validate(); aerr != nil {
		return nil, aerr
	}
	if req.StageCreate {
		return s.icebergStageCreate(ctx, req)
	}
	var meta *icebergmeta.Metadata
	t, aerr := s.createTable(ctx, &createTableRequest{
		TableBucketARN: req.BucketARN, Namespace: req.Namespace, Name: req.Name, Format: formatIceberg,
	}, func(t *tableRecord) ([]byte, *protocol.AWSError) {
		var aerr *protocol.AWSError
		if meta, aerr = newMetadata(req.createSpec(t.TableID, t.WarehouseLocation), t.CreatedAt); aerr != nil {
			return nil, aerr
		}
		return marshalMetadata(meta)
	})
	if aerr != nil {
		return nil, aerr
	}
	return &icebergLoadTableResult{MetadataLocation: t.MetadataLocation, Metadata: meta}, nil
}

// icebergStageCreate answers stage-create: the metadata of a table that does
// not exist until a commit carrying assert-create creates it. AWS's catalog
// refuses stage-create with 400, which is why CTAS cannot run against it;
// Trino creates tables through it (CREATE TABLE and CTAS), so Overcast
// supports it deliberately, as a documented difference. The warehouse bucket
// is created now, because the engine writes the table's data files there
// before it commits.
func (s *Service) icebergStageCreate(ctx context.Context, req *icebergCreateTableRequest) (*icebergLoadTableResult, *protocol.AWSError) {
	b, n, aerr := s.resolveNamespace(ctx, req.BucketARN, req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	if _, found, aerr := s.loadTable(ctx, b.Region, b.Name, n.Name, req.Name); aerr != nil {
		return nil, aerr
	} else if found {
		return nil, errTableExists
	}
	warehouse := s.newWarehouseBucketName()
	meta, aerr := newMetadata(req.createSpec(s.newID(), s3Scheme+warehouse), s.now())
	if aerr != nil {
		return nil, aerr
	}
	if s.ensureWarehouse != nil {
		if aerr := s.ensureWarehouse(ctx, warehouse, b.Region); aerr != nil {
			return nil, aerr
		}
	}
	return &icebergLoadTableResult{Metadata: meta}, nil
}

// ─── Register ─────────────────────────────────────────────────────────────────

// icebergRegisterTableRequest is the spec's RegisterTableRequest.
type icebergRegisterTableRequest struct {
	icebergPath
	Name             string `json:"name"`
	MetadataLocation string `json:"metadata-location"`
	Overwrite        bool   `json:"overwrite,omitempty"`
}

// icebergRegisterTableTyped adopts a table whose metadata file already
// exists, in the warehouse its metadata names. With overwrite, an existing
// table of that name is pointed at the file instead.
func (s *Service) icebergRegisterTableTyped(ctx context.Context, req *icebergRegisterTableRequest) (*icebergLoadTableResult, *protocol.AWSError) {
	if aerr := validateTableName(req.Name); aerr != nil {
		return nil, aerr
	}
	meta, aerr := s.readMetadata(ctx, req.MetadataLocation)
	if aerr != nil {
		return nil, aerr
	}
	location := strings.TrimSuffix(meta.Location, "/")
	if !strings.HasPrefix(req.MetadataLocation, location+"/") {
		return nil, badRequest("The metadata file must lie under the table's location " + meta.Location + ".")
	}
	t, aerr := s.swapMetadata(ctx, req.BucketARN, req.Namespace, req.Name, func(b *tableBucket, n *namespaceRecord, t *tableRecord) (*tableRecord, string, *icebergmeta.Metadata, *protocol.AWSError) {
		switch {
		case t == nil:
			t, aerr := s.newCatalogTable(ctx, b, n, req.Name, location, meta.TableUUID)
			return t, req.MetadataLocation, meta, aerr
		case !req.Overwrite:
			return nil, "", nil, errTableExists
		}
		if aerr := s.claimWarehouse(ctx, b, t, location); aerr != nil {
			return nil, "", nil, aerr
		}
		t.WarehouseLocation = location
		return t, req.MetadataLocation, meta, nil
	})
	if aerr != nil {
		return nil, aerr
	}
	return &icebergLoadTableResult{MetadataLocation: t.MetadataLocation, Metadata: meta}, nil
}

// ─── Load, drop, rename ───────────────────────────────────────────────────────

func (s *Service) icebergLoadTableTyped(ctx context.Context, req *icebergPath) (*icebergLoadTableResult, *protocol.AWSError) {
	_, t, aerr := s.resolveTable(ctx, req.BucketARN, req.Namespace, req.Table)
	if aerr != nil {
		return nil, aerr
	}
	if t.MetadataLocation == "" {
		return nil, icebergError(http.StatusNotFound, "NoSuchTableException",
			"The table has no Iceberg metadata yet: it was created without a schema and nothing has committed to it.")
	}
	meta, aerr := s.readMetadata(ctx, t.MetadataLocation)
	if aerr != nil {
		return nil, aerr
	}
	return &icebergLoadTableResult{MetadataLocation: t.MetadataLocation, Metadata: meta}, nil
}

func (s *Service) icebergTableExistsTyped(ctx context.Context, req *icebergPath) *protocol.AWSError {
	_, _, aerr := s.resolveTable(ctx, req.BucketARN, req.Namespace, req.Table)
	return aerr
}

// icebergDropTableRequest is dropTable's path and its purgeRequested query
// parameter.
type icebergDropTableRequest struct {
	icebergPath
	PurgeRequested bool
}

func fillIcebergDropTable(r *http.Request, req *icebergDropTableRequest) *protocol.AWSError {
	req.bind(r)
	// Case-insensitive: PyIceberg sends Python's "True".
	req.PurgeRequested = strings.EqualFold(r.URL.Query().Get("purgeRequested"), "true")
	return nil
}

// errDropWithoutPurge is AWS's answer to a drop that does not purge: its
// catalog supports only purging drops. The wording is Overcast's.
var errDropWithoutPurge = badRequest("S3 Tables drops a table only with purgeRequested=true.")

// icebergDropTableTyped removes the table from the catalog. As on AWS, only a
// purging drop is accepted; like DeleteTable, it leaves the files in the
// warehouse, where AWS deletes them in the background.
func (s *Service) icebergDropTableTyped(ctx context.Context, req *icebergDropTableRequest) *protocol.AWSError {
	if !req.PurgeRequested {
		return errDropWithoutPurge
	}
	_, aerr := s.deleteTableTyped(ctx, &tableRequest{TableBucketARN: req.BucketARN, Namespace: req.Namespace, Name: req.Table})
	return aerr
}

// icebergListTablesResponse is the spec's ListTablesResponse.
type icebergListTablesResponse struct {
	Identifiers   []icebergTableIdentifier `json:"identifiers"`
	NextPageToken string                   `json:"next-page-token,omitempty"`
}

func (s *Service) icebergListTablesTyped(ctx context.Context, req *icebergListRequest) (*icebergListTablesResponse, *protocol.AWSError) {
	b, n, aerr := s.resolveNamespace(ctx, req.BucketARN, req.Namespace)
	if aerr != nil {
		return nil, aerr
	}
	tables, aerr := s.listTables(ctx, b.Region, b.Name, n.Name)
	if aerr != nil {
		return nil, aerr
	}
	ids := make([]icebergTableIdentifier, 0, len(tables))
	for _, t := range tables {
		ids = append(ids, icebergTableIdentifier{Namespace: []string{t.Namespace}, Name: t.Name})
	}
	page, aerr := paginate(ids, req.PageSize, req.PageToken)
	if aerr != nil {
		return nil, aerr
	}
	return &icebergListTablesResponse{Identifiers: page.Items, NextPageToken: page.NextToken}, nil
}

// icebergRenameTableRequest is the spec's RenameTableRequest.
type icebergRenameTableRequest struct {
	icebergPath
	Source      icebergTableIdentifier `json:"source"`
	Destination icebergTableIdentifier `json:"destination"`
}

func (s *Service) icebergRenameTableTyped(ctx context.Context, req *icebergRenameTableRequest) *protocol.AWSError {
	from, aerr := singleLevel(req.Source.Namespace)
	if aerr != nil {
		return aerr
	}
	to, aerr := singleLevel(req.Destination.Namespace)
	if aerr != nil {
		return aerr
	}
	_, aerr = s.renameTableTyped(ctx, &renameTableRequest{
		TableBucketARN: req.BucketARN, Namespace: from, Name: req.Source.Name,
		NewNamespaceName: to, NewName: req.Destination.Name,
	})
	return aerr
}
