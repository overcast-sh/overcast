package io.overcast.compat.scenario;

import io.overcast.compat.harness.TestContext;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;
import java.util.regex.PatternSyntaxException;

/**
 * Running a generated group ({@code compat/model/README.md} § The scenario file).
 *
 * <p>A group is setup → tests → teardown. Setup runs every step in order and a
 * failure reports every test in the group as {@code skip} with
 * {@code "setup failed: <the six fields>"} — which the harness runner does for
 * us, from the exception thrown here. Teardown runs afterwards <b>even when
 * setup failed</b>, with every step wrapped individually: a setup that failed on
 * its third step has already created what its first two made, and no test will
 * run to remove it.
 *
 * <p>The emitted file declares one instance per group and hangs its setup, tests
 * and teardown off it, so the group name and the scenario file —
 * failure-message fields 1 and 6 — are written once.
 */
public final class Group {

    /** Where the group's context bag lives on the harness {@code TestContext}. */
    private static final String BAG_KEY = "scenario_context";

    private final String name;
    private final String file;

    /**
     * @param name the registry group name ({@code sqs-gen-queue})
     * @param file the scenario file this group was generated from, repository
     *             relative: failure-message field 6's first half
     */
    public Group(String name, String file) {
        this.name = name;
        this.file = file;
    }

    // ── The three phases ─────────────────────────────────────────────────────

    /**
     * Runs one generated test: the primary call, then every clause in order.
     */
    public void runTest(TestContext t, String test, Call call, List<Clause> assertions) {
        Execution e = new Execution(t, test);
        ErrorSpec wantErr = errorCodeClause(assertions);

        Attempt attempt = e.callRaw(call, "call");
        if (wantErr != null) {
            // A test carrying an errorCode clause expects its primary call to
            // fail; the generator refuses such a test any clause that would read
            // the primary response, so every other clause makes a call of its own.
            if (attempt.error() == null) {
                throw e.fail(attempt.observed(), "call", Clause.Kind.ERROR_CODE.label(), "",
                        wantErr.accepted(), "<no error>");
            }
            if (!Errors.matches(attempt.error(), wantErr)) {
                throw e.fail(attempt.observed(), "call", Clause.Kind.ERROR_CODE.label(), "",
                        wantErr.accepted(), Failure.quote(String.valueOf(attempt.error().getMessage())));
            }
        } else if (attempt.error() != null) {
            throw e.failedCall(attempt.observed(), "call", attempt.error());
        } else {
            e.applyExports(call, attempt.observed(), "call");
        }

        for (int i = 0; i < assertions.size(); i++) {
            Clause a = assertions.get(i);
            if (a.kind() == Clause.Kind.ERROR_CODE) {
                continue; // already checked against the primary call
            }
            e.assertClause(a, attempt.observed(), "assert[" + i + "]");
        }
    }

    /**
     * Runs a group's setup steps in order, stopping at the first failure.
     *
     * <p>The failure is thrown to the harness, which reports every test in the
     * group as {@code skip} with {@code "setup failed: <message>"} and still runs
     * teardown. An empty list is a no-op, not a missing phase: a probe group has
     * nothing to set up and still registers the hook, so "a probe creates
     * nothing" is visible in the emitted source rather than being a convention to
     * remember.
     */
    public void runSetup(TestContext t, Call... calls) {
        Execution e = new Execution(t, "setup");
        for (int i = 0; i < calls.length; i++) {
            e.invoke(calls[i], "setup[" + i + "]");
        }
    }

    /**
     * Runs a group's teardown steps, each wrapped individually: an error, or an
     * unresolvable ref, skips that step and the rest still run. Each skip is
     * logged to stderr and none of them fails the group, which is this suite's
     * existing teardown convention.
     *
     * <p>Throwing instead would report a teardown failure on every clean run of a
     * lifecycle group: the delete test has already removed the resource the
     * teardown step names, so a "not found" there is the expected outcome, not a
     * leak. Proof that nothing leaked is the orphan sweep — a {@code {runId}}
     * search after the run — not the teardown's own exit status.
     */
    public void runTeardown(TestContext t, Call... calls) {
        Execution e = new Execution(t, "teardown");
        for (int i = 0; i < calls.length; i++) {
            String step = "teardown[" + i + "]";
            try {
                e.invoke(calls[i], step);
            } catch (RuntimeException | AssertionError skipped) {
                t.log(name + ": skipped " + step + ": " + skipped.getMessage());
            }
        }
    }

    private static ErrorSpec errorCodeClause(List<Clause> assertions) {
        for (Clause a : assertions) {
            if (a.kind() == Clause.Kind.ERROR_CODE) {
                return a.error();
            }
        }
        return null;
    }

