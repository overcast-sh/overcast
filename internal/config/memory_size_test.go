package config

import "testing"

func TestParseMemorySize(t *testing.T) {
	cases := map[string]int64{
		"":           42,
		"1073741824": 1 << 30,
		"1g":         1 << 30,
		"1536m":      1536 << 20,
		"2GiB":       2 << 30,
		" 512 MB ":   512 << 20,
		"64k":        64 << 10,
	}
	for in, want := range cases {
		// When: a size is parsed
		got, err := ParseMemorySize(in, 42)
		// Then: it reads as the bytes it names
		if err != nil || got != want {
			t.Errorf("ParseMemorySize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"g", "1t", "-1g", "0", "one", "99999999999g"} {
		if _, err := ParseMemorySize(in, 42); err == nil {
			t.Errorf("ParseMemorySize(%q) accepted a value that is not a size", in)
		}
	}
}
