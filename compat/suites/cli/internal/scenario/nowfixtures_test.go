package scenario

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The shared $now conformance fixture, compat/model/testdata/now.
//
// $now is the client's clock when a call is made, in epoch milliseconds, plus
// an offset, and the cli backend hands the number straight to
// --cli-input-json, which reads a long member as one. What this suite owes the
// fixture is that each valid spelling evaluates to the fixture's value with
// the clock pinned to its instant, each invalid one is refused before the CLI
// sees it, the clock is read once per call however many $nows the params hold,
// and a $now is never evaluated outside a call's params.

type nowFixture struct {
	Comment string `json:"$comment"`
	Instant int64  `json:"instant"`
	Valid   []struct {
		Name  string  `json:"name"`
		Now   any     `json:"now"`
		Value float64 `json:"value"`
	} `json:"valid"`
	Invalid []struct {
		Name string `json:"name"`
		Now  any    `json:"now"`
	} `json:"invalid"`
	InvalidArguments []struct {
		Name         string `json:"name"`
		Unit         string `json:"unit"`
		OffsetMillis int64  `json:"offsetMillis"`
	} `json:"invalidArguments"`
	Call struct {
		TickMillis int64          `json:"tickMillis"`
		Params     map[string]any `json:"params"`
		Sent       map[string]any `json:"sent"`
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

func TestSharedNowFixture(t *testing.T) {
	f := loadNowFixture(t)
	type obj = map[string]any
	pinned := func() *evaluator {
		e := testEvaluator(nil)
		now := f.Instant
		e.now = &now
		return e
	}
	for _, c := range f.Valid {
		t.Run("valid/"+c.Name, func(t *testing.T) {
			got, err := pinned().eval(obj{"$now": c.Now})
			if err != nil || got != c.Value {
				t.Fatalf("eval = %v, %v; want %v", got, err, c.Value)
			}
		})
	}
	for _, c := range f.Invalid {
		t.Run("invalid/"+c.Name, func(t *testing.T) {
			if got, err := pinned().eval(obj{"$now": c.Now}); err == nil {
				t.Fatalf("eval accepted it as %v", got)
			}
		})
	}
	for _, c := range f.InvalidArguments {
		t.Run("invalidArguments/"+c.Name, func(t *testing.T) {
			if _, err := checkNowArguments(c.Unit, c.OffsetMillis); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("one reading per call", func(t *testing.T) {
		e := testEvaluator(nil)
		e.clock = tickingClock(f.Instant, f.Call.TickMillis)
		sent, err := e.evalParams(f.Call.Params)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(sent, f.Call.Sent) {
			t.Fatalf("sent %s, want %s", render(sent), render(f.Call.Sent))
		}
		if e.now != nil {
			t.Fatal("evalParams left its reading on the shared evaluator")
		}
	})
	t.Run("never an expected value", func(t *testing.T) {
		e := testEvaluator(nil)
		e.clock = tickingClock(f.Instant, f.Call.TickMillis)
		if got, err := e.eval(obj{"$now": obj{"unit": "epochMillis"}}); err == nil {
			t.Fatalf("a $now outside a call's params evaluated to %v", got)
		}
	})
}
