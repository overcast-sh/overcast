import { utf8BomLength } from "./byte-order-mark"

/**
 * The sparse byte-offset index of a CSV, TSV or JSON Lines file.
 *
 * It reads the file as it streams, byte by byte — never as text, never whole —
 * and records where every `every`-th record starts. With one offset per 1,000
 * records a million-row file indexes into a thousand numbers (8 KB), and any
 * block of rows is then one Range request: the bytes between two offsets.
 *
 * **Quote-aware**, because a record is not a line: a quoted CSV field can hold
 * newlines. It runs the same quote rules `parseDelimited` does — a quote opens
 * a quoted field only at the start of a field, `""` inside one is a literal
 * quote, and a quote anywhere else is just a character — so the index and the
 * block parser can never disagree about where row 50,000 is. That is why it
 * needs the delimiter: "the start of a field" means "just after one".
 *
 * JSON Lines is read the way `parseJsonl` reads it (`delimiter: null`): without
 * quote tracking — a JSON string cannot hold a raw newline, and JSON's own
 * `\"` escape is not CSV's — with records ending at LF only, and a line of
 * nothing but whitespace (CR included) not a record.
 *
 * CSV and TSV records end at LF, CRLF or a lone CR; a blank line is not a
 * record, a record holding only `""` is one. A leading UTF-8 BOM is skipped.
 *
 * Offsets are absolute positions in the object, and the indexer keeps its
 * place mid-record between pushes — so an index paused at a byte limit
 * resumes by pushing the bytes from `bytes` on, fetched with an open-ended
 * Range request.
 */

const LF = 0x0a
const CR = 0x0d
const QUOTE = 0x22
const SPACE = 0x20
const TAB = 0x09

export interface IndexerOptions {
  /** Record one offset per this many data records. */
  every: number
  /** The field delimiter's byte (CSV/TSV), or null to track no quotes (JSON Lines). */
  delimiter: number | null
  /** The first record is a header, not data (CSV/TSV). */
  header: boolean
  /** A blank line is a record: one NULL, in a CSV whose values are all quoted. */
  blankRecords?: boolean
}

export class RecordIndexer {
  /** `offsets[k]` is where data record `k * every` starts. */
  readonly offsets: number[] = []
  /** Data records seen so far. */
  rows = 0
  /** Where the header record ends (the first data record starts), once read. */
  headerEnd: number | undefined
  readonly options: IndexerOptions

  private position = 0
  private recordStart = 0
  private hasContent = false
  private inQuotes = false
  /** A quote just closed a quoted field — unless the next byte is a quote too (`""`). */
  private quoteClosing = false
  private fieldStart = true
  private pendingCR = false
  private headerDone: boolean

  constructor(options: IndexerOptions) {
    this.options = options
    this.headerDone = !options.header
  }

  /** Offset just past the last complete record. */
  get end(): number {
    return this.recordStart
  }

  /** Bytes consumed so far (an absolute offset). */
  get bytes(): number {
    return this.position
  }

  /** An unclosed quote at the end of the object: the file is not valid CSV. */
  get unterminatedQuote(): boolean {
    return this.inQuotes
  }

  push(chunk: Uint8Array): void {
    const n = chunk.length
    const base = this.position
    let i = base === 0 ? utf8BomLength(chunk) : 0
    if (i > 0) this.recordStart = i
    const delimiter = this.options.delimiter
    if (delimiter === null) {
      this.pushLines(chunk, i, base)
      this.position = base + n
      return
    }
    for (; i < n; i++) {
      const b = chunk[i]
      if (this.pendingCR) {
        this.pendingCR = false
        if (b === LF) {
          this.endRecord(base + i + 1)
          continue
        }
        // A lone CR ended the record at the byte before this one.
        this.endRecord(base + i)
      }
      if (this.inQuotes) {
        if (b === QUOTE) {
          this.inQuotes = false
          this.quoteClosing = true
        }
        continue
      }
      if (this.quoteClosing) {
        this.quoteClosing = false
        if (b === QUOTE) {
          // `""` inside a quoted field: a literal quote, still quoted.
          this.inQuotes = true
          continue
        }
      }
      if (b === QUOTE && this.fieldStart) {
        this.inQuotes = true
        this.fieldStart = false
        this.hasContent = true
        continue
      }
      if (b === delimiter) {
        this.fieldStart = true
        this.hasContent = true
        continue
      }
      if (b === LF) {
        this.endRecord(base + i + 1)
      } else if (b === CR) {
        this.pendingCR = true
      } else {
        this.hasContent = true
        this.fieldStart = false
      }
    }
    this.position = base + n
  }

  /** JSON Lines: a record per LF-ended line holding anything but whitespace. */
  private pushLines(chunk: Uint8Array, from: number, base: number): void {
    for (let i = from; i < chunk.length; i++) {
      const b = chunk[i]
      if (b === LF) this.endRecord(base + i + 1)
      else if (b !== SPACE && b !== TAB && b !== CR) this.hasContent = true
    }
  }

  /** The object ended: a final record without a line break still counts. */
  finish(): void {
    this.quoteClosing = false
    if (this.pendingCR) {
      this.pendingCR = false
      this.endRecord(this.position)
    }
    if (this.hasContent && !this.inQuotes) this.endRecord(this.position)
  }

  private endRecord(end: number): void {
    if (this.hasContent || this.options.blankRecords) {
      if (!this.headerDone) {
        this.headerDone = true
        this.headerEnd = end
      } else {
        if (this.rows % this.options.every === 0) this.offsets.push(this.recordStart)
        this.rows++
      }
    }
    this.recordStart = end
    this.hasContent = false
    this.fieldStart = true
  }
}
