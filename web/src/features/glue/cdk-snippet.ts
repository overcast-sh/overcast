import type { TableInput } from "@aws-sdk/client-glue"
import { isRecord } from "@/lib/utils"

/**
 * *Copy as CDK*: the `CfnTable` that creates the same table from a CDK app,
 * so the table a developer made in the console is one line of review away
 * from being in their stack. CloudFormation's `TableInput` is the Glue API's
 * with camelCase keys, and a map-valued `Parameters` keeps its keys as
 * written — they are data, not properties.
 */

const INDENT = "  "
const IDENTIFIER = /^[A-Za-z_$][\w$]*$/

function lowerFirst(key: string): string {
  return key.charAt(0).toLowerCase() + key.slice(1)
}

function key(name: string): string {
  return IDENTIFIER.test(name) ? name : JSON.stringify(name)
}

/** A TypeScript literal for `value`. `data` marks a map whose keys are left as written. */
function literal(value: unknown, depth: number, data = false): string {
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]"
    const pad = INDENT.repeat(depth + 1)
    const items = value.map((v) => `${pad}${literal(v, depth + 1)},`)
    return `[\n${items.join("\n")}\n${INDENT.repeat(depth)}]`
  }
  if (isRecord(value)) {
    const entries = Object.entries(value).filter(([, v]) => v !== undefined)
    if (entries.length === 0) return "{}"
    const pad = INDENT.repeat(depth + 1)
    const lines = entries.map(([k, v]) => {
      const name = data ? key(k) : lowerFirst(k)
      return `${pad}${name}: ${literal(v, depth + 1, !data && k === "Parameters")},`
    })
    return `{\n${lines.join("\n")}\n${INDENT.repeat(depth)}}`
  }
  return JSON.stringify(value)
}

/** `orders_2026` → `Orders2026Table`: a construct id from the table's name. */
function constructId(tableName: string): string {
  const words = tableName.split(/[^A-Za-z0-9]+/).filter(Boolean)
  const pascal = words.map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join("")
  return `${/^[0-9]/.test(pascal) ? "T" : ""}${pascal}Table`
}

export function cfnTableSnippet(databaseName: string, tableInput: TableInput): string {
  return `import * as glue from "aws-cdk-lib/aws-glue"

new glue.CfnTable(this, ${JSON.stringify(constructId(tableInput.Name ?? "data"))}, {
  catalogId: this.account,
  databaseName: ${JSON.stringify(databaseName)},
  tableInput: ${literal(tableInput, 1)},
})
`
}
