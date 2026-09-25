namespace OvercastCompat.Scenario;

/// <summary>
/// The IR's closed vocabulary as C# types: a call, a test, the assertion set
/// and the checks inside it.
/// </summary>
/// <remarks>
/// The normative description of every rule this namespace implements is
/// compat/model/README.md. Where this namespace and that page disagree, this
/// namespace is wrong.
/// <para>cmd/compatgen builds a Clause only through the factories on
/// <see cref="Clause"/>, so a combination of fields the IR does not admit
/// cannot be emitted.</para>
/// </remarks>
internal enum ClauseKind
{
    ResponseField,
    Readback,
    ListContains,
    Absent,
    ErrorCode,
    Eventually,
}

/// <summary>The closed set of checks a clause may make on one path.</summary>
internal enum CheckKind
{
    NonEmpty,
    IsList,
    EqualTo,
    Matches,
    EqualsJson,
    Missing,
}

/// <summary>
/// One API call: the operation, the typed request the emitted code builds, the
/// client method that sends it, and the context paths it exports from its
/// response.
/// </summary>
internal sealed class ScenarioCall
{
    /// <summary>The AWS operation name — failure-message field 2.</summary>
    public required string Op { get; init; }

    /// <summary>
    /// The call's params exactly as the scenario file writes them, value
    /// expressions unevaluated.
    /// </summary>
    /// <remarks>
    /// It is failure-message field 3 for a failure raised <em>before</em>
    /// anything was sent (an unresolvable $ref, a value that will not fit the
    /// request property): nothing went on the wire, so the message shows what
    /// the file asked for. Once the request is built, field 3 is the built
    /// request instead, which is what was actually sent.
    /// </remarks>
    public required string Params { get; init; }

    /// <summary>Fills a typed SDK request object.</summary>
    /// <remarks>
    /// It reports problems by recording them on the <see cref="Binder"/>
    /// rather than by throwing, so an emitted body is a flat list of
    /// assignments; the binder's error is checked once, before anything is
    /// sent.
    /// </remarks>
    public required Func<Binder, object> Build { get; init; }

    /// <summary>Invokes the client method with whatever Build returned.</summary>
    public required Func<object, Task<object>> SendAsync { get; init; }

    /// <summary>Context path to a path in this call's own response.</summary>
    /// <remarks>
    /// A concrete Dictionary rather than the interface so the emitter can
    /// write a target-typed <c>new()</c>.
    /// </remarks>
    public Dictionary<string, string>? Export { get; init; }
}

/// <summary>One registry test: a primary call and at least one clause.</summary>
internal sealed class ScenarioTest
{
    public required ScenarioCall Call { get; init; }

    public required Clause[] Assert { get; init; }
}

/// <summary>
/// One assertion. Which fields are set depends on <see cref="Kind"/>; the
/// factories below are the only way the emitter builds one.
/// </summary>
internal sealed class Clause
{
    private Clause(ClauseKind kind) => Kind = kind;

    public ClauseKind Kind { get; }

    /// <summary>readback, and the call-carrying forms of the list clauses.</summary>
    public ScenarioCall? Call { get; private init; }

    public Check[] Checks { get; private init; } = [];

    /// <summary>listContains and absent (list form).</summary>
    public string ItemsPath { get; private init; } = "";

    public WhereEntry[] Where { get; private init; } = [];

    /// <summary>errorCode, and absent (error form).</summary>
    public ErrorSpec? Error { get; private init; }

    /// <summary>eventually.</summary>
    public int MaxAttempts { get; private init; }

    public int DelayMs { get; private init; }

    public Clause? Inner { get; private init; }

    /// <summary>Checks the test's own response.</summary>
    public static Clause ResponseField(params Check[] checks) =>
        new(ClauseKind.ResponseField) { Checks = checks };

    /// <summary>Makes a call of its own and checks its response.</summary>
    public static Clause Readback(ScenarioCall call, params Check[] checks) =>
        new(ClauseKind.Readback) { Call = call, Checks = checks };

    /// <summary>
    /// Requires the list at <paramref name="itemsPath"/> to hold a matching
    /// item. <paramref name="call"/> is null when the list is read from the
    /// test's own response.
    /// </summary>
    public static Clause ListContains(ScenarioCall? call, string itemsPath, params WhereEntry[] where) =>
        new(ClauseKind.ListContains) { Call = call, ItemsPath = itemsPath, Where = where };

