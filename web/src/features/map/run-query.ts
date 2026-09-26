import { formatCount, formatQuantity } from "@/lib/format"

/**
 * The rows a result's first page holds — less a SELECT's header row — and
 * "+" when there are more pages.
 */
export function resultCount(rows: number, dml: boolean, more: boolean): string {
  const n = Math.max(dml ? rows - 1 : rows, 0)
  return more ? `${formatCount(n)}+ rows` : formatQuantity(n, "row")
}
