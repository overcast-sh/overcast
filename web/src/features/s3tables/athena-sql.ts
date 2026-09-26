/**
 * The Athena catalog a table bucket is queried through: Athena reaches every
 * table bucket as `s3tablescatalog/<bucket>`.
 * https://docs.aws.amazon.com/athena/latest/ug/gdc-register-s3-table-bucket-cat.html
 */
export function s3tablesCatalog(bucket: string): string {
  return `s3tablescatalog/${bucket}`
}
