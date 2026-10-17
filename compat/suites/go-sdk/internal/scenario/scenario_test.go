package scenario

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// The runtime the emitted groups call into, exercised against an in-memory
// fake: a Call's Send is a closure, so a fake service is a closure returning a
// canned SDK output. The inputs are the real SDK input structs and the outputs
// the real SDK output structs, so what these tests pin about pointer fields,
// value fields and absent members is what a live run will see.

var testGroup = Group{Name: "sqs-gen-queue", File: "compat/model/scenarios/sqs.json"}

func testContext() *harness.TestContext {
	return harness.NewTestContext("http://127.0.0.1:1", "us-east-1", "oc-test")
}

// sendOK returns a Send that answers with a fixed response and records the
// input it was given.
func sendOK(out any, seen *any) func(context.Context, any) (any, error) {
	return func(_ context.Context, in any) (any, error) {
		if seen != nil {
			*seen = in
		}
		return out, nil
	}
}

func sendErr(err error) func(context.Context, any) (any, error) {
	return func(context.Context, any) (any, error) { return nil, err }
}

// createQueue is the call the emitter writes for sqs-gen-queue/CreateQueue,
// hand-copied here so the test exercises the shape the emitter produces.
func createQueue(out any, seen *any) Call {
	return Call{
		Op:     "CreateQueue",
		Params: `{"QueueName":{"$name":"q"}}`,
		Build: func(b *Binder) any {
			in := &sqs.CreateQueueInput{}
			in.QueueName = aws.String(Bind[string](b, "QueueName", Name("q")))
			return in
		},
		Send:   sendOK(out, seen),
		Export: map[string]string{"queue.url": "$.QueueUrl"},
	}
}

func TestRunTest_callExportsAndEveryClauseHolds(t *testing.T) {
	// Given: a create whose response is exported, and a read-back and a
	// list-membership clause that reference the exported value.
	var sent any
	created := &sqs.CreateQueueOutput{QueueUrl: aws.String("http://q/oc-test-sqs-gen-queue-q")}
	tc := Test{
		Call: createQueue(created, &sent),
		Assert: []Clause{
			ResponseField(NonEmpty("$.QueueUrl")),
			Readback(Call{
				Op:     "GetQueueAttributes",
				Params: `{}`,
				Build: func(b *Binder) any {
					in := &sqs.GetQueueAttributesInput{}
					in.QueueUrl = aws.String(Bind[string](b, "QueueUrl", Ref("queue.url")))
					in.AttributeNames = []sqstypes.QueueAttributeName{"All"}
					return in
				},
				Send: sendOK(&sqs.GetQueueAttributesOutput{
					Attributes: map[string]string{"QueueArn": "arn:aws:sqs:us-east-1:000000000000:q", "VisibilityTimeout": "30"},
				}, nil),
				Export: map[string]string{"queue.arn": "$.Attributes.QueueArn"},
			}, Equals("$.Attributes.VisibilityTimeout", "30")),
			ListContains(&Call{
				Op:     "ListQueues",
				Params: `{}`,
				Build:  func(b *Binder) any { return &sqs.ListQueuesInput{} },
				Send:   sendOK(&sqs.ListQueuesOutput{QueueUrls: []string{"http://q/oc-test-sqs-gen-queue-q"}}, nil),
			}, "$.QueueUrls", Where("$", Ref("queue.url"))),
		},
	}

	// When: the test runs.
	ctx := testContext()
	if err := testGroup.RunTest(context.Background(), ctx, "CreateQueue", tc); err != nil {
		t.Fatalf("RunTest: %v", err)
	}

	// Then: the typed input carried the $name, and the read-back's export
	// reached the bag.
	in, ok := sent.(*sqs.CreateQueueInput)
	if !ok {
		t.Fatalf("Send received %T, want *sqs.CreateQueueInput", sent)
	}
	if got := aws.ToString(in.QueueName); got != "oc-test-sqs-gen-queue-q" {
		t.Errorf("QueueName = %q, want the {runId}-{group}-{suffix} form", got)
	}
	if v, ok := bagFor(ctx).get("queue.arn"); !ok || v != "arn:aws:sqs:us-east-1:000000000000:q" {
		t.Errorf("queue.arn = %v (set %v), want the read-back's export", v, ok)
	}
}

