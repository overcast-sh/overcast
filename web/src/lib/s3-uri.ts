/**
 * `s3://bucket/key` URIs — a Glue table's location, an Athena result's
 * `OutputLocation` — split into the bucket and key the console addresses
 * objects by.
 */

export interface S3Location {
  bucket: string
  /** The key or prefix, without a leading slash; `""` for the bucket root. */
  key: string
}

/** The bucket and key of an `s3://` (or `s3a://`, `s3n://`) URI, or null when it is not one. */
export function parseS3Uri(uri: string): S3Location | null {
  const match = /^s3[an]?:\/\/([^/]+)\/?(.*)$/.exec(uri.trim())
  return match ? { bucket: match[1], key: match[2] } : null
}
