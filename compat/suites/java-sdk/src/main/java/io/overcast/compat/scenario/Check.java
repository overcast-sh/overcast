package io.overcast.compat.scenario;

/**
 * One check on one response path. The set is closed
 * ({@code compat/model/README.md} § Assertions), and the factories below are
 * the only way the emitter builds one.
 *
 * @param path  the response path this check reads
 * @param kind  which check it is
 * @param value the expected value for {@code EQUALS}, the pattern for
 *              {@code MATCHES}, the operand expression for {@code ENDS_WITH},
 *              {@code STARTS_WITH} and {@code CONTAINS}, and the parsed operand document for
 *              {@code EQUALS_JSON}; {@code null} for the rest
 */
public record Check(String path, Check.Kind kind, Object value) {

    /** The closed set of checks a clause may make on one path. */
    public enum Kind {
        NON_EMPTY("nonEmpty"),
        IS_LIST("isList"),
        EQUALS("equals"),
        MATCHES("matches"),
        ENDS_WITH("endsWith"),
        STARTS_WITH("startsWith"),
        CONTAINS("contains"),
        EQUALS_JSON("equalsJSON"),
        MISSING("missing"),
        PRESENT("present");

        private final String label;

        Kind(String label) {
            this.label = label;
        }

        /** The IR's own spelling, which is what a failure message names. */
        public String label() {
            return label;
        }
    }

    /**
     * Holds when the path resolves to a value that is not null, {@code ""},
     * {@code []} or <code>{}</code>. Numbers and booleans are never empty.
     */
    public static Check nonEmpty(String path) {
        return new Check(path, Kind.NON_EMPTY, null);
    }

    /**
     * Holds when the path resolves to a list, empty or not — or does not
     * resolve at all. A present value that is not a list fails it.
     */
    public static Check isList(String path) {
        return new Check(path, Kind.IS_LIST, null);
    }

    /**
     * Holds when the path resolves and the value is equal, as JSON, to the
     * evaluated expression.
     */
    public static Check equalTo(String path, Object want) {
        return new Check(path, Kind.EQUALS, want);
    }

    /** Holds when the path resolves to a string matching the pattern. */
    public static Check matches(String path, String pattern) {
        return new Check(path, Kind.MATCHES, pattern);
    }

    /**
     * Holds when the path resolves to a string that ends with the evaluated
     * operand (case-sensitive, no regex). The operand is a value expression,
     * evaluated like an {@link #equalTo} operand, and must yield a string.
     */
    public static Check endsWith(String path, Object suffix) {
        return new Check(path, Kind.ENDS_WITH, suffix);
    }

    /** {@link #endsWith} for the front of the string. */
    public static Check startsWith(String path, Object prefix) {
        return new Check(path, Kind.STARTS_WITH, prefix);
    }

    /** {@link #endsWith} for any position in the string. */
    public static Check contains(String path, Object part) {
        return new Check(path, Kind.CONTAINS, part);
    }

    /**
     * Holds when the path resolves to a JSON document equal, as JSON, to
     * {@code document}: a string percent-decoded once and parsed, or an object
     * or list as it is (see {@link EqualsJson}).
     *
     * <p>{@code document} is the operand as the compact JSON text the emitter
     * wrote. It is parsed here, once, so an operand that is not a literal
     * object or array fails the test that builds it rather than every
     * evaluation of it; {@code cmd/compatgen} refuses such an operand first, so
     * this is the backstop the shared fixture holds every runtime to.
     *
     * @throws ValueException when the operand is not one JSON object or array,
     *                        or has a {@code $}-prefixed key at any depth
     */
    public static Check equalsJson(String path, String document) {
        return new Check(path, Kind.EQUALS_JSON, EqualsJson.operand(document));
    }

    /**
     * Holds when the path does not resolve. A member the service sent as JSON
     * null resolves, so this fails on it.
     */
    public static Check missing(String path) {
        return new Check(path, Kind.MISSING, null);
    }

    /**
     * Holds when the path resolves, whatever the value: an empty list, an
     * empty string, zero and false all resolve, and so does a member the
     * service sent as JSON null. Exactly the negation of {@link #missing}.
     */
    public static Check present(String path) {
        return new Check(path, Kind.PRESENT, null);
    }
}
