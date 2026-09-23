// Package athena provides a basic emulation of Amazon Athena.
//
// Implemented operations: StartQueryExecution, GetQueryExecution,
// GetQueryResults, StopQueryExecution, ListQueryExecutions, CreateWorkGroup,
// GetWorkGroup, ListWorkGroups, DeleteWorkGroup.
//
// Queries are accepted and immediately marked SUCCEEDED with empty results.
package athena

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/services/glue"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "athena"

// ─── Types ────────────────────────────────────────────────────

// QueryExecution represents an Athena query execution.
type QueryExecution struct {
	QueryExecutionId string `json:"QueryExecutionId"`
	Query            string `json:"Query"`
	WorkGroup        string `json:"WorkGroup,omitempty"`
	Status           struct {
		State              string  `json:"State"`
		SubmissionDateTime float64 `json:"SubmissionDateTime"`
		CompletionDateTime float64 `json:"CompletionDateTime,omitempty"`
	} `json:"Status"`
	ResultConfiguration struct {
		OutputLocation string `json:"OutputLocation,omitempty"`
	} `json:"ResultConfiguration,omitempty"`
}

// WorkGroup represents an Athena workgroup. This is the wire shape — the AWS
// model's WorkGroup carries no Tags member, so tags must never be embedded
// here. See workGroupRecord for how tags are persisted.
type WorkGroup struct {
	Name          string         `json:"Name"`
	State         string         `json:"State"`
	Description   string         `json:"Description,omitempty"`
	Configuration map[string]any `json:"Configuration,omitempty"`
}

// workGroupRecord is a WorkGroup as persisted: the wire shape plus its tags.
// Tags are kept off GetWorkGroup/ListWorkGroups (the model keeps them off
// WorkGroup) and exposed only through TagResource, UntagResource and
// ListTagsForResource.
type workGroupRecord struct {
	WorkGroup
	Tags map[string]string `json:"overcastTags,omitempty"`
}

func (wg *workGroupRecord) GetTags() map[string]string  { return wg.Tags }
func (wg *workGroupRecord) SetTags(t map[string]string) { wg.Tags = t }

// ─── Store ────────────────────────────────────────────────────

type athenaStore struct {
	store state.Store
	cfg   *config.Config
	clk   clock.Clock
}

func newAthenaStore(s state.Store, cfg *config.Config, clk clock.Clock) *athenaStore {
	return &athenaStore{store: s, cfg: cfg, clk: clk}
}

const (
	nsQueries    = "athena:queries"
	nsWorkGroups = "athena:workgroups"
)

func (s *athenaStore) putQuery(ctx context.Context, q *QueryExecution) error {
	raw, err := json.Marshal(q)
	if err != nil {
		return fmt.Errorf("athena: marshal query execution: %w", err)
	}
	return s.store.Set(ctx, nsQueries, q.QueryExecutionId, string(raw))
}

func (s *athenaStore) getQuery(ctx context.Context, id string) (*QueryExecution, bool) {
	raw, found, err := s.store.Get(ctx, nsQueries, id)
	if err != nil || !found {
		return nil, false
	}
	var q QueryExecution
	if json.Unmarshal([]byte(raw), &q) != nil {
		return nil, false
	}
	return &q, true
}

func (s *athenaStore) listQueries(ctx context.Context) ([]*QueryExecution, error) {
	pairs, err := s.store.Scan(ctx, nsQueries, "")
	if err != nil {
		return nil, err
	}
	out := make([]*QueryExecution, 0, len(pairs))
	for _, kv := range pairs {
		var q QueryExecution
		if json.Unmarshal([]byte(kv.Value), &q) == nil {
			out = append(out, &q)
		}
	}
	return out, nil
}

func (s *athenaStore) putWorkGroup(ctx context.Context, wg *workGroupRecord) error {
	raw, err := json.Marshal(wg)
	if err != nil {
		return fmt.Errorf("athena: marshal workgroup: %w", err)
	}
	return s.store.Set(ctx, nsWorkGroups, wg.Name, string(raw))
}

func (s *athenaStore) getWorkGroup(ctx context.Context, name string) (*workGroupRecord, bool) {
	raw, found, err := s.store.Get(ctx, nsWorkGroups, name)
	if err != nil || !found {
		return nil, false
	}
	var wg workGroupRecord
	if json.Unmarshal([]byte(raw), &wg) != nil {
		return nil, false
	}
	return &wg, true
}

