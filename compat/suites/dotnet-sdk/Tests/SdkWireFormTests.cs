using System.Net;
using System.Text;
using System.Text.Json;
using Amazon;
using Amazon.CloudWatchLogs;
using Amazon.CloudWatchLogs.Model;
using Amazon.Runtime;
using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The measurement cmd/compatgen's <c>dotnetEpochMilliseconds</c> row rests on,
/// asserted rather than remembered: AWSSDK.CloudWatchLogs types the model's
/// epoch-millisecond longs as DateTime, and puts exactly those milliseconds on
/// the wire in both directions.
/// </summary>
/// <remarks>
/// The SDK type table (Scenario/SdkTypeTable.cs) can say a property is a
/// DateTime where the model says long. It cannot say what the SDK sends for one:
/// a customization meaning seconds would compile just as well, and emitting
/// milliseconds into it would be a wrong request that passes. So the generator
/// spells the conversion only for a service measured here — through a real
/// client, against an in-process listener, with no emulator.
/// <para>The same number runs through all of it: exported from one response as
/// its document form, bound into the next request, and read on the wire. That
/// is the round trip the logs-events port relies on — every event is stamped
/// with the stream's own creationTime (#2132).</para>
/// </remarks>
public sealed class SdkWireFormTests
{
    /// <summary>2024-09-10T20:26:40.123Z: a millisecond part, so a unit error of any size shows.</summary>
    private const long Millis = 1_726_000_000_123;

    private static readonly AWSCredentials Credentials = new BasicAWSCredentials("test", "test");

    [Fact]
    public async Task ACreationTimeReadsBackAsTheEpochMillisecondsTheServiceSent()
    {
        // Given: CloudWatch Logs answers DescribeLogStreams with a creationTime
        // of epoch milliseconds, as the model says it does.
        var (response, _) = await Exchange(
            "{\"logStreams\":[{\"logStreamName\":\"s\",\"creationTime\":" + Millis + "}]}",
            client => client.DescribeLogStreamsAsync(new DescribeLogStreamsRequest { LogGroupName = "g" }));

        // Then: AWSSDK holds it as a DateTime that many milliseconds after the
        // epoch — the unit, measured.
        var stream = Assert.Single(response.LogStreams);
        Assert.Equal((double)Millis, Documents.ToEpochMilliseconds(stream.CreationTime!.Value));

        // And: registered the way the generated group registers it, the
        // document holds that number, as it does in every other backend.
        Documents.EpochMilliseconds(typeof(LogStream), nameof(LogStream.CreationTime));
        Assert.True(Documents.TryConvert(response, out var document));
        Assert.True(Paths.TryResolve(document, "$.logStreams[0].creationTime", out var created));
        Assert.Equal((double)Millis, created);
    }

    [Fact]
    public async Task AnExportedEpochIsSentAsTheSameMilliseconds()
    {
        // Given: the context holds an epoch exported from an earlier response —
        // a double, which is what every document number is.
        var bag = new ContextBag();
        bag.Set("stream.created", (double)Millis);
        var binder = new Binder("oc-test", "logs-events", bag);

        // When: it is bound into InputLogEvent.Timestamp exactly as the emitted
        // Build body binds it, and the request is sent.
        var request = new PutLogEventsRequest
        {
            LogGroupName = "g",
            LogStreamName = "s",
            LogEvents =
            [
                new() { Message = "event one", Timestamp = binder.EpochMilliseconds("timestamp", Val.Ref("stream.created")) },
            ],
        };
        Assert.Null(binder.Error);
        var (_, body) = await Exchange("{}", client => client.PutLogEventsAsync(request));

        // Then: the wire carries the number the context held, to the
        // millisecond.
        using var sent = JsonDocument.Parse(body);
        Assert.Equal(Millis, sent.RootElement.GetProperty("logEvents")[0].GetProperty("timestamp").GetInt64());

        // And: failure-message field 3 renders the request it sent through the
        // same conversion, so it prints that number too once the property is
        // registered.
        Documents.EpochMilliseconds(typeof(InputLogEvent), nameof(InputLogEvent.Timestamp));
        Assert.True(Documents.TryConvert(request, out var rendered));
        Assert.True(Paths.TryResolve(rendered, "$.logEvents[0].timestamp", out var stamped));
        Assert.Equal((double)Millis, stamped);
    }

