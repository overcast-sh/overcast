//go:build dev

package iam

import "github.com/overcast-sh/overcast/internal/capabilities"

// attachPolicyUnknownARNNote records the one place the Attach*Policy operations
// are deliberately more permissive than AWS. AWS answers NoSuchEntity when
// `PolicyArn` names a policy that does not exist; the AWS managed policies
// (`arn:aws:iam::aws:policy/…`) are not modelled here and nearly every CDK or
// Terraform stack attaches at least one, so the ARN is stored and listed back
// instead of refused.
const attachPolicyUnknownARNNote = "A `PolicyArn` that names no stored policy — an AWS managed policy such as `arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole` — is accepted and listed back under that ARN, where AWS answers `NoSuchEntity`: the AWS managed policies are not modelled"

func init() {
	capabilities.Default.Register(
		// Users
		capabilities.Capability{Service: "iam", Operation: "CreateUser", Category: "Users", Status: capabilities.StatusSupported, Notes: "Inline `Tags` applied at creation and returned on the resource"},
		capabilities.Capability{Service: "iam", Operation: "GetUser", Category: "Users", Status: capabilities.StatusSupported, Notes: "Returns the resource's `Tags`"},
		capabilities.Capability{Service: "iam", Operation: "ListUsers", Category: "Users", Status: capabilities.StatusSupported, Notes: "Returns AWS's listing subset: no `Tags` and no `PermissionsBoundary` — call `GetUser` for those"},
		capabilities.Capability{Service: "iam", Operation: "UpdateUser", Category: "Users", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "DeleteUser", Category: "Users", Status: capabilities.StatusSupported, Notes: "DeleteConflict (409) while access keys, inline or attached policies, or group memberships remain"},
		// Access keys
		capabilities.Capability{Service: "iam", Operation: "CreateAccessKey", Category: "Access keys", Status: capabilities.StatusSupported, Notes: "Generates AKIA-prefixed key + secret"},
		capabilities.Capability{Service: "iam", Operation: "ListAccessKeys", Category: "Access keys", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "DeleteAccessKey", Category: "Access keys", Status: capabilities.StatusSupported},
		// User inline policies
		capabilities.Capability{Service: "iam", Operation: "PutUserPolicy", Category: "User inline policies", Status: capabilities.StatusSupported, Notes: "Rejects a malformed document with `MalformedPolicyDocument` (400) — see `CreatePolicy` for what that check does and does not cover"},
		capabilities.Capability{Service: "iam", Operation: "GetUserPolicy", Category: "User inline policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "DeleteUserPolicy", Category: "User inline policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListUserPolicies", Category: "User inline policies", Status: capabilities.StatusSupported},
		// User managed policies
		capabilities.Capability{Service: "iam", Operation: "AttachUserPolicy", Category: "User managed policies", Status: capabilities.StatusSupported, Notes: attachPolicyUnknownARNNote},
		capabilities.Capability{Service: "iam", Operation: "DetachUserPolicy", Category: "User managed policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListAttachedUserPolicies", Category: "User managed policies", Status: capabilities.StatusSupported},
		// Permissions boundaries
		capabilities.Capability{Service: "iam", Operation: "PutUserPermissionsBoundary", Category: "Permissions boundaries", Status: capabilities.StatusSupported, Notes: "Applied by SimulatePrincipalPolicy and by opt-in enforcement; NoSuchEntity if the policy does not exist"},
		capabilities.Capability{Service: "iam", Operation: "DeleteUserPermissionsBoundary", Category: "Permissions boundaries", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "PutRolePermissionsBoundary", Category: "Permissions boundaries", Status: capabilities.StatusSupported, Notes: "Applied by SimulatePrincipalPolicy and by opt-in enforcement; NoSuchEntity if the policy does not exist"},
		capabilities.Capability{Service: "iam", Operation: "DeleteRolePermissionsBoundary", Category: "Permissions boundaries", Status: capabilities.StatusSupported},
		// User tagging
		capabilities.Capability{Service: "iam", Operation: "TagUser", Category: "User tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "UntagUser", Category: "User tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListUserTags", Category: "User tagging", Status: capabilities.StatusSupported},
		// Roles
		capabilities.Capability{Service: "iam", Operation: "CreateRole", Category: "Roles", Status: capabilities.StatusSupported, Notes: "Inline `Tags` applied at creation and returned on the role; the `AssumeRolePolicyDocument` is parsed, and a malformed one rejected with `MalformedPolicyDocument` (400) — see `CreatePolicy` for what that check does and does not cover"},
		capabilities.Capability{Service: "iam", Operation: "GetRole", Category: "Roles", Status: capabilities.StatusSupported, Notes: "Returns the resource's `Tags`"},
		capabilities.Capability{Service: "iam", Operation: "ListRoles", Category: "Roles", Status: capabilities.StatusSupported, Notes: "Returns AWS's listing subset: no `Tags` and no `PermissionsBoundary` — call `GetRole` for those"},
		capabilities.Capability{Service: "iam", Operation: "DeleteRole", Category: "Roles", Status: capabilities.StatusSupported, Notes: "DeleteConflict (409) while an instance profile association or inline/attached policies remain"},
		capabilities.Capability{Service: "iam", Operation: "UpdateRole", Category: "Roles", Status: capabilities.StatusSupported, Notes: "An empty `Description` clears it; an omitted one is left unchanged"},
		capabilities.Capability{Service: "iam", Operation: "UpdateAssumeRolePolicy", Category: "Roles", Status: capabilities.StatusSupported, Notes: "Rejects a malformed document with `MalformedPolicyDocument` (400) — see `CreatePolicy` for what that check does and does not cover"},
		capabilities.Capability{Service: "iam", Operation: "CreateServiceLinkedRole", Category: "Roles", Status: capabilities.StatusSupported},
		// Role inline policies
		capabilities.Capability{Service: "iam", Operation: "PutRolePolicy", Category: "Role inline policies", Status: capabilities.StatusSupported, Notes: "Rejects a malformed document with `MalformedPolicyDocument` (400) — see `CreatePolicy` for what that check does and does not cover"},
		capabilities.Capability{Service: "iam", Operation: "GetRolePolicy", Category: "Role inline policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListRolePolicies", Category: "Role inline policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "DeleteRolePolicy", Category: "Role inline policies", Status: capabilities.StatusSupported},
		// Role managed policies
		capabilities.Capability{Service: "iam", Operation: "AttachRolePolicy", Category: "Role managed policies", Status: capabilities.StatusSupported, Notes: attachPolicyUnknownARNNote},
		capabilities.Capability{Service: "iam", Operation: "DetachRolePolicy", Category: "Role managed policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListAttachedRolePolicies", Category: "Role managed policies", Status: capabilities.StatusSupported},
		// Role tagging
		capabilities.Capability{Service: "iam", Operation: "TagRole", Category: "Role tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "UntagRole", Category: "Role tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListRoleTags", Category: "Role tagging", Status: capabilities.StatusSupported},
		// Managed policy tagging
		capabilities.Capability{Service: "iam", Operation: "TagPolicy", Category: "Managed policy tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "UntagPolicy", Category: "Managed policy tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListPolicyTags", Category: "Managed policy tagging", Status: capabilities.StatusSupported},
		// Instance profile tagging
		capabilities.Capability{Service: "iam", Operation: "TagInstanceProfile", Category: "Instance profile tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "UntagInstanceProfile", Category: "Instance profile tagging", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListInstanceProfileTags", Category: "Instance profile tagging", Status: capabilities.StatusSupported},
		// Instance profiles
		capabilities.Capability{Service: "iam", Operation: "CreateInstanceProfile", Category: "Instance profiles", Status: capabilities.StatusSupported, Notes: "Inline `Tags` applied at creation and returned on the resource"},
		capabilities.Capability{Service: "iam", Operation: "GetInstanceProfile", Category: "Instance profiles", Status: capabilities.StatusSupported, Notes: "Returns the resource's `Tags`"},
		capabilities.Capability{Service: "iam", Operation: "DeleteInstanceProfile", Category: "Instance profiles", Status: capabilities.StatusSupported, Notes: "DeleteConflict (409) while a role association remains — `RemoveRoleFromInstanceProfile` first"},
		capabilities.Capability{Service: "iam", Operation: "AddRoleToInstanceProfile", Category: "Instance profiles", Status: capabilities.StatusSupported, Notes: "An instance profile holds at most one role, AWS's quota: a second, different role is refused with `LimitExceeded` (409). Re-adding the role already there is a no-op; replacing it means `RemoveRoleFromInstanceProfile` first"},
		capabilities.Capability{Service: "iam", Operation: "RemoveRoleFromInstanceProfile", Category: "Instance profiles", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListInstanceProfiles", Category: "Instance profiles", Status: capabilities.StatusSupported, Notes: "Returns AWS's listing subset: no `Tags` — call the matching `Get` for those"},
		capabilities.Capability{Service: "iam", Operation: "ListInstanceProfilesForRole", Category: "Instance profiles", Status: capabilities.StatusSupported},
		// Managed policies
		capabilities.Capability{Service: "iam", Operation: "CreatePolicy", Category: "Managed policies", Status: capabilities.StatusSupported, Notes: "Inline `Tags` applied at creation and returned on the policy. The `PolicyDocument` is parsed before it is stored and a malformed one is rejected with `MalformedPolicyDocument` (400): it must be valid JSON, a JSON object, carry a `Statement` that is an object or a non-empty array, and give every statement an `Effect` of exactly `Allow` or `Deny` plus an `Action` or `NotAction` (not both) — with `Resource`/`NotResource` mutually exclusive too, and `Version`, when present, one of `2012-10-17` or `2008-10-17`. Deliberately **not** checked: ARN syntax in `Resource`/`Principal`, whether an action name exists, condition-operator and condition-key names, `Sid` uniqueness and character set, `Principal` appearing in an identity policy or being absent from a trust policy, `Resource` being absent where AWS requires it, the 6,144-character document size limit, and an omitted or empty document, which is left to the operation's own required-parameter handling rather than reported as malformed"},
		capabilities.Capability{Service: "iam", Operation: "GetPolicy", Category: "Managed policies", Status: capabilities.StatusSupported, Notes: "Returns the policy's `Tags`. `AttachmentCount` and `PermissionsBoundaryUsageCount` are derived from the users, groups and roles that refer to the policy, so they follow attach/detach and boundary changes; `IsAttachable` is always true, as every policy here is a customer managed one"},
		capabilities.Capability{Service: "iam", Operation: "ListPolicies", Category: "Managed policies", Status: capabilities.StatusSupported, Notes: "Returns AWS's listing subset: no `Tags` and no `Description` — call `GetPolicy` for those. Carries the same derived `AttachmentCount` and `PermissionsBoundaryUsageCount` as `GetPolicy`"},
		capabilities.Capability{Service: "iam", Operation: "DeletePolicy", Category: "Managed policies", Status: capabilities.StatusSupported, Notes: "DeleteConflict (409) while the policy is attached to any user, role or group, or used as one of their permissions boundaries"},
		capabilities.Capability{Service: "iam", Operation: "CreatePolicyVersion", Category: "Managed policies", Status: capabilities.StatusPartial, Notes: "`SetAsDefault=true` replaces the operative document and bumps `DefaultVersionId`; superseded versions are not retained and cannot be read back. Rejects a malformed document with `MalformedPolicyDocument` (400) — see `CreatePolicy` for what that check does and does not cover"},
		// Groups
		capabilities.Capability{Service: "iam", Operation: "CreateGroup", Category: "Groups", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "GetGroup", Category: "Groups", Status: capabilities.StatusSupported, Notes: "Returns the group's members, paginated with Marker/MaxItems (default 100, max 1000)"},
		capabilities.Capability{Service: "iam", Operation: "DeleteGroup", Category: "Groups", Status: capabilities.StatusSupported, Notes: "DeleteConflict (409) while members or inline/attached policies remain"},
		capabilities.Capability{Service: "iam", Operation: "ListGroups", Category: "Groups", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "AddUserToGroup", Category: "Groups", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "RemoveUserFromGroup", Category: "Groups", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListGroupsForUser", Category: "Groups", Status: capabilities.StatusSupported},
		// Group inline policies
		capabilities.Capability{Service: "iam", Operation: "PutGroupPolicy", Category: "Group inline policies", Status: capabilities.StatusSupported, Notes: "Rejects a malformed document with `MalformedPolicyDocument` (400) — see `CreatePolicy` for what that check does and does not cover"},
		capabilities.Capability{Service: "iam", Operation: "GetGroupPolicy", Category: "Group inline policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "DeleteGroupPolicy", Category: "Group inline policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListGroupPolicies", Category: "Group inline policies", Status: capabilities.StatusSupported},
		// Group managed policies
		capabilities.Capability{Service: "iam", Operation: "AttachGroupPolicy", Category: "Group managed policies", Status: capabilities.StatusSupported, Notes: attachPolicyUnknownARNNote},
		capabilities.Capability{Service: "iam", Operation: "DetachGroupPolicy", Category: "Group managed policies", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "iam", Operation: "ListAttachedGroupPolicies", Category: "Group managed policies", Status: capabilities.StatusSupported},
		// Policy simulation
		capabilities.Capability{Service: "iam", Operation: "SimulatePrincipalPolicy", Category: "Policy simulation", Status: capabilities.StatusSupported, Notes: "Real evaluation of the principal's identity policies (plus an optional ResourcePolicy and permissions boundary): allowed / explicitDeny / implicitDeny with MatchedStatements and MissingContextValues"},
		capabilities.Capability{Service: "iam", Operation: "SimulateCustomPolicy", Category: "Policy simulation", Status: capabilities.StatusSupported, Notes: "Evaluates the supplied PolicyInputList without touching any stored entity"},
		// Account details
		capabilities.Capability{Service: "iam", Operation: "GetAccountAuthorizationDetails", Category: "Account details", Status: capabilities.StatusSupported, Notes: "Returns all users, groups, roles, and managed policies in one call"},
	)
}
