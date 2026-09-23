package lambda_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func assertLambdaValidationMessage(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	var body struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	decodeJSON(t, resp, &body)
	if body.Type != "InvalidParameterValueException" || body.Message != want {
		t.Fatalf("validation error = (%q, %q), want (%q, %q)", body.Type, body.Message, "InvalidParameterValueException", want)
	}
}

func assertLambdaUnsupported(t *testing.T, resp *http.Response) {
	t.Helper()
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	if got := resp.Header.Get("x-emulator-unsupported"); got != "true" {
		t.Fatalf("x-emulator-unsupported = %q, want true", got)
	}
	var body struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	decodeJSON(t, resp, &body)
	if body.Type != "NotImplemented" || body.Message != "This operation is not yet emulated. Check docs/services/ for the support matrix." {
		t.Fatalf("unsupported error = (%q, %q)", body.Type, body.Message)
	}
}

func TestFunctionConfiguration_ImageConfigUsesAWSResponseEnvelope(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
		"FunctionName": "image-response-envelope", "Role": "arn:aws:iam::000000000000:role/r",
		"PackageType": "Image", "Code": map[string]any{"ImageUri": "000000000000.dkr.ecr.us-east-1.amazonaws.com/image:latest"},
		"ImageConfig": map[string]any{"Command": []string{"handler"}},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var raw map[string]json.RawMessage
	decodeJSON(t, resp, &raw)
	if _, found := raw["ImageConfig"]; found {
		t.Fatal("AWS FunctionConfiguration must not contain top-level ImageConfig")
	}
	var envelope imageConfigResponse
	if err := json.Unmarshal(raw["ImageConfigResponse"], &envelope); err != nil {
		t.Fatalf("decode ImageConfigResponse: %v", err)
	}
	if envelope.ImageConfig == nil || len(envelope.ImageConfig.Command) != 1 || envelope.ImageConfig.Command[0] != "handler" {
		t.Fatalf("ImageConfigResponse = %#v", envelope)
	}
}

func TestUpdateFunctionConfiguration_IntegerValidationMessagesAndBoundaries(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "integer-validation")
	tests := []struct {
		name   string
		member string
		value  int
		want   string
	}{
		{"timeout below minimum", "Timeout", 0, "1 validation error detected: Value '0' at 'timeout' failed to satisfy constraint: Member must have value greater than or equal to 1"},
		{"timeout above maximum", "Timeout", 901, "1 validation error detected: Value '901' at 'timeout' failed to satisfy constraint: Member must have value less than or equal to 900"},
		{"memory below minimum", "MemorySize", 127, "1 validation error detected: Value '127' at 'memorySize' failed to satisfy constraint: Member must have value greater than or equal to 128"},
		{"memory above maximum", "MemorySize", 32769, "1 validation error detected: Value '32769' at 'memorySize' failed to satisfy constraint: Member must have value less than or equal to 32768"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/integer-validation/configuration"), map[string]any{tc.member: tc.value})
			assertLambdaValidationMessage(t, resp, tc.want)
		})
	}
	for _, body := range []map[string]any{{"Timeout": 1, "MemorySize": 128}, {"Timeout": 900, "MemorySize": 32768}} {
		resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/integer-validation/configuration"), body)
		helpers.AssertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}
}

func TestCreateFunction_IntegerValidationMessagesFailBeforeCreation(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value int
		want  string
	}{
		{"timeout zero", "Timeout", 0, "1 validation error detected: Value '0' at 'timeout' failed to satisfy constraint: Member must have value greater than or equal to 1"},
		{"timeout negative", "Timeout", -1, "1 validation error detected: Value '-1' at 'timeout' failed to satisfy constraint: Member must have value greater than or equal to 1"},
		{"memory zero", "MemorySize", 0, "1 validation error detected: Value '0' at 'memorySize' failed to satisfy constraint: Member must have value greater than or equal to 128"},
		{"memory negative", "MemorySize", -1, "1 validation error detected: Value '-1' at 'memorySize' failed to satisfy constraint: Member must have value greater than or equal to 128"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			name := "invalid-create-" + strings.ReplaceAll(tc.name, " ", "-")
			request := map[string]any{
				"FunctionName": name, "Runtime": "nodejs20.x", "Handler": "index.handler",
				"Role": "arn:aws:iam::000000000000:role/r", "Code": map[string]any{"ZipFile": []byte("zip")}, tc.field: tc.value,
			}
			assertLambdaValidationMessage(t, doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), request), tc.want)
			missing := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/"+name+"/configuration"), nil)
			helpers.AssertStatus(t, missing, http.StatusNotFound)
			missing.Body.Close()
		})
	}
}

