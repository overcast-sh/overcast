package groups

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/smithy-go"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// Glue returns the glue-catalog group: a database holding one partitioned
// table, driven through its definition, an optimistic-concurrency update,
// its versions and its partitions, then torn down.
func Glue(c *clients.Clients) ServiceGroup {
	g := &glueGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"glue-catalog:CreateDatabase":                 g.CreateDatabase,
			"glue-catalog:CreateTable":                    g.CreateTable,
			"glue-catalog:GetTable":                       g.GetTable,
			"glue-catalog:UpdateTable":                    g.UpdateTable,
			"glue-catalog:UpdateTableStaleVersion":        g.UpdateTableStaleVersion,
			"glue-catalog:GetTableVersions":               g.GetTableVersions,
			"glue-catalog:UpdateColumnStatisticsForTable": g.UpdateColumnStatisticsForTable,
			"glue-catalog:GetColumnStatisticsForTable":    g.GetColumnStatisticsForTable,
			"glue-catalog:BatchCreatePartition":           g.BatchCreatePartition,
			"glue-catalog:GetPartitions":                  g.GetPartitions,
			"glue-catalog:DeletePartition":                g.DeletePartition,
			"glue-catalog:DeleteTable":                    g.DeleteTable,
			"glue-catalog:DeleteDatabase":                 g.DeleteDatabase,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"glue-catalog": g.teardown,
		},
	}
}

type glueGroup struct{ c *clients.Clients }

func (g *glueGroup) cl() *glue.Client { return g.c.Glue() }

// Glue folds names to lowercase; the run id already is, so these round-trip.
func glueDatabaseName(t *harness.TestContext) string { return t.RunID + "-glue-catalog-db" }
func glueTableName(t *harness.TestContext) string    { return t.RunID + "-glue-catalog-events" }

func (g *glueGroup) teardown(ctx context.Context, t *harness.TestContext) error {
	// DeleteDatabase removes the table and its partitions with it.
	g.cl().DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(glueDatabaseName(t))}) //nolint:errcheck
	return nil
}

func glueEventsInput(t *harness.TestContext, parameters map[string]string) *types.TableInput {
	return &types.TableInput{
		Name:       aws.String(glueTableName(t)),
		TableType:  aws.String("EXTERNAL_TABLE"),
		Parameters: parameters,
		PartitionKeys: []types.Column{
			{Name: aws.String("year"), Type: aws.String("int")},
			{Name: aws.String("month"), Type: aws.String("string")},
		},
		StorageDescriptor: &types.StorageDescriptor{
			Location: aws.String("s3://compat-glue-catalog/events/"),
			Columns: []types.Column{
				{Name: aws.String("id"), Type: aws.String("bigint")},
				{Name: aws.String("payload"), Type: aws.String("string")},
			},
			SerdeInfo: &types.SerDeInfo{SerializationLibrary: aws.String("org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe")},
		},
	}
}

func (g *glueGroup) CreateDatabase(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &types.DatabaseInput{Name: aws.String(glueDatabaseName(t)), Description: aws.String("compat")},
	})
	if err != nil {
		return err
	}
	resp, err := g.cl().GetDatabase(ctx, &glue.GetDatabaseInput{Name: aws.String(glueDatabaseName(t))})
	if err != nil {
		return err
	}
	if aws.ToString(resp.Database.Name) != glueDatabaseName(t) || aws.ToString(resp.Database.Description) != "compat" {
		return fmt.Errorf("GetDatabase: got name %q description %q", aws.ToString(resp.Database.Name), aws.ToString(resp.Database.Description))
	}
	return nil
}

func (g *glueGroup) CreateTable(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String(glueDatabaseName(t)),
		TableInput:   glueEventsInput(t, map[string]string{"classification": "parquet"}),
	})
	return err
}

