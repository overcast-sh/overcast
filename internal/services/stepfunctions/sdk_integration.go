package stepfunctions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/awsshapes"
)

// AWS SDK service integrations: `arn:aws:states:::aws-sdk:<service>:<action>`.
//
// Every modeled action of every service Overcast implements is reachable, over
// whichever protocol the service speaks. The operation's Smithy shapes
// (internal/awsshapes) drive a real wire request — an X-Amz-Target JSON body,
// a Query form, a REST call with its labels, query string, headers and
// payload, or a Smithy RPC v2 CBOR body — which goes through Overcast's own
// router, exactly as an SDK call would (sdk_request.go). The response is read
// back through the output shape (sdk_response.go).
//
// The conventions are AWS's own for SDK integrations, which run on the AWS SDK
// for Java v2:
//
//   - parameter and result member names are PascalCase whatever the
//     service's wire casing, while map keys — user data such as tags, S3
//     metadata or DynamoDB attribute names — are left exactly as written;
//   - timestamps come back as ISO-8601 strings, blobs as base64 strings, and a
//     payload blob (an S3 object's Body, a Lambda Payload) as its text;
//   - errors are named `<Service>.<Exception>`, the SDK's exception class for
//     the modeled error (S3.NoSuchKeyException, Iam.NoSuchEntityException), or
//     `<Service>.<Service>Exception` for an error the model does not declare
//     (Ec2.Ec2Exception);
//   - a Resource naming an action AWS does not have is rejected by
//     CreateStateMachine, as are Parameters naming a member it does not take.

// sdkCall is one resolved aws-sdk integration.
type sdkCall struct {
	service  string // as written in the Resource ("s3", "cloudwatchlogs")
	action   string // as written ("getObject")
	op       awsapi.Operation
	svc      *awsshapes.Service
	shape    *awsshapes.Shape // the operation
	protocol awsapi.Protocol
	prefix   string // error-name prefix ("S3", "DynamoDb")
}

// sdkOperations caches the manifest lookup for each service/action pair a
// workflow has used, so the corpus is scanned once per pair.
var sdkOperations sync.Map

// findSDKOperation resolves an aws-sdk service name and camelCase action to
// its modeled operation. The service name is the SDK's own identifier with
// spaces and hyphens removed and lower-cased — `sfn`, `dynamodb`,
// `secretsmanager`, `cloudwatchlogs` — which is how AWS spells it in the
// Resource ARN; the action is the operation name with a lower-case first
// letter, and AWS rejects any other spelling.
func findSDKOperation(service, action string) (awsapi.Operation, bool) {
	if r, _ := utf8.DecodeRuneInString(action); !unicode.IsLower(r) {
		return awsapi.Operation{}, false
	}
	key := service + ":" + action
	if cached, ok := sdkOperations.Load(key); ok {
		op, found := cached.(awsapi.Operation)
		return op, found
	}
	name := capitalize(action)
	var match awsapi.Operation
	found := false
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.Name == name && normalizeSDKID(op.SDKID) == service {
			match, found = op, true
			return false
		}
		return true
	})
	if found {
		sdkOperations.Store(key, match)
	} else {
		sdkOperations.Store(key, false)
	}
	return match, found
}

func normalizeSDKID(id string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(id))
}

// sdkProtocolPreference is the order AWS SDKs choose among the protocols a
// service declares — the Smithy protocol-selection order, most efficient
// first. CloudWatch, which declares CBOR, JSON and Query, is called over CBOR.
var sdkProtocolPreference = []awsapi.Protocol{
	awsapi.ProtocolRPCV2CBOR,
	awsapi.ProtocolAWSJSON10,
	awsapi.ProtocolAWSJSON11,
	awsapi.ProtocolRESTJSON,
	awsapi.ProtocolRESTXML,
	awsapi.ProtocolAWSQuery,
	awsapi.ProtocolEC2Query,
}

