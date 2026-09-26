package icebergmeta

// An Iceberg schema as the Glue Data Catalog describes it: Hive type names,
// the spelling Iceberg's own Glue catalog (IcebergToGlueConverter) writes a
// table's columns in, and so the one Athena and the Glue console show.

import (
	"strings"
)

// hivePrimitives are the primitives whose Hive name differs from Iceberg's
// or loses a distinction Hive cannot make: Hive has no time, uuid or fixed,
// and one timestamp for both of Iceberg's.
var hivePrimitives = map[string]string{
	"long": "bigint", "time": "string", "uuid": "string", "timestamptz": "timestamp",
}

// HiveTypeName is t's Hive type name: "bigint", "decimal(10,2)",
// "array<string>", "struct<a:int,b:string>".
func (t Type) HiveTypeName() string {
	switch {
	case t.Struct != nil:
		parts := make([]string, len(t.Struct.Fields))
		for i, f := range t.Struct.Fields {
			parts[i] = f.Name + ":" + f.Type.HiveTypeName()
		}
		return "struct<" + strings.Join(parts, ",") + ">"
	case t.List != nil:
		return "array<" + t.List.Element.HiveTypeName() + ">"
	case t.Map != nil:
		return "map<" + t.Map.Key.HiveTypeName() + "," + t.Map.Value.HiveTypeName() + ">"
	}
	name, ok := canonicalPrimitive(t.Primitive)
	if !ok {
		return t.Primitive
	}
	if m := decimalType.FindStringSubmatch(name); m != nil {
		return "decimal(" + m[1] + "," + m[2] + ")"
	}
	if fixedType.MatchString(name) {
		return "binary"
	}
	if hive, ok := hivePrimitives[name]; ok {
		return hive
	}
	return name
}

// CurrentSchema is the table's current schema, and false when the metadata
// names a schema it does not hold.
func (m *Metadata) CurrentSchema() (Schema, bool) {
	return m.schemaByID(m.CurrentSchemaID)
}
