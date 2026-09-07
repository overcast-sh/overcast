using System.Text;
using OvercastCompat.Harness;

namespace OvercastCompat.Scenario;

/// <summary>
/// The six-field failure message every clause reports through.
/// </summary>
/// <remarks>
/// Debuggability is the generated backend's whole cost, and it is paid here:
/// one builder makes every failure message and every clause uses it, so a
/// generated failure carries as much as a hand-written one would.
/// <para>compat/model/README.md § Failure messages fixes the six fields and
/// their order:</para>
/// <list type="number">
/// <item><description>group/test</description></item>
/// <item><description>the operation — of the primary call, or of the clause's
/// own call</description></item>
/// <item><description>the exact params JSON sent, after evaluating every
/// expression</description></item>
/// <item><description>the assertion kind and, for checks/where, the
/// path</description></item>
/// <item><description>expected vs actual</description></item>
/// <item><description>the scenario file and the step index</description></item>
/// </list>
/// <para>Rendered:</para>
/// <code>
/// sqs-gen-queue/SetQueueAttributes: GetQueueAttributes params {"AttributeNames":["All"],"QueueUrl":"http://…"}: readback equalTo at $.Attributes.VisibilityTimeout: expected "60", actual "30" (compat/model/scenarios/sqs.json assert[0].assert)
/// </code>
/// <para>The wording avoids every phrase
/// Runner.LooksUnimplementedWithoutResponse matches on, so this namespace's
/// own prose can never turn an assertion failure into a false
/// "unimplemented". The SDK's error text is quoted verbatim where it is
/// the actual value, which is what lets a genuine 501 still be classified — by
/// <see cref="IComposedFailure.Unimplemented"/>, not by the message.</para>
/// </remarks>
internal sealed class ScenarioFailure : Exception, IComposedFailure
{
    internal ScenarioFailure(string message, bool unimplemented = false)
        : base(message) => Unimplemented = unimplemented;

    /// <summary>
    /// Whether the emulator answered 501. Decided where the raw SDK error was
    /// still in hand, rather than by a substring test over this message: field
    /// 3 is the params JSON, and a run id or a port number in there says
    /// nothing about the status.
    /// </summary>
    public bool Unimplemented { get; }
}

/// <summary>
/// A response together with the call that produced it, so a clause that reads
/// the primary response and a clause that makes its own call both name the
/// right operation and the right params in fields 2 and 3.
/// </summary>
internal sealed class Observed
{
    public required string Op { get; init; }

    public string Params { get; set; } = "";

    public object? Body { get; set; }

    /// <summary>
    /// False when no call succeeded — the primary call of a test that expects
    /// an error. A clause that reads the primary response then has nothing to
    /// read, and says so rather than asserting against an empty document.
    /// </summary>
    public bool Ok { get; set; }
}

/// <summary>Builds the six-field message for one step of one test.</summary>
internal static class Failures
{
    /// <summary>
    /// Caps one field of one failure message.
    /// </summary>
    /// <remarks>
    /// Every failure ends up in a single-line NDJSON <c>error</c> that the
    /// dashboard renders and the report tooling diffs, so a field running to
    /// megabytes costs far more than the diagnosis it buys. A few KiB is enough
    /// to identify a wrong value and to see the start of the list or the
    /// message it came from.
    /// </remarks>
    private const int MaxRendered = 4096;

    public static ScenarioFailure Build(
        string group,
        string test,
        Observed observed,
        string step,
        string kind,
        string path,
        string expected,
        string actual,
        string file,
        bool unimplemented = false)
    {
        var message = new StringBuilder();
        message.Append(group).Append('/').Append(test).Append(": ").Append(observed.Op);
        if (observed.Params.Length > 0)
        {
            message.Append(" params ").Append(observed.Params);
        }
        message.Append(": ").Append(kind);
        if (path.Length > 0)
        {
            message.Append(" at ").Append(path);
        }
        message.Append(": expected ").Append(expected)
            .Append(", actual ").Append(actual)
            .Append(" (").Append(file).Append(' ').Append(step).Append(')');
        return new ScenarioFailure(message.ToString(), unimplemented);
    }

    /// <summary>
    /// Renders a string as a failure message's expected or actual value.
    /// </summary>
    /// <remarks>
    /// An SDK error's text can be multi-line, so it is folded onto one line:
    /// the NDJSON <c>error</c> field is read as a single line by the report
    /// tooling. It is capped too — a transport failure can carry a long chain
    /// of wrapped causes.
    /// </remarks>
    public static string Quote(string text) =>
        Documents.Canonical(Clip(string.Join(" ", text.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries))));

    /// <summary>
    /// Trims a rendered value and says how much it dropped, so the reader knows
    /// the value is not all of what was there.
    /// </summary>
    public static string Clip(string text) =>
        text.Length <= MaxRendered
            ? text
            : $"{text[..MaxRendered]}… ({text.Length - MaxRendered} characters elided)";

    /// <summary>
    /// Prints the list a membership check searched. It is the actual value of
    /// the failure, so it is printed rather than summarised — a generated
    /// failure that says only "no match" cannot be diagnosed without re-running
    /// — but it is capped, for the same reason every other field is.
    /// </summary>
    public static string RenderList(List<object?> list) =>
        list.Count == 0 ? "an empty list" : Clip(Documents.Render(list));

    /// <summary>Prints a where list for a failure message, in path order.</summary>
    public static string RenderWhere(WhereEntry[] where, object?[] values)
    {
        var parts = new List<string>(where.Length);
        for (var i = 0; i < where.Length; i++)
        {
            parts.Add($"{where[i].Path}={Documents.Render(values[i])}");
        }
        return "{" + string.Join(", ", parts) + "}";
    }

    /// <summary>Renders both halves of an error clause for a failure message.</summary>
    public static string AcceptedCodes(ErrorSpec want) =>
        string.Equals(want.Shape, want.Code, StringComparison.Ordinal)
            ? $"error {Documents.Canonical(want.Shape)}"
            : $"error {Documents.Canonical(want.Shape)} or {Documents.Canonical(want.Code)}";
}
