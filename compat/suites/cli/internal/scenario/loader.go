// Package scenario executes the generated compat scenario IR — the files under
// compat/model/scenarios/, described normatively by compat/model/README.md — as
// cli-suite tests.
//
// It is the cli half of G2 (docs/plans/compat-coverage-modelgen.md § 3.2). A
// generated registry group carries a "scenario" path instead of a Go
// implementation; internal/registry consults Backend.Resolve for any test with
// no static impl, and Backend.Setup/Backend.Teardown supply the group hooks the
// same file declares. Every call is made the way a hand-written group makes one
// — `aws <command> <kebab-op> --cli-input-json '<json>'` through
// internal/awscli — so the interpreter exercises the CLI's own request shaping
// with no Overcast-specific code path.
package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// scenarioVersion is the only scenario file version this interpreter accepts.
// A bump has to be a deliberate multi-file change, exactly as it is for
// registry.generated.json in internal/registry.
const scenarioVersion = 1

// File is one scenario file: compat/model/scenarios/<service>.json.
type File struct {
	Version int     `json:"version"`
	Service string  `json:"service"`
	Client  Client  `json:"client"`
	Groups  []Group `json:"groups"`

	// Path is the repository-relative path the registry named this file by,
	// with '/' separators. It is failure-message field 6, so it is kept
	// exactly as written rather than reconstructed from the service name.
	Path string `json:"-"`
}

// Client is what an interpreter needs to reach the service without a naming
// table of its own (compat/model/README.md § Naming). The cli backend uses
// EndpointPrefix as the `aws` command name; the remaining fields are carried so
// the type matches the IR and a later protocol-sensitive check has them.
type Client struct {
	SDKID          string `json:"sdkId"`
	EndpointPrefix string `json:"endpointPrefix"`
	SigningName    string `json:"signingName"`
	Protocol       string `json:"protocol"`
	APIVersion     string `json:"apiVersion"`
	TargetPrefix   string `json:"targetPrefix"`
}

// Group is one registry group: its setup calls, its tests and its teardown.
type Group struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Setup    []Call `json:"setup"`
	Tests    []Test `json:"tests"`
	Teardown []Call `json:"teardown"`
}

// Call is one service call: an operation, its input members as value
// expressions, and the context paths its response fills in.
type Call struct {
	Op     string            `json:"op"`
	Params map[string]any    `json:"params"`
	Export map[string]string `json:"export"`
}

// Test is one registry test: a primary call and the clauses that verify it.
type Test struct {
	Name    string      `json:"name"`
	Op      string      `json:"op"`
	Call    Call        `json:"call"`
	Assert  []Assertion `json:"assert"`
	Depends []string    `json:"depends"`
}

// Assertion is one clause of the IR's closed assertion set. Kind selects which
// of the remaining fields are present; the schema
// (compat/model/scenario.schema.json) is what guarantees the combination.
type Assertion struct {
	Kind        string           `json:"kind"`
	Checks      map[string]Check `json:"checks"`
	Call        *Call            `json:"call"`
	ItemsPath   string           `json:"itemsPath"`
	Where       map[string]any   `json:"where"`
	Error       *ErrorClause     `json:"error"`
	MaxAttempts int              `json:"maxAttempts"`
	DelayMs     int              `json:"delayMs"`
	Assert      *Assertion       `json:"assert"`
}

// ErrorClause carries the modeled shape name and the wire code. SDKs disagree
// about which of the two they surface, so a match on either is accepted — see
// matchesError.
type ErrorClause struct {
	Shape string `json:"shape"`
	Code  string `json:"code"`
}

// CheckKind names the one check a Checks entry carries.
type CheckKind string

// The closed set of checks (compat/model/README.md § Assertions).
const (
	CheckNonEmpty CheckKind = "nonEmpty"
	CheckIsList   CheckKind = "isList"
	CheckEquals   CheckKind = "equals"
	CheckMatches  CheckKind = "matches"
	CheckMissing  CheckKind = "missing"
)

