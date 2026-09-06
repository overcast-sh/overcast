package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The shared error-matching conformance fixtures, compat/model/testdata/errors.
//
// Three interpreters read the same documents and must agree about which
// clauses they satisfy. Each suite writes this test once, against its own
// matcher, so a rule that only one backend implements fails somewhere rather
// than being discovered when a generated group disagrees with itself across
// suites (compat/model/README.md § Errors).
//
// What this suite observes is narrower than the SDK suites: the AWS CLI hands
// the interpreter a process's stderr, so an exception class name and a
// response header never reach it. Those fixtures are skipped by name and with
// a reason — a silently ignored fixture would look exactly like a passing one.

// knownCarriers is the whole vocabulary. A fixture naming anything else is a
// typo that would otherwise skip quietly in all three suites at once.
var knownCarriers = map[string]bool{
	"exceptionName":    true,
	"bodyType":         true,
	"bodyCode":         true,
	"queryErrorHeader": true,
	"cliBanner":        true,
}

// observedCarriers is what this suite can see. errorCodes reads the CLI's own
// banner and a JSON error body the CLI echoed; nothing else is available to a
// backend whose entire view of a failure is stderr.
var observedCarriers = map[string]bool{
	"bodyType":  true,
	"bodyCode":  true,
	"cliBanner": true,
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
	// bytes — for one that is not, so it is kept undecoded and echoed as the
	// CLI would echo it (compat/model/README.md § Errors).
	Body   json.RawMessage `json:"body"`
	Stderr string          `json:"stderr"`
}

type errorFixtureCase struct {
	Name    string      `json:"name"`
	Error   ErrorClause `json:"error"`
	Matches bool        `json:"matches"`
	Via     string      `json:"via"`
}

// asCLIError renders a fixture the way this suite would have observed it: the
// banner when the fixture states one, and otherwise the body printed after the
// `aws` invocation's exit status, which is how an error the CLI could not
// model arrives.
//
// The body goes out as the service wrote it: a JSON wire's object serialized,
// and a non-JSON wire's raw bytes — the XML of a Query or REST XML error —
// verbatim. Echoing XML as a JSON string would be the fixture writing a wire
// no service sends, and it is exactly what let the old regex extractor read a
// nested code out of a body it could not have parsed.
func (f errorFixture) asCLIError() error {
	if f.Wire.Stderr != "" {
		return fmt.Errorf("%s", f.Wire.Stderr)
	}
	var raw string
	if err := json.Unmarshal(f.Wire.Body, &raw); err == nil {
		return fmt.Errorf("aws widgets get-thing: exit status 255: %s", raw)
	}
	return fmt.Errorf("aws widgets get-thing: exit status 255: %s", f.Wire.Body)
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
				t.Skipf("the cli suite reads none of this fixture's surfaces (%s): its whole view of a failure is the `aws` process's stderr, so an SDK exception class and a response header never reach it",
					strings.Join(f.Carriers, ", "))
			}
			observed := f.asCLIError()
			for _, c := range f.Expect {
				t.Run(c.Name, func(t *testing.T) {
					if c.Matches && !observedCarriers[c.Via] {
						t.Skipf("this expectation matches through %q, which the cli suite does not observe", c.Via)
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

// observesAny reports whether this suite can see any surface the fixture
// states the code on. A fixture that states none — `carriers: []` — is
// observed by everyone: there is nothing to miss, and its expectations are all
// negative, so a suite that cannot see the wire at all still answers them
// correctly.
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
