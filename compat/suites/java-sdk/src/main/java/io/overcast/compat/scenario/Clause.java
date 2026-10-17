package io.overcast.compat.scenario;

import java.util.Arrays;
import java.util.List;

/**
 * One assertion. Which fields are set depends on {@link #kind()}; the factories
 * below are the only way the emitter builds one, so an unrepresentable
 * combination cannot be emitted.
 */
public final class Clause {

    /** The closed assertion set ({@code compat/model/README.md} § Assertions). */
    public enum Kind {
        RESPONSE_FIELD("responseField"),
        READBACK("readback"),
        LIST_CONTAINS("listContains"),
        ABSENT("absent"),
        ERROR_CODE("errorCode"),
        EVENTUALLY("eventually");

        private final String label;

        Kind(String label) {
            this.label = label;
        }

        /** The IR's own spelling, which is what a failure message names. */
        public String label() {
            return label;
        }
    }

    private final Kind kind;
    private final Call call;
    private final List<Check> checks;
    private final String itemsPath;
    private final List<Where> where;
    private final ErrorSpec error;
    private final int maxAttempts;
    private final int delayMs;
    private final Clause inner;
    private final Page page;

    private Clause(Kind kind, Call call, List<Check> checks, String itemsPath, List<Where> where,
                   ErrorSpec error, int maxAttempts, int delayMs, Clause inner) {
        this(kind, call, checks, itemsPath, where, error, maxAttempts, delayMs, inner, null);
    }

    private Clause(Kind kind, Call call, List<Check> checks, String itemsPath, List<Where> where,
                   ErrorSpec error, int maxAttempts, int delayMs, Clause inner, Page page) {
        this.page = page;
        this.kind = kind;
        this.call = call;
        this.checks = checks;
        this.itemsPath = itemsPath;
        this.where = where;
        this.error = error;
        this.maxAttempts = maxAttempts;
        this.delayMs = delayMs;
        this.inner = inner;
    }

    /** Checks the test's own response. */
    public static Clause responseField(Check... checks) {
        return new Clause(Kind.RESPONSE_FIELD, null, Arrays.asList(checks), null, List.of(), null, 0, 0, null);
    }

    /** Makes a call of its own and checks its response. */
    public static Clause readback(Call call, Check... checks) {
        return new Clause(Kind.READBACK, call, Arrays.asList(checks), null, List.of(), null, 0, 0, null);
    }

    /**
     * Requires the list at {@code itemsPath} to hold a matching item.
     * {@code call} is null when the list is read from the test's own response.
     */
    public static Clause listContains(Call call, String itemsPath, Where... where) {
        return new Clause(Kind.LIST_CONTAINS, call, List.of(), itemsPath, Arrays.asList(where), null, 0, 0, null);
    }

    /**
     * Requires the list at {@code itemsPath} to hold no matching item. A missing
     * list counts as empty.
     */
    public static Clause absentFromList(Call call, String itemsPath, Where... where) {
        return new Clause(Kind.ABSENT, call, List.of(), itemsPath, Arrays.asList(where), null, 0, 0, null);
    }

    /** Requires {@code call} to fail with the named error. */
    public static Clause absentByError(Call call, ErrorSpec want) {
        return new Clause(Kind.ABSENT, call, List.of(), null, List.of(), want, 0, 0, null);
    }

    /** Requires the test's own call to fail with the named error. */
    public static Clause errorCode(ErrorSpec want) {
        return new Clause(Kind.ERROR_CODE, null, List.of(), null, List.of(), want, 0, 0, null);
    }

    /**
     * Retries one clause until it holds, at most {@code maxAttempts} times,
     * {@code delayMs} apart.
     */
    public static Clause eventually(int maxAttempts, int delayMs, Clause inner) {
        return new Clause(Kind.EVENTUALLY, null, List.of(), null, List.of(), null, maxAttempts, delayMs, inner);
    }

    /**
     * Makes a list clause with a call walk every page of its list: the clause's
     * call is page one, {@code page.next()} every later page, and the items of
     * all the pages are searched as one list.
     */
    public Clause paged(Page page) {
        return new Clause(kind, call, checks, itemsPath, where, error, maxAttempts, delayMs, inner, page);
    }

    Page page() {
        return page;
    }

    Kind kind() {
        return kind;
    }

    Call call() {
        return call;
    }

    List<Check> checks() {
        return checks;
    }

    String itemsPath() {
        return itemsPath;
    }

    List<Where> where() {
        return where;
    }

    ErrorSpec error() {
        return error;
    }

    int maxAttempts() {
        return maxAttempts;
    }

    int delayMs() {
        return delayMs;
    }

    Clause inner() {
        return inner;
    }
}
