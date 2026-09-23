package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.services.cloudwatchlogs.CloudWatchLogsClient;
import software.amazon.awssdk.services.cloudwatchlogs.model.*;

import java.util.Map;

/**
 * CloudWatch Logs compatibility test group.
 *
 * <p>Groups: logs-events. logs-groups is a ported group, resolved from
 * compat/model/authored/logs-groups.json by the scenario backend (#1116).
 */
public final class CloudWatchLogsGroup implements ServiceGroup {

    private final AwsClients clients;

    public CloudWatchLogsGroup(AwsClients clients) {
        this.clients = clients;
    }

    private CloudWatchLogsClient logs() { return clients.cloudWatchLogs(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("logs-events:DescribeLogStreams",    this::describeLogStreams),
                Map.entry("logs-events:PutLogEvents",          this::putLogEvents),
                Map.entry("logs-events:GetLogEvents",          this::getLogEvents),
                Map.entry("logs-events:FilterLogEvents",       this::filterLogEvents),
                Map.entry("logs-events:DeleteLogStream",       this::deleteLogStream)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.ofEntries(
                Map.entry("logs-events", this::setupEvents)
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.ofEntries(
                Map.entry("logs-events", ctx -> deleteGroupSilently(ctx.getString("logsEventsGroup")))
        );
    }

    // ── logs-events ───────────────────────────────────────────────────────────

    private void setupEvents(TestContext ctx) throws Exception {
        String grp    = "/compat/" + ctx.runId() + "/events";
        String stream = "stream-events";
        logs().createLogGroup(r -> r.logGroupName(grp));
        logs().createLogStream(r -> r.logGroupName(grp).logStreamName(stream));
        ctx.set("logsEventsGroup",  grp);
        ctx.set("logsEventsStream", stream);
    }

    private void putLogEvents(TestContext ctx) throws Exception {
        String grp    = ctx.getString("logsEventsGroup");
        String stream = ctx.getString("logsEventsStream");
        var event = InputLogEvent.builder()
                .timestamp(System.currentTimeMillis())
                .message("compat test event")
                .build();
        var resp = logs().putLogEvents(r -> r
                .logGroupName(grp)
                .logStreamName(stream)
                .logEvents(event));
        Assertions.assertNotNull(resp, "PutLogEvents: response is null");
    }

    private void getLogEvents(TestContext ctx) throws Exception {
        String grp    = ctx.getString("logsEventsGroup");
        String stream = ctx.getString("logsEventsStream");
        var resp = logs().getLogEvents(r -> r
                .logGroupName(grp).logStreamName(stream).limit(10));
        Assertions.assertNotEmpty(resp.events(), "GetLogEvents: no events returned after PutLogEvents");
    }

    private void filterLogEvents(TestContext ctx) throws Exception {
        String grp = ctx.getString("logsEventsGroup");
        var resp = logs().filterLogEvents(r -> r
                .logGroupName(grp).filterPattern("compat"));
        Assertions.assertNotEmpty(resp.events(), "FilterLogEvents: no event matched the 'compat' pattern");
    }

    private void describeLogStreams(TestContext ctx) throws Exception {
        String grp    = ctx.getString("logsEventsGroup");
        String stream = ctx.getString("logsEventsStream");
        Assertions.assertNotNull(grp, "DescribeLogStreams: no log group from setup");
        Assertions.assertNotNull(stream, "DescribeLogStreams: no log stream from setup");
        var resp = logs().describeLogStreams(r -> r.logGroupName(grp).logStreamNamePrefix(stream));
        boolean found = resp.logStreams().stream().anyMatch(s -> s.logStreamName().equals(stream));
        Assertions.assertTrue(found, "DescribeLogStreams: created log stream not found");
    }

    private void deleteLogStream(TestContext ctx) throws Exception {
        String grp = ctx.getString("logsEventsGroup");
        String stream = ctx.getString("logsEventsStream");
        if (grp == null || stream == null) {
            grp = "/compat/" + ctx.runId() + "/delstream";
            stream = "stream-del";
            final String g = grp;
            final String s = stream;
            logs().createLogGroup(r -> r.logGroupName(g));
            logs().createLogStream(r -> r.logGroupName(g).logStreamName(s));
            ctx.set("logsEventsGroup", grp);
            ctx.set("logsEventsStream", stream);
        }
        final String g = grp;
        final String s = stream;
        logs().deleteLogStream(r -> r.logGroupName(g).logStreamName(s));
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    private void deleteGroupSilently(String name) {
        if (name == null) return;
        try { logs().deleteLogGroup(r -> r.logGroupName(name)); } catch (Exception ignored) {}
    }
}
