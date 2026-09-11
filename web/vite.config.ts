import fs from "node:fs"
import path from "node:path"
import { defineConfig } from "vite"
import type { PluginOption } from "vite"
import type tailwindcssFactory from "@tailwindcss/vite"
import type { devtools as devtoolsFactory } from "@tanstack/devtools-vite"
import type tanstackRouterFactory from "@tanstack/router-plugin/vite"
import type reactFactory from "@vitejs/plugin-react"
import type { honoDevPlugin as honoDevPluginFactory } from "./api/src/vite-plugin"
import type mkcertFactory from "vite-plugin-mkcert"

export default defineConfig(async () => {
  const usePolling = needsPolling()
  const enableDevtools = process.env.DEVTOOLS === "1"

  // Load plugins in parallel to cut ~10s off startup (they were loading
  // sequentially as top-level imports, each with large dep trees).
  const pluginLoaders: Promise<unknown>[] = [
    import("@tanstack/router-plugin/vite"),
    import("@vitejs/plugin-react"),
    import("@tailwindcss/vite"),
    import("./api/src/vite-plugin"),
    import("vite-plugin-mkcert"),
  ]
  if (enableDevtools) {
    pluginLoaders.push(import("@tanstack/devtools-vite"))
  }

  const results = await Promise.all(pluginLoaders)
  const { default: tanstackRouter } = results[0] as {
    default: typeof tanstackRouterFactory
  }
  const { default: react } = results[1] as {
    default: typeof reactFactory
  }
  const tailwindcss = (results[2] as { default: typeof tailwindcssFactory }).default
  const { honoDevPlugin } = results[3] as {
    honoDevPlugin: typeof honoDevPluginFactory
  }
  const mkcert = (results[4] as { default: typeof mkcertFactory }).default

  const plugins: PluginOption[] = []

  if (enableDevtools) {
    const { devtools } = results[5] as {
      devtools: typeof devtoolsFactory
    }
    plugins.push(devtools() as PluginOption)
  }

  plugins.push(
    mkcert({ savePath: path.resolve(__dirname, "../.cert") }),
    tanstackRouter({
      routesDirectory: "./src/routes",
      generatedRouteTree: "./src/routeTree.gen.ts",
      autoCodeSplitting: true,
      routeFileIgnorePattern: String.raw`(^|/)?.*\.(test|spec)\.(ts|tsx)$`,
    }),
    honoDevPlugin(),
    react(),
    tailwindcss(),
  )
  return {
    plugins,
    // The highlight tokenize worker is constructed with { type: "module" };
    // build it as one too, so dev (native ESM) and production run the same
    // module graph and Rollup can split shared chunks instead of inlining a
    // second copy of Prism into an IIFE worker bundle.
    worker: {
      format: "es" as const,
    },
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
      // Reduce filesystem checks during module resolution (Vite perf guide).
      // The project uses .tsx/.ts exclusively — skip probing .mjs/.mts/.jsx.
      extensions: [".ts", ".tsx", ".js", ".json"],
    },
    optimizeDeps: {
      // Only pre-bundle deps needed for the shell/initial page load.
      // Route-specific deps (monaco, xyflow, etc.) are discovered on demand
      // and benefit from autoCodeSplitting.
      include: [
        "react",
        "react-dom",
        "react-simple-code-editor",
        "@tanstack/react-query",
        "@tanstack/react-router",
        "lucide-react",
      ],
      // Don't block startup waiting for a full crawl of all routes.
      holdUntilCrawlEnd: false,
    },
    server: {
      port: 3000,
      host: true,
      open: false,
      // The console debugger's bridge is a WebSocket, which the Hono
      // middleware above (a fetch per request) cannot carry; Vite's own
      // proxy upgrades it straight to the Go BFF, at the same address the
      // Hono app proxies everything else to (api/src/service-discovery.ts).
      // The key is matched against the request URL, query string included,
      // which is how a console on a non-default emulator names it (`?ep=`).
      proxy: {
        "^/api/debugger/targets/.+/ws([?].*)?$": {
          target: goBffEndpoint(),
          ws: true,
        },
      },
      watch: usePolling ? { usePolling: true, interval: 1000 } : {},
      // Pre-transform the app shell so the first page load is fast.
      warmup: {
        clientFiles: [
          "./src/main.tsx",
          "./src/routes/__root.tsx",
          "./src/routes/index.tsx",
          "./src/components/layout/app-shell.tsx",
        ],
      },
    },
  }
})

/** The Go BFF's address, as `api/src/service-discovery.ts` resolves it (the env vars are the same). */
function goBffEndpoint(): string {
  return (
    process.env.GO_BFF_ENDPOINT || `http://localhost:${process.env.OVERCAST_UI_PORT || "4567"}`
  ).replace(/\/$/, "")
}

// Detect whether the workspace is on a host-mounted volume (Windows/macOS)
// where native fs.watch does not work reliably. If /.dockerenv exists we are
// in a container; if /proc/1/mountinfo lists an overlay or 9p/grpcfuse mount
// for the workspace root we are likely on a host volume.
function needsPolling(): boolean {
  try {
    if (!fs.existsSync("/.dockerenv")) return false
    const mountInfo = fs.readFileSync("/proc/1/mountinfo", "utf-8")
    const cwd = process.cwd()
    return mountInfo.split("\n").some((line) => {
      if (!line.includes(cwd) && !line.includes("/workspace")) return false
      return /grpcfuse|9p|vboxsf|fuse\.osxfs|cifs|smb/i.test(line)
    })
  } catch {
    return false
  }
}