// Check is exactly one check against one response path. Value carries the
// `equals` value expression or the `matches` pattern; the three boolean checks
// leave it nil.
//
// It is decoded by hand rather than as a struct of optional fields because
// `{"equals": null}` is a legal check — the IR's Value admits null — and a
// struct cannot tell that from an absent key.
type Check struct {
	Kind  CheckKind
	Value any
}

// UnmarshalJSON decodes the single-entry object the schema requires.
func (c *Check) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) != 1 {
		return fmt.Errorf("check must carry exactly one of nonEmpty/isList/equals/matches/missing, got %d entries", len(raw))
	}
	for k, v := range raw {
		switch CheckKind(k) {
		case CheckNonEmpty, CheckIsList, CheckMissing:
			c.Kind, c.Value = CheckKind(k), nil
		case CheckEquals, CheckMatches:
			c.Kind, c.Value = CheckKind(k), v
		default:
			return fmt.Errorf("unknown check %q", k)
		}
	}
	return nil
}

// group returns the named group.
func (f *File) group(name string) (*Group, bool) {
	for i := range f.Groups {
		if f.Groups[i].Name == name {
			return &f.Groups[i], true
		}
	}
	return nil, false
}

// test returns the named test.
func (g *Group) test(name string) (*Test, bool) {
	for i := range g.Tests {
		if g.Tests[i].Name == name {
			return &g.Tests[i], true
		}
	}
	return nil, false
}

// Backend loads scenario files and turns their groups into cli-suite test,
// setup and teardown functions. One Backend serves the whole run; it is safe
// for concurrent use, which it has to be because the harness runs groups in
// parallel.
//
// It holds no per-run state of its own: the context bag a group's exports write
// into lives on the harness TestContext, which is created fresh for every group
// run, so re-running a group in interactive mode starts from an empty context
// exactly as the first run did.
type Backend struct {
	// root is the repository root; a group's Scenario path is relative to it.
	root string
	// run is the process runner. Production is awscliRunner; the tests
	// substitute an in-memory fake.
	run runner

	mu    sync.Mutex
	files map[string]*File
	errs  map[string]error
}

// New returns a Backend that resolves scenario paths against root and calls the
// real `aws` binary through internal/awscli.
func New(root string) *Backend {
	return &Backend{root: root, run: awscliRunner{}, files: map[string]*File{}, errs: map[string]error{}}
}

// load reads and caches one scenario file, keyed by the repository-relative
// path the registry named. Both the file and a load failure are cached: a
// broken file must produce the same loud failure for every test in the group
// rather than a different one per read.
func (b *Backend) load(rel string) (*File, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f, ok := b.files[rel]; ok {
		return f, nil
	}
	if err, ok := b.errs[rel]; ok {
		return nil, err
	}
	f, err := readFile(filepath.Join(b.root, filepath.FromSlash(rel)), rel)
	if err != nil {
		b.errs[rel] = err
		return nil, err
	}
	b.files[rel] = f
	return f, nil
}

// readFile parses one scenario file. rel is recorded on the result so failure
// messages name the file the registry pointed at, not the absolute path this
// process happened to open.
func readFile(abs, rel string) (*File, error) {
	raw, err := os.ReadFile(abs) //nolint:gosec // the path comes from the registry, not from user input
	if err != nil {
		return nil, fmt.Errorf("scenario: read %s: %w", rel, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("scenario: parse %s: %w", rel, err)
	}
	if f.Version != scenarioVersion {
		return nil, fmt.Errorf("scenario: %s: version %d, want %d", rel, f.Version, scenarioVersion)
	}
	if f.Client.EndpointPrefix == "" {
		return nil, fmt.Errorf("scenario: %s: client.endpointPrefix is empty, so no aws command name can be derived", rel)
	}
	f.Path = rel
	return &f, nil
}
