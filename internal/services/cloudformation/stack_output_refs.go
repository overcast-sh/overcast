package cloudformation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// stack_output_refs.go — Fn::GetStackOutput, the weak cross-stack reference.
//
// Fn::ImportValue is the strong reference: the producer exports, the consumer
// imports, and CloudFormation refuses to let the export go while anything
// imports it. Fn::GetStackOutput reads an output straight off the producing
// stack instead — any output, exported or not, in any region — and creates no
// coupling at all: "the referenced value is resolved at stack create or update
// time", and the producer may be changed or deleted afterwards without the
// consumer noticing until its next operation re-resolves the reference. CDK
// emits it for every cross-region or cross-account reference and for any
// reference a construct has marked ReferenceStrength.WEAK.
//
// Per AWS (https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-getstackoutput.html):
//
//	Fn::GetStackOutput:
//	  StackName: <name or stack ID>   required
//	  OutputName: <output logical ID> required
//	  Region: <region>                optional; defaults to the consumer's
//	  RoleArn: <IAM role ARN>         optional; defaults to the execution role
//
// The parameter values may themselves be Ref, Fn::Sub, Fn::Join, Fn::Select,
// Fn::If, Fn::FindInMap or Fn::Base64, as long as none of them depends on a
// resource — a reference is resolved before anything is provisioned, so a
// stack name built from a resource attribute would have nothing to read.
// Overcast resolves the arguments with the ordinary intrinsic walk, which
// covers that list and more.

// stackOutputRef is one Fn::GetStackOutput with its arguments resolved, ready
// for the provisioner to look up. Region is already defaulted to the
// consuming stack's.
type stackOutputRef struct {
	StackName  string
	OutputName string
	Region     string
	RoleArn    string
}

// stackOutputParams is every parameter Fn::GetStackOutput accepts. Anything
// else in the map is a typo AWS would reject, and is refused here for the
// same reason a misspelt property is: a template that deploys locally must
// not fail in the account.
var stackOutputParams = map[string]bool{
	"StackName":  true,
	"OutputName": true,
	"Region":     true,
	"RoleArn":    true,
}

// resolveGetStackOutput handles Fn::GetStackOutput.
//
// A reference that cannot be resolved records its failure on the context the
// way a dynamic reference does, and resolves to the empty string in the
// meantime. The provisioner takes the failure at the point it resolves the
// containing resource's properties and fails that resource with it — the
// "stack operation fails with a validation error" AWS documents, attributed
// to the resource that made the reference.
func resolveGetStackOutput(v any, ctx *resolveContext) any {
	args, ok := v.(map[string]any)
	if !ok {
		ctx.recordDynamicRefErr(fmt.Errorf("Fn::GetStackOutput: expected a map with StackName and OutputName, got %T", v))
		return ""
	}
	for key := range args {
		if !stackOutputParams[key] {
			ctx.recordDynamicRefErr(fmt.Errorf("Fn::GetStackOutput: %q is not a parameter (StackName, OutputName, Region, RoleArn)", key))
			return ""
		}
	}

	ref := stackOutputRef{
		StackName:  stackOutputArg(args, "StackName", ctx),
		OutputName: stackOutputArg(args, "OutputName", ctx),
		Region:     stackOutputArg(args, "Region", ctx),
		RoleArn:    stackOutputArg(args, "RoleArn", ctx),
	}
	if ref.StackName == "" || ref.OutputName == "" {
		ctx.recordDynamicRefErr(fmt.Errorf("Fn::GetStackOutput: StackName and OutputName are required"))
		return ""
	}
	if ref.Region == "" {
		ref.Region = ctx.Region
	}

	if ctx.StackOutput == nil {
		// Same reasoning as a dynamic reference resolved outside provisioning:
		// a recorded failure beats a silently empty property.
		ctx.recordDynamicRefErr(fmt.Errorf("Fn::GetStackOutput: stack %q output %q cannot be resolved outside provisioning", ref.StackName, ref.OutputName))
		return ""
	}
	value, err := ctx.StackOutput(ref)
	if err != nil {
		ctx.recordDynamicRefErr(err)
		return ""
	}
	return value
}

