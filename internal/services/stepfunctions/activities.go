package stepfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Activities: a Task whose Resource is an activity ARN is handed to whatever
// worker polls GetActivityTask for it, and completes when that worker calls
// SendTaskSuccess or SendTaskFailure with the task token it was given.

const actPrefix = "act:"

// Activity is a Step Functions activity.
type Activity struct {
	Name                    string            `json:"Name"`
	ARN                     string            `json:"ARN"`
	CreatedAt               time.Time         `json:"CreatedAt"`
	Tags                    map[string]string `json:"Tags,omitempty"`
	EncryptionConfiguration map[string]any    `json:"EncryptionConfiguration,omitempty"`
}

// GetTags satisfies serviceutil.Taggable.
func (a *Activity) GetTags() map[string]string { return a.Tags }

// SetTags satisfies serviceutil.Taggable.
func (a *Activity) SetTags(t map[string]string) { a.Tags = t }

// ─── Store ────────────────────────────────────────────────────────────────────

// GetActivity returns an activity by name, or nil, nil when there is none.
func (st *Store) GetActivity(ctx context.Context, name string) (*Activity, error) {
	raw, found, err := st.s.Get(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), actPrefix+name))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: get activity %q: %w", name, err)
	}
	if !found {
		return nil, nil
	}
	var act Activity
	if err := json.Unmarshal([]byte(raw), &act); err != nil {
		// A record that cannot be decoded reads as absent rather than failing
		// every call that names it.
		return nil, nil
	}
	return &act, nil
}

// PutActivity saves an activity.
func (st *Store) PutActivity(ctx context.Context, act *Activity) error {
	raw, err := json.Marshal(act)
	if err != nil {
		return fmt.Errorf("stepfunctions: marshal activity %q: %w", act.Name, err)
	}
	return st.s.Set(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), actPrefix+act.Name), string(raw))
}

// DeleteActivity removes an activity.
func (st *Store) DeleteActivity(ctx context.Context, name string) error {
	return st.s.Delete(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), actPrefix+name))
}

// ListActivities returns every activity in the region, oldest first. Records
// that cannot be decoded are skipped.
func (st *Store) ListActivities(ctx context.Context) ([]*Activity, error) {
	pairs, err := st.s.Scan(ctx, storeNS, serviceutil.RegionKey(st.region(ctx), actPrefix))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: scan activities: %w", err)
	}
	out := make([]*Activity, 0, len(pairs))
	for _, p := range pairs {
		var act Activity
		if json.Unmarshal([]byte(p.Value), &act) != nil {
			continue
		}
		out = append(out, &act)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ─── ARNs ─────────────────────────────────────────────────────────────────────

// isActivityARN reports whether an ARN names an activity.
func isActivityARN(arn string) bool {
	parts := strings.SplitN(arn, ":", 7)
	return len(parts) == 7 && parts[2] == "states" && parts[5] == "activity"
}

// activityNameFromARN returns the name segment of an activity ARN.
func activityNameFromARN(arn string) string {
	parts := strings.SplitN(arn, ":", 7)
	if len(parts) == 7 {
		return parts[6]
	}
	return arn
}

// resourceNamePattern is AWS's rule for state machine and activity names: 1–80
// characters, none of them whitespace, brackets, wildcards or the other
// characters AWS reserves.
var resourceNamePattern = regexp.MustCompile("^[^\\s<>{}\\[\\]?*\"#%\\\\^|~`$&,;:/\\x00-\\x1f\\x7f-\\x9f]{1,80}$")

func errActivityDoesNotExist(arn string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ActivityDoesNotExist",
		Message:    fmt.Sprintf("Activity Does Not Exist: '%s'", arn),
		HTTPStatus: http.StatusBadRequest,
	}
}

// loadActivity resolves an activity ARN to its record.
func (h *Handler) loadActivity(ctx context.Context, arn string) (*Activity, *protocol.AWSError) {
	if !isActivityARN(arn) {
		return nil, errInvalidArn(arn)
	}
	act, err := h.store.GetActivity(ctx, activityNameFromARN(arn))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if act == nil || act.ARN != arn {
		return nil, errActivityDoesNotExist(arn)
	}
	return act, nil
}

// ─── Operations ───────────────────────────────────────────────────────────────

type createActivityRequest struct {
	Name                    string         `json:"name" cbor:"name"`
	Tags                    []sfnTag       `json:"tags" cbor:"tags"`
	EncryptionConfiguration map[string]any `json:"encryptionConfiguration" cbor:"encryptionConfiguration"`
}

type createActivityResponse struct {
	ActivityArn  string  `json:"activityArn" cbor:"activityArn"`
	CreationDate float64 `json:"creationDate" cbor:"creationDate"`
}

