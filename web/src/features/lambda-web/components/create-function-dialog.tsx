import { useState, type ReactNode } from "react"
import { AppWindow } from "lucide-react"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { SegmentedControl } from "@/components/ui/segmented-control"
import { Switch } from "@/components/ui/switch"
import type { CreateWebFunctionInput } from "@/services/api/lambda-web"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import {
  AUTH_OPTIONS,
  DEPLOYMENT_OPTIONS,
  NAME_RULE,
  emptyEndpointDraft,
  emptyRevisionDraft,
  hasProblems,
  nameProblem,
  revisionProblems,
  toRevisionConfig,
  type EnvEntry,
} from "../models"
import { KeyValueFields } from "./key-value-fields"
import { RevisionFields } from "./revision-fields"

interface CreateFunctionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  error: unknown
  onSubmit: (input: CreateWebFunctionInput) => void
}

/**
 * Creates a web function — just the function, or with its first revision and
 * first endpoint in the same call, as CreateWebFunction allows.
 *
 * The form mounts only while the dialog is open, so each opening starts empty
 * however the last one closed.
 */
export function CreateFunctionDialog(props: CreateFunctionDialogProps) {
  return props.open ? <CreateFunctionForm {...props} /> : null
}

function CreateFunctionForm({ onOpenChange, pending, error, onSubmit }: CreateFunctionDialogProps) {
  const [name, setName] = useState("")
  const [tags, setTags] = useState<EnvEntry[]>([])
  const [withRevision, setWithRevision] = useState(false)
  const [revision, setRevision] = useState(emptyRevisionDraft)
  const [withEndpoint, setWithEndpoint] = useState(false)
  const [endpoint, setEndpoint] = useState(emptyEndpointDraft)
  const [attempted, setAttempted] = useState(false)

  const trimmed = name.trim()
  const nameIssue = trimmed === "" ? undefined : nameProblem(trimmed)
  const revisionIssues = withRevision ? revisionProblems(revision) : {}
  const endpointNameIssue =
    withEndpoint && (endpoint.name === "" ? "Name the endpoint." : nameProblem(endpoint.name))
  const valid = trimmed !== "" && !nameIssue && !hasProblems(revisionIssues) && !endpointNameIssue

  const submit = () => {
    setAttempted(true)
    if (!valid) return
    const tagMap = Object.fromEntries(
      tags.filter((t) => t.key.trim() !== "").map((t) => [t.key.trim(), t.value]),
    )
    onSubmit({
      functionName: trimmed,
      ...(Object.keys(tagMap).length > 0 ? { tags: tagMap } : {}),
      ...(withRevision ? { revisionConfig: toRevisionConfig(revision) } : {}),
      ...(withEndpoint
        ? {
            endpointConfig: {
              endpointName: endpoint.name,
              endpointType: "HomeRegion" as const,
              authType: endpoint.authType,
              autoDeploymentMode: endpoint.deployment,
            },
          }
        : {}),
    })
  }

  return (
    <ResourceFormDialog
      open
      onOpenChange={onOpenChange}
      icon={<AppWindow />}
      title="Create web function"
      description="A web function owns immutable revisions (code and configuration) and the endpoints that serve them."
      action="create"
      submitLabel="Create"
      busyLabel="Creating"
      pending={pending}
      error={error}
      size="lg"
      // Not gated on validity: an empty field would otherwise leave the button dead with no word why.
      canSubmit
      onSubmit={submit}
    >
      <FormField
        label="Function name"
        required
        error={nameIssue}
        hint={nameIssue ? undefined : NAME_RULE}
      >
        <Input
          autoFocus
          value={name}
          placeholder="my-web-app"
          onChange={(event) => setName(event.target.value)}
        />
      </FormField>
      <FormField label="Tags">
        <KeyValueFields entries={tags} onChange={setTags} noun="tag" addLabel="Add tag" />
      </FormField>

      <Section
        title="Create a first revision"
        description="A revision is a package in S3 plus its configuration. Without one the function has no code."
        checked={withRevision}
        onCheckedChange={setWithRevision}
      >
        <RevisionFields
          draft={revision}
          onChange={setRevision}
          problems={attempted ? revisionIssues : {}}
        />
      </Section>

      <Section
        title="Create a first endpoint"
        description="A HomeRegion endpoint. Multi- and per-Region endpoints are added from the function's page."
        checked={withEndpoint}
        onCheckedChange={setWithEndpoint}
      >
        <FormField
          label="Endpoint name"
          required
          error={attempted && endpointNameIssue ? endpointNameIssue : undefined}
        >
          <Input
            value={endpoint.name}
            placeholder="production"
            onChange={(event) => setEndpoint({ ...endpoint, name: event.target.value })}
          />
        </FormField>
        <FormField label="Authentication">
          <SegmentedControl
            label="Authentication"
            size="md"
            value={endpoint.authType}
            options={AUTH_OPTIONS}
            onChange={(authType) => setEndpoint({ ...endpoint, authType })}
          />
        </FormField>
        <FormField label="Deployment">
          <SegmentedControl
            label="Deployment"
            size="md"
            value={endpoint.deployment}
            options={DEPLOYMENT_OPTIONS}
            onChange={(deployment) => setEndpoint({ ...endpoint, deployment })}
          />
        </FormField>
      </Section>
    </ResourceFormDialog>
  )
}

function Section({
  title,
  description,
  checked,
  onCheckedChange,
  children,
}: {
  title: string
  description: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-4 border-t border-border pt-4">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium">{title}</p>
          <p className="text-xs text-fg-subtle">{description}</p>
        </div>
        <Switch checked={checked} onCheckedChange={onCheckedChange} aria-label={title} />
      </div>
      {checked && children}
    </div>
  )
}
