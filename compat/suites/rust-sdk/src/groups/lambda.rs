use std::collections::HashMap;
use std::sync::Arc;

use aws_sdk_lambda::primitives::Blob;
use aws_sdk_lambda::types::{
    FunctionCode, InvocationType, LayerVersionContentInput, ResponseStreamingInvocationType,
    Runtime,
};
use aws_smithy_types::error::metadata::ProvideErrorMetadata;

use crate::clients::AwsClients;
use crate::groups::ServiceGroup;
use crate::harness::{TestContext, TestFn};

pub struct LambdaGroup {
    clients: Arc<AwsClients>,
}

impl LambdaGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for LambdaGroup {
    fn name(&self) -> &'static str {
        "lambda"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        // ── lambda-crud ────────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "lambda-crud:CreateFunction".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn", ctx.run_id.as_ref());
                    let role = "arn:aws:iam::000000000000:role/lambda-exec";
                    let response = clients
                        .lambda()
                        .create_function()
                        .function_name(&name)
                        .runtime(Runtime::Nodejs20x)
                        .handler("index.handler")
                        .role(role)
                        .code(
                            FunctionCode::builder()
                                .zip_file(Blob::new(dummy_zip()))
                                .build(),
                        )
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.function_arn().unwrap_or_default().is_empty() {
                        return Err("CreateFunction: FunctionArn missing".to_string());
                    }
                    if response.function_name().unwrap_or_default() != name {
                        return Err(format!(
                            "CreateFunction: expected FunctionName={name}, got {}",
                            response.function_name().unwrap_or_default()
                        ));
                    }
                    if response.code_sha256().unwrap_or_default().is_empty() {
                        return Err("CreateFunction: CodeSha256 missing".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-crud:GetFunction".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .get_function()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let config = response
                        .configuration()
                        .ok_or_else(|| "GetFunction: Configuration missing".to_string())?;
                    if config.function_arn().unwrap_or_default().is_empty() {
                        return Err("GetFunction: FunctionArn missing".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-crud:ListFunctions".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .list_functions()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .functions()
                        .iter()
                        .any(|f| f.function_name().unwrap_or_default() == name);
                    found
                        .then_some(())
                        .ok_or_else(|| format!("ListFunctions: {name} not found"))
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-crud:UpdateFunctionCode".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .update_function_code()
                        .function_name(&name)
                        .zip_file(Blob::new(dummy_zip()))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.function_arn().unwrap_or_default().is_empty() {
                        return Err("UpdateFunctionCode: FunctionArn missing".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-crud:UpdateFunctionConfiguration".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn", ctx.run_id.as_ref());
                    let mut env_vars = HashMap::new();
                    env_vars.insert("LOG_LEVEL".to_string(), "debug".to_string());

                    // A real queue, so DeadLetterConfig names a target that
                    // exists rather than a plausible-looking string.
                    let queue_name = format!("{}-fn-dlq", ctx.run_id.as_ref());
                    let queue_url = clients
                        .sqs()
                        .create_queue()
                        .queue_name(&queue_name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?
                        .queue_url()
                        .unwrap_or_default()
                        .to_string();
                    let dlq_arn = clients
                        .sqs()
                        .get_queue_attributes()
                        .queue_url(&queue_url)
                        .attribute_names(aws_sdk_sqs::types::QueueAttributeName::QueueArn)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?
                        .attributes()
                        .and_then(|a| a.get(&aws_sdk_sqs::types::QueueAttributeName::QueueArn))
                        .cloned()
                        .unwrap_or_default();
                    if dlq_arn.is_empty() {
                        return Err("UpdateFunctionConfiguration: queue has no ARN".to_string());
                    }

                    // DeadLetterConfig rides along because it is the member that
                    // used to answer 501 here, which failed every `cdk deploy`
                    // of a function with a DLQ. Both the response and a later
                    // read are checked: an update that answers 200 and drops the
                    // property is the same bug wearing a better status code.
                    let result = async {
                        let response = clients
                            .lambda()
                            .update_function_configuration()
                            .function_name(&name)
                            .timeout(30)
                            .memory_size(256)
                            .environment(
                                aws_sdk_lambda::types::Environment::builder()
                                    .set_variables(Some(env_vars))
                                    .build(),
                            )
                            .dead_letter_config(
                                aws_sdk_lambda::types::DeadLetterConfig::builder()
                                    .target_arn(&dlq_arn)
                                    .build(),
                            )
                            .send()
                            .await
                            .map_err(crate::harness::sdk_error)?;
                        if response.timeout().unwrap_or_default() != 30 {
                            return Err(format!(
                                "UpdateFunctionConfiguration: expected Timeout=30, got {}",
                                response.timeout().unwrap_or_default()
                            ));
                        }
                        let echoed = response
                            .dead_letter_config()
                            .and_then(|d| d.target_arn())
                            .unwrap_or_default();
                        if echoed != dlq_arn {
                            return Err(format!(
                                "UpdateFunctionConfiguration: expected DeadLetterConfig.TargetArn={dlq_arn}, got {echoed}"
                            ));
                        }
                        let fetched = clients
                            .lambda()
                            .get_function_configuration()
                            .function_name(&name)
                            .send()
                            .await
                            .map_err(crate::harness::sdk_error)?;
                        let stored = fetched
                            .dead_letter_config()
                            .and_then(|d| d.target_arn())
                            .unwrap_or_default();
                        if stored != dlq_arn {
                            return Err(format!(
                                "UpdateFunctionConfiguration: expected the stored DeadLetterConfig.TargetArn={dlq_arn}, got {stored}"
                            ));
                        }
                        Ok(())
                    }
                    .await;

                    let _ = clients
                        .sqs()
                        .delete_queue()
                        .queue_url(&queue_url)
                        .send()
                        .await;
                    result
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-crud:DeleteFunction".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn", ctx.run_id.as_ref());
                    clients
                        .lambda()
                        .delete_function()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .lambda()
                        .list_functions()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .functions()
                        .iter()
                        .any(|f| f.function_name().unwrap_or_default() == name);
                    (!found)
                        .then_some(())
                        .ok_or_else(|| format!("DeleteFunction: {name} still present after delete"))
                })
            }),
        );

        // ── lambda-policy ──────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "lambda-policy:AddPermission".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-policy", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .add_permission()
                        .function_name(&name)
                        .statement_id("allow-s3")
                        .action("lambda:InvokeFunction")
                        .principal("s3.amazonaws.com")
                        .source_account("000000000000")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if !response.statement().unwrap_or_default().contains("\"Sid\":\"allow-s3\"") {
                        return Err("AddPermission: statement missing allow-s3 SID".to_string());
                    }
                    let policy = clients
                        .lambda()
                        .get_policy()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    policy
                        .policy()
                        .unwrap_or_default()
                        .contains("\"Sid\":\"allow-s3\"")
                        .then_some(())
                        .ok_or_else(|| "AddPermission: statement missing from GetPolicy".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-policy:GetPolicy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-policy", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .get_policy()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if !response.policy().unwrap_or_default().contains("\"Sid\":\"allow-s3\"") {
                        return Err("GetPolicy: allow-s3 statement missing".to_string());
                    }
                    response
                        .revision_id()
                        .filter(|revision| !revision.is_empty())
                        .map(|_| ())
                        .ok_or_else(|| "GetPolicy: RevisionId missing".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-policy:RemovePermission".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-policy", ctx.run_id.as_ref());
                    clients
                        .lambda()
                        .remove_permission()
                        .function_name(&name)
                        .statement_id("allow-s3")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let result = clients.lambda().get_policy().function_name(&name).send().await;
                    match result {
                        Ok(_) => Err("RemovePermission: policy still exists".to_string()),
                        Err(err)
                            if err.as_service_error().and_then(ProvideErrorMetadata::code)
                                == Some("ResourceNotFoundException") =>
                        {
                            Ok(())
                        }
                        Err(err) => Err(format!(
                            "RemovePermission: expected ResourceNotFoundException, got {}",
                            crate::harness::sdk_error_message(err)
                        )),
                    }
                })
            }),
        );

        // ── lambda-invoke ──────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "lambda-invoke:InvokeDryRun".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .invoke()
                        .function_name(&name)
                        .invocation_type(InvocationType::DryRun)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.status_code() != 204 {
                        return Err(format!(
                            "InvokeDryRun: expected StatusCode=204, got {}",
                            response.status_code()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-invoke:InvokeSync".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .invoke()
                        .function_name(&name)
                        .invocation_type(InvocationType::RequestResponse)
                        .payload(Blob::new(b"{\"test\":true}".to_vec()))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.status_code() != 200 {
                        return Err(format!(
                            "InvokeSync: expected StatusCode=200, got {}",
                            response.status_code()
                        ));
                    }
                    if response.function_error().is_some() {
                        let body = response
                            .payload()
                            .map(|p| String::from_utf8_lossy(p.as_ref()).to_string())
                            .unwrap_or_default();
                        return Err(format!(
                            "InvokeSync: function error: {} — {body}",
                            response.function_error().unwrap_or_default()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-invoke:InvokeAsync".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .invoke()
                        .function_name(&name)
                        .invocation_type(InvocationType::Event)
                        .payload(Blob::new(b"{}".to_vec()))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.status_code() != 202 {
                        return Err(format!(
                            "InvokeAsync: expected StatusCode=202, got {}",
                            response.status_code()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        // ── lambda-invoke-stream ───────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "lambda-invoke-stream:InvokeWithResponseStream".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-stream", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .invoke_with_response_stream()
                        .function_name(&name)
                        .invocation_type(ResponseStreamingInvocationType::RequestResponse)
                        .payload(Blob::new(b"{\"test\":true}".to_vec()))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.status_code() != 200 {
                        return Err(format!(
                            "InvokeWithResponseStream: expected StatusCode=200, got {}",
                            response.status_code()
                        ));
                    }
                    let mut stream = response.event_stream;
                    let mut chunk_count = 0usize;
                    let mut completed = false;
                    loop {
                        use aws_sdk_lambda::types::InvokeWithResponseStreamResponseEvent;
                        match stream.recv().await {
                            Ok(Some(InvokeWithResponseStreamResponseEvent::PayloadChunk(
                                _chunk,
                            ))) => {
                                chunk_count += 1;
                            }
                            Ok(Some(InvokeWithResponseStreamResponseEvent::InvokeComplete(
                                _complete,
                            ))) => {
                                completed = true;
                                break;
                            }
                            Ok(None) | Err(_) => break,
                            _ => {}
                        }
                    }
                    if !completed {
                        return Err(
                            "InvokeWithResponseStream: expected InvokeComplete event".to_string()
                        );
                    }
                    if chunk_count == 0 {
                        return Err(
                            "InvokeWithResponseStream: expected at least one payload chunk"
                                .to_string(),
                        );
                    }
                    Ok(())
                })
            }),
        );

        // ── lambda-invoke-error ────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "lambda-invoke-error:InvokeWithError".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke-err", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .invoke()
                        .function_name(&name)
                        .invocation_type(InvocationType::RequestResponse)
                        .payload(Blob::new(b"{}".to_vec()))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    // A handler that throws is not an SDK error and not a
                    // non-200: AWS answers 200 and signals the throw in the
                    // X-Amz-Function-Error header, which the SDK surfaces as
                    // FunctionError. An emulator that reported the failure as
                    // a 500 — or as a clean 200 with no FunctionError at all —
                    // is what this test exists to catch.
                    if response.status_code() != 200 {
                        return Err(format!(
                            "InvokeWithError: expected StatusCode=200, got {}",
                            response.status_code()
                        ));
                    }
                    let function_error = response.function_error().unwrap_or_default();
                    if function_error != "Unhandled" {
                        return Err(format!(
                            "InvokeWithError: expected FunctionError=Unhandled for a throwing handler, got {function_error:?}"
                        ));
                    }
                    let body = response
                        .payload()
                        .map(|payload| String::from_utf8_lossy(payload.as_ref()).to_string())
                        .unwrap_or_default();
                    if !body.contains("errorMessage") {
                        return Err(format!(
                            "InvokeWithError: expected the payload to carry errorMessage, got {body}"
                        ));
                    }
                    Ok(())
                })
            }),
        );

        // ── lambda-aliases ─────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "lambda-aliases:PublishVersion".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .publish_version()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.version().unwrap_or_default().is_empty() {
                        return Err("PublishVersion: Version missing".to_string());
                    }
                    ctx.set(
                        "_lambdaVersion",
                        response.version().unwrap_or_default().to_string(),
                    );
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-aliases:ListVersionsByFunction".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .list_versions_by_function()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let count = response.versions().len();
                    (count > 0).then_some(()).ok_or_else(|| {
                        "ListVersionsByFunction: expected at least one version".to_string()
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-aliases:CreateAlias".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let version = ctx.get("_lambdaVersion").unwrap_or_else(|| "1".to_string());
                    let response = clients
                        .lambda()
                        .create_alias()
                        .function_name(&name)
                        .name("live")
                        .function_version(&version)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.alias_arn().unwrap_or_default().is_empty() {
                        return Err("CreateAlias: AliasArn missing".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-aliases:GetAlias".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .get_alias()
                        .function_name(&name)
                        .name("live")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.name().unwrap_or_default() != "live" {
                        return Err(format!(
                            "GetAlias: expected Name=live, got {}",
                            response.name().unwrap_or_default()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-aliases:ListAliases".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .list_aliases()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .aliases()
                        .iter()
                        .any(|a| a.name().unwrap_or_default() == "live");
                    found
                        .then_some(())
                        .ok_or_else(|| "ListAliases: expected alias 'live'".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-aliases:UpdateAlias".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .update_alias()
                        .function_name(&name)
                        .name("live")
                        .description("production alias")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.description().unwrap_or_default() != "production alias" {
                        return Err(format!(
                            "UpdateAlias: expected Description=\"production alias\", got {}",
                            response.description().unwrap_or_default()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-aliases:DeleteAlias".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    clients
                        .lambda()
                        .delete_alias()
                        .function_name(&name)
                        .name("live")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .lambda()
                        .list_aliases()
                        .function_name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .aliases()
                        .iter()
                        .any(|a| a.name().unwrap_or_default() == "live");
                    (!found).then_some(()).ok_or_else(|| {
                        "DeleteAlias: alias 'live' still present after delete".to_string()
                    })
                })
            }),
        );

        // ── lambda-layers ──────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "lambda-layers:PublishLayerVersion".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let layer_name = format!("{}-layer", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .publish_layer_version()
                        .layer_name(&layer_name)
                        .content(
                            LayerVersionContentInput::builder()
                                .zip_file(Blob::new(dummy_zip()))
                                .build(),
                        )
                        .compatible_runtimes(Runtime::Nodejs20x)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.layer_version_arn().unwrap_or_default().is_empty() {
                        return Err("PublishLayerVersion: LayerVersionArn missing".to_string());
                    }
                    ctx.set("_layerVersion", response.version().to_string());
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-layers:ListLayers".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let layer_name = format!("{}-layer", ctx.run_id.as_ref());
                    let response = clients
                        .lambda()
                        .list_layers()
                        .compatible_runtime(Runtime::Nodejs20x)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .layers()
                        .iter()
                        .any(|l| l.layer_name().unwrap_or_default() == layer_name);
                    found
                        .then_some(())
                        .ok_or_else(|| format!("ListLayers: {layer_name} not found"))
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "lambda-layers:DeleteLayerVersion".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let layer_name = format!("{}-layer", ctx.run_id.as_ref());
                    let version = ctx
                        .get("_layerVersion")
                        .and_then(|v| v.parse::<i64>().ok())
                        .unwrap_or(1);
                    clients
                        .lambda()
                        .delete_layer_version()
                        .layer_name(&layer_name)
                        .version_number(version)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        impls
    }

    fn setups(&self) -> HashMap<String, TestFn> {
        let mut setups: HashMap<String, TestFn> = HashMap::new();

        let clients = self.clients.clone();
        setups.insert(
            "lambda-policy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-policy", ctx.run_id.as_ref());
                    clients
                        .lambda()
                        .create_function()
                        .function_name(&name)
                        .runtime(Runtime::Nodejs20x)
                        .handler("index.handler")
                        .role("arn:aws:iam::000000000000:role/lambda-exec")
                        .code(FunctionCode::builder().zip_file(Blob::new(dummy_zip())).build())
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "lambda-invoke".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke", ctx.run_id.as_ref());
                    let role = "arn:aws:iam::000000000000:role/lambda-exec";
                    clients
                        .lambda()
                        .create_function()
                        .function_name(&name)
                        .runtime(Runtime::Nodejs20x)
                        .handler("index.handler")
                        .role(role)
                        .timeout(30)
                        .code(
                            FunctionCode::builder()
                                .zip_file(Blob::new(dummy_zip()))
                                .build(),
                        )
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    wait_function_active(&clients, &name, 30).await?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "lambda-invoke-stream".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-stream", ctx.run_id.as_ref());
                    let role = "arn:aws:iam::000000000000:role/lambda-exec";
                    clients
                        .lambda()
                        .create_function()
                        .function_name(&name)
                        .runtime(Runtime::Nodejs20x)
                        .handler("index.handler")
                        .role(role)
                        .timeout(30)
                        .code(
                            FunctionCode::builder()
                                .zip_file(Blob::new(dummy_zip()))
                                .build(),
                        )
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    wait_function_active(&clients, &name, 30).await?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "lambda-invoke-error".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke-err", ctx.run_id.as_ref());
                    let role = "arn:aws:iam::000000000000:role/lambda-exec";
                    clients
                        .lambda()
                        .create_function()
                        .function_name(&name)
                        .runtime(Runtime::Nodejs20x)
                        .handler("index.handler")
                        .role(role)
                        .timeout(30)
                        .code(
                            FunctionCode::builder()
                                .zip_file(Blob::new(throwing_zip()))
                                .build(),
                        )
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    wait_function_active(&clients, &name, 30).await?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "lambda-aliases".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let role = "arn:aws:iam::000000000000:role/lambda-exec";
                    clients
                        .lambda()
                        .create_function()
                        .function_name(&name)
                        .runtime(Runtime::Nodejs20x)
                        .handler("index.handler")
                        .role(role)
                        .code(
                            FunctionCode::builder()
                                .zip_file(Blob::new(dummy_zip()))
                                .build(),
                        )
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
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
            "lambda-policy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-policy", ctx.run_id.as_ref());
                    let _ = clients.lambda().delete_function().function_name(&name).send().await;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "lambda-crud".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn", ctx.run_id.as_ref());
                    let _ = clients
                        .lambda()
                        .delete_function()
                        .function_name(&name)
                        .send()
                        .await;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "lambda-invoke".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke", ctx.run_id.as_ref());
                    let _ = clients
                        .lambda()
                        .delete_function()
                        .function_name(&name)
                        .send()
                        .await;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "lambda-invoke-stream".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-stream", ctx.run_id.as_ref());
                    let _ = clients
                        .lambda()
                        .delete_function()
                        .function_name(&name)
                        .send()
                        .await;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "lambda-invoke-error".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-invoke-err", ctx.run_id.as_ref());
                    let _ = clients
                        .lambda()
                        .delete_function()
                        .function_name(&name)
                        .send()
                        .await;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "lambda-aliases".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-fn-alias", ctx.run_id.as_ref());
                    let _ = clients
                        .lambda()
                        .delete_function()
                        .function_name(&name)
                        .send()
                        .await;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "lambda-layers".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let layer_name = format!("{}-layer", ctx.run_id.as_ref());
                    let version = ctx
                        .get("_layerVersion")
                        .and_then(|v| v.parse::<i64>().ok())
                        .unwrap_or(1);
                    let _ = clients
                        .lambda()
                        .delete_layer_version()
                        .layer_name(&layer_name)
                        .version_number(version)
                        .send()
                        .await;
                    Ok(())
                })
            }),
        );

        teardowns
    }
}

