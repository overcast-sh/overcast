//go:build dev

package kms

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		capabilities.Capability{Service: "kms", Operation: "CreateKey", Category: "Key lifecycle", Status: capabilities.StatusPartial, Notes: "Symmetric and RSA key specs; validates caller-safe custom policies unless bypassed; accepts `Tags`; rejects `Origin` other than `AWS_KMS` and `MultiRegion=true` (not emulated)"},
		capabilities.Capability{Service: "kms", Operation: "DescribeKey", Category: "Key lifecycle", Status: capabilities.StatusSupported, Notes: "Lookup by UUID, ARN, or alias"},
		capabilities.Capability{Service: "kms", Operation: "ListKeys", Category: "Key lifecycle", Status: capabilities.StatusSupported, Notes: "Excludes `PendingDeletion` keys; no pagination (Truncated=false)"},
		capabilities.Capability{Service: "kms", Operation: "EnableKey", Category: "Key lifecycle", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "kms", Operation: "DisableKey", Category: "Key lifecycle", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "kms", Operation: "UpdateKeyDescription", Category: "Key lifecycle", Status: capabilities.StatusSupported, Notes: "Also dispatched by CloudFormation when AWS::KMS::Key Description changes"},
		capabilities.Capability{Service: "kms", Operation: "ScheduleKeyDeletion", Category: "Key lifecycle", Status: capabilities.StatusSupported, Notes: "`PendingWindowInDays` 7-30, defaulting to 30 and returned in the response; response `KeyId` is the key ARN"},
		capabilities.Capability{Service: "kms", Operation: "CancelKeyDeletion", Category: "Key lifecycle", Status: capabilities.StatusSupported, Notes: "Restores key to `Disabled` state; response `KeyId` is the key ARN"},

		capabilities.Capability{Service: "kms", Operation: "CreateAlias", Category: "Aliases", Status: capabilities.StatusSupported, Notes: "`alias/` prefix required; reserved `alias/aws/` and duplicate names rejected"},
		capabilities.Capability{Service: "kms", Operation: "DeleteAlias", Category: "Aliases", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "kms", Operation: "ListAliases", Category: "Aliases", Status: capabilities.StatusSupported, Notes: "Optional `KeyId` filter (UUID, ARN, alias)"},
		capabilities.Capability{Service: "kms", Operation: "UpdateAlias", Category: "Aliases", Status: capabilities.StatusSupported, Notes: "Updates target key for an existing alias"},

		capabilities.Capability{Service: "kms", Operation: "Encrypt", Category: "Symmetric crypto", Status: capabilities.StatusSupported, Notes: "AES-256-GCM; `Plaintext` capped at 4096 bytes; ciphertext envelope includes key ID"},
		capabilities.Capability{Service: "kms", Operation: "Decrypt", Category: "Symmetric crypto", Status: capabilities.StatusSupported, Notes: "Extracts key ID from ciphertext envelope; a `KeyId` naming a different key is an `IncorrectKeyException`"},
		capabilities.Capability{Service: "kms", Operation: "GenerateDataKey", Category: "Symmetric crypto", Status: capabilities.StatusSupported, Notes: "Exactly one of `KeySpec` (`AES_256`/`AES_128`) or `NumberOfBytes` (1-1024); returns plaintext + encrypted"},
		capabilities.Capability{Service: "kms", Operation: "GenerateDataKeyWithoutPlaintext", Category: "Symmetric crypto", Status: capabilities.StatusSupported, Notes: "Same `KeySpec`/`NumberOfBytes` rules as `GenerateDataKey`; returns encrypted data key only"},
		capabilities.Capability{Service: "kms", Operation: "GenerateRandom", Category: "Symmetric crypto", Status: capabilities.StatusPartial, Notes: "`NumberOfBytes` (1-1024) required; `CustomKeyStoreId` and `Recipient` are ignored (not emulated)"},
		capabilities.Capability{Service: "kms", Operation: "ReEncrypt", Category: "Symmetric crypto", Status: capabilities.StatusSupported, Notes: "Decrypts and re-encrypts ciphertext with destination key; a `SourceKeyId` naming a different key is an `IncorrectKeyException`"},
		capabilities.Capability{Service: "kms", Operation: "GenerateDataKeyPair", Category: "Symmetric crypto", Status: capabilities.StatusSupported, Notes: "RSA_2048, RSA_3072, RSA_4096 key pair specs"},

		capabilities.Capability{Service: "kms", Operation: "Sign", Category: "Asymmetric crypto", Status: capabilities.StatusSupported, Notes: "RSA_2048 keys with all six RSASSA_PSS_* / RSASSA_PKCS1_V1_5_* algorithms, RAW or DIGEST messages; other signing key specs are refused"},
		capabilities.Capability{Service: "kms", Operation: "Verify", Category: "Asymmetric crypto", Status: capabilities.StatusSupported, Notes: "RSA_2048 keys, every RSASSA algorithm; a signature that does not verify fails with KMSInvalidSignatureException"},
		capabilities.Capability{Service: "kms", Operation: "GetPublicKey", Category: "Asymmetric crypto", Status: capabilities.StatusSupported, Notes: "Returns DER-encoded public key for RSA keys"},
		capabilities.Capability{Service: "kms", Operation: "VerifyMac", Category: "Asymmetric crypto", Status: capabilities.StatusSupported, Notes: "HMAC_SHA_256, HMAC_SHA_384, HMAC_SHA_512"},

		capabilities.Capability{Service: "kms", Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Add tags to a KMS key"},
		capabilities.Capability{Service: "kms", Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Remove tags from a KMS key"},
		capabilities.Capability{Service: "kms", Operation: "ListResourceTags", Category: "Tags", Status: capabilities.StatusSupported, Notes: "List tags for a KMS key"},

		capabilities.Capability{Service: "kms", Operation: "GetKeyPolicy", Category: "Key policies", Status: capabilities.StatusSupported, Notes: "Returns default or custom key policy"},
		capabilities.Capability{Service: "kms", Operation: "PutKeyPolicy", Category: "Key policies", Status: capabilities.StatusSupported, Notes: "Validates policy structure, principals, and caller lockout safety before mutation"},
		capabilities.Capability{Service: "kms", Operation: "ListKeyPolicies", Category: "Key policies", Status: capabilities.StatusSupported, Notes: "Returns list of policy names"},

		capabilities.Capability{Service: "kms", Operation: "CreateGrant", Category: "Grants", Status: capabilities.StatusSupported, Notes: "Creates a grant with optional constraints and retiring principal"},
		capabilities.Capability{Service: "kms", Operation: "ListGrants", Category: "Grants", Status: capabilities.StatusSupported, Notes: "Lists grants with optional KeyId, GrantId, and GranteePrincipal filters"},
		capabilities.Capability{Service: "kms", Operation: "RevokeGrant", Category: "Grants", Status: capabilities.StatusSupported, Notes: "Revokes a grant by ID"},
		capabilities.Capability{Service: "kms", Operation: "RetireGrant", Category: "Grants", Status: capabilities.StatusSupported, Notes: "Retires a grant by ID or token"},
		capabilities.Capability{Service: "kms", Operation: "ListRetirableGrants", Category: "Grants", Status: capabilities.StatusSupported, Notes: "Lists grants retirable by a principal"},
	)
}
