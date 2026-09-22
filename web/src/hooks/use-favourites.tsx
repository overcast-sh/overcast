import { createContext, useCallback, useContext } from "react"
import { useLocalStorage } from "@/hooks/use-local-storage"
import { isFavouritable } from "@/lib/nav-services"

const FAVOURITES_KEY = "overcast-favourites"
const RECENT_KEY = "overcast-recent-services"
const MAX_RECENT = 8

/** Stable identity so `pinned ?? EMPTY_PINS` does not remount consumers. */
const EMPTY_PINS: string[] = []

interface FavouritesContextValue {
  /** Favourited service keys in display order — the array IS the order. */
  favourites: string[]
  /** Pins go to the end of the order unless `at: "start"` says otherwise. */
  toggleFavourite: (key: string, options?: { at?: "start" | "end" }) => void
  isFavourite: (key: string) => boolean
  reorderFavourites: (ordered: string[]) => void
  /** Recently visited service keys, most-recent first. */
  recentServices: string[]
  addRecentService: (key: string) => void
}

const FavouritesContext = createContext<FavouritesContextValue | null>(null)

/**
 * Pins are stored per user and start empty — every service is always running,
 * so there is no subset the console could infer a useful starting selection
 * from. `null` (never chosen) is still kept distinct from `[]` (chose to pin
 * nothing) because nothing is written until the user pins, unpins, or reorders.
 */
export function FavouritesProvider({ children }: { children: React.ReactNode }) {
  const [pinned, setPinned] = useLocalStorage<string[] | null>(FAVOURITES_KEY, null)
  const [recentServices, setRecentServices] = useLocalStorage<string[]>(RECENT_KEY, [])
  const favourites = pinned ?? EMPTY_PINS

  const toggleFavourite = useCallback(
    (key: string, { at = "end" }: { at?: "start" | "end" } = {}) => {
      setPinned((prev) => {
        const current = prev ?? EMPTY_PINS
        // Unpinning always goes through, so a stale pin can still be removed.
        if (current.includes(key)) return current.filter((k) => k !== key)
        if (!isFavouritable(key)) return current
        return at === "start" ? [key, ...current] : [...current, key]
      })
    },
    [setPinned],
  )

  const reorderFavourites = useCallback(
    (ordered: string[]) => {
      setPinned(ordered)
    },
    [setPinned],
  )

  const addRecentService = useCallback(
    (key: string) => {
      setRecentServices((prev) => [key, ...prev.filter((k) => k !== key)].slice(0, MAX_RECENT))
    },
    [setRecentServices],
  )

  return (
    <FavouritesContext.Provider
      value={{
        favourites,
        toggleFavourite,
        isFavourite: (key) => favourites.includes(key),
        reorderFavourites,
        recentServices,
        addRecentService,
      }}
    >
      {children}
    </FavouritesContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useFavourites() {
  const ctx = useContext(FavouritesContext)
  if (!ctx) throw new Error("useFavourites must be used within FavouritesProvider")
  return ctx
}
