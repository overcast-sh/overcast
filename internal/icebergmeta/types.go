package icebergmeta

// Iceberg's type system as the spec's JSON writes it: a primitive is its name
// ("long", "decimal(10, 2)"), and a struct, list or map is an object carrying
// the ids of the fields, element, key and value it declares.
//
// Spec: https://iceberg.apache.org/spec/#appendix-c-json-serialization

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// Type is an Iceberg type. Exactly one of Struct, List and Map is set for a
// nested type; a primitive sets none of them and names itself in Primitive.
type Type struct {
	Primitive string
	Struct    *StructType
	List      *ListType
	Map       *MapType
}

// PrimitiveType is the primitive type called name.
func PrimitiveType(name string) Type { return Type{Primitive: name} }

// StructType is a struct of named fields.
type StructType struct {
	Fields []Field
}

// ListType is a list of one element type.
type ListType struct {
	ElementID       int
	Element         Type
	ElementRequired bool
}

// MapType maps a key type to a value type.
type MapType struct {
	KeyID         int
	Key           Type
	ValueID       int
	Value         Type
	ValueRequired bool
}

// Field is one field of a struct: a column, or a member of a nested struct.
type Field struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Type     Type   `json:"type"`
	Doc      string `json:"doc,omitempty"`
}

// The JSON objects the spec writes a nested type as.
type (
	structJSON struct {
		Type   string  `json:"type"`
		Fields []Field `json:"fields"`
	}
	listJSON struct {
		Type            string `json:"type"`
		ElementID       int    `json:"element-id"`
		Element         Type   `json:"element"`
		ElementRequired bool   `json:"element-required"`
	}
	mapJSON struct {
		Type          string `json:"type"`
		KeyID         int    `json:"key-id"`
		Key           Type   `json:"key"`
		ValueID       int    `json:"value-id"`
		Value         Type   `json:"value"`
		ValueRequired bool   `json:"value-required"`
	}
)

// MarshalJSON writes the type in the spec's form.
func (t Type) MarshalJSON() ([]byte, error) {
	switch {
	case t.Struct != nil:
		return json.Marshal(structJSON{Type: "struct", Fields: nonNil(t.Struct.Fields)})
	case t.List != nil:
		l := t.List
		return json.Marshal(listJSON{Type: "list", ElementID: l.ElementID, Element: l.Element, ElementRequired: l.ElementRequired})
	case t.Map != nil:
		m := t.Map
		return json.Marshal(mapJSON{Type: "map", KeyID: m.KeyID, Key: m.Key, ValueID: m.ValueID, Value: m.Value, ValueRequired: m.ValueRequired})
	default:
		return json.Marshal(t.Primitive)
	}
}

// UnmarshalJSON reads a type in the spec's form. A primitive's name is kept
// as written; validateType decides whether it is one this package writes.
func (t *Type) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) == nil {
		*t = Type{Primitive: name}
		return nil
	}
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &kind); err != nil {
		return err
	}
	switch kind.Type {
	case "struct":
		var s structJSON
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = Type{Struct: &StructType{Fields: s.Fields}}
	case "list":
		var l listJSON
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		*t = Type{List: &ListType{ElementID: l.ElementID, Element: l.Element, ElementRequired: l.ElementRequired}}
	case "map":
		var m mapJSON
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		*t = Type{Map: &MapType{KeyID: m.KeyID, Key: m.Key, ValueID: m.ValueID, Value: m.Value, ValueRequired: m.ValueRequired}}
	default:
		return invalid("Unknown nested type %q.", kind.Type)
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// primitiveTypes are the format-version 2 primitive type names without
// parameters. The nanosecond timestamps and the other version 3 additions are
// deliberately absent: a v2 table that carries one cannot be read.
var primitiveTypes = map[string]bool{
	"boolean": true, "int": true, "long": true, "float": true, "double": true,
	"date": true, "time": true, "timestamp": true, "timestamptz": true,
	"string": true, "uuid": true, "binary": true,
}

var (
	decimalType = regexp.MustCompile(`^decimal\(\s*(\d+)\s*,\s*(\d+)\s*\)$`)
	fixedType   = regexp.MustCompile(`^fixed\[\s*(\d+)\s*\]$`)
)

// canonicalPrimitive returns typ in the spelling the spec and the reference
// readers parse — "decimal(P, S)", "fixed[N]" — and false when it is not a
// version 2 primitive.
func canonicalPrimitive(typ string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(typ))
	if primitiveTypes[t] {
		return t, true
	}
	if m := decimalType.FindStringSubmatch(t); m != nil {
		return "decimal(" + m[1] + ", " + m[2] + ")", true
	}
	if m := fixedType.FindStringSubmatch(t); m != nil {
		return "fixed[" + m[1] + "]", true
	}
	return "", false
}

// ─── Validation ───────────────────────────────────────────────────────────────

// typeChecker validates a schema's types and collects the ids it declares, so
// that every id in a schema — field, element, key or value — is used once.
type typeChecker struct {
	ids map[int]bool
}

func (c *typeChecker) claim(id int) error {
	if id < 1 || c.ids[id] {
		return invalid("Field id %d is invalid or used more than once.", id)
	}
	c.ids[id] = true
	return nil
}

// fields validates a struct's fields, returning them with their types in
// canonical form.
func (c *typeChecker) fields(in []Field) ([]Field, error) {
	out := make([]Field, len(in))
	names := make(map[string]bool, len(in))
	for i, f := range in {
		if f.Name == "" {
			return nil, invalid("Schema field %d has no name.", i)
		}
		if names[f.Name] {
			return nil, invalid("Schema field name %q is used more than once.", f.Name)
		}
		names[f.Name] = true
		if err := c.claim(f.ID); err != nil {
			return nil, err
		}
		typ, err := c.typ(f.Type, f.Name)
		if err != nil {
			return nil, err
		}
		out[i] = f
		out[i].Type = typ
	}
	return out, nil
}

