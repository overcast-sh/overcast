/**
 * JSON as text: reformatting and parsing that keep what the file said.
 *
 * `JSON.parse` reads every number as a double, so an integer past 2^53 comes
 * back rounded: Iceberg's snapshot id `3051729675574597004` becomes
 * `3051729675574597000`, a snapshot that does not exist. `JSON.stringify`
 * then writes the rounded value back out. Both helpers here work on the text
 * instead, so a viewer shows the file's own digits and a reader gets the id
 * it can look up.
 */

const WHITESPACE = new Set([" ", "\t", "\n", "\r"])
/** Characters that end a bare literal (`true`, `null`, a number). */
const LITERAL_END = new Set([...WHITESPACE, ",", ":", "]", "}"])

/** The index just past the string literal that opens at `start`. */
function stringEnd(text: string, start: number): number {
  let i = start + 1
  while (i < text.length) {
    const c = text[i]
    if (c === "\\") i += 2
    else if (c === '"') return i + 1
    else i++
  }
  return i
}

/** The index just past the bare literal that starts at `start`. */
function literalEnd(text: string, start: number): number {
  let i = start
  while (i < text.length && !LITERAL_END.has(text[i])) i++
  return i
}

function nextNonSpace(text: string, start: number): number {
  let i = start
  while (i < text.length && WHITESPACE.has(text[i])) i++
  return i
}

/**
 * `text` re-indented the way `JSON.stringify(value, null, 2)` lays it out,
 * with every string and number spelled exactly as the source spelled it.
 * Throws a `SyntaxError` for text that is not JSON.
 */
export function reindentJson(text: string, indent = "  "): string {
  JSON.parse(text)
  const out: string[] = []
  let depth = 0
  let i = 0
  while (i < text.length) {
    const c = text[i]
    if (WHITESPACE.has(c)) {
      i++
    } else if (c === '"') {
      const end = stringEnd(text, i)
      out.push(text.slice(i, end))
      i = end
    } else if (c === "{" || c === "[") {
      const close = c === "{" ? "}" : "]"
      const next = nextNonSpace(text, i + 1)
      if (text[next] === close) {
        out.push(c + close)
        i = next + 1
      } else {
        depth++
        out.push(c, "\n", indent.repeat(depth))
        i++
      }
    } else if (c === "}" || c === "]") {
      depth--
      out.push("\n", indent.repeat(depth), c)
      i++
    } else if (c === ",") {
      out.push(",\n", indent.repeat(depth))
      i++
    } else if (c === ":") {
      out.push(": ")
      i++
    } else {
      const end = literalEnd(text, i)
      out.push(text.slice(i, end))
      i = end
    }
  }
  return out.join("")
}

/**
 * `JSON.parse`, except that an integer too large for a double to hold exactly
 * arrives as its decimal string. Everything else parses as usual, so a reader
 * that expects an id as a string should also accept a (small) number.
 */
export function parseJsonKeepingLargeIntegers(text: string): unknown {
  const out: string[] = []
  let from = 0
  let i = 0
  while (i < text.length) {
    const c = text[i]
    if (c === '"') {
      i = stringEnd(text, i)
    } else if (c === "-" || (c >= "0" && c <= "9")) {
      const end = literalEnd(text, i)
      const literal = text.slice(i, end)
      if (/^-?\d+$/.test(literal) && !Number.isSafeInteger(Number(literal))) {
        out.push(text.slice(from, i), `"${literal}"`)
        from = end
      }
      i = end
    } else {
      i++
    }
  }
  out.push(text.slice(from))
  return JSON.parse(out.join(""))
}
