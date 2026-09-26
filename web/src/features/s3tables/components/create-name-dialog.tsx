import { useState, type ReactNode } from "react"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"

interface CreateNameDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  icon: ReactNode
  title: string
  description?: ReactNode
  label: string
  placeholder: string
  /** The naming rule, shown under the field. */
  rule: string
  /** Why a name is not allowed, or undefined when it is. */
  problem: (name: string) => string | undefined
  pending: boolean
  error: unknown
  onSubmit: (name: string) => void
}

/**
 * A create dialog whose only field is the new resource's name — a table
 * bucket, a namespace. The naming rule shows as the hint until a typed name
 * breaks it, and then as the error.
 *
 * The form mounts only while the dialog is open, so each opening starts empty
 * however the last one closed — by Cancel, by `esc`, or by the create
 * succeeding.
 */
export function CreateNameDialog(props: CreateNameDialogProps) {
  return props.open ? <CreateNameForm {...props} /> : null
}

function CreateNameForm({
  onOpenChange,
  icon,
  title,
  description,
  label,
  placeholder,
  rule,
  problem,
  pending,
  error,
  onSubmit,
}: CreateNameDialogProps) {
  const [name, setName] = useState("")
  const trimmed = name.trim()
  const issue = trimmed === "" ? undefined : problem(trimmed)
  return (
    <ResourceFormDialog
      open
      onOpenChange={onOpenChange}
      icon={icon}
      title={title}
      description={description}
      action="create"
      submitLabel="Create"
      busyLabel="Creating"
      pending={pending}
      error={error}
      canSubmit={trimmed !== "" && issue === undefined}
      onSubmit={() => onSubmit(trimmed)}
    >
      <FormField label={label} required error={issue} hint={issue ? undefined : rule}>
        <Input
          autoFocus
          value={name}
          placeholder={placeholder}
          onChange={(event) => setName(event.target.value)}
        />
      </FormField>
    </ResourceFormDialog>
  )
}
