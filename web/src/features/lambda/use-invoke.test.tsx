/**
 * The invoke outlives the Test tab: a debug pause switches the page to the
 * Code tab, which unmounts the Test tab mid-stream, and the result must
 * still be there when the tab comes back (docs/plans/
 * compute-debugger-console.md § 3.5, Test tab).
 */
import { useState } from "react"
import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { render, screen } from "@/test/render"
import { TestTab } from "@/features/lambda/components/test-tab"
import type { InvokeResult } from "@/types"
import { useLambdaInvoke } from "./use-invoke"

const result: InvokeResult = {
  statusCode: 200,
  payload: '{"doubled":42}',
  functionError: null,
  logResult: null,
  executedVersion: "$LATEST",
  logGroupName: null,
  logStreamName: null,
}

/** The route's shape: the invoke above the tab, the tab mounted only while shown. */
function Page({ name }: { name: string }) {
  const invoke = useLambdaInvoke(name)
  const [shown, setShown] = useState(true)
  return (
    <div>
      <button type="button" onClick={() => setShown((s) => !s)}>
        Toggle tab
      </button>
      {shown && <TestTab name={name} invoke={invoke} />}
    </div>
  )
}

describe("useLambdaInvoke", () => {
  it("keeps the event and delivers the result to a Test tab that was unmounted mid-invoke", async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(
      http.get("/api/lambda/functions/:name/test-events", () => HttpResponse.json([])),
      http.post("/api/lambda/functions/:name/invoke-with-progress", async () => {
        await gate
        return new HttpResponse(
          `event: progress\ndata: Invoking\n\nevent: result\ndata: ${JSON.stringify(result)}\n\n`,
          { headers: { "Content-Type": "text/event-stream" } },
        )
      }),
    )
    const { user } = render(<Page name="my-fn" />)
    const editor = document.querySelector("textarea")!
    await user.clear(editor)
    await user.paste('{"n": 1}')
    await user.click(screen.getByRole("button", { name: "Test" }))
    expect(screen.getByText(/Executing function/)).toBeInTheDocument()

    // The pause switches tabs: the Test tab goes away while the stream is open.
    await user.click(screen.getByRole("button", { name: "Toggle tab" }))
    expect(screen.queryByRole("button", { name: "Test" })).not.toBeInTheDocument()
    release()

    await user.click(screen.getByRole("button", { name: "Toggle tab" }))
    expect(await screen.findByText("Execution succeeded")).toBeInTheDocument()
    expect(document.querySelector("textarea")).toHaveValue('{"n": 1}')
  })
})
