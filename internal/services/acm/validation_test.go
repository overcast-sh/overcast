package acm

import (
	"strings"
	"testing"
)

// TestDomainNamePattern_matchesTheModelledLanguage is the check that keeps the
// RE2 rewrite in validation.go honest against the pattern it replaces.
// domainNameConstraint uses negative lookaheads, which Go's regexp engine does
// not implement, so the two cannot be compared mechanically — this table is
// the comparison, written out at the boundaries where a careless rewrite goes
// wrong: label length, hyphen position, and wildcard placement.
func TestDomainNamePattern_matchesTheModelledLanguage(t *testing.T) {
	label63 := strings.Repeat("a", 63)

	cases := []struct {
		domain string
		valid  bool
	}{
		// Given: names the pattern accepts
		{"example.com", true},
		{"www.example.com", true},
		{"a.b.c.example.co.uk", true},
		{"*.example.com", true},
		{"1.example.com", true},
		{"xn--80ak6aa92e.com", true},
		{"my-site.example.com", true},
		{label63 + ".example.com", true},
		{"example." + label63, true},

		// Given: names it rejects
		{"", false},
		{"example", false},              // no dot: one label is not enough
		{"example.c", false},            // the final label needs 2 characters
		{"*", false},                    // a wildcard is not a name
		{"*.com", false},                // and still needs two labels after it
		{"*example.com", false},         // the wildcard is a whole label
		{"www.*.example.com", false},    // and only the leftmost one
		{"bad_domain.com", false},       // underscore is not in the alphabet
		{"-leading.example.com", false}, // a label may not start with a hyphen
		{"trailing-.example.com", false},
		{"example..com", false},
		{"example.com.", false},
		{"exa mple.com", false},
		{"example.-com", false},
		{label63 + "a.example.com", false}, // 64-character label
	}

	for _, tc := range cases {
		// When: the compiled pattern is applied
		got := domainNamePattern.MatchString(tc.domain)

		// Then: it agrees with the modelled language
		if got != tc.valid {
			t.Errorf("domainNamePattern.MatchString(%q) = %v, want %v", tc.domain, got, tc.valid)
		}
	}
}

// TestValidateDomainName_namesTheMemberAndTheConstraint pins the message form
// AWS's front-end validator produces, including the member path and the
// verbatim model pattern a caller is told to satisfy.
func TestValidateDomainName_namesTheMemberAndTheConstraint(t *testing.T) {
	// Given: a malformed domain on a list member
	// When: it is validated
	aerr := validateDomainName("bad_domain", "subjectAlternativeNames.2.member")

	// Then: ValidationException, naming the member and echoing the pattern
	if aerr == nil {
		t.Fatal("expected a validation error")
	}
	if aerr.Code != "ValidationException" {
		t.Errorf("Code = %q, want ValidationException", aerr.Code)
	}
	want := "1 validation error detected: Value 'bad_domain' at 'subjectAlternativeNames.2.member' " +
		"failed to satisfy constraint: Member must satisfy regular expression pattern: " + domainNameConstraint
	if aerr.Message != want {
		t.Errorf("Message =\n %q\nwant\n %q", aerr.Message, want)
	}
}

// TestValidateDomainName_lengthBeforePattern pins that a 254-character name
// that is otherwise well formed reports the length trait, not the pattern.
func TestValidateDomainName_lengthBeforePattern(t *testing.T) {
	// Given: a well-formed name one character over DomainNameString's maximum
	long := strings.Repeat(strings.Repeat("a", 49)+".", 5) + "acom"
	if len(long) != 254 {
		t.Fatalf("test setup: %d characters, want 254", len(long))
	}

	// When: it is validated
	aerr := validateDomainName(long, "domainName")

	// Then: the length bound is what the caller is told about
	if aerr == nil {
		t.Fatal("expected a validation error")
	}
	if !strings.Contains(aerr.Message, "Member must have length less than or equal to 253") {
		t.Errorf("Message = %q, want the length constraint", aerr.Message)
	}
}
