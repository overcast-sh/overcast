import { useState, type FormEvent } from "react"
import { useQuery } from "@tanstack/react-query"
import { Tag } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable } from "@/components/ui/resource-table"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  configKey,
  tagResourceMutationOptions,
  tagsQueryOptions,
  untagResourceMutationOptions,
  type ConfigTarget,
} from "../data"

type TagEntry = { key: string; value: string }

/** The resource's tags: a key/value list with an add row above it. */
export function TagsTab({ target }: { target: ConfigTarget }) {
  const { data, isLoading, error } = useQuery(tagsQueryOptions(target))
  const [key, setKey] = useState("")
  const [value, setValue] = useState("")
  const [deleteTarget, setDeleteTarget] = useState<TagEntry>()
  const add = useResourceMutation({
    options: tagResourceMutationOptions(target),
    invalidateKeys: [configKey(target, "tags")],
    successTitle: "Tag saved",
    successDescription: (tags) => Object.keys(tags).join(", "),
    onSuccess: () => {
      setKey("")
      setValue("")
    },
  })
  const remove = useResourceMutation({
    options: untagResourceMutationOptions(target),
    invalidateKeys: [configKey(target, "tags")],
    successTitle: "Tag removed",
    successVariant: "default",
    onSuccess: () => setDeleteTarget(undefined),
  })
  const tags = Object.entries(data ?? {}).map(([k, v]): TagEntry => ({ key: k, value: v }))
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (key.trim()) add.mutate({ [key.trim()]: value })
  }
  return (
    <ResourceListSection
      actions={
        <form onSubmit={submit} className="flex w-full max-w-2xl items-center gap-2">
          <Input
            aria-label="Tag key"
            placeholder="key"
            value={key}
            onChange={(event) => setKey(event.target.value)}
          />
          <Input
            aria-label="Tag value"
            placeholder="value"
            value={value}
            onChange={(event) => setValue(event.target.value)}
          />
          <Button
            type="submit"
            size="sm"
            busy={add.isPending}
            busyLabel="Saving"
            disabled={!key.trim()}
          >
            Add tag
          </Button>
        </form>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: tags, isLoading, error }}
        noun="tags"
        emptyIcon={Tag}
        emptyTitle="No tags"
        emptyDescription="Add a key and value above. A key that exists already takes the new value."
        errorTitle="Could not read the tags"
        rowKey={(t) => t.key}
        defaultSort={{ id: "key", desc: false }}
        columns={[
          { id: "key", header: "Key", sortValue: (t) => t.key, cell: (t) => t.key },
          { header: "Value", cellClassName: "text-fg-muted", cell: (t) => t.value },
        ]}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: remove,
          getVars: (t) => [t.key],
          label: (t) => t.key,
          noun: "tag",
        }}
      />
    </ResourceListSection>
  )
}
