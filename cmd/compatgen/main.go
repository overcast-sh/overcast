//go:build dev

// Command compatgen turns the pruned AWS shape snapshot plus hand-curated
// recipes into the compat scenario IR, the refusal report and the generated
// registry sibling. It also reads the authored scenarios under
// compat/model/authored — the same IR, written by hand to port a hand-written
// registry group — and renders every backend's source for those too. It is a
// build-time tool whose output is committed data; nothing under compat/ imports
// it or any other emulator Go code.
//
// Usage:
//
//	go run -tags dev ./cmd/compatgen [flags]
//
// Flags:
//
//	(none)                    generate every recipe under compat/model/recipes/,
//	                          and every authored scenario under compat/model/authored/
//	-check                    prove the committed output is byte-identical, writing nothing
//	-scaffold <service>       print a recipe skeleton for a service in the shape snapshot
//	-review-report [service]  print the Markdown review report for a PR body
//	-explain <group>/<test>   render one test as pseudo-code (with -lang)
//	-lang <language>          python | node | cli | go | java | dotnet | rust
//	-sample <n>               scenarios rendered in the review report (default 3)
//	-root <dir>               repository root (default: the current directory)
//
// See compat/model/README.md for the IR and cmd/compatgen/README.md for the
// workflow. Design: docs/plans/compat-coverage-modelgen.md §3.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	compatmodel "github.com/overcast-sh/overcast/compat/model"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options is the parsed command line.
type options struct {
	root     string
	check    bool
	scaffold string
	report   bool
	service  string
	explain  string
	lang     string
	sample   int
}

func run(args []string, stdout, stderr io.Writer) int {
	var opts options
	fs := flag.NewFlagSet("compatgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.root, "root", ".", "repository root")
	fs.BoolVar(&opts.check, "check", false, "verify the committed output matches the generator's without writing")
	fs.StringVar(&opts.scaffold, "scaffold", "", "print a recipe skeleton for the named service")
	fs.BoolVar(&opts.report, "review-report", false, "print the Markdown review report (optionally for one service, given as the argument)")
	fs.StringVar(&opts.explain, "explain", "", "render one test as pseudo-code: <group>/<test>")
	fs.StringVar(&opts.lang, "lang", "python", "language for -explain: python, node, cli, go, java, dotnet, rust")
	fs.IntVar(&opts.sample, "sample", 3, "scenarios rendered in the review report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 || (fs.NArg() == 1 && !opts.report) {
		fmt.Fprintln(stderr, "compatgen: unexpected arguments", fs.Args())
		return 2
	}
	if fs.NArg() == 1 {
		opts.service = fs.Arg(0)
	}
	modes := 0
	for _, on := range []bool{opts.check, opts.scaffold != "", opts.report, opts.explain != ""} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		fmt.Fprintln(stderr, "compatgen: -check, -scaffold, -review-report and -explain are mutually exclusive")
		return 2
	}
	var err error
	switch {
	case opts.scaffold != "":
		err = runScaffold(opts, stdout)
	case opts.explain != "":
		err = runExplain(opts, stdout)
	case opts.report:
		err = runReport(opts, stdout)
	default:
		err = runGenerate(opts, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "compatgen: %v\n", err)
		return 1
	}
	return 0
}

// corpus is everything a generation run reads.
type corpus struct {
	schemas *schemaSet
	// suites holds the registry schemas, which live with the loaders under
	// compat/suites rather than with the model: the generated registry is
	// written against the loaders' contract, not this package's.
	suites  *schemaSet
	recipes []recipe
	// authored is the hand-written scenario layer: an IR file with no recipe,
	// written to port a hand-written registry group (§3.11). It is an input
	// like a recipe, and it reaches the emitters through exactly the same
	// generation the recipes produce. See authored.go.
	authored []authored
	values   *valuesTable
	// promotions is the soak ledger: the one generated-registry field that
	// comes from an input file rather than from the scenario. See
	// promotions.go.
	promotions *compatmodel.Promotions
}

