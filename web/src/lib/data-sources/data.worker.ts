/// <reference lib="webworker" />
/**
 * The data worker: one per open file. All of its logic is `DataWorkerCore`;
 * this file only connects it to the worker's message port.
 */
import { DataWorkerCore } from "./data-worker-core"
import type { FromWorker, ToWorker } from "./worker-protocol"

declare const self: DedicatedWorkerGlobalScope

const core = new DataWorkerCore((message: FromWorker) => self.postMessage(message))

self.onmessage = (event: MessageEvent<ToWorker>) => core.handle(event.data)
