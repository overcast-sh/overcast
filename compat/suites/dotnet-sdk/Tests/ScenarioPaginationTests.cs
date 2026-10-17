using Amazon.SQS.Model;
using OvercastCompat.Harness;
using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The paginated list clause (compat/model/README.md § Paginating a list): a
/// walk to the last page, a match against the union of the pages, a floor on
/// how many pages came back, and a cap so a service that never stops fails
/// instead of hanging.
/// </summary>
/// <remarks>
/// The calls are the two the emitter writes for a paginated clause — page one
/// with the page size set, and the same with the input token read from
/// <see cref="PageWalk.TokenKey"/> — over a fake ListQueues that answers from a
/// script and records every request it was sent. The counterpart is
/// compat/suites/go-sdk/internal/scenario/paginate_test.go.
/// </remarks>
public sealed class ScenarioPaginationTests
{
    private static readonly ScenarioGroup Group = new("sqs-gen-queue", "compat/model/scenarios/sqs.json");

    private static TestContext NewContext() => new("http://127.0.0.1:1", "us-east-1", "oc-test");

    /// <summary>A fake ListQueues: pages in order, the last repeating, every request recorded.</summary>
    private sealed class Queues
    {
        private readonly List<ListQueuesResponse> _pages = [];

        public List<ListQueuesRequest> Seen { get; } = [];

        public Exception? FailOnSecondCall { get; set; }

        public Queues Page(string? token, params string[] urls)
        {
            _pages.Add(new ListQueuesResponse { QueueUrls = [.. urls], NextToken = token });
            return this;
        }

        public Func<object, Task<object>> Send => request =>
        {
            var n = Seen.Count;
            Seen.Add((ListQueuesRequest)request);
            if (n == 1 && FailOnSecondCall is not null)
            {
                return Task.FromException<object>(FailOnSecondCall);
            }
            return Task.FromResult<object>(_pages[Math.Min(n, _pages.Count - 1)]);
        };

        public string Sent() =>
            string.Join(" | ", Seen.Select(r => $"size={r.MaxResults} token={r.NextToken ?? "none"}"));
    }

    private static Clause PagedList(Queues queues, bool absent, string want, int minPages = 0)
    {
        var first = new ScenarioCall
        {
            Op = "ListQueues",
            Params = "{\"MaxResults\":1,\"QueueNamePrefix\":\"oc-\"}",
            Build = _ => new ListQueuesRequest { MaxResults = 1, QueueNamePrefix = "oc-" },
            SendAsync = queues.Send,
        };
        var next = new ScenarioCall
        {
            Op = "ListQueues",
            Params = "{\"MaxResults\":1,\"NextToken\":{\"$ref\":\"$pageToken\"},\"QueueNamePrefix\":\"oc-\"}",
            Build = b =>
            {
                var request = new ListQueuesRequest { MaxResults = 1, QueueNamePrefix = "oc-" };
                request.NextToken = b.Bind<string>("NextToken", Val.Ref(PageWalk.TokenKey));
                return request;
            },
            SendAsync = queues.Send,
        };
        var list = absent
            ? Clause.AbsentFromList(first, "$.QueueUrls", new WhereEntry("$", want))
            : Clause.ListContains(first, "$.QueueUrls", new WhereEntry("$", want));
        return list.Paginated(new PageWalk(1, minPages, "NextToken", next));
    }

    private static Task Run(Clause clause) =>
        Group.RunTestAsync(NewContext(), "ListQueues", new ScenarioTest
        {
            Call = new ScenarioCall
            {
                Op = "CreateQueue",
                Params = "{}",
                Build = _ => new CreateQueueRequest { QueueName = "q" },
                SendAsync = SendOk(new CreateQueueResponse { QueueUrl = "http://q/created" }),
            },
            Assert = [clause],
        });

    private static Func<object, Task<object>> SendOk(object response) => _ => Task.FromResult(response);

    [Fact]
    public async Task WalksEveryPageFeedingTheTokenBack()
    {
        var queues = new Queues().Page("t1", "http://a").Page("t2", "http://b").Page(null, "http://q");

        await Run(PagedList(queues, absent: false, "http://q"));

        Assert.Equal("size=1 token=none | size=1 token=t1 | size=1 token=t2", queues.Sent());
    }