func TestRunTest_failureCarriesTheSixFields(t *testing.T) {
	// Given: a read-back whose equals does not hold.
	tc := Test{
		Call: createQueue(&sqs.CreateQueueOutput{QueueUrl: aws.String("http://q/x")}, nil),
		Assert: []Clause{
			Readback(Call{
				Op:     "GetQueueAttributes",
				Params: `{"QueueUrl":{"$ref":"queue.url"}}`,
				Build: func(b *Binder) any {
					in := &sqs.GetQueueAttributesInput{}
					in.QueueUrl = aws.String(Bind[string](b, "QueueUrl", Ref("queue.url")))
					return in
				},
				Send: sendOK(&sqs.GetQueueAttributesOutput{Attributes: map[string]string{"VisibilityTimeout": "30"}}, nil),
			}, Equals("$.Attributes.VisibilityTimeout", "60")),
		},
	}

	// When: it runs.
	err := testGroup.RunTest(context.Background(), testContext(), "SetQueueAttributes", tc)

	// Then: the message carries group/test, the clause's own operation, the
	// params actually sent, the kind and path, expected vs actual, and the
	// scenario file with the step index.
	if err == nil {
		t.Fatal("a failing equals did not fail the test")
	}
	for _, want := range []string{
		"sqs-gen-queue/SetQueueAttributes",
		"GetQueueAttributes",
		`params {"QueueUrl":"http://q/x"}`,
		"readback equals at $.Attributes.VisibilityTimeout",
		`expected "60", actual "30"`,
		"(compat/model/scenarios/sqs.json assert[0])",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure message lacks %q:\n%s", want, err.Error())
		}
	}
}

func TestRunTest_unresolvableRefNamesThePathAndSendsNothing(t *testing.T) {
	// Given: a call whose only member references a context path nothing set.
	sent := false
	tc := Test{
		Call: Call{
			Op:     "DeleteQueue",
			Params: `{"QueueUrl":{"$ref":"queue.url"}}`,
			Build: func(b *Binder) any {
				in := &sqs.DeleteQueueInput{}
				in.QueueUrl = aws.String(Bind[string](b, "QueueUrl", Ref("queue.url")))
				return in
			},
			Send: func(context.Context, any) (any, error) { sent = true; return &sqs.DeleteQueueOutput{}, nil },
		},
		Assert: []Clause{ResponseField(NonEmpty("$.QueueUrl"))},
	}

	// When: it runs.
	err := testGroup.RunTest(context.Background(), testContext(), "DeleteQueue", tc)

	// Then: nothing was sent, and field 3 shows the params as the scenario
	// file writes them rather than a half-built input.
	if sent {
		t.Error("a call with an unresolvable reference was sent anyway")
	}
	if err == nil {
		t.Fatal("an unresolvable reference did not fail the test")
	}
	for _, want := range []string{
		`params {"QueueUrl":{"$ref":"queue.url"}}`,
		"params at queue.url",
		"expected the context path to be set, actual <unset>",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure message lacks %q:\n%s", want, err.Error())
		}
	}
}

func TestChecks_matchTheIRsClosedSet(t *testing.T) {
	// Given: a response whose absent members are absent rather than null —
	// which is what a nil pointer and a nil slice mean in an SDK object model.
	out := &sqs.ListQueuesOutput{QueueUrls: nil, NextToken: nil}

	for _, tc := range []struct {
		name  string
		check Check
		holds bool
	}{
		{"isList accepts an omitted page", IsList("$.QueueUrls"), true},
		{"nonEmpty fails on an omitted page", NonEmpty("$.QueueUrls"), false},
		{"missing holds for an omitted token", Missing("$.NextToken"), true},
		{"nonEmpty fails for an omitted token", NonEmpty("$.NextToken"), false},
		{"equals fails when the path does not resolve", Equals("$.NextToken", "x"), false},
		{"matches fails when the path does not resolve", Matches("$.NextToken", "^x$"), false},
		{"ResultMetadata is not part of the modeled response", Missing("$.ResultMetadata"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runChecks(t, out, tc.check)
			if holds := err == nil; holds != tc.holds {
				t.Errorf("check held = %v, want %v (%v)", holds, tc.holds, err)
			}
		})
	}
}

