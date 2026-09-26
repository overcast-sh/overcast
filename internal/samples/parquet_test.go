package samples

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// compactReader decodes the compact-protocol structs the writer produces
// into field-id-keyed maps, enough to check a footer's shape.
type compactReader struct {
	t   *testing.T
	b   []byte
	pos int
}

func (r *compactReader) varint() uint64 {
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		r.t.Fatalf("bad varint at %d", r.pos)
	}
	r.pos += n
	return v
}

func (r *compactReader) zigzag() int64 {
	v := r.varint()
	return int64(v>>1) ^ -int64(v&1)
}

func (r *compactReader) value(typ byte) any {
	switch typ {
	case thriftI32, thriftI64:
		return r.zigzag()
	case thriftBinary:
		n := int(r.varint())
		s := string(r.b[r.pos : r.pos+n])
		r.pos += n
		return s
	case thriftList:
		head := r.b[r.pos]
		r.pos++
		n, elem := int(head>>4), head&0x0f
		if n == 15 {
			n = int(r.varint())
		}
		out := make([]any, n)
		for i := range out {
			out[i] = r.value(elem)
		}
		return out
	case thriftStruct:
		return r.structure()
	}
	r.t.Fatalf("unexpected type %d", typ)
	return nil
}

func (r *compactReader) structure() map[int]any {
	fields := map[int]any{}
	last := 0
	for {
		head := r.b[r.pos]
		r.pos++
		if head == 0 {
			return fields
		}
		id := last + int(head>>4)
		if head>>4 == 0 {
			id = int(r.zigzag())
		}
		fields[id] = r.value(head & 0x0f)
		last = id
	}
}

func footer(t *testing.T, file []byte) map[int]any {
	t.Helper()
	if !bytes.HasPrefix(file, []byte("PAR1")) || !bytes.HasSuffix(file, []byte("PAR1")) {
		t.Fatal("missing magic")
	}
	n := int(binary.LittleEndian.Uint32(file[len(file)-8:]))
	r := &compactReader{t: t, b: file[len(file)-8-n : len(file)-8]}
	return r.structure()
}

func TestEncodeParquet_footerDescribesEachColumnsPage(t *testing.T) {
	// Given: two columns of three rows
	id := &parquetColumn{name: "id", typ: parquetInt64, converted: convertedNone}
	name := &parquetColumn{name: "name", typ: parquetByteArray, converted: convertedUTF8}
	price := &parquetColumn{name: "price", typ: parquetDouble, converted: convertedNone}
	for i, s := range []string{"a", "bb", "ccc"} {
		id.putInt64(int64(i + 1))
		name.putString(s)
		price.putDouble(float64(i) + 0.5)
	}

	// When: they are encoded
	file := encodeParquet([]*parquetColumn{id, name, price}, 3)

	// Then: the footer names the schema, the rows and where each page is
	meta := footer(t, file)
	if meta[1] != int64(1) || meta[3] != int64(3) || meta[6] != "overcast samples" {
		t.Fatalf("version/rows/created_by = %v/%v/%v", meta[1], meta[3], meta[6])
	}
	schema := meta[2].([]any)
	if root := schema[0].(map[int]any); root[4] != "schema" || root[5] != int64(3) {
		t.Fatalf("root = %v", root)
	}
	if leaf := schema[2].(map[int]any); leaf[1] != int64(parquetByteArray) || leaf[3] != int64(parquetRequired) || leaf[4] != "name" || leaf[6] != int64(convertedUTF8) {
		t.Fatalf("name column = %v", leaf)
	}
	chunks := meta[4].([]any)[0].(map[int]any)[1].([]any)
	for i, want := range []string{"id", "name", "price"} {
		cm := chunks[i].(map[int]any)[3].(map[int]any)
		offset := int(cm[9].(int64))
		page := &compactReader{t: t, b: file, pos: offset}
		header := page.structure()
		size := int(header[2].(int64))
		if cm[3].([]any)[0] != want || cm[5] != int64(3) || int64(page.pos-offset+size) != cm[6] {
			t.Fatalf("column %d meta = %v, header = %v", i, cm, header)
		}
		values := file[page.pos : page.pos+size]
		switch want {
		case "id":
			if binary.LittleEndian.Uint64(values[16:]) != 3 {
				t.Fatalf("id values = %v", values)
			}
		case "name":
			if !bytes.Equal(values[:5], []byte{1, 0, 0, 0, 'a'}) || string(values[len(values)-3:]) != "ccc" {
				t.Fatalf("name values = %q", values)
			}
		case "price":
			if math.Float64frombits(binary.LittleEndian.Uint64(values[8:])) != 1.5 {
				t.Fatalf("price values = %v", values)
			}
		}
	}
}

func TestThriftWriter_longListsAndFarFieldIDs(t *testing.T) {
	w := &thriftWriter{}
	w.beginStruct()
	w.list(1, thriftI32, 20, func(i int) { w.elemI32(int32(-i)) })
	w.i64(40, -7) // more than 15 past the last id
	w.endStruct()

	got := (&compactReader{t: t, b: w.buf.Bytes()}).structure()

	if list := got[1].([]any); len(list) != 20 || list[19] != int64(-19) || got[40] != int64(-7) {
		t.Fatalf("decoded = %v", got)
	}
}
