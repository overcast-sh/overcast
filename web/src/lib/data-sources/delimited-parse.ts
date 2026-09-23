/**
 * CSV and TSV: an RFC 4180 reader, used by the data worker to parse one block
 * of rows at a time (and the header), plus delimiter sniffing.
 *
 * Hand-written rather than a dependency because the job is small and bounded
 * — at most 1 MiB of text, read once, into strings — and every candidate
 * library carries what this does not need (streaming, workers, type casting,
 * a writer). The parser is one state machine over the text; what it accepts
 * is spelled out on `parseDelimited`.
 */

export type Delimiter = "," | "\t" | ";" | "|"

const CANDIDATES: readonly Delimiter[] = [",", "\t", ";", "|"]

/** Characters in one field before the rest is dropped (see `DelimitedParse.clippedFields`). */
export const MAX_FIELD_CHARS = 64 * 1024

export interface DelimitedParse {
  /** Every complete record read, header included, up to `maxRecords`. */
  records: string[][]
  /** Complete records in the text, counted past `maxRecords`. */
  recordCount: number
  /** Characters of the text the counted records span — the basis of a size estimate. */
  consumedChars: number
  /** Fields longer than `MAX_FIELD_CHARS`, kept only up to it. */
  clippedFields: number
  /**
   * Why the text is not CSV after all, when it is not. The table is withheld
   * and the raw text shown with this as the note, rather than a table that
   * silently misreads the file.
   */
  malformed?: string
}

interface ParseOptions {
  delimiter: string
  /** Records kept in `records`; the rest are only counted. */
  maxRecords: number
  /**
   * The text is the opening window of a longer object. Its last record is
   * almost certainly cut short — mid-field, even mid-quote — so it is dropped
   * rather than shown as if the file ended there, and an open quote at the
   * end is the cut's doing, not the file's.
   */
  truncated: boolean
}

/**
 * Reads delimited text as RFC 4180 describes it, with the leniencies real
 * files need:
 *
 * - a field may be quoted; inside quotes the delimiter, CR and LF are data and
 *   `""` is one quote;
 * - records end at CRLF, LF or a lone CR;
 * - a leading UTF-8 byte-order mark is dropped;
 * - a quote in the middle of an unquoted field is kept as a character, as
 *   spreadsheet exports produce (`5" floppy`);
 * - text after a closing quote is kept too (`"a"b` reads `ab`) — lenient,
 *   because refusing it would hide an otherwise readable file;
 * - a blank line is not a record.
 *
 * What it will not do is guess where a quoted field ends: a quote left open
 * at the end of a complete object is `malformed`.
 */
