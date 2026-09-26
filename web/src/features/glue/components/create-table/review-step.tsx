import type { Database } from "@aws-sdk/client-glue"
import { Combobox } from "@/components/ui/combobox"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { FormField } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { Switch } from "@/components/ui/switch"
import { formatQuantity } from "@/lib/format"
import type { TableDraftState } from "./use-table-draft"

/** Step 3: name the table and its database, and choose whether to add the partitions found. */
export function ReviewStep({ draft }: { draft: TableDraftState }) {
  const partitions = draft.scan.data?.layout.partitions ?? []
  const location = draft.tableInput?.StorageDescriptor?.Location ?? ""
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-3 @lg:grid-cols-2">
        <FormField label="Table name" required>
          <Input
            className="font-mono text-xs"
            value={draft.tableName}
            onChange={(e) => draft.setName(e.target.value)}
            autoFocus
          />
        </FormField>
        <FormField
          label="Database"
          required
          hint={draft.isNewDatabase ? "A new database, created with the table." : undefined}
        >
          <Combobox<Database>
            aria-label="Database"
            placeholder="default"
            items={draft.databases.data ?? []}
            isLoading={draft.databases.isLoading}
            value={draft.database}
            onChange={draft.setDatabase}
            allowCustom
            allowFreeText
            getItemValue={(db) => db.Name ?? ""}
            filterFn={(db, q) => (db.Name ?? "").includes(q.toLowerCase())}
            renderItem={(db) => <span className="font-mono text-xs">{db.Name}</span>}
          />
        </FormField>
      </div>
      <DefinitionList columns={2}>
        <Definition label="Location" value={<S3UriLink uri={location} />} full />
        <Definition label="Columns" value={formatQuantity(draft.columns.length, "column")} />
        <Definition
          label="Partition keys"
          value={draft.partitionKeys.map((k) => k.name).join(", ") || undefined}
        />
      </DefinitionList>
      {partitions.length > 0 && (
        <label className="flex items-center gap-2.5 text-[13px] text-fg">
          <Switch checked={draft.addPartitions} onCheckedChange={draft.setAddPartitions} />
          Add the {formatQuantity(partitions.length, "partition")} found under the location
        </label>
      )}
    </div>
  )
}
