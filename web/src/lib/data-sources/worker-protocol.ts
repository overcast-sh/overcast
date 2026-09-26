import type { DataColumn, IndexingStatus } from "./row-source"
import type { ParquetHead } from "./parquet-reader"

/**
 * The messages between a row source (main thread) and its data worker.
 *
 * One worker per open file: it holds that file's index or footer, does every
 * fetch, parse, decompression and index scan, and is terminated when the file
 * is closed. The main thread only renders.
 *
 * Requests carry an `id`; the reply echoes it (a Parquet read may send
 * `rows-partial` messages first, one per column). Index progress and
 * `changed` are unsolicited and carry none, and so does `continue`, which
 * nothing replies to. `abort` cancels a request by id — its fetch is aborted
 * in the worker — and the main thread settles the caller itself.
 */

export type TextKind = "csv" | "tsv" | "jsonl"

export interface OpenText {
  type: "open-text"
  id: number
  url: string
  size: number
  kind: TextKind
  /** CSV: every value is quoted (Athena's result CSV), so an unquoted empty field is a NULL. */
  quotedValues?: boolean
  /** Rows per block, and per index point. */
  every: number
  /** Bytes indexed per run before pausing to ask. */
  byteLimit: number
  /** `on-demand` (Save-Data) indexes only as far as `untilRows`. */
  mode: "background" | "on-demand"
  untilRows: number
}

export type ToWorker =
  | OpenText
  | { type: "continue"; untilRows?: number }
  | { type: "read-text"; id: number; start: number; end: number; count: number }
  | { type: "open-parquet"; id: number; url: string; size: number }
  | { type: "read-parquet"; id: number; start: number; end: number; columns: number[] }
  | { type: "abort"; id: number }

/** Rows as they cross the port: one array per column, absent where not asked for. */
export interface ColumnarRows {
  count: number
  columns: (unknown[] | undefined)[]
}

export interface TextHead {
  columns: DataColumn[]
  delimiter?: string
  /** The first block, parsed from the first 64 KB — shown before indexing finishes. */
  first: ColumnarRows
}

export interface IndexProgress extends IndexingStatus {
  /** Offsets recorded since the last progress message. */
  offsets: number[]
  /** Offset just past the last complete record indexed. */
  end: number
}

export type FromWorker =
  | ({ type: "text-head"; id: number } & TextHead)
  | ({ type: "index" } & IndexProgress)
  | ({ type: "parquet-head"; id: number } & ParquetHead)
  /** A block of rows; for Parquet, only the columns not already sent as partials. */
  | ({ type: "rows"; id: number } & ColumnarRows)
  /** One column of a pending `rows` reply, sent as soon as it decodes, and not again. */
  | { type: "rows-partial"; id: number; column: number; count: number; values: unknown[] }
  /** The object's ETag changed under an open file: it was overwritten. */
  | { type: "changed" }
  | {
      type: "error"
      id: number
      message: string
      /** The file does not read as the table its name promised (`NotTabularError`). */
      notTabular: boolean
    }

/** The message channel the worker speaks over — a real `Worker`, or in-process in tests. */
export interface DataWorkerPort {
  post(message: ToWorker): void
  listen(listener: (message: FromWorker) => void): () => void
  /**
   * The worker itself failed — its script did not load, threw while
   * evaluating, or a message could not be cloned — so nothing in flight will
   * ever be answered.
   */
  onFailure(listener: (reason: string) => void): () => void
  terminate(): void
}
