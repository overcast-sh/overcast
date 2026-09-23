/**
 * The UTF-8 byte-order mark: optional at the start of a text file, never
 * part of its data. Excel writes one on every "CSV UTF-8" export, and a
 * header read with it attached glues it to the first column's name.
 */

/** Characters to skip at the start of decoded text: 1 when it opens with U+FEFF. */
export function bomLength(text: string): number {
  return text.charCodeAt(0) === 0xfeff ? 1 : 0
}

/** Bytes to skip at the start of raw UTF-8: 3 when it opens with `EF BB BF`. */
export function utf8BomLength(bytes: Uint8Array): number {
  return bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf ? 3 : 0
}
