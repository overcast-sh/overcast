package samples

import (
	"bytes"
	"encoding/binary"
	"math"
)

// parquet.go — a minimal Parquet writer: one row group of required,
// uncompressed, PLAIN-encoded columns, one data page per column. That is all
// a sample table needs, and every reader handles it; a full Parquet library
// would add megabytes to the binary for one small file.

// Parquet physical types and converted types (parquet.thrift).
type parquetType int32

const (
	parquetInt32     parquetType = 1
	parquetInt64     parquetType = 2
	parquetDouble    parquetType = 5
	parquetByteArray parquetType = 6
)

const (
	convertedNone = -1
	convertedUTF8 = 0
	convertedDate = 6
)

const (
	parquetMagic        = "PAR1"
	parquetRequired     = 0 // FieldRepetitionType REQUIRED
	parquetPlain        = 0 // Encoding PLAIN
	parquetRLE          = 3 // Encoding RLE, for the (absent) levels
	parquetUncompressed = 0 // CompressionCodec UNCOMPRESSED
	parquetDataPage     = 0 // PageType DATA_PAGE
)

// parquetColumn is one column: its schema and its PLAIN-encoded values.
type parquetColumn struct {
	name      string
	typ       parquetType
	converted int32
	values    bytes.Buffer
}

func (c *parquetColumn) putInt32(v int32) {
	c.values.Write(binary.LittleEndian.AppendUint32(nil, uint32(v)))
}

func (c *parquetColumn) putInt64(v int64) {
	c.values.Write(binary.LittleEndian.AppendUint64(nil, uint64(v)))
}

func (c *parquetColumn) putDouble(v float64) {
	c.values.Write(binary.LittleEndian.AppendUint64(nil, math.Float64bits(v)))
}

func (c *parquetColumn) putString(v string) {
	c.values.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(v))))
	c.values.WriteString(v)
}

// chunkMeta is where one column's page landed in the file.
type chunkMeta struct {
	offset, size int64
}

// encodeParquet is a Parquet file of numRows rows holding cols.
func encodeParquet(cols []*parquetColumn, numRows int) []byte {
	var out bytes.Buffer
	out.WriteString(parquetMagic)
	chunks := make([]chunkMeta, len(cols))
	for i, c := range cols {
		chunks[i].offset = int64(out.Len())
		out.Write(pageHeader(c.values.Len(), numRows))
		out.Write(c.values.Bytes())
		chunks[i].size = int64(out.Len()) - chunks[i].offset
	}
	footer := fileMetaData(cols, chunks, numRows)
	out.Write(footer)
	out.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(footer))))
	out.WriteString(parquetMagic)
	return out.Bytes()
}

func pageHeader(size, numValues int) []byte {
	w := &thriftWriter{}
	w.beginStruct()
	w.i32(1, parquetDataPage)
	w.i32(2, int32(size))     // uncompressed_page_size
	w.i32(3, int32(size))     // compressed_page_size
	w.structField(5, func() { // data_page_header
		w.i32(1, int32(numValues))
		w.i32(2, parquetPlain)
		w.i32(3, parquetRLE) // definition_level_encoding
		w.i32(4, parquetRLE) // repetition_level_encoding
	})
	w.endStruct()
	return w.buf.Bytes()
}

func fileMetaData(cols []*parquetColumn, chunks []chunkMeta, numRows int) []byte {
	w := &thriftWriter{}
	w.beginStruct()
	w.i32(1, 1) // version
	w.list(2, thriftStruct, len(cols)+1, func(i int) {
		w.elemStruct(func() {
			if i == 0 { // the root
				w.str(4, "schema")
				w.i32(5, int32(len(cols)))
				return
			}
			c := cols[i-1]
			w.i32(1, int32(c.typ))
			w.i32(3, parquetRequired)
			w.str(4, c.name)
			if c.converted != convertedNone {
				w.i32(6, c.converted)
			}
		})
	})
	w.i64(3, int64(numRows))
	w.list(4, thriftStruct, 1, func(int) { // one row group
		w.elemStruct(func() {
			w.list(1, thriftStruct, len(cols), func(i int) { columnChunk(w, cols[i], chunks[i], numRows) })
			w.i64(2, totalSize(chunks))
			w.i64(3, int64(numRows))
		})
	})
	w.str(6, "overcast samples")
	w.endStruct()
	return w.buf.Bytes()
}

func columnChunk(w *thriftWriter, c *parquetColumn, chunk chunkMeta, numRows int) {
	w.elemStruct(func() {
		w.i64(2, chunk.offset)    // file_offset
		w.structField(3, func() { // meta_data
			w.i32(1, int32(c.typ))
			w.list(2, thriftI32, 1, func(int) { w.elemI32(parquetPlain) })
			w.list(3, thriftBinary, 1, func(int) { w.elemString(c.name) })
			w.i32(4, parquetUncompressed)
			w.i64(5, int64(numRows))
			w.i64(6, chunk.size) // total_uncompressed_size
			w.i64(7, chunk.size) // total_compressed_size
			w.i64(9, chunk.offset)
		})
	})
}

func totalSize(chunks []chunkMeta) int64 {
	var n int64
	for _, c := range chunks {
		n += c.size
	}
	return n
}
