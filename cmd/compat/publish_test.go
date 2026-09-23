package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/compat"
)

func TestClassifyResultCoversEveryErrorConvention(t *testing.T) {
	// Every wording a suite emits for a non-pass has to land on a reason code,
	// or the published report shows it as a bare "skipped". The skip wordings
	// here are the ones parity.go already classifies; the report must agree.
	cases := []struct {
		name      string
		status    compat.Status
		err       string
		flaky     bool
		candidate bool
		want      string
	}{
		{"pass", compat.StatusPass, "", false, false, ""},
		{"assertion failure", compat.StatusFail, "expected 200", false, false, reasonBehaviourMismatch},
		{"quarantined failure", compat.StatusFail, "expected 200", true, false, reasonQuarantined},
		{"soaking candidate", compat.StatusFail, "expected 200", false, true, reasonCandidate},
		{"501", compat.StatusUnimplemented, "", false, false, reasonNotEmulated},
		{"sdk has no api", compat.StatusNA, "not yet supported by the AWS CLI", false, false, reasonSDKLacksAPI},
		{"suite sentinel", compat.StatusSkip, notImplementedSentinel("rust-sdk"), false, false, reasonSuiteNotWritten},
		{"legacy sentinel", compat.StatusSkip, "not implemented in cli suite", false, false, reasonSuiteNotWritten},
		{"cascade", compat.StatusSkip, "dependency failed: CreateBucket", false, false, reasonDependencyFailed},
		{"setup cascade", compat.StatusSkip, "setup failed: boom", false, false, reasonDependencyFailed},
		{"group timeout", compat.StatusSkip, "group timed out after 300s", false, false, reasonDependencyFailed},
		{"docker", compat.StatusSkip, "requires: docker", false, false, reasonNeedsEnvironment},
		{"anything else", compat.StatusSkip, "who knows", false, false, reasonOtherSkip},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyResult(tc.status, tc.err, tc.flaky, tc.candidate); got != tc.want {
				t.Fatalf("classifyResult = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEveryReasonCodeIsDescribed(t *testing.T) {
	// The site renders labels from the report's own reason list; a code with
	// no entry would render as its raw slug.
	described := map[string]bool{}
	for _, r := range publishReasons {
		if r.Label == "" || r.Summary == "" || r.Whose == "" {
			t.Errorf("reason %q is missing a label, summary or owner", r.Code)
		}
		described[r.Code] = true
	}
	for _, code := range []string{reasonNotEmulated, reasonBehaviourMismatch, reasonQuarantined, reasonSuiteNotWritten,
		reasonDependencyFailed, reasonNeedsEnvironment, reasonSDKLacksAPI, reasonCandidate, reasonNotReported,
		reasonOtherSkip, reasonUntested} {
		if !described[code] {
			t.Errorf("reason %q has no entry in publishReasons", code)
		}
	}
}

func TestBlockedByNamesEveryDependency(t *testing.T) {
	got := blockedBy("dependency failed: CreateBucket, PutObject")
	if len(got) != 2 || got[0] != "CreateBucket" || got[1] != "PutObject" {
		t.Fatalf("blockedBy = %v", got)
	}
	if blockedBy("setup failed: x") != nil {
		t.Fatal("a setup failure names no dependency")
	}
}

func TestParseMismatchReadsTheSixFieldMessage(t *testing.T) {
	plain := `sqs-gen-queue/SetQueueAttributes: GetQueueAttributes params {"AttributeNames":["All"],"QueueUrl":"http://x:1/q"}: readback equals at $.Attributes.VisibilityTimeout: expected "60", actual "30" (compat/model/scenarios/sqs.json assert[0].assert)`
	m := parseMismatch(plain)
	if m == nil {
		t.Fatal("plain message did not parse")
	}
	want := mismatch{Op: "GetQueueAttributes", Params: `{"AttributeNames":["All"],"QueueUrl":"http://x:1/q"}`, Kind: "readback equals",
		Path: "$.Attributes.VisibilityTimeout", Expected: `"60"`, Actual: `"30"`, Scenario: "compat/model/scenarios/sqs.json", Step: "assert[0].assert"}
	if *m != want {
		t.Fatalf("parsed %+v\nwant   %+v", *m, want)
	}

	// An eventually-consistent assertion wraps the same message.
	wrapped := "eventually gave up after 6 attempt(s) 500ms apart; last failure: " + plain
	if m := parseMismatch(wrapped); m == nil || m.Path != want.Path {
		t.Fatalf("wrapped message parsed as %+v", m)
	}

	// Every backend words the same six fields its own way; each dialect must
	// land on the same parse. These are verbatim from a main-branch run.
	for name, msg := range map[string]string{
		"node":   `ScenarioFailure: eventually gave up after 6 attempt(s) 500ms apart; last failure: iam-gen-role/PutRolePermissionsBoundary: GetRole params={"RoleName":"r"} — readback at $.Role.PermissionsBoundary.PermissionsBoundaryType: expected "PermissionsBoundaryPolicy", actual "Policy" (compat/model/scenarios/iam.json assert[0].assert)`,
		"python": `eventually gave up after 6 attempt(s) 500ms apart; last failure: iam-gen-role/PutRolePermissionsBoundary: op=GetRole params={"RoleName": "r"} assertion=readback path=$.Role.PermissionsBoundary.PermissionsBoundaryType expected=equals "PermissionsBoundaryPolicy" actual="Policy" at compat/model/scenarios/iam.json assert[0].assert`,
		"go":     `eventually gave up after 6 attempt(s) 500ms apart; last failure: iam-gen-role/PutRolePermissionsBoundary: GetRole params {"RoleName":"r"}: readback equals at $.Role.PermissionsBoundary.PermissionsBoundaryType: expected "PermissionsBoundaryPolicy", actual "Policy" (compat/model/scenarios/iam.json assert[0].assert)`,
	} {
		m := parseMismatch(msg)
		if m == nil {
			t.Errorf("%s dialect did not parse", name)
			continue
		}
		if m.Op != "GetRole" || m.Path != "$.Role.PermissionsBoundary.PermissionsBoundaryType" || m.Expected != `"PermissionsBoundaryPolicy"` ||
			m.Actual != `"Policy"` || m.Scenario != "compat/model/scenarios/iam.json" || m.Step != "assert[0].assert" {
			t.Errorf("%s dialect parsed as %+v", name, *m)
		}
	}

	// Assertion kinds are camelCase (responseField, errorCode), and a check
	// with no path goes straight from the kind to the values.
	if m := parseMismatch(`sns-gen-topic/ConfirmSubscription: ConfirmSubscription params {"Token":"t"}: errorCode: expected error "InvalidParameter", actual <no error> (compat/model/scenarios/sns.json call)`); m == nil ||
		m.Kind != "errorCode" || m.Path != "" || m.Actual != "<no error>" || m.Step != "call" {
		t.Errorf("errorCode parsed as %+v", m)
	}

	if parseMismatch("expected status 200, got 400") != nil {
		t.Fatal("a hand-written failure must not parse as a scenario mismatch")
	}
}

func TestIssueLinksPreferCuratedThenMostSpecific(t *testing.T) {
	in := publishInputs{
		IssueRepo: "o/r",
		Issues: &issuesFile{Issues: []struct {
			Number  int      `json:"number"`
			URL     string   `json:"url"`
			Title   string   `json:"title"`
			State   string   `json:"state"`
			Targets []string `json:"targets"`
		}{
			{Number: 10, Title: "all of sqs", State: "OPEN", Targets: []string{"sqs"}},
			{Number: 11, Title: "purge", Targets: []string{"sqs/PurgeQueue"}},
			{Number: 12, Title: "purge on rust", Targets: []string{"sqs/PurgeQueue@rust-sdk"}},
		}},
		Curated: &curatedLinksFile{Links: []struct {
			Target string `json:"target"`
			Issue  int    `json:"issue"`
		}{{Target: "sqs/sqs-crud/DeleteQueue", Issue: 99}}},
	}
	idx := newIssueIndex(in)

	check := func(name string, targets []string, want int) {
		t.Helper()
		got := idx.lookup(targets)
		if got == nil || got.Number != want {
			t.Fatalf("%s: got %+v, want #%d", name, got, want)
		}
	}
	check("suite-scoped beats op", resultTargets("sqs", "sqs-crud", "PurgeQueue", "PurgeQueue", "rust-sdk"), 12)
	check("op beats service", resultTargets("sqs", "sqs-crud", "PurgeQueue", "PurgeQueue", "go-sdk"), 11)
	check("service fallback", resultTargets("sqs", "sqs-crud", "SendMessage", "SendMessage", "go-sdk"), 10)
	check("curated beats marker", resultTargets("sqs", "sqs-crud", "DeleteQueue", "DeleteQueue", "go-sdk"), 99)

	if got := idx.lookup(resultTargets("sqs", "g", "SendMessage", "SendMessage", "go-sdk")); got.URL != "https://github.com/o/r/issues/10" || got.State != "open" {
		t.Fatalf("marker ref = %+v, want a built URL and a lower-cased state", got)
	}
}

func TestFlakyIssueAcceptsEveryForm(t *testing.T) {
	for _, s := range []string{"#42", "42", "https://github.com/o/r/issues/42"} {
		if got := flakyIssue("o/r", s); got == nil || got.Number != 42 {
			t.Errorf("flakyIssue(%q) = %+v", s, got)
		}
	}
	if flakyIssue("o/r", "") != nil || flakyIssue("o/r", "TBD") != nil {
		t.Error("an empty or non-numeric issue has no link")
	}
}

func publishRegistryFromJSON(t *testing.T, doc string) *publishRegistry {
	t.Helper()
	var reg publishRegistry
	if err := json.Unmarshal([]byte(doc), &reg); err != nil {
		t.Fatal(err)
	}
	return &reg
}

func TestBuildPublishedReportJoinsEverySource(t *testing.T) {
	// Given: a registry with an unscoped SDK group, a cdk-only group and a
	// soaking generated group; results from two SDKs and cdk; a gap; and a
	// quarantined flaky test.
	reg := publishRegistryFromJSON(t, `{"groups":[
		{"service":"s3","name":"s3-crud","tests":[
			{"name":"CreateBucket"},
			{"name":"PutObject","depends":["CreateBucket"]},
			{"name":"PutObjectTagged","op":"PutObject","requires":["docker"]}]},
		{"service":"s3","name":"s3-cdk","suites":["cdk"],"tests":[{"name":"Deploy"}]},
		{"service":"sqs","name":"sqs-gen-queue","generated":true,"state":"candidate","suites":["go-sdk"],"tests":[{"name":"PurgeQueue"}]}]}`)
	report := reportWithResults(
		resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusUnimplemented},
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass},
		resultSpec{suite: "rust-sdk", service: "s3", group: "s3-crud", test: "PutObject", status: compat.StatusFail},
		resultSpec{suite: "cdk", service: "s3", group: "s3-cdk", test: "Deploy", status: compat.StatusPass},
		resultSpec{suite: "go-sdk", service: "sqs", group: "sqs-gen-queue", test: "PurgeQueue", status: compat.StatusFail},
		resultSpec{suite: "go-sdk", service: "sns", group: "sns-ad-hoc", test: "Publish", status: compat.StatusPass},
	)
	addSkip(report, "go-sdk", "s3", "s3-crud", "PutObject", "dependency failed: CreateBucket")
	in := publishInputs{
		Report:   report,
		Registry: reg,
		Gaps: &gapsFile{Gaps: []struct {
			Service   string `json:"service"`
			Operation string `json:"operation"`
			Group     string `json:"group"`
			Reason    string `json:"reason"`
			Detail    string `json:"detail"`
		}{{Service: "sqs", Operation: "DeleteQueue", Group: "sqs-gen-queue", Reason: "never-probe", Detail: "destructive"}}},
		Flaky:   &flakyFile{Flaky: []flakyEntry{{Suite: "rust-sdk", Group: "s3-crud", Test: "PutObject", Issue: "#7"}}},
		Version: "v1.2.3",
		Now:     time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
	}

	// When: the report is built
	got := buildPublishedReport(in)

	// Then: suites follow the house order and the metadata is carried through
	if got.Version != "v1.2.3" || got.GeneratedAt != "2026-09-24T00:00:00Z" || got.IssueRepo != defaultIssueRepo {
		t.Fatalf("metadata = %q %q %q", got.Version, got.GeneratedAt, got.IssueRepo)
	}
	var suites []string
	for _, s := range got.Suites {
		suites = append(suites, s.ID)
	}
	if want := []string{"go-sdk", "cdk", "rust-sdk"}; !equalStrings(suites, want) {
		t.Fatalf("suites = %v, want %v", suites, want)
	}

	cell := func(service, group, test, suite string) (publishedResult, bool) {
		t.Helper()
		for _, s := range got.Services {
			if s.ID != service {
				continue
			}
			for _, g := range s.Groups {
				if g.ID != group {
					continue
				}
				for _, tt := range g.Tests {
					if tt.Name == test {
						r, ok := tt.Results[suite]
						return r, ok
					}
				}
			}
		}
		t.Fatalf("no test %s/%s/%s", service, group, test)
		return publishedResult{}, false
	}
	reasonOf := func(service, group, test, suite string) string {
		t.Helper()
		r, ok := cell(service, group, test, suite)
		if !ok {
			return "<absent>"
		}
		return r.Reason
	}

	if r := reasonOf("s3", "s3-crud", "CreateBucket", "go-sdk"); r != reasonNotEmulated {
		t.Errorf("501 = %q", r)
	}
	if r, _ := cell("s3", "s3-crud", "PutObject", "go-sdk"); r.Reason != reasonDependencyFailed || !equalStrings(r.BlockedBy, []string{"CreateBucket"}) {
		t.Errorf("cascade = %+v", r)
	}
	if r, _ := cell("s3", "s3-crud", "PutObject", "rust-sdk"); r.Reason != reasonQuarantined || r.Issue == nil || r.Issue.Number != 7 {
		t.Errorf("flaky = %+v", r)
	}
	// The registry expects an unscoped group of every SDK, so a missing
	// result is shown rather than dropped...
	if r := reasonOf("s3", "s3-crud", "PutObjectTagged", "rust-sdk"); r != reasonNotReported {
		t.Errorf("missing SDK result = %q", r)
	}
	// ...but never of cdk, which runs only groups scoped to it.
	if r := reasonOf("s3", "s3-crud", "PutObjectTagged", "cdk"); r != "<absent>" {
		t.Errorf("cdk out of scope = %q", r)
	}
	// And a scoped group expects nothing of the suites it does not name.
	if r := reasonOf("s3", "s3-cdk", "Deploy", "go-sdk"); r != "<absent>" {
		t.Errorf("scoped group = %q", r)
	}
	if r := reasonOf("sqs", "sqs-gen-queue", "PurgeQueue", "go-sdk"); r != reasonCandidate {
		t.Errorf("candidate = %q", r)
	}
	// A result the registry does not know is still published.
	if r, ok := cell("sns", "sns-ad-hoc", "Publish", "go-sdk"); !ok || r.Status != compat.StatusPass {
		t.Errorf("orphan = %+v %v", r, ok)
	}

	// Registry fields reach the test rows.
	for _, s := range got.Services {
		if s.ID == "s3" && s.Groups[0].Tests[2].Op != "PutObject" {
			t.Errorf("op = %q", s.Groups[0].Tests[2].Op)
		}
		if s.ID == "sqs" {
			if len(s.Untested) != 1 || s.Untested[0].Code != "never-probe" || s.Totals.Untested != 1 {
				t.Errorf("untested = %+v", s.Untested)
			}
			if !s.Groups[0].Generated || s.Groups[0].State != "candidate" {
				t.Errorf("generated group = %+v", s.Groups[0])
			}
		}
	}

	// Totals add up: every cell is a pass or has exactly one reason.
	sum := got.Totals.Pass
	for _, n := range got.Totals.ByReason {
		sum += n
	}
	if sum != got.Totals.Cells {
		t.Errorf("totals: pass+reasons = %d, cells = %d", sum, got.Totals.Cells)
	}
	if got.Totals.Untested != 1 {
		t.Errorf("untested total = %d", got.Totals.Untested)
	}
}

func TestPublishReportFileToleratesMissingOptionalInputs(t *testing.T) {
	dir := t.TempDir()
	results := filepath.Join(dir, "results.json")
	report := reportWithResults(resultSpec{suite: "go-sdk", service: "s3", group: "s3-crud", test: "CreateBucket", status: compat.StatusPass})
	if err := writeRunReportFile(results, report); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "compat-report.json")
	missing := filepath.Join(dir, "absent.json")
	err := publishReportFile(publishOptions{
		ResultsPath: results, RegistryPath: missing, GeneratedRegistryPath: missing,
		GapsPath: missing, FlakyPath: missing, IssuesPath: missing, CuratedPath: missing,
		OutPath: out, Version: "v0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc publishedReport
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != publishSchemaVersion || doc.Totals.Pass != 1 || len(doc.Services) != 1 {
		t.Fatalf("report = %+v", doc)
	}
}

func TestCuratedIssueLinksAreWellFormed(t *testing.T) {
	// compat/report-issues.json is hand-written; a typo in a target links
	// nothing and fails silently, so the shape is checked here.
	b, err := os.ReadFile(filepath.Join("..", "..", "compat", "report-issues.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file curatedLinksFile
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range file.Links {
		if !issueTargetPattern.MatchString(l.Target) {
			t.Errorf("target %q is not <service>[/<group-or-op>[/<test>]][@<suite>]", l.Target)
		}
		if l.Issue < 1 {
			t.Errorf("target %q has no issue number", l.Target)
		}
		if seen[l.Target] {
			t.Errorf("target %q is linked twice", l.Target)
		}
		seen[l.Target] = true
	}
}
