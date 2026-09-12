package cloudformation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// stack_output_refs_test.go — Fn::GetStackOutput, the weak cross-stack
// reference: how its arguments resolve, how a failure reaches the resource
// that made it, and what the provisioner's lookup answers for a stack in
// another region, a missing stack, a missing output and a stack being deleted.

// recordingStackOutput is a resolveContext.StackOutput that remembers what it
// was asked and answers from a fixed table.
type recordingStackOutput struct {
	asked  []stackOutputRef
	values map[string]string // "<region>/<stack>/<output>" → value
}

func (r *recordingStackOutput) resolve(ref stackOutputRef) (string, error) {
	r.asked = append(r.asked, ref)
	if v, ok := r.values[ref.Region+"/"+ref.StackName+"/"+ref.OutputName]; ok {
		return v, nil
	}
	return "", errors.New("stack " + ref.StackName + " does not exist in " + ref.Region)
}

func stackOutputCtx(t *testing.T) (*resolveContext, *recordingStackOutput) {
	t.Helper()
	rec := &recordingStackOutput{values: map[string]string{
		"us-east-1/Producer/VpcId": "vpc-east",
		"us-west-2/Producer/VpcId": "vpc-west",
	}}
	ctx := testCtx()
	ctx.Params["ProducerName"] = "Producer"
	ctx.Params["ProducerRegion"] = "us-west-2"
	ctx.Conditions = map[string]bool{"West": true}
	ctx.StackOutput = rec.resolve
	return ctx, rec
}

// The arguments resolve like any other intrinsic's, Region defaults to the
// consuming stack's, and the value lands wherever the reference was written.
func TestGetStackOutput_resolvesArgumentsAndDefaultsRegion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fragment string
		want     any
		asked    stackOutputRef
	}{
		{
			name:     "same region by default",
			fragment: `{"Fn::GetStackOutput": {"StackName": "Producer", "OutputName": "VpcId"}}`,
			want:     "vpc-east",
			asked:    stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-east-1"},
		},
		{
			name:     "another region, and a RoleArn passed through",
			fragment: `{"Fn::GetStackOutput": {"StackName": "Producer", "OutputName": "VpcId", "Region": "us-west-2", "RoleArn": "arn:aws:iam::111111111111:role/Reader"}}`,
			want:     "vpc-west",
			asked:    stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-west-2", RoleArn: "arn:aws:iam::111111111111:role/Reader"},
		},
		{
			name:     "StackName and Region from parameters — the documented Ref idiom",
			fragment: `{"Fn::GetStackOutput": {"StackName": {"Ref": "ProducerName"}, "OutputName": "VpcId", "Region": {"Ref": "ProducerRegion"}}}`,
			want:     "vpc-west",
			asked:    stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-west-2"},
		},
		{
			name:     "StackName built with Fn::Sub from a pseudo-parameter",
			fragment: `{"Fn::GetStackOutput": {"StackName": {"Fn::Sub": "Produ${AWS::NoValue}cer"}, "OutputName": "VpcId"}}`,
			want:     "vpc-east",
			asked:    stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-east-1"},
		},
		{
			name:     "inside Fn::Join",
			fragment: `{"Fn::Join": ["/", ["prefix", {"Fn::GetStackOutput": {"StackName": "Producer", "OutputName": "VpcId"}}]]}`,
			want:     "prefix/vpc-east",
			asked:    stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-east-1"},
		},
		{
			name:     "inside Fn::If",
			fragment: `{"Fn::If": ["West", {"Fn::GetStackOutput": {"StackName": "Producer", "OutputName": "VpcId", "Region": "us-west-2"}}, "unused"]}`,
			want:     "vpc-west",
			asked:    stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-west-2"},
		},
		{
			name:     "inside Fn::Select",
			fragment: `{"Fn::Select": [1, ["a", {"Fn::GetStackOutput": {"StackName": "Producer", "OutputName": "VpcId"}}]]}`,
			want:     "vpc-east",
			asked:    stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-east-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a context whose lookup answers for Producer in two regions
			ctx, rec := stackOutputCtx(t)

			// When: the fragment is resolved
			got := resolveJSON(t, tc.fragment, ctx)

			// Then: the value is in place, the lookup was asked exactly what
			// the template said, and nothing was recorded as failed
			if got != tc.want {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			if len(rec.asked) != 1 || rec.asked[0] != tc.asked {
				t.Fatalf("lookup asked %+v, want exactly %+v", rec.asked, tc.asked)
			}
			if err := ctx.takeDynamicRefErr(); err != nil {
				t.Fatalf("unexpected recorded failure: %v", err)
			}
		})
	}
}

