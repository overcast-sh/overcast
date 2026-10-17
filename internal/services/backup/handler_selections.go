package backup

// handler_selections.go — AWS Backup's four backup-selection operations
// (#2308), at the bindings the pinned model gives them:
//
//	PUT    /backup/plans/{BackupPlanId}/selections                  CreateBackupSelection
//	GET    /backup/plans/{BackupPlanId}/selections                  ListBackupSelections
//	GET    /backup/plans/{BackupPlanId}/selections/{SelectionId}    GetBackupSelection
//	DELETE /backup/plans/{BackupPlanId}/selections/{SelectionId}    DeleteBackupSelection
//
// A selection is a child of a plan: it is keyed under the plan's id, an
// unknown plan is ResourceNotFoundException on all four, and DeleteBackupPlan
// refuses while any remain (see deleteBackupPlan). Nothing here selects
// anything — no backup job runs, so Resources, NotResources, ListOfTags and
// Conditions are stored and echoed, never evaluated.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

const (
	pathSelectionsSuffix = "/selections"

	// conditionTypeStringEquals is the one member of the ConditionType enum.
	conditionTypeStringEquals = "STRINGEQUALS"
)

// selectionNameRule is BackupSelectionName's modeled pattern.
var selectionNameRule = regexp.MustCompile(`^[a-zA-Z0-9\-_.]{1,50}$`)

// selectionCondition is the model's Condition (a ListOfTags member): all
// three members are required.
type selectionCondition struct {
	ConditionKey   string `json:"ConditionKey"`
	ConditionType  string `json:"ConditionType"`
	ConditionValue string `json:"ConditionValue"`
}

// conditionParameter is the model's ConditionParameter (a member of each
// Conditions list). Neither member is required.
type conditionParameter struct {
	ConditionKey   string `json:"ConditionKey,omitempty"`
	ConditionValue string `json:"ConditionValue,omitempty"`
}

// selectionConditions is the model's Conditions.
type selectionConditions struct {
	StringEquals    []conditionParameter `json:"StringEquals,omitempty"`
	StringLike      []conditionParameter `json:"StringLike,omitempty"`
	StringNotEquals []conditionParameter `json:"StringNotEquals,omitempty"`
	StringNotLike   []conditionParameter `json:"StringNotLike,omitempty"`
}

// backupSelection is the model's BackupSelection, the document a client
// supplies and GetBackupSelection returns. It is persisted as it arrived.
type backupSelection struct {
	SelectionName string               `json:"SelectionName"`
	IamRoleArn    string               `json:"IamRoleArn"`
	Resources     []string             `json:"Resources,omitempty"`
	NotResources  []string             `json:"NotResources,omitempty"`
	ListOfTags    []selectionCondition `json:"ListOfTags,omitempty"`
	Conditions    *selectionConditions `json:"Conditions,omitempty"`
}

// selectionRecord is a backup selection as it is persisted.
type selectionRecord struct {
	BackupPlanID     string          `json:"BackupPlanId"`
	SelectionID      string          `json:"SelectionId"`
	CreatorRequestID string          `json:"CreatorRequestId,omitempty"`
	CreationDate     time.Time       `json:"CreationDate"`
	Selection        backupSelection `json:"Selection"`
}

type createBackupSelectionRequest struct {
	BackupSelection  *backupSelection `json:"BackupSelection"`
	CreatorRequestId string           `json:"CreatorRequestId"`
}

type createBackupSelectionResponse struct {
	BackupPlanId string  `json:"BackupPlanId"`
	CreationDate float64 `json:"CreationDate"`
	SelectionId  string  `json:"SelectionId"`
}

type getBackupSelectionResponse struct {
	BackupPlanId     string          `json:"BackupPlanId"`
	BackupSelection  backupSelection `json:"BackupSelection"`
	CreationDate     float64         `json:"CreationDate"`
	CreatorRequestId string          `json:"CreatorRequestId,omitempty"`
	SelectionId      string          `json:"SelectionId"`
}

// backupSelectionsListMember is AWS's BackupSelectionsListMember.
type backupSelectionsListMember struct {
	BackupPlanId     string  `json:"BackupPlanId"`
	CreationDate     float64 `json:"CreationDate"`
	CreatorRequestId string  `json:"CreatorRequestId,omitempty"`
	IamRoleArn       string  `json:"IamRoleArn"`
	SelectionId      string  `json:"SelectionId"`
	SelectionName    string  `json:"SelectionName"`
}

type listBackupSelectionsResponse struct {
	BackupSelectionsList []backupSelectionsListMember `json:"BackupSelectionsList"`
	NextToken            string                       `json:"NextToken,omitempty"`
}

// ─── Store ────────────────────────────────────────────────────

// Selections are keyed "<planId>/<selectionId>" under the region, so a plan's
// selections are one prefix scan and a selection cannot be read through a
// different plan.

