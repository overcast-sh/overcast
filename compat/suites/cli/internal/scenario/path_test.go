package scenario

import "testing"

func TestResolvePath(t *testing.T) {
	doc := obj{
		"QueueUrl":   "https://sqs/q",
		"Attributes": obj{"QueueArn": "arn:q", "VisibilityTimeout": "30"},
		"Messages": []any{
			obj{"MessageId": "m-1", "ReceiptHandle": "rh-1"},
			obj{"MessageId": "m-2"},
		},
		"Tags":  obj{"compat": "scenario"},
		"Empty": nil,
		// A member name with the punctuation the IR's Path pattern admits.
		"x-amz:meta/one": "v",
	}

	cases := []struct {
		name     string
		path     string
		want     any
		resolved bool
	}{
		{name: "the whole response", path: "$", want: doc, resolved: true},
		{name: "a member", path: "$.QueueUrl", want: "https://sqs/q", resolved: true},
		{name: "a nested member", path: "$.Attributes.QueueArn", want: "arn:q", resolved: true},
		{name: "a map key", path: "$.Tags.compat", want: "scenario", resolved: true},
		{name: "a list element", path: "$.Messages[0].ReceiptHandle", want: "rh-1", resolved: true},
		{name: "a present null", path: "$.Empty", want: nil, resolved: true},
		{name: "punctuated member names", path: "$.x-amz:meta/one", want: "v", resolved: true},
		{name: "an absent member", path: "$.Nope", resolved: false},
		{name: "an absent nested member", path: "$.Attributes.Nope", resolved: false},
		{name: "an absent segment part-way down", path: "$.Nope.Deeper", resolved: false},
		{name: "an index past the end", path: "$.Messages[5]", resolved: false},
		{name: "an absent member of a list element", path: "$.Messages[1].ReceiptHandle", resolved: false},
		{name: "an index into a non-list", path: "$.QueueUrl[0]", resolved: false},
		{name: "a member of a non-object", path: "$.QueueUrl.Nope", resolved: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, resolved, err := resolvePath(doc, tc.path)
			if err != nil {
				t.Fatalf("resolvePath: %v", err)
			}
			if resolved != tc.resolved {
				t.Fatalf("resolved = %v, want %v", resolved, tc.resolved)
			}
			if resolved && !jsonEqual(got, tc.want) {
				t.Errorf("value = %s, want %s", render(got), render(tc.want))
			}
		})
	}
}

func TestParsePathRejectsMalformedPaths(t *testing.T) {
	for _, p := range []string{"", "QueueUrl", "$..Name", "$.Messages[", "$.Messages[a]", "$Name", "$.Name["} {
		if _, err := parsePath(p); err == nil {
			t.Errorf("parsePath(%q) = nil error, want a rejection", p)
		}
	}
}

// TestJSONEqualityDoesNotCoerce pins the equals rule: both sides are JSON
// values, compared as JSON, with no coercion between types.
func TestJSONEqualityDoesNotCoerce(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{name: "equal strings", a: "60", b: "60", want: true},
		{name: "equal numbers", a: float64(60), b: float64(60), want: true},
		{name: "a string is not its number", a: "60", b: float64(60), want: false},
		{name: "equal objects regardless of key order", a: obj{"a": 1, "b": 2}, b: obj{"b": 2, "a": 1}, want: true},
		{name: "equal lists", a: []any{"a", "b"}, b: []any{"a", "b"}, want: true},
		{name: "order matters in a list", a: []any{"a", "b"}, b: []any{"b", "a"}, want: false},
		{name: "null equals null", a: nil, b: nil, want: true},
		{name: "null is not an empty string", a: nil, b: "", want: false},
		{name: "false is not zero", a: false, b: float64(0), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("jsonEqual(%s, %s) = %v, want %v", render(tc.a), render(tc.b), got, tc.want)
			}
		})
	}
}

// TestCanonicalJSONDoesNotEscapeHTML keeps a policy document readable in a
// failure message and identical on the wire.
func TestCanonicalJSONDoesNotEscapeHTML(t *testing.T) {
	got, err := canonicalJSON(obj{"Content": `a<b&c>d`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"Content":"a<b&c>d"}`; got != want {
		t.Errorf("canonicalJSON = %s, want %s", got, want)
	}
}
