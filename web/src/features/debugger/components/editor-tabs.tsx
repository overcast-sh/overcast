/**
 * The editor tab strip of the Debug panel: one tab per server-rendered editor
 * (`DebuggerEditor`), each with a copy button. Rendering is by `kind` only —
 * a numbered "steps" body becomes lists, anything else is a block to paste —
 * so nothing here knows which protocol or editor it is showing.
 */
import { useState } from "react"
import { CopyButton } from "@/components/ui/copy-button"
import { CodeBlock } from "@/components/ui/primitives"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import type { DebuggerEditor } from "@/types"

const STEP_PREFIX = /^\d+\.\s+/

/**
 * A steps body is lines: numbered ones are the steps, anything else is a
 * sentence before or between them. Consecutive numbered lines become one
 * ordered list, so a list restarts its numbering exactly where the server's
 * text does.
 */
function StepsBody({ body }: { body: string }) {
  const blocks: Array<{ kind: "p"; text: string } | { kind: "ol"; items: string[] }> = []
  for (const line of body.split("\n")) {
    if (line.trim() === "") continue
    if (STEP_PREFIX.test(line)) {
      const item = line.replace(STEP_PREFIX, "")
      const last = blocks.at(-1)
      if (last?.kind === "ol") last.items.push(item)
      else blocks.push({ kind: "ol", items: [item] })
    } else {
      blocks.push({ kind: "p", text: line })
    }
  }
  return (
    <div className="flex flex-col gap-2 text-sm text-fg">
      {blocks.map((block, i) =>
        block.kind === "p" ? (
          <p key={i} className="text-fg-muted">
            {block.text}
          </p>
        ) : (
          <ol key={i} className="list-decimal space-y-1 pl-6 font-mono text-xs">
            {block.items.map((item, j) => (
              <li key={j}>{item}</li>
            ))}
          </ol>
        ),
      )}
    </div>
  )
}

function EditorBody({ editor }: { editor: DebuggerEditor }) {
  if (editor.kind === "steps") return <StepsBody body={editor.body} />
  // json and shell are both a block to paste; an unknown kind is shown the
  // same way rather than hidden, so a new server kind degrades to readable.
  return <CodeBlock className="whitespace-pre">{editor.body}</CodeBlock>
}

export function EditorTabs({ editors }: { editors: DebuggerEditor[] }) {
  const [selected, setSelected] = useState(editors[0].id)
  // The server may drop an editor between polls (a protocol change); fall
  // back to the first rather than rendering no panel.
  const selectedKey = editors.some((e) => e.id === selected) ? selected : editors[0].id
  return (
    <Tabs selectedKey={selectedKey} onSelectionChange={setSelected}>
      <TabList aria-label="Editor" className="gap-4">
        {editors.map((editor) => (
          <Tab key={editor.id} id={editor.id}>
            {editor.label}
          </Tab>
        ))}
      </TabList>
      {editors.map((editor) => (
        <TabPanel key={editor.id} id={editor.id} className="flex flex-col gap-2 pt-3">
          <div className="flex items-center justify-between gap-3">
            <span className="text-xs text-fg-muted">
              {editor.verified ? (
                <>Paste into your editor's attach configuration.</>
              ) : (
                <>Unverified — this configuration has not been tried end to end.</>
              )}
            </span>
            <CopyButton value={editor.body} noun={`${editor.label} configuration`} />
          </div>
          <EditorBody editor={editor} />
        </TabPanel>
      ))}
    </Tabs>
  )
}
