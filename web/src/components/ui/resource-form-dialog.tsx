import type { FormEvent, ReactNode } from "react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogKeyHint,
  DialogTitle,
} from "@/components/ui/dialog"

/**
 * The shell of a create or edit dialog: the header band with its icon tile,
 * the fields, and the footer with the `⏎ to <action> · esc to cancel`
 * contract, Cancel, and the primary button with its busy caret.
 *
 * The fields sit in a `<form>`, so `⏎` in a single-line field submits
 * natively and `esc` closes through Radix; the dialog promises both and
 * honours both. A failed submit shows its error above the footer and keeps
 * what was typed.
 *
 * ```tsx
 * <ResourceFormDialog
 *   open={open}
 *   onOpenChange={setOpen}
 *   icon={<Users />}
 *   title="Create workgroup"
 *   action="create"
 *   submitLabel="Create workgroup"
 *   busyLabel="Creating"
 *   pending={create.isPending}
 *   error={create.error}
 *   canSubmit={name.trim() !== ""}
 *   onSubmit={() => create.mutate({ name })}
 * >
 *   <FormField label="Name" required>…</FormField>
 * </ResourceFormDialog>
 * ```
 */
export interface ResourceFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  icon?: ReactNode
  title: string
  description?: ReactNode
  /** The verb the key hint promises: `create`, `save`. */
  action: string
  submitLabel: string
  /** The primary button's label while the mutation runs: `Creating`. */
  busyLabel?: string
  pending?: boolean
  /** The last submit's failure, shown above the footer. */
  error?: unknown
  /** False while the fields cannot be submitted; `⏎` then does nothing either. */
  canSubmit?: boolean
  onSubmit: () => void
  size?: "sm" | "md" | "lg"
  children: ReactNode
}

function errorMessage(error: unknown): string | undefined {
  if (!error) return undefined
  return error instanceof Error ? error.message : String(error)
}

export function ResourceFormDialog({
  open,
  onOpenChange,
  icon,
  title,
  description,
  action,
  submitLabel,
  busyLabel,
  pending = false,
  error,
  canSubmit = true,
  onSubmit,
  size = "md",
  children,
}: ResourceFormDialogProps) {
  const message = errorMessage(error)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (canSubmit && !pending) onSubmit()
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size={size}>
        <form onSubmit={submit} className="flex min-h-0 flex-col">
          <DialogHeader icon={icon}>
            <DialogTitle>{title}</DialogTitle>
            {description && <DialogDescription>{description}</DialogDescription>}
          </DialogHeader>
          <DialogBody className="flex flex-col gap-4">{children}</DialogBody>
          {message && (
            <p role="alert" className="mt-4 text-xs break-words text-danger">
              {message}
            </p>
          )}
          <DialogFooter hint={<DialogKeyHint action={action} />}>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" busy={pending} busyLabel={busyLabel} disabled={!canSubmit}>
              {submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
