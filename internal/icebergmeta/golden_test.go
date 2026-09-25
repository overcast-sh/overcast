package icebergmeta

// Golden commits captured from PyIceberg 0.12.0's own commit path: its
// SqlCatalog over a local warehouse, with the catalog's staging step wrapped to
// record, for each commit, the metadata before, the metadata file it came
// from, the CommitTableRequest a REST client would send, and the metadata
// PyIceberg built from them. testdata/pyiceberg/capture.py regenerates them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type goldenCommit struct {
	Name         string          `json:"name"`
	Base         json.RawMessage `json:"base"`
	BaseLocation string          `json:"base-location"`
	Request      CommitRequest   `json:"request"`
	Expected     json.RawMessage `json:"expected"`
}

func loadGoldenCommits(t *testing.T) []goldenCommit {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "pyiceberg", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no golden fixtures: %v", err)
	}
	out := make([]goldenCommit, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var g goldenCommit
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out = append(out, g)
	}
	return out
}

// normalisedDoc decodes a metadata document for comparison, folding the three
// places where PyIceberg and the reference Java implementation (which this
// package follows) write the same table differently:
//
//   - a table with no current snapshot: PyIceberg omits current-snapshot-id,
//     Java writes -1;
//   - a schema without identifier fields: PyIceberg writes
//     "identifier-field-ids": [], Java omits the member;
//   - a snapshot whose parent was expired: PyIceberg clears its
//     parent-snapshot-id, Java leaves the id of the snapshot that is gone.
//
// Every reader treats each pair the same.
func normalisedDoc(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := doc["current-snapshot-id"]; !ok || v == nil {
		doc["current-snapshot-id"] = float64(noSnapshot)
	}
	for _, s := range doc["schemas"].([]any) {
		schema := s.(map[string]any)
		if ids, ok := schema["identifier-field-ids"].([]any); ok && len(ids) == 0 {
			delete(schema, "identifier-field-ids")
		}
	}
	snapshots := doc["snapshots"].([]any)
	present := map[float64]bool{}
	for _, s := range snapshots {
		present[s.(map[string]any)["snapshot-id"].(float64)] = true
	}
	for _, s := range snapshots {
		snap := s.(map[string]any)
		if parent, ok := snap["parent-snapshot-id"].(float64); ok && !present[parent] {
			delete(snap, "parent-snapshot-id")
		}
	}
	return doc
}

func TestCommit_matchesPyIcebergGoldenCommits(t *testing.T) {
	for _, g := range loadGoldenCommits(t) {
		t.Run(g.Name, func(t *testing.T) {
			// Given: the table as PyIceberg read it before the commit
			var base *Metadata
			if string(g.Base) != "null" {
				var err error
				if base, err = Parse(g.Base); err != nil {
					t.Fatalf("Parse base: %v", err)
				}
			}
			expected := normalisedDoc(t, g.Expected)
			// PyIceberg stamped the commit with its own clock where no snapshot
			// did; replaying at that instant makes the two comparable.
			now := time.UnixMilli(int64(expected["last-updated-ms"].(float64)))

			// When: the same CommitTableRequest is applied here
			got, err := Commit(base, g.BaseLocation, g.Request, now)
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}

			// Then: the metadata is the document PyIceberg wrote
			if doc := normalisedDoc(t, raw); !reflect.DeepEqual(doc, expected) {
				for k := range mergedKeys(doc, expected) {
					if !reflect.DeepEqual(doc[k], expected[k]) {
						t.Errorf("%s:\n got  %v\n want %v", k, doc[k], expected[k])
					}
				}
			}
		})
	}
}

func mergedKeys(a, b map[string]any) map[string]bool {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	return keys
}
