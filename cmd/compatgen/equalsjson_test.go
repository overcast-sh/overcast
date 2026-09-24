//go:build dev

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `equalsJSON` check: a string member holding a JSON document, compared
// by value (compat/model/README.md § Assertions).

func iamModel(t *testing.T) *serviceModel {
	t.Helper()
	model, err := loadModel(filepath.Join("..", "..", "models", "aws", "shapes"), "iam")
	if err != nil {
		t.Fatal(err)
	}
	return model
}

type equalsJSONFixture struct {
	Comment string `json:"$comment"`
	Decode  []struct {
		Name    string `json:"name"`
		Text    string `json:"text"`
		Decoded string `json:"decoded"`
	} `json:"decode"`
	Holds []struct {
		Name     string `json:"name"`
		Actual   any    `json:"actual"`
		Expected any    `json:"expected"`
	} `json:"holds"`
	Fails []struct {
		Name     string `json:"name"`
		Actual   any    `json:"actual"`
		Expected any    `json:"expected"`
	} `json:"fails"`
	InvalidExpected []struct {
		Name     string `json:"name"`
		Expected any    `json:"expected"`
	} `json:"invalidExpected"`
}

func loadEqualsJSONFixture(t *testing.T) equalsJSONFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "compat", "model", "testdata", "equalsjson", "equalsjson.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f equalsJSONFixture
	if err := decodeStrict(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Decode) == 0 || len(f.Holds) == 0 || len(f.Fails) == 0 || len(f.InvalidExpected) == 0 {
		t.Fatal("the equalsJSON fixture may not be skipped by emptying it")
	}
	return f
}

// The generator accepts every operand the fixture compares with and refuses
// exactly the ones every runtime refuses, through the one structural check a
// scenario, a recipe and an authored file all pass.
func TestValidateEqualsJSON_sharedFixture(t *testing.T) {
	f := loadEqualsJSONFixture(t)
	seen := map[string]bool{}
	for _, c := range append(append([]struct {
		Name     string `json:"name"`
		Actual   any    `json:"actual"`
		Expected any    `json:"expected"`
	}{}, f.Holds...), f.Fails...) {
		if seen[c.Name] {
			t.Errorf("%s: named twice", c.Name)
		}
		seen[c.Name] = true
		if err := validateAssertion(responseField(checks("$.Doc", equalsJSON(c.Expected)))); err != nil {
			t.Errorf("%s: operand refused: %v", c.Name, err)
		}
	}
	for _, c := range f.InvalidExpected {
		clause := responseField(checks("$.Doc", check{EqualsJSON: c.Expected}))
		if err := validateAssertion(clause); err == nil {
			t.Errorf("invalidExpected/%s: accepted", c.Name)
		}
		if err := validateEqualsJSON(c.Expected, "check $.Doc"); err == nil {
			t.Errorf("invalidExpected/%s: validateEqualsJSON accepted it", c.Name)
		}
	}
}

// A check is exactly one kind, and equalsJSON counts as one.
func TestValidateAssertion_equalsJSONIsOneKind(t *testing.T) {
	doc := map[string]any{"a": json.Number("1")}
	if err := validateAssertion(responseField(checks("$.Doc", check{EqualsJSON: doc, NonEmpty: true}))); err == nil {
		t.Fatal("equalsJSON beside nonEmpty in one check was accepted")
	}
	if err := validateAssertion(responseField(checks("$.Doc", check{EqualsJSON: doc, Equals: "x"}))); err == nil {
		t.Fatal("equalsJSON beside equals in one check was accepted")
	}
}

// The check goes on a string member and nowhere else, in a recipe's clause
// and an authored one alike.
func TestCheckEqualsJSONTarget(t *testing.T) {
	iam := iamModel(t)
	output := iam.OutputShape("GetRole")
	for path, ok := range map[string]bool{
		"$.Role.AssumeRolePolicyDocument": true,
		"$.Role.RoleName":                 true, // author-asserted: the model cannot say a string is not a document
		"$.Role.MaxSessionDuration":       false,
		"$.Role":                          false,
		"$.Role.Tags":                     false,
	} {
		target, err := iam.ResolvePath(output, mustPath(path))
		if err != nil {
			t.Fatal(err)
		}
		err = checkEqualsJSONTarget(iam, target, "check "+path)
		if ok != (err == nil) {
			t.Errorf("%s: err = %v, want ok=%t", path, err, ok)
		}
	}
}