// createActivityTyped implements CreateActivity. It is idempotent on the name,
// as AWS documents: a second create returns the existing activity and ignores
// differing tags.
func (h *Handler) createActivityTyped(ctx context.Context, req *createActivityRequest) (*createActivityResponse, *protocol.AWSError) {
	if !resourceNamePattern.MatchString(req.Name) {
		return nil, &protocol.AWSError{
			Code:       "InvalidName",
			Message:    fmt.Sprintf("Invalid Name: '%s'", req.Name),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	existing, err := h.store.GetActivity(ctx, req.Name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		return &createActivityResponse{ActivityArn: existing.ARN, CreationDate: epochSeconds(existing.CreatedAt)}, nil
	}
	tags := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		tags[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(sfnTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	act := &Activity{
		Name:                    req.Name,
		ARN:                     protocol.ARN(region, h.cfg.AccountID, "states", "activity:"+req.Name),
		CreatedAt:               h.clk.Now(),
		Tags:                    tags,
		EncryptionConfiguration: req.EncryptionConfiguration,
	}
	if err := h.store.PutActivity(ctx, act); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &createActivityResponse{ActivityArn: act.ARN, CreationDate: epochSeconds(act.CreatedAt)}, nil
}

type activityArnRequest struct {
	ActivityArn string `json:"activityArn" cbor:"activityArn"`
}

type describeActivityResponse struct {
	ActivityArn             string         `json:"activityArn" cbor:"activityArn"`
	Name                    string         `json:"name" cbor:"name"`
	CreationDate            float64        `json:"creationDate" cbor:"creationDate"`
	EncryptionConfiguration map[string]any `json:"encryptionConfiguration,omitempty" cbor:"encryptionConfiguration,omitempty"`
}

func (h *Handler) describeActivityTyped(ctx context.Context, req *activityArnRequest) (*describeActivityResponse, *protocol.AWSError) {
	act, aerr := h.loadActivity(ctx, req.ActivityArn)
	if aerr != nil {
		return nil, aerr
	}
	return &describeActivityResponse{
		ActivityArn:             act.ARN,
		Name:                    act.Name,
		CreationDate:            epochSeconds(act.CreatedAt),
		EncryptionConfiguration: act.EncryptionConfiguration,
	}, nil
}

// deleteActivityTyped implements DeleteActivity. Deleting an activity that
// does not exist succeeds, as on AWS.
func (h *Handler) deleteActivityTyped(ctx context.Context, req *activityArnRequest) (*struct{}, *protocol.AWSError) {
	if !isActivityARN(req.ActivityArn) {
		return nil, errInvalidArn(req.ActivityArn)
	}
	if err := h.store.DeleteActivity(ctx, activityNameFromARN(req.ActivityArn)); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &struct{}{}, nil
}

type listActivitiesRequest struct {
	MaxResults int    `json:"maxResults" cbor:"maxResults"`
	NextToken  string `json:"nextToken" cbor:"nextToken"`
}

type activityListItem struct {
	ActivityArn  string  `json:"activityArn" cbor:"activityArn"`
	Name         string  `json:"name" cbor:"name"`
	CreationDate float64 `json:"creationDate" cbor:"creationDate"`
}

type listActivitiesResponse struct {
	Activities []activityListItem `json:"activities" cbor:"activities"`
	NextToken  string             `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

func (h *Handler) listActivitiesTyped(ctx context.Context, req *listActivitiesRequest) (*listActivitiesResponse, *protocol.AWSError) {
	acts, err := h.store.ListActivities(ctx)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	page, pageErr := paginate(acts, req.MaxResults, req.NextToken)
	if pageErr != nil {
		return nil, pageErr
	}
	items := make([]activityListItem, 0, len(page.Items))
	for _, act := range page.Items {
		items = append(items, activityListItem{ActivityArn: act.ARN, Name: act.Name, CreationDate: epochSeconds(act.CreatedAt)})
	}
	return &listActivitiesResponse{Activities: items, NextToken: page.NextToken}, nil
}

type getActivityTaskRequest struct {
	ActivityArn string `json:"activityArn" cbor:"activityArn"`
	WorkerName  string `json:"workerName" cbor:"workerName"`
}

type getActivityTaskResponse struct {
	TaskToken string `json:"taskToken,omitempty" cbor:"taskToken,omitempty"`
	Input     string `json:"input,omitempty" cbor:"input,omitempty"`
}

// activityPollTimeout is how long GetActivityTask long-polls, as on AWS.
const activityPollTimeout = 60 * time.Second

// getActivityTaskTyped implements GetActivityTask: it hands the oldest
// scheduled task for the activity to this worker, waiting up to 60 seconds
// for one. An empty response means no work arrived in that time.
func (h *Handler) getActivityTaskTyped(ctx context.Context, req *getActivityTaskRequest) (*getActivityTaskResponse, *protocol.AWSError) {
	act, aerr := h.loadActivity(ctx, req.ActivityArn)
	if aerr != nil {
		return nil, aerr
	}
	timer := h.clk.Timer(activityPollTimeout)
	defer timer.Stop()
	task := h.tasks.poll(ctx, act.ARN, timer.C)
	if task == nil {
		return &getActivityTaskResponse{}, nil
	}
	task.pickUp(req.WorkerName)
	return &getActivityTaskResponse{TaskToken: task.token, Input: task.input}, nil
}
