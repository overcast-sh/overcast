package athena

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type startQueryExecReq struct {
	QueryString         string `json:"QueryString" cbor:"QueryString"`
	WorkGroup           string `json:"WorkGroup" cbor:"WorkGroup"`
	ResultConfiguration struct {
		OutputLocation string `json:"OutputLocation" cbor:"OutputLocation"`
	} `json:"ResultConfiguration" cbor:"ResultConfiguration"`
}

type queryIDReq struct {
	QueryExecutionId string `json:"QueryExecutionId" cbor:"QueryExecutionId"`
}

type workGroupNameReq struct {
	WorkGroup string `json:"WorkGroup" cbor:"WorkGroup"`
}

type createWorkGroupReq struct {
	Name        string      `json:"Name" cbor:"Name"`
	Description string      `json:"Description" cbor:"Description"`
	Tags        []athenaTag `json:"Tags" cbor:"Tags"`
	// Configuration is kept as the caller sent it (result location, bytes
	// cutoff, engine version, …) and handed back verbatim by GetWorkGroup;
	// Overcast does not act on any of it.
	Configuration map[string]any `json:"Configuration" cbor:"Configuration"`
}

type startQueryExecResp struct {
	QueryExecutionId string `json:"QueryExecutionId" cbor:"QueryExecutionId"`
}

type getQueryExecResp struct {
	QueryExecution QueryExecution `json:"QueryExecution" cbor:"QueryExecution"`
}

type getQueryResultsResp struct {
	ResultSet resultSetWire `json:"ResultSet" cbor:"ResultSet"`
}

type resultSetWire struct {
	Rows              []any             `json:"Rows" cbor:"Rows"`
	ResultSetMetadata resultSetMetaWire `json:"ResultSetMetadata" cbor:"ResultSetMetadata"`
}

type resultSetMetaWire struct {
	ColumnInfo []any `json:"ColumnInfo" cbor:"ColumnInfo"`
}

type listQueriesResp struct {
	QueryExecutionIds []string `json:"QueryExecutionIds" cbor:"QueryExecutionIds"`
}

type getWorkGroupResp struct {
	WorkGroup WorkGroup `json:"WorkGroup" cbor:"WorkGroup"`
}

type listWorkGroupsResp struct {
	WorkGroups []workGroupSummary `json:"WorkGroups" cbor:"WorkGroups"`
}

type workGroupSummary struct {
	Name  string `json:"Name" cbor:"Name"`
	State string `json:"State" cbor:"State"`
}

