/**
 * groups/cognito.ts — Cognito User Pools compatibility test groups for the Node.js suite.
 *
 * Status: NOT implemented in Overcast. All tests expected to fail with 501.
 * These tests define the coverage target for future Cognito implementation.
 *
 * Groups:
 *   cognito-token-validity — user pool client token validity settings
 *
 * cognito-userpools resolves through its authored scenario
 * (compat/model/authored/cognito-userpools.json).
 */

import {
  CreateUserPoolCommand,
  DeleteUserPoolCommand,
  CreateUserPoolClientCommand,
  DeleteUserPoolClientCommand,
  DescribeUserPoolClientCommand,
  UpdateUserPoolClientCommand,
} from "@aws-sdk/client-cognito-identity-provider";
import { makeClients } from "../lib/clients.ts";
import type { TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

export function makeCognitoGroups(suite: string): TestGroup[] {
  return [
    // ── cognito-token-validity ─────────────────────────────────────────────
    {
      suite,
      service: "cognito",
      name: "cognito-token-validity",
      tests: [
        {
          name: "CreateUserPoolClientWithTokenValidity",
          fn: async (ctx) => {
            const { cognito } = makeClients(ctx);
            const poolName = `compat-tv-${ctx.runId}`;
            const poolResp = await cognito.send(
              new CreateUserPoolCommand({ PoolName: poolName }),
            );
            const poolId = poolResp.UserPool?.Id;
            assert.ok(poolId, "CreateUserPool: missing Id");
            (ctx as Record<string, unknown>)["_tvPoolId"] = poolId;

            const resp = await cognito.send(
              new CreateUserPoolClientCommand({
                UserPoolId: poolId,
                ClientName: `compat-client-${ctx.runId}`,
                AccessTokenValidity: 2,
                IdTokenValidity: 3,
                RefreshTokenValidity: 7,
                TokenValidityUnits: {
                  AccessToken: "hours",
                  IdToken: "hours",
                  RefreshToken: "days",
                },
              }),
            );
            const client = resp.UserPoolClient;
            assert.ok(
              client?.ClientId,
              "CreateUserPoolClient: missing ClientId",
            );
            assert.equal(client.AccessTokenValidity, 2);
            assert.equal(client.IdTokenValidity, 3);
            assert.equal(client.RefreshTokenValidity, 7);
            assert.equal(client.TokenValidityUnits?.AccessToken, "hours");
            assert.equal(client.TokenValidityUnits?.IdToken, "hours");
            assert.equal(client.TokenValidityUnits?.RefreshToken, "days");
            (ctx as Record<string, unknown>)["_tvClientId"] = client.ClientId;
          },
        },
        {
          name: "DescribeUserPoolClientTokenValidity",
          fn: async (ctx) => {
            const { cognito } = makeClients(ctx);
            const poolId = (ctx as Record<string, unknown>)[
              "_tvPoolId"
            ] as string;
            const clientId = (ctx as Record<string, unknown>)[
              "_tvClientId"
            ] as string;
            assert.ok(poolId && clientId, "no pool/client from create");

            const resp = await cognito.send(
              new DescribeUserPoolClientCommand({
                UserPoolId: poolId,
                ClientId: clientId,
              }),
            );
            const client = resp.UserPoolClient;
            assert.equal(client?.AccessTokenValidity, 2);
            assert.equal(client?.IdTokenValidity, 3);
            assert.equal(client?.RefreshTokenValidity, 7);
          },
        },
        {
          name: "UpdateUserPoolClientTokenValidity",
          fn: async (ctx) => {
            const { cognito } = makeClients(ctx);
            const poolId = (ctx as Record<string, unknown>)[
              "_tvPoolId"
            ] as string;
            const clientId = (ctx as Record<string, unknown>)[
              "_tvClientId"
            ] as string;
            assert.ok(poolId && clientId, "no pool/client from create");

            const resp = await cognito.send(
              new UpdateUserPoolClientCommand({
                UserPoolId: poolId,
                ClientId: clientId,
                AccessTokenValidity: 30,
                TokenValidityUnits: {
                  AccessToken: "minutes",
                  IdToken: "hours",
                  RefreshToken: "days",
                },
              }),
            );
            const client = resp.UserPoolClient;
            assert.equal(client?.AccessTokenValidity, 30);
            assert.equal(client?.TokenValidityUnits?.AccessToken, "minutes");
          },
        },
        {
          name: "DeleteUserPoolClient",
          fn: async (ctx) => {
            const { cognito } = makeClients(ctx);
            const poolId = (ctx as Record<string, unknown>)[
              "_tvPoolId"
            ] as string;
            const clientId = (ctx as Record<string, unknown>)[
              "_tvClientId"
            ] as string;
            if (!poolId || !clientId) return;
            await cognito.send(
              new DeleteUserPoolClientCommand({
                UserPoolId: poolId,
                ClientId: clientId,
              }),
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { cognito } = makeClients(ctx);
        const poolId = (ctx as Record<string, unknown>)["_tvPoolId"] as string;
        if (!poolId) return;
        const clientId = (ctx as Record<string, unknown>)[
          "_tvClientId"
        ] as string;
        if (clientId) {
          try {
            await cognito.send(
              new DeleteUserPoolClientCommand({
                UserPoolId: poolId,
                ClientId: clientId,
              }),
            );
          } catch {}
        }
        try {
          await cognito.send(new DeleteUserPoolCommand({ UserPoolId: poolId }));
        } catch {}
      },
    },
  ];
}