func (s *athenaStore) listWorkGroups(ctx context.Context) ([]*workGroupRecord, error) {
	pairs, err := s.store.Scan(ctx, nsWorkGroups, "")
	if err != nil {
		return nil, err
	}
	out := make([]*workGroupRecord, 0, len(pairs))
	for _, kv := range pairs {
		var wg workGroupRecord
		if json.Unmarshal([]byte(kv.Value), &wg) == nil {
			out = append(out, &wg)
		}
	}
	return out, nil
}

func (s *athenaStore) deleteWorkGroup(ctx context.Context, name string) error {
	return s.store.Delete(ctx, nsWorkGroups, name)
}

// ─── Service ──────────────────────────────────────────────────

// Service implements router.Service and router.TargetDispatcher for Athena.
type Service struct {
	log     *serviceutil.ServiceLogger
	store   *athenaStore
	cfg     *config.Config
	clk     clock.Clock
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
	catalog glue.Catalog // set by InitGlueCatalog; see glue_catalog.go
}

// New returns a configured Athena Service.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	s := &Service{
		log:   serviceutil.NewServiceLogger(logger, serviceName),
		store: newAthenaStore(st, cfg, clk),
		cfg:   cfg,
		clk:   clk,
	}
	s.ops = map[string]http.HandlerFunc{
		"StartQueryExecution": s.startQueryExecution,
		"GetQueryExecution":   s.getQueryExecution,
		"GetQueryResults":     s.getQueryResults,
		"StopQueryExecution":  s.stopQueryExecution,
		"ListQueryExecutions": s.listQueryExecutions,
		"CreateWorkGroup":     s.createWorkGroup,
		"GetWorkGroup":        s.getWorkGroup,
		"ListWorkGroups":      s.listWorkGroups,
		"DeleteWorkGroup":     s.deleteWorkGroup,
		"TagResource":         s.tagResource,
		"UntagResource":       s.untagResource,
		"ListTagsForResource": s.listTagsForResource,
	}
	s.typedOp = s.typedOps()
	return s
}

func (s *Service) Name() string                { return serviceName }
func (s *Service) RegisterRoutes(_ chi.Router) {}
func (s *Service) TargetPrefix() string        { return "AmazonAthena." }

func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code: "UnsupportedProtocol", Message: "Athena does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		if c.Name() != codec.NameRPCv2CBOR {
			s.dispatchLegacy(w, r, opName)
			return
		}
		if typed, ok := s.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		c.WriteError(w, r, protocol.ErrNotImplemented)
		return
	}
	target := r.Header.Get("X-Amz-Target")
	opName := target
	if idx := strings.LastIndex(target, "."); idx >= 0 {
		opName = target[idx+1:]
	}
	s.dispatchLegacy(w, r, opName)
}

func (s *Service) dispatchLegacy(w http.ResponseWriter, r *http.Request, opName string) {
	if fn, ok := s.ops[opName]; ok {
		fn(w, r)
		return
	}
	protocol.NotImplementedJSON(w, r)
}

// ─── Handlers ─────────────────────────────────────────────────

func (s *Service) startQueryExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryString         string `json:"QueryString"`
		WorkGroup           string `json:"WorkGroup"`
		ResultConfiguration struct {
			OutputLocation string `json:"OutputLocation"`
		} `json:"ResultConfiguration"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
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

	if err := s.store.putQuery(r.Context(), qe); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"QueryExecutionId": qe.QueryExecutionId,
	})
}

func (s *Service) getQueryExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryExecutionId string `json:"QueryExecutionId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	qe, found := s.store.getQuery(r.Context(), req.QueryExecutionId)
	if !found {
		protocol.WriteJSONError(w, r, errQueryNotFound(req.QueryExecutionId))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"QueryExecution": qe})
}

