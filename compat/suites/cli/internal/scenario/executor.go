package scenario

import (
	"context"
	"encoding/json"
	"encoding/xml"
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

// run uses the context-aware variant so an `aws` that never answers dies with
// the group's five-minute timeout or with a dashboard cancellation, instead of
// holding a parallel slot open until the whole suite is killed. A generated
// group makes far more calls than a hand-written one, so it is likelier to be
// the group sitting on a hung process.
//
// It is the *signed* variant because a generated call has to carry its own
// service's credential scope: unsigned, an operation Overcast has not
// implemented falls through the REST fallback to S3 and answers 405 rather than
// 501, which the harness reports as a failure of the service under test. See
// awscli.RunOutputContextSigned.
func (awscliRunner) run(ctx context.Context, t *harness.TestContext, args []string) (map[string]any, error) {
	return awscli.RunOutputContextSigned(ctx, t.Endpoint, t.Region, args...)
}

// bagKey is where a group's context bag lives on the harness TestContext. The
// harness creates one TestContext per group run and hands the same one to
// setup, every test and teardown, so the bag has exactly the lifetime the IR
// gives a group's context.
const bagKey = "scenario_context"

// bagFor returns the group's context bag, creating it on first use. The
// create-if-absent is atomic because a parallel group's tests share one
// TestContext and reach this concurrently — see harness.TestContext.LoadOrStore.
func bagFor(t *harness.TestContext) *contextBag {
	v := t.LoadOrStore(bagKey, func() any { return newContextBag() })
	if c, ok := v.(*contextBag); ok {
		return c
	}
	// Something else claimed the key. Hand back a private bag rather than
	// panicking: a probe group never reads it, and a lifecycle group would
	// fail loudly on the first unresolvable $ref.
	return newContextBag()
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
// A setup failure is reported to the harness as an error. harness.RunGroup then
// reports every test in the group as skip with "setup failed: <message>" and
// still runs the group's teardown, which is the IR's rule
// (compat/model/README.md § The scenario file) and matters most here: a setup
// that failed on its third step has already created what its first two made,
// and no test will run to delete it.
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

	params, evalErr := e.eval.evalParams(c.Params)
	if evalErr != nil {
		// Nothing was sent, so field 3 shows the params as the scenario file
		// writes them rather than a JSON document that never existed. It is
		// rendered inside the error branches that use it: every call the run
		// makes takes the successful path, and encoding the unevaluated params
		// there would be work no message ever prints.
		obs.params = rawParams(c)
		var ref *refError
		if errors.As(evalErr, &ref) {
			return obs, nil, e.fail(obs, step, "params", ref.path, "the context path to be set", "<unset>")
		}
		return obs, nil, e.fail(obs, step, "params", "", "every value expression to evaluate", quote(evalErr.Error()))
	}

	sent, encErr := canonicalJSON(params)
	if encErr != nil {
		obs.params = rawParams(c)
		return obs, nil, e.fail(obs, step, "params", "", "params that encode as JSON", quote(encErr.Error()))
	}
	obs.params = sent

	body, runErr := e.b.run.run(ctx, e.tc, []string{awsCommand(e.file.Client.EndpointPrefix), kebabOp(c.Op), "--cli-input-json", sent})
	if runErr != nil {
		return obs, runErr, nil
	}
	obs.body, obs.ok = body, true
	return obs, nil, nil
}

// rawParams renders a call's params as the scenario file writes them — value
// expressions unevaluated — for a failure raised before anything was sent.
func rawParams(c *Call) string {
	s, err := canonicalJSON(c.Params)
	if err != nil {
		return fmt.Sprintf("%v", c.Params)
	}
	return s
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

// failedCall reports a call that should have succeeded. The CLI's error text is
// quoted verbatim as the actual value, so the reader sees what the CLI said.
//
// Classification is decided here rather than left to the message: this is the
// one place holding the *raw* CLI error, which states the error code the
// classification reads. A composed failure message is not something
// harness.IsUnimplemented may be pointed at — it embeds the params JSON, where
// a run id or a port number puts a "501" that means nothing. So a 501 is stated
// by wrapping harness.ErrUnimplemented, and every other failure carries no
// sentinel and is a plain fail.
func (e *execution) failedCall(obs observed, step string, cliErr error) error {
	f := e.fail(obs, step, "call", "", "the call to succeed", quote(cliErr.Error()))
	if harness.IsUnimplemented(cliErr) {
		return unimplementedFailure{f}
	}
	return f
}

// unimplementedFailure is a failure the emulator answered with 501. It reads as
// the failure it wraps and unwraps to both it and the sentinel, so the message
// in the NDJSON `error` field is unchanged and harness.IsUnimplemented still
// classifies the test as unimplemented.
type unimplementedFailure struct{ err error }

func (u unimplementedFailure) Error() string { return u.err.Error() }

func (u unimplementedFailure) Unwrap() []error { return []error{u.err, harness.ErrUnimplemented} }

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

// The rest of this file is general call execution; only matchesError,
// errorCodes and errorCodeSpellings below have a counterpart, which is
// compat/suites/go-sdk/internal/scenario/errors.go — that suite's error
// matching lives in its own file because it reads a typed error chain rather
// than parsing stderr. The two apply the same equality rule over their own
// backend's error surfaces and are not byte-identical, but a change to the
// rule here usually needs a matching change there — change both or neither.

// matchesError reports whether a failed call carries the error an `error`
// clause names.
//
// The clause carries both the modeled shape and the wire code, because SDKs
// disagree about which of the two they surface — for SQS's not-found,
// QueueDoesNotExist and AWS.SimpleQueueService.NonExistentQueue — so either is
// accepted, but by **equality** against a code parsed out of the CLI's output,
// never by containment over the whole message. Containment cannot tell a code
// from a code that ends with it: an `absent` clause naming NotFoundException
// would be satisfied by a ResourceNotFoundException, which is a different error
// from a different branch of the service, and by a NotFoundException named
// anywhere else in the CLI's prose.
//
// errorCodes below reads the two places a code can appear. When it finds none —
// the CLI died before it reached the wire, or printed something this parser
// does not know — the clause does **not** match. There is no containment
// fallback, and the absence of one is the rule rather than an omission: a
// message with no code surface in it is no evidence that the service raised the
// named error, and matching it by containment would reinstate the near miss
// this equality exists to exclude, on exactly the inputs where nothing has
// checked the string's shape. `aws sqs delete-queue: … Could not connect to the
// endpoint URL: "…/000000000000/QueueDoesNotExist-probe"` contains the code and
// states none. The clause then fails naming the raw stderr, which is what the
// reader needs: the CLI never got far enough to state a code.
func matchesError(err error, want *ErrorClause) bool {
	if err == nil || want == nil {
		return false
	}
	for _, got := range errorCodes(err.Error()) {
		if (want.Shape != "" && got == want.Shape) || (want.Code != "" && got == want.Code) {
			return true
		}
	}
	return false
}

// errorCodes returns every error code the CLI's output states, in the order
// they were found, or nil when it states none.
//
// Two surfaces, because the CLI has two. Its own rendering of a modeled error
// is the banner `An error occurred (<Code>) when calling the <Op> operation:`.
// When it cannot model the response it prints the body instead, and that body
// is **parsed** rather than pattern-matched: `echoedBodyCodes` finds where it
// starts, decodes it as JSON or as XML, and reads the code out of the
// positions compat/model/README.md § Errors names — `__type`, `Code` or `code`
// at the top level, and the `Code` inside an error node one level down, which
// is the only place an AWS Query or REST XML service ever states it.
//
// It used to be one unanchored regex over the whole of stderr,
// `"(?:__type|Code|code)"\s*:\s*"([^"]+)"`, and that read a nested code
// correctly only by accident: it matched a `Code` member at any depth of
// anything JSON-shaped, including one inside an unrelated object the CLI
// happened to print, and it could not see an XML body at all. Parsing says
// which position the code came from, which is what the shared fixtures assert
// (#1896).
//
// Response headers are not a surface here: the CLI hands this suite a
// process's stderr, so `x-amzn-query-error` only ever reaches it already
// resolved into a banner.
func errorCodes(msg string) []string {
	var codes []string
	for _, code := range harness.BannerCodes(msg) {
		codes = append(codes, errorCodeSpellings(code)...)
	}
	for _, code := range echoedBodyCodes(msg) {
		codes = append(codes, errorCodeSpellings(code)...)
	}
	return codes
}

// echoedBodyCodes returns every code an error body the CLI echoed states, or
// nothing when stderr carries no body this parser can decode.
//
// The body is embedded in a line the CLI wrote around it, so its start is
// found rather than assumed: the first `{` that decodes as a JSON object, or
// the first `<` that decodes as one of the three XML error envelopes AWS uses.
// A candidate that does not decode is not a body, and a decode that succeeds
// on something that is not an error envelope states no code — neither is a
// reason to fall back to matching text.
func echoedBodyCodes(msg string) []string {
	if codes := jsonBodyCodes(msg); len(codes) > 0 {
		return codes
	}
	return xmlBodyCodes(msg)
}

// jsonBodyCodes reads a JSON error body: the three top-level spellings, and
// the same two inside a nested `Error` object.
func jsonBodyCodes(msg string) []string {
	for offset := strings.Index(msg, "{"); offset >= 0; {
		var body map[string]any
		if err := json.NewDecoder(strings.NewReader(msg[offset:])).Decode(&body); err == nil {
			return jsonErrorCodes(body)
		}
		next := strings.Index(msg[offset+1:], "{")
		if next < 0 {
			return nil
		}
		offset += next + 1
	}
	return nil
}

// jsonErrorCodes reads the code positions of one decoded JSON body.
func jsonErrorCodes(body map[string]any) []string {
	var codes []string
	add := func(from map[string]any, keys ...string) {
		for _, key := range keys {
			if value, ok := from[key].(string); ok && value != "" {
				codes = append(codes, value)
			}
		}
	}
	add(body, "__type", "Code", "code")
	if nested, ok := body["Error"].(map[string]any); ok {
		add(nested, "Code", "code")
	}
	return codes
}

// xmlBodyCodes reads an XML error body — the AWS Query `<ErrorResponse>`
// envelope, EC2's `<Response><Errors><Error>` dialect, and REST XML's bare
// `<Error>` root. Only those three roots are read: an emulator or a proxy that
// answered HTML would otherwise have any `<Code>` element in it treated as an
// error code.
func xmlBodyCodes(msg string) []string {
	for offset := strings.Index(msg, "<"); offset >= 0; {
		var body xmlErrorBody
		if err := xml.Unmarshal([]byte(msg[offset:]), &body); err == nil {
			switch body.XMLName.Local {
			case "Error", "ErrorResponse", "Response":
				var codes []string
				for _, code := range []string{body.Code, body.NestedCode, body.EC2Code} {
					if code != "" {
						codes = append(codes, code)
					}
				}
				return codes
			}
		}
		next := strings.Index(msg[offset+1:], "<")
		if next < 0 {
			return nil
		}
		offset += next + 1
	}
	return nil
}

// xmlErrorBody is the three XML error envelopes at once. Each `Code` has its
// own path, so one decode answers whichever envelope the body turned out to
// be and leaves the other two empty.
type xmlErrorBody struct {
	XMLName xml.Name
	// REST XML's bare <Error><Code>, the shape S3 answers with.
	Code string `xml:"Code"`
	// The AWS Query protocol's <ErrorResponse><Error><Code>, which IAM, SNS
	// and every other Query service answers with.
	NestedCode string `xml:"Error>Code"`
	// EC2's own dialect, <Response><Errors><Error><Code>.
	EC2Code string `xml:"Errors>Error>Code"`
}

// errorCodeSpellings returns one observed code in every spelling a clause may
// name it by, which is the list compat/model/README.md § Errors fixes: the
// value itself, what follows the last "#" of a Smithy id
// (`com.amazonaws.sqs#QueueDoesNotExist` states the same code as
// `QueueDoesNotExist`), and what precedes the first ";" of the
// `<code>;<fault>` form the x-amzn-query-error header uses — which reaches the
// CLI only if it ever fails to resolve the header itself.
//
// Splitting at those separators and nowhere else is what keeps the match an
// equality: no spelling of `ResourceNotFoundException` is `NotFoundException`.
func errorCodeSpellings(code string) []string {
	out := []string{code}
	if i := strings.LastIndex(code, "#"); i >= 0 {
		out = append(out, code[i+1:])
	}
	if i := strings.Index(code, ";"); i >= 0 {
		out = append(out, code[:i])
	}
	return out
}

// acceptedCodes renders both halves of an error clause for a failure message.
func acceptedCodes(want *ErrorClause) string {
	if want.Shape == want.Code {
		return fmt.Sprintf("error %q", want.Shape)
	}
	return fmt.Sprintf("error %q or %q", want.Shape, want.Code)
}

// awsCommandOverrides is the whole of compat/model/README.md § Naming's cli
// row: the `aws` command is the scenario's endpoint prefix except for four
// services, where the CLI keeps a shorter historical name. It is a table
// because nothing about "elasticloadbalancing" implies "elb" — the CLI's own
// command names come from botocore's service directory, not from the endpoint,
// and for these four the two disagree.
//
// The plan (§7.3) asked for this to land with the first scenario that names one
// of the four rather than up front, which is `elastic-load-balancing`; the other
// three are here because they are one documented set and a table with one entry
// invites the next author to add theirs somewhere else.
var awsCommandOverrides = map[string]string{
	"elasticloadbalancing": "elb",
	"monitoring":           "cloudwatch",
	"email":                "ses",
	"states":               "stepfunctions",
}

// awsCommand is the `aws` subcommand for a scenario's service.
func awsCommand(endpointPrefix string) string {
	if name, ok := awsCommandOverrides[endpointPrefix]; ok {
		return name
	}
	return endpointPrefix
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