func TestCreateFunction_ModeledValidationPrecedesDuplicateLookup(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "duplicate-validation-order")
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
		"FunctionName": "duplicate-validation-order", "Runtime": "nodejs20.x", "Handler": "index.handler",
		"Role": "arn:aws:iam::000000000000:role/r", "Code": map[string]any{"ZipFile": []byte("zip")}, "Timeout": 0,
	})
	assertLambdaValidationMessage(t, resp,
		"1 validation error detected: Value '0' at 'timeout' failed to satisfy constraint: Member must have value greater than or equal to 1")
}

func TestUpdateFunctionConfiguration_HandlerAndRolePatternMessages(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "string-pattern-validation")
	tests := []struct {
		field string
		want  string
	}{
		{"Handler", "1 validation error detected: Value '' at 'handler' failed to satisfy constraint: Member must satisfy regular expression pattern: [^\\s]+"},
		{"Role", "1 validation error detected: Value '' at 'role' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:(aws[a-zA-Z-]*)?:iam::\\d{12}:role/?[a-zA-Z_0-9+=,.@\\-_/]+"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			assertLambdaValidationMessage(t,
				doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/string-pattern-validation/configuration"), map[string]any{tc.field: ""}), tc.want)
		})
	}
}

func TestPutFunctionConcurrency_ValidationMessagesAndBoundary(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-validation")
	url := srv.URL + "/2017-10-31/functions/concurrency-validation/concurrency"
	assertLambdaValidationMessage(t, doJSON(t, http.MethodPut, url, map[string]any{}),
		"1 validation error detected: Value null at 'reservedConcurrentExecutions' failed to satisfy constraint: Member must not be null")
	assertLambdaValidationMessage(t, doJSON(t, http.MethodPut, url, map[string]any{"ReservedConcurrentExecutions": -1}),
		"1 validation error detected: Value '-1' at 'reservedConcurrentExecutions' failed to satisfy constraint: Member must have value greater than or equal to 0")
	resp := doJSON(t, http.MethodPut, url, map[string]any{"ReservedConcurrentExecutions": 0})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

func TestFunctionConfiguration_CollectionBoundaries(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "collection-boundaries")
	stringsOf := func(count int, value string) []string {
		out := make([]string, count)
		for i := range out {
			out[i] = value
		}
		return out
	}

	valid := map[string]any{
		"VpcConfig":   map[string]any{"SubnetIds": stringsOf(16, "subnet-0123456789abcdef0"), "SecurityGroupIds": stringsOf(5, "sg-0123456789abcdef0")},
		"ImageConfig": map[string]any{"EntryPoint": stringsOf(1500, "entry"), "Command": stringsOf(1500, "arg"), "WorkingDirectory": strings.Repeat("w", 1000)},
	}
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/collection-boundaries/configuration"), valid)
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	invalid := []struct {
		name string
		body map[string]any
		want string
	}{
		{"too many subnets", map[string]any{"VpcConfig": map[string]any{"SubnetIds": stringsOf(17, "subnet-0123456789abcdef0")}}, "1 validation error detected: Value '17' at 'vpcConfig.subnetIds' failed to satisfy constraint: Member must have length less than or equal to 16"},
		{"too many security groups", map[string]any{"VpcConfig": map[string]any{"SecurityGroupIds": stringsOf(6, "sg-0123456789abcdef0")}}, "1 validation error detected: Value '6' at 'vpcConfig.securityGroupIds' failed to satisfy constraint: Member must have length less than or equal to 5"},
		{"too many entry point items", map[string]any{"ImageConfig": map[string]any{"EntryPoint": stringsOf(1501, "entry")}}, "1 validation error detected: Value '1501' at 'imageConfig.entryPoint' failed to satisfy constraint: Member must have length less than or equal to 1500"},
		{"too many command items", map[string]any{"ImageConfig": map[string]any{"Command": stringsOf(1501, "arg")}}, "1 validation error detected: Value '1501' at 'imageConfig.command' failed to satisfy constraint: Member must have length less than or equal to 1500"},
		{"working directory too long", map[string]any{"ImageConfig": map[string]any{"WorkingDirectory": strings.Repeat("w", 1001)}}, "1 validation error detected: Value '1001' at 'imageConfig.workingDirectory' failed to satisfy constraint: Member must have length less than or equal to 1000"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/collection-boundaries/configuration"), tc.body)
			assertLambdaValidationMessage(t, resp, tc.want)
		})
	}
}

