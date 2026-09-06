package scenario

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	awsxml "github.com/aws/aws-sdk-go-v2/aws/protocol/xml"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	apigatewaytypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// The shared error-matching conformance fixtures, compat/model/testdata/errors.
//
// Every backend reads the same documents and must agree about which clauses
// they satisfy. Each suite writes this test once, against its own matcher, so a
// rule only one backend implements fails somewhere rather than being discovered
// when a generated group disagrees with itself across suites
// (compat/model/README.md § Errors).
//
// What this suite observes is wider than the cli suite's and the same as the
// two SDK interpreters': an exception type, the code the deserializer resolved,
// and the response header. What it cannot see is the AWS CLI's stderr banner,
// so those fixtures are skipped by name and with a reason — a silently ignored
// fixture would look exactly like a passing one.

// knownCarriers is the whole vocabulary. A fixture naming anything else is a
// typo that would otherwise skip quietly in every suite at once.
var knownCarriers = map[string]bool{
	"exceptionName":    true,
	"bodyType":         true,
	"bodyCode":         true,
	"queryErrorHeader": true,
	"cliBanner":        true,
}

// observedCarriers is what this suite can see. bodyType and bodyCode are
// observed indirectly but faithfully: the Go SDK parses the body away before
// the caller sees it, and what survives is smithy.APIError.ErrorCode(), which
// is the code the protocol deserializer read — out of __type or the code member
// for a JSON wire, and out of the error node of an XML one, whether that node
// is the Query ErrorResponse envelope's or REST XML's bare <Error> root.
var observedCarriers = map[string]bool{
	"exceptionName":    true,
	"bodyType":         true,
	"bodyCode":         true,
	"queryErrorHeader": true,
}

type errorFixture struct {
	ID       string             `json:"id"`
	Title    string             `json:"title"`
	Why      string             `json:"why"`
	Carriers []string           `json:"carriers"`
	Wire     errorFixtureWire   `json:"wire"`
	Expect   []errorFixtureCase `json:"expect"`
}

type errorFixtureWire struct {
	Status        int               `json:"status"`
	ExceptionName string            `json:"exceptionName"`
	Headers       map[string]string `json:"headers"`
	// Body is a JSON object for a JSON wire and a JSON string — the raw XML
	// bytes — for one that is not, so it is kept undecoded and read by
	// bodyCode/bodyMessage below (compat/model/README.md § Errors).
	Body   json.RawMessage `json:"body"`
	Stderr string          `json:"stderr"`
}

type errorFixtureCase struct {
	Name string `json:"name"`
	// The fixture spells a clause's two halves in the IR's lowercase field
	// names, which encoding/json matches case-insensitively onto ErrorSpec's.
	Error   ErrorSpec `json:"error"`
	Matches bool      `json:"matches"`
	Via     string    `json:"via"`
}

// modeledErrors maps a fixture's exceptionName to the real SDK type of that
// name. These are genuine smithy-go-generated exception types rather than
// stand-ins written here, so the fixture is answered by the same values a run
// against Overcast produces — the type name and the ErrorCode() are the SDK's,
// not the test's.
//
// A fixture naming an exception with no entry fails rather than skipping: the
// list is short and adding to it is a line, while a quiet skip would hide a
// carrier this suite claims to observe.
var modeledErrors = map[string]error{
	"QueueDoesNotExist":         &sqstypes.QueueDoesNotExist{},
	"PolicyNotFoundException":   &orgtypes.PolicyNotFoundException{},
	"ResourceNotFoundException": &dynamodbtypes.ResourceNotFoundException{},
	"NotFoundException":         &apigatewaytypes.NotFoundException{},
}

