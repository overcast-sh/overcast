package groups

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// Athena returns the athena-control group: a workgroup with a result
// location, a query run in it, a named query, a prepared statement and a
// data catalog, then torn down.
func Athena() ServiceGroup {
	g := &athenaCliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"athena-control:CreateWorkGroup":         g.CreateWorkGroup,
			"athena-control:UpdateWorkGroup":         g.UpdateWorkGroup,
			"athena-control:ListWorkGroups":          g.ListWorkGroups,
			"athena-control:StartQueryExecution":     g.StartQueryExecution,
			"athena-control:ListQueryExecutions":     g.ListQueryExecutions,
			"athena-control:CreateNamedQuery":        g.CreateNamedQuery,
			"athena-control:BatchGetNamedQuery":      g.BatchGetNamedQuery,
			"athena-control:CreatePreparedStatement": g.CreatePreparedStatement,
			"athena-control:CreateDataCatalog":       g.CreateDataCatalog,
			"athena-control:ListEngineVersions":      g.ListEngineVersions,
			"athena-control:DeleteDataCatalog":       g.DeleteDataCatalog,
			"athena-control:DeleteWorkGroup":         g.DeleteWorkGroup,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"athena-control": g.teardown,
		},
	}
}

type athenaCliGroup struct{}

const (
	athenaCliResults   = "s3://compat-athena-control/results/"
	athenaCliStatement = "compat_by_id"
)

func athenaCliWorkGroup(t *harness.TestContext) string { return t.RunID + "-athena-control-wg" }
func athenaCliCatalog(t *harness.TestContext) string   { return t.RunID + "-athena-control-catalog" }

func (g *athenaCliGroup) teardown(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, "athena", "delete-data-catalog", "--name", athenaCliCatalog(t)) //nolint:errcheck
	// --recursive-delete-option removes the named query and prepared statement with it.
	awscli.Run(t.Endpoint, t.Region, "athena", "delete-work-group", "--work-group", athenaCliWorkGroup(t), "--recursive-delete-option") //nolint:errcheck
	return nil
}

func (g *athenaCliGroup) getWorkGroup(t *harness.TestContext) (map[string]any, map[string]any, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-work-group", "--work-group", athenaCliWorkGroup(t))
	if err != nil {
		return nil, nil, err
	}
	wg, _ := out["WorkGroup"].(map[string]any)
	cfg, _ := wg["Configuration"].(map[string]any)
	return wg, cfg, nil
}

func (g *athenaCliGroup) CreateWorkGroup(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "athena", "create-work-group", "--name", athenaCliWorkGroup(t),
		"--configuration", "ResultConfiguration={OutputLocation="+athenaCliResults+"}"); err != nil {
		return err
	}
	wg, cfg, err := g.getWorkGroup(t)
	if err != nil {
		return err
	}
	results, _ := cfg["ResultConfiguration"].(map[string]any)
	if wg["Name"] != athenaCliWorkGroup(t) || results["OutputLocation"] != athenaCliResults {
		return fmt.Errorf("GetWorkGroup: got %v", wg)
	}
	return nil
}

func (g *athenaCliGroup) UpdateWorkGroup(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "athena", "update-work-group", "--work-group", athenaCliWorkGroup(t),
		"--description", "compat", "--configuration-updates", "EnforceWorkGroupConfiguration=true"); err != nil {
		return err
	}
	wg, cfg, err := g.getWorkGroup(t)
	if err != nil {
		return err
	}
	if wg["Description"] != "compat" || cfg["EnforceWorkGroupConfiguration"] != true {
		return fmt.Errorf("GetWorkGroup after update: got %v", wg)
	}
	return nil
}

func (g *athenaCliGroup) ListWorkGroups(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "list-work-groups")
	if err != nil {
		return err
	}
	summaries, _ := out["WorkGroups"].([]any)
	var names []string
	for _, s := range summaries {
		if m, ok := s.(map[string]any); ok {
			name, _ := m["Name"].(string)
			names = append(names, name)
		}
	}
	if !slices.Contains(names, athenaCliWorkGroup(t)) || !slices.Contains(names, "primary") {
		return fmt.Errorf("ListWorkGroups: %v lacks %s or primary", names, athenaCliWorkGroup(t))
	}
	return nil
}

func (g *athenaCliGroup) StartQueryExecution(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "start-query-execution",
		"--query-string", "SELECT 1", "--work-group", athenaCliWorkGroup(t))
	if err != nil {
		return err
	}
	id, _ := out["QueryExecutionId"].(string)
	t.Set("athena_query_id", id)
	got, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-query-execution", "--query-execution-id", id)
	if err != nil {
		return err
	}
	qe, _ := got["QueryExecution"].(map[string]any)
	results, _ := qe["ResultConfiguration"].(map[string]any)
	status, _ := qe["Status"].(map[string]any)
	if qe["WorkGroup"] != athenaCliWorkGroup(t) || results["OutputLocation"] != athenaCliResults+id+".csv" || status["State"] == nil {
		return fmt.Errorf("GetQueryExecution: got %v", qe)
	}
	return nil
}