func loadCorpus(root string) (*corpus, error) {
	schemas, err := loadSchemas(filepath.Join(root, filepath.FromSlash(modelDir)))
	if err != nil {
		return nil, err
	}
	suites, err := loadRegistrySchemas(filepath.Join(root, "compat", "suites"))
	if err != nil {
		return nil, err
	}
	recipes, err := loadRecipes(filepath.Join(root, filepath.FromSlash(recipesDir)), schemas)
	if err != nil {
		return nil, err
	}
	values, err := loadValues(filepath.Join(root, filepath.FromSlash(valuesPath)), schemas)
	if err != nil {
		return nil, err
	}
	promotions, err := loadPromotions(filepath.Join(root, filepath.FromSlash(promotionsPath)), schemas)
	if err != nil {
		return nil, err
	}
	authoredScenarios, err := loadAuthored(filepath.Join(root, filepath.FromSlash(authoredDir)), schemas)
	if err != nil {
		return nil, err
	}
	// compat/suites/registry.json is read for one reason and not kept: an
	// authored scenario ports a group of it, and the names — and, once the
	// port is live, the `scenario` field — have to match.
	hand, err := loadHandRegistry(filepath.Join(root, filepath.FromSlash(handRegistryPath)))
	if err != nil {
		return nil, err
	}
	for _, a := range authoredScenarios {
		if err := checkAuthoredAgainstRegistry(a, hand); err != nil {
			return nil, err
		}
	}
	if err := checkPortedGroupsHaveAuthoredScenarios(hand, authoredScenarios); err != nil {
		return nil, err
	}
	return &corpus{
		schemas:    schemas,
		suites:     suites,
		recipes:    recipes,
		authored:   authoredScenarios,
		values:     values,
		promotions: promotions,
	}, nil
}

// generateAll runs the generator over every recipe and renders every output.
func generateAll(root string, c *corpus) ([]*generation, outputSet, error) {
	var generations []*generation
	var scenarios []*scenario
	gaps := gapsDocument{Version: gapsVersion, Gaps: []gap{}}
	outputs := make(outputSet)
	// The typed backends compile source rather than interpreting the IR, so
	// their files are outputs of this run too. unable collects the groups an
	// emitter refused, which decides each group's `suites` below.
	emitted := make(map[string][]string, len(sourceBackends))
	unable := unableSuites{}
	// The Go emitter spells each member as the vendored SDK declares it, so it
	// reads that SDK's own types out of the go-sdk suite's module — the one the
	// emitted source is compiled in. The loader caches per service, so a run
	// pays for each service's packages once however many calls it emits.
	goTypes := newGoSDKTypes(filepath.Join(root, filepath.FromSlash(goSDKModuleDir)))
	// Reading every model first, rather than one per iteration, is what lets
	// the SDK packages be loaded in a single `go list`: that command's cost is
	// dominated by reading the suite module's dependency graph, which is paid
	// per invocation and not per package.
	type work struct {
		recipe recipe
		// authored is set instead of recipe for an authored scenario.
		authored *authored
		model    *serviceModel
		client   clientInfo
	}
	planned := make([]work, 0, len(c.recipes)+len(c.authored))
	var sdkIDs []string
	modelFor := func(modelService, service string) (*serviceModel, clientInfo, error) {
		model, err := loadModel(filepath.Join(root, filepath.FromSlash(shapesDir)), modelService)
		if err != nil {
			return nil, clientInfo{}, err
		}
		client, err := clientInfoFor(model, service)
		if err != nil {
			return nil, clientInfo{}, err
		}
		return model, client, nil
	}
	for _, r := range c.recipes {
		model, client, err := modelFor(r.modelService(), r.Service)
		if err != nil {
			return nil, nil, err
		}
		planned = append(planned, work{recipe: r, model: model, client: client})
		sdkIDs = append(sdkIDs, client.SDKID)
	}
	// Authored scenarios are planned in the same list and primed in the same
	// `go list`: to every backend below, an authored generation and a
	// recipe-generated one are the same thing.
	for _, a := range c.authored {
		model, client, err := modelFor(a.scenario.Service, a.scenario.Service)
		if err != nil {
			return nil, nil, err
		}
		planned = append(planned, work{authored: &a, model: model, client: client})
		sdkIDs = append(sdkIDs, client.SDKID)
	}
	if hasBackend(goSDKSuite) {
		if err := goTypes.prime(sdkIDs); err != nil {
			return nil, nil, err
		}
	}
	for _, w := range planned {
		var (
			gen   *generation
			err   error
			label = w.recipe.Service
		)
		if w.authored != nil {
			label = w.authored.file
			gen, err = generateAuthored(*w.authored, w.model, w.client)
		} else {
			gen, err = generate(w.model, w.recipe, c.values, capabilitiesFor(w.recipe.Service), w.client)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", label, err)
		}
		generations = append(generations, gen)
		scenarios = append(scenarios, gen.scenario)
		gaps.Gaps = append(gaps.Gaps, gen.gaps...)
		// The scenario file is an output for a recipe and an input for an
		// authored scenario. Writing the authored one back would put the
		// generator in charge of a file a human owns, and `-check` would then
		// fail a hand-written comment for not being the generator's own bytes.
		if w.authored == nil {
			contents, err := encodeDocument(gen.scenario)
			if err != nil {
				return nil, nil, err
			}
			outputs[scenarioPath(w.recipe.Service)] = contents
		}
		for _, backend := range sourceBackends {
			if !hasBackend(backend.suite) {
				continue
			}
			emission, err := backend.emit(gen, goTypes)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", label, err)
			}
			outputs[emission.Path] = emission.Contents
			gaps.Gaps = append(gaps.Gaps, emission.Gaps...)
			emitted[backend.suite] = append(emitted[backend.suite], gen.unit)
			markUnable(unable, backend.suite, emission.Refused)
		}
	}
	// Each index is emitted whether or not its backend is enabled: every suite
	// calls into its own index unconditionally, so the file has to exist —
	// empty — for a checkout where scenarioBackends does not name that suite.
	for _, backend := range sourceBackends {
		index, err := backend.index(emitted[backend.suite])
		if err != nil {
			return nil, nil, err
		}
		outputs[backend.indexPath] = index
	}
	sortGaps(gaps.Gaps)
	contents, err := encodeDocument(gaps)
	if err != nil {
		return nil, nil, err
	}
	outputs[gapsPath] = contents
	if err := checkPromotionsAreKnownGroups(c.promotions, scenarios); err != nil {
		return nil, nil, err
	}
	registry := buildRegistry(generations, scenarioBackends, c.promotions, unable)
	contents, err = encodeDocument(registry)
	if err != nil {
		return nil, nil, err
	}
	outputs[registryPath] = contents
	for rel, contents := range outputs {
		if err := validateOutput(c, rel, contents); err != nil {
			return nil, nil, err
		}
	}
	return generations, outputs, nil
}

