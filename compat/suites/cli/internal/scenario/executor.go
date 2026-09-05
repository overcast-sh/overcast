package scenario

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
	"github.com/overcast-sh/overcast-compat-cli/internal/registry"
)

// runner is the one seam between the interpreter and the `aws` binary. The
// production implementation is a thin adapter over internal/awscli, which is
// the only place in this suite that spawns a process — endpoint, region,
// output format and credential handling all come from there, unchanged, so a
// generated call is shaped exactly like a hand-written one. The unit tests
// substitute an in-memory fake and never touch the network.
type runner interface {
	run(ctx context.Context, t *harness.TestContext, args []string) (map[string]any, error)
}

type awscliRunner struct{}

func (awscliRunner) run(_ context.Context, t *harness.TestContext, args []string) (map[string]any, error) {
	return awscli.RunOutput(t.Endpoint, t.Region, args...)
}

// bagKey is where a group's context bag lives on the harness TestContext. The
// harness creates one TestContext per group run and hands the same one to
// setup, every test and teardown, so the bag has exactly the lifetime the IR
// gives a group's context.
const bagKey = "scenario_context"

func bagFor(t *harness.TestContext) *contextBag {
	if v, ok := t.Get(bagKey); ok {
		if c, ok := v.(*contextBag); ok {
			return c
		}
	}
	c := newContextBag()
	t.Set(bagKey, c)
	return c
}

// Resolve implements registry.ScenarioBackend: it claims a test the scenario
// file its group names declares.
//
// "Not mine" (ok false) covers a group with no scenario and a group or test the
// file does not declare — the loader then applies its own rule, which for a
// generated group is a loud failure naming the missing backend. A scenario file
// that cannot be read is different: the group *is* ours, so the test fails with
// the load error rather than with a message blaming a backend that exists.
func (b *Backend) Resolve(rg registry.RegistryGroup, rt registry.RegistryTest) (harness.TestFn, bool) {
	if rg.Scenario == "" {
		return nil, false
	}
	f, err := b.load(rg.Scenario)
	if err != nil {
		return func(context.Context, *harness.TestContext) error { return err }, true
	}
	g, ok := f.group(rg.Name)
	if !ok {
		return nil, false
	}
	tc, ok := g.test(rt.Name)
	if !ok {
		return nil, false
	}
	return func(ctx context.Context, t *harness.TestContext) error {
		return b.newExecution(t, f, g, tc.Name).runTest(ctx, tc)
	}, true
}

// Setup returns the group's setup function, or false when the group has no
// setup steps — a probe group carries neither setup nor teardown, and must not
// be given one.
//
// A setup failure is reported to the harness as an error, which reports every
// test in the group as skip with "setup failed: <message>" and still runs
// teardown. That is the IR's rule, already implemented by harness.RunGroup.
func (b *Backend) Setup(rg registry.RegistryGroup) (func(context.Context, *harness.TestContext) error, bool) {
	f, g, claimed, err := b.groupOf(rg)
	if !claimed {
		return nil, false
	}
	if err != nil {
		return func(context.Context, *harness.TestContext) error { return err }, true
	}
	if len(g.Setup) == 0 {
		return nil, false
	}
	return func(ctx context.Context, t *harness.TestContext) error {
		e := b.newExecution(t, f, g, "setup")
		for i := range g.Setup {
			if _, err := e.invoke(ctx, &g.Setup[i], fmt.Sprintf("setup[%d]", i)); err != nil {
				return err
			}
		}
		return nil
	}, true
}

// Teardown returns the group's teardown function, or false when it has none.
//
// Every step is wrapped individually: an error, or an unresolvable $ref, skips
// that step and the rest still run. Each skip is logged to stderr — which the
// Go runner multiplexes into its own log — and none of them fails the group,
// which is the suite's existing teardown convention (internal/groups ignores
// teardown errors outright) and compat/AGENTS.md's "teardown must not throw".
//
// Returning an error instead would emit group_teardown_error on every clean
// run of a lifecycle group: the delete test has already removed the resource
// the teardown step names, so a "not found" there is the expected outcome, not
// a leak. Proof that nothing leaked is the orphan sweep — a {runId} search
// after the run — not the teardown's own exit status.
func (b *Backend) Teardown(rg registry.RegistryGroup) (func(context.Context, *harness.TestContext) error, bool) {
	f, g, claimed, err := b.groupOf(rg)
	if !claimed {
		return nil, false
	}
	if err != nil {
		return func(context.Context, *harness.TestContext) error { return err }, true
	}
	if len(g.Teardown) == 0 {
		return nil, false
	}
	return func(ctx context.Context, t *harness.TestContext) error {
		e := b.newExecution(t, f, g, "teardown")
		for i := range g.Teardown {
			step := fmt.Sprintf("teardown[%d]", i)
			if _, err := e.invoke(ctx, &g.Teardown[i], step); err != nil {
				t.Log(fmt.Sprintf("%s: skipped %s: %v", g.Name, step, err))
			}
		}
		return nil
	}, true
}

