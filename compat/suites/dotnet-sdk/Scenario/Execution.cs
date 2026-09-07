using System.Text.RegularExpressions;
using OvercastCompat.Harness;

namespace OvercastCompat.Scenario;

/// <summary>
/// One group-scoped run of one test, setup or teardown: the calls it makes and
/// the closed assertion set it evaluates over their responses.
/// </summary>
/// <remarks>
/// This file's counterparts are compat/suites/go-sdk/internal/scenario's
/// exec.go and assert.go: the three evaluate the same closed assertion set
/// (compat/model/README.md § Assertions) against their own backend's response
/// shape and are not line-for-line alike, but a change to how one assertion
/// kind is evaluated here usually needs a matching change there — change all or
/// none.
/// </remarks>
internal sealed class Execution(ScenarioGroup group, TestContext context, ContextBag bag, string test)
{
    /// <summary>
    /// The IR's own names for the assertion kinds and checks. A failure message
    /// quotes the IR's vocabulary, not C#'s, so field 4 reads the same in every
    /// backend.
    /// </summary>
    private static readonly Dictionary<ClauseKind, string> ClauseNames = new()
    {
        [ClauseKind.ResponseField] = "responseField",
        [ClauseKind.Readback] = "readback",
        [ClauseKind.ListContains] = "listContains",
        [ClauseKind.Absent] = "absent",
        [ClauseKind.ErrorCode] = "errorCode",
        [ClauseKind.Eventually] = "eventually",
    };

    private static readonly Dictionary<CheckKind, string> CheckNames = new()
    {
        [CheckKind.NonEmpty] = "nonEmpty",
        [CheckKind.IsList] = "isList",
        [CheckKind.EqualTo] = "equals",
        [CheckKind.Matches] = "matches",
        [CheckKind.Missing] = "missing",
    };

    /// <summary>A fresh binder for one call, so a value that failed to bind in one call cannot suppress the next one's assignments.</summary>
    private Binder NewBinder() => new(context.RunId, group.Name, bag);

    // -- Calls ---------------------------------------------------------------

    /// <summary>
    /// Builds a call's request and sends it, keeping the SDK's own error
    /// separate from this namespace's.
    /// </summary>
    /// <remarks>
    /// The returned exception is the SDK's, for the two clauses that must
    /// inspect it (errorCode, and absent's error form). Everything
    /// attributable to the scenario before anything was sent — an unresolvable
    /// ref, a value that does not fit the request property — is thrown as a
    /// <see cref="ScenarioFailure"/> instead.
    /// <para>The returned <see cref="Observed"/> carries the exact params JSON
    /// sent, so every failure downstream of it quotes what went on the
    /// wire.</para>
    /// </remarks>
    public async Task<(Observed Observed, Exception? SdkError)> CallRawAsync(ScenarioCall call, string step)
    {
        var observed = new Observed { Op = call.Op };

        var binder = NewBinder();
        var request = call.Build(binder);
        if (binder.Error is not null)
        {
            // Nothing was sent, so field 3 shows the params as the scenario
            // file writes them rather than a half-built request that never
            // existed.
            observed.Params = call.Params;
            if (binder.Error is ContextPathUnsetException unset)
            {
                throw Fail(observed, step, "params", unset.Path, "the context path to be set", "<unset>");
            }
            throw Fail(observed, step, "params", binder.FailedMember ?? "", "a value the request property accepts", Failures.Quote(binder.Error.Message));
        }

        // Failure-message field 3: the built request as the document it will be
        // serialized from. That is comparable with the same field in the three
        // interpreters' messages — they print the params document too.
        observed.Params = Documents.TryConvert(request, out var sent)
            ? Documents.Canonical(sent)
            : Documents.Canonical(new SortedDictionary<string, object?>(StringComparer.Ordinal));

        try
        {
            var response = await call.SendAsync(request);
            observed.Body = Documents.TryConvert(response, out var body) ? body : null;
            observed.Ok = true;
            return (observed, null);
        }
        catch (Exception ex)
        {
            return (observed, ex);
        }
    }

    /// <summary>CallRawAsync with the SDK's error turned into a failure — what every clause that simply needs the call to succeed wants.</summary>
    public async Task<Observed> CallAsync(ScenarioCall call, string step)
    {
        var (observed, sdkError) = await CallRawAsync(call, step);
        if (sdkError is not null)
        {
            throw FailedCall(observed, step, sdkError);
        }
        return observed;
    }

