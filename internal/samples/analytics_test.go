package samples

import (
	"bytes"
	"strings"
	"testing"
)

func TestAnalyticsDataset_isDeterministic(t *testing.T) {
	a, b := analyticsDataset(), analyticsDataset()
	if len(a.Objects) != 4 || len(a.Objects) != len(b.Objects) {
		t.Fatalf("objects = %d and %d, want 4", len(a.Objects), len(b.Objects))
	}
	for i := range a.Objects {
		if a.Objects[i].Key != b.Objects[i].Key || !bytes.Equal(a.Objects[i].Body, b.Objects[i].Body) {
			t.Fatalf("object %s differs between two builds", a.Objects[i].Key)
		}
	}
}

func TestAnalyticsDataset_csvIsHivePartitionedByRegion(t *testing.T) {
	ds := analyticsDataset()
	rows := 0
	for _, o := range ds.Objects[:3] {
		if !strings.HasPrefix(o.Key, "csv/orders/region=") {
			t.Fatalf("key = %s", o.Key)
		}
		lines := strings.Split(strings.TrimSpace(string(o.Body)), "\n")
		if lines[0] != "order_id,order_date,customer_id,product,quantity,unit_price" {
			t.Fatalf("header = %q", lines[0])
		}
		rows += len(lines) - 1
	}
	if rows != analyticsOrders {
		t.Fatalf("CSV rows = %d, want %d", rows, analyticsOrders)
	}
	first := strings.Split(string(ds.Objects[0].Body), "\n")[1]
	if first != "1001,2026-01-01,c-001,keyboard,1,49" {
		t.Fatalf("first us row = %q", first)
	}
}

func TestAnalyticsDataset_parquetHoldsEveryOrder(t *testing.T) {
	ds := analyticsDataset()
	meta := footer(t, ds.Objects[3].Body)
	schema := meta[2].([]any)
	if meta[3] != int64(analyticsOrders) || len(schema) != 8 {
		t.Fatalf("rows = %v, schema = %v", meta[3], schema)
	}
	if date := schema[2].(map[int]any); date[4] != "order_date" || date[6] != int64(convertedDate) {
		t.Fatalf("order_date = %v", date)
	}
}
