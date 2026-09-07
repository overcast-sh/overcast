package io.overcast.compat.harness;

import com.fasterxml.jackson.databind.ObjectMapper;

import software.amazon.awssdk.awscore.exception.AwsErrorDetails;
import software.amazon.awssdk.awscore.exception.AwsServiceException;
import software.amazon.awssdk.core.exception.SdkServiceException;

import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Runner executes a list of {@link TestGroup}s and emits NDJSON events to
 * {@code stdout}.
 *
 * <p>Events follow the same schema used by the node-js-sdk and go-sdk suites
 * so that the Overcast compatibility dashboard can aggregate results across
 * all language suites.
 *
 * <pre>
 * {"event":"run_start",   "suite":"java-sdk", "started_at":"...", "endpoint":"...", "version":"0"}
 * {"event":"test_result", "suite":"java-sdk", "service":"s3",  "group":"s3-crud",
 *                          "test":"CreateBucket", "status":"pass", "duration_ms":42}
 * {"event":"run_end",     "suite":"java-sdk", "passed":1, "failed":0, "skipped":0,
 *                          "unimplemented":0, "duration_ms":200}
 * </pre>
 */
public final class Runner {

    // Statuses
    private static final String PASS          = "pass";
    private static final String FAIL          = "fail";
    private static final String SKIP          = "skip";
    private static final String UNIMPLEMENTED = "unimplemented";

    private static final ObjectMapper MAPPER = new ObjectMapper();

    /** Emits run_start, executes every group in parallel (bounded by OVERCAST_COMPAT_PARALLEL_SLOTS), then emits run_end. */
    public static void runSuite(String suite, String endpoint, List<TestGroup> groups) {
        long suiteStart = System.currentTimeMillis();

        int totalTests = groups.stream().mapToInt(g -> g.tests().size()).sum();
        emit(new RunStartEvent(suite, Instant.now().toString(), endpoint, "1", totalTests));

        int slots = parallelSlots();

        AtomicInteger passed = new AtomicInteger();
        AtomicInteger failed = new AtomicInteger();
        AtomicInteger skipped = new AtomicInteger();
        AtomicInteger unimplemented = new AtomicInteger();

        ExecutorService pool = Executors.newFixedThreadPool(slots);
        List<Future<?>> futures = new ArrayList<>(groups.size());
        String region = System.getenv("OVERCAST_DEFAULT_REGION") != null
                ? System.getenv("OVERCAST_DEFAULT_REGION") : "us-east-1";
        String runId = System.getenv("OVERCAST_COMPAT_RUN_ID") != null
                ? System.getenv("OVERCAST_COMPAT_RUN_ID") : "local";

        for (TestGroup group : groups) {
            futures.add(pool.submit(() -> {
                int[] counts = runGroup(suite, endpoint, region, runId, group);
                passed.addAndGet(counts[0]);
                failed.addAndGet(counts[1]);
                skipped.addAndGet(counts[2]);
                unimplemented.addAndGet(counts[3]);
            }));
        }

        for (Future<?> f : futures) {
            try {
                // 5-minute per-group timeout: prevents one hung AWS SDK call
                // from blocking the entire suite indefinitely.
                f.get(5, TimeUnit.MINUTES);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            } catch (ExecutionException | TimeoutException ignored) {}
        }
        pool.shutdown();

        long totalMs = System.currentTimeMillis() - suiteStart;
        emit(new RunEndEvent(suite, passed.get(), failed.get(), skipped.get(), unimplemented.get(), totalMs));
    }

    /** Runs a single group; returns [passed, failed, skipped, unimplemented]. */
    private static int[] runGroup(String suite, String endpoint, String region, String runId, TestGroup group) {
        TestContext ctx = new TestContext(endpoint, region, runId);
        Counts counts = new Counts();

        // Setup phase. A failure reports every test in the group as skipped
        // with the reason, and teardown still runs: setup may have created
        // something before the step that failed.
        if (group.setup() != null) {
            try {
                group.setup().run(ctx);
            } catch (Throwable e) {
                String reason = "setup failed: " + e.getMessage();
                for (TestCase tc : group.tests()) {
                    counts.record(emitted(new TestResultEvent(suite, group.service(), group.name(),
                            tc.name(), SKIP, 0, reason), SKIP));
                }
                runTeardown(group, ctx);
                return counts.toArray();
            }
        }

        // Test phase. A group marked parallel whose tests declare no
        // dependencies runs them concurrently; everything else runs in
        // declaration order. Both halves of that condition are load-bearing:
        // the concurrent path cannot express the dependency gate — it would
        // have to decide what to skip from outcomes that have not happened yet
        // — so a group declaring one runs serially even where the registry says
        // parallel. The IR never produces that combination (only a probe group
        // is parallel, and a probe has no exports for a depends to consume),
        // which is why this is a guard rather than a scheduler.
        if (group.parallel() && !hasDependencies(group.tests())) {
            runTestsConcurrently(suite, group, ctx, counts);
        } else {
            runTestsInOrder(suite, group, ctx, counts);
        }

        runTeardown(group, ctx);
        return counts.toArray();
    }

