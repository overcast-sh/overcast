/**
 * The diagram toolbar's Export menu: download the diagram as SVG or PNG, or
 * copy the SVG markup. What is exported is what is on screen — the execution's
 * statuses and the Map iteration being viewed included — rendered by
 * `export-diagram` as a self-contained file rather than a screenshot of the
 * canvas, so it is sharp at any size and needs no stylesheet to open.
 */
import { createElement } from "react"
import * as DropdownMenu from "@radix-ui/react-dropdown-menu"
import {
  CircleCheck,
  CircleX,
  Copy,
  Download,
  FileCode,
  FileImage,
  LoaderCircle,
  TriangleAlert,
  type LucideIcon,
} from "lucide-react"
import { useToast } from "@/components/ui/toast"
import { writeClipboardText } from "@/lib/clipboard"
import { cn } from "@/lib/utils"
import type { AslModel } from "../asl"
import type { DiagramState } from "../diagram-state"
import { diagramToSvg, type ExportIcons, type ExportPalette } from "../export-diagram"
import type { IterationSelection } from "../execution-trace"
import type { FlowLayout } from "../graph-layout"
import { stateTypeTheme } from "../state-theme"

interface Props {
  model: AslModel
  layout: FlowLayout
  view: DiagramState
  iterationSelection: IterationSelection
  /** File name without extension. */
  fileName: string
  title: string
}

const STATE_TYPES = ["Task", "Pass", "Choice", "Wait", "Succeed", "Fail", "Parallel", "Map"]

/**
 * Resolves a theme token to a literal sRGB colour. Tokens are `var()` chains
 * and `oklch()` values, which a standalone SVG cannot rely on; painting one
 * pixel and reading it back yields the exact colour the page shows.
 */
function resolveColor(token: string, context: CanvasRenderingContext2D): string {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(token).trim()
  if (!raw) return "#888888"
  context.clearRect(0, 0, 1, 1)
  context.fillStyle = "#000"
  context.fillStyle = raw
  context.fillRect(0, 0, 1, 1)
  const [r, g, b] = context.getImageData(0, 0, 1, 1).data
  return `#${[r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("")}`
}

function currentPalette(): ExportPalette {
  const canvas = document.createElement("canvas")
  canvas.width = 1
  canvas.height = 1
  const context = canvas.getContext("2d", { willReadFrequently: true })
  if (!context) throw new Error("Canvas is unavailable, so the theme colours cannot be resolved.")
  const color = (token: string) => resolveColor(token, context)
  const types: Record<string, string> = {}
  for (const type of STATE_TYPES) {
    types[type] = color(stateTypeTheme(type).color.replace(/^var\((--[^)]+)\)$/, "$1"))
  }
  return {
    bg: color("--bg"),
    surface: color("--bg-elevated"),
    border: color("--border"),
    fg: color("--fg"),
    fgMuted: color("--fg-muted"),
    fgSubtle: color("--fg-subtle"),
    accent: color("--accent"),
    success: color("--success"),
    danger: color("--danger"),
    warning: color("--warning"),
    types,
  }
}

/** Icon markup, rendered on demand so react-dom/server only loads when someone exports. */
async function iconMarkup(): Promise<ExportIcons> {
  const { renderToStaticMarkup } = await import("react-dom/server")
  const render = (icon: LucideIcon) =>
    renderToStaticMarkup(createElement(icon, { size: 16, strokeWidth: 2 }))
  const types: Record<string, string> = {}
  for (const type of STATE_TYPES) types[type] = render(stateTypeTheme(type).icon)
  return {
    types,
    statuses: {
      running: render(LoaderCircle),
      succeeded: render(CircleCheck),
      failed: render(CircleX),
      caught: render(TriangleAlert),
      aborted: render(CircleX),
    },
  }
}

