using Amazon.SQS.Model;
using OvercastCompat.Harness;
using OvercastCompat.Scenario;
using Xunit;

namespace OvercastCompat.Tests;

/// <summary>
/// The runtime the emitted groups call into, exercised against an in-memory
/// fake.
/// </summary>
/// <remarks>
/// A ScenarioCall's SendAsync is a delegate, so a fake service is a delegate
/// returning a canned SDK response. The requests are the real SDK request
/// objects and the responses the real SDK response objects, so what these tests
/// pin about nullable properties, ConstantClass enums and absent members is
/// what a live run will see.
/// <para>Their counterpart is
/// compat/suites/go-sdk/internal/scenario/scenario_test.go: the two runtimes
/// implement the same rules (compat/model/README.md) for their own SDK, so a
/// change to one usually needs a matching change to the other.</para>
/// </remarks>
public sealed class ScenarioTests
{
    private static readonly ScenarioGroup Group = new("sqs-gen-queue", "compat/model/scenarios/sqs.json");

    private static TestContext NewContext() => new("http://127.0.0.1:1", "us-east-1", "oc-test");

    /// <summary>A SendAsync that answers with a fixed response and records the request it was given.</summary>
    private static Func<object, Task<object>> SendOk(object response, Action<object>? seen = null) =>
        request =>
        {
            seen?.Invoke(request);
            return Task.FromResult(response);
        };

    private static Func<object, Task<object>> SendError(Exception error) =>
        _ => Task.FromException<object>(error);

    /// <summary>
    /// The call the emitter writes for sqs-gen-queue/CreateQueue, hand-copied
    /// so the test exercises the shape the emitter produces.
    /// </summary>
    private static ScenarioCall CreateQueue(object response, Action<object>? seen = null) => new()
    {
        Op = "CreateQueue",
        Params = "{\"QueueName\":{\"$name\":\"q\"}}",
        Build = b =>
        {
            var request = new CreateQueueRequest();
            request.QueueName = b.Bind<string>("QueueName", Val.Name("q"));
            return request;
        },
        SendAsync = SendOk(response, seen),
        Export = new() { ["queue.url"] = "$.QueueUrl" },
    };

    [Fact]
    public async Task CallExportsAndEveryClauseHolds()
    {
        // Given: a create whose response is exported, and a read-back and a
        // list-membership clause that reference the exported value.
        object? sent = null;
        var test = new ScenarioTest
        {
            Call = CreateQueue(
                new CreateQueueResponse { QueueUrl = "http://q/oc-test-sqs-gen-queue-q" },
                request => sent = request),
            Assert =
            [
                Clause.ResponseField(Check.NonEmpty("$.QueueUrl")),
                Clause.Readback(
                    new ScenarioCall
                    {
                        Op = "GetQueueAttributes",
                        Params = "{}",
                        Build = b =>
                        {
                            var request = new GetQueueAttributesRequest();
                            request.QueueUrl = b.Bind<string>("QueueUrl", Val.Ref("queue.url"));
                            request.AttributeNames = ["All"];
                            return request;
                        },
                        SendAsync = SendOk(new GetQueueAttributesResponse
                        {
                            Attributes = new Dictionary<string, string>
                            {
                                ["QueueArn"] = "arn:aws:sqs:us-east-1:000000000000:q",
                                ["VisibilityTimeout"] = "30",
                            },
                        }),
                        Export = new() { ["queue.arn"] = "$.Attributes.QueueArn" },
                    },
                    Check.EqualTo("$.Attributes.VisibilityTimeout", "30")),
                Clause.ListContains(
                    new ScenarioCall
                    {
                        Op = "ListQueues",
                        Params = "{}",
                        Build = _ => new ListQueuesRequest(),
                        SendAsync = SendOk(new ListQueuesResponse
                        {
                            QueueUrls = ["http://q/oc-test-sqs-gen-queue-q"],
                        }),
                    },
                    "$.QueueUrls",
                    new WhereEntry("$", Val.Ref("queue.url"))),
            ],
        };

        // When: the test runs.
        var context = NewContext();
        await Group.RunTestAsync(context, "CreateQueue", test);

        // Then: the typed request carried the $name, and the read-back's export
        // reached the bag — which the second clause's $ref already proved, and
        // the third clause's where re-read.
        var request = Assert.IsType<CreateQueueRequest>(sent);
        Assert.Equal("oc-test-sqs-gen-queue-q", request.QueueName);
    }

