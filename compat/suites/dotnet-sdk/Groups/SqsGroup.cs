using Amazon.SQS.Model;
using OvercastCompat.Clients;
using OvercastCompat.Harness;

namespace OvercastCompat.Groups;

public sealed class SqsGroup(AwsClients clients) : IServiceGroup
{
    public IReadOnlyDictionary<string, TestFn> Impls() => new Dictionary<string, TestFn>(StringComparer.Ordinal)
    {
        ["sqs-messages:SendMessage"] = SendMessageAsync,
        ["sqs-messages:SendMessageBatch"] = SendMessageBatchAsync,
        ["sqs-messages:ReceiveMessage"] = ReceiveMessageAsync,
        ["sqs-messages:DeleteMessage"] = DeleteMessageAsync,
        ["sqs-messages:ChangeMessageVisibility"] = ChangeMessageVisibilityAsync,
        ["sqs-messages:DeleteMessageBatch"] = DeleteMessageBatchAsync,
        ["sqs-messages:PurgeQueue"] = PurgeQueueAsync,
        ["sqs-dlq:CreateDLQ"] = CreateDLQAsync,
        ["sqs-dlq:SetRedrivePolicy"] = SetRedrivePolicyAsync,
        ["sqs-dlq:GetRedrivePolicy"] = GetRedrivePolicyAsync,
        ["sqs-fifo:CreateFifoQueue"] = CreateFifoQueueAsync,
        ["sqs-fifo:SendFifoMessage"] = SendFifoMessageAsync,
        ["sqs-fifo:ReceiveFifoMessage"] = ReceiveFifoMessageAsync,
    };

    public IReadOnlyDictionary<string, SetupFn> Setups() => new Dictionary<string, SetupFn>(StringComparer.Ordinal)
    {
        ["sqs-messages"] = SetupMessagesAsync,
        ["sqs-dlq"] = SetupDlqAsync,
    };

    public IReadOnlyDictionary<string, SetupFn> Teardowns() => new Dictionary<string, SetupFn>(StringComparer.Ordinal)
    {
        ["sqs-messages"] = TeardownMessagesAsync,
        ["sqs-dlq"] = TeardownDlqAsync,
        ["sqs-fifo"] = TeardownFifoAsync,
    };

    // ── sqs-messages ──

    private async Task SetupMessagesAsync(TestContext context)
    {
        var name = $"{context.RunId}-sqs-msg";
        var response = await clients.SQS().CreateQueueAsync(new CreateQueueRequest { QueueName = name });
        context.Set("sqsMsgUrl", response.QueueUrl);
    }

    private async Task SendMessageAsync(TestContext context)
    {
        var url = RequireQueueUrl(context, "sqsMsgUrl");
        var response = await clients.SQS().SendMessageAsync(new SendMessageRequest
        {
            QueueUrl = url,
            MessageBody = "hello from dotnet-sdk",
        });
        Assertions.NotBlank(response.MessageId, "SendMessage: MessageId");
    }

    private async Task SendMessageBatchAsync(TestContext context)
    {
        var url = RequireQueueUrl(context, "sqsMsgUrl");
        var response = await clients.SQS().SendMessageBatchAsync(new SendMessageBatchRequest
        {
            QueueUrl = url,
            Entries =
            [
                new SendMessageBatchRequestEntry { Id = "a", MessageBody = "batch-a" },
                new SendMessageBatchRequestEntry { Id = "b", MessageBody = "batch-b" },
            ],
        });
        Assertions.GreaterThanOrEqual(2, response.Successful.Count, "SendMessageBatch: expected >= 2 successful");
        Assertions.NotBlank(response.Successful[0].MessageId, "SendMessageBatch: MessageId[0]");
    }

