---
title: "IAM limitations"
description: "What the IAM policy evaluator covers and what it refuses to guess at, how permissions boundaries behave, and what request-time enforcement does not see."
section: "Service Reference"
tags:
  - docs
  - iam
  - limitations
  - services
---

# IAM limitations

What the policy evaluator covers and where it stops, behind the summary on
[IAM](../iam.md).

## What the evaluator covers

| Construct        | Supported                                                                        |
| ---------------- | ---------------------------------------------------------------------------------- |
| Core             | `Effect`, `Action`/`NotAction`, `Resource`/`NotResource`, with `*` and `?` wildcards |
| Matching         | Actions case-insensitively, as on AWS; resource ARNs case-sensitively               |
| Precedence       | Explicit deny wins, then allow, otherwise the default implicit deny                 |
| Conditions       | The `String*`, `Numeric*`, `Date*`, `Bool`, `IpAddress`/`NotIpAddress`, `Arn*` and `Null` families, including the `…IfExists` suffix |
| Policy variables | `${aws:username}`, `${aws:userid}`, … in resources and condition values             |
| Resource policies | Passed as `ResourcePolicy` to the simulator, including `Principal`/`NotPrincipal`  |

Within the single account Overcast emulates, an allow in either the identity
policies or the resource policy is sufficient, and a deny in either is final.

## Policy documents are checked at the API boundary

Every operation that takes a document — `CreatePolicy`, `CreatePolicyVersion`,
`CreateRole`, `UpdateAssumeRolePolicy`, `PutRolePolicy`, `PutUserPolicy`,
`PutGroupPolicy` — parses it first and refuses a malformed one with
`MalformedPolicyDocument` (400), as AWS does. The check is structural, and it
is the same parser the evaluator uses, so a document `CreatePolicy` accepts is
one enforcement can read.

| Checked                                        | Rule                                 |
| ---------------------------------------------- | -------------------------------------- |
| The document                                   | Valid JSON, and a JSON object          |
| `Statement`                                    | Present, and an object or a non-empty array |
| `Effect`                                       | Exactly `Allow` or `Deny` — AWS is case sensitive here |
| `Action`                                       | One of `Action` or `NotAction`, never both |
| `Resource`                                     | `Resource` and `NotResource` are mutually exclusive |
| `Version`                                      | When present, `2012-10-17` or `2008-10-17` |

Deeper checks are deliberately absent rather than half-implemented, so a
document can still be nonsense in ways AWS would catch:

- ARN syntax in `Resource`, `NotResource` and `Principal`
- whether an action name or condition-operator name is real
- `Sid` uniqueness and character set
- a `Principal` in an identity policy, or a trust policy without one — AWS
  refuses both, this does not
- a missing `Resource` where AWS requires one
- the 6,144-character document size limit
- an omitted or empty document. AWS answers a `ValidationError` on the
  parameter's minimum length; here only `CreatePolicyVersion` requires the
  parameter and the other writers still store an empty document

## What it will not guess

A condition operator or principal type the evaluator does not implement makes
the call fail with AWS's `PolicyEvaluation` error naming the construct, rather
than quietly resolving to an allow or a deny. `MissingContextValues` names
condition keys the call did not supply.

| Not implemented                             | Result                                    |
| ------------------------------------------- | ------------------------------------------- |
| Service control policies, session policies  | Not evaluated                              |
| `ForAllValues` / `ForAnyValue` set operators | `PolicyEvaluation`                         |
| An unparseable policy document              | `InvalidInput`                             |
| AWS-managed policy documents                | Not stored, so an attached `arn:aws:iam::aws:policy/…` grants nothing under enforcement |

Because those policies are not stored, `AttachRolePolicy`, `AttachUserPolicy`
and `AttachGroupPolicy` take any `PolicyArn` and list it back rather than
answering `NoSuchEntity` as AWS would for an ARN that names nothing. Every CDK
and Terraform stack attaches at least one AWS managed policy, so refusing them
would refuse the common case.

`StartPosition` / `EndPosition` are Overcast's own byte-accurate computation
against the document text it was given. They are not copied from any upstream
source, and will not match real AWS byte for byte.

## Permissions boundaries

A boundary grants nothing on its own: it caps what the entity's identity
policies can grant, so effective permissions are the **intersection** of the
two, and an explicit deny in either is final. Both `SimulatePrincipalPolicy`
and request-time enforcement read the stored boundary, so one attached out of
band takes effect on the very next call.

Supplying `PermissionsBoundaryPolicyInputList` to `SimulatePrincipalPolicy`
uses that boundary *instead of* the stored one; AWS allows only one boundary
per simulation.

| Case                                            | Behaviour                                                     |
| ----------------------------------------------- | --------------------------------------------------------------- |
| Boundary naming a policy that does not exist    | `NoSuchEntity`                                                 |
| `DeletePolicy` while a policy is still bounding | `DeleteConflict`                                               |
| Boundary attached but unreadable                | Allows nothing, and the reason is logged at warn level          |
| Boundary plus a simulated `ResourcePolicy`      | The boundary still applies — AWS would let a direct-principal resource policy bypass it |

That last row is the one divergence, and it needs a simulation supplying a
`ResourcePolicy` *and* a principal carrying a boundary to appear at all.

## What enforcement does not see

`OVERCAST_ENFORCE_IAM` gates identity policies only. Two further gaps:

- **Resource-based policies** — S3 bucket policies, Lambda/SQS/SNS policies —
  are not consulted at request time. The simulator accepts one explicitly,
  which is the way to test one today.
- **A request whose operation cannot be named** is not gated. S3 reaches this
  routinely, because its sub-resource operations (`?tagging`, `?restore`,
  `?legal-hold`, …) are identified by query parameters rather than by path.
  Denying them would break ordinary S3 traffic the moment enforcement was
  switched on. The gap is logged at debug level rather than passing silently.

Enforcement decides only what this evaluator can see. It is a development aid
for catching a missing permission early, not a security control.

## Entities that are not modelled

Login profiles, signing certificates, SSH public keys, Git credentials and MFA
devices do not exist here, so the AWS delete conflicts for those cannot arise.
Managed policy *versions* are a counter rather than a history:
`CreatePolicyVersion` with `SetAsDefault=true` replaces the operative document
and bumps `DefaultVersionId` — the call `AWS::IAM::ManagedPolicy` updates
dispatch — but superseded documents are not retained.

## Related

- [IAM](../iam.md) — quick start and what works
- [IAM troubleshooting](./troubleshooting.md) — the errors that stop a local run
- [IAM operations](./operations.md) — per-operation status
- [STS](../sts.md) — where an assumed-role session comes from
