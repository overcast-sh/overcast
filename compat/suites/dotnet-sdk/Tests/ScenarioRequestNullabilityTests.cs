using System.Reflection;
using System.Text.RegularExpressions;
using Amazon.Runtime;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The measured fact the .NET emitter is built on, asserted rather than
/// remembered: every value-typed member of every request the generated groups
/// build is <c>Nullable&lt;T&gt;</c>.
/// </summary>
/// <remarks>
/// <c>cmd/compatgen/emit_dotnet.go</c> spells a member against the type the SDK
/// type table (<c>sdk-types/</c>) gives its property, and one of the three
/// facts that keep that spelling small is that AWSSDK v4 made every
/// value-typed member nullable — so writing a zero really does send a zero,
/// and the go-sdk emitter's value-typed-zero refusal has nothing to refuse
/// here. That was measured on three members of one request.
/// If it ever stops being true of a member the corpus sets, the emitter would
/// silently omit that member from the wire — a wrong request that compiles, and
/// a test that passes for the wrong reason.
/// <para>So this checks every request type the emitted groups actually build,
/// discovered from the generated sources rather than from a list somebody has
/// to remember to extend. A new service in the corpus is covered the moment
/// <c>make generate-compat-model</c> writes its file.</para>
/// </remarks>
public sealed class ScenarioRequestNullabilityTests
{
    /// <summary>
    /// <c>new SomethingRequest()</c> in an emitted method — the one place the
    /// generated source names an SDK type.
    /// </summary>
    private static readonly Regex Constructed =
        new(@"new\s+(?<type>[A-Za-z0-9_]+Request)\s*\(\s*\)", RegexOptions.Compiled);

    /// <summary>The <c>using Amazon.X.Model;</c> a generated file opens with.</summary>
    private static readonly Regex ModelNamespace =
        new(@"^using\s+(?<ns>Amazon\.[A-Za-z0-9_.]+\.Model)\s*;", RegexOptions.Compiled | RegexOptions.Multiline);

    [Fact]
    public void EveryValueTypedMemberOfAnEmittedRequestIsNullable()
    {
        var requests = EmittedRequestTypes();
        Assert.True(requests.Count > 0,
            "no request types were discovered in the generated groups; the discovery below has stopped working, "
            + "and an assertion over nothing passes for the wrong reason");

        var nonNullable = new List<string>();
        foreach (var request in requests.OrderBy(type => type.FullName, StringComparer.Ordinal))
        {
            foreach (var property in request.GetProperties(BindingFlags.Public | BindingFlags.Instance))
            {
                // Only modeled members: everything AWSSDK declares on the
                // request base class is its own bookkeeping, and Documents.cs
                // drops it for the same reason.
                if (property.DeclaringType == typeof(AmazonWebServiceRequest) || property.GetSetMethod() is null)
                {
                    continue;
                }
                if (!property.PropertyType.IsValueType || Nullable.GetUnderlyingType(property.PropertyType) is not null)
                {
                    continue;
                }
                nonNullable.Add($"{request.FullName}.{property.Name} : {property.PropertyType.Name}");
            }
        }

        Assert.True(nonNullable.Count == 0,
            "the .NET emitter writes a member's value from the shape model on the understanding that AWSSDK v4 makes "
            + "every value-typed member nullable, so a zero is sent rather than dropped. These are not nullable, and a "
            + "zero written into one would be omitted from the wire: " + string.Join(", ", nonNullable));
    }

    /// <summary>
    /// The request types the emitted groups construct, read out of the
    /// generated sources and resolved against the AWSSDK assemblies beside the
    /// test host.
    /// </summary>
    private static IReadOnlyList<Type> EmittedRequestTypes()
    {
        var byName = ModelTypesByFullName();
        var found = new SortedDictionary<string, Type>(StringComparer.Ordinal);
        var unresolved = new List<string>();

        foreach (var file in Directory.GetFiles(FindGeneratedGroupsDirectory(), "Scenarios*Gen.cs"))
        {
            var source = File.ReadAllText(file);
            var namespaces = ModelNamespace.Matches(source).Select(match => match.Groups["ns"].Value).ToList();
            foreach (Match match in Constructed.Matches(source))
            {
                var name = match.Groups["type"].Value;
                var resolved = namespaces
                    .Select(ns => byName.TryGetValue($"{ns}.{name}", out var type) ? type : null)
                    .FirstOrDefault(type => type is not null);
                if (resolved is null)
                {
                    unresolved.Add($"{Path.GetFileName(file)}: {name}");
                    continue;
                }
                found[resolved.FullName!] = resolved;
            }
        }

        // A type the emitted source names and the pinned packages do not
        // declare would not compile, so this cannot fire in a green build —
        // but it is the difference between "checked nothing" and "checked
        // everything", so it is said rather than assumed.
        Assert.True(unresolved.Count == 0,
            "request types named by the generated sources that no referenced AWSSDK package declares: "
            + string.Join(", ", unresolved));
        return found.Values.ToList();
    }

    /// <summary>
    /// Every public type in an <c>Amazon.*.Model</c> namespace, from the AWSSDK
    /// assemblies published beside the test host. Loading them by file rather
    /// than by name means a service is covered without this test naming it, and
    /// without depending on which assemblies another test happened to touch
    /// first.
    /// </summary>
    private static Dictionary<string, Type> ModelTypesByFullName()
    {
        var types = new Dictionary<string, Type>(StringComparer.Ordinal);
        foreach (var path in Directory.GetFiles(AppContext.BaseDirectory, "AWSSDK.*.dll"))
        {
            Assembly assembly;
            try
            {
                assembly = Assembly.LoadFrom(path);
            }
            catch (BadImageFormatException)
            {
                continue;
            }
            foreach (var type in assembly.GetExportedTypes())
            {
                if (type.FullName is { } name && type.Namespace is { } ns && ns.EndsWith(".Model", StringComparison.Ordinal))
                {
                    types[name] = type;
                }
            }
        }
        return types;
    }

    /// <summary>
    /// Walks up from the test host's own directory to the suite directory that
    /// holds <c>Groups/ScenariosGen.cs</c> — the same rule the registry tests
    /// use to find registry.json, and for the same reason: a VSTest host's
    /// working directory is its build output directory. It resolves both in a
    /// developer's checkout and in the Docker build, where the sources sit one
    /// directory above the test project.
    /// </summary>
    private static string FindGeneratedGroupsDirectory()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null)
        {
            var candidate = Path.Combine(directory.FullName, "Groups");
            if (File.Exists(Path.Combine(candidate, "ScenariosGen.cs")))
            {
                return candidate;
            }
            directory = directory.Parent;
        }
        throw new DirectoryNotFoundException(
            "Groups/ScenariosGen.cs not found walking up from " + AppContext.BaseDirectory);
    }
}
