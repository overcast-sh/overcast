---
title: "CloudFormation operations"
description: "Every CloudFormation operation Overcast declares — 25 of 53 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - cloudformation
  - docs
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# CloudFormation operations

25 of 53 listed operations are implemented. Back to [CloudFormation](../cloudformation.md).

## Summary

| Category             | ✅ Supported | ❌ Unsupported |
| -------------------- | ------------ | -------------- |
| Stacks               | 7            | 5              |
| Change sets          | 5            |                |
| Resources and events | 3            |                |
| Templates            | 3            | 1              |
| Exports              | 2            |                |
| Intrinsic functions  | 2            |                |
| Dynamic references   | 3            | 1              |
| Resource types       |              | 1              |
| StackSets            |              | 13             |
| Type registry        |              | 7              |

---

## Endpoints

### Stacks

| Operation                | Status         | Notes                                                                                                                                       | AWS Docs                                                                                                  |
| ------------------------ | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `CreateStack`            | ✅ Supported   | Async provisioner; JSON templates; intrinsic functions                                                                                      | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStack.html)            |
| `UpdateStack`            | ✅ Supported   | Re-provisions with updated template                                                                                                         | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_UpdateStack.html)            |
| `RollbackStack`          | ✅ Supported   | Rolls a CREATE_FAILED, UPDATE_FAILED, or UPDATE_ROLLBACK_FAILED stack back to a terminal rollback state                                     | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_RollbackStack.html)          |
| `ContinueUpdateRollback` | ✅ Supported   | Retries a failed update rollback from UPDATE_ROLLBACK_FAILED; ResourcesToSkip, including nested-stack paths                                 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ContinueUpdateRollback.html) |
| `DeleteStack`            | ✅ Supported   | Async resource cleanup in reverse dependency order; DELETE_FAILED when a resource refuses deletion                                          | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeleteStack.html)            |
| `DescribeStacks`         | ✅ Supported   | Status, parameters, outputs, tags                                                                                                           | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStacks.html)         |
| `ListStacks`             | ✅ Supported   | StackStatusFilter, validated against the full StackStatus enum; unfiltered lists include DELETE_COMPLETE; summaries carry StackStatusReason | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListStacks.html)             |
| `CancelUpdateStack`      | ❌ Unsupported | stub; returns 501                                                                                                                           | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CancelUpdateStack.html)      |
| `SignalResource`         | ❌ Unsupported | stub; returns 501                                                                                                                           | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_SignalResource.html)         |
| `GetStackPolicy`         | ❌ Unsupported | stub; returns 501                                                                                                                           | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_GetStackPolicy.html)         |
| `SetStackPolicy`         | ❌ Unsupported | stub; returns 501                                                                                                                           | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_SetStackPolicy.html)         |
| `DescribeAccountLimits`  | ❌ Unsupported | stub; returns 501                                                                                                                           | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeAccountLimits.html)  |

### Change sets

| Operation           | Status       | Notes                                                               | AWS Docs                                                                                             |
| ------------------- | ------------ | ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `CreateChangeSet`   | ✅ Supported | Creates a change set from a template                                | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateChangeSet.html)   |
| `DescribeChangeSet` | ✅ Supported | Returns change set details and status; accepts ARN-only lookup      | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeChangeSet.html) |
| `ExecuteChangeSet`  | ✅ Supported | Provisions resources via async provisioner; accepts ARN-only lookup | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ExecuteChangeSet.html)  |
| `DeleteChangeSet`   | ✅ Supported |                                                                     | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeleteChangeSet.html)   |
| `ListChangeSets`    | ✅ Supported | Lists active change sets for a stack                                | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListChangeSets.html)    |

### Resources and events

| Operation                | Status       | Notes                                               | AWS Docs                                                                                                  |
| ------------------------ | ------------ | --------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `DescribeStackResources` | ✅ Supported | Lists resources for a stack, with status and reason | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStackResources.html) |
| `ListStackResources`     | ✅ Supported | Lists resources with status                         | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListStackResources.html)     |
| `DescribeStackEvents`    | ✅ Supported | Returns stack provisioning events                   | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStackEvents.html)    |

### Templates

