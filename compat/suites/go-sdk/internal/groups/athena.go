package groups

import (
	"context"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// Athena returns the athena-control group: a workgroup with a result
// location, a query run in it, a named query, a prepared statement and a
// data catalog, then torn down.
func Athena(c *clients.Clients) ServiceGroup {
	g := &athenaGroup{c: c}
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

type athenaGroup struct{ c *clients.Clients }

func (g *athenaGroup) cl() *athena.Client { return g.c.Athena() }

const (
	athenaResults   = "s3://compat-athena-control/results/"
	athenaStatement = "compat_by_id"
)

func athenaWorkGroup(t *harness.TestContext) string { return t.RunID + "-athena-control-wg" }
func athenaCatalog(t *harness.TestContext) string   { return t.RunID + "-athena-control-catalog" }

func (g *athenaGroup) teardown(ctx context.Context, t *harness.TestContext) error {
	g.cl().DeleteDataCatalog(ctx, &athena.DeleteDataCatalogInput{Name: aws.String(athenaCatalog(t))}) //nolint:errcheck
	// RecursiveDeleteOption removes the named query and prepared statement with it.
	g.cl().DeleteWorkGroup(ctx, &athena.DeleteWorkGroupInput{WorkGroup: aws.String(athenaWorkGroup(t)), RecursiveDeleteOption: aws.Bool(true)}) //nolint:errcheck
	return nil
}

func (g *athenaGroup) getWorkGroup(ctx context.Context, t *harness.TestContext) (*types.WorkGroup, error) {
	resp, err := g.cl().GetWorkGroup(ctx, &athena.GetWorkGroupInput{WorkGroup: aws.String(athenaWorkGroup(t))})
	if err != nil {
		return nil, err
	}
	return resp.WorkGroup, nil
}

func (g *athenaGroup) CreateWorkGroup(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{
		Name: aws.String(athenaWorkGroup(t)),
		Configuration: &types.WorkGroupConfiguration{
			ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String(athenaResults)},
		},
	})
	if err != nil {
		return err
	}
	wg, err := g.getWorkGroup(ctx, t)
	if err != nil {
		return err
	}
	if aws.ToString(wg.Name) != athenaWorkGroup(t) || aws.ToString(wg.Configuration.ResultConfiguration.OutputLocation) != athenaResults {
		return fmt.Errorf("GetWorkGroup: name %q, output location %q", aws.ToString(wg.Name), aws.ToString(wg.Configuration.ResultConfiguration.OutputLocation))
	}
	return nil
}

func (g *athenaGroup) UpdateWorkGroup(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().UpdateWorkGroup(ctx, &athena.UpdateWorkGroupInput{
		WorkGroup:            aws.String(athenaWorkGroup(t)),
		Description:          aws.String("compat"),
		ConfigurationUpdates: &types.WorkGroupConfigurationUpdates{EnforceWorkGroupConfiguration: aws.Bool(true)},
	})
	if err != nil {
		return err
	}
	wg, err := g.getWorkGroup(ctx, t)
	if err != nil {
		return err
	}
	if aws.ToString(wg.Description) != "compat" || !aws.ToBool(wg.Configuration.EnforceWorkGroupConfiguration) {
		return fmt.Errorf("GetWorkGroup after update: description %q, enforce %v", aws.ToString(wg.Description), wg.Configuration.EnforceWorkGroupConfiguration)
	}
	return nil
}

func (g *athenaGroup) ListWorkGroups(ctx context.Context, t *harness.TestContext) error {
	var names []string
	p := athena.NewListWorkGroupsPaginator(g.cl(), &athena.ListWorkGroupsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, s := range page.WorkGroups {
			names = append(names, aws.ToString(s.Name))
		}
	}
	if !slices.Contains(names, athenaWorkGroup(t)) || !slices.Contains(names, "primary") {
		return fmt.Errorf("ListWorkGroups: %v lacks %s or primary", names, athenaWorkGroup(t))
	}
	return nil
}

func (g *athenaGroup) StartQueryExecution(ctx context.Context, t *harness.TestContext) error {
	out, err := g.cl().StartQueryExecution(ctx, &athena.StartQueryExecutionInput{
		QueryString: aws.String("SELECT 1"),
		WorkGroup:   aws.String(athenaWorkGroup(t)),
	})
	if err != nil {
		return err
	}
	id := aws.ToString(out.QueryExecutionId)
	t.Set("athena_query_id", id)
	resp, err := g.cl().GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: aws.String(id)})
	if err != nil {
		return err
	}
	qe := resp.QueryExecution
	if aws.ToString(qe.WorkGroup) != athenaWorkGroup(t) || aws.ToString(qe.ResultConfiguration.OutputLocation) != athenaResults+id+".csv" || qe.Status == nil || qe.Status.State == "" {
		return fmt.Errorf("GetQueryExecution: workgroup %q, output %q, status %+v", aws.ToString(qe.WorkGroup), aws.ToString(qe.ResultConfiguration.OutputLocation), qe.Status)
	}
	return nil
}

