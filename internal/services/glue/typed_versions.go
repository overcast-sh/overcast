package glue

import (
	"context"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Table versions. A table's current definition is its newest version; each
// UpdateTable without SkipArchive keeps the definition it replaced as an
// archived version. GetTableVersions lists the current version and the
// archived ones, newest first. Only an archived version can be deleted: AWS
// refuses to delete the current one (DeleteTable is how that goes).

type getTableVersionReq struct {
	CatalogId    string `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName string `json:"DatabaseName" cbor:"DatabaseName"`
	TableName    string `json:"TableName" cbor:"TableName"`
	VersionId    string `json:"VersionId" cbor:"VersionId"`
}

type getTableVersionResp struct {
	TableVersion *TableVersion `json:"TableVersion" cbor:"TableVersion"`
}

type getTableVersionsReq struct {
	CatalogId    string `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName string `json:"DatabaseName" cbor:"DatabaseName"`
	TableName    string `json:"TableName" cbor:"TableName"`
	MaxResults   int32  `json:"MaxResults" cbor:"MaxResults"`
	NextToken    string `json:"NextToken" cbor:"NextToken"`
}

type getTableVersionsResp struct {
	TableVersions []*TableVersion `json:"TableVersions" cbor:"TableVersions"`
	NextToken     string          `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type deleteTableVersionReq struct {
	CatalogId    string `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName string `json:"DatabaseName" cbor:"DatabaseName"`
	TableName    string `json:"TableName" cbor:"TableName"`
	VersionId    string `json:"VersionId" cbor:"VersionId"`
}

type batchDeleteTableVersionReq struct {
	CatalogId    string   `json:"CatalogId" cbor:"CatalogId"`
	DatabaseName string   `json:"DatabaseName" cbor:"DatabaseName"`
	TableName    string   `json:"TableName" cbor:"TableName"`
	VersionIds   []string `json:"VersionIds" cbor:"VersionIds"`
}

type batchDeleteTableVersionResp struct {
	Errors []TableVersionError `json:"Errors" cbor:"Errors"`
}

// maxBatchDeleteTableVersions is BatchDeleteTableVersionList's length limit.
const maxBatchDeleteTableVersions = 100

func errVersionNotFound(versionID string) *protocol.AWSError {
	return glueError(codeEntityNotFound, "Table version %s not found.", versionID)
}

func (s *Service) getTableVersionTyped(ctx context.Context, req *getTableVersionReq) (*getTableVersionResp, *protocol.AWSError) {
	dbName, name := normName(req.DatabaseName), normName(req.TableName)
	cur, aerr := s.requireTable(ctx, dbName, name)
	if aerr != nil {
		return nil, aerr
	}
	if req.VersionId == "" || req.VersionId == cur.VersionId {
		return &getTableVersionResp{TableVersion: &TableVersion{Table: &cur.Table, VersionId: cur.VersionId}}, nil
	}
	t, found, err := s.store.getTableVersion(ctx, dbName, name, req.VersionId)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errVersionNotFound(req.VersionId)
	}
	return &getTableVersionResp{TableVersion: &TableVersion{Table: t, VersionId: t.VersionId}}, nil
}

func (s *Service) getTableVersionsTyped(ctx context.Context, req *getTableVersionsReq) (*getTableVersionsResp, *protocol.AWSError) {
	dbName, name := normName(req.DatabaseName), normName(req.TableName)
	cur, aerr := s.requireTable(ctx, dbName, name)
	if aerr != nil {
		return nil, aerr
	}
	archived, err := s.store.listTableVersions(ctx, dbName, name)
	if err != nil {
		return nil, errInternal(err)
	}
	versions := make([]*TableVersion, 0, len(archived)+1)
	versions = append(versions, &TableVersion{Table: &cur.Table, VersionId: cur.VersionId})
	for _, t := range archived {
		versions = append(versions, &TableVersion{Table: t, VersionId: t.VersionId})
	}
	page, aerr := paginate(versions, req.MaxResults, req.NextToken, catalogPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &getTableVersionsResp{TableVersions: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) deleteTableVersionTyped(ctx context.Context, req *deleteTableVersionReq) (*struct{}, *protocol.AWSError) {
	dbName, name := normName(req.DatabaseName), normName(req.TableName)
	defer s.locks.Lock("table:" + tableKey(dbName, name))()
	cur, aerr := s.requireTable(ctx, dbName, name)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.deleteOneVersion(ctx, cur, req.VersionId); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) batchDeleteTableVersionTyped(ctx context.Context, req *batchDeleteTableVersionReq) (*batchDeleteTableVersionResp, *protocol.AWSError) {
	if len(req.VersionIds) > maxBatchDeleteTableVersions {
		return nil, errInvalidInput("VersionIds must hold at most %d versions.", maxBatchDeleteTableVersions)
	}
	dbName, name := normName(req.DatabaseName), normName(req.TableName)
	defer s.locks.Lock("table:" + tableKey(dbName, name))()
	cur, aerr := s.requireTable(ctx, dbName, name)
	if aerr != nil {
		return nil, aerr
	}
	resp := &batchDeleteTableVersionResp{Errors: []TableVersionError{}}
	for _, v := range req.VersionIds {
		if aerr := s.deleteOneVersion(ctx, cur, v); aerr != nil {
			resp.Errors = append(resp.Errors, TableVersionError{TableName: req.TableName, VersionId: v, ErrorDetail: errorDetail(aerr)})
		}
	}
	return resp, nil
}

// deleteOneVersion deletes one archived version of cur. The caller holds the
// table's lock.
func (s *Service) deleteOneVersion(ctx context.Context, cur *tableRecord, versionID string) *protocol.AWSError {
	if versionID == "" {
		return errInvalidInput("VersionId is required.")
	}
	if versionID == cur.VersionId {
		return errInvalidInput("Cannot delete the current version %s of table %s.", versionID, cur.Name)
	}
	_, found, err := s.store.getTableVersion(ctx, cur.DatabaseName, cur.Name, versionID)
	if err != nil {
		return errInternal(err)
	}
	if !found {
		return errVersionNotFound(versionID)
	}
	if err := s.store.deleteTableVersion(ctx, cur.DatabaseName, cur.Name, versionID); err != nil {
		return errInternal(err)
	}
	return nil
}
