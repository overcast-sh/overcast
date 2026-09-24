package athena_test

// saved_queries_sdk_test.go — named queries and prepared statements through
// the AWS SDK for Go v2.

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestNamedQuery_lifecycle(t *testing.T) {
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	token := strings.Repeat("n", 36)

	// Given: a named query in primary, created twice with one token
	create := &athena.CreateNamedQueryInput{
		Name: aws.String("top-events"), Database: aws.String("analytics"),
		QueryString: aws.String("SELECT * FROM events LIMIT 10"), Description: aws.String("first ten"),
		ClientRequestToken: aws.String(token),
	}
	id := aws.ToString(must[*athena.CreateNamedQueryOutput](t, "CreateNamedQuery")(c.CreateNamedQuery(ctx, create)).NamedQueryId)
	retry := aws.ToString(must[*athena.CreateNamedQueryOutput](t, "CreateNamedQuery retry")(c.CreateNamedQuery(ctx, create)).NamedQueryId)
	if retry != id {
		t.Fatalf("retry id = %s, want %s", retry, id)
	}

	// When/Then: it reads back whole, in primary
	nq := must[*athena.GetNamedQueryOutput](t, "GetNamedQuery")(c.GetNamedQuery(ctx, &athena.GetNamedQueryInput{NamedQueryId: aws.String(id)})).NamedQuery
	if aws.ToString(nq.Name) != "top-events" || aws.ToString(nq.Database) != "analytics" || aws.ToString(nq.WorkGroup) != "primary" || aws.ToString(nq.Description) != "first ten" {
		t.Fatalf("NamedQuery = %+v", nq)
	}

	// When/Then: it is the only one primary lists
	list := must[*athena.ListNamedQueriesOutput](t, "ListNamedQueries")(c.ListNamedQueries(ctx, &athena.ListNamedQueriesInput{}))
	if len(list.NamedQueryIds) != 1 || list.NamedQueryIds[0] != id {
		t.Fatalf("NamedQueryIds = %v", list.NamedQueryIds)
	}

	// When/Then: an update changes its name and query
	must[*athena.UpdateNamedQueryOutput](t, "UpdateNamedQuery")(c.UpdateNamedQuery(ctx, &athena.UpdateNamedQueryInput{
		NamedQueryId: aws.String(id), Name: aws.String("top-twenty"), QueryString: aws.String("SELECT * FROM events LIMIT 20"),
	}))
	batch := must[*athena.BatchGetNamedQueryOutput](t, "BatchGetNamedQuery")(c.BatchGetNamedQuery(ctx, &athena.BatchGetNamedQueryInput{NamedQueryIds: []string{id, "missing"}}))
	if len(batch.NamedQueries) != 1 || aws.ToString(batch.NamedQueries[0].Name) != "top-twenty" || aws.ToString(batch.NamedQueries[0].Description) != "first ten" {
		t.Fatalf("NamedQueries = %+v", batch.NamedQueries)
	}
	if len(batch.UnprocessedNamedQueryIds) != 1 || aws.ToString(batch.UnprocessedNamedQueryIds[0].NamedQueryId) != "missing" {
		t.Fatalf("UnprocessedNamedQueryIds = %+v", batch.UnprocessedNamedQueryIds)
	}

	// When/Then: after a delete it is gone
	must[*athena.DeleteNamedQueryOutput](t, "DeleteNamedQuery")(c.DeleteNamedQuery(ctx, &athena.DeleteNamedQueryInput{NamedQueryId: aws.String(id)}))
	_, err := c.GetNamedQuery(ctx, &athena.GetNamedQueryInput{NamedQueryId: aws.String(id)})
	wantAPIError(t, "GetNamedQuery after delete", err, "InvalidRequestException")
}

func TestCreateNamedQuery_unknownWorkGroup(t *testing.T) {
	c := athenaClient(t, helpers.NewTestServer(t))
	_, err := c.CreateNamedQuery(context.Background(), &athena.CreateNamedQueryInput{
		Name: aws.String("q"), Database: aws.String("db"), QueryString: aws.String("SELECT 1"), WorkGroup: aws.String("nowhere"),
	})
	wantAPIError(t, "CreateNamedQuery", err, "InvalidRequestException")
}