func selectionKey(planID, selectionID string) string { return planID + "/" + selectionID }

func (s *backupStore) putSelection(ctx context.Context, region string, rec *selectionRecord) error {
	return putRecord(ctx, s, nsSelections, region, selectionKey(rec.BackupPlanID, rec.SelectionID), rec)
}

func (s *backupStore) getSelection(ctx context.Context, region, planID, selectionID string) (*selectionRecord, bool, error) {
	return getRecord[selectionRecord](ctx, s, nsSelections, "selection", region, selectionKey(planID, selectionID))
}

// listSelections returns every selection of one plan.
func (s *backupStore) listSelections(ctx context.Context, region, planID string) ([]selectionRecord, error) {
	pairs, err := s.store.Scan(ctx, nsSelections, serviceutil.RegionKey(region, planID+"/"))
	if err != nil {
		return nil, err
	}
	out := make([]selectionRecord, 0, len(pairs))
	for _, kv := range pairs {
		var rec selectionRecord
		if err := json.Unmarshal([]byte(kv.Value), &rec); err != nil {
			s.log.Warn("skipping undecodable record", zap.String("kind", "selection"), zap.String("key", kv.Key), zap.Error(err))
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *backupStore) deleteSelection(ctx context.Context, region, planID, selectionID string) error {
	return s.deleteRecord(ctx, nsSelections, region, selectionKey(planID, selectionID))
}

// ─── Validation ───────────────────────────────────────────────

// validateSelection checks a BackupSelection against the model: the required
// members, SelectionName's pattern, and ConditionType's enum. Every other
// constraint is one AWS enforces on resource ARNs and role existence, which
// Overcast has nothing to resolve them against.
func validateSelection(sel *backupSelection) *protocol.AWSError {
	if sel.SelectionName == "" {
		return missingParameterError("BackupSelection.SelectionName")
	}
	if sel.IamRoleArn == "" {
		return missingParameterError("BackupSelection.IamRoleArn")
	}
	if !selectionNameRule.MatchString(sel.SelectionName) {
		return invalidParameterError("Selection name must be 1 to 50 characters long and contain only letters, numbers, hyphens, underscores and periods.")
	}
	for i, c := range sel.ListOfTags {
		switch {
		case c.ConditionKey == "":
			return missingParameterError(fmt.Sprintf("BackupSelection.ListOfTags[%d].ConditionKey", i))
		case c.ConditionType == "":
			return missingParameterError(fmt.Sprintf("BackupSelection.ListOfTags[%d].ConditionType", i))
		case c.ConditionValue == "":
			return missingParameterError(fmt.Sprintf("BackupSelection.ListOfTags[%d].ConditionValue", i))
		case c.ConditionType != conditionTypeStringEquals:
			return invalidParameterError(fmt.Sprintf("ConditionType %q is not valid; the only supported value is %s.", c.ConditionType, conditionTypeStringEquals))
		}
	}
	return nil
}

// ─── Handlers ─────────────────────────────────────────────────

// requirePlan resolves the {BackupPlanId} a selection operation addresses,
// writing ResourceNotFoundException when there is no such plan in the region.
func (s *Service) requirePlan(w http.ResponseWriter, r *http.Request, operation, region string) (planID string, ok bool) {
	planID = chi.URLParam(r, "BackupPlanId")
	_, found, err := s.store.getPlan(r.Context(), region, planID)
	if err != nil {
		s.storeError(w, r, operation, err)
		return "", false
	}
	if !found {
		protocol.WriteJSONError(w, r, notFoundError("Backup plan", planID))
		return "", false
	}
	return planID, true
}

func (s *Service) createBackupSelection(w http.ResponseWriter, r *http.Request) {
	var req createBackupSelectionRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.BackupSelection == nil {
		protocol.WriteJSONError(w, r, missingParameterError("BackupSelection"))
		return
	}
	if aerr := validateSelection(req.BackupSelection); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	region := middleware.RegionFromContext(r.Context(), s.cfg.Region)

	// The idempotency check, the plan check and the write are one critical
	// section: without it two retries of one token both miss, and both write.
	s.selectionsMu.Lock()
	defer s.selectionsMu.Unlock()

	planID, ok := s.requirePlan(w, r, "CreateBackupSelection", region)
	if !ok {
		return
	}

	if req.CreatorRequestId != "" {
		existing, err := s.store.listSelections(r.Context(), region, planID)
		if err != nil {
			s.storeError(w, r, "CreateBackupSelection", err)
			return
		}
		for i := range existing {
			if existing[i].CreatorRequestID != req.CreatorRequestId {
				continue
			}
			if !reflect.DeepEqual(existing[i].Selection, *req.BackupSelection) {
				// A token names one request; replaying it with a different
				// document is the conflict AlreadyExistsException models.
				protocol.WriteJSONError(w, r, &protocol.AWSError{
					Code:       "AlreadyExistsException",
					Message:    fmt.Sprintf("A backup selection with CreatorRequestId %s already exists with different parameters.", req.CreatorRequestId),
					HTTPStatus: http.StatusBadRequest,
				})
				return
			}
			protocol.WriteJSON(w, r, http.StatusOK, selectionWriteResponse(&existing[i]))
			return
		}
	}

	rec := &selectionRecord{
		BackupPlanID:     planID,
		SelectionID:      uuid.NewString(),
		CreatorRequestID: req.CreatorRequestId,
		CreationDate:     s.clk.Now().UTC(),
		Selection:        *req.BackupSelection,
	}
	if err := s.store.putSelection(r.Context(), region, rec); err != nil {
		s.storeError(w, r, "CreateBackupSelection", err)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, selectionWriteResponse(rec))
}

func (s *Service) getBackupSelection(w http.ResponseWriter, r *http.Request) {
	region := middleware.RegionFromContext(r.Context(), s.cfg.Region)
	planID, ok := s.requirePlan(w, r, "GetBackupSelection", region)
	if !ok {
		return
	}
	selectionID := chi.URLParam(r, "SelectionId")

	rec, found, err := s.store.getSelection(r.Context(), region, planID, selectionID)
	if err != nil {
		s.storeError(w, r, "GetBackupSelection", err)
		return
	}
	if !found {
		protocol.WriteJSONError(w, r, notFoundError("Backup selection", selectionID))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, getBackupSelectionResponse{
		BackupPlanId:     rec.BackupPlanID,
		BackupSelection:  rec.Selection,
		CreationDate:     epochSeconds(rec.CreationDate),
		CreatorRequestId: rec.CreatorRequestID,
		SelectionId:      rec.SelectionID,
	})
}

func (s *Service) listBackupSelections(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, aerr := pageSize(query.Get("maxResults"), maxResultsCeiling)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	region := middleware.RegionFromContext(r.Context(), s.cfg.Region)
	planID, ok := s.requirePlan(w, r, "ListBackupSelections", region)
	if !ok {
		return
	}

	recs, err := s.store.listSelections(r.Context(), region, planID)
	if err != nil {
		s.storeError(w, r, "ListBackupSelections", err)
		return
	}
	// Name, then id, so a page boundary falls in the same place on every call
	// even when two selections share a name.
	sortByName(recs, func(rec selectionRecord) string { return rec.Selection.SelectionName + "\x00" + rec.SelectionID })

	out := make([]backupSelectionsListMember, 0, len(recs))
	for i := range recs {
		rec := &recs[i]
		out = append(out, backupSelectionsListMember{
			BackupPlanId:     rec.BackupPlanID,
			CreationDate:     epochSeconds(rec.CreationDate),
			CreatorRequestId: rec.CreatorRequestID,
			IamRoleArn:       rec.Selection.IamRoleArn,
			SelectionId:      rec.SelectionID,
			SelectionName:    rec.Selection.SelectionName,
		})
	}
	page, err := serviceutil.Paginate(out, limit, query.Get("nextToken"),
		serviceutil.PaginateOptions{DefaultLimit: maxResultsCeiling, MaxLimit: maxResultsCeiling})
	if err != nil {
		protocol.WriteJSONError(w, r, invalidParameterError("The specified continuation token is not valid."))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, listBackupSelectionsResponse{
		BackupSelectionsList: page.Items,
		NextToken:            page.NextToken,
	})
}

func (s *Service) deleteBackupSelection(w http.ResponseWriter, r *http.Request) {
	region := middleware.RegionFromContext(r.Context(), s.cfg.Region)
	planID, ok := s.requirePlan(w, r, "DeleteBackupSelection", region)
	if !ok {
		return
	}
	selectionID := chi.URLParam(r, "SelectionId")

	_, found, err := s.store.getSelection(r.Context(), region, planID, selectionID)
	if err != nil {
		s.storeError(w, r, "DeleteBackupSelection", err)
		return
	}
	if !found {
		protocol.WriteJSONError(w, r, notFoundError("Backup selection", selectionID))
		return
	}
	if err := s.store.deleteSelection(r.Context(), region, planID, selectionID); err != nil {
		s.storeError(w, r, "DeleteBackupSelection", err)
		return
	}
	// DeleteBackupSelection's modeled output is Unit.
	protocol.WriteEmpty(w, r, http.StatusOK)
}

func selectionWriteResponse(rec *selectionRecord) createBackupSelectionResponse {
	return createBackupSelectionResponse{
		BackupPlanId: rec.BackupPlanID,
		CreationDate: epochSeconds(rec.CreationDate),
		SelectionId:  rec.SelectionID,
	}
}