    /// <summary>
    /// Requires the list at <paramref name="itemsPath"/> to hold no matching
    /// item. A missing list counts as empty.
    /// </summary>
    public static Clause AbsentFromList(ScenarioCall? call, string itemsPath, params WhereEntry[] where) =>
        new(ClauseKind.Absent) { Call = call, ItemsPath = itemsPath, Where = where };

    /// <summary>Requires <paramref name="call"/> to fail with the named error.</summary>
    public static Clause AbsentByError(ScenarioCall call, ErrorSpec want) =>
        new(ClauseKind.Absent) { Call = call, Error = want };

    /// <summary>Requires the test's own call to fail with the named error.</summary>
    public static Clause ErrorCode(ErrorSpec want) =>
        new(ClauseKind.ErrorCode) { Error = want };

    /// <summary>
    /// Retries one clause until it holds, at most <paramref name="maxAttempts"/>
    /// times, <paramref name="delayMs"/> apart.
    /// </summary>
    public static Clause Eventually(int maxAttempts, int delayMs, Clause inner) =>
        new(ClauseKind.Eventually) { MaxAttempts = maxAttempts, DelayMs = delayMs, Inner = inner };
}

/// <summary>
/// One check on one response path. <see cref="Value"/> carries the expected
/// value for EqualTo, the pattern for Matches and the parsed operand document
/// for EqualsJson, and is null for the rest.
/// </summary>
internal sealed record Check(string Path, CheckKind Kind, object? Value)
{
    /// <summary>
    /// Holds when the path resolves to a value that is not null, "", [] or {}.
    /// Numbers and booleans are never empty.
    /// </summary>
    public static Check NonEmpty(string path) => new(path, CheckKind.NonEmpty, null);

    /// <summary>
    /// Holds when the path resolves to a list, empty or not — or does not
    /// resolve at all. A present value that is not a list fails it.
    /// </summary>
    public static Check IsList(string path) => new(path, CheckKind.IsList, null);

    /// <summary>
    /// Holds when the path resolves and the value is equal, as JSON, to the
    /// evaluated expression.
    /// </summary>
    /// <remarks>
    /// Named EqualTo rather than Equals because object already declares a
    /// static Equals(object, object): an overload beside it reads as a mistake
    /// even where the compiler resolves it correctly.
    /// </remarks>
    public static Check EqualTo(string path, object? want) => new(path, CheckKind.EqualTo, want);

    /// <summary>Holds when the path resolves to a string matching the pattern.</summary>
    public static Check Matches(string path, string pattern) => new(path, CheckKind.Matches, pattern);

    /// <summary>
    /// Holds when the path resolves to a JSON document equal, as JSON, to
    /// <paramref name="document"/>: a string percent-decoded once and parsed,
    /// or an object or list as it is (see <see cref="EqualsJsonCheck"/>).
    /// </summary>
    /// <remarks>
    /// <paramref name="document"/> is the operand as the compact JSON text the
    /// emitter wrote. It is parsed here, once, so an operand that is not a
    /// literal object or array fails the test that builds it rather than every
    /// evaluation of it; cmd/compatgen refuses such an operand first, so this is
    /// the backstop the shared fixture holds every runtime to.
    /// </remarks>
    /// <exception cref="ScenarioValueException">
    /// The operand is not one JSON object or array, or has a <c>$</c>-prefixed
    /// key at any depth.
    /// </exception>
    public static Check EqualsJson(string path, string document) =>
        new(path, CheckKind.EqualsJson, EqualsJsonCheck.Operand(document));

    /// <summary>
    /// Holds when the path does not resolve. A member the service sent as JSON
    /// null resolves, so Missing fails on it.
    /// </summary>
    public static Check Missing(string path) => new(path, CheckKind.Missing, null);
}

/// <summary>
/// One criterion an item of a list must satisfy. "$" is the item itself, which
/// is how a list of strings is matched.
/// </summary>
internal sealed record WhereEntry(string Path, object? Value);

/// <summary>
/// One error named two ways, because SDKs disagree about which they surface:
/// the modeled shape name and the wire code. Either is accepted.
/// </summary>
internal sealed record ErrorSpec(string Shape, string Code);
