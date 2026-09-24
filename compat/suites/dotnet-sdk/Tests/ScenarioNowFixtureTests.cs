using System.Text.Json;
using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The shared <c>$now</c> conformance fixture, compat/model/testdata/now.
/// </summary>
/// <remarks>
/// <c>$now</c> is the client's clock when a call is made, in epoch
/// milliseconds, plus an offset (compat/model/README.md § Values). The emitted
/// source hands this runtime <c>Val.Now(unit, offsetMillis)</c> — never the
/// JSON — so the fixture's <c>invalid</c> spellings are the generator's to
/// refuse, and this runs the rest: each valid spelling binds to the fixture's
/// value into a long and into the DateTime AWSSDK makes of one, every invalid
/// argument is refused naming the member, and the clock is read once per call
/// however many <c>$now</c>s the call holds. Found and required exactly as
/// <see cref="ScenarioBlobFixtureTests"/> finds its fixture.
/// </remarks>
public sealed class ScenarioNowFixtureTests
{
    private const string RequiredEnvVar = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

    /// <summary>A clock that moves on by <paramref name="tick"/> every time it is read.</summary>
    private static Func<long> Ticking(long start, long tick)
    {
        var next = start;
        return () =>
        {
            var reading = next;
            next += tick;
            return reading;
        };
    }

    private static Binder BinderWith(Func<long> clock) => new("oc-test", "logs-events", new ContextBag(), clock);

    [Fact]
    public void SharedNowFixture()
    {
        var file = TryFixtureFile();
        if (file is null)
        {
            if (Environment.GetEnvironmentVariable(RequiredEnvVar) == "1")
            {
                throw new InvalidOperationException(
                    $"{RequiredEnvVar}=1 but compat/model/testdata/now/now.json was not found walking up from "
                    + AppContext.BaseDirectory + " — this suite's fixture test must run from a full checkout");
            }
            Console.Error.WriteLine(
                "[dotnet-sdk] compat/model/testdata/now not found; skipping the shared $now fixture "
                + $"(set {RequiredEnvVar}=1 to make this fatal instead)");
            return;
        }

        using var document = JsonDocument.Parse(File.ReadAllText(file));
        var root = document.RootElement;
        foreach (var property in root.EnumerateObject())
        {
            Assert.Contains(property.Name, new[] { "$comment", "instant", "valid", "invalid", "invalidArguments", "call" });
        }
        Assert.True(
            root.GetProperty("valid").GetArrayLength() > 0
            && root.GetProperty("invalid").GetArrayLength() > 0
            && root.GetProperty("invalidArguments").GetArrayLength() > 0,
            "the $now fixture may not be skipped by emptying it");
        var instant = root.GetProperty("instant").GetInt64();
        var call = root.GetProperty("call");
        var tick = call.GetProperty("tickMillis").GetInt64();

        foreach (var c in root.GetProperty("valid").EnumerateArray())
        {
            var now = c.GetProperty("now");
            var unit = now.GetProperty("unit").GetString()!;
            var offset = now.TryGetProperty("offsetMillis", out var o) ? o.GetInt64() : 0L;
            var want = c.GetProperty("value").GetInt64();

            var binder = BinderWith(Ticking(instant, tick));
            Assert.Equal(want, binder.Bind<long>("timestamp", Val.Now(unit, offset)));
            Assert.Null(binder.Error);

            binder = BinderWith(Ticking(instant, tick));
            Assert.Equal(
                DateTimeOffset.FromUnixTimeMilliseconds(want).UtcDateTime,
                binder.EpochMilliseconds("timestamp", Val.Now(unit, offset)));
            Assert.Null(binder.Error);
        }

        foreach (var c in root.GetProperty("invalidArguments").EnumerateArray())
        {
            var binder = BinderWith(Ticking(instant, 0));
            binder.Bind<long>("timestamp", Val.Now(c.GetProperty("unit").GetString()!, c.GetProperty("offsetMillis").GetInt64()));
            Assert.NotNull(binder.Error);
            Assert.Equal("timestamp", binder.FailedMember);
        }

        // One reading per call: the emitted request for the fixture's call binds
        // both events through one binder, and the next call's binder reads the
        // clock afresh.
        var clock = Ticking(instant, tick);
        var first = BinderWith(clock);
        var sent = call.GetProperty("sent").GetProperty("logEvents");
        Assert.Equal(sent[0].GetProperty("timestamp").GetInt64(), first.Bind<long>("logEvents", Val.Now("epochMillis", -1L)));
        Assert.Equal(sent[1].GetProperty("timestamp").GetInt64(), first.Bind<long>("logEvents", Val.Now("epochMillis", 0L)));
        Assert.Equal(instant + tick, BinderWith(clock).Bind<long>("timestamp", Val.Now("epochMillis", 0L)));
    }

    private static string? TryFixtureFile()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null)
        {
            var candidate = Path.Combine(directory.FullName, "compat", "model", "testdata", "now", "now.json");
            if (File.Exists(candidate))
            {
                return candidate;
            }
            directory = directory.Parent;
        }
        return null;
    }
}
