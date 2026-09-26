import {
  activeQueryTab,
  closeQueryTab,
  initialQueryTabs,
  openQueryTab,
  restoreQueryTabs,
  selectQueryTab,
  updateQueryTab,
} from "./query-tabs"

describe("query tabs", () => {
  it("start with one empty tab in the default context", () => {
    expect(activeQueryTab(initialQueryTabs())).toMatchObject({
      title: "Query 1",
      sql: "",
      workGroup: "primary",
      catalog: "AwsDataCatalog",
      database: "default",
    })
  })

  it("open a new tab after the others and select it", () => {
    const state = openQueryTab(initialQueryTabs(), { sql: "SELECT 1", database: "sales" })
    expect(state.tabs).toHaveLength(2)
    expect(activeQueryTab(state)).toMatchObject({
      title: "Query 2",
      sql: "SELECT 1",
      database: "sales",
    })
  })

  it("update one tab and leave the others", () => {
    const opened = openQueryTab(initialQueryTabs())
    const [first, second] = opened.tabs
    const state = updateQueryTab(opened, first.id, { title: "orders by day" })
    expect(state.tabs.map((t) => t.title)).toEqual(["orders by day", second.title])
  })

  it("select the neighbour when the selected tab closes", () => {
    let state = openQueryTab(openQueryTab(initialQueryTabs()))
    const [first, second, third] = state.tabs
    state = selectQueryTab(state, second.id)
    expect(closeQueryTab(state, second.id).activeId).toBe(third.id)
    expect(closeQueryTab(selectQueryTab(state, third.id), third.id).activeId).toBe(second.id)
    expect(closeQueryTab(state, first.id).activeId).toBe(second.id)
  })

  it("leave one empty tab when the last one closes", () => {
    const state = initialQueryTabs()
    const closed = closeQueryTab(state, state.activeId)
    expect(closed.tabs).toHaveLength(1)
    expect(closed.activeId).not.toBe(state.activeId)
  })

  it("ignore a selection of a tab that does not exist", () => {
    const state = initialQueryTabs()
    expect(selectQueryTab(state, "gone")).toBe(state)
  })
})

describe("restoreQueryTabs", () => {
  it("starts afresh from anything that is not stored tabs", () => {
    expect(restoreQueryTabs("nonsense").tabs).toHaveLength(1)
  })

  it("repairs missing fields and drops entries that are not tabs", () => {
    const restored = restoreQueryTabs({
      tabs: [{ id: "a", sql: "SELECT 1", parameters: ["1", 2] }, 42],
      activeId: "missing",
    })
    expect(restored).toEqual({
      tabs: [
        {
          id: "a",
          title: "Query",
          sql: "SELECT 1",
          workGroup: "primary",
          catalog: "AwsDataCatalog",
          database: "default",
          parameters: ["1"],
          executionId: undefined,
        },
      ],
      activeId: "a",
    })
  })
})
