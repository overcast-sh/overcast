/**
 * What the device asks of the grid: how much decoded data it may hold, and
 * whether the user asked the browser to fetch less.
 */

/** The decoded-row budget on a capable desktop. */
export const DEFAULT_CACHE_BYTES = 64 * 1024 * 1024

export interface DeviceSignals {
  /** `navigator.deviceMemory`, in GB — Chromium only. */
  deviceMemory?: number
  /** The primary pointer is coarse: a tablet or a phone. */
  coarsePointer?: boolean
}

/**
 * The decoded-row budget for this device: 64 MB, halved when
 * `navigator.deviceMemory` reports 4 GB or less (elsewhere nothing is
 * assumed), and halved again on a tablet or a phone, where the browser is the
 * first thing the OS reclaims.
 */
export function cacheBudget({ deviceMemory, coarsePointer }: DeviceSignals): number {
  let budget = DEFAULT_CACHE_BYTES
  if (deviceMemory !== undefined && deviceMemory <= 4) budget /= 2
  if (coarsePointer) budget /= 2
  return budget
}

/** `navigator.deviceMemory` where the browser has it. */
export function deviceMemory(): number | undefined {
  return typeof navigator === "undefined"
    ? undefined
    : (navigator as Navigator & { deviceMemory?: number }).deviceMemory
}

/**
 * `Save-Data`: the user asked the browser to fetch less, so nothing is
 * indexed in the background — rows load as they are scrolled to.
 */
export function saveDataOn(): boolean {
  if (typeof navigator === "undefined") return false
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection
  return connection?.saveData === true
}
