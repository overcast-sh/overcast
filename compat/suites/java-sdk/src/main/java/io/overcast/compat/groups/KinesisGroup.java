package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.core.SdkBytes;
import software.amazon.awssdk.services.kinesis.KinesisClient;
import software.amazon.awssdk.services.kinesis.model.*;

import java.nio.charset.StandardCharsets;
import java.util.Map;

/**
 * Kinesis compatibility test group.
 *
 * <p>Groups: kinesis-records. kinesis-streams and kinesis-shards resolve through
 * their authored scenarios (compat/model/authored/kinesis-streams.json and
 * compat/model/authored/kinesis-shards.json).
 */
public final class KinesisGroup implements ServiceGroup {

    private final AwsClients clients;

    public KinesisGroup(AwsClients clients) {
        this.clients = clients;
    }

    private KinesisClient kinesis() { return clients.kinesis(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("kinesis-records:PutRecord",             this::putRecord),
                Map.entry("kinesis-records:PutRecords",            this::putRecords),
                Map.entry("kinesis-records:GetRecords",            this::getRecords),
                Map.entry("kinesis-records:GetShardIterator",      this::getShardIterator)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.ofEntries(
                Map.entry("kinesis-records", this::setupRecords)
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.ofEntries(
                Map.entry("kinesis-records", ctx -> deleteStreamSilently(ctx.getString("kinesisRecordsStream")))
        );
    }

    // ── kinesis-records ───────────────────────────────────────────────────────

    private void setupRecords(TestContext ctx) throws Exception {
        String name = "compat-rec-" + ctx.runId();
        kinesis().createStream(r -> r.streamName(name).shardCount(1));
        waitActive(name);
        ctx.set("kinesisRecordsStream", name);
    }

    private void putRecord(TestContext ctx) throws Exception {
        String name = ctx.getString("kinesisRecordsStream");
        var resp = kinesis().putRecord(r -> r
                .streamName(name)
                .partitionKey("pk1")
                .data(SdkBytes.fromString("hello", StandardCharsets.UTF_8)));
        Assertions.assertNotBlank(resp.sequenceNumber(), "PutRecord: sequenceNumber is blank");
    }

    private void putRecords(TestContext ctx) throws Exception {
        String name = ctx.getString("kinesisRecordsStream");
        var entry = PutRecordsRequestEntry.builder()
                .partitionKey("pk2")
                .data(SdkBytes.fromString("world", StandardCharsets.UTF_8))
                .build();
        var resp = kinesis().putRecords(r -> r.streamName(name).records(entry));
        Assertions.assertEquals(0, resp.failedRecordCount(), "PutRecords: some records failed");
    }

    private void getShardIterator(TestContext ctx) throws Exception {
        String name = ctx.getString("kinesisRecordsStream");
        String shardId = firstShardId(name);
        var resp = kinesis().getShardIterator(r -> r
                .streamName(name)
                .shardId(shardId)
                .shardIteratorType(ShardIteratorType.TRIM_HORIZON));
        Assertions.assertNotBlank(resp.shardIterator(), "GetShardIterator: iterator is blank");
        ctx.set("shardIterator", resp.shardIterator());
    }

    private void getRecords(TestContext ctx) throws Exception {
        String savedIterator = ctx.getString("shardIterator");
        final String iterator;
        if (savedIterator == null) {
            // Obtain iterator on-demand if the previous test didn't set it.
            String name    = ctx.getString("kinesisRecordsStream");
            String shardId = firstShardId(name);
            iterator = kinesis().getShardIterator(r -> r
                    .streamName(name).shardId(shardId)
                    .shardIteratorType(ShardIteratorType.TRIM_HORIZON))
                    .shardIterator();
        } else {
            iterator = savedIterator;
        }
        var resp = kinesis().getRecords(r -> r.shardIterator(iterator).limit(10));
        Assertions.assertNotEmpty(resp.records(), "GetRecords: no records returned from TRIM_HORIZON after PutRecord(s)");
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    /** Block until the stream reaches ACTIVE status (polls up to 30 s). */
    private void waitActive(String name) throws InterruptedException {
        for (int i = 0; i < 30; i++) {
            var status = kinesis().describeStream(r -> r.streamName(name))
                    .streamDescription().streamStatus();
            if (status == StreamStatus.ACTIVE) return;
            Thread.sleep(1_000);
        }
    }

    private String firstShardId(String streamName) {
        return kinesis().listShards(r -> r.streamName(streamName)).shards().get(0).shardId();
    }

    private void deleteStreamSilently(String name) {
        if (name == null) return;
        try { kinesis().deleteStream(r -> r.streamName(name)); } catch (Exception ignored) {}
    }
}
