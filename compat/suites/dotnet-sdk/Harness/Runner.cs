using System.Net;
using System.Text.Json;

using Amazon.Runtime;

namespace OvercastCompat.Harness;

public static class Runner
{
    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = null,
        DefaultIgnoreCondition = System.Text.Json.Serialization.JsonIgnoreCondition.WhenWritingNull,
    };

    public static async Task RunSuiteAsync(string suite, string endpoint, string region, IReadOnlyList<TestGroup> groups)
    {
        var started = DateTimeOffset.UtcNow;
        Emit(new
        {
            @event = "run_start",
            suite,
            started_at = started.ToString("O"),
            endpoint,
            version = "1",
            total_tests = groups.Sum(group => group.Tests.Count),
        });

        var slots = ParallelSlots();
        var semaphore = new SemaphoreSlim(slots, slots);
        var tasks = groups.Select(async group =>
        {
            await semaphore.WaitAsync();
            try
            {
                return await RunGroupAsync(suite, endpoint, region, group);
            }
            finally
            {
                semaphore.Release();
            }
        });

        var results = await Task.WhenAll(tasks);
        Emit(new
        {
            @event = "run_end",
            suite,
            passed = results.Sum(result => result.Passed),
            failed = results.Sum(result => result.Failed),
            skipped = results.Sum(result => result.Skipped),
            unimplemented = results.Sum(result => result.Unimplemented),
            duration_ms = (long)(DateTimeOffset.UtcNow - started).TotalMilliseconds,
        });
    }

    /// <summary>
    /// Runs one group: setup, then its tests serially or concurrently, then
    /// teardown. Internal rather than private so the suite's own tests can
    /// drive a group without a live emulator behind it.
    /// </summary>
    internal static async Task<GroupResult> RunGroupAsync(string suite, string endpoint, string region, TestGroup group)
    {
        var context = new TestContext(endpoint, region, Environment.GetEnvironmentVariable("OVERCAST_COMPAT_RUN_ID") ?? "local");
        var result = new GroupResult();

        if (group.Setup is not null)
        {
            try
            {
                await group.Setup(context);
            }
            catch (Exception ex)
            {
                var reason = $"setup failed: {ex.Message}";
                foreach (var test in group.Tests)
                {
                    EmitTestResult(suite, group, test.Name, "skip", 0, reason);
                    result.Skipped++;
                }

                await RunTeardownAsync(group, context);
                return result;
            }
        }

        if (group.Parallel && group.Tests.All(test => test.Depends.Count == 0))
        {
            await RunTestsConcurrentlyAsync(suite, group, context, result);
            await RunTeardownAsync(group, context);
            return result;
        }

        var blocked = new HashSet<string>(StringComparer.Ordinal);
        foreach (var test in group.Tests)
        {
            if (!string.IsNullOrWhiteSpace(test.Skip))
            {
                EmitTestResult(suite, group, test.Name, "skip", 0, test.Skip);
                result.Skipped++;
                blocked.Add(test.Name);
                continue;
            }

            var failedDeps = test.Depends.Where(blocked.Contains).ToList();
            if (failedDeps.Count > 0)
            {
                EmitTestResult(suite, group, test.Name, "skip", 0, $"dependency failed: {string.Join(", ", failedDeps)}");
                result.Skipped++;
                blocked.Add(test.Name);
                continue;
            }

            var started = DateTimeOffset.UtcNow;
            try
            {
                await test.Fn(context);
                EmitTestResult(suite, group, test.Name, "pass", (long)(DateTimeOffset.UtcNow - started).TotalMilliseconds, null);
                result.Passed++;
            }
            catch (Exception ex)
            {
                var status = IsUnimplemented(ex) ? "unimplemented" : "fail";
                EmitTestResult(suite, group, test.Name, status, (long)(DateTimeOffset.UtcNow - started).TotalMilliseconds, ex.Message);
                if (status == "unimplemented")
                {
                    result.Unimplemented++;
                }
                else
                {
                    result.Failed++;
                }
                blocked.Add(test.Name);
            }
        }

        await RunTeardownAsync(group, context);
        return result;
    }

    /// <summary>
    /// Runs a group's tests concurrently and emits their results in
    /// declaration order once all of them are in.
    /// </summary>
    /// <remarks>
    /// Only a generated probe group is marked parallel
    /// (registry.generated.json's "parallel"): its tests have no setup, no
    /// teardown, no exports and no depends, so nothing orders them and no test
    /// can observe another's outcome. Both halves of the caller's condition are
    /// load-bearing - this path cannot express the dependency gate, because it
    /// would have to decide what to skip from outcomes that have not happened
    /// yet, so a group declaring one is run serially even where the registry
    /// says parallel.
    /// <para>Emitting in declaration order rather than as each finishes is what
    /// keeps this stream identical to the serial path's, test for test. The
    /// dashboard, the baseline and the flake detector all read it, and a result
    /// order that depended on which call answered first would be a new source
    /// of diff noise for no benefit.</para>
    /// </remarks>
    private static async Task RunTestsConcurrentlyAsync(string suite, TestGroup group, TestContext context, GroupResult result)
    {
        var slots = ParallelSlots();
        using var semaphore = new SemaphoreSlim(slots, slots);
        var outcomes = await Task.WhenAll(group.Tests.Select(async test =>
        {
            if (!string.IsNullOrWhiteSpace(test.Skip))
            {
                return ("skip", 0L, (string?)test.Skip);
            }
            await semaphore.WaitAsync();
            try
            {
                var started = DateTimeOffset.UtcNow;
                try
                {
                    await test.Fn(context);
                    return ("pass", (long)(DateTimeOffset.UtcNow - started).TotalMilliseconds, (string?)null);
                }
                catch (Exception ex)
                {
                    return (IsUnimplemented(ex) ? "unimplemented" : "fail",
                        (long)(DateTimeOffset.UtcNow - started).TotalMilliseconds, ex.Message);
                }
            }
            finally
            {
                semaphore.Release();
            }
        }));

        for (var i = 0; i < group.Tests.Count; i++)
        {
            var (status, duration, error) = outcomes[i];
            EmitTestResult(suite, group, group.Tests[i].Name, status, duration, error);
            switch (status)
            {
                case "pass":
                    result.Passed++;
                    break;
                case "skip":
                    result.Skipped++;
                    break;
                case "unimplemented":
                    result.Unimplemented++;
                    break;
                default:
                    result.Failed++;
                    break;
            }
        }
    }

    /// <summary>
    /// How many things this suite may do at once - groups in RunSuiteAsync, and
    /// the tests of one parallel group.
    /// </summary>
    internal static int ParallelSlots() =>
        int.TryParse(Environment.GetEnvironmentVariable("OVERCAST_COMPAT_PARALLEL_SLOTS"), out var configured) && configured > 0
            ? configured
            : 8;

    private static async Task RunTeardownAsync(TestGroup group, TestContext context)
    {
        if (group.Teardown is null)
        {
            return;
        }

        try
        {
            await group.Teardown(context);
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"[dotnet-sdk] teardown failed for {group.Name}: {ex.Message}");
        }
    }

    /// <summary>
    /// Whether an exception signals a 501 / not-implemented response from the
    /// Overcast emulator.
    /// </summary>
    /// <remarks>
    /// A caller that has already read the raw SDK error and classified it is
    /// believed: an <see cref="IComposedFailure"/> states the answer itself.
    /// Everything else is decided from the <b>response the SDK's exception
    /// carries</b> - <see cref="ClassifyResponse"/> - and only an exception
    /// carrying none reaches the substring heuristic. The prose was the whole
    /// rule for a hand-written group until #1924, and a 400 was enough to
    /// defeat it: the sibling go-sdk suite reported a test that asserts an
    /// InvalidRequestException as unimplemented on one CI run whose request id
    /// happened to contain "501", flipping a gated baseline row and failing an
    /// unrelated pull request.
    /// </remarks>
    public static bool IsUnimplemented(Exception exception)
    {
        for (Exception? link = exception; link is not null; link = link.InnerException)
        {
            if (link is IComposedFailure composed)
            {
                return composed.Unimplemented;
            }
            if (link is AmazonServiceException service && ClassifyResponse(service) is bool decided)
            {
                return decided;
            }
        }
        return LooksUnimplementedWithoutResponse(exception.ToString());
    }

    /// <summary>
    /// Decides, from the response the SDK parsed, whether the emulator refused
    /// the operation as unimplemented.
    /// </summary>
    /// <remarks>
    /// Two things say so, and both are facts of the response rather than of its
    /// wording: HTTP 501; and an error <b>code</b> of NotImplemented or
    /// UnknownOperationException, by equality. AWS - and Overcast - answer a
    /// target naming no modeled operation with the latter at HTTP 400, so the
    /// status alone would miss it.
    /// <para>Returns null when the exception states neither - the SDK raised it
    /// without a response to read - which is the one case a caller may fall
    /// back to the text.</para>
    /// </remarks>
    public static bool? ClassifyResponse(AmazonServiceException service)
    {
        if (service.StatusCode == 0 && string.IsNullOrEmpty(service.ErrorCode))
        {
            return null;
        }
        return service.StatusCode == HttpStatusCode.NotImplemented
            || service.ErrorCode is "NotImplemented" or "UnknownOperationException";
    }

    /// <summary>
    /// The substring heuristic, for an exception carrying <b>no response</b> -
    /// the SDK failed before or after the exchange, so there is nothing else to
    /// read. Pass it what the SDK said and nothing else.
    /// </summary>
    /// <remarks>
    /// Never right for an exception that reached the wire: the response states
    /// the status, and "501" appears in request ids, ARNs, resource names and
    /// port numbers. <see cref="ClassifyResponse"/> answers that case.
    /// </remarks>
    public static bool LooksUnimplementedWithoutResponse(string text) =>
        text.Contains("501", StringComparison.OrdinalIgnoreCase)
        || text.Contains("NotImplemented", StringComparison.OrdinalIgnoreCase)
        || text.Contains("UnknownOperationException", StringComparison.OrdinalIgnoreCase)
        || text.Contains("Unknown action", StringComparison.OrdinalIgnoreCase)
        || text.Contains("not implemented", StringComparison.OrdinalIgnoreCase);

    private static void EmitTestResult(string suite, TestGroup group, string test, string status, long durationMs, string? error)
    {
        Emit(new
        {
            @event = "test_result",
            suite,
            service = group.Service,
            group = group.Name,
            test,
            status,
            duration_ms = durationMs,
            error,
        });
    }

    private static readonly object EmitLock = new();

    private static void Emit(object value)
    {
        lock (EmitLock)
        {
            Console.Out.WriteLine(JsonSerializer.Serialize(value, JsonOptions));
            Console.Out.Flush();
        }
    }

    internal sealed class GroupResult
    {
        public int Passed { get; set; }
        public int Failed { get; set; }
        public int Skipped { get; set; }
        public int Unimplemented { get; set; }
    }
}
