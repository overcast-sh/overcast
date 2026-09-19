package stepfunctions

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// State machine aliases: Create/Describe/Update/Delete/ListStateMachineAliases.
// An alias routes StartExecution to one or two versions of the same state
// machine by weight; see resolveExecutionTarget (execution_target.go) for the
// routing itself.
//
// https://docs.aws.amazon.com/step-functions/latest/dg/concepts-state-machine-alias.html

const (
	// maxAliasesPerStateMachine is AWS's quota: "You can create up to 100
	// aliases for each state machine" (the per-state-machine alias quota).
	maxAliasesPerStateMachine = 100
	maxAliasNameLength        = 80
	maxRoutingEntries         = 2
	totalRoutingWeight        = 100
)

// routingConfigItem is AWS's RoutingConfigurationListItem.
type routingConfigItem struct {
	StateMachineVersionArn string `json:"stateMachineVersionArn" cbor:"stateMachineVersionArn"`
	Weight                 int    `json:"weight" cbor:"weight"`
}

// ─── Validation ───────────────────────────────────────────────────────────────

// validAliasName enforces AWS's pattern ^(?=.*[a-zA-Z_\-\.])[a-zA-Z0-9_\-\.]+$
// — letters, digits, '_', '-' and '.', with at least one non-digit so an alias
// name can never be mistaken for a version number.
func validAliasName(name string) bool {
	if name == "" || len(name) > maxAliasNameLength {
		return false
	}
	nonDigit := false
	for _, r := range name {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '-', r == '.':
			nonDigit = true
		default:
			return false
		}
	}
	return nonDigit
}