func TestCheckAuthoredValues_equalsJSONRule(t *testing.T) {
	iam := iamModel(t)
	doc := map[string]any{"Version": "2012-10-17", "Statement": []any{}}
	withCheck := func(path string, c check) group {
		return group{Name: "iam-roles", Tests: []test{{Name: "GetRole", Op: "GetRole",
			Call:   call{Op: "GetRole", Params: map[string]any{"RoleName": "r"}},
			Assert: []assertion{responseField(checks(path, c))}}}}
	}
	if err := checkAuthoredValues(iam, withCheck("$.Role.AssumeRolePolicyDocument", equalsJSON(doc))); err != nil {
		t.Fatalf("equalsJSON on a string member was refused: %v", err)
	}
	for name, tc := range map[string]struct {
		g    group
		want string
	}{
		"on an integer":  {withCheck("$.Role.MaxSessionDuration", equalsJSON(doc)), "is integer"},
		"on a structure": {withCheck("$.Role", equalsJSON(doc)), "is structure"},
		"with a string operand": {withCheck("$.Role.AssumeRolePolicyDocument", equalsJSON(`{"a":1}`)),
			"not a string"},
		"with an expression inside": {withCheck("$.Role.AssumeRolePolicyDocument",
			equalsJSON(map[string]any{"Statement": []any{map[string]any{"$ref": "x"}}})), "$ref"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkAuthoredValues(iam, tc.g)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A recipe's authored clause is held to the operand grammar before the model
// is consulted.
func TestRecipeValidate_equalsJSONOperand(t *testing.T) {
	a := responseField(checks("$.Role.AssumeRolePolicyDocument", equalsJSON(json.Number("1"))))
	if err := validateAuthoredAssertion(a, "operations[0].assert[0]"); err == nil {
		t.Fatal("a number operand was accepted")
	}
}

// The typed emitters hand their runtime the document as compact JSON text:
// members sorted, numbers as the scenario spells them, nothing HTML-escaped.
func TestEmitters_spellEqualsJSON(t *testing.T) {
	doc := map[string]any{
		"Version":   "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Condition": map[string]any{"NumericLessThan": map[string]any{"n": json.Number("1.50")}}, "Resource": "<&>"}},
	}
	text := `{"Statement":[{"Condition":{"NumericLessThan":{"n":1.50}},"Effect":"Allow","Resource":"<&>"}],"Version":"2012-10-17"}`
	got, err := equalsJSONText(doc)
	if err != nil || got != text {
		t.Fatalf("equalsJSONText = %q, %v; want %q", got, err, text)
	}
	c := equalsJSON(doc)
	path := "$.Role.AssumeRolePolicyDocument"
	want := func(lang, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", lang, got, want)
		}
	}
	quoted := `"{\"Statement\":[{\"Condition\":{\"NumericLessThan\":{\"n\":1.50}},\"Effect\":\"Allow\",\"Resource\":\"<&>\"}],\"Version\":\"2012-10-17\"}"`
	want("go", goCheck(path, c, ""), `scenario.EqualsJSON("$.Role.AssumeRolePolicyDocument", `+quoted+`)`)
	want("java", javaCheck(path, c), `Check.equalsJson("$.Role.AssumeRolePolicyDocument", `+quoted+`)`)
	want("dotnet", dotnetCheck(path, c), `Check.EqualsJson("$.Role.AssumeRolePolicyDocument", `+quoted+`)`)
	rust, err := rustCheck(path, c, "")
	if err != nil {
		t.Fatal(err)
	}
	want("rust", rust, `scenario::equals_json("$.Role.AssumeRolePolicyDocument", `+quoted+`)`)
}

// The pseudo-code rendering names the decode and prints the document, never
// the IR's own spelling.
func TestExplain_rendersEqualsJSON(t *testing.T) {
	e := &explainer{st: pyStyle()}
	e.checkLines("resp", checks("$.Role.AssumeRolePolicyDocument", equalsJSON(map[string]any{"Version": "2012-10-17"})))
	out := strings.Join(e.lines, "\n")
	if !strings.Contains(out, `percent-decoded`) || !strings.Contains(out, `{"Version":"2012-10-17"}`) || strings.Contains(out, "equalsJSON") {
		t.Fatalf("rendering:\n%s", out)
	}
}
