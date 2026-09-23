package glue_test

// catalog_sdk_test.go — the Data Catalog through the AWS SDK for Go v2: full
// table definitions round-tripping, UpdateTable's optimistic concurrency and
// versions, partitions with an Expression filter, cascades, and the modeled
// errors each path answers with.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func glueClient(t *testing.T, srv *helpers.TestServer) *glue.Client {
	t.Helper()
	return glue.New(glue.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

// wantAPIError asserts err is the modeled Glue error code.
func wantAPIError(t *testing.T, what string, err error, code string) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: err = %v, want API error %s", what, err, code)
	}
	if apiErr.ErrorCode() != code {
		t.Fatalf("%s: code = %s (%s), want %s", what, apiErr.ErrorCode(), apiErr.ErrorMessage(), code)
	}
}

func must[T any](t *testing.T, what string) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return v
	}
}

// eventsTableInput is a Hive-style Parquet table partitioned by (year, month),
// the shape Athena DDL and CDK both produce.
func eventsTableInput(name string) *types.TableInput {
	return &types.TableInput{
		Name:        aws.String(name),
		Description: aws.String("click events"),
		Owner:       aws.String("owner"),
		TableType:   aws.String("EXTERNAL_TABLE"),
		Parameters:  map[string]string{"classification": "parquet", "EXTERNAL": "TRUE"},
		PartitionKeys: []types.Column{
			{Name: aws.String("year"), Type: aws.String("int")},
			{Name: aws.String("month"), Type: aws.String("string"), Comment: aws.String("two digits")},
		},
		StorageDescriptor: &types.StorageDescriptor{
			Columns: []types.Column{
				{Name: aws.String("id"), Type: aws.String("bigint")},
				{Name: aws.String("payload"), Type: aws.String("string"), Parameters: map[string]string{"k": "v"}},
			},
			Location:     aws.String("s3://data/events/"),
			InputFormat:  aws.String("org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"),
			OutputFormat: aws.String("org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat"),
			SerdeInfo: &types.SerDeInfo{
				SerializationLibrary: aws.String("org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"),
				Parameters:           map[string]string{"serialization.format": "1"},
			},
			SortColumns:     []types.Order{{Column: aws.String("id"), SortOrder: 1}},
			BucketColumns:   []string{"id"},
			NumberOfBuckets: 4,
		},
	}
}

func setupEvents(t *testing.T, c *glue.Client) {
	t.Helper()
	ctx := context.Background()
	must[*glue.CreateDatabaseOutput](t, "CreateDatabase")(c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &types.DatabaseInput{Name: aws.String("analytics"), Description: aws.String("d"), LocationUri: aws.String("s3://data/"),
			Parameters: map[string]string{"owner": "team"}},
	}))
	must[*glue.CreateTableOutput](t, "CreateTable")(c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String("analytics"), TableInput: eventsTableInput("events"),
	}))
}

