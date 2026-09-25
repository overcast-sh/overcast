package config

import (
	"fmt"
	"strconv"
	"strings"
)

// memoryUnits are the suffixes ParseMemorySize accepts, as `docker run
// --memory` reads them: binary multiples, case-insensitive, with an optional
// trailing "b" or "ib".
var memoryUnits = map[string]int64{
	"": 1, "b": 1,
	"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30,
}

// ParseMemorySize reads a memory size such as "1g", "1536m" or "1073741824".
// An empty value is fallback.
func ParseMemorySize(raw string, fallback int64) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return fallback, nil
	}
	split := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if split < 0 {
		split = len(s)
	}
	n, err := strconv.ParseInt(s[:split], 10, 64)
	unit, known := memoryUnits[strings.TrimSpace(s[split:])]
	if err != nil || !known || n <= 0 {
		return 0, fmt.Errorf("%q is not a memory size (expected a number of bytes, or one with a k, m or g suffix)", raw)
	}
	return n * unit, nil
}