// resolveSDKCall finds the operation and its shapes. A service/action AWS does
// not model is a definition error, reported here as States.Runtime for a
// state machine that predates the check; a modeled one Overcast has no shape
// table for is an Overcast gap and fails loudly.
func resolveSDKCall(service, action string) (*sdkCall, *stateError) {
	op, found := findSDKOperation(service, action)
	if !found {
		return nil, newStateError(errRuntime, "aws-sdk:%s:%s does not name an AWS API action", service, action)
	}
	svc, ok, err := awsshapes.Lookup(op.Service)
	if err != nil {
		return nil, newStateError(errRuntime, "the %s shape table could not be read: %v", op.SDKID, err)
	}
	if !ok {
		return nil, unsupportedError("the aws-sdk:%s:%s integration — %s is not a service Overcast implements", service, action, op.SDKID)
	}
	shape, ok := svc.Operation(op.Name)
	if !ok {
		return nil, unsupportedError("the aws-sdk:%s:%s integration — its shapes are missing from Overcast's %s table", service, action, op.SDKID)
	}
	if shape.HasEventStream() {
		return nil, unsupportedError("the aws-sdk:%s:%s integration — it streams events, which AWS does not offer as an SDK integration", service, action)
	}
	call := &sdkCall{service: service, action: action, op: op, svc: svc, shape: shape, prefix: sdkErrorPrefix(op.SDKID)}
	for _, p := range sdkProtocolPreference {
		if svc.Supports(p) {
			call.protocol = p
			break
		}
	}
	if call.protocol == awsapi.ProtocolUnknown {
		return nil, unsupportedError("the aws-sdk:%s:%s integration — %s speaks no protocol Overcast can call", service, action, op.SDKID)
	}
	return call, nil
}

// invokeAWSSDK runs one aws-sdk integration.
func (in *interpreter) invokeAWSSDK(ctx context.Context, service, action string, payload any) (any, *stateError) {
	params, ok := payload.(map[string]any)
	if !ok {
		if payload != nil {
			return nil, newStateError(errParameterPathFailure, "aws-sdk:%s:%s requires Parameters that are a JSON object", service, action)
		}
		params = map[string]any{}
	}
	call, serr := resolveSDKCall(service, action)
	if serr != nil {
		return nil, serr
	}
	req, err := call.buildRequest(ctx, params)
	if err != nil {
		return nil, newStateError(errRuntime, "aws-sdk:%s:%s: %v", service, action, err)
	}
	req.Header.Set("X-Overcast-Region", in.region)
	rec := httptest.NewRecorder()
	in.handler.router.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		return nil, call.wireError(rec)
	}
	result, err := call.decodeResponse(rec)
	if err != nil {
		return nil, newStateError(errTaskFailed, "the %s %s response could not be decoded: %v", call.op.SDKID, call.op.Name, err)
	}
	return result, nil
}

// wireError turns a failed response into the Step Functions error AWS raises:
// the SDK exception for the modeled error, else the service's base exception.
func (c *sdkCall) wireError(rec *httptest.ResponseRecorder) *stateError {
	code, message, requestID := c.errorDetails(rec)
	exception := c.prefix + "Exception"
	if shape, ok := c.svc.ErrorShape(code); ok {
		exception = sdkExceptionName(shape.Name)
	}
	cause := message
	if cause == "" {
		cause = http.StatusText(rec.Code)
	}
	cause += " (Service: " + c.prefix + ", Status Code: " + strconv.Itoa(rec.Code) + ", Request ID: " + requestID
	if code != "" {
		cause += ", Error Code: " + code
	}
	cause += ")"
	return &stateError{name: c.prefix + "." + exception, cause: cause}
}

// sdkErrorPrefix is the AWS SDK for Java v2 client name for a service — the
// first half of every SDK integration error name.
func sdkErrorPrefix(sdkID string) string { return javaPascalCase(sdkID) }

