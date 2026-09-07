package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.services.sqs.SqsClient;
import software.amazon.awssdk.services.sqs.model.*;

import java.util.List;
import java.util.Map;

/**
 * SQS compatibility test group.
 *
 * <p>Groups: sqs-messages, sqs-dlq, sqs-fifo. sqs-queues is a ported group, resolved
 * from compat/model/authored/sqs-queues.json by the scenario backend (#1903).
 */
public final class SqsGroup implements ServiceGroup {

    private final AwsClients clients;

    public SqsGroup(AwsClients clients) {
        this.clients = clients;
    }

    private SqsClient sqs() { return clients.sqs(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("sqs-messages:SendMessage",             this::sendMessage),
                Map.entry("sqs-messages:SendMessageBatch",        this::sendMessageBatch),
                Map.entry("sqs-messages:ReceiveMessage",          this::receiveMessage),
                Map.entry("sqs-messages:DeleteMessage",           this::deleteMessage),
                Map.entry("sqs-messages:ChangeMessageVisibility", this::changeMessageVisibility),
                Map.entry("sqs-messages:DeleteMessageBatch",      this::deleteMessageBatch),
                Map.entry("sqs-messages:PurgeQueue",              this::purgeQueue),
                Map.entry("sqs-dlq:CreateDLQ",                    this::createDlq),
                Map.entry("sqs-dlq:SetRedrivePolicy",             this::setRedrivePolicy),
                Map.entry("sqs-dlq:GetRedrivePolicy",             this::getRedrivePolicy),
                Map.entry("sqs-fifo:CreateFifoQueue",             this::createFifoQueue),
                Map.entry("sqs-fifo:SendFifoMessage",             this::sendFifoMessage),
                Map.entry("sqs-fifo:ReceiveFifoMessage",          this::receiveFifoMessage)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.ofEntries(
                Map.entry("sqs-messages", this::setupMessages),
                Map.entry("sqs-dlq",      this::setupDlq),
                Map.entry("sqs-fifo",     this::setupFifo)
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.ofEntries(
                Map.entry("sqs-messages", ctx -> deleteQueueSilently(ctx.getString("sqsMsgQueueUrl"))),
                Map.entry("sqs-dlq",      this::teardownDlq),
                Map.entry("sqs-fifo",     ctx -> deleteQueueSilently(ctx.getString("sqsFifoUrl")))
        );
    }

    // ── sqs-messages ──────────────────────────────────────────────────────────

    private void setupMessages(TestContext ctx) throws Exception {
        String name = ctx.runId() + "-sqsmsg";
        var resp = sqs().createQueue(r -> r.queueName(name));
        ctx.set("sqsMsgQueueUrl", resp.queueUrl());
    }

    private void sendMessage(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsMsgQueueUrl");
        var resp = sqs().sendMessage(r -> r.queueUrl(url).messageBody("hello SQS"));
        Assertions.assertNotBlank(resp.messageId(), "SendMessage: messageId");
    }

    private void sendMessageBatch(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsMsgQueueUrl");
        var resp = sqs().sendMessageBatch(r -> r.queueUrl(url).entries(
                SendMessageBatchRequestEntry.builder().id("m1").messageBody("batch-1").build(),
                SendMessageBatchRequestEntry.builder().id("m2").messageBody("batch-2").build()
        ));
        Assertions.assertEquals(2, resp.successful().size(), "SendMessageBatch: expected 2 successful");
    }

    private void receiveMessage(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsMsgQueueUrl");
        var resp = sqs().receiveMessage(r -> r.queueUrl(url).maxNumberOfMessages(10).waitTimeSeconds(0));
        Assertions.assertNotEmpty(resp.messages(), "ReceiveMessage: no messages received");
        ctx.set("sqsReceiptHandle", resp.messages().get(0).receiptHandle());
    }

    private void deleteMessage(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsMsgQueueUrl");
        String rh  = ctx.getString("sqsReceiptHandle");
        if (rh != null) {
            sqs().deleteMessage(r -> r.queueUrl(url).receiptHandle(rh));
        }
    }

    private void changeMessageVisibility(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsMsgQueueUrl");
        // Send a fresh message to change visibility on.
        sqs().sendMessage(r -> r.queueUrl(url).messageBody("vis-test"));
        var recv = sqs().receiveMessage(r -> r.queueUrl(url).maxNumberOfMessages(1).waitTimeSeconds(1));
        Assertions.assertNotEmpty(recv.messages(), "ChangeMessageVisibility: no message to change visibility for");
        String rh = recv.messages().get(0).receiptHandle();
        sqs().changeMessageVisibility(r -> r.queueUrl(url).receiptHandle(rh).visibilityTimeout(0));
        // With the timeout set to 0 the message must be immediately receivable again.
        var recv2 = sqs().receiveMessage(r -> r.queueUrl(url).maxNumberOfMessages(1).waitTimeSeconds(1));
        Assertions.assertNotEmpty(recv2.messages(),
                "ChangeMessageVisibility: message not re-visible after setting timeout to 0");
    }

    private void deleteMessageBatch(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsMsgQueueUrl");
        sqs().sendMessage(r -> r.queueUrl(url).messageBody("batch-del-1"));
        sqs().sendMessage(r -> r.queueUrl(url).messageBody("batch-del-2"));
        var recv = sqs().receiveMessage(r -> r.queueUrl(url).maxNumberOfMessages(10).waitTimeSeconds(1));
        Assertions.assertNotEmpty(recv.messages(), "DeleteMessageBatch: no messages received to delete");
        var entries = recv.messages().stream()
                .map(m -> DeleteMessageBatchRequestEntry.builder()
                        .id(m.messageId()).receiptHandle(m.receiptHandle()).build())
                .toList();
        var resp = sqs().deleteMessageBatch(r -> r.queueUrl(url).entries(entries));
        Assertions.assertEquals(entries.size(), resp.successful().size(),
                "DeleteMessageBatch: not every entry deleted successfully (failed=" + resp.failed() + ")");
    }

    private void purgeQueue(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsMsgQueueUrl");
        sqs().purgeQueue(r -> r.queueUrl(url));
    }

    // ── sqs-dlq ────────────────────────────────────────────────────────────────

    private void setupDlq(TestContext ctx) throws Exception {
        String dlqName  = ctx.runId() + "-sqsdlq";
        String srcName  = ctx.runId() + "-sqssrc";
        var dlqResp     = sqs().createQueue(r -> r.queueName(dlqName));
        var dlqAttrs    = sqs().getQueueAttributes(r -> r
                .queueUrl(dlqResp.queueUrl())
                .attributeNames(QueueAttributeName.QUEUE_ARN));
        String dlqArn   = dlqAttrs.attributesAsStrings().get("QueueArn");
        var srcResp     = sqs().createQueue(r -> r.queueName(srcName));
        ctx.set("sqsDlqUrl", dlqResp.queueUrl());
        ctx.set("sqsDlqArn", dlqArn);
        ctx.set("sqsSrcUrl", srcResp.queueUrl());
    }

    private void teardownDlq(TestContext ctx) {
        deleteQueueSilently(ctx.getString("sqsDlqUrl"));
        deleteQueueSilently(ctx.getString("sqsSrcUrl"));
    }

    private void createDlq(TestContext ctx) {
        Assertions.assertNotBlank(ctx.getString("sqsDlqUrl"), "sqsDlqUrl");
    }

    private void setRedrivePolicy(TestContext ctx) throws Exception {
        String url    = ctx.getString("sqsSrcUrl");
        String dlqArn = ctx.getString("sqsDlqArn");
        String policy = "{\"deadLetterTargetArn\":\"" + dlqArn + "\",\"maxReceiveCount\":\"3\"}";
        sqs().setQueueAttributes(r -> r.queueUrl(url)
                .attributes(Map.of(QueueAttributeName.REDRIVE_POLICY, policy)));
    }

    private void getRedrivePolicy(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsSrcUrl");
        var resp = sqs().getQueueAttributes(r -> r
                .queueUrl(url).attributeNames(QueueAttributeName.REDRIVE_POLICY));
        Assertions.assertTrue(resp.attributesAsStrings().containsKey("RedrivePolicy"),
                "GetRedrivePolicy: RedrivePolicy attribute not present");
    }

    // ── sqs-fifo ───────────────────────────────────────────────────────────────

    private void setupFifo(TestContext ctx) throws Exception {
        String name = ctx.runId() + "-sqsfifo.fifo";
        var resp = sqs().createQueue(r -> r.queueName(name)
                .attributes(Map.of(QueueAttributeName.FIFO_QUEUE, "true")));
        ctx.set("sqsFifoUrl", resp.queueUrl());
    }

    private void createFifoQueue(TestContext ctx) {
        Assertions.assertNotBlank(ctx.getString("sqsFifoUrl"), "sqsFifoUrl");
    }

    private void sendFifoMessage(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsFifoUrl");
        var resp = sqs().sendMessage(r -> r.queueUrl(url)
                .messageBody("fifo-msg")
                .messageGroupId("grp1")
                .messageDeduplicationId("dedup1"));
        Assertions.assertNotBlank(resp.messageId(), "SendFifoMessage: messageId");
    }

    private void receiveFifoMessage(TestContext ctx) throws Exception {
        String url = ctx.getString("sqsFifoUrl");
        var resp = sqs().receiveMessage(r -> r.queueUrl(url)
                .maxNumberOfMessages(1).waitTimeSeconds(2));
        Assertions.assertNotEmpty(resp.messages(), "ReceiveFifoMessage: no messages received");
        Assertions.assertEquals("fifo-msg", resp.messages().get(0).body(),
                "ReceiveFifoMessage: body mismatch");
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    private void deleteQueueSilently(String url) {
        if (url == null) return;
        try { sqs().deleteQueue(r -> r.queueUrl(url)); } catch (Exception ignored) {}
    }
}
