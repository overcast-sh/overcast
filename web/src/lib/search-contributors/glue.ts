import type { Database, Table } from "@aws-sdk/client-glue"
import { glue } from "@/services/api"
import { glueKeys } from "@/features/glue/data"
import { createSearchContributor } from "./create-contributor"

createSearchContributor<Database>({
  id: "glue:databases",
  cacheKey: () => glueKeys.databases(),
  fetchAll: () => glue.listDatabases(),
  matchFields: (db) => [db.Name, db.Description],
  toResult: (db) => ({
    id: `glue:database:${db.Name}`,
    label: db.Name ?? "",
    sublabel: db.LocationUri || db.Description || undefined,
    service: "Glue",
    serviceKey: "/glue",
    type: "Database",
    href: `/glue/${encodeURIComponent(db.Name ?? "")}`,
  }),
})

createSearchContributor<Table>({
  id: "glue:tables",
  cacheKey: () => glueKeys.allTables(),
  fetchAll: () => glue.listAllTables(),
  matchFields: (t) => [t.Name, `${t.DatabaseName}.${t.Name}`],
  toResult: (t) => ({
    id: `glue:table:${t.DatabaseName}.${t.Name}`,
    label: t.Name ?? "",
    sublabel: t.DatabaseName,
    service: "Glue",
    serviceKey: "/glue",
    type: "Table",
    href: `/glue/${encodeURIComponent(t.DatabaseName ?? "")}/${encodeURIComponent(t.Name ?? "")}`,
  }),
})