    private async Task ReceiveMessageAsync(TestContext context)
    {
        var url = RequireQueueUrl(context, "sqsMsgUrl");
        await clients.SQS().SendMessageAsync(new SendMessageRequest { QueueUrl = url, MessageBody = "receive-test" });
        List<Message> messages = new();
        for (var i = 0; i < 5; i++)
        {
            var response = await clients.SQS().ReceiveMessageAsync(new ReceiveMessageRequest
            {
                QueueUrl = url,
                MaxNumberOfMessages = 10,
                WaitTimeSeconds = 1,
            });
            messages = response.Messages;
            if (messages.Count > 0)
            {
                break;
            }
        }
        Assertions.True(messages.Count > 0, "ReceiveMessage: expected at least 1 message");
    }

    private async Task DeleteMessageAsync(TestContext context)
    {
        var url = RequireQueueUrl(context, "sqsMsgUrl");
        await clients.SQS().SendMessageAsync(new SendMessageRequest { QueueUrl = url, MessageBody = "delete-me" });
        var recv = await clients.SQS().ReceiveMessageAsync(new ReceiveMessageRequest
        {
            QueueUrl = url,
            MaxNumberOfMessages = 1,
            WaitTimeSeconds = 3,
        });
        Assertions.True(recv.Messages.Count > 0, "DeleteMessage: expected at least 1 message");
        var handle = recv.Messages[0].ReceiptHandle;
        Assertions.NotBlank(handle, "DeleteMessage: ReceiptHandle");
        await clients.SQS().DeleteMessageAsync(new DeleteMessageRequest { QueueUrl = url, ReceiptHandle = handle });
    }

    private async Task ChangeMessageVisibilityAsync(TestContext context)
    {
        var url = RequireQueueUrl(context, "sqsMsgUrl");
        await clients.SQS().SendMessageAsync(new SendMessageRequest { QueueUrl = url, MessageBody = "visibility-test" });
        var recv = await clients.SQS().ReceiveMessageAsync(new ReceiveMessageRequest
        {
            QueueUrl = url,
            MaxNumberOfMessages = 1,
            WaitTimeSeconds = 3,
        });
        Assertions.True(recv.Messages.Count > 0, "ChangeMessageVisibility: expected at least 1 message");
        await clients.SQS().ChangeMessageVisibilityAsync(new ChangeMessageVisibilityRequest
        {
            QueueUrl = url,
            ReceiptHandle = recv.Messages[0].ReceiptHandle,
            VisibilityTimeout = 30,
        });
    }

    private async Task DeleteMessageBatchAsync(TestContext context)
    {
        var url = RequireQueueUrl(context, "sqsMsgUrl");
        await clients.SQS().SendMessageAsync(new SendMessageRequest { QueueUrl = url, MessageBody = "batch-delete-a" });
        await clients.SQS().SendMessageAsync(new SendMessageRequest { QueueUrl = url, MessageBody = "batch-delete-b" });
        var recv = await clients.SQS().ReceiveMessageAsync(new ReceiveMessageRequest
        {
            QueueUrl = url,
            MaxNumberOfMessages = 10,
            WaitTimeSeconds = 3,
        });
        Assertions.GreaterThanOrEqual(2, recv.Messages.Count, "DeleteMessageBatch: expected >= 2 messages");
        var batchResp = await clients.SQS().DeleteMessageBatchAsync(new DeleteMessageBatchRequest
        {
            QueueUrl = url,
            Entries = recv.Messages.Select((msg, idx) => new DeleteMessageBatchRequestEntry
            {
                Id = (idx + 1).ToString(),
                ReceiptHandle = msg.ReceiptHandle,
            }).ToList(),
        });
        Assertions.GreaterThanOrEqual(2, batchResp.Successful.Count, "DeleteMessageBatch: expected >= 2 successful deletes");
    }

