/**
 * What the Lambda Web Functions forms edit, and how it becomes an API request.
 *
 * The drafts hold what a person typed — strings, for the numeric fields — and
 * the `to*` functions turn a draft the `*Problems` functions accepted into the
 * request. The limits are the model's own (and `lambdaweb/validate.go`'s), so a
 * form refuses what the service would, before the round trip.
 */

import type {
  ApplicationLogLevel,
  AuthType,
  AutoDeploymentMode,
  CreateWebFunctionEndpointRequest,
  EndpointType,
  FunctionEndpointSummary,
  LoggingConfig,
  RevisionConfig,
  RevisionWeight,
  ScalingConfig,
  SystemLogLevel,
  ThrottleConfig,
  UpdateWebFunctionEndpointRequest,
} from "@aws-sdk/client-lambda-web"

// ─── States ───────────────────────────────────────────────────────────────

/** How often a Pending revision or endpoint is re-read until it settles (ms). */
export const TRANSITION_POLL_MS = 1_500

/** Whether a state is one that passes: the thing is still being prepared or removed. */
export function isTransitionalState(state: string | undefined): boolean {
  return state === "Pending" || state === "Deleting"
}

/** An endpoint that is still settling: Pending or Deleting, or an update still in progress. */
export function isTransitionalEndpoint(e: { state?: string; updateStatus?: string }): boolean {
  return isTransitionalState(e.state) || e.updateStatus === "InProgress"
}

// ─── Names ────────────────────────────────────────────────────────────────

export const NAME_RULE =
  "Letters, numbers, hyphens and underscores; not starting or ending with a hyphen or underscore; up to 64 characters."

const NAME_PATTERN = /^[a-zA-Z0-9]([a-zA-Z0-9_-]*[a-zA-Z0-9])?$/

/** Why `name` is not a valid function or endpoint name, or undefined when it is. */
export function nameProblem(name: string): string | undefined {
  if (name.length > 64) return "At most 64 characters."
  if (!NAME_PATTERN.test(name)) return NAME_RULE
  return undefined
}

// ─── Revisions ────────────────────────────────────────────────────────────

/** Web Functions run Node.js only. */
export const RUNTIMES = ["nodejs24.x", "nodejs22.x", "nodejs20.x"] as const

export const TIMEOUT_RANGE = { min: 3, max: 900, default: 30 } as const
export const CONCURRENCY_RANGE = { min: 1, max: 128, default: 64 } as const

export const APPLICATION_LOG_LEVELS: readonly ApplicationLogLevel[] = [
  "TRACE",
  "DEBUG",
  "INFO",
  "WARN",
  "ERROR",
  "FATAL",
]
export const SYSTEM_LOG_LEVELS: readonly SystemLogLevel[] = ["DEBUG", "INFO", "WARN"]

/**
 * The CloudWatch Logs group a function's logs go to unless a revision's
 * `loggingConfig.logGroup` says otherwise. System logs go to the same name
 * with `.local` appended (internal/services/lambdaweb/telemetry.go).
 */
export function defaultLogGroup(functionName: string): string {
  return `/aws/lambda/web/${functionName}`
}

/** The group holding the system logs of `logGroup`. */
export function systemLogGroup(logGroup: string): string {
  return `${logGroup}.local`
}

export interface EnvEntry {
  key: string
  value: string
}

export interface RevisionDraft {
  bucket: string
  key: string
  versionId: string
  runtime: string
  executionRoleArn: string
  timeoutSeconds: string
  maxConcurrency: string
  description: string
  env: EnvEntry[]
  /** Blank for the default group, `/aws/lambda/web/{function}`. */
  logGroup: string
  /** Blank for the service default (INFO). */
  applicationLogLevel: ApplicationLogLevel | ""
  systemLogLevel: SystemLogLevel | ""
}

export function emptyRevisionDraft(): RevisionDraft {
  return {
    bucket: "",
    key: "",
    versionId: "",
    runtime: RUNTIMES[0],
    executionRoleArn: "",
    timeoutSeconds: "",
    maxConcurrency: "",
    description: "",
    env: [],
    logGroup: "",
    applicationLogLevel: "",
    systemLogLevel: "",
  }
}

export type RevisionProblems = Partial<
  Record<
    "bucket" | "key" | "executionRoleArn" | "timeoutSeconds" | "maxConcurrency" | "logGroup",
    string
  >
>