func TestChecks_presentValues(t *testing.T) {
	out := &sqs.GetQueueAttributesOutput{Attributes: map[string]string{
		"VisibilityTimeout":            "30",
		"ApproximateNumberOfMessages":  "0",
		"QueueArn":                     "arn:aws:sqs:us-east-1:000000000000:q",
		"SqsManagedSseEnabled":         "true",
		"ApproximateNumberOfMessagesD": "",
	}}
	for _, tc := range []struct {
		name  string
		check Check
		holds bool
	}{
		{"equals compares as JSON, without coercion", Equals("$.Attributes.VisibilityTimeout", "30"), true},
		{"a number never equals its string", Equals("$.Attributes.VisibilityTimeout", 30), false},
		{"matches anchors only where the pattern does", Matches("$.Attributes.QueueArn", `^arn:aws:sqs:`), true},
		{"nonEmpty accepts a present value", NonEmpty("$.Attributes.QueueArn"), true},
		{"nonEmpty rejects an empty string", NonEmpty("$.Attributes.ApproximateNumberOfMessagesD"), false},
		{"isList rejects a present non-list", IsList("$.Attributes"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runChecks(t, out, tc.check)
			if holds := err == nil; holds != tc.holds {
				t.Errorf("check held = %v, want %v (%v)", holds, tc.holds, err)
			}
		})
	}
}

func TestAffixChecks_operandIsAValueExpression(t *testing.T) {
	// Given: a response whose ARN carries the run's queue name at its end,
	// and a number where a string would be.
	out := &sqs.GetQueueAttributesOutput{Attributes: map[string]string{
		"QueueArn": "arn:aws:sqs:us-east-1:000000000000:oc-test-sqs-gen-queue-q",
		"Count":    "7",
	}}
	for _, tc := range []struct {
		name    string
		check   Check
		holds   bool
		message string
	}{
		{"endsWith holds on a concat of a literal and a name", EndsWith("$.Attributes.QueueArn", Concat(":", Name("q"))), true, ""},
		{"endsWith is case-sensitive", EndsWith("$.Attributes.QueueArn", "-Q"), false, "ending with"},
		{"endsWith fails when the suffix is only inside the string", EndsWith("$.Attributes.QueueArn", "arn:aws"), false, "ending with"},
		{"startsWith holds on a literal", StartsWith("$.Attributes.QueueArn", "arn:aws:sqs:"), true, ""},
		{"startsWith fails when the string starts otherwise", StartsWith("$.Attributes.QueueArn", "sqs"), false, "starting with"},
		{"contains holds in the middle", Contains("$.Attributes.QueueArn", Concat(":000000000000:", Name("q"))), true, ""},
		{"contains fails on a part that is not there", Contains("$.Attributes.QueueArn", ":elsewhere:"), false, "containing"},
		{"endsWith fails when the path does not resolve", EndsWith("$.Attributes.Nope", "x"), false, "ending with"},
		{"endsWith fails on a non-string value", EndsWith("$.Attributes", "x"), false, "ending with"},
		{"an operand that is not a string fails the check", EndsWith("$.Attributes.QueueArn", Lit(7)), false, "non-empty string operand"},
		{"an empty operand fails the check", StartsWith("$.Attributes.QueueArn", ""), false, "non-empty string operand"},
		{"an operand that cannot be evaluated fails the check", EndsWith("$.Attributes.QueueArn", Ref("no.such")), false, "no.such"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runChecks(t, out, tc.check)
			if holds := err == nil; holds != tc.holds {
				t.Fatalf("check held = %v, want %v (%v)", holds, tc.holds, err)
			}
			if err != nil && !strings.Contains(err.Error(), tc.message) {
				t.Errorf("message lacks %q:\n%s", tc.message, err.Error())
			}
		})
	}
}

// TestUnsupportedPatternIsAnOrdinaryMismatch pins the wording every backend
// shares for a pattern its regex engine will not compile.
func TestUnsupportedPatternIsAnOrdinaryMismatch(t *testing.T) {
	err := runChecks(t, &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"QueueArn": "x"}}, Matches("$.Attributes.QueueArn", "a(b"))
	if err == nil {
		t.Fatal("an uncompilable pattern did not fail the check")
	}
	for _, want := range []string{"expected pattern a(b", "unsupported pattern:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%s", want, err.Error())
		}
	}
}

func runChecks(t *testing.T, out any, checks ...Check) error {
	t.Helper()
	return testGroup.RunTest(context.Background(), testContext(), "Check", Test{
		Call: Call{
			Op:     "ListQueues",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.ListQueuesInput{} },
			Send:   sendOK(out, nil),
		},
		Assert: []Clause{ResponseField(checks...)},
	})
}