func (g *glueGroup) GetTable(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String(glueDatabaseName(t)), Name: aws.String(glueTableName(t))})
	if err != nil {
		return err
	}
	tbl := resp.Table
	if tbl.StorageDescriptor == nil || aws.ToString(tbl.StorageDescriptor.Location) != "s3://compat-glue-catalog/events/" || len(tbl.StorageDescriptor.Columns) != 2 {
		return fmt.Errorf("GetTable: StorageDescriptor not returned as created: %+v", tbl.StorageDescriptor)
	}
	if len(tbl.PartitionKeys) != 2 || tbl.Parameters["classification"] != "parquet" {
		return fmt.Errorf("GetTable: PartitionKeys %d, Parameters %v", len(tbl.PartitionKeys), tbl.Parameters)
	}
	if tbl.VersionId == nil || tbl.CreateTime == nil {
		return fmt.Errorf("GetTable: missing VersionId or CreateTime")
	}
	t.Set("glue_version", aws.ToString(tbl.VersionId))
	return nil
}

func (g *glueGroup) UpdateTable(ctx context.Context, t *harness.TestContext) error {
	read := t.GetString("glue_version")
	_, err := g.cl().UpdateTable(ctx, &glue.UpdateTableInput{
		DatabaseName: aws.String(glueDatabaseName(t)),
		TableInput:   glueEventsInput(t, map[string]string{"classification": "parquet", "compat": "updated"}),
		VersionId:    aws.String(read),
	})
	if err != nil {
		return err
	}
	resp, err := g.cl().GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String(glueDatabaseName(t)), Name: aws.String(glueTableName(t))})
	if err != nil {
		return err
	}
	if resp.Table.Parameters["compat"] != "updated" || aws.ToString(resp.Table.VersionId) == read {
		return fmt.Errorf("UpdateTable: Parameters %v, VersionId %q (was %q)", resp.Table.Parameters, aws.ToString(resp.Table.VersionId), read)
	}
	return nil
}

// UpdateTableStaleVersion commits against the version the table had before
// UpdateTable: AWS refuses it, which is how Iceberg's Glue catalog detects a
// lost commit race.
func (g *glueGroup) UpdateTableStaleVersion(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().UpdateTable(ctx, &glue.UpdateTableInput{
		DatabaseName: aws.String(glueDatabaseName(t)),
		TableInput:   glueEventsInput(t, map[string]string{"compat": "stale"}),
		VersionId:    aws.String(t.GetString("glue_version")),
	})
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ConcurrentModificationException" {
		return fmt.Errorf("UpdateTable with a stale VersionId: want ConcurrentModificationException, got %v", err)
	}
	return nil
}

func (g *glueGroup) GetTableVersions(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().GetTableVersions(ctx, &glue.GetTableVersionsInput{DatabaseName: aws.String(glueDatabaseName(t)), TableName: aws.String(glueTableName(t))})
	if err != nil {
		return err
	}
	for _, v := range resp.TableVersions {
		if aws.ToString(v.VersionId) == t.GetString("glue_version") {
			return nil
		}
	}
	return fmt.Errorf("GetTableVersions: the pre-update version %q is not listed (%d versions)", t.GetString("glue_version"), len(resp.TableVersions))
}

func (g *glueGroup) UpdateColumnStatisticsForTable(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().UpdateColumnStatisticsForTable(ctx, &glue.UpdateColumnStatisticsForTableInput{
		DatabaseName: aws.String(glueDatabaseName(t)), TableName: aws.String(glueTableName(t)),
		ColumnStatisticsList: []types.ColumnStatistics{{
			ColumnName: aws.String("id"), ColumnType: aws.String("bigint"), AnalyzedTime: aws.Time(time.Unix(1700000000, 0)),
			StatisticsData: &types.ColumnStatisticsData{
				Type:                     types.ColumnStatisticsTypeLong,
				LongColumnStatisticsData: &types.LongColumnStatisticsData{NumberOfNulls: 0, NumberOfDistinctValues: 3, MaximumValue: 9},
			},
		}},
	})
	if err != nil {
		return err
	}
	if len(resp.Errors) != 0 {
		return fmt.Errorf("UpdateColumnStatisticsForTable: %d errors", len(resp.Errors))
	}
	return nil
}

