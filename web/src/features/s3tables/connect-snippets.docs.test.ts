import { readFileSync, writeFileSync } from "node:fs"
import { resolve } from "node:path"
import { connectSnippets, DOCS_CONNECT_TARGET } from "./connect-snippets"

/**
 * The docs page and the console's *Connect a client* panel show the same
 * snippets, from the same source. Each one sits in the page under a
 * `<!-- connect-snippet:<client> -->` marker; this test fails when a block no
 * longer matches what `connectSnippets` generates for the documented bucket.
 *
 * To regenerate the page after changing a snippet:
 *   UPDATE_DOCS=1 pnpm vitest run connect-snippets.docs
 */

const DOCS = resolve(__dirname, "../../../../docs/services/s3tables/iceberg-rest.md")

/** The marker and opening fence, the block's lines, and the closing fence on a line of its own. */
function blockPattern(client: string): RegExp {
  return new RegExp(
    `(<!-- connect-snippet:${client} -->\\n\`\`\`[a-z]*\\n)([\\s\\S]*?)(^\`\`\`$)`,
    "m",
  )
}

const snippets = connectSnippets(DOCS_CONNECT_TARGET)

if (process.env.UPDATE_DOCS) {
  let page = readFileSync(DOCS, "utf8")
  for (const snippet of snippets) {
    page = page.replace(blockPattern(snippet.client), (_, open: string, __, close: string) => {
      const fence = open.replace(/```[a-z]*/, `\`\`\`${snippet.language}`)
      return `${fence}${snippet.code}
${close}`
    })
  }
  writeFileSync(DOCS, page)
}

describe("the S3 Tables Iceberg REST docs", () => {
  const page = readFileSync(DOCS, "utf8")

  it.each(snippets.map((s) => [s.label, s] as const))(
    "show the %s snippet the console generates",
    (_, snippet) => {
      const block = blockPattern(snippet.client).exec(page)
      expect(
        block,
        `no <!-- connect-snippet:${snippet.client} --> block in the docs`,
      ).not.toBeNull()
      expect(block?.[1]).toContain(`\`\`\`${snippet.language}\n`)
      expect(block?.[2]).toBe(`${snippet.code}
`)
    },
  )
})
