// cmd/compat/publish.go — the public compatibility report.
//
// compat-results.json is the raw record of a run: one status and at most one
// free-text error per (suite, test). That is enough for the gates, but a
// reader outside the project cannot tell from it *why* a test did not pass, who
// is expected to fix it, whether anyone is tracking it, or what was never
// tested at all. Those answers live in five other files, joined only by name.
//
// --publish-report does that join once and writes compat-report.json: one
// versioned document, attached to every release, that overcast.sh renders as
// the compatibility report. Every non-passing cell carries exactly one reason
// code from publishReasons, so the site never has to parse error strings, and
// the reason says whose move it is — an Overcast gap reads differently from a
// test nobody has written yet.
//
// The schema is compat/report.schema.json. Bump publishSchemaVersion on any
// change a consumer could notice.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/compat"
)

const publishSchemaVersion = 1

// defaultIssueRepo is where bare issue numbers point.
const defaultIssueRepo = "overcast-sh/overcast"

// ---------------------------------------------------------------------------
// Output shape
// ---------------------------------------------------------------------------

type publishedReport struct {
	Schema        int                `json:"schema"`
	Version       string             `json:"version"`
	Commit        string             `json:"commit,omitempty"`
	GeneratedAt   string             `json:"generatedAt"`
	RunStartedAt  string             `json:"runStartedAt,omitempty"`
	RunFinishedAt string             `json:"runFinishedAt,omitempty"`
	IssueRepo     string             `json:"issueRepo"`
	Reasons       []publishedReason  `json:"reasons"`
	Suites        []publishedSuite   `json:"suites"`
	Totals        publishedTotals    `json:"totals"`
	Services      []publishedService `json:"services"`
}

type publishedReason struct {
	Code    string `json:"code"`
	Label   string `json:"label"`
	Summary string `json:"summary"`
	// Whose is who has to act: overcast (the emulator), suite (the test
	// suite), sdk (the client library), environment (the machine that ran
	// the tests), or none.
	Whose string `json:"whose"`
}

type publishedSuite struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Kind     string          `json:"kind"`
	Language string          `json:"language"`
	Totals   publishedTotals `json:"totals"`
}

// publishedTotals counts cells: every cell is either a pass or has exactly one
// reason, so Pass plus the sum of ByReason is Cells. Which reasons count
// against a pass rate is the reader's call; the report only classifies.
type publishedTotals struct {
	Cells    int            `json:"cells"`
	Pass     int            `json:"pass"`
	ByReason map[string]int `json:"byReason"`
	// Untested counts operations the generator refused to probe; they have
	// no cells, so they are counted apart.
	Untested int `json:"untested,omitempty"`
}

type publishedService struct {
	ID       string           `json:"id"`
	Totals   publishedTotals  `json:"totals"`
	Groups   []publishedGroup `json:"groups"`
	Untested []publishedGap   `json:"untested,omitempty"`
}

type publishedGroup struct {
	ID        string          `json:"id"`
	Generated bool            `json:"generated,omitempty"`
	State     string          `json:"state,omitempty"`
	Scenario  string          `json:"scenario,omitempty"`
	Tests     []publishedTest `json:"tests"`
}

type publishedTest struct {
	Name     string                     `json:"name"`
	Op       string                     `json:"op"`
	Depends  []string                   `json:"depends,omitempty"`
	Requires []string                   `json:"requires,omitempty"`
	Results  map[string]publishedResult `json:"results"`
}

type publishedResult struct {
	Status     compat.Status `json:"status"`
	Reason     string        `json:"reason,omitempty"`
	DurationMS int64         `json:"durationMs,omitempty"`
	Error      string        `json:"error,omitempty"`
	Mismatch   *mismatch     `json:"mismatch,omitempty"`
	BlockedBy  []string      `json:"blockedBy,omitempty"`
	Issue      *issueRef     `json:"issue,omitempty"`
}

// mismatch is a generated scenario's six-field failure message, parsed. See
// compat/model/README.md § Failure messages.
type mismatch struct {
	Op       string `json:"op"`
	Params   string `json:"params,omitempty"`
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Scenario string `json:"scenario"`
	Step     string `json:"step"`
}

type publishedGap struct {
	Op     string    `json:"op"`
	Group  string    `json:"group"`
	Reason string    `json:"reason"`
	Code   string    `json:"code"`
	Detail string    `json:"detail"`
	Issue  *issueRef `json:"issue,omitempty"`
}

