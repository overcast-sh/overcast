"""
groups/iam.py — IAM compatibility test implementations for the Python suite.

iam-users and iam-roles resolve through their authored scenarios
(compat/model/authored/iam-users.json, compat/model/authored/iam-roles.json).
"""

from __future__ import annotations
import json
from lib.harness import TestContext
from lib.clients import make_clients

_ASSUME_POLICY = json.dumps({
    "Version": "2012-10-17",
    "Statement": [{
        "Effect": "Allow",
        "Principal": {"Service": "lambda.amazonaws.com"},
        "Action": "sts:AssumeRole",
    }],
})

_POLICY_DOC = json.dumps({
    "Version": "2012-10-17",
    "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "*"}],
})

# A document AWS refuses with MalformedPolicyDocument: "Statements must include
# either an Action or NotAction element" (IAM User Guide,
# reference_policies_elements_action.html). Every writer that takes a document
# names that error (IAM API Reference, API_CreatePolicy.html and
# API_CreateRole.html, Errors).
_MALFORMED_POLICY = json.dumps({
    "Version": "2012-10-17",
    "Statement": [{"Effect": "Allow", "Resource": "*"}],
})

# The tag set the policy fixture is created with, and the one GetPolicy must
# hand back on the resource itself.
_RESOURCE_TAGS = [{"Key": "owner", "Value": "compat"}, {"Key": "stage", "Value": "dev"}]


def _assert_malformed_policy_document(op: str, call) -> None:
    """Run call() and require MalformedPolicyDocument (HTTP 400) back."""
    import botocore.exceptions
    try:
        call()
    except botocore.exceptions.ClientError as exc:
        code = exc.response["Error"]["Code"]
        if code != "MalformedPolicyDocument":
            raise AssertionError(f"{op}: expected MalformedPolicyDocument, got {code}") from exc
        status = exc.response.get("ResponseMetadata", {}).get("HTTPStatusCode")
        if status != 400:
            raise AssertionError(
                f"{op}: expected HTTP 400 for MalformedPolicyDocument, got {status}"
            ) from exc
        return
    raise AssertionError(f"{op}: expected MalformedPolicyDocument, got success")


def _assert_resource_tags(op: str, tags) -> None:
    """Check the two fixture tags on a resource returned by a Get* call."""
    got = {tag["Key"]: tag["Value"] for tag in (tags or [])}
    for want in _RESOURCE_TAGS:
        if got.get(want["Key"]) != want["Value"]:
            raise AssertionError(
                f"{op}: tag {want['Key']} = {got.get(want['Key'])!r}, "
                f"want {want['Value']!r} (tags: {got})"
            )


def _iam(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region).iam


# ── iam-policies ──────────────────────────────────────────────────────────────

def setup_iam_policies(ctx: TestContext) -> None:
    ctx["iam_policy_arn"] = None
    # The counter tests attach this group's own policy to a role of its own, so
    # AttachmentCount moves for a customer managed policy rather than for the
    # AWS managed one iam-roles attaches.
    role = f"{ctx.run_id}-pol-role"
    _iam(ctx).create_role(RoleName=role, AssumeRolePolicyDocument=_ASSUME_POLICY)
    ctx["iam_policy_role"] = role


def teardown_iam_policies(ctx: TestContext) -> None:
    iam = _iam(ctx)
    arn = ctx.get("iam_policy_arn")
    role = ctx.get("iam_policy_role")
    if role:
        if arn:
            try:
                iam.detach_role_policy(RoleName=role, PolicyArn=arn)
            except Exception:
                pass
        try:
            iam.delete_role(RoleName=role)
        except Exception:
            pass
    if arn:
        try:
            iam.delete_policy(PolicyArn=arn)
        except Exception:
            pass


def CreatePolicy(ctx: TestContext) -> None:
    iam = _iam(ctx)
    name = f"{ctx.run_id}-policy"
    resp = iam.create_policy(
        PolicyName=name,
        PolicyDocument=_POLICY_DOC,
        Tags=_RESOURCE_TAGS,
    )
    arn = resp.get("Policy", {}).get("Arn")
    if not arn:
        raise AssertionError("CreatePolicy: missing PolicyArn")
    ctx["iam_policy_arn"] = arn


