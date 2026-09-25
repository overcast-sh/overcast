package athena

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// prepared_sql.go — SQL's PREPARE and DEALLOCATE PREPARE. On Athena they
// create and delete the workgroup's prepared statements, the same ones
// CreatePreparedStatement and DeletePreparedStatement manage and a later
// EXECUTE runs. Trino would keep them in its session, which ends with the
// query, so Overcast runs them itself.

// preparedRunner runs PREPARE name FROM statement, or DEALLOCATE PREPARE name.
type preparedRunner struct {
	s          *Service
	workGroup  string
	name       string
	query      string
	deallocate bool
}

// preparedRunnerFor is the runner for qe when it is PREPARE or DEALLOCATE
// PREPARE.
func (s *Service) preparedRunnerFor(qe QueryExecution) (queryRunner, bool) {
	p, err := newEngineParser(qe.Query)
	if err != nil {
		return nil, false
	}
	r := preparedRunner{s: s, workGroup: qe.WorkGroup}
	switch {
	case p.accept("PREPARE"):
		if r.name, err = p.preparedName(); err == nil {
			err = p.expect("FROM")
		}
		if err == nil {
			if r.query = strings.TrimSpace(p.src[p.peek().start:]); r.query == "" {
				err = p.fail("expected a statement")
			}
		}
	case p.accept("DEALLOCATE", "PREPARE"):
		r.deallocate = true
		if r.name, err = p.preparedName(); err == nil {
			err = p.end()
		}
	default:
		return nil, false
	}
	if err != nil {
		return failedRunner{syntaxFailure(err)}, true
	}
	return r, true
}

// run saves or deletes the statement. A statement's name is unique in its
// workgroup, so PREPARE of a name that exists fails, as
// CreatePreparedStatement does.
func (r preparedRunner) run(ctx context.Context, _ func()) (*queryResult, *queryFailure) {
	var aerr *protocol.AWSError
	if r.deallocate {
		_, aerr = r.s.deletePreparedStatementTyped(ctx, &preparedStatementNameReq{StatementName: r.name, WorkGroup: r.workGroup})
	} else {
		_, aerr = r.s.createPreparedStatementTyped(ctx, &preparedStatementReq{StatementName: r.name, WorkGroup: r.workGroup, QueryStatement: r.query})
	}
	if aerr != nil {
		return nil, failure(errorCategoryUser, errorTypeUser, aerr.Message)
	}
	return emptyResult(), nil
}

func (preparedRunner) background() bool { return false }