// A modeled member Overcast does not implement is refused only when the caller
// actually asked for it. AWS's own default for every gated flag below is the
// falsy value, and SDKs, CloudFormation clear-values and hand-written clients
// all send those explicitly — `"Publish": false` is a request to do nothing, so
// refusing it makes an ordinary no-op call fail.
func TestUnsupportedFields_ExplicitNoOpValuesAreNotRequests(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "explicit-noop-fn")

	t.Run("CreateFunction", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
			"FunctionName": "explicit-noop-create", "Runtime": "python3.12", "Handler": "index.handler",
			"Role": "arn:aws:iam::000000000000:role/lambda-role",
			"Code": map[string]any{"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA=="},
			// Every one of these is a still-gated member whose value means
			// "leave it alone". (TracingConfig, EphemeralStorage and KMSKeyArn
			// are no longer gated at all — they are stored and echoed, and
			// tracing_storage_kms_test.go owns them; nor is DeadLetterConfig,
			// which dead_letter_config_test.go owns.)
			"Publish": false, "PublishTo": nil,
			"SnapStart": map[string]any{}, "DurableConfig": map[string]any{},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusCreated)
	})

	t.Run("UpdateFunctionCode", func(t *testing.T) {
		resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/explicit-noop-fn/code"), map[string]any{
			"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA==", "DryRun": false, "Publish": false,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
	})

	t.Run("CreateEventSourceMapping", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/event-source-mappings/"), map[string]any{
			"FunctionName": "explicit-noop-fn", "EventSourceArn": sqsARN("explicit-noop-queue"),
			// CloudFormation sends exactly these to clear an ESM's optional
			// source configuration back to its default. FunctionResponseTypes
			// is no longer one of them — it is implemented, so an empty list is
			// stored as "report nothing" rather than waved through.
			"SourceAccessConfigurations": []any{},
			"Topics":                     []any{}, "Queues": []any{}, "MetricsConfig": map[string]any{},
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusAccepted)
	})

	t.Run("PublishVersion", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/explicit-noop-fn/versions"), map[string]any{
			"PublishTo": nil,
		})
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusCreated)
	})
}

// The other half of the same rule: a value that genuinely asks for an
// unimplemented feature must still be an honest 501, not a silent success.
func TestUnsupportedFields_RealRequestsStill501(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "real-request-fn")

	t.Run("CreateFunction", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
			"FunctionName": "real-request-create", "Runtime": "python3.12", "Handler": "index.handler",
			"Role": "arn:aws:iam::000000000000:role/lambda-role",
			"Code": map[string]any{"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA=="},
			// Publish itself is implemented now; PublishTo is the member of
			// the pair that still asks for something Overcast does not have.
			"PublishTo": "LATEST_PUBLISHED",
		})
		assertLambdaUnsupported(t, resp)
	})

	t.Run("UpdateFunctionCode", func(t *testing.T) {
		resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/real-request-fn/code"), map[string]any{
			"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA==", "DryRun": true,
		})
		assertLambdaUnsupported(t, resp)
	})

	t.Run("CreateEventSourceMapping", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/event-source-mappings/"), map[string]any{
			"FunctionName": "real-request-fn", "EventSourceArn": sqsARN("real-request-queue"),
			"ParallelizationFactor": 2,
		})
		assertLambdaUnsupported(t, resp)
	})

	t.Run("PublishVersion", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/real-request-fn/versions"), map[string]any{
			"PublishTo": "LATEST_PUBLISHED",
		})
		assertLambdaUnsupported(t, resp)
	})
}

