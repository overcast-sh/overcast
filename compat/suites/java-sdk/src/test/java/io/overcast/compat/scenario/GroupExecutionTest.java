package io.overcast.compat.scenario;

import io.overcast.compat.harness.Runner;
import io.overcast.compat.harness.TestContext;
import org.junit.jupiter.api.Test;
import software.amazon.awssdk.awscore.exception.AwsErrorDetails;
import software.amazon.awssdk.awscore.exception.AwsServiceException;
import software.amazon.awssdk.http.SdkHttpResponse;
import software.amazon.awssdk.services.iam.model.GetRoleResponse;
import software.amazon.awssdk.services.iam.model.Role;
import software.amazon.awssdk.services.sqs.model.CreateQueueRequest;
import software.amazon.awssdk.services.sqs.model.CreateQueueResponse;
import software.amazon.awssdk.services.sqs.model.GetQueueAttributesRequest;
import software.amazon.awssdk.services.sqs.model.GetQueueAttributesResponse;
import software.amazon.awssdk.services.sqs.model.ListQueuesRequest;
import software.amazon.awssdk.services.sqs.model.ListQueuesResponse;
import software.amazon.awssdk.services.sqs.model.QueueAttributeName;
import software.amazon.awssdk.services.sqs.model.QueueDoesNotExistException;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Function;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The runtime the emitted groups call into, exercised against an in-memory
 * fake: a {@link Call}'s send is a lambda, so a fake service is a lambda
 * returning a canned SDK response. The requests are the real SDK request classes
 * and the responses the real SDK response classes, so what these tests pin about
 * absent members, boxed scalars and enum-keyed maps is what a live run sees.
 */
class GroupExecutionTest {

    private static final Group GROUP =
            new Group("sqs-gen-queue", "compat/model/scenarios/sqs.json");

    private static TestContext ctx() {
        return new TestContext("http://127.0.0.1:1", "us-east-1", "oc-test");
    }

    private static Function<Object, Object> ok(Object response, AtomicReference<Object> seen) {
        return request -> {
            if (seen != null) {
                seen.set(request);
            }
            return response;
        };
    }

    private static Function<Object, Object> fails(RuntimeException e) {
        return request -> {
            throw e;
        };
    }

    /** The call the emitter writes for sqs-gen-queue/CreateQueue, hand-copied. */
    private static Call createQueue(Object response, AtomicReference<Object> seen) {
        return new Call("CreateQueue", "{\"QueueName\":{\"$name\":\"q\"}}",
                b -> CreateQueueRequest.builder()
                        .queueName(b.string("QueueName", Values.name("q")))
                        .build(),
                ok(response, seen))
                .export("queue.url", "$.QueueUrl");
    }

    private static Call getQueueAttributes(Object response, String... exports) {
        Call c = new Call("GetQueueAttributes", "{\"AttributeNames\":[\"All\"]}",
                b -> GetQueueAttributesRequest.builder()
                        .attributeNames(List.of(QueueAttributeName.fromValue("All")))
                        .queueUrl(b.string("QueueUrl", Values.ref("queue.url")))
                        .build(),
                ok(response, null));
        for (int i = 0; i < exports.length; i += 2) {
            c.export(exports[i], exports[i + 1]);
        }
        return c;
    }

    // ── The happy path ───────────────────────────────────────────────────────

