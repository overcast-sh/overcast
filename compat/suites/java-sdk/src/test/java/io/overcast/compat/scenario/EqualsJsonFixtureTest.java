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
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

/**
 * The shared {@code equalsJSON} conformance fixture,
 * {@code compat/model/testdata/equalsjson}.
 *
 * <p>The emitted source hands this runtime
 * {@code Check.equalsJson(path, operandText)}, so every case is run the way a
 * generated group runs it: the fixture's {@code expected} is serialized to the
 * JSON text the emitter would write and given to {@link Check#equalsJson}, and
 * its {@code actual} is the document value a path resolved to. {@code decode}
 * pins the percent-decoding, {@code holds} and {@code fails} the check, and
 * {@code invalidExpected} the operands the factory refuses.
 *
 * <p>Found and required exactly as {@link BlobFixtureTest} finds its fixture.
 */
class EqualsJsonFixtureTest {

    private static final String REQUIRED_ENV_VAR = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

    private static final ObjectMapper MAPPER = new ObjectMapper();

    /** What the check makes of a fixture case: {@code null} when it holds, else the actual field. */
    private static String evaluate(JsonNode c) throws IOException {
        Check check = Check.equalsJson("$.Document", MAPPER.writeValueAsString(c.get("expected")));
        assertEquals(Check.Kind.EQUALS_JSON, check.kind());
        Object actual = EqualsJson.fromNode(c.get("actual"));
        return EqualsJson.mismatch(check.value(), new Paths.Resolved(actual, true));
    }

    @TestFactory
    List<DynamicNode> sharedEqualsJsonFixture() throws IOException {
        File file = fixtureFile();
        if (file == null) {
            if ("1".equals(System.getenv(REQUIRED_ENV_VAR))) {
                fail(REQUIRED_ENV_VAR + "=1 but no compat/model/testdata/equalsjson/equalsjson.json found above the"
                        + " working directory — this suite's fixture test must run from a full checkout");
            }
            return List.of(DynamicTest.dynamicTest("shared equalsJSON fixture", () -> Assumptions.abort(
                    "no compat/model/testdata/equalsjson above the working directory;"
                            + " run `mvn -B test` from compat/suites/java-sdk to check it")));
        }
        JsonNode root = MAPPER.readTree(file);
        List<String> sections = List.of("decode", "holds", "fails", "invalidExpected");
        for (Iterator<String> it = root.fieldNames(); it.hasNext(); ) {
            String key = it.next();
            assertTrue(key.equals("$comment") || sections.contains(key), "unknown key " + key + " in " + file);
        }
        for (String section : sections) {
            assertTrue(root.has(section) && root.get(section).size() > 0,
                    "the equalsJSON fixture may not be skipped by emptying " + section);
        }

        List<DynamicNode> out = new ArrayList<>();
        for (JsonNode c : root.get("decode")) {
            requireKeys(c, "name", "text", "decoded");
            out.add(DynamicTest.dynamicTest("decode/" + c.get("name").asText(), () ->
                    assertEquals(c.get("decoded").asText(), EqualsJson.percentDecode(c.get("text").asText()))));
        }
        for (JsonNode c : root.get("holds")) {
            requireKeys(c, "name", "actual", "expected");
            out.add(DynamicTest.dynamicTest("holds/" + c.get("name").asText(), () -> assertNull(evaluate(c))));
        }
        for (JsonNode c : root.get("fails")) {
            requireKeys(c, "name", "actual", "expected");
            out.add(DynamicTest.dynamicTest("fails/" + c.get("name").asText(), () -> assertNotNull(evaluate(c))));
        }
        for (JsonNode c : root.get("invalidExpected")) {
            requireKeys(c, "name", "expected");
            // Handed the operand's JSON serialization, so a JSON string holding
            // an object's text reaches the factory as a string and is refused.
            String text = MAPPER.writeValueAsString(c.get("expected"));
            out.add(DynamicTest.dynamicTest("invalidExpected/" + c.get("name").asText(), () ->
                    assertThrows(ValueException.class, () -> Check.equalsJson("$.Document", text))));
        }
        return out;
    }

    /** The six-field message's expected and actual fields, once for each actual rendering. */
    @TestFactory
    List<DynamicNode> failureFields() {
        Object operand = Check.equalsJson("$.D", "{\"a\":1}").value();
        return List.of(
                DynamicTest.dynamicTest("expected", () ->
                        assertEquals("equalsJSON {\"a\":1}", EqualsJson.expected(operand))),
                DynamicTest.dynamicTest("a document", () -> assertEquals("document {\"a\":1,\"b\":2}",
                        EqualsJson.mismatch(operand, new Paths.Resolved("{\"b\":2,\"a\":1}", true)))),
                DynamicTest.dynamicTest("not a document", () -> assertEquals("not a JSON document: \"not a policy\"",
                        EqualsJson.mismatch(operand, new Paths.Resolved("not a policy", true)))),
                DynamicTest.dynamicTest("a number", () -> assertEquals("not a JSON document: 5",
                        EqualsJson.mismatch(operand, new Paths.Resolved(5.0, true)))),
                DynamicTest.dynamicTest("missing", () -> assertEquals(Json.MISSING,
                        EqualsJson.mismatch(operand, new Paths.Resolved(null, false)))),
                DynamicTest.dynamicTest("holds on an SDK document", () -> assertNull(
                        EqualsJson.mismatch(operand, new Paths.Resolved(Map.of("a", 1.0), true)))));
    }

    /** The strict reader: a key this test does not know is a fixture it cannot be trusted to run. */
    private static void requireKeys(JsonNode c, String... keys) {
        List<String> known = List.of(keys);
        for (Iterator<String> it = c.fieldNames(); it.hasNext(); ) {
            String key = it.next();
            assertTrue(known.contains(key), "unknown key " + key + " in " + c);
        }
        for (String key : keys) {
            assertTrue(c.has(key), "missing key " + key + " in " + c);
        }
    }

    private static File fixtureFile() {
        File dir = new File("").getAbsoluteFile();
        for (int i = 0; i < 8 && dir != null; i++) {
            File candidate = new File(dir, "compat/model/testdata/equalsjson/equalsjson.json");
            if (candidate.isFile()) {
                return candidate;
            }
            dir = dir.getParentFile();
        }
        return null;
    }
}
