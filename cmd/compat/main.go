// cmd/compat/main.go — Overcast compatibility test CLI.
//
// Runs one or more test suite subprocesses, collects their NDJSON output, and
// prints a summary report. When --serve is set a live compatibility dashboard
// is served too.
//
// Unless an endpoint is pinned, compat starts and owns a throwaway Overcast
// instance on a free port — 4566/4567 are reserved for the developer's own
// instance (AGENTS.md § Reserved ports). See launch.go.
//
// Usage:
//
//	go run ./cmd/compat --dev            # dashboard + hot-reloading UI + browser
//	go run ./cmd/compat --format agent   # headless run, agent-readable summary
//	go build -o bin/compat ./cmd/compat
//
// Flags (the full list is in the var block below):
//
//	--dev             Dashboard + hot-reloading UI + browser, on free ports
//	--endpoint        Target an instance you already run (skips managing one)
//	--start-overcast  auto | always | never
//	--suite           Comma-separated suite names to run (default: all)
//	--shard           Run only shard i of n test groups, e.g. "2/4" (default: all)
//	--format          Output format: pretty | json | agent | junit
//	--serve           Start the compatibility dashboard HTTP server
//	--port            Preferred dashboard port; a free one is picked if taken
package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/overcast-sh/overcast/compat"
)

