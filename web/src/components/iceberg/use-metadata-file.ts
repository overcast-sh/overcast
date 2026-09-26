import { useQuery } from "@tanstack/react-query"
import { icebergMetadataFileQueryOptions, type MetadataFile, type MetadataReader } from "./data"

/** The folder a metadata file sits in: every version of one table shares it. */
function folderOf(location: string): string {
  return location.slice(0, location.lastIndexOf("/"))
}

/**
 * The table's current metadata file, followed across commits. A commit moves
 * the table to a new file, and so to a new query; the last file stays on
 * screen while the next one loads, so a page does not flash to a skeleton
 * and lose its expanded rows on every commit. Only a file of the same table
 * — one in the same metadata folder — stands in, never another table's.
 */
export function useIcebergMetadataFile(location: string | undefined, read?: MetadataReader) {
  const current = location ?? ""
  return useQuery({
    ...icebergMetadataFileQueryOptions(current, read),
    placeholderData: (previous?: MetadataFile) =>
      previous && folderOf(previous.location) === folderOf(current) ? previous : undefined,
  })
}
