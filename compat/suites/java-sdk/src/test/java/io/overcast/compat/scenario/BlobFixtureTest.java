package io.overcast.compat.scenario;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Assumptions;
import org.junit.jupiter.api.DynamicNode;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;
import software.amazon.awssdk.core.SdkBytes;
import software.amazon.awssdk.services.kinesis.model.Record;

import java.io.File;
import java.io.IOException;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.Iterator;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

/**
 * The shared blob-value conformance fixture, {@code compat/model/testdata/blobs}.
 *
 * <p>Every backend agrees on one document form for a blob — its canonical
 * standard base64 text ({@code compat/model/README.md} § Values) — and this is
 * where java-sdk proves it against the same cases every other suite reads:
 * {@code $base64} binds to exactly the fixture's bytes, whether written as a
 * literal or as a {@code $ref} to an exported blob; the SDK's {@link SdkBytes}
 * renders back to exactly that text; an {@code equals} against the
 * {@code $base64} holds; and every spelling the fixture calls invalid is
 * refused rather than decoded into something else.
 *
 * <p>Found and required exactly as {@link ErrorFixturesTest} finds its corpus.
 */
class BlobFixtureTest {

    private static final String REQUIRED_ENV_VAR = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

    private static Binder binder(String contextPath, String value) {
        ContextBag bag = new ContextBag();
        bag.set(contextPath, value);
        return new Binder("oc-test", "kinesis-records", bag);
    }

    @TestFactory
    List<DynamicNode> sharedBlobFixture() throws IOException {
        File file = fixtureFile();
        if (file == null) {
            if ("1".equals(System.getenv(REQUIRED_ENV_VAR))) {
                fail(REQUIRED_ENV_VAR + "=1 but no compat/model/testdata/blobs/blobs.json found above the"
                        + " working directory — this suite's fixture test must run from a full checkout");
            }
            return List.of(DynamicTest.dynamicTest("shared blob fixture", () -> Assumptions.abort(
                    "no compat/model/testdata/blobs above the working directory;"
                            + " run `mvn -B test` from compat/suites/java-sdk to check it")));
        }
        JsonNode root = new ObjectMapper().readTree(file);
        List<String> keys = new ArrayList<>();
        for (Iterator<String> it = root.fieldNames(); it.hasNext(); ) {
            keys.add(it.next());
        }
        for (String key : keys) {
            assertTrue(List.of("$comment", "valid", "invalid").contains(key), "unknown key " + key + " in " + file);
        }
        assertTrue(root.get("valid").size() > 0 && root.get("invalid").size() > 0,
                "the blob fixture may not be skipped by emptying it");

        List<DynamicNode> out = new ArrayList<>();
        for (JsonNode c : root.get("valid")) {
            String name = c.get("name").asText();
            String text = c.get("base64").asText();
            byte[] want = HexFormat.of().parseHex(c.get("hex").asText());
            out.add(DynamicTest.dynamicTest("valid/" + name, () -> {
                Binder b = binder("rec.data", text);
                assertArrayEquals(want, b.blob("Data", Values.base64(text)).asByteArray());
                assertArrayEquals(want, b.blob("Data", Values.base64(Values.ref("rec.data"))).asByteArray());
                assertArrayEquals(want, Values.decodeBase64(text));

                Object doc = Doc.of(Record.builder().data(SdkBytes.fromByteArray(want)).build());
                Object data = ((Map<?, ?>) doc).get("Data");
                assertEquals(text, data, "an SdkBytes renders to the fixture's text");
                assertTrue(Json.equal(data, b.eval(Values.base64(text))), "equals $base64 holds");
            }));
        }
        for (JsonNode c : root.get("invalid")) {
            String name = c.get("name").asText();
            String text = c.get("base64").asText();
            out.add(DynamicTest.dynamicTest("invalid/" + name, () -> {
                assertThrows(ValueException.class, () -> Values.decodeBase64(text));
                ValueException e = assertThrows(ValueException.class,
                        () -> binder("rec.data", text).blob("Data", Values.base64(Values.ref("rec.data"))));
                assertEquals("Data", e.member());
            }));
        }
        return out;
    }

    private static File fixtureFile() {
        File dir = new File("").getAbsoluteFile();
        for (int i = 0; i < 8 && dir != null; i++) {
            File candidate = new File(dir, "compat/model/testdata/blobs/blobs.json");
            if (candidate.isFile()) {
                return candidate;
            }
            dir = dir.getParentFile();
        }
        return null;
    }
}
