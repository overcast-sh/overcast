//go:build dev

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// What every source emitter needs, in the IR's vocabulary rather than in one
// language's.
//
// cmd/compatgen writes real source for more than one typed backend — Go
// (emit_go.go) and Java (emit_java.go) today, with .NET and Rust behind them —
// and each walks the same IR asking the same questions: which calls does this
// group make, what JSON type is this value, what number does this literal
// carry, and do two names fold to one identifier. Answered once here they
// cannot drift between backends, and the fourth copy per language that a
// per-emitter helper invites never gets written.
//
// A helper belongs here when it reads the IR and nothing else. Anything that
// spells a type, a name or a literal is that backend's own and stays in its
// emit_<lang>*.go.

// callsOf collects every call a group makes: setup, each test's primary call,
// every clause's call however deeply an eventually nests it, and teardown.
func callsOf(g group) []call {
	var out []call
	out = append(out, g.Setup...)
	var fromClause func(a assertion)
	fromClause = func(a assertion) {
		if a.Call != nil {
			out = append(out, *a.Call)
		}
		if a.Assert != nil {
			fromClause(*a.Assert)
		}
	}
	for _, t := range g.Tests {
		out = append(out, t.Call)
		for _, a := range t.Assert {
			fromClause(a)
		}
	}
	out = append(out, g.Teardown...)
	return out
}

// valueKind names an IR value's JSON type for an error message.
func valueKind(v any) string {
	switch value := v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number, float64, int:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		if len(value) == 0 {
			return "an object"
		}
		return "an object (" + strings.Join(sortedKeys(value), ", ") + ")"
	}
	return fmt.Sprintf("a %T", v)
}

// ---------------------------------------------------------------------------
// Numbers
// ---------------------------------------------------------------------------

// irNumber is one IR number, kept as the scenario file wrote it.
//
// The text is parsed as an integer first and only then as a float, because a
// float64 is not a faithful carrier of a JSON number: it rounds silently above
// 2^53 (9007199254740993 becomes …992) and saturates at 2^63, so a backend that
// converted through one would emit a literal that compiles and is not the
// number the scenario asked for. Both values are within reach of a scenario
// file — an account id is sixteen digits — so this is a wrong request waiting
// rather than a theoretical loss.
type irNumber struct {
	// Text is the literal as written, so an error message quotes what the
	// scenario said rather than what a conversion made of it.
	Text string
	// Float is the value as a float64; ±Inf when the literal overflows one.
	Float float64
	// Int is the exact value, valid only when Whole and Fits are both true.
	Int int64
	// Whole says the literal denotes a whole number, whether or not it fits.
	Whole bool
	// Fits says that whole number is exactly an int64 — the widest integer any
	// backend emits, so a literal wider than one is out of range everywhere.
	Fits bool
	// Representable says a float64 carries the literal at all. A literal that
	// overflows or underflows one is still a number, and saying "out of range"
	// about it is more use than saying it is not one.
	Representable bool
}

// numberOf reports the number an IR value carries, and whether it is one.
func numberOf(v any) (irNumber, bool) {
	switch n := v.(type) {
	case json.Number:
		return parseIRNumber(n.String())
	case float64:
		return parseIRNumber(strconv.FormatFloat(n, 'g', -1, 64))
	case int:
		return irNumber{
			Text: strconv.Itoa(n), Float: float64(n), Int: int64(n),
			Whole: true, Fits: true, Representable: true,
		}, true
	}
	return irNumber{}, false
}

func parseIRNumber(text string) (irNumber, bool) {
	if i, err := strconv.ParseInt(text, 10, 64); err == nil {
		return irNumber{
			Text: text, Float: float64(i), Int: i,
			Whole: true, Fits: true, Representable: true,
		}, true
	}
	out := irNumber{Text: text}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		var numErr *strconv.NumError
		if !errors.As(err, &numErr) || !errors.Is(numErr.Err, strconv.ErrRange) {
			return irNumber{}, false
		}
		// Out of a float64's range in either direction. ParseFloat still hands
		// back its nearest value (±Inf, or a zero), which is deliberately not
		// kept: emitting it would silently change the number.
		out.Float = f
		return out, true
	}
	out.Float, out.Representable = f, true
	// A whole number too wide for an int64 — 2^63, or 1e300 — is still whole,
	// and reporting it as such is what lets an integer member refuse it as out
	// of range rather than as "not a whole number", which it is not.
	out.Whole = f == math.Trunc(f)
	return out, true
}