// A reference that cannot be resolved does not leave a stray value behind: it
// resolves empty and records the failure for the resource to take.
func TestGetStackOutput_recordsTheFailureForTheResource(t *testing.T) {
	// Given: a lookup that has no such stack
	ctx, _ := stackOutputCtx(t)

	// When: the reference is resolved
	got := resolveJSON(t, `{"Fn::GetStackOutput": {"StackName": "Missing", "OutputName": "VpcId"}}`, ctx)

	// Then: the property is empty and the failure names the stack
	if got != "" {
		t.Fatalf("got %#v, want the empty string", got)
	}
	err := ctx.takeDynamicRefErr()
	if err == nil || !strings.Contains(err.Error(), "Missing does not exist in us-east-1") {
		t.Fatalf("recorded failure = %v, want the lookup's own error", err)
	}
	// And: taking it cleared it
	if err := ctx.takeDynamicRefErr(); err != nil {
		t.Fatalf("failure still recorded after being taken: %v", err)
	}
}

// Malformed references are refused before any lookup, so a template that
// would fail in the account fails here first.
func TestGetStackOutput_refusesMalformedReferences(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fragment string
		wantErr  string
	}{
		{
			name:     "not a map",
			fragment: `{"Fn::GetStackOutput": "Producer.VpcId"}`,
			wantErr:  "expected a map",
		},
		{
			name:     "OutputName missing",
			fragment: `{"Fn::GetStackOutput": {"StackName": "Producer"}}`,
			wantErr:  "StackName and OutputName are required",
		},
		{
			name:     "StackName resolves empty",
			fragment: `{"Fn::GetStackOutput": {"StackName": {"Ref": "Undefined"}, "OutputName": "VpcId"}}`,
			wantErr:  "StackName and OutputName are required",
		},
		{
			name:     "a parameter AWS does not define",
			fragment: `{"Fn::GetStackOutput": {"StackName": "Producer", "OutputName": "VpcId", "OutputKey": "VpcId"}}`,
			wantErr:  `"OutputKey" is not a parameter`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a context with a working lookup
			ctx, rec := stackOutputCtx(t)
			if tc.name == "StackName resolves empty" {
				ctx.Params["Undefined"] = ""
			}

			// When: the malformed reference is resolved
			got := resolveJSON(t, tc.fragment, ctx)

			// Then: it resolves empty, records why, and never reached the lookup
			if got != "" {
				t.Fatalf("got %#v, want the empty string", got)
			}
			err := ctx.takeDynamicRefErr()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("recorded failure = %v, want one containing %q", err, tc.wantErr)
			}
			if len(rec.asked) != 0 {
				t.Fatalf("lookup was asked %+v; a malformed reference must not be looked up", rec.asked)
			}
		})
	}
}

// Outside provisioning there is nothing to look a stack up in; that is a
// recorded failure, not an empty property nobody explained.
func TestGetStackOutput_withoutALookupRecordsAFailure(t *testing.T) {
	// Given: a context with no StackOutput resolver
	ctx := testCtx()

	// When: a reference is resolved
	got := resolveJSON(t, `{"Fn::GetStackOutput": {"StackName": "Producer", "OutputName": "VpcId"}}`, ctx)

	// Then: it is empty and the failure says so
	if got != "" {
		t.Fatalf("got %#v, want the empty string", got)
	}
	if err := ctx.takeDynamicRefErr(); err == nil || !strings.Contains(err.Error(), "outside provisioning") {
		t.Fatalf("recorded failure = %v, want one about provisioning", err)
	}
}