    private async Task PurgeQueueAsync(TestContext context)
    {
        var url = RequireQueueUrl(context, "sqsMsgUrl");
        await clients.SQS().SendMessageAsync(new SendMessageRequest { QueueUrl = url, MessageBody = "purge-test" });
        await clients.SQS().PurgeQueueAsync(new PurgeQueueRequest { QueueUrl = url });
        var recv = await clients.SQS().ReceiveMessageAsync(new ReceiveMessageRequest
        {
            QueueUrl = url,
            MaxNumberOfMessages = 10,
            WaitTimeSeconds = 0,
        });
        // The AWS SDK for .NET v4 leaves response collections null when the
        // service returns none, so ".Count" throws rather than reading 0.
        Assertions.Equal(0, recv.Messages?.Count ?? 0, "PurgeQueue: expected no messages after purge");
    }

    private async Task TeardownMessagesAsync(TestContext context)
    {
        var url = context.GetString("sqsMsgUrl");
        if (!string.IsNullOrWhiteSpace(url))
        {
            try { await clients.SQS().DeleteQueueAsync(new DeleteQueueRequest { QueueUrl = url }); } catch { }
        }
    }

    // ── sqs-dlq ──

    private async Task SetupDlqAsync(TestContext context)
    {
        var srcName = $"{context.RunId}-sqs-src";
        var dlqName = $"{context.RunId}-sqs-dlq";
        var srcResponse = await clients.SQS().CreateQueueAsync(new CreateQueueRequest { QueueName = srcName });
        var dlqResponse = await clients.SQS().CreateQueueAsync(new CreateQueueRequest { QueueName = dlqName });
        context.Set("sqsSrcUrl", srcResponse.QueueUrl);
        context.Set("sqsDlqUrl", dlqResponse.QueueUrl);
    }

    private async Task CreateDLQAsync(TestContext context)
    {
        var srcUrl = context.GetString("sqsSrcUrl") ?? throw new InvalidOperationException("sqsSrcUrl not set");
        var dlqUrl = context.GetString("sqsDlqUrl") ?? throw new InvalidOperationException("sqsDlqUrl not set");
        var list = await clients.SQS().ListQueuesAsync(new ListQueuesRequest { QueueNamePrefix = $"{context.RunId}-sqs-src" });
        Assertions.True(list.QueueUrls.Any(u => u == srcUrl), $"CreateDLQ: src queue not found (runId={context.RunId})");
        var listDlq = await clients.SQS().ListQueuesAsync(new ListQueuesRequest { QueueNamePrefix = $"{context.RunId}-sqs-dlq" });
        Assertions.True(listDlq.QueueUrls.Any(u => u == dlqUrl), $"CreateDLQ: dlq queue not found (runId={context.RunId})");
    }

    private async Task SetRedrivePolicyAsync(TestContext context)
    {
        var srcUrl = context.GetString("sqsSrcUrl") ?? throw new InvalidOperationException("sqsSrcUrl not set");
        var dlqUrl = context.GetString("sqsDlqUrl") ?? throw new InvalidOperationException("sqsDlqUrl not set");
        var dlqAttrs = await clients.SQS().GetQueueAttributesAsync(new GetQueueAttributesRequest
        {
            QueueUrl = dlqUrl,
            AttributeNames = ["QueueArn"],
        });
        var dlqArn = dlqAttrs.Attributes["QueueArn"];
        var policy = $"{{\"maxReceiveCount\":\"3\",\"deadLetterTargetArn\":\"{dlqArn}\"}}";
        await clients.SQS().SetQueueAttributesAsync(new SetQueueAttributesRequest
        {
            QueueUrl = srcUrl,
            Attributes = new Dictionary<string, string> { ["RedrivePolicy"] = policy },
        });
        var srcAttrs = await clients.SQS().GetQueueAttributesAsync(new GetQueueAttributesRequest
        {
            QueueUrl = srcUrl,
            AttributeNames = ["RedrivePolicy"],
        });
        Assertions.True(srcAttrs.Attributes.ContainsKey("RedrivePolicy"), "SetRedrivePolicy: RedrivePolicy not found");
        Assertions.True(srcAttrs.Attributes["RedrivePolicy"].Contains("deadLetterTargetArn"), "SetRedrivePolicy: missing deadLetterTargetArn");
    }

