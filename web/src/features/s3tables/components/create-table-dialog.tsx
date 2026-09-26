import { useState } from "react"
import { useNavigate } from "@tanstack/react-router"
import { Code2, Table2 } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { SectionLabel } from "@/components/ui/primitives"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import { Select } from "@/components/ui/select"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  draftProblems,
  hasNestedFields,
  newSchemaRow,
  partitionableColumns,
  toCdk,
  toCreateTableInput,
  type PartitionRow,
  type SchemaRow,
} from "../create-table-model"
import { createTableMutationOptions, s3tablesKeys, tableIdOf } from "../data"
import { IDENTIFIER_RULE } from "../names"
import { PartitionSpecEditor } from "./partition-spec-editor"
import { SchemaBuilder } from "./schema-builder"

interface CreateTableDialogProps {
  bucketName: string
  tableBucketARN: string
  namespaces: string[]
  /** The namespace the table goes in by default; null while the dialog is closed. */
  namespace: string | null
  onClose: () => void
}

/**
 * *Create table*: a name, a schema and an optional partition spec, created
 * with `CreateTable` and its `metadata.iceberg` so the table has a first
 * `metadata.json` straight away. *Copy as CDK* gives the same table as a
 * `CfnTable`, for the stack that will own it.
 */
export function CreateTableDialog(props: CreateTableDialogProps) {
  // Keyed on the namespace it opened with, so each opening starts a fresh draft.
  return props.namespace === null ? null : <CreateTableForm key={props.namespace} {...props} />
}

function CreateTableForm({
  bucketName,
  tableBucketARN,
  namespaces,
  namespace: initialNamespace,
  onClose,
}: CreateTableDialogProps) {
  const navigate = useNavigate()
  const { copy } = useCopyToClipboard()
  const [namespace, setNamespace] = useState(initialNamespace ?? "")
  const [name, setName] = useState("")
  const [rows, setRows] = useState<SchemaRow[]>(() => [newSchemaRow()])
  const [partitions, setPartitions] = useState<PartitionRow[]>([])
  const [attempted, setAttempted] = useState(false)

  const create = useResourceMutation({
    options: createTableMutationOptions(),
    invalidateKeys: [s3tablesKeys.tables()],
    successTitle: "Table created",
    successDescription: (input) => `${input.namespace}.${input.name}`,
    onSuccess: (tableARN) => {
      onClose()
      void navigate({
        to: "/s3tables/$bucket/$tableId",
        params: { bucket: bucketName, tableId: tableIdOf(tableARN) },
      })
    },
  })

  const draft = { tableBucketARN, namespace, name: name.trim(), rows, partitions }
  const problems = draftProblems(draft)
  const nested = hasNestedFields(rows)

  return (
    <ResourceFormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      size="lg"
      icon={<Table2 />}
      title="Create table"
      description={`Iceberg table in ${bucketName}`}
      action="create"
      submitLabel="Create table"
      busyLabel="Creating"
      pending={create.isPending}
      error={create.error}
      // Nested structs need schemaV2, which Overcast does not emulate (the advisory says so).
      canSubmit={!nested && (problems.length === 0 || !attempted)}
      onSubmit={() => {
        setAttempted(true)
        if (problems.length === 0) create.mutate(toCreateTableInput(draft))
      }}
    >
      <div className="grid grid-cols-[12rem_minmax(0,1fr)] gap-3">
        <FormField label="Namespace" required>
          <Select value={namespace} onChange={(event) => setNamespace(event.target.value)}>
            {namespaces.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </FormField>
        <FormField label="Table name" required hint={IDENTIFIER_RULE}>
          <Input
            autoFocus
            value={name}
            placeholder="orders"
            onChange={(event) => setName(event.target.value)}
          />
        </FormField>
      </div>

      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between gap-2">
          <SectionLabel as="h3">Schema</SectionLabel>
          <span className="text-2xs text-fg-subtle">
            ⌥→ indents a field under the struct above it
          </span>
        </div>
        <SchemaBuilder rows={rows} onChange={setRows} />
      </section>

      {nested && (
        <Advisory
          title="Nested structs are not emulated yet"
          docsPath="services/s3tables/limitations.md#iceberg-metadata"
        >
          On AWS this table is created with <code>metadata.iceberg.schemaV2</code>, which Overcast
          answers with 501 for now. Copy it as CDK for your stack, or create the table flat here and
          add the struct columns through the Iceberg REST catalog.
        </Advisory>
      )}

      <section className="flex flex-col gap-2">
        <SectionLabel as="h3">Partition spec · optional</SectionLabel>
        <PartitionSpecEditor
          columns={partitionableColumns(rows)}
          rows={partitions}
          onChange={setPartitions}
        />
      </section>

      {attempted && problems.length > 0 && (
        <ul role="alert" className="flex flex-col gap-0.5 text-xs text-danger">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}

      <div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={problems.length > 0}
          title={problems.length > 0 ? problems[0] : undefined}
          onClick={() => copy(toCdk(draft), { noun: "CDK CfnTable" })}
        >
          <Code2 className="size-3.5" />
          Copy as CDK
        </Button>
      </div>
    </ResourceFormDialog>
  )
}
