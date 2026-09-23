import { describe, expect, it } from "vitest"
import type { TaskAttempt } from "./execution-trace"
import {
  errorHint,
  groupInvocations,
  invocationPayloadOf,
  lambdaTargetOf,
  matchAttempt,
  parseCause,
  parseFunctionRef,
  requestIdOf,
  type LogLine,
} from "./lambda-invocations"

const T0 = Date.UTC(2026, 8, 19, 12, 0, 0)
const ID_A = "11111111-1111-4111-8111-111111111111"
const ID_B = "22222222-2222-4222-8222-222222222222"

function attempt(overrides: Partial<TaskAttempt> = {}): TaskAttempt {
  return { scheduledEventId: 3, scheduledAt: T0, status: "running", ...overrides }
}

function line(ms: number, message: string, stream = "s1"): LogLine {
  return { timestamp: T0 + ms, message, logStreamName: stream }
}

/** A text-format invocation: START, the given lines, END, REPORT. */
function invocation(
  id: string,
  startMs: number,
  body: string[],
  stream = "s1",
  extra = "",
): LogLine[] {
  return [
    line(startMs, `START RequestId: ${id} Version: $LATEST`, stream),
    ...body.map((b, i) => line(startMs + 1 + i, b, stream)),
    line(startMs + 50, `END RequestId: ${id}`, stream),
    line(
      startMs + 51,
      `REPORT RequestId: ${id}\tDuration: 48.20 ms\tBilled Duration: 49 ms\tMemory Size: 128 MB\tMax Memory Used: 61 MB${extra}`,
      stream,
    ),
  ]
}

describe("parseFunctionRef", () => {
  it.each([
    ["orders", { functionName: "orders", qualifier: undefined }],
    ["orders:live", { functionName: "orders", qualifier: "live" }],
    [
      "arn:aws:lambda:eu-west-1:000000000000:function:orders:3",
      { functionName: "orders", qualifier: "3", region: "eu-west-1" },
    ],
    ["000000000000:function:orders", { functionName: "orders", qualifier: undefined }],
  ])("reads %s", (ref, expected) => {
    expect(parseFunctionRef(ref)).toEqual(expected)
  })
})

describe("lambdaTargetOf", () => {
  it("reads the function a function-ARN Task called from its resource", () => {
    const target = lambdaTargetOf(
      attempt({ resource: "arn:aws:lambda:us-east-1:000000000000:function:orders" }),
    )
    expect(target).toMatchObject({
      functionName: "orders",
      region: "us-east-1",
      via: "function-arn",
    })
  })

  it("reads the function lambda:invoke called from its resolved parameters", () => {
    const target = lambdaTargetOf(
      attempt({
        resourceType: "lambda",
        resource: "invoke",
        region: "us-east-1",
        parameters: JSON.stringify({ FunctionName: "orders", Qualifier: "live" }),
      }),
    )
    expect(target).toMatchObject({
      functionName: "orders",
      qualifier: "live",
      via: "lambda:invoke",
    })
  })

  it("ignores an integration that is not Lambda", () => {
    expect(
      lambdaTargetOf(attempt({ resourceType: "sqs", resource: "sendMessage", parameters: "{}" })),
    ).toBeNull()
  })
})

describe("requestIdOf", () => {
  it("reads the request id lambda:invoke returned", () => {
    const output = JSON.stringify({ StatusCode: 200, SdkResponseMetadata: { RequestId: ID_A } })
    expect(requestIdOf(attempt({ output }))).toBe(ID_A)
  })
})

describe("invocationPayloadOf", () => {
  it("replays lambda:invoke's Payload, not the whole parameter block", () => {
    const a = attempt({
      resourceType: "lambda",
      resource: "invoke",
      parameters: JSON.stringify({ FunctionName: "orders", Payload: { id: 7 } }),
    })
    expect(JSON.parse(invocationPayloadOf(a, lambdaTargetOf(a)!))).toEqual({ id: 7 })
  })
})

describe("parseCause", () => {
  it("splits a Lambda error payload into type, message and stack", () => {
    const cause = JSON.stringify({
      errorType: "TypeError",
      errorMessage: "x is undefined",
      trace: ["TypeError: x is undefined", "    at handler (/var/task/index.js:3:9)"],
    })
    expect(parseCause(cause)).toEqual({
      errorType: "TypeError",
      errorMessage: "x is undefined",
      stack: ["TypeError: x is undefined", "    at handler (/var/task/index.js:3:9)"],
    })
  })

  it("keeps a cause that is not an error payload as its message", () => {
    expect(parseCause("Function not found")).toEqual({
      errorMessage: "Function not found",
      stack: [],
    })
  })
})

describe("errorHint", () => {
  it("explains a function that could not be found", () => {
    expect(errorHint("Lambda.ResourceNotFoundException", "")).toMatch(/never ran/)
  })

  it("recognises a function timeout from its cause", () => {
    expect(errorHint("Lambda.Unknown", "Task timed out after 3.00 seconds")).toMatch(
      /own configured timeout/,
    )
  })
})

