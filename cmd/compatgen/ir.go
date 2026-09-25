//go:build dev

package main

import (
	"fmt"
)

// The scenario IR.
//
// This is the contract between the generator and the interpreters (python-sdk,
// node-js-sdk, cli) and source emitters (the typed SDK suites). It is
// documented for interpreter authors in compat/model/README.md and validated
// by compat/model/scenario.schema.json; the types here are the same shape, and
// the emit path is the only way to construct a test, which is what makes a
// vacuous test unrepresentable: newTest takes its first assertion as a
// non-optional argument.

const scenarioVersion = 1

// Group kinds.
const (
	groupLifecycle = "lifecycle"
	groupProbe     = "probe"
)

// Assertion kinds — the closed set from docs/plans/compat-coverage-modelgen.md §3.4.
const (
	assertResponseField = "responseField"
	assertReadback      = "readback"
	assertListContains  = "listContains"
	assertAbsent        = "absent"
	assertErrorCode     = "errorCode"
	assertEventually    = "eventually"
)

type scenario struct {
	// Comment is prose an authored scenario carries and a generated one never
	// does: the emitters and interpreters ignore it, and the generator writes
	// none, so byte-identical regeneration is unaffected. It exists because an
	// authored scenario is the review artifact for a group that used to be
	// seven per-language implementations, and a review artifact that cannot
	// say why a clause is what it is is worse than the code it replaced.
	Comment string     `json:"$comment,omitempty"`
	Version int        `json:"version"`
	Service string     `json:"service"`
	Client  clientInfo `json:"client"`
	Groups  []group    `json:"groups"`
}

// clientInfo is what an interpreter needs to construct a client for the
// service without a naming table of its own (§7.3). Per-SDK package names are
// deliberately absent; the README says how each backend derives them.
type clientInfo struct {
	SDKID          string `json:"sdkId"`
	EndpointPrefix string `json:"endpointPrefix"`
	SigningName    string `json:"signingName,omitempty"`
	Protocol       string `json:"protocol"`
	APIVersion     string `json:"apiVersion"`
	TargetPrefix   string `json:"targetPrefix,omitempty"`
	// AWSQueryCompatible is emitted for every service, true or false, because
	// its absence would be indistinguishable from "this scenario predates the
	// field" — and it decides which error codes an interpreter accepts.
	AWSQueryCompatible bool `json:"awsQueryCompatible"`
}

