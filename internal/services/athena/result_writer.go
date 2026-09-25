package athena

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// result_writer.go — a finished query's result, written to its
// OutputLocation as Athena writes it.
//
// A DML statement's result is "<id>.csv": every field in double quotes, a
// quote inside a field doubled, a NULL as an empty unquoted field, one row a
// line, the header first. A DDL or UTILITY statement's is "<id>.txt": its
// rows as tab-separated lines. Beside either, "<object>.metadata" describes
// the columns. Athena writes that file in an undocumented binary encoding;
// Overcast writes the same ResultSetMetadata as JSON.

const csvContentType = "text/csv"

// resultWriter writes result objects through the in-process S3 accessor.
type resultWriter struct {
	put events.S3PutObjectFunc
}

// write stores res at location, an "s3://bucket/key" object URI, and its
// metadata beside it.
func (w resultWriter) write(ctx context.Context, location string, res *queryResult) *protocol.AWSError {
	bucket, key, ok := splitS3URI(location)
	if !ok {
		return errInvalidRequest("Result location %s is not an S3 URI.", location)
	}
	body, contentType := encodeText(res.Rows), "text/plain"
	if strings.HasSuffix(key, ".csv") {
		body, contentType = encodeCSV(res.Rows), csvContentType
	}
	if _, aerr := w.put(ctx, bucket, key, body, events.S3PutObjectOptions{ContentType: contentType}); aerr != nil {
		return aerr
	}
	meta, err := json.Marshal(ResultSetMetadata{ColumnInfo: res.Columns})
	if err != nil {
		return errInternal(err)
	}
	_, aerr := w.put(ctx, bucket, key+".metadata", meta, events.S3PutObjectOptions{ContentType: "application/json"})
	return aerr
}

// splitS3URI splits "s3://bucket/key" into its bucket and key.
func splitS3URI(uri string) (bucket, key string, ok bool) {
	rest, found := strings.CutPrefix(uri, "s3://")
	if !found {
		return "", "", false
	}
	bucket, key, found = strings.Cut(rest, "/")
	return bucket, key, found && bucket != ""
}

// encodeCSV renders rows with Athena's quoting.
func encodeCSV(rows [][]*string) []byte {
	return encodeRows(rows, ',', func(b *bytes.Buffer, cell string) {
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(cell, `"`, `""`))
		b.WriteByte('"')
	})
}

// encodeText renders rows as tab-separated lines.
func encodeText(rows [][]*string) []byte {
	return encodeRows(rows, '\t', func(b *bytes.Buffer, cell string) { b.WriteString(cell) })
}

// encodeRows writes one line per row, its cells separated by sep and each
// non-NULL cell written by field; a NULL is an empty cell.
func encodeRows(rows [][]*string, sep byte, field func(b *bytes.Buffer, cell string)) []byte {
	var b bytes.Buffer
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				b.WriteByte(sep)
			}
			if cell != nil {
				field(&b, *cell)
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}