    @Test
    void callExportsAndEveryClauseHolds() {
        AtomicReference<Object> sent = new AtomicReference<>();
        TestContext t = ctx();

        GROUP.runTest(t, "CreateQueue",
                createQueue(CreateQueueResponse.builder()
                        .queueUrl("http://q/oc-test-sqs-gen-queue-q").build(), sent),
                List.of(
                        Clause.responseField(Check.nonEmpty("$.QueueUrl")),
                        Clause.readback(
                                getQueueAttributes(GetQueueAttributesResponse.builder()
                                                .attributesWithStrings(Map.of(
                                                        "QueueArn", "arn:aws:sqs:us-east-1:000000000000:q",
                                                        "VisibilityTimeout", "30"))
                                                .build(),
                                        "queue.arn", "$.Attributes.QueueArn"),
                                Check.equalTo("$.Attributes.VisibilityTimeout", "30")),
                        Clause.listContains(
                                new Call("ListQueues", "{}",
                                        b -> ListQueuesRequest.builder().build(),
                                        ok(ListQueuesResponse.builder()
                                                .queueUrls("http://q/oc-test-sqs-gen-queue-q").build(), null)),
                                "$.QueueUrls",
                                Where.of("$", Values.ref("queue.url")))));

        // The typed request carried the $name, and the read-back's export
        // reached the bag.
        CreateQueueRequest request = assertInstanceOf(CreateQueueRequest.class, sent.get());
        assertEquals("oc-test-sqs-gen-queue-q", request.queueName(),
                "the {runId}-{group}-{suffix} form");
        assertEquals("arn:aws:sqs:us-east-1:000000000000:q", bag(t).get("queue.arn"));
    }

    // ── Failure messages ─────────────────────────────────────────────────────

