package athena_test

// s3tablescatalog_sdk_test.go — S3 Tables through Athena's metadata
// operations: a table bucket's catalog is addressed as
// "s3tablescatalog/<bucket>" without being registered, and reads Glue's
// federated catalog of the same name.

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestS3TablesCatalog_metadataOperationsReadTheTableBucket(t *testing.T) {
	// Given: a table with a schema in namespace "sales" of bucket "lake"
	srv := helpers.NewTestServer(t)
	helpers.SeedS3Table(t, srv, "lake", "sales", "orders")
	c, ctx := athenaClient(t, srv), context.Background()
	catalog := aws.String("s3tablescatalog/lake")

	// When: its catalog is read through Athena
	dbs := must[*athena.ListDatabasesOutput](t, "ListDatabases")(c.ListDatabases(ctx, &athena.ListDatabasesInput{CatalogName: catalog})).DatabaseList
	tables := must[*athena.ListTableMetadataOutput](t, "ListTableMetadata")(c.ListTableMetadata(ctx, &athena.ListTableMetadataInput{
		CatalogName: catalog, DatabaseName: aws.String("sales"),
	})).TableMetadataList
	md := must[*athena.GetTableMetadataOutput](t, "GetTableMetadata")(c.GetTableMetadata(ctx, &athena.GetTableMetadataInput{
		CatalogName: catalog, DatabaseName: aws.String("sales"), TableName: aws.String("orders"),
	})).TableMetadata

	// Then: the namespace is a database, and the table an Iceberg table with its columns
	if len(dbs) != 1 || aws.ToString(dbs[0].Name) != "sales" {
		t.Fatalf("databases = %+v", dbs)
	}
	if len(tables) != 1 || aws.ToString(tables[0].Name) != "orders" {
		t.Fatalf("tables = %+v", tables)
	}
	if md.Parameters["table_type"] != "ICEBERG" || len(md.Columns) != 2 || aws.ToString(md.Columns[0].Type) != "bigint" {
		t.Fatalf("orders = %+v", md)
	}

	// And: as on AWS, ListDataCatalogs does not list it
	list := must[*athena.ListDataCatalogsOutput](t, "ListDataCatalogs")(c.ListDataCatalogs(ctx, &athena.ListDataCatalogsInput{})).DataCatalogsSummary
	if len(list) != 1 || aws.ToString(list[0].CatalogName) != "AwsDataCatalog" {
		t.Fatalf("DataCatalogsSummary = %+v", list)
	}

	// And: a bucket that does not exist is not a catalog
	_, err := c.ListDatabases(ctx, &athena.ListDatabasesInput{CatalogName: aws.String("s3tablescatalog/absent")})
	wantAPIError(t, "ListDatabases s3tablescatalog/absent", err, "InvalidRequestException")
}

func TestS3TablesCatalog_registeredAsAGlueDataCatalog(t *testing.T) {
	// Given: a table bucket's catalog registered as an Athena data source
	srv := helpers.NewTestServer(t)
	helpers.SeedS3Table(t, srv, "lake", "sales", "orders")
	c, ctx := athenaClient(t, srv), context.Background()
	must[*athena.CreateDataCatalogOutput](t, "CreateDataCatalog")(c.CreateDataCatalog(ctx, &athena.CreateDataCatalogInput{
		Name: aws.String("lake"), Type: types.DataCatalogTypeGlue,
		Parameters: map[string]string{"catalog-id": "000000000000:s3tablescatalog/lake"},
	}))

	// When: its databases are listed by that name
	dbs := must[*athena.ListDatabasesOutput](t, "ListDatabases")(c.ListDatabases(ctx, &athena.ListDatabasesInput{CatalogName: aws.String("lake")})).DatabaseList

	// Then: they are the bucket's namespaces
	if len(dbs) != 1 || aws.ToString(dbs[0].Name) != "sales" {
		t.Fatalf("databases = %+v", dbs)
	}
}