func TestSDK_tableDefinitionRoundTrips(t *testing.T) {
	// Given: a database and a fully specified table
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	setupEvents(t, c)

	// When: the table and database are read back
	tbl := must[*glue.GetTableOutput](t, "GetTable")(c.GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String("analytics"), Name: aws.String("events")})).Table
	db := must[*glue.GetDatabaseOutput](t, "GetDatabase")(c.GetDatabase(ctx, &glue.GetDatabaseInput{Name: aws.String("analytics")})).Database

	// Then: everything that was put comes back, stamped with times and a version
	sd := tbl.StorageDescriptor
	switch {
	case sd == nil:
		t.Fatal("StorageDescriptor missing")
	case aws.ToString(sd.Location) != "s3://data/events/",
		len(sd.Columns) != 2 || sd.Columns[1].Parameters["k"] != "v",
		sd.SerdeInfo == nil || !strings.HasSuffix(aws.ToString(sd.SerdeInfo.SerializationLibrary), "ParquetHiveSerDe"),
		sd.NumberOfBuckets != 4, len(sd.SortColumns) != 1, len(sd.BucketColumns) != 1:
		t.Errorf("StorageDescriptor = %+v", sd)
	}
	if len(tbl.PartitionKeys) != 2 || aws.ToString(tbl.PartitionKeys[1].Comment) != "two digits" {
		t.Errorf("PartitionKeys = %+v", tbl.PartitionKeys)
	}
	if tbl.Parameters["classification"] != "parquet" || aws.ToString(tbl.Owner) != "owner" || aws.ToString(tbl.TableType) != "EXTERNAL_TABLE" {
		t.Errorf("table = %+v", tbl)
	}
	if tbl.CreateTime == nil || tbl.UpdateTime == nil || aws.ToString(tbl.VersionId) != "0" || aws.ToString(tbl.CatalogId) == "" {
		t.Errorf("CreateTime=%v UpdateTime=%v VersionId=%v CatalogId=%v", tbl.CreateTime, tbl.UpdateTime, tbl.VersionId, tbl.CatalogId)
	}
	if db.CreateTime == nil || aws.ToString(db.LocationUri) != "s3://data/" || db.Parameters["owner"] != "team" || len(db.CreateTableDefaultPermissions) != 1 {
		t.Errorf("database = %+v", db)
	}
}

func TestSDK_namesAreFoldedAndDuplicatesRefused(t *testing.T) {
	// Given: a table created in mixed case
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	must[*glue.CreateDatabaseOutput](t, "CreateDatabase")(c.CreateDatabase(ctx, &glue.CreateDatabaseInput{DatabaseInput: &types.DatabaseInput{Name: aws.String("Mixed")}}))
	must[*glue.CreateTableOutput](t, "CreateTable")(c.CreateTable(ctx, &glue.CreateTableInput{DatabaseName: aws.String("MIXED"), TableInput: &types.TableInput{Name: aws.String("MyTable")}}))

	// When/Then: it is stored lowercase, found under any casing, and a
	// second create of either is AlreadyExistsException
	tbl := must[*glue.GetTableOutput](t, "GetTable")(c.GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String("mixed"), Name: aws.String("MYTABLE")})).Table
	if aws.ToString(tbl.Name) != "mytable" || aws.ToString(tbl.DatabaseName) != "mixed" {
		t.Errorf("names = %s.%s, want mixed.mytable", aws.ToString(tbl.DatabaseName), aws.ToString(tbl.Name))
	}
	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{DatabaseInput: &types.DatabaseInput{Name: aws.String("mixed")}})
	wantAPIError(t, "CreateDatabase duplicate", err, "AlreadyExistsException")
	_, err = c.CreateTable(ctx, &glue.CreateTableInput{DatabaseName: aws.String("mixed"), TableInput: &types.TableInput{Name: aws.String("mytable")}})
	wantAPIError(t, "CreateTable duplicate", err, "AlreadyExistsException")
	_, err = c.CreateTable(ctx, &glue.CreateTableInput{DatabaseName: aws.String("nope"), TableInput: &types.TableInput{Name: aws.String("t")}})
	wantAPIError(t, "CreateTable in missing database", err, "EntityNotFoundException")
	_, err = c.GetTables(ctx, &glue.GetTablesInput{DatabaseName: aws.String("nope")})
	wantAPIError(t, "GetTables of missing database", err, "EntityNotFoundException")
}

