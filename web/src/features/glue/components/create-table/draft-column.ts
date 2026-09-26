/** A column being edited. `id` is the row's identity, so a rename or a removal keeps focus in place. */
export interface DraftColumn {
  id: number
  name: string
  type: string
}

let lastId = 0

/** A new draft column with an identity of its own. */
export function draftColumn(name: string, type: string): DraftColumn {
  return { id: ++lastId, name, type }
}
