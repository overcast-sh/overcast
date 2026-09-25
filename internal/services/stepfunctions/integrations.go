package stepfunctions

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

// Task resource integrations.
//
// Every integration is dispatched through Overcast's own router, so a Task
// state exercises exactly the same handler an SDK call would — no second,
// divergent code path. The optimized integrations are below, AWS SDK
// integrations in sdk_integration.go, activities and .waitForTaskToken in
// callbacks.go. What none of them covers fails the execution with
// States.Runtime naming the resource, which is neither retriable nor catchable
// (see errorMatches): a workflow can never silently skip a step Overcast
// cannot run.

// taskIntegration is a parsed Task Resource ARN.
type taskIntegration struct {
	// service is the history-event resourceType: "lambda", "sqs", "sns",
	// "dynamodb", "athena", "states".
	service string
	// action is the history-event resource: "invoke", "sendMessage", …
	action string
	// pattern is the service-integration pattern suffix: "", "sync",
	// "sync:2" or "waitForTaskToken".
	pattern string
	// functionName is set for a Task whose Resource is a Lambda function ARN
	// rather than an optimized `arn:aws:states:::lambda:invoke` integration.
	functionName string
	// activityArn is set for a Task whose Resource is an activity ARN.
	activityArn string
}

// The service-integration patterns: Run a Job and Wait for a Callback.
const (
	patternSync             = "sync"
	patternWaitForTaskToken = "waitForTaskToken"
)

// direct reports whether the Resource named a Lambda function ARN directly,
// which AWS records with LambdaFunction* history events rather than Task*.
func (t taskIntegration) direct() bool { return t.functionName != "" }

// parseTaskResource classifies a Task state's Resource ARN.
func parseTaskResource(resource string) (taskIntegration, *stateError) {
	trimmed := strings.TrimSpace(resource)
	if trimmed == "" {
		return taskIntegration{}, newStateError(errRuntime, "Task Resource is empty")
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) < 6 || parts[0] != "arn" {
		return taskIntegration{}, unsupportedError("Task Resource %q — it is not an ARN", resource)
	}
	switch parts[2] {
	case "lambda":
		// arn:aws:lambda:<region>:<account>:function:<name>[:<qualifier>]
		if parts[5] != "function" || len(parts) < 7 {
			return taskIntegration{}, unsupportedError("Lambda Task Resource %q — only function ARNs are interpreted", resource)
		}
		name := parts[6]
		if len(parts) > 7 {
			name = parts[6] + ":" + parts[7]
		}
		return taskIntegration{service: "lambda", action: "invoke", functionName: name}, nil
	case "states":
		// arn:aws:states:::<service>:<action>[.<pattern>]
		if parts[3] != "" || parts[4] != "" {
			if parts[5] == "activity" {
				return taskIntegration{service: "activity", activityArn: trimmed}, nil
			}
			return taskIntegration{}, unsupportedError("Task Resource %q", resource)
		}
		if len(parts) < 7 {
			return taskIntegration{}, unsupportedError("Task Resource %q", resource)
		}
		service := parts[5]
		action := parts[6]
		if service == "aws-sdk" {
			// arn:aws:states:::aws-sdk:<service>:<action>[.waitForTaskToken]
			if len(parts) < 8 {
				return taskIntegration{}, newStateError(errRuntime, "Task Resource %q names no aws-sdk action", resource)
			}
			service = "aws-sdk:" + parts[6]
			action = parts[7]
		}
		pattern := ""
		if dot := strings.Index(action, "."); dot >= 0 {
			pattern = action[dot+1:]
			action = action[:dot]
		}
		// `.sync:2` arrives as a trailing ARN segment because of the colon.
		if len(parts) > 7 && pattern != "" && !strings.HasPrefix(service, "aws-sdk:") {
			pattern = pattern + ":" + strings.Join(parts[7:], ":")
		}
		return taskIntegration{service: service, action: action, pattern: pattern}, nil
	}
	return taskIntegration{}, unsupportedError("Task Resource %q", resource)
}

