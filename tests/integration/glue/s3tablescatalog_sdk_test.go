package glue_test

// s3tablescatalog_sdk_test.go — S3 Tables through the Data Catalog, the way
// AWS's analytics integration federates it: s3tablescatalog with one child
// catalog per table bucket, whose databases are namespaces and whose tables
// are the bucket's Iceberg tables.

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/glue/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestS3TablesCatalog_catalogsListTheTableBuckets(t *testing.T) {
	// Given: a table bucket
	srv := helpers.NewTestServer(t)
	bucketARN := helpers.SeedS3Table(t, srv, "lake", "sales", "orders")
	c, ctx := glueClient(t, srv), context.Background()

	// When: s3tablescatalog and the bucket's catalog are read
	parent := must[*glue.GetCatalogOutput](t, "GetCatalog s3tablescatalog")(c.GetCatalog(ctx, &glue.GetCatalogInput{CatalogId: aws.String("s3tablescatalog")})).Catalog
	children := must[*glue.GetCatalogsOutput](t, "GetCatalogs")(c.GetCatalogs(ctx, &glue.GetCatalogsInput{ParentCatalogId: aws.String("s3tablescatalog")})).CatalogList
	child := must[*glue.GetCatalogOutput](t, "GetCatalog lake")(c.GetCatalog(ctx, &glue.GetCatalogInput{CatalogId: aws.String("000000000000:s3tablescatalog/lake")})).Catalog

	// Then: the parent is federated to every table bucket, and the bucket is its child
	if aws.ToString(parent.Name) != "s3tablescatalog" || parent.FederatedCatalog == nil ||
		aws.ToString(parent.FederatedCatalog.ConnectionName) != "aws:s3tables" {
		t.Fatalf("s3tablescatalog = %+v", parent)
	}
	if len(children) != 1 || aws.ToString(children[0].Name) != "lake" {
		t.Fatalf("children = %+v", children)
	}
	if aws.ToString(child.CatalogId) != "000000000000:s3tablescatalog/lake" ||
		aws.ToString(child.ResourceArn) != "arn:aws:glue:us-east-1:000000000000:catalog/s3tablescatalog/lake" ||
		aws.ToString(child.FederatedCatalog.Identifier) != bucketARN || child.CreateTime == nil {
		t.Fatalf("lake = %+v", child)
	}

	// And: the account level lists s3tablescatalog, with the root on request
	top := must[*glue.GetCatalogsOutput](t, "GetCatalogs root")(c.GetCatalogs(ctx, &glue.GetCatalogsInput{IncludeRoot: aws.Bool(true)})).CatalogList
	if len(top) != 2 || aws.ToString(top[0].CatalogId) != "000000000000" || aws.ToString(top[1].Name) != "s3tablescatalog" {
		t.Fatalf("account-level catalogs = %+v", top)
	}

	// And: a bucket that does not exist is not a catalog
	_, err := c.GetCatalog(ctx, &glue.GetCatalogInput{CatalogId: aws.String("000000000000:s3tablescatalog/absent")})
	wantAPIError(t, "GetCatalog absent", err, "EntityNotFoundException")
}

func TestS3TablesCatalog_databasesAndTablesAreNamespacesAndTables(t *testing.T) {
	// Given: a table with a schema in namespace "sales" of bucket "lake"
	srv := helpers.NewTestServer(t)
	helpers.SeedS3Table(t, srv, "lake", "sales", "orders")
	c, ctx := glueClient(t, srv), context.Background()
	catalog := aws.String("000000000000:s3tablescatalog/lake")

	// When: the bucket's catalog is read through the database and table APIs
	dbs := must[*glue.GetDatabasesOutput](t, "GetDatabases")(c.GetDatabases(ctx, &glue.GetDatabasesInput{CatalogId: catalog})).DatabaseList
	tables := must[*glue.GetTablesOutput](t, "GetTables")(c.GetTables(ctx, &glue.GetTablesInput{CatalogId: catalog, DatabaseName: aws.String("sales")})).TableList
	tbl := must[*glue.GetTableOutput](t, "GetTable")(c.GetTable(ctx, &glue.GetTableInput{
		CatalogId: catalog, DatabaseName: aws.String("sales"), Name: aws.String("orders"),
	})).Table

	// Then: the namespace is a database and the table an Iceberg table with its columns
	if len(dbs) != 1 || aws.ToString(dbs[0].Name) != "sales" || aws.ToString(dbs[0].CatalogId) != aws.ToString(catalog) {
		t.Fatalf("databases = %+v", dbs)
	}
	if len(tables) != 1 || aws.ToString(tables[0].Name) != "orders" {
		t.Fatalf("tables = %+v", tables)
	}
	if tbl.Parameters["table_type"] != "ICEBERG" || tbl.Parameters["metadata_location"] == "" ||
		tbl.StorageDescriptor == nil || len(tbl.StorageDescriptor.Columns) != 2 ||
		aws.ToString(tbl.StorageDescriptor.Columns[0].Type) != "bigint" || aws.ToString(tbl.StorageDescriptor.Columns[1].Type) != "decimal(10,2)" {
		t.Fatalf("orders = %+v", tbl)
	}

	// And: the account's own catalog does not hold them
	_, err := c.GetDatabase(ctx, &glue.GetDatabaseInput{Name: aws.String("sales")})
	wantAPIError(t, "GetDatabase in the account's catalog", err, "EntityNotFoundException")
}

func TestS3TablesCatalog_refusesWritesRatherThanApplyingThemElsewhere(t *testing.T) {
	// Given: a table bucket's catalog
	srv := helpers.NewTestServer(t)
	helpers.SeedS3Table(t, srv, "lake", "sales", "orders")
	c, ctx := glueClient(t, srv), context.Background()

	// When: a database is created in it through Glue
	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		CatalogId: aws.String("000000000000:s3tablescatalog/lake"), DatabaseInput: &types.DatabaseInput{Name: aws.String("reports")},
	})

	// Then: it is not implemented, and nothing lands in the account's catalog
	wantAPIError(t, "CreateDatabase in s3tablescatalog/lake", err, "NotImplemented")
	_, err = c.GetDatabase(ctx, &glue.GetDatabaseInput{Name: aws.String("reports")})
	wantAPIError(t, "GetDatabase reports", err, "EntityNotFoundException")
}
