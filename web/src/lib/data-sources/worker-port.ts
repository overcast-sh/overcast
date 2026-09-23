import { DataWorkerCore } from "./data-worker-core"
import type { DataWorkerPort, FromWorker, ToWorker } from "./worker-protocol"

/**
 * A data worker for one file. Terminating it is how a closed preview stops
 * every fetch and scan it started — the worker's own aborts are a courtesy,
 * `terminate()` is the guarantee.
 *
 * Not the shared `lib/worker-client.ts` kernel: that one is a lazy singleton
 * answering one reply per request, and this is a worker per file that streams
 * unsolicited progress and must die with the file.
 */
export function spawnDataWorker(): DataWorkerPort {
  const worker = new Worker(new URL("./data.worker.ts", import.meta.url), { type: "module" })
  return fromWorker(worker)
}

function fromWorker(worker: Worker): DataWorkerPort {
  const listeners = new Set<(message: FromWorker) => void>()
  const failures = new Set<(reason: string) => void>()
  worker.onmessage = (event: MessageEvent<FromWorker>) => {
    for (const listener of listeners) listener(event.data)
  }
  const fail = (reason: string) => {
    for (const listener of failures) listener(reason)
  }
  worker.onerror = (event) => fail(event.message || "its script could not be loaded")
  worker.onmessageerror = () => fail("a message could not be read")
  return {
    post: (message) => worker.postMessage(message),
    listen(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    onFailure(listener) {
      failures.add(listener)
      return () => failures.delete(listener)
    },
    terminate() {
      listeners.clear()
      failures.clear()
      worker.terminate()
    },
  }
}

/**
 * The same worker, run on this thread — for tests, and for an environment
 * without `Worker`. Messages are delivered asynchronously, as they would be
 * across a real port, so ordering bugs show up here too.
 */
export function inProcessDataWorker(fetchImpl?: typeof fetch): DataWorkerPort {
  const listeners = new Set<(message: FromWorker) => void>()
  let dead = false
  const core = new DataWorkerCore((message) => {
    if (dead) return
    queueMicrotask(() => {
      if (dead) return
      for (const listener of listeners) listener(message)
    })
  }, fetchImpl)
  return {
    post(message: ToWorker) {
      if (dead) return
      queueMicrotask(() => {
        if (!dead) core.handle(message)
      })
    },
    listen(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    // The core runs on this thread: a failure is an exception the caller sees.
    onFailure: () => () => {},
    terminate() {
      dead = true
      core.dispose()
      listeners.clear()
    },
  }
}

/** A real worker where the environment has one, in-process otherwise. */
export function createDataWorker(): DataWorkerPort {
  return typeof Worker === "undefined" ? inProcessDataWorker() : spawnDataWorker()
}
