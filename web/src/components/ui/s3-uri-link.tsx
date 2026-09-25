import { Link } from "@tanstack/react-router"
import { parseS3Uri } from "@/lib/s3-uri"
import { cn } from "@/lib/utils"
import { LINK_CLASS } from "./arn-link"

/**
 * An `s3://` URI, linked to the S3 browser at that folder or object — a
 * table's location, a query's result file. Anything that is not an S3 URI
 * renders as plain mono text.
 */
export function S3UriLink({ uri, className }: { uri: string; className?: string }) {
  const base = cn("font-mono text-xs break-all", className)
  const location = parseS3Uri(uri)
  if (!location) return <span className={base}>{uri}</span>
  return (
    <Link
      to="/s3/$bucket/objects/$"
      params={{ bucket: location.bucket, _splat: location.key }}
      className={cn(base, LINK_CLASS)}
    >
      {uri}
    </Link>
  )
}