// validateTaskResource rejects, at CreateStateMachine, a Task Resource AWS
// refuses there. Only aws-sdk and athena integrations are checked so far
// (#2188); any other Resource Overcast cannot run still provisions and fails
// at run time.
func validateTaskResource(state *aslState, loc string) error {
	integration, serr := parseTaskResource(state.Resource)
	if serr != nil {
		return nil
	}
	if service, ok := strings.CutPrefix(integration.service, "aws-sdk:"); ok {
		return validateSDKTask(state, loc, service, integration)
	}
	if integration.service == "athena" && !athenaIntegrationOffered(integration) {
		return resourceNotRecognized(loc, state.Resource)
	}
	return nil
}

// runTask executes one attempt of a Task state.
func (in *interpreter) runTask(ctx context.Context, f *flow) (any, *stateError) {
	name, state := f.name, f.state
	integration, serr := parseTaskResource(state.Resource)
	if serr != nil {
		return nil, serr
	}
	// A Task resumed after a restart continues waiting on the token it was
	// parked on; it is never dispatched a second time.
	if cp := in.takeResumed(parkCallback, parkActivity); cp != nil {
		return in.resumeParkedTask(ctx, name, integration, cp)
	}
	if integration.activityArn != "" {
		return in.runActivity(ctx, f, integration.activityArn)
	}

	// A callback task issues its token before its arguments are evaluated,
	// so $$.Task.Token (JSONata: $states.context.Task.Token) resolves in them.
	var callback *pendingTask
	if integration.pattern == patternWaitForTaskToken {
		callback = in.handler.tasks.register("", "")
		defer in.handler.tasks.release(callback)
		f = f.withContext(withTaskToken(f.ctxObj, callback.token))
	}

	payload, serr := f.arguments()
	if serr != nil {
		return nil, serr
	}
	payloadJSON, encErr := encodeJSON(payload)
	if encErr != nil {
		return nil, newStateError(errRuntime, "%s", encErr.Error())
	}

	timeout, serr := f.positiveSeconds(state.TimeoutSeconds, state.TimeoutSecondsPath, "TimeoutSeconds")
	if serr != nil {
		return nil, serr
	}
	var heartbeat *int64
	if callback != nil {
		if heartbeat, serr = f.positiveSeconds(state.HeartbeatSeconds, state.HeartbeatSecondsPath, "HeartbeatSeconds"); serr != nil {
			return nil, serr
		}
	}
	in.recordTaskScheduled(integration, state.Resource, payloadJSON, timeout, heartbeat)
	in.recordTaskStarted(integration)

	// The Task's own budget, on top of the execution's. Deriving a context is
	// what makes it real rather than decorative: every integration dispatches
	// through the router with this context, so an over-running Lambda invoke
	// or a `.sync` child execution is actually interrupted. The deadline is
	// wall-clock for the same reason the execution budget in runExecution is —
	// a context has no clock to inject.
	taskCtx := ctx
	if timeout != nil {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
		defer cancel()
	}

	var result any
	if callback != nil {
		// The call itself is the request-response form of the integration;
		// its answer is TaskSubmitted, and the Task's result is whatever a
		// worker later reports with the token.
		submit := integration
		submit.pattern = ""
		var submitted any
		if submitted, serr = in.dispatchTask(taskCtx, submit, payload); serr == nil {
			in.recordTaskSubmitted(integration, submitted)
			now := in.handler.clk.Now()
			park := in.park(ctx, f, &executionCheckpoint{
				Kind:              parkCallback,
				Token:             callback.token,
				HeartbeatSeconds:  heartbeat,
				HeartbeatDeadline: deadlineAfter(now, heartbeat),
				TimeoutSeconds:    timeout,
				TimeoutDeadline:   deadlineAfter(now, timeout),
			})
			result, serr = in.awaitCallback(taskCtx, callback, heartbeat, park)
			in.unpark(ctx, park, serr != nil && ctx.Err() != nil)
		}
	} else if mock := in.taskMock(); mock != nil {
		result, serr = mockedResult(mock)
	} else {
		result, serr = in.dispatchTask(taskCtx, integration, payload)
	}
	return in.finishTask(ctx, taskCtx, name, integration, timeout, result, serr)
}

