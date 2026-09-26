import type {
  Column,
  PartitionInput,
  StorageDescriptor,
  Table,
  TableInput,
} from "@aws-sdk/client-glue"
import type { TabularKind } from "@/features/s3/preview-kind"

/**
 * The Glue `TableInput` for a table over S3 data, with the storage a Glue
 * crawler registers for each format — so the table reads in Athena exactly
 * as a crawled one would, and the *Copy as CDK* snippet is what a developer
 * would commit.
 */

export interface TableDraft {
  name: string
  /** The table's prefix, `s3://bucket/path/`. */
  location: string
  format: TabularKind
  columns: Column[]
  partitionKeys: Column[]
  /** The delimiter, for CSV and TSV. */
  delimiter?: string
  /** Quoted CSV values: OpenCSVSerde rather than LazySimpleSerDe. */
  quoted?: boolean
}

const TEXT_INPUT = "org.apache.hadoop.mapred.TextInputFormat"
const TEXT_OUTPUT = "org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat"

interface Storage {
  classification: string
  inputFormat: string
  outputFormat: string
  serde: string
  serdeParameters: Record<string, string>
  /** Table parameters beyond `classification`. */
  tableParameters?: Record<string, string>
}

function textStorage(draft: TableDraft): Storage {
  if (draft.format === "jsonl") {
    return {
      classification: "json",
      inputFormat: TEXT_INPUT,
      outputFormat: TEXT_OUTPUT,
      serde: "org.openx.data.jsonserde.JsonSerDe",
      serdeParameters: { paths: draft.columns.map((c) => c.Name).join(",") },
    }
  }
  const delimiter = draft.delimiter ?? (draft.format === "tsv" ? "\t" : ",")
  const header = { "skip.header.line.count": "1" }
  return draft.quoted
    ? {
        classification: "csv",
        inputFormat: TEXT_INPUT,
        outputFormat: TEXT_OUTPUT,
        serde: "org.apache.hadoop.hive.serde2.OpenCSVSerde",
        serdeParameters: { separatorChar: delimiter, quoteChar: '"', escapeChar: "\\" },
        tableParameters: header,
      }
    : {
        classification: "csv",
        inputFormat: TEXT_INPUT,
        outputFormat: TEXT_OUTPUT,
        serde: "org.apache.hadoop.hive.serde2.lazy.LazySimpleSerDe",
        serdeParameters: { "field.delim": delimiter },
        tableParameters: header,
      }
}

function storageFor(draft: TableDraft): Storage {
  if (draft.format !== "parquet") return textStorage(draft)
  return {
    classification: "parquet",
    inputFormat: "org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat",
    outputFormat: "org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat",
    serde: "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe",
    serdeParameters: { "serialization.format": "1" },
  }
}

export function buildTableInput(draft: TableDraft): TableInput {
  const storage = storageFor(draft)
  return {
    Name: draft.name,
    TableType: "EXTERNAL_TABLE",
    Parameters: {
      EXTERNAL: "TRUE",
      classification: storage.classification,
      ...storage.tableParameters,
    },
    PartitionKeys: draft.partitionKeys,
    StorageDescriptor: {
      Columns: draft.columns,
      Location: draft.location,
      InputFormat: storage.inputFormat,
      OutputFormat: storage.outputFormat,
      SerdeInfo: {
        SerializationLibrary: storage.serde,
        Parameters: storage.serdeParameters,
      },
    },
  }
}

/**
 * A partition of `table` at `location`: the table's own storage — columns,
 * formats, SerDe — pointed at the partition's prefix, as `ALTER TABLE ADD
 * PARTITION` and `MSCK REPAIR TABLE` register one.
 */
export function buildPartitionInput(
  table: Pick<Table, "StorageDescriptor">,
  values: string[],
  location: string,
): PartitionInput {
  const sd: StorageDescriptor = { ...table.StorageDescriptor, Location: location }
  return { Values: values, StorageDescriptor: sd }
}

/**
 * The `TableInput` a stored table (or table version) was created from — the
 * writable half of `Table`, which is what a version diff compares and what
 * `UpdateTable` would take. Server-assigned fields (times, `CreatedBy`,
 * `VersionId`, `DatabaseName`, `CatalogId`, `IsRegisteredWithLakeFormation`)
 * are left out, so a diff shows what the writer changed.
 */
export function tableInputOf(table: Table): TableInput {
  return {
    Name: table.Name,
    Description: table.Description,
    Owner: table.Owner,
    Retention: table.Retention,
    TableType: table.TableType,
    Parameters: table.Parameters,
    PartitionKeys: table.PartitionKeys,
    StorageDescriptor: table.StorageDescriptor,
    ViewOriginalText: table.ViewOriginalText,
    ViewExpandedText: table.ViewExpandedText,
    TargetTable: table.TargetTable,
  }
}
