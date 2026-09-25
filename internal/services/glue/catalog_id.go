package glue

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

// S3TablesCatalogName is the federated catalog S3 Tables' table buckets are
// mounted under, one child catalog per bucket: "s3tablescatalog/<bucket>",
// or "<account>:s3tablescatalog/<bucket>" as a CatalogId.
const S3TablesCatalogName = "s3tablescatalog"

// s3TablesConnection is the Glue connection AWS federates s3tablescatalog
// through.
const s3TablesConnection = "aws:s3tables"

// catalogKind is which catalog a CatalogId names.
type catalogKind int

const (
	// catalogDefault is the account's own Data Catalog.
	catalogDefault catalogKind = iota
	// catalogS3Tables is s3tablescatalog, the parent of the bucket catalogs.
	catalogS3Tables
	// catalogTableBucket is one table bucket's child of s3tablescatalog.
	catalogTableBucket
)

// catalogPath is a parsed CatalogId.
type catalogPath struct {
	kind   catalogKind
	bucket string
}

// parseCatalogID reads a CatalogId: "[<account>:]s3tablescatalog" is the
// federated parent and "[<account>:]s3tablescatalog/<bucket>" a table
// bucket's catalog, for this account. Anything else — empty, the account ID,
// or an ID Overcast has no catalog for — is the account's own catalog, which
// is how every Data Catalog operation has always read it: Overcast emulates
// one account.
func (s *Service) parseCatalogID(id string) catalogPath {
	if account, rest, ok := strings.Cut(id, ":"); ok {
		if account != s.cfg.AccountID {
			return catalogPath{}
		}
		id = rest
	}
	name, bucket, child := strings.Cut(id, "/")
	switch {
	case !strings.EqualFold(name, S3TablesCatalogName):
		return catalogPath{}
	case !child:
		return catalogPath{kind: catalogS3Tables}
	case bucket == "" || strings.Contains(bucket, "/"):
		return catalogPath{}
	}
	return catalogPath{kind: catalogTableBucket, bucket: strings.ToLower(bucket)}
}

// federated reports whether the CatalogId names a catalog S3 Tables backs.
func (p catalogPath) federated() bool { return p.kind != catalogDefault }

// catalogIDOf is the CatalogId AWS reports for the path's catalog.
func (s *Service) catalogIDOf(p catalogPath) string {
	switch p.kind {
	case catalogS3Tables:
		return s.cfg.AccountID + ":" + S3TablesCatalogName
	case catalogTableBucket:
		return s.cfg.AccountID + ":" + S3TablesCatalogName + "/" + p.bucket
	}
	return s.cfg.AccountID
}

// catalogRef is the CatalogId member of every Data Catalog request.
type catalogRef struct {
	CatalogId string `json:"CatalogId" cbor:"CatalogId"`
}

func (r catalogRef) catalogID() string { return r.CatalogId }

// catalogScoped is a request that names the catalog it acts on.
type catalogScoped interface{ catalogID() string }

// ownCatalogOp is op.NewTyped for an operation served only on the account's
// own catalog. A federated catalog is S3 Tables' to change, through its own
// API or the Iceberg REST catalog, and Overcast serves only its database and
// table reads through Glue; anything else addressed to it is not
// implemented, rather than quietly applied to the account's own catalog.
func ownCatalogOp[In any, Out any, P interface {
	*In
	catalogScoped
}](s *Service, name string, fn func(context.Context, P) (*Out, *protocol.AWSError)) op.Operation {
	return op.NewTyped(name, func(ctx context.Context, in *In) (*Out, *protocol.AWSError) {
		req := P(in)
		if s.parseCatalogID(req.catalogID()).federated() {
			return nil, errFederatedNotImplemented(name, req.catalogID())
		}
		return fn(ctx, req)
	})
}
