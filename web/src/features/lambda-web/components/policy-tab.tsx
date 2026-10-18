import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ShieldCheck } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { CopyButton } from "@/components/ui/copy-button"
import { JsonEditor } from "@/components/ui/json-editor"
import { EmptyState } from "@/components/ui/primitives"
import { ResourceListCard } from "@/components/ui/resource-list-page"
import { SkeletonRows } from "@/components/ui/skeleton"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { reindentJson } from "@/lib/json-text"
import {
  deletePolicyMutationOptions,
  lambdaWebKeys,
  putPolicyMutationOptions,
  webPolicyQueryOptions,
} from "../data"

/** A starting point for a new policy: one role in the function's account may call its endpoints. */
function templatePolicy(functionArn: string): string {
  const account = functionArn.split(":")[4] ?? ""
  return reindentJson(
    JSON.stringify({
      Version: "2012-10-17",
      Statement: [
        {
          Effect: "Allow",
          Principal: { AWS: `arn:aws:iam::${account}:role/caller` },
          Action: "lambda:InvokeWebFunctionEndpoint",
          Resource: `${functionArn}/endpoint/*`,
        },
      ],
    }),
  )
}

/** Pretty-printed; a stored policy that is somehow not JSON shows as it is. */
function formatted(policy: string): string {
  try {
    return reindentJson(policy)
  } catch {
    return policy
  }
}

function parseProblem(text: string): string | null {
  try {
    JSON.parse(text)
    return null
  } catch (error) {
    return error instanceof Error ? error.message : "Not valid JSON"
  }
}

/**
 * The function's resource policy, edited as JSON. Every change carries the
 * policy's revision id, so a policy changed elsewhere since it was loaded is
 * refused rather than overwritten; a public policy is refused with the
 * service's own explanation, shown beside the button.
 */
export function PolicyTab({ functionArn }: { functionArn: string }) {
  const policy = useQuery(webPolicyQueryOptions(functionArn))
  /** The text being edited, or null while the stored policy is shown as it is. */
  const [draft, setDraft] = useState<string | null>(null)
  const [confirmingDelete, setConfirmingDelete] = useState(false)
  const save = useResourceMutation({
    options: putPolicyMutationOptions(functionArn),
    invalidateKeys: [lambdaWebKeys.policy(functionArn)],
    successTitle: "Policy saved",
    errorToast: false,
    onSuccess: () => setDraft(null),
  })
  const remove = useResourceMutation({
    options: deletePolicyMutationOptions(functionArn),
    invalidateKeys: [lambdaWebKeys.policy(functionArn)],
    successTitle: "Policy deleted",
    successVariant: "default",
    errorToast: false,
    onSuccess: () => setConfirmingDelete(false),
  })

  const advisory = (
    <Advisory
      tone="info"
      title="Checked when IAM enforcement is on"
      docsPath="services/lambda-web/limitations.md#not-as-on-aws"
    >
      A policy that grants access to an unspecified set of principals is refused, as on AWS. For an
      IamAuth endpoint, requests are checked against the policy only when signature verification
      and IAM enforcement are enabled (OVERCAST_SIGV4_VALIDATE and OVERCAST_ENFORCE_IAM).
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
  const stored = policy.data ? formatted(policy.data.policy) : null
  const revisionId = policy.data?.revisionId
  const startDraft = () => {
    save.reset()
    setDraft(templatePolicy(functionArn))
  }
  if (stored === null && draft === null) {
    return (
      <div className="flex flex-col gap-3">
        {advisory}
        <ResourceListCard>
          <EmptyState
            icon={<ShieldCheck className="size-8" />}
            title="No resource policy"
            description="Nothing but IAM decides who can reach this function's endpoints."
            action={
              <Button size="sm" onClick={startDraft}>
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
      {revisionId && (
        <p className="flex items-center gap-1.5 font-mono text-2xs text-fg-subtle">
          policy revision {revisionId}
          <CopyButton value={revisionId} noun="policy revision ID" tone="inline" />
        </p>
      )}
      <JsonEditor
        value={text}
        onChange={(next) => {
          save.reset()
          setDraft(next)
        }}
        error={problem}
        minHeight={260}
        className="max-w-4xl"
      />
      {save.error && (
        <p role="alert" className="max-w-4xl text-xs break-words text-danger">
          {save.error.message}
        </p>
      )}
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          disabled={draft === null || problem !== null}
          busy={save.isPending}
          busyLabel="Saving"
          onClick={() => draft !== null && save.mutate({ policy: draft, revisionId })}
        >
          Save policy
        </Button>
        {draft !== null && (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              save.reset()
              setDraft(null)
            }}
          >
            Discard changes
          </Button>
        )}
        {stored !== null && (
          <Button
            size="sm"
            variant="ghost"
            className="ml-auto text-danger"
            onClick={() => {
              remove.reset()
              setConfirmingDelete(true)
            }}
          >
            Delete policy
          </Button>
        )}
      </div>
      <ConfirmDialog
        open={confirmingDelete}
        onOpenChange={setConfirmingDelete}
        title="Delete policy?"
        description={
          <>
            Remove this function&apos;s resource policy.
            {remove.error && (
              <span role="alert" className="mt-2 block text-danger">
                {remove.error.message}
              </span>
            )}
          </>
        }
        variant="danger"
        isPending={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </div>
  )
}
