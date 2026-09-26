import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ShieldCheck } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { JsonEditor } from "@/components/ui/json-editor"
import { EmptyState } from "@/components/ui/primitives"
import { ResourceListCard } from "@/components/ui/resource-list-page"
import { SkeletonRows } from "@/components/ui/skeleton"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { reindentJson } from "@/lib/json-text"
import {
  configKey,
  deletePolicyMutationOptions,
  policyQueryOptions,
  putPolicyMutationOptions,
  type ConfigTarget,
} from "../data"

/** A starting point for a new policy: read access for one role, on this resource. */
function templatePolicy(target: ConfigTarget): string {
  const resource = target.resource.kind === "bucket" ? `${target.arn}/table/*` : target.arn
  return reindentJson(
    JSON.stringify({
      Version: "2012-10-17",
      Statement: [
        {
          Effect: "Allow",
          Principal: { AWS: "arn:aws:iam::000000000000:role/reader" },
          Action: ["s3tables:GetTable", "s3tables:GetTableData"],
          Resource: resource,
        },
      ],
    }),
  )
}

function parseProblem(text: string): string | null {
  try {
    JSON.parse(text)
    return null
  } catch (error) {
    return error instanceof Error ? error.message : "Not valid JSON"
  }
}

/** The bucket's or table's resource policy, edited as JSON. */
export function PolicyTab({ target }: { target: ConfigTarget }) {
  const policy = useQuery(policyQueryOptions(target))
  /** The text being edited, or null while the stored policy is shown as it is. */
  const [draft, setDraft] = useState<string | null>(null)
  const [confirmingDelete, setConfirmingDelete] = useState(false)
  const noun = target.resource.kind === "bucket" ? "table bucket" : "table"
  const save = useResourceMutation({
    options: putPolicyMutationOptions(target),
    invalidateKeys: [configKey(target, "policy")],
    successTitle: "Policy saved",
    onSuccess: () => setDraft(null),
  })
  const remove = useResourceMutation({
    options: deletePolicyMutationOptions(target),
    invalidateKeys: [configKey(target, "policy")],
    successTitle: "Policy deleted",
    successVariant: "default",
    onSuccess: () => setConfirmingDelete(false),
  })

  const advisory = (
    <Advisory
      tone="info"
      title="Stored, not enforced"
      docsPath="services/s3tables/limitations.md#stored-never-run"
    >
      Overcast keeps and returns the policy exactly as written, but no request is ever refused
      because of it.
    </Advisory>
  )

  if (policy.isLoading) return <SkeletonRows rows={6} noun="policy" />
  if (policy.error) {
    return (
      <EmptyState
        icon={<ShieldCheck className="size-8" />}
        title="Could not read the policy"
        description={policy.error.message}
      />
    )
  }
  const stored = policy.data ? reindentJson(policy.data) : null
  if (stored === null && draft === null) {
    return (
      <div className="flex flex-col gap-3">
        {advisory}
        <ResourceListCard>
          <EmptyState
            icon={<ShieldCheck className="size-8" />}
            title="No resource policy"
            description={`Nothing but IAM decides who can reach this ${noun}.`}
            action={
              <Button size="sm" onClick={() => setDraft(templatePolicy(target))}>
                Add a policy
              </Button>
            }
          />
        </ResourceListCard>
      </div>
    )
  }
  const text = draft ?? stored ?? ""
  const problem = draft === null ? null : parseProblem(draft)
  return (
    <div className="flex flex-col gap-3">
      {advisory}
      <JsonEditor
        value={text}
        onChange={setDraft}
        error={problem}
        minHeight={260}
        className="max-w-4xl"
      />
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          disabled={draft === null || problem !== null}
          busy={save.isPending}
          busyLabel="Saving"
          onClick={() => draft !== null && save.mutate(draft)}
        >
          Save policy
        </Button>
        {draft !== null && (
          <Button size="sm" variant="ghost" onClick={() => setDraft(null)}>
            Discard changes
          </Button>
        )}
        {stored !== null && (
          <Button
            size="sm"
            variant="ghost"
            className="ml-auto text-danger"
            onClick={() => setConfirmingDelete(true)}
          >
            Delete policy
          </Button>
        )}
      </div>
      <ConfirmDialog
        open={confirmingDelete}
        onOpenChange={setConfirmingDelete}
        title="Delete policy?"
        description={`Remove this ${noun}'s resource policy.`}
        variant="danger"
        isPending={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </div>
  )
}