func TestPreparedStatement_lifecycle(t *testing.T) {
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	get := func() (*athena.GetPreparedStatementOutput, error) {
		return c.GetPreparedStatement(ctx, &athena.GetPreparedStatementInput{StatementName: aws.String("by_id"), WorkGroup: aws.String("primary")})
	}

	// Given: a prepared statement
	must[*athena.CreatePreparedStatementOutput](t, "CreatePreparedStatement")(c.CreatePreparedStatement(ctx, &athena.CreatePreparedStatementInput{
		StatementName: aws.String("by_id"), WorkGroup: aws.String("primary"),
		QueryStatement: aws.String("SELECT * FROM events WHERE id = ?"), Description: aws.String("one event"),
	}))

	// When/Then: a second with the same name is refused
	_, err := c.CreatePreparedStatement(ctx, &athena.CreatePreparedStatementInput{
		StatementName: aws.String("by_id"), WorkGroup: aws.String("primary"), QueryStatement: aws.String("SELECT 1"),
	})
	wantAPIError(t, "duplicate CreatePreparedStatement", err, "InvalidRequestException")

	// When/Then: it reads back, and an update replaces the statement
	ps := must[*athena.GetPreparedStatementOutput](t, "GetPreparedStatement")(get()).PreparedStatement
	if aws.ToString(ps.WorkGroupName) != "primary" || aws.ToString(ps.Description) != "one event" || ps.LastModifiedTime == nil {
		t.Fatalf("PreparedStatement = %+v", ps)
	}
	must[*athena.UpdatePreparedStatementOutput](t, "UpdatePreparedStatement")(c.UpdatePreparedStatement(ctx, &athena.UpdatePreparedStatementInput{
		StatementName: aws.String("by_id"), WorkGroup: aws.String("primary"), QueryStatement: aws.String("SELECT id FROM events WHERE id = ?"),
	}))
	if got := aws.ToString(must[*athena.GetPreparedStatementOutput](t, "GetPreparedStatement")(get()).PreparedStatement.QueryStatement); got != "SELECT id FROM events WHERE id = ?" {
		t.Fatalf("QueryStatement = %q", got)
	}

	// When/Then: list and batch get find it, and name the miss
	list := must[*athena.ListPreparedStatementsOutput](t, "ListPreparedStatements")(c.ListPreparedStatements(ctx, &athena.ListPreparedStatementsInput{WorkGroup: aws.String("primary")}))
	if len(list.PreparedStatements) != 1 || aws.ToString(list.PreparedStatements[0].StatementName) != "by_id" {
		t.Fatalf("PreparedStatements = %+v", list.PreparedStatements)
	}
	batch := must[*athena.BatchGetPreparedStatementOutput](t, "BatchGetPreparedStatement")(c.BatchGetPreparedStatement(ctx, &athena.BatchGetPreparedStatementInput{
		WorkGroup: aws.String("primary"), PreparedStatementNames: []string{"by_id", "nope"},
	}))
	if len(batch.PreparedStatements) != 1 || len(batch.UnprocessedPreparedStatementNames) != 1 ||
		aws.ToString(batch.UnprocessedPreparedStatementNames[0].ErrorCode) != "STATEMENT_NOT_FOUND" {
		t.Fatalf("batch = %+v / %+v", batch.PreparedStatements, batch.UnprocessedPreparedStatementNames)
	}

	// When/Then: after a delete it is the modeled ResourceNotFoundException
	must[*athena.DeletePreparedStatementOutput](t, "DeletePreparedStatement")(c.DeletePreparedStatement(ctx, &athena.DeletePreparedStatementInput{
		StatementName: aws.String("by_id"), WorkGroup: aws.String("primary"),
	}))
	_, err = get()
	wantAPIError(t, "GetPreparedStatement after delete", err, "ResourceNotFoundException")
}