func (s *Service) getQueryResults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryExecutionId string `json:"QueryExecutionId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, found := s.store.getQuery(r.Context(), req.QueryExecutionId); !found {
		protocol.WriteJSONError(w, r, errQueryNotFound(req.QueryExecutionId))
		return
	}
	// Return empty result set.
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"ResultSet": map[string]any{
			"Rows":              []any{},
			"ResultSetMetadata": map[string]any{"ColumnInfo": []any{}},
		},
	})
}

func (s *Service) stopQueryExecution(w http.ResponseWriter, r *http.Request) {
	// Delegates to stopQueryExecutionTyped (typed_logic.go) so the legacy
	// JSON1.0/1.1 path and the CBOR typed path share one implementation.
	var req queryIDReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, aerr := s.stopQueryExecutionTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) listQueryExecutions(w http.ResponseWriter, r *http.Request) {
	queries, err := s.store.listQueries(r.Context())
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	ids := make([]string, 0, len(queries))
	for _, q := range queries {
		ids = append(ids, q.QueryExecutionId)
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"QueryExecutionIds": ids})
}

func (s *Service) createWorkGroup(w http.ResponseWriter, r *http.Request) {
	// Delegates to createWorkGroupTyped (typed_logic.go) so the legacy
	// JSON1.0/1.1 path and the CBOR typed path share one implementation —
	// the legacy copy previously re-implemented this inline and silently
	// ignored Tags (#1196).
	var req createWorkGroupReq
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, aerr := s.createWorkGroupTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) getWorkGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkGroup string `json:"WorkGroup"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	wg, found := s.store.getWorkGroup(r.Context(), req.WorkGroup)
	if !found {
		protocol.WriteJSONError(w, r, errWorkGroupNotFound(req.WorkGroup))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"WorkGroup": &wg.WorkGroup})
}

func (s *Service) listWorkGroups(w http.ResponseWriter, r *http.Request) {
	workgroups, err := s.store.listWorkGroups(r.Context())
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	summaries := make([]map[string]any, 0, len(workgroups))
	for _, wg := range workgroups {
		summaries = append(summaries, map[string]any{
			"Name":  wg.Name,
			"State": wg.State,
		})
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"WorkGroups": summaries})
}

func (s *Service) deleteWorkGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkGroup string `json:"WorkGroup"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, found := s.store.getWorkGroup(r.Context(), req.WorkGroup); !found {
		protocol.WriteJSONError(w, r, errWorkGroupNotFound(req.WorkGroup))
		return
	}
	if err := s.store.deleteWorkGroup(r.Context(), req.WorkGroup); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

// athenaTagCfg tunes shared tag validation to Athena's error shape.
var athenaTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidRequestException",
	InvalidCode:     "InvalidRequestException",
	ExceededMessage: "Too many tags. A resource can hold a maximum of 50 tags.",
}

func (s *Service) tagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string      `json:"ResourceARN"`
		Tags        []athenaTag `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ResourceARN == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "InvalidRequestException", Message: "ResourceARN is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	wgName, aerr := workGroupNameFromARN(req.ResourceARN)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	wg, found := s.store.getWorkGroup(r.Context(), wgName)
	if !found {
		protocol.WriteJSONError(w, r, errWorkGroupNotFound(wgName))
		return
	}
	if wg.Tags == nil {
		wg.Tags = map[string]string{}
	}
	for _, t := range req.Tags {
		wg.Tags[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(athenaTagCfg, wg.Tags); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if err := s.store.putWorkGroup(r.Context(), wg); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) untagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ResourceARN == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "InvalidRequestException", Message: "ResourceARN is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	wgName, aerr := workGroupNameFromARN(req.ResourceARN)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	wg, found := s.store.getWorkGroup(r.Context(), wgName)
	if !found {
		protocol.WriteJSONError(w, r, errWorkGroupNotFound(wgName))
		return
	}
	if wg.Tags != nil {
		for _, k := range req.TagKeys {
			delete(wg.Tags, k)
		}
	}
	if err := s.store.putWorkGroup(r.Context(), wg); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) listTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ResourceARN == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "InvalidRequestException", Message: "ResourceARN is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	wgName, aerr := workGroupNameFromARN(req.ResourceARN)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	wg, found := s.store.getWorkGroup(r.Context(), wgName)
	if !found {
		protocol.WriteJSONError(w, r, errWorkGroupNotFound(wgName))
		return
	}
	tagList := tagsToList(wg.Tags)
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"Tags": tagList})
}

type athenaTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

func workGroupNameFromARN(arn string) (string, *protocol.AWSError) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return "", &protocol.AWSError{
			Code: "InvalidRequestException", Message: "Invalid ResourceARN format",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	resource := parts[5]
	if !strings.HasPrefix(resource, "workgroup/") {
		return "", &protocol.AWSError{
			Code: "InvalidRequestException", Message: "ResourceARN must be a workgroup ARN",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return strings.TrimPrefix(resource, "workgroup/"), nil
}

func tagsToList(tags map[string]string) []athenaTag {
	return serviceutil.TagElements(tags, func(k, v string) athenaTag {
		return athenaTag{Key: k, Value: v}
	})
}