type group struct {
	// Comment is prose; see scenario.Comment.
	Comment string `json:"$comment,omitempty"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	// Parallel says the group's tests may run concurrently with one another.
	// Only a probe group carries it, and every probe group does: a probe has
	// no setup, no teardown and no exports (README § What a probe may bind),
	// so nothing orders its tests. A lifecycle group's tests hand resources to
	// one another through the context bag and must stay in order.
	// validateScenario pins both halves.
	Parallel bool   `json:"parallel,omitempty"`
	Setup    []call `json:"setup"`
	Tests    []test `json:"tests"`
	Teardown []call `json:"teardown"`
}

// call is one API call: an operation, its params (a value — see expr.go), and
// the context paths it exports from its response.
type call struct {
	Op     string            `json:"op"`
	Params map[string]any    `json:"params"`
	Export map[string]string `json:"export,omitempty"`
}

// test is one registry test: a primary call and at least one assertion.
type test struct {
	// Comment is prose; see scenario.Comment.
	Comment string      `json:"$comment,omitempty"`
	Name    string      `json:"name"`
	Op      string      `json:"op"`
	Call    call        `json:"call"`
	Assert  []assertion `json:"assert"`
	Depends []string    `json:"depends,omitempty"`
}

// assertion is one clause. Which fields are set depends on Kind; the README
// and the schema pin the combinations, and validateAssertion enforces them.
type assertion struct {
	// Comment is accepted in recipes and dropped on emission: the IR carries
	// no prose, and the recipe is where a human explains a clause.
	Comment string `json:"$comment,omitempty"`
	Kind    string `json:"kind"`
	// responseField: Checks against the test's own response.
	// readback: Call is made and Checks are evaluated against its response.
	Call   *call            `json:"call,omitempty"`
	Checks map[string]check `json:"checks,omitempty"`
	// listContains / absent (list form): the list at ItemsPath (of Call's
	// response, or the test's own response when Call is absent) must contain
	// / must not contain an item matching every Where entry.
	ItemsPath string         `json:"itemsPath,omitempty"`
	Where     map[string]any `json:"where,omitempty"`
	// errorCode: the test's own call must fail with Error.
	// absent (error form): Call must fail with Error.
	Error *errorSpec `json:"error,omitempty"`
	// eventually: retry Assert until it passes, at most MaxAttempts times,
	// DelayMs apart.
	MaxAttempts int        `json:"maxAttempts,omitempty"`
	DelayMs     int        `json:"delayMs,omitempty"`
	Assert      *assertion `json:"assert,omitempty"`
}

// check is exactly one of: nonEmpty, isList, equals, equalsJSON, matches,
// missing.
//
// isList exists because nonEmpty cannot say "this is a page of results" — a
// single-page List* legally returns an empty one, so nonEmpty on a list the
// test did not populate is false by construction. isList also holds when the
// path does not resolve at all: several AWS services omit an empty list
// member instead of serializing [] (SQS's ListQueues among them), and a
// missing list already counts as empty for absent and listContains, so isList
// treats it the same way. A present value that is not a list still fails it —
// isList is the strongest check that is true of a correct answer, present or
// omitted, and false of everything else.
//
// equalsJSON compares a string member that holds a JSON document by value,
// because the SDKs disagree about what such a member is: IAM sends a policy
// document as percent-encoded JSON text, botocore hands python-sdk and cli the
// parsed object, and the other five backends see the string. Its operand is a
// literal JSON object or array, never evaluated (validateEqualsJSON).
type check struct {
	NonEmpty   bool   `json:"nonEmpty,omitempty"`
	IsList     bool   `json:"isList,omitempty"`
	Equals     any    `json:"equals,omitempty"`
	EqualsJSON any    `json:"equalsJSON,omitempty"`
	Matches    string `json:"matches,omitempty"`
	Missing    bool   `json:"missing,omitempty"`
}

// errorSpec names an error two ways, because SDKs disagree on which they
// surface: the modeled shape name, and the wire code (the awsQueryError code
// where the service declares one, else the shape name again). An interpreter
// accepts either.
type errorSpec struct {
	Shape string `json:"shape"`
	Code  string `json:"code"`
}

// newTest is the only constructor for a test, and it cannot build one without
// an assertion.
func newTest(name, op string, primary call, first assertion, rest ...assertion) test {
	return test{Name: name, Op: op, Call: primary, Assert: append([]assertion{first}, rest...)}
}

// Assertion constructors, so the emitter never assembles a clause by hand.

func responseField(checks map[string]check) assertion {
	return assertion{Kind: assertResponseField, Checks: checks}
}

func readback(c call, checks map[string]check) assertion {
	return assertion{Kind: assertReadback, Call: &c, Checks: checks}
}

func listContains(c *call, itemsPath string, where map[string]any) assertion {
	return assertion{Kind: assertListContains, Call: c, ItemsPath: itemsPath, Where: where}
}

func absentFromList(c *call, itemsPath string, where map[string]any) assertion {
	return assertion{Kind: assertAbsent, Call: c, ItemsPath: itemsPath, Where: where}
}

func absentByError(c call, err errorSpec) assertion {
	return assertion{Kind: assertAbsent, Call: &c, Error: &err}
}

func eventually(inner assertion, retry retrySpec) assertion {
	return assertion{Kind: assertEventually, MaxAttempts: retry.MaxAttempts, DelayMs: retry.DelayMs, Assert: &inner}
}

func nonEmpty() check         { return check{NonEmpty: true} }
func isList() check           { return check{IsList: true} }
func equals(value any) check  { return check{Equals: value} }
func matches(re string) check { return check{Matches: re} }

// equalsJSON is the document check. Its operand is not a value expression, so
// nothing here evaluates or binds it; validateEqualsJSON holds it to the
// grammar every backend reads.
func equalsJSON(document any) check { return check{EqualsJSON: document} }
func missing() check                { return check{Missing: true} }
func checks(path string, c check) map[string]check {
	return map[string]check{path: c}
}

// validateScenario re-checks the structural invariants on a finished
// scenario. The constructors already make them hold; this is the belt to their
// braces, and it is what the sync test runs over the committed corpus.
func validateScenario(s *scenario) error {
	if s.Version != scenarioVersion {
		return fmt.Errorf("scenario version %d, want %d", s.Version, scenarioVersion)
	}
	seenGroups := make(map[string]struct{})
	for _, g := range s.Groups {
		if _, dup := seenGroups[g.Name]; dup {
			return fmt.Errorf("group %s declared twice", g.Name)
		}
		seenGroups[g.Name] = struct{}{}
		if g.Kind != groupLifecycle && g.Kind != groupProbe {
			return fmt.Errorf("group %s: kind %q", g.Name, g.Kind)
		}
		// parallel is a property of the kind, both ways round. A lifecycle
		// group that acquired it would have its tests raced against the
		// exports they consume; a probe group that lost it would silently give
		// back the wall clock the flag exists to buy, with nothing failing.
		if g.Parallel != (g.Kind == groupProbe) {
			return fmt.Errorf("group %s: kind %q carries parallel=%t; only a probe group is parallel, and every probe group is",
				g.Name, g.Kind, g.Parallel)
		}
		if len(g.Tests) == 0 {
			return fmt.Errorf("group %s has no tests", g.Name)
		}
		seenTests := make(map[string]struct{})
		for _, t := range g.Tests {
			if _, dup := seenTests[t.Name]; dup {
				return fmt.Errorf("group %s: test %s declared twice", g.Name, t.Name)
			}
			seenTests[t.Name] = struct{}{}
			if len(t.Assert) == 0 {
				return fmt.Errorf("group %s: test %s has no assertion clause", g.Name, t.Name)
			}
			errorCodes := 0
			for i, a := range t.Assert {
				if err := validateAssertion(a); err != nil {
					return fmt.Errorf("group %s: test %s: assert[%d]: %w", g.Name, t.Name, i, err)
				}
				if a.Kind == assertErrorCode {
					errorCodes++
				}
			}
			if errorCodes > 1 {
				return fmt.Errorf("group %s: test %s: more than one errorCode clause", g.Name, t.Name)
			}
			if errorCodes == 1 {
				// A call expected to fail has no response: nothing may export
				// from it or read it.
				if len(t.Call.Export) > 0 {
					return fmt.Errorf("group %s: test %s: a call expected to fail cannot export", g.Name, t.Name)
				}
				for i, a := range t.Assert {
					if a.Kind == assertResponseField || ((a.Kind == assertListContains || a.Kind == assertAbsent) && a.Call == nil) {
						return fmt.Errorf("group %s: test %s: assert[%d] reads the response of a call expected to fail", g.Name, t.Name, i)
					}
				}
			}
		}
	}
	return nil
}

func validateAssertion(a assertion) error {
	switch a.Kind {
	case assertResponseField:
		if a.Call != nil || len(a.Checks) == 0 || a.ItemsPath != "" || a.Where != nil || a.Error != nil || a.Assert != nil {
			return fmt.Errorf("responseField carries checks only")
		}
	case assertReadback:
		if a.Call == nil || len(a.Checks) == 0 || a.ItemsPath != "" || a.Where != nil || a.Error != nil || a.Assert != nil {
			return fmt.Errorf("readback carries a call and checks only")
		}
	case assertListContains:
		if a.ItemsPath == "" || len(a.Where) == 0 || len(a.Checks) > 0 || a.Error != nil || a.Assert != nil {
			return fmt.Errorf("listContains carries itemsPath and where (and optionally a call) only")
		}
	case assertAbsent:
		listForm := a.ItemsPath != "" && len(a.Where) > 0 && a.Error == nil
		errorForm := a.ItemsPath == "" && a.Where == nil && a.Error != nil && a.Call != nil
		if (!listForm && !errorForm) || len(a.Checks) > 0 || a.Assert != nil {
			return fmt.Errorf("absent is either itemsPath+where or call+error")
		}
	case assertErrorCode:
		if a.Error == nil || a.Call != nil || len(a.Checks) > 0 || a.ItemsPath != "" || a.Where != nil || a.Assert != nil {
			return fmt.Errorf("errorCode carries error only")
		}
	case assertEventually:
		if a.Assert == nil || a.MaxAttempts < 1 || a.DelayMs < 0 || a.Call != nil || len(a.Checks) > 0 || a.Error != nil {
			return fmt.Errorf("eventually carries maxAttempts, delayMs and one inner assertion")
		}
		switch a.Assert.Kind {
		case assertReadback, assertListContains, assertAbsent:
		default:
			return fmt.Errorf("eventually may only wrap readback, listContains or absent, not %s", a.Assert.Kind)
		}
		return validateAssertion(*a.Assert)
	default:
		return fmt.Errorf("unknown assertion kind %q", a.Kind)
	}
	for path, c := range a.Checks {
		set := 0
		if c.NonEmpty {
			set++
		}
		if c.IsList {
			set++
		}
		if c.Equals != nil {
			set++
		}
		if c.EqualsJSON != nil {
			set++
			if err := validateEqualsJSON(c.EqualsJSON, "check "+path); err != nil {
				return err
			}
		}
		if c.Matches != "" {
			set++
		}
		if c.Missing {
			set++
		}
		if set != 1 {
			return fmt.Errorf("check on %s must be exactly one of nonEmpty, isList, equals, equalsJSON, matches, missing", path)
		}
	}
	return nil
}

// findTest locates a test by group/name.
func (s *scenario) findTest(groupName, testName string) (*group, *test, bool) {
	for i := range s.Groups {
		if s.Groups[i].Name != groupName {
			continue
		}
		for j := range s.Groups[i].Tests {
			if s.Groups[i].Tests[j].Name == testName {
				return &s.Groups[i], &s.Groups[i].Tests[j], true
			}
		}
	}
	return nil, nil, false
}
