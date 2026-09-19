package stepfunctions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// State machine versions: PublishStateMachineVersion, ListStateMachineVersions
// and DeleteStateMachineVersion, plus the ARN parsing and error shapes that
// versions, aliases and qualified-ARN executions share.
//
// https://docs.aws.amazon.com/step-functions/latest/dg/concepts-state-machine-version.html

const (
	// maxVersionsPerStateMachine is AWS's quota: "You can publish up to 1000
	// versions for each state machine" (the per-state-machine version quota).
	maxVersionsPerStateMachine = 1000
	// initialRevisionID is how PublishStateMachineVersion's revisionId names
	// the revision of a state machine that has never been updated.
	initialRevisionID = "INITIAL"
	// maxDescriptionLength bounds every version and alias description.
	maxDescriptionLength = 256
	// listDefaultPageSize and listMaxPageSize are ListStateMachines',
	// ListStateMachineVersions' and ListStateMachineAliases' documented
	// maxResults default and ceiling.
	listDefaultPageSize = 100
	listMaxPageSize     = 1000
)

// ─── Qualified ARNs ───────────────────────────────────────────────────────────

// smARN is a state machine ARN split into its parts. A qualified ARN names a
// version (`…:stateMachine:name:3`) or an alias (`…:stateMachine:name:PROD`).
type smARN struct {
	base      string // the unqualified state machine ARN
	name      string
	qualifier string // "" for an unqualified ARN
	version   int    // > 0 when qualifier is a version number
}

func (a smARN) isVersion() bool { return a.version > 0 }
func (a smARN) isAlias() bool   { return a.qualifier != "" && a.version == 0 }

// parseSMARN splits arn:partition:states:region:account:stateMachine:name[:qualifier].
// ok is false for anything that is not a state machine ARN.
func parseSMARN(arn string) (smARN, bool) {
	parts := strings.Split(arn, ":")
	if len(parts) < 7 || len(parts) > 8 || parts[0] != "arn" || parts[2] != "states" || parts[5] != "stateMachine" || parts[6] == "" {
		return smARN{}, false
	}
	out := smARN{base: strings.Join(parts[:7], ":"), name: parts[6]}
	if len(parts) == 8 {
		if parts[7] == "" {
			return smARN{}, false
		}
		out.qualifier = parts[7]
		if n, err := strconv.Atoi(parts[7]); err == nil {
			if n <= 0 {
				return smARN{}, false
			}
			out.version = n
		}
	}
	return out, true
}

// ─── Errors ───────────────────────────────────────────────────────────────────

func errInvalidArn(arn string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArn",
		Message:    fmt.Sprintf("Invalid Arn: 'Resource type not valid in this context: %s'", arn),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errResourceNotFound(arn string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFound",
		Message:    fmt.Sprintf("Resource not found: '%s'", arn),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errConflict(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "ConflictException", Message: message, HTTPStatus: http.StatusConflict}
}

func errValidation(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "ValidationException", Message: message, HTTPStatus: http.StatusBadRequest}
}

func errServiceQuota(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "ServiceQuotaExceededException", Message: message, HTTPStatus: http.StatusBadRequest}
}

func errInvalidToken() *protocol.AWSError {
	return &protocol.AWSError{Code: "InvalidToken", Message: "Invalid Token: 'the pagination token is not valid'", HTTPStatus: http.StatusBadRequest}
}

// paginate applies the maxResults/nextToken contract every Step Functions
// list operation shares: 0 means the default page of 100, the ceiling is
// 1000, and an undecodable token is InvalidToken.
func paginate[T any](items []T, maxResults int, nextToken string) (serviceutil.Page[T], *protocol.AWSError) {
	if maxResults < 0 || maxResults > listMaxPageSize {
		return serviceutil.Page[T]{}, errValidation(fmt.Sprintf(
			"1 validation error detected: Value '%d' at 'maxResults' failed to satisfy constraint: Member must have value less than or equal to %d",
			maxResults, listMaxPageSize))
	}
	page, err := serviceutil.Paginate(items, maxResults, nextToken, serviceutil.PaginateOptions{
		DefaultLimit: listDefaultPageSize,
		MaxLimit:     listMaxPageSize,
	})
	if errors.Is(err, serviceutil.ErrInvalidPageToken) {
		return page, errInvalidToken()
	}
	return page, nil
}

func validateDescription(field, description string) *protocol.AWSError {
	if len(description) > maxDescriptionLength {
		return errValidation(fmt.Sprintf(
			"1 validation error detected: Value at '%s' failed to satisfy constraint: Member must have length less than or equal to %d",
			field, maxDescriptionLength))
	}
	return nil
}

// errVersionDescriptionWithoutPublish is CreateStateMachine's and
// UpdateStateMachine's answer to a versionDescription with publish unset.
func errVersionDescriptionWithoutPublish() *protocol.AWSError {
	return errValidation("Version description can only be set when publish is true")
}

