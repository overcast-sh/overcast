import type { KeyboardEvent } from "react"

/** How far a key moves the selection: a step, or to either end. */
type RovingStep = number | "first" | "last"

/** A horizontal tablist: Left and Right step, Home and End jump. */
export const HORIZONTAL_KEYS: ReadonlyMap<string, RovingStep> = new Map<string, RovingStep>([
  ["ArrowRight", 1],
  ["ArrowLeft", -1],
  ["Home", "first"],
  ["End", "last"],
])

/** A radio group, which answers Up and Down as well as Left and Right. */
export const RADIO_KEYS: ReadonlyMap<string, RovingStep> = new Map<string, RovingStep>([
  ...HORIZONTAL_KEYS,
  ["ArrowDown", 1],
  ["ArrowUp", -1],
])

/**
 * Arrow-key navigation for a composite widget with a roving `tabIndex`: a
 * tablist or a radio group, where only the selected item is in the tab order,
 * so the arrow keys are the only way the keyboard reaches the others.
 *
 * Moves focus to the enabled item matching `itemSelector` that the key
 * points at, wrapping at either end, and clicks it, so it is selected as well
 * as focused. Keys not in `keys` are left alone.
 */
export function handleRovingKeyDown(
  event: KeyboardEvent<HTMLElement>,
  itemSelector: string,
  keys: ReadonlyMap<string, RovingStep> = HORIZONTAL_KEYS,
) {
  const step = keys.get(event.key)
  if (step === undefined) return
  event.preventDefault()
  const items = [
    ...event.currentTarget.querySelectorAll<HTMLElement>(`${itemSelector}:not(:disabled)`),
  ]
  if (items.length === 0) return
  const current = items.indexOf(event.target as HTMLElement)
  const next =
    step === "first"
      ? 0
      : step === "last"
        ? items.length - 1
        : (current + step + items.length) % items.length
  items[next].focus()
  items[next].click()
}