def GetPolicy(ctx: TestContext) -> None:
    iam = _iam(ctx)
    arn = ctx.get("iam_policy_arn")
    if not arn:
        raise AssertionError("GetPolicy: no policy ARN")
    resp = iam.get_policy(PolicyArn=arn)
    if resp.get("Policy", {}).get("Arn") != arn:
        raise AssertionError(f"GetPolicy: wrong ARN {resp.get('Policy', {}).get('Arn')!r}")


def ListPolicies(ctx: TestContext) -> None:
    iam = _iam(ctx)
    arn = ctx.get("iam_policy_arn")
    resp = iam.list_policies(Scope="Local")
    arns = [p["Arn"] for p in resp.get("Policies", [])]
    if arn and arn not in arns:
        raise AssertionError(f"ListPolicies: {arn!r} not found in local policies")


def CreatePolicyMalformedDocument(ctx: TestContext) -> None:
    """The same refusal on the identity-policy writer."""
    iam = _iam(ctx)
    name = f"{ctx.run_id}-p-malformed"
    _assert_malformed_policy_document(
        "CreatePolicyMalformedDocument",
        lambda: iam.create_policy(PolicyName=name, PolicyDocument=_MALFORMED_POLICY),
    )


def GetPolicyReturnsTags(ctx: TestContext) -> None:
    """Tags given to CreatePolicy come back on the policy (API_Policy.html)."""
    iam = _iam(ctx)
    arn = ctx.get("iam_policy_arn")
    if not arn:
        raise AssertionError("GetPolicyReturnsTags: no policy ARN")
    resp = iam.get_policy(PolicyArn=arn)
    _assert_resource_tags("GetPolicyReturnsTags", resp.get("Policy", {}).get("Tags"))


def _attachment_count(ctx: TestContext, op: str) -> int:
    """Read AttachmentCount back through GetPolicy."""
    resp = _iam(ctx).get_policy(PolicyArn=ctx["iam_policy_arn"])
    policy = resp.get("Policy", {})
    if "AttachmentCount" not in policy:
        raise AssertionError(f"{op}: GetPolicy returned no AttachmentCount: {policy}")
    return policy["AttachmentCount"]


def GetPolicyAttachmentCountAfterAttach(ctx: TestContext) -> None:
    """AttachmentCount moves 0 to 1.

    "The number of entities (users, groups, and roles) that the policy is
    attached to" (IAM API Reference, API_Policy.html) is what a cleanup script
    reads before deleting a policy, so a stuck 0 deletes something in use.
    """
    op = "GetPolicyAttachmentCountAfterAttach"
    iam = _iam(ctx)
    before = _attachment_count(ctx, op)
    if before != 0:
        raise AssertionError(f"{op}: AttachmentCount = {before} before the attach, want 0")
    iam.attach_role_policy(RoleName=ctx["iam_policy_role"], PolicyArn=ctx["iam_policy_arn"])
    after = _attachment_count(ctx, op)
    if after != 1:
        raise AssertionError(f"{op}: AttachmentCount = {after} after attaching to one role, want 1")


def GetPolicyAttachmentCountAfterDetach(ctx: TestContext) -> None:
    """The counter moves back to 0, which a never-decremented one would fail."""
    op = "GetPolicyAttachmentCountAfterDetach"
    iam = _iam(ctx)
    iam.detach_role_policy(RoleName=ctx["iam_policy_role"], PolicyArn=ctx["iam_policy_arn"])
    after = _attachment_count(ctx, op)
    if after != 0:
        raise AssertionError(f"{op}: AttachmentCount = {after} after the detach, want 0")


def DeletePolicy(ctx: TestContext) -> None:
    iam = _iam(ctx)
    arn = ctx.get("iam_policy_arn")
    if not arn:
        raise AssertionError("DeletePolicy: no policy to delete")
    iam.delete_policy(PolicyArn=arn)
    ctx["iam_policy_arn"] = None


# ── iam-groups ────────────────────────────────────────────────────────────────

def setup_iam_groups(ctx: TestContext) -> None:
    iam = _iam(ctx)
    group_name = f"{ctx.run_id}-group"
    user_name = f"{ctx.run_id}-guser"
    iam.create_group(GroupName=group_name)
    iam.create_user(UserName=user_name)
    ctx["iam_group_name"] = group_name
    ctx["iam_group_user"] = user_name