// ─── Lookups ──────────────────────────────────────────────────────────────────

// getBaseStateMachine loads the state machine an ARN (qualified or not)
// belongs to, or StateMachineDoesNotExist naming the ARN as given.
func (h *Handler) getBaseStateMachine(ctx context.Context, arn smARN, given string) (*StateMachine, *protocol.AWSError) {
	sm, err := h.store.GetStateMachine(ctx, arn.name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if sm == nil {
		return nil, errSMNotFound(given)
	}
	return sm, nil
}

// getVersion loads the version a version ARN names. nil, nil when the state
// machine or the version does not exist.
func (h *Handler) getVersion(ctx context.Context, arn smARN) (*StateMachineVersion, *protocol.AWSError) {
	v, err := h.store.GetVersion(ctx, arn.name, arn.version)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return v, nil
}

// withVersion returns a copy of sm carrying a version's snapshot of the
// definition, role and configuration — what an execution of that version
// runs. The state machine's own ARN and name are kept: executions of a
// version belong to the base state machine.
func withVersion(sm *StateMachine, v *StateMachineVersion) *StateMachine {
	out := *sm
	out.Definition = v.Definition
	out.RoleArn = v.RoleArn
	out.LoggingConfiguration = v.LoggingConfiguration
	out.TracingConfiguration = v.TracingConfiguration
	out.RevisionID = v.RevisionID
	return &out
}

// ─── Publishing ───────────────────────────────────────────────────────────────

// publishVersion snapshots sm's current revision as a new version, or returns
// the version already published for that revision — PublishStateMachineVersion
// is idempotent on the revision. sm is persisted with its advanced version
// counter.
func (h *Handler) publishVersion(ctx context.Context, sm *StateMachine, description string) (*StateMachineVersion, *protocol.AWSError) {
	versions, err := h.store.ListVersions(ctx, sm.Name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, v := range versions {
		if v.RevisionID == sm.RevisionID {
			return v, nil
		}
	}
	if len(versions) >= maxVersionsPerStateMachine {
		return nil, errServiceQuota(fmt.Sprintf(
			"The state machine '%s' already has the maximum of %d versions; delete unused versions first", sm.ARN, maxVersionsPerStateMachine))
	}
	sm.LastVersion++
	v := &StateMachineVersion{
		StateMachineName:     sm.Name,
		ARN:                  sm.ARN + ":" + strconv.Itoa(sm.LastVersion),
		Version:              sm.LastVersion,
		Definition:           sm.Definition,
		RoleArn:              sm.RoleArn,
		Type:                 sm.Type,
		Description:          description,
		RevisionID:           sm.RevisionID,
		LoggingConfiguration: sm.LoggingConfiguration,
		TracingConfiguration: sm.TracingConfiguration,
		CreatedAt:            h.clk.Now(),
	}
	// The counter is persisted first: a failure between the two writes then
	// skips a number rather than ever reusing one.
	if err := h.store.PutStateMachine(ctx, sm); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := h.store.PutVersion(ctx, v); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return v, nil
}

// ─── PublishStateMachineVersion ───────────────────────────────────────────────

type publishStateMachineVersionRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
	RevisionID      string `json:"revisionId" cbor:"revisionId"`
	Description     string `json:"description" cbor:"description"`
}

type publishStateMachineVersionResponse struct {
	CreationDate           float64 `json:"creationDate" cbor:"creationDate"`
	StateMachineVersionArn string  `json:"stateMachineVersionArn" cbor:"stateMachineVersionArn"`
}

func (h *Handler) publishStateMachineVersionTyped(ctx context.Context, req *publishStateMachineVersionRequest) (*publishStateMachineVersionResponse, *protocol.AWSError) {
	if aerr := validateDescription("description", req.Description); aerr != nil {
		return nil, aerr
	}
	arn, ok := parseSMARN(req.StateMachineArn)
	if !ok || arn.qualifier != "" {
		return nil, errInvalidArn(req.StateMachineArn)
	}
	sm, aerr := h.getBaseStateMachine(ctx, arn, req.StateMachineArn)
	if aerr != nil {
		return nil, aerr
	}
	if req.RevisionID != "" {
		current := sm.RevisionID
		if current == "" {
			current = initialRevisionID
		}
		if req.RevisionID != current {
			return nil, errConflict(fmt.Sprintf(
				"Failed to publish the State Machine version for revision %s. The current State Machine revision is %s.",
				req.RevisionID, current))
		}
	}
	v, aerr := h.publishVersion(ctx, sm, req.Description)
	if aerr != nil {
		return nil, aerr
	}
	return &publishStateMachineVersionResponse{
		CreationDate:           epochSeconds(v.CreatedAt),
		StateMachineVersionArn: v.ARN,
	}, nil
}

// ─── ListStateMachineVersions ─────────────────────────────────────────────────

type listStateMachineVersionsRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
	MaxResults      int    `json:"maxResults" cbor:"maxResults"`
	NextToken       string `json:"nextToken" cbor:"nextToken"`
}

