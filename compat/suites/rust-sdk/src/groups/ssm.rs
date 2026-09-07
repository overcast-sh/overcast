use std::collections::HashMap;
use std::sync::Arc;

use aws_sdk_ssm::types::{ParameterStringFilter, ParameterType, ResourceTypeForTagging};

use crate::clients::AwsClients;
use crate::groups::ServiceGroup;
use crate::harness::{TestContext, TestFn};

pub struct SsmGroup {
    clients: Arc<AwsClients>,
}

impl SsmGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for SsmGroup {
    fn name(&self) -> &'static str {
        "ssm"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        // ═══ ssm-parameters ═══════════════════════════════════════════════════

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:PutParameter".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-ssm-param-new", ctx.run_id.as_ref());
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name)
                        .value("test-value")
                        .r#type(ParameterType::String)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let get = clients
                        .ssm()
                        .get_parameter()
                        .name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let param = get.parameter().ok_or_else(|| {
                        "PutParameter: parameter not found after create".to_string()
                    })?;
                    (param.value().unwrap_or_default() == "test-value")
                        .then_some(())
                        .ok_or_else(|| "PutParameter: value mismatch".to_string())?;
                    ctx.set("ssmParamNew", name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:GetParameter".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("ssmParam")
                        .ok_or_else(|| "ssmParam not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameter()
                        .name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let param = response
                        .parameter()
                        .ok_or_else(|| "GetParameter: parameter missing".to_string())?;
                    (param.value().unwrap_or_default() == "v1")
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetParameter: expected 'v1', got '{}'",
                                param.value().unwrap_or_default()
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:PutParameterOverwrite".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("ssmParam")
                        .ok_or_else(|| "ssmParam not set".to_string())?;
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name)
                        .value("v2")
                        .r#type(ParameterType::String)
                        .overwrite(true)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let get = clients
                        .ssm()
                        .get_parameter()
                        .name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let param = get
                        .parameter()
                        .ok_or_else(|| "PutParameterOverwrite: parameter missing".to_string())?;
                    (param.value().unwrap_or_default() == "v2")
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "PutParameterOverwrite: expected 'v2', got '{}'",
                                param.value().unwrap_or_default()
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:GetParameterHistory".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("ssmParam")
                        .ok_or_else(|| "ssmParam not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameter_history()
                        .name(&name)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (response.parameters().len() >= 2)
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetParameterHistory: expected >= 2 versions, got {}",
                                response.parameters().len()
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:PutMultipleParameters".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name1 = format!("{}-ssm-param-multi1", ctx.run_id.as_ref());
                    let name2 = format!("{}-ssm-param-multi2", ctx.run_id.as_ref());
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name1)
                        .value("multi-value-1")
                        .r#type(ParameterType::String)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name2)
                        .value("multi-value-2")
                        .r#type(ParameterType::String)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .ssm()
                        .get_parameters()
                        .names(&name1)
                        .names(&name2)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (response.parameters().len() >= 2)
                        .then_some(())
                        .ok_or_else(|| {
                            "PutMultipleParameters: expected >= 2 parameters".to_string()
                        })?;
                    ctx.set("ssmMultiple1", name1);
                    ctx.set("ssmMultiple2", name2);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:GetParameters".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name1 = ctx
                        .get("ssmMultiple1")
                        .ok_or_else(|| "ssmMultiple1 not set".to_string())?;
                    let name2 = ctx
                        .get("ssmMultiple2")
                        .ok_or_else(|| "ssmMultiple2 not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameters()
                        .names(&name1)
                        .names(&name2)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (response.parameters().len() >= 2)
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetParameters: expected >= 2 parameters, got {}",
                                response.parameters().len()
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:DescribeParameters".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let prefix = format!("{}-ssm", ctx.run_id.as_ref());
                    let filter = ParameterStringFilter::builder()
                        .key("Name")
                        .option("Contains")
                        .values(prefix)
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let response = clients
                        .ssm()
                        .describe_parameters()
                        .parameter_filters(filter)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (!response.parameters().is_empty())
                        .then_some(())
                        .ok_or_else(|| "DescribeParameters: no parameters found".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:TagParameter".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("ssmParam")
                        .ok_or_else(|| "ssmParam not set".to_string())?;
                    let tag_project = aws_sdk_ssm::types::Tag::builder()
                        .key("project")
                        .value("overcast")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let tag_env = aws_sdk_ssm::types::Tag::builder()
                        .key("env")
                        .value("test")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    clients
                        .ssm()
                        .add_tags_to_resource()
                        .resource_id(&name)
                        .resource_type(ResourceTypeForTagging::Parameter)
                        .tags(tag_project)
                        .tags(tag_env)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:ListSSMTagsForResource".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("ssmParam")
                        .ok_or_else(|| "ssmParam not set".to_string())?;
                    let response = clients
                        .ssm()
                        .list_tags_for_resource()
                        .resource_id(&name)
                        .resource_type(ResourceTypeForTagging::Parameter)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (!response.tag_list().is_empty())
                        .then_some(())
                        .ok_or_else(|| "ListSSMTagsForResource: no tags returned".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-parameters:DeleteParameters".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let mut names: Vec<String> = Vec::new();
                    for key in &["ssmParam", "ssmParamNew", "ssmMultiple1", "ssmMultiple2"] {
                        if let Some(name) = ctx.get(key) {
                            names.push(name);
                        }
                    }
                    if names.is_empty() {
                        return Err("DeleteParameters: no parameter names to delete".to_string());
                    }
                    let mut request = clients.ssm().delete_parameters();
                    for name in &names {
                        request = request.names(name);
                    }
                    request.send().await.map_err(crate::harness::sdk_error)?;
                    let prefix = format!("{}-ssm-param", ctx.run_id.as_ref());
                    let filter = ParameterStringFilter::builder()
                        .key("Name")
                        .option("Contains")
                        .values(prefix)
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let response = clients
                        .ssm()
                        .describe_parameters()
                        .parameter_filters(filter)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    for param in response.parameters() {
                        let param_name = param.name().unwrap_or_default();
                        if names.contains(&param_name.to_string()) {
                            return Err(format!(
                                "DeleteParameters: param {param_name} still present after deletion"
                            ));
                        }
                    }
                    Ok(())
                })
            }),
        );

        // ═══ ssm-secure ═══════════════════════════════════════════════════════

        let clients = self.clients.clone();
        impls.insert(
            "ssm-secure:PutSecureStringParameter".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-ssm-secure-new", ctx.run_id.as_ref());
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name)
                        .value("secret-123")
                        .r#type(ParameterType::SecureString)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let get = clients
                        .ssm()
                        .get_parameter()
                        .name(&name)
                        .with_decryption(true)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let param = get.parameter().ok_or_else(|| {
                        "PutSecureStringParameter: parameter not found".to_string()
                    })?;
                    (param.value().unwrap_or_default() == "secret-123")
                        .then_some(())
                        .ok_or_else(|| "PutSecureStringParameter: value mismatch".to_string())?;
                    ctx.set("ssmSecureNewParam", name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-secure:GetSecureStringParameter".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("ssmSecureParam")
                        .ok_or_else(|| "ssmSecureParam not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameter()
                        .name(&name)
                        .with_decryption(true)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let param = response
                        .parameter()
                        .ok_or_else(|| "GetSecureStringParameter: parameter missing".to_string())?;
                    (param.r#type() == Some(&ParameterType::SecureString))
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetSecureStringParameter: expected SecureString type, got {:?}",
                                param.r#type()
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-secure:GetSecureStringWithoutDecryption".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = ctx
                        .get("ssmSecureParam")
                        .ok_or_else(|| "ssmSecureParam not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameter()
                        .name(&name)
                        .with_decryption(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let param = response.parameter().ok_or_else(|| {
                        "GetSecureStringWithoutDecryption: parameter missing".to_string()
                    })?;
                    // AWS returns the KMS ciphertext, not a mask, so the
                    // portable assertion is that the plaintext did not come
                    // back — which is what node-js-sdk and go-sdk assert. This
                    // used to demand a literal "***", matching neither AWS nor
                    // the emulator, and so failed a behaviour that is correct.
                    let value = param.value().unwrap_or_default();
                    (value != "my-secret").then_some(()).ok_or_else(|| {
                        "GetSecureStringWithoutDecryption: plaintext returned for an encrypted parameter"
                            .to_string()
                    })
                })
            }),
        );

        // ═══ ssm-path ═════════════════════════════════════════════════════════

        let clients = self.clients.clone();
        impls.insert(
            "ssm-path:GetParametersByPath".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let path = ctx
                        .get("ssmPathPrefix")
                        .ok_or_else(|| "ssmPathPrefix not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameters_by_path()
                        .path(&path)
                        .recursive(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (!response.parameters().is_empty())
                        .then_some(())
                        .ok_or_else(|| "GetParametersByPath: no parameters returned".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-path:GetParametersByPathRecursive".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let path = ctx
                        .get("ssmPathPrefix")
                        .ok_or_else(|| "ssmPathPrefix not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameters_by_path()
                        .path(&path)
                        .recursive(true)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (response.parameters().len() >= 2)
                        .then_some(())
                        .ok_or_else(|| {
                            format!(
                                "GetParametersByPathRecursive: expected >= 2 parameters, got {}",
                                response.parameters().len()
                            )
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "ssm-path:GetParametersByPathPaginated".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let path = ctx
                        .get("ssmPathPrefix")
                        .ok_or_else(|| "ssmPathPrefix not set".to_string())?;
                    let response = clients
                        .ssm()
                        .get_parameters_by_path()
                        .path(&path)
                        .recursive(true)
                        .max_results(1)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let next_token = response.next_token().unwrap_or_default();
                    (!next_token.is_empty()).then_some(()).ok_or_else(|| {
                        "GetParametersByPathPaginated: expected NextToken to be present".to_string()
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
            "ssm-parameters".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-ssm-param", ctx.run_id.as_ref());
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name)
                        .value("v1")
                        .r#type(ParameterType::String)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("ssmParam", name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "ssm-secure".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let name = format!("{}-ssm-secure", ctx.run_id.as_ref());
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name)
                        .value("my-secret")
                        .r#type(ParameterType::SecureString)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("ssmSecureParam", name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        setups.insert(
            "ssm-path".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    // Run-scoped: a fixed path collides between concurrent
                    // runs, and overwrite(false) then fails the second one.
                    // value1 sits directly under the prefix so the
                    // non-recursive query can see it — AWS returns only direct
                    // children there — while value2 is nested for the
                    // recursive query, mirroring node-js-sdk's layout.
                    let prefix = format!("/{}/ssm-path", ctx.run_id.as_ref());
                    let name1 = format!("{}/value1", prefix);
                    let name2 = format!("{}/nested/value2", prefix);
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name1)
                        .value("v1")
                        .r#type(ParameterType::String)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    clients
                        .ssm()
                        .put_parameter()
                        .name(&name2)
                        .value("v2")
                        .r#type(ParameterType::String)
                        .overwrite(false)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("ssmPathPrefix", format!("{prefix}/"));
                    ctx.set("ssmPathParam1", name1);
                    ctx.set("ssmPathParam2", name2);
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
            "ssm-parameters".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let mut names: Vec<String> = Vec::new();
                    for key in &["ssmParam", "ssmParamNew", "ssmMultiple1", "ssmMultiple2"] {
                        if let Some(name) = ctx.get(key) {
                            names.push(name);
                        }
                    }
                    if !names.is_empty() {
                        let mut request = clients.ssm().delete_parameters();
                        for name in &names {
                            request = request.names(name);
                        }
                        let _ = request.send().await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "ssm-secure".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let mut names: Vec<String> = Vec::new();
                    for key in &["ssmSecureParam", "ssmSecureNewParam"] {
                        if let Some(name) = ctx.get(key) {
                            names.push(name);
                        }
                    }
                    if !names.is_empty() {
                        let mut request = clients.ssm().delete_parameters();
                        for name in &names {
                            request = request.names(name);
                        }
                        let _ = request.send().await;
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        teardowns.insert(
            "ssm-path".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let mut names: Vec<String> = Vec::new();
                    for key in &["ssmPathParam1", "ssmPathParam2"] {
                        if let Some(name) = ctx.get(key) {
                            names.push(name);
                        }
                    }
                    if !names.is_empty() {
                        let mut request = clients.ssm().delete_parameters();
                        for name in &names {
                            request = request.names(name);
                        }
                        let _ = request.send().await;
                    }
                    Ok(())
                })
            }),
        );

        teardowns
    }
}
