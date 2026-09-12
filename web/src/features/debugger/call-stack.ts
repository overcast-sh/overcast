import type { StackFrame } from "./session/session"

/** A call-stack row: one frame, or a run of internal frames folded into one line. */
export type CallStackRow =
  | { kind: "frame"; index: number; frame: StackFrame }
  | { kind: "fold"; key: string; frames: Array<{ index: number; frame: StackFrame }> }

/**
 * Consecutive internal frames — the runtime's bootstrap, Node's own modules —
 * become one fold, so the reader's frames are what the list is about. Frame
 * indices are kept, since selecting a frame is by index.
 */
export function foldFrames(frames: readonly StackFrame[]): CallStackRow[] {
  const rows: CallStackRow[] = []
  frames.forEach((frame, index) => {
    if (!frame.internal) {
      rows.push({ kind: "frame", index, frame })
      return
    }
    const last = rows.at(-1)
    if (last?.kind === "fold") last.frames.push({ index, frame })
    else rows.push({ kind: "fold", key: `fold-${index}`, frames: [{ index, frame }] })
  })
  return rows
}
