import type { Table } from "@aws-sdk/client-glue"
import { buildTableInput } from "../table-input"

/** A partitioned CSV table as the wizard would create it. */
export const csvTable: Table = {
  ...buildTableInput({
    name: "orders",
    location: "s3://lake/orders/",
    format: "csv",
    delimiter: ",",
    columns: [
      { Name: "id", Type: "bigint" },
      { Name: "amount", Type: "double", Comment: "in cents" },
    ],
    partitionKeys: [{ Name: "dt", Type: "string" }],
  }),
  Name: "orders",
  DatabaseName: "sales",
  CatalogId: "000000000000",
  VersionId: "2",
  CreateTime: new Date("2026-09-01T10:00:00Z"),
  UpdateTime: new Date("2026-09-02T10:00:00Z"),
}

/** An Iceberg table as Athena registers one. */
export const icebergTable: Table = {
  Name: "events",
  DatabaseName: "sales",
  CatalogId: "000000000000",
  TableType: "EXTERNAL_TABLE",
  Parameters: {
    table_type: "ICEBERG",
    metadata_location: "s3://lake/events/metadata/00001-abc.metadata.json",
  },
  StorageDescriptor: {
    Location: "s3://lake/events",
    Columns: [
      {
        Name: "id",
        Type: "bigint",
        Parameters: { "iceberg.field.id": "1", "iceberg.field.optional": "false" },
      },
    ],
  },
}
