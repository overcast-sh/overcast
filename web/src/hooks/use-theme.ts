import { useEffect, useSyncExternalStore } from "react"
import { useLocalStorage } from "@/hooks/use-local-storage"

type Theme = "light" | "dark" | "system"

const STORAGE_KEY = "overcast:theme"

function applyTheme(theme: Theme) {
  const root = document.documentElement
  if (theme === "system") {
    root.removeAttribute("data-theme")
  } else {
    root.setAttribute("data-theme", theme)
  }
}

function readStoredTheme(): Theme {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === null) return "system"
    const parsed: unknown = JSON.parse(raw)
    return parsed === "light" || parsed === "dark" ? parsed : "system"
  } catch {
    // Corrupt JSON, or localStorage unavailable (private browsing): the
    // system preference is the honest answer, not a crash on boot.
    return "system"
  }
}

/**
 * Applies the persisted theme to `<html>` at boot, before React renders.
 *
 * The hook below only runs where it is mounted, and its only mount point is
 * the header — which lives *behind* the connection gate. A console that
 * cannot reach its emulator yet (the connection dialog, the cold-boot
 * screen) therefore rendered in the system theme however the user had set
 * it, and so did the first paint of every load. Applying the stored value
 * once, synchronously, from `main.tsx` fixes both: every screen honours the
 * preference, and there is no flash of the wrong theme before hydration.
 */
export function applyStoredTheme(): void {
  applyTheme(readStoredTheme())
}

export function useTheme() {
  const [theme, setTheme] = useLocalStorage<Theme>(STORAGE_KEY, "system")

  useEffect(() => {
    applyTheme(theme)
  }, [theme])

  return { theme, setTheme }
}

const DARK_QUERY = "(prefers-color-scheme: dark)"

function readIsDark(): boolean {
  const explicit = document.documentElement.getAttribute("data-theme")
  if (explicit === "dark") return true
  if (explicit === "light") return false
  return window.matchMedia(DARK_QUERY).matches
}

function subscribeIsDark(onChange: () => void): () => void {
  const observer = new MutationObserver(onChange)
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] })
  const query = window.matchMedia(DARK_QUERY)
  query.addEventListener("change", onChange)
  return () => {
    observer.disconnect()
    query.removeEventListener("change", onChange)
  }
}

/**
 * Whether the page is dark right now — the explicit `data-theme`, else the
 * system preference — and re-rendered when either changes. For a component
 * that paints its own colours rather than reading the CSS tokens (the Monaco
 * editor takes a theme name), so a theme toggle mid-session reaches it too.
 */
export function useIsDarkTheme(): boolean {
  return useSyncExternalStore(subscribeIsDark, readIsDark, () => false)
}
