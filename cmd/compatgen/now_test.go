//go:build dev

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `$now` value: the client's clock when a call is made, in epoch
// milliseconds, for a long member only and never on the expected side of a
// check (compat/model/README.md § Values).

func logsModel(t *testing.T) *serviceModel {
	t.Helper()
	model, err := loadModel(filepath.Join("..", "..", "models", "aws", "shapes"), "cloudwatch-logs")
	if err != nil {
		t.Fatal(err)
	}
	return model
}

type nowFixture struct {
	Comment string `json:"$comment"`
	Instant int64  `json:"instant"`
	Valid   []struct {
		Name  string `json:"name"`
		Now   any    `json:"now"`
		Value int64  `json:"value"`
	} `json:"valid"`
	Invalid []struct {
		Name string `json:"name"`
		Now  any    `json:"now"`
	} `json:"invalid"`
	InvalidArguments []struct {
		Name         string `json:"name"`
		Unit         string `json:"unit"`
		OffsetMillis int64  `json:"offsetMillis"`
	} `json:"invalidArguments"`
	Call struct {
		TickMillis int64          `json:"tickMillis"`
		Params     map[string]any `json:"params"`
		Sent       map[string]any `json:"sent"`
	} `json:"call"`
}

func loadNowFixture(t *testing.T) nowFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "compat", "model", "testdata", "now", "now.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f nowFixture
	if err := decodeStrict(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Valid) == 0 || len(f.Invalid) == 0 || len(f.InvalidArguments) == 0 {
		t.Fatal("the $now fixture may not be skipped by emptying it")
	}
	return f
}

// The generator accepts exactly the spellings every runtime evaluates, and
// refuses exactly the ones every runtime refuses.
func TestNowOf_sharedFixture(t *testing.T) {
	f := loadNowFixture(t)
	for _, c := range f.Valid {
		value := map[string]any{"$now": c.Now}
		if err := validateValue(value, "v"); err != nil {
			t.Errorf("valid/%s: refused: %v", c.Name, err)
			continue
		}
		offset, err := nowOf(c.Now)
		if err != nil || f.Instant+offset != c.Value {
			t.Errorf("valid/%s: instant %d + offset %d = %d, %v; want %d", c.Name, f.Instant, offset, f.Instant+offset, err, c.Value)
		}
	}
	for _, c := range f.Invalid {
		if err := validateValue(map[string]any{"$now": c.Now}, "v"); err == nil {
			t.Errorf("invalid/%s: accepted", c.Name)
		}
	}
	for _, c := range f.InvalidArguments {
		arg := map[string]any{"unit": c.Unit}
		if c.OffsetMillis != 0 {
			arg["offsetMillis"] = json.Number(jsonInt(c.OffsetMillis))
		}
		if err := validateValue(map[string]any{"$now": arg}, "v"); err == nil {
			t.Errorf("invalidArguments/%s: accepted", c.Name)
		}
	}
}

func jsonInt(n int64) string {
	out, _ := json.Marshal(n)
	return string(out)
}

// A $now is a number, so it is no $concat part and no $index list.
func TestValidateValue_nowIsNotAnArgument(t *testing.T) {
	now := map[string]any{"$now": map[string]any{"unit": "epochMillis"}}
	for name, value := range map[string]any{
		"$concat": map[string]any{"$concat": []any{"at-", now}},
		"$index":  map[string]any{"$index": []any{now, json.Number("0")}},
		"$base64": map[string]any{"$base64": now},
	} {
		if err := validateValue(value, "v"); err == nil {
			t.Errorf("a $now inside %s was accepted", name)
		}
	}
}

// Where a $now may go: a long, which is how CloudWatch Logs models the epoch
// milliseconds an event is stamped with. Not an int, not a string, and not a
// timestamp — no-portable-value stands for that one.
func TestCheckNowTarget(t *testing.T) {
	logs := logsModel(t)
	stamp, _ := logs.MemberTarget("InputLogEvent", "timestamp")
	limit, _ := logs.MemberTarget(logs.InputShape("GetLogEvents"), "limit")
	name, _ := logs.MemberTarget(logs.InputShape("GetLogEvents"), "logGroupName")
	if err := checkNowTarget(logs, stamp, "logEvents[0].timestamp"); err != nil {
		t.Fatalf("a long refused: %v", err)
	}
	for member, target := range map[string]string{"limit": limit, "logGroupName": name} {
		if err := checkNowTarget(logs, target, member); err == nil || !strings.Contains(err.Error(), "for a long member") {
			t.Errorf("%s: %v", member, err)
		}
	}
	kinesis := kinesisModel(t)
	when, _ := kinesis.MemberTarget(kinesis.InputShape("GetShardIterator"), "Timestamp")
	if err := checkNowTarget(kinesis, when, "Timestamp"); err == nil || !strings.Contains(err.Error(), "no portable value") {
		t.Errorf("a timestamp member: %v", err)
	}

	b := &binder{model: logs, service: "logs"}
	events, _ := logs.MemberTarget(logs.InputShape("PutLogEvents"), "logEvents")
	value := []any{map[string]any{
		"message":   "m",
		"timestamp": map[string]any{"$now": map[string]any{"unit": "epochMillis", "offsetMillis": json.Number("-1")}},
	}}
	if err := b.checkValue(value, events, exportKinds{}, "PutLogEvents.logEvents", "logs-events"); err != nil {
		t.Fatalf("a recipe's $now on a long was refused: %v", err)
	}
	if err := b.checkValue(map[string]any{"$now": map[string]any{"unit": "epochMillis"}}, limit, exportKinds{}, "GetLogEvents.limit", "logs-events"); err == nil {
		t.Fatal("a recipe's $now on an integer was accepted")
	}
}

