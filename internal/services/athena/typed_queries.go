package athena

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Length limits from the model: QueryString, and each ExecutionParameter.
const (
	maxQueryLength     = 262144
	maxParameterLength = 1024
)

// normalize gives a record written before workgroups were tracked the
// workgroup it ran in: primary, as every query that names none does.
func (qe *QueryExecution) normalize() {
	qe.WorkGroup = orPrimary(qe.WorkGroup)
}

type startQueryExecReq struct {
	QueryString              string                    `json:"QueryString"`
	ClientRequestToken       string                    `json:"ClientRequestToken"`
	QueryExecutionContext    *QueryExecutionContext    `json:"QueryExecutionContext"`
	ResultConfiguration      *ResultConfiguration      `json:"ResultConfiguration"`
	ResultReuseConfiguration *ResultReuseConfiguration `json:"ResultReuseConfiguration"`
	WorkGroup                string                    `json:"WorkGroup"`
	ExecutionParameters      []string                  `json:"ExecutionParameters"`
	EngineConfiguration      any                       `json:"EngineConfiguration"`
}

type startQueryExecResp struct {
	QueryExecutionId string `json:"QueryExecutionId"`
}

type queryIDReq struct {
	QueryExecutionId string `json:"QueryExecutionId"`
}

type getQueryExecResp struct {
	QueryExecution QueryExecution `json:"QueryExecution"`
}

type batchGetQueryExecReq struct {
	QueryExecutionIds []string `json:"QueryExecutionIds"`
}

type batchGetQueryExecResp struct {
	QueryExecutions              []QueryExecution              `json:"QueryExecutions"`
	UnprocessedQueryExecutionIds []UnprocessedQueryExecutionId `json:"UnprocessedQueryExecutionIds"`
}

type listQueriesReq struct {
	MaxResults int32  `json:"MaxResults"`
	NextToken  string `json:"NextToken"`
	WorkGroup  string `json:"WorkGroup"`
}

type listQueriesResp struct {
	QueryExecutionIds []string `json:"QueryExecutionIds"`
	NextToken         string   `json:"NextToken,omitempty"`
}

type getQueryResultsReq struct {
	QueryExecutionId string `json:"QueryExecutionId"`
	MaxResults       int32  `json:"MaxResults"`
	NextToken        string `json:"NextToken"`
	QueryResultType  string `json:"QueryResultType"`
}

type getQueryResultsResp struct {
	ResultSet   ResultSet `json:"ResultSet"`
	UpdateCount *int64    `json:"UpdateCount,omitempty"`
	NextToken   string    `json:"NextToken,omitempty"`
}

func validateStart(req *startQueryExecReq) *protocol.AWSError {
	if req.QueryString == "" {
		return errRequired("QueryString")
	}
	if len(req.QueryString) > maxQueryLength {
		return errInvalidRequest("QueryString must be at most %d characters long.", maxQueryLength)
	}
	for _, p := range req.ExecutionParameters {
		if p == "" || len(p) > maxParameterLength {
			return errInvalidRequest("Each ExecutionParameters value must be between 1 and %d characters long.", maxParameterLength)
		}
	}
	return nil
}

func (s *Service) startQueryExecutionTyped(ctx context.Context, req *startQueryExecReq) (*startQueryExecResp, *protocol.AWSError) {
	if aerr := validateStart(req); aerr != nil {
		return nil, aerr
	}
	fingerprinted := *req
	fingerprinted.ClientRequestToken = ""
	var submitted *QueryExecution
	id, aerr := s.idempotent(ctx, "StartQueryExecution", req.ClientRequestToken, &fingerprinted, func() (string, *protocol.AWSError) {
		qe, aerr := s.newQueryExecution(ctx, req)
		if aerr != nil {
			return "", aerr
		}
		if err := s.store.putQuery(ctx, qe); err != nil {
			return "", errInternal(err)
		}
		submitted = qe
		return qe.QueryExecutionId, nil
	})
	if aerr != nil {
		return nil, aerr
	}
	// Submitted outside the idempotency lock: an executor may report
	// transitions from its own goroutine, and a retry must not wait on it.
	if submitted != nil {
		s.executor.Submit(context.WithoutCancel(ctx), *submitted, s.applyTransition)
	}
	return &startQueryExecResp{QueryExecutionId: id}, nil
}

