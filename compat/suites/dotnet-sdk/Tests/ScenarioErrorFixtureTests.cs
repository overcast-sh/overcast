using System.Net;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Amazon;
using Amazon.Runtime;
using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The shared error-matching conformance fixtures, compat/model/testdata/errors.
/// </summary>
/// <remarks>
/// Every backend reads the same documents and must agree about which clauses
/// they satisfy. Each suite writes this test once, against its own matcher, so
/// a rule only one backend implements fails somewhere rather than being
/// discovered when a generated group disagrees with itself across suites
/// (compat/model/README.md § Errors).
/// <para>Where the go-sdk suite constructs the SDK's exception types by hand,
/// this one <b>replays each fixture's wire through a real client</b>: an
/// in-process HttpListener answers with the fixture's status, headers and body,
/// and the AWS SDK for .NET unmarshals it. That is the more faithful of the two
/// and it is what the awsQueryCompatible fixtures need — the SDK, like
/// botocore, resolves ErrorCode from the <c>x-amzn-query-error</c> header in
/// preference to the body, so the header is a carrier this suite observes only
/// because the SDK really read it.</para>
/// <para>A fixture whose surfaces this suite cannot see is skipped by name and
/// with a reason: a silently ignored fixture would look exactly like a passing
/// one. The AWS CLI's stderr banner never reaches an SDK caller, so the two
/// cliBanner fixtures are the ones skipped.</para>
/// </remarks>
public sealed class ScenarioErrorFixtureTests
{
    /// <summary>
    /// The whole carrier vocabulary. A fixture naming anything else is a typo
    /// that would otherwise skip quietly in every suite at once.
    /// </summary>
    private static readonly HashSet<string> KnownCarriers = new(StringComparer.Ordinal)
    {
        "exceptionName", "bodyType", "bodyCode", "queryErrorHeader", "cliBanner",
    };

    /// <summary>
    /// What this suite can see. bodyType, bodyCode and queryErrorHeader are all
    /// observed through AmazonServiceException.ErrorCode: the SDK parses the
    /// body away before the caller sees the error, and what survives is the
    /// code its unmarshaller resolved — from __type, from the body's lowercase
    /// code member, from the Code inside an XML error node (the Query protocol's
    /// ErrorResponse envelope and REST XML's bare Error root), or from the
    /// query-error header where the service sends one.
    /// </summary>
    private static readonly HashSet<string> ObservedCarriers = new(StringComparer.Ordinal)
    {
        "exceptionName", "bodyType", "bodyCode", "queryErrorHeader",
    };

    /// <summary>
    /// The client each fixture's wire is replayed through, chosen so the
    /// exceptionName carrier is really observed: an SDK only mints a modeled
    /// exception for an error the service it belongs to declares.
    /// </summary>
    /// <remarks>
    /// A fixture naming an exception with no entry fails rather than skipping:
    /// the list is short and adding to it is a line, while a quiet skip would
    /// hide a carrier this suite claims to observe.
    /// </remarks>
    private static readonly Dictionary<string, Func<string, Task>> Replays = new(StringComparer.Ordinal)
    {
        [""] = Sqs,
        ["PolicyNotFoundException"] = async endpoint =>
        {
            using var client = new Amazon.Organizations.AmazonOrganizationsClient(
                Credentials, Configure(new Amazon.Organizations.AmazonOrganizationsConfig(), endpoint));
            await client.DescribePolicyAsync(new Amazon.Organizations.Model.DescribePolicyRequest { PolicyId = "p-compat00" });
        },
        ["ResourceNotFoundException"] = async endpoint =>
        {
            using var client = new Amazon.DynamoDBv2.AmazonDynamoDBClient(
                Credentials, Configure(new Amazon.DynamoDBv2.AmazonDynamoDBConfig(), endpoint));
            await client.DescribeTableAsync(new Amazon.DynamoDBv2.Model.DescribeTableRequest { TableName = "compat" });
        },
        ["NotFoundException"] = async endpoint =>
        {
            using var client = new Amazon.KeyManagementService.AmazonKeyManagementServiceClient(
                Credentials, Configure(new Amazon.KeyManagementService.AmazonKeyManagementServiceConfig(), endpoint));
            await client.DescribeKeyAsync(new Amazon.KeyManagementService.Model.DescribeKeyRequest { KeyId = "compat" });
        },
    };

    private static readonly AWSCredentials Credentials = new BasicAWSCredentials("test", "test");

