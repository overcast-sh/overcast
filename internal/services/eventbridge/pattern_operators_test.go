package eventbridge

import (
	"encoding/json"
	"testing"
)

// operatorEvent is the event every content-filter case below is evaluated
// against. It carries a string, two numbers, a boolean and a null so each
// operator can be exercised against the JSON types it is defined for.
const operatorEvent = `{
	"source": "com.example.orders",
	"detail-type": "Order Created",
	"detail": {
		"state": "initializing",
		"file": "report.PNG",
		"amount": 100,
		"count": 3,
		"paid": false,
		"note": null
	}
}`

func mustEvent(t *testing.T, raw string) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("event fixture is not JSON: %v", err)
	}
	return event
}

func TestEventPatternOperators(t *testing.T) {
	// Given: an event with string, numeric, boolean and null leaves.
	event := mustEvent(t, operatorEvent)

	cases := []struct {
		name    string
		pattern string
		want    bool
	}{
		// prefix
		{"prefix matches", `{"source":[{"prefix":"com.example."}]}`, true},
		{"prefix does not match", `{"source":[{"prefix":"com.other."}]}`, false},
		{"prefix is case sensitive", `{"source":[{"prefix":"COM.EXAMPLE."}]}`, false},
		{"prefix ignoring case", `{"source":[{"prefix":{"equals-ignore-case":"COM.Example."}}]}`, true},
		{"prefix against a non-string leaf", `{"detail":{"amount":[{"prefix":"1"}]}}`, false},

		// suffix
		{"suffix matches", `{"detail":{"file":[{"suffix":".PNG"}]}}`, true},
		{"suffix is case sensitive", `{"detail":{"file":[{"suffix":".png"}]}}`, false},
		{"suffix ignoring case", `{"detail":{"file":[{"suffix":{"equals-ignore-case":".png"}}]}}`, true},

		// equals-ignore-case
		{"equals-ignore-case matches", `{"detail-type":[{"equals-ignore-case":"order created"}]}`, true},
		{"equals-ignore-case does not match", `{"detail-type":[{"equals-ignore-case":"order shipped"}]}`, false},

		// exists
		{"exists true on a present leaf", `{"detail":{"state":[{"exists":true}]}}`, true},
		{"exists true on an absent leaf", `{"detail":{"missing":[{"exists":true}]}}`, false},
		{"exists false on an absent leaf", `{"detail":{"missing":[{"exists":false}]}}`, true},
		{"exists false on a present leaf", `{"detail":{"state":[{"exists":false}]}}`, false},
		{"exists true on a null leaf", `{"detail":{"note":[{"exists":true}]}}`, true},
		{"exists true on an intermediate node", `{"detail":[{"exists":true}]}`, false},
		{"exists false on an intermediate node", `{"detail":[{"exists":false}]}`, false},

		// numeric
		{"numeric equals", `{"detail":{"amount":[{"numeric":["=",100]}]}}`, true},
		{"numeric not equals", `{"detail":{"amount":[{"numeric":["!=",100]}]}}`, false},
		{"numeric less than", `{"detail":{"count":[{"numeric":["<",10]}]}}`, true},
		{"numeric less than or equal", `{"detail":{"count":[{"numeric":["<=",3]}]}}`, true},
		{"numeric greater than", `{"detail":{"count":[{"numeric":[">",3]}]}}`, false},
		{"numeric greater than or equal", `{"detail":{"count":[{"numeric":[">=",3]}]}}`, true},
		{"numeric range matches", `{"detail":{"count":[{"numeric":[">",0,"<=",5]}]}}`, true},
		{"numeric range misses", `{"detail":{"count":[{"numeric":[">",3,"<=",5]}]}}`, false},
		{"numeric against a string leaf", `{"detail":{"state":[{"numeric":["=",100]}]}}`, false},

		// anything-but
		{"anything-but a single value", `{"detail":{"state":[{"anything-but":"initializing"}]}}`, false},
		{"anything-but another value", `{"detail":{"state":[{"anything-but":"stopped"}]}}`, true},
		{"anything-but a list containing the value", `{"detail":{"state":[{"anything-but":["stopped","initializing"]}]}}`, false},
		{"anything-but a list missing the value", `{"detail":{"state":[{"anything-but":["stopped","overloaded"]}]}}`, true},
		{"anything-but a number list containing the value", `{"detail":{"amount":[{"anything-but":[100,200]}]}}`, false},
		{"anything-but a number list missing the value", `{"detail":{"amount":[{"anything-but":[200,300]}]}}`, true},
		{"anything-but a matching prefix", `{"detail":{"state":[{"anything-but":{"prefix":"init"}}]}}`, false},
		{"anything-but a non-matching prefix", `{"detail":{"state":[{"anything-but":{"prefix":"stop"}}]}}`, true},
		{"anything-but a prefix list", `{"detail":{"state":[{"anything-but":{"prefix":["stop","init"]}}]}}`, false},
		{"anything-but a matching suffix", `{"detail":{"file":[{"anything-but":{"suffix":".PNG"}}]}}`, false},
		{"anything-but a suffix list", `{"detail":{"file":[{"anything-but":{"suffix":[".txt",".rtf"]}}]}}`, true},
		{"anything-but ignoring case", `{"detail":{"state":[{"anything-but":{"equals-ignore-case":"INITIALIZING"}}]}}`, false},
		{"anything-but on an absent leaf", `{"detail":{"missing":[{"anything-but":"x"}]}}`, false},

		// exact values still work, alongside operators in the same array
		{"exact value", `{"source":["com.example.orders"]}`, true},
		{"operator alternative in an array", `{"source":["nope",{"prefix":"com.example."}]}`, true},
		{"boolean leaf", `{"detail":{"paid":[false]}}`, true},
		{"null leaf", `{"detail":{"note":[null]}}`, true},

		// every element of an outer pattern must hold
		{"one clause fails", `{"source":[{"prefix":"com.example."}],"detail-type":["Other"]}`, false},

		// a match expression outside a candidate array is a nested field
		// literally named after the match type, not a filter
		{"match type outside an array", `{"detail":{"numeric":[">",0]}}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the pattern is evaluated with the matcher delivery uses.
			if got := eventPatternMatches(tc.pattern, event); got != tc.want {
				t.Fatalf("match = %v, want %v for %s", got, tc.want, tc.pattern)
			}
		})
	}
}

func TestEventPatternOperators_patternCacheAgreesWithUncachedMatcher(t *testing.T) {
	// Given: an operator pattern and the cache PutEvents evaluates through.
	event := mustEvent(t, operatorEvent)
	cache := newPatternCache()
	pattern := `{"detail":{"count":[{"numeric":[">",0,"<=",5]}]}}`

	// When: it is matched twice, so the second call reads the cached parse.
	// Then: both agree with the uncached reference.
	for i := 0; i < 2; i++ {
		if got := cache.matches(pattern, event); got != eventPatternMatches(pattern, event) {
			t.Fatalf("call %d: cached matcher disagrees with the uncached one", i)
		}
	}
}

func TestValidateEventPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"array leaf", `{"source":["a"]}`, false},
		{"nested object", `{"detail":{"state":["a"]}}`, false},
		{"string leaf", `{"source":"a"}`, true},
		{"number leaf", `{"source":1}`, true},
		{"boolean leaf", `{"source":true}`, true},
		{"null leaf", `{"source":null}`, true},
		{"array of arrays", `{"source":[["a"]]}`, true},
		{"two keys in one match expression", `{"source":[{"prefix":"a","suffix":"b"}]}`, true},
		{"empty match expression", `{"source":[{}]}`, true},
		{"unknown match type", `{"source":[{"begins-with":"a"}]}`, true},
		{"numeric with an odd argument list", `{"a":[{"numeric":[">",1,"<"]}]}`, true},
		{"numeric with a non-numeric bound", `{"a":[{"numeric":[">","1"]}]}`, true},
		{"anything-but with a mixed list", `{"a":[{"anything-but":["x",{"prefix":"y"}]}]}`, true},
		{"empty candidate array", `{"source":[]}`, true},
		{"empty anything-but list", `{"a":[{"anything-but":[]}]}`, true},
		{"empty anything-but prefix list", `{"a":[{"anything-but":{"prefix":[]}}]}`, true},
		{"anything-but nesting equals-ignore-case under prefix", `{"a":[{"anything-but":{"prefix":{"equals-ignore-case":"x"}}}]}`, true},
		{"numeric bound past AWS's range", `{"a":[{"numeric":[">",6000000000]}]}`, true},
		{"numeric bound at AWS's range", `{"a":[{"numeric":[">",5000000000]}]}`, false},
		{"exists with a string", `{"a":[{"exists":"true"}]}`, true},
		{"cidr", `{"a":[{"cidr":"10.0.0.0/24"}]}`, true},
		{"wildcard", `{"a":[{"wildcard":"a*b"}]}`, true},
		{"dollar-or", `{"$or":[{"a":["1"]}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a candidate pattern document.
			var doc map[string]any
			if err := json.Unmarshal([]byte(tc.pattern), &doc); err != nil {
				t.Fatalf("fixture is not JSON: %v", err)
			}

			// When/Then: validation agrees with what AWS accepts.
			err := validateEventPattern(doc)
			if tc.wantErr && err == nil {
				t.Fatalf("validateEventPattern(%s) = nil, want an error", tc.pattern)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateEventPattern(%s) = %v, want nil", tc.pattern, err)
			}
		})
	}
}

func TestValidateEventPattern_reasonIsDeterministic(t *testing.T) {
	// Given: a pattern with two invalid keys, so map iteration order could
	// otherwise make the reported reason vary between runs.
	var doc map[string]any
	if err := json.Unmarshal([]byte(`{"zeta":"x","alpha":"y"}`), &doc); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}

	// When: it is validated repeatedly.
	first := validateEventPattern(doc)
	if first == nil {
		t.Fatal("expected an error")
	}
	// Then: the same reason comes back every time.
	for i := 0; i < 20; i++ {
		if got := validateEventPattern(doc); got == nil || got.Error() != first.Error() {
			t.Fatalf("reason = %v, want the stable %v", got, first)
		}
	}
}

