package icebergmeta

// Which of a schema's ids its identifier field ids, partition fields and sort
// fields may name. A nested schema declares ids a column cannot be derived
// from — a struct, a list element, a map key or value — and each reference is
// held to the rule the spec, and the reference implementation's checks, give
// it.
//
// Spec: https://iceberg.apache.org/spec/#identifier-field-ids and
// https://iceberg.apache.org/spec/#partitioning

// voidTransform is the partition transform that always produces null. The
// reference implementation checks nothing about a void field's source type,
// which a version 1 table keeps in place of a dropped partition field.
const voidTransform = "void"

// checkIdentifierField applies the spec's rules for an identifier field: a
// primitive reached through structs only, neither float nor double, and
// required along with every struct it is nested in.
func checkIdentifierField(s Schema, id int) error {
	if problem := s.structPrimitiveProblem(id); problem != "" {
		return invalid("Identifier field id %d %s.", id, problem)
	}
	path, _ := structPath(s.Fields, id)
	f := path[len(path)-1]
	if f.Type.Primitive == "float" || f.Type.Primitive == "double" {
		return invalid("Identifier field %q must not be a float or double field.", f.Name)
	}
	for _, p := range path {
		if !p.Required {
			return invalid("Identifier field %q must be required, and nested only in required structs.", f.Name)
		}
	}
	return nil
}

// partitionSourceProblem says why id cannot be the source of a partition
// field with transform, or "" when it can: the spec requires a primitive,
// which may be nested in a struct but not contained in a list or map.
func (s Schema) partitionSourceProblem(id int, transform string) string {
	if transform == voidTransform {
		return s.declaredProblem(id)
	}
	return s.structPrimitiveProblem(id)
}

// sortSourceProblem says why id cannot be the source of a sort field, or ""
// when it can. The reference implementation asks only for a primitive: unlike
// a partition source, a sort source may be a list element or a map key or
// value.
func (s Schema) sortSourceProblem(id int) string {
	t, ok := typeOf(s.Fields, id)
	switch {
	case !ok:
		return "is not a field of the schema"
	case !t.isPrimitive():
		return "is not a primitive field"
	}
	return ""
}

// structPrimitiveProblem says why id is not a primitive field reached through
// structs only, or "" when it is.
func (s Schema) structPrimitiveProblem(id int) string {
	if problem := s.declaredProblem(id); problem != "" {
		return problem
	}
	path, ok := structPath(s.Fields, id)
	switch {
	case !ok:
		return "is a list element, a map key or value, or nested in one"
	case !path[len(path)-1].Type.isPrimitive():
		return "is not a primitive field"
	}
	return ""
}

func (s Schema) declaredProblem(id int) string {
	if _, ok := typeOf(s.Fields, id); !ok {
		return "is not a field of the schema"
	}
	return ""
}

// structPath is the chain of fields from the top of the schema down to the
// field declaring id, descending through structs only. ok is false when no
// field reached that way declares it.
func structPath(fields []Field, id int) ([]Field, bool) {
	for _, f := range fields {
		if f.ID == id {
			return []Field{f}, true
		}
		if f.Type.Struct == nil {
			continue
		}
		if path, ok := structPath(f.Type.Struct.Fields, id); ok {
			return append([]Field{f}, path...), true
		}
	}
	return nil, false
}

// typeOf is the type of whatever in fields declares id — a field, a list
// element, or a map key or value — at any depth.
func typeOf(fields []Field, id int) (Type, bool) {
	for _, f := range fields {
		if f.ID == id {
			return f.Type, true
		}
		if t, ok := typeWithin(f.Type, id); ok {
			return t, true
		}
	}
	return Type{}, false
}

func typeWithin(t Type, id int) (Type, bool) {
	switch {
	case t.Struct != nil:
		return typeOf(t.Struct.Fields, id)
	case t.List != nil:
		if t.List.ElementID == id {
			return t.List.Element, true
		}
		return typeWithin(t.List.Element, id)
	case t.Map != nil:
		if t.Map.KeyID == id {
			return t.Map.Key, true
		}
		if t.Map.ValueID == id {
			return t.Map.Value, true
		}
		if found, ok := typeWithin(t.Map.Key, id); ok {
			return found, true
		}
		return typeWithin(t.Map.Value, id)
	}
	return Type{}, false
}

func (t Type) isPrimitive() bool { return t.Struct == nil && t.List == nil && t.Map == nil }
