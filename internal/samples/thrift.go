package samples

import (
	"bytes"
	"encoding/binary"
)

// thrift.go — the few pieces of Thrift's compact protocol a Parquet footer
// and page header need: structs of i32, i64, binary, list and struct fields.

// Compact protocol field and element types.
const (
	thriftI32    byte = 5
	thriftI64    byte = 6
	thriftBinary byte = 8
	thriftList   byte = 9
	thriftStruct byte = 12
)

// thriftWriter encodes one value in the compact protocol. Each struct being
// written keeps the id of its last field, which the next field's header is
// relative to.
type thriftWriter struct {
	buf  bytes.Buffer
	last []int16
}

func (w *thriftWriter) beginStruct() { w.last = append(w.last, 0) }

func (w *thriftWriter) endStruct() {
	w.buf.WriteByte(0) // stop
	w.last = w.last[:len(w.last)-1]
}

func (w *thriftWriter) fieldHeader(id int16, typ byte) {
	top := len(w.last) - 1
	if delta := id - w.last[top]; delta > 0 && delta <= 15 {
		w.buf.WriteByte(byte(delta)<<4 | typ)
	} else {
		w.buf.WriteByte(typ)
		w.varint(zigzag(int64(id)))
	}
	w.last[top] = id
}

func (w *thriftWriter) i32(id int16, v int32) {
	w.fieldHeader(id, thriftI32)
	w.varint(zigzag(int64(v)))
}

func (w *thriftWriter) i64(id int16, v int64) {
	w.fieldHeader(id, thriftI64)
	w.varint(zigzag(v))
}

func (w *thriftWriter) str(id int16, s string) {
	w.fieldHeader(id, thriftBinary)
	w.binary(s)
}

// structField writes a struct-valued field whose fields body writes.
func (w *thriftWriter) structField(id int16, body func()) {
	w.fieldHeader(id, thriftStruct)
	w.beginStruct()
	body()
	w.endStruct()
}

// list writes a list field of n elements of type elem; element writes
// element i with the element encoders below.
func (w *thriftWriter) list(id int16, elem byte, n int, element func(i int)) {
	w.fieldHeader(id, thriftList)
	if n < 15 {
		w.buf.WriteByte(byte(n)<<4 | elem)
	} else {
		w.buf.WriteByte(0xf0 | elem)
		w.varint(uint64(n))
	}
	for i := range n {
		element(i)
	}
}

// Element encoders, for a list's elements, which have no field header.

func (w *thriftWriter) elemI32(v int32)     { w.varint(zigzag(int64(v))) }
func (w *thriftWriter) elemString(s string) { w.binary(s) }

func (w *thriftWriter) elemStruct(body func()) {
	w.beginStruct()
	body()
	w.endStruct()
}

func (w *thriftWriter) binary(s string) {
	w.varint(uint64(len(s)))
	w.buf.WriteString(s)
}

func (w *thriftWriter) varint(v uint64) { w.buf.Write(binary.AppendUvarint(nil, v)) }

func zigzag(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }
