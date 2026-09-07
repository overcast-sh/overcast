use std::collections::HashMap;
use std::sync::Arc;

use aws_sdk_secretsmanager::types::FilterNameStringType;

use crate::clients::AwsClients;
use crate::groups::ServiceGroup;
use crate::harness::{TestContext, TestFn};

/// AWS wants a ClientRequestToken of 32-64 characters, and the token becomes
/// the version ID, so a fixed UUID keeps the staging-label assertions readable.
const PENDING_TOKEN: &str = "0f9c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f";

/// A minimal, well-formed secret resource policy.
const COMPAT_RESOURCE_POLICY: &str = r#"{"Version":"2012-10-17","Statement":[{"Sid":"OvercastCompatRead","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}"#;

/// A placeholder rotation function. The rotate group always sets
/// `rotate_immediately(false)`, so it is never invoked: driving the four-step
/// protocol needs a real rotation Lambda and belongs with the Docker-dependent
/// Lambda groups.
fn rotation_lambda_arn(region: &str) -> String {
    format!("arn:aws:lambda:{region}:000000000000:function:oc-rotate-fn")
}

pub struct SecretsManagerGroup {
    clients: Arc<AwsClients>,
}

impl SecretsManagerGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for SecretsManagerGroup {
    fn name(&self) -> &'static str {
        "secretsmanager"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:CreateSecret".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-sm-crud-create", ctx.run_id.as_ref());
                    let response = clients
                        .secretsmanager()
                        .create_secret()
                        .name(&name)
                        .secret_string("create-test-value")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let arn = response.arn().unwrap_or_default();
                    if arn.is_empty() {
                        return Err("CreateSecret: ARN missing".to_string());
                    }
                    let _ = clients
                        .secretsmanager()
                        .delete_secret()
                        .secret_id(&name)
                        .force_delete_without_recovery(true)
                        .send()
                        .await;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:GetSecretValue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .get_secret_value()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let secret_string = response.secret_string().unwrap_or_default();
                    (secret_string == "initial-value")
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetSecretValue: expected 'initial-value', got '{}'",
                                secret_string
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:DescribeSecret".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let secret_name = response.name().unwrap_or_default();
                    (secret_name == name)
                        .then_some(())
                        .ok_or_else(|| "DescribeSecret: name mismatch".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:PutSecretValue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .put_secret_value()
                        .secret_id(&name)
                        .secret_string("updated-value")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.version_id().unwrap_or_default().is_empty() {
                        return Err("PutSecretValue: versionId missing".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:ListSecretVersionIds".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .list_secret_version_ids()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (response.versions().len() >= 2)
                        .then_some(())
                        .ok_or_else(|| "ListSecretVersionIds: expected >= 2 versions".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:UpdateSecret".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    clients
                        .secretsmanager()
                        .update_secret()
                        .secret_id(&name)
                        .description("updated desc")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let desc = response.description().unwrap_or_default();
                    (desc == "updated desc")
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "UpdateSecret: expected description 'updated desc', got '{}'",
                                desc
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:TagResource".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    let tag_project = aws_sdk_secretsmanager::types::Tag::builder()
                        .key("project")
                        .value("overcast")
                        .build();
                    let tag_env = aws_sdk_secretsmanager::types::Tag::builder()
                        .key("env")
                        .value("test")
                        .build();
                    clients
                        .secretsmanager()
                        .tag_resource()
                        .secret_id(&name)
                        .tags(tag_project)
                        .tags(tag_env)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let tags = response.tags();
                    let has_project = tags.iter().any(|tag| {
                        tag.key().unwrap_or_default() == "project"
                            && tag.value().unwrap_or_default() == "overcast"
                    });
                    has_project
                        .then_some(())
                        .ok_or_else(|| "TagResource: project=overcast tag not found".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:UntagResource".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    clients
                        .secretsmanager()
                        .untag_resource()
                        .secret_id(&name)
                        .tag_keys("env")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let tags = response.tags();
                    let has_env = tags
                        .iter()
                        .any(|tag| tag.key().unwrap_or_default() == "env");
                    (!has_env)
                        .then_some(())
                        .ok_or_else(|| "UntagResource: env tag still present".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:GetRandomPassword".to_string(),
            Arc::new(move |_ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let response = clients
                        .secretsmanager()
                        .get_random_password()
                        .password_length(20)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let password = response.random_password().unwrap_or_default();
                    (password.len() >= 20)
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetRandomPassword: expected length >= 20, got {}",
                                password.len()
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:BatchGetSecretValue".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    let filter = aws_sdk_secretsmanager::types::Filter::builder()
                        .key(FilterNameStringType::Name)
                        .values(name.clone())
                        .build();
                    let response = clients
                        .secretsmanager()
                        .batch_get_secret_value()
                        .filters(filter)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let values = response.secret_values();
                    (!values.is_empty())
                        .then_some(())
                        .ok_or_else(|| {
                            "BatchGetSecretValue: no secret values returned".to_string()
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:ListSecrets".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .list_secrets()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .secret_list()
                        .iter()
                        .any(|item| item.name().unwrap_or_default() == name);
                    found.then_some(()).ok_or_else(|| {
                        format!(
                            "ListSecrets: secret {} not found (runId={})",
                            name,
                            ctx.run_id
                        )
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-crud:DeleteSecret".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smCrudSecret")
                        .ok_or_else(|| "smCrudSecret not set".to_string())?;
                    clients
                        .secretsmanager()
                        .delete_secret()
                        .secret_id(&name)
                        .force_delete_without_recovery(true)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .secretsmanager()
                        .list_secrets()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .secret_list()
                        .iter()
                        .any(|item| item.name().unwrap_or_default() == name);
                    (!found).then_some(()).ok_or_else(|| {
                        format!(
                            "DeleteSecret: secret {} still present after deletion",
                            name
                        )
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-rotate:RotateSecretWithoutLambda".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smRotateSecret")
                        .ok_or_else(|| "smRotateSecret not set".to_string())?;
                    let rules = aws_sdk_secretsmanager::types::RotationRulesType::builder()
                        .automatically_after_days(30)
                        .build();
                    // AWS refuses to enable rotation on a secret with no rotation
                    // function, neither on the call nor already configured.
                    match clients
                        .secretsmanager()
                        .rotate_secret()
                        .secret_id(&name)
                        .rotation_rules(rules)
                        .send()
                        .await
                    {
                        Ok(_) => Err(
                            "RotateSecretWithoutLambda: expected InvalidRequestException, call succeeded"
                                .to_string(),
                        ),
                        Err(err) => {
                            let msg = crate::harness::sdk_error_message(err);
                            if msg.contains("InvalidRequestException") {
                                Ok(())
                            } else {
                                Err(format!(
                                    "RotateSecretWithoutLambda: expected InvalidRequestException, got {msg}"
                                ))
                            }
                        }
                    }
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-rotate:RotateSecret".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smRotateSecret")
                        .ok_or_else(|| "smRotateSecret not set".to_string())?;
                    let lambda_arn = rotation_lambda_arn(ctx.region.as_ref());
                    let rules = aws_sdk_secretsmanager::types::RotationRulesType::builder()
                        .automatically_after_days(30)
                        .build();
                    let response = clients
                        .secretsmanager()
                        .rotate_secret()
                        .secret_id(&name)
                        .rotation_lambda_arn(&lambda_arn)
                        .rotation_rules(rules)
                        .rotate_immediately(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.arn().unwrap_or_default().is_empty() {
                        return Err("RotateSecret: ARN missing".to_string());
                    }

                    let desc = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if !desc.rotation_enabled().unwrap_or(false) {
                        return Err("RotateSecret: RotationEnabled not set".to_string());
                    }
                    if desc.rotation_lambda_arn().unwrap_or_default() != lambda_arn {
                        return Err(format!(
                            "RotateSecret: RotationLambdaARN '{}', want '{}'",
                            desc.rotation_lambda_arn().unwrap_or_default(),
                            lambda_arn
                        ));
                    }
                    if desc.next_rotation_date().is_none() {
                        return Err("RotateSecret: NextRotationDate not set".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-rotate:PutSecretValuePending".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smRotateSecret")
                        .ok_or_else(|| "smRotateSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .put_secret_value()
                        .secret_id(&name)
                        .secret_string("pending-value")
                        .client_request_token(PENDING_TOKEN)
                        .version_stages("AWSPENDING")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.version_id().unwrap_or_default() != PENDING_TOKEN {
                        return Err(format!(
                            "PutSecretValuePending: VersionId '{}', want the ClientRequestToken '{}'",
                            response.version_id().unwrap_or_default(),
                            PENDING_TOKEN
                        ));
                    }
                    if !response
                        .version_stages()
                        .iter()
                        .any(|stage| stage == "AWSPENDING")
                    {
                        return Err(
                            "PutSecretValuePending: AWSPENDING missing from VersionStages".to_string()
                        );
                    }

                    // Staging a pending version must not change AWSCURRENT.
                    let current = clients
                        .secretsmanager()
                        .get_secret_value()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if current.secret_string().unwrap_or_default() != "rotate-test-value" {
                        return Err(format!(
                            "PutSecretValuePending: AWSCURRENT is '{}', want the untouched 'rotate-test-value'",
                            current.secret_string().unwrap_or_default()
                        ));
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-rotate:GetSecretValueByStage".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smRotateSecret")
                        .ok_or_else(|| "smRotateSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .get_secret_value()
                        .secret_id(&name)
                        .version_stage("AWSPENDING")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let value = response.secret_string().unwrap_or_default();
                    (value == "pending-value").then_some(()).ok_or_else(|| {
                        format!("GetSecretValueByStage: expected 'pending-value', got '{value}'")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-rotate:UpdateSecretVersionStage".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smRotateSecret")
                        .ok_or_else(|| "smRotateSecret not set".to_string())?;
                    let desc = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let current = desc
                        .version_ids_to_stages()
                        .and_then(|map| {
                            map.iter()
                                .find(|(_, stages)| stages.iter().any(|s| s == "AWSCURRENT"))
                                .map(|(id, _)| id.clone())
                        })
                        .ok_or_else(|| "UpdateSecretVersionStage: no AWSCURRENT version".to_string())?;

                    // This is what a rotation function's finishSecret step does.
                    clients
                        .secretsmanager()
                        .update_secret_version_stage()
                        .secret_id(&name)
                        .version_stage("AWSCURRENT")
                        .move_to_version_id(PENDING_TOKEN)
                        .remove_from_version_id(&current)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;

                    let after = clients
                        .secretsmanager()
                        .get_secret_value()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if after.secret_string().unwrap_or_default() != "pending-value" {
                        return Err(format!(
                            "UpdateSecretVersionStage: AWSCURRENT is '{}', want 'pending-value'",
                            after.secret_string().unwrap_or_default()
                        ));
                    }
                    if after.version_id().unwrap_or_default() != PENDING_TOKEN {
                        return Err(format!(
                            "UpdateSecretVersionStage: AWSCURRENT version is '{}', want '{}'",
                            after.version_id().unwrap_or_default(),
                            PENDING_TOKEN
                        ));
                    }

                    let post = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let displaced_is_previous = post
                        .version_ids_to_stages()
                        .and_then(|map| map.get(&current))
                        .map(|stages| stages.iter().any(|s| s == "AWSPREVIOUS"))
                        .unwrap_or(false);
                    displaced_is_previous.then_some(()).ok_or_else(|| {
                        "UpdateSecretVersionStage: displaced version did not become AWSPREVIOUS"
                            .to_string()
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-rotate:CancelRotateSecret".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smRotateSecret")
                        .ok_or_else(|| "smRotateSecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .cancel_rotate_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.arn().unwrap_or_default().is_empty() {
                        return Err("CancelRotateSecret: ARN missing".to_string());
                    }

                    let desc = clients
                        .secretsmanager()
                        .describe_secret()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if desc.rotation_enabled().unwrap_or(false) {
                        return Err(
                            "CancelRotateSecret: rotation still enabled after cancel".to_string()
                        );
                    }
                    // AWS keeps the function configured so rotation can be turned back on.
                    let want = rotation_lambda_arn(ctx.region.as_ref());
                    if desc.rotation_lambda_arn().unwrap_or_default() != want {
                        return Err(format!(
                            "CancelRotateSecret: RotationLambdaARN '{}', want it retained as '{}'",
                            desc.rotation_lambda_arn().unwrap_or_default(),
                            want
                        ));
                    }
                    Ok(())
                })
            }),
        );

        // ── secretsmanager-policy ────────────────────────────────────────────
        //
        // Overcast stores and validates a secret's resource policy but does not
        // evaluate it, so these assert the round-trip and the validation
        // verdict, never an access decision.

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-policy:ValidateResourcePolicy".to_string(),
            Arc::new(move |_ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let ok = clients
                        .secretsmanager()
                        .validate_resource_policy()
                        .resource_policy(COMPAT_RESOURCE_POLICY)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if !ok.policy_validation_passed() {
                        return Err(format!(
                            "ValidateResourcePolicy: valid policy rejected: {:?}",
                            ok.validation_errors()
                        ));
                    }

                    let bad = clients
                        .secretsmanager()
                        .validate_resource_policy()
                        .resource_policy(r#"{"Version":"2012-10-17"}"#)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if bad.policy_validation_passed() {
                        return Err(
                            "ValidateResourcePolicy: policy with no Statement should not pass"
                                .to_string(),
                        );
                    }
                    (!bad.validation_errors().is_empty())
                        .then_some(())
                        .ok_or_else(|| {
                            "ValidateResourcePolicy: no ValidationErrors for a failing policy"
                                .to_string()
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-policy:PutResourcePolicy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smPolicySecret")
                        .ok_or_else(|| "smPolicySecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .put_resource_policy()
                        .secret_id(&name)
                        .resource_policy(COMPAT_RESOURCE_POLICY)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if response.arn().unwrap_or_default().is_empty() {
                        return Err("PutResourcePolicy: ARN missing".to_string());
                    }
                    (response.name().unwrap_or_default() == name)
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "PutResourcePolicy: Name '{}', want '{}'",
                                response.name().unwrap_or_default(),
                                name
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-policy:GetResourcePolicy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smPolicySecret")
                        .ok_or_else(|| "smPolicySecret not set".to_string())?;
                    let response = clients
                        .secretsmanager()
                        .get_resource_policy()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let policy = response.resource_policy().unwrap_or_default();
                    policy
                        .contains("OvercastCompatRead")
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetResourcePolicy: policy '{policy}' does not carry the Sid that was stored"
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "secretsmanager-policy:DeleteResourcePolicy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("smPolicySecret")
                        .ok_or_else(|| "smPolicySecret not set".to_string())?;
                    clients
                        .secretsmanager()
                        .delete_resource_policy()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .secretsmanager()
                        .get_resource_policy()
                        .secret_id(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let policy = response.resource_policy().unwrap_or_default();
                    policy.is_empty().then_some(()).ok_or_else(|| {
                        format!("DeleteResourcePolicy: policy still present: '{policy}'")
                    })
                })
            }),
        );

        impls
    }

    fn setups(&self) -> HashMap<String, TestFn> {
        let mut setups: HashMap<String, TestFn> = HashMap::new();

        let clients = self.clients.clone();
        setups.insert(
            "secretsmanager-crud".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-sm-crud", ctx.run_id.as_ref());
                    clients
                        .secretsmanager()
                        .create_secret()
                        .name(&name)
                        .secret_string("initial-value")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("smCrudSecret", name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "secretsmanager-rotate".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-sm-rotate", ctx.run_id.as_ref());
                    clients
                        .secretsmanager()
                        .create_secret()
                        .name(&name)
                        .secret_string("rotate-test-value")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("smRotateSecret", name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "secretsmanager-policy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-sm-policy", ctx.run_id.as_ref());
                    clients
                        .secretsmanager()
                        .create_secret()
                        .name(&name)
                        .secret_string("policy-value")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("smPolicySecret", name);
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
            "secretsmanager-crud".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(name) = ctx.get("smCrudSecret") {
                        let _ = clients
                            .secretsmanager()
                            .delete_secret()
                            .secret_id(&name)
                            .force_delete_without_recovery(true)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "secretsmanager-rotate".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(name) = ctx.get("smRotateSecret") {
                        let _ = clients
                            .secretsmanager()
                            .delete_secret()
                            .secret_id(&name)
                            .force_delete_without_recovery(true)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "secretsmanager-policy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(name) = ctx.get("smPolicySecret") {
                        let _ = clients
                            .secretsmanager()
                            .delete_secret()
                            .secret_id(&name)
                            .force_delete_without_recovery(true)
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
