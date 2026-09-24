namespace OvercastCompat.Scenario;

/// <summary>
/// Value expressions (compat/model/README.md § Values), as C#.
/// </summary>
/// <remarks>
/// The IR's five forms are five factories here. A value is ordinary data — a
/// string, a number, a bool, an object?[], a Dictionary&lt;string, object?&gt;
/// — and a <see cref="ScenarioValue"/> anywhere inside it is an expression to
/// evaluate, exactly as an object with one $-prefixed key is an expression in
/// the JSON the interpreters read. There are no conditionals, no arithmetic
/// and no scripting: eight implementations have to agree on every value.
/// <code>
/// {"$lit": v}        → Val.Lit(v)
/// {"$ref": "q.url"}  → Val.Ref("q.url")
/// {"$name": "q"}     → Val.Name("q")
/// {"$concat": [...]} → Val.Concat(...)
/// {"$index": [v, n]} → Val.Index(v, n)
/// {"$base64": x}     → Val.Base64(x), and b.Blob(member, Val.Base64(x)) in a blob slot
/// </code>
/// </remarks>
internal delegate object? ScenarioValue(Binder binder);

/// <summary>The IR's five value expressions.</summary>
internal static class Val
{
    /// <summary>
    /// Wraps a literal that would otherwise be mistaken for something else. It
    /// is rarely needed — a bare C# literal is already a literal — and exists
    /// for the IR's <c>$lit</c>, whose whole job is to stop an object being
    /// read as an expression.
    /// </summary>
    public static ScenarioValue Lit(object? value) => _ => value;

    /// <summary>Reads a context path a previous call exported.</summary>
    public static ScenarioValue Ref(string path) => binder => binder.Lookup(path);

    /// <summary>
    /// The IR's only way to name a resource: {runId}-{group}-{suffix}, with the
    /// group token the whole group name and no shortening anywhere.
    /// </summary>
    /// <remarks>
    /// That is what makes the name-hygiene convention hold by construction, and
    /// what lets the orphan sweep find anything a crashed run left behind.
    /// </remarks>
    public static ScenarioValue Name(string suffix) =>
        binder => $"{binder.RunId}-{binder.Group}-{suffix}";

    /// <summary>
    /// Joins its parts. A part that is a bare string is a literal; anything
    /// else is an expression that must evaluate to a string.
    /// </summary>
    public static ScenarioValue Concat(params object?[] parts) => binder =>
    {
        var joined = new System.Text.StringBuilder();
        foreach (var part in parts)
        {
            var evaluated = binder.Evaluate(part);
            if (evaluated is not string text)
            {
                throw new ScenarioValueException(
                    $"concat part evaluated to {Documents.Render(evaluated)}, which is not a string");
            }
            joined.Append(text);
        }
        return joined.ToString();
    };

    /// <summary>
    /// <c>$base64</c>: a blob. Its value is the blob's document form — the
    /// canonical standard base64 text, which is also how Documents renders a
    /// MemoryStream out of a response and so how an exported blob sits in the
    /// context bag.
    /// </summary>
    /// <remarks>
    /// That is what lets an <c>equals</c> compare a blob path against it as two
    /// strings. <paramref name="arg"/> is a literal base64 string or an
    /// expression evaluating to one (the generator allows only a <c>$ref</c> to
    /// an exported blob); text that is not canonical standard base64 is an
    /// error, never a second spelling of the same bytes. A blob member does not
    /// take this value as it is: <see cref="Binder.Blob"/> decodes it into the
    /// MemoryStream the request property wants.
    /// </remarks>
    public static ScenarioValue Base64(object? arg) => binder =>
    {
        if (binder.Evaluate(arg) is not string text)
        {
            throw new ScenarioValueException(
                $"$base64 takes base64 text, got {Documents.Render(binder.Evaluate(arg))}");
        }
        DecodeBase64(text);
        return text;
    };

    /// <summary>
    /// Decodes a blob's document form: standard base64 with padding, in its one
    /// canonical spelling.
    /// </summary>
    /// <remarks>
    /// <c>Convert.FromBase64String</c> skips whitespace and does not check the
    /// trailing bits, so the round trip is what refuses a second spelling of the
    /// same bytes. compat/model/testdata/blobs pins what every backend accepts
    /// and refuses.
    /// </remarks>
    public static byte[] DecodeBase64(string text)
    {
        byte[] raw;
        try
        {
            raw = System.Convert.FromBase64String(text);
        }
        catch (FormatException ex)
        {
            throw new ScenarioValueException(
                $"$base64 {Documents.Render(text)} is not standard padded base64: {ex.Message}");
        }
        var canonical = System.Convert.ToBase64String(raw);
        if (canonical != text)
        {
            throw new ScenarioValueException(
                $"$base64 {Documents.Render(text)} is not the canonical spelling of its bytes, which is {Documents.Render(canonical)}");
        }
        return raw;
    }

