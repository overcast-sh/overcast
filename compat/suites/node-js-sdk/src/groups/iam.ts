/**
 * groups/iam.ts — IAM compatibility test groups for the Node.js suite.
 *
 * Status: NOT implemented in Overcast. All tests expected to fail with 501.
 * These tests define the coverage target for future IAM implementation.
 *
 * Groups:
 *   iam-policies — managed and inline policies
 *   iam-groups   — group lifecycle and membership
 *   iam-simulate — policy simulation (SimulateCustomPolicy / SimulatePrincipalPolicy)
 *
 * iam-users and iam-roles resolve through their authored scenarios
 * (compat/model/authored/iam-users.json, compat/model/authored/iam-roles.json).
 */

import {
  CreateUserCommand,
  DeleteUserCommand,
  CreateRoleCommand,
  DeleteRoleCommand,
  AttachRolePolicyCommand,
  DetachRolePolicyCommand,
  CreatePolicyCommand,
  DeletePolicyCommand,
  GetPolicyCommand,
  ListPoliciesCommand,
  PutUserPolicyCommand,
  DeleteUserPolicyCommand,
  CreateGroupCommand,
  DeleteGroupCommand,
  GetGroupCommand,
  AddUserToGroupCommand,
  RemoveUserFromGroupCommand,
  ListGroupsForUserCommand,
  SimulateCustomPolicyCommand,
  SimulatePrincipalPolicyCommand,
} from "@aws-sdk/client-iam";
import { makeClients } from "../lib/clients.ts";
import type { TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

/**
 * A document AWS refuses with MalformedPolicyDocument: "Statements must include
 * either an Action or NotAction element" (IAM User Guide,
 * reference_policies_elements_action.html). Every writer that takes a document
 * names that error (IAM API Reference, API_CreatePolicy.html and
 * API_CreateRole.html, Errors).
 */
const MALFORMED_POLICY = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Resource: "*" }],
});

/**
 * The tag set the policy fixture is created with, and the one GetPolicy must
 * hand back on the resource itself.
 */
const RESOURCE_TAGS = [
  { Key: "owner", Value: "compat" },
  { Key: "stage", Value: "dev" },
];

/** Checks the two fixture tags on a resource returned by a Get* call. */
function assertResourceTags(
  op: string,
  tags: { Key?: string; Value?: string }[] | undefined,
): void {
  const got = new Map((tags ?? []).map((t) => [t.Key, t.Value]));
  for (const want of RESOURCE_TAGS) {
    assert.equal(
      got.get(want.Key),
      want.Value,
      `${op}: tag ${want.Key} = ${got.get(want.Key)}, want ${want.Value}`,
    );
  }
}

/**
 * Asserts both halves of the error contract: the code AWS's model names, and
 * the 400 the Query protocol binds it to.
 */
async function assertMalformedPolicyDocument(
  op: string,
  send: Promise<unknown>,
): Promise<void> {
  try {
    await send;
  } catch (err: unknown) {
    const e = err as { name?: string; $metadata?: { httpStatusCode?: number } };
    assert.equal(
      e.name,
      "MalformedPolicyDocumentException",
      `${op}: expected MalformedPolicyDocument, got ${e.name}`,
    );
    assert.equal(
      e.$metadata?.httpStatusCode,
      400,
      `${op}: expected HTTP 400 for MalformedPolicyDocument, got ${e.$metadata?.httpStatusCode}`,
    );
    return;
  }
  throw new Error(`${op}: expected MalformedPolicyDocument, got success`);
}

