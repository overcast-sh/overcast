//go:build dev

package main

import (
	"strings"
	"testing"
)

// The affix checks take a value expression and hold it to "evaluates to a
// non-empty string" in the generator, so no backend is left to fail on it
// differently at run time.
func TestCheckAffixOperand(t *testing.T) {
	exports := exportKinds{"plan.id": "string", "plan.mode": "enum", "plan.count": "integer", "plan.items": "list"}
	for name, tc := range map[string]struct {
		operand any
		strict  bool
		want    string // empty means accepted
	}{
		"a literal":                {operand: ":backup-vault:"},
		"a $lit string":            {operand: map[string]any{"$lit": "x"}},
		"a $name":                  {operand: map[string]any{"$name": "vault"}},
		"a $ref to a string":       {operand: map[string]any{"$ref": "plan.id"}, strict: true},
		"a $ref to an enum":        {operand: map[string]any{"$ref": "plan.mode"}, strict: true},
		"a $concat of both":        {operand: map[string]any{"$concat": []any{":backup-plan:", map[string]any{"$ref": "plan.id"}}}, strict: true},
		"a $concat with an empty":  {operand: map[string]any{"$concat": []any{"", map[string]any{"$name": "v"}}}},
		"an $index, checked later": {operand: map[string]any{"$index": []any{map[string]any{"$ref": "plan.items"}, 0}}},

		"an empty literal":      {operand: "", want: "asserts nothing"},
		"an empty $lit":         {operand: map[string]any{"$lit": ""}, want: "asserts nothing"},
		"a number":              {operand: float64(7), want: "got a number"},
		"a boolean":             {operand: true, want: "got a boolean"},
		"an object":             {operand: map[string]any{"a": "b"}, want: "got an object"},
		"a list":                {operand: []any{"a"}, want: "got a list"},
		"a $lit number":         {operand: map[string]any{"$lit": float64(7)}, want: "its $lit is a number"},
		"a $ref to an integer":  {operand: map[string]any{"$ref": "plan.count"}, want: "plan.count is a integer"},
		"a $ref to a list":      {operand: map[string]any{"$ref": "plan.items"}, want: "plan.items is a list"},
		"an unexported $ref":    {operand: map[string]any{"$ref": "nope.id"}, strict: true, want: "not exported"},
		"a $now":                {operand: map[string]any{"$now": map[string]any{"unit": "epochMillis"}}, want: "$now does not evaluate"},
		"a $base64":             {operand: map[string]any{"$base64": "AAAA"}, want: "$base64 does not evaluate"},
		"a bad part in $concat": {operand: map[string]any{"$concat": []any{"a", map[string]any{"$ref": "plan.count"}}}, want: "$concat[1]"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkAffixOperand("endsWith", tc.operand, exports, tc.strict, "check $.Arn", "g")
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("accepted, want a refusal mentioning %q", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A check carries exactly one kind, so an affix beside equals is malformed.
func TestValidateAssertion_affixIsExclusive(t *testing.T) {
	ok := responseField(checks("$.Arn", endsWith(map[string]any{"$name": "v"})))
	if err := validateAssertion(ok); err != nil {
		t.Fatalf("a lone endsWith was refused: %v", err)
	}
	both := responseField(checks("$.Arn", check{EndsWith: "a", StartsWith: "b"}))
	if err := validateAssertion(both); err == nil {
		t.Fatal("endsWith beside startsWith in one check was accepted")
	}
	three := responseField(checks("$.Arn", check{EndsWith: "a", Contains: "b"}))
	if err := validateAssertion(three); err == nil {
		t.Fatal("endsWith beside contains in one check was accepted")
	}
	mixed := responseField(checks("$.Arn", check{StartsWith: "a", Equals: "b"}))
	if err := validateAssertion(mixed); err == nil {
		t.Fatal("startsWith beside equals in one check was accepted")
	}
}

// In an authored scenario the check goes on a string member and nowhere else,
// and its operand is held to the same rule.
func TestCheckAuthoredValues_affixRule(t *testing.T) {
	iam := iamModel(t)
	withCheck := func(path string, c check) group {
		return group{Name: "iam-roles", Tests: []test{{Name: "GetRole", Op: "GetRole",
			Call:   call{Op: "GetRole", Params: map[string]any{"RoleName": "r"}},
			Assert: []assertion{responseField(checks(path, c))}}}}
	}
	arnTail := map[string]any{"$concat": []any{":role/", map[string]any{"$name": "r"}}}
	if err := checkAuthoredValues(iam, withCheck("$.Role.Arn", endsWith(arnTail))); err != nil {
		t.Fatalf("endsWith on a string member was refused: %v", err)
	}
	if err := checkAuthoredValues(iam, withCheck("$.Role.Arn", startsWith("arn:"))); err != nil {
		t.Fatalf("startsWith on a string member was refused: %v", err)
	}
	if err := checkAuthoredValues(iam, withCheck("$.Role.Arn", contains(":role/"))); err != nil {
		t.Fatalf("contains on a string member was refused: %v", err)
	}
	for name, tc := range map[string]struct {
		g    group
		want string
	}{
		"on an integer":   {withCheck("$.Role.MaxSessionDuration", endsWith("0")), "compares text"},
		"contains on one": {withCheck("$.Role.MaxSessionDuration", contains("0")), "compares text"},
		"on a structure":  {withCheck("$.Role", startsWith("a")), "compares text"},
		"an empty":        {withCheck("$.Role.Arn", endsWith("")), "asserts nothing"},
		"a number":        {withCheck("$.Role.Arn", endsWith(float64(1))), "got a number"},
		"a $now operand":  {withCheck("$.Role.Arn", endsWith(map[string]any{"$now": map[string]any{"unit": "epochMillis"}})), "$now"},
		"a blob operand":  {withCheck("$.Role.Arn", startsWith(map[string]any{"$base64": "AAAA"})), "$base64"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkAuthoredValues(iam, tc.g)
			if tc.want == "" {
				return // an unexported $ref is the loader's ordering check, not this one
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A recipe's authored clause is held to the operand rule before the model is
// consulted.
func TestRecipeValidate_affixOperand(t *testing.T) {
	good := responseField(checks("$.Arn", endsWith(map[string]any{"$name": "v"})))
	if err := validateAuthoredAssertion(good, "operations[0].assert[0]"); err != nil {
		t.Fatalf("a $name operand was refused: %v", err)
	}
	for name, c := range map[string]check{
		"an empty operand":      endsWith(""),
		"a number operand":      startsWith(float64(3)),
		"an unknown expression": endsWith(map[string]any{"$nope": "x"}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateAuthoredAssertion(responseField(checks("$.Arn", c)), "operations[0].assert[0]"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// Every typed emitter hands its runtime the operand as the value expression it
// builds for an equals operand, under a name of its own.
func TestEmitters_spellAffixChecks(t *testing.T) {
	operand := map[string]any{"$concat": []any{":backup-vault:", map[string]any{"$name": "vault"}}}
	path := "$.BackupVaultArn"
	want := func(lang, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", lang, got, want)
		}
	}
	ends, starts := endsWith(operand), startsWith("arn:")
	want("go", goCheck(path, ends, ""), `scenario.EndsWith("$.BackupVaultArn", scenario.Concat(":backup-vault:", scenario.Name("vault")))`)
	want("go", goCheck(path, starts, ""), `scenario.StartsWith("$.BackupVaultArn", "arn:")`)
	want("go", goCheck(path, contains(":vault:"), ""), `scenario.Contains("$.BackupVaultArn", ":vault:")`)
	want("java", javaCheck(path, contains(":vault:")), `Check.contains("$.BackupVaultArn", ":vault:")`)
	want("dotnet", dotnetCheck(path, contains(":vault:")), `Check.Contains("$.BackupVaultArn", ":vault:")`)
	rustContains, err := rustCheck(path, contains(":vault:"), "")
	if err != nil {
		t.Fatal(err)
	}
	want("rust", rustContains, `scenario::contains("$.BackupVaultArn", scenario::lit(::serde_json::json!(":vault:")))`)
	want("java", javaCheck(path, ends), `Check.endsWith("$.BackupVaultArn", Values.concat(":backup-vault:", Values.name("vault")))`)
	want("java", javaCheck(path, starts), `Check.startsWith("$.BackupVaultArn", "arn:")`)
	want("dotnet", dotnetCheck(path, ends), `Check.EndsWith("$.BackupVaultArn", Val.Concat(":backup-vault:", Val.Name("vault")))`)
	want("dotnet", dotnetCheck(path, starts), `Check.StartsWith("$.BackupVaultArn", "arn:")`)
	rustEnds, err := rustCheck(path, ends, "")
	if err != nil {
		t.Fatal(err)
	}
	want("rust", rustEnds, "scenario::ends_with(\"$.BackupVaultArn\", scenario::concat(vec![\n"+
		"    scenario::lit(::serde_json::json!(\":backup-vault:\")),\n"+
		"    scenario::name(\"vault\"),\n"+
		"]))")
	rustStarts, err := rustCheck(path, starts, "")
	if err != nil {
		t.Fatal(err)
	}
	want("rust", rustStarts, `scenario::starts_with("$.BackupVaultArn", scenario::lit(::serde_json::json!("arn:")))`)
}