type issueRef struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	State  string `json:"state,omitempty"`
}

// ---------------------------------------------------------------------------
// Reasons
// ---------------------------------------------------------------------------

const (
	reasonNotEmulated       = "not-emulated"
	reasonBehaviourMismatch = "behaviour-mismatch"
	reasonQuarantined       = "quarantined-flaky"
	reasonSuiteNotWritten   = "suite-not-written"
	reasonDependencyFailed  = "dependency-failed"
	reasonNeedsEnvironment  = "needs-environment"
	reasonSDKLacksAPI       = "sdk-lacks-api"
	reasonCandidate         = "candidate"
	reasonNotReported       = "not-reported"
	reasonOtherSkip         = "other-skip"
	reasonUntested          = "untested"
)

// publishReasons is the vocabulary, in the order a reader should meet it:
// Overcast's own gaps first, then the suite's, then everyone else's.
var publishReasons = []publishedReason{
	{reasonBehaviourMismatch, "Behaves differently from AWS", "Overcast answered, but the response or its side effects differ from what AWS does.", "overcast"},
	{reasonNotEmulated, "Not emulated yet", "Overcast returned 501 Not Implemented: the operation has no emulation yet.", "overcast"},
	{reasonQuarantined, "Quarantined as flaky", "The test gives different answers on identical input, so its result is not counted until the tracking issue is resolved.", "overcast"},
	{reasonDependencyFailed, "Blocked by an earlier step", "An earlier test in the same group failed, so this one could not run. Fixing the first failure unblocks it.", "overcast"},
	{reasonSuiteNotWritten, "Test not written for this SDK", "Overcast may well support this; the test has not been written in this language yet.", "suite"},
	{reasonCandidate, "New test, still soaking", "A test generated from the AWS model that has not yet run cleanly enough times to count.", "suite"},
	{reasonNotReported, "No result reported", "The registry expects this test, but the suite reported nothing for it.", "suite"},
	{reasonSDKLacksAPI, "Not in this SDK", "The SDK or CLI has no API for this operation, so there is nothing to test.", "sdk"},
	{reasonNeedsEnvironment, "Needs a special environment", "The test needs Docker, SMTP or network access that the release run does not provide.", "environment"},
	{reasonOtherSkip, "Skipped", "The suite skipped the test; the message says why.", "suite"},
	{reasonUntested, "Not tested", "No test exists for this operation. The generator declined to write one, and says why.", "none"},
}

// classifyResult gives a result its reason code. A pass has none.
func classifyResult(status compat.Status, errText string, flaky, candidate bool) string {
	switch status {
	case compat.StatusPass:
		return ""
	case compat.StatusFail:
		switch {
		case flaky:
			return reasonQuarantined
		case candidate:
			return reasonCandidate
		}
		return reasonBehaviourMismatch
	case compat.StatusUnimplemented:
		return reasonNotEmulated
	case compat.StatusNA:
		return reasonSDKLacksAPI
	}
	// skip
	switch {
	case isNotImplementedSkip(errText):
		return reasonSuiteNotWritten
	case isCascadeSkip(errText):
		return reasonDependencyFailed
	case isEnvironmentalSkip(errText):
		return reasonNeedsEnvironment
	}
	return reasonOtherSkip
}

