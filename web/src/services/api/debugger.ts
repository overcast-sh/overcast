/**
 * Compute debugger — the two emulator-only endpoints under
 * /_overcast/debugger (docs/plans/compute-debugger.md § 6), proxied at
 * /api/debugger/*. Types are generated from the Go structs by cmd/tsgen;
 * nothing here is hand-written.
 */
import { apiFetch } from "./base"
import type { DebuggerTarget, DebuggerTargetList } from "@/types"

// `debugger` is a reserved word, so the module's export cannot carry the
// endpoint's own name the way `lambda` and `ecs` do.
export const debuggerTargets = {
  /** Every registered target, ordered by id. */
  list: () => apiFetch<DebuggerTargetList>("/debugger/targets").then((r) => r.targets),

  /**
   * One target — a registered one, or the synthesised "not tagged" entry for
   * a resource the service knows. `null` when the server has nothing to say
   * about it (404): a resource that does not exist, or a service with no
   * describer yet. The console renders that as "off", never as an error, so
   * the status is accepted here rather than thrown.
   */
  get: (service: string, resource: string, container?: string) => {
    const query = container ? `?container=${encodeURIComponent(container)}` : ""
    return apiFetch<DebuggerTarget | { error: string }>(
      `/debugger/targets/${encodeURIComponent(service)}/${encodeURIComponent(resource)}${query}`,
      undefined,
      { acceptStatuses: [404] },
    ).then((body): DebuggerTarget | null => ("id" in body ? body : null))
  },
}