    /// <summary>CallAsync plus its exports, for a setup or teardown step, whose whole purpose is the context values it leaves behind.</summary>
    public async Task<Observed> InvokeAsync(ScenarioCall call, string step)
    {
        var observed = await CallAsync(call, step);
        ApplyExports(call, observed, step);
        return observed;
    }

    /// <summary>
    /// Writes a call's response paths into the context bag.
    /// </summary>
    /// <remarks>
    /// An export path that does not resolve is an error for the step that
    /// carries it: the value a later step will reference is not there, and
    /// failing here names the path instead of failing later with an
    /// unresolvable reference.
    /// </remarks>
    public void ApplyExports(ScenarioCall call, Observed observed, string step)
    {
        if (call.Export is null)
        {
            return;
        }
        foreach (var path in call.Export.Keys.OrderBy(key => key, StringComparer.Ordinal))
        {
            var responsePath = call.Export[path];
            bool resolved;
            object? value;
            try
            {
                resolved = Paths.TryResolve(observed.Body, responsePath, out value);
            }
            catch (ScenarioPathException ex)
            {
                throw Fail(observed, step, "export", responsePath, "a well-formed response path", Failures.Quote(ex.Message));
            }
            if (!resolved)
            {
                throw Fail(observed, step, "export", responsePath, $"a value to export into {Documents.Canonical(path)}", Documents.MissingValue);
            }
            bag.Set(path, value);
        }
    }

    // -- Assertions ----------------------------------------------------------

    /// <summary>Evaluates one clause. <paramref name="primary"/> is the test's own response, which responseField and a call-less list clause read.</summary>
    public async Task AssertAsync(Clause clause, Observed primary, string step)
    {
        switch (clause.Kind)
        {
            case ClauseKind.ResponseField:
                CheckAll(primary, clause.Checks, ClauseNames[clause.Kind], step);
                return;

            case ClauseKind.Readback:
            {
                var observed = await CallAsync(clause.Call!, step);
                CheckAll(observed, clause.Checks, ClauseNames[clause.Kind], step);
                // A clause's exports are applied only once the clause holds:
                // inside an eventually, the failing attempts must not leave a
                // half-read response in the context bag for the next clause to
                // reference.
                ApplyExports(clause.Call!, observed, step);
                return;
            }

            case ClauseKind.ListContains:
            case ClauseKind.Absent:
                await AssertListAsync(clause, primary, step);
                return;

            case ClauseKind.Eventually:
                await EventuallyAsync(clause, primary, step);
                return;

            default:
                // errorCode is checked against the primary call in
                // RunTestAsync; a nested one is not representable (eventually
                // wraps only readback/listContains/absent).
                throw Fail(primary, step, "errorCode", "", "an errorCode clause on the test's own call", "a nested one");
        }
    }

    /// <summary>Evaluates listContains and both forms of absent.</summary>
    private async Task AssertListAsync(Clause clause, Observed primary, string step)
    {
        var kind = ClauseNames[clause.Kind];

        // absent's error form: the call must fail with the named error.
        if (clause.Kind == ClauseKind.Absent && clause.Error is not null)
        {
            var (attempted, sdkError) = await CallRawAsync(clause.Call!, step);
            if (sdkError is null)
            {
                throw Fail(attempted, step, kind, "", Failures.AcceptedCodes(clause.Error), "<no error>");
            }
            if (!Errors.Matches(sdkError, clause.Error))
            {
                throw Fail(attempted, step, kind, "", Failures.AcceptedCodes(clause.Error), Failures.Quote(sdkError.Message));
            }
            return;
        }

        // The list forms read the clause's own call when it has one, else the
        // test's own response.
        var observed = primary;
        if (clause.Call is not null)
        {
            observed = await CallAsync(clause.Call, step);
        }
        if (!observed.Ok)
        {
            throw Fail(observed, step, kind, clause.ItemsPath, "a response to read the list from", "<no response>");
        }

        bool resolved;
        object? items;
        try
        {
            resolved = Paths.TryResolve(observed.Body, clause.ItemsPath, out items);
        }
        catch (ScenarioPathException ex)
        {
            throw Fail(observed, step, kind, clause.ItemsPath, "a well-formed items path", Failures.Quote(ex.Message));
        }
        var list = new List<object?>();
        if (resolved)
        {
            if (items is not List<object?> found)
            {
                throw Fail(observed, step, kind, clause.ItemsPath, "a list", Documents.Render(items));
            }
            list = found;
        }
        // A missing list counts as empty: several AWS services omit an empty
        // list member rather than serializing [].

        var (matched, wanted) = MatchItem(observed, list, clause.Where, kind, step);

        if (clause.Kind == ClauseKind.ListContains)
        {
            if (matched < 0)
            {
                throw Fail(observed, step, kind, clause.ItemsPath,
                    $"an item matching {Failures.RenderWhere(clause.Where, wanted)}", Failures.RenderList(list));
            }
        }
        else if (matched >= 0)
        {
            throw Fail(observed, step, kind, clause.ItemsPath,
                $"no item matching {Failures.RenderWhere(clause.Where, wanted)}", Documents.Render(list[matched]));
        }

        // The clause held. A list clause may carry a call with exports of its
        // own, and they are applied on the same terms as a read-back's: only
        // once the clause holds.
        if (clause.Call is not null)
        {
            ApplyExports(clause.Call, observed, step);
        }
    }