// blockedBy extracts the tests a cascade skip names: "dependency failed: A, B".
func blockedBy(errText string) []string {
	rest, ok := strings.CutPrefix(errText, "dependency failed:")
	if !ok {
		return nil
	}
	var out []string
	for _, name := range strings.Split(rest, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// The six-field failure message is built by each backend's own helper, and
// they agree on the fields but not the punctuation (compat/model/README.md §
// Failure messages). One pattern per dialect, tried in turn. None is anchored at
// the start, because an eventually-consistent assertion wraps the message
// ("eventually gave up after 6 attempt(s) 500ms apart; last failure: <message>")
// and node prefixes the error class. The params JSON can hold anything, so it is
// matched lazily and the parse is best-effort: a message that fits none of them
// is published verbatim and loses nothing.
var scenarioFailures = []struct {
	re *regexp.Regexp
	// build maps the submatches onto the fields; dialects differ in order.
	build func(m []string) *mismatch
}{
	// go-sdk, cli, java, dotnet, rust:
	//   g/t: Op params {...}: readback equals at $.P: expected "a", actual "b" (file step)
	{
		regexp.MustCompile(`[^\s:]+/[^\s:]+: (\w+)(?: params (.*?))?: ([a-zA-Z][A-Za-z ]*?)(?: at (\S+))?: expected (.*), actual (.*) \((\S+) ([^)]+)\)$`),
		func(m []string) *mismatch {
			return &mismatch{Op: m[1], Params: m[2], Kind: m[3], Path: m[4], Expected: m[5], Actual: m[6], Scenario: m[7], Step: m[8]}
		},
	},
	// node-js-sdk:
	//   g/t: Op params={...} — readback at $.P: expected "a", actual "b" (file step)
	{
		regexp.MustCompile(`[^\s:]+/[^\s:]+: (\w+)(?: params=(.*?))? \S ([a-zA-Z][A-Za-z ]*?)(?: at (\S+))?: expected (.*), actual (.*) \((\S+) ([^)]+)\)$`),
		func(m []string) *mismatch {
			return &mismatch{Op: m[1], Params: m[2], Kind: m[3], Path: m[4], Expected: m[5], Actual: m[6], Scenario: m[7], Step: m[8]}
		},
	},
	// python-sdk:
	//   g/t: op=Op params={...} assertion=readback path=$.P expected=equals "a" actual="b" at file step
	{
		regexp.MustCompile(`[^\s:]+/[^\s:]+: op=(\w+)(?: params=(.*?))? assertion=(\S+)(?: path=(\S+))? expected=(?:(\w+) )?(.*) actual=(.*) at (\S+) (\S+)$`),
		func(m []string) *mismatch {
			kind := m[3]
			if m[5] != "" {
				kind += " " + m[5]
			}
			return &mismatch{Op: m[1], Params: m[2], Kind: kind, Path: m[4], Expected: m[6], Actual: m[7], Scenario: m[8], Step: m[9]}
		},
	},
}

func parseMismatch(errText string) *mismatch {
	for _, dialect := range scenarioFailures {
		if m := dialect.re.FindStringSubmatch(errText); m != nil {
			return dialect.build(m)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Suites
// ---------------------------------------------------------------------------

var publishSuiteInfo = map[string]struct{ label, kind, language string }{
	"node-js-sdk": {"AWS SDK for JavaScript v3", "sdk", "TypeScript"},
	"python-sdk":  {"AWS SDK for Python (boto3)", "sdk", "Python"},
	"go-sdk":      {"AWS SDK for Go v2", "sdk", "Go"},
	"java-sdk":    {"AWS SDK for Java 2.x", "sdk", "Java"},
	"dotnet-sdk":  {"AWS SDK for .NET", "sdk", "C#"},
	"rust-sdk":    {"AWS SDK for Rust", "sdk", "Rust"},
	"cli":         {"AWS CLI v2", "cli", "Shell"},
	"cdk":         {"AWS CDK", "iac", "TypeScript"},
}

// ---------------------------------------------------------------------------
// Inputs
// ---------------------------------------------------------------------------

// publishRegistry is the registry read in full: the gate path reads names
// only (parityTest), but the report shows ops, dependencies and requirements.
type publishRegistry struct {
	Groups []struct {
		Service   string   `json:"service"`
		Name      string   `json:"name"`
		Suites    []string `json:"suites"`
		Generated bool     `json:"generated"`
		State     string   `json:"state"`
		Scenario  string   `json:"scenario"`
		Tests     []struct {
			Name     string   `json:"name"`
			Op       string   `json:"op"`
			Depends  []string `json:"depends"`
			Requires []string `json:"requires"`
		} `json:"tests"`
	} `json:"groups"`
}

type gapsFile struct {
	Gaps []struct {
		Service   string `json:"service"`
		Operation string `json:"operation"`
		Group     string `json:"group"`
		Reason    string `json:"reason"`
		Detail    string `json:"detail"`
	} `json:"gaps"`
}

// issuesFile is what scripts/compat-issues.py writes: open issues that carry
// one or more <!-- compat:<target> --> markers.
type issuesFile struct {
	Issues []struct {
		Number  int      `json:"number"`
		URL     string   `json:"url"`
		Title   string   `json:"title"`
		State   string   `json:"state"`
		Targets []string `json:"targets"`
	} `json:"issues"`
}

// curatedLinksFile is compat/report-issues.json: hand-written links for
// results no marker covers. A curated link beats a marker.
type curatedLinksFile struct {
	Links []struct {
		Target string `json:"target"`
		Issue  int    `json:"issue"`
	} `json:"links"`
}

type publishInputs struct {
	Report    *compat.RunReport
	Registry  *publishRegistry
	Gaps      *gapsFile
	Flaky     *flakyFile
	Issues    *issuesFile
	Curated   *curatedLinksFile
	Version   string
	Commit    string
	IssueRepo string
	Now       time.Time
}

// readOptionalJSON decodes path into v, leaving v untouched when the file is
// absent or path is empty: every input but the results is optional, so a
// checkout that predates one still publishes.
func readOptionalJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Issue links
// ---------------------------------------------------------------------------

// issueIndex resolves a result to its tracking issue by target, from most to
// least specific. Targets are <service>[/<group-or-op>[/<test>]][@<suite>].
type issueIndex struct {
	curated map[string]*issueRef
	marked  map[string]*issueRef
}

func newIssueIndex(in publishInputs) *issueIndex {
	idx := &issueIndex{curated: map[string]*issueRef{}, marked: map[string]*issueRef{}}
	byNumber := map[int]*issueRef{}
	if in.Issues != nil {
		for _, is := range in.Issues.Issues {
			ref := &issueRef{Number: is.Number, URL: is.URL, Title: is.Title, State: strings.ToLower(is.State)}
			if ref.URL == "" {
				ref.URL = issueURL(in.IssueRepo, is.Number)
			}
			byNumber[is.Number] = ref
			for _, t := range is.Targets {
				t = normaliseTarget(t)
				// Two issues claiming one target: the lower number is the
				// older, and usually the one the others were duplicated from.
				if prev, ok := idx.marked[t]; !ok || is.Number < prev.Number {
					idx.marked[t] = ref
				}
			}
		}
	}
	if in.Curated != nil {
		for _, l := range in.Curated.Links {
			ref := byNumber[l.Issue]
			if ref == nil {
				ref = &issueRef{Number: l.Issue, URL: issueURL(in.IssueRepo, l.Issue)}
			}
			idx.curated[normaliseTarget(l.Target)] = ref
		}
	}
	return idx
}

// issueTargetPattern is the target grammar, shared with
// scripts/compat-issues.py and compat/report-issues.schema.json.
var issueTargetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[A-Za-z0-9_.-]+){0,2}(@[a-z0-9-]+)?$`)

func normaliseTarget(t string) string { return strings.ToLower(strings.TrimSpace(t)) }

func issueURL(repo string, n int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, n)
}

// lookup tries each candidate target in order, curated links first.
func (idx *issueIndex) lookup(candidates []string) *issueRef {
	for _, table := range []map[string]*issueRef{idx.curated, idx.marked} {
		for _, c := range candidates {
			if ref, ok := table[normaliseTarget(c)]; ok {
				return ref
			}
		}
	}
	return nil
}

func resultTargets(service, group, test, op, suite string) []string {
	var out []string
	for _, base := range []string{
		service + "/" + group + "/" + test,
		service + "/" + op,
		service + "/" + group,
		service,
	} {
		out = append(out, base+"@"+suite, base)
	}
	return out
}

// flakyIssue reads flaky.json's free-form issue field: a URL, "#123" or "123".
func flakyIssue(repo, s string) *issueRef {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	tail := s[strings.LastIndexAny(s, "/#")+1:]
	n, err := strconv.Atoi(tail)
	if err != nil {
		return nil
	}
	url := s
	if !strings.HasPrefix(s, "http") {
		url = issueURL(repo, n)
	}
	return &issueRef{Number: n, URL: url}
}

// ---------------------------------------------------------------------------
// The join
// ---------------------------------------------------------------------------

func buildPublishedReport(in publishInputs) *publishedReport {
	if in.IssueRepo == "" {
		in.IssueRepo = defaultIssueRepo
	}
	issues := newIssueIndex(in)

	flaky := map[string]flakyEntry{}
	if in.Flaky != nil {
		for _, e := range in.Flaky.Flaky {
			flaky[e.key()] = e
		}
	}

	// Results by suite/group/test.
	type cell struct {
		service string
		result  compat.TestResultEvent
	}
	results := map[string]cell{}
	var suiteIDs []string
	for _, sr := range in.Report.Suites {
		suiteIDs = append(suiteIDs, sr.Suite)
		for _, gr := range sr.Groups {
			for _, t := range gr.Tests {
				svc := t.Service
				if svc == "" {
					svc = gr.Service
				}
				results[sr.Suite+"/"+gr.Name+"/"+t.Test] = cell{service: svc, result: t}
			}
		}
	}
	sort.SliceStable(suiteIDs, func(i, j int) bool { return suiteOrder(suiteIDs[i]) < suiteOrder(suiteIDs[j]) })

	out := &publishedReport{
		Schema:      publishSchemaVersion,
		Version:     in.Version,
		Commit:      in.Commit,
		GeneratedAt: in.Now.UTC().Format(time.RFC3339),
		IssueRepo:   in.IssueRepo,
		Reasons:     publishReasons,
		Totals:      newTotals(),
	}
	if !in.Report.StartedAt.IsZero() {
		out.RunStartedAt = in.Report.StartedAt.UTC().Format(time.RFC3339)
		out.RunFinishedAt = in.Report.FinishedAt.UTC().Format(time.RFC3339)
	}
	suiteTotals := map[string]*publishedTotals{}
	for _, id := range suiteIDs {
		t := newTotals()
		suiteTotals[id] = &t
	}

	services := map[string]*publishedService{}
	service := func(id string) *publishedService {
		s := services[id]
		if s == nil {
			s = &publishedService{ID: id, Totals: newTotals()}
			services[id] = s
		}
		return s
	}

	seen := map[string]bool{}
	addCell := func(svc *publishedService, test *publishedTest, suite, group string, res publishedResult) {
		if res.Reason != "" && res.Issue == nil {
			res.Issue = issues.lookup(resultTargets(svc.ID, group, test.Name, test.Op, suite))
		}
		test.Results[suite] = res
		for _, t := range []*publishedTotals{&out.Totals, &svc.Totals, suiteTotals[suite]} {
			t.add(res)
		}
	}
	toResult := func(r compat.TestResultEvent, group string) publishedResult {
		key := r.Suite + "/" + group + "/" + r.Test
		fe, isFlaky := flaky[key]
		res := publishedResult{
			Status:     r.Status,
			DurationMS: r.DurationMS,
			Reason:     classifyResult(r.Status, r.Error, isFlaky, false),
		}
		if res.Reason != "" {
			res.Error = r.Error
		}
		switch res.Reason {
		case reasonBehaviourMismatch, reasonQuarantined:
			res.Mismatch = parseMismatch(r.Error)
		case reasonDependencyFailed:
			res.BlockedBy = blockedBy(r.Error)
		}
		if isFlaky {
			res.Issue = flakyIssue(in.IssueRepo, fe.Issue)
		}
		return res
	}

	// Registry order first: it is the order the suites and the dashboard use.
	if in.Registry != nil {
		for _, g := range in.Registry.Groups {
			svc := service(g.Service)
			pg := publishedGroup{ID: g.Name, Generated: g.Generated, State: g.State, Scenario: g.Scenario}
			scope := parityGroup{Suites: g.Suites}
			candidate := g.Generated && g.State == generatedStateCandidate
			for _, rt := range g.Tests {
				op := rt.Op
				if op == "" {
					op = rt.Name
				}
				test := publishedTest{Name: rt.Name, Op: op, Depends: rt.Depends, Requires: rt.Requires, Results: map[string]publishedResult{}}
				for _, suite := range suiteIDs {
					key := suite + "/" + g.Name + "/" + rt.Name
					c, ok := results[key]
					seen[key] = true
					if !ok {
						if scope.expects(suite) {
							addCell(svc, &test, suite, g.Name, publishedResult{Status: compat.StatusSkip, Reason: reasonNotReported})
						}
						continue
					}
					res := toResult(c.result, g.Name)
					if candidate && res.Reason == reasonBehaviourMismatch {
						res.Reason = reasonCandidate
					}
					addCell(svc, &test, suite, g.Name, res)
				}
				pg.Tests = append(pg.Tests, test)
			}
			svc.Groups = append(svc.Groups, pg)
		}
	}

	// Results the registry does not know about still get published: a report
	// that silently dropped them would under-count what was run.
	orphans := map[string]*publishedGroup{}
	var orphanOrder []string
	for _, sr := range in.Report.Suites {
		for _, gr := range sr.Groups {
			for _, t := range gr.Tests {
				key := sr.Suite + "/" + gr.Name + "/" + t.Test
				if seen[key] {
					continue
				}
				seen[key] = true
				c := results[key]
				gkey := c.service + "/" + gr.Name
				pg := orphans[gkey]
				if pg == nil {
					pg = &publishedGroup{ID: gr.Name}
					orphans[gkey] = pg
					orphanOrder = append(orphanOrder, gkey)
				}
				var test *publishedTest
				for i := range pg.Tests {
					if pg.Tests[i].Name == t.Test {
						test = &pg.Tests[i]
					}
				}
				if test == nil {
					op := t.Op
					if op == "" {
						op = t.Test
					}
					pg.Tests = append(pg.Tests, publishedTest{Name: t.Test, Op: op, Results: map[string]publishedResult{}})
					test = &pg.Tests[len(pg.Tests)-1]
				}
				addCell(service(c.service), test, sr.Suite, gr.Name, toResult(t, gr.Name))
			}
		}
	}
	for _, gkey := range orphanOrder {
		svc := service(strings.SplitN(gkey, "/", 2)[0])
		svc.Groups = append(svc.Groups, *orphans[gkey])
	}

	if in.Gaps != nil {
		for _, g := range in.Gaps.Gaps {
			svc := service(g.Service)
			code := reasonCode(g.Reason)
			svc.Untested = append(svc.Untested, publishedGap{
				Op: g.Operation, Group: g.Group, Reason: g.Reason, Code: code, Detail: g.Detail,
				Issue: issues.lookup([]string{g.Service + "/" + g.Operation, g.Service + "/" + g.Group, g.Service}),
			})
			svc.Totals.Untested++
			out.Totals.Untested++
		}
	}

	for _, id := range suiteIDs {
		info, ok := publishSuiteInfo[id]
		if !ok {
			info.label, info.kind = id, "sdk"
		}
		out.Suites = append(out.Suites, publishedSuite{ID: id, Label: info.label, Kind: info.kind, Language: info.language, Totals: *suiteTotals[id]})
	}
	ids := make([]string, 0, len(services))
	for id := range services {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := services[id]
		if s.Groups == nil {
			s.Groups = []publishedGroup{}
		}
		out.Services = append(out.Services, *s)
	}
	return out
}

// reasonCode strips a gaps.json reason to its stable code: the part before
// the first ':'. cmd/compatgen has the same helper behind the dev build tag.
func reasonCode(reason string) string {
	code, _, _ := strings.Cut(reason, ":")
	return code
}

func newTotals() publishedTotals { return publishedTotals{ByReason: map[string]int{}} }

func (t *publishedTotals) add(r publishedResult) {
	t.Cells++
	if r.Reason == "" {
		t.Pass++
		return
	}
	t.ByReason[r.Reason]++
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

type publishOptions struct {
	ResultsPath, RegistryPath, GeneratedRegistryPath string
	GapsPath, FlakyPath, IssuesPath, CuratedPath     string
	OutPath, Version, Commit, IssueRepo              string
}

func publishReportFile(opts publishOptions) error {
	report, err := readRunReportFile(opts.ResultsPath)
	if err != nil {
		return err
	}
	in := publishInputs{Report: report, Version: opts.Version, Commit: opts.Commit, IssueRepo: opts.IssueRepo, Now: time.Now()}

	var hand, gen publishRegistry
	if err := readOptionalJSON(opts.RegistryPath, &hand); err != nil {
		return err
	}
	if err := readOptionalJSON(opts.GeneratedRegistryPath, &gen); err != nil {
		return err
	}
	hand.Groups = append(hand.Groups, gen.Groups...)
	in.Registry = &hand

	in.Gaps, in.Flaky, in.Issues, in.Curated = &gapsFile{}, &flakyFile{}, &issuesFile{}, &curatedLinksFile{}
	for _, f := range []struct {
		path string
		v    any
	}{{opts.GapsPath, in.Gaps}, {opts.FlakyPath, in.Flaky}, {opts.IssuesPath, in.Issues}, {opts.CuratedPath, in.Curated}} {
		if err := readOptionalJSON(f.path, f.v); err != nil {
			return err
		}
	}

	b, err := json.MarshalIndent(buildPublishedReport(in), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	tmp := opts.OutPath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", opts.OutPath, err)
	}
	if err := os.Rename(tmp, opts.OutPath); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("write %s: %w", opts.OutPath, err)
	}
	return nil
}