    private static async Task Sqs(string endpoint)
    {
        using var client = new Amazon.SQS.AmazonSQSClient(
            Credentials, Configure(new Amazon.SQS.AmazonSQSConfig(), endpoint));
        await client.DeleteQueueAsync(new Amazon.SQS.Model.DeleteQueueRequest
        {
            QueueUrl = $"{endpoint}/000000000000/compat",
        });
    }

    /// <summary>
    /// The AWS Query protocol, replayed through IAM — a service whose whole
    /// API is Query, so the SDK's XML error unmarshaller is what reads the
    /// wire and resolves ErrorCode out of
    /// <c>&lt;ErrorResponse&gt;&lt;Error&gt;&lt;Code&gt;</c>.
    /// </summary>
    /// <remarks>
    /// That is the point of routing an XML fixture away from the SQS default:
    /// SQS is AWS JSON, its unmarshaller reads <c>__type</c>, and an XML body
    /// replayed through it states no code at all — which is what this suite
    /// was silently unable to say before #1896, because its fixture schema
    /// could not carry such a body in the first place.
    /// </remarks>
    private static async Task QueryProtocol(string endpoint)
    {
        using var client = new Amazon.IdentityManagement.AmazonIdentityManagementServiceClient(
            Credentials, Configure(new Amazon.IdentityManagement.AmazonIdentityManagementServiceConfig(), endpoint));
        await client.GetGroupAsync(new Amazon.IdentityManagement.Model.GetGroupRequest { GroupName = "compat-absent" });
    }

    /// <summary>
    /// REST XML, replayed through S3 — the dialect that states the code in a
    /// bare <c>&lt;Error&gt;</c> root with no envelope around it, and the one
    /// service whose unmarshaller is written for that shape.
    /// </summary>
    private static async Task RestXmlProtocol(string endpoint)
    {
        var config = Configure(new Amazon.S3.AmazonS3Config(), endpoint);
        config.ForcePathStyle = true;
        using var client = new Amazon.S3.AmazonS3Client(Credentials, config);
        await client.ListObjectsV2Async(new Amazon.S3.Model.ListObjectsV2Request { BucketName = "compat" });
    }

    /// <summary>
    /// The client a fixture's wire is replayed through.
    /// </summary>
    /// <remarks>
    /// An exceptionName names the service that models that error, because an
    /// SDK only mints a modeled exception for one its own service declares.
    /// A wire with none is chosen by protocol instead: an XML body goes to the
    /// client whose unmarshaller reads that envelope, and everything else to
    /// the AWS JSON default.
    /// </remarks>
    private static Func<string, Task> ChooseReplay(FixtureWire wire)
    {
        if (!string.IsNullOrEmpty(wire.ExceptionName))
        {
            return Replays.TryGetValue(wire.ExceptionName, out var modeled)
                ? modeled
                : throw new InvalidOperationException(
                    $"no client replays {wire.ExceptionName}; add one to Replays so the exceptionName carrier is really observed");
        }
        return XmlRootElement(wire) switch
        {
            "Error" => RestXmlProtocol,
            "ErrorResponse" => QueryProtocol,
            null => Replays[""],
            var root => throw new InvalidOperationException(
                $"no client replays an XML error body rooted at <{root}>; add one so the bodyCode carrier is really observed"),
        };
    }

    /// <summary>
    /// The root element of a raw XML body, or null when the wire's body is not
    /// one.
    /// </summary>
    private static string? XmlRootElement(FixtureWire wire)
    {
        if (RawBody(wire) is not { } raw)
        {
            return null;
        }
        using var reader = System.Xml.XmlReader.Create(new StringReader(raw));
        return reader.MoveToContent() == System.Xml.XmlNodeType.Element ? reader.Name : null;
    }

    /// <summary>
    /// The raw bytes of a wire whose body is not JSON, or null for one whose
    /// body is a JSON object.
    /// </summary>
    private static string? RawBody(FixtureWire wire) =>
        wire.Body is { ValueKind: JsonValueKind.String } body ? body.GetString() : null;

    private static T Configure<T>(T config, string endpoint) where T : ClientConfig
    {
        config.ServiceURL = endpoint;
        config.UseHttp = true;
        config.AuthenticationRegion = RegionEndpoint.USEast1.SystemName;
        // One request per replay: the listener answers once, and a retry would
        // hang waiting for a second answer.
        config.MaxErrorRetry = 0;
        return config;
    }