func (c *typeChecker) typ(t Type, name string) (Type, error) {
	switch {
	case t.Struct != nil:
		fields, err := c.fields(t.Struct.Fields)
		return Type{Struct: &StructType{Fields: fields}}, err
	case t.List != nil:
		l := *t.List
		if err := c.claim(l.ElementID); err != nil {
			return Type{}, err
		}
		var err error
		l.Element, err = c.typ(l.Element, name+".element")
		return Type{List: &l}, err
	case t.Map != nil:
		m := *t.Map
		if err := c.claim(m.KeyID); err != nil {
			return Type{}, err
		}
		if err := c.claim(m.ValueID); err != nil {
			return Type{}, err
		}
		var err error
		if m.Key, err = c.typ(m.Key, name+".key"); err != nil {
			return Type{}, err
		}
		m.Value, err = c.typ(m.Value, name+".value")
		return Type{Map: &m}, err
	default:
		canonical, ok := canonicalPrimitive(t.Primitive)
		if !ok {
			return Type{}, invalid("Schema field %q has an unsupported type %q.", name, t.Primitive)
		}
		return PrimitiveType(canonical), nil
	}
}

// validateSchema checks a schema the caller proposed — at least one field,
// names unique within each struct, every id positive and used once, types
// the spec defines, identifier fields that exist — and returns it with its
// types in canonical form.
func validateSchema(s Schema) (Schema, error) {
	if len(s.Fields) == 0 {
		return Schema{}, invalid("The schema must contain at least one field.")
	}
	c := typeChecker{ids: map[int]bool{}}
	fields, err := c.fields(s.Fields)
	if err != nil {
		return Schema{}, err
	}
	for _, id := range s.IdentifierFieldIDs {
		if !c.ids[id] {
			return Schema{}, invalid("Identifier field id %d is not a field of the schema.", id)
		}
	}
	return Schema{Type: "struct", SchemaID: s.SchemaID, IdentifierFieldIDs: s.IdentifierFieldIDs, Fields: fields}, nil
}

// ─── Ids ──────────────────────────────────────────────────────────────────────

// visitIDs calls fn with every id the fields declare, nested ones included.
func visitIDs(fields []Field, fn func(int)) {
	for _, f := range fields {
		fn(f.ID)
		visitTypeIDs(f.Type, fn)
	}
}

func visitTypeIDs(t Type, fn func(int)) {
	switch {
	case t.Struct != nil:
		visitIDs(t.Struct.Fields, fn)
	case t.List != nil:
		fn(t.List.ElementID)
		visitTypeIDs(t.List.Element, fn)
	case t.Map != nil:
		fn(t.Map.KeyID)
		fn(t.Map.ValueID)
		visitTypeIDs(t.Map.Key, fn)
		visitTypeIDs(t.Map.Value, fn)
	}
}

// highestFieldID is the largest id the schema declares, nested ones included.
func (s Schema) highestFieldID() int {
	highest := 0
	visitIDs(s.Fields, func(id int) { highest = max(highest, id) })
	return highest
}

// hasFieldID reports whether the schema declares id anywhere.
func (s Schema) hasFieldID(id int) bool {
	found := false
	visitIDs(s.Fields, func(v int) { found = found || v == id })
	return found
}

// sameAs reports whether two schemas are the same apart from their ids, the
// test the reference implementation uses to reuse a schema id.
func (s Schema) sameAs(o Schema) bool {
	return slices.Equal(s.IdentifierFieldIDs, o.IdentifierFieldIDs) && equalJSON(s.Fields, o.Fields)
}

// freshIDs renumbers every id a schema declares from 1, the way the
// reference implementation's AssignFreshIds does for a new table: a struct's
// own fields first, in order, then each field's nested type in turn; a list's
// element, and a map's key then value, before what they contain. It returns
// the renumbered fields and the old-to-new id mapping.
func freshIDs(fields []Field) ([]Field, map[int]int) {
	a := idAssigner{mapping: map[int]int{}}
	return a.fields(fields), a.mapping
}

type idAssigner struct {
	last    int
	mapping map[int]int
}

func (a *idAssigner) id(old int) int {
	a.last++
	a.mapping[old] = a.last
	return a.last
}

func (a *idAssigner) fields(in []Field) []Field {
	out := slices.Clone(in)
	for i := range out {
		out[i].ID = a.id(in[i].ID)
	}
	for i := range out {
		out[i].Type = a.typ(in[i].Type)
	}
	return out
}

func (a *idAssigner) typ(t Type) Type {
	switch {
	case t.Struct != nil:
		return Type{Struct: &StructType{Fields: a.fields(t.Struct.Fields)}}
	case t.List != nil:
		l := *t.List
		l.ElementID = a.id(l.ElementID)
		l.Element = a.typ(l.Element)
		return Type{List: &l}
	case t.Map != nil:
		m := *t.Map
		m.KeyID = a.id(m.KeyID)
		m.ValueID = a.id(m.ValueID)
		m.Key = a.typ(m.Key)
		m.Value = a.typ(m.Value)
		return Type{Map: &m}
	default:
		return t
	}
}

// equalJSON compares two values by their JSON, which is how the spec defines
// them; it is used only for the small structures a commit compares.
func equalJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}
