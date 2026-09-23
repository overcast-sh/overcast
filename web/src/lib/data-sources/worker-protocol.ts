import type { GridColumn, IndexingStatus } from "@/components/data-grid/row-source"
import type { ParquetField } from "./parquet-reader"

/**
 * The messages between a row source (main thread) and its data worker.
 *
 * One worker per open file: it holds that file's index or footer, does every
 * fetch, parse, decompression and index scan, and is terminated when the file
 * is closed. The main thread only renders.
 *
 * Requests carry an `id`; the reply echoes it (a Parquet read may send
 * `rows-partial` messages first, one per column). Index progress and
 * `changed` are unsolicited and carry none. `abort` cancels a request by id — its fetch is aborted in
 * the worker — and the main thread settles the caller itself.
 */

export type TextKind = "csv" | "tsv" | "jsonl"

export type ToWorker =
  | {
      type: "open-text"
      id: number
      url: string
      size: number
      kind: TextKind
      /** Rows per block, and per index point. */
      every: number
      /** Bytes indexed per run before pausing to ask. */
      byteLimit: number
      /** `on-demand` (Save-Data) indexes only as far as `untilRows`. */
      mode: "background" | "on-demand"
      untilRows: number
    }
  | { type: "continue"; id: number; untilRows?: number }
  | { type: "read-text"; id: number; start: number; end: number; count: number }
  | { type: "open-parquet"; id: number; url: string; size: number }
  | { type: "read-parquet"; id: number; start: number; end: number; columns: number[] }
  | { type: "abort"; id: number }

export interface TextHead {
  columns: GridColumn[]
  delimiter?: string
  /** The first block, parsed as the stream arrived — shown before indexing finishes. */
  first: { count: number; columns: unknown[][] }
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
  | {
      type: "parquet-head"
      id: number
      columns: GridColumn[]
      fields: ParquetField[]
      numRows: number
      rowGroups: number
      codecs: string[]
      rowsError?: string
    }
  | { type: "rows"; id: number; count: number; columns: (unknown[] | undefined)[] }
  /** One column of a pending `rows` reply, sent as soon as it decodes. */
  | { type: "rows-partial"; id: number; column: number; count: number; values: unknown[] }
  /** The object's ETag changed under an open file: it was overwritten. */
  | { type: "changed" }
  | {
      type: "error"
      id: number
      message: string
      /** `not-tabular`: the file does not read as the table its name promised. */
      code?: "not-tabular" | "http"
    }

/** The message channel the worker speaks over — a real `Worker`, or in-process in tests. */
export interface DataWorkerPort {
  post(message: ToWorker): void
  listen(listener: (message: FromWorker) => void): () => void
  terminate(): void
}
