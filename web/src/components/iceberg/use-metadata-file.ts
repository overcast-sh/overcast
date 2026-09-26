import { useQuery } from "@tanstack/react-query"
import { icebergMetadataFileQueryOptions, type MetadataFile, type MetadataReader } from "./data"
import { metadataFolderOf } from "./metadata-versions"

/**
 * What stands in while the file at `location` loads: the file shown before
 * it, when that is a file of the same table — one in the same metadata
 * folder — and nothing otherwise, so one table never shows another's.
 */
export function sameTablePlaceholder(location: string) {
  return (previous?: MetadataFile): MetadataFile | undefined =>
    location !== "" &&
    previous !== undefined &&
    metadataFolderOf(previous.location) === metadataFolderOf(location)
      ? previous
      : undefined
}

/**
 * The table's current metadata file, followed across commits. A commit moves
 * the table to a new file, and so to a new query; the last file stays on
 * screen while the next one loads, so a page does not flash to a skeleton and
 * lose its expanded rows on every commit.
 */
export function useIcebergMetadataFile(location: string | undefined, read?: MetadataReader) {
  const current = location ?? ""
  return useQuery({
    ...icebergMetadataFileQueryOptions(current, read),
    placeholderData: sameTablePlaceholder(current),
  })
}