func TestSDK_updateTableVersionsAndConcurrency(t *testing.T) {
	// Given: the events table at version 0
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	setupEvents(t, c)
	in := eventsTableInput("events")
	in.Parameters = map[string]string{"table_type": "ICEBERG", "metadata_location": "s3://data/events/metadata/00001.json"}

	// When: an Iceberg-style commit updates against the version it read
	must[*glue.UpdateTableOutput](t, "UpdateTable")(c.UpdateTable(ctx, &glue.UpdateTableInput{
		DatabaseName: aws.String("analytics"), TableInput: in, VersionId: aws.String("0"),
	}))

	// Then: a second commit against the same version loses the race
	_, err := c.UpdateTable(ctx, &glue.UpdateTableInput{DatabaseName: aws.String("analytics"), TableInput: in, VersionId: aws.String("0")})
	wantAPIError(t, "stale UpdateTable", err, "ConcurrentModificationException")

	// And: the new definition is current, the old one is a version
	tbl := must[*glue.GetTableOutput](t, "GetTable")(c.GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String("analytics"), Name: aws.String("events")})).Table
	if aws.ToString(tbl.VersionId) != "1" || tbl.Parameters["metadata_location"] == "" {
		t.Fatalf("after update: VersionId=%s Parameters=%v", aws.ToString(tbl.VersionId), tbl.Parameters)
	}
	versions := must[*glue.GetTableVersionsOutput](t, "GetTableVersions")(c.GetTableVersions(ctx, &glue.GetTableVersionsInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events")})).TableVersions
	if len(versions) != 2 || aws.ToString(versions[0].VersionId) != "1" || aws.ToString(versions[1].VersionId) != "0" {
		t.Fatalf("versions = %d entries", len(versions))
	}
	v0 := must[*glue.GetTableVersionOutput](t, "GetTableVersion")(c.GetTableVersion(ctx, &glue.GetTableVersionInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events"), VersionId: aws.String("0")}))
	if v0.TableVersion.Table.Parameters["classification"] != "parquet" {
		t.Errorf("version 0 = %+v", v0.TableVersion.Table.Parameters)
	}

	// And: only the archived version can be deleted
	_, err = c.DeleteTableVersion(ctx, &glue.DeleteTableVersionInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events"), VersionId: aws.String("1")})
	wantAPIError(t, "DeleteTableVersion current", err, "InvalidInputException")
	must[*glue.DeleteTableVersionOutput](t, "DeleteTableVersion")(c.DeleteTableVersion(ctx, &glue.DeleteTableVersionInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events"), VersionId: aws.String("0")}))
	batch := must[*glue.BatchDeleteTableVersionOutput](t, "BatchDeleteTableVersion")(c.BatchDeleteTableVersion(ctx, &glue.BatchDeleteTableVersionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"), VersionIds: []string{"0"},
	}))
	if len(batch.Errors) != 1 || aws.ToString(batch.Errors[0].ErrorDetail.ErrorCode) != "EntityNotFoundException" {
		t.Errorf("BatchDeleteTableVersion Errors = %+v", batch.Errors)
	}
}

func TestSDK_updateDatabaseAndBatchDeleteTable(t *testing.T) {
	// Given: a database with two tables
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	setupEvents(t, c)
	must[*glue.CreateTableOutput](t, "CreateTable")(c.CreateTable(ctx, &glue.CreateTableInput{DatabaseName: aws.String("analytics"), TableInput: &types.TableInput{Name: aws.String("other")}}))

	// When: the database is redefined
	must[*glue.UpdateDatabaseOutput](t, "UpdateDatabase")(c.UpdateDatabase(ctx, &glue.UpdateDatabaseInput{
		Name: aws.String("analytics"), DatabaseInput: &types.DatabaseInput{Name: aws.String("analytics"), Description: aws.String("new")},
	}))
	db := must[*glue.GetDatabaseOutput](t, "GetDatabase")(c.GetDatabase(ctx, &glue.GetDatabaseInput{Name: aws.String("analytics")})).Database

	// Then: the definition is replaced and the creation time kept
	if aws.ToString(db.Description) != "new" || db.LocationUri != nil || db.CreateTime == nil {
		t.Errorf("database after update = %+v", db)
	}
	_, err := c.UpdateDatabase(ctx, &glue.UpdateDatabaseInput{Name: aws.String("missing"), DatabaseInput: &types.DatabaseInput{Name: aws.String("missing")}})
	wantAPIError(t, "UpdateDatabase missing", err, "EntityNotFoundException")

	// When: two tables and a missing one are batch-deleted
	out := must[*glue.BatchDeleteTableOutput](t, "BatchDeleteTable")(c.BatchDeleteTable(ctx, &glue.BatchDeleteTableInput{
		DatabaseName: aws.String("analytics"), TablesToDelete: []string{"events", "other", "ghost"},
	}))

	// Then: the missing one is the only error and the database is empty
	if len(out.Errors) != 1 || aws.ToString(out.Errors[0].TableName) != "ghost" || aws.ToString(out.Errors[0].ErrorDetail.ErrorCode) != "EntityNotFoundException" {
		t.Errorf("Errors = %+v", out.Errors)
	}
	tables := must[*glue.GetTablesOutput](t, "GetTables")(c.GetTables(ctx, &glue.GetTablesInput{DatabaseName: aws.String("analytics")}))
	if len(tables.TableList) != 0 {
		t.Errorf("%d tables survived BatchDeleteTable", len(tables.TableList))
	}
}

func TestSDK_partitions(t *testing.T) {
	// Given: the events table and partitions for three months of 2024 and one of 2023
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	setupEvents(t, c)
	partition := func(year, month string) types.PartitionInput {
		return types.PartitionInput{Values: []string{year, month}, Parameters: map[string]string{"p": year + month},
			StorageDescriptor: &types.StorageDescriptor{Location: aws.String("s3://data/events/year=" + year + "/month=" + month + "/"),
				Columns: []types.Column{{Name: aws.String("id"), Type: aws.String("bigint")}}}}
	}
	first := partition("2023", "12")
	must[*glue.CreatePartitionOutput](t, "CreatePartition")(c.CreatePartition(ctx, &glue.CreatePartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"), PartitionInput: &first,
	}))
	created := must[*glue.BatchCreatePartitionOutput](t, "BatchCreatePartition")(c.BatchCreatePartition(ctx, &glue.BatchCreatePartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"),
		PartitionInputList: []types.PartitionInput{partition("2024", "01"), partition("2024", "02"), partition("2024", "03"), partition("2023", "12")},
	}))
	if len(created.Errors) != 1 || aws.ToString(created.Errors[0].ErrorDetail.ErrorCode) != "AlreadyExistsException" {
		t.Fatalf("BatchCreatePartition Errors = %+v", created.Errors)
	}

	// When/Then: GetPartitions filters by Expression, following the key types
	cases := []struct{ expr, want string }{
		{"", "2023/12,2024/01,2024/02,2024/03"},
		{"year = 2024 AND month IN ('01', '03')", "2024/01,2024/03"},
		{"year > 2023 OR month = '12'", "2023/12,2024/01,2024/02,2024/03"},
		{"month BETWEEN '02' AND '12' AND NOT year = 2023", "2024/02,2024/03"},
		{"(year = 2023)", "2023/12"},
	}
	for _, tc := range cases {
		got := must[*glue.GetPartitionsOutput](t, "GetPartitions "+tc.expr)(c.GetPartitions(ctx, &glue.GetPartitionsInput{
			DatabaseName: aws.String("analytics"), TableName: aws.String("events"), Expression: aws.String(tc.expr),
		}))
		var names []string
		for _, p := range got.Partitions {
			names = append(names, strings.Join(p.Values, "/"))
		}
		sort.Strings(names)
		if strings.Join(names, ",") != tc.want {
			t.Errorf("Expression %q matched %v, want %s", tc.expr, names, tc.want)
		}
	}
	_, err := c.GetPartitions(ctx, &glue.GetPartitionsInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events"), Expression: aws.String("substr(month, 1) = '0'")})
	wantAPIError(t, "unsupported Expression", err, "InvalidInputException")

	// And: paging with MaxResults visits every partition exactly once
	pager := glue.NewGetPartitionsPaginator(c, &glue.GetPartitionsInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events"), MaxResults: aws.Int32(3)})
	seen := 0
	for pager.HasMorePages() {
		page := must[*glue.GetPartitionsOutput](t, "GetPartitions page")(pager.NextPage(ctx))
		seen += len(page.Partitions)
	}
	if seen != 4 {
		t.Errorf("paged through %d partitions, want 4", seen)
	}

	// And: GetPartition / BatchGetPartition read them back whole
	one := must[*glue.GetPartitionOutput](t, "GetPartition")(c.GetPartition(ctx, &glue.GetPartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"), PartitionValues: []string{"2024", "02"},
	})).Partition
	if aws.ToString(one.StorageDescriptor.Location) != "s3://data/events/year=2024/month=02/" || one.CreationTime == nil || one.Parameters["p"] != "202402" {
		t.Errorf("GetPartition = %+v", one)
	}
	got := must[*glue.BatchGetPartitionOutput](t, "BatchGetPartition")(c.BatchGetPartition(ctx, &glue.BatchGetPartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"),
		PartitionsToGet: []types.PartitionValueList{{Values: []string{"2024", "01"}}, {Values: []string{"1999", "01"}}},
	}))
	if len(got.Partitions) != 1 || len(got.UnprocessedKeys) != 0 {
		t.Errorf("BatchGetPartition = %d partitions, %d unprocessed", len(got.Partitions), len(got.UnprocessedKeys))
	}

	// And: UpdatePartition replaces the definition
	updated := partition("2024", "02")
	updated.Parameters = map[string]string{"p": "updated"}
	must[*glue.UpdatePartitionOutput](t, "UpdatePartition")(c.UpdatePartition(ctx, &glue.UpdatePartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"), PartitionValueList: []string{"2024", "02"}, PartitionInput: &updated,
	}))
	one = must[*glue.GetPartitionOutput](t, "GetPartition")(c.GetPartition(ctx, &glue.GetPartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"), PartitionValues: []string{"2024", "02"},
	})).Partition
	if one.Parameters["p"] != "updated" {
		t.Errorf("UpdatePartition not applied: %v", one.Parameters)
	}

	// And: deletes, single and batch, report what was missing
	must[*glue.DeletePartitionOutput](t, "DeletePartition")(c.DeletePartition(ctx, &glue.DeletePartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"), PartitionValues: []string{"2023", "12"},
	}))
	_, err = c.GetPartition(ctx, &glue.GetPartitionInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events"), PartitionValues: []string{"2023", "12"}})
	wantAPIError(t, "GetPartition deleted", err, "EntityNotFoundException")
	deleted := must[*glue.BatchDeletePartitionOutput](t, "BatchDeletePartition")(c.BatchDeletePartition(ctx, &glue.BatchDeletePartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"),
		PartitionsToDelete: []types.PartitionValueList{{Values: []string{"2024", "01"}}, {Values: []string{"2023", "12"}}},
	}))
	if len(deleted.Errors) != 1 || deleted.Errors[0].PartitionValues[0] != "2023" {
		t.Errorf("BatchDeletePartition Errors = %+v", deleted.Errors)
	}
	_, err = c.CreatePartition(ctx, &glue.CreatePartitionInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events"),
		PartitionInput: &types.PartitionInput{Values: []string{"2024"}}})
	wantAPIError(t, "CreatePartition wrong arity", err, "InvalidInputException")
}