    /// <summary>
    /// Set to "1" only by test.yml's compat-suite-unit-tests job, which runs
    /// from a full checkout where the corpus is always reachable. Its absence
    /// there would mean the shared conformance set silently stopped being
    /// checked anywhere — see compat/AGENTS.md § Where the shared error
    /// corpus runs.
    /// </summary>
    private const string RequiredEnvVar = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

    /// <summary>
    /// Sentinel theory case standing in for "the corpus was not found here".
    /// Not a real fixture id, so <see cref="SharedFixture"/> special-cases it
    /// before ever resolving a path from it.
    /// </summary>
    private const string MissingCorpusId = "__no-fixture-corpus__";

    public static TheoryData<string> FixtureIds()
    {
        var data = new TheoryData<string>();
        var dir = TryFixtureDirectory();
        if (dir is null)
        {
            if (Environment.GetEnvironmentVariable(RequiredEnvVar) == "1")
            {
                throw new InvalidOperationException(
                    $"{RequiredEnvVar}=1 but compat/model/testdata/errors was not found walking up from "
                    + AppContext.BaseDirectory
                    + " — this suite's fixture test must run from a full checkout"
                    + " (test.yml's compat-suite-unit-tests job)");
            }
            data.Add(MissingCorpusId);
            return data;
        }
        foreach (var path in FixturePaths(dir))
        {
            data.Add(Path.GetFileNameWithoutExtension(path));
        }
        return data;
    }

    [Theory]
    [MemberData(nameof(FixtureIds))]
    public async Task SharedFixture(string id)
    {
        if (id == MissingCorpusId)
        {
            // The Docker build's context is compat/suites/, which does not
            // contain compat/model/testdata/errors — see the Dockerfile.
            // Reported by name and with a reason rather than dropped: a
            // conformance set that quietly asserted nothing would look
            // exactly like one that held.
            Console.Error.WriteLine(
                $"[dotnet-sdk] compat/model/testdata/errors not found; skipping the shared error-fixture "
                + $"conformance set (set {RequiredEnvVar}=1 to make this fatal instead — "
                + "test.yml's compat-suite-unit-tests job does, from a full checkout)");
            return;
        }
        var fixture = Read(Path.Combine(FixtureDirectory(), id + ".json"));
        Assert.Equal(id, fixture.Id);

        foreach (var carrier in fixture.Carriers)
        {
            Assert.True(KnownCarriers.Contains(carrier),
                $"unknown carrier \"{carrier}\"; the vocabulary is fixed by compat/model/README.md § Errors");
        }

        // A fixture that states no code anywhere is observed by everyone: there
        // is nothing to miss, and its expectations are necessarily all
        // negative, so a suite that cannot see the wire still answers them
        // correctly.
        if (fixture.Carriers.Count > 0 && !fixture.Carriers.Any(ObservedCarriers.Contains))
        {
            // Skipped by name and with a reason. xunit 2 has no dynamic skip,
            // so the reason is asserted rather than reported: the only fixtures
            // this suite cannot see are the CLI's stderr banner, which never
            // reaches an SDK caller, and naming them here means a new one
            // cannot join them silently.
            Assert.Equal(["cliBanner"], fixture.Carriers);
            return;
        }

        var observed = await Observe(fixture);
        var asserted = 0;
        foreach (var expectation in fixture.Expect)
        {
            if (expectation.Matches && !ObservedCarriers.Contains(expectation.Via ?? ""))
            {
                continue;
            }
            asserted++;
            Assert.Equal(expectation.Matches, Errors.Matches(observed, expectation.Error));
        }
        Assert.True(asserted > 0, $"{id}: every expectation was skipped, so this fixture asserts nothing");
    }

