using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The committed SDK type table is what the pinned AWSSDK assemblies declare,
/// byte for byte.
/// </summary>
/// <remarks>
/// cmd/compatgen spells emitted C# from <c>sdk-types/</c> without loading an
/// assembly — it runs offline, where there is no .NET SDK — and checks only
/// that each file's version matches the csproj pin. This is the other half: the
/// table rendered from the assemblies beside the test host, which are the ones
/// the suite compiles against, compared with the committed files. A pin bump
/// whose table was not refreshed, or a hand edit, fails here — in the image
/// build and in CI's compat-suite-unit-tests job alike — naming the command
/// that fixes it.
/// </remarks>
public sealed class SdkTypeTableTests
{
    private const string Refresh =
        "docker build -f compat/suites/dotnet-sdk/Dockerfile --target sdk-types "
        + "--output type=local,dest=compat/suites/dotnet-sdk/sdk-types compat/suites";

    [Fact]
    public void TheCommittedTableIsWhatThePinnedAssembliesDeclare()
    {
        var rendered = SdkTypeTable.Render(AppContext.BaseDirectory);
        Assert.True(rendered.Count > 0,
            "no AWSSDK service assembly was found beside the test host, so nothing was compared");

        var directory = FindTableDirectory();
        var committed = Directory.GetFiles(directory, "*.txt")
            .ToDictionary(Path.GetFileName, File.ReadAllText, StringComparer.Ordinal);

        var problems = new List<string>();
        foreach (var (file, contents) in rendered)
        {
            if (!committed.TryGetValue(file, out var text))
            {
                problems.Add($"{file} is missing");
            }
            else if (Normalise(text) != contents)
            {
                problems.Add($"{file} differs from the assembly it names");
            }
        }
        problems.AddRange(committed.Keys
            .Where(file => !rendered.ContainsKey(file!))
            .Select(file => $"{file} names a package the suite no longer references"));

        Assert.True(problems.Count == 0,
            $"the SDK type table under {directory} is stale: {string.Join("; ", problems)}. Refresh it with `{Refresh}`.");
    }

    [Fact]
    public void ATypeIsSpelledInTheTablesClosedGrammar()
    {
        const string ns = "Amazon.CloudWatchLogs.Model";
        Assert.Equal("DateTime?", SdkTypeTable.Spell(typeof(DateTime?), ns));
        Assert.Equal("long?", SdkTypeTable.Spell(typeof(long?), ns));
        Assert.Equal("string", SdkTypeTable.Spell(typeof(string), ns));
        Assert.Equal("MemoryStream", SdkTypeTable.Spell(typeof(MemoryStream), ns));
        Assert.Equal("List<class:InputLogEvent>",
            SdkTypeTable.Spell(typeof(List<Amazon.CloudWatchLogs.Model.InputLogEvent>), ns));
        Assert.Equal("Dictionary<string,List<string>>",
            SdkTypeTable.Spell(typeof(Dictionary<string, List<string>>), ns));
        Assert.Equal("enum:Distribution", SdkTypeTable.Spell(typeof(Amazon.CloudWatchLogs.Distribution), ns));
        // A class from another namespace is not one of this package's model
        // classes, so it is spelled for a reader rather than for the emitter.
        Assert.Equal("type:Amazon.SQS.Model.Message", SdkTypeTable.Spell(typeof(Amazon.SQS.Model.Message), ns));
    }

    /// <summary>
    /// A checkout on Windows may carry the files with CRLF endings; the
    /// generator's reader and this comparison both mean the LF text.
    /// </summary>
    private static string Normalise(string text) => text.Replace("\r\n", "\n", StringComparison.Ordinal);

    /// <summary>
    /// Walks up from the test host to the suite directory holding
    /// <c>sdk-types/</c> — in a checkout, and in the Docker build, where the
    /// Dockerfile copies it beside the sources.
    /// </summary>
    private static string FindTableDirectory()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null)
        {
            var candidate = Path.Combine(directory.FullName, SdkTypeTable.DirectoryName);
            if (Directory.Exists(candidate) && File.Exists(Path.Combine(directory.FullName, "OvercastCompat.csproj")))
            {
                return candidate;
            }
            directory = directory.Parent;
        }
        throw new DirectoryNotFoundException(
            $"{SdkTypeTable.DirectoryName}/ beside OvercastCompat.csproj not found walking up from {AppContext.BaseDirectory}");
    }
}