    @Test
    void failureCarriesTheSixFields() {
        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "CreateQueue",
                createQueue(CreateQueueResponse.builder().queueUrl("http://q/x").build(), null),
                List.of(Clause.readback(
                        getQueueAttributes(GetQueueAttributesResponse.builder()
                                .attributesWithStrings(Map.of("VisibilityTimeout", "30")).build()),
                        Check.equalTo("$.Attributes.VisibilityTimeout", "60")))));

        assertEquals("sqs-gen-queue/CreateQueue: GetQueueAttributes"
                + " params {\"AttributeNames\":[\"All\"],\"QueueUrl\":\"http://q/x\"}:"
                + " readback equals at $.Attributes.VisibilityTimeout:"
                + " expected \"60\", actual \"30\""
                + " (compat/model/scenarios/sqs.json assert[0])", e.getMessage());
    }

    @Test
    void unresolvableRefNamesThePathAndSendsNothing() {
        AtomicInteger sends = new AtomicInteger();
        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "GetQueueAttributes",
                new Call("GetQueueAttributes", "{\"QueueUrl\":{\"$ref\":\"queue.url\"}}",
                        b -> GetQueueAttributesRequest.builder()
                                .queueUrl(b.string("QueueUrl", Values.ref("queue.url")))
                                .build(),
                        request -> {
                            sends.incrementAndGet();
                            return GetQueueAttributesResponse.builder().build();
                        }),
                List.of(Clause.responseField(Check.nonEmpty("$.Attributes")))));

        assertEquals(0, sends.get(), "nothing may be sent once a value fails to bind");
        assertTrue(e.getMessage().contains("params {\"QueueUrl\":{\"$ref\":\"queue.url\"}}"),
                "field 3 shows the params as the scenario file writes them: " + e.getMessage());
        assertTrue(e.getMessage().contains("params at queue.url: expected the context path to be set, actual <unset>"),
                e.getMessage());
    }

    @Test
    void aValueOfTheWrongTypeNamesTheMember() {
        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "CreateQueue",
                new Call("CreateQueue", "{\"QueueName\":1}",
                        b -> CreateQueueRequest.builder()
                                .queueName(b.string("QueueName", 1))
                                .build(),
                        ok(CreateQueueResponse.builder().build(), null)),
                List.of(Clause.responseField(Check.nonEmpty("$.QueueUrl")))));

        assertTrue(e.getMessage().contains("params at QueueName:"), e.getMessage());
        assertTrue(e.getMessage().contains("wanted a string"), e.getMessage());
    }

    // ── The closed check set ─────────────────────────────────────────────────

    @Test
    void checksMatchTheIRsClosedSet() {
        // An omitted list member: absent, and an auto-construct list rather
        // than null in the SDK's own object model.
        Object response = ListQueuesResponse.builder().build();
        assertHolds(response, Check.isList("$.QueueUrls"));
        assertHolds(response, Check.missing("$.QueueUrls"));
        assertHolds(response, Check.missing("$.NextToken"));
        assertFails(response, Check.nonEmpty("$.QueueUrls"));

        Object page = ListQueuesResponse.builder().queueUrls("http://q/a").build();
        assertHolds(page, Check.isList("$.QueueUrls"));
        assertHolds(page, Check.nonEmpty("$.QueueUrls"));
        assertHolds(page, Check.equalTo("$.QueueUrls[0]", "http://q/a"));
        assertHolds(page, Check.matches("$.QueueUrls[0]", "^http://q/"));
        assertFails(page, Check.missing("$.QueueUrls"));
        assertFails(page, Check.equalTo("$.QueueUrls[0]", "http://q/b"));
        assertFails(page, Check.equalTo("$.QueueUrls", "http://q/a"));

        // A present value that is not a list fails isList.
        assertFails(page, Check.isList("$.QueueUrls[0]"));
    }

    /**
     * {@code equalsJSON} through the group, on the member it exists for: IAM
     * hands this SDK the role's trust policy as the percent-encoded text the
     * service sent, and the check decodes and compares it as a document.
     */
    @Test
    void equalsJsonComparesTheMembersDocument() {
        Object response = GetRoleResponse.builder().role(Role.builder()
                .assumeRolePolicyDocument("%7B%22Version%22%3A%222012-10-17%22%2C%22Statement%22%3A%5B%5D%7D")
                .build()).build();
        assertHolds(response, Check.equalsJson("$.Role.AssumeRolePolicyDocument",
                "{\"Statement\":[],\"Version\":\"2012-10-17\"}"));

        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "Probe", probe(response),
                List.of(Clause.responseField(Check.equalsJson("$.Role.AssumeRolePolicyDocument",
                        "{\"Version\":\"2008-10-17\"}")))));
        assertTrue(e.getMessage().contains("responseField equalsJSON at $.Role.AssumeRolePolicyDocument:"
                + " expected equalsJSON {\"Version\":\"2008-10-17\"},"
                + " actual document {\"Statement\":[],\"Version\":\"2012-10-17\"}"), e.getMessage());

        e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "Probe", probe(response),
                List.of(Clause.responseField(Check.equalsJson("$.Role.Description", "{}")))));
        assertTrue(e.getMessage().contains("expected equalsJSON {}, actual <missing>"), e.getMessage());
    }

    /**
     * Pins the wording every backend uses for a pattern its own engine will not
     * compile: an ordinary six-field mismatch, never an exception out of the
     * evaluator.
     */
    @Test
    void anUnsupportedPatternIsAnOrdinaryMismatch() {
        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "ListQueues",
                new Call("ListQueues", "{}", b -> ListQueuesRequest.builder().build(),
                        ok(ListQueuesResponse.builder().queueUrls("http://q/a").build(), null)),
                List.of(Clause.responseField(Check.matches("$.QueueUrls[0]", "a(b")))));
        assertTrue(e.getMessage().contains("expected pattern a(b, actual \"unsupported pattern:"),
                e.getMessage());
    }

    @Test
    void listClausesTreatAnAbsentListAsEmpty() {
        Call listQueues = new Call("ListQueues", "{}",
                b -> ListQueuesRequest.builder().build(),
                ok(ListQueuesResponse.builder().build(), null));

        // A missing list counts as empty: absent holds, listContains does not.
        GROUP.runTest(ctx(), "DeleteQueue", listQueues,
                List.of(Clause.absentFromList(null, "$.QueueUrls", Where.of("$", "http://q/a"))));

        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "ListQueues", listQueues,
                List.of(Clause.listContains(null, "$.QueueUrls", Where.of("$", "http://q/a")))));
        assertTrue(e.getMessage().contains("actual an empty list"), e.getMessage());
    }

    // ── eventually ───────────────────────────────────────────────────────────

    @Test
    void eventuallyRetriesAndGivesUpWithTheSharedPrefix() {
        AtomicInteger attempts = new AtomicInteger();
        Call readback = new Call("GetQueueAttributes", "{}",
                b -> GetQueueAttributesRequest.builder().build(),
                request -> GetQueueAttributesResponse.builder()
                        .attributesWithStrings(Map.of("VisibilityTimeout",
                                attempts.incrementAndGet() < 3 ? "30" : "60"))
                        .build());

        GROUP.runTest(ctx(), "SetQueueAttributes",
                new Call("SetQueueAttributes", "{}", b -> GetQueueAttributesRequest.builder().build(),
                        ok(GetQueueAttributesResponse.builder().build(), null)),
                List.of(Clause.eventually(5, 1, Clause.readback(readback,
                        Check.equalTo("$.Attributes.VisibilityTimeout", "60")))));
        assertEquals(3, attempts.get(), "the clause should stop retrying once it holds");

        AtomicInteger never = new AtomicInteger();
        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "SetQueueAttributes",
                new Call("SetQueueAttributes", "{}", b -> GetQueueAttributesRequest.builder().build(),
                        ok(GetQueueAttributesResponse.builder().build(), null)),
                List.of(Clause.eventually(3, 1, Clause.readback(
                        new Call("GetQueueAttributes", "{}",
                                b -> GetQueueAttributesRequest.builder().build(),
                                request -> {
                                    never.incrementAndGet();
                                    return GetQueueAttributesResponse.builder()
                                            .attributesWithStrings(Map.of("VisibilityTimeout", "30")).build();
                                }),
                        Check.equalTo("$.Attributes.VisibilityTimeout", "60"))))));
        assertEquals(3, never.get());
        assertTrue(e.getMessage().startsWith(
                        "eventually gave up after 3 attempt(s) 1ms apart; last failure: sqs-gen-queue/"),
                e.getMessage());
        assertTrue(e.getMessage().endsWith("assert[0].assert)"), e.getMessage());
    }

    /**
     * A read-back inside an {@code eventually} writes its exports only on the
     * attempt that passes, so a failing attempt cannot leave a stale reading in
     * the bag for a later clause to reference.
     */
    @Test
    void eventuallyExportsOnlyOnThePassingAttempt() {
        AtomicInteger attempts = new AtomicInteger();
        TestContext t = ctx();
        GROUP.runTest(t, "SetQueueAttributes",
                new Call("SetQueueAttributes", "{}", b -> GetQueueAttributesRequest.builder().build(),
                        ok(GetQueueAttributesResponse.builder().build(), null)),
                List.of(Clause.eventually(5, 1, Clause.readback(
                        new Call("GetQueueAttributes", "{}",
                                b -> GetQueueAttributesRequest.builder().build(),
                                request -> GetQueueAttributesResponse.builder()
                                        .attributesWithStrings(Map.of("QueueArn",
                                                attempts.incrementAndGet() < 2 ? "stale" : "fresh"))
                                        .build())
                                .export("queue.arn", "$.Attributes.QueueArn"),
                        Check.equalTo("$.Attributes.QueueArn", "fresh")))));
        assertEquals("fresh", bag(t).get("queue.arn"));
    }

    // ── Hooks ────────────────────────────────────────────────────────────────

    @Test
    void setupStopsAtTheFirstFailureAndTeardownWrapsEveryStep() {
        TestContext t = ctx();
        AtomicInteger ran = new AtomicInteger();
        Call second = new Call("CreateQueue", "{}",
                b -> CreateQueueRequest.builder().queueName("x").build(),
                fails(AwsServiceException.builder().message("boom").statusCode(400).build()));
        Call third = new Call("CreateQueue", "{}",
                b -> CreateQueueRequest.builder().queueName("y").build(),
                request -> {
                    ran.incrementAndGet();
                    return CreateQueueResponse.builder().build();
                });

        AssertionError e = assertThrows(AssertionError.class,
                () -> GROUP.runSetup(t, createQueue(CreateQueueResponse.builder().queueUrl("u").build(), null),
                        second, third));
        assertEquals(0, ran.get(), "setup stops at the first failure");
        assertTrue(e.getMessage().contains("setup[1]"), e.getMessage());

        // Teardown wraps each step: the first fails, the second still runs.
        AtomicInteger tornDown = new AtomicInteger();
        GROUP.runTeardown(t, second, new Call("DeleteQueue", "{}",
                b -> CreateQueueRequest.builder().queueName("z").build(),
                request -> {
                    tornDown.incrementAndGet();
                    return CreateQueueResponse.builder().build();
                }));
        assertEquals(1, tornDown.get(), "a failed teardown step must not stop the next one");
    }

    /** An empty list is a no-op, not a missing phase. */
    @Test
    void emptyHooksAreNoOps() {
        TestContext t = ctx();
        GROUP.runSetup(t);
        GROUP.runTeardown(t);
    }

    // ── Error clauses ────────────────────────────────────────────────────────

    @Test
    void errorClauses() {
        RuntimeException notFound = (RuntimeException) QueueDoesNotExistException.builder()
                .awsErrorDetails(AwsErrorDetails.builder()
                        .errorCode("com.amazonaws.sqs#QueueDoesNotExist")
                        .sdkHttpResponse(SdkHttpResponse.builder().statusCode(400).build())
                        .build())
                .message("nope").statusCode(400).build();

        Call missing = new Call("GetQueueAttributes", "{}",
                b -> GetQueueAttributesRequest.builder().build(), fails(notFound));

        // absent's error form holds when the call fails with the named error.
        GROUP.runTest(ctx(), "DeleteQueue",
                new Call("DeleteQueue", "{}", b -> CreateQueueRequest.builder().queueName("q").build(),
                        ok(CreateQueueResponse.builder().build(), null)),
                List.of(Clause.absentByError(missing,
                        ErrorSpec.of("QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue"))));

        // errorCode expects the test's own call to fail.
        GROUP.runTest(ctx(), "GetQueueAttributes", missing,
                List.of(Clause.errorCode(
                        ErrorSpec.of("QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue"))));

        // A call that succeeds fails an errorCode clause, and says so.
        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "GetQueueAttributes",
                new Call("GetQueueAttributes", "{}",
                        b -> GetQueueAttributesRequest.builder().build(),
                        ok(GetQueueAttributesResponse.builder().build(), null)),
                List.of(Clause.errorCode(ErrorSpec.of("QueueDoesNotExist", "QueueDoesNotExist")))));
        assertTrue(e.getMessage().contains("actual <no error>"), e.getMessage());
    }

    // ── Classification ───────────────────────────────────────────────────────

    @Test
    void unimplementedReachesTheHarnessAsAClassification() {
        AwsServiceException notImplemented = AwsServiceException.builder()
                .awsErrorDetails(AwsErrorDetails.builder()
                        .errorCode("UnknownOperation")
                        .sdkHttpResponse(SdkHttpResponse.builder().statusCode(501).build())
                        .build())
                .message("the operation is not supported").statusCode(501).build();

        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "ListMessageMoveTasks",
                new Call("ListMessageMoveTasks", "{}",
                        b -> ListQueuesRequest.builder().build(), fails(notImplemented)),
                List.of(Clause.responseField(Check.nonEmpty("$.Results")))));

        assertInstanceOf(UnimplementedFailure.class, e);
        assertTrue(Runner.isUnimplemented(e), "the harness must classify this as unimplemented");
    }

    /**
     * An {@code eventually} that gave up on a 501 still reports as
     * unimplemented. The classification belongs to the call, not to how many
     * times it was retried, and a probe wrapped in a poll would otherwise
     * report as a hard failure — which for a candidate group is the difference
     * between "not implemented yet" and "this regressed".
     */
    @Test
    void eventuallyKeepsAnInnerUnimplementedClassification() {
        AwsServiceException notImplemented = AwsServiceException.builder()
                .awsErrorDetails(AwsErrorDetails.builder()
                        .errorCode("UnknownOperation")
                        .sdkHttpResponse(SdkHttpResponse.builder().statusCode(501).build())
                        .build())
                .message("the operation is not supported").statusCode(501).build();

        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "ListMessageMoveTasks",
                new Call("ListMessageMoveTasks", "{}",
                        b -> ListQueuesRequest.builder().build(),
                        ok(ListQueuesResponse.builder().build(), null)),
                List.of(Clause.eventually(2, 1, Clause.readback(
                        new Call("ListMessageMoveTasks", "{}",
                                b -> ListQueuesRequest.builder().build(), fails(notImplemented)),
                        Check.nonEmpty("$.Results"))))));

        assertInstanceOf(UnimplementedFailure.class, e,
                "the give-up must carry the inner call's classification");
        assertTrue(Runner.isUnimplemented(e), "the harness must still classify this as unimplemented");
        assertTrue(e.getMessage().startsWith("eventually gave up after 2 attempt(s) 1ms apart; last failure: "),
                e.getMessage());

        // And a give-up on an ordinary mismatch stays an ordinary failure.
        AssertionError plain = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "ListQueues",
                new Call("ListQueues", "{}", b -> ListQueuesRequest.builder().build(),
                        ok(ListQueuesResponse.builder().build(), null)),
                List.of(Clause.eventually(2, 1, Clause.readback(
                        new Call("ListQueues", "{}", b -> ListQueuesRequest.builder().build(),
                                ok(ListQueuesResponse.builder().build(), null)),
                        Check.nonEmpty("$.QueueUrls"))))));
        assertFalse(plain instanceof UnimplementedFailure);
        assertFalse(Runner.isUnimplemented(plain));
    }

    /**
     * A composed failure message is never sniffed for "501": field 3 is the
     * params JSON, where a queue URL, a run id or a port can contain the string
     * while saying nothing about the status.
     */
    @Test
    void aComposedFailureIsNeverSniffedFor501() {
        AssertionError e = assertThrows(AssertionError.class, () -> GROUP.runTest(ctx(), "ListQueues",
                new Call("ListQueues", "{}",
                        b -> ListQueuesRequest.builder().queueNamePrefix("port-501-not-implemented").build(),
                        ok(ListQueuesResponse.builder().queueUrls("http://127.0.0.1:4501/q").build(), null)),
                List.of(Clause.responseField(Check.equalTo("$.QueueUrls[0]", "http://elsewhere/q")))));

        assertTrue(e.getMessage().contains("501"), "the fixture must actually contain the substring");
        assertFalse(Runner.isUnimplemented(e),
                "an assertion failure must never be reported as unimplemented");
    }

    // ── Helpers ──────────────────────────────────────────────────────────────

    private static ContextBag bag(TestContext t) {
        ContextBag b = t.get("scenario_context");
        assertNotNull(b, "the group's context bag");
        return b;
    }

    private static void assertHolds(Object response, Check check) {
        GROUP.runTest(ctx(), "Probe", probe(response), List.of(Clause.responseField(check)));
    }

    private static void assertFails(Object response, Check check) {
        List<Clause> clauses = new ArrayList<>(List.of(Clause.responseField(check)));
        assertThrows(AssertionError.class,
                () -> GROUP.runTest(ctx(), "Probe", probe(response), clauses),
                "check " + check.kind().label() + " at " + check.path() + " should not hold");
    }

    private static Call probe(Object response) {
        return new Call("ListQueues", "{}", b -> ListQueuesRequest.builder().build(), ok(response, null));
    }
}
