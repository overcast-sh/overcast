import type { TableSummary } from "@aws-sdk/client-s3tables"

/** A namespace and the tables in it, as the bucket's Tables tab lists them. */
export interface NamespaceGroup {
  namespace: string
  tables: TableSummary[]
}

/**
 * Tables under their namespace, namespaces A→Z, tables A→Z. A filter keeps a
 * namespace whose name matches whole, and otherwise only its matching tables.
 *
 * The namespaces are the ones listed plus any a table names: the two lists
 * refresh separately, and a table created in a brand-new namespace (PyIceberg's
 * `create_namespace` then `create_table`) must not vanish until both have.
 */
export function groupByNamespace(
  namespaces: string[],
  tables: TableSummary[],
  needle: string,
): NamespaceGroup[] {
  const all = new Set([...namespaces, ...tables.map((t) => t.namespace?.join(".") ?? "")])
  return [...all].sort().flatMap((namespace) => {
    const own = tables
      .filter((t) => t.namespace?.join(".") === namespace)
      .sort((a, b) => (a.name ?? "").localeCompare(b.name ?? ""))
    if (!needle || namespace.includes(needle)) return [{ namespace, tables: own }]
    const matching = own.filter((t) => t.name?.includes(needle))
    return matching.length > 0 ? [{ namespace, tables: matching }] : []
  })
}
