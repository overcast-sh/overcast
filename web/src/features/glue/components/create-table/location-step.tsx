import { PrefixPicker } from "@/features/s3/components/prefix-picker"

interface LocationStepProps {
  location: string
  onLocationChange: (uri: string) => void
}

/** Step 1: the folder that holds the table's files. */
export function LocationStep({ location, onLocationChange }: LocationStepProps) {
  return (
    <div className="flex flex-col gap-3">
      <p className="text-[13px] text-fg-muted">
        Pick the folder that holds the table&apos;s files. Hive-style{" "}
        <code className="font-mono text-xs">key=value/</code> folders under it become partition
        keys.
      </p>
      <PrefixPicker value={location} onChange={onLocationChange} />
    </div>
  )
}
