import { Link } from "@tanstack/react-router"
import { SquareTerminal } from "lucide-react"
import { Button } from "@/components/ui/button"
import { RowAction } from "@/components/ui/resource-list-page"
import { athenaEditorLink } from "@/features/athena/links"
import { AWS_DATA_CATALOG, previewSql } from "../athena-link"

interface QueryInAthenaProps {
  database: string
  /** The table to select from; without one the editor opens in the database with no SQL written. */
  table?: string
}

function linkFor({ database, table }: QueryInAthenaProps) {
  return athenaEditorLink({
    catalog: AWS_DATA_CATALOG,
    database,
    sql: table ? previewSql(database, table) : "",
  })
}

/** *Query in Athena*: the editor, in this database, with a `SELECT` over the table ready to run. */
export function QueryInAthenaButton(props: QueryInAthenaProps) {
  return (
    <Button size="sm" variant="ghost" asChild>
      <Link {...linkFor(props)}>
        <SquareTerminal className="h-3.5 w-3.5" />
        Query in Athena
      </Link>
    </Button>
  )
}

/** The same link as a table row's icon action. */
export function QueryInAthenaRowAction(props: QueryInAthenaProps) {
  return (
    <RowAction label={`Query ${props.table ?? props.database} in Athena`} asChild>
      <Link {...linkFor(props)}>
        <SquareTerminal className="h-3.5 w-3.5" />
      </Link>
    </RowAction>
  )
}
