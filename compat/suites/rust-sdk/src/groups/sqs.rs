use std::collections::HashMap;
use std::sync::Arc;

use aws_sdk_sqs::types::{
    DeleteMessageBatchRequestEntry, QueueAttributeName, SendMessageBatchRequestEntry,
};

use crate::clients::AwsClients;
use crate::groups::ServiceGroup;
use crate::harness::{TestContext, TestFn};

pub struct SqsGroup {
    clients: Arc<AwsClients>,
}

impl SqsGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for SqsGroup {
    fn name(&self) -> &'static str {
        "sqs"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        // ── sqs-queues ──────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:CreateQueue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = format!("{}-sqs-c", ctx.run_id.as_ref());
                    let response = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let url = response
                        .queue_url()
                        .ok_or_else(|| "CreateQueue: queue_url missing".to_string())?;
                    if url.is_empty() || !url.contains(&queue_name) {
                        return Err(format!(
                            "CreateQueue: queue_url {url} does not contain queue name {queue_name}"
                        ));
                    }
                    let list_resp = clients
                        .sqs()
                        .list_queues()
                        .queue_name_prefix(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = list_resp.queue_urls().iter().any(|u| u == url);
                    let _ = clients.sqs().delete_queue().queue_url(url).send().await;
                    found.then_some(()).ok_or_else(|| {
                        format!(
                            "CreateQueue: queue {queue_name} not found in ListQueues (runId={})",
                            ctx.run_id
                        )
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:GetQueueUrl".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = ctx
                        .get("queueName")
                        .ok_or_else(|| "queueName not set".to_string())?;
                    let response = clients
                        .sqs()
                        .get_queue_url()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let url = response
                        .queue_url()
                        .ok_or_else(|| "GetQueueUrl: queue_url missing".to_string())?;
                    (!url.is_empty() && url.contains(&queue_name))
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetQueueUrl: url {url} does not contain queue name {queue_name}"
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:ListQueues".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = ctx
                        .get("queueName")
                        .ok_or_else(|| "queueName not set".to_string())?;
                    let response = clients
                        .sqs()
                        .list_queues()
                        .queue_name_prefix(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .queue_urls()
                        .iter()
                        .any(|u| u.contains(&queue_name));
                    found.then_some(()).ok_or_else(|| {
                        format!(
                            "ListQueues: queue {queue_name} not found (runId={})",
                            ctx.run_id
                        )
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:SetQueueAttributes".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueUrl")
                        .ok_or_else(|| "queueUrl not set".to_string())?;
                    clients
                        .sqs()
                        .set_queue_attributes()
                        .queue_url(&queue_url)
                        .attributes(QueueAttributeName::VisibilityTimeout, "60")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:GetQueueAttributes".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueUrl")
                        .ok_or_else(|| "queueUrl not set".to_string())?;
                    let response = clients
                        .sqs()
                        .get_queue_attributes()
                        .queue_url(&queue_url)
                        .attribute_names(QueueAttributeName::VisibilityTimeout)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let attrs = response
                        .attributes()
                        .ok_or_else(|| "GetQueueAttributes: attributes missing".to_string())?;
                    let timeout = attrs
                        .get(&QueueAttributeName::VisibilityTimeout)
                        .map(|s| s.as_str())
                        .unwrap_or_default();
                    (timeout == "60").then_some(()).ok_or_else(|| {
                        format!("GetQueueAttributes: expected VisibilityTimeout=60, got {timeout}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:TagQueue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueUrl")
                        .ok_or_else(|| "queueUrl not set".to_string())?;
                    clients
                        .sqs()
                        .tag_queue()
                        .queue_url(&queue_url)
                        .tags("project", "overcast")
                        .tags("env", "test")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let list_resp = clients
                        .sqs()
                        .list_queue_tags()
                        .queue_url(&queue_url)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response_tags = list_resp
                        .tags()
                        .ok_or_else(|| "TagQueue: tags missing".to_string())?;
                    let project = response_tags
                        .get("project")
                        .map(|s| s.as_str())
                        .unwrap_or_default();
                    (project == "overcast").then_some(()).ok_or_else(|| {
                        format!("TagQueue: expected project=overcast, got {project}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:UntagQueue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueUrl")
                        .ok_or_else(|| "queueUrl not set".to_string())?;
                    clients
                        .sqs()
                        .untag_queue()
                        .queue_url(&queue_url)
                        .tag_keys("env")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let list_resp = clients
                        .sqs()
                        .list_queue_tags()
                        .queue_url(&queue_url)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response_tags = list_resp
                        .tags()
                        .ok_or_else(|| "UntagQueue: tags missing".to_string())?;
                    (response_tags.get("env").is_none())
                        .then_some(())
                        .ok_or_else(|| "UntagQueue: env tag should be absent".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-queues:DeleteQueue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = ctx
                        .get("queueName")
                        .ok_or_else(|| "queueName not set".to_string())?;
                    let url_resp = clients
                        .sqs()
                        .get_queue_url()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let queue_url = url_resp
                        .queue_url()
                        .ok_or_else(|| "DeleteQueue: queue_url missing".to_string())?;
                    clients
                        .sqs()
                        .delete_queue()
                        .queue_url(queue_url)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let list_resp = clients
                        .sqs()
                        .list_queues()
                        .queue_name_prefix(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = list_resp
                        .queue_urls()
                        .iter()
                        .any(|u| u.contains(&queue_name));
                    (!found).then_some(()).ok_or_else(|| {
                        format!("DeleteQueue: queue {queue_name} still present after deletion")
                    })
                })
            }),
        );

        // ── sqs-messages ────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "sqs-messages:SendMessage".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueMsgUrl")
                        .ok_or_else(|| "queueMsgUrl not set".to_string())?;
                    let response = clients
                        .sqs()
                        .send_message()
                        .queue_url(&queue_url)
                        .message_body("hello-sqs")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let message_id = response
                        .message_id()
                        .ok_or_else(|| "SendMessage: message_id missing".to_string())?;
                    (!message_id.is_empty())
                        .then_some(())
                        .ok_or_else(|| "SendMessage: message_id is empty".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-messages:SendMessageBatch".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueMsgUrl")
                        .ok_or_else(|| "queueMsgUrl not set".to_string())?;
                    let entry1 = SendMessageBatchRequestEntry::builder()
                        .id("1")
                        .message_body("batch-1")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let entry2 = SendMessageBatchRequestEntry::builder()
                        .id("2")
                        .message_body("batch-2")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let response = clients
                        .sqs()
                        .send_message_batch()
                        .queue_url(&queue_url)
                        .entries(entry1)
                        .entries(entry2)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let count = response.successful().len();
                    (count >= 2).then_some(()).ok_or_else(|| {
                        format!("SendMessageBatch: expected >= 2 successful, got {count}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-messages:ReceiveMessage".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueMsgUrl")
                        .ok_or_else(|| "queueMsgUrl not set".to_string())?;
                    for _ in 0..5 {
                        let response = clients
                            .sqs()
                            .receive_message()
                            .queue_url(&queue_url)
                            .max_number_of_messages(10)
                            .wait_time_seconds(1)
                            .send()
                            .await
                            .map_err(crate::harness::sdk_error)?;
                        if !response.messages().is_empty() {
                            return Ok(());
                        }
                    }
                    Err("ReceiveMessage: no messages received after 5 polls".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-messages:DeleteMessage".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueMsgUrl")
                        .ok_or_else(|| "queueMsgUrl not set".to_string())?;
                    clients
                        .sqs()
                        .send_message()
                        .queue_url(&queue_url)
                        .message_body("to-delete")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let recv = clients
                        .sqs()
                        .receive_message()
                        .queue_url(&queue_url)
                        .max_number_of_messages(1)
                        .wait_time_seconds(5)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let msg = recv
                        .messages()
                        .first()
                        .ok_or_else(|| "DeleteMessage: no message to delete".to_string())?;
                    let receipt_handle = msg
                        .receipt_handle()
                        .ok_or_else(|| "DeleteMessage: receipt_handle missing".to_string())?;
                    clients
                        .sqs()
                        .delete_message()
                        .queue_url(&queue_url)
                        .receipt_handle(receipt_handle)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-messages:ChangeMessageVisibility".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueMsgUrl")
                        .ok_or_else(|| "queueMsgUrl not set".to_string())?;
                    clients
                        .sqs()
                        .send_message()
                        .queue_url(&queue_url)
                        .message_body("visibility-test")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let first_recv = clients
                        .sqs()
                        .receive_message()
                        .queue_url(&queue_url)
                        .max_number_of_messages(1)
                        .wait_time_seconds(5)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let msg = first_recv
                        .messages()
                        .first()
                        .ok_or_else(|| "ChangeMessageVisibility: no message received on first poll".to_string())?;
                    let receipt_handle = msg
                        .receipt_handle()
                        .ok_or_else(|| "ChangeMessageVisibility: receipt_handle missing".to_string())?;
                    clients
                        .sqs()
                        .change_message_visibility()
                        .queue_url(&queue_url)
                        .receipt_handle(receipt_handle)
                        .visibility_timeout(0)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let second_recv = clients
                        .sqs()
                        .receive_message()
                        .queue_url(&queue_url)
                        .max_number_of_messages(1)
                        .wait_time_seconds(5)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (!second_recv.messages().is_empty())
                        .then_some(())
                        .ok_or_else(|| "ChangeMessageVisibility: message did not become re-visible after visibility_timeout=0".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-messages:DeleteMessageBatch".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueMsgUrl")
                        .ok_or_else(|| "queueMsgUrl not set".to_string())?;
                    clients
                        .sqs()
                        .send_message()
                        .queue_url(&queue_url)
                        .message_body("batch-del-1")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    clients
                        .sqs()
                        .send_message()
                        .queue_url(&queue_url)
                        .message_body("batch-del-2")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let recv = clients
                        .sqs()
                        .receive_message()
                        .queue_url(&queue_url)
                        .max_number_of_messages(10)
                        .wait_time_seconds(5)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let msgs = recv.messages();
                    if msgs.len() < 2 {
                        return Err(format!(
                            "DeleteMessageBatch: expected at least 2 messages, got {}",
                            msgs.len()
                        ));
                    }
                    let entries: Vec<DeleteMessageBatchRequestEntry> = msgs
                        .iter()
                        .enumerate()
                        .filter_map(|(i, msg)| {
                            msg.receipt_handle().map(|rh| {
                                DeleteMessageBatchRequestEntry::builder()
                                    .id(format!("{}", i))
                                    .receipt_handle(rh)
                                    .build()
                                    .expect("valid entry")
                            })
                        })
                        .collect();
                    clients
                        .sqs()
                        .delete_message_batch()
                        .queue_url(&queue_url)
                        .set_entries(Some(entries))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-messages:PurgeQueue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("queueMsgUrl")
                        .ok_or_else(|| "queueMsgUrl not set".to_string())?;
                    clients
                        .sqs()
                        .purge_queue()
                        .queue_url(&queue_url)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .sqs()
                        .get_queue_attributes()
                        .queue_url(&queue_url)
                        .attribute_names(QueueAttributeName::ApproximateNumberOfMessages)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let attrs = response
                        .attributes()
                        .ok_or_else(|| "PurgeQueue: attributes missing".to_string())?;
                    let count = attrs
                        .get(&QueueAttributeName::ApproximateNumberOfMessages)
                        .map(|s| s.as_str())
                        .unwrap_or_default();
                    (count == "0")
                        .then_some(())
                        .ok_or_else(|| format!("PurgeQueue: expected 0 messages, got {count}"))
                })
            }),
        );

        // ── sqs-dlq ─────────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "sqs-dlq:CreateDLQ".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = format!("{}-sqs-dlq-test", ctx.run_id.as_ref());
                    let response = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let url = response
                        .queue_url()
                        .ok_or_else(|| "CreateDLQ: queue_url missing".to_string())?;
                    if url.is_empty() || !url.contains(&queue_name) {
                        return Err(format!(
                            "CreateDLQ: queue_url {url} does not contain queue name {queue_name}"
                        ));
                    }
                    let list_resp = clients
                        .sqs()
                        .list_queues()
                        .queue_name_prefix(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = list_resp.queue_urls().iter().any(|u| u == url);
                    let _ = clients.sqs().delete_queue().queue_url(url).send().await;
                    found.then_some(()).ok_or_else(|| {
                        format!(
                            "CreateDLQ: queue {queue_name} not found in ListQueues (runId={})",
                            ctx.run_id
                        )
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-dlq:SetRedrivePolicy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let dlq_url = ctx
                        .get("queueDlqUrl")
                        .ok_or_else(|| "queueDlqUrl not set".to_string())?;
                    let src_url = ctx
                        .get("queueSrcUrl")
                        .ok_or_else(|| "queueSrcUrl not set".to_string())?;
                    let dlq_attrs = clients
                        .sqs()
                        .get_queue_attributes()
                        .queue_url(&dlq_url)
                        .attribute_names(QueueAttributeName::QueueArn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let dlq_arn = dlq_attrs
                        .attributes()
                        .ok_or_else(|| "SetRedrivePolicy: DLQ attributes missing".to_string())?
                        .get(&QueueAttributeName::QueueArn)
                        .cloned()
                        .ok_or_else(|| "SetRedrivePolicy: DLQ QueueArn missing".to_string())?;
                    let redrive_policy = format!(
                        r#"{{"deadLetterTargetArn":"{}","maxReceiveCount":3}}"#,
                        dlq_arn
                    );
                    clients
                        .sqs()
                        .set_queue_attributes()
                        .queue_url(&src_url)
                        .attributes(QueueAttributeName::RedrivePolicy, redrive_policy)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-dlq:GetRedrivePolicy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let src_url = ctx
                        .get("queueSrcUrl")
                        .ok_or_else(|| "queueSrcUrl not set".to_string())?;
                    let response = clients
                        .sqs()
                        .get_queue_attributes()
                        .queue_url(&src_url)
                        .attribute_names(QueueAttributeName::RedrivePolicy)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let attrs = response
                        .attributes()
                        .ok_or_else(|| "GetRedrivePolicy: attributes missing".to_string())?;
                    let policy = attrs
                        .get(&QueueAttributeName::RedrivePolicy)
                        .ok_or_else(|| "GetRedrivePolicy: RedrivePolicy attribute missing".to_string())?;
                    (policy.contains(r#""maxReceiveCount":3"#))
                        .then_some(())
                        .ok_or_else(|| format!("GetRedrivePolicy: expected maxReceiveCount=3 in policy, got {policy}"))
                })
            }),
        );

        // ── sqs-fifo ────────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "sqs-fifo:CreateFifoQueue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = format!("{}-sqs-fifo.fifo", ctx.run_id.as_ref());
                    let response = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&queue_name)
                        .attributes(QueueAttributeName::FifoQueue, "true")
                        .attributes(
                            QueueAttributeName::ContentBasedDeduplication,
                            "true",
                        )
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let url = response
                        .queue_url()
                        .ok_or_else(|| "CreateFifoQueue: queue_url missing".to_string())?;
                    if url.is_empty() || !url.contains(&queue_name) {
                        return Err(format!(
                            "CreateFifoQueue: queue_url {url} does not contain queue name {queue_name}"
                        ));
                    }
                    ctx.set("_fifoUrl", url.to_string());
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-fifo:SendFifoMessage".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("_fifoUrl")
                        .ok_or_else(|| "_fifoUrl not set".to_string())?;
                    let response = clients
                        .sqs()
                        .send_message()
                        .queue_url(&queue_url)
                        .message_body("fifo-msg")
                        .message_group_id("grp1")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let message_id = response
                        .message_id()
                        .ok_or_else(|| "SendFifoMessage: message_id missing".to_string())?;
                    (!message_id.is_empty())
                        .then_some(())
                        .ok_or_else(|| "SendFifoMessage: message_id is empty".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sqs-fifo:ReceiveFifoMessage".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_url = ctx
                        .get("_fifoUrl")
                        .ok_or_else(|| "_fifoUrl not set".to_string())?;
                    let response = clients
                        .sqs()
                        .receive_message()
                        .queue_url(&queue_url)
                        .max_number_of_messages(1)
                        .wait_time_seconds(5)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let msg = response
                        .messages()
                        .first()
                        .ok_or_else(|| "ReceiveFifoMessage: no message received".to_string())?;
                    let body = msg.body().unwrap_or_default();
                    (body == "fifo-msg").then_some(()).ok_or_else(|| {
                        format!("ReceiveFifoMessage: expected body 'fifo-msg', got '{body}'")
                    })
                })
            }),
        );

        impls
    }

    fn setups(&self) -> HashMap<String, TestFn> {
        let mut setups: HashMap<String, TestFn> = HashMap::new();

        // ── sqs-queues setup ────────────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "sqs-queues".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = format!("{}-sqs-q", ctx.run_id.as_ref());
                    let response = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let url = response
                        .queue_url()
                        .ok_or_else(|| "setup(sqs-queues): queue_url missing".to_string())?;
                    ctx.set("queueName", queue_name);
                    ctx.set("queueUrl", url.to_string());
                    Ok(())
                })
            }),
        );

        // ── sqs-messages setup ──────────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "sqs-messages".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let queue_name = format!("{}-sqs-msg", ctx.run_id.as_ref());
                    let response = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let url = response
                        .queue_url()
                        .ok_or_else(|| "setup(sqs-messages): queue_url missing".to_string())?;
                    ctx.set("queueMsgName", queue_name);
                    ctx.set("queueMsgUrl", url.to_string());
                    Ok(())
                })
            }),
        );

        // ── sqs-dlq setup ───────────────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "sqs-dlq".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let src_name = format!("{}-sqs-src", ctx.run_id.as_ref());
                    let dlq_name = format!("{}-sqs-dlq", ctx.run_id.as_ref());
                    let src_resp = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&src_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let src_url = src_resp
                        .queue_url()
                        .ok_or_else(|| "setup(sqs-dlq): src queue_url missing".to_string())?;
                    let dlq_resp = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&dlq_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let dlq_url = dlq_resp
                        .queue_url()
                        .ok_or_else(|| "setup(sqs-dlq): dlq queue_url missing".to_string())?;
                    ctx.set("queueSrcName", src_name);
                    ctx.set("queueSrcUrl", src_url.to_string());
                    ctx.set("queueDlqName", dlq_name);
                    ctx.set("queueDlqUrl", dlq_url.to_string());
                    Ok(())
                })
            }),
        );

        setups
    }

    fn teardowns(&self) -> HashMap<String, TestFn> {
        let mut teardowns: HashMap<String, TestFn> = HashMap::new();

        // ── sqs-queues teardown ─────────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "sqs-queues".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(queue_name) = ctx.get("queueName") {
                        if let Ok(resp) = clients
                            .sqs()
                            .get_queue_url()
                            .queue_name(&queue_name)
                            .send()
                            .await
                        {
                            let _ = clients
                                .sqs()
                                .delete_queue()
                                .queue_url(resp.queue_url().unwrap_or_default())
                                .send()
                                .await;
                        }
                    }
                    Ok(())
                })
            }),
        );

        // ── sqs-messages teardown ───────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "sqs-messages".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(queue_name) = ctx.get("queueMsgName") {
                        if let Ok(resp) = clients
                            .sqs()
                            .get_queue_url()
                            .queue_name(&queue_name)
                            .send()
                            .await
                        {
                            let _ = clients
                                .sqs()
                                .delete_queue()
                                .queue_url(resp.queue_url().unwrap_or_default())
                                .send()
                                .await;
                        }
                    }
                    Ok(())
                })
            }),
        );

        // ── sqs-dlq teardown ────────────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "sqs-dlq".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(src_name) = ctx.get("queueSrcName") {
                        if let Ok(resp) = clients
                            .sqs()
                            .get_queue_url()
                            .queue_name(&src_name)
                            .send()
                            .await
                        {
                            let _ = clients
                                .sqs()
                                .delete_queue()
                                .queue_url(resp.queue_url().unwrap_or_default())
                                .send()
                                .await;
                        }
                    }
                    if let Some(dlq_name) = ctx.get("queueDlqName") {
                        if let Ok(resp) = clients
                            .sqs()
                            .get_queue_url()
                            .queue_name(&dlq_name)
                            .send()
                            .await
                        {
                            let _ = clients
                                .sqs()
                                .delete_queue()
                                .queue_url(resp.queue_url().unwrap_or_default())
                                .send()
                                .await;
                        }
                    }
                    Ok(())
                })
            }),
        );

        // ── sqs-fifo teardown ───────────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "sqs-fifo".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(url) = ctx.get("_fifoUrl") {
                        let _ = clients.sqs().delete_queue().queue_url(&url).send().await;
                    }
                    Ok(())
                })
            }),
        );

        teardowns
    }
}
