/**
 * A read-only model of an Amazon States Language definition, shaped for the
 * console: which states exist, which scope (the top level, a Parallel branch,
 * a Map's item processor) each one lives in, and every transition out of it
 * with a short human label. The flow diagram, the execution trace and the
 * state inspector all read the same model, so a state is spelled one way
 * everywhere.
 *
 * It is deliberately forgiving: the console draws whatever the emulator stored,
 * including a definition that would not pass validation, and reports what is
 * wrong as `issues` instead of refusing to draw it.
 */

import { formatQuantity } from "@/lib/format"
import { isRecord } from "@/lib/utils"

export type StateType =
  "Task" | "Pass" | "Choice" | "Wait" | "Succeed" | "Fail" | "Parallel" | "Map"

const STATE_TYPES: ReadonlySet<string> = new Set([
  "Task",
  "Pass",
  "Choice",
  "Wait",
  "Succeed",
  "Fail",
  "Parallel",
  "Map",
])

export type TransitionKind = "next" | "choice" | "default" | "catch"

export interface AslTransition {
  kind: TransitionKind
  /** Target state name. */
  to: string
  /** Short label for the edge — a Choice condition, "Default", or the errors a Catch handles. */
  label?: string
}

export interface AslState {
  name: string
  /** The state's `Type` — one of `StateType`, or the raw string when it is not a known type. */
  type: string
  /** The id of the scope this state belongs to. */
  scopeId: string
  /** The state's own JSON, as written. */
  raw: Record<string, unknown>
  comment?: string
  /** `End: true`, or a Succeed/Fail state — the scope finishes here. */
  terminal: boolean
  transitions: AslTransition[]
  /** Child scopes: one per Parallel branch, or the single Map item processor. */
  childScopes: string[]
  /** One-line summary of what the state does, e.g. "Lambda · resize-image". */
  summary: string
  /** A Map whose items run as child executions (`ProcessorConfig.Mode: DISTRIBUTED`). */
  distributed: boolean
}

export interface AslScope {
  id: string
  /** The Parallel or Map state that owns this scope; absent for the top level. */
  container?: string
  /** Branch number within a Parallel. */
  branchIndex?: number
  startAt: string
  /** State names in definition order. */
  states: string[]
}

export interface AslModel {
  comment?: string
  queryLanguage: "JSONPath" | "JSONata"
  scopes: Map<string, AslScope>
  states: Map<string, AslState>
  /** Problems found while reading the definition; the model is still usable. */
  issues: string[]
}

export const ROOT_SCOPE = "root"

type Json = Record<string, unknown>

/** Parses a definition string. Returns an error rather than throwing when it is not a JSON object. */
export function parseDefinition(definition: string | undefined): {
  model?: AslModel
  error?: string
} {
  if (!definition?.trim()) return { error: "The definition is empty." }
  let parsed: unknown
  try {
    parsed = JSON.parse(definition)
  } catch (err) {
    return { error: `The definition is not valid JSON: ${(err as Error).message}` }
  }
  if (!isRecord(parsed)) return { error: "The definition must be a JSON object." }
  return { model: buildModel(parsed) }
}

export function buildModel(doc: Json): AslModel {
  const model: AslModel = {
    comment: typeof doc.Comment === "string" ? doc.Comment : undefined,
    queryLanguage: doc.QueryLanguage === "JSONata" ? "JSONata" : "JSONPath",
    scopes: new Map(),
    states: new Map(),
    issues: [],
  }
  readScope(model, doc, ROOT_SCOPE, undefined, undefined, model.queryLanguage)
  checkTransitions(model)
  return model
}

