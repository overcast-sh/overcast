/**
 * Reading Iceberg metadata files, for every page that shows a table's.
 *
 * The viewer takes a metadata location and a reader rather than fetching for
 * itself, so a table from any catalog — S3 Tables, Glue, a file opened in the
 * S3 browser — goes through the same component. The default reader fetches
 * from the emulator's S3 through the same download route the S3 preview uses.
 *
 * Key factory:
 *   icebergKeys.metadataFile(location) -> [...endpoint, "iceberg", "metadata-file", location]
 */

import { queryOptions } from "@tanstack/react-query"
import { s3 } from "@/services/api"
import { endpointStore } from "@/services/endpoint-store"
import { parseS3Uri } from "@/lib/s3-uri"
import { parseIcebergMetadata, type IcebergMetadata } from "./metadata"

/** A metadata file's text, and whether the read stopped before its end. */
export interface MetadataText {
  text: string
  truncated: boolean
}

/**
 * Reads the metadata file at a location (`s3://…`). Files are cached by
 * location alone, so any two readers must return the same bytes for one
 * location — true of every reader of one emulator's S3.
 */
export type MetadataReader = (location: string) => Promise<MetadataText>

/**
 * How much of a metadata file is read. Files grow with every snapshot and
 * schema the table keeps; this is far past any a local table reaches, and
 * still bounded, so a pathological file cannot take the tab's memory with it.
 */
export const METADATA_READ_LIMIT = 16 * 1024 * 1024

/** The emulator's S3, through the console's object download route. */
export const s3MetadataReader: MetadataReader = (location) => {
  const where = parseS3Uri(location)
  if (!where) return Promise.reject(new Error(`Not an S3 location: ${location}`))
  return s3.getObjectText(where.bucket, where.key, undefined, METADATA_READ_LIMIT)
}

/** A metadata file as read: its text for the raw view, and the typed metadata when it parses. */
export interface MetadataFile extends MetadataText {
  location: string
  metadata: IcebergMetadata | null
}

export const icebergKeys = {
  all: () => [...endpointStore.getKeys(), "iceberg"] as const,
  metadataFile: (location: string) => [...icebergKeys.all(), "metadata-file", location] as const,
}

/**
 * One metadata file. Iceberg never rewrites one — each commit writes the next
 * file and moves the table's pointer — so a file read once is never stale,
 * and a live table page follows commits by asking for the new location.
 */
export function icebergMetadataFileQueryOptions(
  location: string,
  read: MetadataReader = s3MetadataReader,
) {
  return queryOptions({
    queryKey: icebergKeys.metadataFile(location),
    queryFn: async (): Promise<MetadataFile> => {
      const file = await read(location)
      return { ...file, location, metadata: parseIcebergMetadata(file.text) }
    },
    enabled: location !== "",
    staleTime: Infinity,
  })
}
