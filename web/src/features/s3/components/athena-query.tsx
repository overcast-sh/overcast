import * as PopoverPrimitive from "@radix-ui/react-popover"
import { Database } from "lucide-react"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import type { GridColumn } from "@/components/data-grid/row-source"
import { athenaSql } from "../athena-sql"

/**
 * *Query with Athena* — the grid's answer to sort and filter.
 *
 * The grid does not sort or filter a file in the browser: over millions of
 * rows that needs an engine, and doing it over the loaded rows would misstate
 * the data. This hands the question to Athena instead: a `CREATE EXTERNAL
 * TABLE` over the object's folder, typed from the columns the grid already
 * knows, and a `SELECT` to start from. The Athena console lands with #2072;
 * until then the SQL is copied, for the CLI or an SDK.
 */
export function AthenaQuery({
  bucket,
  objectKey,
  format,
  columns,
  delimiter,
}: {
  bucket: string
  objectKey: string
  format: "csv" | "tsv" | "jsonl" | "parquet"
  columns: readonly GridColumn[]
  delimiter?: string
}) {
  const sql = athenaSql({ bucket, objectKey, format, columns, delimiter })
  return (
    <PopoverPrimitive.Root>
      <PopoverPrimitive.Trigger asChild>
        <Button variant="ghost" size="sm">
          <Database aria-hidden className="h-3.5 w-3.5" />
          Query with Athena
        </Button>
      </PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          align="end"
          sideOffset={6}
          collisionPadding={12}
          className="z-50 flex w-[min(34rem,92vw)] flex-col gap-2 rounded-card border border-border bg-bg-elevated p-3 shadow-xl"
        >
          <p className="text-[13px] text-fg-muted">
            To sort or filter the whole file, query it. This SQL makes a table over the object’s
            folder and selects from it; run it with <code className="font-mono text-xs">aws athena</code>{" "}
            against this emulator.
          </p>
          <div className="relative">
            <pre className="max-h-64 overflow-auto rounded-control border border-border bg-bg-muted p-2 pr-9 font-mono text-xs leading-relaxed text-fg">
              {sql}
            </pre>
            <CopyButton value={sql} noun="Athena SQL" tone="inline" className="absolute top-2 right-2" />
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