function readScope(
  model: AslModel,
  doc: Json,
  scopeId: string,
  container: string | undefined,
  branchIndex: number | undefined,
  inheritedLanguage: string,
) {
  const where = container
    ? `"${container}"${branchIndex !== undefined ? ` branch ${branchIndex + 1}` : ""}`
    : "the top level"
  const startAt = typeof doc.StartAt === "string" ? doc.StartAt : ""
  const statesDoc = isRecord(doc.States) ? doc.States : {}
  if (!startAt) model.issues.push(`StartAt is missing in ${where}.`)
  if (!isRecord(doc.States)) model.issues.push(`States is missing in ${where}.`)

  const scope: AslScope = { id: scopeId, container, branchIndex, startAt, states: [] }
  model.scopes.set(scopeId, scope)
  const language = doc.QueryLanguage === "JSONata" ? "JSONata" : inheritedLanguage

  for (const [name, value] of Object.entries(statesDoc)) {
    if (!isRecord(value)) {
      model.issues.push(`State "${name}" is not an object.`)
      continue
    }
    if (model.states.has(name)) {
      model.issues.push(
        `State name "${name}" is used more than once; names must be unique across the whole state machine.`,
      )
      continue
    }
    scope.states.push(name)
    model.states.set(name, readState(model, name, value, scopeId, language))
  }
  if (startAt && !(startAt in statesDoc)) {
    model.issues.push(`StartAt "${startAt}" in ${where} does not name a state.`)
  }
}

function readState(
  model: AslModel,
  name: string,
  raw: Json,
  scopeId: string,
  language: string,
): AslState {
  const type = typeof raw.Type === "string" ? raw.Type : "?"
  if (!STATE_TYPES.has(type)) model.issues.push(`State "${name}" has unknown Type "${type}".`)
  const stateLanguage = raw.QueryLanguage === "JSONata" ? "JSONata" : language

  const transitions: AslTransition[] = []
  if (typeof raw.Next === "string") transitions.push({ kind: "next", to: raw.Next })
  if (type === "Choice") {
    const choices = Array.isArray(raw.Choices) ? raw.Choices : []
    for (const rule of choices) {
      if (!isRecord(rule) || typeof rule.Next !== "string") continue
      transitions.push({
        kind: "choice",
        to: rule.Next,
        label: describeChoiceRule(rule, stateLanguage),
      })
    }
    if (typeof raw.Default === "string") {
      transitions.push({ kind: "default", to: raw.Default, label: "Default" })
    }
  }
  const catchers = Array.isArray(raw.Catch) ? raw.Catch : []
  for (const catcher of catchers) {
    if (!isRecord(catcher) || typeof catcher.Next !== "string") continue
    const errors = Array.isArray(catcher.ErrorEquals) ? catcher.ErrorEquals.map(String) : []
    transitions.push({ kind: "catch", to: catcher.Next, label: `Catch ${formatErrors(errors)}` })
  }

  const childScopes: string[] = []
  if (type === "Parallel") {
    const branches = Array.isArray(raw.Branches) ? raw.Branches : []
    branches.forEach((branch, index) => {
      if (!isRecord(branch)) return
      const id = `${name}#${index}`
      childScopes.push(id)
      readScope(model, branch, id, name, index, stateLanguage)
    })
  } else if (type === "Map") {
    const processor = isRecord(raw.ItemProcessor)
      ? raw.ItemProcessor
      : isRecord(raw.Iterator)
        ? raw.Iterator
        : undefined
    if (processor) {
      const id = `${name}#items`
      childScopes.push(id)
      readScope(model, processor, id, name, undefined, stateLanguage)
    } else {
      model.issues.push(`Map state "${name}" has no ItemProcessor.`)
    }
  }

  return {
    name,
    type,
    scopeId,
    raw,
    comment: typeof raw.Comment === "string" ? raw.Comment : undefined,
    terminal: raw.End === true || type === "Succeed" || type === "Fail",
    transitions,
    childScopes,
    summary: summarizeState(type, raw),
    distributed: type === "Map" && isDistributed(raw),
  }
}

function isDistributed(raw: Json): boolean {
  const processor = isRecord(raw.ItemProcessor) ? raw.ItemProcessor : undefined
  const config =
    processor && isRecord(processor.ProcessorConfig) ? processor.ProcessorConfig : undefined
  return config?.Mode === "DISTRIBUTED"
}

