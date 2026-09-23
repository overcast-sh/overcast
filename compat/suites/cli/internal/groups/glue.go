package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// Glue returns the glue-catalog group: a database holding one partitioned
// table, driven through its definition, an optimistic-concurrency update,
// its versions and its partitions, then torn down.
func Glue() ServiceGroup {
	g := &glueCliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"glue-catalog:CreateDatabase":          g.CreateDatabase,
			"glue-catalog:CreateTable":             g.CreateTable,
			"glue-catalog:GetTable":                g.GetTable,
			"glue-catalog:UpdateTable":             g.UpdateTable,
			"glue-catalog:UpdateTableStaleVersion": g.UpdateTableStaleVersion,
			"glue-catalog:GetTableVersions":        g.GetTableVersions,
			"glue-catalog:BatchCreatePartition":    g.BatchCreatePartition,
			"glue-catalog:GetPartitions":           g.GetPartitions,
			"glue-catalog:DeletePartition":         g.DeletePartition,
			"glue-catalog:DeleteTable":             g.DeleteTable,
			"glue-catalog:DeleteDatabase":          g.DeleteDatabase,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"glue-catalog": g.teardown,
		},
	}
}

type glueCliGroup struct{}

// Glue folds names to lowercase; the run id already is, so these round-trip.
func glueCliDatabase(t *harness.TestContext) string { return t.RunID + "-glue-catalog-db" }
func glueCliTable(t *harness.TestContext) string    { return t.RunID + "-glue-catalog-events" }

func glueJSON(v any) string {
	b, _ := json.Marshal(v) //nolint:errchkjson // literal maps of strings
	return string(b)
}

func glueCliTableInput(t *harness.TestContext, parameters map[string]string) string {
	return glueJSON(map[string]any{
		"Name":          glueCliTable(t),
		"TableType":     "EXTERNAL_TABLE",
		"Parameters":    parameters,
		"PartitionKeys": []map[string]string{{"Name": "year", "Type": "int"}, {"Name": "month", "Type": "string"}},
		"StorageDescriptor": map[string]any{
			"Location": "s3://compat-glue-catalog/events/",
			"Columns":  []map[string]string{{"Name": "id", "Type": "bigint"}, {"Name": "payload", "Type": "string"}},
			"SerdeInfo": map[string]string{
				"SerializationLibrary": "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe",
			},
		},
	})
}

func (g *glueCliGroup) teardown(_ context.Context, t *harness.TestContext) error {
	// DeleteDatabase removes the table and its partitions with it.
	awscli.Run(t.Endpoint, t.Region, "glue", "delete-database", "--name", glueCliDatabase(t)) //nolint:errcheck
	return nil
}

func (g *glueCliGroup) getTable(t *harness.TestContext) (map[string]any, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "glue", "get-table",
		"--database-name", glueCliDatabase(t), "--name", glueCliTable(t))
	if err != nil {
		return nil, err
	}
	table, _ := out["Table"].(map[string]any)
	return table, nil
}

func (g *glueCliGroup) CreateDatabase(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "glue", "create-database",
		"--database-input", glueJSON(map[string]string{"Name": glueCliDatabase(t), "Description": "compat"})); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "glue", "get-database", "--name", glueCliDatabase(t))
	if err != nil {
		return err
	}
	db, _ := out["Database"].(map[string]any)
	if db["Name"] != glueCliDatabase(t) || db["Description"] != "compat" {
		return fmt.Errorf("GetDatabase: got %v", db)
	}
	return nil
}

func (g *glueCliGroup) CreateTable(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region, "glue", "create-table",
		"--database-name", glueCliDatabase(t),
		"--table-input", glueCliTableInput(t, map[string]string{"classification": "parquet"}))
}

func (g *glueCliGroup) GetTable(_ context.Context, t *harness.TestContext) error {
	table, err := g.getTable(t)
	if err != nil {
		return err
	}
	sd, _ := table["StorageDescriptor"].(map[string]any)
	cols, _ := sd["Columns"].([]any)
	if sd["Location"] != "s3://compat-glue-catalog/events/" || len(cols) != 2 {
		return fmt.Errorf("GetTable: StorageDescriptor not returned as created: %v", sd)
	}
	keys, _ := table["PartitionKeys"].([]any)
	params, _ := table["Parameters"].(map[string]any)
	if len(keys) != 2 || params["classification"] != "parquet" {
		return fmt.Errorf("GetTable: PartitionKeys/Parameters not returned: %v", table)
	}
	version, _ := table["VersionId"].(string)
	if version == "" || table["CreateTime"] == nil {
		return fmt.Errorf("GetTable: missing VersionId or CreateTime")
	}
	t.Set("glue_version", version)
	return nil
}