    /// <summary>
    /// The index of the first item satisfying every where entry, or -1,
    /// together with the evaluated expected values so a failure message can
    /// print them.
    /// </summary>
    /// <remarks>
    /// An unevaluatable where value (an unresolvable ref) is an error for the
    /// step rather than a non-match.
    /// </remarks>
    private (int Matched, object?[] Wanted) MatchItem(Observed observed, List<object?> list, WhereEntry[] where, string kind, string step)
    {
        var binder = NewBinder();
        var wanted = new object?[where.Length];
        for (var i = 0; i < where.Length; i++)
        {
            try
            {
                wanted[i] = binder.Evaluate(where[i].Value);
            }
            catch (ScenarioValueException ex)
            {
                throw Fail(observed, step, kind, where[i].Path, "the where value to evaluate", Failures.Quote(ex.Message));
            }
        }

        for (var i = 0; i < list.Count; i++)
        {
            var all = true;
            for (var j = 0; j < where.Length; j++)
            {
                // "$" is the item itself, which is how a list of strings is
                // matched: new WhereEntry("$", Val.Ref("queue.url")).
                bool resolved;
                object? got;
                try
                {
                    resolved = Paths.TryResolve(list[i], where[j].Path, out got);
                }
                catch (ScenarioPathException ex)
                {
                    throw Fail(observed, step, kind, where[j].Path, "a well-formed where path", Failures.Quote(ex.Message));
                }
                if (!resolved || !Documents.JsonEqual(got, wanted[j]))
                {
                    all = false;
                    break;
                }
            }
            if (all)
            {
                return (i, wanted);
            }
        }
        return (-1, wanted);
    }

    /// <summary>
    /// Retries the inner clause up to MaxAttempts times, waiting DelayMs
    /// between attempts and no longer.
    /// </summary>
    /// <remarks>
    /// The last failure is the reported one, and a read-back inside applies its
    /// exports only on the attempt that passes — which AssertAsync already
    /// guarantees, because it applies them only when the checks hold.
    /// <para>That last failure is reported behind the budget that was spent on
    /// it. Bare, it is indistinguishable from a clause evaluated once, and the
    /// two want opposite fixes: a real disagreement, or a poll budget too short
    /// for how long this service takes to settle. Every backend words the
    /// prefix identically, so a generated group's give-up reads the same
    /// whichever suite reports it. An inner 501 keeps its classification: the
    /// give-up carries the last failure's own Unimplemented flag.</para>
    /// </remarks>
    private async Task EventuallyAsync(Clause clause, Observed primary, string step)
    {
        var attempts = Math.Max(clause.MaxAttempts, 1);
        var inner = step + ".assert";
        ScenarioFailure? last = null;
        for (var attempt = 0; attempt < attempts; attempt++)
        {
            if (attempt > 0 && clause.DelayMs > 0)
            {
                await Task.Delay(clause.DelayMs);
            }
            try
            {
                await AssertAsync(clause.Inner!, primary, inner);
                return;
            }
            catch (ScenarioFailure failure)
            {
                last = failure;
            }
        }
        throw new ScenarioFailure(
            $"eventually gave up after {attempts} attempt(s) {clause.DelayMs}ms apart; last failure: {last!.Message}",
            last.Unimplemented);
    }

