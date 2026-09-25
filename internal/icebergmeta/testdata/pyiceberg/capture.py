"""Capture golden Iceberg commit fixtures from PyIceberg's own commit path.

Each fixture is one commit as PyIceberg's SqlCatalog performs it: the table's
metadata before (null for a create), the metadata file it was read from, the
CommitTableRequest a REST client would send for it, and the metadata PyIceberg
built from them. The temporary warehouse's path is rewritten to
s3://golden--table-s3 so the fixtures do not depend on where they were made.

    pip install "pyiceberg[pyarrow,pyiceberg-core,sql-sqlite]" tzdata
    python capture.py internal/icebergmeta/testdata/pyiceberg

The committed fixtures were captured with PyIceberg 0.12.0.
"""
import json
import os
import sys
import tempfile
from datetime import datetime, timezone

import pyarrow as pa
import pyiceberg.catalog as catalog_mod
from pyiceberg.catalog.sql import SqlCatalog
from pyiceberg.partitioning import PartitionField, PartitionSpec
from pyiceberg.schema import Schema
from pyiceberg.table import CommitTableRequest
from pyiceberg.table.sorting import SortField, SortOrder
from pyiceberg.transforms import BucketTransform, DayTransform, IdentityTransform
from pyiceberg.types import (
    DateType, DoubleType, ListType, LongType, MapType, NestedField, StringType,
    StructType, TimestamptzType,
)

out_dir = sys.argv[1]
os.makedirs(out_dir, exist_ok=True)
captured = []

original_stage = catalog_mod.MetastoreCatalog._update_and_stage_table


def capturing_stage(self, current_table, table_identifier, requirements, updates):
    staged = original_stage(self, current_table, table_identifier, requirements, updates)
    request = CommitTableRequest(identifier={"namespace": list(table_identifier[:-1]), "name": table_identifier[-1]}, requirements=requirements, updates=updates)
    captured.append({
        "base": json.loads(current_table.metadata.model_dump_json()) if current_table else None,
        "base-location": current_table.metadata_location if current_table else "",
        "request": json.loads(request.model_dump_json()),
        "expected": json.loads(staged.metadata.model_dump_json()),
    })
    return staged


catalog_mod.MetastoreCatalog._update_and_stage_table = capturing_stage

warehouse = tempfile.mkdtemp()
# A drive-less file URI: PyArrow cannot open "file:///C:/..." on Windows.
warehouse_uri = "file://" + os.path.splitdrive(warehouse)[1].replace("\\", "/")
cat = SqlCatalog("golden", uri="sqlite:///:memory:", warehouse=warehouse_uri)
cat.create_namespace("ns")


def save(name):
    fixture = captured.pop()
    fixture["name"] = name
    text = json.dumps(fixture, indent=2, sort_keys=True).replace(warehouse_uri, "s3://golden--table-s3")
    with open(os.path.join(out_dir, name + ".json"), "w", encoding="utf-8", newline="\n") as f:
        f.write(text + "\n")
    captured.clear()


schema = Schema(
    NestedField(1, "id", LongType(), required=True),
    NestedField(2, "ts", TimestamptzType(), required=False),
    NestedField(3, "tags", ListType(4, StringType(), element_required=False), required=False),
    NestedField(5, "attrs", MapType(6, StringType(), 7, DoubleType(), value_required=False), required=False),
    NestedField(8, "point", StructType(NestedField(9, "x", DoubleType()), NestedField(10, "y", DoubleType())), required=False),
    identifier_field_ids=[1],
)
spec = PartitionSpec(
    PartitionField(source_id=2, field_id=1000, transform=DayTransform(), name="ts_day"),
    PartitionField(source_id=1, field_id=1001, transform=BucketTransform(8), name="id_bucket"),
)
order = SortOrder(SortField(source_id=1, transform=IdentityTransform()))

txn = cat.create_table_transaction("ns.events", schema, partition_spec=spec, sort_order=order,
                                   properties={"write.metadata.previous-versions-max": "2"})
txn.commit_transaction()
save("01-create-transaction")

tbl = cat.load_table("ns.events")
arrow_schema = tbl.schema().as_arrow()


def batch(ids):
    return pa.Table.from_pylist([
        {"id": i, "ts": datetime(2026, 9, 1 + i, tzinfo=timezone.utc), "tags": ["a"], "attrs": [("k", 1.0)],
         "point": {"x": 1.0, "y": 2.0}}
        for i in ids
    ], schema=arrow_schema)


tbl.append(batch([1, 2]))
save("02-first-append")

tbl = cat.load_table("ns.events")
tbl.append(batch([3]))
save("03-second-append")

tbl = cat.load_table("ns.events")
with tbl.update_schema() as upd:
    upd.add_column("note", StringType())
save("04-add-column")

tbl = cat.load_table("ns.events")
with tbl.update_spec() as upd:
    upd.add_identity("note")
save("05-add-partition-field")

tbl = cat.load_table("ns.events")
first = tbl.snapshots()[0].snapshot_id
tbl.manage_snapshots().create_tag(first, "v1").create_branch(first, "audit").commit()
save("06-tag-and-branch")

tbl = cat.load_table("ns.events")
with tbl.transaction() as t:
    t.set_properties(owner="overcast")
    t.remove_properties("write.metadata.previous-versions-max")
save("07-properties")

tbl = cat.load_table("ns.events")
tbl.manage_snapshots().remove_tag("v1").remove_branch("audit").commit()
save("08-remove-refs")

tbl = cat.load_table("ns.events")
tbl.maintenance.expire_snapshots().by_id(first).commit()
save("09-expire-first-snapshot")

v1 = cat.create_table("ns.legacy", Schema(NestedField(1, "d", DateType(), required=False)),
                      properties={"format-version": "1"})
with v1.transaction() as t:
    t.upgrade_table_version(2)
save("10-upgrade-v1-to-v2")

print("wrote fixtures to", out_dir)
