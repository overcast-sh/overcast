/**
 * Default MSW request handlers for the BFF `/api/*` routes.
 *
 * Each handler mirrors a real BFF endpoint (internal/bff/bff.go).
 * Tests override specific handlers with `server.use(...)` inside
 * a `describe` block or individual test — see render.tsx for the
 * `server` import.
 *
 * Keep handlers minimal: return the smallest shape that makes the
 * component under test render without errors. Detailed fixture
 * scenarios belong in the individual test files.
 */

import { http, HttpResponse } from "msw"

// ─── Health ───────────────────────────────────────────────────────────────

export const healthHandlers = [
  http.get("/api/health", () =>
    HttpResponse.json({
      status: "ok",
      services: ["s3", "sqs", "dynamodb", "lambda", "sns"],
    }),
  ),
  http.get("http://localhost:4566/_overcast/info", () =>
    HttpResponse.json({
      region: "us-east-1",
      account_id: "000000000000",
      version: "test",
      debug: false,
    }),
  ),
]

// ─── S3 ───────────────────────────────────────────────────────────────────

export const s3Handlers = [http.get("/api/s3/buckets", () => HttpResponse.json({ buckets: [] }))]

// ─── SQS ──────────────────────────────────────────────────────────────────

export const sqsHandlers = [http.get("/api/sqs/queues", () => HttpResponse.json({ queues: [] }))]

// ─── ECR ──────────────────────────────────────────────────────────────────

export const ecrHandlers = [
  http.get("/api/ecr/repositories", () => HttpResponse.json({ repositories: [] })),
]

// ─── CloudWatch ───────────────────────────────────────────────────────────

export const cloudwatchHandlers = [
  http.get("/api/logs/log-groups", () => HttpResponse.json({ logGroups: [] })),
]

// --- ECS ------------------------------------------------------------------

export const ecsHandlers = [
  http.get("/api/ecs/tasks/:taskArn/logs/:container", ({ params }) =>
    HttpResponse.json({
      taskArn: params.taskArn,
      containerName: params.container,
      logs: "",
    }),
  ),
]

// ─── Inbox ────────────────────────────────────────────────────────────────

export const inboxHandlers = [http.get("/api/inbox/messages", () => HttpResponse.json([]))]

// ─── Debug ────────────────────────────────────────────────────────────────

export const debugHandlers = [http.get("/api/debug/state", () => HttpResponse.json({}))]

// ─── Compute debugger ─────────────────────────────────────────────────────

// The default configuration has no targets, and a resource the server knows
// nothing about is a 404 the console renders as "off". Tests that want a
// target seed the query cache or override these with `server.use(...)`.
export const debuggerHandlers = [
  http.get("/api/debugger/targets", () => HttpResponse.json({ targets: [] })),
  http.get("/api/debugger/targets/:service/:resource", ({ params }) =>
    HttpResponse.json(
      { error: `no such resource: ${String(params.service)}/${String(params.resource)}` },
      { status: 404 },
    ),
  ),
]

// ─── Preflight ────────────────────────────────────────────────────────────

// Every list page asks this once it has rendered nothing, so the default is
// the healthy answer: nothing in this region, and nothing anywhere else
// either. A test that wants the advisory overrides it with `server.use(...)`.
export const preflightHandlers = [
  http.get("/api/preflight/region", ({ request }) =>
    HttpResponse.json({
      kind: new URL(request.url).searchParams.get("kind"),
      region: "us-east-1",
      count: 0,
      elsewhere: [],
    }),
  ),
]

// ─── Settings ─────────────────────────────────────────────────────────────

export const settingsHandlers = [
  http.get("/api/settings/https", () =>
    HttpResponse.json({
      mode: "off",
      caExists: false,
      trustStore: "not_installed",
      inContainer: false,
      restartCommand: "OVERCAST_TLS=auto overcast serve",
      httpsEndpoint: "https://localhost:4566",
      caEphemeral: false,
      caCertPath: "/home/dev/.overcast/data/ca/rootCA.pem",
    }),
  ),
  http.post("/api/settings/https/enable", () =>
    HttpResponse.json({
      ok: true,
      caReady: true,
      trustInstalled: true,
      alreadyInstalled: false,
      restartRequired: true,
      restartCommand: "OVERCAST_TLS=auto overcast serve",
    }),
  ),
]

// ─── Default handler set (all services, empty state) ─────────────────────

export const handlers = [
  ...healthHandlers,
  ...s3Handlers,
  ...sqsHandlers,
  ...ecrHandlers,
  ...cloudwatchHandlers,
  ...ecsHandlers,
  ...inboxHandlers,
  ...debugHandlers,
  ...debuggerHandlers,
  ...preflightHandlers,
  ...settingsHandlers,
]
