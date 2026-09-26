/**
 * S3 Tables' naming rules, checked as the developer types so the create
 * dialogs can say what is wrong before the service does. The service stays
 * the authority: whatever it refuses is shown word for word.
 * https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-buckets-naming.html
 */

const BUCKET_NAME = /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/
const TABLE_OR_NAMESPACE = /^[a-z0-9][a-z0-9_]{0,254}$/
const RESERVED_PREFIXES = ["xn--", "sthree-", "amzn-s3-demo-", "aws"]
const RESERVED_SUFFIXES = ["-s3alias", "--ol-s3", "--x-s3", "--table-s3"]

export const BUCKET_NAME_RULE =
  "3–63 lowercase letters, digits and hyphens, starting and ending with a letter or digit."
export const IDENTIFIER_RULE =
  "Lowercase letters, digits and underscores, starting with a letter or digit."

/** Why `name` cannot name a table bucket, or undefined when it can. */
export function bucketNameProblem(name: string): string | undefined {
  if (!BUCKET_NAME.test(name)) {
    return BUCKET_NAME_RULE
  }
  if (
    RESERVED_PREFIXES.some((p) => name.startsWith(p)) ||
    RESERVED_SUFFIXES.some((s) => name.endsWith(s))
  ) {
    return "That prefix or suffix is reserved by Amazon S3."
  }
  return undefined
}

/** Why `name` cannot name a namespace or a table, or undefined when it can. */
export function identifierProblem(name: string, kind: "namespace" | "table"): string | undefined {
  if (!TABLE_OR_NAMESPACE.test(name)) {
    return IDENTIFIER_RULE
  }
  if (kind === "namespace" && name.startsWith("aws")) {
    return "Namespaces starting with “aws” are reserved."
  }
  return undefined
}
