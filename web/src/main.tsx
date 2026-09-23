/* eslint-disable react-refresh/only-export-components */
import { StrictMode, lazy, Suspense, useState, useCallback, useEffect } from "react"
import { createRoot } from "react-dom/client"
import { createRouter, RouterProvider } from "@tanstack/react-router"
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { routeTree } from "./routeTree.gen"
import { ToastContextProvider, useToast } from "@/components/ui/toast"
import { RoutePending } from "@/components/layout/route-pending"
import { RouteError } from "@/components/layout/route-error"
import { DevToolsContext } from "@/hooks/use-dev-tools"
import { isNetworkError } from "@/lib/network-error"
import { preloadRouteChunksWhenIdle } from "@/lib/preload-route-chunks"
import { endpointStore } from "@/services/endpoint-store"
import { applyStoredTheme } from "@/hooks/use-theme"
import "@/styles/global.css"

// Before the first render, so every screen honours the stored theme —
// including the ones in front of the connection gate, which never mount the
// header and so never ran the hook that applies it.
applyStoredTheme()

const DevToolsPanel = lazy(() => import("@/components/dev-tools"))

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  return "Unknown error"
}

// On startup, seed the region from the server's OVERCAST_DEFAULT_REGION if
// nothing — the user, or a `?region=` in the URL — has chosen one.
void endpointStore.seedServerRegion()

const router = createRouter({
  routeTree,
  context: {},
  defaultPreload: "intent",
  // A navigation must never read as a dropped click. Routes are code-split,
  // so when a chunk or loader is still in flight the pending skeleton takes
  // over the content area almost immediately (50 ms filters the loads that
  // resolve within a frame or two) and, once shown, holds for 300 ms so a
  // fast resolution doesn't read as a flash.
  defaultPendingComponent: RoutePending,
  defaultPendingMs: 50,
  defaultPendingMinMs: 300,
  // Back must land where you left, not at the top. Reading a trace means
  // scrolling a long list, opening one, and coming back — and coming back to
  // row one means finding your place again by hand, every time.
  //
  // The offsets are cached per history entry, so Back restores what that entry
  // had while a fresh navigation to the same URL still starts at the top.
  //
  // <main> in the app shell owns the scroll, not the window (see
  // app-shell.tsx), so it has to be named twice: once as the container whose
  // offset is tracked, through the data-scroll-restoration-id the router reads
  // off the scroll event's target, and again here as the element a navigation
  // with nothing cached resets to the top.
  scrollRestoration: true,
  scrollToTopSelectors: ['[data-scroll-restoration-id="app-main"]'],
  // Without this the router's built-in fallback prints the message and nothing
  // else — no retry, no stack, and no hint that a dead endpoint is the usual
  // cause. See route-error.tsx.
  defaultErrorComponent: RouteError,
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}

function QueryProvider({ children }: { children: React.ReactNode }) {
  const { toast } = useToast()
  const [queryClient] = useState(
    () =>
      new QueryClient({
        // These fire per failed request, so a dropped connection would raise
        // one for every query and mutation in flight at the time. They are not
        // gated here: `toast()` drops a transport failure while the shell
        // already knows it is offline — see `isOfflineNoise` in ui/toast.tsx.
        queryCache: new QueryCache({
          onError: (error) => {
            if (!isNetworkError(error)) return
            toast({
              title: "Network error",
              description: getErrorMessage(error),
              variant: "danger",
            })
          },
        }),
        mutationCache: new MutationCache({
          onError: (error) => {
            if (!isNetworkError(error)) return
            toast({
              title: "Network error",
              description: getErrorMessage(error),
              variant: "danger",
            })
          },
        }),
        defaultOptions: {
          queries: {
            staleTime: 1000 * 30,
            retry: 1,
          },
        },
      }),
  )

  // When the active endpoint changes, reset all queries scoped to the
  // previous endpoint so stale data from the old (baseUrl, region) pair is
  // never shown.
  useEffect(() => {
    return endpointStore.subscribe((prev) => {
      void queryClient.resetQueries({ queryKey: [prev.baseUrl, prev.region] })
    })
  }, [queryClient])

  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
}

function App() {
  const [devToolsOpen, setDevToolsOpen] = useState(false)
  const toggle = useCallback(() => setDevToolsOpen((v) => !v), [])

  return (
    <StrictMode>
      <ToastContextProvider>
        <DevToolsContext value={{ open: devToolsOpen, toggle }}>
          <QueryProvider>
            <RouterProvider router={router} />
            {import.meta.env.DEV && devToolsOpen && (
              <Suspense>
                <DevToolsPanel />
              </Suspense>
            )}
          </QueryProvider>
        </DevToolsContext>
      </ToastContextProvider>
    </StrictMode>
  )
}

const root = document.getElementById("root")!
createRoot(root).render(<App />)

// After first paint, warm every route's code chunk in the background so a
// later navigation never has to compete for a socket with the SSE stream,
// invoke progress streams or S3 transfers. Code only — loaders and queries
// still run on intent/navigation. See preload-route-chunks.ts.
preloadRouteChunksWhenIdle(router)