    // -- Checks --------------------------------------------------------------

    /// <summary>
    /// Evaluates every check of a clause against one response, in the order the
    /// emitter wrote them — which is path order, so a failure message is the
    /// same on every run.
    /// </summary>
    private void CheckAll(Observed observed, Check[] checks, string kind, string step)
    {
        if (!observed.Ok)
        {
            throw Fail(observed, step, kind, "", "a response to check", "<no response>");
        }
        foreach (var check in checks)
        {
            CheckOne(observed, check, kind, step);
        }
    }

    /// <summary>Evaluates one check against one response path.</summary>
    private void CheckOne(Observed observed, Check check, string kind, string step)
    {
        var label = kind + " " + CheckNames[check.Kind];
        bool resolved;
        object? got;
        try
        {
            resolved = Paths.TryResolve(observed.Body, check.Path, out got);
        }
        catch (ScenarioPathException ex)
        {
            throw Fail(observed, step, label, check.Path, "a well-formed path", Failures.Quote(ex.Message));
        }

        ScenarioFailure Mismatch(string expected) =>
            Fail(observed, step, label, check.Path, expected, Documents.RenderResolved(got, resolved));

        switch (check.Kind)
        {
            case CheckKind.Missing:
                if (resolved)
                {
                    throw Mismatch("the path not to resolve");
                }
                return;

            case CheckKind.IsList:
                // True of a present list, empty or not, and of an absent
                // member: several AWS services omit an empty list rather than
                // serializing []. A present value that is not a list still
                // fails.
                if (resolved && got is not List<object?>)
                {
                    throw Mismatch("a list, or no such member");
                }
                return;

            case CheckKind.NonEmpty:
                if (!resolved || Documents.IsEmpty(got))
                {
                    throw Mismatch("a non-empty value");
                }
                return;

            case CheckKind.EqualTo:
            {
                object? want;
                try
                {
                    want = NewBinder().Evaluate(check.Value);
                }
                catch (ScenarioValueException ex)
                {
                    throw Fail(observed, step, label, check.Path, "the expected value to evaluate", Failures.Quote(ex.Message));
                }
                if (!resolved || !Documents.JsonEqual(got, want))
                {
                    throw Mismatch(Documents.Render(want));
                }
                return;
            }

            default:
            {
                var pattern = check.Value as string ?? "";
                Regex regex;
                try
                {
                    regex = new Regex(pattern);
                }
                catch (ArgumentException ex)
                {
                    // The model states its patterns in RE2, which .NET's engine
                    // is a superset of, so this is nearly unreachable — but a
                    // pattern the engine will not compile is a normal six-field
                    // mismatch in every backend (compat/model/README.md
                    // § Assertions), never an exception out of the evaluator,
                    // and the phrase is the same in all of them.
                    throw Fail(observed, step, label, check.Path,
                        $"pattern {pattern}", Failures.Quote("unsupported pattern: " + ex.Message));
                }
                if (!resolved || got is not string text || !regex.IsMatch(text))
                {
                    throw Mismatch($"a string matching {Documents.Canonical(pattern)}");
                }
                return;
            }
        }
    }

    // -- Failures ------------------------------------------------------------

    /// <summary>Builds a failure for one step of one test.</summary>
    public ScenarioFailure Fail(Observed observed, string step, string kind, string path, string expected, string actual) =>
        Failures.Build(group.Name, test, observed, step, kind, path, expected, actual, group.File);

    /// <summary>
    /// Reports a call that should have succeeded. The SDK's error text is
    /// quoted verbatim as the actual value, so the reader sees what the SDK
    /// said.
    /// </summary>
    /// <remarks>
    /// Classification is decided here rather than left to the message: this is
    /// the one place holding the <em>raw</em> SDK error, and a composed failure
    /// message is not something Runner.IsUnimplemented may be pointed at —
    /// it embeds the params JSON, where a run id or a port puts a "501" that
    /// means nothing. So a 501 is stated on the failure, and every other
    /// failure carries no such flag and is a plain fail.
    /// </remarks>
    public ScenarioFailure FailedCall(Observed observed, string step, Exception sdkError) =>
        Failures.Build(group.Name, test, observed, step, "call", "", "the call to succeed",
            Failures.Quote(sdkError.Message), group.File, Errors.IsUnimplementedResponse(sdkError));
}
