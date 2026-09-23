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
 * <p>Groups: kinesis-records, kinesis-shards. kinesis-streams resolves through
 * its authored scenario (compat/model/authored/kinesis-streams.json).
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
                Map.entry("kinesis-shards:ListShards",             this::listShards),
                Map.entry("kinesis-records:PutRecord",             this::putRecord),
                Map.entry("kinesis-records:PutRecords",            this::putRecords),
                Map.entry("kinesis-records:GetRecords",            this::getRecords),
                Map.entry("kinesis-records:GetShardIterator",      this::getShardIterator),
                Map.entry("kinesis-shards:MergeShards",            this::mergeShards),
                Map.entry("kinesis-shards:SplitShard",             this::splitShard)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.ofEntries(
                Map.entry("kinesis-records", this::setupRecords),
                Map.entry("kinesis-shards",  this::setupShards)
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.ofEntries(
                Map.entry("kinesis-records", ctx -> deleteStreamSilently(ctx.getString("kinesisRecordsStream"))),
                Map.entry("kinesis-shards",  ctx -> deleteStreamSilently(ctx.getString("kinesisShardsStream")))
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

    // ── kinesis-shards ────────────────────────────────────────────────────────

    private void setupShards(TestContext ctx) throws Exception {
        String name = "compat-shard-" + ctx.runId();
        kinesis().createStream(r -> r.streamName(name).shardCount(2));
        waitActive(name);
        ctx.set("kinesisShardsStream", name);
    }

    private void listShards(TestContext ctx) throws Exception {
        String name = ctx.getString("kinesisShardsStream");
        if (name == null) name = ctx.getString("kinesisRecordsStream");
        final String streamName = name;
        var resp = kinesis().listShards(r -> r.streamName(streamName));
        Assertions.assertGreaterThanOrEqual(1, resp.shards().size(), "ListShards: expected >= 1 shard");
        ctx.set("shardId", resp.shards().get(0).shardId());
    }

    private void splitShard(TestContext ctx) throws Exception {
        String name    = ctx.getString("kinesisShardsStream");
        var shards     = kinesis().listShards(r -> r.streamName(name)).shards();
        Assertions.assertGreaterThanOrEqual(1, shards.size(), "SplitShard: no shards to split");
        var shard      = shards.get(0);
        // Starting hash key must be in the middle of the shard's hash key range.
        String newStart = midHash(shard.hashKeyRange().startingHashKey(),
                shard.hashKeyRange().endingHashKey());
        kinesis().splitShard(r -> r.streamName(name)
                .shardToSplit(shard.shardId())
                .newStartingHashKey(newStart));
        waitActive(name);
    }

    private void mergeShards(TestContext ctx) throws Exception {
        String name = ctx.getString("kinesisShardsStream");
        var shards   = kinesis().listShards(r -> r.streamName(name)).shards()
                .stream().filter(s -> s.sequenceNumberRange().endingSequenceNumber() == null)
                .sorted((a, b) -> a.hashKeyRange().startingHashKey()
                        .compareTo(b.hashKeyRange().startingHashKey()))
                .toList();
        if (shards.size() < 2) return; // nothing to merge
        kinesis().mergeShards(r -> r.streamName(name)
                .shardToMerge(shards.get(0).shardId())
                .adjacentShardToMerge(shards.get(1).shardId()));
        waitActive(name);
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

    /** Compute the midpoint of two decimal strings for SplitShard. */
    private String midHash(String start, String end) {
        java.math.BigInteger s = new java.math.BigInteger(start);
        java.math.BigInteger e = new java.math.BigInteger(end);
        return s.add(e).divide(java.math.BigInteger.TWO).toString();
    }

    private void deleteStreamSilently(String name) {
        if (name == null) return;
        try { kinesis().deleteStream(r -> r.streamName(name)); } catch (Exception ignored) {}
    }
}
