package io.overcast.compat.scenario;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Assumptions;
import org.junit.jupiter.api.DynamicNode;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

import java.io.File;
import java.io.IOException;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;
import java.util.function.LongSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

/**
 * The shared {@code $now} conformance fixture, {@code compat/model/testdata/now}.
 *
 * <p>{@code $now} is the client's clock when a call is made, in epoch
 * milliseconds, plus an offset ({@code compat/model/README.md} § Values). The
 * emitted source hands this runtime {@code Values.now(unit, offsetMillis)} —
 * never the JSON — so the fixture's {@code invalid} spellings are the
 * generator's to refuse, and this runs the rest: each valid spelling binds to
 * the fixture's value into the {@code Long} a long member is, every invalid
 * argument is refused naming the member, and the clock is read once per call
 * however many {@code $now}s the call holds.
 *
 * <p>Found and required exactly as {@link BlobFixtureTest} finds its fixture.
 */
class NowFixtureTest {

    private static final String REQUIRED_ENV_VAR = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

    /** A clock that moves on by {@code tick} every time it is read. */
    private static LongSupplier ticking(long start, long tick) {
        long[] next = {start};
        return () -> {
            long reading = next[0];
            next[0] += tick;
            return reading;
        };
    }

    private static Binder binder(LongSupplier clock) {
        return new Binder("oc-test", "logs-events", new ContextBag(), clock);
    }

    @TestFactory
    List<DynamicNode> sharedNowFixture() throws IOException {
        File file = fixtureFile();
        if (file == null) {
            if ("1".equals(System.getenv(REQUIRED_ENV_VAR))) {
                fail(REQUIRED_ENV_VAR + "=1 but no compat/model/testdata/now/now.json found above the"
                        + " working directory — this suite's fixture test must run from a full checkout");
            }
            return List.of(DynamicTest.dynamicTest("shared $now fixture", () -> Assumptions.abort(
                    "no compat/model/testdata/now above the working directory;"
                            + " run `mvn -B test` from compat/suites/java-sdk to check it")));
        }
        JsonNode root = new ObjectMapper().readTree(file);
        for (Iterator<String> it = root.fieldNames(); it.hasNext(); ) {
            String key = it.next();
            assertTrue(List.of("$comment", "instant", "valid", "invalid", "invalidArguments", "call").contains(key),
                    "unknown key " + key + " in " + file);
        }
        assertTrue(root.get("valid").size() > 0 && root.get("invalid").size() > 0
                        && root.get("invalidArguments").size() > 0,
                "the $now fixture may not be skipped by emptying it");
        long instant = root.get("instant").asLong();
        JsonNode call = root.get("call");
        long tick = call.get("tickMillis").asLong();

        List<DynamicNode> out = new ArrayList<>();
        for (JsonNode c : root.get("valid")) {
            String unit = c.get("now").get("unit").asText();
            long offset = c.get("now").has("offsetMillis") ? c.get("now").get("offsetMillis").asLong() : 0L;
            long want = c.get("value").asLong();
            out.add(DynamicTest.dynamicTest("valid/" + c.get("name").asText(), () ->
                    assertEquals(want, binder(ticking(instant, tick)).longValue("timestamp", Values.now(unit, offset)).longValue())));
        }
        for (JsonNode c : root.get("invalidArguments")) {
            String unit = c.get("unit").asText();
            long offset = c.get("offsetMillis").asLong();
            out.add(DynamicTest.dynamicTest("invalidArguments/" + c.get("name").asText(), () -> {
                ValueException e = assertThrows(ValueException.class,
                        () -> binder(ticking(instant, 0)).longValue("timestamp", Values.now(unit, offset)));
                assertEquals("timestamp", e.member());
            }));
        }
        out.add(DynamicTest.dynamicTest("one reading per call", () -> {
            LongSupplier clock = ticking(instant, tick);
            // The emitted request for the fixture's call: one binder, two
            // events, each timestamp bound in turn.
            Binder b = binder(clock);
            JsonNode sent = call.get("sent").get("logEvents");
            assertEquals(sent.get(0).get("timestamp").asLong(), b.longValue("logEvents", Values.now("epochMillis", -1L)).longValue());
            assertEquals(sent.get(1).get("timestamp").asLong(), b.longValue("logEvents", Values.now("epochMillis", 0L)).longValue());
            // The next call builds a fresh binder, and so reads the clock afresh.
            assertEquals(instant + tick, binder(clock).longValue("timestamp", Values.now("epochMillis", 0L)).longValue());
        }));
        return out;
    }

    private static File fixtureFile() {
        File dir = new File("").getAbsoluteFile();
        for (int i = 0; i < 8 && dir != null; i++) {
            File candidate = new File(dir, "compat/model/testdata/now/now.json");
            if (candidate.isFile()) {
                return candidate;
            }
            dir = dir.getParentFile();
        }
        return null;
    }
}