// resumeParkedTask continues a Task that was waiting on its token when
// Overcast last shut down (durable.go). The token was registered again during
// rehydration; its heartbeat and TimeoutSeconds run to the deadlines the
// checkpoint recorded, on the injected clock.
func (in *interpreter) resumeParkedTask(ctx context.Context, name string, integration taskIntegration, cp *executionCheckpoint) (any, *stateError) {
	task := in.run.resumeTask
	defer in.handler.tasks.release(task)
	taskCtx := ctx
	if cp.TimeoutDeadline != nil {
		var cancel context.CancelFunc
		taskCtx, cancel = in.handler.clk.WithDeadline(ctx, *cp.TimeoutDeadline)
		defer cancel()
	}
	park := in.resumePark(cp)
	if cp.Kind == parkActivity {
		result, serr := in.awaitActivity(ctx, taskCtx, task, cp.HeartbeatSeconds, park)
		in.unpark(ctx, park, serr != nil && ctx.Err() != nil)
		return in.finishActivity(ctx, taskCtx, name, cp.TimeoutSeconds, result, serr)
	}
	result, serr := in.awaitCallback(taskCtx, task, cp.HeartbeatSeconds, park)
	in.unpark(ctx, park, serr != nil && ctx.Err() != nil)
	return in.finishTask(ctx, taskCtx, name, integration, cp.TimeoutSeconds, result, serr)
}

// finishTask records how a Task attempt ended and returns its result.
func (in *interpreter) finishTask(ctx, taskCtx context.Context, name string, integration taskIntegration, timeout *int64, result any, serr *stateError) (any, *stateError) {
	if serr != nil {
		// Whatever the interrupted integration reported, a failure after the
		// Task's own deadline is States.Timeout — the error name AWS raises,
		// which Retry/Catch can match and which recordTaskFailed turns into a
		// TaskTimedOut event. The parent context is checked first so an
		// execution-budget unwind or a StopExecution is not relabelled.
		if ctx.Err() != nil {
			// The execution (or a sibling branch) is unwinding: the state is
			// aborted, not failed, and runState records TaskStateAborted.
			return nil, in.unwindReason(ctx, name)
		}
		if timeout != nil && taskCtx.Err() != nil {
			serr = newStateError(errTimeout, "the Task state %q exceeded its TimeoutSeconds of %d", name, *timeout)
		}
		in.recordTaskFailed(integration, serr)
		return nil, serr
	}
	in.recordTaskSucceeded(integration, result)
	return result, nil
}

// dispatchTask routes a Task to the integration that runs it.
func (in *interpreter) dispatchTask(ctx context.Context, integration taskIntegration, payload any) (any, *stateError) {
	if in.handler.router == nil {
		return nil, newStateError(errRuntime,
			"Overcast's Step Functions service has no router wired, so Task states cannot reach other services")
	}
	if integration.direct() {
		return in.invokeLambdaDirect(ctx, integration.functionName, payload)
	}
	switch integration.service {
	case "lambda":
		if integration.action != "invoke" {
			return nil, unsupportedError("the lambda:%s integration", integration.action)
		}
		if integration.pattern != "" {
			return nil, unsupportedError("the lambda:invoke.%s service-integration pattern", integration.pattern)
		}
		return in.invokeLambdaOptimized(ctx, payload)
	case "sqs":
		if integration.action != "sendMessage" || integration.pattern != "" {
			return nil, unsupportedError("the sqs:%s%s integration — only sqs:sendMessage is interpreted", integration.action, patternSuffix(integration.pattern))
		}
		return in.sendSQSMessage(ctx, payload)
	case "sns":
		if integration.action != "publish" || integration.pattern != "" {
			return nil, unsupportedError("the sns:%s%s integration — only sns:publish is interpreted", integration.action, patternSuffix(integration.pattern))
		}
		return in.publishSNS(ctx, payload)
	case "dynamodb":
		return in.invokeDynamoDB(ctx, integration, payload)
	case "athena":
		return in.invokeAthena(ctx, integration, payload)
	case "events":
		if integration.action != "putEvents" || integration.pattern != "" {
			return nil, unsupportedError("the events:%s%s integration — only events:putEvents is interpreted", integration.action, patternSuffix(integration.pattern))
		}
		return in.putEvents(ctx, payload)
	case "states":
		if integration.action != "startExecution" {
			return nil, unsupportedError("the states:%s integration — only states:startExecution is interpreted", integration.action)
		}
		return in.startNestedExecution(ctx, integration, payload)
	}
	if sdkService, ok := strings.CutPrefix(integration.service, "aws-sdk:"); ok {
		if integration.pattern != "" {
			return nil, unsupportedError("the .%s pattern on an aws-sdk integration — AWS offers only request-response and .waitForTaskToken there", integration.pattern)
		}
		return in.invokeAWSSDK(ctx, sdkService, integration.action, payload)
	}
	return nil, unsupportedError("the %s:%s%s service integration", integration.service, integration.action, patternSuffix(integration.pattern))
}

