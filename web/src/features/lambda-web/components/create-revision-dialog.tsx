import { useState } from "react"
import { Layers } from "lucide-react"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import type { CreateRevisionInput } from "@/services/api/lambda-web"
import { emptyRevisionDraft, hasProblems, revisionProblems, toRevisionConfig } from "../models"
import { RevisionFields } from "./revision-fields"

interface CreateRevisionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  functionName: string
  pending: boolean
  error: unknown
  onSubmit: (input: CreateRevisionInput) => void
}

/**
 * Adds a revision to a function. A revision is immutable: to change one, create
 * the next. Endpoints on `LatestRevision` move to it as soon as it exists.
 *
 * The form mounts only while the dialog is open, so each opening starts empty.
 */
export function CreateRevisionDialog(props: CreateRevisionDialogProps) {
  return props.open ? <CreateRevisionForm {...props} /> : null
}

function CreateRevisionForm({
  onOpenChange,
  functionName,
  pending,
  error,
  onSubmit,
}: CreateRevisionDialogProps) {
  const [draft, setDraft] = useState(emptyRevisionDraft)
  const [attempted, setAttempted] = useState(false)
  const problems = revisionProblems(draft)
  return (
    <ResourceFormDialog
      open
      onOpenChange={onOpenChange}
      icon={<Layers />}
      title="Create revision"
      description={`An immutable snapshot of ${functionName}'s code and configuration.`}
      action="create"
      submitLabel="Create revision"
      busyLabel="Creating"
      pending={pending}
      error={error}
      size="lg"
      // Not gated on validity: an empty field would otherwise leave the button dead with no word why.
      canSubmit
      onSubmit={() => {
        setAttempted(true)
        if (hasProblems(problems)) return
        const config = toRevisionConfig(draft)
        onSubmit({
          functionName,
          description: config.description,
          buildConfig: config.buildConfig,
          serviceConfig: config.serviceConfig,
        })
      }}
    >
      <RevisionFields draft={draft} onChange={setDraft} problems={attempted ? problems : {}} />
    </ResourceFormDialog>
  )
}
