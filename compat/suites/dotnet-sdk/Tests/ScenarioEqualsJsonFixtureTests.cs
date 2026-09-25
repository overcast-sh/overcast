using System.Text.Json;
using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The shared <c>equalsJSON</c> conformance fixture,
/// compat/model/testdata/equalsjson.
/// </summary>
/// <remarks>
/// The emitted source hands this runtime <c>Check.EqualsJson(path,
/// operandText)</c>, so every case runs the way a generated group runs it: the
/// fixture's <c>expected</c> is the JSON text the emitter would write, given to
/// <see cref="Check.EqualsJson"/>, and its <c>actual</c> is the document value
/// a path resolved to. <c>decode</c> pins the percent-decoding, <c>holds</c>
/// and <c>fails</c> the check, and <c>invalidExpected</c> the operands the
/// factory refuses. Found and required exactly as
/// <see cref="ScenarioNowFixtureTests"/> finds its fixture.
/// </remarks>
public sealed class ScenarioEqualsJsonFixtureTests
{
    private const string RequiredEnvVar = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

    private static readonly string[] Sections = ["decode", "holds", "fails", "invalidExpected"];

    /// <summary>What the check makes of a fixture case: null when it holds, else the actual field.</summary>
    private static string? Evaluate(JsonElement c)
    {
        var check = Check.EqualsJson("$.Document", c.GetProperty("expected").GetRawText());
        Assert.Equal(CheckKind.EqualsJson, check.Kind);
        var actual = EqualsJsonCheck.FromElement(c.GetProperty("actual"));
        return EqualsJsonCheck.Mismatch(check.Value!, actual, resolved: true);
    }

    [Fact]
    public void SharedEqualsJsonFixture()
    {
        var file = TryFixtureFile();
        if (file is null)
        {
            if (Environment.GetEnvironmentVariable(RequiredEnvVar) == "1")
            {
                throw new InvalidOperationException(
                    $"{RequiredEnvVar}=1 but compat/model/testdata/equalsjson/equalsjson.json was not found walking up from "
                    + AppContext.BaseDirectory + " — this suite's fixture test must run from a full checkout");
            }
            Console.Error.WriteLine(
                "[dotnet-sdk] compat/model/testdata/equalsjson not found; skipping the shared equalsJSON fixture "
                + $"(set {RequiredEnvVar}=1 to make this fatal instead)");
            return;
        }

        using var document = JsonDocument.Parse(File.ReadAllText(file));
        var root = document.RootElement;
        foreach (var property in root.EnumerateObject())
        {
            Assert.True(property.Name == "$comment" || Sections.Contains(property.Name), $"unknown key {property.Name} in {file}");
        }
        foreach (var section in Sections)
        {
            Assert.True(
                root.TryGetProperty(section, out var cases) && cases.GetArrayLength() > 0,
                $"the equalsJSON fixture may not be skipped by emptying {section}");
        }

        var failures = new List<string>();
        foreach (var c in root.GetProperty("decode").EnumerateArray())
        {
            RequireKeys(c, "name", "text", "decoded");
            var got = EqualsJsonCheck.PercentDecode(c.GetProperty("text").GetString()!);
            if (got != c.GetProperty("decoded").GetString())
            {
                failures.Add($"decode/{Name(c)}: got {Documents.Render(got)}");
            }
        }
        foreach (var c in root.GetProperty("holds").EnumerateArray())
        {
            RequireKeys(c, "name", "actual", "expected");
            if (Evaluate(c) is { } actual)
            {
                failures.Add($"holds/{Name(c)}: failed with actual {actual}");
            }
        }
        foreach (var c in root.GetProperty("fails").EnumerateArray())
        {
            RequireKeys(c, "name", "actual", "expected");
            if (Evaluate(c) is null)
            {
                failures.Add($"fails/{Name(c)}: held");
            }
        }
        foreach (var c in root.GetProperty("invalidExpected").EnumerateArray())
        {
            RequireKeys(c, "name", "expected");
            // Handed the operand's JSON serialization, so a JSON string holding
            // an object's text reaches the factory as a string and is refused.
            try
            {
                Check.EqualsJson("$.Document", c.GetProperty("expected").GetRawText());
                failures.Add($"invalidExpected/{Name(c)}: accepted");
            }
            catch (ScenarioValueException)
            {
            }
        }
        // Every case is run before any is reported, so one run names them all.
        Assert.True(failures.Count == 0, string.Join("\n", failures));
    }

    [Fact]
    public void FailureFields()
    {
        var operand = Check.EqualsJson("$.D", "{\"a\":1}").Value!;
        Assert.Equal("equalsJSON {\"a\":1}", EqualsJsonCheck.Expected(operand));
        Assert.Equal("document {\"a\":1,\"b\":2}", EqualsJsonCheck.Mismatch(operand, "{\"b\":2,\"a\":1}", true));
        Assert.Equal("not a JSON document: \"not a policy\"", EqualsJsonCheck.Mismatch(operand, "not a policy", true));
        Assert.Equal("not a JSON document: 5", EqualsJsonCheck.Mismatch(operand, 5.0, true));
        Assert.Equal(Documents.MissingValue, EqualsJsonCheck.Mismatch(operand, null, false));
        // An SDK document member is already in document form, and -0 is 0.
        Assert.Null(EqualsJsonCheck.Mismatch(operand,
            new SortedDictionary<string, object?>(StringComparer.Ordinal) { ["a"] = 1.0 }, true));
        Assert.Null(EqualsJsonCheck.Mismatch(Check.EqualsJson("$.D", "[0]").Value!, "[-0.0]", true));
    }

    private static string Name(JsonElement c) => c.GetProperty("name").GetString()!;

    /// <summary>The strict reader: a key this test does not know is a fixture it cannot be trusted to run.</summary>
    private static void RequireKeys(JsonElement c, params string[] keys)
    {
        foreach (var property in c.EnumerateObject())
        {
            Assert.True(keys.Contains(property.Name), $"unknown key {property.Name} in {c.GetRawText()}");
        }
        foreach (var key in keys)
        {
            Assert.True(c.TryGetProperty(key, out _), $"missing key {key} in {c.GetRawText()}");
        }
    }

    private static string? TryFixtureFile()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null)
        {
            var candidate = Path.Combine(directory.FullName, "compat", "model", "testdata", "equalsjson", "equalsjson.json");
            if (File.Exists(candidate))
            {
                return candidate;
            }
            directory = directory.Parent;
        }
        return null;
    }
}