    /**
     * The serial path: one test at a time, in declaration order, each result
     * emitted as it completes.
     */
    private static void runTestsInOrder(String suite, TestGroup group, TestContext ctx, Counts counts) {
        // Tests that did not pass, so a test declaring one of them as a
        // dependency is skipped rather than run against a prerequisite that
        // never happened.
        Set<String> failedOrSkipped = new HashSet<>();

        for (TestCase tc : group.tests()) {
            Outcome out = marker(suite, group, tc);
            if (out == null) {
                // Dependency gate — skip if any declared dependency failed or
                // was skipped. Without it a single broken prerequisite reports
                // as a cascade of unrelated failures, and "dependency failed:
                // X" is what tells a reader the cause is elsewhere.
                out = dependencyGate(suite, group, tc, failedOrSkipped);
            }
            if (out == null) {
                out = runOne(suite, group, ctx, tc);
            }
            counts.record(emitted(out));
            if (!PASS.equals(out.status())) {
                failedOrSkipped.add(tc.name());
            }
        }
    }

    /**
     * The concurrent path: a group's tests through a bounded pool, their
     * results emitted in declaration order once all of them are in.
     *
     * <p>Emitting in order rather than as each finishes is what keeps this
     * stream identical to the serial path's, test for test. The dashboard, the
     * baseline and the flake detector all read it, and a result order that
     * depended on which call answered first would be a new source of diff noise
     * for no benefit.
     *
     * <p>No dependency bookkeeping: this path is taken only when no test
     * declares one, so the set the serial path maintains would be read by
     * nobody.
     */
    private static void runTestsConcurrently(String suite, TestGroup group, TestContext ctx, Counts counts) {
        List<TestCase> tests = group.tests();
        List<Outcome> outcomes = new ArrayList<>(Collections.nCopies(tests.size(), null));
        ExecutorService pool = Executors.newFixedThreadPool(Math.min(parallelSlots(), Math.max(1, tests.size())));
        List<Future<?>> futures = new ArrayList<>(tests.size());
        for (int i = 0; i < tests.size(); i++) {
            final int index = i;
            final TestCase tc = tests.get(i);
            futures.add(pool.submit(() -> {
                Outcome marked = marker(suite, group, tc);
                outcomes.set(index, marked != null ? marked : runOne(suite, group, ctx, tc));
            }));
        }
        for (Future<?> f : futures) {
            try {
                f.get(5, TimeUnit.MINUTES);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            } catch (ExecutionException | TimeoutException ignored) {}
        }
        pool.shutdown();

        for (int i = 0; i < tests.size(); i++) {
            Outcome out = outcomes.get(i);
            if (out == null) {
                // The worker never produced one: it was interrupted, or it hit
                // the per-test timeout. Reported as a failure rather than
                // dropped — a test missing from the stream reads as a registry
                // gap in every consumer of it.
                out = new Outcome(new TestResultEvent(suite, group.service(), group.name(),
                        tests.get(i).name(), FAIL, 0, "test did not complete"), FAIL);
            }
            counts.record(emitted(out));
        }
    }

    /** One test's result, held rather than emitted, so a parallel group can emit in order. */
    private record Outcome(TestResultEvent event, String status) {}

    /** The running per-group tally, in the order runGroup returns it. */
    private static final class Counts {
        private int passed, failed, skipped, unimplemented;

        void record(String status) {
            switch (status) {
                case PASS -> passed++;
                case FAIL -> failed++;
                case UNIMPLEMENTED -> unimplemented++;
                default -> skipped++;
            }
        }

        int[] toArray() {
            return new int[]{passed, failed, skipped, unimplemented};
        }
    }

    private static String emitted(Outcome out) {
        emit(out.event());
        return out.status();
    }

    private static String emitted(TestResultEvent event, String status) {
        emit(event);
        return status;
    }

