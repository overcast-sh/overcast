package io.overcast.compat.scenario;

import software.amazon.awssdk.core.SdkBytes;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Resolves the deferred parts of one call's typed input.
 *
 * <p>The emitted source writes the enum conversion and every composite around a
 * call to this class itself — {@code cmd/compatgen} resolved the member's type
 * from the pinned model before writing the file — so all that is left at run
 * time is the one thing that cannot be known before the run: what a {@code $ref}
 * into the group's context, or a {@code $name} built from the run id, evaluates
 * to.
 *
 * <p>Each accessor converts the evaluated value to the one Java type the AWS SDK
 * for Java v2 gives that member. The Java SDK boxes every scalar, so a member is
 * always nullable and a boxed {@code 0} really is sent — verified on the wire by
 * {@code JavaSdkWireFactsTest} — which is why this class has no counterpart to
 * the Go emitter's zero-value refusal.
 *
 * <p>A mismatch throws rather than coercing: {@code "30"} is not {@code 30}
 * anywhere else in the IR, and the exception abandons the whole call before
 * anything is sent.
 */
public final class Binder {

    private final String runId;
    private final String group;
    private final ContextBag bag;

    Binder(String runId, String group, ContextBag bag) {
        this.runId = runId;
        this.group = group;
        this.bag = bag;
    }

    String runId() {
        return runId;
    }

    String group() {
        return group;
    }

    /** Reads a context path, or throws the one failure a teardown step may skip for. */
    Object lookup(String path) {
        if (!bag.has(path)) {
            throw ValueException.unresolvedRef(path);
        }
        return bag.get(path);
    }

    void export(String path, Object value) {
        bag.set(path, value);
    }

    // ── Typed accessors — the emitted source's whole run-time vocabulary ──────

    /** Binds an expression into a {@code String} member. */
    public String string(String member, Object v) {
        Object value = evalFor(member, v);
        if (!(value instanceof String s)) {
            throw member(member, "wanted a string, got " + Json.render(value));
        }
        return s;
    }

    /** Binds an expression into a {@code Boolean} member. */
    public Boolean bool(String member, Object v) {
        Object value = evalFor(member, v);
        if (!(value instanceof Boolean b)) {
            throw member(member, "wanted a boolean, got " + Json.render(value));
        }
        return b;
    }

    /** Binds an expression into a {@code Byte} member. */
    public Byte byteValue(String member, Object v) {
        return (byte) whole(member, v, Byte.MIN_VALUE, Byte.MAX_VALUE, "Byte");
    }

    /** Binds an expression into a {@code Short} member. */
    public Short shortValue(String member, Object v) {
        return (short) whole(member, v, Short.MIN_VALUE, Short.MAX_VALUE, "Short");
    }

    /** Binds an expression into an {@code Integer} member. */
    public Integer integer(String member, Object v) {
        return (int) whole(member, v, Integer.MIN_VALUE, Integer.MAX_VALUE, "Integer");
    }

    /** Binds an expression into a {@code Long} member. */
    public Long longValue(String member, Object v) {
        return whole(member, v, Long.MIN_VALUE, Long.MAX_VALUE, "Long");
    }

    /** Binds an expression into a {@code Float} member. */
    public Float floatValue(String member, Object v) {
        return (float) fractional(member, v, "Float");
    }

    /** Binds an expression into a {@code Double} member. */
    public Double doubleValue(String member, Object v) {
        return fractional(member, v, "Double");
    }

    /**
     * Binds a {@code $base64} expression into a blob member. The emitted source
     * calls this only for a deferred blob — a {@code $base64} around a
     * {@code $ref} — because a literal's bytes are written into the source
     * directly.
     */
    public SdkBytes blob(String member, Object v) {
        Object value = evalFor(member, v);
        if (!(value instanceof String text)) {
            throw member(member, "wanted a blob's base64 text, got " + Json.render(value));
        }
        try {
            return SdkBytes.fromByteArray(Values.decodeBase64(text));
        } catch (ValueException e) {
            throw member(member, e.getMessage());
        }
    }

    // ── Evaluation ───────────────────────────────────────────────────────────

    /**
     * Evaluates one value: a {@link Value} is an expression, a {@link List} is a
     * list of values, a {@link Map} is a structure or map of values, and
     * anything else is itself — normalised the way a document is, so a Java
     * literal and a value read back out of a response compare in the same type
     * system.
     */
    Object eval(Object v) {
        if (v instanceof Value expr) {
            return eval(expr.eval(this));
        }
        if (v instanceof List<?> items) {
            List<Object> out = new ArrayList<>(items.size());
            for (Object item : items) {
                out.add(eval(item));
            }
            return out;
        }
        if (v instanceof Map<?, ?> members) {
            Map<String, Object> out = new LinkedHashMap<>();
            for (Map.Entry<?, ?> entry : members.entrySet()) {
                out.put(String.valueOf(entry.getKey()), eval(entry.getValue()));
            }
            return out;
        }
        return Doc.of(v);
    }

    private Object evalFor(String member, Object v) {
        try {
            return eval(v);
        } catch (ValueException e) {
            if (e.contextPath() != null) {
                throw e;
            }
            throw member(member, e.getMessage());
        }
    }

    private long whole(String member, Object v, long min, long max, String type) {
        double n = number(member, v, type);
        if (n != Math.rint(n)) {
            throw member(member, "wanted a whole number, got " + Json.render(n));
        }
        if (n < min || n > max) {
            throw member(member, "wanted a number in range for " + type + ", got " + Json.render(n));
        }
        return (long) n;
    }

    private double fractional(String member, Object v, String type) {
        return number(member, v, type);
    }

    private double number(String member, Object v, String type) {
        Object value = evalFor(member, v);
        if (!(value instanceof Number n)) {
            throw member(member, "wanted a number for " + type + ", got " + Json.render(value));
        }
        return n.doubleValue();
    }

    private static ValueException member(String member, String detail) {
        return ValueException.forMember(member, detail);
    }
}
