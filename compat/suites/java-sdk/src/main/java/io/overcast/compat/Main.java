package io.overcast.compat;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.groups.*;
import io.overcast.compat.harness.TestFn;
import io.overcast.compat.harness.TestGroup;
import io.overcast.compat.harness.Runner;
import io.overcast.compat.harness.InteractiveRunner;
import io.overcast.compat.registry.Registry;
import io.overcast.compat.registry.ScenarioBackend;

import java.util.*;

/**
 * Entry point for the Overcast Java SDK v2 compatibility suite.
 *
 * <p>Reads configuration from environment variables, wires all service groups,
 * loads the shared {@code registry.json}, and runs the suite via
 * {@link Runner#runSuite} which emits NDJSON to {@code stdout}.
 *
 * <h2>Environment variables</h2>
 * <table>
 *   <tr><td>{@code OVERCAST_ENDPOINT}</td>         <td>Overcast base URL (default: http://127.0.0.1:4566)</td></tr>
 *   <tr><td>{@code OVERCAST_DEFAULT_REGION}</td>            <td>AWS region (default: us-east-1)</td></tr>
 *   <tr><td>{@code OVERCAST_COMPAT_RUN_ID}</td>     <td>Unique run ID; all resources must be prefixed
 *                                                   with this value so the orphan sweep can find them</td></tr>
 *   <tr><td>{@code OVERCAST_COMPAT_SKIP_DOCKER}</td><td>Set to "1" to skip tests that require Docker</td></tr>
 *   <tr><td>{@code OVERCAST_COMPAT_GROUPS}</td>     <td>Comma-separated group names to run (default: all)</td></tr>
 *   <tr><td>{@code OVERCAST_COMPAT_SERVICE}</td>    <td>AWS service name to run (default: all)</td></tr>
 *   <tr><td>{@code OVERCAST_COMPAT_TESTS}</td>      <td>Comma-separated test names to run (default: all)</td></tr>
 *   <tr><td>{@code OVERCAST_REGISTRY_PATH}</td>     <td>Override path to registry.json</td></tr>
 * </table>
 */
public final class Main {

    private static final String SUITE = "java-sdk";

    public static void main(String[] args) {
        // Kinesis uses CBOR by default in the Java SDK v2; Overcast only supports JSON.
        System.setProperty("aws.cborEnabled", "false");
        // 127.0.0.1, not localhost: on a dual-stack host "localhost" resolves
        // to ::1 first while the container publishes IPv4 only, so every new
        // connection pays a ~2s IPv6-then-IPv4 fallback.
        String endpoint   = env("OVERCAST_ENDPOINT",  "http://127.0.0.1:4566");
        boolean skipDocker = "1".equals(System.getenv("OVERCAST_COMPAT_SKIP_DOCKER"));

        AwsClients clients = new AwsClients(endpoint, env("OVERCAST_DEFAULT_REGION", "us-east-1"));

        // ── Collect impls / setups / teardowns from all service groups ─────────
        // The impls go through mergeImpls rather than putAll: a key two service
        // groups both register would otherwise lose one implementation with
        // nothing said about it.
        List<Registry.ImplSource> implSources = new ArrayList<>();
        Map<String, TestFn> setups    = new LinkedHashMap<>();
        Map<String, TestFn> teardowns = new LinkedHashMap<>();

        try {
            for (ServiceGroup sg : serviceGroups(clients)) {
                implSources.add(new Registry.ImplSource(sg.sourceName(), sg.impls()));
                mergeHooks(setups, sg.setups(), "setup", sg.sourceName());
                mergeHooks(teardowns, sg.teardowns(), "teardown", sg.sourceName());
            }
        } catch (IllegalStateException e) {
            System.err.println(e.getMessage());
            System.exit(1);
            return;
        }

        // The generated groups resolve through the ScenarioBackend hook rather
        // than through the impl map, which is the loader's designed extension
        // point for them. Their setup and teardown hooks are ordinary entries in
        // the two maps below — the hook resolves tests only — and cannot collide
        // with a hand-written group's, whose names never contain "-gen-".
        List<ServiceGroup> generated = ScenariosGen.all(clients);
        Map<String, TestFn> generatedImpls;
        try {
            generatedImpls = generatedImpls(generated);
        } catch (IllegalStateException e) {
            System.err.println(e.getMessage());
            System.exit(1);
            return;
        }
        try {
            for (ServiceGroup sg : generated) {
                mergeHooks(setups, sg.setups(), "setup", sg.sourceName());
                mergeHooks(teardowns, sg.teardowns(), "teardown", sg.sourceName());
            }
        } catch (IllegalStateException e) {
            System.err.println(e.getMessage());
            System.exit(1);
            return;
        }
        ScenarioBackend backend = (group, test) ->
                generatedImpls.get(Registry.qualifiedKey(group.name(), test.name()));

        Map<String, TestFn> impls;
        try {
            impls = Registry.mergeImpls(implSources, SUITE);
        } catch (IllegalStateException e) {
            // Duplicate registrations — see Registry#mergeImpls. Aborting is
            // the point: the discarded implementation never runs, and its test
            // reports the surviving group's result under its own name.
            System.err.println(e.getMessage());
            System.exit(1);
            return;
        }

        // ── Build capabilities set ─────────────────────────────────────────────
        Set<String> capabilities = new HashSet<>();
        if (!skipDocker) {
            capabilities.add("docker");
        }

        // ── Load registry and build groups ─────────────────────────────────────
        List<TestGroup> allGroups;
        try {
            allGroups = Registry.buildGroups(SUITE, impls, setups, teardowns, capabilities, backend);
        } catch (IllegalStateException e) {
            // Unusable impl registrations — see Registry#validateImpls. Aborting
            // is the point: binding a test to another group's implementation
            // would report a result for a test that never ran.
            System.err.println(e.getMessage());
            System.exit(1);
            return;
        } catch (Exception e) {
            System.err.println("[java-sdk] failed to load registry: " + e.getMessage());
            System.exit(1);
            return;
        }

        // ── Apply filters ──────────────────────────────────────────────────────
        Set<String> filterServices = splitFilter(System.getenv("OVERCAST_COMPAT_SERVICE"));
        Set<String> filterGroups   = splitFilter(System.getenv("OVERCAST_COMPAT_GROUPS"));
        Set<String> filterTests    = splitFilter(System.getenv("OVERCAST_COMPAT_TESTS"));

        List<TestGroup> groups = allGroups;
        if (!filterServices.isEmpty()) {
            groups = groups.stream()
                    .filter(g -> filterServices.contains(g.service()))
                    .toList();
        }
        if (!filterGroups.isEmpty()) {
            groups = groups.stream()
                    .filter(g -> filterGroups.contains(g.name()))
                    .toList();
        }
        if (!filterTests.isEmpty()) {
            groups = groups.stream()
                    .map(g -> {
                        var tests = g.tests().stream()
                                .filter(tc -> filterTests.contains(tc.name()))
                                .toList();
                        return tests.isEmpty() ? null
                                : new TestGroup(g.suite(), g.service(), g.name(), tests,
                                                g.setup(), g.teardown(), g.parallel());
                    })
                    .filter(Objects::nonNull)
                    .toList();
        }

        // ── Run ────────────────────────────────────────────────────────────────
        if ("1".equals(System.getenv("OVERCAST_COMPAT_INTERACTIVE"))) {
            InteractiveRunner.run(SUITE, endpoint, allGroups);
        } else {
            Runner.runSuite(SUITE, endpoint, groups);
        }
    }