    /// <summary>
    /// Renders a fixture the way this suite would have observed it: the wire,
    /// served once over loopback, and the exception the real SDK raised.
    /// </summary>
    private static async Task<Exception> Observe(ErrorFixture fixture)
    {
        if (fixture.Wire.Stderr is { Length: > 0 } stderr)
        {
            // No HTTP exchange at all: the process died before the wire.
            // Nothing states a code, which is exactly what
            // cli-no-parseable-code pins — and an SDK failing the same way
            // raises a service exception with no ErrorCode.
            return new Amazon.SQS.AmazonSQSException(stderr);
        }

        var replay = ChooseReplay(fixture.Wire);

        var port = FreePort();
        var endpoint = $"http://127.0.0.1:{port}";
        using var listener = new HttpListener();
        listener.Prefixes.Add(endpoint + "/");
        listener.Start();
        var serving = Task.Run(async () =>
        {
            var http = await listener.GetContextAsync();
            // A non-JSON wire is served as the service wrote it, byte for
            // byte, under the content type the fixture states. Re-encoding it
            // as JSON would be serving a wire no service sends, and the SDK's
            // XML unmarshaller — the whole reason an XML fixture is replayed
            // through a Query or REST XML client — would never run.
            var body = Encoding.UTF8.GetBytes(
                RawBody(fixture.Wire) ?? JsonSerializer.Serialize(fixture.Wire.Body));
            http.Response.StatusCode = fixture.Wire.Status;
            foreach (var header in fixture.Wire.Headers)
            {
                // Content-Type is a restricted header on HttpListenerResponse
                // and is set through its own property below.
                if (string.Equals(header.Key, "Content-Type", StringComparison.OrdinalIgnoreCase))
                {
                    continue;
                }
                http.Response.Headers[header.Key] = header.Value;
            }
            http.Response.ContentType = fixture.Wire.Headers
                .FirstOrDefault(header => string.Equals(header.Key, "Content-Type", StringComparison.OrdinalIgnoreCase))
                .Value ?? "application/x-amz-json-1.0";
            http.Response.ContentLength64 = body.Length;
            await http.Response.OutputStream.WriteAsync(body);
            http.Response.Close();
        });

        try
        {
            await replay(endpoint);
        }
        catch (Exception ex)
        {
            return ex;
        }
        finally
        {
            // Stopping first, and awaiting after, is the whole point. A replay
            // that throws before the listener ever accepts — an SDK-side
            // validation throw, a connection reset, a FreePort() race lost to
            // something else binding the port — leaves the handler parked in
            // GetContextAsync forever, and awaiting it there would hang the
            // whole test run inside `docker build` with no timeout to end it.
            // Stopping the listener is what makes that await return.
            listener.Stop();
            await DrainAsync(serving);
        }
        throw new InvalidOperationException($"{fixture.Id}: the replayed wire raised nothing");
    }

    /// <summary>How long the handler gets to finish once the listener is stopped.</summary>
    private static readonly TimeSpan ServingTimeout = TimeSpan.FromSeconds(10);

    /// <summary>
    /// Observes the server task, so it can neither hang the run nor resurface
    /// later as an unobserved task exception.
    /// </summary>
    /// <remarks>
    /// Two outcomes are expected and neither says anything about the fixture:
    /// the handler faulted because the listener was stopped underneath it, or it
    /// never accepted at all and the timeout ends the wait. Both are swallowed
    /// here — the exception the test wants is the one the SDK raised.
    /// </remarks>
    private static async Task DrainAsync(Task serving)
    {
        using var timeout = new CancellationTokenSource(ServingTimeout);
        try
        {
            await serving.WaitAsync(timeout.Token);
        }
        catch (Exception)
        {
            // A task still running after the timeout is observed when it ends.
            _ = serving.ContinueWith(static task => _ = task.Exception, TaskScheduler.Default);
        }
    }

    /// <summary>
    /// A port nothing is listening on. Binding to 0 and reading back what the
    /// operating system chose is the portable way to ask.
    /// </summary>
    private static int FreePort()
    {
        var probe = new System.Net.Sockets.TcpListener(IPAddress.Loopback, 0);
        probe.Start();
        var port = ((IPEndPoint)probe.LocalEndpoint).Port;
        probe.Stop();
        return port;
    }

    /// <summary>
    /// Pins the assumption the exceptionName surface rests on: AWSSDK names the
    /// class after the modeled shape, appending "Exception" where the shape
    /// does not already end in one — and the suffix is deliberately not
    /// stripped, because a clause naming ResourceNotFound must not match a
    /// ResourceNotFoundException.
    /// </summary>
    [Fact]
    public void AModeledSdkErrorSurfacesItsClassName()
    {
        Assert.Equal("PolicyNotFoundException",
            Errors.ModeledTypeName(new Amazon.Organizations.Model.PolicyNotFoundException("x")));
        Assert.Equal("QueueDoesNotExistException",
            Errors.ModeledTypeName(new Amazon.SQS.Model.QueueDoesNotExistException("x")));

        // The per-service catch-all is not a modeled shape, and a clause naming
        // it must never be satisfied by one.
        Assert.Null(Errors.ModeledTypeName(new Amazon.SQS.AmazonSQSException("x")));
        Assert.False(Errors.Matches(
            new Amazon.SQS.AmazonSQSException("x"),
            new ErrorSpec("AmazonSQSException", "AmazonSQSException")));
    }

