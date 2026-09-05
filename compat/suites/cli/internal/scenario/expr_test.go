package scenario

import (
	"strings"
	"testing"
)

func testEvaluator(values map[string]any) *evaluator {
	bag := newContextBag()
	for k, v := range values {
		bag.set(k, v)
	}
	return &evaluator{runID: "oc-run", group: "sqs-gen-queue", bag: bag}
}

func TestEvalValueExpressions(t *testing.T) {
	e := testEvaluator(map[string]any{
		"queue.url":  "https://sqs/q",
		"queue.arn":  "arn:aws:sqs:us-east-1:1234:q",
		"batch.ids":  []any{"m-1", "m-2"},
		"queue.size": float64(30),
	})

	cases := []struct {
		name string
		in   any
		want any
	}{
		{name: "a scalar is itself", in: "plain", want: "plain"},
		{name: "a number is itself", in: float64(3), want: float64(3)},
		{name: "$lit is verbatim", in: obj{"$lit": obj{"$ref": "not a reference"}}, want: obj{"$ref": "not a reference"}},
		{name: "$ref reads the context", in: obj{"$ref": "queue.url"}, want: "https://sqs/q"},
		{name: "$ref keeps the value's JSON type", in: obj{"$ref": "queue.size"}, want: float64(30)},
		{name: "$name is runId-group-suffix", in: obj{"$name": "q"}, want: "oc-run-sqs-gen-queue-q"},
		{
			name: "$concat joins literals and expressions",
			in: obj{"$concat": []any{
				`{"deadLetterTargetArn":"`, obj{"$ref": "queue.arn"}, `","maxReceiveCount":"5"}`,
			}},
			want: `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:1234:q","maxReceiveCount":"5"}`,
		},
		{name: "$index selects from a list", in: obj{"$index": []any{obj{"$ref": "batch.ids"}, float64(1)}}, want: "m-2"},
		{
			name: "a structure's leaves are evaluated",
			in:   obj{"Attributes": obj{"VisibilityTimeout": "30", "Name": obj{"$name": "q"}}},
			want: obj{"Attributes": obj{"VisibilityTimeout": "30", "Name": "oc-run-sqs-gen-queue-q"}},
		},
		{
			name: "a list's items are evaluated",
			in:   []any{obj{"$ref": "queue.url"}, "literal"},
			want: []any{"https://sqs/q", "literal"},
		},
		{
			name: "an object with two keys is structural even if one starts with $",
			in:   obj{"$odd": "a", "Other": "b"},
			want: obj{"$odd": "a", "Other": "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.eval(tc.in)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			if !jsonEqual(got, tc.want) {
				t.Errorf("eval = %s, want %s", render(got), render(tc.want))
			}
		})
	}
}

func TestEvalValueExpressionErrors(t *testing.T) {
	e := testEvaluator(map[string]any{"queue.url": "https://sqs/q", "queue.size": float64(30)})

	cases := []struct {
		name string
		in   any
		want string
	}{
		{name: "an unresolvable $ref", in: obj{"$ref": "queue.missing"}, want: `context path "queue.missing" is not set`},
		{name: "an unknown expression", in: obj{"$nope": "x"}, want: `unknown value expression "$nope"`},
		{name: "a non-string $concat part", in: obj{"$concat": []any{obj{"$ref": "queue.size"}}}, want: "not a string"},
		{name: "$index past the end", in: obj{"$index": []any{[]any{"a"}, float64(4)}}, want: "past the end"},
		{name: "$index on a non-list", in: obj{"$index": []any{"a string", float64(0)}}, want: "applies to a list"},
		{name: "a nested unresolvable $ref", in: obj{"A": []any{obj{"$ref": "no.such"}}}, want: `"no.such" is not set`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.eval(tc.in)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v does not contain %q", err, tc.want)
			}
		})
	}
}

// TestNameIsNeverShortened pins the name-hygiene rule: the group token is the
// whole group name, so the orphan sweep can find anything a crashed run left.
func TestNameIsNeverShortened(t *testing.T) {
	e := &evaluator{runID: "oc-a3f9b12c", group: "organizations-gen-policy", bag: newContextBag()}
	if got, want := e.name("policy"), "oc-a3f9b12c-organizations-gen-policy-policy"; got != want {
		t.Errorf("$name = %q, want %q", got, want)
	}
}

func TestEvalParamsLeavesTypesAlone(t *testing.T) {
	e := testEvaluator(nil)
	got, err := e.evalParams(map[string]any{
		"MaxNumberOfMessages": float64(1),
		"WaitTimeSeconds":     float64(5),
		"VisibilityTimeout":   float64(0),
		"MessageBody":         "compat scenario message",
		"Attributes":          obj{"VisibilityTimeout": "30"},
	})
	if err != nil {
		t.Fatalf("evalParams: %v", err)
	}
	// The scenario carries strings for string members and numbers for numeric
	// ones; --cli-input-json takes the modelled JSON verbatim, so nothing here
	// may be re-typed on the way out.
	want := `{"Attributes":{"VisibilityTimeout":"30"},"MaxNumberOfMessages":1,"MessageBody":"compat scenario message","VisibilityTimeout":0,"WaitTimeSeconds":5}`
	if s := render(got); s != want {
		t.Errorf("params = %s, want %s", s, want)
	}
}