fn dummy_zip() -> Vec<u8> {
    use base64::Engine;
    let b64 = "UEsDBBQAAAAAAAAAAAAKhksPNQAAADUAAAAIAAAAaW5kZXguanNleHBvcnRzLmhhbmRsZXI9YXN5bmMoKT0+KHtzdGF0dXNDb2RlOjIwMCxib2R5OiJvayJ9KVBLAQIUABQAAAAAAAAAAAAKhksPNQAAADUAAAAIAAAAAAAAAAAAAAAAAAAAAABpbmRleC5qc1BLBQYAAAAAAQABADYAAABbAAAAAAA=";
    base64::engine::general_purpose::STANDARD
        .decode(b64)
        .unwrap_or_default()
}

/// The same one-file `index.js` zip as [`dummy_zip`], but with a handler that
/// throws unconditionally:
///
/// ```js
/// exports.handler=async()=>{throw new Error("compat: intentional failure")}
/// ```
///
/// `lambda-invoke-error` asserts on `FunctionError`, which only appears when
/// the handler really runs and really throws — a zip that returns cleanly
/// would make the test pass or fail for the wrong reason.
fn throwing_zip() -> Vec<u8> {
    use base64::Engine;
    let b64 = "UEsDBBQAAAAAAAAAIQDjPBUwSQAAAEkAAAAIAAAAaW5kZXguanNleHBvcnRzLmhhbmRsZXI9YXN5bmMoKT0+e3Rocm93IG5ldyBFcnJvcigiY29tcGF0OiBpbnRlbnRpb25hbCBmYWlsdXJlIil9UEsBAhQAFAAAAAAAAAAhAOM8FTBJAAAASQAAAAgAAAAAAAAAAAAAAKQBAAAAAGluZGV4LmpzUEsFBgAAAAABAAEANgAAAG8AAAAAAA==";
    base64::engine::general_purpose::STANDARD
        .decode(b64)
        .unwrap_or_default()
}

async fn wait_function_active(
    clients: &AwsClients,
    name: &str,
    max_attempts: u32,
) -> Result<(), String> {
    for _ in 0..max_attempts {
        let response = clients
            .lambda()
            .get_function()
            .function_name(name)
            .send()
            .await
            .map_err(crate::harness::sdk_error)?;
        let state = response.configuration().and_then(|c| c.state().cloned());
        match state {
            Some(s) if s.as_str() == "Active" => return Ok(()),
            Some(s) if s.as_str() != "Pending" => return Ok(()),
            _ => {}
        }
        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
    }
    Err(format!(
        "Function {name} did not become Active after {max_attempts} attempts"
    ))
}
