package io.overcast.compat.scenario;

import java.util.ArrayList;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Value expressions ({@code compat/model/README.md} § Values), as Java.
 *
 * <p>The IR's five forms are five factories here. A value is ordinary Java data
 * — a {@link String}, a number, a {@link Boolean}, a {@link List}, a {@link Map}
 * — and a {@link Value} anywhere inside it is an expression to evaluate,
 * exactly as an object with one {@code $}-prefixed key is an expression in the
 * JSON the interpreters read. There are no conditionals, no arithmetic and no
 * scripting: eight implementations have to agree on every value.
 *
 * <pre>
 *   {"$lit": v}        → Values.lit(v)
 *   {"$ref": "q.url"}  → Values.ref("q.url")
 *   {"$name": "q"}     → Values.name("q")
 *   {"$concat": [...]} → Values.concat(...)
 *   {"$index": [v, n]} → Values.index(v, n)
 *   {"$base64": x}     → Values.base64(x), and b.blob(member, Values.base64(x)) in a blob slot
 *   {"$now": {...}}    → Values.now(unit, offsetMillis), in a Long slot
 * </pre>
 */
public final class Values {

    private Values() {}

    /**
     * Wraps a literal that would otherwise be mistaken for something else. It
     * is rarely needed — a bare Java literal is already a literal — and exists
     * for the IR's {@code $lit}, whose whole job is to stop an object being read
     * as an expression.
     */
    public static Value lit(Object v) {
        return b -> v;
    }

    /** Reads a context path a previous call exported. */
    public static Value ref(String path) {
        return b -> b.lookup(path);
    }

    /**
     * The IR's only way to name a resource: {@code {runId}-{group}-{suffix}},
     * with the group token the whole group name and no shortening anywhere.
     * That is what makes the name-hygiene convention hold by construction, and
     * what lets the orphan sweep find anything a crashed run left behind.
     */
    public static Value name(String suffix) {
        return b -> b.runId() + "-" + b.group() + "-" + suffix;
    }

    /**
     * Joins its parts. A part that is a bare string is a literal; anything else
     * is an expression that must evaluate to a string.
     */
    public static Value concat(Object... parts) {
        return b -> {
            StringBuilder out = new StringBuilder();
            for (Object part : parts) {
                Object v = b.eval(part);
                if (!(v instanceof String s)) {
                    throw ValueException.of("concat part evaluated to " + Json.render(v)
                            + ", which is not a string");
                }
                out.append(s);
            }
            return out.toString();
        };
    }

    /** Takes element {@code n} of a list-valued expression. */
    public static Value index(Object list, int n) {
        return b -> {
            Object v = b.eval(list);
            if (!(v instanceof List<?> items)) {
                throw ValueException.of("index applies to a list, got " + Json.render(v));
            }
            if (n < 0 || n >= items.size()) {
                throw ValueException.of("index " + n + " is past the end of a list of " + items.size());
            }
            return items.get(n);
        };
    }

    /**
     * {@code $base64}: a blob. Its value is the blob's document form — the
     * canonical standard base64 text, which is also how {@link Doc} renders an
     * {@code SdkBytes} out of a response and so how an exported blob sits in
     * the context bag — which is what lets an {@code equals} compare a blob
     * path against it as two strings. {@code arg} is a literal base64 string or
     * an expression evaluating to one (the generator allows only a
     * {@code $ref} to an exported blob); text that is not canonical standard
     * base64 is an error, never a second spelling of the same bytes.
     *
     * <p>A blob member does not take this value as it is:
     * {@link Binder#blob(String, Object)} decodes it into the {@code SdkBytes}
     * the builder setter wants.
     */
    public static Value base64(Object arg) {
        return b -> {
            Object v = b.eval(arg);
            if (!(v instanceof String text)) {
                throw ValueException.of("$base64 takes base64 text, got " + Json.render(v));
            }
            decodeBase64(text);
            return text;
        };
    }

    /** {@code $now}'s one unit, and the bound on its offset either way: one hour. */
    private static final String NOW_UNIT = "epochMillis";
    private static final long NOW_MAX_OFFSET_MILLIS = 3_600_000L;

    /**
     * {@code $now}: the client's clock when the call is made, in epoch
     * milliseconds, plus {@code offsetMillis} — which {@code cmd/compatgen}
     * writes as 0 where the scenario omits it. The binder reads the clock once
     * per call, so every {@code $now} in one call's params sees the same
     * instant and their offsets order them.
     */
    public static Value now(String unit, long offsetMillis) {
        return b -> {
            checkNowArguments(unit, offsetMillis);
            return b.instant() + offsetMillis;
        };
    }

    /**
     * Holds the two things any {@code $now} comes down to — a unit the IR has,
     * and an offset inside an hour — to the rule every runtime holds its
     * {@code Now} to. {@code compat/model/testdata/now} pins it for every
     * backend at once.
     */
    static void checkNowArguments(String unit, long offsetMillis) {
        if (!NOW_UNIT.equals(unit)) {
            throw ValueException.of("$now unit " + Json.render(unit)
                    + " is not one the IR has; its one unit is \"epochMillis\"");
        }
        if (Math.abs(offsetMillis) > NOW_MAX_OFFSET_MILLIS) {
            throw ValueException.of("$now offsetMillis " + offsetMillis + " is outside ±"
                    + NOW_MAX_OFFSET_MILLIS + " (one hour)");
        }
    }

    /**
     * Decodes a blob's document form: standard base64 with padding, in its one
     * canonical spelling. {@code java.util.Base64}'s basic decoder refuses a
     * character outside the alphabet but not non-zero trailing bits or a
     * missing pad, so the round trip is what refuses a second spelling of the
     * same bytes. {@code compat/model/testdata/blobs} pins what every backend
     * accepts and refuses.
     */
    public static byte[] decodeBase64(String text) {
        byte[] raw;
        try {
            raw = Base64.getDecoder().decode(text);
        } catch (IllegalArgumentException e) {
            throw ValueException.of("$base64 " + Json.render(text) + " is not standard padded base64: " + e.getMessage());
        }
        String canonical = Base64.getEncoder().encodeToString(raw);
        if (!canonical.equals(text)) {
            throw ValueException.of("$base64 " + Json.render(text)
                    + " is not the canonical spelling of its bytes, which is " + Json.render(canonical));
        }
        return raw;
    }

    /**
     * An untyped object, for the places a value is compared rather than sent:
     * an {@code equals} expectation and a {@code where} entry. Keys and values
     * alternate, and the iteration order is the emitted one so a failure message
     * reads the same on every run.
     */
    public static Map<String, Object> map(Object... keysAndValues) {
        if (keysAndValues.length % 2 != 0) {
            throw new IllegalArgumentException("Values.map wants alternating keys and values");
        }
        Map<String, Object> out = new LinkedHashMap<>();
        for (int i = 0; i < keysAndValues.length; i += 2) {
            out.put((String) keysAndValues[i], keysAndValues[i + 1]);
        }
        return out;
    }

    /** An untyped list, for the same two places {@link #map} serves. */
    public static List<Object> list(Object... items) {
        List<Object> out = new ArrayList<>(items.length);
        for (Object item : items) {
            out.add(item);
        }
        return out;
    }
}
