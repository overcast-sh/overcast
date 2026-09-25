package scenario

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The shared equalsJSON conformance fixture, compat/model/testdata/equalsjson.
//
// The cli sees an IAM policy document as an object, because botocore decodes
// it before the CLI prints it, but the check has to answer the same for the
// wire's percent-encoded text as every other backend does. What this suite
// owes the fixture is that percentDecode is Python's unquote case for case,
// that the check holds and fails exactly where the fixture says whichever
// form the actual value takes, and that the loader refuses every operand the
// fixture calls invalid.

type equalsJSONCase struct {
	Name     string          `json:"name"`
	Actual   json.RawMessage `json:"actual"`
	Expected json.RawMessage `json:"expected"`
}

type equalsJSONFixture struct {
	Comment string `json:"$comment"`
	Decode  []struct {
		Name    string `json:"name"`
		Text    string `json:"text"`
		Decoded string `json:"decoded"`
	} `json:"decode"`
	Holds           []equalsJSONCase `json:"holds"`
	Fails           []equalsJSONCase `json:"fails"`
	InvalidExpected []struct {
		Name     string          `json:"name"`
		Expected json.RawMessage `json:"expected"`
	} `json:"invalidExpected"`
}

func loadEqualsJSONFixture(t *testing.T) equalsJSONFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "compat", "model", "testdata", "equalsjson", "equalsjson.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f equalsJSONFixture
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	if len(f.Decode) == 0 || len(f.Holds) == 0 || len(f.Fails) == 0 || len(f.InvalidExpected) == 0 {
		t.Fatal("the equalsJSON fixture may not be skipped by emptying it")
	}
	return f
}

// decodeCase reads one holds/fails case's two sides as the loader and
// internal/awscli would have handed them over: decoded by encoding/json, the
// operand through the Check decoder so a case the loader would refuse cannot
// pass here.
func decodeCase(t *testing.T, c equalsJSONCase) (actual any, check Check) {
	t.Helper()
	if c.Actual == nil || c.Expected == nil {
		t.Fatalf("case %q needs both actual and expected", c.Name)
	}
	if err := json.Unmarshal(c.Actual, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"equalsJSON":`+string(c.Expected)+`}`), &check); err != nil {
		t.Fatalf("the loader refused the operand: %v", err)
	}
	return actual, check
}

func TestSharedEqualsJSONFixture(t *testing.T) {
	f := loadEqualsJSONFixture(t)
	for _, c := range f.Decode {
		t.Run("decode/"+c.Name, func(t *testing.T) {
			if got := percentDecode(c.Text); got != c.Decoded {
				t.Fatalf("percentDecode(%q) = %q, want %q", c.Text, got, c.Decoded)
			}
		})
	}
	for _, c := range f.Holds {
		t.Run("holds/"+c.Name, func(t *testing.T) {
			actual, check := decodeCase(t, c)
			if holds, got := equalsJSON(actual, true, check.Value); !holds {
				t.Fatalf("failed with actual %s", got)
			}
		})
	}
	for _, c := range f.Fails {
		t.Run("fails/"+c.Name, func(t *testing.T) {
			actual, check := decodeCase(t, c)
			if holds, _ := equalsJSON(actual, true, check.Value); holds {
				t.Fatal("held")
			}
		})
	}
	for _, c := range f.InvalidExpected {
		t.Run("invalidExpected/"+c.Name, func(t *testing.T) {
			if c.Expected == nil {
				t.Fatal("the case has no expected")
			}
			var check Check
			if err := json.Unmarshal([]byte(`{"equalsJSON":`+string(c.Expected)+`}`), &check); err == nil {
				t.Fatalf("the loader accepted %s", c.Expected)
			}
		})
	}
	t.Run("failure message", func(t *testing.T) {
		byName := map[string]equalsJSONCase{}
		for _, c := range f.Fails {
			byName[c.Name] = c
		}
		for name, want := range map[string]string{
			"extra-member":  `document {"a":1,"b":2}`,
			"not-json":      `not a JSON document: "not a policy"`,
			"number-actual": "not a JSON document: 5",
		} {
			c, ok := byName[name]
			if !ok {
				t.Fatalf("the fixture has no fails case %q", name)
			}
			actual, check := decodeCase(t, c)
			if _, got := equalsJSON(actual, true, check.Value); got != want {
				t.Errorf("%s: actual renders %s, want %s", name, got, want)
			}
		}
		if _, got := equalsJSON(nil, false, map[string]any{}); got != missingValue {
			t.Errorf("an unresolved path renders %s, want %s", got, missingValue)
		}
	})
}
