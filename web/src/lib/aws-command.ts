/**
 * An AWS API call written out as the code a developer would run it with —
 * the AWS CLI, boto3 or the JavaScript SDK v3 — pointed at this emulator.
 *
 * Generic: a page describes the call it just made (service, operation, the
 * request's input as the SDK takes it) and gets back a snippet with the
 * endpoint and region filled in, so what it copies runs as it stands.
 *
 * ```ts
 * const call = { service: ATHENA, operation: "StartQueryExecution", input: { QueryString: sql } }
 * awsCommand(call, "cli", endpointStore.get())
 * // aws athena start-query-execution --endpoint-url http://localhost:4566 --region us-east-1 \
 * //   --query-string 'SELECT 1'
 * ```
 */

import type { CopyOptions } from "@/hooks/use-clipboard"
import { endpointStore } from "@/services/endpoint-store"

/** How one service is named by each tool. */
export interface AwsService {
  /** The CLI's and boto3's name: `athena`, `s3tables`. */
  cli: string
  /** The SDK v3 package: `@aws-sdk/client-athena`. */
  sdkPackage: string
  /** The SDK v3 client class: `AthenaClient`. */
  sdkClient: string
}

export interface AwsCall {
  service: AwsService
  /** The operation's API name: `StartQueryExecution`. */
  operation: string
  /** The request as the SDK takes it. Undefined members are left out. */
  input: Record<string, unknown>
}

export type AwsCommandFlavor = "cli" | "boto3" | "sdk-v3"

/** Each flavour's menu label and the noun its copy toast names. */
export const AWS_COMMAND_FLAVORS: Record<AwsCommandFlavor, { label: string; noun: string }> = {
  cli: { label: "Copy as AWS CLI", noun: "AWS CLI command" },
  boto3: { label: "Copy as boto3", noun: "boto3 snippet" },
  "sdk-v3": { label: "Copy as SDK v3 (JS)", noun: "SDK v3 snippet" },
}

export interface AwsCommandTarget {
  endpoint: string
  region: string
}

/**
 * `StartQueryExecution` → `["start", "query", "execution"]`, and
 * `DescribeDBInstances` → `["describe", "db", "instances"]`: the CLI's and
 * boto3's spelling splits a run of capitals before its last one, which
 * `wordsFromIdentifier` does not.
 */
function words(name: string): string[] {
  return name
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .toLowerCase()
    .split(" ")
}

/** The input's members that are set, in the order the caller wrote them. */
function members(input: Record<string, unknown>): [string, unknown][] {
  return Object.entries(input).filter(([, value]) => value !== undefined)
}

/** A POSIX shell word: single-quoted unless it needs no quoting at all. */
function shellWord(text: string): string {
  return /^[\w@%+=:,./-]+$/.test(text) ? text : `'${text.replaceAll("'", `'\\''`)}'`
}

function cliCommand({ service, operation, input }: AwsCall, target: AwsCommandTarget): string {
  const lines = [
    `aws ${service.cli} ${words(operation).join("-")}`,
    `--endpoint-url ${shellWord(target.endpoint)}`,
    `--region ${shellWord(target.region)}`,
    ...members(input).map(([name, value]) => {
      // Strings go as they are; lists, structures, numbers and booleans as
      // JSON, which the CLI accepts for any parameter.
      const text = typeof value === "string" ? value : JSON.stringify(value)
      return `--${words(name).join("-")} ${shellWord(text)}`
    }),
  ]
  return lines.join(" \\\n  ")
}

/** A value as a Python literal. JSON's string escapes are Python's too. */
function pythonLiteral(value: unknown, indent: string): string {
  if (value === null || value === undefined) return "None"
  if (typeof value === "boolean") return value ? "True" : "False"
  if (typeof value === "string" || typeof value === "number") return JSON.stringify(value)
  const inner = `${indent}    `
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]"
    return `[\n${value.map((v) => `${inner}${pythonLiteral(v, inner)},`).join("\n")}\n${indent}]`
  }
  const entries = members(value as Record<string, unknown>)
  if (entries.length === 0) return "{}"
  const body = entries.map(([k, v]) => `${inner}${JSON.stringify(k)}: ${pythonLiteral(v, inner)},`)
  return `{\n${body.join("\n")}\n${indent}}`
}

function boto3Snippet({ service, operation, input }: AwsCall, target: AwsCommandTarget): string {
  const args = members(input).map(([name, value]) => `    ${name}=${pythonLiteral(value, "    ")},`)
  return [
    "import boto3",
    "",
    `client = boto3.client(`,
    `    ${JSON.stringify(service.cli)},`,
    `    endpoint_url=${JSON.stringify(target.endpoint)},`,
    `    region_name=${JSON.stringify(target.region)},`,
    ")",
    `response = client.${words(operation).join("_")}(${args.length ? `\n${args.join("\n")}\n` : ""})`,
  ].join("\n")
}

function sdkV3Snippet({ service, operation, input }: AwsCall, target: AwsCommandTarget): string {
  const command = `${operation}Command`
  const body = JSON.stringify(Object.fromEntries(members(input)), null, 2)
  return [
    `import { ${service.sdkClient}, ${command} } from ${JSON.stringify(service.sdkPackage)}`,
    "",
    `const client = new ${service.sdkClient}({`,
    `  endpoint: ${JSON.stringify(target.endpoint)},`,
    `  region: ${JSON.stringify(target.region)},`,
    "})",
    `const response = await client.send(new ${command}(${body}))`,
  ].join("\n")
}

const RENDERERS: Record<AwsCommandFlavor, (call: AwsCall, target: AwsCommandTarget) => string> = {
  cli: cliCommand,
  boto3: boto3Snippet,
  "sdk-v3": sdkV3Snippet,
}

/** The call as a runnable snippet in `flavor`, aimed at `target`. */
export function awsCommand(
  call: AwsCall,
  flavor: AwsCommandFlavor,
  target: AwsCommandTarget,
): string {
  return RENDERERS[flavor](call, target)
}

/** The emulator the console is talking to now, as a snippet should reach it. */
export function currentAwsTarget(): AwsCommandTarget {
  const { baseUrl, region } = endpointStore.get()
  return { endpoint: baseUrl || window.location.origin, region }
}

/**
 * Copies the call as `flavor` against the current emulator. `copy` is
 * `useCopyToClipboard().copy`, so the toast and the fallback are the app's.
 */
export function copyAwsCommand(
  copy: (text: string, options?: CopyOptions) => void,
  call: AwsCall,
  flavor: AwsCommandFlavor,
): void {
  copy(awsCommand(call, flavor, currentAwsTarget()), { noun: AWS_COMMAND_FLAVORS[flavor].noun })
}