func (g *glueCliGroup) UpdateTable(_ context.Context, t *harness.TestContext) error {
	read := t.GetString("glue_version")
	if err := awscli.Run(t.Endpoint, t.Region, "glue", "update-table",
		"--database-name", glueCliDatabase(t), "--version-id", read,
		"--table-input", glueCliTableInput(t, map[string]string{"classification": "parquet", "compat": "updated"})); err != nil {
		return err
	}
	table, err := g.getTable(t)
	if err != nil {
		return err
	}
	params, _ := table["Parameters"].(map[string]any)
	if params["compat"] != "updated" || table["VersionId"] == read {
		return fmt.Errorf("UpdateTable: Parameters %v, VersionId %v (was %q)", params, table["VersionId"], read)
	}
	return nil
}

// UpdateTableStaleVersion commits against the version the table had before
// UpdateTable: AWS refuses it, which is how Iceberg's Glue catalog detects a
// lost commit race.
func (g *glueCliGroup) UpdateTableStaleVersion(_ context.Context, t *harness.TestContext) error {
	return assertAWSFailure(awscli.RunStatus, t, "UpdateTable with a stale VersionId", "ConcurrentModificationException", http.StatusBadRequest,
		"glue", "update-table", "--database-name", glueCliDatabase(t), "--version-id", t.GetString("glue_version"),
		"--table-input", glueCliTableInput(t, map[string]string{"compat": "stale"}))
}

func (g *glueCliGroup) GetTableVersions(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "glue", "get-table-versions",
		"--database-name", glueCliDatabase(t), "--table-name", glueCliTable(t))
	if err != nil {
		return err
	}
	versions, _ := out["TableVersions"].([]any)
	for _, v := range versions {
		if m, _ := v.(map[string]any); m["VersionId"] == t.GetString("glue_version") {
			return nil
		}
	}
	return fmt.Errorf("GetTableVersions: the pre-update version %q is not listed (%d versions)", t.GetString("glue_version"), len(versions))
}

func (g *glueCliGroup) BatchCreatePartition(_ context.Context, t *harness.TestContext) error {
	var inputs []map[string]any
	for _, v := range [][]string{{"2023", "12"}, {"2024", "01"}, {"2024", "02"}} {
		inputs = append(inputs, map[string]any{
			"Values":            v,
			"StorageDescriptor": map[string]string{"Location": "s3://compat-glue-catalog/events/year=" + v[0] + "/month=" + v[1] + "/"},
		})
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "glue", "batch-create-partition",
		"--database-name", glueCliDatabase(t), "--table-name", glueCliTable(t),
		"--partition-input-list", glueJSON(inputs))
	if err != nil {
		return err
	}
	if errs, _ := out["Errors"].([]any); len(errs) != 0 {
		return fmt.Errorf("BatchCreatePartition: %d errors", len(errs))
	}
	return nil
}

func (g *glueCliGroup) GetPartitions(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "glue", "get-partitions",
		"--database-name", glueCliDatabase(t), "--table-name", glueCliTable(t),
		"--expression", "year = 2024 AND month IN ('01', '02')")
	if err != nil {
		return err
	}
	parts, _ := out["Partitions"].([]any)
	var got []string
	for _, p := range parts {
		m, _ := p.(map[string]any)
		values, _ := m["Values"].([]any)
		var s []string
		for _, v := range values {
			s = append(s, fmt.Sprint(v))
		}
		got = append(got, strings.Join(s, "/"))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "2024/01,2024/02" {
		return fmt.Errorf("GetPartitions: matched %v, want [2024/01 2024/02]", got)
	}
	return nil
}

func (g *glueCliGroup) DeletePartition(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "glue", "delete-partition",
		"--database-name", glueCliDatabase(t), "--table-name", glueCliTable(t), "--partition-values", "2023", "12"); err != nil {
		return err
	}
	return assertAWSFailure(awscli.RunStatus, t, "GetPartition after DeletePartition", "EntityNotFoundException", http.StatusBadRequest,
		"glue", "get-partition", "--database-name", glueCliDatabase(t), "--table-name", glueCliTable(t), "--partition-values", "2023", "12")
}

func (g *glueCliGroup) DeleteTable(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "glue", "delete-table",
		"--database-name", glueCliDatabase(t), "--name", glueCliTable(t)); err != nil {
		return err
	}
	return assertAWSFailure(awscli.RunStatus, t, "GetTable after DeleteTable", "EntityNotFoundException", http.StatusBadRequest,
		"glue", "get-table", "--database-name", glueCliDatabase(t), "--name", glueCliTable(t))
}

func (g *glueCliGroup) DeleteDatabase(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "glue", "delete-database", "--name", glueCliDatabase(t)); err != nil {
		return err
	}
	return assertAWSFailure(awscli.RunStatus, t, "GetDatabase after DeleteDatabase", "EntityNotFoundException", http.StatusBadRequest,
		"glue", "get-database", "--name", glueCliDatabase(t))
}