// validateOutput checks a generated file against its schema before it is
// written: the schema is the interpreters' and loaders' contract, so a
// document the generator produced but the schema rejects is a generator bug.
// Every output is covered, the registry included — a schema check that only
// ran in a test would let `go run ./cmd/compatgen` write a file CI then
// rejects.
func validateOutput(c *corpus, rel string, contents []byte) error {
	switch {
	case strings.HasPrefix(rel, scenarioDir+"/"):
		return wrapSchemaErr(rel, c.schemas.validate(schemaScenario, contents))
	case rel == gapsPath:
		return wrapSchemaErr(rel, c.schemas.validate(schemaGaps, contents))
	case rel == registryPath:
		return wrapSchemaErr(rel, c.suites.validate(schemaGeneratedRegistry, contents))
	case strings.HasPrefix(rel, goSuiteDir+"/"):
		// Emitted Go has no JSON schema; its contract is that it parses and is
		// gofmt-clean, which emitGo proves by running go/format over the bytes
		// it is about to return and failing generation if it will not parse.
		return nil
	case strings.HasPrefix(rel, javaSuiteDir+"/"),
		strings.HasPrefix(rel, dotnetSuiteDir+"/"),
		strings.HasPrefix(rel, rustSuiteDir+"/"):
		// Emitted Java, C# and Rust have no JSON schema either, and no formatter
		// the generator can run to prove they parse: cmd/compatgen is a Go
		// program, and CI's docs job carries no JDK, .NET SDK or Rust toolchain.
		// Their contract is each suite's own build — `mvn package`, `dotnet
		// publish`, `cargo build` — which compiles every emitted file, the same
		// evidence the go-sdk suite's build gives, arriving one step later.
		return nil
	}
	return fmt.Errorf("internal: no schema is checked for generated file %s", rel)
}

