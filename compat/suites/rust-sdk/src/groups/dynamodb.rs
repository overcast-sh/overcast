use std::collections::HashMap;
use std::sync::Arc;

use aws_sdk_dynamodb::types::{
    AttributeDefinition, AttributeValue, BillingMode, CreateGlobalSecondaryIndexAction,
    GlobalSecondaryIndexUpdate, KeySchemaElement, KeyType, KeysAndAttributes, Projection,
    ProjectionType, PutRequest, ScalarAttributeType, TimeToLiveSpecification, TransactGetItem,
    TransactWriteItem, WriteRequest,
};

use crate::clients::AwsClients;
use crate::groups::ServiceGroup;
use crate::harness::{TestContext, TestFn};

pub struct DynamoDbGroup {
    clients: Arc<AwsClients>,
}

impl DynamoDbGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for DynamoDbGroup {
    fn name(&self) -> &'static str {
        "dynamodb"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        // ── dynamodb-tables ────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-tables:CreateTable".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = format!("{}-dyntbls", ctx.run_id.as_ref());
                    let response = clients
                        .dynamodb()
                        .create_table()
                        .table_name(&table)
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("pk")
                                .key_type(KeyType::Hash)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("sk")
                                .key_type(KeyType::Range)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("pk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("sk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .billing_mode(BillingMode::PayPerRequest)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let desc = response
                        .table_description()
                        .ok_or_else(|| "CreateTable: table_description missing".to_string())?;
                    if desc.table_arn().unwrap_or_default().is_empty() {
                        return Err("CreateTable: table ARN is empty".to_string());
                    }
                    ctx.set("dynamoTable", table);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-tables:DescribeTable".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let response = clients
                        .dynamodb()
                        .describe_table()
                        .table_name(&table)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let desc = response
                        .table()
                        .ok_or_else(|| "DescribeTable: table missing".to_string())?;
                    if desc.table_status().map(|s| s.as_str()).unwrap_or_default() != "ACTIVE" {
                        return Err(format!(
                            "DescribeTable: expected ACTIVE, got {}",
                            desc.table_status().map(|s| s.as_str()).unwrap_or_default()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-tables:ListTables".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let response = clients
                        .dynamodb()
                        .list_tables()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response.table_names().iter().any(|t| t.as_str() == table);
                    if !found {
                        return Err(format!("ListTables: table {} not found", table));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-tables:UpdateTable".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let gsi_action = CreateGlobalSecondaryIndexAction::builder()
                        .index_name("gsi-by-gpk")
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("gpk")
                                .key_type(KeyType::Hash)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .projection(
                            Projection::builder()
                                .projection_type(ProjectionType::All)
                                .build(),
                        )
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    clients
                        .dynamodb()
                        .update_table()
                        .table_name(&table)
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("gpk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .global_secondary_index_updates(
                            GlobalSecondaryIndexUpdate::builder()
                                .create(gsi_action)
                                .build(),
                        )
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-tables:DeleteTable".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    clients
                        .dynamodb()
                        .delete_table()
                        .table_name(&table)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .dynamodb()
                        .list_tables()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response.table_names().iter().any(|t| t.as_str() == table);
                    if found {
                        return Err(format!(
                            "DeleteTable: table {} still present after deletion",
                            table
                        ));
                    }
                    Ok(())
                })
            }),
        );

