import { isAbortError, NotTabularError } from "./row-source"
import { WorkerChannel } from "./worker-channel"
import type { DataWorkerPort, FromWorker, ToWorker } from "./worker-protocol"

/** A port whose worker side the test plays by hand. */
function fakePort() {
  const sent: ToWorker[] = []
  let deliver: (message: FromWorker) => void = () => {}
  const port: DataWorkerPort = {
    post: (message) => sent.push(message),
    listen(listener) {
      deliver = listener
      return () => (deliver = () => {})
    },
    terminate: () => {},
  }
  return { port, sent, reply: (message: FromWorker) => deliver(message) }
}

const readText = (id: number): ToWorker => ({ type: "read-text", id, start: 0, end: 10, count: 1 })

describe("WorkerChannel", () => {
  it("settles a request with the reply that echoes its id", async () => {
    const { port, reply } = fakePort()
    const channel = new WorkerChannel(port, () => {})
    const request = channel.request("rows", readText)
    reply({ type: "rows", id: 1, count: 1, columns: [["x"]] })
    await expect(request).resolves.toMatchObject({ columns: [["x"]] })
  })

  it("rejects with NotTabularError when the worker says the file is not a table", async () => {
    const { port, reply } = fakePort()
    const channel = new WorkerChannel(port, () => {})
    const request = channel.request("text-head", readText)
    reply({ type: "error", id: 1, message: "Records differ.", notTabular: true })
    await expect(request).rejects.toBeInstanceOf(NotTabularError)
  })

  it("rejects the caller and tells the worker when the signal aborts", async () => {
    // Given: a request in flight
    const { port, sent } = fakePort()
    const channel = new WorkerChannel(port, () => {})
    const controller = new AbortController()
    const request = channel.request("rows", readText, { signal: controller.signal })
    // When: its signal aborts
    controller.abort()
    // Then: the caller hears AbortError, and the worker hears abort for that id
    await expect(request).rejects.toSatisfy(isAbortError)
    expect(sent.at(-1)).toEqual({ type: "abort", id: 1 })
  })

  it("passes partial rows to the caller ahead of the reply", () => {
    const { port, reply } = fakePort()
    const channel = new WorkerChannel(port, () => {})
    const onPartial = vi.fn()
    void channel.request("rows", readText, { onPartial })
    reply({ type: "rows-partial", id: 1, column: 2, count: 1, values: ["v"] })
    expect(onPartial).toHaveBeenCalledWith(expect.objectContaining({ column: 2, values: ["v"] }))
  })

  it("hands messages with no id to the event listener", () => {
    const { port, reply } = fakePort()
    const onEvent = vi.fn()
    new WorkerChannel(port, onEvent)
    reply({ type: "changed" })
    expect(onEvent).toHaveBeenCalledWith({ type: "changed" })
  })

  it("rejects what is pending and terminates the worker on close", async () => {
    const { port } = fakePort()
    const terminate = vi.spyOn(port, "terminate")
    const channel = new WorkerChannel(port, () => {})
    const request = channel.request("rows", readText)
    channel.close()
    await expect(request).rejects.toSatisfy(isAbortError)
    expect(terminate).toHaveBeenCalled()
  })
})
