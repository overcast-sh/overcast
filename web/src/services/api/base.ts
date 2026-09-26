/**
 * Shared HTTP helpers for the API client modules.
 *
 * All per-service API modules import from here.
 */

import { endpointResolver } from "../discovery"
import type { EmulatorEndpoint } from "../discovery"

export const API_BASE = "/api"

export function endpointHeaders(endpoint: EmulatorEndpoint): Record<string, string> {
  return {
    "x-overcast-endpoint": endpoint.baseUrl,
    "x-overcast-region": endpoint.region,
  }
}

export interface ApiFetchOptions {
  /**
   * Status codes to decode as data instead of throwing.
   *
   * A few emulator endpoints answer a non-2xx status whose body is still the
   * answer the caller wanted — RDS's logs endpoint 404s a DB instance with no
   * logs and describes the instance in the same breath, and that description
   * is the whole reason the caller asked. Listing the status here keeps the
   * default (any non-2xx is an error) intact everywhere else.
   */
  acceptStatuses?: number[]
}

export async function apiFetch<T>(
  path: string,
  init?: RequestInit,
  opts?: ApiFetchOptions,
): Promise<T> {
  const endpoint = endpointResolver.get()
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...endpointHeaders(endpoint),
      ...init?.headers,
    },
  })

  if (!res.ok && !opts?.acceptStatuses?.includes(res.status)) {
    const body = (await res.json().catch(() => ({ error: res.statusText }))) as {
      error?: string
      message?: string
    }
    throw new Error(body.message ?? body.error ?? `HTTP ${res.status}`)
  }

  // 204 / empty body
  const text = await res.text()
  return text ? (JSON.parse(text) as T) : ({} as T)
}

export { endpointResolver }

/**
 * GETs one of the emulator's own `/_overcast/*` endpoints, straight from the
 * browser: they have no SDK client, so `fetch` is the policy's allowed
 * exception. Throws the emulator's message on a non-2xx status.
 */
export async function overcastFetch<T>(path: string): Promise<T> {
  const ep = endpointResolver.get()
  const res = await fetch(`${ep.baseUrl}${path}`, {
    headers: { "x-overcast-region": ep.region },
  })
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { message?: string; __type?: string }
    throw new Error(body.message ?? body.__type ?? `HTTP ${res.status}`)
  }
  return (await res.json()) as T
}