// An authored scenario is held to the same rules, at any depth, and may not
// compare anything with a $now.
func TestCheckAuthoredValues_nowRule(t *testing.T) {
	logs := logsModel(t)
	now := func(offset string) any {
		arg := map[string]any{"unit": "epochMillis"}
		if offset != "" {
			arg["offsetMillis"] = json.Number(offset)
		}
		return map[string]any{"$now": arg}
	}
	put := func(stamp any) call {
		return call{Op: "PutLogEvents", Params: map[string]any{
			"logGroupName":  "g",
			"logStreamName": "s",
			"logEvents":     []any{map[string]any{"message": "m", "timestamp": stamp}},
		}}
	}
	withTest := func(tc test) group { return group{Name: "logs-events", Tests: []test{tc}} }
	ok := withTest(test{Name: "PutLogEvents", Op: "PutLogEvents", Call: put(now("-1")),
		Assert: []assertion{responseField(map[string]check{"$.nextSequenceToken": {NonEmpty: true}})}})
	if err := checkAuthoredValues(logs, ok); err != nil {
		t.Fatalf("a $now on a long was refused: %v", err)
	}

	for name, tc := range map[string]struct {
		g    group
		want string
	}{
		"on a string member": {
			withTest(test{Name: "PutLogEvents", Op: "PutLogEvents",
				Call:   call{Op: "PutLogEvents", Params: map[string]any{"logGroupName": now(""), "logStreamName": "s", "logEvents": []any{}}},
				Assert: ok.Tests[0].Assert}),
			"logGroupName",
		},
		"with a zero offset": {
			withTest(test{Name: "PutLogEvents", Op: "PutLogEvents", Call: put(now("0")), Assert: ok.Tests[0].Assert}),
			"omit it",
		},
		"as an equals": {
			withTest(test{Name: "GetLogEvents", Op: "GetLogEvents",
				Call:   call{Op: "GetLogEvents", Params: map[string]any{"logGroupName": "g", "logStreamName": "s"}},
				Assert: []assertion{responseField(map[string]check{"$.events[0].timestamp": equals(now(""))})}}),
			"cannot be an expected value",
		},
		"in a where": {
			withTest(test{Name: "GetLogEvents", Op: "GetLogEvents",
				Call: call{Op: "GetLogEvents", Params: map[string]any{"logGroupName": "g", "logStreamName": "s"}},
				Assert: []assertion{listContains(nil, "$.events", map[string]any{
					"$.timestamp": now(""),
				})}}),
			"cannot be an expected value",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkAuthoredValues(logs, tc.g)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The expected side of a generated check is refused on the same terms.
func TestCheckNotExpected(t *testing.T) {
	if err := checkNotExpected(json.Number("1"), "check $.x"); err != nil {
		t.Fatal(err)
	}
	nested := map[string]any{"a": []any{map[string]any{"$now": map[string]any{"unit": "epochMillis"}}}}
	if err := checkNotExpected(nested, "check $.x"); err == nil {
		t.Fatal("a $now nested in an expected value was accepted")
	}
}

// Each emitter spells a $now as its runtime's Now(unit, offset), inside the
// long-typed binding it already writes for a deferred value — the instant is
// read once per call by the runtime, never baked into the source.
func TestEmitters_spellNow(t *testing.T) {
	now := map[string]any{"$now": map[string]any{"unit": "epochMillis", "offsetMillis": json.Number("-2")}}
	bare := map[string]any{"$now": map[string]any{"unit": "epochMillis"}}
	check := func(lang, got, want string, err error) {
		t.Helper()
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", lang, got, err, want)
		}
	}
	got, err := goValue(now, "")
	check("go", got, `scenario.Now("epochMillis", -2)`, err)
	got, err = javaValue(now)
	check("java", got, `Values.now("epochMillis", -2L)`, err)
	got, err = dotnetValue(bare)
	check("dotnet", got, `Val.Now("epochMillis", 0L)`, err)
	got, err = rustValue(now, "")
	check("rust", got, `scenario::now("epochMillis", -2)`, err)

	logs := logsModel(t)
	stamp, _ := logs.MemberTarget("InputLogEvent", "timestamp")
	var bind rustBindings
	got, err = rustValueOfKind(logs, "aws_sdk_cloudwatchlogs", stamp, now, "logEvents[0].timestamp", &bind)
	check("rust setter", got, `b.i64("logEvents[0].timestamp")?`, err)
	limit, _ := logs.MemberTarget(logs.InputShape("GetLogEvents"), "limit")
	if _, err := rustValueOfKind(logs, "aws_sdk_cloudwatchlogs", limit, now, "limit", &bind); err == nil {
		t.Error("rust: a $now into an i32 was spelled")
	}
}

// The pseudo-code renderings read the clock once for the call and add the
// offset, in each language's own spelling.
func TestExplain_rendersNow(t *testing.T) {
	now := map[string]any{"$now": map[string]any{"unit": "epochMillis", "offsetMillis": json.Number("-2")}}
	for lang, tc := range map[string]struct {
		st   style
		want string
	}{
		"python": {pyStyle(), "now_ms - 2"},
		"node":   {jsStyle(), "nowMs - 2"},
		"cli":    {cliStyle(clientInfo{EndpointPrefix: "logs"}), "$((NOW_MS - 2))"},
	} {
		if got := tc.st.value(now); got != tc.want {
			t.Errorf("%s: %q, want %q", lang, got, tc.want)
		}
	}
}