def teardown_iam_groups(ctx: TestContext) -> None:
    iam = _iam(ctx)
    user = ctx.get("iam_group_user")
    group = ctx.get("iam_group_name")
    if user and group:
        try:
            iam.remove_user_from_group(GroupName=group, UserName=user)
        except Exception:
            pass
    if group:
        try:
            iam.delete_group(GroupName=group)
        except Exception:
            pass
    if user:
        try:
            iam.delete_user(UserName=user)
        except Exception:
            pass


def CreateGroup(ctx: TestContext) -> None:
    if not ctx.get("iam_group_name"):
        raise AssertionError("CreateGroup: group not created in setup")


def AddUserToGroup(ctx: TestContext) -> None:
    iam = _iam(ctx)
    iam.add_user_to_group(
        GroupName=ctx["iam_group_name"],
        UserName=ctx["iam_group_user"],
    )


def ListGroupsForUser(ctx: TestContext) -> None:
    iam = _iam(ctx)
    user = ctx["iam_group_user"]
    group = ctx["iam_group_name"]
    resp = iam.list_groups_for_user(UserName=user)
    names = [g["GroupName"] for g in resp.get("Groups", [])]
    if group not in names:
        raise AssertionError(f"ListGroupsForUser: {group!r} not found in {names}")


def RemoveUserFromGroup(ctx: TestContext) -> None:
    iam = _iam(ctx)
    iam.remove_user_from_group(
        GroupName=ctx["iam_group_name"],
        UserName=ctx["iam_group_user"],
    )


def GetGroup(ctx: TestContext) -> None:
    iam = _iam(ctx)
    group = ctx["iam_group_name"]
    resp = iam.get_group(GroupName=group)
    if resp.get("Group", {}).get("GroupName") != group:
        raise AssertionError(f"GetGroup: wrong group name")


def DeleteGroup(ctx: TestContext) -> None:
    iam = _iam(ctx)
    name = f"{ctx.run_id}-grp-del"
    iam.create_group(GroupName=name)
    iam.delete_group(GroupName=name)


# -- iam-simulate --------------------------------------------------------------

def _sim_policy(ctx: TestContext) -> str:
    return json.dumps({
        "Version": "2012-10-17",
        "Statement": [{
            "Effect": "Allow",
            "Action": "s3:GetObject",
            "Resource": f"arn:aws:s3:::{ctx.run_id}-sim/*",
        }],
    })


def setup_iam_simulate(ctx: TestContext) -> None:
    iam = _iam(ctx)
    name = f"{ctx.run_id}-sim-user"
    iam.create_user(UserName=name)
    iam.put_user_policy(
        UserName=name, PolicyName="sim-allow-read", PolicyDocument=_sim_policy(ctx),
    )
    ctx["iam_sim_user"] = name


def teardown_iam_simulate(ctx: TestContext) -> None:
    iam = _iam(ctx)
    name = ctx.get("iam_sim_user")
    if not name:
        return
    try:
        iam.delete_user_policy(UserName=name, PolicyName="sim-allow-read")
    except Exception:
        pass
    try:
        iam.delete_user(UserName=name)
    except Exception:
        pass


def _decision(resp) -> str:
    results = resp.get("EvaluationResults", [])
    if not results:
        raise AssertionError("simulate: missing EvaluationResults")
    return results[0].get("EvalDecision", "")


def SimulateCustomPolicyAllowed(ctx: TestContext) -> None:
    iam = _iam(ctx)
    resp = iam.simulate_custom_policy(
        PolicyInputList=[_sim_policy(ctx)],
        ActionNames=["s3:GetObject"],
        ResourceArns=[f"arn:aws:s3:::{ctx.run_id}-sim/report.csv"],
    )
    decision = _decision(resp)
    if decision != "allowed":
        raise AssertionError(f"SimulateCustomPolicy: expected allowed, got {decision!r}")
    if resp["EvaluationResults"][0].get("EvalActionName") != "s3:GetObject":
        raise AssertionError("SimulateCustomPolicy: wrong EvalActionName")


def SimulateCustomPolicyImplicitDeny(ctx: TestContext) -> None:
    iam = _iam(ctx)
    resp = iam.simulate_custom_policy(
        PolicyInputList=[_sim_policy(ctx)],
        ActionNames=["s3:PutObject"],
        ResourceArns=[f"arn:aws:s3:::{ctx.run_id}-sim/report.csv"],
    )
    decision = _decision(resp)
    if decision != "implicitDeny":
        raise AssertionError(f"SimulateCustomPolicy: expected implicitDeny, got {decision!r}")