// ─── EventBridge ──────────────────────────────────────────────────────────────

// putEvents runs events:putEvents. ASL lets an entry's Detail be a JSON
// object; the API takes it as a string. As on AWS, a response reporting any
// failed entry fails the task with EventBridge.FailedEntry.
func (in *interpreter) putEvents(ctx context.Context, payload any) (any, *stateError) {
	params, ok := payload.(map[string]any)
	if !ok {
		return nil, newStateError(errParameterPathFailure, "events:putEvents requires Parameters with Entries")
	}
	entries, _ := params["Entries"].([]any)
	if len(entries) == 0 {
		return nil, newStateError(errParameterPathFailure, "events:putEvents requires a non-empty Parameters.Entries")
	}
	body := map[string]any{}
	for key, value := range params {
		body[key] = value
	}
	encoded := make([]any, 0, len(entries))
	for _, raw := range entries {
		entry, isObject := raw.(map[string]any)
		if !isObject {
			return nil, newStateError(errParameterPathFailure, "events:putEvents Entries must be objects")
		}
		copied := map[string]any{}
		for key, value := range entry {
			copied[key] = value
		}
		if detail, present := copied["Detail"]; present {
			if _, isString := detail.(string); !isString {
				text, err := encodeJSON(detail)
				if err != nil {
					return nil, newStateError(errRuntime, "%s", err.Error())
				}
				copied["Detail"] = text
			}
		}
		encoded = append(encoded, copied)
	}
	body["Entries"] = encoded
	result, serr := in.invokeTargetAs(ctx, "AWSEvents.PutEvents", "application/x-amz-json-1.1", body, "EventBridge")
	if serr != nil {
		return nil, serr
	}
	if response, isObject := result.(map[string]any); isObject {
		if failed, _ := toNumber(response["FailedEntryCount"]); failed > 0 {
			return nil, newStateError("EventBridge.FailedEntry", "%v of the events could not be put: %v", failed, response["Entries"])
		}
	}
	return result, nil
}

func patternSuffix(pattern string) string {
	if pattern == "" {
		return ""
	}
	return "." + pattern
}

// ─── SQS ──────────────────────────────────────────────────────────────────────

func (in *interpreter) sendSQSMessage(ctx context.Context, payload any) (any, *stateError) {
	params, ok := payload.(map[string]any)
	if !ok {
		return nil, newStateError(errParameterPathFailure, "sqs:sendMessage requires Parameters with QueueUrl and MessageBody")
	}
	if _, present := params["QueueUrl"]; !present {
		return nil, newStateError(errParameterPathFailure, "sqs:sendMessage requires Parameters.QueueUrl")
	}
	body := map[string]any{}
	for key, value := range params {
		body[key] = value
	}
	// ASL lets MessageBody be any JSON value; the SQS API takes a string.
	if messageBody, present := body["MessageBody"]; present {
		if _, isString := messageBody.(string); !isString {
			encoded, err := json.Marshal(messageBody)
			if err != nil {
				return nil, newStateError(errRuntime, "MessageBody could not be encoded: %v", err)
			}
			body["MessageBody"] = string(encoded)
		}
	}
	return in.invokeTarget(ctx, "AmazonSQS.SendMessage", body, "Sqs")
}

// ─── SNS ──────────────────────────────────────────────────────────────────────

// snsScalarParameters are the sns:publish parameters Overcast forwards. SNS
// speaks the AWS Query protocol, so structured parameters (MessageAttributes)
// would need Query member encoding — they fail loudly instead of being dropped.
var snsScalarParameters = map[string]bool{
	"TopicArn": true, "TargetArn": true, "PhoneNumber": true,
	"Message": true, "Subject": true, "MessageStructure": true,
	"MessageGroupId": true, "MessageDeduplicationId": true,
}