func wrapSchemaErr(rel string, err error) error {
	if err != nil {
		return fmt.Errorf("generated %s: %w", rel, err)
	}
	return nil
}

func runGenerate(opts options, stdout io.Writer) error {
	c, err := loadCorpus(opts.root)
	if err != nil {
		return err
	}
	generations, outputs, err := generateAll(opts.root, c)
	if err != nil {
		return err
	}
	if err := checkStaleScenarios(opts.root, outputs, opts.check); err != nil {
		return err
	}
	for _, backend := range sourceBackends {
		if err := checkStaleEmitted(opts.root, outputs, opts.check, backend.dir, backend.language, backend.emittedFile); err != nil {
			return err
		}
	}
	if opts.check {
		if err := outputs.check(opts.root); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "compat model is up to date (%d unit(s), %d file(s))\n", len(generations), len(outputs))
		return nil
	}
	if err := outputs.write(opts.root); err != nil {
		return err
	}
	for _, gen := range generations {
		fmt.Fprintf(stdout, "%s: %s\n", gen.describe(), gen.summaryLine())
	}
	return nil
}

// checkStaleScenarios catches a scenario file whose recipe was deleted: it
// would otherwise linger, and a loader would keep reading it.
func checkStaleScenarios(root string, outputs outputSet, check bool) error {
	dir := filepath.Join(root, filepath.FromSlash(scenarioDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var stale []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		rel := scenarioDir + "/" + entry.Name()
		if _, produced := outputs[rel]; produced {
			continue
		}
		if check {
			stale = append(stale, rel)
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return fmt.Errorf("scenario file(s) with no recipe: %s; run `make generate-compat-model` to remove them", strings.Join(stale, ", "))
	}
	return nil
}

// A stale emitted file — one whose recipe was deleted, or one left behind by a
// checkout where its suite was a scenario backend and this one where it is not
// — is removed here, or reported under -check. Every suite compiles the whole
// emitted directory, so a stale file is a build failure rather than dead
// weight. Which files belong to which backend is sourceBackends' to say.
//
// checkStaleEmitted removes — or, under -check, reports — a generated source
// file in dir that this run did not produce.
func checkStaleEmitted(root string, outputs outputSet, check bool, dir, language string, emitted func(name string) bool) error {
	path := filepath.Join(root, filepath.FromSlash(dir))
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var stale []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !emitted(name) {
			continue
		}
		rel := dir + "/" + name
		if _, produced := outputs[rel]; produced {
			continue
		}
		if check {
			stale = append(stale, rel)
			continue
		}
		if err := os.Remove(filepath.Join(path, name)); err != nil {
			return err
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return fmt.Errorf("emitted %s file(s) with no scenario: %s; run `make generate-compat-model` to remove them", language, strings.Join(stale, ", "))
	}
	return nil
}

// markUnable records that one backend refused a set of groups, so buildRegistry
// scopes each of them away from that suite rather than listing a backend
// against a group it will not compile.
func markUnable(unable unableSuites, suite string, refused map[string]bool) {
	for name := range refused {
		if unable[name] == nil {
			unable[name] = map[string]bool{}
		}
		unable[name][suite] = true
	}
}

// summaryLine is the one-line generation summary printed per unit.
func (gen *generation) summaryLine() string {
	tests := 0
	for _, g := range gen.scenario.Groups {
		tests += len(g.Tests)
	}
	// Operation coverage and refusals are a recipe's measurements: how much of
	// the service the generator managed to reach, and what it declined. An
	// authored scenario reaches exactly what its author wrote, so quoting
	// "0 of 23 operation(s) covered" for one would report a hand-written file's
	// deliberate scope as a shortfall.
	if gen.isAuthored() {
		return fmt.Sprintf("%d group(s), %d test(s)", len(gen.scenario.Groups), tests)
	}
	return fmt.Sprintf("%d group(s), %d test(s), %d of %d operation(s) covered, %d refusal(s)",
		len(gen.scenario.Groups), tests, len(gen.covered), len(gen.model.Operations()), len(gen.gaps))
}