    /**
     * The registry's own reason for not running a test, or null when there is
     * none. It outranks the dependency gate: a test the suite never intended to
     * run here reports why it was marked, not what happened to something it
     * does not depend on.
     */
    private static Outcome marker(String suite, TestGroup group, TestCase tc) {
        if (tc.skip() == null || tc.skip().isEmpty()) {
            return null;
        }
        return new Outcome(new TestResultEvent(suite, group.service(), group.name(),
                tc.name(), SKIP, 0, tc.skip()), SKIP);
    }

    private static Outcome dependencyGate(String suite, TestGroup group, TestCase tc, Set<String> failedOrSkipped) {
        List<String> missingDeps = new ArrayList<>();
        for (String dep : tc.depends()) {
            if (failedOrSkipped.contains(dep)) {
                missingDeps.add(dep);
            }
        }
        if (missingDeps.isEmpty()) {
            return null;
        }
        String reason = "dependency failed: " + String.join(", ", missingDeps);
        return new Outcome(new TestResultEvent(suite, group.service(), group.name(),
                tc.name(), SKIP, 0, reason), SKIP);
    }

    /** Runs one test body and classifies whatever it threw. */
    private static Outcome runOne(String suite, TestGroup group, TestContext ctx, TestCase tc) {
        long start = System.currentTimeMillis();
        try {
            tc.fn().run(ctx);
            long ms = System.currentTimeMillis() - start;
            return new Outcome(new TestResultEvent(suite, group.service(), group.name(),
                    tc.name(), PASS, ms, null), PASS);
        } catch (Throwable e) {
            long ms = System.currentTimeMillis() - start;
            String msg = e.getMessage() != null ? e.getMessage() : e.getClass().getSimpleName();
            String status = isUnimplemented(e) ? UNIMPLEMENTED : FAIL;
            return new Outcome(new TestResultEvent(suite, group.service(), group.name(),
                    tc.name(), status, ms, msg), status);
        }
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    /** Whether any test in the group declares a dependency. */
    static boolean hasDependencies(List<TestCase> tests) {
        return tests.stream().anyMatch(tc -> !tc.depends().isEmpty());
    }

    /**
     * How many things this suite may do at once — groups in
     * {@link #runSuite}, and the tests of one parallel group in
     * {@link #runTestsConcurrently}. OVERCAST_COMPAT_PARALLEL_SLOTS is injected
     * by the Go runner; default 8.
     *
     * <p>One number bounds both because it answers one question — how much load
     * this machine should put on the emulator at once — and a second knob would
     * only let the two drift apart.
     */
    static int parallelSlots() {
        String slotsEnv = System.getenv("OVERCAST_COMPAT_PARALLEL_SLOTS");
        if (slotsEnv != null && !slotsEnv.isEmpty()) {
            try {
                return Math.max(1, Integer.parseInt(slotsEnv));
            } catch (NumberFormatException ignored) {
                // Fall through to the default: a malformed value is not worth
                // failing a run over, and 8 is what an unset one gives.
            }
        }
        return 8;
    }

    private static void runTeardown(TestGroup group, TestContext ctx) {
        if (group.teardown() != null) {
            try {
                group.teardown().run(ctx);
            } catch (Throwable e) {
                System.err.println("[java-sdk] teardown failed for " + group.name() + ": " + e.getMessage());
            }
        }
    }

    /**
     * Returns {@code true} when {@code e} signals a 501 / not-implemented
     * response from the Overcast emulator.
     *
     * <p>Two markers come first, and both exist for the generated scenario
     * runtime — the only code here that composes a failure message out of data
     * it was handed. {@link Unimplemented} is a classification already decided
     * from the SDK's own status code, and it must survive being wrapped.
     * {@link ComposedFailure} is the opposite guarantee: such a message embeds
     * the exact params JSON sent, where a run id, a queue URL or a port number
     * can put a {@code "501"} that says nothing about the response.
     *
     * <p>Everything else is decided from the <b>response the SDK's exception
     * carries</b> — {@link #classifyResponse} — and only an exception carrying
     * none reaches the substring heuristic. The prose used to be the whole rule
     * for a hand-written group, and a 400 was enough to defeat it: the sibling
     * go-sdk suite reported a test that asserts an
     * {@code InvalidRequestException} as {@code unimplemented} on one CI run
     * whose request id happened to contain "501", flipping a gated baseline row
     * and failing an unrelated pull request (#1924).
     */
    public static boolean isUnimplemented(Throwable e) {
        if (e == null) return false;
        if (e instanceof Unimplemented) return true;
        if (e instanceof ComposedFailure) return false;
        Boolean fromResponse = classifyResponse(e);
        if (fromResponse != null) return fromResponse;
        String msg = e.getMessage();
        if (msg == null) msg = e.getClass().getName();
        return looksUnimplementedWithoutResponse(msg);
    }

    /**
     * Decides, from the HTTP response an AWS SDK exception carries, whether the
     * emulator refused the operation as unimplemented. Returns {@code null}
     * when the exception carries no response — the SDK failed before or after
     * the exchange — which is the one case a caller may fall back to the text.
     *
     * <p>Two things say "unimplemented", and both are facts of the response
     * rather than of its wording: HTTP 501, with the
     * {@code x-emulator-unsupported} header Overcast sets alongside every one
     * of them; and an error <b>code</b> of {@code NotImplemented} or
     * {@code UnknownOperationException}, by equality. AWS — and Overcast —
     * answer a target naming no modeled operation with the latter at HTTP 400,
     * so the status alone would miss it.
     */
    public static Boolean classifyResponse(Throwable e) {
        for (Throwable link = e; link != null; link = link.getCause()) {
            if (!(link instanceof SdkServiceException sdk)) continue;
            AwsErrorDetails details =
                    link instanceof AwsServiceException aws ? aws.awsErrorDetails() : null;
            if (sdk.statusCode() == 0 && details == null) {
                // A service exception the SDK raised without a response to
                // read. Keep looking, and let the text answer if nothing else
                // in the chain carries one.
                continue;
            }
            if (sdk.statusCode() == 501 || emulatorUnsupported(details)) return true;
            String code = details == null ? null : details.errorCode();
            return "NotImplemented".equals(code) || "UnknownOperationException".equals(code);
        }
        return null;
    }

    /** Whether the response carries the header Overcast sets on every 501. */
    private static boolean emulatorUnsupported(AwsErrorDetails details) {
        if (details == null || details.sdkHttpResponse() == null) return false;
        return details.sdkHttpResponse()
                .firstMatchingHeader("x-emulator-unsupported")
                .filter("true"::equalsIgnoreCase)
                .isPresent();
    }

    /**
     * The substring heuristic over an SDK error's own text, and it is for an
     * exception carrying <b>no HTTP response</b> — the SDK failed before or
     * after the exchange, so there is nothing else to read.
     *
     * <p>It is never right for an exception that reached the wire: the response
     * states the status, and "501" appears in request ids, ARNs, resource names
     * and port numbers. {@link #classifyResponse} answers that case.
     */
    public static boolean looksUnimplementedWithoutResponse(String msg) {
        if (msg == null) return false;
        return msg.contains("501")
                || msg.contains("NotImplemented")
                || msg.contains("UnknownOperationException")
                || msg.contains("Unknown action")
                || msg.contains("not implemented");
    }

    // Serialises {@code v} as a single NDJSON line to stdout.  Must be
    // synchronised because multiple threads could call emit() concurrently;
    // this ensures lines are never interleaved.
    private static synchronized void emit(Object v) {
        try {
            System.out.println(MAPPER.writeValueAsString(v));
            System.out.flush();
        } catch (Exception e) {
            System.err.println("[java-sdk] failed to serialise event: " + e.getMessage());
        }
    }

    // ── Event record types ────────────────────────────────────────────────────

    record RunStartEvent(
            String event,
            String suite,
            String started_at,
            String endpoint,
            String version,
            int total_tests) {

        RunStartEvent(String suite, String startedAt, String endpoint, String version, int totalTests) {
            this("run_start", suite, startedAt, endpoint, version, totalTests);
        }
    }

    record TestResultEvent(
            String event,
            String suite,
            String service,
            String group,
            String test,
            String status,
            long duration_ms,
            String error) {

        TestResultEvent(String suite, String service, String group, String test,
                        String status, long durationMs, String error) {
            this("test_result", suite, service, group, test, status, durationMs, error);
        }

        // Jackson omits null fields automatically when the mapper is configured
        // with INCLUDE_NON_NULL — but the constructor sets the field, so we use a
        // custom serialiser or just accept null in JSON. The dashboard ignores null.
    }

    record RunEndEvent(
            String event,
            String suite,
            int passed,
            int failed,
            int skipped,
            int unimplemented,
            long duration_ms) {

        RunEndEvent(String suite, int passed, int failed, int skipped, int unimplemented, long ms) {
            this("run_end", suite, passed, failed, skipped, unimplemented, ms);
        }
    }
}
