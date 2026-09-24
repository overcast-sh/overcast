package athena_test

// catalogs_sdk_test.go — data catalogs and the metadata operations through
// the AWS SDK for Go v2. Databases and tables are created through Glue and
// read back through Athena, as AwsDataCatalog does on AWS.

import (
	"context"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func glueClient(srv *helpers.TestServer) *glue.Client {
	return glue.New(glue.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

func TestDataCatalogs_builtinAndCustom(t *testing.T) {
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))

	// Given/When: AwsDataCatalog is read
	builtin := must[*athena.GetDataCatalogOutput](t, "GetDataCatalog")(c.GetDataCatalog(ctx, &athena.GetDataCatalogInput{Name: aws.String("AwsDataCatalog")})).DataCatalog

	// Then: it is the account's Glue catalog, and cannot be changed
	if builtin.Type != types.DataCatalogTypeGlue || builtin.Parameters["catalog-id"] == "" {
		t.Fatalf("AwsDataCatalog = %+v", builtin)
	}
	_, err := c.DeleteDataCatalog(ctx, &athena.DeleteDataCatalogInput{Name: aws.String("AwsDataCatalog")})
	wantAPIError(t, "DeleteDataCatalog AwsDataCatalog", err, "InvalidRequestException")

	// When: a HIVE catalog is created with tags, and updated
	created := must[*athena.CreateDataCatalogOutput](t, "CreateDataCatalog")(c.CreateDataCatalog(ctx, &athena.CreateDataCatalogInput{
		Name: aws.String("hive_meta"), Type: types.DataCatalogTypeHive, Description: aws.String("on-prem"),
		Parameters: map[string]string{"metadata-function": "arn:aws:lambda:us-east-1:000000000000:function:meta"},
		Tags:       []types.Tag{{Key: aws.String("team"), Value: aws.String("data")}},
	})).DataCatalog
	must[*athena.UpdateDataCatalogOutput](t, "UpdateDataCatalog")(c.UpdateDataCatalog(ctx, &athena.UpdateDataCatalogInput{
		Name: aws.String("hive_meta"), Type: types.DataCatalogTypeHive, Description: aws.String("moved"),
	}))

	// Then: it is listed after AwsDataCatalog, carries the update and its tags
	if created.Status != types.DataCatalogStatusCreateComplete {
		t.Fatalf("created Status = %s", created.Status)
	}
	list := must[*athena.ListDataCatalogsOutput](t, "ListDataCatalogs")(c.ListDataCatalogs(ctx, &athena.ListDataCatalogsInput{}))
	if len(list.DataCatalogsSummary) != 2 || aws.ToString(list.DataCatalogsSummary[0].CatalogName) != "AwsDataCatalog" ||
		aws.ToString(list.DataCatalogsSummary[1].CatalogName) != "hive_meta" {
		t.Fatalf("DataCatalogsSummary = %+v", list.DataCatalogsSummary)
	}
	got := must[*athena.GetDataCatalogOutput](t, "GetDataCatalog")(c.GetDataCatalog(ctx, &athena.GetDataCatalogInput{Name: aws.String("hive_meta")})).DataCatalog
	if aws.ToString(got.Description) != "moved" || got.Parameters["metadata-function"] == "" {
		t.Fatalf("hive_meta = %+v", got)
	}
	tags := must[*athena.ListTagsForResourceOutput](t, "ListTagsForResource")(c.ListTagsForResource(ctx, &athena.ListTagsForResourceInput{
		ResourceARN: aws.String("arn:aws:athena:us-east-1:000000000000:datacatalog/hive_meta"),
	}))
	if len(tags.Tags) != 1 || aws.ToString(tags.Tags[0].Key) != "team" {
		t.Fatalf("Tags = %+v", tags.Tags)
	}

	// When/Then: a duplicate is refused, and after a delete it is gone
	_, err = c.CreateDataCatalog(ctx, &athena.CreateDataCatalogInput{
		Name: aws.String("hive_meta"), Type: types.DataCatalogTypeGlue, Parameters: map[string]string{"catalog-id": "000000000000"},
	})
	wantAPIError(t, "duplicate CreateDataCatalog", err, "InvalidRequestException")
	must[*athena.DeleteDataCatalogOutput](t, "DeleteDataCatalog")(c.DeleteDataCatalog(ctx, &athena.DeleteDataCatalogInput{Name: aws.String("hive_meta")}))
	_, err = c.GetDataCatalog(ctx, &athena.GetDataCatalogInput{Name: aws.String("hive_meta")})
	wantAPIError(t, "GetDataCatalog after delete", err, "InvalidRequestException")
}

func TestCreateDataCatalog_missingTypeParameter(t *testing.T) {
	c := athenaClient(t, helpers.NewTestServer(t))
	_, err := c.CreateDataCatalog(context.Background(), &athena.CreateDataCatalogInput{Name: aws.String("g"), Type: types.DataCatalogTypeGlue})
	wantAPIError(t, "CreateDataCatalog GLUE without catalog-id", err, "InvalidRequestException")
}

func TestMetadata_readsTheGlueCatalog(t *testing.T) {
	// Given: a Glue database with a partitioned Parquet table
	ctx := context.Background()
	srv := helpers.NewTestServer(t)
	c, g := athenaClient(t, srv), glueClient(srv)
	must[*glue.CreateDatabaseOutput](t, "CreateDatabase")(g.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String("analytics"), Description: aws.String("clicks")},
	}))
	for _, name := range []string{"events", "sessions"} {
		must[*glue.CreateTableOutput](t, "CreateTable")(g.CreateTable(ctx, &glue.CreateTableInput{
			DatabaseName: aws.String("analytics"),
			TableInput: &gluetypes.TableInput{
				Name: aws.String(name), TableType: aws.String("EXTERNAL_TABLE"),
				Parameters:    map[string]string{"classification": "parquet"},
				PartitionKeys: []gluetypes.Column{{Name: aws.String("dt"), Type: aws.String("string")}},
				StorageDescriptor: &gluetypes.StorageDescriptor{
					Columns:     []gluetypes.Column{{Name: aws.String("id"), Type: aws.String("bigint"), Comment: aws.String("pk")}},
					Location:    aws.String("s3://data/" + name + "/"),
					InputFormat: aws.String("org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"),
					SerdeInfo: &gluetypes.SerDeInfo{
						SerializationLibrary: aws.String("org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"),
						Parameters:           map[string]string{"serialization.format": "1"},
					},
				},
			},
		}))
	}
	catalog := aws.String("AwsDataCatalog")

	// When/Then: the database is visible through Athena
	db := must[*athena.GetDatabaseOutput](t, "GetDatabase")(c.GetDatabase(ctx, &athena.GetDatabaseInput{CatalogName: catalog, DatabaseName: aws.String("analytics")})).Database
	if aws.ToString(db.Name) != "analytics" || aws.ToString(db.Description) != "clicks" {
		t.Fatalf("Database = %+v", db)
	}
	dbs := must[*athena.ListDatabasesOutput](t, "ListDatabases")(c.ListDatabases(ctx, &athena.ListDatabasesInput{CatalogName: catalog}))
	if len(dbs.DatabaseList) != 1 {
		t.Fatalf("DatabaseList = %+v", dbs.DatabaseList)
	}

	// When/Then: a table's schema and storage come back as Athena shapes them
	md := must[*athena.GetTableMetadataOutput](t, "GetTableMetadata")(c.GetTableMetadata(ctx, &athena.GetTableMetadataInput{
		CatalogName: catalog, DatabaseName: aws.String("analytics"), TableName: aws.String("events"),
	})).TableMetadata
	if len(md.Columns) != 1 || aws.ToString(md.Columns[0].Comment) != "pk" || len(md.PartitionKeys) != 1 || aws.ToString(md.TableType) != "EXTERNAL_TABLE" {
		t.Fatalf("TableMetadata = %+v", md)
	}
	for k, want := range map[string]string{
		"classification": "parquet", "location": "s3://data/events/",
		"serde.serialization.lib":          "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe",
		"serde.param.serialization.format": "1",
	} {
		if md.Parameters[k] != want {
			t.Errorf("Parameters[%q] = %q, want %q", k, md.Parameters[k], want)
		}
	}

	// When/Then: the expression filters the listing by name
	tables := must[*athena.ListTableMetadataOutput](t, "ListTableMetadata")(c.ListTableMetadata(ctx, &athena.ListTableMetadataInput{
		CatalogName: catalog, DatabaseName: aws.String("analytics"), Expression: aws.String("sess.*"),
	}))
	if len(tables.TableMetadataList) != 1 || aws.ToString(tables.TableMetadataList[0].Name) != "sessions" {
		t.Fatalf("TableMetadataList = %+v", tables.TableMetadataList)
	}

	// When/Then: what Glue does not have is a MetadataException
	_, err := c.GetTableMetadata(ctx, &athena.GetTableMetadataInput{CatalogName: catalog, DatabaseName: aws.String("analytics"), TableName: aws.String("nope")})
	wantAPIError(t, "GetTableMetadata missing table", err, "MetadataException")
	_, err = c.GetDatabase(ctx, &athena.GetDatabaseInput{CatalogName: catalog, DatabaseName: aws.String("nope")})
	wantAPIError(t, "GetDatabase missing database", err, "MetadataException")
}