// The YAML short form tags a mapping — the one intrinsic that does — and the
// full form may carry other intrinsics as parameter values.
func TestParseTemplateYAML_getStackOutputForms(t *testing.T) {
	// Given: a template using both forms
	tmpl, err := parseTemplateYAML(`
Parameters:
  Source:
    Type: String
Resources:
  Short:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: !GetStackOutput
        StackName: Producer
        OutputName: QueueName
        Region: us-west-2
  Full:
    Type: AWS::SQS::Queue
    Properties:
      QueueName:
        Fn::GetStackOutput:
          StackName: !Ref Source
          OutputName: QueueName
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// When: both properties are resolved against a lookup
	ctx, rec := stackOutputCtx(t)
	ctx.Params["Source"] = "Producer"
	rec.values["us-west-2/Producer/QueueName"] = "west-queue"
	rec.values["us-east-1/Producer/QueueName"] = "east-queue"
	short := resolveIntrinsics(tmpl.Resources["Short"].Properties["QueueName"], ctx)
	full := resolveIntrinsics(tmpl.Resources["Full"].Properties["QueueName"], ctx)

	// Then: the short form parsed as the function, and the Ref inside the
	// full form resolved before the lookup
	if short != "west-queue" || full != "east-queue" {
		t.Fatalf("short = %#v, full = %#v; want west-queue and east-queue", short, full)
	}
	if err := ctx.takeDynamicRefErr(); err != nil {
		t.Fatalf("unexpected recorded failure: %v", err)
	}
}

// putStackInRegion stores a stack as the provisioner would find it.
func putStackInRegion(t *testing.T, p *provisioner, region string, stack *Stack) {
	t.Helper()
	if err := p.store.putStack(middleware.ContextWithRegion(context.Background(), region), stack); err != nil {
		t.Fatalf("put stack %s in %s: %v", stack.StackName, region, err)
	}
}

// The provisioner's lookup reads the stack in the region the reference names,
// whether or not the output is exported, and by name or by stack ID.
func TestStackOutputResolver_readsAStackInAnotherRegion(t *testing.T) {
	// Given: a producer in us-west-2 with an unexported output
	p, _ := newTestProvisioner(t, nil)
	putStackInRegion(t, p, "us-west-2", &Stack{
		StackName: "Producer",
		StackID:   "arn:aws:cloudformation:us-west-2:000000000000:stack/Producer/11111111-1111-1111-1111-111111111111",
		Status:    StatusCreateComplete,
		Outputs:   []Output{{Key: "VpcId", Value: "vpc-west"}},
	})
	resolve := p.stackOutputResolver()

	for _, tc := range []struct {
		name string
		ref  stackOutputRef
	}{
		{"by name", stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-west-2"}},
		{"by stack ID", stackOutputRef{StackName: "arn:aws:cloudformation:us-west-2:000000000000:stack/Producer/11111111-1111-1111-1111-111111111111", OutputName: "VpcId", Region: "us-west-2"}},
		{"through a role in another account — one account is all there is", stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-west-2", RoleArn: "arn:aws:iam::111111111111:role/GetStackOutputRole"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When: the reference is looked up
			got, err := resolve(tc.ref)

			// Then: the output's value comes back
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != "vpc-west" {
				t.Fatalf("got %q, want vpc-west", got)
			}
		})
	}

	// And: the same name in the consuming region is a different stack
	if _, err := resolve(stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-east-1"}); err == nil ||
		!strings.Contains(err.Error(), `stack "Producer" does not exist in us-east-1`) {
		t.Fatalf("us-east-1 lookup = %v, want stack-not-found there", err)
	}
}

// Each way a reference can dangle is its own error, naming what was looked
// for and where — the stack event is all a deploy gets to see.
func TestStackOutputResolver_namesWhatIsMissing(t *testing.T) {
	// Given: a producer with one output, and a producer being deleted
	p, _ := newTestProvisioner(t, nil)
	putStackInRegion(t, p, "us-east-1", &Stack{
		StackName: "Producer", StackID: "arn:aws:cloudformation:us-east-1:000000000000:stack/Producer/1",
		Status:  StatusCreateComplete,
		Outputs: []Output{{Key: "VpcId", Value: "vpc-east"}, {Key: "SubnetId", Value: "subnet-east"}},
	})
	putStackInRegion(t, p, "us-east-1", &Stack{
		StackName: "Leaving", StackID: "arn:aws:cloudformation:us-east-1:000000000000:stack/Leaving/1",
		Status:  StatusDeleteInProgress,
		Outputs: []Output{{Key: "VpcId", Value: "vpc-gone"}},
	})
	resolve := p.stackOutputResolver()

	for _, tc := range []struct {
		name    string
		ref     stackOutputRef
		wantErr string
	}{
		{
			name:    "no such stack",
			ref:     stackOutputRef{StackName: "Nope", OutputName: "VpcId", Region: "us-east-1"},
			wantErr: `stack "Nope" does not exist in us-east-1`,
		},
		{
			name:    "no such output, with the outputs it does have",
			ref:     stackOutputRef{StackName: "Producer", OutputName: "VpcID", Region: "us-east-1"},
			wantErr: `output "VpcID" not found on stack "Producer" in us-east-1 (has SubnetId, VpcId)`,
		},
		{
			name:    "a stack on its way out",
			ref:     stackOutputRef{StackName: "Leaving", OutputName: "VpcId", Region: "us-east-1"},
			wantErr: `stack "Leaving" does not exist in us-east-1`,
		},
		{
			name:    "a region that is not one",
			ref:     stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "West"},
			wantErr: `region "West" is not valid`,
		},
		{
			name:    "a RoleArn that is not a role",
			ref:     stackOutputRef{StackName: "Producer", OutputName: "VpcId", Region: "us-east-1", RoleArn: "GetStackOutputRole"},
			wantErr: `RoleArn "GetStackOutputRole" is not an IAM role ARN`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When: the reference is looked up
			_, err := resolve(tc.ref)

			// Then: the error says what was missing
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("resolve = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}