// ---------------------------------------------------------------------------
// Identifier collisions
// ---------------------------------------------------------------------------

// nameClaim is one emitted identifier and what claimed it.
type nameClaim struct{ name, owner string }

// uniqueNames refuses a service whose group and test names collide once folded
// into one backend's identifiers. Two names differing only in where their
// hyphens fall would otherwise emit two methods of the same name, and the suite
// would fail to build with no indication of which pair caused it.
//
// language is the noun the message uses ("Go", "Java"): the fault is the same
// in every backend, and only the identifier scheme that produced it differs.
func uniqueNames(service, language string, claims []nameClaim) error {
	seen := map[string]string{}
	for _, c := range claims {
		if first, dup := seen[c.name]; dup {
			return fmt.Errorf("%s: %s and %s both emit the %s method %s; rename one",
				service, first, c.owner, language, c.name)
		}
		seen[c.name] = c.owner
	}
	return nil
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// refusalChecks is one backend's answer to "can this be emitted", which is the
// only part of refusal detection that is language-specific.
//
// Both hooks report by returning an error. Each backend answers by attempting
// the very spelling emission would write, through a throwaway speller, so one
// code path decides "can this be emitted" and "how" and the two cannot drift.
type refusalChecks struct {
	// call refuses a whole call before its members are looked at, naming the
	// member the refusal is recorded under. May be nil.
	call func(op string) (member string, err error)
	// member refuses one of a call's input members.
	member func(op, member string, v any) error
}

// refusals reports the members of a group's calls a backend cannot express,
// as gaps carrying reason. A group with any is not emitted and is scoped away
// from that suite: a suite that cannot execute a group must not be listed as
// able to.
func refusals(gen *generation, g group, reason string, checks refusalChecks) []gap {
	var out []gap
	seen := map[string]bool{}
	record := func(op, member, detail string) {
		key := op + "." + member
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, gap{
			Service:   gen.scenario.Service,
			Operation: op,
			Group:     g.Name,
			Reason:    reason + ":" + member,
			Detail:    detail,
		})
	}
	for _, c := range callsOf(g) {
		if checks.call != nil {
			if member, err := checks.call(c.Op); err != nil {
				record(c.Op, member, err.Error())
				continue
			}
		}
		for _, member := range sortedValueKeys(c.Params) {
			if err := checks.member(c.Op, member, c.Params[member]); err != nil {
				record(c.Op, member, err.Error())
			}
		}
	}
	sortGaps(out)
	return out
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

// camel turns a kebab-case registry name into an identifier fragment:
// sqs-gen-queue → SqsGenQueue. Every backend whose identifiers are PascalCase
// builds its group, test and file names out of it, and they agree because it is
// one function rather than one per emitter.
func camel(name string) string {
	var out strings.Builder
	for _, part := range strings.Split(name, "-") {
		out.WriteString(pascal(part))
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// sourceWriter accumulates emitted source lines.
//
// A trailing space is never meaningful in any language this emits — a string
// literal ends with its own quote — so lines are right-trimmed here rather than
// in each emitter, and no backend has to prove it did not leave one behind. A
// backend whose output goes through a formatter (Go's through go/format) is
// unaffected either way.
type sourceWriter struct{ lines []string }

func (w *sourceWriter) linef(format string, args ...any) {
	w.lines = append(w.lines, strings.TrimRight(fmt.Sprintf(format, args...), " "))
}

func (w *sourceWriter) String() string { return strings.Join(w.lines, "\n") + "\n" }

// ---------------------------------------------------------------------------
// Emission, and the backend table
// ---------------------------------------------------------------------------

// sourceEmission is one service's emitted source plus what the backend could
// not express. Every source emitter returns one, which is what lets generateAll
// drive them from a table rather than from a block per backend.
type sourceEmission struct {
	// Path is the emitted file, repository-relative.
	Path string
	// Contents is the source, formatted the way that backend commits it.
	Contents []byte
	// Refused names the groups this backend cannot execute, and Gaps says why.
	Refused map[string]bool
	Gaps    []gap
}

// sourceBackend is one typed source emitter as generateAll drives it: which
// suite it writes for, how it renders a service and the index that suite calls
// into, and how one of its own files is recognised so a stale one can be
// removed. Adding a backend is a row in sourceBackends, not another copy of the
// block that used to sit in main.go.
//
// emit takes the SDK type sources the backends that read their SDK need — the
// Go emitter's loader over the vendored Go SDK (emit_go.go) and the .NET
// emitter's committed type table (dotnetsdktypes.go). A backend that reads the
// model alone ignores both.
type sourceBackend struct {
	suite string
	emit  func(gen *generation, sdk sdkTypes) (*sourceEmission, error)
	// indexPath is the index file, repository-relative, and index renders it
	// from the services actually emitted.
	indexPath string
	index     func(services []string) ([]byte, error)
	// dir is where the emitted files live, repository-relative; language names
	// the source in a stale-file message; emittedFile reports whether a file
	// name in dir is one this backend writes.
	dir         string
	language    string
	emittedFile func(name string) bool
}

// sdkTypes is what the typed emitters that read their SDK read it through. Each
// is nil when its backend is not enabled.
type sdkTypes struct {
	goTypes *goSDKTypes
	dotnet  *dotnetSDKTypes
}

// sourceBackends is every typed source emitter, in the order their files are
// rendered. A suite appears here whether or not scenarioBackends names it: the
// index is emitted either way, and a stale file has to be removed either way.
var sourceBackends = []sourceBackend{
	{
		suite:     goSDKSuite,
		emit:      func(gen *generation, sdk sdkTypes) (*sourceEmission, error) { return emitGo(gen, sdk.goTypes) },
		indexPath: goIndexPath,
		index:     emitGoIndex,
		dir:       goSuiteDir,
		language:  "Go",
		emittedFile: func(name string) bool {
			return strings.HasPrefix(name, "scenarios_") && strings.HasSuffix(name, "_gen.go")
		},
	},
	{
		suite:     javaSDKSuite,
		emit:      func(gen *generation, _ sdkTypes) (*sourceEmission, error) { return emitJava(gen) },
		indexPath: javaIndexPath,
		index:     func(services []string) ([]byte, error) { return emitJavaIndex(services), nil },
		dir:       javaSuiteDir,
		language:  "Java",
		emittedFile: func(name string) bool {
			return strings.HasPrefix(name, "Scenarios") && strings.HasSuffix(name, "Gen.java")
		},
	},
	{
		suite:     dotnetSDKSuite,
		emit:      func(gen *generation, sdk sdkTypes) (*sourceEmission, error) { return emitDotnet(gen, sdk.dotnet) },
		indexPath: dotnetIndexPath,
		index:     func(services []string) ([]byte, error) { return emitDotnetIndex(services), nil },
		dir:       dotnetSuiteDir,
		language:  "C#",
		emittedFile: func(name string) bool {
			return strings.HasPrefix(name, "Scenarios") && strings.HasSuffix(name, "Gen.cs")
		},
	},
	{
		suite:     rustSDKSuite,
		emit:      func(gen *generation, _ sdkTypes) (*sourceEmission, error) { return emitRust(gen) },
		indexPath: rustIndexPath,
		index:     emitRustIndex,
		dir:       rustSuiteDir,
		language:  "Rust",
		emittedFile: func(name string) bool {
			return strings.HasPrefix(name, "scenarios_") && strings.HasSuffix(name, "_gen.rs")
		},
	},
}
