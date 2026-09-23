using System.Text.Json;
using Amazon.Kinesis.Model;
using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The shared blob-value conformance fixture, compat/model/testdata/blobs.
/// </summary>
/// <remarks>
/// Every backend agrees on one document form for a blob — its canonical
/// standard base64 text (compat/model/README.md § Values) — and this is where
/// dotnet-sdk proves it against the same cases every other suite reads:
/// <c>$base64</c> binds to exactly the fixture's bytes, whether written as a
/// literal or as a <c>$ref</c> to an exported blob; the SDK's MemoryStream
/// renders back to exactly that text; an <c>equals</c> against the
/// <c>$base64</c> holds; and every spelling the fixture calls invalid is refused
/// rather than decoded into something else. Found and required exactly as
/// <see cref="ScenarioErrorFixtureTests"/> finds its corpus.
/// </remarks>
public sealed class ScenarioBlobFixtureTests
{
    private const string RequiredEnvVar = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

    private sealed record BlobCase(string Name, string Base64, string? Hex);

    private static Binder BinderWith(string path, object? value)
    {
        var bag = new ContextBag();
        bag.Set(path, value);
        return new Binder("oc-test", "kinesis-records", bag);
    }

    [Fact]
    public void SharedBlobFixture()
    {
        var file = TryFixtureFile();
        if (file is null)
        {
            if (Environment.GetEnvironmentVariable(RequiredEnvVar) == "1")
            {
                throw new InvalidOperationException(
                    $"{RequiredEnvVar}=1 but compat/model/testdata/blobs/blobs.json was not found walking up from "
                    + AppContext.BaseDirectory + " — this suite's fixture test must run from a full checkout");
            }
            Console.Error.WriteLine(
                "[dotnet-sdk] compat/model/testdata/blobs not found; skipping the shared blob fixture "
                + $"(set {RequiredEnvVar}=1 to make this fatal instead)");
            return;
        }

        using var document = JsonDocument.Parse(File.ReadAllText(file));
        var root = document.RootElement;
        foreach (var property in root.EnumerateObject())
        {
            Assert.Contains(property.Name, new[] { "$comment", "valid", "invalid" });
        }
        var valid = Cases(root.GetProperty("valid"));
        var invalid = Cases(root.GetProperty("invalid"));
        Assert.True(valid.Count > 0 && invalid.Count > 0, "the blob fixture may not be skipped by emptying it");

        foreach (var c in valid)
        {
            var want = System.Convert.FromHexString(c.Hex ?? "");
            var binder = BinderWith("rec.data", c.Base64);

            var literal = binder.Blob("Data", Val.Base64(c.Base64));
            Assert.Null(binder.Error);
            Assert.Equal(want, literal.ToArray());
            var viaRef = binder.Blob("Data", Val.Base64(Val.Ref("rec.data")));
            Assert.Null(binder.Error);
            Assert.Equal(want, viaRef.ToArray());

            Assert.True(Documents.TryConvert(new Record { Data = new MemoryStream(want) }, out var rendered));
            Assert.True(Paths.TryResolve(rendered, "$.Data", out var data));
            Assert.Equal(c.Base64, data);
            Assert.True(Documents.JsonEqual(data, binder.Evaluate(Val.Base64(c.Base64))),
                $"{c.Name}: equals $base64 does not hold");
        }

        foreach (var c in invalid)
        {
            Assert.Throws<ScenarioValueException>(() => Val.DecodeBase64(c.Base64));
            var binder = BinderWith("rec.data", c.Base64);
            binder.Blob("Data", Val.Base64(Val.Ref("rec.data")));
            Assert.NotNull(binder.Error);
            Assert.Equal("Data", binder.FailedMember);
        }
    }

    private static List<BlobCase> Cases(JsonElement array)
    {
        var cases = new List<BlobCase>();
        foreach (var item in array.EnumerateArray())
        {
            cases.Add(new BlobCase(
                item.GetProperty("name").GetString()!,
                item.GetProperty("base64").GetString()!,
                item.TryGetProperty("hex", out var hex) ? hex.GetString() : null));
        }
        return cases;
    }

    private static string? TryFixtureFile()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null)
        {
            var candidate = Path.Combine(directory.FullName, "compat", "model", "testdata", "blobs", "blobs.json");
            if (File.Exists(candidate))
            {
                return candidate;
            }
            directory = directory.Parent;
        }
        return null;
    }
}
