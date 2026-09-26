package main

// athena_output.go — how `overcast athena query` prints a result: an aligned
// table for a person, JSON (one object per row) for jq, or CSV.

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/overcast-sh/overcast/internal/athenaquery"
)

type resultFormat string

const (
	formatTable resultFormat = "table"
	formatJSON  resultFormat = "json"
	formatCSV   resultFormat = "csv"
)

func parseResultFormat(s string) (resultFormat, error) {
	switch f := resultFormat(strings.ToLower(s)); f {
	case formatTable, formatJSON, formatCSV:
		return f, nil
	}
	return "", fmt.Errorf("unknown --output %q: want table, json or csv", s)
}

func writeResult(out io.Writer, format resultFormat, res *athenaquery.Result) error {
	switch format {
	case formatJSON:
		return writeResultJSON(out, res)
	case formatCSV:
		return writeResultCSV(out, res)
	}
	return writeResultTable(out, res)
}

// writeResultTable prints the rows under their column names, with NULL for a
// null, then the row count; a statement with no result columns prints the
// rows it changed, when it says.
func writeResultTable(out io.Writer, res *athenaquery.Result) error {
	if len(res.Columns) == 0 {
		if res.UpdateCount != nil {
			_, err := fmt.Fprintf(out, "%d rows affected\n", *res.UpdateCount)
			return err
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	fmt.Fprintln(tw, strings.Join(names, "\t"))
	for _, row := range res.Rows {
		fmt.Fprintln(tw, strings.Join(cells(row, "NULL"), "\t"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "(%d %s)\n", len(res.Rows), plural(len(res.Rows), "row"))
	return err
}

// writeResultJSON prints the rows as an array of objects keyed by column
// name, null for a null. Athena returns every value as text, so every value
// is a string.
func writeResultJSON(out io.Writer, res *athenaquery.Result) error {
	rows := make([]map[string]*string, len(res.Rows))
	for i, row := range res.Rows {
		rows[i] = make(map[string]*string, len(res.Columns))
		for j, c := range res.Columns {
			rows[i][c.Name] = row[j]
		}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

// writeResultCSV prints a header and the rows, a null as an empty field.
func writeResultCSV(out io.Writer, res *athenaquery.Result) error {
	w := csv.NewWriter(out)
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	if len(names) > 0 {
		_ = w.Write(names)
	}
	for _, row := range res.Rows {
		_ = w.Write(cells(row, ""))
	}
	w.Flush()
	return w.Error()
}

func cells(row []*string, null string) []string {
	out := make([]string, len(row))
	for i, v := range row {
		if v == nil {
			out[i] = null
		} else {
			out[i] = *v
		}
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
