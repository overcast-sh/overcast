/**
 * The SQL keywords the editor completes and Format upper-cases: Trino's
 * reserved words (https://trino.io/docs/483/language/reserved.html) plus the
 * non-reserved ones a query or Athena's Hive DDL commonly spells out. Several to
 * a line, which is why Prettier leaves the list alone.
 */
// prettier-ignore
export const SQL_KEYWORDS: ReadonlySet<string> = new Set([
  // Reserved in Trino 483.
  "ALTER", "AND", "AS", "BETWEEN", "BY", "CASE", "CAST", "CONSTRAINT", "CREATE", "CROSS", "CUBE",
  "CURRENT_CATALOG", "CURRENT_DATE", "CURRENT_PATH", "CURRENT_ROLE", "CURRENT_SCHEMA",
  "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "DEALLOCATE", "DELETE", "DESCRIBE",
  "DISTINCT", "DROP", "ELSE", "END", "ESCAPE", "EXCEPT", "EXECUTE", "EXISTS", "EXTRACT", "FALSE",
  "FOR", "FROM", "FULL", "GROUP", "GROUPING", "HAVING", "IN", "INNER", "INSERT", "INTERSECT",
  "INTO", "IS", "JOIN", "JSON_ARRAY", "JSON_EXISTS", "JSON_OBJECT", "JSON_QUERY", "JSON_TABLE",
  "JSON_VALUE", "LEFT", "LIKE", "LISTAGG", "LOCALTIME", "LOCALTIMESTAMP", "NATURAL", "NORMALIZE",
  "NOT", "NULL", "ON", "OR", "ORDER", "OUTER", "PREPARE", "RECURSIVE", "RIGHT", "ROLLUP", "SELECT",
  "SKIP", "TABLE", "THEN", "TRIM", "TRUE", "UESCAPE", "UNION", "UNNEST", "USING", "VALUES", "WHEN",
  "WHERE", "WITH",
  // Non-reserved, common in queries.
  "ALL", "ANALYZE", "ANY", "ARRAY", "ASC", "COLUMNS", "COMMENT", "DATA", "DATABASE", "DATABASES",
  "DATE", "DAY", "DESC", "EXPLAIN", "FETCH", "FILTER", "FIRST", "FOLLOWING", "FUNCTIONS", "HOUR",
  "IF", "IGNORE", "INTERVAL", "LAST", "LATERAL", "LIMIT", "MAP", "MATCHED", "MERGE", "MINUTE",
  "MONTH", "NULLS", "OFFSET", "OVER", "PARTITION", "PARTITIONED", "PARTITIONS", "PRECEDING",
  "RANGE", "RENAME", "REPLACE", "ROW", "ROWS", "SCHEMA", "SCHEMAS", "SECOND", "SET", "SHOW",
  "SOME", "TABLES", "TABLESAMPLE", "TIME", "TIMESTAMP", "TO", "TRUNCATE", "TRY_CAST", "UNBOUNDED",
  "UPDATE", "USE", "VIEW", "WINDOW", "YEAR", "ZONE",
  // Athena's Hive DDL.
  "EXTERNAL", "LOCATION", "MSCK", "REPAIR", "FORMAT", "SERDE", "SERDEPROPERTIES", "STORED",
  "TBLPROPERTIES",
])
