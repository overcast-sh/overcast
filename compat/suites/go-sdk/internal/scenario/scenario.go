// Package scenario is the hand-written half of this suite's generated compat
// coverage: the runtime the emitted groups in internal/groups/scenarios_*_gen.go
// call into.
//
// The go-sdk suite is a **source-emitting** backend, not an interpreter. The
// AWS SDK for Go v2 has no public dynamic-dispatch API, so cmd/compatgen emits
// one Go function per scenario test, and each of those builds a real typed
// input struct and calls a real client method — the SDK is exercised exactly
// as production code exercises it (docs/plans/compat-coverage-modelgen.md
// §3.2). What is *not* re-emitted per test is the semantics: the context bag,
// $name/$ref evaluation, the closed check set, error matching, `eventually`
// and the six-field failure message all live here, once, and the emitted code
// is the data plus the typed calls.
//
// The normative description of every rule implemented here is
// compat/model/README.md. Where this package and that page disagree, this
// package is wrong. In particular:
//
//   - A group is setup → tests → teardown, with teardown running even after a
//     failed setup, and every teardown step wrapped individually.
//   - Assertion kinds are a closed set, and so are the checks inside them.
//   - An error clause matches by equality against a code parsed out of one of
//     the surfaces this SDK actually has, never by containment.
//   - `eventually` gives up with a fixed prefix in front of the last attempt's
//     six-field message, byte for byte identical to the three interpreters.
//   - A 501 reaches the harness as its `unimplemented` classification, via the
//     harness.ErrUnimplemented sentinel rather than a substring test over a
//     message this package composed.
package scenario

import (
	"context"
)

// The closed assertion set (compat/model/README.md § Assertions).
const (
	KindResponseField = "responseField"
	KindReadback      = "readback"
	KindListContains  = "listContains"
	KindAbsent        = "absent"
	KindErrorCode     = "errorCode"
	KindEventually    = "eventually"
)

// A Call is one API call: the operation, the typed input the emitted code
// builds, the client method that sends it, and the context paths it exports
// from its response.
type Call struct {
	// Op is the AWS operation name — failure-message field 2.
	Op string
	// Params is the call's params exactly as the scenario file writes them,
	// value expressions unevaluated. It is failure-message field 3 for a
	// failure raised *before* anything was sent (an unresolvable $ref, a value
	// that will not fit the input field): nothing went on the wire, so the
	// message shows what the file asked for. Once the input is built, field 3
	// is the built input instead, which is what was actually sent.
	Params string
	// Build fills a typed SDK input struct. It reports problems by recording
	// them on the Binder rather than by returning them, so an emitted body is
	// a flat list of assignments; Binder.err is checked once, here, before
	// anything is sent.
	Build func(b *Binder) any
	// Send invokes the client method. in is whatever Build returned.
	Send func(ctx context.Context, in any) (any, error)
	// Export maps a context path to a path in this call's own response.
	Export map[string]string
}

// A Test is one registry test: a primary call and at least one clause.
type Test struct {
	Call   Call
	Assert []Clause
}

// A Clause is one assertion. Which fields are set depends on Kind; the
// constructors below are the only way the emitter builds one, so an
// unrepresentable combination cannot be emitted.
type Clause struct {
	Kind string
	// responseField: Checks against the test's own response.
	// readback: Call is made and Checks are evaluated against its response.
	Call   *Call
	Checks []Check
	// listContains / absent (list form).
	ItemsPath string
	Where     []WhereEntry
	// errorCode, and absent (error form).
	Error *ErrorSpec
	// eventually.
	MaxAttempts int
	DelayMs     int
	Assert      *Clause
}

// CheckKind is the closed set of checks a clause may make on one path.
type CheckKind string

const (
	CheckNonEmpty CheckKind = "nonEmpty"
	CheckIsList   CheckKind = "isList"
	CheckEquals   CheckKind = "equals"
	CheckMatches  CheckKind = "matches"
	CheckMissing  CheckKind = "missing"
	// CheckEqualsJSON compares a member holding a JSON document by value.
	CheckEqualsJSON CheckKind = "equalsJSON"
)

