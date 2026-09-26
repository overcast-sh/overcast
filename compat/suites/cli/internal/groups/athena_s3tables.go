package groups

import (
	"context"
	"fmt"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// AthenaS3Tables returns the athena-s3tables group: an Iceberg table created,
// written and read through Athena in a table bucket's catalog,
// "s3tablescatalog/<bucket>", each write moving the table's metadata
// location in S3 Tables. S3 Tables' calls are signed, because its paths are
// also legal S3 bucket names.
func AthenaS3Tables() ServiceGroup {
	g := &athenaS3TablesCliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"athena-s3tables:CreateTableStatement": g.CreateTableStatement,
			"athena-s3tables:InsertStatement":      g.InsertStatement,
			"athena-s3tables:SelectFromTable":      g.SelectFromTable,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"athena-s3tables": g.setup,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"athena-s3tables": g.teardown,
		},
	}
}

type athenaS3TablesCliGroup struct{}

const (
	athenaS3TablesCliNamespace = "sales"
	athenaS3TablesCliTable     = "orders"
)

func athenaS3TablesCliBucket(t *harness.TestContext) string {
	return tableBucketName("athena-s3tables-", t.RunID)
}

func athenaS3TablesCliCatalog(t *harness.TestContext) string {
	return "s3tablescatalog/" + athenaS3TablesCliBucket(t)
}

// athenaS3TablesCliResults is the S3 bucket query results are written to.
func athenaS3TablesCliResults(t *harness.TestContext) string {
	return t.RunID + "-athena-s3tables-results"
}

func (g *athenaS3TablesCliGroup) setup(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", athenaS3TablesCliResults(t)); err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "create-table-bucket", "--name", athenaS3TablesCliBucket(t))
	if err != nil {
		return err
	}
	arn, _ := out["arn"].(string)
	t.Set("athena_s3tables_bucket_arn", arn)
	return awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "create-namespace", "--table-bucket-arn", arn, "--namespace", athenaS3TablesCliNamespace)
}

func (g *athenaS3TablesCliGroup) teardown(_ context.Context, t *harness.TestContext) error {
	if arn := t.GetString("athena_s3tables_bucket_arn"); arn != "" {
		ns := athenaS3TablesCliNamespace
		awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-table", "--table-bucket-arn", arn, "--namespace", ns, "--name", athenaS3TablesCliTable) //nolint:errcheck
		awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-namespace", "--table-bucket-arn", arn, "--namespace", ns)                               //nolint:errcheck
		awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-table-bucket", "--table-bucket-arn", arn)                                               //nolint:errcheck
	}
	awscli.Run(t.Endpoint, t.Region, "s3", "rb", "s3://"+athenaS3TablesCliResults(t), "--force") //nolint:errcheck
	return nil
}

// results is the --result-configuration every query writes to.
func (g *athenaS3TablesCliGroup) results(t *harness.TestContext) []string {
	return []string{"--result-configuration", "OutputLocation=s3://" + athenaS3TablesCliResults(t) + "/"}
}

// run runs query in the table bucket's catalog, in the namespace.
func (g *athenaS3TablesCliGroup) run(ctx context.Context, t *harness.TestContext, query string) (string, error) {
	return runAthenaQuery(ctx, t, query, append(g.results(t),
		"--query-execution-context", "Catalog="+athenaS3TablesCliCatalog(t)+",Database="+athenaS3TablesCliNamespace)...)
}

// metadataLocation is the table's current metadata file, from S3 Tables.
func (g *athenaS3TablesCliGroup) metadataLocation(t *harness.TestContext) (string, error) {
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "get-table-metadata-location",
		"--table-bucket-arn", t.GetString("athena_s3tables_bucket_arn"), "--namespace", athenaS3TablesCliNamespace, "--name", athenaS3TablesCliTable)
	if err != nil {
		return "", err
	}
	loc, _ := out["metadataLocation"].(string)
	return loc, nil
}

func (g *athenaS3TablesCliGroup) CreateTableStatement(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.run(ctx, t, "CREATE TABLE "+athenaS3TablesCliTable+" (id int, amount double) TBLPROPERTIES ('table_type' = 'iceberg')"); err != nil {
		return err
	}
	loc, err := g.metadataLocation(t)
	if err != nil {
		return err
	}
	if loc == "" {
		return fmt.Errorf("GetTableMetadataLocation: the created table has no metadata location")
	}
	t.Set("athena_s3tables_metadata_location", loc)
	return nil
}

func (g *athenaS3TablesCliGroup) InsertStatement(ctx context.Context, t *harness.TestContext) error {
	id, err := g.run(ctx, t, "INSERT INTO "+athenaS3TablesCliTable+" VALUES (1, 9.5), (2, 20.0)")
	if err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-query-results", "--query-execution-id", id)
	if err != nil {
		return err
	}
	if out["UpdateCount"] != float64(2) {
		return fmt.Errorf("GetQueryResults: UpdateCount %v, want 2", out["UpdateCount"])
	}
	loc, err := g.metadataLocation(t)
	if err != nil {
		return err
	}
	if loc == t.GetString("athena_s3tables_metadata_location") {
		return fmt.Errorf("GetTableMetadataLocation: still %q after the INSERT", loc)
	}
	return nil
}

func (g *athenaS3TablesCliGroup) SelectFromTable(ctx context.Context, t *harness.TestContext) error {
	query := `SELECT id, amount FROM "` + athenaS3TablesCliCatalog(t) + `"."` + athenaS3TablesCliNamespace + `"."` + athenaS3TablesCliTable + `" ORDER BY id`
	id, err := runAthenaQuery(ctx, t, query, g.results(t)...)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-query-results", "--query-execution-id", id)
	if err != nil {
		return err
	}
	if got := strings.Join(queryResultRows(out), ";"); got != "id,amount;1,9.5;2,20.0" {
		return fmt.Errorf("GetQueryResults: rows %q, want the header then 1,9.5 and 2,20.0", got)
	}
	return nil
}