func errInvalidAliasName(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidName",
		Message:    fmt.Sprintf("Invalid Name: '%s'", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

// validateRouting checks a routingConfiguration against AWS's rules — one or
// two distinct versions of one state machine, weights 0–100 summing to 100,
// every version published — and returns the state machine it routes for.
func (h *Handler) validateRouting(ctx context.Context, routes []routingConfigItem) (*StateMachine, *protocol.AWSError) {
	if len(routes) == 0 || len(routes) > maxRoutingEntries {
		return nil, errValidation(fmt.Sprintf(
			"1 validation error detected: Value at 'routingConfiguration' failed to satisfy constraint: Member must have length between 1 and %d",
			maxRoutingEntries))
	}
	sum := 0
	var smName string
	seen := make(map[string]bool, len(routes))
	parsed := make([]smARN, 0, len(routes))
	for _, route := range routes {
		if route.Weight < 0 || route.Weight > totalRoutingWeight {
			return nil, errValidation(fmt.Sprintf(
				"1 validation error detected: Value '%d' at 'routingConfiguration.member.weight' failed to satisfy constraint: Member must have value between 0 and %d",
				route.Weight, totalRoutingWeight))
		}
		sum += route.Weight
		arn, ok := parseSMARN(route.StateMachineVersionArn)
		if !ok || !arn.isVersion() {
			return nil, errValidation(fmt.Sprintf("Routing configuration must reference state machine versions: '%s'", route.StateMachineVersionArn))
		}
		if smName != "" && arn.name != smName {
			return nil, errValidation("Routing configuration can only reference versions of the same state machine")
		}
		smName = arn.name
		if seen[route.StateMachineVersionArn] {
			return nil, errValidation("Routing configuration must not reference the same state machine version twice")
		}
		seen[route.StateMachineVersionArn] = true
		parsed = append(parsed, arn)
	}
	if sum != totalRoutingWeight {
		return nil, errValidation(fmt.Sprintf("Sum of routing configuration weights must equal %d, got %d", totalRoutingWeight, sum))
	}
	sm, err := h.store.GetStateMachine(ctx, smName)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if sm == nil {
		return nil, errResourceNotFound(parsed[0].base)
	}
	for i, arn := range parsed {
		v, aerr := h.getVersion(ctx, arn)
		if aerr != nil {
			return nil, aerr
		}
		if v == nil {
			return nil, errResourceNotFound(routes[i].StateMachineVersionArn)
		}
	}
	return sm, nil
}

func routeRecords(routes []routingConfigItem) []aliasRouteRecord {
	out := make([]aliasRouteRecord, 0, len(routes))
	for _, r := range routes {
		out = append(out, aliasRouteRecord(r))
	}
	return out
}

func routeItems(records []aliasRouteRecord) []routingConfigItem {
	out := make([]routingConfigItem, 0, len(records))
	for _, r := range records {
		out = append(out, routingConfigItem(r))
	}
	return out
}

func aliasRoutesTo(a *StateMachineAlias, versionARN string) bool {
	return slices.ContainsFunc(a.RoutingConfiguration, func(r aliasRouteRecord) bool {
		return r.StateMachineVersionArn == versionARN
	})
}

// getAlias loads the alias an alias ARN names, or ResourceNotFound.
func (h *Handler) getAlias(ctx context.Context, given string) (*StateMachineAlias, *protocol.AWSError) {
	arn, ok := parseSMARN(given)
	if !ok || !arn.isAlias() {
		return nil, errInvalidArn(given)
	}
	a, err := h.store.GetAlias(ctx, arn.name, arn.qualifier)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if a == nil {
		return nil, errResourceNotFound(given)
	}
	return a, nil
}

// ─── CreateStateMachineAlias ──────────────────────────────────────────────────

type createStateMachineAliasRequest struct {
	Name                 string              `json:"name" cbor:"name"`
	Description          string              `json:"description" cbor:"description"`
	RoutingConfiguration []routingConfigItem `json:"routingConfiguration" cbor:"routingConfiguration"`
}

type createStateMachineAliasResponse struct {
	CreationDate         float64 `json:"creationDate" cbor:"creationDate"`
	StateMachineAliasArn string  `json:"stateMachineAliasArn" cbor:"stateMachineAliasArn"`
}

// createStateMachineAliasTyped creates an alias. It is idempotent on the
// name, description and routing configuration: repeating an identical request
// returns the existing alias, while reusing the name with a different
// configuration is a ConflictException.
func (h *Handler) createStateMachineAliasTyped(ctx context.Context, req *createStateMachineAliasRequest) (*createStateMachineAliasResponse, *protocol.AWSError) {
	if aerr := validateDescription("description", req.Description); aerr != nil {
		return nil, aerr
	}
	if !validAliasName(req.Name) {
		return nil, errInvalidAliasName(req.Name)
	}
	sm, aerr := h.validateRouting(ctx, req.RoutingConfiguration)
	if aerr != nil {
		return nil, aerr
	}
	existing, err := h.store.GetAlias(ctx, sm.Name, req.Name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		if existing.Description == req.Description && slices.Equal(existing.RoutingConfiguration, routeRecords(req.RoutingConfiguration)) {
			return &createStateMachineAliasResponse{CreationDate: epochSeconds(existing.CreatedAt), StateMachineAliasArn: existing.ARN}, nil
		}
		return nil, errConflict(fmt.Sprintf("Failed to create alias because an alias with the same name and a different routing configuration already exists: '%s'", existing.ARN))
	}
	aliases, err := h.store.ListAliases(ctx, sm.Name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if len(aliases) >= maxAliasesPerStateMachine {
		return nil, errServiceQuota(fmt.Sprintf(
			"The state machine '%s' already has the maximum of %d aliases; delete unused aliases first", sm.ARN, maxAliasesPerStateMachine))
	}
	now := h.clk.Now()
	a := &StateMachineAlias{
		StateMachineName:     sm.Name,
		Name:                 req.Name,
		ARN:                  sm.ARN + ":" + req.Name,
		Description:          req.Description,
		RoutingConfiguration: routeRecords(req.RoutingConfiguration),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := h.store.PutAlias(ctx, a); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &createStateMachineAliasResponse{CreationDate: epochSeconds(now), StateMachineAliasArn: a.ARN}, nil
}

// ─── DescribeStateMachineAlias ────────────────────────────────────────────────

type describeStateMachineAliasRequest struct {
	StateMachineAliasArn string `json:"stateMachineAliasArn" cbor:"stateMachineAliasArn"`
}

type describeStateMachineAliasResponse struct {
	StateMachineAliasArn string              `json:"stateMachineAliasArn" cbor:"stateMachineAliasArn"`
	Name                 string              `json:"name" cbor:"name"`
	Description          string              `json:"description,omitempty" cbor:"description,omitempty"`
	RoutingConfiguration []routingConfigItem `json:"routingConfiguration" cbor:"routingConfiguration"`
	CreationDate         float64             `json:"creationDate" cbor:"creationDate"`
	UpdateDate           float64             `json:"updateDate" cbor:"updateDate"`
}

func (h *Handler) describeStateMachineAliasTyped(ctx context.Context, req *describeStateMachineAliasRequest) (*describeStateMachineAliasResponse, *protocol.AWSError) {
	a, aerr := h.getAlias(ctx, req.StateMachineAliasArn)
	if aerr != nil {
		return nil, aerr
	}
	return &describeStateMachineAliasResponse{
		StateMachineAliasArn: a.ARN,
		Name:                 a.Name,
		Description:          a.Description,
		RoutingConfiguration: routeItems(a.RoutingConfiguration),
		CreationDate:         epochSeconds(a.CreatedAt),
		UpdateDate:           epochSeconds(a.UpdatedAt),
	}, nil
}

// ─── UpdateStateMachineAlias ──────────────────────────────────────────────────

type updateStateMachineAliasRequest struct {
	StateMachineAliasArn string              `json:"stateMachineAliasArn" cbor:"stateMachineAliasArn"`
	Description          *string             `json:"description" cbor:"description"`
	RoutingConfiguration []routingConfigItem `json:"routingConfiguration" cbor:"routingConfiguration"`
}

type updateStateMachineAliasResponse struct {
	UpdateDate float64 `json:"updateDate" cbor:"updateDate"`
}

func (h *Handler) updateStateMachineAliasTyped(ctx context.Context, req *updateStateMachineAliasRequest) (*updateStateMachineAliasResponse, *protocol.AWSError) {
	if req.Description == nil && req.RoutingConfiguration == nil {
		return nil, errValidation("Either description or routingConfiguration must be specified")
	}
	a, aerr := h.getAlias(ctx, req.StateMachineAliasArn)
	if aerr != nil {
		return nil, aerr
	}
	if req.Description != nil {
		if aerr := validateDescription("description", *req.Description); aerr != nil {
			return nil, aerr
		}
		a.Description = *req.Description
	}
	if req.RoutingConfiguration != nil {
		sm, aerr := h.validateRouting(ctx, req.RoutingConfiguration)
		if aerr != nil {
			return nil, aerr
		}
		if sm.Name != a.StateMachineName {
			return nil, errValidation("Routing configuration can only reference versions of the alias's own state machine")
		}
		a.RoutingConfiguration = routeRecords(req.RoutingConfiguration)
	}
	a.UpdatedAt = h.clk.Now()
	if err := h.store.PutAlias(ctx, a); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &updateStateMachineAliasResponse{UpdateDate: epochSeconds(a.UpdatedAt)}, nil
}

// ─── DeleteStateMachineAlias ──────────────────────────────────────────────────

type deleteStateMachineAliasRequest struct {
	StateMachineAliasArn string `json:"stateMachineAliasArn" cbor:"stateMachineAliasArn"`
}

func (h *Handler) deleteStateMachineAliasTyped(ctx context.Context, req *deleteStateMachineAliasRequest) (*struct{}, *protocol.AWSError) {
	a, aerr := h.getAlias(ctx, req.StateMachineAliasArn)
	if aerr != nil {
		return nil, aerr
	}
	if err := h.store.DeleteAlias(ctx, a.StateMachineName, a.Name); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &struct{}{}, nil
}

// ─── ListStateMachineAliases ──────────────────────────────────────────────────

type listStateMachineAliasesRequest struct {
	StateMachineArn string `json:"stateMachineArn" cbor:"stateMachineArn"`
	MaxResults      int    `json:"maxResults" cbor:"maxResults"`
	NextToken       string `json:"nextToken" cbor:"nextToken"`
}

type stateMachineAliasListItem struct {
	StateMachineAliasArn string  `json:"stateMachineAliasArn" cbor:"stateMachineAliasArn"`
	CreationDate         float64 `json:"creationDate" cbor:"creationDate"`
}

type listStateMachineAliasesResponse struct {
	StateMachineAliases []stateMachineAliasListItem `json:"stateMachineAliases" cbor:"stateMachineAliases"`
	NextToken           string                      `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

// listStateMachineAliasesTyped lists a state machine's aliases, most recent
// first. Given a version ARN it lists only the aliases routing to that
// version.
func (h *Handler) listStateMachineAliasesTyped(ctx context.Context, req *listStateMachineAliasesRequest) (*listStateMachineAliasesResponse, *protocol.AWSError) {
	arn, ok := parseSMARN(req.StateMachineArn)
	if !ok || arn.isAlias() {
		return nil, errInvalidArn(req.StateMachineArn)
	}
	if _, aerr := h.getBaseStateMachine(ctx, arn, req.StateMachineArn); aerr != nil {
		return nil, aerr
	}
	if arn.isVersion() {
		v, aerr := h.getVersion(ctx, arn)
		if aerr != nil {
			return nil, aerr
		}
		if v == nil {
			return nil, errResourceNotFound(req.StateMachineArn)
		}
	}
	aliases, err := h.store.ListAliases(ctx, arn.name)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if arn.isVersion() {
		aliases = slices.DeleteFunc(aliases, func(a *StateMachineAlias) bool { return !aliasRoutesTo(a, req.StateMachineArn) })
	}
	page, aerr := paginate(aliases, req.MaxResults, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	items := make([]stateMachineAliasListItem, 0, len(page.Items))
	for _, a := range page.Items {
		items = append(items, stateMachineAliasListItem{StateMachineAliasArn: a.ARN, CreationDate: epochSeconds(a.CreatedAt)})
	}
	return &listStateMachineAliasesResponse{StateMachineAliases: items, NextToken: page.NextToken}, nil
}