    [Fact]
    public async Task KeepsWalkingAfterTheItemIsFound()
    {
        var queues = new Queues().Page("t1", "http://q").Page(null, "http://b");

        await Run(PagedList(queues, absent: false, "http://q"));

        Assert.Equal(2, queues.Seen.Count);
    }

    [Fact]
    public async Task AnEmptyTokenEndsTheWalk()
    {
        var queues = new Queues().Page("", "http://q");

        await Run(PagedList(queues, absent: false, "http://q"));

        Assert.Single(queues.Seen);
    }

    [Fact]
    public async Task AMissSaysHowManyPagesWereWalkedAndQuotesPageOnesParams()
    {
        var queues = new Queues().Page("t1", "http://a").Page("t2", "http://b").Page(null, "http://c");

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Run(PagedList(queues, absent: false, "http://q")));

        Assert.Contains("walked 3 page(s), 3 item(s): ", failure.Message, StringComparison.Ordinal);
        Assert.Contains("\"http://a\"", failure.Message, StringComparison.Ordinal);
        Assert.Contains("listContains at $.QueueUrls", failure.Message, StringComparison.Ordinal);
        var params_ = failure.Message[..failure.Message.IndexOf("expected", StringComparison.Ordinal)];
        Assert.Contains("MaxResults", params_, StringComparison.Ordinal);
        Assert.DoesNotContain("NextToken", params_, StringComparison.Ordinal);
    }

    [Fact]
    public async Task ListContainsStillNeedsANonEmptyList()
    {
        var queues = new Queues().Page(null);

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Run(PagedList(queues, absent: false, "http://q")));

        Assert.Contains("walked 1 page(s), 0 item(s)", failure.Message, StringComparison.Ordinal);
    }

    [Fact]
    public async Task MinPagesFailsASinglePageThatHoldsEverything()
    {
        var single = new Queues().Page(null, "http://q");

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Run(PagedList(single, absent: false, "http://q", minPages: 2)));

        Assert.Contains("expected at least 2 page(s) at a page size of 1", failure.Message, StringComparison.Ordinal);
        Assert.Contains("actual walked 1 page(s), 1 item(s)", failure.Message, StringComparison.Ordinal);
        await Run(PagedList(new Queues().Page("t", "http://a").Page(null, "http://q"), absent: false, "http://q", minPages: 2));
    }

    [Fact]
    public async Task AServiceThatNeverStopsFailsAtThePageCap()
    {
        var queues = new Queues().Page("again", "http://a");

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Run(PagedList(queues, absent: false, "http://q")));

        Assert.Equal(PageWalk.MaxPages, queues.Seen.Count);
        Assert.Contains($"no NextToken on a page within {PageWalk.MaxPages} pages", failure.Message, StringComparison.Ordinal);
        Assert.Contains(
            $"walked {PageWalk.MaxPages} page(s), {PageWalk.MaxPages} item(s), and the last still returned a NextToken",
            failure.Message,
            StringComparison.Ordinal);
    }

    [Fact]
    public async Task AbsentLooksAtEveryPage()
    {
        await Run(PagedList(new Queues().Page("t", "http://a").Page(null, "http://b"), absent: true, "http://q"));

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Run(PagedList(new Queues().Page("t", "http://a").Page(null, "http://q"), absent: true, "http://q")));

        Assert.Contains("walked 2 page(s), 2 item(s): ", failure.Message, StringComparison.Ordinal);
    }

    [Fact]
    public async Task ACallFailingOnALaterPageIsACallFailureCarryingThatPagesParams()
    {
        var queues = new Queues { FailOnSecondCall = new InvalidOperationException("boom") }.Page("t1", "http://a");

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Run(PagedList(queues, absent: false, "http://q")));

        Assert.Contains("boom", failure.Message, StringComparison.Ordinal);
        Assert.Contains("\"NextToken\":\"t1\"", failure.Message, StringComparison.Ordinal);
    }

    [Fact]
    public async Task InsideEventuallyEveryAttemptWalksAgain()
    {
        var queues = new Queues()
            .Page("t1", "http://a").Page(null, "http://b")   // attempt 1: no match
            .Page("t1", "http://a").Page(null, "http://q");  // attempt 2: match

        await Run(Clause.Eventually(3, 0, PagedList(queues, absent: false, "http://q")));

        Assert.Equal(
            "size=1 token=none | size=1 token=t1 | size=1 token=none | size=1 token=t1",
            queues.Sent());
    }
}
