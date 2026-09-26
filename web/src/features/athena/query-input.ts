import type { StartQueryExecutionInput } from "@aws-sdk/client-athena"
import type { AwsCall, AwsService } from "@/lib/aws-command"
import type { QueryTab } from "./query-tabs"
import { placeholderCount } from "./sql-text"

/** Athena as each tool names it, for *Copy as CLI / boto3 / SDK v3*. */
export const ATHENA_SERVICE: AwsService = {
  cli: "athena",
  sdkPackage: "@aws-sdk/client-athena",
  sdkClient: "AthenaClient",
}

/** What one run sends: the SQL, and a value for each of its parameters. */
export interface QueryRunRequest {
  sql: string
  /** Undefined when the SQL takes none. */
  parameters?: string[]
}

/**
 * The run of a tab's SQL, or of `selection` within it (starting `offset`
 * characters in). The parameters strip numbers the whole query's `?`s, so a
 * selection takes the values of the placeholders it contains, not the first
 * ones. `executeCount` is the placeholder count of the prepared statement an
 * `EXECUTE name` runs, whose `?`s are not in the tab's own SQL.
 *
 * A value left blank is sent as written, and the engine says what is wrong
 * with it, as it would on AWS.
 */
export function queryRun(
  tab: QueryTab,
  {
    selection,
    offset = 0,
    executeCount,
  }: { selection?: string; offset?: number; executeCount?: number } = {},
): QueryRunRequest {
  const sql = selection ?? tab.sql
  const count = executeCount ?? placeholderCount(sql)
  if (count === 0) return { sql }
  const skip = selection === undefined ? 0 : placeholderCount(tab.sql.slice(0, offset))
  return {
    sql,
    parameters: Array.from({ length: count }, (_, i) => tab.parameters[skip + i] ?? ""),
  }
}

/** The `StartQueryExecution` request for a run: its SQL and values, in the tab's workgroup and context. */
export function startQueryInput(
  tab: QueryTab,
  run: QueryRunRequest = queryRun(tab),
): StartQueryExecutionInput {
  return {
    QueryString: run.sql,
    WorkGroup: tab.workGroup,
    QueryExecutionContext: { Catalog: tab.catalog, Database: tab.database },
    ExecutionParameters: run.parameters,
  }
}

/** The tab's request as an `AwsCall`, for `copyAwsCommand`. */
export function startQueryCall(tab: QueryTab, run?: QueryRunRequest): AwsCall {
  return {
    service: ATHENA_SERVICE,
    operation: "StartQueryExecution",
    input: { ...startQueryInput(tab, run) },
  }
}
