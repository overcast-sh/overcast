/**
 * endpoint-store — module-level singleton for the active emulator endpoint.
 *
 * Components subscribe via useSyncExternalStore (see use-endpoint.tsx).
 * React Query cache is reset automatically on endpoint change via the
 * subscription wired in main.tsx.
 *
 * Key factory functions (e.g. s3Keys.buckets()) call getKeys() at call time,
 * so every render always sees the current endpoint in the query key.
 */

import {
  DEFAULT_ENDPOINT,
  endpointResolver,
  fetchServerRegion,
  hasPersistedRegion,
  isConfigured,
} from "./discovery"
import type { EmulatorEndpoint, SetEndpointOptions } from "./discovery"

type Listener = (prev: EmulatorEndpoint, next: EmulatorEndpoint) => void

let current: EmulatorEndpoint = endpointResolver.get()
const listeners = new Set<Listener>()

export const endpointStore = {
  get: (): EmulatorEndpoint => current,

  set(next: EmulatorEndpoint, opts?: SetEndpointOptions): void {
    // Persist even if the values match the in-memory default — an explicit
    // set must make isConfigured() return true on the next page load. Only
    // explicit (user-entered) sets persist baseUrl/label; implicit ones
    // (region seeding, region switches) persist the region alone.
    const wasConfigured = isConfigured()
    endpointResolver.set(next, opts)
    // Unchanged values need no notice — unless this set is what configured
    // the endpoint. Accepting the connection dialog's prefilled default is
    // exactly that case: `useIsConfigured` has to hear about it, or the
    // dialog stays up until a reload.
    if (
      current.baseUrl === next.baseUrl &&
      current.region === next.region &&
      current.label === next.label &&
      (wasConfigured || !isConfigured())
    )
      return
    const prev = current
    current = next
    listeners.forEach((l) => l(prev, next))
  },

  /** Clears persisted endpoint so isConfigured() returns false on next page load. */
  reset(): void {
    const prev = current
    current = DEFAULT_ENDPOINT
    endpointResolver.clear()
    listeners.forEach((l) => l(prev, DEFAULT_ENDPOINT))
  },

  subscribe(listener: Listener): () => void {
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  },

  /**
   * Seeds the region from the server's OVERCAST_DEFAULT_REGION when nothing
   * has chosen one yet. The check runs again when the answer arrives: the
   * router's `?region=` (or the user) can pick a region while the request is
   * in flight, and the server default is only a fallback for having none.
   * Checking only up front let a slower /_overcast/info overwrite a URL's
   * `?region=` with the server default.
   */
  async seedServerRegion(): Promise<void> {
    if (hasPersistedRegion()) return
    const serverRegion = await fetchServerRegion(current.baseUrl)
    if (!serverRegion || hasPersistedRegion() || current.region === serverRegion) return
    endpointStore.set({ ...current, region: serverRegion })
  },

  /** Returns [baseUrl, region] — use as the first two segments of every endpoint-scoped query key. */
  getKeys(): readonly [baseUrl: string, region: string] {
    return [current.baseUrl, current.region]
  },
}