    [Fact]
    public async Task FailureCarriesTheSixFields()
    {
        var test = new ScenarioTest
        {
            Call = CreateQueue(new CreateQueueResponse { QueueUrl = "http://q/x" }),
            Assert =
            [
                Clause.Readback(
                    new ScenarioCall
                    {
                        Op = "GetQueueAttributes",
                        Params = "{}",
                        Build = b =>
                        {
                            var request = new GetQueueAttributesRequest();
                            request.QueueUrl = b.Bind<string>("QueueUrl", Val.Ref("queue.url"));
                            return request;
                        },
                        SendAsync = SendOk(new GetQueueAttributesResponse
                        {
                            Attributes = new Dictionary<string, string> { ["VisibilityTimeout"] = "30" },
                        }),
                    },
                    Check.EqualTo("$.Attributes.VisibilityTimeout", "60")),
            ],
        };

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Group.RunTestAsync(NewContext(), "SetQueueAttributes", test));

        foreach (var field in new[]
                 {
                     "sqs-gen-queue/SetQueueAttributes",             // 1
                     "GetQueueAttributes",                           // 2
                     "params {\"QueueUrl\":\"http://q/x\"}",         // 3
                     "readback equals at $.Attributes.VisibilityTimeout", // 4
                     "expected \"60\", actual \"30\"",               // 5
                     "(compat/model/scenarios/sqs.json assert[0])",  // 6
                 })
        {
            Assert.Contains(field, failure.Message, StringComparison.Ordinal);
        }
    }

    [Fact]
    public async Task UnresolvableRefNamesThePathAndSendsNothing()
    {
        var sent = false;
        var test = new ScenarioTest
        {
            Call = new ScenarioCall
            {
                Op = "GetQueueAttributes",
                Params = "{\"QueueUrl\":{\"$ref\":\"queue.url\"}}",
                Build = b =>
                {
                    var request = new GetQueueAttributesRequest();
                    request.QueueUrl = b.Bind<string>("QueueUrl", Val.Ref("queue.url"));
                    return request;
                },
                SendAsync = request =>
                {
                    sent = true;
                    return Task.FromResult<object>(new GetQueueAttributesResponse());
                },
            },
            Assert = [Clause.ResponseField(Check.NonEmpty("$.Attributes"))],
        };

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(
            () => Group.RunTestAsync(NewContext(), "GetQueueAttributes", test));

        Assert.False(sent, "nothing may reach the wire once a value expression cannot be evaluated");
        Assert.Contains("params at queue.url", failure.Message, StringComparison.Ordinal);
        Assert.Contains("expected the context path to be set, actual <unset>", failure.Message, StringComparison.Ordinal);
        // Field 3 is the params as the scenario file writes them, because
        // nothing was sent.
        Assert.Contains("params {\"QueueUrl\":{\"$ref\":\"queue.url\"}}", failure.Message, StringComparison.Ordinal);
    }

    /// <summary>
    /// The closed check set, over one response carrying a member of each shape
    /// the checks distinguish.
    /// </summary>
    /// <remarks>
    /// One Fact rather than a Theory because Check is internal to the suite and
    /// an xunit theory parameter has to be as public as the test method.
    /// </remarks>
    [Fact]
    public async Task ChecksMatchTheIRsClosedSet()
    {
        (Check Check, bool Holds)[] cases =
        [
            // An absent member: missing and isList hold, nonEmpty does not.
            (Check.Missing("$.NextToken"), true),
            (Check.IsList("$.QueueUrls"), true),
            (Check.NonEmpty("$.QueueUrls"), false),
            // A present list, empty: isList holds, nonEmpty does not.
            (Check.IsList("$.Empty"), true),
            (Check.NonEmpty("$.Empty"), false),
            // A present value that is not a list fails isList.
            (Check.IsList("$.Name"), false),
            (Check.NonEmpty("$.Name"), true),
            (Check.Missing("$.Name"), false),
            // equals compares in the JSON type system, with no coercion.
            (Check.EqualTo("$.Name", "q"), true),
            (Check.EqualTo("$.Count", 2), true),
            (Check.EqualTo("$.Count", "2"), false),
            (Check.Matches("$.Name", "^q$"), true),
            (Check.Matches("$.Name", "^r$"), false),
        ];

        foreach (var (check, holds) in cases)
        {
            var test = new ScenarioTest
            {
                Call = new ScenarioCall
                {
                    Op = "ListQueues",
                    Params = "{}",
                    Build = _ => new ListQueuesRequest(),
                    // The response under test is a stand-in rather than one
                    // SDK response object, so one case list covers members no
                    // single response carries.
                    SendAsync = SendOk(new ProbeResponse { Name = "q", Count = 2, Empty = [] }),
                },
                Assert = [Clause.ResponseField(check)],
            };
            var failure = await Record.ExceptionAsync(() => Group.RunTestAsync(NewContext(), "ListQueues", test));
            if (holds)
            {
                Assert.True(failure is null, $"{check.Kind} at {check.Path} should hold: {failure?.Message}");
            }
            else
            {
                Assert.IsType<ScenarioFailure>(failure);
            }
        }
    }

    /// <summary>
    /// equalsJSON through the group, on the member it exists for: IAM hands
    /// AWSSDK the role's trust policy as the percent-encoded text the service
    /// sent, and the check decodes and compares it as a document.
    /// </summary>
    [Fact]
    public async Task EqualsJsonComparesTheMembersDocument()
    {
        var response = new Amazon.IdentityManagement.Model.GetRoleResponse
        {
            Role = new Amazon.IdentityManagement.Model.Role
            {
                AssumeRolePolicyDocument = "%7B%22Version%22%3A%222012-10-17%22%2C%22Statement%22%3A%5B%5D%7D",
            },
        };
        ScenarioTest Probe(Check check) => new()
        {
            Call = new ScenarioCall
            {
                Op = "GetRole",
                Params = "{}",
                Build = _ => new Amazon.IdentityManagement.Model.GetRoleRequest(),
                SendAsync = SendOk(response),
            },
            Assert = [Clause.ResponseField(check)],
        };

        await Group.RunTestAsync(NewContext(), "GetRole",
            Probe(Check.EqualsJson("$.Role.AssumeRolePolicyDocument", "{\"Statement\":[],\"Version\":\"2012-10-17\"}")));

        var failure = await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunTestAsync(NewContext(), "GetRole",
            Probe(Check.EqualsJson("$.Role.AssumeRolePolicyDocument", "{\"Version\":\"2008-10-17\"}"))));
        Assert.Contains(
            "responseField equalsJSON at $.Role.AssumeRolePolicyDocument:"
            + " expected equalsJSON {\"Version\":\"2008-10-17\"},"
            + " actual document {\"Statement\":[],\"Version\":\"2012-10-17\"}",
            failure.Message, StringComparison.Ordinal);

        failure = await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunTestAsync(NewContext(), "GetRole",
            Probe(Check.EqualsJson("$.Role.Description", "{}"))));
        Assert.Contains("expected equalsJSON {}, actual <missing>", failure.Message, StringComparison.Ordinal);
    }

    /// <summary>A stand-in response with one member of each shape the checks distinguish.</summary>
    private sealed class ProbeResponse
    {
        public string? Name { get; init; }
        public int? Count { get; init; }
        public List<string>? Empty { get; init; }
        public string? NextToken { get; init; }
        public List<string>? QueueUrls { get; init; }
    }

    [Fact]
    public async Task AnAbsentListReadsLikeAnEmptyOne()
    {
        // ListQueues legally omits QueueUrls when there is nothing to page.
        var call = new ScenarioCall
        {
            Op = "ListQueues",
            Params = "{}",
            Build = _ => new ListQueuesRequest(),
            SendAsync = SendOk(new ListQueuesResponse()),
        };

        // absent holds over it…
        await Group.RunTestAsync(NewContext(), "ListQueues", new ScenarioTest
        {
            Call = call,
            Assert = [Clause.AbsentFromList(null, "$.QueueUrls", new WhereEntry("$", "http://q/x"))],
        });

        // …and listContains does not.
        await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunTestAsync(NewContext(), "ListQueues", new ScenarioTest
        {
            Call = call,
            Assert = [Clause.ListContains(null, "$.QueueUrls", new WhereEntry("$", "http://q/x"))],
        }));
    }

    [Fact]
    public async Task EventuallyRetriesAndGivesUpWithTheSharedPrefix()
    {
        var attempts = 0;
        var call = new ScenarioCall
        {
            Op = "GetQueueAttributes",
            Params = "{}",
            Build = _ => new GetQueueAttributesRequest(),
            SendAsync = _ =>
            {
                attempts++;
                return Task.FromResult<object>(new GetQueueAttributesResponse
                {
                    Attributes = new Dictionary<string, string> { ["VisibilityTimeout"] = attempts >= 3 ? "60" : "30" },
                });
            },
        };

        // It passes on the third attempt.
        await Group.RunTestAsync(NewContext(), "SetQueueAttributes", new ScenarioTest
        {
            Call = CreateQueue(new CreateQueueResponse { QueueUrl = "http://q/x" }),
            Assert =
            [
                Clause.Eventually(5, 1, Clause.Readback(call, Check.EqualTo("$.Attributes.VisibilityTimeout", "60"))),
            ],
        });
        Assert.Equal(3, attempts);

        // And it gives up with the budget in front of the last attempt's
        // message, byte for byte as every other backend words it.
        var failure = await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunTestAsync(NewContext(), "SetQueueAttributes", new ScenarioTest
        {
            Call = CreateQueue(new CreateQueueResponse { QueueUrl = "http://q/x" }),
            Assert =
            [
                Clause.Eventually(2, 1, Clause.Readback(
                    new ScenarioCall
                    {
                        Op = "GetQueueAttributes",
                        Params = "{}",
                        Build = _ => new GetQueueAttributesRequest(),
                        SendAsync = SendOk(new GetQueueAttributesResponse
                        {
                            Attributes = new Dictionary<string, string> { ["VisibilityTimeout"] = "30" },
                        }),
                    },
                    Check.EqualTo("$.Attributes.VisibilityTimeout", "60"))),
            ],
        }));
        Assert.StartsWith("eventually gave up after 2 attempt(s) 1ms apart; last failure: ", failure.Message, StringComparison.Ordinal);
        Assert.Contains("assert[0].assert", failure.Message, StringComparison.Ordinal);
    }

    [Fact]
    public async Task EventuallyExportsOnlyOnThePassingAttempt()
    {
        var attempts = 0;
        var context = NewContext();
        await Group.RunTestAsync(context, "CreateQueue", new ScenarioTest
        {
            Call = CreateQueue(new CreateQueueResponse { QueueUrl = "http://q/x" }),
            Assert =
            [
                Clause.Eventually(5, 1, Clause.Readback(
                    new ScenarioCall
                    {
                        Op = "GetQueueAttributes",
                        Params = "{}",
                        Build = _ => new GetQueueAttributesRequest(),
                        SendAsync = _ =>
                        {
                            attempts++;
                            return Task.FromResult<object>(new GetQueueAttributesResponse
                            {
                                Attributes = new Dictionary<string, string>
                                {
                                    ["QueueArn"] = attempts >= 2 ? "arn:final" : "arn:stale",
                                    ["VisibilityTimeout"] = attempts >= 2 ? "60" : "30",
                                },
                            });
                        },
                        Export = new() { ["queue.arn"] = "$.Attributes.QueueArn" },
                    },
                    Check.EqualTo("$.Attributes.VisibilityTimeout", "60"))),
                // The failing attempt must not have left arn:stale behind for
                // this clause to read.
                Clause.ListContains(
                    new ScenarioCall
                    {
                        Op = "ListQueues",
                        Params = "{}",
                        Build = _ => new ListQueuesRequest(),
                        SendAsync = SendOk(new ListQueuesResponse { QueueUrls = ["arn:final"] }),
                    },
                    "$.QueueUrls",
                    new WhereEntry("$", Val.Ref("queue.arn"))),
            ],
        });
        Assert.Equal(2, attempts);
    }

    [Fact]
    public async Task SetupFailsLoudlyAndTeardownWrapsEveryStep()
    {
        var context = NewContext();

        // A setup that fails on its second step throws, which is what makes the
        // harness report every test in the group as skip.
        var reached = 0;
        var failure = await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunSetupAsync(
            context,
            CreateQueue(new CreateQueueResponse { QueueUrl = "http://q/x" }, _ => reached++),
            new ScenarioCall
            {
                Op = "GetQueueAttributes",
                Params = "{}",
                Build = _ => new GetQueueAttributesRequest(),
                SendAsync = SendError(new InvalidOperationException("boom")),
            }));
        Assert.Equal(1, reached);
        Assert.Contains("setup[1]", failure.Message, StringComparison.Ordinal);

        // Teardown wraps every step individually: the first fails, the second
        // still runs, and the group is not failed.
        var second = false;
        await Group.RunTeardownAsync(
            context,
            new ScenarioCall
            {
                Op = "DeleteQueue",
                Params = "{}",
                Build = _ => new DeleteQueueRequest(),
                SendAsync = SendError(new InvalidOperationException("already gone")),
            },
            new ScenarioCall
            {
                Op = "DeleteQueue",
                Params = "{}",
                Build = _ => new DeleteQueueRequest(),
                SendAsync = request =>
                {
                    second = true;
                    return Task.FromResult<object>(new DeleteQueueResponse());
                },
            });
        Assert.True(second, "a failing teardown step must not stop the ones after it");
    }

    [Fact]
    public async Task EmptyHooksAreNoOps()
    {
        var context = NewContext();
        await Group.RunSetupAsync(context);
        await Group.RunTeardownAsync(context);
    }

    [Fact]
    public async Task ErrorClausesAcceptEitherSpelling()
    {
        var missing = new QueueDoesNotExistException("The specified queue does not exist.")
        {
            ErrorCode = "QueueDoesNotExist",
        };
        var call = new ScenarioCall
        {
            Op = "GetQueueAttributes",
            Params = "{}",
            Build = _ => new GetQueueAttributesRequest(),
            SendAsync = SendError(missing),
        };
        var spec = new ErrorSpec("QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue");

        // errorCode: the test's own call is expected to fail.
        await Group.RunTestAsync(NewContext(), "DeleteQueue", new ScenarioTest
        {
            Call = call,
            Assert = [Clause.ErrorCode(spec)],
        });

        // absent's error form: a clause's own call is expected to fail.
        await Group.RunTestAsync(NewContext(), "DeleteQueue", new ScenarioTest
        {
            Call = CreateQueue(new CreateQueueResponse { QueueUrl = "http://q/x" }),
            Assert = [Clause.AbsentByError(call, spec)],
        });

        // A call that succeeds where an error was expected fails the clause.
        var failure = await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunTestAsync(NewContext(), "DeleteQueue", new ScenarioTest
        {
            Call = new ScenarioCall
            {
                Op = "GetQueueAttributes",
                Params = "{}",
                Build = _ => new GetQueueAttributesRequest(),
                SendAsync = SendOk(new GetQueueAttributesResponse()),
            },
            Assert = [Clause.ErrorCode(spec)],
        }));
        Assert.Contains("actual <no error>", failure.Message, StringComparison.Ordinal);
    }

    [Fact]
    public async Task UnimplementedReachesTheHarnessAsAClassification()
    {
        var notImplemented = new Amazon.SQS.AmazonSQSException("not implemented")
        {
            StatusCode = System.Net.HttpStatusCode.NotImplemented,
        };
        var failure = await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunTestAsync(NewContext(), "ListMessageMoveTasks", new ScenarioTest
        {
            Call = new ScenarioCall
            {
                Op = "ListMessageMoveTasks",
                Params = "{}",
                Build = _ => new ListMessageMoveTasksRequest(),
                SendAsync = SendError(notImplemented),
            },
            Assert = [Clause.ResponseField(Check.IsList("$.Results"))],
        }));
        Assert.True(failure.Unimplemented);
        Assert.True(Runner.IsUnimplemented(failure));
    }

    [Fact]
    public async Task AComposedFailureIsNeverSniffedFor501()
    {
        // A run id with 501 in it, and a params JSON carrying it, is exactly
        // what the substring heuristic gets wrong. The composed failure states
        // the classification instead.
        var context = new TestContext("http://127.0.0.1:4501", "us-east-1", "oc-501");
        var failure = await Assert.ThrowsAsync<ScenarioFailure>(() => Group.RunTestAsync(context, "CreateQueue", new ScenarioTest
        {
            Call = CreateQueue(new CreateQueueResponse { QueueUrl = "http://q/x" }),
            Assert = [Clause.ResponseField(Check.NonEmpty("$.MissingMember"))],
        }));
        Assert.Contains("501", failure.Message, StringComparison.Ordinal);
        Assert.False(failure.Unimplemented);
        Assert.False(Runner.IsUnimplemented(failure), "a composed message must never be sniffed for 501");
    }

    [Fact]
    public void NameIsRunIdGroupSuffix()
    {
        var binder = new Binder("oc-test", "sqs-gen-queue", new ContextBag());
        Assert.Equal("oc-test-sqs-gen-queue-q", binder.Bind<string>("QueueName", Val.Name("q")));
    }

    [Fact]
    public void ConcatAndIndexEvaluateThroughTheContext()
    {
        var bag = new ContextBag();
        bag.Set("dlq.arn", "arn:aws:sqs:us-east-1:000000000000:dlq");
        bag.Set("queue.urls", new List<object?> { "http://q/a", "http://q/b" });
        var binder = new Binder("oc-test", "sqs-gen-queue", bag);

        Assert.Equal(
            "{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:000000000000:dlq\"}",
            binder.Bind<string>("Attributes", Val.Concat("{\"deadLetterTargetArn\":\"", Val.Ref("dlq.arn"), "\"}")));
        Assert.Equal("http://q/b", binder.Bind<string>("QueueUrl", Val.Index(Val.Ref("queue.urls"), 1)));
    }

    [Fact]
    public void BindConvertsEveryScalarTheGeneratorCanSpell()
    {
        var binder = new Binder("oc-test", "sqs-gen-queue", new ContextBag());
        Assert.Equal("blue", binder.Bind<string>("Color", "blue"));
        Assert.True(binder.Bind<bool>("Force", true));
        Assert.Equal(30, binder.Bind<int>("VisibilityTimeout", 30));
        Assert.Equal(30L, binder.Bind<long>("Size", 30));
        Assert.Equal(1.5, binder.Bind<double>("Ratio", 1.5));
        Assert.Null(binder.Error);
    }

    [Fact]
    public void BindStopsAtTheFirstFailureAndNamesTheMember()
    {
        var binder = new Binder("oc-test", "sqs-gen-queue", new ContextBag());
        // "30" is not 30 anywhere else in the IR, and is not coerced here.
        Assert.Equal(0, binder.Bind<int>("VisibilityTimeout", "30"));
        Assert.NotNull(binder.Error);
        Assert.Equal("VisibilityTimeout", binder.FailedMember);

        // Every later assignment is a no-op, so one failure abandons the call
        // rather than compounding.
        Assert.Null(binder.Bind<string>("QueueUrl", Val.Name("q")));
        Assert.Equal("VisibilityTimeout", binder.FailedMember);
    }

    [Fact]
    public void RefErrorNamesThePath()
    {
        var binder = new Binder("oc-test", "sqs-gen-queue", new ContextBag());
        binder.Bind<string>("QueueUrl", Val.Ref("queue.url"));
        var unset = Assert.IsType<ContextPathUnsetException>(binder.Error);
        Assert.Equal("queue.url", unset.Path);
    }
}
