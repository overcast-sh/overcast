package scenario

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// The shared equalsJSON conformance fixture, compat/model/testdata/equalsjson.
//
// This SDK hands an IAM policy document back as the percent-encoded text the
// wire carries, where botocore would have decoded it. The emitted source hands
// this runtime EqualsJSON(path, operandText), so each `invalidExpected`
// operand is given here as its JSON text, exactly as cmd/compatgen would write
// it. What this suite owes the fixture is that percentDecode is Python's
// unquote case for case, that the check holds and fails exactly where the
// fixture says whichever form the actual value takes, and that EqualsJSON
// refuses every operand the fixture calls invalid.

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

// decodeCase reads one holds/fails case: the actual side in the document form
// toDocument produces, and the operand through EqualsJSON from its JSON text,
// so a case EqualsJSON would refuse cannot pass here.
func decodeCase(t *testing.T, c equalsJSONCase) (actual any, check Check) {
	t.Helper()
	if c.Actual == nil || c.Expected == nil {
		t.Fatalf("case %q needs both actual and expected", c.Name)
	}
	if err := json.Unmarshal(c.Actual, &actual); err != nil {
		t.Fatal(err)
	}
	check = EqualsJSON("$.PolicyDocument", string(c.Expected))
	if check.operandErr != nil {
		t.Fatalf("EqualsJSON refused the operand: %v", check.operandErr)
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
			if check := EqualsJSON("$.PolicyDocument", string(c.Expected)); check.operandErr == nil {
				t.Fatalf("EqualsJSON accepted %s", c.Expected)
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

// TestEqualsJSON_throughARealResponse runs the check the way an emitted group
// does, against the IAM output struct whose PolicyDocument this SDK leaves
// percent-encoded.
func TestEqualsJSON_throughARealResponse(t *testing.T) {
	out := &iam.GetRolePolicyOutput{
		PolicyDocument: aws.String("%7B%22Version%22%3A%222012-10-17%22%2C%22Statement%22%3A%5B%5D%7D"),
		RoleName:       aws.String("r"),
	}
	const policy = `{"Statement":[],"Version":"2012-10-17"}`
	for _, tc := range []struct {
		name  string
		check Check
		want  []string // substrings of the failure; nil means the check holds
	}{
		{name: "holds on the wire form", check: EqualsJSON("$.PolicyDocument", policy)},
		{name: "fails naming the decoded document", check: EqualsJSON("$.PolicyDocument", `{"Version":"2008-10-17","Statement":[]}`),
			want: []string{
				"responseField equalsJSON at $.PolicyDocument",
				`expected equalsJSON {"Statement":[],"Version":"2008-10-17"}, actual document {"Statement":[],"Version":"2012-10-17"}`,
			}},
		{name: "fails on a string that is not a document", check: EqualsJSON("$.RoleName", policy),
			want: []string{`actual not a JSON document: "r"`}},
		{name: "fails when the path does not resolve", check: EqualsJSON("$.Nope", policy),
			want: []string{"actual " + missingValue}},
		{name: "a refused operand fails the check naming why", check: EqualsJSON("$.PolicyDocument", `{"$ref":"role.policy"}`),
			want: []string{"expected a JSON object or array operand in the generated source", `its key \"$ref\" may not start with $`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runChecks(t, out, tc.check)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want a pass, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want a failure, got a pass")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("failure lacks %q:\n%s", w, err.Error())
				}
			}
		})
	}
}
