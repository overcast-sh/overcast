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
 * JSON Lines is read without quote tracking (`delimiter: null`): a JSON string
 * cannot hold a raw newline, and JSON's own `\"` escape is not CSV's.
 *
 * Records end at LF, CRLF or a lone CR; a blank line is not a record, a record
 * holding only `""` is one, and a leading UTF-8 BOM is skipped. Offsets are
 * absolute positions in the object, so an index paused at a byte limit
 * resumes from `resumeState()` with a Range request.
 */

const LF = 0x0a
const CR = 0x0d
const QUOTE = 0x22

export interface IndexerOptions {
  /** Record one offset per this many data records. */
  every: number
  /** The field delimiter's byte (CSV/TSV), or null to track no quotes (JSON Lines). */
  delimiter: number | null
  /** The first record is a header, not data (CSV/TSV). */
  header: boolean
}

export interface IndexerState {
  /** Absolute offset the next pushed chunk starts at. */
  position: number
  /** Data records seen so far. */
  rows: number
  /** Whether the header has been passed. */
  headerDone: boolean
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

  constructor(options: IndexerOptions, resume?: IndexerState) {
    this.options = options
    this.headerDone = !options.header
    if (resume) {
      this.position = resume.position
      this.recordStart = resume.position
      this.rows = resume.rows
      this.headerDone = resume.headerDone
    }
  }

  /** Where a resumed index should start reading: after the last complete record. */
  resumeState(): IndexerState {
    return { position: this.recordStart, rows: this.rows, headerDone: this.headerDone }
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
    let i = 0
    const n = chunk.length
    const base = this.position
    if (base === 0 && n >= 3 && chunk[0] === 0xef && chunk[1] === 0xbb && chunk[2] === 0xbf) {
      i = 3
      this.recordStart = 3
    }
    const delimiter = this.options.delimiter
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
      if (delimiter !== null) {
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
    if (this.hasContent) {
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
