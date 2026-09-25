#!/usr/bin/env node
/**
 * Generates src/features/athena/trino-functions.gen.json: every function
 * the Athena editor completes, with its signatures and description, read
 * from the Trino version Overcast's query engine runs.
 *
 * The version is pinned, not assumed. The engine's image is
 * `DefaultAthenaEngineImage` in internal/config/config.go
 * (`trinodb/trino:<version>@sha256:…`); this script refuses to write the list
 * from a Trino that reports any other version, and records the version in the
 * file. `trino-functions.test.ts` fails when the config's version moves on
 * without the list, so bumping the engine means regenerating it.
 *
 * Usage, with the engine's own image (digest from the config):
 *
 *   docker run -d --name trino-functions -p 127.0.0.1:18080:8080 trinodb/trino@sha256:…
 *   node scripts/generate-trino-functions.mjs http://127.0.0.1:18080
 *   docker rm -f trino-functions
 */

import { readFileSync, writeFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const here = dirname(fileURLToPath(import.meta.url))
const CONFIG = resolve(here, "../../internal/config/config.go")
const OUTPUT = resolve(here, "../src/features/athena/trino-functions.gen.json")

/** The Trino version the engine image is pinned to. */
export function pinnedTrinoVersion(configSource) {
  const match = /DefaultAthenaEngineImage\s*=\s*"trinodb\/trino:(\d+)@/.exec(configSource)
  if (!match) throw new Error("DefaultAthenaEngineImage not found in internal/config/config.go")
  return match[1]
}

async function trinoJson(url, init) {
  const res = await fetch(url, init)
  if (!res.ok) throw new Error(`${init?.method ?? "GET"} ${url}: HTTP ${res.status}`)
  return res.json()
}

/** Runs one statement and returns its columns and every row, following `nextUri`. */
async function query(server, sql) {
  const headers = { "X-Trino-User": "overcast", "X-Trino-Source": "generate-trino-functions" }
  let page = await trinoJson(`${server}/v1/statement`, { method: "POST", body: sql, headers })
  let columns
  const rows = []
  for (;;) {
    if (page.error) throw new Error(`${sql}: ${page.error.message}`)
    columns ??= page.columns
    rows.push(...(page.data ?? []))
    if (!page.nextUri) break
    page = await trinoJson(page.nextUri, { headers })
  }
  return { columns: (columns ?? []).map((c) => c.name), rows }
}

/** Waits for a freshly started Trino to accept queries. */
async function waitForTrino(server) {
  for (let attempt = 0; attempt < 120; attempt++) {
    try {
      const info = await trinoJson(`${server}/v1/info`)
      if (!info.starting) return info
    } catch {
      // Not listening yet.
    }
    await new Promise((r) => setTimeout(r, 1000))
  }
  throw new Error(`Trino at ${server} did not start`)
}

async function main() {
  const server = (process.argv[2] ?? "http://127.0.0.1:18080").replace(/\/$/, "")
  const pinned = pinnedTrinoVersion(readFileSync(CONFIG, "utf8"))
  const info = await waitForTrino(server)
  const version = String(info.nodeVersion?.version ?? "")
  if (version !== pinned) {
    throw new Error(`Trino at ${server} is ${version}; the engine is pinned to ${pinned}`)
  }

  const { columns, rows } = await query(server, "SHOW FUNCTIONS")
  const col = (name) => {
    const i = columns.indexOf(name)
    if (i < 0) throw new Error(`SHOW FUNCTIONS has no ${name} column (has ${columns.join(", ")})`)
    return i
  }
  const [fn, ret, args, kind, description] = [
    col("Function"),
    col("Return Type"),
    col("Argument Types"),
    col("Function Type"),
    col("Description"),
  ]

  const byName = new Map()
  for (const row of rows) {
    const name = row[fn]
    const entry = byName.get(name) ?? { name, kind: row[kind], signatures: [], description: "" }
    const signature = `${name}(${row[args]}) → ${row[ret]}`
    if (!entry.signatures.includes(signature)) entry.signatures.push(signature)
    entry.description ||= row[description] ?? ""
    byName.set(name, entry)
  }
  // Operators ($operator$…) and internal functions ($…) are not callable by name.
  const functions = [...byName.values()]
    .filter((f) => /^[a-z_][a-z0-9_]*$/.test(f.name))
    .sort((a, b) => a.name.localeCompare(b.name))

  writeFileSync(OUTPUT, `${JSON.stringify({ trinoVersion: version, functions }, null, 1)}\n`)
  console.log(`Wrote ${functions.length} Trino ${version} functions to ${OUTPUT}`)
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error.message)
    process.exit(1)
  })
}
