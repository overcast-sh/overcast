import * as PopoverPrimitive from "@radix-ui/react-popover"
import { Link } from "@tanstack/react-router"
import { Database } from "lucide-react"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import { athenaEditorLink } from "@/features/athena/links"
import type { DataColumn } from "@/lib/data-sources/row-source"
import type { TabularKind } from "../preview-kind"
import { athenaSql } from "../athena-sql"

/**
 * *Query with Athena* — the grid's answer to sort and filter.
 *
 * The grid does not sort or filter a file in the browser: over millions of
 * rows that needs an engine, and doing it over the loaded rows would misstate
 * the data. This hands the question to Athena instead: a `CREATE EXTERNAL
 * TABLE` over the object's folder, typed from the columns the grid already
 * knows, and a `SELECT` to start from — opened in the Athena editor, or
 * copied for the CLI or an SDK.
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
  format: TabularKind
  columns: readonly DataColumn[]
  delimiter?: string
}) {
  const sql = athenaSql({ bucket, objectKey, format, columns, delimiter })
  return (
    <PopoverPrimitive.Root>
      <PopoverPrimitive.Trigger asChild>
        <Button variant="ghost" size="sm" title="Query with Athena">
          <Database aria-hidden className="size-3.5" />
          <span className="sr-only sm:not-sr-only">Query with Athena</span>
        </Button>
      </PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          align="end"
          sideOffset={6}
          collisionPadding={12}
          className="z-50 flex w-[min(34rem,92vw)] flex-col gap-2 rounded-card border border-border bg-bg-elevated p-3 shadow-xl"
        >
          <p className="text-xs text-fg-muted">
            To sort or filter the whole file, query it. This SQL makes a table over the object’s
            folder and selects from it: run each statement in the Athena editor, or with{" "}
            <code className="font-mono text-xs">aws athena</code>.
          </p>
          <div className="relative">
            <pre className="max-h-64 overflow-auto rounded-control border border-border bg-bg-muted p-2 pr-9 font-mono text-xs leading-relaxed text-fg">
              {sql}
            </pre>
            <CopyButton
              value={sql}
              noun="Athena SQL"
              tone="inline"
              className="absolute top-2 right-2"
            />
          </div>
          <Button asChild size="sm" className="self-end">
            <Link {...athenaEditorLink({ sql })}>
              <Database aria-hidden className="size-3.5" />
              Open in the Athena editor
            </Link>
          </Button>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
