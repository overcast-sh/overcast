// Package stepfunctions_test — JSONata 2.x functions and operators, as AWS
// evaluates them.
//
// Run: go test ./tests/integration/stepfunctions/...
package stepfunctions_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestStartExecution_jsonata2Functions(t *testing.T) {
	// Given: a JSONata workflow using functions and operators JSONata 1.x lacks
	srv := helpers.NewTestServer(t)
	def := `{"QueryLanguage":"JSONata","StartAt":"Shape","States":{
	  "Shape":{"Type":"Pass",
	    "Assign":{"total":"{% $sum($states.input.orders.price) %}"},
	    "Output":{
	      "orders":"{% $states.input.orders %}",
	      "words":"{% $formatInteger($count($states.input.orders), 'w') %}",
	      "parsed":"{% $parseInteger('12,345', '#,##0') %}",
	      "ids":"{% $distinct($states.input.orders.customer) %}",
	      "padded":"{% $pad($string($states.input.orders[0].id), -4, '0') %}",
	      "withParent":"{% $states.input.orders.lines.{'sku': sku, 'order': %.id} %}",
	      "day":"{% $fromMillis($toMillis('2024-11-20T10:00:00Z'), '[FNn] [D1o] [MNn]') %}",
	      "nowIsISO":"{% $contains($now(), /^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{3}Z$/) %}",
	      "chunks":"{% $partition($states.input.orders.id, 2) %}"
	    },
	    "Next":"Total"},
	  "Total":{"Type":"Pass","Output":"{% $merge([$states.input, {'total': $formatNumber($total, '#,##0.00')}]) %}","End":true}
	}}`
	input := `{"orders":[
	  {"id":7,"customer":"a","price":1000.5,"lines":[{"sku":"x"}]},
	  {"id":8,"customer":"b","price":20,"lines":[{"sku":"y"},{"sku":"z"}]},
	  {"id":9,"customer":"a","price":3}
	]}`

	// When: it runs
	got, _ := runToEnd(t, srv, "jsonata2-functions", def, input)

	// Then: every expression evaluates as JSONata 2.x does on AWS
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status=%q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(got.Output), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"words":      `"three"`,
		"parsed":     `12345`,
		"ids":        `["a","b"]`,
		"padded":     `"0007"`,
		"withParent": `[{"order":7,"sku":"x"},{"order":8,"sku":"y"},{"order":8,"sku":"z"}]`,
		"day":        `"Wednesday 20th November"`,
		"nowIsISO":   `true`,
		"chunks":     `[[7,8],[9]]`,
		"total":      `"1,023.50"`,
	}
	for key, expected := range want {
		encoded, _ := json.Marshal(out[key])
		if string(encoded) != expected {
			t.Errorf("%s = %s, want %s", key, encoded, expected)
		}
	}
}

func TestStartExecution_jsonataEvalIsNotAvailable(t *testing.T) {
	// Given: a JSONata state calling $eval, which Step Functions does not offer
	srv := helpers.NewTestServer(t)
	def := `{"QueryLanguage":"JSONata","StartAt":"E","States":{"E":{"Type":"Pass","Output":"{% $eval('1 + 1') %}","End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "jsonata-eval", def, `{}`)

	// Then: the state fails with States.QueryEvaluationError pointing at $parse
	if got.Status != "FAILED" || got.Error != "States.QueryEvaluationError" || !strings.Contains(got.Cause, "$parse") {
		t.Fatalf("status=%q error=%q cause=%q", got.Status, got.Error, got.Cause)
	}
}
