import { searchOneOf, searchText } from "@/lib/search-params"
import { WEB_FUNCTION_TABS, type WebFunctionTab } from "./data"

/** The Lambda Web Functions routes' search params: the list filter and sort, and the detail tab. */

export interface WebFunctionsSearch {
  q?: string
  sort?: string
}

export interface WebFunctionSearch {
  tab?: WebFunctionTab
}

export const validateWebFunctionsSearch = (s: Record<string, unknown>): WebFunctionsSearch => ({
  q: searchText(s.q),
  sort: searchText(s.sort),
})

export const validateWebFunctionSearch = (s: Record<string, unknown>): WebFunctionSearch => ({
  tab: searchOneOf(WEB_FUNCTION_TABS, s.tab),
})
