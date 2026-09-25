/** The bucket and key of an `s3://bucket/key` URI, or null for anything else. */
export function parseS3Uri(uri: string): { bucket: string; key: string } | null {
  const match = /^s3a?:\/\/([^/]+)\/?(.*)$/.exec(uri)
  return match ? { bucket: match[1], key: match[2] } : null
}
