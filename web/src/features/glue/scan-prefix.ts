import { createDataWorker } from "@/lib/data-sources/worker-port"
import { HEAD_BYTES } from "@/lib/data-sources/text-file"
import { openParquetSource } from "@/lib/data-sources/parquet-source"
import { dataPreviewKind, isTabularKind, kindLabel } from "@/features/s3/preview-kind"
import { s3 } from "@/services/api"
import {
  discoverPartitions,
  isDataObject,
  type ListedObject,
  type PartitionLayout,
} from "./hive-partitions"
import { schemaFromParquet, schemaFromText, type InferredSchema } from "./infer-schema"

/**
 * What the create-table wizard learns from an S3 prefix: its partition
 * layout, from a recursive listing, and its schema, from one sampled object.
 */

/**
 * Keys listed before the scan stops. Enough for the partitions of any table
 * a developer is building locally; past it the wizard says the listing was
 * cut short, and `MSCK REPAIR TABLE` finds the rest.
 */
export const SCAN_KEY_LIMIT = 5000

export interface PrefixScan {
  layout: PartitionLayout
  /** The object the schema is read from: the first data object under the prefix. */
  sample?: ListedObject
  /** The listing stopped at `SCAN_KEY_LIMIT`. */
  truncated: boolean
}

export async function scanPrefix(bucket: string, prefix: string): Promise<PrefixScan> {
  const objects: ListedObject[] = []
  let token: string | undefined
  do {
    const page = await s3.listObjects(bucket, { prefix, delimiter: "", maxKeys: 1000, token })
    objects.push(...page.objects)
    token = page.isTruncated ? page.nextContinuationToken : undefined
  } while (token && objects.length < SCAN_KEY_LIMIT)
  return {
    layout: discoverPartitions(bucket, prefix, objects),
    sample: objects.find((o) => isDataObject(o, prefix)),
    truncated: token !== undefined,
  }
}

/** The formats a schema can be inferred from, as the wizard lists them. */
const INFERRED_FORMATS = (["csv", "tsv", "jsonl", "parquet"] as const).map(kindLabel).join(", ")

/**
 * The schema of one object, read the way the S3 preview reads it: a text
 * file's opening 64 KB, or a Parquet file's footer alone (in the data
 * worker, with `hyparquet` loaded on demand).
 */
export async function sampleSchema(bucket: string, object: ListedObject): Promise<InferredSchema> {
  const kind = dataPreviewKind("", object.key)
  if (!isTabularKind(kind)) {
    throw new Error(
      `${object.key} is not a format the wizard can read a schema from (${INFERRED_FORMATS}).`,
    )
  }
  if (kind !== "parquet") {
    const { text, truncated } = await s3.getObjectText(bucket, object.key, undefined, HEAD_BYTES)
    return schemaFromText(kind, text, truncated)
  }
  const source = await openParquetSource({
    url: s3.getObjectDownloadUrl(bucket, object.key),
    size: object.size,
    port: createDataWorker(),
  })
  try {
    return schemaFromParquet(source.info.fields)
  } finally {
    source.dispose()
  }
}