    /**
     * Every service group in the suite, in registration order.
     *
     * <p>Package-private and separate from {@link #main} so tests can resolve
     * the suite's real impl keys against the real {@code registry.json} without
     * starting a run.
     */
    static List<ServiceGroup> serviceGroups(AwsClients clients) {
        return List.of(
                new S3Group(clients),
                new SqsGroup(clients),
                new DynamoDbGroup(clients),
                new SnsGroup(clients),
                new LambdaGroup(clients),
                new StsGroup(clients),
                new KmsGroup(clients),
                new SecretsManagerGroup(clients),
                new SsmGroup(clients),
                new IamGroup(clients),
                new SesGroup(clients),
                new EventBridgeGroup(clients),
                new CloudFormationGroup(clients),
                new Ec2Group(clients),
                new EcsGroup(clients),
                new CognitoGroup(clients),
                new AppSyncGroup(clients),
                new ApiGatewayGroup(clients),
                new CloudFrontGroup(clients),
                new RdsGroup(clients),
                new StepFunctionsGroup(clients),
                new PipesGroup(clients),
                new WafGroup(clients),
                new ShieldGroup(clients),
                new ElastiCacheGroup(clients),
                new EfsGroup(clients));
    }

    /**
     * Flattens the generated service groups' impl maps, refusing a key two of
     * them both register.
     *
     * <p>{@code Registry.validateImpls} does not see these — the backend resolves
     * them lazily, by group and test — so the duplicate check is the one guard
     * they get, and it is the same one {@code mergeImpls} gives the hand-written
     * half.
     *
     * @throws IllegalStateException if any key is registered more than once.
     */
    static Map<String, TestFn> generatedImpls(List<ServiceGroup> generated) {
        List<Registry.ImplSource> sources = new ArrayList<>();
        for (ServiceGroup sg : generated) {
            sources.add(new Registry.ImplSource(sg.sourceName(), sg.impls()));
        }
        return Registry.mergeImpls(sources, SUITE);
    }

    /**
     * Merges one service group's setup or teardown hooks into the suite's map,
     * refusing a group name two of them both register.
     *
     * <p>{@code putAll} would keep the last registration silently, and the
     * group whose hook was dropped would run without its setup — reporting a
     * cascade of failures naming its tests rather than the collision that
     * caused them. {@link Registry#mergeImpls} already refuses the same thing
     * for test implementations; these two maps are what it does not see.
     *
     * @throws IllegalStateException if {@code group} is already registered.
     */
    static void mergeHooks(Map<String, TestFn> into, Map<String, TestFn> from,
                           String phase, String source) {
        from.forEach((group, fn) -> {
            if (into.putIfAbsent(group, fn) != null) {
                throw new IllegalStateException(String.format(
                        "[%s] duplicate %s registration for group \"%s\" (%s); one of them would never run",
                        SUITE, phase, group, source));
            }
        });
    }

    // ── Helpers ────────────────────────────────────────────────────────────────

    private static String env(String name, String defaultValue) {
        String v = System.getenv(name);
        return (v != null && !v.isBlank()) ? v : defaultValue;
    }

    private static Set<String> splitFilter(String value) {
        if (value == null || value.isBlank()) return Set.of();
        Set<String> set = new HashSet<>();
        for (String s : value.split(",")) {
            String t = s.trim();
            if (!t.isEmpty()) set.add(t);
        }
        return Set.copyOf(set);
    }
}
