import { useState } from "react"
import { Link } from "@tanstack/react-router"
import type { Column, TableMetadata } from "@aws-sdk/client-athena"
import {
  ChevronDown,
  ChevronRight,
  Eye,
  FileCode,
  LibraryBig,
  MoreHorizontal,
  Table2,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/menu"
import { cn } from "@/lib/utils"
import { qualifiedName } from "../../sql-text"
import { tableFormat } from "../../table-format"

/**
 * One table in the data browser: click its name to insert the qualified
 * name at the cursor, expand it for its columns (partition keys marked),
 * and act on it from its menu.
 */
export interface TableEntryProps {
  table: TableMetadata
  database: string
  /** Glue holds this catalog's tables, so *Open in Glue* has somewhere to go. */
  inGlue: boolean
  onInsert: (text: string) => void
  /** Opens SQL in a new query tab and runs it. */
  onRunInNewTab: (sql: string, title: string) => void
}

export function TableEntry({ table, database, inGlue, onInsert, onRunInNewTab }: TableEntryProps) {
  const [open, setOpen] = useState(false)
  const name = table.Name ?? ""
  const qualified = qualifiedName(database, name)
  const format = tableFormat(table)
  const Chevron = open ? ChevronDown : ChevronRight
  return (
    <li>
      <div className="group flex items-center gap-1 rounded-sm pr-1 hover:bg-bg-muted">
        <button
          type="button"
          aria-expanded={open}
          aria-label={`${open ? "Collapse" : "Expand"} ${name}`}
          onClick={() => setOpen(!open)}
          className="flex size-6 shrink-0 items-center justify-center text-fg-subtle hover:text-fg"
        >
          <Chevron aria-hidden className="size-3.5" />
        </button>
        <button
          type="button"
          title={`Insert ${qualified}`}
          onClick={() => onInsert(qualified)}
          className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left font-mono text-xs text-fg"
        >
          <Table2 aria-hidden className="size-3.5 shrink-0 text-fg-subtle" />
          <span className="truncate">{name}</span>
        </button>
        {format && (
          <Badge variant={format === "ICEBERG" ? "accent" : "outline"} className="shrink-0">
            {format}
          </Badge>
        )}
        <Menu>
          <MenuTrigger asChild>
            <button
              type="button"
              aria-label={`Actions for ${name}`}
              className="flex size-6 shrink-0 items-center justify-center rounded-sm text-fg-subtle hover:bg-bg-elevated hover:text-fg"
            >
              <MoreHorizontal aria-hidden className="size-3.5" />
            </button>
          </MenuTrigger>
          <MenuContent>
            <MenuItem onSelect={() => onRunInNewTab(`SELECT * FROM ${qualified} LIMIT 10`, name)}>
              <Eye aria-hidden />
              Preview
            </MenuItem>
            <MenuItem
              onSelect={() => onRunInNewTab(`SHOW CREATE TABLE ${qualified}`, `${name} DDL`)}
            >
              <FileCode aria-hidden />
              Show DDL
            </MenuItem>
            {inGlue && (
              <MenuItem asChild>
                <Link to="/glue/$database/$table" params={{ database, table: name }}>
                  <LibraryBig aria-hidden />
                  Open in Glue
                </Link>
              </MenuItem>
            )}
          </MenuContent>
        </Menu>
      </div>
      {open && (
        <ul className="mb-1 ml-6 border-l border-border pl-2">
          <ColumnList columns={table.Columns ?? []} onInsert={onInsert} />
          <ColumnList columns={table.PartitionKeys ?? []} partition onInsert={onInsert} />
        </ul>
      )}
    </li>
  )
}

function ColumnList({
  columns,
  partition = false,
  onInsert,
}: {
  columns: readonly Column[]
  partition?: boolean
  onInsert: (text: string) => void
}) {
  return columns.map((column) => (
    <li key={column.Name}>
      <button
        type="button"
        title={partition ? "Partition key" : column.Comment || undefined}
        onClick={() => onInsert(qualifiedName(column.Name ?? ""))}
        className="flex w-full items-center gap-2 rounded-sm px-1 py-0.5 text-left font-mono text-2xs hover:bg-bg-muted"
      >
        <span className={cn("truncate", partition ? "text-accent" : "text-fg")}>{column.Name}</span>
        {partition && <span className="shrink-0 text-accent">◆ partition</span>}
        <span className="ml-auto shrink-0 truncate text-fg-subtle">{column.Type}</span>
      </button>
    </li>
  ))
}
