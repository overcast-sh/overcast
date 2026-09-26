import { useState, type FormEvent } from "react"
import { useNavigate } from "@tanstack/react-router"
import { Code2, FolderInput } from "lucide-react"
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
import { useToast } from "@/components/ui/toast"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { formatQuantity } from "@/lib/format"
import { cfnTableSnippet } from "../../cdk-snippet"
import type { CreateTableWizardState } from "../../create-table-param"
import { createTableMutationOptions, glueKeys } from "../../data"
import { LocationStep } from "./location-step"
import { ReviewStep } from "./review-step"
import { SchemaStep } from "./schema-step"
import { tableFolder, useTableDraft } from "./use-table-draft"

const STEPS = ["Location", "Schema", "Create"] as const

interface CreateTableWizardProps {
  state: CreateTableWizardState
  /** The database to create the table in, when the wizard is opened from one. */
  database?: string
}

/**
 * *Create table from S3 data* — the local stand-in for a Glue crawler, which
 * Overcast does not emulate. Three steps: pick a prefix; read the format and
 * schema from one sampled object and its Hive partitions from the listing;
 * name it and create it with `CreateTable`, optionally adding the partitions.
 */
export function CreateTableWizard({ state, database }: CreateTableWizardProps) {
  return (
    <Dialog open={state.open} onOpenChange={(open) => !open && state.onClose()}>
      <DialogContent size="lg" className="max-w-3xl">
        <WizardBody
          initialLocation={state.location ?? ""}
          database={database ?? "default"}
          onClose={state.onClose}
        />
      </DialogContent>
    </Dialog>
  )
}

interface WizardBodyProps {
  initialLocation: string
  database: string
  onClose: () => void
}

/** Mounted with the dialog's content, so every opening starts afresh. */
function WizardBody({ initialLocation, database, onClose }: WizardBodyProps) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { copy } = useCopyToClipboard()
  const [location, setLocation] = useState(initialLocation)
  // A link that names a prefix opens straight on what was found there.
  const [step, setStep] = useState(tableFolder(initialLocation) ? 1 : 0)
  const draft = useTableDraft(location, database)

  const create = useResourceMutation({
    options: createTableMutationOptions(),
    invalidateKeys: [glueKeys.databases(), glueKeys.tables(), glueKeys.partitions()],
    successTitle: "Table created",
    successDescription: (vars) => `${vars.database}.${vars.tableInput.Name ?? ""}`,
    errorTitle: "Could not create the table",
    onSuccess: (refused, vars) => {
      if (refused.length > 0) {
        toast({
          title: `${formatQuantity(refused.length, "partition")} not added`,
          description: refused[0].ErrorDetail?.ErrorMessage,
          variant: "danger",
        })
      }
      void navigate({
        to: "/glue/$database/$table",
        params: { database: vars.database, table: vars.tableInput.Name ?? "" },
      })
    },
  })

  const canContinue = [
    draft.folder !== null,
    !!draft.tableInput && draft.columns.length > 0 && !draft.sample.error,
    !!draft.tableInput && !draft.problem,
  ][step]
  const onReview = step === STEPS.length - 1

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!canContinue || create.isPending) return
    if (!onReview) {
      setStep(step + 1)
      return
    }
    if (!draft.tableInput) return
    create.mutate({
      database: draft.database,
      createDatabase: draft.isNewDatabase,
      tableInput: draft.tableInput,
      partitions: draft.partitionInputs,
    })
  }

  return (
    <form onSubmit={submit} className="flex min-h-0 flex-col">
      <DialogHeader icon={<FolderInput />}>
        <DialogTitle>Create table from S3 data</DialogTitle>
        <DialogDescription>
          Step {step + 1} of {STEPS.length} · {STEPS[step]}
        </DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-4">
        {step === 0 && <LocationStep location={location} onLocationChange={setLocation} />}
        {step === 1 && <SchemaStep draft={draft} />}
        {step === 2 && <ReviewStep draft={draft} />}
      </DialogBody>
      {onReview && draft.problem && (
        <p role="alert" className="mt-4 text-xs text-danger">
          {draft.problem}
        </p>
      )}
      <DialogFooter hint={<DialogKeyHint action={onReview ? "create" : "continue"} />}>
        {onReview && draft.tableInput && (
          <Button
            type="button"
            variant="ghost"
            onClick={() =>
              draft.tableInput &&
              copy(cfnTableSnippet(draft.database, draft.tableInput), { noun: "CDK snippet" })
            }
          >
            <Code2 className="h-3.5 w-3.5" />
            Copy as CDK
          </Button>
        )}
        {step > 0 ? (
          <Button type="button" variant="ghost" onClick={() => setStep(step - 1)}>
            Back
          </Button>
        ) : (
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
        )}
        <Button type="submit" disabled={!canContinue} busy={create.isPending} busyLabel="Creating">
          {onReview ? "Create table" : "Next"}
        </Button>
      </DialogFooter>
    </form>
  )
}
