import type { Scope } from "./session/session"

const SCOPE_LABELS: Partial<Record<string, string>> = {
  local: "Local",
  closure: "Closure",
  block: "Block",
  catch: "Catch",
  with: "With",
  script: "Script",
  module: "Module",
  global: "Global",
  eval: "Eval",
}

/** `Closure (handler)` when the runtime names the scope, `Closure` when it does not. */
export function scopeLabel(scope: Pick<Scope, "kind" | "name">): string {
  const label = SCOPE_LABELS[scope.kind] ?? scope.kind
  return scope.name ? `${label} (${scope.name})` : label
}
