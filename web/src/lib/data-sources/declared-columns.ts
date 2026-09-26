import type { Field } from "./delimited-parse"
import type { DataColumn, RowBlock, RowSource } from "./row-source"

/**
 * Types for a text file's columns, declared somewhere other than the file —
 * an Athena result's CSV has none, and `GetQueryResults` says what they are.
 */
export interface DeclaredColumns {
  columns: readonly DataColumn[]
  /** A field's text, or `null` for a NULL, as the value column `index` holds. */
  value: (text: Field, index: number) => unknown
}

/**
 * A text source with its columns typed by `declared`: the columns replaced,
 * and every value read from its text a block at a time as it is fetched.
 *
 * A file whose width is not the declared one is not the table the
 * declaration describes, so it keeps the columns inferred from its text.
 */
export function withDeclaredColumns(source: RowSource, declared: DeclaredColumns): RowSource {
  return source.columns.length === declared.columns.length
    ? new DeclaredColumnsSource(source, declared)
    : source
}

class DeclaredColumnsSource implements RowSource {
  readonly continueIndexing?: () => void
  private readonly source: RowSource
  private readonly declared: DeclaredColumns

  constructor(source: RowSource, declared: DeclaredColumns) {
    this.source = source
    this.declared = declared
    if (source.continueIndexing) this.continueIndexing = () => source.continueIndexing?.()
  }

  get columns() {
    return this.declared.columns
  }
  get rowCount() {
    return this.source.rowCount
  }
  get blockSize() {
    return this.source.blockSize
  }
  get projects() {
    return this.source.projects
  }
  get indexing() {
    return this.source.indexing
  }
  get changed() {
    return this.source.changed
  }

  async getRows(
    start: number,
    end: number,
    cols: readonly number[],
    signal: AbortSignal,
    onPartial?: (block: RowBlock) => void,
  ): Promise<RowBlock> {
    const block = await this.source.getRows(
      start,
      end,
      cols,
      signal,
      onPartial && ((partial) => onPartial(this.typed(partial))),
    )
    return this.typed(block)
  }

  subscribe(listener: () => void): () => void {
    return this.source.subscribe(listener)
  }

  dispose(): void {
    this.source.dispose()
  }

  private typed(block: RowBlock): RowBlock {
    return {
      ...block,
      columns: block.columns.map(
        (values, index) =>
          values &&
          Array.from(values, (text) => this.declared.value(text as Field, index)),
      ),
    }
  }
}
