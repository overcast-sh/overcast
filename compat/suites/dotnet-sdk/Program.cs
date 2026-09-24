using OvercastCompat.Clients;
using OvercastCompat.Groups;
using OvercastCompat.Harness;
using OvercastCompat.Registry;
using OvercastCompat.Scenario;

const string suite = "dotnet-sdk";

// `--sdk-types <dir>` writes the SDK type table cmd/compatgen spells emitted C#
// from, reflected from the AWSSDK assemblies beside this program, and runs no
// test. The Dockerfile's sdk-types target is the only caller; see
// Scenario/SdkTypeTable.cs.
if (args is ["--sdk-types", var tableDirectory])
{
    Directory.CreateDirectory(tableDirectory);
    foreach (var (file, contents) in SdkTypeTable.Render(AppContext.BaseDirectory))
    {
        File.WriteAllText(Path.Combine(tableDirectory, file), contents);
    }
    return;
}

// 127.0.0.1, not localhost: on a dual-stack host "localhost" resolves to ::1
// first while the container publishes IPv4 only, so every new connection
// pays a ~2s IPv6-then-IPv4 fallback.
var endpoint = EnvOr("OVERCAST_ENDPOINT", "http://127.0.0.1:4566");
var region = EnvOr("OVERCAST_DEFAULT_REGION", "us-east-1");
var skipDocker = Environment.GetEnvironmentVariable("OVERCAST_COMPAT_SKIP_DOCKER") == "1";

var clients = new AwsClients(endpoint, region);
var serviceGroups = ServiceGroups.All(clients);
// The generated groups are kept apart from the hand-written ones on purpose.
// Their impls are resolved through the loader's ScenarioBackend hook rather
// than merged into the impl map, which is where a generated group belongs in
// the resolution order (#1393); their setup and teardown hooks are ordinary
// registrations and go into the same two maps.
var scenarioGroups = ScenarioGroups.All(clients);

// The two hook maps go through the same duplicate check the impls do below.
// They used to merge last-writer-wins, which made the halves disagree about one
// mistake: two group classes claiming one group's setup lost one of them
// silently, and the group then ran against a fixture that was never created -
// which reads as every test in it failing, not as a registration error.
// Generated and hand-written groups share these two maps, so a collision
// between the halves is caught here too.
var registeringGroups = serviceGroups.Concat(scenarioGroups).ToList();
Dictionary<string, SetupFn> setups;
Dictionary<string, SetupFn> teardowns;
try
{
    setups = RegistryLoader.MergeSetups(
        registeringGroups.Select(group => (group.SourceName, group.Setups())), suite);
    teardowns = RegistryLoader.MergeTeardowns(
        registeringGroups.Select(group => (group.SourceName, group.Teardowns())), suite);
}
catch (InvalidOperationException ex)
{
    Console.Error.WriteLine(ex.Message);
    Environment.Exit(1);
    return;
}

// The impls go through MergeImpls rather than a plain assignment: a key two
// group classes both register would otherwise lose one implementation with
// nothing said about it.
Dictionary<string, TestFn> impls;
try
{
    impls = RegistryLoader.MergeImpls(
        serviceGroups.Select(group => (group.SourceName, group.Impls())), suite);
}
catch (InvalidOperationException ex)
{
    // Duplicate registrations - see RegistryLoader.MergeImpls. Aborting is the
    // point: the discarded implementation never runs, and its test reports the
    // surviving group's result under its own name.
    Console.Error.WriteLine(ex.Message);
    Environment.Exit(1);
    return;
}

// One map per generated service class, merged with the same duplicate check
// the hand-written half gets: two classes claiming one generated test would
// otherwise leave one of them unreachable with nothing said about it.
Dictionary<string, TestFn> scenarioImpls;
try
{
    scenarioImpls = RegistryLoader.MergeImpls(
        scenarioGroups.Select(group => (group.SourceName, group.Impls())), suite);
}
catch (InvalidOperationException ex)
{
    Console.Error.WriteLine(ex.Message);
    Environment.Exit(1);
    return;
}

// Generated groups are always registered group-qualified, so the backend needs
// no bare-name fallback and cannot bind one group's test to another's.
ScenarioBackend backend = (group, test) =>
    scenarioImpls.TryGetValue($"{group.Name}:{test.Name}", out var implementation) ? implementation : null;

var capabilities = new HashSet<string>(StringComparer.Ordinal);
if (!skipDocker)
{
    capabilities.Add("docker");
}

IReadOnlyList<TestGroup> allGroups;
try
{
    allGroups = RegistryLoader.BuildGroups(suite, impls, setups, teardowns, capabilities, backend);
}
catch (InvalidOperationException ex)
{
    // Unusable impl registrations - see RegistryLoader.ValidateImpls. Aborting
    // is the point: binding a test to another group's implementation would
    // report a result for a test that never ran.
    Console.Error.WriteLine(ex.Message);
    Environment.Exit(1);
    return;
}
catch (Exception ex)
{
    Console.Error.WriteLine($"[dotnet-sdk] failed to load registry: {ex.Message}");
    Environment.Exit(1);
    return;
}

var filterServices = SplitFilter(Environment.GetEnvironmentVariable("OVERCAST_COMPAT_SERVICE"));
var filterGroups = SplitFilter(Environment.GetEnvironmentVariable("OVERCAST_COMPAT_GROUPS"));
var filterTests = SplitFilter(Environment.GetEnvironmentVariable("OVERCAST_COMPAT_TESTS"));
var filterTestPairs = SplitFilter(Environment.GetEnvironmentVariable("OVERCAST_COMPAT_TEST_PAIRS"));

var groups = allGroups;
if (filterServices.Count > 0)
{
    groups = groups.Where(group => filterServices.Contains(group.Service)).ToList();
}
if (filterGroups.Count > 0)
{
    groups = groups.Where(group => filterGroups.Contains(group.Name)).ToList();
}
if (filterTests.Count > 0)
{
    groups = groups
        .Select(group => group with { Tests = group.Tests.Where(test => filterTests.Contains(test.Name)).ToList() })
        .Where(group => group.Tests.Count > 0)
        .ToList();
}
if (filterTestPairs.Count > 0)
{
    groups = allGroups
        .Select(group => group with { Tests = group.Tests.Where(test => filterTestPairs.Contains($"{group.Name}:{test.Name}")).ToList() })
        .Where(group => group.Tests.Count > 0)
        .ToList();
}

if (Environment.GetEnvironmentVariable("OVERCAST_COMPAT_INTERACTIVE") == "1")
{
    await InteractiveRunner.RunAsync(suite, endpoint, region, allGroups);
}
else
{
    await Runner.RunSuiteAsync(suite, endpoint, region, groups);
}

static string EnvOr(string name, string defaultValue)
{
    var value = Environment.GetEnvironmentVariable(name);
    return string.IsNullOrWhiteSpace(value) ? defaultValue : value;
}

static HashSet<string> SplitFilter(string? value)
{
    if (string.IsNullOrWhiteSpace(value))
    {
        return [];
    }

    return value
        .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
        .Where(entry => !string.IsNullOrWhiteSpace(entry))
        .ToHashSet(StringComparer.Ordinal);
}
