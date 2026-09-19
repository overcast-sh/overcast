/// <reference lib="webworker" />

import { buildLayout } from "./map-layout"

self.onmessage = (e: MessageEvent) => {
  const { id, topologyNodes, topologyEdges, nodeSizeOverrides, activeRegion, collapsedStacks } =
    e.data
  try {
    const layout = buildLayout(
      topologyNodes,
      topologyEdges,
      nodeSizeOverrides ?? {},
      activeRegion,
      new Set(collapsedStacks ?? []),
    )
    self.postMessage({ id, layout, error: null })
  } catch (err) {
    self.postMessage({ id, layout: null, error: String(err) })
  }
}