type stateMachineVersionListItem struct {
	StateMachineVersionArn string  `json:"stateMachineVersionArn" cbor:"stateMachineVersionArn"`
	CreationDate           float64 `json:"creationDate" cbor:"creationDate"`
}

type listStateMachineVersionsResponse struct {
	StateMachineVersions []stateMachineVersionListItem `json:"stateMachineVersions" cbor:"stateMachineVersions"`
	NextToken            string                        `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

func (h *Handler) listStateMachineVersionsTyped(ctx context.Context, req *listStateMachineVersionsRequest) (*listStateMachineVersionsResponse, *protocol.AWSError) {
	arn, ok := parseSMARN(req.StateMachineArn)
	if !ok || arn.qualifier != "" {
		return nil, errInvalidArn(req.StateMachineArn)
	}
	if _, aerr := h.getBaseStateMachine(ctx, arn, req.StateMachineArn); aerr != nil {
		return nil, aerr
	}
	versions, err := h.store.ListVersions(ctx, arn.name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	page, aerr := paginate(versions, req.MaxResults, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	items := make([]stateMachineVersionListItem, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, stateMachineVersionListItem{StateMachineVersionArn: v.ARN, CreationDate: epochSeconds(v.CreatedAt)})
	}
	return &listStateMachineVersionsResponse{StateMachineVersions: items, NextToken: page.NextToken}, nil
}

// ─── DeleteStateMachineVersion ────────────────────────────────────────────────

type deleteStateMachineVersionRequest struct {
	StateMachineVersionArn string `json:"stateMachineVersionArn" cbor:"stateMachineVersionArn"`
}

// deleteStateMachineVersionTyped deletes a version. Deleting one that does not
// exist succeeds — the operation declares no not-found error — but a version
// an alias still routes to is refused, as on AWS. Executions already running
// on the version are unaffected.
func (h *Handler) deleteStateMachineVersionTyped(ctx context.Context, req *deleteStateMachineVersionRequest) (*struct{}, *protocol.AWSError) {
	arn, ok := parseSMARN(req.StateMachineVersionArn)
	if !ok || !arn.isVersion() {
		return nil, errInvalidArn(req.StateMachineVersionArn)
	}
	aliases, err := h.store.ListAliases(ctx, arn.name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	var referencing []string
	for _, a := range aliases {
		if aliasRoutesTo(a, req.StateMachineVersionArn) {
			referencing = append(referencing, a.ARN)
		}
	}
	if len(referencing) > 0 {
		return nil, errConflict(fmt.Sprintf(
			"Version to be deleted must not be referenced by an alias. Current list of aliases referencing this version: [%s]",
			strings.Join(referencing, ", ")))
	}
	if err := h.store.DeleteVersion(ctx, arn.name, arn.version); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &struct{}{}, nil
}

// deleteVersionsAndAliases removes everything published under a state
// machine; DeleteStateMachine calls it so no version or alias outlives the
// state machine it belongs to.
func (h *Handler) deleteVersionsAndAliases(ctx context.Context, smName string) error {
	aliases, err := h.store.ListAliases(ctx, smName)
	if err != nil {
		return err
	}
	for _, a := range aliases {
		if err := h.store.DeleteAlias(ctx, smName, a.Name); err != nil {
			return err
		}
	}
	versions, err := h.store.ListVersions(ctx, smName)
	if err != nil {
		return err
	}
	for _, v := range versions {
		if err := h.store.DeleteVersion(ctx, smName, v.Version); err != nil {
			return err
		}
	}
	return nil
}

// describeVersion renders DescribeStateMachine for a version ARN: the
// version's own snapshot, its description and revision, and its ARN.
func (h *Handler) describeVersion(ctx context.Context, arn smARN, given string) (*describeStateMachineResponse, *protocol.AWSError) {
	sm, aerr := h.getBaseStateMachine(ctx, arn, given)
	if aerr != nil {
		return nil, aerr
	}
	v, aerr := h.getVersion(ctx, arn)
	if aerr != nil {
		return nil, aerr
	}
	if v == nil {
		return nil, errSMNotFound(given)
	}
	return &describeStateMachineResponse{
		StateMachineArn:      v.ARN,
		Name:                 sm.Name,
		Definition:           v.Definition,
		RoleArn:              v.RoleArn,
		Type:                 v.Type,
		Status:               sm.Status,
		CreationDate:         epochSeconds(v.CreatedAt),
		LoggingConfiguration: sfnLoggingConfigOrDefault(v.LoggingConfiguration),
		TracingConfiguration: sfnTracingConfigOrDefault(v.TracingConfiguration),
		RevisionID:           v.RevisionID,
		Description:          v.Description,
	}, nil
}
