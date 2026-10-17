//go:build dev

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// present is the exact negation of missing: it holds when the path resolves,
// whatever the value. The generator holds it to the one-check-per-path rule
// like every other check, so a clause cannot say two things about one member.
func TestValidate_presentIsAloneOnItsPath(t *testing.T) {
	if err := validateAssertion(responseField(checks("$.BackupVaultList", present()))); err != nil {
		t.Fatalf("a present check on its own is refused: %v", err)
	}
	both := check{Present: true, IsList: true}
	err := validateAssertion(responseField(map[string]check{"$.BackupVaultList": both}))
	if err == nil || !strings.Contains(err.Error(), "exactly one of") || !strings.Contains(err.Error(), "present") {
		t.Fatalf("present + isList on one path: err = %v, want the exactly-one refusal naming present", err)
	}
	both = check{Present: true, Missing: true}
	if err := validateAssertion(responseField(map[string]check{"$.X": both})); err == nil {
		t.Fatal("present + missing on one path was accepted")
	}
}

// An authored scenario spells it {"present": true}; the zero check is not a
// check, so {"present": false} is refused as no check at all.
func TestPresent_decodesFromTheIR(t *testing.T) {
	var c check
	if err := json.Unmarshal([]byte(`{"present": true}`), &c); err != nil || !c.Present {
		t.Fatalf("decode {present:true} = %+v, %v", c, err)
	}
	var off check
	if err := json.Unmarshal([]byte(`{"present": false}`), &off); err != nil {
		t.Fatal(err)
	}
	if err := validateAssertion(responseField(map[string]check{"$.X": off})); err == nil {
		t.Fatal("{present:false} was accepted as a check")
	}
	out, err := json.Marshal(present())
	if err != nil || string(out) != `{"present":true}` {
		t.Fatalf("encode = %s, %v", out, err)
	}
}

// Every typed emitter hands its runtime a present check under a name of its
// own, mirroring missing.
func TestEmitters_spellPresent(t *testing.T) {
	path := "$.BackupVaultList"
	rust, err := rustCheck(path, present(), "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"go":     goCheck(path, present(), ""),
		"java":   javaCheck(path, present()),
		"dotnet": dotnetCheck(path, present()),
		"rust":   rust,
	}
	want := map[string]string{
		"go":     `scenario.Present("$.BackupVaultList")`,
		"java":   `Check.present("$.BackupVaultList")`,
		"dotnet": `Check.Present("$.BackupVaultList")`,
		"rust":   `scenario::present("$.BackupVaultList")`,
	}
	for lang, w := range want {
		if got[lang] != w {
			t.Errorf("%s: got %s, want %s", lang, got[lang], w)
		}
	}
}
