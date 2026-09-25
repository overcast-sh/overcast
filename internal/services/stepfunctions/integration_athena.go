package stepfunctions

import (
	"context"
	"time"
)

// The optimized Athena integrations — exactly the four AWS offers:
//
//	athena:startQueryExecution        request-response and .sync
//	athena:stopQueryExecution         request-response
//	athena:getQueryExecution          request-response
//	athena:getQueryResults            request-response
//
// Each is the Athena API call of the same name, dispatched through Overcast's
// own router with the Task's Parameters as its request, so its result is that
// API's response. `.sync` records the start as TaskSubmitted, then polls
// GetQueryExecution until the query reaches a final state: its result is that
// final response, and a query that ends FAILED or CANCELLED fails the Task
// with States.TaskFailed, the response as the cause, as AWS reports every
// `.sync` job that does not succeed.

// athenaActions maps each optimized Athena integration to its API operation.
var athenaActions = map[string]string{
	"startQueryExecution": "StartQueryExecution",
	"stopQueryExecution":  "StopQueryExecution",
	"getQueryExecution":   "GetQueryExecution",
	"getQueryResults":     "GetQueryResults",
}

// athenaPollInterval is how often a `.sync` Task asks for its query's state.
const athenaPollInterval = time.Second

// athenaIntegrationOffered reports whether AWS offers the integration: every
// action request-response, and startQueryExecution also as .sync. No Athena
// integration takes .waitForTaskToken.
func athenaIntegrationOffered(integration taskIntegration) bool {
	if _, known := athenaActions[integration.action]; !known {
		return false
	}
	return integration.pattern == "" || integration.pattern == patternSync && integration.action == "startQueryExecution"
}

// invokeAthena runs an optimized Athena integration.
func (in *interpreter) invokeAthena(ctx context.Context, integration taskIntegration, payload any) (any, *stateError) {
	if !athenaIntegrationOffered(integration) {
		return nil, unsupportedError("the athena:%s%s integration — AWS offers athena:startQueryExecution (request-response and .sync), stopQueryExecution, getQueryExecution and getQueryResults",
			integration.action, patternSuffix(integration.pattern))
	}
	params, ok := payload.(map[string]any)
	if !ok {
		return nil, newStateError(errParameterPathFailure, "athena:%s requires Parameters", integration.action)
	}
	result, serr := in.callAthena(ctx, athenaActions[integration.action], params)
	if serr != nil || integration.pattern == "" {
		return result, serr
	}
	started, _ := result.(map[string]any)
	queryID, _ := started["QueryExecutionId"].(string)
	if queryID == "" {
		return nil, newStateError(errRuntime, "Athena's StartQueryExecution response named no QueryExecutionId")
	}
	in.recordTaskSubmitted(integration, result)
	return in.awaitAthenaQuery(ctx, queryID)
}

// awaitAthenaQuery polls a query until it reaches a final state. If the Task
// is interrupted first — it timed out, or the execution was stopped — the
// query is stopped too, as AWS stops a `.sync` job it abandons.
func (in *interpreter) awaitAthenaQuery(ctx context.Context, queryID string) (any, *stateError) {
	request := map[string]any{"QueryExecutionId": queryID}
	defer func() {
		if ctx.Err() != nil {
			// Best effort: the Task has already ended, whatever Athena says.
			_, _ = in.callAthena(context.WithoutCancel(ctx), "StopQueryExecution", request)
		}
	}()
	for {
		execution, serr := in.callAthena(ctx, "GetQueryExecution", request)
		if serr != nil {
			return nil, serr
		}
		switch queryState := athenaQueryState(execution); queryState {
		case "QUEUED", "RUNNING":
		case "SUCCEEDED":
			return execution, nil
		case "FAILED", "CANCELLED":
			cause, err := encodeJSON(execution)
			if err != nil {
				return nil, newStateError(errRuntime, "%s", err.Error())
			}
			return nil, &stateError{name: errTaskFailed, cause: cause}
		default:
			return nil, newStateError(errRuntime, "Athena query %s reported an unrecognized state %q", queryID, queryState)
		}
		if !in.pause(ctx, athenaPollInterval) {
			return nil, newStateError(errTaskFailed, "the Task stopped waiting for Athena query %s", queryID)
		}
	}
}

// athenaQueryState reads QueryExecution.Status.State from a GetQueryExecution
// response.
func athenaQueryState(response any) string {
	body, _ := response.(map[string]any)
	execution, _ := body["QueryExecution"].(map[string]any)
	status, _ := execution["Status"].(map[string]any)
	queryState, _ := status["State"].(string)
	return queryState
}

// callAthena makes one Athena API call (AWS JSON 1.1) through the router. The
// response passes through as Athena sends it, timestamps as epoch seconds.
//
// TODO(priority:P2): verify the timestamp format AWS emits in the output of
// the optimized Athena integrations, and convert if it differs.
func (in *interpreter) callAthena(ctx context.Context, operation string, body map[string]any) (any, *stateError) {
	return in.invokeTargetAs(ctx, "AmazonAthena."+operation, "application/x-amz-json-1.1", body, "Athena")
}
