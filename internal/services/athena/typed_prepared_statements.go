package athena

import (
	"context"
	"regexp"
	"sort"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// statementNamePattern is the model's StatementName pattern.
var statementNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_@:]{1,256}$`)

// batchErrorStatementNotFound is UnprocessedPreparedStatementName's code for
// "a prepared statement with the name provided could not be found."
const batchErrorStatementNotFound = "STATEMENT_NOT_FOUND"

type preparedStatementReq struct {
	StatementName  string  `json:"StatementName"`
	WorkGroup      string  `json:"WorkGroup"`
	QueryStatement string  `json:"QueryStatement"`
	Description    *string `json:"Description"`
}

type preparedStatementNameReq struct {
	StatementName string `json:"StatementName"`
	WorkGroup     string `json:"WorkGroup"`
}

type getPreparedStatementResp struct {
	PreparedStatement PreparedStatement `json:"PreparedStatement"`
}

type batchGetPreparedStatementReq struct {
	PreparedStatementNames []string `json:"PreparedStatementNames"`
	WorkGroup              string   `json:"WorkGroup"`
}

type batchGetPreparedStatementResp struct {
	PreparedStatements                []PreparedStatement                `json:"PreparedStatements"`
	UnprocessedPreparedStatementNames []UnprocessedPreparedStatementName `json:"UnprocessedPreparedStatementNames"`
}

type listPreparedStatementsReq struct {
	WorkGroup  string `json:"WorkGroup"`
	MaxResults int32  `json:"MaxResults"`
	NextToken  string `json:"NextToken"`
}

type listPreparedStatementsResp struct {
	PreparedStatements []PreparedStatementSummary `json:"PreparedStatements"`
	NextToken          string                     `json:"NextToken,omitempty"`
}

func preparedStatementLockKey(workGroup, name string) string {
	return "prepared-statement:" + preparedStatementKey(workGroup, name)
}

// validatePreparedStatement checks a create or update request and loads the
// workgroup it names.
func (s *Service) validatePreparedStatement(ctx context.Context, req *preparedStatementReq) *protocol.AWSError {
	if aerr := requireMembers([2]string{"StatementName", req.StatementName}, [2]string{"WorkGroup", req.WorkGroup}, [2]string{"QueryStatement", req.QueryStatement}); aerr != nil {
		return aerr
	}
	if len(req.QueryStatement) > maxQueryLength {
		return errInvalidRequest("QueryStatement must be at most %d characters long.", maxQueryLength)
	}
	_, aerr := s.loadWorkGroup(ctx, req.WorkGroup)
	return aerr
}

func (s *Service) createPreparedStatementTyped(ctx context.Context, req *preparedStatementReq) (*struct{}, *protocol.AWSError) {
	if aerr := s.validatePreparedStatement(ctx, req); aerr != nil {
		return nil, aerr
	}
	if !statementNamePattern.MatchString(req.StatementName) {
		return nil, errInvalidRequest("StatementName %q is not valid: it must match [a-zA-Z_][a-zA-Z0-9_@:]{1,256}.", req.StatementName)
	}
	defer s.lock(preparedStatementLockKey(req.WorkGroup, req.StatementName))()
	existing, err := s.store.getPreparedStatement(ctx, req.WorkGroup, req.StatementName)
	if err != nil {
		return nil, errInternal(err)
	}
	if existing != nil {
		return nil, errInvalidRequest("Prepared statement %s already exists in workgroup %s.", req.StatementName, req.WorkGroup)
	}
	return s.putPreparedStatement(ctx, &PreparedStatement{StatementName: req.StatementName, WorkGroupName: req.WorkGroup}, req)
}

func (s *Service) updatePreparedStatementTyped(ctx context.Context, req *preparedStatementReq) (*struct{}, *protocol.AWSError) {
	if aerr := s.validatePreparedStatement(ctx, req); aerr != nil {
		return nil, aerr
	}
	defer s.lock(preparedStatementLockKey(req.WorkGroup, req.StatementName))()
	ps, aerr := s.requirePreparedStatement(ctx, req.WorkGroup, req.StatementName)
	if aerr != nil {
		return nil, aerr
	}
	return s.putPreparedStatement(ctx, ps, req)
}

// putPreparedStatement applies a create or update request to ps and saves it.
func (s *Service) putPreparedStatement(ctx context.Context, ps *PreparedStatement, req *preparedStatementReq) (*struct{}, *protocol.AWSError) {
	ps.QueryStatement = req.QueryStatement
	if req.Description != nil {
		ps.Description = *req.Description
	}
	ps.LastModifiedTime = s.now()
	if err := s.store.putPreparedStatement(ctx, ps); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}

func (s *Service) requirePreparedStatement(ctx context.Context, workGroup, name string) (*PreparedStatement, *protocol.AWSError) {
	if aerr := requireMembers([2]string{"StatementName", name}, [2]string{"WorkGroup", workGroup}); aerr != nil {
		return nil, aerr
	}
	ps, err := s.store.getPreparedStatement(ctx, workGroup, name)
	if err != nil {
		return nil, errInternal(err)
	}
	if ps == nil {
		return nil, errPreparedStatementNotFound(name, workGroup)
	}
	return ps, nil
}

func (s *Service) getPreparedStatementTyped(ctx context.Context, req *preparedStatementNameReq) (*getPreparedStatementResp, *protocol.AWSError) {
	ps, aerr := s.requirePreparedStatement(ctx, req.WorkGroup, req.StatementName)
	if aerr != nil {
		return nil, aerr
	}
	return &getPreparedStatementResp{PreparedStatement: *ps}, nil
}

func (s *Service) batchGetPreparedStatementTyped(ctx context.Context, req *batchGetPreparedStatementReq) (*batchGetPreparedStatementResp, *protocol.AWSError) {
	if req.WorkGroup == "" {
		return nil, errRequired("WorkGroup")
	}
	if aerr := requireBatch("PreparedStatementNames", len(req.PreparedStatementNames)); aerr != nil {
		return nil, aerr
	}
	found, missed, aerr := batchGet(req.PreparedStatementNames,
		func(name string) (*PreparedStatement, *protocol.AWSError) {
			return s.requirePreparedStatement(ctx, req.WorkGroup, name)
		},
		func(name string, aerr *protocol.AWSError) UnprocessedPreparedStatementName {
			code := batchErrorStatementNotFound
			if aerr.Code != codeResourceNotFound {
				code = batchErrorInvalidInput
			}
			return UnprocessedPreparedStatementName{StatementName: name, ErrorCode: code, ErrorMessage: aerr.Message}
		})
	if aerr != nil {
		return nil, aerr
	}
	return &batchGetPreparedStatementResp{PreparedStatements: found, UnprocessedPreparedStatementNames: missed}, nil
}

func (s *Service) listPreparedStatementsTyped(ctx context.Context, req *listPreparedStatementsReq) (*listPreparedStatementsResp, *protocol.AWSError) {
	if req.WorkGroup == "" {
		return nil, errRequired("WorkGroup")
	}
	if _, aerr := s.loadWorkGroup(ctx, req.WorkGroup); aerr != nil {
		return nil, aerr
	}
	statements, err := scan[PreparedStatement](ctx, s.store, nsPreparedStatements, preparedStatementKey(req.WorkGroup, ""))
	if err != nil {
		return nil, errInternal(err)
	}
	sort.Slice(statements, func(i, j int) bool { return statements[i].StatementName < statements[j].StatementName })
	summaries := make([]PreparedStatementSummary, len(statements))
	for i, ps := range statements {
		summaries[i] = PreparedStatementSummary{StatementName: ps.StatementName, LastModifiedTime: ps.LastModifiedTime}
	}
	page, aerr := paginate(summaries, req.MaxResults, req.NextToken, preparedStatementsPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listPreparedStatementsResp{PreparedStatements: page.Items, NextToken: page.NextToken}, nil
}

func (s *Service) deletePreparedStatementTyped(ctx context.Context, req *preparedStatementNameReq) (*struct{}, *protocol.AWSError) {
	defer s.lock(preparedStatementLockKey(req.WorkGroup, req.StatementName))()
	if _, aerr := s.requirePreparedStatement(ctx, req.WorkGroup, req.StatementName); aerr != nil {
		return nil, aerr
	}
	if err := s.store.delete(ctx, nsPreparedStatements, preparedStatementKey(req.WorkGroup, req.StatementName)); err != nil {
		return nil, errInternal(err)
	}
	return &struct{}{}, nil
}