// Flags are package-level so that both main and run can read them; run holds
// the deferred cleanup that must survive every exit path.
var (
	endpoint        = flag.String("endpoint", envOr("OVERCAST_ENDPOINT", ""), "Overcast base URL (default: a throwaway instance compat starts itself)")
	region          = flag.String("region", envOr("OVERCAST_DEFAULT_REGION", "us-east-1"), "AWS region")
	suiteFlag       = flag.String("suite", "", "Comma-separated suite names to run (empty = all)")
	shardFlag       = flag.String("shard", "", "Run only shard i of n test groups, e.g. \"2/4\" (1-based i; empty = every group, today's behaviour)")
	format          = flag.String("format", envOr("OVERCAST_COMPAT_FORMAT", "pretty"), "Output format: pretty|json|agent")
	serve           = flag.Bool("serve", false, "Start the compatibility dashboard HTTP server")
	port            = flag.String("port", envOr("OVERCAST_COMPAT_PORT", ":7777"), "Preferred dashboard listen address; a free port is chosen if it is taken")
	resultsFile     = flag.String("results-file", envOr("OVERCAST_COMPAT_RESULTS_FILE", "compat-results.json"), "Path to persist last run results (empty to disable)")
	agentReportFile = flag.String("agent-report-file", envOr("OVERCAST_COMPAT_AGENT_REPORT", "compat-report.txt"), "Path to write the agent-friendly text report after each run (empty to disable)")
	reportMode      = flag.Bool("report", false, "Read an existing results file and print an agent-friendly summary (no tests are run)")
	mergeResults    = flag.String("merge-results", "", "Comma-separated result files or glob patterns to merge, then exit")
	// A directory of per-suite shards (compat/baseline/<suite>.json) or the
	// single file that predates the split — see baseline_shards.go. The name
	// stays --baseline-file so that every recorded invocation, in CI and in
	// people's shell history, keeps working.
	baselineFile        = flag.String("baseline-file", "compat/baseline", "Compatibility baseline: a directory of per-suite shards, or a single baseline JSON file")
	compareBaselineFlag = flag.Bool("compare-baseline", false, "Compare --results-file against --baseline-file, then exit")
	annotate            = flag.Bool("annotate", false, "Emit GitHub workflow ::error commands for baseline regressions so they surface as PR annotations")
	flakyFilePath       = flag.String("flaky-file", "compat/flaky.json", "Tests quarantined as intermittent: exempt from the baseline gate in both directions")
	updateBaselineFlag  = flag.Bool("update-baseline", false, "Update --baseline-file from --results-file with improvements only, then exit")
	maxFailures         = flag.Int("max-failures", -1, "Fail if --results-file holds more than N failing tests, whatever the baseline records (-1 disables). Quarantined flaky tests are excluded")

	registryFile = flag.String("registry-file", "compat/suites/registry.json", "Shared compat test registry")
	// The generated sibling is concatenated onto --registry-file everywhere the
	// registry is read. A missing file is an empty registry, so a checkout or
	// suite image that predates it behaves exactly as before.
	generatedRegistryFile = flag.String("generated-registry-file", "compat/suites/registry.generated.json", "Generated compat test registry, concatenated with --registry-file (missing = empty). Groups in state \"candidate\" are excluded from --compare-baseline and --max-failures")
	parityDebtFilePath    = flag.String("parity-debt-file", "compat/parity-debt.json", "Cross-suite parity debt file")
	// The candidate → gated soak. --promote-generated writes the promotions
	// ledger and nothing else; cmd/compatgen reads it and emits each group's
	// state, so the caller regenerates afterwards. See promote.go.
	promotionsFilePath  = flag.String("promotions-file", "compat/model/promotions.json", "Soak ledger read by cmd/compatgen to decide each generated group's state; --promote-generated is its only writer")
	promoteGeneratedRun = flag.Bool("promote-generated", false, "Soak --promote-runs against --generated-registry-file and promote qualifying candidate groups in --promotions-file, then exit. Regenerate afterwards (`make generate-compat-model`)")
	promoteRuns         = flag.String("promote-runs", "", "Run reports the soak reads: comma-separated files, globs, or directories of *.json. Each file is one run, identified by its base name")
	promoteMinRuns      = flag.Int("promote-min-runs", 3, "Consecutive agreeing runs a candidate group needs before it is promoted to \"gated\"")
	compareShadowRun    = flag.Bool("compare-shadow", false, "Compare every shadow group in --generated-registry-file against the hand-written group its \"shadowOf\" names, from --results-file, then exit. Non-zero on any (suite, test) that answered differently. Step 2 of the native-group migration: docs/plans/compat-coverage-modelgen.md §3.11")
	checkParity         = flag.Bool("check-parity", false, "Check cross-suite registry parity against --parity-debt-file, then exit")
	updateParityDebt    = flag.Bool("update-parity-debt", false, "Regenerate --parity-debt-file from --results-file, then exit")

	lintBaselineFrom = flag.String("lint-baseline-from", "", "Old compatibility baseline file for downgrade linting")
	lintBaselineTo   = flag.String("lint-baseline-to", "", "New compatibility baseline file for downgrade linting")
	lintBaselineSize = flag.Bool("lint-baseline-size", false, "Fail if any --baseline-file shard exceeds the per-shard size budget, then exit")
	reportFlakyAge   = flag.Bool("report-flaky-overdue", false, "Report quarantined tests older than the soft deadline, then exit")
	lintFlakyFrom    = flag.String("lint-flaky-from", "", "Old flaky list for growth linting")
	lintFlakyTo      = flag.String("lint-flaky-to", "", "New flaky list for growth linting")
	flakyGrowthOK    = flag.Bool("flaky-growth-approved", false, "Accept new flaky-list entries: a reviewer has agreed to the quarantine (CI sets this from the PR's quarantine-approved label). Per-entry checks (reason, issue, date, deadline) still apply")
	// --publish-report: the public report overcast.sh renders. See publish.go.
	publishReport     = flag.String("publish-report", "", "Join --results-file with the registry, gaps, flaky list and issue links into the public compatibility report at this path, then exit")
	publishVersion    = flag.String("publish-version", "", "Release version recorded in --publish-report (e.g. v0.42.0)")
	publishCommit     = flag.String("publish-commit", "", "Commit SHA recorded in --publish-report")
	gapsFilePath      = flag.String("gaps-file", "compat/model/gaps.json", "Operations the generator refused to test, published as untested by --publish-report")
	issuesFilePath    = flag.String("issues-file", "", "Marker-linked issues from scripts/compat-issues.py, for --publish-report (empty = none)")
	curatedIssuesPath = flag.String("curated-issues-file", "compat/report-issues.json", "Hand-written issue links for --publish-report; they beat marker links")
	issueRepo         = flag.String("issue-repo", defaultIssueRepo, "owner/repo that bare issue numbers in --publish-report refer to")
	interactive       = flag.Bool("interactive", false, "Start in interactive mode (long-lived suite processes)")
	noUI              = flag.Bool("no-ui", false, "Don't serve embedded UI (use with external Vite dev server)")

	// Environment management — see launch.go.
	dev               = flag.Bool("dev", false, "One-command dev loop: manage Overcast, serve the dashboard with a hot-reloading UI, open a browser")
	startOvercastMode = flag.String("start-overcast", envOr("OVERCAST_COMPAT_START", startOvercastAuto), "Manage a throwaway Overcast instance: auto|always|never")
	// Default is 127.0.0.1, not "localhost": on a dual-stack host "localhost"
	// resolves ::1 first and the container publishes IPv4 only, so every new
	// connection pays a ~2s IPv6-then-IPv4 fallback.
	overcastHost    = flag.String("overcast-host", envOr("OVERCAST_COMPAT_HOST", "127.0.0.1"), "Hostname the suites address the managed instance by (e.g. localhost.overcast.sh for virtual-host-style S3)")
	overcastBin     = flag.String("overcast-bin", envOr("OVERCAST_COMPAT_BIN", ""), "Run this overcast binary; naming one is honoured or the run fails (unset: bin/overcast, then PATH, then a container)")
	overcastImage   = flag.String("overcast-image", envOr("OVERCAST_COMPAT_IMAGE", defaultOvercastImage), "Run this container image; naming one selects the container even when a local binary exists. Unset, it is only the fallback for when no binary is found")
	overcastUI      = flag.Bool("overcast-ui", false, "Also expose the managed instance's own web UI on a free port")
	overcastTimeout = flag.Int("overcast-timeout", 60, "Seconds to wait for the managed instance to become healthy")
	// On by default because a compat run without it cannot invoke a Lambda,
	// and a flag defaulting off would have to be remembered by everyone
	// testing a release candidate. It is a flag at all — rather than
	// unconditional — because mounting the socket hands the emulator control
	// of this machine's Docker daemon, and a grant that large should be
	// visible in --help and refusable in one word.
	mountDockerSocket = flag.Bool("mount-docker-socket", true, "Bind-mount the host Docker socket into a managed container, so the instance can run Lambda and ECS containers (COMPAT_DOCKER_SOCK overrides the host path)")
	portBase          = flag.Int("port-base", defaultPortBase, "First port considered when scanning for free ports (never 4566/4567)")
	uiDev             = flag.Bool("ui-dev", false, "Serve the dashboard UI from Vite with hot reloading instead of the embedded build")
	uiDir             = flag.String("ui-dir", "", "Serve the dashboard UI from this directory instead of the build embedded in the binary")
	buildUI           = flag.Bool("build-ui", false, "Build the dashboard UI before serving it (implies --ui-dir compat/ui/dist)")
	openBrowserFlag   = flag.Bool("open", false, "Open the dashboard in a browser once it is ready")
)