// sdkExceptionName is the Java v2 exception class for a modeled error shape:
// a "Fault" suffix becomes "Exception", any other name gains it.
func sdkExceptionName(shape string) string {
	base := strings.TrimSuffix(shape, "Fault")
	if !strings.HasSuffix(base, "Exception") {
		base += "Exception"
	}
	return javaPascalCase(base)
}

// versionSuffix splits a trailing lower-case version marker off an acronym
// ("SESv2" → "SES V2"), which the Java code generator treats as its own word.
var versionSuffix = regexp.MustCompile(`([A-Z])v(\d+)$`)

// javaPascalCase reproduces the Java v2 code generator's class naming: split
// into words at spaces, hyphens and case boundaries, then capitalise each word
// and lower-case the rest of it — "DynamoDB" → "DynamoDb", "EC2" → "Ec2",
// "CloudWatch Logs" → "CloudWatchLogs", "DBInstanceNotFound" →
// "DbInstanceNotFound".
func javaPascalCase(s string) string {
	var out strings.Builder
	for _, word := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '-' || r == '_' }) {
		word = versionSuffix.ReplaceAllString(word, "$1 V$2")
		for _, part := range strings.Fields(word) {
			for _, token := range splitWordBoundaries(part) {
				out.WriteString(strings.ToUpper(token[:1]) + strings.ToLower(token[1:]))
			}
		}
	}
	return out.String()
}

// splitWordBoundaries splits "DBInstanceNotFound" into DB, Instance, Not,
// Found: a new word starts at a lower→upper transition, and before the last
// capital of an acronym that runs into a lower-case word. Digits stay with
// the word they follow.
func splitWordBoundaries(s string) []string {
	runes := []rune(s)
	var words []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		boundary := unicode.IsLower(prev) && unicode.IsUpper(cur)
		if !boundary && unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
			boundary = true
		}
		if unicode.IsDigit(prev) && unicode.IsUpper(cur) {
			boundary = true
		}
		if boundary {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	return append(words, string(runes[start:]))
}

// ─── Definition-time validation ───────────────────────────────────────────────

// validateSDKTask rejects, at CreateStateMachine, what AWS rejects there for an
// aws-sdk Task: a Resource naming no AWS action (or a pattern SDK integrations
// do not offer), and static Parameters/Arguments naming a member the action
// does not take. Anything valid that Overcast cannot run still provisions and
// fails at run time, like every other gap.
func validateSDKTask(state *aslState, loc string) error {
	integration, serr := parseTaskResource(state.Resource)
	if serr != nil {
		return nil
	}
	service, ok := strings.CutPrefix(integration.service, "aws-sdk:")
	if !ok {
		return nil
	}
	notRecognised := invalidDefinitionf("%s: The resource provided %s is not recognized. The value is not a valid resource ARN, or the resource is not available in this region.", loc, state.Resource)
	if integration.pattern != "" && integration.pattern != patternWaitForTaskToken {
		return notRecognised
	}
	op, found := findSDKOperation(service, integration.action)
	if !found {
		return notRecognised
	}
	svc, ok, err := awsshapes.Lookup(op.Service)
	if err != nil || !ok {
		return nil
	}
	shape, ok := svc.Operation(op.Name)
	if !ok {
		return nil
	}
	if shape.HasEventStream() {
		return notRecognised
	}
	for _, raw := range []json.RawMessage{state.Parameters, state.Arguments} {
		var fields map[string]json.RawMessage
		if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil {
			continue
		}
		for field := range fields {
			name := strings.TrimSuffix(field, ".$")
			if sdkMember(shape.Input, name) == nil {
				return invalidDefinitionf("%s: The field %q is not supported by Step Functions", loc, name)
			}
		}
	}
	return nil
}

// sdkMember finds the input member a PascalCase parameter names.
func sdkMember(structure *awsshapes.Shape, param string) *awsshapes.Member {
	if structure == nil {
		return nil
	}
	for _, m := range structure.Members {
		if capitalize(m.Name) == param {
			return m
		}
	}
	return nil
}

func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