/** Flags transitions that leave their scope or name no state, and states with nowhere to go. */
function checkTransitions(model: AslModel) {
  for (const state of model.states.values()) {
    for (const t of state.transitions) {
      const target = model.states.get(t.to)
      if (!target) {
        model.issues.push(`State "${state.name}" transitions to "${t.to}", which does not exist.`)
      } else if (target.scopeId !== state.scopeId) {
        model.issues.push(
          `State "${state.name}" transitions to "${t.to}", which is in a different branch.`,
        )
      }
    }
    const leaves = state.terminal || state.transitions.some((t) => t.kind !== "catch")
    if (!leaves && STATE_TYPES.has(state.type)) {
      model.issues.push(`State "${state.name}" has neither Next nor End.`)
    }
  }
}

// ─── Labels and summaries ────────────────────────────────────────────────────

function formatErrors(errors: string[]): string {
  if (errors.length === 0) return ""
  if (errors.length === 1) return errors[0]
  return `${errors[0]} +${errors.length - 1}`
}

const COMPARISON_OPERATORS: Array<[string, string]> = [
  ["StringEquals", "=="],
  ["StringLessThanEquals", "<="],
  ["StringLessThan", "<"],
  ["StringGreaterThanEquals", ">="],
  ["StringGreaterThan", ">"],
  ["StringMatches", "matches"],
  ["NumericEquals", "=="],
  ["NumericLessThanEquals", "<="],
  ["NumericLessThan", "<"],
  ["NumericGreaterThanEquals", ">="],
  ["NumericGreaterThan", ">"],
  ["BooleanEquals", "=="],
  ["TimestampEquals", "=="],
  ["TimestampLessThanEquals", "<="],
  ["TimestampLessThan", "<"],
  ["TimestampGreaterThanEquals", ">="],
  ["TimestampGreaterThan", ">"],
]

const TYPE_TESTS: Record<string, string> = {
  IsNull: "null",
  IsPresent: "present",
  IsNumeric: "numeric",
  IsString: "a string",
  IsBoolean: "a boolean",
  IsTimestamp: "a timestamp",
}

/**
 * Renders a Choice rule as a compact expression — `$.status == "DONE"`,
 * `$.count > 3 && $.ready == true` — or the Condition text in JSONata mode.
 */
export function describeChoiceRule(rule: Json, language = "JSONPath"): string {
  if (language === "JSONata" && typeof rule.Condition === "string") {
    return stripJsonata(rule.Condition)
  }
  if (typeof rule.Condition === "string") return stripJsonata(rule.Condition)
  if (Array.isArray(rule.And)) {
    return rule.And.map((r) => wrap(isRecord(r) ? describeChoiceRule(r, language) : "?")).join(
      " && ",
    )
  }
  if (Array.isArray(rule.Or)) {
    return rule.Or.map((r) => wrap(isRecord(r) ? describeChoiceRule(r, language) : "?")).join(
      " || ",
    )
  }
  if (isRecord(rule.Not)) return `!(${describeChoiceRule(rule.Not, language)})`

  const variable = typeof rule.Variable === "string" ? rule.Variable : "?"
  for (const [op, symbol] of COMPARISON_OPERATORS) {
    if (op in rule) return `${variable} ${symbol} ${JSON.stringify(rule[op])}`
    if (`${op}Path` in rule) return `${variable} ${symbol} ${String(rule[`${op}Path`])}`
  }
  for (const [op, noun] of Object.entries(TYPE_TESTS)) {
    if (op in rule) return `${variable} ${rule[op] === false ? "is not" : "is"} ${noun}`
  }
  return variable
}

function wrap(expression: string): string {
  return expression.includes(" && ") || expression.includes(" || ") ? `(${expression})` : expression
}

function stripJsonata(expression: string): string {
  const trimmed = expression.trim()
  return trimmed.startsWith("{%") && trimmed.endsWith("%}") ? trimmed.slice(2, -2).trim() : trimmed
}

