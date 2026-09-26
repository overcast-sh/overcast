import {
  errorPosition,
  executedStatement,
  formatSql,
  placeholderCount,
  qualifiedName,
  quoteIdentifier,
  scanSql,
} from "./sql-text"

const KEYWORDS = new Set([
  "SELECT",
  "FROM",
  "WHERE",
  "AND",
  "GROUP",
  "BY",
  "ORDER",
  "LIMIT",
  "AS",
  "JOIN",
  "LEFT",
  "ON",
])

describe("scanSql", () => {
  it("splits code from strings, quoted identifiers and comments", () => {
    expect(scanSql(`SELECT 'a''b', "c" -- d\n/* e */ 1`).map((s) => s.kind)).toEqual([
      "code",
      "string",
      "code",
      "identifier",
      "code",
      "comment",
      "code",
      "comment",
      "code",
    ])
  })

  it("keeps an unterminated literal to the end", () => {
    expect(scanSql("SELECT 'open").at(-1)).toEqual({ kind: "string", text: "'open" })
  })
})

describe("placeholderCount", () => {
  it("counts the ? in code", () => {
    expect(placeholderCount("SELECT * FROM t WHERE a = ? AND b > ?")).toBe(2)
  })

  it.each([
    ["a string", "SELECT '?' FROM t"],
    ["a quoted identifier", 'SELECT "why?" FROM t'],
    ["a line comment", "SELECT 1 -- what?"],
    ["a block comment", "SELECT /* ? */ 1"],
  ])("ignores a ? inside %s", (_, sql) => {
    expect(placeholderCount(sql)).toBe(0)
  })
})

describe("placeholderCount and prepared statements", () => {
  it("gives a PREPARE no parameters of its own", () => {
    expect(placeholderCount("PREPARE by_id FROM SELECT * FROM t WHERE id = ?")).toBe(0)
  })

  it.each([
    ["EXECUTE by_id", "by_id"],
    ["-- run it\nEXECUTE by_id;", "by_id"],
    ["EXECUTE by_id USING 1", undefined],
    ["SELECT 1", undefined],
  ])("names the statement %j executes with ExecutionParameters", (sql, name) => {
    expect(executedStatement(sql)).toBe(name)
  })
})

describe("formatSql", () => {
  it("upper-cases keywords and puts each clause on its own line", () => {
    expect(
      formatSql("select a,  b from t where a = 1 group by a order by b limit 10", KEYWORDS),
    ).toBe(
      ["SELECT a, b", "FROM t", "WHERE a = 1", "GROUP BY a", "ORDER BY b", "LIMIT 10"].join("\n"),
    )
  })

  it("keeps a multi-word join together", () => {
    expect(formatSql("select * from a left   join b on a.id = b.id", KEYWORDS)).toBe(
      ["SELECT *", "FROM a", "LEFT JOIN b ON a.id = b.id"].join("\n"),
    )
  })

  it("leaves literals, quoted identifiers and comments as written", () => {
    expect(formatSql(`select 'from  x', "select" from t -- where  now`, KEYWORDS)).toBe(
      [`SELECT 'from  x', "select"`, "FROM t -- where  now"].join("\n"),
    )
  })
})

describe("quoteIdentifier", () => {
  it.each([
    ["orders", "orders"],
    ["Orders", '"Orders"'],
    ["order-items", '"order-items"'],
    ['a"b', '"a""b"'],
  ])("writes %s as %s", (name, quoted) => {
    expect(quoteIdentifier(name)).toBe(quoted)
  })

  it("qualifies each part", () => {
    expect(qualifiedName("sales", "Order Items")).toBe('sales."Order Items"')
  })
})

describe("errorPosition", () => {
  it("reads Trino's line and column", () => {
    expect(
      errorPosition("TABLE_NOT_FOUND: line 3:15: Table 'awsdatacatalog.sales.x' does not exist"),
    ).toEqual({ line: 3, column: 15 })
  })

  it("is null when the message names no position", () => {
    expect(errorPosition("Query exhausted resources")).toBeNull()
  })
})
