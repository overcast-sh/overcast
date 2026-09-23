package cloudformation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

// recordingGlue is a router that answers every Glue call with an empty 200 and
// keeps the last request body, so a test can see exactly what a handler sent.
type recordingGlue struct {
	target string
	body   map[string]any
}

func (r *recordingGlue) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.target = req.Header.Get("X-Amz-Target")
	raw, _ := io.ReadAll(req.Body)
	r.body = nil
	_ = json.Unmarshal(raw, &r.body)
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_, _ = w.Write([]byte("{}"))
}

func partitionProps(values ...any) map[string]any {
	return map[string]any{
		"CatalogId":      "123456789012",
		"DatabaseName":   "db",
		"TableName":      "events",
		"PartitionInput": map[string]any{"Values": values, "Parameters": map[string]any{"k": "v"}},
	}
}

func TestGluePartitionHandler_numericValuesAreSentAsStrings(t *testing.T) {
	// Given: a template that wrote its partition values unquoted
	router := &recordingGlue{}
	props := partitionProps(float64(2020), float64(1.5))

	// When: the partition is created
	id, _, err := (&gluePartitionHandler{}).Create(context.Background(), router, nil, props, &resolveContext{Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}

	// Then: CreatePartition received strings spelled as written, and the
	// template's own map was left alone
	input, _ := router.body["PartitionInput"].(map[string]any)
	if got := input["Values"]; !reflect.DeepEqual(got, []any{"2020", "1.5"}) {
		t.Errorf("Values sent = %#v, want [2020 1.5] as strings", got)
	}
	if id != "db/events/2020/1.5" {
		t.Errorf("physical ID = %q", id)
	}
	if v := props["PartitionInput"].(map[string]any)["Values"].([]any)[0]; v != float64(2020) {
		t.Errorf("the template's props were mutated: %#v", v)
	}
}

func TestGluePartitionHandler_updateNamesThePartitionFromOldProps(t *testing.T) {
	// Given: a rollback, where the provisioner passes the previous physical ID
	// while the partition already lives at the values the failed update set
	router := &recordingGlue{}
	previous := partitionProps("2024")
	attempted := partitionProps("2025")

	// When: the handler rolls back from attempted to previous
	id, _, err := (&gluePartitionHandler{}).Update(context.Background(), router, nil,
		"db/events/2024", previous, attempted, &resolveContext{Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}

	// Then: UpdatePartition addresses the partition where it is now, and
	// moves it back to the previous values
	if router.target != "AWSGlue.UpdatePartition" {
		t.Fatalf("called %q", router.target)
	}
	if got := router.body["PartitionValueList"]; !reflect.DeepEqual(got, []any{"2025"}) {
		t.Errorf("PartitionValueList = %#v, want [2025]", got)
	}
	input, _ := router.body["PartitionInput"].(map[string]any)
	if got := input["Values"]; !reflect.DeepEqual(got, []any{"2024"}) {
		t.Errorf("PartitionInput.Values = %#v, want [2024]", got)
	}
	if id != "db/events/2024" {
		t.Errorf("physical ID = %q, want the previous one", id)
	}
}
