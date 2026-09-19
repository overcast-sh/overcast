package stepfunctions

import (
	"context"
	"math/rand/v2"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Resolving the state machine ARN an execution operation was given — plain,
// version-qualified or alias-qualified — into what actually runs.

// executionTarget records which qualified ARN an execution went through.
// Both fields are empty for an unqualified state machine ARN.
type executionTarget struct {
	versionArn string // the version that runs (also set when an alias routed to it)
	aliasArn   string
}

// resolveExecutionTarget resolves StartExecution's / StartSyncExecution's
// stateMachineArn. For a version ARN it returns the base state machine record
// with that version's definition and role swapped in; for an alias ARN it
// first picks one of the alias's versions by weight. The returned record
// keeps the base ARN and name — executions of a version or alias are
// executions of the state machine, named under it, as on AWS.
func (h *Handler) resolveExecutionTarget(ctx context.Context, arn string) (*StateMachine, *executionTarget, *protocol.AWSError) {
	parsed, ok := parseSMARN(arn)
	if !ok || parsed.qualifier == "" {
		sm, err := h.store.GetStateMachine(ctx, extractSMName(arn))
		if err != nil {
			return nil, nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if sm == nil {
			return nil, nil, errSMNotFound(arn)
		}
		return sm, &executionTarget{}, nil
	}

	sm, aerr := h.getBaseStateMachine(ctx, parsed, arn)
	if aerr != nil {
		return nil, nil, aerr
	}
	target := &executionTarget{}
	versionARN := parsed
	if parsed.isAlias() {
		alias, err := h.store.GetAlias(ctx, parsed.name, parsed.qualifier)
		if err != nil {
			return nil, nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if alias == nil || len(alias.RoutingConfiguration) == 0 {
			return nil, nil, errSMNotFound(arn)
		}
		target.aliasArn = alias.ARN
		picked := h.pickRoute(alias.RoutingConfiguration)
		if versionARN, ok = parseSMARN(picked); !ok || !versionARN.isVersion() {
			return nil, nil, errSMNotFound(arn)
		}
	}
	v, aerr := h.getVersion(ctx, versionARN)
	if aerr != nil {
		return nil, nil, aerr
	}
	if v == nil {
		return nil, nil, errSMNotFound(arn)
	}
	target.versionArn = v.ARN
	return withVersion(sm, v), target, nil
}

// pickRoute chooses the version an alias execution runs, at random in
// proportion to the routes' weights (which sum to 100). h.pickWeight is the
// source of randomness, injectable so tests can pin the choice.
func (h *Handler) pickRoute(routes []aliasRouteRecord) string {
	intN := h.pickWeight
	if intN == nil {
		intN = rand.IntN
	}
	roll := intN(totalRoutingWeight)
	cumulative := 0
	for _, r := range routes {
		cumulative += r.Weight
		if roll < cumulative {
			return r.StateMachineVersionArn
		}
	}
	return routes[len(routes)-1].StateMachineVersionArn
}

// executionFilter resolves ListExecutions' stateMachineArn. An unqualified
// ARN lists every execution of the state machine; a version ARN lists the
// executions that ran that version (started through it, or routed to it by an
// alias); an alias ARN lists the executions started through the alias.
func (h *Handler) executionFilter(ctx context.Context, arn string) (*StateMachine, func(*Execution) bool, *protocol.AWSError) {
	parsed, ok := parseSMARN(arn)
	if !ok || parsed.qualifier == "" {
		sm, err := h.store.GetStateMachine(ctx, extractSMName(arn))
		if err != nil {
			return nil, nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if sm == nil {
			return nil, nil, errSMNotFound(arn)
		}
		return sm, func(*Execution) bool { return true }, nil
	}
	sm, aerr := h.getBaseStateMachine(ctx, parsed, arn)
	if aerr != nil {
		return nil, nil, aerr
	}
	if parsed.isVersion() {
		return sm, func(e *Execution) bool { return e.StateMachineVersionArn == arn }, nil
	}
	return sm, func(e *Execution) bool { return e.StateMachineAliasArn == arn }, nil
}

// stateMachineForExecution returns the state machine record an execution ran:
// the base state machine with its version's snapshot swapped in when the
// execution ran a version. A version deleted since falls back to the base
// state machine's current definition.
func (h *Handler) stateMachineForExecution(ctx context.Context, exec *Execution) (*StateMachine, *protocol.AWSError) {
	sm, err := h.store.GetStateMachine(ctx, extractSMName(exec.StateMachineArn))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if sm == nil {
		return nil, errSMNotFound(exec.StateMachineArn)
	}
	if exec.StateMachineVersionArn == "" {
		return sm, nil
	}
	parsed, ok := parseSMARN(exec.StateMachineVersionArn)
	if !ok || !parsed.isVersion() {
		return sm, nil
	}
	v, aerr := h.getVersion(ctx, parsed)
	if aerr != nil {
		return nil, aerr
	}
	if v == nil {
		return sm, nil
	}
	return withVersion(sm, v), nil
}