// stackOutputArg resolves one argument of the map to a string. An absent
// argument is the empty string; the caller decides whether that is allowed.
func stackOutputArg(args map[string]any, key string, ctx *resolveContext) string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	resolved := resolveIntrinsics(raw, ctx)
	if s, ok := resolved.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", resolved)
}

// regionPattern is the shape of an AWS region code: us-east-1,
// ap-southeast-2, us-gov-west-1, eu-isob-east-1. AWS fails the stack
// operation on a Region that is not a region at all ("Referenced Region is
// invalid"), and Overcast keys state on the string, so an unshaped value is
// refused before it is looked up rather than answered "stack not found".
var regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)

// iamRoleARNPattern is arn:<partition>:iam::<account>:role/<path-and-name>.
var iamRoleARNPattern = regexp.MustCompile(`^arn:aws[a-z0-9-]*:iam::\d{12}:role/[\w+=,.@/-]+$`)

// stackOutputResolver returns the lookup buildResolveContext installs as
// resolveContext.StackOutput: the reference read against the stacks this
// emulator holds.
//
// RoleArn is checked for shape and nothing more. On AWS it names the role in
// the producing account that CloudFormation assumes to describe the stack —
// the cross-account case. Overcast emulates one account and authenticates
// nothing (sts:AssumeRole hands back credentials for any role), so the only
// account a stack can be in is this one, and a reference through a role in
// another account reads it from here. That is the lenient side, and the
// right one: the CDK cannot deploy to two accounts against one Overcast in
// the first place, so a template written for two accounts and pointed here
// is being run in one on purpose.
func (p *provisioner) stackOutputResolver() func(stackOutputRef) (string, error) {
	return func(ref stackOutputRef) (string, error) {
		if !regionPattern.MatchString(ref.Region) {
			return "", fmt.Errorf("Fn::GetStackOutput: region %q is not valid", ref.Region)
		}
		if ref.RoleArn != "" && !iamRoleARNPattern.MatchString(ref.RoleArn) {
			return "", fmt.Errorf("Fn::GetStackOutput: RoleArn %q is not an IAM role ARN", ref.RoleArn)
		}

		ctx := middleware.ContextWithRegion(p.ctx, ref.Region)
		stack, aerr := p.store.getStackByNameOrARN(ctx, ref.StackName)
		if aerr != nil {
			return "", fmt.Errorf("Fn::GetStackOutput: reading stack %q in %s: %s", ref.StackName, ref.Region, aerr.Message)
		}
		// A stack on its way out is one AWS's DescribeStacks would still show,
		// but its outputs describe resources that are being torn down. Failing
		// here is what AWS's own "protect referenced stacks" guidance warns
		// the reference will do once the producer is gone.
		if stack == nil || stack.Status == StatusDeleteComplete || stack.Status == StatusDeleteInProgress {
			return "", fmt.Errorf("Fn::GetStackOutput: stack %q does not exist in %s", ref.StackName, ref.Region)
		}
		for _, o := range stack.Outputs {
			if o.Key == ref.OutputName {
				return o.Value, nil
			}
		}
		return "", fmt.Errorf("Fn::GetStackOutput: output %q not found on stack %q in %s (has %s)",
			ref.OutputName, ref.StackName, ref.Region, describeOutputKeys(stack.Outputs))
	}
}

// describeOutputKeys lists a stack's output keys for an error message, so a
// misspelt OutputName is diagnosable from the stack event alone.
func describeOutputKeys(outputs []Output) string {
	if len(outputs) == 0 {
		return "no outputs"
	}
	keys := make([]string, 0, len(outputs))
	for _, o := range outputs {
		keys = append(keys, o.Key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