export function makeIAMGroups(suite: string): TestGroup[] {
  return [
    // ── iam-policies ───────────────────────────────────────────────────────
    {
      suite,
      service: "iam",
      name: "iam-policies",
      tests: [
        {
          name: "CreatePolicy",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new CreatePolicyCommand({
                PolicyName: `${ctx.runId}-policy`,
                PolicyDocument: JSON.stringify({
                  Version: "2012-10-17",
                  Statement: [{ Effect: "Deny", Action: "*", Resource: "*" }],
                }),
                Tags: RESOURCE_TAGS,
              }),
            );
            assert.ok(resp.Policy?.Arn, "CreatePolicy: missing Arn");
            ctx["_policyArn"] = resp.Policy.Arn;
          },
        },
        {
          // The same refusal on the identity-policy writer.
          name: "CreatePolicyMalformedDocument",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            await assertMalformedPolicyDocument(
              "CreatePolicyMalformedDocument",
              iam.send(
                new CreatePolicyCommand({
                  PolicyName: `${ctx.runId}-policy-malformed`,
                  PolicyDocument: MALFORMED_POLICY,
                }),
              ),
            );
          },
        },
        {
          name: "GetPolicy",
          fn: async (ctx) => {
            const policyArn = ctx["_policyArn"] as string;
            assert.ok(policyArn, "no policy ARN");
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new GetPolicyCommand({ PolicyArn: policyArn }),
            );
            assert.ok(resp.Policy?.PolicyName, "GetPolicy: missing PolicyName");
          },
        },
        {
          name: "ListPolicies",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new ListPoliciesCommand({ Scope: "Local" }),
            );
            const name = `${ctx.runId}-policy`;
            assert.ok(
              resp.Policies?.some((p) => p.PolicyName === name),
              `ListPolicies: ${name} not found`,
            );
          },
        },
        {
          // Tags given to CreatePolicy come back on the policy
          // (API_Policy.html).
          name: "GetPolicyReturnsTags",
          fn: async (ctx) => {
            const policyArn = ctx["_policyArn"] as string;
            assert.ok(policyArn, "no policy ARN");
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new GetPolicyCommand({ PolicyArn: policyArn }),
            );
            assertResourceTags("GetPolicyReturnsTags", resp.Policy?.Tags);
          },
        },
        {
          // AttachmentCount moves 0 to 1. "The number of entities (users,
          // groups, and roles) that the policy is attached to"
          // (API_Policy.html) is what a cleanup script reads before deleting a
          // policy, so a stuck 0 deletes something in use.
          name: "GetPolicyAttachmentCountAfterAttach",
          fn: async (ctx) => {
            const op = "GetPolicyAttachmentCountAfterAttach";
            const policyArn = ctx["_policyArn"] as string;
            assert.ok(policyArn, "no policy ARN");
            const { iam } = makeClients(ctx);
            const before = await iam.send(
              new GetPolicyCommand({ PolicyArn: policyArn }),
            );
            assert.equal(
              before.Policy?.AttachmentCount,
              0,
              `${op}: AttachmentCount = ${before.Policy?.AttachmentCount} before the attach, want 0`,
            );

            // The group attaches its own customer managed policy to a role of
            // its own, so the counter moves for this policy rather than for the
            // AWS managed one iam-roles attaches.
            const roleName = `${ctx.runId}-policy-role`;
            await iam.send(
              new CreateRoleCommand({
                RoleName: roleName,
                AssumeRolePolicyDocument: JSON.stringify({
                  Version: "2012-10-17",
                  Statement: [
                    {
                      Effect: "Allow",
                      Principal: { Service: "lambda.amazonaws.com" },
                      Action: "sts:AssumeRole",
                    },
                  ],
                }),
              }),
            );
            ctx["_policyRoleName"] = roleName;
            await iam.send(
              new AttachRolePolicyCommand({
                RoleName: roleName,
                PolicyArn: policyArn,
              }),
            );
            const after = await iam.send(
              new GetPolicyCommand({ PolicyArn: policyArn }),
            );
            assert.equal(
              after.Policy?.AttachmentCount,
              1,
              `${op}: AttachmentCount = ${after.Policy?.AttachmentCount} after attaching to one role, want 1`,
            );
          },
        },
        {
          // The counter moves back to 0, which a never-decremented one fails.
          name: "GetPolicyAttachmentCountAfterDetach",
          fn: async (ctx) => {
            const op = "GetPolicyAttachmentCountAfterDetach";
            const policyArn = ctx["_policyArn"] as string;
            const roleName = ctx["_policyRoleName"] as string;
            assert.ok(policyArn, "no policy ARN");
            assert.ok(roleName, "no role from the attach test");
            const { iam } = makeClients(ctx);
            await iam.send(
              new DetachRolePolicyCommand({
                RoleName: roleName,
                PolicyArn: policyArn,
              }),
            );
            const after = await iam.send(
              new GetPolicyCommand({ PolicyArn: policyArn }),
            );
            assert.equal(
              after.Policy?.AttachmentCount,
              0,
              `${op}: AttachmentCount = ${after.Policy?.AttachmentCount} after the detach, want 0`,
            );
          },
        },
        {
          name: "DeletePolicy",
          fn: async (ctx) => {
            const policyArn = ctx["_policyArn"] as string;
            assert.ok(policyArn, "no policy ARN");
            const { iam } = makeClients(ctx);
            await iam.send(new DeletePolicyCommand({ PolicyArn: policyArn }));
          },
        },
      ],
      teardown: async (ctx) => {
        const { iam } = makeClients(ctx);
        const policyArn = ctx["_policyArn"] as string;
        const roleName = ctx["_policyRoleName"] as string;
        if (roleName) {
          if (policyArn) {
            try {
              await iam.send(
                new DetachRolePolicyCommand({
                  RoleName: roleName,
                  PolicyArn: policyArn,
                }),
              );
            } catch {}
          }
          try {
            await iam.send(new DeleteRoleCommand({ RoleName: roleName }));
          } catch {}
        }
        if (policyArn) {
          try {
            await iam.send(new DeletePolicyCommand({ PolicyArn: policyArn }));
          } catch {}
        }
      },
    },

    // ── iam-groups ─────────────────────────────────────────────────────────
    {
      suite,
      service: "iam",
      name: "iam-groups",
      tests: [
        {
          name: "CreateGroup",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            await iam.send(
              new CreateGroupCommand({ GroupName: `${ctx.runId}-grp` }),
            );
          },
        },
        {
          name: "AddUserToGroup",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            // Create a temp user for this test
            await iam.send(
              new CreateUserCommand({ UserName: `${ctx.runId}-grp-user` }),
            );
            await iam.send(
              new AddUserToGroupCommand({
                GroupName: `${ctx.runId}-grp`,
                UserName: `${ctx.runId}-grp-user`,
              }),
            );
          },
        },
        {
          name: "ListGroupsForUser",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new ListGroupsForUserCommand({
                UserName: `${ctx.runId}-grp-user`,
              }),
            );
            assert.ok(
              resp.Groups?.some((g) => g.GroupName === `${ctx.runId}-grp`),
              "ListGroupsForUser: group not found",
            );
          },
        },
        {
          name: "RemoveUserFromGroup",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            await iam.send(
              new RemoveUserFromGroupCommand({
                GroupName: `${ctx.runId}-grp`,
                UserName: `${ctx.runId}-grp-user`,
              }),
            );
          },
        },
        {
          name: "GetGroup",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new GetGroupCommand({ GroupName: `${ctx.runId}-grp` }),
            );
            assert.ok(resp.Group, "GetGroup: missing Group");
            assert.strictEqual(
              resp.Group.GroupName,
              `${ctx.runId}-grp`,
              `GetGroup: expected group name ${ctx.runId}-grp`,
            );
          },
        },
        {
          name: "DeleteGroup",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            await iam.send(
              new DeleteGroupCommand({ GroupName: `${ctx.runId}-grp` }),
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { iam } = makeClients(ctx);
        try {
          await iam.send(
            new RemoveUserFromGroupCommand({
              GroupName: `${ctx.runId}-grp`,
              UserName: `${ctx.runId}-grp-user`,
            }),
          );
        } catch {}
        try {
          await iam.send(
            new DeleteUserCommand({ UserName: `${ctx.runId}-grp-user` }),
          );
        } catch {}
        try {
          await iam.send(
            new DeleteGroupCommand({ GroupName: `${ctx.runId}-grp` }),
          );
        } catch {}
      },
    },
    // ── iam-simulate ───────────────────────────────────────────────────────
    {
      suite,
      service: "iam",
      name: "iam-simulate",
      setup: async (ctx) => {
        const { iam } = makeClients(ctx);
        await iam.send(
          new CreateUserCommand({ UserName: `${ctx.runId}-sim-user` }),
        );
        await iam.send(
          new PutUserPolicyCommand({
            UserName: `${ctx.runId}-sim-user`,
            PolicyName: "sim-allow-read",
            PolicyDocument: JSON.stringify({
              Version: "2012-10-17",
              Statement: [
                {
                  Effect: "Allow",
                  Action: "s3:GetObject",
                  Resource: `arn:aws:s3:::${ctx.runId}-sim/*`,
                },
              ],
            }),
          }),
        );
      },
      tests: [
        {
          name: "SimulateCustomPolicyAllowed",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new SimulateCustomPolicyCommand({
                PolicyInputList: [
                  JSON.stringify({
                    Version: "2012-10-17",
                    Statement: [
                      {
                        Effect: "Allow",
                        Action: "s3:GetObject",
                        Resource: `arn:aws:s3:::${ctx.runId}-sim/*`,
                      },
                    ],
                  }),
                ],
                ActionNames: ["s3:GetObject"],
                ResourceArns: [`arn:aws:s3:::${ctx.runId}-sim/report.csv`],
              }),
            );
            const result = resp.EvaluationResults?.[0];
            assert.ok(result, "SimulateCustomPolicy: missing EvaluationResults");
            assert.strictEqual(
              result.EvalDecision,
              "allowed",
              `SimulateCustomPolicy: expected allowed, got ${result.EvalDecision}`,
            );
            assert.strictEqual(result.EvalActionName, "s3:GetObject");
          },
        },
        {
          name: "SimulateCustomPolicyImplicitDeny",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new SimulateCustomPolicyCommand({
                PolicyInputList: [
                  JSON.stringify({
                    Version: "2012-10-17",
                    Statement: [
                      {
                        Effect: "Allow",
                        Action: "s3:GetObject",
                        Resource: `arn:aws:s3:::${ctx.runId}-sim/*`,
                      },
                    ],
                  }),
                ],
                ActionNames: ["s3:PutObject"],
                ResourceArns: [`arn:aws:s3:::${ctx.runId}-sim/report.csv`],
              }),
            );
            assert.strictEqual(
              resp.EvaluationResults?.[0]?.EvalDecision,
              "implicitDeny",
              "SimulateCustomPolicy: uncovered action should be implicitDeny",
            );
          },
        },
        {
          name: "SimulateCustomPolicyExplicitDeny",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new SimulateCustomPolicyCommand({
                PolicyInputList: [
                  JSON.stringify({
                    Version: "2012-10-17",
                    Statement: [
                      { Effect: "Allow", Action: "s3:*", Resource: "*" },
                      {
                        Effect: "Deny",
                        Action: "s3:DeleteObject",
                        Resource: "*",
                      },
                    ],
                  }),
                ],
                ActionNames: ["s3:DeleteObject"],
                ResourceArns: [`arn:aws:s3:::${ctx.runId}-sim/report.csv`],
              }),
            );
            assert.strictEqual(
              resp.EvaluationResults?.[0]?.EvalDecision,
              "explicitDeny",
              "SimulateCustomPolicy: explicit deny should win over allow",
            );
          },
        },
        {
          name: "SimulatePrincipalPolicyAllowed",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new SimulatePrincipalPolicyCommand({
                PolicySourceArn: `arn:aws:iam::000000000000:user/${ctx.runId}-sim-user`,
                ActionNames: ["s3:GetObject"],
                ResourceArns: [`arn:aws:s3:::${ctx.runId}-sim/report.csv`],
              }),
            );
            const result = resp.EvaluationResults?.[0];
            assert.ok(
              result,
              "SimulatePrincipalPolicy: missing EvaluationResults",
            );
            assert.strictEqual(
              result.EvalDecision,
              "allowed",
              `SimulatePrincipalPolicy: expected allowed, got ${result.EvalDecision}`,
            );
            assert.ok(
              result.MatchedStatements?.some(
                (m) => m.SourcePolicyId === "sim-allow-read",
              ),
              "SimulatePrincipalPolicy: MatchedStatements should name the inline policy",
            );
          },
        },
        {
          name: "SimulatePrincipalPolicyImplicitDeny",
          fn: async (ctx) => {
            const { iam } = makeClients(ctx);
            const resp = await iam.send(
              new SimulatePrincipalPolicyCommand({
                PolicySourceArn: `arn:aws:iam::000000000000:user/${ctx.runId}-sim-user`,
                ActionNames: ["s3:DeleteObject"],
                ResourceArns: [`arn:aws:s3:::${ctx.runId}-sim/report.csv`],
              }),
            );
            assert.strictEqual(
              resp.EvaluationResults?.[0]?.EvalDecision,
              "implicitDeny",
              "SimulatePrincipalPolicy: uncovered action should be implicitDeny",
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { iam } = makeClients(ctx);
        try {
          await iam.send(
            new DeleteUserPolicyCommand({
              UserName: `${ctx.runId}-sim-user`,
              PolicyName: "sim-allow-read",
            }),
          );
        } catch {}
        try {
          await iam.send(
            new DeleteUserCommand({ UserName: `${ctx.runId}-sim-user` }),
          );
        } catch {}
      },
    },
  ];
}