function saveBlob(blob: Blob, fileName: string) {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = fileName
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  // Let the download start before the URL goes away.
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** Rasterises an SVG document at `scale`× for a crisp PNG on high-density screens. */
async function svgToPng(svg: string, scale = 2): Promise<Blob> {
  const url = URL.createObjectURL(new Blob([svg], { type: "image/svg+xml" }))
  try {
    const image = new Image()
    image.decoding = "async"
    image.src = url
    await image.decode()
    const canvas = document.createElement("canvas")
    canvas.width = Math.ceil(image.naturalWidth * scale)
    canvas.height = Math.ceil(image.naturalHeight * scale)
    const context = canvas.getContext("2d")
    if (!context) throw new Error("Canvas is unavailable.")
    context.scale(scale, scale)
    context.drawImage(image, 0, 0)
    return await new Promise<Blob>((resolve, reject) =>
      canvas.toBlob(
        (blob) => (blob ? resolve(blob) : reject(new Error("PNG encoding failed."))),
        "image/png",
      ),
    )
  } finally {
    URL.revokeObjectURL(url)
  }
}

function fileStem(name: string): string {
  return name.replaceAll(/[^\w.-]+/g, "-").replaceAll(/^-+|-+$/g, "") || "state-machine"
}

export function ExportMenu({ model, layout, view, iterationSelection, fileName, title }: Props) {
  const { toast } = useToast()

  const build = async () => {
    const statusWord = view.hasTrace ? `execution ${view.executionStatus}` : "definition"
    const iterations = Object.entries(iterationSelection)
      .filter(([, i]) => i !== undefined)
      .map(([map, i]) => `${map} #${i}`)
    const subtitle = [
      statusWord,
      ...iterations,
      `exported ${new Date().toISOString().slice(0, 16).replace("T", " ")} UTC`,
    ].join(" · ")
    return diagramToSvg(model, layout, view, iterationSelection, {
      title,
      subtitle,
      palette: currentPalette(),
      icons: await iconMarkup(),
    })
  }

  const run = (action: () => Promise<void>) => () => {
    action().catch((error: unknown) =>
      toast({ title: "Export failed", description: (error as Error).message, variant: "danger" }),
    )
  }

  const stem = fileStem(fileName)
  const items = [
    {
      label: "Download SVG",
      hint: "Vector, any size",
      icon: FileCode,
      onSelect: run(async () =>
        saveBlob(new Blob([await build()], { type: "image/svg+xml" }), `${stem}.svg`),
      ),
    },
    {
      label: "Download PNG",
      hint: "2× image",
      icon: FileImage,
      onSelect: run(async () => saveBlob(await svgToPng(await build()), `${stem}.png`)),
    },
    {
      label: "Copy SVG",
      hint: "Markup to clipboard",
      icon: Copy,
      onSelect: run(async () => {
        await writeClipboardText(await build())
        toast({ title: "Copied diagram SVG", variant: "success" })
      }),
    },
  ]

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label="Export the diagram"
          className={cn(
            "flex h-7 items-center gap-1.5 rounded-md border border-border bg-bg-elevated/90 px-2 text-2xs font-medium text-fg-muted shadow-xs backdrop-blur-sm transition-colors",
            "hover:bg-bg-muted hover:text-fg data-[state=open]:bg-bg-muted data-[state=open]:text-fg",
          )}
        >
          <Download className="h-3.5 w-3.5" />
          Export
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="start"
          sideOffset={4}
          className="z-50 min-w-52 rounded-md border border-border bg-bg-elevated p-1 shadow-lg"
        >
          {items.map((item) => (
            <DropdownMenu.Item
              key={item.label}
              onSelect={item.onSelect}
              className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-xs text-fg outline-none data-highlighted:bg-bg-muted"
            >
              <item.icon className="h-3.5 w-3.5 text-fg-muted" />
              {item.label}
              <span className="ml-auto text-2xs text-fg-subtle">{item.hint}</span>
            </DropdownMenu.Item>
          ))}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}
