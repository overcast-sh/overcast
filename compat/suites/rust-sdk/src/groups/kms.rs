use std::collections::HashMap;
use std::sync::Arc;

use aws_sdk_kms::primitives::Blob;
use aws_sdk_kms::types::{DataKeySpec, KeySpec, KeyUsageType, SigningAlgorithmSpec, Tag};

use crate::clients::AwsClients;
use crate::groups::ServiceGroup;
use crate::harness::{TestContext, TestFn};

pub struct KmsGroup {
    clients: Arc<AwsClients>,
}

impl KmsGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for KmsGroup {
    fn name(&self) -> &'static str {
        "kms"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        // ── kms-keys ────────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:CreateKey".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let response = clients
                        .kms()
                        .create_key()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let metadata = response
                        .key_metadata()
                        .ok_or_else(|| "CreateKey: key_metadata missing".to_string())?;
                    let key_id = metadata.key_id();
                    let arn = metadata.arn().unwrap_or_default();
                    if key_id.is_empty() {
                        return Err("CreateKey: key_id is empty".to_string());
                    }
                    if arn.is_empty() {
                        return Err("CreateKey: arn is empty".to_string());
                    }
                    ctx.set("keyId", key_id.to_string());
                    ctx.set("keyArn", arn.to_string());
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:DescribeKey".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    let response = clients
                        .kms()
                        .describe_key()
                        .key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let metadata = response
                        .key_metadata()
                        .ok_or_else(|| "DescribeKey: key_metadata missing".to_string())?;
                    let returned_id = metadata.key_id();
                    (returned_id == key_id).then_some(()).ok_or_else(|| {
                        format!("DescribeKey: expected key_id {key_id}, got {returned_id}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:PutKeyPolicy".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    let initial = clients
                        .kms()
                        .get_key_policy()
                        .key_id(&key_id)
                        .policy_name("default")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let policy = initial
                        .policy()
                        .ok_or_else(|| "PutKeyPolicy: initial policy missing".to_string())?;
                    clients
                        .kms()
                        .put_key_policy()
                        .key_id(&key_id)
                        .policy_name("default")
                        .policy(policy)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let got = clients
                        .kms()
                        .get_key_policy()
                        .key_id(&key_id)
                        .policy_name("default")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    (got.policy() == Some(policy))
                        .then_some(())
                        .ok_or_else(|| "PutKeyPolicy: stored policy mismatch".to_string())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:CreateKmsAlias".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    let alias_name = format!("alias/overcast-compat-key-{}", ctx.run_id.as_ref());
                    clients
                        .kms()
                        .create_alias()
                        .alias_name(&alias_name)
                        .target_key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    ctx.set("aliasName", alias_name);
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:ListKmsAliases".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    let alias_name = ctx
                        .get("aliasName")
                        .ok_or_else(|| "aliasName not set".to_string())?;
                    let response = clients
                        .kms()
                        .list_aliases()
                        .key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .aliases()
                        .iter()
                        .any(|a| a.alias_name().unwrap_or_default() == alias_name);
                    found
                        .then_some(())
                        .ok_or_else(|| format!("ListKmsAliases: alias {alias_name} not found"))
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:ListKeys".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    let response = clients
                        .kms()
                        .list_keys()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let found = response
                        .keys()
                        .iter()
                        .any(|k| k.key_id().unwrap_or_default() == key_id);
                    found
                        .then_some(())
                        .ok_or_else(|| format!("ListKeys: key {key_id} not found"))
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:DisableKey".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    clients
                        .kms()
                        .disable_key()
                        .key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:EnableKey".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    clients
                        .kms()
                        .enable_key()
                        .key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:ScheduleKeyDeletion".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    clients
                        .kms()
                        .schedule_key_deletion()
                        .key_id(&key_id)
                        .pending_window_in_days(7)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:CancelKeyDeletion".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    clients
                        .kms()
                        .cancel_key_deletion()
                        .key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:TagKMSResource".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    let tag_project = Tag::builder()
                        .tag_key("project")
                        .tag_value("overcast")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    let tag_env = Tag::builder()
                        .tag_key("env")
                        .tag_value("test")
                        .build()
                        .map_err(crate::harness::sdk_error_message)?;
                    clients
                        .kms()
                        .tag_resource()
                        .key_id(&key_id)
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
            "kms-keys:ListKMSResourceTags".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    let response = clients
                        .kms()
                        .list_resource_tags()
                        .key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let tags = response.tags();
                    let project = tags
                        .iter()
                        .find(|t| t.tag_key() == "project")
                        .map(|t| t.tag_value())
                        .unwrap_or_default();
                    (project == "overcast").then_some(()).ok_or_else(|| {
                        format!("ListKMSResourceTags: expected project=overcast tag, got {project}")
                    })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-keys:UntagKMSResource".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("keyId")
                        .ok_or_else(|| "keyId not set".to_string())?;
                    clients
                        .kms()
                        .untag_resource()
                        .key_id(&key_id)
                        .tag_keys("env")
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let response = clients
                        .kms()
                        .list_resource_tags()
                        .key_id(&key_id)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let has_env = response.tags().iter().any(|t| t.tag_key() == "env");
                    (!has_env)
                        .then_some(())
                        .ok_or_else(|| "UntagKMSResource: env tag should be absent".to_string())
                })
            }),
        );

        // ── kms-crypto ───────────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "kms-crypto:Encrypt".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("cryptoKeyId")
                        .ok_or_else(|| "cryptoKeyId not set".to_string())?;
                    let response = clients
                        .kms()
                        .encrypt()
                        .key_id(&key_id)
                        .plaintext(Blob::new(b"hello"))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let ciphertext = response
                        .ciphertext_blob()
                        .ok_or_else(|| "Encrypt: ciphertext_blob missing".to_string())?;
                    if ciphertext.as_ref().is_empty() {
                        return Err("Encrypt: ciphertext_blob is empty".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-crypto:Decrypt".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("cryptoKeyId")
                        .ok_or_else(|| "cryptoKeyId not set".to_string())?;
                    let encrypt_response = clients
                        .kms()
                        .encrypt()
                        .key_id(&key_id)
                        .plaintext(Blob::new(b"hello"))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let ciphertext = encrypt_response
                        .ciphertext_blob()
                        .ok_or_else(|| "Decrypt: ciphertext_blob from encrypt missing".to_string())?
                        .clone();
                    let decrypt_response = clients
                        .kms()
                        .decrypt()
                        .key_id(&key_id)
                        .ciphertext_blob(ciphertext)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let plaintext = decrypt_response
                        .plaintext()
                        .ok_or_else(|| "Decrypt: plaintext missing".to_string())?;
                    (plaintext.as_ref() == b"hello")
                        .then_some(())
                        .ok_or_else(|| {
                            format!("Decrypt: expected 'hello', got {:?}", plaintext.as_ref())
                        })
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-crypto:GenerateDataKey".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("cryptoKeyId")
                        .ok_or_else(|| "cryptoKeyId not set".to_string())?;
                    let response = clients
                        .kms()
                        .generate_data_key()
                        .key_id(&key_id)
                        .key_spec(DataKeySpec::Aes256)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let ciphertext = response
                        .ciphertext_blob()
                        .ok_or_else(|| "GenerateDataKey: ciphertext_blob missing".to_string())?;
                    let plaintext = response
                        .plaintext()
                        .ok_or_else(|| "GenerateDataKey: plaintext missing".to_string())?;
                    if ciphertext.as_ref().is_empty() {
                        return Err("GenerateDataKey: ciphertext_blob is empty".to_string());
                    }
                    if plaintext.as_ref().is_empty() {
                        return Err("GenerateDataKey: plaintext is empty".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-crypto:GenerateDataKeyWithoutPlaintext".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("cryptoKeyId")
                        .ok_or_else(|| "cryptoKeyId not set".to_string())?;
                    let response = clients
                        .kms()
                        .generate_data_key_without_plaintext()
                        .key_id(&key_id)
                        .key_spec(DataKeySpec::Aes256)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let ciphertext = response.ciphertext_blob().ok_or_else(|| {
                        "GenerateDataKeyWithoutPlaintext: ciphertext_blob missing".to_string()
                    })?;
                    if ciphertext.as_ref().is_empty() {
                        return Err(
                            "GenerateDataKeyWithoutPlaintext: ciphertext_blob is empty".to_string()
                        );
                    }
                    Ok(())
                })
            }),
        );

        // ── kms-asymmetric ───────────────────────────────────────────────

        let clients = self.clients.clone();
        impls.insert(
            "kms-asymmetric:Sign".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("asymKeyId")
                        .ok_or_else(|| "asymKeyId not set".to_string())?;
                    let response = clients
                        .kms()
                        .sign()
                        .key_id(&key_id)
                        .message(Blob::new(b"data"))
                        .signing_algorithm(SigningAlgorithmSpec::RsassaPkcs1V15Sha256)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let signature = response
                        .signature()
                        .ok_or_else(|| "Sign: signature missing".to_string())?;
                    if signature.as_ref().is_empty() {
                        return Err("Sign: signature is empty".to_string());
                    }
                    Ok(())
                })
            }),
        );

        let clients = self.clients.clone();
        impls.insert(
            "kms-asymmetric:Verify".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let key_id = ctx
                        .get("asymKeyId")
                        .ok_or_else(|| "asymKeyId not set".to_string())?;
                    let sign_response = clients
                        .kms()
                        .sign()
                        .key_id(&key_id)
                        .message(Blob::new(b"data"))
                        .signing_algorithm(SigningAlgorithmSpec::RsassaPkcs1V15Sha256)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let signature = sign_response
                        .signature()
                        .ok_or_else(|| "Verify: signature from sign missing".to_string())?
                        .clone();
                    let verify_response = clients
                        .kms()
                        .verify()
                        .key_id(&key_id)
                        .message(Blob::new(b"data"))
                        .signature(signature)
                        .signing_algorithm(SigningAlgorithmSpec::RsassaPkcs1V15Sha256)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let valid = verify_response.signature_valid();
                    valid
                        .then_some(())
                        .ok_or_else(|| "Verify: signature should be valid".to_string())
                })
            }),
        );

        impls
    }

    fn setups(&self) -> HashMap<String, TestFn> {
        let mut setups: HashMap<String, TestFn> = HashMap::new();

        // ── kms-keys setup ───────────────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "kms-keys".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let response = clients
                        .kms()
                        .create_key()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let metadata = response
                        .key_metadata()
                        .ok_or_else(|| "setup(kms-keys): key_metadata missing".to_string())?;
                    let key_id = metadata.key_id();
                    let arn = metadata.arn().unwrap_or_default();
                    if key_id.is_empty() {
                        return Err("setup(kms-keys): key_id is empty".to_string());
                    }
                    ctx.set("keyId", key_id.to_string());
                    ctx.set("keyArn", arn.to_string());
                    Ok(())
                })
            }),
        );

        // ── kms-crypto setup ─────────────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "kms-crypto".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let response = clients
                        .kms()
                        .create_key()
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let metadata = response
                        .key_metadata()
                        .ok_or_else(|| "setup(kms-crypto): key_metadata missing".to_string())?;
                    let key_id = metadata.key_id();
                    if key_id.is_empty() {
                        return Err("setup(kms-crypto): key_id is empty".to_string());
                    }
                    ctx.set("cryptoKeyId", key_id.to_string());
                    Ok(())
                })
            }),
        );

        // ── kms-asymmetric setup ─────────────────────────────────────────

        let clients = self.clients.clone();
        setups.insert(
            "kms-asymmetric".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let response = clients
                        .kms()
                        .create_key()
                        .key_spec(KeySpec::Rsa2048)
                        .key_usage(KeyUsageType::SignVerify)
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    let metadata = response
                        .key_metadata()
                        .ok_or_else(|| "setup(kms-asymmetric): key_metadata missing".to_string())?;
                    let key_id = metadata.key_id();
                    if key_id.is_empty() {
                        return Err("setup(kms-asymmetric): key_id is empty".to_string());
                    }
                    ctx.set("asymKeyId", key_id.to_string());
                    Ok(())
                })
            }),
        );

        setups
    }

    fn teardowns(&self) -> HashMap<String, TestFn> {
        let mut teardowns: HashMap<String, TestFn> = HashMap::new();

        // ── kms-keys teardown ────────────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "kms-keys".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(key_id) = ctx.get("keyId") {
                        if let Some(alias_name) = ctx.get("aliasName") {
                            let _ = clients
                                .kms()
                                .delete_alias()
                                .alias_name(&alias_name)
                                .send()
                                .await;
                        }
                        let _ = clients
                            .kms()
                            .schedule_key_deletion()
                            .key_id(&key_id)
                            .pending_window_in_days(7)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        // ── kms-crypto teardown ──────────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "kms-crypto".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(key_id) = ctx.get("cryptoKeyId") {
                        let _ = clients
                            .kms()
                            .schedule_key_deletion()
                            .key_id(&key_id)
                            .pending_window_in_days(7)
                            .send()
                            .await;
                    }
                    Ok(())
                })
            }),
        );

        // ── kms-asymmetric teardown ──────────────────────────────────────

        let clients = self.clients.clone();
        teardowns.insert(
            "kms-asymmetric".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    if let Some(key_id) = ctx.get("asymKeyId") {
                        let _ = clients
                            .kms()
                            .schedule_key_deletion()
                            .key_id(&key_id)
                            .pending_window_in_days(7)
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
