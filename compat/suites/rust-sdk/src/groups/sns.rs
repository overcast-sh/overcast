use std::collections::HashMap;
use std::sync::Arc;

use aws_sdk_sns::types::{MessageAttributeValue, PublishBatchRequestEntry};
use aws_sdk_sqs::types::QueueAttributeName;

use crate::clients::AwsClients;
use crate::groups::ServiceGroup;
use crate::harness::{TestContext, TestFn};

pub struct SnsGroup {
    clients: Arc<AwsClients>,
}

impl SnsGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for SnsGroup {
    fn name(&self) -> &'static str {
        "sns"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        // ── sns-topics ──────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "sns-topics:CreateTopic".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = format!("{}-sns-topics", ctx.run_id.as_ref());
                    let response = clients
                        .sns()
                        .create_topic()
                        .name(&topic_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let topic_arn = response
                        .topic_arn()
                        .ok_or_else(|| "CreateTopic: topic_arn missing".to_string())?;
                    if topic_arn.is_empty() || !topic_arn.contains(&topic_name) {
                        return Err(format!(
                            "CreateTopic: topic_arn {topic_arn} does not contain topic name {topic_name}"
                        ));
                    }
                    ctx.set("topicName", topic_name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-topics:ListTopics".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("topicName")
                        .ok_or_else(|| "topicName not set".to_string())?;
                    let response = clients
                        .sns()
                        .list_topics()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .topics()
                        .iter()
                        .any(|t| t.topic_arn().unwrap_or_default().contains(&topic_name));
                    found.then_some(()).ok_or_else(|| {
                        format!(
                            "ListTopics: topic {topic_name} not found (runId={})",
                            ctx.run_id
                        )
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-topics:GetTopicAttributes".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("topicName")
                        .ok_or_else(|| "topicName not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    let response = clients
                        .sns()
                        .get_topic_attributes()
                        .topic_arn(&topic_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let attrs = response
                        .attributes()
                        .ok_or_else(|| "GetTopicAttributes: attributes missing".to_string())?;
                    let arn_attr = attrs.get("TopicArn").map(|s| s.as_str()).unwrap_or_default();
                    (!arn_attr.is_empty())
                        .then_some(())
                        .ok_or_else(|| "GetTopicAttributes: TopicArn attribute missing or empty".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-topics:SetTopicAttributes".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("topicName")
                        .ok_or_else(|| "topicName not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    clients
                        .sns()
                        .set_topic_attributes()
                        .topic_arn(&topic_arn)
                        .attribute_name("DisplayName")
                        .attribute_value("compat-test")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .sns()
                        .get_topic_attributes()
                        .topic_arn(&topic_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let attrs = response
                        .attributes()
                        .ok_or_else(|| "SetTopicAttributes: attributes missing".to_string())?;
                    let display = attrs
                        .get("DisplayName")
                        .map(|s| s.as_str())
                        .unwrap_or_default();
                    (display == "compat-test").then_some(()).ok_or_else(|| {
                        format!("SetTopicAttributes: expected DisplayName=compat-test, got {display}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-topics:DeleteTopic".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("topicName")
                        .ok_or_else(|| "topicName not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    clients
                        .sns()
                        .delete_topic()
                        .topic_arn(&topic_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .sns()
                        .list_topics()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .topics()
                        .iter()
                        .any(|t| t.topic_arn().unwrap_or_default().contains(&topic_name));
                    (!found).then_some(()).ok_or_else(|| {
                        format!("DeleteTopic: topic {topic_name} still present after deletion")
                    })
                })
            }),
        );

        // ── sns-publish ─────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "sns-publish:Publish".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("pubTopicName")
                        .ok_or_else(|| "pubTopicName not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    let response = clients
                        .sns()
                        .publish()
                        .topic_arn(&topic_arn)
                        .message("hello-sns")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let message_id = response
                        .message_id()
                        .ok_or_else(|| "Publish: message_id missing".to_string())?;
                    (!message_id.is_empty())
                        .then_some(())
                        .ok_or_else(|| "Publish: message_id is empty".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-publish:PublishWithAttributes".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("pubTopicName")
                        .ok_or_else(|| "pubTopicName not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    let mut message_attributes = HashMap::new();
                    message_attributes.insert(
                        "color".to_string(),
                        MessageAttributeValue::builder()
                            .data_type("String")
                            .string_value("red")
                            .build()
                            .map_err(crate::harness::sdk_error_message)?,
                    );
                    message_attributes.insert(
                        "count".to_string(),
                        MessageAttributeValue::builder()
                            .data_type("Number")
                            .string_value("5")
                            .build()
                            .map_err(crate::harness::sdk_error_message)?,
                    );
                    let response = clients
                        .sns()
                        .publish()
                        .topic_arn(&topic_arn)
                        .message("hello-with-attrs")
                        .subject("Test Subject")
                        .set_message_attributes(Some(message_attributes))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let message_id = response
                        .message_id()
                        .ok_or_else(|| "PublishWithAttributes: message_id missing".to_string())?;
                    (!message_id.is_empty())
                        .then_some(())
                        .ok_or_else(|| "PublishWithAttributes: message_id is empty".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-publish:PublishBatch".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("pubTopicName")
                        .ok_or_else(|| "pubTopicName not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    let entry1 = PublishBatchRequestEntry::builder()
                        .id("1")
                        .message("batch-msg-1")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let entry2 = PublishBatchRequestEntry::builder()
                        .id("2")
                        .message("batch-msg-2")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let response = clients
                        .sns()
                        .publish_batch()
                        .topic_arn(&topic_arn)
                        .publish_batch_request_entries(entry1)
                        .publish_batch_request_entries(entry2)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let count = response.successful().len();
                    (count >= 2).then_some(()).ok_or_else(|| {
                        format!("PublishBatch: expected >= 2 successful, got {count}")
                    })
                })
            }),
        );

        // ── sns-subscriptions ───────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "sns-subscriptions:SubscribeSQS".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("subTopicName")
                        .ok_or_else(|| "subTopicName not set".to_string())?;
                    let queue_url = ctx
                        .get("subQueueUrl")
                        .ok_or_else(|| "subQueueUrl not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    let q_attrs = clients
                        .sqs()
                        .get_queue_attributes()
                        .queue_url(&queue_url)
                        .attribute_names(QueueAttributeName::QueueArn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let queue_arn = q_attrs
                        .attributes()
                        .ok_or_else(|| "SubscribeSQS: queue attributes missing".to_string())?
                        .get(&QueueAttributeName::QueueArn)
                        .cloned()
                        .ok_or_else(|| "SubscribeSQS: QueueArn attribute missing".to_string())?;
                    let response = clients
                        .sns()
                        .subscribe()
                        .topic_arn(&topic_arn)
                        .protocol("sqs")
                        .endpoint(&queue_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let sub_arn = response
                        .subscription_arn()
                        .ok_or_else(|| "SubscribeSQS: subscription_arn missing".to_string())?;
                    if sub_arn.is_empty() {
                        return Err("SubscribeSQS: subscription_arn is empty".to_string());
                    }
                    ctx.set("_subArn", sub_arn.to_string());
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-subscriptions:ListSubscriptionsByTopic".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("subTopicName")
                        .ok_or_else(|| "subTopicName not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    let response = clients
                        .sns()
                        .list_subscriptions_by_topic()
                        .topic_arn(&topic_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let count = response.subscriptions().len();
                    (count >= 1).then_some(()).ok_or_else(|| {
                        format!("ListSubscriptionsByTopic: expected >= 1 subscription, got {count}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-subscriptions:GetSubscriptionAttributes".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let sub_arn = ctx
                        .get("_subArn")
                        .ok_or_else(|| "_subArn not set".to_string())?;
                    let response = clients
                        .sns()
                        .get_subscription_attributes()
                        .subscription_arn(&sub_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let attrs = response
                        .attributes()
                        .ok_or_else(|| "GetSubscriptionAttributes: attributes missing".to_string())?;
                    let protocol = attrs
                        .get("Protocol")
                        .map(|s| s.as_str())
                        .unwrap_or_default();
                    (protocol == "sqs").then_some(()).ok_or_else(|| {
                        format!("GetSubscriptionAttributes: expected Protocol=sqs, got {protocol}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-subscriptions:PublishDeliveredToSQS".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("subTopicName")
                        .ok_or_else(|| "subTopicName not set".to_string())?;
                    let queue_url = ctx
                        .get("subQueueUrl")
                        .ok_or_else(|| "subQueueUrl not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    clients
                        .sns()
                        .publish()
                        .topic_arn(&topic_arn)
                        .message("delivery-test")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    for _ in 0..10 {
                        let recv = clients
                            .sqs()
                            .receive_message()
                            .queue_url(&queue_url)
                            .max_number_of_messages(10)
                            .wait_time_seconds(1)
                            .send()
                            .await
                            .map_err(crate::harness::sdk_error)?;
                        if !recv.messages().is_empty() {
                            return Ok(());
                        }
                    }
                    Err("PublishDeliveredToSQS: no message received after 10 polls".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-subscriptions:SetSubscriptionAttributes".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let sub_arn = ctx
                        .get("_subArn")
                        .ok_or_else(|| "_subArn not set".to_string())?;
                    clients
                        .sns()
                        .set_subscription_attributes()
                        .subscription_arn(&sub_arn)
                        .attribute_name("RawMessageDelivery")
                        .attribute_value("true")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .sns()
                        .get_subscription_attributes()
                        .subscription_arn(&sub_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let attrs = response.attributes().ok_or_else(|| {
                        "SetSubscriptionAttributes: attributes missing".to_string()
                    })?;
                    let raw = attrs
                        .get("RawMessageDelivery")
                        .map(|s| s.as_str())
                        .unwrap_or_default();
                    (raw == "true").then_some(()).ok_or_else(|| {
                        format!("SetSubscriptionAttributes: expected RawMessageDelivery=true, got {raw}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "sns-subscriptions:Unsubscribe".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx
                        .get("subTopicName")
                        .ok_or_else(|| "subTopicName not set".to_string())?;
                    let sub_arn = ctx
                        .get("_subArn")
                        .ok_or_else(|| "_subArn not set".to_string())?;
                    let topic_arn = find_topic_arn(&clients, &topic_name).await?;
                    clients
                        .sns()
                        .unsubscribe()
                        .subscription_arn(&sub_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .sns()
                        .list_subscriptions_by_topic()
                        .topic_arn(&topic_arn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .subscriptions()
                        .iter()
                        .any(|s| s.subscription_arn().unwrap_or_default() == sub_arn);
                    (!found).then_some(()).ok_or_else(|| {
                        "Unsubscribe: subscription still present after unsubscribe".to_string()
                    })
                })
            }),
        );

        impls
    }

    fn setups(&self) -> HashMap<String, TestFn> {
        let mut setups: HashMap<String, TestFn> = HashMap::new();

        // ── sns-publish setup ────────────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "sns-publish".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = format!("{}-sns-pub", ctx.run_id.as_ref());
                    let response = clients
                        .sns()
                        .create_topic()
                        .name(&topic_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let topic_arn = response
                        .topic_arn()
                        .ok_or_else(|| "setup(sns-publish): topic_arn missing".to_string())?;
                    if topic_arn.is_empty() {
                        return Err("setup(sns-publish): topic_arn is empty".to_string());
                    }
                    ctx.set("pubTopicName", topic_name);
                    Ok(())
                })
            }),
        );

        // ── sns-subscriptions setup ─────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "sns-subscriptions".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = format!("{}-sns-sub", ctx.run_id.as_ref());
                    let queue_name = format!("{}-sns-sub-q", ctx.run_id.as_ref());
                    let topic_resp = clients
                        .sns()
                        .create_topic()
                        .name(&topic_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let topic_arn = topic_resp
                        .topic_arn()
                        .ok_or_else(|| "setup(sns-subscriptions): topic_arn missing".to_string())?;
                    if topic_arn.is_empty() {
                        return Err("setup(sns-subscriptions): topic_arn is empty".to_string());
                    }
                    let queue_resp = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let queue_url = queue_resp
                        .queue_url()
                        .ok_or_else(|| "setup(sns-subscriptions): queue_url missing".to_string())?;
                    if queue_url.is_empty() {
                        return Err("setup(sns-subscriptions): queue_url is empty".to_string());
                    }
                    ctx.set("subTopicName", topic_name);
                    ctx.set("subQueueName", queue_name);
                    ctx.set("subQueueUrl", queue_url.to_string());
                    Ok(())
                })
            }),
        );

        setups
    }

    fn teardowns(&self) -> HashMap<String, TestFn> {
        let mut teardowns: HashMap<String, TestFn> = HashMap::new();

        // ── sns-topics teardown ─────────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "sns-topics".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx.get("topicName").unwrap_or_default();
                    if !topic_name.is_empty() {
                        if let Ok(topic_arn) = find_topic_arn(&clients, &topic_name).await {
                            let _ = clients
                                .sns()
                                .delete_topic()
                                .topic_arn(&topic_arn)
                                .send()
                                .await;
                        }
                    }
                    Ok(())
                })
            }),
        );

        // ── sns-publish teardown ────────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "sns-publish".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx.get("pubTopicName").unwrap_or_default();
                    if !topic_name.is_empty() {
                        if let Ok(topic_arn) = find_topic_arn(&clients, &topic_name).await {
                            let _ = clients
                                .sns()
                                .delete_topic()
                                .topic_arn(&topic_arn)
                                .send()
                                .await;
                        }
                    }
                    Ok(())
                })
            }),
        );

        // ── sns-subscriptions teardown ──────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "sns-subscriptions".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let topic_name = ctx.get("subTopicName").unwrap_or_default();
                    if !topic_name.is_empty() {
                        if let Ok(topic_arn) = find_topic_arn(&clients, &topic_name).await {
                            let _ = clients
                                .sns()
                                .delete_topic()
                                .topic_arn(&topic_arn)
                                .send()
                                .await;
                        }
                    }
                    let queue_url = ctx.get("subQueueUrl").unwrap_or_default();
                    if !queue_url.is_empty() {
                        let _ = clients.sqs().purge_queue().queue_url(&queue_url).send().await;
                        let _ = clients
                            .sqs()
                            .delete_queue()
                            .queue_url(&queue_url)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        teardowns
    }
}

async fn find_topic_arn(clients: &AwsClients, topic_name: &str) -> Result<String, String> {
    let response = clients
        .sns()
        .list_topics()
        .send()
        .await
        .map_err(crate::harness::sdk_error)?;
    for topic in response.topics() {
        let arn = topic.topic_arn().unwrap_or_default();
        if arn.contains(topic_name) {
            return Ok(arn.to_string());
        }
    }
    Err(format!(
        "find_topic_arn: topic {topic_name} not found in ListTopics"
    ))
}