func TestEventPatternOperators_unreadableClauseNeverMatches(t *testing.T) {
	// Given: patterns PutRule now refuses, as a rule stored before it learned
	// to would still be. The matcher has to cope, and the dangerous failure is
	// not "matches nothing" but an anything-but that negates a clause it could
	// not read and so matches every event.
	event := mustEvent(t, `{"detail":{"path":"/usr/lib/x"}}`)

	for _, pattern := range []string{
		// match types Overcast does not evaluate
		`{"detail":{"path":[{"wildcard":"*"}]}}`,
		`{"detail":{"path":[{"cidr":"10.0.0.0/24"}]}}`,
		`{"detail":{"path":[{"anything-but":{"wildcard":"*/lib/*"}}]}}`,
		`{"detail":{"path":[{"anything-but":{"cidr":"10.0.0.0/24"}}]}}`,
		// match expressions that are not one match type with one argument
		`{"detail":{"path":[{"prefix":"/usr","suffix":"/x"}]}}`,
		`{"detail":{"path":[{}]}}`,
		// anything-but nesting an argument shape it does not take
		`{"detail":{"path":[{"anything-but":{"prefix":{"equals-ignore-case":"/usr"}}}]}}`,
		`{"detail":{"path":[{"anything-but":{"prefix":[123]}}]}}`,
		`{"detail":{"path":[{"anything-but":{"prefix":[]}}]}}`,
		`{"detail":{"path":[{"anything-but":[]}]}}`,
		// the bare-scalar leaf shape, which no longer reaches the store
		`{"detail":{"path":[]}}`,
	} {
		// When/Then: it matches nothing rather than everything.
		if eventPatternMatches(pattern, event) {
			t.Fatalf("a clause the matcher cannot read matched: %s", pattern)
		}
	}
}

func TestEventPatternOperators_legacyBareScalarLeafStillMatchesAsItDid(t *testing.T) {
	// Given: the pattern shape PutRule accepted before this change. Rules are
	// persisted, so one may still be in the store; it must keep behaving the
	// way it did rather than start matching more.
	event := mustEvent(t, `{"source":"com.example.orders"}`)

	if !eventPatternMatches(`{"source":"com.example.orders"}`, event) {
		t.Fatal("a stored bare-scalar pattern stopped matching its own event")
	}
	if eventPatternMatches(`{"source":"com.example.other"}`, event) {
		t.Fatal("a stored bare-scalar pattern matched a different value")
	}
	if eventPatternMatches(`{"missing":"x"}`, event) {
		t.Fatal("a stored bare-scalar pattern matched an absent field")
	}
}
