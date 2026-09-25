package glue

// typed_catalogs.go — GetCatalog and GetCatalogs, over the catalogs Overcast
// has: the account's own (the root, which GetCatalogs lists only with
// IncludeRoot) and s3tablescatalog with one child per table bucket. Glue's
// CreateCatalog is not implemented, so s3tablescatalog is always there, as
// if S3 Tables' analytics integration had been enabled — which the S3
// console does by default.

import (
	"context"
	"fmt"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// catalogsPageSize is GetCatalogs' PageSize limit, 1–1000.
const catalogsPageSize = 1000

// catalogInfo is the model's Catalog structure, as far as Overcast fills it.
type catalogInfo struct {
	CatalogId                        string            `json:"CatalogId,omitempty"`
	Name                             string            `json:"Name"`
	ResourceArn                      string            `json:"ResourceArn,omitempty"`
	CreateTime                       float64           `json:"CreateTime,omitempty"`
	FederatedCatalog                 *federatedCatalog `json:"FederatedCatalog,omitempty"`
	CreateDatabaseDefaultPermissions []any             `json:"CreateDatabaseDefaultPermissions"`
	CreateTableDefaultPermissions    []any             `json:"CreateTableDefaultPermissions"`
	AllowFullTableExternalDataAccess string            `json:"AllowFullTableExternalDataAccess,omitempty"`
}

// federatedCatalog is the model's FederatedCatalog: the entity outside Glue
// a catalog stands for, and the connection it is reached through.
type federatedCatalog struct {
	Identifier     string `json:"Identifier,omitempty"`
	ConnectionName string `json:"ConnectionName,omitempty"`
}

type getCatalogReq struct {
	CatalogId string `json:"CatalogId" cbor:"CatalogId"`
}

type getCatalogResp struct {
	Catalog catalogInfo `json:"Catalog" cbor:"Catalog"`
}

type getCatalogsReq struct {
	ParentCatalogId string `json:"ParentCatalogId" cbor:"ParentCatalogId"`
	IncludeRoot     *bool  `json:"IncludeRoot" cbor:"IncludeRoot"`
	Recursive       bool   `json:"Recursive" cbor:"Recursive"`
	HasDatabases    *bool  `json:"HasDatabases" cbor:"HasDatabases"`
	MaxResults      int32  `json:"MaxResults" cbor:"MaxResults"`
	NextToken       string `json:"NextToken" cbor:"NextToken"`
}

type getCatalogsResp struct {
	CatalogList []catalogInfo `json:"CatalogList" cbor:"CatalogList"`
	NextToken   string        `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

func (s *Service) getCatalogTyped(ctx context.Context, req *getCatalogReq) (*getCatalogResp, *protocol.AWSError) {
	if req.CatalogId == "" {
		return nil, errInvalidInput("CatalogId is required.")
	}
	if req.CatalogId == s.cfg.AccountID {
		return &getCatalogResp{Catalog: s.rootCatalog(ctx)}, nil
	}
	p := s.parseCatalogID(req.CatalogId)
	if !p.federated() || s.s3tables == nil {
		return nil, errCatalogNotFound(req.CatalogId)
	}
	if p.kind == catalogS3Tables {
		return &getCatalogResp{Catalog: s.s3TablesCatalog(ctx)}, nil
	}
	b, found, err := s.s3tables.GetTableBucket(ctx, p.bucket)
	if err != nil {
		return nil, errInternal(err)
	}
	if !found {
		return nil, errCatalogNotFound(req.CatalogId)
	}
	return &getCatalogResp{Catalog: s.bucketCatalog(ctx, b)}, nil
}

// getCatalogsTyped lists the catalogs in ParentCatalogId: under the account
// (the default), s3tablescatalog, with the root first when IncludeRoot is
// set and the bucket catalogs too when Recursive is; under s3tablescatalog,
// the bucket catalogs; under a bucket catalog, nothing. HasDatabases is
// accepted and not applied.
func (s *Service) getCatalogsTyped(ctx context.Context, req *getCatalogsReq) (*getCatalogsResp, *protocol.AWSError) {
	parent := req.ParentCatalogId
	atRoot := parent == "" || parent == s.cfg.AccountID
	if req.IncludeRoot != nil && !atRoot {
		return nil, errInvalidInput("IncludeRoot can only be set when ParentCatalogId is the account's.")
	}
	p := s.parseCatalogID(parent)
	if !atRoot && (!p.federated() || s.s3tables == nil) {
		return nil, errCatalogNotFound(parent)
	}
	var list []catalogInfo
	switch {
	case atRoot:
		if req.IncludeRoot != nil && *req.IncludeRoot {
			list = append(list, s.rootCatalog(ctx))
		}
		if s.s3tables != nil {
			list = append(list, s.s3TablesCatalog(ctx))
		}
		if req.Recursive && s.s3tables != nil {
			children, aerr := s.bucketCatalogs(ctx)
			if aerr != nil {
				return nil, aerr
			}
			list = append(list, children...)
		}
	case p.kind == catalogS3Tables:
		children, aerr := s.bucketCatalogs(ctx)
		if aerr != nil {
			return nil, aerr
		}
		list = children
	default: // a bucket catalog holds no catalogs, but must exist
		if _, aerr := s.readCatalog(ctx, parent); aerr != nil {
			return nil, aerr
		}
	}
	page, aerr := paginate(list, req.MaxResults, req.NextToken, catalogsPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &getCatalogsResp{CatalogList: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) bucketCatalogs(ctx context.Context) ([]catalogInfo, *protocol.AWSError) {
	buckets, err := s.s3tables.ListTableBuckets(ctx)
	if err != nil {
		return nil, errInternal(err)
	}
	out := make([]catalogInfo, len(buckets))
	for i, b := range buckets {
		out[i] = s.bucketCatalog(ctx, b)
	}
	return out, nil
}

// catalogShape is what every catalog carries: its ID, name and ARN, and
// the default permissions of IAM access control, under which Lake Formation
// grants nothing and every principal IAM allows may use it.
func (s *Service) catalogShape(ctx context.Context, p catalogPath, name string) catalogInfo {
	arn := fmt.Sprintf("arn:aws:glue:%s:%s:catalog", middleware.RegionFromContext(ctx, s.cfg.Region), s.cfg.AccountID)
	switch p.kind {
	case catalogS3Tables:
		arn += "/" + S3TablesCatalogName
	case catalogTableBucket:
		arn += "/" + S3TablesCatalogName + "/" + p.bucket
	}
	return catalogInfo{
		CatalogId: s.catalogIDOf(p), Name: name, ResourceArn: arn,
		CreateDatabaseDefaultPermissions: iamAllowedPrincipals(), CreateTableDefaultPermissions: iamAllowedPrincipals(),
	}
}

// rootCatalog is the account's own catalog, named by its account ID.
func (s *Service) rootCatalog(ctx context.Context) catalogInfo {
	return s.catalogShape(ctx, catalogPath{}, s.cfg.AccountID)
}

// s3TablesCatalog is s3tablescatalog as the S3 Tables integration creates
// it: federated to every table bucket of the account and Region through the
// aws:s3tables connection.
func (s *Service) s3TablesCatalog(ctx context.Context) catalogInfo {
	c := s.catalogShape(ctx, catalogPath{kind: catalogS3Tables}, S3TablesCatalogName)
	c.FederatedCatalog = &federatedCatalog{
		Identifier:     fmt.Sprintf("arn:aws:s3tables:%s:%s:bucket/*", middleware.RegionFromContext(ctx, s.cfg.Region), s.cfg.AccountID),
		ConnectionName: s3TablesConnection,
	}
	c.AllowFullTableExternalDataAccess = "True"
	return c
}

// bucketCatalog is one table bucket's child of s3tablescatalog, named by
// the bucket and federated to it.
func (s *Service) bucketCatalog(ctx context.Context, b events.S3TableBucket) catalogInfo {
	c := s.catalogShape(ctx, catalogPath{kind: catalogTableBucket, bucket: b.Name}, b.Name)
	c.CreateTime = epochSeconds(b.CreatedAt)
	c.FederatedCatalog = &federatedCatalog{Identifier: b.ARN, ConnectionName: s3TablesConnection}
	return c
}
