package eventbridge

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// putRuleValidationService returns a service whose store is empty apart from
// whatever the test creates, so "the bus does not exist" is the default state.
func putRuleValidationService() *Service { return newTestEventPatternService() }

func TestPutRuleTyped_invalidEventPatternIsRefused(t *testing.T) {
	// Given: a service with only the default bus.
	s := putRuleValidationService()
	ctx := context.Background()

	cases := []struct {
		name    string
		pattern string
	}{
		{"not JSON at all", `not json`},
		{"JSON but not an object", `["source"]`},
		{"truncated JSON", `{"source":[`},
		{"leaf is a bare string", `{"source":"com.example.orders"}`},
		{"leaf is a bare number", `{"detail":{"amount":42}}`},
		{"leaf is a bare object that is not a nested pattern", `{"source":{"equals":"x"}}`},
		{"unrecognized match type", `{"source":[{"starts-with":"com."}]}`},
		{"exists takes a boolean", `{"source":[{"exists":"true"}]}`},
		{"numeric needs an operator and a number", `{"detail":{"amount":[{"numeric":[100]}]}}`},
		{"numeric comparison is not one AWS defines", `{"detail":{"amount":[{"numeric":["~",100]}]}}`},
		{"prefix takes a string", `{"source":[{"prefix":123}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: PutRule is called with it.
			_, aerr := s.putRuleTyped(ctx, &putRuleRequest{Name: "r", EventPattern: tc.pattern})

			// Then: the documented InvalidEventPatternException comes back —
			// not a 200 that stores a rule which can never fire.
			if aerr == nil {
				t.Fatal("expected InvalidEventPatternException, got a stored rule")
			}
			if aerr.Code != "InvalidEventPatternException" || aerr.HTTPStatus != http.StatusBadRequest {
				t.Fatalf("error = %s/%d (%s), want InvalidEventPatternException/400", aerr.Code, aerr.HTTPStatus, aerr.Message)
			}
			if !strings.HasPrefix(aerr.Message, "Event pattern is not valid. Reason: ") {
				t.Fatalf("message = %q, want AWS's \"Event pattern is not valid. Reason: …\" shape", aerr.Message)
			}
		})
	}
}

func TestPutRuleTyped_unsupportedOperatorIsRefusedNotSilentlyStored(t *testing.T) {
	// Given: the three pattern operators Overcast does not implement.
	s := putRuleValidationService()
	ctx := context.Background()

	cases := map[string]string{
		"cidr":                         `{"detail":{"sourceIPAddress":[{"cidr":"10.0.0.0/24"}]}}`,
		"wildcard":                     `{"detail":{"FileName":[{"wildcard":"dir/*.png"}]}}`,
		"anything-but with a wildcard": `{"detail":{"FilePath":[{"anything-but":{"wildcard":"*/lib/*"}}]}}`,
		"$or":                          `{"detail":{"$or":[{"a":["1"]},{"b":["2"]}]}}`,
	}
	for name, pattern := range cases {
		t.Run(name, func(t *testing.T) {
			// When: a rule is created with one.
			_, aerr := s.putRuleTyped(ctx, &putRuleRequest{Name: "r", EventPattern: pattern})

			// Then: it is refused loudly rather than stored as a rule that
			// silently never matches anything (#484).
			if aerr == nil {
				t.Fatal("expected InvalidEventPatternException for an unsupported operator")
			}
			if aerr.Code != "InvalidEventPatternException" {
				t.Fatalf("error = %s (%s), want InvalidEventPatternException", aerr.Code, aerr.Message)
			}
			if !strings.Contains(aerr.Message, "Overcast") {
				t.Fatalf("message = %q, want it to name Overcast as the limitation", aerr.Message)
			}
		})
	}
}

func TestPutRuleTyped_acceptsSupportedOperators(t *testing.T) {
	// Given: a pattern using every operator Overcast implements.
	s := putRuleValidationService()
	ctx := context.Background()

	patterns := []string{
		`{"source":[{"prefix":"com.example."}]}`,
		`{"source":[{"prefix":{"equals-ignore-case":"COM.Example."}}]}`,
		`{"detail":{"file":[{"suffix":".png"}]}}`,
		`{"detail":{"file":[{"suffix":{"equals-ignore-case":".PNG"}}]}}`,
		`{"detail":{"state":[{"exists":true}]}}`,
		`{"detail":{"state":[{"exists":false}]}}`,
		`{"detail":{"state":[{"anything-but":"initializing"}]}}`,
		`{"detail":{"state":[{"anything-but":["stopped","overloaded"]}]}}`,
		`{"detail":{"limit":[{"anything-but":[100,200]}]}}`,
		`{"detail":{"state":[{"anything-but":{"prefix":"init"}}]}}`,
		`{"detail":{"state":[{"anything-but":{"prefix":["init","stop"]}}]}}`,
		`{"detail":{"file":[{"anything-but":{"suffix":[".txt",".rtf"]}}]}}`,
		`{"detail":{"state":[{"anything-but":{"equals-ignore-case":"initializing"}}]}}`,
		`{"detail":{"amount":[{"numeric":["=",100]}]}}`,
		`{"detail":{"amount":[{"numeric":[">",0,"<=",5]}]}}`,
		`{"detail-type":[{"equals-ignore-case":"order created"}]}`,
		`{"detail":{"id":["1",2,true,null]}}`,
	}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			// When/Then: PutRule accepts it.
			if _, aerr := s.putRuleTyped(ctx, &putRuleRequest{Name: "r", EventPattern: pattern}); aerr != nil {
				t.Fatalf("PutRule refused a supported pattern: %s: %s", aerr.Code, aerr.Message)
			}
		})
	}
}

func TestPutRuleTyped_requiresEventPatternOrScheduleExpression(t *testing.T) {
	// Given: a PutRule request carrying neither.
	s := putRuleValidationService()

	// When: it is submitted.
	_, aerr := s.putRuleTyped(context.Background(), &putRuleRequest{Name: "no-trigger"})

	// Then: AWS's documented requirement — "A rule must contain at least an
	// EventPattern or ScheduleExpression" — is enforced.
	if aerr == nil {
		t.Fatal("expected a ValidationException for a rule with no trigger")
	}
	if aerr.Code != "ValidationException" || aerr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("error = %s/%d (%s), want ValidationException/400", aerr.Code, aerr.HTTPStatus, aerr.Message)
	}
	if aerr.Message != "Parameter(s) EventPattern or ScheduleExpression must be specified." {
		t.Fatalf("message = %q", aerr.Message)
	}
}

func TestPutRuleTyped_overlongEventPatternIsRefused(t *testing.T) {
	// Given: a pattern past the documented 4096-character constraint.
	s := putRuleValidationService()
	long := `{"source":["` + strings.Repeat("a", maxEventPatternLength) + `"]}`

	// When: a rule is created with it.
	_, aerr := s.putRuleTyped(context.Background(), &putRuleRequest{Name: "r", EventPattern: long})

	// Then: the same ValidationException TestEventPattern already returns.
	if aerr == nil || aerr.Code != "ValidationException" {
		t.Fatalf("error = %v, want ValidationException for a >%d-byte pattern", aerr, maxEventPatternLength)
	}
}

func TestPutRuleTyped_unknownEventBusIsResourceNotFound(t *testing.T) {
	// Given: a service where "missing-bus" was never created.
	s := putRuleValidationService()
	ctx := context.Background()

	// When: a rule is put on it.
	_, aerr := s.putRuleTyped(ctx, &putRuleRequest{
		Name:         "r",
		EventBusName: "missing-bus",
		EventPattern: `{"source":["com.example.orders"]}`,
	})

	// Then: ResourceNotFoundException, rather than a rule stranded on a bus
	// no event can ever be published to.
	if aerr == nil {
		t.Fatal("expected ResourceNotFoundException for an unknown event bus")
	}
	if aerr.Code != "ResourceNotFoundException" || aerr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("error = %s/%d (%s), want ResourceNotFoundException/400", aerr.Code, aerr.HTTPStatus, aerr.Message)
	}
	if !strings.Contains(aerr.Message, "missing-bus") {
		t.Fatalf("message = %q, want it to name the bus", aerr.Message)
	}
}

func TestPutRuleTyped_knownAndDefaultBusesAreAccepted(t *testing.T) {
	// Given: a created bus alongside the default one, which is never stored.
	s := putRuleValidationService()
	ctx := context.Background()
	if _, aerr := s.createEventBusTyped(ctx, &createEventBusRequest{Name: "orders"}); aerr != nil {
		t.Fatalf("CreateEventBus: %s", aerr.Message)
	}

	// When/Then: every spelling of a reachable bus is accepted, including the
	// bus ARN form the API's EventBusName pattern admits as an optional
	// prefix — the existence check must not turn a legal ARN into a phantom
	// "bus does not exist".
	for _, bus := range []string{
		"",
		"default",
		"orders",
		"arn:aws:events:us-east-1:000000000000:event-bus/orders",
	} {
		if _, aerr := s.putRuleTyped(ctx, &putRuleRequest{
			Name:         "r",
			EventBusName: bus,
			EventPattern: `{"source":["com.example.orders"]}`,
		}); aerr != nil {
			t.Fatalf("PutRule on bus %q: %s: %s", bus, aerr.Code, aerr.Message)
		}
	}
}

func TestPutRuleTyped_busARNStoresTheRuleUnderTheBusName(t *testing.T) {
	// Given: a bus created by name.
	s := putRuleValidationService()
	ctx := context.Background()
	if _, aerr := s.createEventBusTyped(ctx, &createEventBusRequest{Name: "orders"}); aerr != nil {
		t.Fatalf("CreateEventBus: %s", aerr.Message)
	}

	// When: a rule is created against the bus ARN rather than the name.
	resp, aerr := s.putRuleTyped(ctx, &putRuleRequest{
		Name:         "by-arn",
		EventBusName: "arn:aws:events:us-east-1:000000000000:event-bus/orders",
		EventPattern: `{"source":["com.example.orders"]}`,
	})
	if aerr != nil {
		t.Fatalf("PutRule: %s: %s", aerr.Code, aerr.Message)
	}

	// Then: it is reachable by bus name, which is the only spelling
	// DescribeRule, ListRules, PutTargets and delivery key off — storing it
	// under the ARN would strand it exactly as an unknown bus would.
	want := "arn:aws:events:us-east-1:000000000000:rule/orders/by-arn"
	if resp.RuleArn != want {
		t.Fatalf("RuleArn = %q, want %q", resp.RuleArn, want)
	}
	rule, aerr := s.describeRuleTyped(ctx, &describeRuleRequest{Name: "by-arn", EventBusName: "orders"})
	if aerr != nil {
		t.Fatalf("DescribeRule after an ARN-spelled PutRule: %s: %s", aerr.Code, aerr.Message)
	}
	if rule.EventBusName != "orders" {
		t.Fatalf("stored EventBusName = %q, want orders", rule.EventBusName)
	}
}

func TestPutRuleTyped_acceptsBothTriggersOnOneRule(t *testing.T) {
	// Given: AWS's documented "A rule can have both an EventPattern and a
	// ScheduleExpression".
	s := putRuleValidationService()

	// When/Then: both together are accepted.
	if _, aerr := s.putRuleTyped(context.Background(), &putRuleRequest{
		Name:         "both",
		EventPattern: `{"source":["com.example.orders"]}`,
		ScheduleExpr: "rate(5 minutes)",
	}); aerr != nil {
		t.Fatalf("PutRule with both triggers: %s: %s", aerr.Code, aerr.Message)
	}
}

func TestPutRuleTyped_refusedRuleIsNotStored(t *testing.T) {
	// Given: a rule name that has never been used.
	s := putRuleValidationService()
	ctx := context.Background()

	// When: PutRule is refused for an invalid pattern.
	if _, aerr := s.putRuleTyped(ctx, &putRuleRequest{Name: "ghost", EventPattern: `{"source":"x"}`}); aerr == nil {
		t.Fatal("expected the invalid pattern to be refused")
	}

	// Then: nothing was written — validation runs before the store, the same
	// ordering CreateEventBus uses for tags (#1196).
	if _, aerr := s.describeRuleTyped(ctx, &describeRuleRequest{Name: "ghost"}); aerr == nil {
		t.Fatal("a refused PutRule left a rule behind")
	}
}
