package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.services.eventbridge.EventBridgeClient;
import software.amazon.awssdk.services.eventbridge.model.*;
import software.amazon.awssdk.services.sqs.SqsClient;
import software.amazon.awssdk.services.sqs.model.Message;
import software.amazon.awssdk.services.sqs.model.QueueAttributeName;

import java.util.List;
import java.util.Map;

/**
 * EventBridge compatibility test group.
 *
 * <p>Groups: eventbridge-buses, eventbridge-events,
 * eventbridge-target-fanout, eventbridge-patterns.
 */
public final class EventBridgeGroup implements ServiceGroup {

    private final AwsClients clients;

    public EventBridgeGroup(AwsClients clients) {
        this.clients = clients;
    }

    private EventBridgeClient eb() { return clients.eventBridge(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("eventbridge-buses:CreateEventBus",                        this::createEventBus),
                Map.entry("eventbridge-buses:DescribeEventBus",                      this::describeEventBus),
                Map.entry("eventbridge-buses:ListEventBuses",                        this::listEventBuses),
                Map.entry("eventbridge-buses:TagEventBus",                           this::tagEventBus),
                Map.entry("eventbridge-buses:ListEventBridgeTagsForResource",        this::listTagsForEventBus),
                Map.entry("eventbridge-buses:DeleteEventBus",                        this::deleteEventBus),
                Map.entry("eventbridge-events:PutEvents",                            this::putEvents),
                Map.entry("eventbridge-events:PutEventsBatch",                       this::putEventsCustomBus),
                Map.entry("eventbridge-target-fanout:PutFanoutTargets",              this::putFanoutTargets),
                Map.entry("eventbridge-target-fanout:PutEventsToQueueTarget",        this::putEventsToQueueTarget),
                Map.entry("eventbridge-target-fanout:PutEventsWithInputTransformer", this::putEventsWithInputTransformer),
                Map.entry("eventbridge-patterns:TestEventPattern",                   this::testEventPattern),
                Map.entry("eventbridge-patterns:TestEventPatternNoMatch",            this::testEventPatternNoMatch)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.ofEntries(
                Map.entry("eventbridge-buses",  this::setupBuses),
                Map.entry("eventbridge-events", this::setupEventsGroup),
                Map.entry("eventbridge-target-fanout", this::setupFanout)
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.ofEntries(
                Map.entry("eventbridge-buses",  ctx -> deleteBusSilently(ctx.getString("eventBusName"))),
                Map.entry("eventbridge-events", ctx -> deleteBusSilently(ctx.getString("eventsBusName"))),
                Map.entry("eventbridge-target-fanout", this::teardownFanout)
        );
    }

    // ── eventbridge-buses ─────────────────────────────────────────────────────

    private void setupBuses(TestContext ctx) {
        ctx.set("eventBusName", "compat-bus-" + ctx.runId());
    }

    private void createEventBus(TestContext ctx) throws Exception {
        String name = ctx.getString("eventBusName");
        var resp = eb().createEventBus(r -> r.name(name));
        Assertions.assertNotBlank(resp.eventBusArn(), "CreateEventBus: eventBusArn is blank");
    }

    private void describeEventBus(TestContext ctx) throws Exception {
        String name = ctx.getString("eventBusName");
        var resp = eb().describeEventBus(r -> r.name(name));
        Assertions.assertEquals(name, resp.name(), "DescribeEventBus: name mismatch");
    }

    private void listEventBuses(TestContext ctx) throws Exception {
        String name = ctx.getString("eventBusName");
        var resp = eb().listEventBuses(r -> r.limit(100));
        boolean found = resp.eventBuses().stream().anyMatch(b -> b.name().equals(name));
        Assertions.assertTrue(found, "ListEventBuses: created bus not found");
    }

    private void tagEventBus(TestContext ctx) throws Exception {
        var resp = eb().describeEventBus(r -> r.name(ctx.getString("eventBusName")));
        eb().tagResource(r -> r.resourceARN(resp.arn()).tags(
                Tag.builder().key("env").value("compat").build()));
    }

    private void listTagsForEventBus(TestContext ctx) throws Exception {
        var resp = eb().describeEventBus(r -> r.name(ctx.getString("eventBusName")));
        var tags = eb().listTagsForResource(r -> r.resourceARN(resp.arn()));
        boolean found = tags.tags().stream().anyMatch(t -> "env".equals(t.key()));
        Assertions.assertTrue(found, "ListTagsForEventBus: expected 'env' tag");
    }

    private void deleteEventBus(TestContext ctx) throws Exception {
        String name = ctx.getString("eventBusName");
        eb().deleteEventBus(r -> r.name(name));
        ctx.set("eventBusName", null);
    }

    // ── eventbridge-events ────────────────────────────────────────────────────

    private void setupEventsGroup(TestContext ctx) throws Exception {
        String bus = "compat-ev-" + ctx.runId();
        eb().createEventBus(r -> r.name(bus));
        ctx.set("eventsBusName", bus);
    }

    private void putEvents(TestContext ctx) throws Exception {
        var entry = PutEventsRequestEntry.builder()
                .eventBusName("default")
                .source("compat.test")
                .detailType("CompatTest")
                .detail("{\"key\":\"value\"}")
                .build();
        var resp = eb().putEvents(r -> r.entries(entry));
        Assertions.assertEquals(0, resp.failedEntryCount(), "PutEvents: some events failed");
    }

    private void putEventsCustomBus(TestContext ctx) throws Exception {
        String bus = ctx.getString("eventsBusName");
        var entry = PutEventsRequestEntry.builder()
                .eventBusName(bus)
                .source("compat.test")
                .detailType("CompatCustomBus")
                .detail("{\"bus\":\"custom\"}")
                .build();
        var resp = eb().putEvents(r -> r.entries(entry));
        Assertions.assertEquals(0, resp.failedEntryCount(), "PutEventsCustomBus: some events failed");
    }


    // -- eventbridge-target-fanout ---------------------------------------------
    //
    // Target fan-out: an event put on a bus reaches the rule's targets, with the
    // target's input transformation applied first. The group provisions its own
    // queues and rule so it never races the other EventBridge groups (#388).

    private SqsClient sqs() { return clients.sqs(); }

    private void setupFanout(TestContext ctx) throws Exception {
        for (String[] pair : new String[][] {
                {"fanoutPlain", "oc-" + ctx.runId() + "-fanout-plain"},
                {"fanoutShaped", "oc-" + ctx.runId() + "-fanout-shaped"},
        }) {
            String key = pair[0];
            String name = pair[1];
            String url = sqs().createQueue(r -> r.queueName(name)).queueUrl();
            var attrs = sqs().getQueueAttributes(r -> r.queueUrl(url)
                    .attributeNames(QueueAttributeName.QUEUE_ARN));
            ctx.set(key + "Url", url);
            ctx.set(key + "Arn", attrs.attributes().get(QueueAttributeName.QUEUE_ARN));
        }
        String rule = "oc-" + ctx.runId() + "-fanout";
        String source = fanoutSource(ctx);
        eb().putRule(r -> r.name(rule)
                .eventPattern("{\"source\":[\"" + source + "\"]}")
                .state(RuleState.ENABLED));
        ctx.set("fanoutRule", rule);
    }

    private void teardownFanout(TestContext ctx) {
        String rule = ctx.getString("fanoutRule");
        if (rule != null) {
            try { eb().removeTargets(r -> r.rule(rule).ids("plain", "shaped")); } catch (Exception ignored) {}
            try { eb().deleteRule(r -> r.name(rule)); } catch (Exception ignored) {}
        }
        for (String key : new String[] {"fanoutPlainUrl", "fanoutShapedUrl"}) {
            String url = ctx.getString(key);
            if (url == null) continue;
            try { sqs().deleteQueue(r -> r.queueUrl(url)); } catch (Exception ignored) {}
        }
    }

    private void putFanoutTargets(TestContext ctx) throws Exception {
        String rule = ctx.getString("fanoutRule");
        var plain = Target.builder()
                .id("plain")
                .arn(ctx.getString("fanoutPlainArn"))
                .build();
        var shaped = Target.builder()
                .id("shaped")
                .arn(ctx.getString("fanoutShapedArn"))
                .inputTransformer(InputTransformer.builder()
                        .inputPathsMap(Map.of("order", "$.detail.orderId"))
                        .inputTemplate("{\"order\":\"<order>\"}")
                        .build())
                .build();
        var resp = eb().putTargets(r -> r.rule(rule).targets(plain, shaped));
        Assertions.assertEquals(0, resp.failedEntryCount(), "PutFanoutTargets: some targets were rejected");

        var listed = eb().listTargetsByRule(r -> r.rule(rule));
        Assertions.assertEquals(2, listed.targets().size(), "PutFanoutTargets: expected 2 targets");
        Target stored = listed.targets().stream()
                .filter(t -> "shaped".equals(t.id())).findFirst().orElse(null);
        Assertions.assertTrue(
                stored != null && stored.inputTransformer() != null
                        && stored.inputTransformer().inputTemplate() != null,
                "PutFanoutTargets: InputTransformer did not round-trip");
    }

    private void putEventsToQueueTarget(TestContext ctx) throws Exception {
        putFanoutEvent(ctx, "queue-target");
        String body = awaitFanoutMessage(ctx.getString("fanoutPlainUrl"), "queue-target");
        Assertions.assertTrue(
                body.contains(fanoutSource(ctx)) && body.contains("queue-target"),
                "PutEventsToQueueTarget: delivered body missing the event envelope: " + body);
    }

    private void putEventsWithInputTransformer(TestContext ctx) throws Exception {
        putFanoutEvent(ctx, "transformed");
        String body = awaitFanoutMessage(ctx.getString("fanoutShapedUrl"), "transformed");
        Assertions.assertEquals("{\"order\":\"transformed\"}", body,
                "PutEventsWithInputTransformer: expected the rendered template");
    }

    private String fanoutSource(TestContext ctx) {
        return "oc.fanout." + ctx.runId();
    }

    private void putFanoutEvent(TestContext ctx, String orderId) {
        var entry = PutEventsRequestEntry.builder()
                .source(fanoutSource(ctx))
                .detailType("FanoutTest")
                .detail("{\"orderId\":\"" + orderId + "\"}")
                .build();
        var resp = eb().putEvents(r -> r.entries(entry));
        Assertions.assertEquals(0, resp.failedEntryCount(), "PutEvents: some events failed");
    }

    /**
     * Polls a target queue for a delivered message containing {@code want}.
     *
     * <p>Both targets hang off one rule, so every event reaches both queues and
     * a queue may hold an earlier test's message; matching on {@code want} (and
     * consuming what does not match) keeps the tests order-independent.
     * Delivery is asynchronous, so a bounded poll replaces a fixed sleep.
     */
    private String awaitFanoutMessage(String queueUrl, String want) throws Exception {
        for (int attempt = 0; attempt < 15; attempt++) {
            var resp = sqs().receiveMessage(r -> r.queueUrl(queueUrl)
                    .maxNumberOfMessages(10).waitTimeSeconds(1));
            List<Message> messages = resp.messages();
            String matched = null;
            for (Message message : messages) {
                try {
                    sqs().deleteMessage(r -> r.queueUrl(queueUrl).receiptHandle(message.receiptHandle()));
                } catch (Exception ignored) {}
                if (message.body().contains(want)) {
                    matched = message.body();
                }
            }
            if (matched != null) {
                return matched;
            }
            Thread.sleep(100);
        }
        throw new AssertionError("no message containing " + want + " delivered to the target queue");
    }

    // -- eventbridge-patterns ---------------------------------------------------
    //
    // TestEventPattern is stateless — it evaluates a pattern against an event
    // without touching a bus or rule, so the group needs no setup/teardown.

    private void testEventPattern(TestContext ctx) throws Exception {
        var resp = eb().testEventPattern(r -> r
                .eventPattern(patternsMatchingPattern())
                .event(patternsEvent(ctx)));
        Assertions.assertNotNull(resp.result(), "TestEventPattern: Result is null");
        Assertions.assertTrue(resp.result(), "TestEventPattern: expected Result true for a matching pattern");
    }

    private void testEventPatternNoMatch(TestContext ctx) throws Exception {
        var resp = eb().testEventPattern(r -> r
                .eventPattern(patternsNonMatchingPattern())
                .event(patternsEvent(ctx)));
        Assertions.assertNotNull(resp.result(), "TestEventPatternNoMatch: Result is null");
        Assertions.assertFalse(resp.result(), "TestEventPatternNoMatch: expected Result false for a non-matching pattern");
    }

    private String patternsEvent(TestContext ctx) {
        return "{\"id\":\"" + ctx.runId() + "\",\"detail-type\":\"order.created\","
                + "\"source\":\"compat.eventbridge-patterns\",\"account\":\"000000000000\","
                + "\"time\":\"2026-01-01T00:00:00Z\",\"region\":\"" + ctx.region() + "\","
                + "\"resources\":[],\"detail\":{\"orderId\":\"1\"}}";
    }

    private String patternsMatchingPattern() {
        return "{\"source\":[\"compat.eventbridge-patterns\"],\"detail-type\":[\"order.created\"]}";
    }

    private String patternsNonMatchingPattern() {
        return "{\"source\":[\"compat.eventbridge-patterns.other\"]}";
    }

    // -- Helpers ---------------------------------------------------------------

    private void deleteBusSilently(String name) {
        if (name == null) return;
        try { eb().deleteEventBus(r -> r.name(name)); } catch (Exception ignored) {}
    }
}
