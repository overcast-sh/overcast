/**
 * RegionGroupNode — React Flow group node that wraps all resources in a region.
 *
 * Renders a subtle dashed border with the region name as a badge that sits on
 * the box's top edge and, like a sticky section header, slides down to stay
 * in view while the box is scrolled past — never into the middle of the
 * contents, where it would read as a border cutting through the cards.
 * pointer-events are disabled on the container so child nodes remain interactive.
 */

import { memo, useMemo, useState, useEffect } from "react"
import { createPortal } from "react-dom"
import { useViewport, useStore, type NodeProps } from "@xyflow/react"
import { cn } from "@/lib/utils"

export interface RegionGroupData extends Record<string, unknown> {
  region: string
  empty?: boolean
  active?: boolean
}

function clamp(v: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, v))
}

export const RegionGroupNode = memo(function RegionGroupNode({
  data,
  width,
  height,
  positionAbsoluteX,
  positionAbsoluteY,
}: NodeProps) {
  const { region = "", empty = false, active = false } = data as RegionGroupData
  const w = width ?? 0
  const h = height ?? 0

  const viewport = useViewport()
  const containerW = useStore((s) => s.width)
  const containerH = useStore((s) => s.height)

  // Compute badge position clamped to the visible portion of this group box.
  // The badge is kept fully inside the viewport with a margin so it never
  // clips off-screen.
  const badgeStyle = useMemo(() => {
    const zoom = viewport.zoom
    // Approximate badge half-dimensions in flow-coordinate units.
    // The badge lives in the node's local CSS space which maps 1:1 to
    // flow coordinates, so these are constant regardless of zoom.
    const BADGE_HALF_W = 52
    const BADGE_HALF_H = 12
    const MARGIN = 16 // breathing room from viewport edge (in flow coords)

    // Visible viewport bounds in flow coordinates
    const vpLeft = -viewport.x / zoom
    const vpTop = -viewport.y / zoom
    const vpRight = vpLeft + containerW / zoom

    // Group box bounds in flow coordinates
    const boxLeft = positionAbsoluteX
    const boxRight = positionAbsoluteX + w

    // Badge X: centre of the visible horizontal overlap, clamped so the
    // pill doesn't overflow the viewport or the box.
    const visLeft = Math.max(boxLeft, vpLeft)
    const visRight = Math.min(boxRight, vpRight)
    const rawX = (visLeft + visRight) / 2 - boxLeft
    const vpMinX = vpLeft + BADGE_HALF_W + MARGIN - boxLeft
    const vpMaxX = vpRight - BADGE_HALF_W - MARGIN - boxLeft
    // Box bounds are the hard constraint — badge must stay inside the box.
    const badgeX = clamp(clamp(rawX, vpMinX, vpMaxX), 0, w)

    // Badge Y: on the top edge, pushed down only as far as needed to stay in
    // view, and never past the bottom of the box.
    const vpMinY = vpTop + BADGE_HALF_H + MARGIN - positionAbsoluteY
    const badgeY = clamp(Math.max(0, vpMinY), 0, Math.max(0, h - BADGE_HALF_H))

    // Badge opacity — solid on the box's own edge, quieter once it has slid
    // down over the contents.
    const opacity = badgeY === 0 ? 1 : 0.7

    // Absolute flow-coordinate position for the portal-rendered badge.
    const absX = positionAbsoluteX + badgeX
    const absY = positionAbsoluteY + badgeY

    return { badgeX, badgeY, absX, absY, opacity } as const
  }, [viewport, containerW, containerH, positionAbsoluteX, positionAbsoluteY, w, h])

  // Resolve the viewport DOM element for the portal. useEffect ensures
  // we pick it up after React Flow has mounted the element.
  const [viewportEl, setViewportEl] = useState<Element | null>(null)
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setViewportEl(document.querySelector(".react-flow__viewport"))
  }, [])

  return (
    <div
      className={cn(
        "pointer-events-none relative rounded-xl border border-dashed bg-bg-elevated/30",
        active ? "border-accent/40" : "border-border/60",
      )}
      style={{ width: w, height: h }}
    >
      {/* Empty region placeholder */}
      {empty && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
          <p className="text-xs text-fg-muted/60">No resources in this region</p>
        </div>
      )}

      {/* Region badge — portalled above all nodes */}
      {viewportEl &&
        createPortal(
          <div
            className="pointer-events-auto absolute rounded-full border border-border bg-bg-elevated px-3 py-0.5 font-mono text-2xs tracking-[0.12em] whitespace-nowrap text-fg-muted uppercase shadow-sm transition-opacity duration-200"
            style={{
              left: badgeStyle.absX,
              top: badgeStyle.absY,
              opacity: badgeStyle.opacity,
              transform: "translate(-50%, -50%)",
              zIndex: 10000,
            }}
          >
            {region}
          </div>,
          viewportEl,
        )}
    </div>
  )
})