// newQueryExecution builds the QUEUED record for a start request, resolved
// against the workgroup it runs in.
func (s *Service) newQueryExecution(ctx context.Context, req *startQueryExecReq) (*QueryExecution, *protocol.AWSError) {
	wg, aerr := s.loadWorkGroup(ctx, orPrimary(req.WorkGroup))
	if aerr != nil {
		return nil, aerr
	}
	if wg.State == workGroupDisabled {
		return nil, errInvalidRequest("WorkGroup %s is disabled.", wg.Name)
	}
	results, aerr := resolveResultConfiguration(wg.Configuration, req.ResultConfiguration)
	if aerr != nil {
		return nil, aerr
	}
	id, kind := uuid.NewString(), statementType(req.QueryString)
	results.OutputLocation = resultObject(results.OutputLocation, id, kind)
	qe := &QueryExecution{
		QueryExecutionId:         id,
		Query:                    req.QueryString,
		StatementType:            kind,
		WorkGroup:                wg.Name,
		ResultConfiguration:      results,
		ResultReuseConfiguration: resultReuseOrDefault(req.ResultReuseConfiguration),
		ExecutionParameters:      req.ExecutionParameters,
		EngineVersion:            wg.engineVersion(),
		Status:                   QueryExecutionStatus{State: stateQueued, SubmissionDateTime: s.now()},
	}
	if cfg := wg.Configuration; cfg != nil {
		qe.ManagedQueryResultsConfiguration = cfg.ManagedQueryResultsConfiguration
		qe.QueryResultsS3AccessGrantsConfiguration = cfg.QueryResultsS3AccessGrantsConfiguration
	}
	if req.QueryExecutionContext != nil {
		qe.QueryExecutionContext = *req.QueryExecutionContext
	}
	return qe, nil
}

// resultObject is the object a query's results are written to, which is
// what GetQueryExecution reports as OutputLocation: the result location the
// query resolved to, then "<id>.csv" — or "<id>.txt" for the plain-text
// output of a DDL or UTILITY statement. Managed results have no location.
func resultObject(location, id, kind string) string {
	if location == "" {
		return ""
	}
	ext := ".csv"
	if kind != statementDML {
		ext = ".txt"
	}
	return strings.TrimSuffix(location, "/") + "/" + id + ext
}

// resolveResultConfiguration decides where a query's results go. "If set to
// true, the settings for the workgroup override client-side settings. If set
// to false, client-side settings are used" — member by member, so a client
// that names only an OutputLocation still gets the workgroup's encryption.
// "If none of them is set, Athena issues an error that no output location is
// provided", unless the workgroup keeps results in Athena-managed storage.
func resolveResultConfiguration(wgCfg *WorkGroupConfiguration, client *ResultConfiguration) (ResultConfiguration, *protocol.AWSError) {
	var workGroup ResultConfiguration
	enforced, managed := false, false
	if wgCfg != nil {
		if wgCfg.ResultConfiguration != nil {
			workGroup = *wgCfg.ResultConfiguration
		}
		enforced = isTrue(wgCfg.EnforceWorkGroupConfiguration)
		managed = wgCfg.ManagedQueryResultsConfiguration != nil && wgCfg.ManagedQueryResultsConfiguration.Enabled
	}
	resolved := workGroup
	if client != nil && !enforced {
		setIf(&resolved.OutputLocation, client.OutputLocation, client.OutputLocation != "")
		setIf(&resolved.ExpectedBucketOwner, client.ExpectedBucketOwner, client.ExpectedBucketOwner != "")
		setIf(&resolved.AclConfiguration, client.AclConfiguration, client.AclConfiguration != nil)
		setIf(&resolved.EncryptionConfiguration, client.EncryptionConfiguration, client.EncryptionConfiguration != nil)
	}
	if resolved.OutputLocation == "" && !managed {
		return resolved, errInvalidRequest("No output location provided. An output location is required either through the Workgroup result configuration setting or as an API input.")
	}
	return resolved, nil
}

// resultReuseOrDefault is the reuse configuration a query reports: reuse is
// off unless the caller turned it on.
func resultReuseOrDefault(c *ResultReuseConfiguration) *ResultReuseConfiguration {
	if c != nil && c.ResultReuseByAgeConfiguration != nil {
		return c
	}
	return &ResultReuseConfiguration{ResultReuseByAgeConfiguration: &ResultReuseByAgeConfiguration{}}
}

func (s *Service) requireQuery(ctx context.Context, id string) (*QueryExecution, *protocol.AWSError) {
	if id == "" {
		return nil, errRequired("QueryExecutionId")
	}
	qe, err := s.store.getQuery(ctx, id)
	if err != nil {
		return nil, errInternal(err)
	}
	if qe == nil {
		return nil, errQueryNotFound(id)
	}
	return qe, nil
}

