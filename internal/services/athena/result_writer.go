package athena

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// result_writer.go — a query's result, written to its OutputLocation as
// Athena writes it, while the statement is still producing it.
//
// A DML statement's result is "<id>.csv": every field in double quotes, a
// quote inside a field doubled, a NULL as an empty unquoted field, one row a
// line, the header first. A DDL or UTILITY statement's is "<id>.txt": its
// rows as tab-separated lines. Beside either, "<object>.metadata" describes
// the columns. Athena writes that file in an undocumented binary encoding;
// Overcast writes the same ResultSetMetadata as JSON.
//
// The object is streamed to S3: rows are encoded as they arrive and handed
// over in batches of resultFlushBytes, and S3 stores the object, and sends
// its one notification, when the result is closed. An abandoned result
// leaves nothing behind.

const csvContentType = "text/csv"

// resultFlushBytes is how much encoded result gathers before it is handed
// to S3.
const resultFlushBytes = 64 << 10

// errResultAbandoned ends the write of a result that is not kept.
var errResultAbandoned = errors.New("athena: query result abandoned")

// resultWriter writes result objects through the in-process S3 accessor.
type resultWriter struct {
	put events.S3PutObjectStreamFunc
}

// resultFormat is how a result object lays out its rows: sep between cells,
// and field for each non-NULL cell; a NULL is an empty cell.
type resultFormat struct {
	contentType string
	sep         byte
	field       func(b *bytes.Buffer, cell string)
}

var (
	csvFormat = resultFormat{contentType: csvContentType, sep: ',', field: func(b *bytes.Buffer, cell string) {
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(cell, `"`, `""`))
		b.WriteByte('"')
	}}
	textFormat = resultFormat{contentType: "text/plain", sep: '\t', field: func(b *bytes.Buffer, cell string) {
		b.WriteString(cell)
	}}
)

// formatFor is the format of the result object at key.
func formatFor(key string) resultFormat {
	if strings.HasSuffix(key, ".csv") {
		return csvFormat
	}
	return textFormat
}

// appendRow writes row to b as one line.
func (f resultFormat) appendRow(b *bytes.Buffer, row []*string) {
	for i, cell := range row {
		if i > 0 {
			b.WriteByte(f.sep)
		}
		if cell != nil {
			f.field(b, *cell)
		}
	}
	b.WriteByte('\n')
}

// resultUpload is one result object being written.
type resultUpload struct {
	w           resultWriter
	bucket, key string
	format      resultFormat
	pending     bytes.Buffer
	body        *io.PipeWriter
	// done is closed when the put has returned, with aerr.
	done chan struct{}
	aerr *protocol.AWSError
}

// open starts the result object at location, an "s3://bucket/key" object
// URI. The put runs until the object is closed or abandoned.
func (w resultWriter) open(ctx context.Context, location string) (*resultUpload, *protocol.AWSError) {
	bucket, key, ok := serviceutil.SplitS3URI(location)
	if !ok {
		return nil, errInvalidRequest("Result location %s is not an S3 URI.", location)
	}
	pr, pw := io.Pipe()
	o := &resultUpload{w: w, bucket: bucket, key: key, format: formatFor(key), body: pw, done: make(chan struct{})}
	go func() {
		defer close(o.done)
		_, o.aerr = w.put(ctx, bucket, key, pr, events.S3PutObjectOptions{ContentType: o.format.contentType})
		pr.Close() // a put that failed before reading everything must not block the writer
	}()
	return o, nil
}

// write encodes row, and hands the encoded rows to S3 once enough gather.
func (o *resultUpload) write(row []*string) *protocol.AWSError {
	o.format.appendRow(&o.pending, row)
	if o.pending.Len() < resultFlushBytes {
		return nil
	}
	return o.flush()
}

func (o *resultUpload) flush() *protocol.AWSError {
	if o.pending.Len() == 0 {
		return nil
	}
	_, err := o.body.Write(o.pending.Bytes())
	o.pending.Reset()
	if err != nil {
		<-o.done
		if o.aerr != nil { // the put's own error, NoSuchBucket above all
			return o.aerr
		}
		return errInternal(err)
	}
	return nil
}

// close finishes the object, and writes its metadata beside it.
func (o *resultUpload) close(ctx context.Context, columns []ColumnInfo) *protocol.AWSError {
	if aerr := o.flush(); aerr != nil {
		return aerr
	}
	o.body.Close()
	<-o.done
	if o.aerr != nil {
		return o.aerr
	}
	meta, err := json.Marshal(ResultSetMetadata{ColumnInfo: columns})
	if err != nil {
		return errInternal(err)
	}
	_, aerr := o.w.put(ctx, o.bucket, o.key+".metadata", bytes.NewReader(meta), events.S3PutObjectOptions{ContentType: "application/json"})
	return aerr
}

// abandon ends the write without storing the object. Once the object is
// closed it does nothing.
func (o *resultUpload) abandon() {
	o.body.CloseWithError(errResultAbandoned)
	<-o.done
}

// resultWriteFailure is how a query whose result could not be written to
// location fails: a missing bucket is the user's error, anything else the
// service's.
func resultWriteFailure(location string, aerr *protocol.AWSError) *queryFailure {
	category, errorType := errorCategorySystem, errorTypeWriteResults
	if aerr.Code == "NoSuchBucket" {
		category, errorType = errorCategoryUser, errorTypeBucketNotFound
	}
	return failure(category, errorType, "Unable to write query results to "+location+": "+aerr.Code+": "+aerr.Message)
}
