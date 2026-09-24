package scenario

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The shared $now conformance fixture, compat/model/testdata/now.
//
// $now is the client's clock when a call is made, in epoch milliseconds, plus
// an offset. The emitted source hands this runtime Now(unit, offsetMillis) —
// never the JSON — so the fixture's `invalid` spellings are the generator's to
// refuse (cmd/compatgen's TestNowOf_sharedFixture) and this file runs the rest:
// each valid spelling binds to the fixture's value into the int64 a long
// member is, every invalid argument is refused naming the member, and the clock
// is read once per call however many $nows the call holds.

type nowFixture struct {
	Comment string `json:"$comment"`
	Instant int64  `json:"instant"`
	Valid   []struct {
		Name  string         `json:"name"`
		Now   map[string]any `json:"now"`
		Value int64          `json:"value"`
	} `json:"valid"`
	Invalid          []json.RawMessage `json:"invalid"`
	InvalidArguments []struct {
		Name         string `json:"name"`
		Unit         string `json:"unit"`
		OffsetMillis int64  `json:"offsetMillis"`
	} `json:"invalidArguments"`
	Call struct {
		TickMillis int64           `json:"tickMillis"`
		Params     json.RawMessage `json:"params"`
		Sent       json.RawMessage `json:"sent"`
	} `json:"call"`
}

func loadNowFixture(t *testing.T) nowFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "compat", "model", "testdata", "now", "now.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f nowFixture
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	if len(f.Valid) == 0 || len(f.Invalid) == 0 || len(f.InvalidArguments) == 0 {
		t.Fatal("the $now fixture may not be skipped by emptying it")
	}
	return f
}

// tickingClock moves on by tick every time it is read, from start.
func tickingClock(start, tick int64) func() int64 {
	next := start
	return func() int64 {
		reading := next
		next += tick
		return reading
	}
}

// nowArgs is what cmd/compatgen writes for a $now argument: its unit, and its
// offset or 0.
func nowArgs(t *testing.T, arg map[string]any) (string, int64) {
	t.Helper()
	unit, _ := arg["unit"].(string)
	offset, _ := arg["offsetMillis"].(float64)
	return unit, int64(offset)
}

func TestSharedNowFixture(t *testing.T) {
	f := loadNowFixture(t)
	for _, c := range f.Valid {
		t.Run("valid/"+c.Name, func(t *testing.T) {
			unit, offset := nowArgs(t, c.Now)
			b := &Binder{runID: "oc", group: "g", bag: newContextBag(), clock: tickingClock(f.Instant, f.Call.TickMillis)}
			if got := Bind[int64](b, "timestamp", Now(unit, offset)); b.err != nil || got != c.Value {
				t.Fatalf("Bind[int64](Now(%q, %d)) = %d, %v; want %d", unit, offset, got, b.err, c.Value)
			}
		})
	}
	for _, c := range f.InvalidArguments {
		t.Run("invalidArguments/"+c.Name, func(t *testing.T) {
			b := &Binder{runID: "oc", group: "g", bag: newContextBag(), clock: tickingClock(f.Instant, 0)}
			if got := Bind[int64](b, "timestamp", Now(c.Unit, c.OffsetMillis)); b.err == nil {
				t.Fatalf("Now(%q, %d) bound %d instead of failing", c.Unit, c.OffsetMillis, got)
			}
			if b.member != "timestamp" {
				t.Fatalf("failure names member %q, want timestamp", b.member)
			}
		})
	}
	t.Run("one reading per call", func(t *testing.T) {
		clock := tickingClock(f.Instant, f.Call.TickMillis)
		// The emitted Build body for the fixture's call: one Binder, two
		// events, each timestamp bound in turn.
		b := &Binder{runID: "oc", group: "g", bag: newContextBag(), clock: clock}
		sent := map[string]any{
			"logGroupName": "g",
			"logEvents": []any{
				map[string]any{"message": "first", "timestamp": float64(Bind[int64](b, "logEvents", Now("epochMillis", -1)))},
				map[string]any{"message": "second", "timestamp": float64(Bind[int64](b, "logEvents", Now("epochMillis", 0)))},
			},
		}
		if b.err != nil {
			t.Fatal(b.err)
		}
		var want map[string]any
		if err := json.Unmarshal(f.Call.Sent, &want); err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(sent, want) {
			t.Fatalf("sent %s, want %s", render(sent), f.Call.Sent)
		}
		// The next call builds a fresh Binder, and so reads the clock afresh.
		next := &Binder{runID: "oc", group: "g", bag: newContextBag(), clock: clock}
		if got := Bind[int64](next, "timestamp", Now("epochMillis", 0)); got != f.Instant+f.Call.TickMillis {
			t.Fatalf("the next call read %d, want %d", got, f.Instant+f.Call.TickMillis)
		}
	})
}
