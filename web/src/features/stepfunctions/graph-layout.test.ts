import { describe, expect, it } from "vitest"
import { parseDefinition } from "./asl"
import {
  END_ID,
  START_ID,
  arrowHead,
  forkId,
  joinId,
  layoutModel,
  roundedPath,
} from "./graph-layout"
import { DEFINITION_TEMPLATES } from "./templates"

function tourLayout() {
  const { model } = parseDefinition(JSON.stringify(DEFINITION_TEMPLATES[0].definition))
  if (!model) throw new Error("tour template did not parse")
  return layoutModel(model)
}

const contains = (
  outer: { x: number; y: number; width: number; height: number },
  inner: { x: number; y: number; width: number; height: number },
) =>
  inner.x >= outer.x &&
  inner.y >= outer.y &&
  inner.x + inner.width <= outer.x + outer.width &&
  inner.y + inner.height <= outer.y + outer.height

describe("layoutModel", () => {
  it("places Start above the first state and End below everything", () => {
    const { nodes } = tourLayout()
    const start = nodes.find((n) => n.id === START_ID)
    const end = nodes.find((n) => n.id === END_ID)
    const first = nodes.find((n) => n.id === "Prepare order")
    expect(start && first && start.y < first.y).toBe(true)
    expect(end && Math.max(...nodes.filter((n) => n.id !== END_ID).map((n) => n.y)) < end.y).toBe(
      true,
    )
  })

  it("draws a Parallel as a container whose branches sit side by side inside it", () => {
    // When: the tour is laid out
    const { nodes } = tourLayout()
    const parallel = nodes.find((n) => n.id === "Fulfil in parallel")
    const map = nodes.find((n) => n.id === "Pick items")
    const charge = nodes.find((n) => n.id === "Charge card")
    const pick = nodes.find((n) => n.id === "Pick item")
    if (!parallel || !map || !charge || !pick) throw new Error("missing node")

    // Then: containers hold their children, and the two branches are side by side
    expect(parallel.kind).toBe("container")
    expect(parallel.lanes).toHaveLength(2)
    expect(contains(parallel, map)).toBe(true)
    expect(contains(parallel, charge)).toBe(true)
    expect(contains(map, pick)).toBe(true)
    expect(map.x + map.width).toBeLessThanOrEqual(charge.x)
    // Nesting depth drives which layer draws on top.
    expect(pick.depth).toBe(2)
  })

  it("routes every lane from the container's fork and back into its join", () => {
    const { edges, nodes } = tourLayout()
    const parallel = nodes.find((n) => n.id === "Fulfil in parallel")
    const fork = edges.find((e) => e.from === forkId("Fulfil in parallel#1"))
    const join = edges.find((e) => e.to === joinId("Fulfil in parallel#1"))
    if (!parallel || !fork || !join) throw new Error("missing edge")

    // The fork starts on the container's centre line, under its header.
    expect(fork.points[0].x).toBeCloseTo(parallel.x + parallel.width / 2)
    expect(fork.to).toBe("Charge card")
    expect(fork.sourceNode).toBe(forkId("Fulfil in parallel#1"))
    // The join ends back on the centre line, inside the container.
    const last = join.points[join.points.length - 1]
    expect(last.x).toBeCloseTo(parallel.x + parallel.width / 2)
    expect(last.y).toBeLessThanOrEqual(parallel.y + parallel.height)
  })

  it("labels Choice and Catch edges and gives dagre room for the label", () => {
    const { edges } = tourLayout()
    const choice = edges.find((e) => e.kind === "choice")
    const catcher = edges.find((e) => e.kind === "catch")
    expect(choice?.label).toBe("$.express == true")
    expect(choice?.labelPosition).toBeDefined()
    expect(catcher?.to).toBe("Order failed")
  })

  it("lays out a loop without dropping the back edge", () => {
    // Given: a polling loop — Wait → Check → Choice back to Wait
    const { model } = parseDefinition(
      JSON.stringify({
        StartAt: "Wait",
        States: {
          Wait: { Type: "Wait", Seconds: 1, Next: "Check" },
          Check: {
            Type: "Choice",
            Choices: [{ Variable: "$.done", BooleanEquals: true, Next: "Done" }],
            Default: "Wait",
          },
          Done: { Type: "Succeed" },
        },
      }),
    )
    if (!model) throw new Error("did not parse")
    const back = layoutModel(model).edges.find((e) => e.from === "Check" && e.to === "Wait")
    expect(back?.kind).toBe("default")
    expect(back?.points.length).toBeGreaterThan(1)
  })
})

describe("paths", () => {
  it("rounds the corners of a routed path and keeps its ends", () => {
    const d = roundedPath([
      { x: 0, y: 0 },
      { x: 0, y: 50 },
      { x: 50, y: 50 },
    ])
    expect(d.startsWith("M0,0")).toBe(true)
    expect(d).toContain("Q0,50")
    expect(d.endsWith("L50,50")).toBe(true)
  })

  it("points the arrowhead along the last segment", () => {
    // A downward edge: the tip is the end point, the base is above it.
    const head = arrowHead([
      { x: 10, y: 0 },
      { x: 10, y: 40 },
    ])
    expect(head.startsWith("M10,40")).toBe(true)
  })
})
