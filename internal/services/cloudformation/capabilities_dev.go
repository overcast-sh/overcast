//go:build dev

package cloudformation

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Stacks
		capabilities.Capability{Service: "cloudformation", Operation: "CreateStack", Category: "Stacks", Status: capabilities.StatusSupported, Notes: "Async provisioner; JSON templates; intrinsic functions"},
		capabilities.Capability{Service: "cloudformation", Operation: "UpdateStack", Category: "Stacks", Status: capabilities.StatusSupported, Notes: "Re-provisions with updated template"},
		capabilities.Capability{Service: "cloudformation", Operation: "RollbackStack", Category: "Stacks", Status: capabilities.StatusSupported, Notes: "Rolls a CREATE_FAILED, UPDATE_FAILED, or UPDATE_ROLLBACK_FAILED stack back to a terminal rollback state"},
		capabilities.Capability{Service: "cloudformation", Operation: "ContinueUpdateRollback", Category: "Stacks", Status: capabilities.StatusSupported, Notes: "Retries a failed update rollback from UPDATE_ROLLBACK_FAILED; ResourcesToSkip, including nested-stack paths"},
		capabilities.Capability{Service: "cloudformation", Operation: "DeleteStack", Category: "Stacks", Status: capabilities.StatusSupported, Notes: "Async resource cleanup in reverse dependency order; DELETE_FAILED when a resource refuses deletion"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeStacks", Category: "Stacks", Status: capabilities.StatusSupported, Notes: "Status, parameters, outputs, tags"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListStacks", Category: "Stacks", Status: capabilities.StatusSupported, Notes: "StackStatusFilter, validated against the full StackStatus enum; unfiltered lists include DELETE_COMPLETE; summaries carry StackStatusReason"},
		capabilities.Capability{Service: "cloudformation", Operation: "CancelUpdateStack", Category: "Stacks", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "SignalResource", Category: "Stacks", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "GetStackPolicy", Category: "Stacks", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "SetStackPolicy", Category: "Stacks", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeAccountLimits", Category: "Stacks", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		// Change sets
		capabilities.Capability{Service: "cloudformation", Operation: "CreateChangeSet", Category: "Change sets", Status: capabilities.StatusSupported, Notes: "Creates a change set from a template"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeChangeSet", Category: "Change sets", Status: capabilities.StatusSupported, Notes: "Returns change set details and status; accepts ARN-only lookup"},
		capabilities.Capability{Service: "cloudformation", Operation: "ExecuteChangeSet", Category: "Change sets", Status: capabilities.StatusSupported, Notes: "Provisions resources via async provisioner; accepts ARN-only lookup"},
		capabilities.Capability{Service: "cloudformation", Operation: "DeleteChangeSet", Category: "Change sets", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "cloudformation", Operation: "ListChangeSets", Category: "Change sets", Status: capabilities.StatusSupported, Notes: "Lists active change sets for a stack"},
		// Resources and events
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeStackResources", Category: "Resources and events", Status: capabilities.StatusSupported, Notes: "Lists resources for a stack, with status and reason"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListStackResources", Category: "Resources and events", Status: capabilities.StatusSupported, Notes: "Lists resources with status"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeStackEvents", Category: "Resources and events", Status: capabilities.StatusSupported, Notes: "Returns stack provisioning events"},
		// Templates
		capabilities.Capability{Service: "cloudformation", Operation: "GetTemplate", Category: "Templates", Status: capabilities.StatusSupported, Notes: "Returns the stack's template body"},
		capabilities.Capability{Service: "cloudformation", Operation: "GetTemplateSummary", Category: "Templates", Status: capabilities.StatusSupported, Notes: "Returns parameters and resource types"},
		capabilities.Capability{Service: "cloudformation", Operation: "ValidateTemplate", Category: "Templates", Status: capabilities.StatusSupported, Notes: "Validates template syntax"},
		capabilities.Capability{Service: "cloudformation", Operation: "EstimateTemplateCost", Category: "Templates", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		// Exports
		capabilities.Capability{Service: "cloudformation", Operation: "ListExports", Category: "Exports", Status: capabilities.StatusSupported, Notes: "Returns exports from all active stacks in region"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListImports", Category: "Exports", Status: capabilities.StatusSupported, Notes: "Returns stacks that import a given export name"},
		capabilities.Capability{Service: "cloudformation", Operation: "Fn::ImportValue", Category: "Intrinsic functions", Status: capabilities.StatusSupported, Notes: "Cross-stack reference resolution", DocOnly: true,
			DocsURL: "[docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/intrinsic-function-reference-importvalue.html)"},
		capabilities.Capability{Service: "cloudformation", Operation: "Fn::GetStackOutput", Category: "Intrinsic functions", Status: capabilities.StatusSupported, Notes: "Weak cross-stack reference: any output of a stack in any region, no export needed; RoleArn accepted but not assumed", DocOnly: true,
			DocsURL: "[docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-getstackoutput.html)"},
		// Dynamic references — plain text in a property value, resolved at
		// deploy time against another service. Not intrinsic functions, and
		// listed separately because they are documented separately by AWS.
		capabilities.Capability{Service: "cloudformation", Operation: "{{resolve:secretsmanager}}", Category: "Dynamic references", Status: capabilities.StatusSupported, Notes: "Secret by name or ARN; JSON key, version stage and version ID selectors", DocOnly: true,
			DocsURL: "[docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html#dynamic-references-secretsmanager)"},
		capabilities.Capability{Service: "cloudformation", Operation: "{{resolve:ssm}}", Category: "Dynamic references", Status: capabilities.StatusSupported, Notes: "Plaintext parameter; an explicit version resolves to the current value", DocOnly: true,
			DocsURL: "[docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html#dynamic-references-ssm)"},
		capabilities.Capability{Service: "cloudformation", Operation: "{{resolve:ssm-secure}}", Category: "Dynamic references", Status: capabilities.StatusSupported, Notes: "Read with decryption, in the properties AWS enumerates only; an explicit version resolves to the current value", DocOnly: true,
			DocsURL: "[docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html#dynamic-references-ssm-secure)"},
		capabilities.Capability{Service: "cloudformation", Operation: "{{resolve:s3}}", Category: "Dynamic references", Status: capabilities.StatusUnsupported, Notes: "Not resolved; fails the resource that uses it", DocOnly: true,
			DocsURL: "[docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html)"},
		capabilities.Capability{Service: "cloudformation", Operation: "AWS::CloudFormation::WaitConditionHandle", Category: "Resource types", Status: capabilities.StatusUnsupported, Notes: "Stub", DocOnly: true,
			DocsURL: "[docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-cloudformation-waitconditionhandle.html)"},
		// StackSets
		capabilities.Capability{Service: "cloudformation", Operation: "CreateStackSet", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "CreateStackInstances", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DeleteStackSet", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DeleteStackInstances", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeStackSet", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeStackInstance", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeStackSetOperation", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListStackSets", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListStackInstances", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListStackSetOperations", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListStackSetOperationResults", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "UpdateStackSet", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "UpdateStackInstances", Category: "StackSets", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		// Type registry
		capabilities.Capability{Service: "cloudformation", Operation: "RegisterType", Category: "Type registry", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DeregisterType", Category: "Type registry", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeType", Category: "Type registry", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "DescribeTypeRegistration", Category: "Type registry", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListTypes", Category: "Type registry", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "ListTypeRegistrations", Category: "Type registry", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudformation", Operation: "SetTypeDefaultVersion", Category: "Type registry", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
	)
}
