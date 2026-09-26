import type { Table } from "@aws-sdk/client-glue"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"

function ParameterCard({
  title,
  parameters,
}: {
  title: string
  parameters?: Record<string, string>
}) {
  const entries = Object.entries(parameters ?? {}).sort(([a], [b]) => a.localeCompare(b))
  return (
    <DefinitionCard title={title}>
      {entries.length === 0 ? (
        <Definition label="Parameters" value={undefined} full />
      ) : (
        entries.map(([key, value]) => <Definition key={key} label={key} value={value} />)
      )}
    </DefinitionCard>
  )
}

/** The table's parameters, its storage descriptor and its SerDe — the fields a format is made of. */
export function PropertiesTab({ table }: { table: Table }) {
  const sd = table.StorageDescriptor
  return (
    <div className="flex flex-col gap-4">
      <ParameterCard title="Table parameters" parameters={table.Parameters} />
      <DefinitionCard title="Storage descriptor">
        <Definition label="Input format" value={sd?.InputFormat} full />
        <Definition label="Output format" value={sd?.OutputFormat} full />
        <Definition
          label="Compressed"
          value={sd?.Compressed === undefined ? undefined : String(sd.Compressed)}
        />
        <Definition label="Buckets" value={sd?.NumberOfBuckets} />
        <Definition
          label="Stored as sub-directories"
          value={
            sd?.StoredAsSubDirectories === undefined ? undefined : String(sd.StoredAsSubDirectories)
          }
        />
      </DefinitionCard>
      <DefinitionCard title="SerDe">
        <Definition label="Library" value={sd?.SerdeInfo?.SerializationLibrary} full />
        {Object.entries(sd?.SerdeInfo?.Parameters ?? {}).map(([key, value]) => (
          <Definition key={key} label={key} value={JSON.stringify(value).slice(1, -1)} />
        ))}
      </DefinitionCard>
    </div>
  )
}