// asSDKError renders a fixture the way this suite would have observed it: the
// SDK's modeled exception where the wire names one, else the generic API error
// its deserializer mints, wrapped in the transport and operation errors the SDK
// really wraps them in — which is what puts the response header three links
// down the chain, where the matcher has to go and find it.
func (f errorFixture) asSDKError(t *testing.T) error {
	t.Helper()
	if f.Wire.Stderr != "" {
		// No HTTP exchange at all: the SDK failed before the wire. Nothing
		// states a code, which is exactly what cli-no-parseable-code pins.
		return fmt.Errorf("operation error SQS: DeleteQueue, %s", f.Wire.Stderr)
	}

	var apiErr error
	if f.Wire.ExceptionName != "" {
		modeled, ok := modeledErrors[f.Wire.ExceptionName]
		if !ok {
			t.Fatalf("no SDK exception type for %q; add one to modeledErrors so the exceptionName carrier is really observed", f.Wire.ExceptionName)
		}
		apiErr = modeled
	} else {
		// The body's own code member, unsanitised: splitting a Smithy id at
		// "#" is the matcher's job, and pre-splitting it here would test the
		// fixture rather than the matcher.
		apiErr = &smithy.GenericAPIError{Code: bodyCode(f.Wire.Body), Message: bodyMessage(f.Wire.Body)}
	}

	header := http.Header{}
	for name, value := range f.Wire.Headers {
		header.Set(name, value)
	}
	resp := &smithyhttp.Response{Response: &http.Response{StatusCode: f.Wire.Status, Header: header}}
	transport := &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{Response: resp, Err: apiErr},
		RequestID:     "fixture-request-id",
	}
	return &smithy.OperationError{ServiceID: "SQS", OperationName: "DeleteQueue", Err: transport}
}

// bodyCode is the code this suite's SDK would have resolved out of the error
// body — the value that reaches the matcher as smithy.APIError.ErrorCode().
//
// A JSON body states it at the top level, in one of the three spellings
// compat/model/README.md § Errors fixes. An XML one states it inside an error
// node instead, and the code is resolved there by the SDK's own
// awsxml.GetErrorResponseComponents — the function every generated Query and
// REST XML deserializer calls, so what this returns for an XML wire is what a
// live failure against a Query service really carries rather than a second
// reading written here.
func bodyCode(body json.RawMessage) string {
	if components, ok := xmlErrorComponents(body); ok {
		return components.Code
	}
	members := jsonBody(body)
	for _, key := range []string{"__type", "Code", "code"} {
		if v, ok := members[key].(string); ok {
			return v
		}
	}
	return ""
}

func bodyMessage(body json.RawMessage) string {
	if components, ok := xmlErrorComponents(body); ok {
		return components.Message
	}
	if v, ok := jsonBody(body)["message"].(string); ok {
		return v
	}
	return ""
}

// xmlErrorComponents deserializes an XML error body the way the SDK does, or
// reports false for a wire whose body is JSON.
//
// The fixture spells a non-JSON body as a JSON string holding its raw bytes,
// so a body that decodes to a string is the raw wire and one that decodes to
// an object is already parsed. Which of GetErrorResponseComponents' two modes
// applies is the noErrorWrapping argument the generated deserializers pass:
// REST XML's bare <Error> root is true, and the Query protocol's
// <ErrorResponse> wrapper around one is false.
func xmlErrorComponents(body json.RawMessage) (awsxml.ErrorComponents, bool) {
	var raw string
	if err := json.Unmarshal(body, &raw); err != nil {
		return awsxml.ErrorComponents{}, false
	}
	components, err := awsxml.GetErrorResponseComponents(strings.NewReader(raw), bareErrorRoot(raw))
	if err != nil {
		return awsxml.ErrorComponents{}, true
	}
	return components, true
}

// jsonBody decodes an object body, or returns nothing for a body that is
// absent or not an object.
func jsonBody(body json.RawMessage) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}

// bareErrorRoot reports whether an XML error body is REST XML's bare <Error>
// root rather than the Query protocol's <ErrorResponse> wrapper around one.
func bareErrorRoot(raw string) bool {
	decoder := xml.NewDecoder(strings.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if start, ok := token.(xml.StartElement); ok {
			return start.Name.Local == "Error"
		}
	}
}