const LOG_GROUP_PATTERN = /^[\w./#-]{1,512}$/

const ROLE_ARN = /^arn:aws[a-z-]*:iam::\d{12}:role\/.+$/

function integerIn(text: string, min: number, max: number): string | undefined {
  if (text.trim() === "") return undefined
  const n = Number(text)
  return Number.isInteger(n) && n >= min && n <= max
    ? undefined
    : `A whole number from ${min} to ${max}.`
}

/** What is wrong with a revision draft, field by field; empty when it can be submitted. */
export function revisionProblems(d: RevisionDraft): RevisionProblems {
  const problems: RevisionProblems = {}
  if (d.bucket.trim() === "") problems.bucket = "The S3 bucket holding the package."
  if (d.key.trim() === "") problems.key = "The package's key in the bucket."
  if (!ROLE_ARN.test(d.executionRoleArn.trim())) {
    problems.executionRoleArn = "An IAM role ARN, such as arn:aws:iam::000000000000:role/web-role."
  }
  const timeout = integerIn(d.timeoutSeconds, TIMEOUT_RANGE.min, TIMEOUT_RANGE.max)
  if (timeout) problems.timeoutSeconds = timeout
  const concurrency = integerIn(d.maxConcurrency, CONCURRENCY_RANGE.min, CONCURRENCY_RANGE.max)
  if (concurrency) problems.maxConcurrency = concurrency
  const logGroup = d.logGroup.trim()
  if (logGroup !== "" && !LOG_GROUP_PATTERN.test(logGroup)) {
    problems.logGroup = "Letters, numbers and . _ - / # up to 512 characters."
  }
  return problems
}

export function hasProblems(problems: object): boolean {
  return Object.keys(problems).length > 0
}

/** The revision request members a draft stands for. Optional fields left blank are omitted. */
export function toRevisionConfig(d: RevisionDraft): RevisionConfig {
  const env = Object.fromEntries(
    d.env.filter((e) => e.key.trim() !== "").map((e) => [e.key.trim(), e.value]),
  )
  const versionId = d.versionId.trim()
  const description = d.description.trim()
  const logGroup = d.logGroup.trim()
  const logging: LoggingConfig = {
    ...(logGroup ? { logGroup } : {}),
    ...(d.applicationLogLevel ? { applicationLogLevel: d.applicationLogLevel } : {}),
    ...(d.systemLogLevel ? { systemLogLevel: d.systemLogLevel } : {}),
  }
  return {
    ...(description ? { description } : {}),
    buildConfig: {
      codeConfig: {
        s3Object: {
          bucket: d.bucket.trim(),
          key: d.key.trim(),
          ...(versionId ? { versionId } : {}),
        },
      },
      runtimeConfig: { runtime: d.runtime },
    },
    serviceConfig: {
      executionRoleArn: d.executionRoleArn.trim(),
      ...(d.timeoutSeconds.trim() ? { timeoutSeconds: Number(d.timeoutSeconds) } : {}),
      ...(d.maxConcurrency.trim()
        ? { maxConcurrencyPerEnvironment: Number(d.maxConcurrency) }
        : {}),
      ...(Object.keys(env).length > 0 ? { environmentVariables: env } : {}),
      ...(Object.keys(logging).length > 0 ? { telemetryConfig: { loggingConfig: logging } } : {}),
    },
  }
}

// ─── Endpoints ────────────────────────────────────────────────────────────

export const ENDPOINT_TYPES: readonly EndpointType[] = ["HomeRegion", "MultiRegion", "PerRegion"]
export const AUTH_TYPES: readonly AuthType[] = ["ApplicationManaged", "IamAuth"]
export const DEPLOYMENT_MODES: readonly AutoDeploymentMode[] = ["LatestRevision", "Disabled"]

/** How an endpoint chooses the revision it serves. */
export const DEPLOYMENT_OPTIONS = [
  {
    value: "LatestRevision",
    label: "Latest revision",
    hint: "Every new revision takes all the traffic.",
  },
  { value: "Disabled", label: "Pinned", hint: "Traffic goes where the weights below say." },
] as const

export const AUTH_OPTIONS = [
  {
    value: "ApplicationManaged",
    label: "Application managed",
    hint: "Your code decides who may call.",
  },
  { value: "IamAuth", label: "IAM auth", hint: "Callers sign requests with SigV4." },
] as const

export const MAX_ENVIRONMENTS_RANGE = { min: 2, max: 10_000 } as const

/** The rates the throttle accepts: 0, 100 to 1000 by 100, then 2000 to 10000 by 1000. */
export const RATE_LIMITS: readonly number[] = [
  0,
  ...Array.from({ length: 10 }, (_, i) => (i + 1) * 100),
  ...Array.from({ length: 9 }, (_, i) => (i + 2) * 1000),
]

/**
 * Where an endpoint's traffic goes: one revision, or a split between two.
 * With a `secondary`, `primaryWeight` is the primary's share and the secondary
 * gets the rest.
 */
export interface WeightsDraft {
  primary: string
  secondary: string
  primaryWeight: number
}

export function emptyWeights(): WeightsDraft {
  return { primary: "", secondary: "", primaryWeight: 90 }
}

/** The draft for weights as an endpoint holds them. */
export function weightsDraftOf(weights: readonly RevisionWeight[] | undefined): WeightsDraft {
  const list = weights ?? []
  if (list.length === 0) return emptyWeights()
  const first = list[0]
  if (list.length === 1)
    return { primary: first.revisionId ?? "", secondary: "", primaryWeight: 90 }
  return {
    primary: first.revisionId ?? "",
    secondary: list[1].revisionId ?? "",
    primaryWeight: first.weight ?? 50,
  }
}

/** The weights a draft stands for: none when no revision is chosen. */
export function toRevisionWeights(d: WeightsDraft): RevisionWeight[] {
  if (!d.primary) return []
  if (!d.secondary) return [{ revisionId: d.primary, weight: 100 }]
  return [
    { revisionId: d.primary, weight: d.primaryWeight },
    { revisionId: d.secondary, weight: 100 - d.primaryWeight },
  ]
}

export function weightsProblem(d: WeightsDraft): string | undefined {
  if (!d.primary) return "Choose the revision that serves traffic."
  if (d.secondary && d.secondary === d.primary) return "Choose two different revisions."
  return undefined
}

/** Whether two sets of weights route the same traffic. */
export function sameWeights(a: readonly RevisionWeight[], b: readonly RevisionWeight[]): boolean {
  return (
    a.length === b.length &&
    a.every((w, i) => w.revisionId === b[i].revisionId && w.weight === b[i].weight)
  )
}

export interface LimitsDraft {
  maxEnvironments: string
  /** A value from `RATE_LIMITS`, as text, or "" for no limit. */
  rateLimit: string
}

export function limitsDraftOf(
  scaling: ScalingConfig | undefined,
  throttle: ThrottleConfig | undefined,
): LimitsDraft {
  return {
    maxEnvironments: scaling?.maxEnvironments?.toString() ?? "",
    rateLimit: throttle?.rateLimit?.toString() ?? "",
  }
}

export function maxEnvironmentsProblem(text: string): string | undefined {
  return integerIn(text, MAX_ENVIRONMENTS_RANGE.min, MAX_ENVIRONMENTS_RANGE.max)
}

/**
 * The scaling and throttle members for a request. A blank field sends nothing
 * when the endpoint never had a value, and an empty object when it did — that
 * is how UpdateWebFunctionEndpoint clears one.
 */
export function toLimits(
  d: LimitsDraft,
  before?: { scaling?: ScalingConfig; throttle?: ThrottleConfig },
): { scalingConfig?: ScalingConfig; throttleConfig?: ThrottleConfig } {
  const out: { scalingConfig?: ScalingConfig; throttleConfig?: ThrottleConfig } = {}
  if (d.maxEnvironments.trim()) out.scalingConfig = { maxEnvironments: Number(d.maxEnvironments) }
  else if (before?.scaling?.maxEnvironments !== undefined) out.scalingConfig = {}
  if (d.rateLimit !== "") out.throttleConfig = { rateLimit: Number(d.rateLimit) }
  else if (before?.throttle?.rateLimit !== undefined) out.throttleConfig = {}
  return out
}

export interface EndpointDraft {
  name: string
  description: string
  type: EndpointType
  authType: AuthType
  deployment: AutoDeploymentMode
  /** The Regions besides the function's own, for a MultiRegion or PerRegion endpoint. */
  regions: string[]
  weights: WeightsDraft
  limits: LimitsDraft
}

export function emptyEndpointDraft(): EndpointDraft {
  return {
    name: "",
    description: "",
    type: "HomeRegion",
    authType: "ApplicationManaged",
    deployment: "LatestRevision",
    regions: [],
    weights: emptyWeights(),
    limits: { maxEnvironments: "", rateLimit: "" },
  }
}

export type EndpointProblems = Partial<
  Record<"name" | "regions" | "weights" | "maxEnvironments", string>
>

/**
 * What is wrong with an endpoint draft. `taken` is the endpoint names the
 * function already has; an edit passes `creating: false` and skips the name and
 * Region rules, which an update cannot change.
 */
export function endpointProblems(
  d: EndpointDraft,
  { creating, taken = [] }: { creating: boolean; taken?: readonly string[] },
): EndpointProblems {
  const problems: EndpointProblems = {}
  if (creating) {
    if (d.name === "") problems.name = "Name the endpoint."
    else if (nameProblem(d.name)) problems.name = nameProblem(d.name)
    else if (taken.includes(d.name))
      problems.name = "The function already has an endpoint by this name."
    if (d.type !== "HomeRegion" && d.regions.length === 0) {
      problems.regions = "Choose at least one Region besides the function's own."
    }
  }
  // LatestRevision routing owns the weights. A MultiRegion or PerRegion
  // endpoint has to be given them; elsewhere they are optional.
  const needsWeights = creating ? d.type !== "HomeRegion" : d.weights.primary !== ""
  if (d.deployment === "Disabled" && needsWeights) {
    const weights = weightsProblem(d.weights)
    if (weights) problems.weights = weights
  }
  const environments = maxEnvironmentsProblem(d.limits.maxEnvironments)
  if (environments) problems.maxEnvironments = environments
  return problems
}

/** The draft for editing an endpoint as it stands. */
export function endpointDraftOf(e: FunctionEndpointSummary): EndpointDraft {
  return {
    name: e.endpointName ?? "",
    description: e.description ?? "",
    type: e.endpointType ?? "HomeRegion",
    authType: e.authType ?? "ApplicationManaged",
    deployment: e.autoDeploymentMode ?? "Disabled",
    regions: e.regions ?? [],
    weights: weightsDraftOf(e.revisionWeights),
    limits: limitsDraftOf(e.scalingConfig, e.throttleConfig),
  }
}

export function toCreateEndpoint(
  functionName: string,
  d: EndpointDraft,
): CreateWebFunctionEndpointRequest {
  const weights = d.deployment === "Disabled" ? toRevisionWeights(d.weights) : []
  return {
    functionName,
    endpointName: d.name,
    ...(d.description.trim() ? { description: d.description.trim() } : {}),
    endpointType: d.type,
    authType: d.authType,
    autoDeploymentMode: d.deployment,
    ...(weights.length > 0 ? { revisionWeights: weights } : {}),
    ...(d.type !== "HomeRegion" ? { regions: d.regions } : {}),
    ...toLimits(d.limits),
  }
}

/**
 * The update for an endpoint: what the form shows, with the weights sent only
 * when they changed (and only for a pinned endpoint — the service refuses
 * weights beside `LatestRevision`), and a cleared limit sent as the empty
 * object that clears it.
 */
export function toUpdateEndpoint(
  functionName: string,
  before: FunctionEndpointSummary,
  d: EndpointDraft,
): UpdateWebFunctionEndpointRequest {
  const weights = toRevisionWeights(d.weights)
  const reweighted =
    d.deployment === "Disabled" &&
    weights.length > 0 &&
    !sameWeights(weights, before.revisionWeights ?? [])
  return {
    functionName,
    endpointName: before.endpointName,
    description: d.description.trim(),
    authType: d.authType,
    autoDeploymentMode: d.deployment,
    ...(reweighted ? { revisionWeights: weights } : {}),
    ...toLimits(d.limits, { scaling: before.scalingConfig, throttle: before.throttleConfig }),
  }
}

// ─── Display ──────────────────────────────────────────────────────────────

/** The names of the endpoints whose traffic includes `revisionId`. */
export function endpointsServing(
  revisionId: string,
  endpoints: readonly {
    endpointName?: string
    revisionWeights?: readonly { revisionId?: string; weight?: number }[]
  }[],
): string[] {
  return endpoints
    .filter((e) =>
      e.revisionWeights?.some((w) => w.revisionId === revisionId && (w.weight ?? 0) > 0),
    )
    .map((e) => e.endpointName ?? "")
}

/** `rev-ab12c 90% · rev-de34f 10%` — what an endpoint serves, as the map and the tables show it. */
export function describeServes(
  serves: readonly { revisionId?: string; weight?: number }[] | undefined,
): string {
  if (!serves || serves.length === 0) return "no revision"
  return serves.map((w) => `${w.revisionId ?? "?"} ${w.weight ?? 0}%`).join(" · ")
}
