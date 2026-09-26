import { useState } from "react"
import type { WorkGroup } from "@aws-sdk/client-athena"
import { Users } from "lucide-react"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import { Switch } from "@/components/ui/switch"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { athenaKeys, createWorkGroupMutationOptions, updateWorkGroupMutationOptions } from "../data"
import {
  createWorkGroupInput,
  MIN_CUTOFF_MB,
  updateWorkGroupInput,
  workGroupForm,
  workGroupFormErrors,
  type WorkGroupForm,
} from "../workgroup-form"

/** Create a workgroup, or edit one (`workGroup`): its result location, enforcement and cutoff. */
export function WorkGroupFormDialog({
  workGroup,
  onClose,
}: {
  workGroup?: WorkGroup
  onClose: () => void
}) {
  const editing = workGroup !== undefined
  const [original] = useState(() => workGroupForm(workGroup))
  const [form, setForm] = useState(original)
  const [touched, setTouched] = useState(false)
  const errors = workGroupFormErrors(form)
  const shownErrors = touched ? errors : {}
  const set = (patch: Partial<WorkGroupForm>) => setForm((f) => ({ ...f, ...patch }))
  const options = { invalidateKeys: [athenaKeys.workGroups()], onSuccess: onClose }
  const create = useResourceMutation({
    ...options,
    options: createWorkGroupMutationOptions(),
    successTitle: "Workgroup created",
    errorTitle: "Could not create the workgroup",
  })
  const update = useResourceMutation({
    ...options,
    options: updateWorkGroupMutationOptions(),
    successTitle: "Workgroup updated",
    errorTitle: "Could not update the workgroup",
  })
  const mutation = editing ? update : create

  return (
    <ResourceFormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      icon={<Users />}
      title={editing ? `Edit ${workGroup.Name}` : "Create workgroup"}
      description="Where results go, and whether queries may override it"
      action={editing ? "save" : "create"}
      submitLabel={editing ? "Save" : "Create workgroup"}
      busyLabel={editing ? "Saving" : "Creating"}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => {
        setTouched(true)
        if (Object.keys(errors).length > 0) return
        if (editing) update.mutate(updateWorkGroupInput(original, form))
        else create.mutate(createWorkGroupInput(form))
      }}
    >
      {!editing && (
        <FormField label="Name" required error={shownErrors.name}>
          <Input
            autoFocus
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder="adhoc"
          />
        </FormField>
      )}
      <FormField label="Description">
        <Input value={form.description} onChange={(e) => set({ description: e.target.value })} />
      </FormField>
      <FormField
        label="Query result location"
        hint="Where each query writes its result CSV and metadata."
        error={shownErrors.outputLocation}
      >
        <Input
          value={form.outputLocation}
          onChange={(e) => set({ outputLocation: e.target.value })}
          placeholder="s3://athena-results/adhoc/"
        />
      </FormField>
      <label className="flex items-start justify-between gap-4">
        <span className="flex flex-col gap-0.5">
          <span className="text-xs text-fg">Override client-side settings</span>
          <span className="text-xs text-fg-subtle">
            Queries use this workgroup&rsquo;s result location, whatever they ask for.
          </span>
        </span>
        <Switch checked={form.enforce} onCheckedChange={(enforce) => set({ enforce })} />
      </label>
      <FormField
        label="Data scanned limit per query (MB)"
        hint={`Blank for none; at least ${MIN_CUTOFF_MB} MB.`}
        error={shownErrors.cutoffMb}
      >
        <Input
          inputMode="decimal"
          value={form.cutoffMb}
          onChange={(e) => set({ cutoffMb: e.target.value })}
        />
      </FormField>
    </ResourceFormDialog>
  )
}