    /// <summary>Takes element <paramref name="index"/> of a list-valued expression.</summary>
    public static ScenarioValue Index(object? list, int index) => binder =>
    {
        if (binder.Evaluate(list) is not List<object?> items)
        {
            throw new ScenarioValueException(
                $"index applies to a list, got {Documents.Render(binder.Evaluate(list))}");
        }
        if (index < 0 || index >= items.Count)
        {
            throw new ScenarioValueException($"index {index} is past the end of a list of {items.Count}");
        }
        return items[index];
    };
}

/// <summary>A value expression that could not be evaluated.</summary>
internal class ScenarioValueException(string message) : Exception(message);

/// <summary>
/// An unresolvable $ref: an error for the step that carries it, and the one
/// failure a teardown step is allowed to be skipped for.
/// </summary>
internal sealed class ContextPathUnsetException(string path)
    : ScenarioValueException($"context path \"{path}\" is not set")
{
    public string Path { get; } = path;
}

/// <summary>
/// The map from context path ("queue.url") to value that a group's exports fill
/// in and its refs read.
/// </summary>
/// <remarks>
/// It lives on the harness TestContext for exactly one group run, so it has the
/// lifetime the IR gives a group's context — and it is locked because the
/// harness may run a group's tests concurrently, each reaching this through the
/// same TestContext.
/// </remarks>
internal sealed class ContextBag
{
    private readonly Dictionary<string, object?> _values = new(StringComparer.Ordinal);
    private readonly object _gate = new();

    public bool TryGet(string path, out object? value)
    {
        lock (_gate)
        {
            return _values.TryGetValue(path, out value);
        }
    }

    public void Set(string path, object? value)
    {
        lock (_gate)
        {
            _values[path] = value;
        }
    }
}

/// <summary>
/// Resolves the deferred parts of one call's typed request.
/// </summary>
/// <remarks>
/// The emitted code assigns member by member and never checks an error: a
/// failure is recorded here and the whole call is abandoned before anything is
/// sent, which is what keeps an emitted Build body a flat list of assignments.
/// </remarks>
internal sealed class Binder(string runId, string group, ContextBag bag)
{
    private readonly ContextBag _bag = bag;

    public string RunId { get; } = runId;

    public string Group { get; } = group;

    /// <summary>The member whose assignment failed, for failure-message field 4.</summary>
    public string? FailedMember { get; private set; }

    /// <summary>Why it failed, or null while nothing has.</summary>
    public Exception? Error { get; private set; }

    /// <summary>Reads a context path, or throws when it is not set.</summary>
    public object? Lookup(string path) =>
        _bag.TryGet(path, out var value) ? value : throw new ContextPathUnsetException(path);

    /// <summary>
    /// Evaluates one value expression into the C# type the member's modeled
    /// kind names. <paramref name="member"/> is the modeled member name, for
    /// failure-message field 4.
    /// </summary>
    /// <remarks>
    /// The emitted source writes every composite around this call itself —
    /// cmd/compatgen resolved the member's kind from the shape model before
    /// writing the file — so all that is left at run time is the one thing that
    /// cannot be known before the run: what a <c>$ref</c> into the group's
    /// context, or a <c>$name</c> built from the run id, evaluates to. The
    /// conversion into the SDK's own property type is the C# compiler's:
    /// int widens into int?, and a string converts into a ConstantClass enum
    /// through the implicit operator AWSSDK declares.
    /// <para>A type mismatch is recorded and abandons the call rather than
    /// being coerced: "30" is not 30 anywhere else in the IR. The default value
    /// returned on failure is never sent — the call is abandoned before
    /// then.</para>
    /// </remarks>
    public T Bind<T>(string member, object? value)
    {
        if (Error is not null)
        {
            return default!;
        }
        try
        {
            return Convert<T>(Evaluate(value));
        }
        catch (ScenarioValueException ex)
        {
            FailedMember = member;
            Error = ex;
            return default!;
        }
    }

    /// <summary>
    /// Binds a <c>$base64</c> expression into a blob member, which AWSSDK for
    /// .NET types as a MemoryStream.
    /// </summary>
    /// <remarks>
    /// The emitted source calls this only for a deferred blob — a
    /// <c>$base64</c> around a <c>$ref</c> — because a literal's bytes are
    /// written into the source directly. Like <see cref="Bind{T}"/>, a failure
    /// is recorded and abandons the call; the null returned then is never sent.
    /// </remarks>
    public MemoryStream Blob(string member, object? value)
    {
        if (Error is not null)
        {
            return null!;
        }
        try
        {
            if (Evaluate(value) is not string text)
            {
                throw new ScenarioValueException(
                    $"wanted a blob's base64 text, got {Documents.Render(Evaluate(value))}");
            }
            return new MemoryStream(Val.DecodeBase64(text));
        }
        catch (ScenarioValueException ex)
        {
            FailedMember = member;
            Error = ex;
            return null!;
        }
    }