func (g *athenaGroup) ListQueryExecutions(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().ListQueryExecutions(ctx, &athena.ListQueryExecutionsInput{WorkGroup: aws.String(athenaWorkGroup(t))})
	if err != nil {
		return err
	}
	if id := t.GetString("athena_query_id"); !slices.Contains(resp.QueryExecutionIds, id) {
		return fmt.Errorf("ListQueryExecutions: %v lacks %s", resp.QueryExecutionIds, id)
	}
	return nil
}

func (g *athenaGroup) CreateNamedQuery(ctx context.Context, t *harness.TestContext) error {
	out, err := g.cl().CreateNamedQuery(ctx, &athena.CreateNamedQueryInput{
		Name: aws.String("compat-query"), Database: aws.String("compat_db"),
		QueryString: aws.String("SELECT 1"), WorkGroup: aws.String(athenaWorkGroup(t)),
	})
	if err != nil {
		return err
	}
	id := aws.ToString(out.NamedQueryId)
	t.Set("athena_named_query_id", id)
	resp, err := g.cl().GetNamedQuery(ctx, &athena.GetNamedQueryInput{NamedQueryId: aws.String(id)})
	if err != nil {
		return err
	}
	if aws.ToString(resp.NamedQuery.Name) != "compat-query" || aws.ToString(resp.NamedQuery.WorkGroup) != athenaWorkGroup(t) {
		return fmt.Errorf("GetNamedQuery: %+v", resp.NamedQuery)
	}
	return nil
}

func (g *athenaGroup) BatchGetNamedQuery(ctx context.Context, t *harness.TestContext) error {
	id := t.GetString("athena_named_query_id")
	resp, err := g.cl().BatchGetNamedQuery(ctx, &athena.BatchGetNamedQueryInput{NamedQueryIds: []string{id, "compat-missing-id"}})
	if err != nil {
		return err
	}
	if len(resp.NamedQueries) != 1 || aws.ToString(resp.NamedQueries[0].NamedQueryId) != id || len(resp.UnprocessedNamedQueryIds) != 1 {
		return fmt.Errorf("BatchGetNamedQuery: %d found, %d unprocessed", len(resp.NamedQueries), len(resp.UnprocessedNamedQueryIds))
	}
	return nil
}

func (g *athenaGroup) CreatePreparedStatement(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().CreatePreparedStatement(ctx, &athena.CreatePreparedStatementInput{
		StatementName: aws.String(athenaStatement), WorkGroup: aws.String(athenaWorkGroup(t)),
		QueryStatement: aws.String("SELECT * FROM t WHERE id = ?"),
	})
	if err != nil {
		return err
	}
	resp, err := g.cl().GetPreparedStatement(ctx, &athena.GetPreparedStatementInput{
		StatementName: aws.String(athenaStatement), WorkGroup: aws.String(athenaWorkGroup(t)),
	})
	if err != nil {
		return err
	}
	if aws.ToString(resp.PreparedStatement.QueryStatement) != "SELECT * FROM t WHERE id = ?" {
		return fmt.Errorf("GetPreparedStatement: %+v", resp.PreparedStatement)
	}
	return nil
}

func (g *athenaGroup) CreateDataCatalog(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().CreateDataCatalog(ctx, &athena.CreateDataCatalogInput{
		Name: aws.String(athenaCatalog(t)), Type: types.DataCatalogTypeHive,
		Parameters: map[string]string{"metadata-function": "arn:aws:lambda:us-east-1:000000000000:function:compat-meta"},
	})
	if err != nil {
		return err
	}
	resp, err := g.cl().GetDataCatalog(ctx, &athena.GetDataCatalogInput{Name: aws.String(athenaCatalog(t))})
	if err != nil {
		return err
	}
	if aws.ToString(resp.DataCatalog.Name) != athenaCatalog(t) || resp.DataCatalog.Type != types.DataCatalogTypeHive {
		return fmt.Errorf("GetDataCatalog: %+v", resp.DataCatalog)
	}
	return nil
}

func (g *athenaGroup) ListEngineVersions(ctx context.Context, _ *harness.TestContext) error {
	resp, err := g.cl().ListEngineVersions(ctx, &athena.ListEngineVersionsInput{})
	if err != nil {
		return err
	}
	for _, ev := range resp.EngineVersions {
		if aws.ToString(ev.SelectedEngineVersion) == "AUTO" {
			return nil
		}
	}
	return fmt.Errorf("ListEngineVersions: no AUTO in %+v", resp.EngineVersions)
}

func (g *athenaGroup) DeleteDataCatalog(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.cl().DeleteDataCatalog(ctx, &athena.DeleteDataCatalogInput{Name: aws.String(athenaCatalog(t))}); err != nil {
		return err
	}
	if _, err := g.cl().GetDataCatalog(ctx, &athena.GetDataCatalogInput{Name: aws.String(athenaCatalog(t))}); err == nil {
		return fmt.Errorf("GetDataCatalog after delete: still there")
	}
	return nil
}

func (g *athenaGroup) DeleteWorkGroup(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().DeleteWorkGroup(ctx, &athena.DeleteWorkGroupInput{WorkGroup: aws.String(athenaWorkGroup(t)), RecursiveDeleteOption: aws.Bool(true)})
	if err != nil {
		return err
	}
	if _, err := g.getWorkGroup(ctx, t); err == nil {
		return fmt.Errorf("GetWorkGroup after delete: still there")
	}
	return nil
}