func (g *athenaCliGroup) ListQueryExecutions(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "list-query-executions", "--work-group", athenaCliWorkGroup(t))
	if err != nil {
		return err
	}
	ids, _ := out["QueryExecutionIds"].([]any)
	if id := t.GetString("athena_query_id"); !slices.Contains(ids, any(id)) {
		return fmt.Errorf("ListQueryExecutions: %v lacks %s", ids, id)
	}
	return nil
}

func (g *athenaCliGroup) CreateNamedQuery(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "create-named-query",
		"--name", "compat-query", "--database", "compat_db", "--query-string", "SELECT 1", "--work-group", athenaCliWorkGroup(t))
	if err != nil {
		return err
	}
	id, _ := out["NamedQueryId"].(string)
	t.Set("athena_named_query_id", id)
	got, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-named-query", "--named-query-id", id)
	if err != nil {
		return err
	}
	nq, _ := got["NamedQuery"].(map[string]any)
	if nq["Name"] != "compat-query" || nq["WorkGroup"] != athenaCliWorkGroup(t) {
		return fmt.Errorf("GetNamedQuery: got %v", nq)
	}
	return nil
}

func (g *athenaCliGroup) BatchGetNamedQuery(_ context.Context, t *harness.TestContext) error {
	id := t.GetString("athena_named_query_id")
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "batch-get-named-query",
		"--named-query-ids", id, "compat-missing-id")
	if err != nil {
		return err
	}
	found, _ := out["NamedQueries"].([]any)
	missed, _ := out["UnprocessedNamedQueryIds"].([]any)
	if len(found) != 1 || len(missed) != 1 {
		return fmt.Errorf("BatchGetNamedQuery: %d found, %d unprocessed", len(found), len(missed))
	}
	return nil
}

func (g *athenaCliGroup) CreatePreparedStatement(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "athena", "create-prepared-statement",
		"--statement-name", athenaCliStatement, "--work-group", athenaCliWorkGroup(t),
		"--query-statement", "SELECT * FROM t WHERE id = ?"); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-prepared-statement",
		"--statement-name", athenaCliStatement, "--work-group", athenaCliWorkGroup(t))
	if err != nil {
		return err
	}
	ps, _ := out["PreparedStatement"].(map[string]any)
	if ps["QueryStatement"] != "SELECT * FROM t WHERE id = ?" {
		return fmt.Errorf("GetPreparedStatement: got %v", ps)
	}
	return nil
}

func (g *athenaCliGroup) CreateDataCatalog(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "athena", "create-data-catalog", "--name", athenaCliCatalog(t), "--type", "HIVE",
		"--parameters", "metadata-function=arn:aws:lambda:us-east-1:000000000000:function:compat-meta"); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-data-catalog", "--name", athenaCliCatalog(t))
	if err != nil {
		return err
	}
	dc, _ := out["DataCatalog"].(map[string]any)
	if dc["Name"] != athenaCliCatalog(t) || dc["Type"] != "HIVE" {
		return fmt.Errorf("GetDataCatalog: got %v", dc)
	}
	return nil
}

func (g *athenaCliGroup) ListEngineVersions(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "list-engine-versions")
	if err != nil {
		return err
	}
	versions, _ := out["EngineVersions"].([]any)
	for _, v := range versions {
		if m, ok := v.(map[string]any); ok && m["SelectedEngineVersion"] == "AUTO" {
			return nil
		}
	}
	return fmt.Errorf("ListEngineVersions: no AUTO in %v", versions)
}

func (g *athenaCliGroup) DeleteDataCatalog(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "athena", "delete-data-catalog", "--name", athenaCliCatalog(t)); err != nil {
		return err
	}
	return assertAWSFailure(awscli.RunStatus, t, "GetDataCatalog after DeleteDataCatalog", "InvalidRequestException", http.StatusBadRequest,
		"athena", "get-data-catalog", "--name", athenaCliCatalog(t))
}

func (g *athenaCliGroup) DeleteWorkGroup(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region, "athena", "delete-work-group", "--work-group", athenaCliWorkGroup(t), "--recursive-delete-option"); err != nil {
		return err
	}
	return assertAWSFailure(awscli.RunStatus, t, "GetWorkGroup after DeleteWorkGroup", "InvalidRequestException", http.StatusBadRequest,
		"athena", "get-work-group", "--work-group", athenaCliWorkGroup(t))
}