| Operation              | Status         | Notes                                 | AWS Docs                                                                                                |
| ---------------------- | -------------- | ------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `GetTemplate`          | ✅ Supported   | Returns the stack's template body     | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_GetTemplate.html)          |
| `GetTemplateSummary`   | ✅ Supported   | Returns parameters and resource types | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_GetTemplateSummary.html)   |
| `ValidateTemplate`     | ✅ Supported   | Validates template syntax             | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ValidateTemplate.html)     |
| `EstimateTemplateCost` | ❌ Unsupported | stub; returns 501                     | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_EstimateTemplateCost.html) |

### Exports

| Operation     | Status       | Notes                                            | AWS Docs                                                                                       |
| ------------- | ------------ | ------------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| `ListExports` | ✅ Supported | Returns exports from all active stacks in region | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListExports.html) |
| `ListImports` | ✅ Supported | Returns stacks that import a given export name   | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListImports.html) |

### Intrinsic functions

| Operation            | Status       | Notes                                                                                                               | AWS Docs                                                                                                                        |
| -------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `Fn::ImportValue`    | ✅ Supported | Cross-stack reference resolution                                                                                    | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/intrinsic-function-reference-importvalue.html)            |
| `Fn::GetStackOutput` | ✅ Supported | Weak cross-stack reference: any output of a stack in any region, no export needed; RoleArn accepted but not assumed | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-getstackoutput.html) |

### Dynamic references

| Operation                    | Status         | Notes                                                                                                          | AWS Docs                                                                                                                         |
| ---------------------------- | -------------- | -------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `{{resolve:secretsmanager}}` | ✅ Supported   | Secret by name or ARN; JSON key, version stage and version ID selectors                                        | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html#dynamic-references-secretsmanager) |
| `{{resolve:ssm}}`            | ✅ Supported   | Plaintext parameter; an explicit version resolves to the current value                                         | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html#dynamic-references-ssm)            |
| `{{resolve:ssm-secure}}`     | ✅ Supported   | Read with decryption, in the properties AWS enumerates only; an explicit version resolves to the current value | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html#dynamic-references-ssm-secure)     |
| `{{resolve:s3}}`             | ❌ Unsupported | Not resolved; fails the resource that uses it                                                                  | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references.html)                                   |

### Resource types

| Operation                                  | Status         | Notes | AWS Docs                                                                                                                    |
| ------------------------------------------ | -------------- | ----- | --------------------------------------------------------------------------------------------------------------------------- |
| `AWS::CloudFormation::WaitConditionHandle` | ❌ Unsupported | Stub  | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-cloudformation-waitconditionhandle.html) |

### StackSets

| Operation                      | Status         | Notes             | AWS Docs                                                                                                        |
| ------------------------------ | -------------- | ----------------- | --------------------------------------------------------------------------------------------------------------- |
| `CreateStackSet`               | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStackSet.html)               |
| `CreateStackInstances`         | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStackInstances.html)         |
| `DeleteStackSet`               | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeleteStackSet.html)               |
| `DeleteStackInstances`         | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeleteStackInstances.html)         |
| `DescribeStackSet`             | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStackSet.html)             |
| `DescribeStackInstance`        | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStackInstance.html)        |
| `DescribeStackSetOperation`    | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStackSetOperation.html)    |
| `ListStackSets`                | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListStackSets.html)                |
| `ListStackInstances`           | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListStackInstances.html)           |
| `ListStackSetOperations`       | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListStackSetOperations.html)       |
| `ListStackSetOperationResults` | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListStackSetOperationResults.html) |
| `UpdateStackSet`               | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_UpdateStackSet.html)               |
| `UpdateStackInstances`         | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_UpdateStackInstances.html)         |

### Type registry

| Operation                  | Status         | Notes             | AWS Docs                                                                                                    |
| -------------------------- | -------------- | ----------------- | ----------------------------------------------------------------------------------------------------------- |
| `RegisterType`             | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_RegisterType.html)             |
| `DeregisterType`           | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeregisterType.html)           |
| `DescribeType`             | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeType.html)             |
| `DescribeTypeRegistration` | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeTypeRegistration.html) |
| `ListTypes`                | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListTypes.html)                |
| `ListTypeRegistrations`    | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ListTypeRegistrations.html)    |
| `SetTypeDefaultVersion`    | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_SetTypeDefaultVersion.html)    |

## Related

- [CloudFormation](../cloudformation.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