    /** A response together with the call that produced it. */
    private record Observed(String op, String params, Object body, boolean ok) {}

    /** One send: the response it observed, and the SDK's own error if it failed. */
    private record Attempt(Observed observed, RuntimeException error) {}

    /** One group-scoped run of one test, setup or teardown. */
    private final class Execution {

        private final TestContext ctx;
        private final ContextBag bag;
        private final String test;

        Execution(TestContext ctx, String test) {
            this.ctx = ctx;
            this.bag = bagFor(ctx);
            this.test = test;
        }

        private Binder binder() {
            return new Binder(ctx.runId(), name, bag);
        }

        // ── Calling ──────────────────────────────────────────────────────────

        /**
         * Builds a call's request and sends it, keeping the SDK's own error
         * separate from this package's.
         *
         * <p>The returned {@link Observed} carries the exact params JSON sent, so
         * every failure downstream of it quotes what went on the wire. A failure
         * attributable to the scenario before anything was sent — an unresolvable
         * ref, a value that does not fit the member — is thrown here, already
         * fully described.
         */
        Attempt callRaw(Call c, String step) {
            Object request;
            try {
                request = c.build(binder());
            } catch (ValueException e) {
                // Nothing was sent, so field 3 shows the params as the scenario
                // file writes them rather than a half-built request that never
                // existed.
                Observed obs = new Observed(c.op(), c.params(), null, false);
                if (e.contextPath() != null) {
                    throw fail(obs, step, "params", e.contextPath(),
                            "the context path to be set", "<unset>");
                }
                throw fail(obs, step, "params", e.member() == null ? "" : e.member(),
                        "a value the input member accepts", Failure.quote(String.valueOf(e.getMessage())));
            }

            Observed obs = new Observed(c.op(), Json.canonical(Doc.of(request)), null, false);
            try {
                Object response = c.send(request);
                return new Attempt(new Observed(c.op(), obs.params(), Doc.of(response), true), null);
            } catch (RuntimeException sdkErr) {
                return new Attempt(obs, sdkErr);
            }
        }

        /** {@link #callRaw} with the SDK's error turned into a failure. */
        Observed call(Call c, String step) {
            Attempt attempt = callRaw(c, step);
            if (attempt.error() != null) {
                throw failedCall(attempt.observed(), step, attempt.error());
            }
            return attempt.observed();
        }

        /**
         * {@link #call} plus its exports, for a setup or teardown step, whose
         * whole purpose is the context values it leaves behind.
         */
        void invoke(Call c, String step) {
            applyExports(c, call(c, step), step);
        }

        /**
         * Writes a call's response paths into the context bag. An export path
         * that does not resolve is a failure of the step that carries it: the
         * value a later step will reference is not there, and failing here names
         * the path instead of failing later with an unresolvable reference.
         */
        void applyExports(Call c, Observed obs, String step) {
            for (Map.Entry<String, String> entry : c.exports().entrySet()) {
                String responsePath = entry.getValue();
                Paths.Resolved resolved = resolve(obs, responsePath, step, "export");
                if (!resolved.ok()) {
                    throw fail(obs, step, "export", responsePath,
                            "a value to export into " + Json.quote(entry.getKey()), Json.MISSING);
                }
                bag.set(entry.getKey(), resolved.value());
            }
        }

        // ── Asserting ────────────────────────────────────────────────────────

        void assertClause(Clause a, Observed primary, String step) {
            switch (a.kind()) {
                case RESPONSE_FIELD -> checkAll(primary, a.checks(), Clause.Kind.RESPONSE_FIELD.label(), step);
                case READBACK -> {
                    Observed obs = call(a.call(), step);
                    checkAll(obs, a.checks(), Clause.Kind.READBACK.label(), step);
                    // A clause's exports are applied only once the clause holds:
                    // inside an eventually, the failing attempts must not leave a
                    // half-read response in the bag for the next clause to
                    // reference.
                    applyExports(a.call(), obs, step);
                }
                case LIST_CONTAINS, ABSENT -> assertList(a, primary, step);
                case EVENTUALLY -> eventually(a, primary, step);
                case ERROR_CODE -> throw fail(primary, step, Clause.Kind.ERROR_CODE.label(), "",
                        "an errorCode clause on the test's own call", "a nested one");
            }
        }

