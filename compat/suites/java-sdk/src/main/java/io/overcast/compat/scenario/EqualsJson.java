package io.overcast.compat.scenario;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.io.ByteArrayOutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * The {@code equalsJSON} check: a member holding a JSON document, compared by
 * value rather than by text ({@code compat/model/README.md} § Assertions).
 *
 * <p>It exists because the SDKs disagree about what such a member is. IAM sends
 * a policy document as percent-encoded JSON text; botocore decodes it for the
 * two Python backends, and this SDK hands over the string as sent. So a string
 * is percent-decoded once and parsed here, while an object or a list is the
 * document already. {@code compat/model/testdata/equalsjson} pins every rule for
 * every backend at once.
 *
 * <p>Both sides end up in {@link Doc}'s document form — maps, lists, strings,
 * doubles, booleans and {@link Json#NULL} — so the comparison is
 * {@link Json#equal}, the same "equal, as JSON" {@code equals} uses. Its
 * canonical encoding keys objects by sorted name and writes each double in
 * exactly one spelling, which is what makes {@code 1}, {@code 1.0},
 * {@code 1e0} and {@code -0}/{@code 0} equal and nothing else.
 */
final class EqualsJson {

    private EqualsJson() {}

    /**
     * Refuses trailing tokens: {@code readTree} otherwise stops after the first
     * value and ignores the rest, which would make {@code "{} x"} a document.
     */
    private static final ObjectMapper STRICT = new ObjectMapper()
            .enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS);

    /**
     * Parses the operand the emitter wrote as compact JSON text. The operand is
     * a literal object or array and is never evaluated, so a {@code $}-prefixed
     * key anywhere inside it — which the IR would otherwise read as an
     * expression — is refused rather than compared either way.
     *
     * @return the operand in document form
     * @throws ValueException when the text is not one JSON object or array, or
     *                        holds a {@code $} key
     */
    static Object operand(String text) {
        Object doc = parse(text);
        if (doc == null) {
            throw ValueException.of("equalsJSON operand is not JSON text: " + Json.quote(text));
        }
        if (!(doc instanceof Map<?, ?>) && !(doc instanceof List<?>)) {
            throw ValueException.of("equalsJSON operand must be a JSON object or array, not " + Json.render(doc));
        }
        String key = dollarKey(doc);
        if (key != null) {
            throw ValueException.of("equalsJSON operand has the key " + Json.quote(key)
                    + "; it is a literal document, and a $ key would read as an expression");
        }
        return doc;
    }

    /**
     * The document an actual value holds: a string percent-decoded once and
     * parsed as one JSON text, an object or list as it is.
     *
     * @return the document, or {@code null} when the value is not one — a
     *         number, a boolean, null, or text that does not parse
     */
    static Object document(Object actual) {
        if (actual instanceof String s) {
            return parse(percentDecode(s));
        }
        if (actual instanceof Map<?, ?> || actual instanceof List<?>) {
            return actual;
        }
        return null;
    }

    /**
     * Evaluates the check against what the path resolved to.
     *
     * @return {@code null} when it holds; otherwise the failure message's
     *         actual field
     */
    static String mismatch(Object operand, Paths.Resolved got) {
        if (!got.ok()) {
            return Json.MISSING;
        }
        Object doc = document(got.value());
        if (doc == null) {
            return "not a JSON document: " + Json.render(got.value());
        }
        return Json.equal(doc, operand) ? null : "document " + Json.render(doc);
    }

    /** The failure message's expected field. */
    static String expected(Object operand) {
        return "equalsJSON " + Json.render(operand);
    }

    /**
     * Python's {@code urllib.parse.unquote}, which botocore applies to the
     * member unconditionally: {@code %XX} becomes that byte and everything else
     * — {@code +} included, and a {@code %} not followed by two hex digits — is
     * kept as its own UTF-8 bytes, then the bytes are read as UTF-8. Hand-rolled
     * because {@code URLDecoder} turns {@code +} into a space and throws on a
     * malformed escape, and both would part from the other backends.
     */
    static String percentDecode(String text) {
        ByteArrayOutputStream bytes = new ByteArrayOutputStream(text.length());
        int i = 0;
        while (i < text.length()) {
            char c = text.charAt(i);
            if (c == '%' && i + 2 < text.length()
                    && hex(text.charAt(i + 1)) >= 0 && hex(text.charAt(i + 2)) >= 0) {
                bytes.write(hex(text.charAt(i + 1)) << 4 | hex(text.charAt(i + 2)));
                i += 3;
                continue;
            }
            // One code point at a time, so a surrogate pair stays one character
            // rather than two lone halves encoded apart.
            int cp = text.codePointAt(i);
            byte[] utf8 = new String(Character.toChars(cp)).getBytes(StandardCharsets.UTF_8);
            bytes.write(utf8, 0, utf8.length);
            i += Character.charCount(cp);
        }
        return bytes.toString(StandardCharsets.UTF_8);
    }

    /** An ASCII hex digit's value, or -1: {@code Character.digit} also takes fullwidth digits. */
    private static int hex(char c) {
        return c < 128 ? Character.digit(c, 16) : -1;
    }

    /**
     * Parses exactly one JSON text, whitespace around it allowed.
     *
     * @return the document, or {@code null} when the text is not one
     */
    private static Object parse(String text) {
        JsonNode node;
        try {
            node = STRICT.readTree(text);
        } catch (JsonProcessingException e) {
            return null;
        }
        // Empty or all-whitespace content reads as a missing node, not an error.
        if (node == null || node.isMissingNode()) {
            return null;
        }
        return fromNode(node);
    }

    /** A Jackson tree in {@link Doc}'s document form. */
    static Object fromNode(JsonNode node) {
        if (node.isObject()) {
            Map<String, Object> out = new LinkedHashMap<>();
            for (Iterator<Map.Entry<String, JsonNode>> it = node.fields(); it.hasNext(); ) {
                Map.Entry<String, JsonNode> e = it.next();
                out.put(e.getKey(), fromNode(e.getValue()));
            }
            return out;
        }
        if (node.isArray()) {
            List<Object> out = new ArrayList<>(node.size());
            for (JsonNode item : node) {
                out.add(fromNode(item));
            }
            return out;
        }
        if (node.isTextual()) {
            return node.textValue();
        }
        if (node.isNumber()) {
            return node.doubleValue();
        }
        if (node.isBoolean()) {
            return node.booleanValue();
        }
        return Json.NULL;
    }

    /** The first {@code $}-prefixed object key at any depth, or {@code null}. */
    private static String dollarKey(Object doc) {
        if (doc instanceof Map<?, ?> m) {
            for (Map.Entry<?, ?> e : m.entrySet()) {
                String key = String.valueOf(e.getKey());
                if (key.startsWith("$")) {
                    return key;
                }
                String inner = dollarKey(e.getValue());
                if (inner != null) {
                    return inner;
                }
            }
        } else if (doc instanceof List<?> l) {
            for (Object item : l) {
                String inner = dollarKey(item);
                if (inner != null) {
                    return inner;
                }
            }
        }
        return null;
    }
}