func TestSharedErrorFixtures(t *testing.T) {
	root := repoRootFromTest(t)
	dir := filepath.Join(root, "compat", "model", "testdata", "errors")
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatalf("no fixtures in %s: the shared conformance set may not be skipped by deleting it", dir)
	}
	sort.Strings(names)

	checked := 0
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var f errorFixture
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		t.Run(f.ID, func(t *testing.T) {
			for _, c := range f.Carriers {
				if !knownCarriers[c] {
					t.Fatalf("unknown carrier %q; the vocabulary is fixed by compat/model/README.md § Errors", c)
				}
			}
			if !observesAny(f.Carriers) {
				t.Skipf("the go-sdk suite reads none of this fixture's surfaces (%s): the AWS CLI's stderr banner never reaches an SDK caller",
					strings.Join(f.Carriers, ", "))
			}
			observed := f.asSDKError(t)
			for _, c := range f.Expect {
				t.Run(c.Name, func(t *testing.T) {
					if c.Matches && !observedCarriers[c.Via] {
						t.Skipf("this expectation matches through %q, which the go-sdk suite does not observe", c.Via)
					}
					checked++
					if got := matchesError(observed, &c.Error); got != c.Matches {
						t.Errorf("matchesError(%v, {shape:%q, code:%q}) = %v, want %v",
							observed, c.Error.Shape, c.Error.Code, got, c.Matches)
					}
				})
			}
		})
	}
	if checked == 0 {
		t.Fatal("every fixture was skipped: this suite is asserting nothing about error matching")
	}
}

// observesAny reports whether this suite can see any surface the fixture states
// the code on. A fixture that states none — `carriers: []` — is observed by
// everyone: there is nothing to miss, and its expectations are all negative, so
// a suite that cannot see the wire at all still answers them correctly.
func observesAny(carriers []string) bool {
	if len(carriers) == 0 {
		return true
	}
	for _, c := range carriers {
		if observedCarriers[c] {
			return true
		}
	}
	return false
}

// TestModeledSDKErrorSurfacesItsShapeName pins the assumption the exceptionName
// surface rests on: smithy-go names the Go type after the modeled shape, and
// reads the same string back out of ErrorCode(). If an SDK release ever broke
// that, the fixtures above would still pass — they would simply be matching
// through the other surface — so it is asserted separately.
func TestModeledSDKErrorSurfacesItsShapeName(t *testing.T) {
	for _, tc := range []struct {
		err   error
		shape string
	}{
		{&sqstypes.QueueDoesNotExist{}, "QueueDoesNotExist"},
		{&orgtypes.PolicyNotFoundException{}, "PolicyNotFoundException"},
	} {
		if got := modeledTypeName(tc.err); got != tc.shape {
			t.Errorf("modeledTypeName(%T) = %q, want %q", tc.err, got, tc.shape)
		}
		api, ok := tc.err.(smithy.APIError)
		if !ok {
			t.Fatalf("%T does not implement smithy.APIError", tc.err)
		}
		if got := api.ErrorCode(); got != tc.shape {
			t.Errorf("%T.ErrorCode() = %q, want %q", tc.err, got, tc.shape)
		}
	}
}

// TestGenericAPIErrorIsNotAnExceptionName keeps the exceptionName surface from
// answering with the name of a type that is not a modeled shape.
// *smithy.GenericAPIError is what the SDK mints when it cannot model the error,
// and a clause naming "GenericAPIError" must never be satisfied by one.
func TestGenericAPIErrorIsNotAnExceptionName(t *testing.T) {
	err := &smithy.GenericAPIError{Code: "SomethingElse"}
	if got := modeledTypeName(err); got != "" {
		t.Errorf("modeledTypeName(*smithy.GenericAPIError) = %q, want no name", got)
	}
	if matchesError(err, &ErrorSpec{Shape: "GenericAPIError", Code: "GenericAPIError"}) {
		t.Error("a clause naming smithy-go's own catch-all type matched it")
	}
}

// fixturesRequiredEnvVar is set to "1" only by test.yml's
// compat-suite-unit-tests job, which runs this test from a full checkout
// where the corpus is always reachable. Its absence there would mean the
// shared conformance set silently stopped being checked anywhere — see
// compat/AGENTS.md § Where the shared error corpus runs.
const fixturesRequiredEnvVar = "OVERCAST_COMPAT_FIXTURES_REQUIRED"

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	start, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := start
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "compat", "model", "scenarios")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if os.Getenv(fixturesRequiredEnvVar) == "1" {
		t.Fatalf("%s=1 but no compat/model found walking up from %s — this suite's fixture test must run from a full checkout (test.yml's compat-suite-unit-tests job)",
			fixturesRequiredEnvVar, start)
	}
	t.Skipf("no compat/model above %s; nothing to check (set %s=1 to make this fatal)", start, fixturesRequiredEnvVar)
	return ""
}