        // ── dynamodb-items ─────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-items:PutItem".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut item = HashMap::new();
                    item.insert("pk".to_string(), AttributeValue::S("user#1".to_string()));
                    item.insert("sk".to_string(), AttributeValue::S("profile".to_string()));
                    item.insert("name".to_string(), AttributeValue::S("Alice".to_string()));
                    item.insert("age".to_string(), AttributeValue::N("30".to_string()));
                    clients
                        .dynamodb()
                        .put_item()
                        .table_name(&table)
                        .set_item(Some(item))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let mut key = HashMap::new();
                    key.insert("pk".to_string(), AttributeValue::S("user#1".to_string()));
                    key.insert("sk".to_string(), AttributeValue::S("profile".to_string()));
                    let response = clients
                        .dynamodb()
                        .get_item()
                        .table_name(&table)
                        .set_key(Some(key))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let item = response
                        .item()
                        .ok_or_else(|| "PutItem: item not found after put".to_string())?;
                    let name = item
                        .get("name")
                        .and_then(|v| v.as_s().ok())
                        .map(|s| s.as_str())
                        .unwrap_or("");
                    if name != "Alice" {
                        return Err(format!("PutItem: expected name=Alice, got {}", name));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-items:GetItem".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut key = HashMap::new();
                    key.insert("pk".to_string(), AttributeValue::S("user#1".to_string()));
                    key.insert("sk".to_string(), AttributeValue::S("profile".to_string()));
                    let response = clients
                        .dynamodb()
                        .get_item()
                        .table_name(&table)
                        .set_key(Some(key))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let item = response
                        .item()
                        .ok_or_else(|| "GetItem: item not found".to_string())?;
                    let name = item
                        .get("name")
                        .and_then(|v| v.as_s().ok())
                        .map(|s| s.as_str())
                        .unwrap_or("");
                    if name != "Alice" {
                        return Err(format!("GetItem: expected name=Alice, got {}", name));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-items:UpdateItem".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut key = HashMap::new();
                    key.insert("pk".to_string(), AttributeValue::S("user#1".to_string()));
                    key.insert("sk".to_string(), AttributeValue::S("profile".to_string()));
                    let mut expr_vals = HashMap::new();
                    expr_vals.insert(":newAge".to_string(), AttributeValue::N("31".to_string()));
                    clients
                        .dynamodb()
                        .update_item()
                        .table_name(&table)
                        .set_key(Some(key.clone()))
                        .update_expression("SET age = :newAge")
                        .set_expression_attribute_values(Some(expr_vals))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .dynamodb()
                        .get_item()
                        .table_name(&table)
                        .set_key(Some(key))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let item = response
                        .item()
                        .ok_or_else(|| "UpdateItem: item not found".to_string())?;
                    let age = item
                        .get("age")
                        .and_then(|v| v.as_n().ok())
                        .map(|s| s.as_str())
                        .unwrap_or("");
                    if age != "31" {
                        return Err(format!("UpdateItem: expected age=31, got {}", age));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-items:PutItemConditionFail".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut item = HashMap::new();
                    item.insert("pk".to_string(), AttributeValue::S("user#1".to_string()));
                    item.insert("sk".to_string(), AttributeValue::S("profile".to_string()));
                    item.insert("name".to_string(), AttributeValue::S("Bob".to_string()));
                    let result = clients
                        .dynamodb()
                        .put_item()
                        .table_name(&table)
                        .set_item(Some(item))
                        .condition_expression("attribute_not_exists(pk)")
                        .send()
                        .await;
                    match result {
                        Err(err) => {
                            let rendered = crate::harness::sdk_error_message(err);
                            let msg = rendered.to_ascii_lowercase();
                            if msg.contains("conditionalcheckfailed")
                                || msg.contains("condition")
                            {
                                Ok(())
                            } else {
                                Err(format!(
                                    "PutItemConditionFail: unexpected error: {rendered}"
                                ))
                            }
                        }
                        Ok(_) => Err(
                            "PutItemConditionFail: expected ConditionalCheckFailedException but got success"
                                .to_string(),
                        ),
                    }
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-items:DeleteItem".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut key = HashMap::new();
                    key.insert("pk".to_string(), AttributeValue::S("user#1".to_string()));
                    key.insert("sk".to_string(), AttributeValue::S("profile".to_string()));
                    clients
                        .dynamodb()
                        .delete_item()
                        .table_name(&table)
                        .set_key(Some(key.clone()))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .dynamodb()
                        .get_item()
                        .table_name(&table)
                        .set_key(Some(key))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.item().is_some() {
                        return Err("DeleteItem: item still present after deletion".to_string());
                    }
                    Ok(())
                })
            }),
        );

        // ── dynamodb-query ─────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-query:Query".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut expr_vals = HashMap::new();
                    expr_vals.insert(":pk".to_string(), AttributeValue::S("user#1".to_string()));
                    let response = clients
                        .dynamodb()
                        .query()
                        .table_name(&table)
                        .key_condition_expression("pk = :pk")
                        .set_expression_attribute_values(Some(expr_vals))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.count() < 3 {
                        return Err(format!(
                            "Query: expected >=3 items, got {}",
                            response.count()
                        ));
                    }
                    if response.items().len() < 3 {
                        return Err(format!(
                            "Query: expected >=3 items in response, got {}",
                            response.items().len()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-query:QueryWithFilterExpression".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut expr_vals = HashMap::new();
                    expr_vals.insert(":pk".to_string(), AttributeValue::S("user#1".to_string()));
                    expr_vals.insert(":minScore".to_string(), AttributeValue::N("50".to_string()));
                    let response = clients
                        .dynamodb()
                        .query()
                        .table_name(&table)
                        .key_condition_expression("pk = :pk")
                        .filter_expression("score > :minScore")
                        .set_expression_attribute_values(Some(expr_vals))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.count() < 1 {
                        return Err(format!(
                            "QueryWithFilterExpression: expected >=1 item, got {}",
                            response.count()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-query:QueryWithLimit".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut expr_vals = HashMap::new();
                    expr_vals.insert(":pk".to_string(), AttributeValue::S("user#1".to_string()));
                    let response = clients
                        .dynamodb()
                        .query()
                        .table_name(&table)
                        .key_condition_expression("pk = :pk")
                        .set_expression_attribute_values(Some(expr_vals))
                        .limit(2)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.items().len() > 2 {
                        return Err(format!(
                            "QueryWithLimit: expected <=2 items, got {}",
                            response.items().len()
                        ));
                    }
                    if response.last_evaluated_key().is_none() {
                        return Err(
                            "QueryWithLimit: expected LastEvaluatedKey to be present".to_string()
                        );
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-query:QueryPagination".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut expr_vals = HashMap::new();
                    expr_vals.insert(":pk".to_string(), AttributeValue::S("user#1".to_string()));
                    let page1 = clients
                        .dynamodb()
                        .query()
                        .table_name(&table)
                        .key_condition_expression("pk = :pk")
                        .set_expression_attribute_values(Some(expr_vals.clone()))
                        .limit(2)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if page1.items().len() > 2 {
                        return Err(format!(
                            "QueryPagination page1: expected <=2 items, got {}",
                            page1.items().len()
                        ));
                    }
                    let lek = page1.last_evaluated_key().ok_or_else(|| {
                        "QueryPagination: expected LastEvaluatedKey on page 1".to_string()
                    })?;
                    let page2 = clients
                        .dynamodb()
                        .query()
                        .table_name(&table)
                        .key_condition_expression("pk = :pk")
                        .set_expression_attribute_values(Some(expr_vals))
                        .set_exclusive_start_key(Some(lek.clone()))
                        .limit(2)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let total = page1.items().len() + page2.items().len();
                    if total < 2 {
                        return Err(format!(
                            "QueryPagination: expected >=2 items across pages, got {}",
                            total
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-query:Scan".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let response = clients
                        .dynamodb()
                        .scan()
                        .table_name(&table)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.count() < 1 {
                        return Err(format!(
                            "Scan: expected >=1 items, got {}",
                            response.count()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-query:ScanWithFilter".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut expr_vals = HashMap::new();
                    expr_vals.insert(":pk".to_string(), AttributeValue::S("user#1".to_string()));
                    let response = clients
                        .dynamodb()
                        .scan()
                        .table_name(&table)
                        .filter_expression("pk = :pk")
                        .set_expression_attribute_values(Some(expr_vals))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    for item in response.items() {
                        let item_pk = item
                            .get("pk")
                            .and_then(|v| v.as_s().ok())
                            .map(|s| s.as_str())
                            .unwrap_or("");
                        if item_pk != "user#1" {
                            return Err(format!(
                                "ScanWithFilter: item with pk={} violates filter",
                                item_pk
                            ));
                        }
                    }
                    Ok(())
                })
            }),
        );

        // ── dynamodb-batch ─────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-batch:BatchWriteItem".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut writes = Vec::new();
                    for i in 1..=3 {
                        let mut item = HashMap::new();
                        item.insert("pk".to_string(), AttributeValue::S(format!("batch{}", i)));
                        item.insert("sk".to_string(), AttributeValue::S("detail".to_string()));
                        item.insert("value".to_string(), AttributeValue::N(i.to_string()));
                        let put = PutRequest::builder()
                            .set_item(Some(item))
                            .build()
                            .map_err(crate::harness::sdk_error_message)?;
                        writes.push(WriteRequest::builder().put_request(put).build());
                    }
                    let mut request_items: HashMap<String, Vec<WriteRequest>> = HashMap::new();
                    request_items.insert(table.clone(), writes);
                    let response = clients
                        .dynamodb()
                        .batch_write_item()
                        .set_request_items(Some(request_items))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if !response
                        .unprocessed_items()
                        .cloned()
                        .unwrap_or_default()
                        .is_empty()
                    {
                        return Err("BatchWriteItem: UnprocessedItems is not empty".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-batch:BatchGetItem".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut keys = Vec::new();
                    for i in 1..=2 {
                        let mut key = HashMap::new();
                        key.insert("pk".to_string(), AttributeValue::S(format!("batch{}", i)));
                        key.insert("sk".to_string(), AttributeValue::S("detail".to_string()));
                        keys.push(key);
                    }
                    let ka = KeysAndAttributes::builder()
                        .set_keys(Some(keys))
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let mut request_items = HashMap::new();
                    request_items.insert(table.clone(), ka);
                    let response = clients
                        .dynamodb()
                        .batch_get_item()
                        .set_request_items(Some(request_items))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let responses = response
                        .responses()
                        .ok_or_else(|| "BatchGetItem: responses missing".to_string())?;
                    let items = responses.get(&table).map(|v| v.len()).unwrap_or(0);
                    if items < 2 {
                        return Err(format!("BatchGetItem: expected >=2 items, got {}", items));
                    }
                    Ok(())
                })
            }),
        );

        // ── dynamodb-txn ───────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-txn:TransactWriteItems".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut transact_items = Vec::new();
                    for (pk_val, data_val) in [("txn1", "hello"), ("txn2", "world")] {
                        let mut item = HashMap::new();
                        item.insert("pk".to_string(), AttributeValue::S(pk_val.to_string()));
                        item.insert("sk".to_string(), AttributeValue::S("meta".to_string()));
                        item.insert("data".to_string(), AttributeValue::S(data_val.to_string()));
                        let put = aws_sdk_dynamodb::types::Put::builder()
                            .table_name(&table)
                            .set_item(Some(item))
                            .build()
                            .map_err(crate::harness::sdk_error_message)?;
                        transact_items.push(TransactWriteItem::builder().put(put).build());
                    }
                    clients
                        .dynamodb()
                        .transact_write_items()
                        .set_transact_items(Some(transact_items))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let mut key = HashMap::new();
                    key.insert("pk".to_string(), AttributeValue::S("txn1".to_string()));
                    key.insert("sk".to_string(), AttributeValue::S("meta".to_string()));
                    let response = clients
                        .dynamodb()
                        .get_item()
                        .table_name(&table)
                        .set_key(Some(key))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let item = response
                        .item()
                        .ok_or_else(|| "TransactWriteItems: txn1 item not found".to_string())?;
                    let data = item
                        .get("data")
                        .and_then(|v| v.as_s().ok())
                        .map(|s| s.as_str())
                        .unwrap_or("");
                    if data != "hello" {
                        return Err(format!(
                            "TransactWriteItems: expected data=hello, got {}",
                            data
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-txn:TransactGetItems".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut transact_items = Vec::new();
                    for pk_val in ["txn1", "txn2"] {
                        let mut key = HashMap::new();
                        key.insert("pk".to_string(), AttributeValue::S(pk_val.to_string()));
                        key.insert("sk".to_string(), AttributeValue::S("meta".to_string()));
                        let get = aws_sdk_dynamodb::types::Get::builder()
                            .table_name(&table)
                            .set_key(Some(key))
                            .build()
                            .map_err(crate::harness::sdk_error_message)?;
                        transact_items.push(TransactGetItem::builder().get(get).build());
                    }
                    let response = clients
                        .dynamodb()
                        .transact_get_items()
                        .set_transact_items(Some(transact_items))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let responses = response.responses();
                    if responses.len() < 2 {
                        return Err(format!(
                            "TransactGetItems: expected >=2 responses, got {}",
                            responses.len()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-txn:TransactWriteConditionFail".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let mut item = HashMap::new();
                    item.insert("pk".to_string(), AttributeValue::S("txn1".to_string()));
                    item.insert("sk".to_string(), AttributeValue::S("meta".to_string()));
                    item.insert(
                        "data".to_string(),
                        AttributeValue::S("should-fail".to_string()),
                    );
                    let put = aws_sdk_dynamodb::types::Put::builder()
                        .table_name(&table)
                        .set_item(Some(item))
                        .condition_expression("attribute_not_exists(pk)")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let transact_items = vec![TransactWriteItem::builder().put(put).build()];
                    let result = clients
                        .dynamodb()
                        .transact_write_items()
                        .set_transact_items(Some(transact_items))
                        .send()
                        .await;
                    match result {
                        Err(err) => {
                            let rendered = crate::harness::sdk_error_message(err);
                            let msg = rendered.to_ascii_lowercase();
                            if msg.contains("transactioncanceled")
                                || msg.contains("condition")
                            {
                                Ok(())
                            } else {
                                Err(format!(
                                    "TransactWriteConditionFail: unexpected error: {rendered}"
                                ))
                            }
                        }
                        Ok(_) => Err("TransactWriteConditionFail: expected TransactionCanceledException but got success".to_string()),
                    }
                })
            }),
        );

        // ── dynamodb-ttl ───────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-ttl:UpdateTimeToLive".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let spec = TimeToLiveSpecification::builder()
                        .enabled(true)
                        .attribute_name("expires_at")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let response = clients
                        .dynamodb()
                        .update_time_to_live()
                        .table_name(&table)
                        .time_to_live_specification(spec)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let ttl_spec = response
                        .time_to_live_specification()
                        .ok_or_else(|| "UpdateTimeToLive: specification missing".to_string())?;
                    if !ttl_spec.enabled() {
                        return Err("UpdateTimeToLive: TTL not enabled".to_string());
                    }
                    if ttl_spec.attribute_name() != "expires_at" {
                        return Err(format!(
                            "UpdateTimeToLive: expected attribute_name=expires_at, got {}",
                            ttl_spec.attribute_name()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "dynamodb-ttl:DescribeTimeToLive".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = ctx
                        .get("dynamoTable")
                        .ok_or_else(|| "dynamoTable not set".to_string())?;
                    let response = clients
                        .dynamodb()
                        .describe_time_to_live()
                        .table_name(&table)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let desc = response
                        .time_to_live_description()
                        .ok_or_else(|| "DescribeTimeToLive: description missing".to_string())?;
                    // The change is asynchronous on AWS, so a read taken
                    // shortly after it lands on ENABLING; whether it has
                    // settled is not something the API promises either way.
                    let status = desc
                        .time_to_live_status()
                        .map(|s| s.as_str())
                        .unwrap_or_default();
                    if status != "ENABLED" && status != "ENABLING" {
                        return Err(format!(
                            "DescribeTimeToLive: expected ENABLED or ENABLING, got {status}"
                        ));
                    }
                    if desc.attribute_name().unwrap_or("") != "expires_at" {
                        return Err(format!(
                            "DescribeTimeToLive: expected attribute_name=expires_at, got {}",
                            desc.attribute_name().unwrap_or("")
                        ));
                    }
                    Ok(())
                })
            }),
        );

        impls
    }

    fn setups(&self) -> HashMap<String, TestFn> {
        let mut setups: HashMap<String, TestFn> = HashMap::new();

        // dynamodb-items
        let clients = self.clients.clone();
        setups.insert(
            "dynamodb-items".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = format!("{}-dynitems", ctx.run_id.as_ref());
                    clients
                        .dynamodb()
                        .create_table()
                        .table_name(&table)
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("pk")
                                .key_type(KeyType::Hash)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("sk")
                                .key_type(KeyType::Range)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("pk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("sk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .billing_mode(BillingMode::PayPerRequest)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("dynamoTable", table);
                    Ok(())
                })
            }),
        );

        // dynamodb-query
        let clients = self.clients.clone();
        setups.insert(
            "dynamodb-query".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = format!("{}-dynquery", ctx.run_id.as_ref());
                    clients
                        .dynamodb()
                        .create_table()
                        .table_name(&table)
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("pk")
                                .key_type(KeyType::Hash)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("sk")
                                .key_type(KeyType::Range)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("pk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("sk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .billing_mode(BillingMode::PayPerRequest)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    for (sk, score) in [
                        ("item#1", "90"),
                        ("item#2", "45"),
                        ("item#3", "80"),
                        ("item#4", "10"),
                    ] {
                        let mut item = HashMap::new();
                        item.insert("pk".to_string(), AttributeValue::S("user#1".to_string()));
                        item.insert("sk".to_string(), AttributeValue::S(sk.to_string()));
                        item.insert("score".to_string(), AttributeValue::N(score.to_string()));
                        clients
                            .dynamodb()
                            .put_item()
                            .table_name(&table)
                            .set_item(Some(item))
                            .send()
                            .await
                            .map_err(crate::harness::sdk_error)?;
                    }
                    ctx.set("dynamoTable", table);
                    Ok(())
                })
            }),
        );

        // dynamodb-batch
        let clients = self.clients.clone();
        setups.insert(
            "dynamodb-batch".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = format!("{}-dynbatch", ctx.run_id.as_ref());
                    clients
                        .dynamodb()
                        .create_table()
                        .table_name(&table)
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("pk")
                                .key_type(KeyType::Hash)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("sk")
                                .key_type(KeyType::Range)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("pk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("sk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .billing_mode(BillingMode::PayPerRequest)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("dynamoTable", table);
                    Ok(())
                })
            }),
        );

        // dynamodb-txn
        let clients = self.clients.clone();
        setups.insert(
            "dynamodb-txn".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = format!("{}-dyntxn", ctx.run_id.as_ref());
                    clients
                        .dynamodb()
                        .create_table()
                        .table_name(&table)
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("pk")
                                .key_type(KeyType::Hash)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("sk")
                                .key_type(KeyType::Range)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("pk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("sk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .billing_mode(BillingMode::PayPerRequest)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("dynamoTable", table);
                    Ok(())
                })
            }),
        );

        // dynamodb-ttl
        let clients = self.clients.clone();
        setups.insert(
            "dynamodb-ttl".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let table = format!("{}-dynttl", ctx.run_id.as_ref());
                    clients
                        .dynamodb()
                        .create_table()
                        .table_name(&table)
                        .key_schema(
                            KeySchemaElement::builder()
                                .attribute_name("pk")
                                .key_type(KeyType::Hash)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .attribute_definitions(
                            AttributeDefinition::builder()
                                .attribute_name("pk")
                                .attribute_type(ScalarAttributeType::S)
                                .build()
                                .map_err(crate::harness::sdk_error_message)?,
                        )
                        .billing_mode(BillingMode::PayPerRequest)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("dynamoTable", table);
                    Ok(())
                })
            }),
        );

        setups
    }

    fn teardowns(&self) -> HashMap<String, TestFn> {
        let mut teardowns: HashMap<String, TestFn> = HashMap::new();

        let clients = self.clients.clone();
        teardowns.insert(
            "dynamodb-tables".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(table) = ctx.get("dynamoTable") {
                        let _ = clients
                            .dynamodb()
                            .delete_table()
                            .table_name(&table)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "dynamodb-items".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(table) = ctx.get("dynamoTable") {
                        let _ = clients
                            .dynamodb()
                            .delete_table()
                            .table_name(&table)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "dynamodb-query".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(table) = ctx.get("dynamoTable") {
                        let _ = clients
                            .dynamodb()
                            .delete_table()
                            .table_name(&table)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "dynamodb-batch".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(table) = ctx.get("dynamoTable") {
                        let _ = clients
                            .dynamodb()
                            .delete_table()
                            .table_name(&table)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "dynamodb-txn".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(table) = ctx.get("dynamoTable") {
                        let _ = clients
                            .dynamodb()
                            .delete_table()
                            .table_name(&table)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "dynamodb-ttl".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(table) = ctx.get("dynamoTable") {
                        let _ = clients
                            .dynamodb()
                            .delete_table()
                            .table_name(&table)
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