func TestSDK_deleteCascades(t *testing.T) {
	// Given: a partitioned, versioned table
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	setupEvents(t, c)
	must[*glue.CreatePartitionOutput](t, "CreatePartition")(c.CreatePartition(ctx, &glue.CreatePartitionInput{
		DatabaseName: aws.String("analytics"), TableName: aws.String("events"), PartitionInput: &types.PartitionInput{Values: []string{"2024", "01"}},
	}))
	must[*glue.UpdateTableOutput](t, "UpdateTable")(c.UpdateTable(ctx, &glue.UpdateTableInput{DatabaseName: aws.String("analytics"), TableInput: eventsTableInput("events")}))

	// When: the database is deleted and everything recreated under the same names
	must[*glue.DeleteDatabaseOutput](t, "DeleteDatabase")(c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String("analytics")}))
	_, err := c.GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String("analytics"), Name: aws.String("events")})
	wantAPIError(t, "GetTable after DeleteDatabase", err, "EntityNotFoundException")
	setupEvents(t, c)

	// Then: the new table starts clean — no partitions, one version
	parts := must[*glue.GetPartitionsOutput](t, "GetPartitions")(c.GetPartitions(ctx, &glue.GetPartitionsInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events")}))
	versions := must[*glue.GetTableVersionsOutput](t, "GetTableVersions")(c.GetTableVersions(ctx, &glue.GetTableVersionsInput{DatabaseName: aws.String("analytics"), TableName: aws.String("events")}))
	if len(parts.Partitions) != 0 || len(versions.TableVersions) != 1 {
		t.Errorf("recreated table inherited %d partitions and %d versions", len(parts.Partitions), len(versions.TableVersions))
	}
}

func TestSDK_getTablesAndDatabasesPaginate(t *testing.T) {
	// Given: five tables
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	must[*glue.CreateDatabaseOutput](t, "CreateDatabase")(c.CreateDatabase(ctx, &glue.CreateDatabaseInput{DatabaseInput: &types.DatabaseInput{Name: aws.String("db")}}))
	for _, n := range []string{"a1", "a2", "b1", "b2", "b3"} {
		must[*glue.CreateTableOutput](t, "CreateTable")(c.CreateTable(ctx, &glue.CreateTableInput{DatabaseName: aws.String("db"), TableInput: &types.TableInput{Name: aws.String(n)}}))
	}

	// When: they are paged two at a time, filtered by a name pattern
	pager := glue.NewGetTablesPaginator(c, &glue.GetTablesInput{DatabaseName: aws.String("db"), Expression: aws.String("b.*"), MaxResults: aws.Int32(2)})
	var names []string
	for pager.HasMorePages() {
		for _, tbl := range must[*glue.GetTablesOutput](t, "GetTables")(pager.NextPage(ctx)).TableList {
			names = append(names, aws.ToString(tbl.Name))
		}
	}

	// Then: every matching table once, in name order
	if strings.Join(names, ",") != "b1,b2,b3" {
		t.Errorf("tables = %v", names)
	}
	dbs := must[*glue.GetDatabasesOutput](t, "GetDatabases")(c.GetDatabases(ctx, &glue.GetDatabasesInput{MaxResults: aws.Int32(1)}))
	if len(dbs.DatabaseList) != 1 {
		t.Errorf("GetDatabases returned %d", len(dbs.DatabaseList))
	}
	_, err := c.GetTables(ctx, &glue.GetTablesInput{DatabaseName: aws.String("db"), NextToken: aws.String("not-a-token")})
	wantAPIError(t, "GetTables bad token", err, "InvalidInputException")
}

func TestSDK_icebergOpenTableFormatInputCreatesTableWithoutMetadata(t *testing.T) {
	// Given: a database
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	must[*glue.CreateDatabaseOutput](t, "CreateDatabase")(c.CreateDatabase(ctx, &glue.CreateDatabaseInput{DatabaseInput: &types.DatabaseInput{Name: aws.String("db")}}))

	// When: a table is created asking Glue to write its Iceberg metadata
	must[*glue.CreateTableOutput](t, "CreateTable")(c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName:         aws.String("db"),
		TableInput:           &types.TableInput{Name: aws.String("ice"), StorageDescriptor: &types.StorageDescriptor{Location: aws.String("s3://data/ice/")}},
		OpenTableFormatInput: &types.OpenTableFormatInput{IcebergInput: &types.IcebergInput{MetadataOperation: types.MetadataOperationCreate}},
	}))

	// Then: the table exists as an Iceberg table, without a metadata location
	tbl := must[*glue.GetTableOutput](t, "GetTable")(c.GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String("db"), Name: aws.String("ice")})).Table
	if tbl.Parameters["table_type"] != "ICEBERG" || aws.ToString(tbl.TableType) != "EXTERNAL_TABLE" {
		t.Errorf("table = %+v", tbl)
	}
	if _, ok := tbl.Parameters["metadata_location"]; ok {
		t.Error("metadata_location set, but no metadata was written")
	}
}
