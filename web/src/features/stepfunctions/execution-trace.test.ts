import { describe, expect, it } from "vitest"
import type { HistoryEvent } from "@aws-sdk/client-sfn"
import { parseDefinition, type AslModel } from "./asl"
import { buildTrace, summarizeNode, summarizeTransitions } from "./execution-trace"
import { END_ID, START_ID, forkId, joinId } from "./graph-layout"

const T0 = Date.UTC(2026, 8, 19, 12, 0, 0)

/** One history event, `ms` after T0, linked to `prev`. */
function ev(
  id: number,
  prev: number,
  type: string,
  details?: Record<string, unknown>,
  ms = id * 10,
): HistoryEvent {
  const event: Record<string, unknown> = {
    id,
    previousEventId: prev,
    type,
    timestamp: new Date(T0 + ms),
  }
  if (details) {
    const key = type.endsWith("StateEntered")
      ? "stateEnteredEventDetails"
      : type.endsWith("StateExited")
        ? "stateExitedEventDetails"
        : `${type[0].toLowerCase()}${type.slice(1)}EventDetails`
    event[key] = details
  }
  return event as unknown as HistoryEvent
}

/** Links each event to the one before it, the way a sequential interpreter records history. */
function linear(events: Array<[string, Record<string, unknown>?]>): HistoryEvent[] {
  return events.map(([type, details], i) => ev(i + 1, i, type, details))
}

function model(definition: object): AslModel {
  const { model: m } = parseDefinition(JSON.stringify(definition))
  if (!m) throw new Error("definition did not parse")
  return m
}

const parallel = model({
  StartAt: "P",
  States: {
    P: {
      Type: "Parallel",
      Branches: [
        { StartAt: "A", States: { A: { Type: "Pass", End: true } } },
        { StartAt: "B", States: { B: { Type: "Pass", End: true } } },
      ],
      Next: "Done",
    },
    Done: { Type: "Succeed" },
  },
})