        /** Evaluates {@code listContains} and both forms of {@code absent}. */
        void assertList(Clause a, Observed primary, String step) {
            String kind = a.kind().label();

            if (a.kind() == Clause.Kind.ABSENT && a.error() != null) {
                Attempt attempt = callRaw(a.call(), step);
                if (attempt.error() == null) {
                    throw fail(attempt.observed(), step, kind, "", a.error().accepted(), "<no error>");
                }
                if (!Errors.matches(attempt.error(), a.error())) {
                    throw fail(attempt.observed(), step, kind, "", a.error().accepted(),
                            Failure.quote(String.valueOf(attempt.error().getMessage())));
                }
                return;
            }

            Observed obs = a.call() == null ? primary : call(a.call(), step);
            if (!obs.ok()) {
                throw fail(obs, step, kind, a.itemsPath(), "a response to read the list from", "<no response>");
            }

            Paths.Resolved resolved = resolve(obs, a.itemsPath(), step, kind);
            List<Object> list = List.of();
            if (resolved.ok()) {
                if (!(resolved.value() instanceof List<?> items)) {
                    throw fail(obs, step, kind, a.itemsPath(), "a list", Json.render(resolved.value()));
                }
                list = new ArrayList<>(items);
            }
            // A missing list counts as empty: several AWS services omit an empty
            // list member rather than serializing [].

            List<Object> wanted = new ArrayList<>(a.where().size());
            Binder b = binder();
            for (Where entry : a.where()) {
                try {
                    wanted.add(b.eval(entry.value()));
                } catch (ValueException e) {
                    throw fail(obs, step, kind, entry.path(), "the where value to evaluate",
                            Failure.quote(String.valueOf(e.getMessage())));
                }
            }

            int matched = -1;
            for (int i = 0; i < list.size() && matched < 0; i++) {
                boolean all = true;
                for (int j = 0; j < a.where().size() && all; j++) {
                    Paths.Resolved got = resolveIn(list.get(i), a.where().get(j).path(), obs, step, kind);
                    all = got.ok() && Json.equal(got.value(), wanted.get(j));
                }
                if (all) {
                    matched = i;
                }
            }

            if (a.kind() == Clause.Kind.LIST_CONTAINS) {
                if (matched < 0) {
                    throw fail(obs, step, kind, a.itemsPath(),
                            "an item matching " + Failure.renderWhere(a.where(), wanted),
                            Failure.renderList(list));
                }
            } else if (matched >= 0) {
                throw fail(obs, step, kind, a.itemsPath(),
                        "no item matching " + Failure.renderWhere(a.where(), wanted),
                        Json.render(list.get(matched)));
            }

            // The clause held. A list clause may carry a call with exports of its
            // own, and they are applied on the same terms as a read-back's.
            if (a.call() != null) {
                applyExports(a.call(), obs, step);
            }
        }

        /**
         * Retries the inner clause up to {@code maxAttempts} times, waiting
         * {@code delayMs} between attempts and no longer.
         *
         * <p>The last failure is the reported one, behind the budget that was
         * spent on it. Bare, it is indistinguishable from a clause evaluated once,
         * and the two want opposite fixes: a real disagreement, or a poll budget
         * too short for how long this service takes to settle. All the backends
         * word the prefix identically, so a generated group's give-up reads the
         * same whichever suite reports it.
         */
        void eventually(Clause a, Observed primary, String step) {
            int attempts = Math.max(1, a.maxAttempts());
            String inner = step + ".assert";
            AssertionError last = null;
            for (int i = 0; i < attempts; i++) {
                if (i > 0) {
                    sleep(a.delayMs());
                }
                try {
                    assertClause(a.inner(), primary, inner);
                    return;
                } catch (AssertionError e) {
                    last = e;
                }
            }
            String message = "eventually gave up after " + attempts + " attempt(s) "
                    + a.delayMs() + "ms apart; last failure: " + last.getMessage();
            // An inner 501 still reaches the harness as unimplemented: the
            // classification belongs to the call, not to how many times it was
            // retried.
            throw last instanceof UnimplementedFailure
                    ? new UnimplementedFailure(message)
                    : new Failure(message);
        }