func TestListClauses_treatAnAbsentListAsEmpty(t *testing.T) {
	// Given: a page the service omitted rather than serializing as [].
	absentPage := Test{
		Call: Call{
			Op:     "ListQueues",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.ListQueuesInput{} },
			Send:   sendOK(&sqs.ListQueuesOutput{}, nil),
		},
		Assert: []Clause{AbsentFromList(nil, "$.QueueUrls", Where("$", "http://q/x"))},
	}
	if err := testGroup.RunTest(context.Background(), testContext(), "ListQueues", absentPage); err != nil {
		t.Errorf("absent over an omitted list: %v", err)
	}

	contains := absentPage
	contains.Assert = []Clause{ListContains(nil, "$.QueueUrls", Where("$", "http://q/x"))}
	if err := testGroup.RunTest(context.Background(), testContext(), "ListQueues", contains); err == nil {
		t.Error("listContains held over an omitted list")
	}
}

func TestEventually_retriesAndGivesUpWithTheSharedPrefix(t *testing.T) {
	// Given: a read-back that only holds on the third attempt.
	attempts := 0
	readback := func(until int) Clause {
		return Readback(Call{
			Op:     "GetQueueAttributes",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.GetQueueAttributesInput{} },
			Send: func(context.Context, any) (any, error) {
				attempts++
				value := "30"
				if attempts >= until {
					value = "60"
				}
				return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"VisibilityTimeout": value}}, nil
			},
		}, Equals("$.Attributes.VisibilityTimeout", "60"))
	}

	base := Test{
		Call: Call{
			Op:     "SetQueueAttributes",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.SetQueueAttributesInput{} },
			Send:   sendOK(&sqs.SetQueueAttributesOutput{}, nil),
		},
	}

	// When: it is wrapped in a budget wide enough.
	holds := base
	holds.Assert = []Clause{Eventually(5, 1, readback(3))}
	if err := testGroup.RunTest(context.Background(), testContext(), "SetQueueAttributes", holds); err != nil {
		t.Fatalf("eventually did not hold within its budget: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want the clause to stop at the first that holds", attempts)
	}

	// And when: the budget is too short.
	attempts = 0
	gives := base
	gives.Assert = []Clause{Eventually(2, 7, readback(99))}
	err := testGroup.RunTest(context.Background(), testContext(), "SetQueueAttributes", gives)
	if err == nil {
		t.Fatal("eventually held with a budget too short for it")
	}

	// Then: the give-up reads byte for byte as the interpreters' does, with
	// the budget in front of the last attempt's six fields.
	const prefix = "eventually gave up after 2 attempt(s) 7ms apart; last failure: "
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("give-up message is\n%s\nwant the prefix %q", err.Error(), prefix)
	}
	if !strings.Contains(err.Error(), "assert[0].assert") {
		t.Errorf("the inner clause's step index is missing:\n%s", err.Error())
	}
}

// TestEventuallyExportsOnlyOnThePassingAttempt pins the rule that keeps a
// failing attempt from writing a stale reading into the bag.
func TestEventuallyExportsOnlyOnThePassingAttempt(t *testing.T) {
	attempts := 0
	clause := Eventually(3, 1, Readback(Call{
		Op:     "GetQueueAttributes",
		Params: `{}`,
		Build:  func(b *Binder) any { return &sqs.GetQueueAttributesInput{} },
		Send: func(context.Context, any) (any, error) {
			attempts++
			value := "stale"
			if attempts >= 2 {
				value = "settled"
			}
			return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"QueueArn": value}}, nil
		},
		Export: map[string]string{"queue.arn": "$.Attributes.QueueArn"},
	}, Equals("$.Attributes.QueueArn", "settled")))

	ctx := testContext()
	err := testGroup.RunTest(context.Background(), ctx, "CreateQueue", Test{
		Call: Call{
			Op:     "CreateQueue",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.CreateQueueInput{} },
			Send:   sendOK(&sqs.CreateQueueOutput{QueueUrl: aws.String("http://q/x")}, nil),
		},
		Assert: []Clause{clause},
	})
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if v, _ := bagFor(ctx).get("queue.arn"); v != "settled" {
		t.Errorf("queue.arn = %v, want the passing attempt's value only", v)
	}
}

