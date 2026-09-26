import { useState } from "react"
import { Save } from "lucide-react"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import { Textarea } from "@/components/ui/textarea"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { athenaKeys, createNamedQueryMutationOptions } from "../data"
import type { QueryTab } from "../query-tabs"

/**
 * *Save as named query*: the tab's SQL, database and workgroup as a
 * `NamedQuery`, under the name the tab already has unless changed here.
 */
export function SaveQueryDialog({
  tab,
  open,
  onOpenChange,
}: {
  tab: QueryTab
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [name, setName] = useState(tab.title)
  const [description, setDescription] = useState("")
  const save = useResourceMutation({
    options: createNamedQueryMutationOptions(),
    invalidateKeys: [athenaKeys.namedQueries()],
    successTitle: "Query saved",
    successDescription: (input) => input.Name ?? "",
    errorTitle: "Could not save the query",
    onSuccess: () => onOpenChange(false),
  })
  return (
    <ResourceFormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<Save />}
      title="Save as named query"
      description={`${tab.database} · ${tab.workGroup}`}
      action="save"
      submitLabel="Save query"
      busyLabel="Saving"
      pending={save.isPending}
      error={save.error}
      canSubmit={name.trim() !== "" && tab.sql.trim() !== ""}
      onSubmit={() =>
        save.mutate({
          Name: name.trim(),
          Description: description.trim() || undefined,
          Database: tab.database,
          WorkGroup: tab.workGroup,
          QueryString: tab.sql,
        })
      }
    >
      <FormField label="Name" required>
        <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} maxLength={128} />
      </FormField>
      <FormField label="Description">
        <Textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          maxLength={1024}
          rows={2}
        />
      </FormField>
    </ResourceFormDialog>
  )
}