func TestAddPermission_FieldSpecificValidationMessages(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "policy-message-validation")
	base := map[string]any{"StatementId": "statement", "Action": "lambda:InvokeFunction", "Principal": "sns.amazonaws.com"}
	longStatementID := strings.Repeat("s", 101)
	longAction := "lambda:" + strings.Repeat("A", 9994)
	longPrincipal := strings.Repeat("p", 2049)
	longSourceARN := "arn:aws:sns:us-east-1:000000000000:" + strings.Repeat("r", 1025)
	longOrgID := "o-" + strings.Repeat("a", 33)
	longToken := strings.Repeat("t", 257)
	tests := []struct {
		name  string
		field string
		value string
		want  string
	}{
		{"statement ID", "StatementId", "bad id", "1 validation error detected: Value 'bad id' at 'statementId' failed to satisfy constraint: Member must satisfy regular expression pattern: ([a-zA-Z0-9-_]+)"},
		{"statement ID length", "StatementId", longStatementID, "1 validation error detected: Value '" + longStatementID + "' at 'statementId' failed to satisfy constraint: Member must have length less than or equal to 100"},
		{"action", "Action", "s3:GetObject", "1 validation error detected: Value 's3:GetObject' at 'action' failed to satisfy constraint: Member must satisfy regular expression pattern: (lambda:[*]|lambda:[a-zA-Z]+|[*])"},
		{"action length", "Action", longAction, "1 validation error detected: Value '" + longAction + "' at 'action' failed to satisfy constraint: Member must have length less than or equal to 10000"},
		{"principal", "Principal", "bad principal", "1 validation error detected: Value 'bad principal' at 'principal' failed to satisfy constraint: Member must satisfy regular expression pattern: [^\\s]+"},
		{"principal length", "Principal", longPrincipal, "1 validation error detected: Value '" + longPrincipal + "' at 'principal' failed to satisfy constraint: Member must have length less than or equal to 2048"},
		{"source account", "SourceAccount", "123", "1 validation error detected: Value '123' at 'sourceAccount' failed to satisfy constraint: Member must satisfy regular expression pattern: \\d{12}"},
		{"source account length", "SourceAccount", "1234567890123", "1 validation error detected: Value '1234567890123' at 'sourceAccount' failed to satisfy constraint: Member must have length less than or equal to 12"},
		{"source ARN", "SourceArn", "not-an-arn", "1 validation error detected: Value 'not-an-arn' at 'sourceArn' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:(aws[a-zA-Z0-9-]*):([a-zA-Z0-9\\-])+:([a-z]{2}(-gov)?-[a-z]+-\\d{1})?:(\\d{12})?:(.*)"},
		{"source ARN length", "SourceArn", longSourceARN, "1 validation error detected: Value '" + longSourceARN + "' at 'sourceArn' failed to satisfy constraint: Member must have length less than or equal to 1024"},
		{"organization minimum length", "PrincipalOrgID", "org", "1 validation error detected: Value 'org' at 'principalOrgID' failed to satisfy constraint: Member must have length greater than or equal to 12"},
		{"organization maximum length", "PrincipalOrgID", longOrgID, "1 validation error detected: Value '" + longOrgID + "' at 'principalOrgID' failed to satisfy constraint: Member must have length less than or equal to 34"},
		{"event source token", "EventSourceToken", "bad token", "1 validation error detected: Value 'bad token' at 'eventSourceToken' failed to satisfy constraint: Member must satisfy regular expression pattern: [a-zA-Z0-9._\\-]+"},
		{"event source token length", "EventSourceToken", longToken, "1 validation error detected: Value '" + longToken + "' at 'eventSourceToken' failed to satisfy constraint: Member must have length less than or equal to 256"},
		{"function URL auth", "FunctionUrlAuthType", "PUBLIC", "1 validation error detected: Value 'PUBLIC' at 'functionUrlAuthType' failed to satisfy constraint: Member must satisfy enum value set: [NONE, AWS_IAM]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := make(map[string]any, len(base)+1)
			for key, value := range base {
				request[key] = value
			}
			request[tc.field] = tc.value
			assertLambdaValidationMessage(t, doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/policy-message-validation/policy"), request), tc.want)
		})
	}
}

func TestAddPermission_missingStatementID(t *testing.T) {
	// Given: a function and an otherwise valid AddPermission request.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "policy-missing-statement")

	// When: StatementId is omitted.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/policy-missing-statement/policy"), map[string]any{
		"Action": "lambda:InvokeFunction", "Principal": "sns.amazonaws.com",
	})

	// Then: Lambda reports the missing required member, not its pattern constraint.
	assertLambdaValidationMessage(t, resp,
		"1 validation error detected: Value null at 'statementId' failed to satisfy constraint: Member must not be null")
}

func TestAddPermission_emptyStatementID(t *testing.T) {
	// Given: a function and an otherwise valid AddPermission request.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "policy-empty-statement")

	// When: StatementId is explicitly empty rather than omitted.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/policy-empty-statement/policy"), map[string]any{
		"StatementId": "", "Action": "lambda:InvokeFunction", "Principal": "sns.amazonaws.com",
	})

	// Then: Lambda reports the minimum-length constraint for the supplied value.
	assertLambdaValidationMessage(t, resp,
		"1 validation error detected: Value '' at 'statementId' failed to satisfy constraint: Member must have length greater than or equal to 1")
}

func TestFunctionPolicy_IdentifierLengthMessages(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "policy-identifier-length")
	request := map[string]any{"StatementId": "statement", "Action": "lambda:InvokeFunction", "Principal": "sns.amazonaws.com"}

	longName := strings.Repeat("n", 65)
	assertLambdaValidationMessage(t,
		doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/"+longName+"/policy"), request),
		"1 validation error detected: Value '"+longName+"' at 'functionName' failed to satisfy constraint: Member must have length less than or equal to 64")

	longQualifier := strings.Repeat("q", 129)
	assertLambdaValidationMessage(t,
		doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/policy-identifier-length/policy")+"?Qualifier="+longQualifier, request),
		"1 validation error detected: Value '"+longQualifier+"' at 'qualifier' failed to satisfy constraint: Member must have length less than or equal to 128")
}