func TestSetupAndTeardown(t *testing.T) {
	// Given: a setup whose second step fails.
	ok := Call{
		Op:     "CreateQueue",
		Params: `{}`,
		Build:  func(b *Binder) any { return &sqs.CreateQueueInput{} },
		Send:   sendOK(&sqs.CreateQueueOutput{QueueUrl: aws.String("http://q/x")}, nil),
		Export: map[string]string{"dlq.url": "$.QueueUrl"},
	}
	bad := Call{
		Op:     "GetQueueAttributes",
		Params: `{}`,
		Build:  func(b *Binder) any { return &sqs.GetQueueAttributesInput{} },
		Send:   sendErr(errors.New("the queue is not ready")),
	}

	ctx := testContext()
	err := testGroup.RunSetup(context.Background(), ctx, ok, bad)
	if err == nil {
		t.Fatal("a failing setup step did not fail the setup")
	}
	// The harness renders this as "setup failed: <message>" on every test in
	// the group, so the message has to be the six fields.
	for _, want := range []string{"sqs-gen-queue/setup", "GetQueueAttributes", "(compat/model/scenarios/sqs.json setup[1])"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("setup failure lacks %q:\n%s", want, err.Error())
		}
	}
	if v, _ := bagFor(ctx).get("dlq.url"); v != "http://q/x" {
		t.Error("the first setup step's export was rolled back; a failed setup still created what it created")
	}

	// And: teardown wraps each step individually and never fails the group.
	ran := 0
	counting := Call{
		Op:     "DeleteQueue",
		Params: `{}`,
		Build:  func(b *Binder) any { return &sqs.DeleteQueueInput{} },
		Send:   func(context.Context, any) (any, error) { ran++; return &sqs.DeleteQueueOutput{}, nil },
	}
	if err := testGroup.RunTeardown(context.Background(), ctx, bad, counting); err != nil {
		t.Errorf("teardown returned an error: %v", err)
	}
	if ran != 1 {
		t.Errorf("the step after a failing teardown step ran %d times, want 1", ran)
	}
}

// TestEmptyHooksAreNoOps pins the IR's "an empty list is a no-op, not a missing
// phase" for the probe groups, whose two lists are empty by construction.
func TestEmptyHooksAreNoOps(t *testing.T) {
	ctx := testContext()
	if err := testGroup.RunSetup(context.Background(), ctx); err != nil {
		t.Errorf("empty setup: %v", err)
	}
	if err := testGroup.RunTeardown(context.Background(), ctx); err != nil {
		t.Errorf("empty teardown: %v", err)
	}
}

func TestErrorClauses(t *testing.T) {
	notFound := sdkError(400, &sqstypes.QueueDoesNotExist{}, nil)
	spec := Error("QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue")

	// absent's error form holds when the call fails with the named error.
	absent := Test{
		Call: Call{
			Op:     "DeleteQueue",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.DeleteQueueInput{} },
			Send:   sendOK(&sqs.DeleteQueueOutput{}, nil),
		},
		Assert: []Clause{AbsentByError(Call{
			Op:     "GetQueueAttributes",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.GetQueueAttributesInput{} },
			Send:   sendErr(notFound),
		}, spec)},
	}
	if err := testGroup.RunTest(context.Background(), testContext(), "DeleteQueue", absent); err != nil {
		t.Errorf("absent-by-error did not hold: %v", err)
	}

	// A call that succeeds fails the clause, and says so.
	succeeds := absent
	succeeds.Assert = []Clause{AbsentByError(Call{
		Op:     "GetQueueAttributes",
		Params: `{}`,
		Build:  func(b *Binder) any { return &sqs.GetQueueAttributesInput{} },
		Send:   sendOK(&sqs.GetQueueAttributesOutput{}, nil),
	}, spec)}
	err := testGroup.RunTest(context.Background(), testContext(), "DeleteQueue", succeeds)
	if err == nil || !strings.Contains(err.Error(), "actual <no error>") {
		t.Errorf("a successful call satisfied an absent-by-error clause: %v", err)
	}

	// errorCode expects the test's own call to fail.
	code := Test{
		Call: Call{
			Op:     "GetQueueUrl",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.GetQueueUrlInput{} },
			Send:   sendErr(notFound),
		},
		Assert: []Clause{ErrorCode(spec)},
	}
	if err := testGroup.RunTest(context.Background(), testContext(), "GetQueueUrl", code); err != nil {
		t.Errorf("errorCode did not hold: %v", err)
	}
}

