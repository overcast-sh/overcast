// Command runner is the entry point for the Overcast compat CLI suite.
// It loads registry.json, builds test groups from all service group implementations,
// and runs them against a live Overcast endpoint, emitting NDJSON results to stdout.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/overcast-sh/overcast-compat-cli/internal/groups"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
	"github.com/overcast-sh/overcast-compat-cli/internal/registry"
	"github.com/overcast-sh/overcast-compat-cli/internal/scenario"
)

// splitCSV parses a comma-separated env var into a set of trimmed non-empty
// entries. Returns nil if the var is unset or empty (meaning "no filter").
func splitCSV(v string) map[string]bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	out := map[string]bool{}
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const suite = "cli"

func main() {
	endpoint := os.Getenv("OVERCAST_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	region := os.Getenv("OVERCAST_DEFAULT_REGION")
	if region == "" {
		region = "us-east-1"
	}
	runID := os.Getenv("OVERCAST_COMPAT_RUN_ID")
	if runID == "" {
		runID = "local"
	}

	reg, err := registry.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[cli] fatal: %v\n", err)
		os.Exit(1)
	}

	// If the aws CLI is not installed, emit all tests as N/A and exit cleanly.
	// The CLI suite requires the AWS CLI binary; without it no test can run.
	if _, lookErr := exec.LookPath("aws"); lookErr != nil {
		registry.EmitAllNA(reg, suite, runID, "aws CLI not found in PATH")
		return
	}

	svcGroups := groups.All()

	// Flatten all impls and setup/teardown from every service group. The impls
	// go through MergeImpls rather than a plain map assignment: a key two
	// service files both register would otherwise lose one implementation with
	// nothing said about it.
	implSources := make([]registry.ImplSource, 0, len(svcGroups))
	setupFns := map[string]func(context.Context, *harness.TestContext) error{}
	teardownFns := map[string]func(context.Context, *harness.TestContext) error{}

	for _, sg := range svcGroups {
		implSources = append(implSources, registry.ImplSource{Name: sg.Name, Impls: sg.Impls})
		for name, fn := range sg.Setup {
			setupFns[name] = fn
		}
		for name, fn := range sg.Teardown {
			teardownFns[name] = fn
		}
	}

	impls, err := registry.MergeImpls(implSources, suite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	if err := registry.ValidateImpls(reg, impls, suite); err != nil {
		fmt.Fprintf(os.Stderr, "cli: %v\n", err)
		os.Exit(1)
	}

	// Detect Docker availability for tests that require it (e.g. Lambda invoke).
	caps := map[string]bool{}
	if _, dockerErr := exec.LookPath("docker"); dockerErr == nil {
		// docker CLI exists — try a quick ping to confirm the daemon is reachable.
		if pingErr := exec.Command("docker", "info").Run(); pingErr == nil {
			caps["docker"] = true
		}
	}

	// The scenario backend executes the generated groups — the ones carrying a
	// "scenario" path instead of a registered impl. Their setup and teardown
	// are step lists in the same file, so they are installed here, before
	// BuildGroups reads the maps. A hand-written group never declares a
	// scenario (the field is only legal in registry.generated.json), so this
	// cannot displace a registered setup.
	scenarios := scenario.New(registry.RepoRoot())
	for _, rg := range reg.Groups {
		if fn, ok := scenarios.Setup(rg); ok {
			setupFns[rg.Name] = fn
		}
		if fn, ok := scenarios.Teardown(rg); ok {
			teardownFns[rg.Name] = fn
		}
	}

	testGroups := registry.BuildGroups(reg, impls, registry.BuildGroupsOptions{
		Suite:        suite,
		Setup:        setupFns,
		Teardown:     teardownFns,
		Capabilities: caps,
		Scenario:     scenarios.Resolve,
	})

	// Apply dashboard filters passed in via env vars. The Go runner sets these
	// when the user clicks a per-row or per-group re-run button.
	filterService := strings.TrimSpace(os.Getenv("OVERCAST_COMPAT_SERVICE"))
	filterGroups := splitCSV(os.Getenv("OVERCAST_COMPAT_GROUPS"))
	filterTests := splitCSV(os.Getenv("OVERCAST_COMPAT_TESTS"))
	filterPairs := splitCSV(os.Getenv("OVERCAST_COMPAT_TEST_PAIRS"))

	// TEST_PAIRS overrides the scalar filters — it's used by "re-run
	// non-passing" which expands a status filter into concrete group:test keys.
	if len(filterPairs) > 0 {
		filtered := testGroups[:0]
		for _, g := range testGroups {
			tests := g.Tests[:0:0]
			for _, tc := range g.Tests {
				if filterPairs[g.Name+":"+tc.Name] {
					tests = append(tests, tc)
				}
			}
			if len(tests) > 0 {
				g.Tests = tests
				filtered = append(filtered, g)
			}
		}
		testGroups = filtered
	} else {
		filtered := testGroups[:0]
		for _, g := range testGroups {
			if filterService != "" && g.Service != filterService {
				continue
			}
			if filterGroups != nil && !filterGroups[g.Name] {
				continue
			}
			if filterTests != nil {
				tests := g.Tests[:0:0]
				for _, tc := range g.Tests {
					if filterTests[tc.Name] {
						tests = append(tests, tc)
					}
				}
				if len(tests) == 0 {
					continue
				}
				g.Tests = tests
			}
			filtered = append(filtered, g)
		}
		testGroups = filtered
	}

	// Interactive mode: don't auto-run, wait for commands on stdin.
	if os.Getenv("OVERCAST_COMPAT_INTERACTIVE") == "1" {
		harness.EmitBuilding(suite, "Loading registry and building test groups...")

		totalTests := 0
		for _, g := range testGroups {
			totalTests += len(g.Tests)
		}
		harness.EmitReady(suite, totalTests)

		groupMap := make(map[string]harness.TestGroup)
		for _, g := range testGroups {
			groupMap[g.Name] = g
		}

		cmds := harness.ReadCommands()

		var cancelMu sync.Mutex
		cancels := make(map[string]context.CancelFunc)
		// runningTest tracks the currently executing test for ping/pong.

		// Handle SIGINT/SIGTERM: cancel all in-flight tests and exit.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Fprintf(os.Stderr, "[cli] received signal — shutting down\n")
			cancelMu.Lock()
			funcs := make([]context.CancelFunc, 0, len(cancels))
			for _, cancel := range cancels {
				funcs = append(funcs, cancel)
			}
			cancelMu.Unlock()
			for _, cancel := range funcs {
				cancel()
			}
			os.Exit(0)
		}()
		var runningTestMu sync.Mutex
		runningTest := ""

		for cmd := range cmds {
			switch cmd.Command {
			case "run":
				var groupsToRun []harness.TestGroup
				// Empty tests means "run all groups".
				if len(cmd.Tests) == 0 {
					for _, g := range testGroups {
						groupsToRun = append(groupsToRun, g)
					}
				}
				for _, ref := range cmd.Tests {
					g, ok := groupMap[ref.Group]
					if !ok {
						fmt.Fprintf(os.Stderr, "[cli] unknown group: %s\n", ref.Group)
						continue
					}
					if len(ref.Tests) > 0 {
						requested := make(map[string]bool)
						for _, t := range ref.Tests {
							requested[t] = true
						}
						var filtered []harness.TestCase
						for _, tc := range g.Tests {
							if requested[tc.Name] {
								filtered = append(filtered, tc)
							}
						}
						g.Tests = filtered
					}
					groupsToRun = append(groupsToRun, g)
				}

				batchID := cmd.BatchID
				batchStart := time.Now()

				slots := 8
				if v := os.Getenv("OVERCAST_COMPAT_PARALLEL_SLOTS"); v != "" {
					if n, _ := strconv.Atoi(v); n > 0 {
						slots = n
					}
				}
				sem := make(chan struct{}, slots)

				// Dispatch test execution to a background goroutine so the
				// main goroutine keeps reading stdin commands.
				go func() {
					results := make([]harness.GroupCounts, len(groupsToRun))
					var wg sync.WaitGroup
					for i, g := range groupsToRun {
						wg.Add(1)
						go func(i int, g harness.TestGroup) {
							defer wg.Done()
							sem <- struct{}{}
							defer func() { <-sem }()

							baseCtx := harness.NewRunContext(context.Background(), endpoint, region, runID)
							groupCtx, groupCancel := context.WithTimeout(baseCtx, 5*time.Minute)
							defer groupCancel()

							cancelMu.Lock()
							for _, tc := range g.Tests {
								cancels[g.Name+":"+tc.Name] = groupCancel
							}
							cancelMu.Unlock()
							runningTestMu.Lock()
							for _, tc := range g.Tests {
								if tc.Skip == "" && runningTest == "" {
									runningTest = g.Name + ":" + tc.Name
								}
							}
							runningTestMu.Unlock()

							results[i] = harness.RunGroup(groupCtx, g)

							cancelMu.Lock()
							for _, tc := range g.Tests {
								delete(cancels, g.Name+":"+tc.Name)
							}
							cancelMu.Unlock()
						}(i, g)
					}
					wg.Wait()

					runningTestMu.Lock()
					runningTest = ""
					runningTestMu.Unlock()

					var total harness.GroupCounts
					for _, r := range results {
						total.Passed += r.Passed
						total.Failed += r.Failed
						total.Skipped += r.Skipped
						total.Unimplemented += r.Unimplemented
						total.Cancelled += r.Cancelled
					}

					harness.EmitBatchComplete(suite, batchID, total, time.Since(batchStart).Milliseconds())
				}()

			case "cancel":
				cancelMu.Lock()
				if cmd.Group != "" && cmd.Test != "" {
					if cancel, ok := cancels[cmd.Group+":"+cmd.Test]; ok {
						cancel()
					}
				} else {
					for _, cancel := range cancels {
						cancel()
					}
				}
				cancelMu.Unlock()

			case "ping":
				runningTestMu.Lock()
				rt := runningTest
				runningTestMu.Unlock()
				harness.EmitPong(suite, rt)

			case "shutdown":
				cancelMu.Lock()
				for _, cancel := range cancels {
					cancel()
				}
				cancelMu.Unlock()
				os.Exit(0)
			}
		}
		os.Exit(0)
	}

	harness.RunSuite(suite, testGroups, endpoint, region, runID)
}
