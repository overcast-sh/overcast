import { readFileSync, writeFileSync } from "node:fs"
import { resolve } from "node:path"
import { connectSnippets, DOCS_CONNECT_TARGET, type ConnectClient } from "./connect-snippets"

/**
 * The docs and the console's *Connect a client* panel show the same
 * snippets, from the same source. Each one sits in a page under a
 * `<!-- connect-snippet:<client> -->` marker; this test fails when a block no
 * longer matches what `connectSnippets` generates for the documented bucket,
 * or when a page is missing a client it lists below.
 *
 * To regenerate the pages after changing a snippet:
 *   UPDATE_DOCS=1 pnpm vitest run connect-snippets.docs
 */

const DOCS_ROOT = resolve(__dirname, "../../../../docs")

/** Each page that carries snippets, and the clients it shows. */
const PAGES: { path: string; clients: readonly ConnectClient[] }[] = [
  {
    path: "services/s3tables/iceberg-rest.md",
    clients: ["pyiceberg", "spark", "trino", "duckdb", "aws-cli"],
  },
  { path: "iceberg-locally.md", clients: ["pyiceberg", "spark"] },
]

/** The marker and opening fence, the block's lines, and the closing fence on a line of its own. */
function blockPattern(client: string): RegExp {
  return new RegExp(
    `(<!-- connect-snippet:${client} -->\\n\`\`\`[a-z]*\\n)([\\s\\S]*?)(^\`\`\`$)`,
    "m",
  )
}

const snippets = connectSnippets(DOCS_CONNECT_TARGET)

function snippetsOn(page: (typeof PAGES)[number]) {
  return snippets.filter((s) => page.clients.includes(s.client))
}

if (process.env.UPDATE_DOCS) {
  for (const page of PAGES) {
    const file = resolve(DOCS_ROOT, page.path)
    let text = readFileSync(file, "utf8")
    for (const snippet of snippetsOn(page)) {
      text = text.replace(blockPattern(snippet.client), (_, open: string, __, close: string) => {
        const fence = open.replace(/```[a-z]*/, `\`\`\`${snippet.language}`)
        return `${fence}${snippet.code}
${close}`
      })
    }
    writeFileSync(file, text)
  }
}

describe.each(PAGES)("the docs page $path", (page) => {
  const text = readFileSync(resolve(DOCS_ROOT, page.path), "utf8")

  it.each(snippetsOn(page).map((s) => [s.label, s] as const))(
    "shows the %s snippet the console generates",
    (_, snippet) => {
      const block = blockPattern(snippet.client).exec(text)
      expect(
        block,
        `no <!-- connect-snippet:${snippet.client} --> block in ${page.path}`,
      ).not.toBeNull()
      expect(block?.[1]).toContain(`\`\`\`${snippet.language}\n`)
      expect(block?.[2]).toBe(`${snippet.code}
`)
    },
  )
})