// groupOf resolves a registry group to its scenario group. claimed is false for
// a group this backend does not own; err is non-nil when it owns the group but
// could not read the file.
func (b *Backend) groupOf(rg registry.RegistryGroup) (f *File, g *Group, claimed bool, err error) {
	if rg.Scenario == "" {
		return nil, nil, false, nil
	}
	f, err = b.load(rg.Scenario)
	if err != nil {
		return nil, nil, true, err
	}
	g, ok := f.group(rg.Name)
	if !ok {
		return nil, nil, false, nil
	}
	return f, g, true, nil
}

// execution is one group-scoped run of one test, setup or teardown.
type execution struct {
	b     *Backend
	tc    *harness.TestContext
	file  *File
	group *Group
	eval  *evaluator
	// test is failure-message field 1's second half: the test name, or
	// "setup"/"teardown" for a group hook.
	test string
}

func (b *Backend) newExecution(t *harness.TestContext, f *File, g *Group, test string) *execution {
	return &execution{
		b:     b,
		tc:    t,
		file:  f,
		group: g,
		eval:  &evaluator{runID: t.RunID, group: g.Name, bag: bagFor(t)},
		test:  test,
	}
}

// runTest runs one test: the primary call, then every clause in order.
func (e *execution) runTest(ctx context.Context, t *Test) error {
	wantErr := errorCodeClause(t.Assert)

	obs, cliErr, err := e.callRaw(ctx, &t.Call, "call")
	if err != nil {
		return err
	}
	switch {
	case wantErr != nil:
		// A test carrying an errorCode clause expects its primary call to
		// fail; the generator refuses such a test any clause that would read
		// the primary response, so every other clause makes a call of its own.
		if cliErr == nil {
			return e.fail(obs, "call", kindErrorCode, "", acceptedCodes(wantErr), "<no error>")
		}
		if !matchesError(cliErr, wantErr) {
			return e.fail(obs, "call", kindErrorCode, "", acceptedCodes(wantErr), quote(cliErr.Error()))
		}
	case cliErr != nil:
		return e.failedCall(obs, "call", cliErr)
	default:
		if err := e.applyExports(&t.Call, obs, "call"); err != nil {
			return err
		}
	}

	for i := range t.Assert {
		a := &t.Assert[i]
		if a.Kind == kindErrorCode {
			continue // already checked against the primary call
		}
		if err := e.assert(ctx, a, obs, fmt.Sprintf("assert[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

// callRaw evaluates a call's params and runs it through the AWS CLI, keeping
// the CLI's own error separate from the interpreter's.
//
// cliErr is the `aws` invocation's error, unwrapped, for the two clauses that
// must inspect it (errorCode, and absent's error form). err is everything the
// interpreter can attribute to the scenario before anything was sent — an
// unresolvable $ref, params that will not encode — and is already a *failure.
//
// The returned observed carries the exact params JSON sent, so every failure
// downstream of it quotes what went on the wire.
func (e *execution) callRaw(ctx context.Context, c *Call, step string) (obs observed, cliErr, err error) {
	obs = observed{op: c.Op}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return obs, nil, fmt.Errorf("%s/%s: %s: %w", e.group.Name, e.test, c.Op, ctxErr)
	}

	rawParams, rawErr := canonicalJSON(c.Params)
	if rawErr != nil {
		rawParams = fmt.Sprintf("%v", c.Params)
	}

	params, evalErr := e.eval.evalParams(c.Params)
	if evalErr != nil {
		// Nothing was sent, so field 3 shows the params as the scenario file
		// writes them rather than a JSON document that never existed.
		obs.params = rawParams
		var ref *refError
		if errors.As(evalErr, &ref) {
			return obs, nil, e.fail(obs, step, "params", ref.path, "the context path to be set", "<unset>")
		}
		return obs, nil, e.fail(obs, step, "params", "", "every value expression to evaluate", quote(evalErr.Error()))
	}

	sent, encErr := canonicalJSON(params)
	if encErr != nil {
		obs.params = rawParams
		return obs, nil, e.fail(obs, step, "params", "", "params that encode as JSON", quote(encErr.Error()))
	}
	obs.params = sent

	body, runErr := e.b.run.run(ctx, e.tc, []string{e.file.Client.EndpointPrefix, kebabOp(c.Op), "--cli-input-json", sent})
	if runErr != nil {
		return obs, runErr, nil
	}
	obs.body, obs.ok = body, true
	return obs, nil, nil
}

// call is callRaw with the CLI's error turned into a failure — what every
// clause that simply needs the call to succeed wants.
func (e *execution) call(ctx context.Context, c *Call, step string) (observed, error) {
	obs, cliErr, err := e.callRaw(ctx, c, step)
	if err != nil {
		return obs, err
	}
	if cliErr != nil {
		return obs, e.failedCall(obs, step, cliErr)
	}
	return obs, nil
}

// failedCall reports a call that should have succeeded. The CLI's error text
// is quoted verbatim as the actual value — it carries the error code in
// parentheses, and it is what keeps harness.IsUnimplemented able to classify a
// 501 as unimplemented rather than as a failure.
func (e *execution) failedCall(obs observed, step string, cliErr error) error {
	return e.fail(obs, step, "call", "", "the call to succeed", quote(cliErr.Error()))
}

// invoke is call plus its exports, for a setup or teardown step, whose whole
// purpose is the context values it leaves behind.
func (e *execution) invoke(ctx context.Context, c *Call, step string) (observed, error) {
	obs, err := e.call(ctx, c, step)
	if err != nil {
		return obs, err
	}
	return obs, e.applyExports(c, obs, step)
}

// applyExports writes a call's response paths into the context bag. An export
// path that does not resolve is an error for the step that carries it: the
// value a later step will $ref is not there, and failing here names the path
// instead of failing later with an unresolvable reference.
func (e *execution) applyExports(c *Call, obs observed, step string) error {
	for path, respPath := range c.Export {
		v, ok, err := resolvePath(obs.body, respPath)
		if err != nil {
			return e.fail(obs, step, "export", respPath, "a well-formed response path", quote(err.Error()))
		}
		if !ok {
			return e.fail(obs, step, "export", respPath, fmt.Sprintf("a value to export into %q", path), missingValue)
		}
		e.eval.bag.set(path, v)
	}
	return nil
}

// wait sleeps between eventually attempts for exactly the delay the IR asks
// for — no more, because the cli suite spawns a process per call and every
// added second is paid on every attempt of every retried clause.
func wait(ctx context.Context, delayMs int) error {
	if delayMs <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// matchesError reports whether a failed call carries the error an `error`
// clause names.
//
// The clause carries both the modeled shape and the wire code, because SDKs
// disagree about which of the two they surface — for SQS's not-found,
// QueueDoesNotExist and AWS.SimpleQueueService.NonExistentQueue — so either is
// accepted. The test is containment over the CLI's message, which is what every
// hand-written group in this suite already does (internal/groups/util.go): the
// AWS CLI prints the code in parentheses and nothing machine-readable beside
// it, so the message is the only place the code exists.
func matchesError(err error, want *ErrorClause) bool {
	if err == nil || want == nil {
		return false
	}
	msg := err.Error()
	return (want.Shape != "" && strings.Contains(msg, want.Shape)) ||
		(want.Code != "" && strings.Contains(msg, want.Code))
}

// acceptedCodes renders both halves of an error clause for a failure message.
func acceptedCodes(want *ErrorClause) string {
	if want.Shape == want.Code {
		return fmt.Sprintf("error %q", want.Shape)
	}
	return fmt.Sprintf("error %q or %q", want.Shape, want.Code)
}

// kebabOp derives the `aws` subcommand from an operation name.
//
// This is botocore's xform_name with a "-" separator, which is exactly how the
// AWS CLI names its own subcommands (awscli/clidriver.py builds them from
// xform_name(operation.name, '-')), so it is a derivation rather than a table:
// ListAWSServiceAccessForOrganization becomes
// list-aws-service-access-for-organization the same way the CLI does. Every
// operation in the pilot corpus is covered by TestKebabOpMatchesTheCLI.
func kebabOp(op string) string {
	if strings.Contains(op, "-") {
		return op
	}
	if m := trailingAcronymPluralRe.FindString(op); m != "" {
		op = op[:len(op)-len(m)] + "-" + strings.ToLower(m)
	}
	s := firstCapRe.ReplaceAllString(op, "${1}-${2}")
	s = numberCapRe.ReplaceAllString(s, "${1}-${2}")
	s = endCapRe.ReplaceAllString(s, "${1}-${2}")
	return strings.ToLower(s)
}

var (
	trailingAcronymPluralRe = regexp.MustCompile(`[A-Z]{2,}s$`)
	firstCapRe              = regexp.MustCompile(`(.)([A-Z][a-z]+)`)
	endCapRe                = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	numberCapRe             = regexp.MustCompile(`([a-z])([0-9]+)`)
)
