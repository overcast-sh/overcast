import type { CreateTableSearch } from "./views"

/** The create-table wizard's open state, as the page's search params hold it. */
export interface CreateTableWizardState {
  open: boolean
  /** The `s3://` prefix the wizard starts on. */
  location?: string
  onOpen: (location?: string) => void
  onClose: () => void
}

/**
 * Binds the wizard to `?create=s3&location=…`, so a link can open it on a
 * prefix and Back closes it. `search`/`navigate` are the route's own.
 */
export function createTableWizardState<TSearch extends CreateTableSearch>(
  search: TSearch,
  navigate: (opts: { search: (prev: TSearch) => TSearch; replace?: boolean }) => unknown,
): CreateTableWizardState {
  return {
    open: search.create === "s3",
    location: search.location,
    onOpen: (location) =>
      void navigate({ search: (prev) => ({ ...prev, create: "s3", location }) }),
    onClose: () =>
      void navigate({
        search: (prev) => ({ ...prev, create: undefined, location: undefined }),
        replace: true,
      }),
  }
}
