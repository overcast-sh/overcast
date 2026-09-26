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
 */
export function CreateNameDialog({
  open,
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
  const close = (next: boolean) => {
    if (!next) setName("")
    onOpenChange(next)
  }
  return (
    <ResourceFormDialog
      open={open}
      onOpenChange={close}
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