/** "arn:aws:states:::lambda:invoke.waitForTaskToken" → service, action and pattern. */
export function parseTaskResource(resource: string): {
  service: string
  action: string
  pattern?: "sync" | "callback"
} {
  const lambdaArn = /^arn:[^:]+:lambda:[^:]*:[^:]*:function:([^:]+)/.exec(resource)
  if (lambdaArn) return { service: "Lambda", action: lambdaArn[1] }
  const activity = /^arn:[^:]+:states:[^:]*:[^:]*:activity:(.+)$/.exec(resource)
  if (activity) return { service: "Activity", action: activity[1] }
  const integration = /^arn:[^:]+:states:::([^:]+):(.+)$/.exec(resource)
  if (integration) {
    let action = integration[2]
    let pattern: "sync" | "callback" | undefined
    if (action.endsWith(".waitForTaskToken")) {
      pattern = "callback"
      action = action.slice(0, -".waitForTaskToken".length)
    } else if (/\.sync(:\d+)?$/.test(action)) {
      pattern = "sync"
      action = action.replace(/\.sync(:\d+)?$/, "")
    }
    return { service: serviceLabel(integration[1]), action, pattern }
  }
  return { service: "", action: resource }
}

const SERVICE_LABELS: Record<string, string> = {
  lambda: "Lambda",
  sqs: "SQS",
  sns: "SNS",
  dynamodb: "DynamoDB",
  events: "EventBridge",
  states: "Step Functions",
  ecs: "ECS",
  batch: "Batch",
  glue: "Glue",
  athena: "Athena",
  s3: "S3",
  http: "HTTP",
  "aws-sdk": "SDK",
  bedrock: "Bedrock",
}

function serviceLabel(service: string): string {
  return SERVICE_LABELS[service] ?? service
}

function summarizeState(type: string, raw: Json): string {
  switch (type) {
    case "Task": {
      const resource = typeof raw.Resource === "string" ? raw.Resource : ""
      const { service, action, pattern } = parseTaskResource(resource)
      const params = isRecord(raw.Parameters)
        ? raw.Parameters
        : isRecord(raw.Arguments)
          ? raw.Arguments
          : {}
      let target = action
      // Lambda invoke through the optimised integration names the function in its parameters.
      const fn = params.FunctionName ?? params["FunctionName.$"]
      if (service === "Lambda" && action === "invoke" && typeof fn === "string") {
        target = fn.split(":function:").pop()?.split(":")[0] ?? fn
      }
      const suffix = pattern === "sync" ? " · sync" : pattern === "callback" ? " · callback" : ""
      return service ? `${service} · ${target}${suffix}` : target || "Task"
    }
    case "Wait": {
      if (raw.Seconds !== undefined) return `Wait ${String(raw.Seconds)}s`
      if (typeof raw.SecondsPath === "string") return `Wait ${raw.SecondsPath} seconds`
      if (typeof raw.Timestamp === "string") return `Until ${raw.Timestamp}`
      if (typeof raw.TimestampPath === "string") return `Until ${raw.TimestampPath}`
      return "Wait"
    }
    case "Choice": {
      const count = Array.isArray(raw.Choices) ? raw.Choices.length : 0
      return `${formatQuantity(count, "rule")}${typeof raw.Default === "string" ? " + default" : ""}`
    }
    case "Parallel": {
      const count = Array.isArray(raw.Branches) ? raw.Branches.length : 0
      return `${formatQuantity(count, "branch", "branches")} in parallel`
    }
    case "Map": {
      const items =
        typeof raw.ItemsPath === "string"
          ? raw.ItemsPath
          : typeof raw.Items === "string"
            ? stripJsonata(raw.Items)
            : isRecord(raw.ItemReader)
              ? "items from S3"
              : "$"
      const mode = isDistributed(raw) ? "Distributed · " : ""
      const concurrency =
        typeof raw.MaxConcurrency === "number" && raw.MaxConcurrency > 0
          ? ` · max ${raw.MaxConcurrency}`
          : ""
      return `${mode}for each in ${items}${concurrency}`
    }
    case "Pass":
      return raw.Result !== undefined ? "Inject result" : "Pass input through"
    case "Succeed":
      return "End successfully"
    case "Fail": {
      const error =
        typeof raw.Error === "string"
          ? raw.Error
          : typeof raw.ErrorPath === "string"
            ? raw.ErrorPath
            : ""
      return error ? `Fail · ${error}` : "Fail the execution"
    }
    default:
      return type
  }
}

/** The container state that directly owns a state, if any. */
export function parentContainer(model: AslModel, stateName: string): string | undefined {
  return model.scopes.get(model.states.get(stateName)?.scopeId ?? "")?.container
}