    /// <summary>
    /// Binds an expression into a DateTime property AWSSDK declares where the
    /// model says a long of epoch milliseconds — CloudWatch Logs'
    /// <c>InputLogEvent.Timestamp</c>.
    /// </summary>
    /// <remarks>
    /// The value is the model's: the number every other backend sends as it
    /// stands, and the number <see cref="Documents"/> renders a registered
    /// DateTime back to, so an epoch exported from one response and bound into
    /// the next request round-trips exactly. The SDK turns the DateTime back
    /// into that number on the wire, which the suite's SdkWireFormTests
    /// measure. cmd/compatgen emits this only for a service whose unit is
    /// measured. Like <see cref="Bind{T}"/>, a failure is recorded and abandons
    /// the call; the default returned then is never sent.
    /// </remarks>
    public DateTime EpochMilliseconds(string member, object? value)
    {
        if (Error is not null)
        {
            return default;
        }
        try
        {
            var evaluated = Evaluate(value);
            var milliseconds = AsInteger(evaluated, long.MinValue, long.MaxValue, "long");
            try
            {
                return DateTimeOffset.FromUnixTimeMilliseconds(milliseconds).UtcDateTime;
            }
            catch (ArgumentOutOfRangeException)
            {
                throw new ScenarioValueException(
                    $"wanted epoch milliseconds a DateTime can hold, got {Documents.Render(evaluated)}");
            }
        }
        catch (ScenarioValueException ex)
        {
            FailedMember = member;
            Error = ex;
            return default;
        }
    }

    /// <summary>
    /// Evaluates one value: a ScenarioValue is an expression, a list is a list
    /// of values, a dictionary is a structure or map of values, and anything
    /// else is itself — normalised the way a document is, so a C# literal and a
    /// value read back out of a response compare in the same type system.
    /// </summary>
    public object? Evaluate(object? value)
    {
        switch (value)
        {
            case ScenarioValue expression:
                return Evaluate(expression(this));
            // Only the IR's own composite spellings are walked for nested
            // expressions, because only those can contain one: cmd/compatgen
            // writes an untyped object as Dictionary<string, object?> and an
            // untyped list as object?[]. Anything else is data the SDK or a
            // test handed over, and goes through the document conversion
            // unchanged.
            case IReadOnlyDictionary<string, object?> members:
            {
                var evaluated = new SortedDictionary<string, object?>(StringComparer.Ordinal);
                foreach (var entry in members)
                {
                    evaluated[entry.Key] = Evaluate(entry.Value);
                }
                return evaluated;
            }
            case object?[] array:
                return EvaluateList(array);
            case List<object?> list:
                return EvaluateList(list);
        }
        return Documents.TryConvert(value, out var document) ? document : null;
    }

    private List<object?> EvaluateList(IEnumerable<object?> items)
    {
        var evaluated = new List<object?>();
        foreach (var item in items)
        {
            evaluated.Add(Evaluate(item));
        }
        return evaluated;
    }

    /// <summary>
    /// Narrows an evaluated document value to one scalar C# type.
    /// </summary>
    /// <remarks>
    /// The set is the one cmd/compatgen's <c>scalarType</c> can name, which is
    /// the AWS SDK for .NET's own scalar mapping: byte, short, int, long,
    /// float, double, plus string and bool. An enum reaches this as its
    /// underlying string and is converted by the SDK's implicit operator in the
    /// emitted source, which is what keeps this list of primitives closed.
    /// </remarks>
    private static T Convert<T>(object? value)
    {
        var wanted = typeof(T);
        object converted = wanted switch
        {
            _ when wanted == typeof(string) => AsString(value),
            _ when wanted == typeof(bool) => AsBool(value),
            _ when wanted == typeof(byte) => (byte)AsInteger(value, byte.MinValue, byte.MaxValue, "byte"),
            _ when wanted == typeof(short) => (short)AsInteger(value, short.MinValue, short.MaxValue, "short"),
            _ when wanted == typeof(int) => (int)AsInteger(value, int.MinValue, int.MaxValue, "int"),
            _ when wanted == typeof(long) => AsInteger(value, long.MinValue, long.MaxValue, "long"),
            _ when wanted == typeof(float) => (float)AsNumber(value),
            _ when wanted == typeof(double) => AsNumber(value),
            // Unreachable: the emitter instantiates Bind with nothing else, and
            // adding a type there without a branch here fails this suite's own
            // tests rather than a compat run.
            _ => throw new ScenarioValueException($"internal: no conversion to {wanted.Name}"),
        };
        return (T)converted;
    }

    private static string AsString(object? value) =>
        value as string ?? throw new ScenarioValueException($"wanted a string, got {Documents.Render(value)}");

    private static bool AsBool(object? value) =>
        value is bool flag ? flag : throw new ScenarioValueException($"wanted a boolean, got {Documents.Render(value)}");

    private static long AsInteger(object? value, long min, long max, string kind)
    {
        var number = AsNumber(value);
        if (number != Math.Truncate(number))
        {
            throw new ScenarioValueException($"wanted a whole number, got {Documents.Render(value)}");
        }
        if (number < min || number > max)
        {
            throw new ScenarioValueException($"wanted a number in range for {kind}, got {Documents.Render(value)}");
        }
        return (long)number;
    }

    private static double AsNumber(object? value) =>
        value is double number
            ? number
            : throw new ScenarioValueException($"wanted a number, got {Documents.Render(value)}");
}
