/**
 * The map's one data-lake peek: a Glue table's first rows or an S3 Tables
 * table's latest commit, for the table a node row opened it on. It reads the
 * table from the topology as it is now, and closes if the table goes.
 */

import { useEffect } from "react"
import type { TopologyNode } from "@/types"
import type { DataLakePeek } from "./data-lake-context"
import { tableRowName } from "./data-lake-overlay"
import { LatestCommitPeek } from "./latest-commit-peek"
import { TablePreviewPeek } from "./table-preview-peek"

interface DataLakePeeksProps {
  peek: DataLakePeek | null
  nodes: ReadonlyMap<string, TopologyNode>
  onClose: () => void
}

export function DataLakePeeks({ peek, nodes, onClose }: DataLakePeeksProps) {
  const node = peek ? nodes.get(peek.nodeId) : undefined
  const table = node?.tables?.find((t) => tableRowName(t) === peek?.row) ?? null
  const gone = peek !== null && table === null

  // A dropped table takes its peek with it, rather than leaving it to reopen
  // if a table of the same name comes back.
  useEffect(() => {
    if (gone) onClose()
  }, [gone, onClose])

  return (
    <>
      <TablePreviewPeek
        database={node?.label ?? ""}
        table={peek?.kind === "preview" ? table : null}
        onClose={onClose}
      />
      <LatestCommitPeek
        bucket={node?.label ?? ""}
        table={peek?.kind === "commit" ? table : null}
        onClose={onClose}
      />
    </>
  )
}