        private void sleep(int delayMs) {
            if (delayMs <= 0) {
                return;
            }
            try {
                // Bounded by maxAttempts and written by the recipe, which is what
                // the "no fixed sleeps" rule asks for: this is the wait between
                // two attempts of a poll loop, not a guess at how long the
                // service takes.
                Thread.sleep(delayMs);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        }

        /**
         * Evaluates every check of a clause against one response, in the order
         * the emitter wrote them — which is path order, so a failure message is
         * the same on every run.
         */
        void checkAll(Observed obs, List<Check> checks, String kind, String step) {
            if (!obs.ok()) {
                throw fail(obs, step, kind, "", "a response to check", "<no response>");
            }
            for (Check c : checks) {
                check(obs, c, kind, step);
            }
        }

        void check(Observed obs, Check c, String kind, String step) {
            String label = kind + " " + c.kind().label();
            Paths.Resolved got = resolve(obs, c.path(), step, label);

            switch (c.kind()) {
                case MISSING -> {
                    if (got.ok()) {
                        throw checkFailed(obs, step, label, c, got, "the path not to resolve");
                    }
                }
                case IS_LIST -> {
                    // True of a present list, empty or not, and of an absent
                    // member: several AWS services omit an empty list rather than
                    // serializing []. A present value that is not a list fails.
                    if (got.ok() && !(got.value() instanceof List<?>)) {
                        throw checkFailed(obs, step, label, c, got, "a list, or no such member");
                    }
                }
                case NON_EMPTY -> {
                    if (!got.ok() || Json.isEmpty(got.value())) {
                        throw checkFailed(obs, step, label, c, got, "a non-empty value");
                    }
                }
                case EQUALS -> {
                    Object want;
                    try {
                        want = binder().eval(c.value());
                    } catch (ValueException e) {
                        throw fail(obs, step, label, c.path(), "the expected value to evaluate",
                                Failure.quote(String.valueOf(e.getMessage())));
                    }
                    if (!got.ok() || !Json.equal(got.value(), want)) {
                        throw checkFailed(obs, step, label, c, got, Json.render(want));
                    }
                }
                case EQUALS_JSON -> {
                    String actual = EqualsJson.mismatch(c.value(), got);
                    if (actual != null) {
                        throw fail(obs, step, label, c.path(), EqualsJson.expected(c.value()), actual);
                    }
                }
                case MATCHES -> {
                    String pattern = String.valueOf(c.value());
                    Pattern re;
                    try {
                        re = Pattern.compile(pattern);
                    } catch (PatternSyntaxException e) {
                        // A pattern the engine will not compile is a normal
                        // six-field mismatch in every backend, never an exception
                        // out of the evaluator, and the phrase is the same in all
                        // of them.
                        throw fail(obs, step, label, c.path(), "pattern " + pattern,
                                Failure.quote("unsupported pattern: " + e.getMessage()));
                    }
                    if (!got.ok() || !(got.value() instanceof String s) || !re.matcher(s).find()) {
                        throw checkFailed(obs, step, label, c, got,
                                "a string matching " + Json.quote(pattern));
                    }
                }
            }
        }

        // ── Failures ─────────────────────────────────────────────────────────

        private Paths.Resolved resolve(Observed obs, String path, String step, String kind) {
            try {
                return Paths.resolve(obs.body(), path);
            } catch (ValueException e) {
                throw fail(obs, step, kind, path, "a well-formed path",
                        Failure.quote(String.valueOf(e.getMessage())));
            }
        }

        private Paths.Resolved resolveIn(Object item, String path, Observed obs, String step, String kind) {
            try {
                return Paths.resolve(item, path);
            } catch (ValueException e) {
                throw fail(obs, step, kind, path, "a well-formed where path",
                        Failure.quote(String.valueOf(e.getMessage())));
            }
        }

        private Failure checkFailed(Observed obs, String step, String label, Check c,
                                    Paths.Resolved got, String expected) {
            return fail(obs, step, label, c.path(), expected,
                    Json.renderResolved(got.value(), got.ok()));
        }

        Failure fail(Observed obs, String step, String kind, String path, String expected, String actual) {
            return new Failure(Failure.message(name, test, obs.op(), obs.params(),
                    kind, path, expected, actual, file, step));
        }

        /**
         * Reports a call that should have succeeded. The SDK's error text is
         * quoted verbatim as the actual value, so the reader sees what the SDK
         * said.
         *
         * <p>Classification is decided here rather than left to the message: this
         * is the one place holding the raw SDK exception, and a composed message
         * is not something the runner's substring heuristic may be pointed at.
         */
        Failure failedCall(Observed obs, String step, RuntimeException sdkErr) {
            String message = Failure.message(name, test, obs.op(), obs.params(), "call", "",
                    "the call to succeed", Failure.quote(String.valueOf(sdkErr.getMessage())), file, step);
            return Errors.isUnimplemented(sdkErr) ? new UnimplementedFailure(message) : new Failure(message);
        }
    }

    /**
     * The group's context bag, created on first use. The harness creates one
     * {@code TestContext} per group run and hands the same one to setup, every
     * test and teardown, so the bag has exactly the lifetime the IR gives a
     * group's context.
     */
    private static ContextBag bagFor(TestContext ctx) {
        synchronized (ctx) {
            ContextBag bag = ctx.get(BAG_KEY);
            if (bag == null) {
                bag = new ContextBag();
                ctx.set(BAG_KEY, bag);
            }
            return bag;
        }
    }
}
