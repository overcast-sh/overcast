package athena

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// legacyEncodeCSV and legacyEncodeText are the encoders that built a whole
// result in one buffer before results were streamed, kept verbatim as the
// oracle the streaming encoder must match byte for byte.
func legacyEncodeCSV(rows [][]*string) []byte {
	return legacyEncodeRows(rows, ',', func(b *bytes.Buffer, cell string) {
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(cell, `"`, `""`))
		b.WriteByte('"')
	})
}

func legacyEncodeText(rows [][]*string) []byte {
	return legacyEncodeRows(rows, '\t', func(b *bytes.Buffer, cell string) { b.WriteString(cell) })
}

func legacyEncodeRows(rows [][]*string, sep byte, field func(b *bytes.Buffer, cell string)) []byte {
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

// encodeAll encodes rows one at a time, as a streamed result is.
func encodeAll(f resultFormat, rows [][]*string) []byte {
	var b bytes.Buffer
	for _, row := range rows {
		f.appendRow(&b, row)
	}
	return b.Bytes()
}

// awkwardRows is a result with every value the quoting has to get right:
// quotes, separators, line breaks, empty strings, NULLs, and text that is
// not ASCII.
func awkwardRows() [][]*string {
	s := func(v string) *string { return &v }
	return [][]*string{
		textRow("id", "name", "note"),
		{s("1"), s(`say "hi", ok`), nil},
		{s("2"), s(""), s("tab\there")},
		{s("3"), s("line\nbreak\r\n"), s(`""`)},
		{nil, nil, nil},
		{s("4"), s("  padded  "), s("naïve café, 東京")},
		{},
	}
}

func TestResultFormat_matchesTheBufferedEncoder(t *testing.T) {
	rows := awkwardRows()
	if got, want := encodeAll(csvFormat, rows), legacyEncodeCSV(rows); !bytes.Equal(got, want) {
		t.Errorf("CSV =\n%q\nwant\n%q", got, want)
	}
	if got, want := encodeAll(textFormat, rows), legacyEncodeText(rows); !bytes.Equal(got, want) {
		t.Errorf("text =\n%q\nwant\n%q", got, want)
	}
}

func TestResultFormat_quotesAsAthenaDoes(t *testing.T) {
	quoted, empty := `say "hi", ok`, ""
	got := string(encodeAll(csvFormat, [][]*string{textRow("a", "b", "c"), {&quoted, nil, &empty}}))
	if want := "\"a\",\"b\",\"c\"\n\"say \"\"hi\"\", ok\",,\"\"\n"; got != want {
		t.Fatalf("CSV = %q, want %q", got, want)
	}
}

func TestResultUpload_isStoredWhenClosed(t *testing.T) {
	// Given: a result object written a row at a time, past a flush
	s3 := &fakeS3{objects: map[string]string{}}
	up, aerr := resultWriter{put: s3.put}.open(context.Background(), "s3://results/q.csv")
	mustOK(t, "open", aerr)
	rows := [][]*string{textRow("n")}
	for range resultFlushBytes / 4 {
		rows = append(rows, textRow("ab"))
	}
	for _, row := range rows {
		mustOK(t, "write", up.write(row))
	}

	// When: it is closed
	mustOK(t, "close", up.close(context.Background(), textColumns("n")))

	// Then: the object is the whole result, with its metadata beside it
	if got := s3.objects["results/q.csv"]; got != string(legacyEncodeCSV(rows)) {
		t.Fatalf("object is %d bytes, want %d", len(got), len(legacyEncodeCSV(rows)))
	}
	if meta := s3.objects["results/q.csv.metadata"]; !strings.Contains(meta, `"Name":"n"`) {
		t.Fatalf("metadata = %s", meta)
	}
}

func TestResultUpload_abandonedLeavesNothing(t *testing.T) {
	s3 := &fakeS3{objects: map[string]string{}}
	up, aerr := resultWriter{put: s3.put}.open(context.Background(), "s3://results/q.txt")
	mustOK(t, "open", aerr)
	mustOK(t, "write", up.write(textRow(strings.Repeat("x", resultFlushBytes))))

	up.abandon()

	if len(s3.objects) != 0 {
		t.Fatalf("objects = %v", s3.objects)
	}
}

func TestResultUpload_missingBucketFailsTheWrite(t *testing.T) {
	up, aerr := resultWriter{put: (&fakeS3{}).put}.open(context.Background(), "s3://missing/q.csv")
	mustOK(t, "open", aerr)

	aerr = up.write(textRow(strings.Repeat("x", resultFlushBytes)))

	if aerr == nil || aerr.Code != "NoSuchBucket" {
		t.Fatalf("write = %v, want NoSuchBucket", aerr)
	}
}
