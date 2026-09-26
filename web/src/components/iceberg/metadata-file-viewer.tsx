import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { FileJson } from "lucide-react"
import { CopyButton } from "@/components/ui/copy-button"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import { EmptyState } from "@/components/ui/primitives"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { Select } from "@/components/ui/select"
import { SkeletonRows } from "@/components/ui/skeleton"
import { TextDiff } from "@/components/ui/text-diff"
import { formatDate } from "@/lib/format"
import { MAX_FORMAT_BYTES } from "@/lib/format-body"
import { reindentJson } from "@/lib/json-text"
import { icebergMetadataFileQueryOptions, type MetadataFile, type MetadataReader } from "./data"
import {
  findVersion,
  metadataVersions,
  previousVersion,
  type MetadataVersion,
} from "./metadata-versions"

/** The version picked, and the one it is diffed against, each by file name. */
export interface MetadataSelection {
  version?: string
  compare?: string
}

interface IcebergMetadataFilesProps {
  /** The table's current metadata file, already read. */
  current: MetadataFile
  /** Controlled by the page, so a version and a diff deep-link. Unset follows the current file. */
  selection: MetadataSelection
  onSelectionChange: (next: MetadataSelection) => void
  read?: MetadataReader
}

/**
 * A table's `metadata.json`, raw, with a picker across every version its
 * `metadata-log` remembers and a diff between any two.
 *
 * Unless a version is picked the view follows the current file, so a table
 * page that re-reads its pointer on each commit shows commits as they land.
 */
export function IcebergMetadataFiles({
  current,
  selection,
  onSelectionChange,
  read,
}: IcebergMetadataFilesProps) {
  const versions = metadataVersions(current.location, current.metadata)
  const selected = findVersion(versions, selection.version) ?? versions[0]
  const compared = findVersion(versions, selection.compare)
  const selectedFile = useQuery(icebergMetadataFileQueryOptions(selected.location, read))
  const comparedFile = useQuery({
    ...icebergMetadataFileQueryOptions(compared?.location ?? "", read),
    enabled: compared !== undefined,
  })
  const previous = previousVersion(versions, selected)

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <VersionPicker
          label="Version"
          versions={versions}
          value={selected.fileName}
          onChange={(fileName) => {
            // The current file is followed rather than pinned, so a commit moves the view on.
            const version = fileName === versions[0].fileName ? undefined : fileName
            const compare = selection.compare === fileName ? undefined : selection.compare
            onSelectionChange({ version, compare })
          }}
        />
        <VersionPicker
          label="Diff against"
          versions={versions.filter((v) => v.location !== selected.location)}
          value={compared?.fileName ?? ""}
          noneLabel="No diff"
          onChange={(compare) => onSelectionChange({ ...selection, compare: compare || undefined })}
        />
        {previous && compared?.location !== previous.location && (
          <button
            type="button"
            className="h-8 cursor-pointer text-xs text-accent hover:underline"
            onClick={() => onSelectionChange({ ...selection, compare: previous.fileName })}
          >
            Diff with previous
          </button>
        )}
        <span className="ml-auto flex min-w-0 items-center gap-1.5 text-fg-muted">
          <S3UriLink uri={selected.location} className="truncate" />
          <CopyButton value={selected.location} noun="metadata location" tone="inline" />
        </span>
      </div>
      <MetadataBody
        selected={selectedFile}
        compared={compared ? comparedFile : undefined}
        comparedName={compared?.fileName}
      />
    </div>
  )
}

function versionLabel(v: MetadataVersion): string {
  const when = v.timestampMs === undefined ? "" : ` · ${formatDate(v.timestampMs)}`
  return `${v.fileName.split("-")[0]}${v.current ? " (current)" : ""}${when}`
}

function VersionPicker({
  label,
  versions,
  value,
  noneLabel,
  onChange,
}: {
  label: string
  versions: MetadataVersion[]
  value: string
  noneLabel?: string
  onChange: (fileName: string) => void
}) {
  return (
    <label className="flex flex-col gap-1 text-2xs text-fg-subtle">
      {label}
      <Select
        className="w-80"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        disabled={versions.length === 0}
      >
        {noneLabel !== undefined && <option value="">{noneLabel}</option>}
        {versions.map((v) => (
          <option key={v.location} value={v.fileName} title={v.location}>
            {versionLabel(v)}
          </option>
        ))}
      </Select>
    </label>
  )
}

type FileQuery = { data?: MetadataFile; isLoading: boolean; error: Error | null }

/** Pretty-printed as the file spells it: ids keep their digits. Text that is not JSON shows as it is. */
function displayText(file: MetadataFile): string {
  try {
    return reindentJson(file.text)
  } catch {
    return file.text
  }
}

function MetadataBody({
  selected,
  compared,
  comparedName,
}: {
  selected: FileQuery
  compared?: FileQuery
  comparedName?: string
}) {
  const text = useMemo(() => (selected.data ? displayText(selected.data) : ""), [selected.data])
  const comparedData = compared?.data
  const before = useMemo(() => (comparedData ? displayText(comparedData) : ""), [comparedData])
  const failed = selected.error ?? compared?.error
  if (failed) {
    return (
      <EmptyState
        icon={<FileJson className="size-8" />}
        title="Could not read the metadata file"
        description={failed.message}
      />
    )
  }
  if (
    selected.isLoading ||
    !selected.data ||
    (compared && (compared.isLoading || !compared.data))
  ) {
    return <SkeletonRows rows={8} noun="metadata" />
  }
  const truncated = selected.data.truncated || compared?.data?.truncated
  return (
    <div className="flex flex-col gap-2">
      {truncated && (
        <p className="text-xs text-warning">
          The file is larger than the console reads, so what follows stops before its end.
        </p>
      )}
      {compared ? (
        <TextDiff
          aria-label={`Diff from ${comparedName ?? "the earlier version"}`}
          before={before}
          after={text}
          className="max-h-[70vh]"
        />
      ) : (
        <HighlightedCode
          text={text}
          // Highlighting is linear in the text; past the shared cap it renders plain.
          language={text.length > MAX_FORMAT_BYTES ? null : "json"}
          className="max-h-[70vh] overflow-auto rounded-md border border-border bg-bg p-3 font-mono text-xs leading-5"
        />
      )}
    </div>
  )
}
