/**
 * The scroll-time half of the message pipeline: rows mounted mid-scroll defer
 * their syntax-highlight markup (hundreds of spans per JSON document) and
 * render the identical text plain, then hydrate once the scroll settles — and
 * never shed the markup again. The DOM-weight numbers behind this live in
 * `useScrollSettled`'s module docs.
 */
import { describe, expect, it } from "vitest"
import { render } from "@testing-library/react"
import { LogMessage } from "./log-message"

const JSON_MESSAGE = '{"level":"error","msg":"boom","attempt":3}'

function renderMessage(defer: boolean) {
  return render(
    <LogMessage
      message={JSON_MESSAGE}
      formatted={false}
      syntaxHighlight
      wrapLines={false}
      filterMatcher={null}
      level="error"
      defer={defer}
    />,
  )
}

const tokenCount = (container: HTMLElement) => container.querySelectorAll("span.token").length

describe("LogMessage scroll-time highlight deferral", () => {
  it("highlights immediately when mounted idle", () => {
    const { container } = renderMessage(false)
    expect(tokenCount(container)).toBeGreaterThan(0)
  })

  it("renders the same text without token spans when mounted mid-scroll", () => {
    const idle = renderMessage(false)
    const scrolled = renderMessage(true)
    expect(tokenCount(scrolled.container)).toBe(0)
    // Identical text in an identically-classed <pre>: the hydration swap can
    // change colour but never the pixels a measured row occupies.
    expect(scrolled.container.textContent).toBe(idle.container.textContent)
    expect(scrolled.container.querySelector("pre")?.className).toBe(
      idle.container.querySelector("pre")?.className,
    )
  })

  it("hydrates when the scroll settles and keeps the markup through the next scroll", () => {
    const view = renderMessage(true)
    expect(tokenCount(view.container)).toBe(0)

    view.rerender(
      <LogMessage
        message={JSON_MESSAGE}
        formatted={false}
        syntaxHighlight
        wrapLines={false}
        filterMatcher={null}
        level="error"
        defer={false}
      />,
    )
    const hydrated = tokenCount(view.container)
    expect(hydrated).toBeGreaterThan(0)

    // The latch: a row that has hydrated must not dump its subtree when the
    // next scroll starts — that would hand the churn right back to the
    // observers the deferral exists to spare.
    view.rerender(
      <LogMessage
        message={JSON_MESSAGE}
        formatted={false}
        syntaxHighlight
        wrapLines={false}
        filterMatcher={null}
        level="error"
        defer
      />,
    )
    expect(tokenCount(view.container)).toBe(hydrated)
  })

  it("lets a wrapped message pre shrink below its content (min-w-0), but not in no-wrap mode", () => {
    // A flex item's min-width:auto pins it to min-content width, and
    // break-word does not lower min-content — a single-token error line once
    // forced a ~42,000px scroller. Wrap mode must carry min-w-0; no-wrap mode
    // must not (the wide scroller is no-wrap's whole point).
    const wrapped = render(
      <LogMessage
        message={JSON_MESSAGE}
        formatted={false}
        syntaxHighlight
        wrapLines
        filterMatcher={null}
        level="error"
      />,
    )
    expect(wrapped.container.querySelector("pre")?.className).toContain("min-w-0")
    const noWrap = renderMessage(false)
    expect(noWrap.container.querySelector("pre")?.className).not.toContain("min-w-0")
  })

  it("defers nothing for plain (non-JSON) messages — they were already cheap", () => {
    const { container } = render(
      <LogMessage
        message="plain text line"
        formatted={false}
        syntaxHighlight
        wrapLines={false}
        filterMatcher={null}
        level={null}
        defer
      />,
    )
    expect(container.textContent).toContain("plain text line")
    expect(tokenCount(container)).toBe(0)
  })
})

/*
 * A collapsed row is one truncated line, and Syntax used to be skipped for it
 * outright — so ticking Collapse in a stream of JSON documents turned every
 * colour off. The single line is still a document; it gets its tokens.
 */
describe("LogMessage collapsed rows", () => {
  const PRETTY_MESSAGE = '{\n  "level": "error",\n  "msg": "boom"\n}'

  function renderCollapsed(props: {
    syntaxHighlight: boolean
    formatted?: boolean
    defer?: boolean
  }) {
    return render(
      <LogMessage
        message={PRETTY_MESSAGE}
        formatted={props.formatted ?? false}
        syntaxHighlight={props.syntaxHighlight}
        wrapLines
        filterMatcher={null}
        level="error"
        collapsed
        defer={props.defer ?? false}
      />,
    )
  }

  it("syntax-highlights a JSON document on its single-line form", () => {
    const { container } = renderCollapsed({ syntaxHighlight: true })
    expect(tokenCount(container)).toBeGreaterThan(0)
    // Compact, whatever shape it arrived in: a collapsed line cannot break.
    expect(container.querySelector("pre")?.textContent).toBe('{"level":"error","msg":"boom"}')
    expect(container.querySelector("pre")?.className).toContain("truncate")
  })

  it("ignores Format while collapsed — pretty-printing has nothing to print on one line", () => {
    const { container } = renderCollapsed({ syntaxHighlight: true, formatted: true })
    expect(container.querySelector("pre")?.textContent).toBe('{"level":"error","msg":"boom"}')
  })

  it("renders the raw line without a highlight block when Syntax is off", () => {
    const { container } = renderCollapsed({ syntaxHighlight: false })
    expect(container.querySelector("pre")).toBeNull()
    expect(tokenCount(container)).toBe(0)
  })

  it("defers the tokens mid-scroll like an expanded row", () => {
    const { container } = renderCollapsed({ syntaxHighlight: true, defer: true })
    expect(tokenCount(container)).toBe(0)
    expect(container.querySelector("pre")?.textContent).toBe('{"level":"error","msg":"boom"}')
  })

  it("keeps a platform record's summary as the collapsed text, uncoloured", () => {
    const { container } = render(
      <LogMessage
        message='{"time":"2026-08-10T02:34:42.700Z","type":"platform.report","record":{"requestId":"abc","status":"success","metrics":{"durationMs":3.1}}}'
        summary="REPORT RequestId: abc\tDuration: 3.10 ms"
        formatted={false}
        syntaxHighlight
        wrapLines
        filterMatcher={null}
        level="info"
        collapsed
      />,
    )
    expect(container.textContent).toContain("REPORT RequestId: abc")
    expect(container.querySelector("pre")).toBeNull()
  })
})
