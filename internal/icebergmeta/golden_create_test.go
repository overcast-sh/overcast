package icebergmeta

// Golden new tables captured from PyIceberg 0.12.0's new_table_metadata: a
// nested schema in the caller's own out-of-order numbering, with the
// partition spec and sort order that name its columns by those ids, and the
// metadata PyIceberg built from it. testdata/pyiceberg-create/capture.py
// regenerates them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type goldenCreate struct {
	Name    string `json:"name"`
	Request struct {
		TableUUID       string            `json:"table-uuid"`
		Location        string            `json:"location"`
		Schema          Schema            `json:"schema"`
		PartitionFields []PartitionField  `json:"partition-fields"`
		WriteOrder      SortOrder         `json:"write-order"`
		Properties      map[string]string `json:"properties"`
	} `json:"request"`
	Expected json.RawMessage `json:"expected"`
}

func loadGoldenCreates(t *testing.T) []goldenCreate {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "pyiceberg-create", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no golden fixtures: %v", err)
	}
	out := make([]goldenCreate, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var g goldenCreate
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out = append(out, g)
	}
	return out
}

func TestNew_matchesPyIcebergGoldenCreates(t *testing.T) {
	for _, g := range loadGoldenCreates(t) {
		t.Run(g.Name, func(t *testing.T) {
			// Given: the table definition PyIceberg was given
			r := g.Request
			spec := CreateSpec{
				TableUUID:          r.TableUUID,
				Location:           r.Location,
				Fields:             r.Schema.Fields,
				IdentifierFieldIDs: r.Schema.IdentifierFieldIDs,
				PartitionFields:    r.PartitionFields,
				SortFields:         r.WriteOrder.Fields,
				Properties:         r.Properties,
			}
			expected := normalisedDoc(t, g.Expected)
			now := time.UnixMilli(int64(expected["last-updated-ms"].(float64)))

			// When: the table is created here
			got, err := New(spec, now)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}

			// Then: the metadata is the document PyIceberg built
			assertSameDoc(t, normalisedDoc(t, raw), expected)
		})
	}
}
