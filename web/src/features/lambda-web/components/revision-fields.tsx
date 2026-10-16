import { useQuery } from "@tanstack/react-query"
import { Combobox } from "@/components/ui/combobox"
import { FormField, FormRow } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { ResourceArnCombobox } from "@/components/ui/resource-arn-combobox"
import { Select } from "@/components/ui/select"
import { s3BucketsQueryOptions } from "@/features/s3/data"
import {
  CONCURRENCY_RANGE,
  RUNTIMES,
  TIMEOUT_RANGE,
  type RevisionDraft,
  type RevisionProblems,
} from "../models"
import { KeyValueFields } from "./key-value-fields"

interface RevisionFieldsProps {
  draft: RevisionDraft
  onChange: (draft: RevisionDraft) => void
  /** Shown once the person has tried to submit, so an untouched form is not all red. */
  problems: RevisionProblems
}

/**
 * The fields of a revision — where its package is, what runs it, and the
 * service configuration — shared by the create-revision dialog and the
 * create-function dialog's first revision.
 */
export function RevisionFields({ draft, onChange, problems }: RevisionFieldsProps) {
  const { data: buckets = [], isPending } = useQuery(s3BucketsQueryOptions())
  const set = (patch: Partial<RevisionDraft>) => onChange({ ...draft, ...patch })
  return (
    <>
      <FormRow>
        <FormField label="S3 bucket" required error={problems.bucket}>
          <Combobox
            aria-label="S3 bucket"
            value={draft.bucket}
            onChange={(bucket) => set({ bucket })}
            items={buckets.map((b) => b.name)}
            filterFn={(name, q) => name.includes(q.toLowerCase())}
            getItemValue={(name) => name}
            renderItem={(name) => <span className="font-mono text-sm">{name}</span>}
            isLoading={isPending}
            allowCustom
            allowFreeText
            placeholder="code-bucket"
            inputClassName="font-mono"
          />
        </FormField>
        <FormField label="Object key" required error={problems.key}>
          <Input
            value={draft.key}
            placeholder="app.zip"
            onChange={(event) => set({ key: event.target.value })}
          />
        </FormField>
      </FormRow>
      <FormField
        label="Object version"
        hint="Optional. Pins the revision to one version of a versioned bucket's object."
      >
        <Input
          value={draft.versionId}
          onChange={(event) => set({ versionId: event.target.value })}
        />
      </FormField>
      <FormField
        label="Runtime"
        hint="Web functions run Node.js only. The package is stored, not unpacked."
      >
        <Select value={draft.runtime} onChange={(event) => set({ runtime: event.target.value })}>
          {RUNTIMES.map((runtime) => (
            <option key={runtime} value={runtime}>
              {runtime}
            </option>
          ))}
        </Select>
      </FormField>
      <FormField label="Execution role ARN" required error={problems.executionRoleArn}>
        <ResourceArnCombobox
          resourceType="iam-role"
          value={draft.executionRoleArn}
          onChange={(executionRoleArn) => set({ executionRoleArn })}
        />
      </FormField>
      <FormRow>
        <FormField
          label="Timeout (seconds)"
          error={problems.timeoutSeconds}
          hint={`${TIMEOUT_RANGE.min} to ${TIMEOUT_RANGE.max}. Default ${TIMEOUT_RANGE.default}.`}
        >
          <Input
            inputMode="numeric"
            value={draft.timeoutSeconds}
            placeholder={String(TIMEOUT_RANGE.default)}
            onChange={(event) => set({ timeoutSeconds: event.target.value })}
          />
        </FormField>
        <FormField
          label="Max concurrency"
          error={problems.maxConcurrency}
          hint={`Per environment, ${CONCURRENCY_RANGE.min} to ${CONCURRENCY_RANGE.max}. Default ${CONCURRENCY_RANGE.default}.`}
        >
          <Input
            inputMode="numeric"
            value={draft.maxConcurrency}
            placeholder={String(CONCURRENCY_RANGE.default)}
            onChange={(event) => set({ maxConcurrency: event.target.value })}
          />
        </FormField>
      </FormRow>
      <FormField label="Description">
        <Input
          value={draft.description}
          maxLength={256}
          onChange={(event) => set({ description: event.target.value })}
        />
      </FormField>
      <FormField label="Environment variables">
        <KeyValueFields
          entries={draft.env}
          onChange={(env) => set({ env })}
          noun="environment variable"
          addLabel="Add variable"
        />
      </FormField>
    </>
  )
}