def SimulateCustomPolicyExplicitDeny(ctx: TestContext) -> None:
    iam = _iam(ctx)
    doc = json.dumps({
        "Version": "2012-10-17",
        "Statement": [
            {"Effect": "Allow", "Action": "s3:*", "Resource": "*"},
            {"Effect": "Deny", "Action": "s3:DeleteObject", "Resource": "*"},
        ],
    })
    resp = iam.simulate_custom_policy(
        PolicyInputList=[doc],
        ActionNames=["s3:DeleteObject"],
        ResourceArns=[f"arn:aws:s3:::{ctx.run_id}-sim/report.csv"],
    )
    decision = _decision(resp)
    if decision != "explicitDeny":
        raise AssertionError(f"SimulateCustomPolicy: expected explicitDeny, got {decision!r}")


def SimulatePrincipalPolicyAllowed(ctx: TestContext) -> None:
    iam = _iam(ctx)
    name = ctx["iam_sim_user"]
    resp = iam.simulate_principal_policy(
        PolicySourceArn=f"arn:aws:iam::000000000000:user/{name}",
        ActionNames=["s3:GetObject"],
        ResourceArns=[f"arn:aws:s3:::{ctx.run_id}-sim/report.csv"],
    )
    decision = _decision(resp)
    if decision != "allowed":
        raise AssertionError(f"SimulatePrincipalPolicy: expected allowed, got {decision!r}")
    matched = resp["EvaluationResults"][0].get("MatchedStatements", [])
    if not any(m.get("SourcePolicyId") == "sim-allow-read" for m in matched):
        raise AssertionError("SimulatePrincipalPolicy: MatchedStatements missing sim-allow-read")


def SimulatePrincipalPolicyImplicitDeny(ctx: TestContext) -> None:
    iam = _iam(ctx)
    name = ctx["iam_sim_user"]
    resp = iam.simulate_principal_policy(
        PolicySourceArn=f"arn:aws:iam::000000000000:user/{name}",
        ActionNames=["s3:DeleteObject"],
        ResourceArns=[f"arn:aws:s3:::{ctx.run_id}-sim/report.csv"],
    )
    decision = _decision(resp)
    if decision != "implicitDeny":
        raise AssertionError(f"SimulatePrincipalPolicy: expected implicitDeny, got {decision!r}")


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "iam-policies:CreatePolicy": CreatePolicy,
    "iam-policies:CreatePolicyMalformedDocument": CreatePolicyMalformedDocument,
    "iam-policies:GetPolicy": GetPolicy,
    "iam-policies:GetPolicyReturnsTags": GetPolicyReturnsTags,
    "iam-policies:ListPolicies": ListPolicies,
    "iam-policies:GetPolicyAttachmentCountAfterAttach": GetPolicyAttachmentCountAfterAttach,
    "iam-policies:GetPolicyAttachmentCountAfterDetach": GetPolicyAttachmentCountAfterDetach,
    "iam-policies:DeletePolicy": DeletePolicy,
    "iam-groups:CreateGroup": CreateGroup,
    "iam-groups:AddUserToGroup": AddUserToGroup,
    "iam-groups:ListGroupsForUser": ListGroupsForUser,
    "iam-groups:RemoveUserFromGroup": RemoveUserFromGroup,
    "iam-groups:GetGroup": GetGroup,
    "iam-groups:DeleteGroup": DeleteGroup,
    "iam-simulate:SimulateCustomPolicyAllowed": SimulateCustomPolicyAllowed,
    "iam-simulate:SimulateCustomPolicyImplicitDeny": SimulateCustomPolicyImplicitDeny,
    "iam-simulate:SimulateCustomPolicyExplicitDeny": SimulateCustomPolicyExplicitDeny,
    "iam-simulate:SimulatePrincipalPolicyAllowed": SimulatePrincipalPolicyAllowed,
    "iam-simulate:SimulatePrincipalPolicyImplicitDeny": SimulatePrincipalPolicyImplicitDeny,
}

SETUP = {
    "iam-policies": setup_iam_policies,
    "iam-groups": setup_iam_groups,
    "iam-simulate": setup_iam_simulate,
}

TEARDOWN = {
    "iam-policies": teardown_iam_policies,
    "iam-groups": teardown_iam_groups,
    "iam-simulate": teardown_iam_simulate,
}