// flagGiven reports whether a flag was set on the command line. flag.Visit
// walks only the flags that were actually passed, which is the one way to tell
// a caller's choice apart from a default — and several of these flags have
// defaults that look exactly like a choice.
func flagGiven(name string) bool {
	given := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// endpointPinned reports whether the caller chose the Overcast endpoint, in
// which case compat targets it rather than starting one of its own.
func endpointPinned() bool {
	return os.Getenv("OVERCAST_ENDPOINT") != "" || flagGiven("endpoint")
}

// artifactNamed reports whether a caller named an artifact to test: a
// non-empty value that came from somewhere other than the compiled-in default.
//
// The env var counts, for the same reason endpointPinned counts
// OVERCAST_ENDPOINT: OVERCAST_COMPAT_IMAGE has no meaning other than "test this
// image", nothing in this repo sets it ambiently, and a run that ignored it
// would be the very trap this closes. The compiled-in default is the only
// value that is nobody's request — and it is why the value alone cannot answer
// the question, since --overcast-image is non-empty either way.
//
// The empty-value guard covers an explicit `--overcast-image ""`, which asks
// for nothing and must not outrank a local build.
func artifactNamed(value, env string, onCommandLine bool) bool {
	return value != "" && (env != "" || onCommandLine)
}

// imageRequested reports whether the caller named the container image to test.
// That decides whether it outranks a locally built binary — see
// chooseOvercastArtifact.
func imageRequested() bool {
	return artifactNamed(*overcastImage, os.Getenv("OVERCAST_COMPAT_IMAGE"), flagGiven("overcast-image"))
}

// binRequested reports whether the caller named a binary to test. Unlike the
// image, this flag's default is empty, so a value is nearly always a request —
// it is read the same way anyway, so the two flags cannot drift apart.
func binRequested() bool {
	return artifactNamed(*overcastBin, os.Getenv("OVERCAST_COMPAT_BIN"), flagGiven("overcast-bin"))
}

// warnUnusedArtifactFlags says so when the caller named an artifact to test but
// compat is not the one starting Overcast. --overcast-image and --overcast-bin
// only ever apply to a managed instance, so pinning --endpoint or passing
// --start-overcast=never discards them — which is the same "you tested
// something other than what you named" trap that chooseOvercastArtifact exists
// to close, arriving by a different road.
func warnUnusedArtifactFlags(endpointURL string) {
	var named string
	switch {
	case imageRequested():
		named = "--overcast-image " + *overcastImage
	case binRequested():
		named = "--overcast-bin " + *overcastBin
	default:
		return
	}
	why := "--start-overcast=" + *startOvercastMode
	if endpointPinned() {
		why = "the endpoint is pinned"
	}
	fmt.Fprintf(os.Stderr,
		"compat: WARNING: %s is ignored (%s, so compat is not starting Overcast); "+
			"the suites run against whatever is already serving %s\n",
		named, why, endpointURL)
}

// skipDockerEnv is the suites' own opt-out: a suite that sees it drops the
// "docker" capability, so every test the registry marks `requires: [docker]`
// is reported as a skip rather than run. compat/docker-compose.yml and the CI
// workflow both decide it for themselves, and this never overrides a value
// somebody set — which is also what keeps the automatic skip below out of CI,
// where the endpoint is pinned and no instance is managed at all.
const skipDockerEnv = "OVERCAST_COMPAT_SKIP_DOCKER"

// announceNoDocker reports a managed instance that cannot start containers,
// once, at the top of the run — and, when the machine is the reason, tells the
// suites not to run the tests that need one.
//
// Before this existed, an --overcast-image run with no socket mounted reached
// the end and reported five failures across four suites, every one of them
// reading as a broken Lambda (issue #867). The environment was the fault, and
// nothing said so until somebody read the emulator's own error text.
//
// The skip is confined to the environmental case on purpose: see dockerVerdict
// in launch.go for where that line is drawn and why moving it would recreate
// the blindspot compat/AGENTS.md warns about.
func announceNoDocker(w io.Writer, noDocker string, environmental bool) {
	if noDocker == "" {
		return
	}
	fmt.Fprintf(w, "compat: WARNING: the managed instance cannot run containers — %s\n", noDocker)
	if !environmental {
		fmt.Fprintf(w,
			"compat: the Docker-dependent tests (Lambda invocation above all) will run and "+
				"fail; compat gave the instance what it needed, so the failures are its answer\n")
		return
	}
	if existing, set := os.LookupEnv(skipDockerEnv); set {
		if existing != "1" {
			fmt.Fprintf(w,
				"compat: leaving %s=%s as you set it, so the Docker-dependent tests will "+
					"run and fail for want of a daemon\n", skipDockerEnv, existing)
		}
		return
	}
	if err := os.Setenv(skipDockerEnv, "1"); err != nil {
		fmt.Fprintf(w, "compat: could not set %s: %v\n", skipDockerEnv, err)
		return
	}
	fmt.Fprintf(w,
		"compat: skipping the tests that need a daemon (%s=1) — this machine cannot run "+
			"them, and failing them would blame Overcast for that. Set %s=0 to run them "+
			"anyway. The cli suite decides for itself and is not covered.\n",
		skipDockerEnv, skipDockerEnv)
}

func main() {
	flag.Parse()

	if *compareBaselineFlag {
		if err := compareBaselineFile(*baselineFile, *resultsFile); err != nil {
			fmt.Fprintf(os.Stderr, "compat: baseline check failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *maxFailures >= 0 {
		if err := enforceMaxFailuresFile(*resultsFile, *maxFailures); err != nil {
			fmt.Fprintf(os.Stderr, "compat: failure gate failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *updateBaselineFlag {
		if err := updateBaselineFile(*baselineFile, *resultsFile); err != nil {
			fmt.Fprintf(os.Stderr, "compat: update baseline: %v\n", err)
			os.Exit(2)
		}
		return
	}

	if *promoteGeneratedRun {
		if err := promoteGeneratedFile(*promotionsFilePath, *registryFile, *generatedRegistryFile,
			*promoteRuns, *promoteMinRuns, *annotate, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "compat: promote generated groups: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *compareShadowRun {
		if err := compareShadowFile(*generatedRegistryFile, *resultsFile, *annotate, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "compat: shadow comparison: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *checkParity {
		if err := checkParityFiles(*registryFile, *generatedRegistryFile, *parityDebtFilePath, *resultsFile, *annotate); err != nil {
			fmt.Fprintf(os.Stderr, "compat: parity check failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *updateParityDebt {
		if err := updateParityDebtFile(*registryFile, *generatedRegistryFile, *parityDebtFilePath, *resultsFile); err != nil {
			fmt.Fprintf(os.Stderr, "compat: update parity debt: %v\n", err)
			os.Exit(2)
		}
		return
	}

	if *reportFlakyAge {
		if err := reportFlakyOverdue(*flakyFilePath); err != nil {
			fmt.Fprintf(os.Stderr, "compat: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *lintFlakyFrom != "" || *lintFlakyTo != "" {
		if *lintFlakyFrom == "" || *lintFlakyTo == "" {
			fmt.Fprintln(os.Stderr, "compat: both --lint-flaky-from and --lint-flaky-to are required")
			os.Exit(2)
		}
		if err := lintFlakyChangeFiles(*lintFlakyFrom, *lintFlakyTo, *flakyGrowthOK); err != nil {
			fmt.Fprintf(os.Stderr, "compat: flaky list check failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *lintBaselineFrom != "" || *lintBaselineTo != "" {
		if *lintBaselineFrom == "" || *lintBaselineTo == "" {
			fmt.Fprintln(os.Stderr, "compat: both --lint-baseline-from and --lint-baseline-to are required")
			os.Exit(2)
		}
		if err := lintBaselineChangeFiles(*lintBaselineFrom, *lintBaselineTo); err != nil {
			fmt.Fprintf(os.Stderr, "compat: baseline change lint failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *lintBaselineSize {
		if err := lintBaselineShardSizes(*baselineFile); err != nil {
			fmt.Fprintf(os.Stderr, "compat: baseline size check failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *publishReport != "" {
		if err := publishReportFile(publishOptions{
			ResultsPath: *resultsFile, RegistryPath: *registryFile, GeneratedRegistryPath: *generatedRegistryFile,
			GapsPath: *gapsFilePath, FlakyPath: *flakyFilePath, IssuesPath: *issuesFilePath, CuratedPath: *curatedIssuesPath,
			OutPath: *publishReport, Version: *publishVersion, Commit: *publishCommit, IssueRepo: *issueRepo,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "compat: publish report: %v\n", err)
			os.Exit(2)
		}
		return
	}

	if *mergeResults != "" {
		report, err := mergeRunReportFiles(splitCSV(*mergeResults))
		if err != nil {
			fmt.Fprintf(os.Stderr, "compat: merge results: %v\n", err)
			os.Exit(2)
		}
		if *resultsFile != "" {
			if err := writeRunReportFile(*resultsFile, report); err != nil {
				fmt.Fprintf(os.Stderr, "compat: save merged results: %v\n", err)
				os.Exit(2)
			}
		}
		switch *format {
		case "json":
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				fmt.Fprintf(os.Stderr, "compat: json encode: %v\n", err)
				os.Exit(2)
			}
		case "agent":
			printAgentReport(report)
		default:
			printPretty(report)
		}
		return
	}

	// --report: parse an existing file and summarise it for agents, then exit.
	if *reportMode {
		path := *resultsFile
		if path == "" {
			path = "compat-results.json"
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "compat: cannot read %s: %v\n", path, err)
			os.Exit(2)
		}
		var rep compat.RunReport
		if err := json.Unmarshal(data, &rep); err != nil {
			fmt.Fprintf(os.Stderr, "compat: cannot parse %s: %v\n", path, err)
			os.Exit(2)
		}
		printAgentReport(&rep)
		return
	}

	os.Exit(run())
}

// run performs a compat run and/or serves the dashboard, returning the process
// exit code. It is separate from main so that deferred cleanup — stopping a
// managed Overcast instance, the Vite dev server — always happens, which
// os.Exit inside main would skip.
func run() int {
	// Settle the suite selection first: a mistyped --suite is worth saying so
	// before building a UI or launching a throwaway Overcast for a run that
	// cannot happen.
	suites, err := resolveSuiteSelection(*suiteFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat: %v\n", err)
		return 2
	}

	// --shard: resolve which groups this shard runs, same reasoning as above —
	// fail before anything is launched. See shard.go for the partitioning.
	shardGroups, err := resolveShardGroups(*shardFlag, *registryFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat: %v\n", err)
		return 2
	}

	// --dev is the one-command loop: manage the emulator, serve the dashboard
	// with a hot-reloading UI, open a browser.
	if *dev {
		*serve = true
		*interactive = true
		*uiDev = true
		*openBrowserFlag = true
	}
	if *uiDev {
		*noUI = true
	}
	if *buildUI {
		if err := buildDashboardUI(context.Background(), uiProjectDir(),
			func(f string, a ...any) { fmt.Fprintf(os.Stderr, "compat: "+f+"\n", a...) }); err != nil {
			fmt.Fprintf(os.Stderr, "compat: %v\n", err)
			return 2
		}
		if *uiDir == "" {
			*uiDir = uiDistDir()
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Pick a dashboard port that is actually free, and never the reserved
	// pair, so concurrent sessions don't collide.
	addr, err := resolveListenAddr(*port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat: %v\n", err)
		return 2
	}
	// 127.0.0.1, not localhost: a dual-stack host resolves "localhost" to ::1
	// first, adding a ~2s IPv6-then-IPv4 fallback to the dashboard's own URL.
	compatURL := "http://127.0.0.1" + addr

	// Resolve the endpoint. Unless the caller pinned one, compat starts and
	// owns a throwaway instance on a free port — AGENTS.md reserves 4566/4567
	// for the developer's own instance.
	endpointURL := *endpoint
	managed := shouldStartOvercast(*startOvercastMode, endpointPinned())
	if managed {
		oc, err := startOvercast(ctx, overcastOptions{
			Host:              *overcastHost,
			Bin:               *overcastBin,
			BinRequested:      binRequested(),
			Image:             *overcastImage,
			ImageRequested:    imageRequested(),
			PortBase:          *portBase,
			WithUI:            *overcastUI,
			MountDockerSocket: *mountDockerSocket,
			DockerSocket:      os.Getenv("COMPAT_DOCKER_SOCK"),
			Timeout:           time.Duration(*overcastTimeout) * time.Second,
			LogLevel:          envOr("OVERCAST_LOG_LEVEL", "warn"),
			Logf:              func(f string, a ...any) { fmt.Fprintf(os.Stderr, "compat: "+f+"\n", a...) },
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "compat: %v\n", err)
			return 2
		}
		defer oc.Stop()
		endpointURL = oc.Endpoint
		// Name the artifact, not just the mode: "docker" is the same word
		// whether the image was the release candidate the caller asked for or
		// the compiled-in default (issue #801).
		fmt.Fprintf(os.Stderr, "compat: Overcast ready at %s (%s, managed by compat)\n",
			oc.Endpoint, oc.Artifact.Describe())
		if oc.UIURL != "" {
			fmt.Fprintf(os.Stderr, "compat: Overcast web UI at %s\n", oc.UIURL)
		}
		announceNoDocker(os.Stderr, oc.NoDocker, oc.DockerIsEnvironmental)
	}
	if endpointURL == "" {
		// --start-overcast=never with no endpoint: target the developer's own
		// instance on the default port, which is what 4566 is reserved for.
		// 127.0.0.1, not localhost: a dual-stack host resolves "localhost" to
		// ::1 first, adding a ~2s IPv6-then-IPv4 fallback to every connection
		// the container (IPv4-only) never answers on.
		endpointURL = fmt.Sprintf("http://127.0.0.1:%d", reservedAPIPort)
	}
	if !managed {
		warnUnusedArtifactFlags(endpointURL)
	}

	// --ui-dev: Vite serves the dashboard UI with hot reloading and proxies
	// its API calls to the compat server.
	dashboardURL := compatURL
	if *uiDev {
		viteURL, stopVite, err := startViteDev(ctx, viteOptions{
			Dir:       uiProjectDir(),
			CompatURL: compatURL,
			PortBase:  *portBase,
			Logf:      func(f string, a ...any) { fmt.Fprintf(os.Stderr, "compat: "+f+"\n", a...) },
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "compat: %v\n", err)
			return 2
		}
		defer stopVite()
		dashboardURL = viteURL
	}

	// --interactive: long-lived suite processes with orchestrator control.
	if *serve && *interactive {
		srv := compat.NewServer(dashboardUIFS())
		if *resultsFile != "" {
			if err := srv.LoadResultsFile(*resultsFile); err != nil {
				fmt.Fprintf(os.Stderr, "compat: warning: %v\n", err)
			}
		}

		configs := compat.DefaultSuiteConfigs(endpointURL, *region)
		if len(suites) > 0 {
			configs = compat.FilterSuiteConfigs(configs, suites)
		}

		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
		orch := compat.NewOrchestrator(ctx, configs, srv.Broadcast, logger)
		orch.Endpoint = endpointURL
		orch.Region = *region
		// Finalise results whenever the dashboard's queue drains, so
		// GET /results, the results file, and --report reflect interactive
		// runs the same way they do batch ones.
		orch.OnIdle = func(rep *compat.RunReport) {
			srv.FinishRun(rep)
			if *resultsFile != "" {
				if err := srv.SaveResultsFile(*resultsFile); err != nil {
					fmt.Fprintf(os.Stderr, "compat: warning: %v\n", err)
				}
			}
			if *agentReportFile != "" {
				if err := writeAgentReportFile(*agentReportFile, rep); err != nil {
					fmt.Fprintf(os.Stderr, "compat: warning: %v\n", err)
				}
			}
		}
		srv.SetOrchestrator(orch)

		if err := orch.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "compat: orchestrator start: %v\n", err)
			return 2
		}

		httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()} //nolint:gosec
		go func() {
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "compat: server error: %v\n", err)
			}
		}()
		printBanner(dashboardURL, compatURL, endpointURL, true)
		if *openBrowserFlag {
			openBrowser(dashboardURL)
		}

		<-ctx.Done()
		orch.Shutdown()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return 0
	}

	// --serve: start the live dashboard server before running suites.
	var srv *compat.Server
	var httpSrv *http.Server
	if *serve {
		srv = compat.NewServer(dashboardUIFS())
		// Pre-populate from disk so the dashboard shows the last run immediately.
		if *resultsFile != "" {
			if err := srv.LoadResultsFile(*resultsFile); err != nil {
				fmt.Fprintf(os.Stderr, "compat: warning: %v\n", err)
			}
		}

		// Register the re-run function so the dashboard can trigger new runs.
		rf := *resultsFile
		arf := *agentReportFile
		srv.SetRunFunc(func(filter compat.RunFilter) error {
			runCfg := compat.RunConfig{
				Endpoint:  endpointURL,
				Region:    *region,
				Suites:    suites,
				Service:   filter.Service,
				Group:     filter.Group,
				Test:      filter.Test,
				TestPairs: filter.TestPairs,
				OnEvent:   srv.Broadcast,
			}
			// A filter may narrow suites further.
			if filter.Suite != "" {
				runCfg.Suites = []string{filter.Suite}
			}
			srv.ResetRun(runCfg.Suites...)
			r2 := compat.NewRunner(runCfg)
			rep, err := r2.Run(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "compat: re-run error: %v\n", err)
				return err
			}
			srv.FinishRun(rep)
			if rf != "" {
				if err := srv.SaveResultsFile(rf); err != nil {
					fmt.Fprintf(os.Stderr, "compat: warning: %v\n", err)
				}
			}
			if arf != "" {
				if err := writeAgentReportFile(arf, rep); err != nil {
					fmt.Fprintf(os.Stderr, "compat: warning: %v\n", err)
				}
			}
			return nil
		})

		httpSrv = &http.Server{Addr: addr, Handler: srv.Handler()} //nolint:gosec
		go func() {
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "compat: server error: %v\n", err)
			}
		}()
		printBanner(dashboardURL, compatURL, endpointURL, false)
		if *openBrowserFlag {
			openBrowser(dashboardURL)
		}
	}

	cfg := compat.RunConfig{
		Endpoint: endpointURL,
		Region:   *region,
		Suites:   suites,
		// shardGroups is "" unless --shard was given, in which case it is
		// already the comma-separated OVERCAST_COMPAT_GROUPS value for this
		// shard — see resolveShardGroups in shard.go.
		Group: shardGroups,
	}
	if srv != nil {
		cfg.OnEvent = srv.Broadcast
	}

	runner := compat.NewRunner(cfg)
	if srv != nil {
		// Broadcast run_reset before the initial run so the UI knows which
		// suites are about to refresh and can keep the others visible.
		srv.ResetRun(runner.Suites()...)
	}
	report, err := runner.Run(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat: fatal: %v\n", err)
		return 2
	}

	if *resultsFile != "" {
		if err := writeRunReportFile(*resultsFile, report); err != nil {
			fmt.Fprintf(os.Stderr, "compat: save results: %v\n", err)
			return 2
		}
	}

	if srv != nil {
		srv.FinishRun(report)
		if *agentReportFile != "" {
			if err := writeAgentReportFile(*agentReportFile, report); err != nil {
				fmt.Fprintf(os.Stderr, "compat: warning: %v\n", err)
			}
		}
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "compat: json encode: %v\n", err)
			return 2
		}
	case "junit":
		if err := printJUnit(report); err != nil {
			fmt.Fprintf(os.Stderr, "compat: junit: %v\n", err)
			return 2
		}
	case "agent":
		printAgentReport(report)
	default:
		printPretty(report)
	}

	// When --serve is active, keep the server alive so users can review results.
	if *serve {
		fmt.Fprintf(os.Stderr, "compat: run complete — dashboard still at %s (Ctrl+C to quit)\n", dashboardURL)
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}

	// Always exit 0: individual test failures are expected coverage gaps.
	// Only infrastructure errors (handled above) use non-zero exit codes.
	return 0
}

// dashboardUIFS chooses where the dashboard UI is served from: nothing when an
// external Vite server owns it, a directory when --ui-dir/--build-ui asked for
// a freshly built one, otherwise the build embedded in this binary.
func dashboardUIFS() fs.FS {
	switch {
	case *noUI:
		return nil
	case *uiDir != "":
		return os.DirFS(*uiDir)
	default:
		return uiFS
	}
}

// printBanner summarises the URLs a session is reachable on. Every port is
// chosen at runtime, so this is the only reliable place to learn them.
func printBanner(dashboardURL, compatURL, endpointURL string, interactive bool) {
	fmt.Fprintf(os.Stderr, "\n  Dashboard:  %s\n", dashboardURL)
	if dashboardURL != compatURL {
		fmt.Fprintf(os.Stderr, "  Compat API: %s\n", compatURL)
	}
	fmt.Fprintf(os.Stderr, "  Overcast:   %s\n", endpointURL)
	if interactive {
		fmt.Fprintf(os.Stderr, "\n  Tests run on demand from the dashboard. Ctrl+C to stop.\n\n")
	} else {
		fmt.Fprintf(os.Stderr, "\n")
	}
}

func printPretty(report *compat.RunReport) {
	fmt.Printf("Overcast Compatibility Report\n")
	fmt.Printf("Endpoint: %s\n", report.Endpoint)
	fmt.Printf("Duration: %s\n\n", report.FinishedAt.Sub(report.StartedAt).Round(1e6))

	var totalPass, totalFail, totalSkip int

	for _, sr := range report.Suites {
		fmt.Printf("Suite: %s\n", sr.Suite)
		fmt.Printf("  %-40s %6s %6s %6s\n", "Group", "Pass", "Fail", "Skip")
		fmt.Printf("  %-40s %6s %6s %6s\n", strings.Repeat("-", 40), "------", "------", "------")
		for _, gr := range sr.Groups {
			prefix := "✓"
			if gr.Failed > 0 {
				prefix = "✗"
			}
			fmt.Printf("  %s %-38s %6d %6d %6d\n", prefix, gr.Name, gr.Passed, gr.Failed, gr.Skipped)
			if gr.Failed > 0 {
				for _, t := range gr.Tests {
					if t.Status == compat.StatusFail {
						msg := t.Error
						if len(msg) > 120 {
							msg = msg[:117] + "..."
						}
						fmt.Printf("      ✗ %s: %s\n", t.Test, msg)
					}
				}
			}
		}
		fmt.Printf("\n  Total: %d passed, %d failed, %d skipped\n\n",
			sr.Passed, sr.Failed, sr.Skipped)
		totalPass += sr.Passed
		totalFail += sr.Failed
		totalSkip += sr.Skipped
	}

	fmt.Printf("Overall: %d passed, %d failed, %d skipped\n",
		totalPass, totalFail, totalSkip)
}

// writeRunReportFile writes the structured compatibility report atomically.
func writeRunReportFile(path string, report *compat.RunReport) error {
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("compat: marshal results: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("compat: write results: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("compat: write results: %w", err)
	}
	return nil
}

// resolveSuiteSelection turns the --suite flag into the list of suites to run.
// An empty flag selects every suite; any name that no suite answers to is an
// error naming it and listing the ones that exist.
func resolveSuiteSelection(flagValue string) ([]string, error) {
	names := splitCSV(flagValue)
	if err := compat.ValidateSuiteNames(names); err != nil {
		return nil, err
	}
	return names, nil
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func mergeRunReportFiles(inputs []string) (*compat.RunReport, error) {
	paths, err := expandResultInputs(inputs)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no result files matched")
	}

	merged := &compat.RunReport{}
	for _, path := range paths {
		report, err := readRunReportFile(path)
		if err != nil {
			return nil, err
		}
		if merged.Endpoint == "" {
			merged.Endpoint = report.Endpoint
		}
		if !report.StartedAt.IsZero() && (merged.StartedAt.IsZero() || report.StartedAt.Before(merged.StartedAt)) {
			merged.StartedAt = report.StartedAt
		}
		if report.FinishedAt.After(merged.FinishedAt) {
			merged.FinishedAt = report.FinishedAt
		}
		merged.Suites = append(merged.Suites, report.Suites...)
	}
	sort.SliceStable(merged.Suites, func(i, j int) bool {
		return suiteOrder(merged.Suites[i].Suite) < suiteOrder(merged.Suites[j].Suite)
	})
	return merged, nil
}

func expandResultInputs(inputs []string) ([]string, error) {
	seen := make(map[string]struct{})
	var paths []string
	for _, input := range inputs {
		matches, err := filepath.Glob(input)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", input, err)
		}
		if len(matches) == 0 {
			matches = []string{input}
		}
		for _, match := range matches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			paths = append(paths, match)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func readRunReportFile(path string) (*compat.RunReport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var report compat.RunReport
	if err := json.Unmarshal(b, &report); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &report, nil
}

func suiteOrder(name string) int {
	for i, suite := range []string{"node-js-sdk", "python-sdk", "go-sdk", "cli", "cdk", "java-sdk", "dotnet-sdk", "rust-sdk"} {
		if name == suite {
			return i
		}
	}
	return 1000
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// writeAgentReportFile writes the agent-friendly text report to path,
// atomically replacing any previous file.
func writeAgentReportFile(path string, report *compat.RunReport) error {
	// Write the temp file in the same directory as path so os.Rename succeeds
	// even when the destination is on a different filesystem from os.TempDir().
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, ".compat-report-*.txt")
	if err != nil {
		return fmt.Errorf("compat: agent report: %w", err)
	}
	tmp := f.Name()
	old := os.Stdout
	os.Stdout = f
	printAgentReport(report)
	os.Stdout = old
	if err := f.Close(); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("compat: agent report: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("compat: agent report: %w", err)
	}
	return nil
}

// printAgentReport prints a terse, structured summary optimised for AI agents.
// It groups results by action category so an agent can quickly decide what to
// implement or fix next.
//
// Categories:
//  1. Suite totals (one line per suite).
//  2. Unimplemented services — emulator returns 501; map to internal/services/<svc>.
//  3. Genuine failures — assertion errors that should not be failing.
//  4. Cascade failures — failed only because an earlier step in the same group failed.
func printAgentReport(report *compat.RunReport) {
	fmt.Printf("=== Compat Results — %s ===\n", report.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
	fmt.Printf("Endpoint: %s   Duration: %s\n\n",
		report.Endpoint, report.FinishedAt.Sub(report.StartedAt).Round(1e6))

	// --- Suite totals ---
	fmt.Printf("%-16s %5s %5s %7s %5s\n", "SUITE", "Pass", "Fail", "Unimpl", "Skip")
	fmt.Printf("%-16s %5s %5s %7s %5s\n", strings.Repeat("-", 16), "-----", "-----", "-------", "-----")
	for _, sr := range report.Suites {
		fmt.Printf("%-16s %5d %5d %7d %5d\n",
			sr.Suite, sr.Passed, sr.Failed, sr.Unimplemented, sr.Skipped)
	}
	fmt.Println()

	// --- Unimplemented: keyed by service, then op name → suites that saw it ---
	// Key: "service/op"  Value: set of suite names
	type opKey struct{ service, op string }
	unimplOps := make(map[opKey]map[string]struct{})
	for _, sr := range report.Suites {
		for _, gr := range sr.Groups {
			for _, t := range gr.Tests {
				if t.Status != compat.StatusUnimplemented {
					continue
				}
				k := opKey{service: gr.Service, op: t.Test}
				if unimplOps[k] == nil {
					unimplOps[k] = make(map[string]struct{})
				}
				unimplOps[k][sr.Suite] = struct{}{}
			}
		}
	}

	// Group by service.
	type serviceOp struct {
		op     string
		suites []string
	}
	svcOps := make(map[string][]serviceOp)
	for k, suiteSet := range unimplOps {
		var ss []string
		for s := range suiteSet {
			ss = append(ss, s)
		}
		sort.Strings(ss)
		svcOps[k.service] = append(svcOps[k.service], serviceOp{op: k.op, suites: ss})
	}
	svcs := make([]string, 0, len(svcOps))
	for s := range svcOps {
		svcs = append(svcs, s)
	}
	sort.Strings(svcs)

	if len(svcs) > 0 {
		fmt.Println("UNIMPLEMENTED SERVICES  (emulator returns 501 — implement in internal/services/<service>/)")
		for _, svc := range svcs {
			ops := svcOps[svc]
			sort.Slice(ops, func(i, j int) bool { return ops[i].op < ops[j].op })
			fmt.Printf("  %-22s → internal/services/%s/\n", svc, svc)
			for _, op := range ops {
				fmt.Printf("    %-28s [%s]\n", op.op, strings.Join(op.suites, ", "))
			}
		}
		fmt.Println()
	}

	// --- Genuine failures vs cascade failures.
	// A test is a cascade failure when its group already had an earlier failure
	// and the error contains phrases like "no <resource> from <PreviousOp>".
	type failEntry struct {
		suite   string
		group   string
		test    string
		err     string
		cascade bool
	}
	var fails []failEntry
	for _, sr := range report.Suites {
		for _, gr := range sr.Groups {
			// Track which tests in this group failed so we can detect cascades.
			groupFailed := false
			for _, t := range gr.Tests {
				if t.Status != compat.StatusFail {
					continue
				}
				cascade := groupFailed && isCascadeError(t.Error)
				fails = append(fails, failEntry{
					suite:   sr.Suite,
					group:   gr.Name,
					test:    t.Test,
					err:     t.Error,
					cascade: cascade,
				})
				groupFailed = true
			}
		}
	}

	var genuine, cascades []failEntry
	for _, f := range fails {
		if f.cascade {
			cascades = append(cascades, f)
		} else {
			genuine = append(genuine, f)
		}
	}

	if len(genuine) > 0 {
		fmt.Println("GENUINE FAILURES  (should work but doesn't — investigate emulator implementation)")
		for _, f := range genuine {
			msg := f.err
			if len(msg) > 160 {
				msg = msg[:157] + "..."
			}
			fmt.Printf("  %s/%s/%s\n    → %s\n", f.suite, f.group, f.test, msg)
		}
		fmt.Println()
	}

	if len(cascades) > 0 {
		fmt.Println("CASCADE FAILURES  (caused by a genuine failure above — fix that first)")
		for _, f := range cascades {
			fmt.Printf("  %s/%s/%s\n", f.suite, f.group, f.test)
		}
		fmt.Println()
	}

	total := len(genuine) + len(cascades)
	if total == 0 && len(svcs) == 0 {
		fmt.Println("All tests passed.")
	}
}

// isCascadeError returns true when the error message is characteristic of a
// test that failed only because a previous step in the same group failed
// (e.g. "no bucket from CreateBucket", "no queue from CreateQueue").
func isCascadeError(msg string) bool {
	lower := strings.ToLower(msg)
	markers := []string{" from create", " from register", " from put", " from start", "no cluster from", "no state machine from", "no function from"}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// JUnit XML output — CI-friendly format (GitLab, Jenkins, etc.)
// ---------------------------------------------------------------------------

type junitXML struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     float64     `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string     `xml:"name,attr"`
	ClassName string     `xml:"classname,attr"`
	Time      float64    `xml:"time,attr"`
	Failure   *junitFail `xml:"failure,omitempty"`
	Skipped   *junitSkip `xml:"skipped,omitempty"`
	Error     *junitFail `xml:"error,omitempty"`
}

type junitFail struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type junitSkip struct {
	Message string `xml:"message,attr"`
}

func printJUnit(report *compat.RunReport) error {
	fmt.Fprint(os.Stdout, xml.Header)

	var j junitXML
	for _, sr := range report.Suites {
		if sr == nil {
			continue
		}
		js := junitSuite{
			Name: sr.Suite,
			Time: report.FinishedAt.Sub(report.StartedAt).Seconds(),
		}
		for _, gr := range sr.Groups {
			for _, tr := range gr.Tests {
				js.Tests++
				tc := junitCase{
					Name:      gr.Name + "." + tr.Test,
					ClassName: sr.Suite + "." + gr.Name,
					Time:      float64(tr.DurationMS) / 1000.0,
				}
				switch tr.Status {
				case "fail":
					js.Failures++
					tc.Failure = &junitFail{
						Message: tr.Test + " failed",
						Body:    tr.Error,
					}
				case "skip", "na":
					js.Skipped++
					msg := tr.Error
					if msg == "" {
						msg = "skipped"
					}
					tc.Skipped = &junitSkip{Message: msg}
				case "unimplemented":
					js.Skipped++
					tc.Skipped = &junitSkip{Message: "unimplemented: " + tr.Error}
				}
				js.Cases = append(js.Cases, tc)
			}
		}
		j.Suites = append(j.Suites, js)
	}

	enc := xml.NewEncoder(os.Stdout)
	enc.Indent("", "  ")
	return enc.Encode(j)
}
