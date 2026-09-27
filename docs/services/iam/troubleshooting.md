---
title: "IAM troubleshooting"
description: "Symptom, cause and fix for the IAM errors that stop a local run: DeleteConflict on teardown, DELETE_FAILED stacks, and AccessDenied once enforcement is on."
section: "Service Reference"
tags:
  - docs
  - iam
  - services
  - troubleshooting
---

# IAM troubleshooting

Symptom, cause and fix for the errors that stop a local run behind
[IAM](../iam.md).

## `DeleteConflict` (409) deleting a user, role, group or policy

**Cause.** The entity still has dependencies. Overcast refuses the delete with
AWS's own message naming what to clear — the behaviour Terraform, CDK,
`aws-nuke` and eksctl already expect from real IAM.

| Delete         | Refused while                                                                       | Message                                                            |
| -------------- | ------------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| `DeleteUser`   | access keys exist                                                                   | `Cannot delete entity, must delete access keys first.`             |
|                | inline policies exist                                                               | `Cannot delete entity, must delete policies first.`                |
|                | managed policies are attached                                                       | `Cannot delete entity, must detach all policies first.`            |
|                | the user is in a group                                                              | `Cannot delete entity, must remove users from group first.`        |
| `DeleteRole`   | the role is in an instance profile                                                  | `Cannot delete entity, must remove roles from instance profile first.` |
|                | inline policies exist                                                               | `Cannot delete entity, must delete policies first.`                |
|                | managed policies are attached                                                       | `Cannot delete entity, must detach all policies first.`            |
| `DeleteGroup`  | the group has members                                                               | `Cannot delete entity, must remove users from group first.`        |
|                | inline policies exist                                                               | `Cannot delete entity, must delete policies first.`                |
|                | managed policies are attached                                                       | `Cannot delete entity, must detach all policies first.`            |
| `DeletePolicy` | attached to any user, role or group, or used as one of their permissions boundaries | `Cannot delete a policy attached to entities.`                     |

**Fix.** Remove the dependency through the modelled API —
`DeleteAccessKey`, `DeleteUserPolicy`, `DetachUserPolicy`,
`RemoveUserFromGroup`, `RemoveRoleFromInstanceProfile`, `DeleteRolePolicy`,
`DetachRolePolicy`, `DeleteGroupPolicy`, `DetachGroupPolicy` — exactly as you
would against AWS, then delete again.

When several dependencies block at once, the checks run in the order above,
which is the order AWS's own API Reference lists the prerequisites in. AWS does
not document which one wins, so clearing them top to bottom is what to expect.
A non-existent entity is still `NoSuchEntity` (404): existence is checked
before dependencies.

## `DELETE_FAILED` tearing down a CloudFormation stack

**Cause.** A `DeleteConflict` the stack cannot clear for itself — something
outside the stack attached a policy, minted an access key, or added the user to
a group. `DeleteStack` reports `DELETE_FAILED` with IAM's own message on the
resource's event and status reason, and leaves the entity standing, as real
CloudFormation does.

**Fix.** Clear the dependency, then delete the stack again.

A stack handles the dependencies it owns itself: `AWS::IAM::Policy` removes its
inline document from the entities it named; roles, users, groups and managed
policies detach the `ManagedPolicyArns` / `Policies` / `Groups` / attachment
relationships their own template properties declared; and reverse dependency
order puts instance profiles before their roles. See
[CloudFormation § Teardown failure](../cloudformation.md).

## `AccessDenied` after switching enforcement on

**Cause.** The error message names the principal, the action and the resource
that were checked, and whether a `Deny` statement or the lack of an `Allow`
refused them. Either the policy genuinely does not allow that action, or it
names an action Overcast does not check the call as. The action is the one AWS
documents, which is not always named after the operation or the service key:
S3's `ListObjectsV2` is `s3:ListBucket`, and Step Functions uses `states:` — see
[IAM actions](./actions.md).

Enforcement is fail-closed, so an unsigned request, an unparseable policy, or a
policy construct the evaluator does not implement also denies. The reason is
logged at debug level.

**Fix.** Run the same call through `SimulatePrincipalPolicy` — it uses the same
evaluator, works with enforcement off, and `MatchedStatements` names the
statement that decided it. `MissingContextValues` names condition keys the call
did not supply. If the policy is an AWS-managed ARN, note that those documents
are not stored here and grant nothing.

## Related

- [IAM](../iam.md) — quick start and what works
- [IAM actions](./actions.md) — which action a request is checked as
- [IAM limitations](./limitations.md) — what the policy evaluator covers
- [IAM operations](./operations.md) — per-operation status