describe("groupInvocations", () => {
  it("gives a line with no request id to the invocation open in its stream", () => {
    const [inv] = groupInvocations(invocation(ID_A, 0, ["hello from print()"]))
    expect(inv.lines.map((l) => l.message)).toContain("hello from print()")
  })

  it("keeps two environments' interleaved invocations apart by stream", () => {
    const events = [...invocation(ID_A, 0, ["a"], "s1"), ...invocation(ID_B, 5, ["b"], "s2")].sort(
      (x, y) => x.timestamp! - y.timestamp!,
    )
    const invs = groupInvocations(events)
    expect(invs.map((i) => [i.requestId, i.lines.some((l) => l.message === "a")])).toEqual([
      [ID_A, true],
      [ID_B, false],
    ])
  })

  it("gives start-up output before START to the invocation that follows it", () => {
    const events = [line(-5, "Traceback: ImportError"), ...invocation(ID_A, 0, [])]
    expect(groupInvocations(events)[0].lines[0].message).toBe("Traceback: ImportError")
  })

  it("keeps a line written after REPORT with the invocation that wrote it, not the next", () => {
    const events = [
      ...invocation(ID_A, 0, []),
      line(60, "still running after the timeout"),
      ...invocation(ID_B, 100, []),
    ]
    const [a, b] = groupInvocations(events)
    expect([
      a.lines.some((l) => l.message === "still running after the timeout"),
      b.lines.some((l) => l.message === "still running after the timeout"),
    ]).toEqual([true, false])
  })

  it("reads the REPORT line's metrics, cold start and platform status", () => {
    const [inv] = groupInvocations(
      invocation(ID_A, 0, [], "s1", "\tInit Duration: 180.50 ms\tStatus: timeout"),
    )
    expect(inv.report).toEqual({
      durationMs: 48.2,
      billedDurationMs: 49,
      memorySizeMb: 128,
      maxMemoryUsedMb: 61,
      initDurationMs: 180.5,
      status: "timeout",
    })
  })

  it("reads JSON-format platform records", () => {
    const rec = (type: string, record: object, ms: number) =>
      line(ms, JSON.stringify({ time: new Date(T0 + ms).toISOString(), type, record }))
    const [inv] = groupInvocations([
      rec("platform.start", { requestId: ID_A }, 0),
      line(1, JSON.stringify({ level: "ERROR", message: "bad", requestId: ID_A })),
      rec(
        "platform.report",
        { requestId: ID_A, status: "timeout", metrics: { durationMs: 3000, memorySizeMB: 128 } },
        3001,
      ),
    ])
    expect([inv.requestId, inv.lines.length, inv.report?.status]).toEqual([ID_A, 3, "timeout"])
  })
})

describe("matchAttempt", () => {
  const both = groupInvocations([
    ...invocation(ID_A, 10, ["ok"]),
    ...invocation(ID_B, 20, [], "s2"),
  ])

  it("matches exactly on the request id lambda:invoke returned", () => {
    const output = JSON.stringify({ SdkResponseMetadata: { RequestId: ID_B } })
    const m = matchAttempt(attempt({ output, endAt: T0 + 100, status: "succeeded" }), both, T0)
    expect([m.quality, m.invocation?.requestId]).toEqual(["request-id", ID_B])
  })

  it("matches the only invocation that started inside the attempt", () => {
    const one = groupInvocations(invocation(ID_A, 10, []))
    const m = matchAttempt(attempt({ endAt: T0 + 100, status: "failed" }), one, T0)
    expect([m.quality, m.invocation?.requestId]).toEqual(["only-one", ID_A])
  })

  it("says it is guessing when several invocations overlap the attempt", () => {
    const m = matchAttempt(attempt({ endAt: T0 + 100, status: "succeeded" }), both, T0)
    expect([m.quality, m.candidates.length]).toEqual(["best-guess", 2])
  })

  it("picks the one overlapping invocation that logged the attempt's error", () => {
    const cause = JSON.stringify({ errorType: "Error", errorMessage: "card declined for o-9" })
    const invs = groupInvocations([
      ...invocation(ID_A, 10, ["ok"]),
      ...invocation(ID_B, 20, ["ERROR Invoke Error card declined for o-9"], "s2"),
    ])
    const m = matchAttempt(attempt({ endAt: T0 + 100, status: "failed", cause }), invs, T0)
    expect([m.quality, m.invocation?.requestId]).toEqual(["error-message", ID_B])
  })

  it("finds nothing when no invocation started in the window", () => {
    const later = groupInvocations(invocation(ID_A, 60_000, []))
    expect(matchAttempt(attempt({ endAt: T0 + 100 }), later, T0).quality).toBe("none")
  })
})
