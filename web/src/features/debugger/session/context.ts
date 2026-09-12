import { createContext } from "react"
import type { DebugSession } from "./session"

/** The page's session; provided by `DebugSessionProvider`, read by the hooks in `./hooks`. */
export const DebugSessionContext = createContext<DebugSession | null>(null)
