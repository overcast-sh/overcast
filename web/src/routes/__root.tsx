import { createRootRoute, HeadContent, Outlet, useRouterState } from "@tanstack/react-router"
import { AppShell } from "@/components/layout/app-shell"
import { TooltipProvider } from "@/components/ui/tooltip"
import { endpointStore } from "@/services/endpoint-store"
import { FileQuestion } from "lucide-react"

type RootSearch = {
  region?: string
}

function NotFoundPage() {
  const location = useRouterState({ select: (s) => s.location })
  return (
    <div className="mx-auto max-w-2xl py-16 text-center">
      <div className="mb-6 flex justify-center">
        <div className="flex h-16 w-16 items-center justify-center rounded-2xl border border-border bg-bg-elevated">
          <FileQuestion className="h-8 w-8 text-fg-subtle" />
        </div>
      </div>
      <h1 className="font-mono text-2xl font-semibold text-fg">Page not found</h1>
      <p className="mt-2 text-sm text-fg-muted">
        <code className="rounded bg-bg-elevated px-1.5 py-0.5 font-mono text-xs">
          {location.pathname}
        </code>{" "}
        does not match any Overcast route.
      </p>
    </div>
  )
}

export const Route = createRootRoute({
  validateSearch: (search: Record<string, unknown>): RootSearch => ({
    region: typeof search.region === "string" ? search.region : undefined,
  }),
  beforeLoad: ({ search }) => {
    const { region } = search
    // Set even when it matches the current region: the set is what records
    // the region as chosen, and an unrecorded one is replaced by the server's
    // default when that arrives (see endpointStore.seedServerRegion). An
    // unchanged set persists without notifying anyone.
    if (region) endpointStore.set({ ...endpointStore.get(), region })
  },
  head: () => ({
    meta: [{ title: "Overcast" }],
  }),
  component: () => (
    <>
      <HeadContent />
      <TooltipProvider>
        <AppShell>
          <Outlet />
        </AppShell>
      </TooltipProvider>
    </>
  ),
  notFoundComponent: () => (
    <TooltipProvider>
      <NotFoundPage />
    </TooltipProvider>
  ),
})
