// Package stepfunctions_test — inline Map semantics.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestStartExecution_mapItemSelectorReadsTheMapInput(t *testing.T) {
	// Given: an ItemSelector combining a field of the Map's input with the item
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "M",
	  "States": {
	    "M": {
	      "Type": "Map",
	      "ItemsPath": "$.items",
	      "ItemSelector": {"prefix.$": "$.prefix", "value.$": "$$.Map.Item.Value", "index.$": "$$.Map.Item.Index"},
	      "ItemProcessor": {"StartAt": "Echo", "States": {"Echo": {"Type": "Pass", "End": true}}},
	      "End": true
	    }
	  }
	}`

	// When: it runs
	got, _ := runToEnd(t, srv, "map-selector", def, `{"prefix":"p","items":["a","b"]}`)

	// Then: `$` in the ItemSelector is the Map state's input, not the item
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	want := `[{"index":0,"prefix":"p","value":"a"},{"index":1,"prefix":"p","value":"b"}]`
	if got.Output != want {
		t.Errorf("output = %s, want %s", got.Output, want)
	}
}

func TestStartExecution_mapMaxConcurrencyOneRunsIterationsInOrder(t *testing.T) {
	// Given: a Map limited to one iteration at a time
	srv := helpers.NewTestServer(t)
	def := `{
	  "StartAt": "M",
	  "States": {
	    "M": {
	      "Type": "Map",
	      "MaxConcurrency": 1,
	      "ItemProcessor": {"StartAt": "Echo", "States": {"Echo": {"Type": "Pass", "End": true}}},
	      "End": true
	    }
	  }
	}`

	// When: it runs over four items
	got, execARN := runToEnd(t, srv, "map-serial", def, `[0,1,2,3]`)
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q", got.Status)
	}

	// Then: each iteration finishes before the next one starts
	var order []string
	for _, e := range rawHistory(t, srv, execARN) {
		switch e.typ() {
		case "MapIterationStarted", "MapIterationSucceeded":
			order = append(order, e.typ())
		}
	}
	if len(order) != 8 {
		t.Fatalf("iteration events = %v", order)
	}
	for i := 0; i+1 < len(order); i += 2 {
		if order[i] != "MapIterationStarted" || order[i+1] != "MapIterationSucceeded" {
			t.Fatalf("iterations overlapped: %v", order)
		}
	}
}