describe("buildTrace", () => {
  it("keeps concurrent branches apart by following previousEventId, not list order", () => {
    // Given: AWS-style history where branch A and branch B interleave, each
    // chaining back to its own branch's start
    const events = [
      ev(1, 0, "ExecutionStarted", { input: "{}" }),
      ev(2, 1, "ParallelStateEntered", { name: "P", input: "{}" }),
      ev(3, 2, "ParallelStateStarted"),
      ev(4, 3, "PassStateEntered", { name: "A" }),
      ev(5, 3, "PassStateEntered", { name: "B" }),
      ev(6, 4, "PassStateExited", { name: "A", output: '"a"' }),
      ev(7, 5, "PassStateExited", { name: "B", output: '"b"' }),
      ev(8, 7, "ParallelStateSucceeded"),
      ev(9, 8, "ParallelStateExited", { name: "P", output: '["a","b"]' }),
      ev(10, 9, "SucceedStateEntered", { name: "Done" }),
      ev(11, 10, "SucceedStateExited", { name: "Done" }),
      ev(12, 11, "ExecutionSucceeded", { output: '["a","b"]' }),
    ]

    // When: the history is walked
    const trace = buildTrace(events, parallel)

    // Then: both branches are children of the Parallel run, and every edge
    // taken is recorded, including each lane's fork and join
    const p = trace.runsByName.get("P")?.[0]
    expect(trace.runsByName.get("A")?.[0].parentKey).toBe(p?.key)
    expect(trace.runsByName.get("B")?.[0].parentKey).toBe(p?.key)
    expect(trace.runs.map((r) => [r.name, r.status])).toEqual([
      ["P", "succeeded"],
      ["A", "succeeded"],
      ["B", "succeeded"],
      ["Done", "succeeded"],
    ])
    const moves = trace.transitions.map((t) => `${t.from}->${t.to}`)
    expect(moves).toEqual(
      expect.arrayContaining([
        `${START_ID}->P`,
        `${forkId("P#0")}->A`,
        `${forkId("P#1")}->B`,
        `A->${joinId("P#0")}`,
        `B->${joinId("P#1")}`,
        "P->Done",
        `Done->${END_ID}`,
      ]),
    )
    expect(trace.status).toBe("SUCCEEDED")
    expect(trace.runsByName.get("A")?.[0].output).toBe('"a"')
  })

  it("counts retries and keeps the errors they absorbed", () => {
    const m = model({
      StartAt: "Call",
      States: { Call: { Type: "Task", Resource: "arn:aws:states:::lambda:invoke", End: true } },
    })
    const trace = buildTrace(
      linear([
        ["ExecutionStarted", { input: "{}" }],
        ["TaskStateEntered", { name: "Call", input: "{}" }],
        ["TaskScheduled", { resource: "invoke" }],
        ["TaskFailed", { error: "Lambda.ServiceException", cause: "boom" }],
        ["TaskScheduled", { resource: "invoke" }],
        ["TaskSucceeded", { output: "{}" }],
        ["TaskStateExited", { name: "Call", output: "{}" }],
        ["ExecutionSucceeded", { output: "{}" }],
      ]),
      m,
    )
    const run = trace.runsByName.get("Call")?.[0]
    expect(run?.status).toBe("succeeded")
    expect(run?.attempts).toBe(2)
    expect(run?.retriedErrors).toEqual([{ error: "Lambda.ServiceException", cause: "boom" }])
    expect(run?.error).toBeUndefined()
  })

  it("marks a state that failed and moved on through a Catch as caught, and the move as a catch", () => {
    // Given: a Task fails and its Catch goes to Recover — no exit event for the Task, as on AWS
    const m = model({
      StartAt: "Call",
      States: {
        Call: {
          Type: "Task",
          Resource: "arn:aws:states:::lambda:invoke",
          Catch: [{ ErrorEquals: ["States.ALL"], Next: "Recover" }],
          Next: "Recover",
        },
        Recover: { Type: "Pass", End: true },
      },
    })
    const trace = buildTrace(
      linear([
        ["ExecutionStarted", { input: "{}" }],
        ["TaskStateEntered", { name: "Call" }],
        ["TaskScheduled", {}],
        ["TaskFailed", { error: "Boom", cause: "bad" }],
        ["PassStateEntered", { name: "Recover" }],
        ["PassStateExited", { name: "Recover" }],
        ["ExecutionSucceeded", {}],
      ]),
      m,
    )
    expect(trace.runsByName.get("Call")?.[0].status).toBe("caught")
    expect(trace.runsByName.get("Call")?.[0].error).toBe("Boom")
    // The move belongs on the Catch edge, not on the Next edge to the same state.
    expect(summarizeTransitions(trace, {}).get("Call->Recover")).toMatchObject({
      count: 0,
      catchCount: 1,
    })
  })

  it("records Map iterations so the view can narrow to one of them", () => {
    // Given: a Map over two items where the second iteration fails
    const m = model({
      StartAt: "M",
      States: {
        M: {
          Type: "Map",
          ItemProcessor: {
            StartAt: "X",
            States: { X: { Type: "Task", Resource: "r", End: true } },
          },
          End: true,
        },
      },
    })
    const trace = buildTrace(
      linear([
        ["ExecutionStarted", { input: "[1,2]" }],
        ["MapStateEntered", { name: "M" }],
        ["MapStateStarted", { length: 2 }],
        ["MapIterationStarted", { name: "M", index: 0 }],
        ["TaskStateEntered", { name: "X" }],
        ["TaskScheduled", {}],
        ["TaskSucceeded", {}],
        ["TaskStateExited", { name: "X" }],
        ["MapIterationSucceeded", { name: "M", index: 0 }],
        ["MapIterationStarted", { name: "M", index: 1 }],
        ["TaskStateEntered", { name: "X" }],
        ["TaskScheduled", {}],
        ["TaskFailed", { error: "Nope", cause: "item 2" }],
        ["MapIterationFailed", { name: "M", index: 1 }],
        ["MapStateFailed", {}],
        ["ExecutionFailed", { error: "States.BranchFailed", cause: "iteration 1" }],
      ]),
      m,
    )

    // Then: the Map run knows its items and each iteration's outcome
    const map = trace.runsByName.get("M")?.[0]
    expect(map?.itemCount).toBe(2)
    expect([...(map?.iterations?.values() ?? [])].map((i) => i.status)).toEqual([
      "succeeded",
      "failed",
    ])
    expect(map?.status).toBe("failed")

    // And the diagram can show all iterations, or just one of them
    expect(summarizeNode(trace, "X", {}).status).toBe("failed")
    expect(summarizeNode(trace, "X", {}).runs).toHaveLength(2)
    expect(summarizeNode(trace, "X", { M: 0 }).status).toBe("succeeded")
    expect(summarizeNode(trace, "X", { M: 1 }).focusRun?.error).toBe("Nope")
    expect(trace.runsByName.get("X")?.[1].iterationPath).toEqual([{ map: "M", index: 1 }])
    expect(trace.failedRunKey).toBeDefined()
  })

  it("leaves the current state running while the execution is still in flight", () => {
    const m = model({ StartAt: "W", States: { W: { Type: "Wait", Seconds: 10, End: true } } })
    const trace = buildTrace(
      linear([
        ["ExecutionStarted", { input: "{}" }],
        ["WaitStateEntered", { name: "W" }],
      ]),
      m,
    )
    expect(trace.status).toBe("RUNNING")
    expect(summarizeNode(trace, "W", {}).status).toBe("running")
    expect(summarizeNode(trace, "W", {}).focusRun?.end).toBeUndefined()
  })

  it("sorts events that arrive out of order before walking them", () => {
    const events = linear([
      ["ExecutionStarted", { input: "{}" }],
      ["PassStateEntered", { name: "A" }],
      ["PassStateExited", { name: "A" }],
      ["ExecutionSucceeded", {}],
    ]).reverse()
    const trace = buildTrace(events)
    expect(trace.runs.map((r) => r.status)).toEqual(["succeeded"])
    expect(trace.status).toBe("SUCCEEDED")
  })
})