// A Check is one check on one response path. Value carries the expected value
// for equals, the pattern for matches and the parsed document for equalsJSON,
// and is nil for the rest.
type Check struct {
	Path  string
	Kind  CheckKind
	Value any
	// operandErr is why EqualsJSON refused its document. A constructor has no
	// error to return, and the emitted groups build their checks when the
	// suite registers them, so a refusal is carried here and fails the one
	// check that holds it rather than panicking the whole run.
	operandErr error
}

// A WhereEntry is one criterion an item of a list must satisfy. "$" is the
// item itself, which is how a list of strings is matched.
type WhereEntry struct {
	Path  string
	Value any
}

// An ErrorSpec names one error two ways, because SDKs disagree about which
// they surface: the modeled shape name and the wire code. Either is accepted.
type ErrorSpec struct {
	Shape string
	Code  string
}

// ---------------------------------------------------------------------------
// Constructors — the emitter's whole vocabulary
// ---------------------------------------------------------------------------

// ResponseField checks the test's own response.
func ResponseField(checks ...Check) Clause {
	return Clause{Kind: KindResponseField, Checks: checks}
}

// Readback makes a call of its own and checks its response.
func Readback(c Call, checks ...Check) Clause {
	return Clause{Kind: KindReadback, Call: &c, Checks: checks}
}

// ListContains requires the list at itemsPath to hold a matching item. c is
// nil when the list is read from the test's own response.
func ListContains(c *Call, itemsPath string, where ...WhereEntry) Clause {
	return Clause{Kind: KindListContains, Call: c, ItemsPath: itemsPath, Where: where}
}

// AbsentFromList requires the list at itemsPath to hold no matching item. A
// missing list counts as empty.
func AbsentFromList(c *Call, itemsPath string, where ...WhereEntry) Clause {
	return Clause{Kind: KindAbsent, Call: c, ItemsPath: itemsPath, Where: where}
}

// AbsentByError requires c to fail with the named error.
func AbsentByError(c Call, want ErrorSpec) Clause {
	return Clause{Kind: KindAbsent, Call: &c, Error: &want}
}

// ErrorCode requires the test's own call to fail with the named error.
func ErrorCode(want ErrorSpec) Clause {
	return Clause{Kind: KindErrorCode, Error: &want}
}

// Eventually retries one clause until it holds, at most maxAttempts times,
// delayMs apart.
func Eventually(maxAttempts, delayMs int, inner Clause) Clause {
	return Clause{Kind: KindEventually, MaxAttempts: maxAttempts, DelayMs: delayMs, Assert: &inner}
}

// Error names an error by its modeled shape and its wire code.
func Error(shape, code string) ErrorSpec { return ErrorSpec{Shape: shape, Code: code} }

// NonEmpty holds when the path resolves to a value that is not null, "", []
// or {}. Numbers and booleans are never empty.
func NonEmpty(path string) Check { return Check{Path: path, Kind: CheckNonEmpty} }

// IsList holds when the path resolves to a list, empty or not — or does not
// resolve at all. A present value that is not a list fails it.
func IsList(path string) Check { return Check{Path: path, Kind: CheckIsList} }

// Equals holds when the path resolves and the value is equal, as JSON, to the
// evaluated expression.
func Equals(path string, want any) Check {
	return Check{Path: path, Kind: CheckEquals, Value: want}
}

// Matches holds when the path resolves to a string matching the pattern.
func Matches(path, pattern string) Check {
	return Check{Path: path, Kind: CheckMatches, Value: pattern}
}

// EqualsJSON holds when the path resolves to a JSON document equal, as a JSON
// value, to document: a string percent-decoded once and parsed as one JSON
// text, which is how this SDK hands back an IAM policy document, or an object
// or list as it stands. document is the operand as cmd/compatgen writes it —
// compact JSON text of a literal object or array — and one that is not, or
// that holds a `$`-prefixed key anywhere, fails the check when it runs.
func EqualsJSON(path, document string) Check {
	doc, err := parseEqualsJSONOperand(document)
	return Check{Path: path, Kind: CheckEqualsJSON, Value: doc, operandErr: err}
}

// Missing holds when the path does not resolve. A member the service sent as
// JSON null resolves, so Missing fails on it.
func Missing(path string) Check { return Check{Path: path, Kind: CheckMissing} }

// Where is one item criterion for ListContains or AbsentFromList.
func Where(path string, want any) WhereEntry { return WhereEntry{Path: path, Value: want} }
