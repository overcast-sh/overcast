"""Capture golden new-table fixtures from PyIceberg's new_table_metadata.

Each fixture is one table definition in the caller's own field numbering — a
nested schema whose ids are unique but in no particular order, with the
partition spec and sort order that refer to its columns by those ids — and the
metadata PyIceberg builds for it: fresh ids assigned depth-first, and every
reference moved to follow its column. The partition fields carry no field-id:
PyIceberg numbers them from 1000 whatever the caller proposed.

    pip install "pyiceberg" tzdata
    python capture.py internal/icebergmeta/testdata/pyiceberg-create

The committed fixtures were captured with PyIceberg 0.12.0.
"""
import json
import os
import sys
import uuid

from pyiceberg.partitioning import PartitionField, PartitionSpec
from pyiceberg.schema import Schema
from pyiceberg.table.metadata import new_table_metadata
from pyiceberg.table.sorting import NullOrder, SortDirection, SortField, SortOrder
from pyiceberg.transforms import BucketTransform, IdentityTransform, TruncateTransform
from pyiceberg.types import (
    DoubleType, IntegerType, ListType, LongType, MapType, NestedField, StringType, StructType,
)

out_dir = sys.argv[1]
os.makedirs(out_dir, exist_ok=True)

TABLE_UUID = uuid.UUID("0f3a5b7c-9d1e-4f20-8a4b-6c7d8e9fa0b1")
LOCATION = "s3://golden--table-s3"


def save(name, schema, spec, order, properties):
    request = {
        "table-uuid": str(TABLE_UUID),
        "location": LOCATION,
        "schema": json.loads(schema.model_dump_json()),
        "partition-fields": [
            {"source-id": f.source_id, "name": f.name, "transform": str(f.transform)} for f in spec.fields
        ],
        "write-order": json.loads(order.model_dump_json()),
        "properties": properties,
    }
    metadata = new_table_metadata(schema, spec, order, LOCATION, dict(properties), TABLE_UUID)
    fixture = {"name": name, "request": request, "expected": json.loads(metadata.model_dump_json())}
    with open(os.path.join(out_dir, name + ".json"), "w", encoding="utf-8", newline="\n") as f:
        f.write(json.dumps(fixture, indent=2, sort_keys=True) + "\n")


# Every nested kind, nested in every other, numbered out of order: a list of
# structs, a map whose value is a struct holding a list, a list of maps, and a
# required struct whose field is the identifier and a partition source.
nested = Schema(
    NestedField(100, "id", LongType(), required=True, doc="row id"),
    NestedField(5, "tenant", StructType(
        NestedField(50, "name", StringType(), required=True),
        NestedField(51, "tags", ListType(52, StructType(
            NestedField(53, "k", StringType(), required=True),
            NestedField(54, "v", IntegerType(), required=False),
        ), element_required=True), required=False),
    ), required=True),
    NestedField(7, "attrs", MapType(8, StringType(), 9, StructType(
        NestedField(90, "scores", ListType(91, DoubleType(), element_required=False), required=False),
    ), value_required=False), required=False),
    NestedField(3, "events", ListType(30, MapType(31, StringType(), 32, DoubleType(), value_required=True),
                                      element_required=False), required=False),
    identifier_field_ids=[100, 50],
)
save(
    "01-nested-out-of-order",
    nested,
    PartitionSpec(
        PartitionField(source_id=50, field_id=1000, transform=IdentityTransform(), name="tenant_name"),
        PartitionField(source_id=100, field_id=1001, transform=BucketTransform(16), name="id_bucket"),
    ),
    SortOrder(
        SortField(source_id=50, transform=TruncateTransform(4), direction=SortDirection.ASC, null_order=NullOrder.NULLS_FIRST),
        SortField(source_id=100, transform=IdentityTransform(), direction=SortDirection.DESC, null_order=NullOrder.NULLS_LAST),
        order_id=5,  # a new table's sort order is renumbered to 1
    ),
    {"write.format.default": "parquet"},
)

save(
    "02-nested-format-version-1",
    nested,
    PartitionSpec(PartitionField(source_id=50, field_id=1000, transform=IdentityTransform(), name="tenant_name")),
    SortOrder(),
    {"format-version": "1"},
)

print("wrote fixtures to", out_dir)