func (in *interpreter) publishSNS(ctx context.Context, payload any) (any, *stateError) {
	params, ok := payload.(map[string]any)
	if !ok {
		return nil, newStateError(errParameterPathFailure, "sns:publish requires Parameters with a Message and a TopicArn")
	}
	form := url.Values{}
	form.Set("Action", "Publish")
	form.Set("Version", "2010-03-31")
	for key, value := range params {
		if !snsScalarParameters[key] {
			return nil, unsupportedError("the sns:publish parameter %q", key)
		}
		text, isString := value.(string)
		if !isString {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, newStateError(errRuntime, "the sns:publish parameter %q could not be encoded: %v", key, err)
			}
			text = string(encoded)
		}
		form.Set(key, text)
	}
	if form.Get("Message") == "" {
		return nil, newStateError(errParameterPathFailure, "sns:publish requires Parameters.Message")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, newStateError(errRuntime, "%v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Overcast-Region", in.region)
	rec := httptest.NewRecorder()
	in.handler.router.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		code, message := decodeXMLError(rec.Body.Bytes())
		return nil, &stateError{name: "Sns." + code, cause: message}
	}

	var response struct {
		MessageID string `xml:"PublishResult>MessageId"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		return nil, newStateError(errTaskFailed, "the SNS Publish response could not be decoded: %v", err)
	}
	return map[string]any{"MessageId": response.MessageID}, nil
}

// ─── DynamoDB ─────────────────────────────────────────────────────────────────

// dynamoDBActions maps the optimized DynamoDB integrations Overcast
// interprets to their API target. Anything else fails loudly.
var dynamoDBActions = map[string]string{
	"putItem":    "PutItem",
	"getItem":    "GetItem",
	"updateItem": "UpdateItem",
	"deleteItem": "DeleteItem",
}

func (in *interpreter) invokeDynamoDB(ctx context.Context, integration taskIntegration, payload any) (any, *stateError) {
	operation, known := dynamoDBActions[integration.action]
	if !known || integration.pattern != "" {
		return nil, unsupportedError("the dynamodb:%s%s integration — Overcast interprets dynamodb:putItem, getItem, updateItem and deleteItem", integration.action, patternSuffix(integration.pattern))
	}
	params, ok := payload.(map[string]any)
	if !ok {
		return nil, newStateError(errParameterPathFailure, "dynamodb:%s requires Parameters", integration.action)
	}
	return in.invokeTarget(ctx, "DynamoDB_20120810."+operation, params, "DynamoDB")
}

// ─── Nested state machine executions ──────────────────────────────────────────

func (in *interpreter) startNestedExecution(ctx context.Context, integration taskIntegration, payload any) (any, *stateError) {
	switch integration.pattern {
	case "", "sync", "sync:2":
	default:
		return nil, unsupportedError("the states:startExecution.%s service-integration pattern", integration.pattern)
	}
	if in.depth >= maxNestedExecutionDepth {
		return nil, newStateError(errRuntime, "nested state machine executions are limited to %d levels", maxNestedExecutionDepth)
	}
	params, ok := payload.(map[string]any)
	if !ok {
		return nil, newStateError(errParameterPathFailure, "states:startExecution requires Parameters with a StateMachineArn")
	}
	smARN, _ := params["StateMachineArn"].(string)
	if smARN == "" {
		return nil, newStateError(errParameterPathFailure, "states:startExecution requires Parameters.StateMachineArn")
	}
	execName, _ := params["Name"].(string)

	input := ""
	if raw, present := params["Input"]; present {
		if text, isString := raw.(string); isString {
			input = text
		} else {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return nil, newStateError(errRuntime, "the nested execution input could not be encoded: %v", err)
			}
			input = string(encoded)
		}
	}

	// A plain states:startExecution fires and forgets, as on AWS; only the
	// .sync forms block on the child.
	mode := executionAsync
	if integration.pattern != "" {
		mode = executionSync
	}
	child, aerr := in.handler.startExecution(ctx, smARN, execName, input, in.depth+1, mode)
	if aerr != nil {
		return nil, &stateError{name: "StepFunctions." + aerr.Code, cause: aerr.Message}
	}

	if integration.pattern == "" {
		return map[string]any{
			"ExecutionArn": child.ExecutionArn,
			"StartDate":    float64(child.StartDate.UnixMilli()) / 1000.0,
		}, nil
	}
	if child.Status != statusSucceeded {
		return nil, &stateError{name: errTaskFailed, cause: nestedFailureCause(child)}
	}

	result := map[string]any{
		"ExecutionArn":    child.ExecutionArn,
		"Input":           child.Input,
		"InputDetails":    map[string]any{"Included": true},
		"Name":            child.Name,
		"OutputDetails":   map[string]any{"Included": true},
		"StartDate":       float64(child.StartDate.UnixMilli()) / 1000.0,
		"StateMachineArn": child.StateMachineArn,
		"Status":          child.Status,
	}
	if child.StopDate != nil {
		result["StopDate"] = float64(child.StopDate.UnixMilli()) / 1000.0
	}
	// `.sync` hands the child's output back as a JSON string; `.sync:2`
	// parses it first, which is the whole difference between the two.
	if integration.pattern == "sync:2" {
		var decoded any
		if child.Output != "" && json.Unmarshal([]byte(child.Output), &decoded) == nil {
			result["Output"] = decoded
		}
	} else if child.Output != "" {
		result["Output"] = child.Output
	}
	return result, nil
}

func nestedFailureCause(child *Execution) string {
	cause := fmt.Sprintf("the nested execution %s ended %s", child.ExecutionArn, child.Status)
	if child.Error != "" {
		cause += ": " + child.Error
	}
	if child.Cause != "" {
		cause += " — " + child.Cause
	}
	return cause
}

// ─── Shared router dispatch ───────────────────────────────────────────────────

// invokeTarget performs an AWS JSON 1.0 target-dispatch call against
// Overcast's own router and returns the decoded response body.
func (in *interpreter) invokeTarget(ctx context.Context, target string, body map[string]any, errorPrefix string) (any, *stateError) {
	return in.invokeTargetAs(ctx, target, "application/x-amz-json-1.0", body, errorPrefix)
}

// invokeTargetAs is invokeTarget for a service that speaks a specific AWS
// JSON version.
func (in *interpreter) invokeTargetAs(ctx context.Context, target, contentType string, body map[string]any, errorPrefix string) (any, *stateError) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, newStateError(errRuntime, "the %s request could not be encoded: %v", target, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(encoded))
	if err != nil {
		return nil, newStateError(errRuntime, "%v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Amz-Target", target)
	req.Header.Set("X-Overcast-Region", in.region)
	rec := httptest.NewRecorder()
	in.handler.router.ServeHTTP(rec, req)

	if rec.Code >= 400 {
		code, message := decodeJSONError(rec.Body.Bytes())
		return nil, &stateError{name: errorPrefix + "." + code, cause: message}
	}
	var decoded any
	raw := bytes.TrimSpace(rec.Body.Bytes())
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, newStateError(errTaskFailed, "the %s response could not be decoded: %v", target, err)
	}
	return decoded, nil
}

// decodeJSONError pulls an AWS error code and message out of a JSON error body.
func decodeJSONError(body []byte) (code, message string) {
	var payload struct {
		Type    string `json:"__type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"Message"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return "Unknown", strings.TrimSpace(string(body))
	}
	code = payload.Type
	if code == "" {
		code = payload.Code
	}
	// Some services qualify the type with a shape prefix ("com.amazonaws#Foo").
	if hash := strings.LastIndex(code, "#"); hash >= 0 {
		code = code[hash+1:]
	}
	if code == "" {
		code = "Unknown"
	}
	message = payload.Message
	if message == "" {
		message = payload.Msg
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return code, message
}

// decodeXMLError pulls an AWS error code and message out of an XML error body.
func decodeXMLError(body []byte) (code, message string) {
	var payload struct {
		Code    string `xml:"Error>Code"`
		Message string `xml:"Error>Message"`
	}
	if xml.Unmarshal(body, &payload) != nil || payload.Code == "" {
		return "Unknown", strings.TrimSpace(string(body))
	}
	return payload.Code, payload.Message
}