export function parseDelimited(text: string, options: ParseOptions): DelimitedParse {
  const { delimiter, maxRecords, truncated } = options
  const records: string[][] = []
  let recordCount = 0
  let consumedChars = 0
  let clippedFields = 0

  let record: string[] = []
  let field = ""
  let fieldClipped = false
  let inQuotes = false
  let quotedField = false
  // Whether the field just ended was quoted: `""` on a line of its own is a
  // record holding one empty string, where an empty line is no record at all.
  let lastFieldWasQuoted = false
  let i = text.charCodeAt(0) === 0xfeff ? 1 : 0
  const n = text.length

  const append = (chunk: string) => {
    if (fieldClipped) return
    const room = MAX_FIELD_CHARS - field.length
    if (chunk.length <= room) {
      field += chunk
    } else {
      field += chunk.slice(0, room)
      fieldClipped = true
    }
  }
  const endField = () => {
    lastFieldWasQuoted = quotedField
    record.push(field)
    if (fieldClipped) clippedFields++
    field = ""
    fieldClipped = false
    quotedField = false
  }
  const endRecord = (end: number) => {
    endField()
    // A blank line is one empty unquoted field; it separates, it is not data.
    const blank = record.length === 1 && record[0] === "" && !lastFieldWasQuoted
    if (!blank) {
      if (records.length < maxRecords) records.push(record)
      recordCount++
    }
    consumedChars = end
    record = []
  }

  while (i < n) {
    if (inQuotes) {
      // Copy the run up to the next quote in one step: fields are mostly
      // plain text, and char-at-a-time concatenation is the slow path.
      const close = text.indexOf('"', i)
      if (close === -1) {
        append(text.slice(i))
        break
      }
      append(text.slice(i, close))
      if (text.charCodeAt(close + 1) === 0x22) {
        append('"')
        i = close + 2
      } else {
        inQuotes = false
        i = close + 1
      }
      continue
    }
    const c = text[i]
    if (c === delimiter) {
      endField()
      i++
    } else if (c === "\n" || c === "\r") {
      const next = c === "\r" && text[i + 1] === "\n" ? i + 2 : i + 1
      endRecord(next)
      i = next
    } else if (c === '"' && field.length === 0 && !quotedField) {
      inQuotes = true
      quotedField = true
      i++
    } else {
      // Plain run: everything up to the next delimiter, quote or line end.
      let j = i + 1
      while (j < n) {
        const d = text[j]
        if (d === delimiter || d === "\n" || d === "\r" || d === '"') break
        j++
      }
      // A quote here is mid-field (the start-of-field case is above), so it
      // is a character like any other.
      if (j < n && text[j] === '"') j++
      append(text.slice(i, j))
      i = j
    }
  }

  if (inQuotes && !truncated) {
    return {
      records,
      recordCount,
      consumedChars,
      clippedFields,
      malformed: "A quoted field is never closed, so the file cannot be split into rows reliably.",
    }
  }
  // The text ran out without a final line break. For a whole object that is
  // just a file with no trailing newline; for a truncated window it is the
  // record the cut went through, and it is dropped.
  const pending = record.length > 0 || field.length > 0 || quotedField
  if (pending && !truncated && !inQuotes) {
    endRecord(n)
  }
  return { records, recordCount, consumedChars, clippedFields }
}

/**
 * Picks the delimiter from the text itself: the candidate that splits the
 * most sample lines into the same number of fields (more than one), then the
 * one giving more fields. `preferred` — what the key or content type says —
 * wins a tie, and is the answer when nothing splits at all (a one-column
 * file). European exports with `;` in a `.csv`, and `.txt` files that are
 * really TSV, are the cases this is for.
 */
export function sniffDelimiter(text: string, preferred?: Delimiter): Delimiter {
  const sample = parseSampleLines(text)
  const score = (d: Delimiter) => {
    const counts = sample.map((line) => fieldCount(line, d))
    const first = counts[0] ?? 0
    if (first < 2) return 0
    const consistent = counts.filter((c) => c === first).length
    return consistent * 1000 + first
  }
  let best: Delimiter = preferred ?? ","
  let bestScore = preferred ? score(preferred) : 0
  for (const d of CANDIDATES) {
    const s = score(d)
    if (s > bestScore) {
      best = d
      bestScore = s
    }
  }
  return best
}

/** Up to ten complete lines, quote-aware enough that an embedded newline does not split one. */
function parseSampleLines(text: string): string[] {
  const lines: string[] = []
  let start = text.charCodeAt(0) === 0xfeff ? 1 : 0
  let inQuotes = false
  for (let i = start; i < text.length && lines.length < 10; i++) {
    const c = text[i]
    if (c === '"') inQuotes = !inQuotes
    else if ((c === "\n" || c === "\r") && !inQuotes) {
      if (i > start) lines.push(text.slice(start, i))
      if (c === "\r" && text[i + 1] === "\n") i++
      start = i + 1
    }
  }
  return lines
}

function fieldCount(line: string, delimiter: string): number {
  let count = 1
  let inQuotes = false
  for (const c of line) {
    if (c === '"') inQuotes = !inQuotes
    else if (c === delimiter && !inQuotes) count++
  }
  return count
}

/**
 * Names for a header record: blank names become `column_N` and a repeated
 * name is suffixed, so every column can be told apart. `width` may exceed the
 * header — a row longer than it gets `column_N` for the extra fields.
 */
export function columnNames(header: readonly string[], width: number): string[] {
  const seen = new Map<string, number>()
  return Array.from({ length: width }, (_, index) => {
    const raw = (header[index] ?? "").trim()
    const base = raw === "" ? `column_${index + 1}` : raw
    const count = seen.get(base) ?? 0
    seen.set(base, count + 1)
    return count === 0 ? base : `${base}_${count + 1}`
  })
}