    private async Task GetRedrivePolicyAsync(TestContext context)
    {
        var srcUrl = context.GetString("sqsSrcUrl") ?? throw new InvalidOperationException("sqsSrcUrl not set");
        var attrs = await clients.SQS().GetQueueAttributesAsync(new GetQueueAttributesRequest
        {
            QueueUrl = srcUrl,
            AttributeNames = ["RedrivePolicy"],
        });
        Assertions.True(attrs.Attributes.ContainsKey("RedrivePolicy"), "GetRedrivePolicy: RedrivePolicy not found");
        Assertions.True(attrs.Attributes["RedrivePolicy"].Contains("maxReceiveCount"), "GetRedrivePolicy: missing maxReceiveCount");
    }

    private async Task TeardownDlqAsync(TestContext context)
    {
        var srcUrl = context.GetString("sqsSrcUrl");
        if (!string.IsNullOrWhiteSpace(srcUrl))
        {
            try { await clients.SQS().DeleteQueueAsync(new DeleteQueueRequest { QueueUrl = srcUrl }); } catch { }
        }
        var dlqUrl = context.GetString("sqsDlqUrl");
        if (!string.IsNullOrWhiteSpace(dlqUrl))
        {
            try { await clients.SQS().DeleteQueueAsync(new DeleteQueueRequest { QueueUrl = dlqUrl }); } catch { }
        }
    }

    // ── sqs-fifo ──

    private async Task CreateFifoQueueAsync(TestContext context)
    {
        var name = $"{context.RunId}-sqs-fifo.fifo";
        var response = await clients.SQS().CreateQueueAsync(new CreateQueueRequest
        {
            QueueName = name,
            Attributes = new Dictionary<string, string> { ["FifoQueue"] = "true" },
        });
        var url = response.QueueUrl;
        Assertions.NotBlank(url, "CreateFifoQueue: url");
        context.Set("sqsFifoUrl", url);
    }

    private async Task SendFifoMessageAsync(TestContext context)
    {
        var url = context.GetString("sqsFifoUrl") ?? throw new InvalidOperationException("sqsFifoUrl not set");
        var response = await clients.SQS().SendMessageAsync(new SendMessageRequest
        {
            QueueUrl = url,
            MessageBody = "fifo-message",
            MessageGroupId = "test-group",
            MessageDeduplicationId = $"{context.RunId}-dedup-1",
        });
        Assertions.NotBlank(response.MessageId, "SendFifoMessage: MessageId");
    }

    private async Task ReceiveFifoMessageAsync(TestContext context)
    {
        var url = context.GetString("sqsFifoUrl") ?? throw new InvalidOperationException("sqsFifoUrl not set");
        List<Message> messages = new();
        for (var i = 0; i < 5; i++)
        {
            var response = await clients.SQS().ReceiveMessageAsync(new ReceiveMessageRequest
            {
                QueueUrl = url,
                MaxNumberOfMessages = 10,
                WaitTimeSeconds = 1,
            });
            messages = response.Messages;
            if (messages.Count > 0)
            {
                break;
            }
        }
        Assertions.True(messages.Count > 0, "ReceiveFifoMessage: expected at least 1 message");
    }

    private async Task TeardownFifoAsync(TestContext context)
    {
        var url = context.GetString("sqsFifoUrl");
        if (!string.IsNullOrWhiteSpace(url))
        {
            try { await clients.SQS().DeleteQueueAsync(new DeleteQueueRequest { QueueUrl = url }); } catch { }
        }
    }

    private static string RequireQueueUrl(TestContext context, string key)
    {
        return context.GetString(key) ?? throw new InvalidOperationException($"{key} not set");
    }
}
