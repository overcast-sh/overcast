import { useState } from "react"
import type { Table } from "@aws-sdk/client-glue"
import { FolderPlus } from "lucide-react"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { ResourceFormDialog } from "@/components/ui/resource-form-dialog"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { createPartitionMutationOptions, glueKeys } from "../../data"
import { hiveEscapePath } from "../../hive-partitions"
import { tableLocation } from "../../table-format"
import { buildPartitionInput } from "../../table-input"

interface AddPartitionDialogProps {
  table: Table
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** Where Hive puts a partition by default: `key=value/` folders under the table's location. */
function defaultLocation(table: Table, keys: string[], values: string[]): string {
  const base = tableLocation(table) ?? ""
  const folders = keys
    .map((k, i) => `${k.toLowerCase()}=${hiveEscapePath(values[i] ?? "")}/`)
    .join("")
  return `${base.endsWith("/") ? base : `${base}/`}${folders}`
}

/** CreatePartition: a value per key, and the prefix holding the partition's files. */
export function AddPartitionDialog({ table, open, onOpenChange }: AddPartitionDialogProps) {
  const keys = (table.PartitionKeys ?? []).map((k) => k.Name ?? "")
  const [values, setValues] = useState<string[]>(() => keys.map(() => ""))
  const [location, setLocation] = useState<string>()
  const shownLocation = location ?? defaultLocation(table, keys, values)

  const close = () => {
    onOpenChange(false)
    setValues(keys.map(() => ""))
    setLocation(undefined)
  }
  const create = useResourceMutation({
    options: createPartitionMutationOptions(table.DatabaseName ?? "", table.Name ?? ""),
    invalidateKeys: [glueKeys.partitions()],
    successTitle: "Partition added",
    successDescription: (input) => (input.Values ?? []).join(" / "),
    errorTitle: "Could not add the partition",
    onSuccess: close,
  })

  return (
    <ResourceFormDialog
      open={open}
      onOpenChange={(next) => (next ? onOpenChange(true) : close())}
      icon={<FolderPlus />}
      title="Add partition"
      description={`${table.DatabaseName}.${table.Name}`}
      action="add"
      submitLabel="Add partition"
      busyLabel="Adding"
      pending={create.isPending}
      canSubmit={values.every((v) => v !== "")}
      onSubmit={() => create.mutate(buildPartitionInput(table, values, shownLocation))}
    >
      {keys.map((key, i) => (
        <FormField key={key} label={key} required>
          <Input
            className="font-mono text-xs"
            value={values[i]}
            autoFocus={i === 0}
            onChange={(e) => setValues(values.map((v, j) => (j === i ? e.target.value : v)))}
          />
        </FormField>
      ))}
      <FormField label="Location" hint="Defaults to key=value/ folders under the table's location.">
        <Input
          className="font-mono text-xs"
          value={shownLocation}
          onChange={(e) => setLocation(e.target.value)}
        />
      </FormField>
    </ResourceFormDialog>
  )
}