// errQueryNotFound and errWorkGroupNotFound report an identifier Athena does
// not know.
//
// 400, not 404. No exception in the Athena model overrides @httpError, and
// InvalidRequestException is a client error, so the awsJson1_1 default
// applies, and each operation's Errors section states it outright:
// "InvalidRequestException ... HTTP Status Code: 400". Kinesis's
// errNoSuchStream and Firehose's errStreamNotFound answer the same way for
// the same reason (#2009).
//
// InvalidRequestException is the only client error GetQueryExecution,
// GetQueryResults, GetWorkGroup, DeleteWorkGroup and StopQueryExecution model.
// The three tag operations additionally model ResourceNotFoundException, but
// both shapes are 400 client errors, so the status below is right for every
// caller; which of the two AWS picks per operation is a separate question this
// change does not settle.
func errQueryNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidRequestException",
		Message:    fmt.Sprintf("QueryExecution %s not found", id),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errWorkGroupNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidRequestException",
		Message:    fmt.Sprintf("WorkGroup %s not found", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

func (s *Service) startQueryExecutionTyped(ctx context.Context, req *startQueryExecReq) (*startQueryExecResp, *protocol.AWSError) {
	now := float64(s.clk.Now().Unix())
	qe := &QueryExecution{
		QueryExecutionId: uuid.NewString(),
		Query:            req.QueryString,
		WorkGroup:        req.WorkGroup,
	}
	qe.Status.State = "SUCCEEDED"
	qe.Status.SubmissionDateTime = now
	qe.Status.CompletionDateTime = now
	qe.ResultConfiguration.OutputLocation = req.ResultConfiguration.OutputLocation
	if err := s.store.putQuery(ctx, qe); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &startQueryExecResp{QueryExecutionId: qe.QueryExecutionId}, nil
}

func (s *Service) getQueryExecutionTyped(ctx context.Context, req *queryIDReq) (*getQueryExecResp, *protocol.AWSError) {
	qe, found := s.store.getQuery(ctx, req.QueryExecutionId)
	if !found {
		return nil, errQueryNotFound(req.QueryExecutionId)
	}
	return &getQueryExecResp{QueryExecution: *qe}, nil
}

func (s *Service) getQueryResultsTyped(ctx context.Context, req *queryIDReq) (*getQueryResultsResp, *protocol.AWSError) {
	if _, found := s.store.getQuery(ctx, req.QueryExecutionId); !found {
		return nil, errQueryNotFound(req.QueryExecutionId)
	}
	return &getQueryResultsResp{ResultSet: resultSetWire{
		Rows:              []any{},
		ResultSetMetadata: resultSetMetaWire{ColumnInfo: []any{}},
	}}, nil
}

// stopQueryExecutionTyped answers StopQueryExecution: an unknown id is the
// modeled InvalidRequestException, and a known one an empty
// StopQueryExecutionOutput.
//
// It changes no state, and cannot: Overcast completes a query inside
// StartQueryExecution, so every stored query is already SUCCEEDED — a terminal
// state with nothing left to interrupt — and AWS leaves a terminal query's
// state alone. The model backs that reading: the operation is
// smithy.api#idempotent and declares no wrong-state exception, so a Stop that
// arrives after completion has to be accepted rather than rejected. If queries
// ever run asynchronously here, this is where CANCELLED would be set.
func (s *Service) stopQueryExecutionTyped(ctx context.Context, req *queryIDReq) (*struct{}, *protocol.AWSError) {
	if _, found := s.store.getQuery(ctx, req.QueryExecutionId); !found {
		return nil, errQueryNotFound(req.QueryExecutionId)
	}
	return &struct{}{}, nil
}

func (s *Service) listQueryExecutionsTyped(ctx context.Context, _ *struct{}) (*listQueriesResp, *protocol.AWSError) {
	queries, err := s.store.listQueries(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	ids := make([]string, 0, len(queries))
	for _, q := range queries {
		ids = append(ids, q.QueryExecutionId)
	}
	return &listQueriesResp{QueryExecutionIds: ids}, nil
}

func (s *Service) createWorkGroupTyped(ctx context.Context, req *createWorkGroupReq) (*struct{}, *protocol.AWSError) {
	if req.Name == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidRequestException", Message: "Name is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	tags := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		tags[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(athenaTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	wg := &workGroupRecord{
		WorkGroup: WorkGroup{Name: req.Name, State: "ENABLED", Description: req.Description, Configuration: req.Configuration},
		Tags:      tags,
	}
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (s *Service) getWorkGroupTyped(ctx context.Context, req *workGroupNameReq) (*getWorkGroupResp, *protocol.AWSError) {
	wg, found := s.store.getWorkGroup(ctx, req.WorkGroup)
	if !found {
		return nil, errWorkGroupNotFound(req.WorkGroup)
	}
	return &getWorkGroupResp{WorkGroup: wg.WorkGroup}, nil
}

func (s *Service) listWorkGroupsTyped(ctx context.Context, _ *struct{}) (*listWorkGroupsResp, *protocol.AWSError) {
	workgroups, err := s.store.listWorkGroups(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	summaries := make([]workGroupSummary, 0, len(workgroups))
	for _, wg := range workgroups {
		summaries = append(summaries, workGroupSummary{Name: wg.Name, State: wg.State})
	}
	return &listWorkGroupsResp{WorkGroups: summaries}, nil
}

func (s *Service) deleteWorkGroupTyped(ctx context.Context, req *workGroupNameReq) (*struct{}, *protocol.AWSError) {
	if _, found := s.store.getWorkGroup(ctx, req.WorkGroup); !found {
		return nil, errWorkGroupNotFound(req.WorkGroup)
	}
	if err := s.store.deleteWorkGroup(ctx, req.WorkGroup); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

type tagResourceReq struct {
	ResourceARN string      `json:"ResourceARN" cbor:"ResourceARN"`
	Tags        []athenaTag `json:"Tags" cbor:"Tags"`
}

type untagResourceReq struct {
	ResourceARN string   `json:"ResourceARN" cbor:"ResourceARN"`
	TagKeys     []string `json:"TagKeys" cbor:"TagKeys"`
}

type listTagsForResourceReq struct {
	ResourceARN string `json:"ResourceARN" cbor:"ResourceARN"`
}

type listTagsForResourceResp struct {
	Tags []athenaTag `json:"Tags" cbor:"Tags"`
}

func (s *Service) tagResourceTyped(ctx context.Context, req *tagResourceReq) (*struct{}, *protocol.AWSError) {
	if req.ResourceARN == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidRequestException", Message: "ResourceARN is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	wgName, aerr := workGroupNameFromARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	wg, found := s.store.getWorkGroup(ctx, wgName)
	if !found {
		return nil, errWorkGroupNotFound(wgName)
	}
	tags := wg.GetTags()
	if tags == nil {
		tags = map[string]string{}
	}
	for _, t := range req.Tags {
		tags[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(athenaTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	wg.SetTags(tags)
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (s *Service) untagResourceTyped(ctx context.Context, req *untagResourceReq) (*struct{}, *protocol.AWSError) {
	if req.ResourceARN == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidRequestException", Message: "ResourceARN is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	wgName, aerr := workGroupNameFromARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	wg, found := s.store.getWorkGroup(ctx, wgName)
	if !found {
		return nil, errWorkGroupNotFound(wgName)
	}
	tags := wg.GetTags()
	if tags != nil {
		for _, k := range req.TagKeys {
			delete(tags, k)
		}
		wg.SetTags(tags)
	}
	if err := s.store.putWorkGroup(ctx, wg); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (s *Service) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceReq) (*listTagsForResourceResp, *protocol.AWSError) {
	if req.ResourceARN == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidRequestException", Message: "ResourceARN is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	wgName, aerr := workGroupNameFromARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	wg, found := s.store.getWorkGroup(ctx, wgName)
	if !found {
		return nil, errWorkGroupNotFound(wgName)
	}
	return &listTagsForResourceResp{Tags: tagsToList(wg.GetTags())}, nil
}