    [Fact]
    public void AModeledTimestampStaysIsoText()
    {
        // STS's Expiration is a Smithy timestamp, not a long: no backend
        // compares one, and nothing registers it, so it keeps its ISO form.
        Assert.True(Documents.TryConvert(
            new Amazon.SecurityToken.Model.Credentials
            {
                Expiration = DateTimeOffset.FromUnixTimeMilliseconds(Millis).UtcDateTime,
            },
            out var document));
        Assert.True(Paths.TryResolve(document, "$.Expiration", out var expiration));
        Assert.Equal("2024-09-10T20:26:40.123Z", expiration);
    }

    [Theory]
    [InlineData("\"1726000000123\"", "wanted a number")]
    [InlineData("1726000000123.5", "wanted a whole number")]
    [InlineData("1e300", "wanted a number in range for long")]
    [InlineData("300000000000000", "wanted epoch milliseconds a DateTime can hold")]
    public void AValueThatIsNotEpochMillisecondsAbandonsTheCall(string json, string want)
    {
        // A mismatch is recorded, never coerced: "30" is not 30 anywhere in the
        // IR, and a value no DateTime can hold is not clamped into one.
        var bag = new ContextBag();
        using var parsed = JsonDocument.Parse(json);
        bag.Set("t", parsed.RootElement.ValueKind == JsonValueKind.String
            ? parsed.RootElement.GetString()
            : parsed.RootElement.GetDouble());
        var binder = new Binder("oc-test", "logs-events", bag);

        binder.EpochMilliseconds("timestamp", Val.Ref("t"));

        Assert.Equal("timestamp", binder.FailedMember);
        Assert.Contains(want, binder.Error!.Message, StringComparison.Ordinal);
    }

    [Fact]
    public void OnlyADateTimePropertyCanBeRegistered()
    {
        // The generator names each property through nameof, so a wrong name is
        // a compile error there; these are the run-time halves of the same
        // promise, for anything that registers by hand.
        Assert.Throws<ArgumentException>(() => Documents.EpochMilliseconds(typeof(LogStream), "NoSuchProperty"));
        Assert.Throws<ArgumentException>(() => Documents.EpochMilliseconds(typeof(LogStream), nameof(LogStream.StoredBytes)));
    }

    /// <summary>
    /// Sends one request through a real CloudWatch Logs client to an in-process
    /// listener that answers <paramref name="reply"/>, and returns the SDK's
    /// response with the body the client sent.
    /// </summary>
    private static async Task<(T Response, string Body)> Exchange<T>(string reply, Func<AmazonCloudWatchLogsClient, Task<T>> send)
    {
        var port = FreePort();
        var endpoint = $"http://127.0.0.1:{port}";
        using var listener = new HttpListener();
        listener.Prefixes.Add(endpoint + "/");
        listener.Start();
        var serving = Task.Run(async () =>
        {
            var http = await listener.GetContextAsync();
            using var reader = new StreamReader(http.Request.InputStream, Encoding.UTF8);
            var received = await reader.ReadToEndAsync();
            var bytes = Encoding.UTF8.GetBytes(reply);
            http.Response.StatusCode = 200;
            http.Response.ContentType = "application/x-amz-json-1.1";
            http.Response.ContentLength64 = bytes.Length;
            await http.Response.OutputStream.WriteAsync(bytes);
            http.Response.Close();
            return received;
        });

        var config = new AmazonCloudWatchLogsConfig
        {
            ServiceURL = endpoint,
            UseHttp = true,
            AuthenticationRegion = RegionEndpoint.USEast1.SystemName,
            // One request per exchange: the listener answers once.
            MaxErrorRetry = 0,
        };
        using var client = new AmazonCloudWatchLogsClient(Credentials, config);
        try
        {
            var response = await send(client);
            return (response, await serving.WaitAsync(TimeSpan.FromSeconds(10)));
        }
        finally
        {
            // Stopping ends a GetContextAsync that never accepted, so a send
            // that failed before reaching the listener cannot hang the run.
            listener.Stop();
        }
    }

    private static int FreePort()
    {
        var probe = new System.Net.Sockets.TcpListener(IPAddress.Loopback, 0);
        probe.Start();
        var port = ((IPEndPoint)probe.LocalEndpoint).Port;
        probe.Stop();
        return port;
    }
}