func (s *Service) getQueryExecutionTyped(ctx context.Context, req *queryIDReq) (*getQueryExecResp, *protocol.AWSError) {
	qe, aerr := s.requireQuery(ctx, req.QueryExecutionId)
	if aerr != nil {
		return nil, aerr
	}
	return &getQueryExecResp{QueryExecution: *qe}, nil
}

func (s *Service) batchGetQueryExecutionTyped(ctx context.Context, req *batchGetQueryExecReq) (*batchGetQueryExecResp, *protocol.AWSError) {
	if aerr := requireBatch("QueryExecutionIds", len(req.QueryExecutionIds)); aerr != nil {
		return nil, aerr
	}
	found, missed, aerr := batchGet(req.QueryExecutionIds,
		func(id string) (*QueryExecution, *protocol.AWSError) { return s.requireQuery(ctx, id) },
		func(id string, aerr *protocol.AWSError) UnprocessedQueryExecutionId {
			return UnprocessedQueryExecutionId{QueryExecutionId: id, ErrorCode: batchErrorInvalidInput, ErrorMessage: aerr.Message}
		})
	if aerr != nil {
		return nil, aerr
	}
	return &batchGetQueryExecResp{QueryExecutions: found, UnprocessedQueryExecutionIds: missed}, nil
}

// batchErrorInvalidInput is the ErrorCode a BatchGet* call reports for an
// identifier it cannot return. UnprocessedPreparedStatementName documents
// INVALID_INPUT and STATEMENT_NOT_FOUND; the named-query and
// query-execution shapes document no values, so they use INVALID_INPUT.
const batchErrorInvalidInput = "INVALID_INPUT"

// listQueryExecutionsTyped lists a workgroup's executions, most recent
// first. "If a workgroup is not specified, returns a list of query execution
// IDs for the primary workgroup," per ListQueryExecutions.
func (s *Service) listQueryExecutionsTyped(ctx context.Context, req *listQueriesReq) (*listQueriesResp, *protocol.AWSError) {
	wg, aerr := s.loadWorkGroup(ctx, orPrimary(req.WorkGroup))
	if aerr != nil {
		return nil, aerr
	}
	queries, err := s.store.listQueries(ctx)
	if err != nil {
		return nil, errInternal(err)
	}
	kept := queries[:0]
	for _, qe := range queries {
		if qe.WorkGroup == wg.Name {
			kept = append(kept, qe)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		a, b := kept[i].Status.SubmissionDateTime, kept[j].Status.SubmissionDateTime
		if a != b {
			return a > b
		}
		return kept[i].QueryExecutionId < kept[j].QueryExecutionId
	})
	ids := make([]string, len(kept))
	for i, qe := range kept {
		ids[i] = qe.QueryExecutionId
	}
	page, aerr := paginate(ids, req.MaxResults, req.NextToken, queryExecutionsPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listQueriesResp{QueryExecutionIds: page.Items, NextToken: page.NextToken}, nil
}

// stopQueryExecutionTyped cancels a query that has not finished. A finished
// one keeps its state: the operation is idempotent and models no wrong-state
// error, so a stop after completion is accepted and changes nothing.
func (s *Service) stopQueryExecutionTyped(ctx context.Context, req *queryIDReq) (*struct{}, *protocol.AWSError) {
	qe, aerr := s.requireQuery(ctx, req.QueryExecutionId)
	if aerr != nil {
		return nil, aerr
	}
	if !isTerminal(qe.Status.State) {
		s.applyTransition(ctx, qe.QueryExecutionId, queryTransition{State: stateCancelled, StateChangeReason: "Query was cancelled by the user."})
		s.executor.Cancel(ctx, qe.QueryExecutionId)
	}
	return &struct{}{}, nil
}

// maxQueryResults is GetQueryResults' MaxQueryResults cap.
const maxQueryResults = 1000

func (s *Service) getQueryResultsTyped(ctx context.Context, req *getQueryResultsReq) (*getQueryResultsResp, *protocol.AWSError) {
	if req.MaxResults < 0 || req.MaxResults > maxQueryResults {
		return nil, errInvalidRequest("MaxResults must be between 1 and %d.", maxQueryResults)
	}
	qe, aerr := s.requireQuery(ctx, req.QueryExecutionId)
	if aerr != nil {
		return nil, aerr
	}
	switch state := qe.Status.State; {
	case state == stateSucceeded:
		return s.executor.Results(ctx, *qe, req)
	case isTerminal(state):
		return nil, errInvalidRequest("Query did not finish successfully. Final query state: %s", state)
	default:
		return nil, errInvalidRequest("Query has not yet finished. Current state: %s", state)
	}
}