func TestUnimplementedReachesTheHarnessAsAClassification(t *testing.T) {
	// Given: a call the emulator answers with 501.
	notImplemented := sdkError(http.StatusNotImplemented, &smithy.GenericAPIError{Code: "NotImplemented"}, nil)
	tc := Test{
		Call: Call{
			Op:     "ListMessageMoveTasks",
			Params: `{}`,
			Build:  func(b *Binder) any { return &sqs.ListMessageMoveTasksInput{} },
			Send:   sendErr(notImplemented),
		},
		Assert: []Clause{ResponseField(NonEmpty("$.Results"))},
	}

	// When: the test runs.
	err := testGroup.RunTest(context.Background(), testContext(), "ListMessageMoveTasks", tc)

	// Then: the harness classifies it as unimplemented, through the sentinel
	// rather than through a substring test over a message this package wrote.
	if err == nil {
		t.Fatal("a 501 did not fail the call")
	}
	if !harness.IsUnimplemented(err) {
		t.Errorf("a 501 was not classified as unimplemented: %v", err)
	}
	if !errors.Is(err, harness.ErrUnimplemented) {
		t.Error("the classification did not come from the sentinel")
	}
}

// TestAComposedFailureIsNeverSniffedFor501 is the #1790 rule, restated for this
// suite: field 3 of a failure message is the params JSON, and a port or a run
// id in there can put a "501" in a message about something else entirely.
func TestAComposedFailureIsNeverSniffedFor501(t *testing.T) {
	tc := Test{
		Call: Call{
			Op:     "GetQueueUrl",
			Params: `{"QueueName":"q-501"}`,
			Build: func(b *Binder) any {
				in := &sqs.GetQueueUrlInput{}
				in.QueueName = aws.String("q-501")
				return in
			},
			Send: sendOK(&sqs.GetQueueUrlOutput{}, nil),
		},
		Assert: []Clause{ResponseField(NonEmpty("$.QueueUrl"))},
	}
	err := testGroup.RunTest(context.Background(), testContext(), "GetQueueUrl", tc)
	if err == nil {
		t.Fatal("an empty QueueUrl passed nonEmpty")
	}
	if !strings.Contains(err.Error(), "501") {
		t.Fatalf("the fixture no longer puts a 501 in the message, so it pins nothing:\n%s", err.Error())
	}
	if harness.IsUnimplemented(err) {
		t.Error("an assertion failure whose params contain \"501\" was classified as unimplemented")
	}
}

// sdkError builds the error chain the AWS SDK really produces: a modeled or
// generic API error, wrapped in the transport error that carries the response,
// wrapped in the operation error.
func sdkError(status int, api error, header http.Header) error {
	if header == nil {
		header = http.Header{}
	}
	resp := &smithyhttp.Response{Response: &http.Response{StatusCode: status, Header: header}}
	return &smithy.OperationError{
		ServiceID:     "SQS",
		OperationName: "GetQueueAttributes",
		Err: &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{Response: resp, Err: api},
			RequestID:     "req",
		},
	}
}

func TestName_isRunIDGroupSuffix(t *testing.T) {
	b := &Binder{runID: "oc-abc", group: "sqs-gen-queue", bag: newContextBag()}
	got, err := b.eval(Name("q"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "oc-abc-sqs-gen-queue-q" {
		t.Errorf("Name(\"q\") = %v, want the whole group name between the run id and the suffix", got)
	}
}

func TestConcatAndIndex(t *testing.T) {
	bag := newContextBag()
	bag.set("dlq.arn", "arn:aws:sqs:us-east-1:000000000000:dlq")
	bag.set("queue.urls", []any{"a", "b"})
	b := &Binder{runID: "oc", group: "g", bag: bag}

	got, err := b.eval(Concat(`{"deadLetterTargetArn":"`, Ref("dlq.arn"), `"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dlq"}`
	if got != want {
		t.Errorf("Concat = %v, want %v", got, want)
	}

	if got, err := b.eval(Index(Ref("queue.urls"), 1)); err != nil || got != "b" {
		t.Errorf("Index = %v, %v; want b", got, err)
	}
	if _, err := b.eval(Index(Ref("queue.urls"), 5)); err == nil {
		t.Error("an index past the end of the list did not fail")
	}
}

func TestRefErrorNamesThePath(t *testing.T) {
	b := &Binder{runID: "oc", group: "g", bag: newContextBag()}
	_, err := b.eval(Ref("queue.url"))
	var ref *refError
	if !errors.As(err, &ref) || ref.path != "queue.url" {
		t.Fatalf("eval(Ref) = %v, want a refError naming queue.url", err)
	}
	if !strings.Contains(fmt.Sprint(err), `"queue.url" is not set`) {
		t.Errorf("refError reads %q", err)
	}
}