func (g *glueGroup) GetColumnStatisticsForTable(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().GetColumnStatisticsForTable(ctx, &glue.GetColumnStatisticsForTableInput{
		DatabaseName: aws.String(glueDatabaseName(t)), TableName: aws.String(glueTableName(t)), ColumnNames: []string{"id", "payload"},
	})
	if err != nil {
		return err
	}
	if len(resp.ColumnStatisticsList) != 1 || aws.ToString(resp.ColumnStatisticsList[0].ColumnName) != "id" ||
		resp.ColumnStatisticsList[0].StatisticsData.LongColumnStatisticsData == nil ||
		resp.ColumnStatisticsList[0].StatisticsData.LongColumnStatisticsData.NumberOfDistinctValues != 3 {
		return fmt.Errorf("GetColumnStatisticsForTable: %+v", resp.ColumnStatisticsList)
	}
	if len(resp.Errors) != 1 || aws.ToString(resp.Errors[0].ColumnName) != "payload" {
		return fmt.Errorf("GetColumnStatisticsForTable: errors %+v, want payload, which has none", resp.Errors)
	}
	return nil
}

func (g *glueGroup) BatchCreatePartition(ctx context.Context, t *harness.TestContext) error {
	var inputs []types.PartitionInput
	for _, v := range [][]string{{"2023", "12"}, {"2024", "01"}, {"2024", "02"}} {
		inputs = append(inputs, types.PartitionInput{
			Values:            v,
			StorageDescriptor: &types.StorageDescriptor{Location: aws.String("s3://compat-glue-catalog/events/year=" + v[0] + "/month=" + v[1] + "/")},
		})
	}
	resp, err := g.cl().BatchCreatePartition(ctx, &glue.BatchCreatePartitionInput{
		DatabaseName: aws.String(glueDatabaseName(t)), TableName: aws.String(glueTableName(t)), PartitionInputList: inputs,
	})
	if err != nil {
		return err
	}
	if len(resp.Errors) != 0 {
		return fmt.Errorf("BatchCreatePartition: %d errors", len(resp.Errors))
	}
	return nil
}

func (g *glueGroup) GetPartitions(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().GetPartitions(ctx, &glue.GetPartitionsInput{
		DatabaseName: aws.String(glueDatabaseName(t)), TableName: aws.String(glueTableName(t)),
		Expression: aws.String("year = 2024 AND month IN ('01', '02')"),
	})
	if err != nil {
		return err
	}
	var got []string
	for _, p := range resp.Partitions {
		got = append(got, strings.Join(p.Values, "/"))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "2024/01,2024/02" {
		return fmt.Errorf("GetPartitions: matched %v, want [2024/01 2024/02]", got)
	}
	return nil
}

func (g *glueGroup) DeletePartition(ctx context.Context, t *harness.TestContext) error {
	values := []string{"2023", "12"}
	if _, err := g.cl().DeletePartition(ctx, &glue.DeletePartitionInput{
		DatabaseName: aws.String(glueDatabaseName(t)), TableName: aws.String(glueTableName(t)), PartitionValues: values,
	}); err != nil {
		return err
	}
	_, err := g.cl().GetPartition(ctx, &glue.GetPartitionInput{
		DatabaseName: aws.String(glueDatabaseName(t)), TableName: aws.String(glueTableName(t)), PartitionValues: values,
	})
	var notFound *types.EntityNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("GetPartition after DeletePartition: want EntityNotFoundException, got %v", err)
	}
	return nil
}

func (g *glueGroup) DeleteTable(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.cl().DeleteTable(ctx, &glue.DeleteTableInput{DatabaseName: aws.String(glueDatabaseName(t)), Name: aws.String(glueTableName(t))}); err != nil {
		return err
	}
	_, err := g.cl().GetTable(ctx, &glue.GetTableInput{DatabaseName: aws.String(glueDatabaseName(t)), Name: aws.String(glueTableName(t))})
	var notFound *types.EntityNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("GetTable after DeleteTable: want EntityNotFoundException, got %v", err)
	}
	return nil
}

func (g *glueGroup) DeleteDatabase(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.cl().DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(glueDatabaseName(t))}); err != nil {
		return err
	}
	_, err := g.cl().GetDatabase(ctx, &glue.GetDatabaseInput{Name: aws.String(glueDatabaseName(t))})
	var notFound *types.EntityNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("GetDatabase after DeleteDatabase: want EntityNotFoundException, got %v", err)
	}
	return nil
}