    /// <summary>Every spelling a clause may name one observed code by, and no others.</summary>
    [Theory]
    [InlineData("QueueDoesNotExist", "QueueDoesNotExist")]
    [InlineData("com.amazonaws.sqs#QueueDoesNotExist", "com.amazonaws.sqs#QueueDoesNotExist|QueueDoesNotExist")]
    [InlineData("AWS.SimpleQueueService.NonExistentQueue;Sender", "AWS.SimpleQueueService.NonExistentQueue;Sender|AWS.SimpleQueueService.NonExistentQueue")]
    public void SpellingsSplitAtHashAndSemicolonAndNowhereElse(string code, string expected)
    {
        Assert.Equal(expected.Split('|'), Errors.Spellings(code));
    }

    /// <summary>
    /// Walks up from the test host's base directory looking for the corpus,
    /// or returns null when there is none above it — the Docker build's case.
    /// </summary>
    private static string? TryFixtureDirectory()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null)
        {
            var candidate = Path.Combine(directory.FullName, "compat", "model", "testdata", "errors");
            if (Directory.Exists(candidate))
            {
                return candidate;
            }
            directory = directory.Parent;
        }
        return null;
    }

    private static string FixtureDirectory() =>
        TryFixtureDirectory()
        ?? throw new DirectoryNotFoundException(
            "compat/model/testdata/errors not found walking up from " + AppContext.BaseDirectory
            + "; the shared conformance set may not be skipped by not shipping it");

    private static IReadOnlyList<string> FixturePaths(string directory)
    {
        var paths = Directory.GetFiles(directory, "*.json");
        Array.Sort(paths, StringComparer.Ordinal);
        if (paths.Length == 0)
        {
            throw new InvalidOperationException(
                "no fixtures in compat/model/testdata/errors: the shared conformance set may not be skipped by deleting it");
        }
        return paths;
    }

    /// <summary>
    /// Reads one fixture strictly: an unknown key anywhere is an error, not a
    /// field the suite that added it happens to ignore.
    /// </summary>
    private static ErrorFixture Read(string path)
    {
        var options = new JsonSerializerOptions
        {
            PropertyNameCaseInsensitive = true,
            UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
        };
        using var stream = File.OpenRead(path);
        return JsonSerializer.Deserialize<ErrorFixture>(stream, options)
               ?? throw new InvalidOperationException($"failed to read {path}");
    }

    private sealed record ErrorFixture
    {
        [JsonPropertyName("id")] public string Id { get; init; } = "";
        [JsonPropertyName("title")] public string Title { get; init; } = "";
        [JsonPropertyName("why")] public string Why { get; init; } = "";
        [JsonPropertyName("carriers")] public IReadOnlyList<string> Carriers { get; init; } = [];
        [JsonPropertyName("wire")] public FixtureWire Wire { get; init; } = new();
        [JsonPropertyName("expect")] public IReadOnlyList<FixtureCase> Expect { get; init; } = [];
    }

    private sealed record FixtureWire
    {
        [JsonPropertyName("status")] public int Status { get; init; }
        [JsonPropertyName("exceptionName")] public string? ExceptionName { get; init; }
        [JsonPropertyName("headers")] public IReadOnlyDictionary<string, string> Headers { get; init; } = new Dictionary<string, string>();
        /// <summary>
        /// A JSON object for a JSON wire, and a JSON string — the raw XML
        /// bytes — for one that is not, so it is held as a JsonElement rather
        /// than a flat dictionary of strings (compat/model/README.md
        /// § Errors). It used to be
        /// IReadOnlyDictionary&lt;string, string&gt;, which could represent
        /// neither an XML body nor a nested one, and that is why the shared
        /// corpus carried no Query error until #1896.
        /// </summary>
        [JsonPropertyName("body")] public JsonElement? Body { get; init; }
        [JsonPropertyName("stderr")] public string? Stderr { get; init; }
    }

    private sealed record FixtureCase
    {
        [JsonPropertyName("name")] public string Name { get; init; } = "";
        [JsonPropertyName("error")] public ErrorSpec Error { get; init; } = new("", "");
        [JsonPropertyName("matches")] public bool Matches { get; init; }
        [JsonPropertyName("via")] public string? Via { get; init; }
    }
}
