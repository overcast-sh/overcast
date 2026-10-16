import { useState } from "react"
import { Globe } from "lucide-react"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import type { WebEndpoint, WebRevision } from "@/services/api/lambda-web"
import {
  emptyEndpointDraft,
  endpointDraftOf,
  endpointProblems,
  hasProblems,
  type EndpointDraft,
} from "../models"
import { EndpointFields } from "./endpoint-fields"

interface EndpointDialogProps {
  /** The endpoint being edited; absent when creating one. */
  endpoint?: WebEndpoint
  open: boolean
  onOpenChange: (open: boolean) => void
  functionName: string
  homeRegion: string
  revisions: WebRevision[]
  /** The function's endpoint names, which a new one cannot repeat. */
  taken: string[]
  pending: boolean
  error: unknown
  onSubmit: (draft: EndpointDraft) => void
}

/**
 * Creates an endpoint, or edits what UpdateWebFunctionEndpoint can change on
 * one: its description, authentication, deployment mode, traffic weights and
 * limits. A name, type and Regions are fixed at creation.
 *
 * The form mounts only while the dialog is open, so each opening starts from
 * the endpoint as it is now.
 */
export function EndpointDialog(props: EndpointDialogProps) {
  return props.open ? <EndpointForm {...props} /> : null
}

function EndpointForm({
  endpoint,
  onOpenChange,
  functionName,
  homeRegion,
  revisions,
  taken,
  pending,
  error,
  onSubmit,
}: EndpointDialogProps) {
  const creating = endpoint === undefined
  const [draft, setDraft] = useState(() =>
    endpoint ? endpointDraftOf(endpoint) : emptyEndpointDraft(),
  )
  const [attempted, setAttempted] = useState(false)
  const problems = endpointProblems(draft, { creating, taken })
  return (
    <ResourceFormDialog
      open
      onOpenChange={onOpenChange}
      icon={<Globe />}
      title={creating ? "Create endpoint" : `Edit ${endpoint.endpointName}`}
      description={
        creating
          ? `An endpoint exposes ${functionName} and routes its traffic to revisions.`
          : "Name, type and Regions are fixed. An omitted setting keeps its value; clearing a limit removes it."
      }
      action={creating ? "create" : "save"}
      submitLabel={creating ? "Create endpoint" : "Save changes"}
      busyLabel={creating ? "Creating" : "Saving"}
      pending={pending}
      error={error}
      size="lg"
      // Not gated on validity: an empty field would otherwise leave the button dead with no word why.
      canSubmit
      onSubmit={() => {
        setAttempted(true)
        if (!hasProblems(problems)) onSubmit(draft)
      }}
    >
      <EndpointFields
        draft={draft}
        onChange={setDraft}
        problems={attempted ? problems : {}}
        creating={creating}
        homeRegion={homeRegion}
        revisions={revisions}
      />
    </ResourceFormDialog>
  )
}
