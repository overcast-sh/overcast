package dynamodb

import "testing"

// Each case is worked by hand from the rules in item_size.go's header.
func TestItemSizeBytes(t *testing.T) {
	cases := []struct {
		name string
		item Item
		want int
	}{
		{"string", Item{"pk": {"S": "abc"}}, 2 + 3},
		{"utf8 name and value", Item{"ключ": {"S": "é"}}, 8 + 2},
		{"number two digits per byte", Item{"n": {"N": "1234"}}, 1 + 2 + 1},
		{"number odd digits", Item{"n": {"N": "12345"}}, 1 + 3 + 1},
		{"number trims zeroes sign and point", Item{"n": {"N": "-0100.00"}}, 1 + 1 + 1},
		{"number zero", Item{"n": {"N": "0"}}, 1 + 1},
		{"number exponent", Item{"n": {"N": "1.5E+10"}}, 1 + 1 + 1},
		{"binary raw bytes", Item{"b": {"B": "aGVsbG8="}}, 1 + 5},
		{"bool and null", Item{"t": {"BOOL": true}, "z": {"NULL": true}}, 1 + 1 + 1 + 1},
		{"string set", Item{"ss": {"SS": []any{"ab", "cde"}}}, 2 + 2 + 3},
		{"number set", Item{"ns": {"NS": []any{"1", "22"}}}, 2 + 2 + 2},
		{"empty list", Item{"l": {"L": []any{}}}, 1 + 3},
		{"list", Item{"l": {"L": []any{map[string]any{"S": "ab"}, map[string]any{"N": "7"}}}}, 1 + 3 + (1 + 2) + (1 + 2)},
		{"map", Item{"m": {"M": map[string]any{"k": map[string]any{"S": "v"}}}}, 1 + 3 + (1 + 1 + 1)},
		{"nested map in list", Item{"l": {"L": []any{map[string]any{"M": map[string]any{"a": map[string]any{"BOOL": false}}}}}}, 1 + 3 + 1 + (3 + 1 + 1 + 1)},
		{"malformed value counts nothing", Item{"x": {}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := itemSizeBytes(tc.item); got != tc.want {
				t.Errorf("itemSizeBytes = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestItemSizeBytes_limitBoundary(t *testing.T) {
	// Given: an item whose accounted size is exactly the ceiling
	blob := make([]byte, maxItemSizeBytes-len("pk")-len("k")-len("blob"))
	for i := range blob {
		blob[i] = 'x'
	}
	item := Item{"pk": {"S": "k"}, "blob": {"S": string(blob)}}

	// Then: it sits on the limit, and one more byte crosses it
	if got := itemSizeBytes(item); got != maxItemSizeBytes {
		t.Fatalf("itemSizeBytes = %d, want %d", got, maxItemSizeBytes)
	}
	item["blob"] = attrValue{"S": string(blob) + "x"}
	if got := itemSizeBytes(item); got <= maxItemSizeBytes {
		t.Errorf("itemSizeBytes = %d, want > %d", got, maxItemSizeBytes)
	}
}
