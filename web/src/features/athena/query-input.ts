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

/**
 * The `StartQueryExecution` request a tab runs: its SQL (or `sql`, the
 * selection), its workgroup and query context, and one `ExecutionParameters`
 * value per `?` in the SQL. A parameter left blank is sent as written, and
 * the engine says what is wrong with it, as it would on AWS.
 */
export function startQueryInput(tab: QueryTab, sql = tab.sql): StartQueryExecutionInput {
  const count = placeholderCount(sql)
  return {
    QueryString: sql,
    WorkGroup: tab.workGroup,
    QueryExecutionContext: { Catalog: tab.catalog, Database: tab.database },
    ExecutionParameters:
      count > 0 ? Array.from({ length: count }, (_, i) => tab.parameters[i] ?? "") : undefined,
  }
}

/** The same request, for `copyAwsCommand`. */
export function startQueryCall(tab: QueryTab): AwsCall {
  return {
    service: ATHENA_SERVICE,
    operation: "StartQueryExecution",
    input: { ...startQueryInput(tab) },
  }
}
