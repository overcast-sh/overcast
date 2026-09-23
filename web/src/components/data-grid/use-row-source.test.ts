import { memorySource } from "@/lib/data-sources/memory-source"
import type { RowSource } from "@/lib/data-sources/row-source"
import { act, renderHook } from "@/test/render"
import { useRowSource } from "./use-row-source"

/** A source whose open the test resolves, and whose dispose it watches. */
function deferredOpen() {
  const source = memorySource([], [])
  const dispose = vi.spyOn(source, "dispose")
  let resolve: (source: RowSource) => void = () => {}
  const open = vi.fn(() => new Promise<RowSource>((r) => (resolve = r)))
  const settle = (opened = source) =>
    act(() => {
      resolve(opened)
      return Promise.resolve()
    })
  return { source, dispose, open, resolve: settle }
}

describe("useRowSource", () => {
  it("hands over the source once it opens", async () => {
    const { source, open, resolve } = deferredOpen()
    const { result } = renderHook(() => useRowSource("a", open))
    expect(result.current.source).toBeNull()
    await resolve()
    expect(result.current.source).toBe(source)
  })

  it("disposes of the source when the component unmounts", async () => {
    const { dispose, open, resolve } = deferredOpen()
    const { unmount } = renderHook(() => useRowSource("a", open))
    await resolve()
    unmount()
    expect(dispose).toHaveBeenCalled()
  })

  it("disposes of a source that opens after the component has gone", async () => {
    // Given: an open still in flight when the preview closes
    const { dispose, open, resolve } = deferredOpen()
    const { unmount } = renderHook(() => useRowSource("a", open))
    unmount()
    // When: it opens anyway
    await resolve()
    // Then: it is disposed of at once, its worker with it
    expect(dispose).toHaveBeenCalled()
  })

  it("opens afresh for a new key, never showing the old key's source", async () => {
    const first = deferredOpen()
    const { result, rerender } = renderHook(({ key }) => useRowSource(key, first.open), {
      initialProps: { key: "a" },
    })
    await first.resolve()
    rerender({ key: "b" })
    expect(result.current.source).toBeNull()
    expect(first.dispose).toHaveBeenCalled()
    expect(first.open).toHaveBeenCalledTimes(2)
  })

  it("reports a source that fails to open", async () => {
    const open = () => Promise.reject(new Error("Read failed: HTTP 403"))
    const { result } = renderHook(() => useRowSource("a", open))
    await act(async () => {})
    expect(result.current.error).toEqual(new Error("Read failed: HTTP 403"))
  })
})
