package s3tables

import (
	"encoding/json"
	"testing"
	"time"
)

func intp(v int) *int { return &v }

func TestBuildInitialMetadata_declaredIDsAddressTheirOwnColumn(t *testing.T) {
	// Given: "a" declares id 2 and "b" declares nothing, so position 2 is taken
	in := &icebergMetadata{
		Schema: &icebergSchema{Fields: []icebergSchemaField{
			{ID: intp(2), Name: "a", Type: "int"},
			{Name: "b", Type: "int"},
		}},
		PartitionSpec: &icebergPartitionSpec{Fields: []icebergPartitionField{{SourceID: 2, Transform: "identity", Name: "a_part"}}},
	}

	// When: the first metadata document is built
	raw, aerr := buildInitialMetadata("uuid", "s3://w--table-s3", time.Unix(0, 0), in)
	if aerr != nil {
		t.Fatalf("buildInitialMetadata: %v", aerr)
	}

	// Then: the partition refers to "a", now column 1, not to "b"
	var doc struct {
		PartitionSpecs []struct {
			Fields []struct {
				SourceID int `json:"source-id"`
			} `json:"fields"`
		} `json:"partition-specs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.PartitionSpecs[0].Fields[0].SourceID; got != 1 {
		t.Errorf("source-id = %d, want 1", got)
	}
}

func TestBuildInitialMetadata_duplicateDeclaredIDIsRefused(t *testing.T) {
	in := &icebergMetadata{Schema: &icebergSchema{Fields: []icebergSchemaField{
		{ID: intp(1), Name: "a", Type: "int"},
		{ID: intp(1), Name: "b", Type: "int"},
	}}}
	if _, aerr := buildInitialMetadata("uuid", "s3://w--table-s3", time.Unix(0, 0), in); aerr == nil || aerr.Code != "BadRequestException" {
		t.Errorf("aerr = %v", aerr)
	}
}
