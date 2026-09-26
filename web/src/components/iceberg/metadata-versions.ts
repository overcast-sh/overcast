import type { IcebergMetadata } from "./metadata"

/** One version of a table's metadata: a file the table's pointer has named. */
export interface MetadataVersion {
  location: string
  /** The file's own name, `00003-<uuid>.metadata.json` — unique within a table, and what a deep link carries. */
  fileName: string
  /** When the file was written: its own `last-updated-ms`, as the metadata log records it. */
  timestampMs?: number
  current: boolean
}

export function fileNameOf(location: string): string {
  return location.slice(location.lastIndexOf("/") + 1)
}

/** The folder a metadata file sits in: every version of one table shares it. */
export function metadataFolderOf(location: string): string {
  return location.slice(0, location.lastIndexOf("/"))
}

/**
 * Every version the current file knows of, newest first: the current file,
 * then its `metadata-log`. The log is capped at
 * `write.metadata.previous-versions-max` (100 by default), so the oldest
 * versions of a busy table fall out of it.
 */
export function metadataVersions(
  location: string,
  metadata: IcebergMetadata | null,
): MetadataVersion[] {
  const previous = (metadata?.metadataLog ?? []).map((entry): MetadataVersion => ({
    location: entry.metadataFile,
    fileName: fileNameOf(entry.metadataFile),
    timestampMs: entry.timestampMs,
    current: false,
  }))
  const current: MetadataVersion = {
    location,
    fileName: fileNameOf(location),
    timestampMs: metadata?.lastUpdatedMs,
    current: true,
  }
  return [current, ...previous.reverse()]
}

/** The version a deep link names by file name, or undefined when the table no longer lists it. */
export function findVersion(
  versions: MetadataVersion[],
  fileName: string | undefined,
): MetadataVersion | undefined {
  return fileName === undefined ? undefined : versions.find((v) => v.fileName === fileName)
}

/** The version just before `version`, the natural thing to diff it against. */
export function previousVersion(
  versions: MetadataVersion[],
  version: MetadataVersion,
): MetadataVersion | undefined {
  const index = versions.findIndex((v) => v.location === version.location)
  return index === -1 ? undefined : versions[index + 1]
}
