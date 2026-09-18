import { describe, it, expect } from "vitest"
import {
  executionStatusVariant,
  historyEventVariant,
  eventCategory,
  humanizeEventType,
  historyEventFailure,
  formatTimestamp,
  prettyJSON,
} from "./format"

describe("executionStatusVariant", () => {
  it("colours each execution status by what it means", () => {
    expect(executionStatusVariant("SUCCEEDED")).toBe("success")
    expect(executionStatusVariant("RUNNING")).toBe("info")
    expect(executionStatusVariant("FAILED")).toBe("danger")
    expect(executionStatusVariant("TIMED_OUT")).toBe("danger")
    expect(executionStatusVariant("ABORTED")).toBe("warning")
    expect(executionStatusVariant(undefined)).toBe("default")
  })
})

describe("historyEventVariant", () => {
  it("colours history events by outcome", () => {
    expect(historyEventVariant("TaskSucceeded")).toBe("success")
    expect(historyEventVariant("ExecutionFailed")).toBe("danger")
    expect(historyEventVariant("ExecutionTimedOut")).toBe("danger")
    expect(historyEventVariant("TaskScheduled")).toBe("info")
    expect(historyEventVariant("PassStateEntered")).toBe("default")
    expect(historyEventVariant(undefined)).toBe("default")
  })
})

describe("historyEventFailure", () => {
  it("finds the error and cause in whichever typed detail field carries them", () => {
    expect(
      historyEventFailure({
        type: "ExecutionFailed",
        executionFailedEventDetails: { error: "States.Runtime", cause: "not supported" },
      }),
    ).toEqual({ error: "States.Runtime", cause: "not supported" })

    expect(
      historyEventFailure({
        type: "TaskFailed",
        taskFailedEventDetails: { resourceType: "lambda", error: "Lambda.Unknown" },
      }),
    ).toEqual({ error: "Lambda.Unknown", cause: "" })
  })

  it("reports nothing for a successful event", () => {
    expect(
      historyEventFailure({
        type: "PassStateEntered",
        stateEnteredEventDetails: { name: "P", input: "{}" },
      }),
    ).toEqual({ error: "", cause: "" })
  })
})

describe("formatTimestamp", () => {
  it("renders an em dash when there is no timestamp", () => {
    expect(formatTimestamp(undefined)).toBe("—")
  })

  it("renders a date", () => {
    expect(formatTimestamp(new Date(0))).not.toBe("—")
  })
})

describe("prettyJSON", () => {
  it("pretty-prints JSON payloads", () => {
    expect(prettyJSON('{"a":1}')).toBe('{\n  "a": 1\n}')
  })

  it("leaves non-JSON text alone", () => {
    expect(prettyJSON("not json")).toBe("not json")
    expect(prettyJSON(undefined)).toBe("")
  })
})

describe("humanizeEventType", () => {
  it("splits a camel-cased event type into readable words", () => {
    expect(humanizeEventType("TaskStateEntered")).toBe("Task state entered")
    expect(humanizeEventType("MapIterationSucceeded")).toBe("Map iteration succeeded")
    expect(humanizeEventType("ExecutionTimedOut")).toBe("Execution timed out")
    expect(humanizeEventType(undefined)).toBe("")
  })
})

describe("eventCategory", () => {
  it("sorts events into what a reader filters by", () => {
    expect(eventCategory("PassStateEntered")).toBe("states")
    expect(eventCategory("ExecutionStarted")).toBe("states")
    expect(eventCategory("TaskScheduled")).toBe("tasks")
    expect(eventCategory("LambdaFunctionSucceeded")).toBe("tasks")
    expect(eventCategory("MapIterationStarted")).toBe("flow")
    // A failure is an error first, whatever emitted it.
    expect(eventCategory("MapIterationFailed")).toBe("errors")
    expect(eventCategory("TaskTimedOut")).toBe("errors")
  })
})
