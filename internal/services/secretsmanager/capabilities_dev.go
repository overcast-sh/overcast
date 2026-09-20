//go:build dev

package secretsmanager

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		capabilities.Capability{Service: "secretsmanager", Operation: "CreateSecret", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "String + binary, KMS key, tags, description"},
		capabilities.Capability{Service: "secretsmanager", Operation: "GetSecretValue", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "By name, ARN, version ID, or stage"},
		capabilities.Capability{Service: "secretsmanager", Operation: "DescribeSecret", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "Metadata, KMS key, tags, versions, rotation dates"},
		capabilities.Capability{Service: "secretsmanager", Operation: "PutSecretValue", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "Staging labels + ClientRequestToken"},
		capabilities.Capability{Service: "secretsmanager", Operation: "UpdateSecret", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "Description, KMS key + optional new value"},
		capabilities.Capability{Service: "secretsmanager", Operation: "ListSecrets", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "Sorted by name, KMS metadata, optional filters — Filter.Key validated against AWS's enum"},
		capabilities.Capability{Service: "secretsmanager", Operation: "ListSecretVersionIds", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "All versions with staging labels"},
		capabilities.Capability{Service: "secretsmanager", Operation: "DeleteSecret", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "Recovery window 7-30 days (default 30) or ForceDeleteWithoutRecovery"},
		capabilities.Capability{Service: "secretsmanager", Operation: "BatchGetSecretValue", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "Partial results on missing secrets; Filter.Key validated against AWS's enum"},
		capabilities.Capability{Service: "secretsmanager", Operation: "RotateSecret", Category: "Rotation", Status: capabilities.StatusSupported, Notes: "Invokes the rotation Lambda, all four steps"},
		capabilities.Capability{Service: "secretsmanager", Operation: "CancelRotateSecret", Category: "Rotation", Status: capabilities.StatusSupported, Notes: "Turns rotation off, keeps the config"},
		capabilities.Capability{Service: "secretsmanager", Operation: "UpdateSecretVersionStage", Category: "Rotation", Status: capabilities.StatusSupported, Notes: "Moves staging labels between versions"},
		capabilities.Capability{Service: "secretsmanager", Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Merge/overwrite tags"},
		capabilities.Capability{Service: "secretsmanager", Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Removes specified tag keys"},
		capabilities.Capability{Service: "secretsmanager", Operation: "GetRandomPassword", Category: "Password", Status: capabilities.StatusSupported, Notes: "Modeled length bounds, exclusions, RequireEachIncludedType"},
		capabilities.Capability{Service: "secretsmanager", Operation: "GetResourcePolicy", Category: "Policy/Misc", Status: capabilities.StatusSupported, Notes: "Stored policy; not evaluated (#496)"},
		capabilities.Capability{Service: "secretsmanager", Operation: "PutResourcePolicy", Category: "Policy/Misc", Status: capabilities.StatusSupported, Notes: "Validated + stored; not evaluated (#496)"},
		capabilities.Capability{Service: "secretsmanager", Operation: "DeleteResourcePolicy", Category: "Policy/Misc", Status: capabilities.StatusSupported, Notes: "Removes the stored policy"},
		capabilities.Capability{Service: "secretsmanager", Operation: "ValidateResourcePolicy", Category: "Policy/Misc", Status: capabilities.StatusSupported, Notes: "Syntax + schema checks, no evaluation"},
		capabilities.Capability{Service: "secretsmanager", Operation: "RestoreSecret", Category: "Secret CRUD", Status: capabilities.StatusSupported, Notes: "Cancels a scheduled deletion inside the recovery window"},
		capabilities.Capability{Service: "secretsmanager", Operation: "ReplicateSecretToRegions", Category: "Policy/Misc", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "secretsmanager", Operation: "RemoveRegionsFromReplication", Category: "Policy/Misc", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
	)
}
