/**
 * The Test tab's invoke, held above the tab so it outlives a tab switch.
 * A console debug session moves the page to the Code tab the moment the
 * function pauses (docs/plans/compute-debugger-console.md § 3.5); the Test
 * tab unmounts with it, and state kept inside the tab — the event being
 * sent, the progress line, the result the stream is about to deliver —
 * would go with it. The function route owns one of these and hands it to
 * the tab, so Continue lands the result where it was asked for.
 */
import { useCallback, useState } from "react"
import { lambda } from "@/services/api"
import type { InvokeResult } from "@/types"

export const DEFAULT_TEST_EVENT = '{\n  "key": "value"\n}'

export interface LambdaInvoke {
  /** The event JSON as typed; `setPayload` is the tab's editor writing it. */
  payload: string
  setPayload: (payload: string) => void
  isPending: boolean
  /** The stream's latest progress line while pending. */
  progressStep: string | null
  result: InvokeResult | null
  error: string | null
  /** Send `payload`; resolves when the stream ends. A second call while pending is ignored. */
  run: () => Promise<void>
}

export function useLambdaInvoke(name: string): LambdaInvoke {
  const [payload, setPayload] = useState(DEFAULT_TEST_EVENT)
  const [isPending, setIsPending] = useState(false)
  const [progressStep, setProgressStep] = useState<string | null>(null)
  const [result, setResult] = useState<InvokeResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = useCallback(async () => {
    if (isPending) return
    setResult(null)
    setError(null)
    setIsPending(true)
    setProgressStep("Starting invocation")
    try {
      for await (const event of lambda.invokeStream(name, payload)) {
        if (event.type === "progress") setProgressStep(event.step)
        else setResult(event.data)
      }
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setProgressStep(null)
      setIsPending(false)
    }
  }, [name, payload, isPending])

  return { payload, setPayload, isPending, progressStep, result, error, run }
}
