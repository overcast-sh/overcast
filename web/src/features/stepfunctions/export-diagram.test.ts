import { describe, expect, it } from "vitest"
import type { HistoryEvent } from "@aws-sdk/client-sfn"
import { parseDefinition } from "./asl"
import { computeDiagramState } from "./diagram-state"
import { buildTrace } from "./execution-trace"
import { diagramToSvg, escapeXml, type ExportPalette } from "./export-diagram"
import { layoutModel } from "./graph-layout"

const palette: ExportPalette = {
  bg: "#101418",
  surface: "#161b22",
  border: "#30363d",
  fg: "#e6edf3",
  fgMuted: "#9da7b3",
  fgSubtle: "#6e7681",
  accent: "#3e97df",
  success: "#2ea043",
  danger: "#f85149",
  warning: "#d29922",
  types: {
    Task: "#3e7bdf",
    Pass: "#6e7681",
    Choice: "#b08800",
    Succeed: "#2ea043",
    Fail: "#f85149",
  },
}

const { model } = parseDefinition(
  JSON.stringify({
    StartAt: "Check <input>",
    States: {
      "Check <input>": {
        Type: "Choice",
        Choices: [{ Variable: "$.ok", BooleanEquals: true, Next: "Done" }],
        Default: "Broken & bad",
      },
      Done: { Type: "Succeed" },
      "Broken & bad": { Type: "Fail", Error: "Nope" },
    },
  }),
)
if (!model) throw new Error("did not parse")
const layout = layoutModel(model)

describe("diagramToSvg", () => {
  it("produces a standalone document: literal colours only, names escaped", () => {
    // When: the definition alone is exported
    const svg = diagramToSvg(
      model,
      layout,
      computeDiagramState(model, layout, undefined, {}),
      {},
      {
        title: "checker",
        subtitle: "definition",
        palette,
      },
    )

    // Then: a sized SVG with no CSS variables, and markup-safe text
    expect(svg.startsWith('<svg xmlns="http://www.w3.org/2000/svg"')).toBe(true)
    expect(svg).not.toContain("var(--")
    expect(svg).toContain("Check &lt;input&gt;")
    expect(svg).toContain("Broken &amp; bad")
    expect(svg).toContain("$.ok == true")
    expect(svg.trimEnd().endsWith("</svg>")).toBe(true)
  })

  it("paints an execution's outcome: the failed state in the danger colour", () => {
    // Given: an execution that took the Default branch into the Fail state
    const events = [
      ["ExecutionStarted", "executionStartedEventDetails", {}],
      ["ChoiceStateEntered", "stateEnteredEventDetails", { name: "Check <input>" }],
      ["ChoiceStateExited", "stateExitedEventDetails", { name: "Check <input>" }],
      ["FailStateEntered", "stateEnteredEventDetails", { name: "Broken & bad" }],
      ["ExecutionFailed", "executionFailedEventDetails", { error: "Nope" }],
    ].map(([type, key, details], i) => ({
      id: i + 1,
      previousEventId: i,
      type,
      timestamp: new Date(1_000 + i * 10),
      [key as string]: details,
    })) as unknown as HistoryEvent[]
    const trace = buildTrace(events, model)

    // When: it is exported
    const svg = diagramToSvg(
      model,
      layout,
      computeDiagramState(model, layout, trace, {}),
      {},
      {
        title: "checker / run",
        palette,
        now: 2_000,
      },
    )

    // Then: the failed state's border and the End marker carry the danger colour
    expect(svg).toContain(`stroke="${palette.danger}" stroke-width="2"`)
    expect(svg).toContain(">END<")
  })
})

describe("escapeXml", () => {
  it("escapes every character that is special in markup", () => {
    expect(escapeXml(`<a href="x">'&'</a>`)).toBe(
      "&lt;a href=&quot;x&quot;&gt;&apos;&amp;&apos;&lt;/a&gt;",
    )
  })
})
