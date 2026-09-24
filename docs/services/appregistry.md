---
title: "AppRegistry — Service Catalog AppRegistry"
description: "Quick start, the resource and CloudFormation coverage, how CDK awsApplication tags become associations, and why an AppRegistry call must be signed as servicecatalog."
section: "Service Reference"
tags:
  - appregistry
  - docs
  - service-catalog
  - services
---

# AppRegistry — Service Catalog AppRegistry

Groups related resources into named applications, and associates CloudFormation
stacks and CDK-tagged resources with them automatically.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

APP=$(aws servicecatalog-appregistry create-application --name my-app \
  --query application.id --output text)

aws servicecatalog-appregistry associate-resource \
  --application "$APP" --resource-type CFN_STACK --resource my-stack

aws servicecatalog-appregistry list-associated-resources --application "$APP"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Resources | Applications, attribute groups, resource associations and tagging — all 22 modelled operations, over REST-JSON under `/applications` |
| CloudFormation | `AWS::ServiceCatalogAppRegistry::Application`, `::ResourceAssociation`, `::AttributeGroup` and `::AttributeGroupAssociation` are provisioned resource types |
| CDK `awsApplication` tags | Recorded as direct associations during a deploy |
| Web console | A resource detail page shows a "belongs to application X" banner when a match is found |

### CloudFormation attributes

The application exposes `Id`, `Arn`, `Name`, `ApplicationName`,
`ApplicationTagKey` and `ApplicationTagValue` to `Fn::GetAtt`; an attribute
group exposes `Id` and `Arn`. A resource association's physical ID is
`<appId>/<resourceType>/<resource>`, and `ResourceType` defaults to
`CFN_STACK`; an attribute-group association's is `<appId>/<attributeGroupId>`,
and — since both its properties are replacement-only on AWS — a template
change to either always replaces it.

An application's or attribute group's `Tags` merge with the stack's own tags
on create, and a stack update reconciles a tag change through `TagResource`/
`UntagResource` against the resource's ARN.

### CDK `awsApplication` tags

The provisioner scans each resource's `Tags` for the `awsApplication=<app-arn>`
entry that CDK's `Application` L2 construct propagates, and records a direct
association immediately after provisioning under AWS's `RESOURCE_TAG_VALUE`
resource type. Those resources come back from `ListAssociatedResources` without
the console having to expand the parent stack.

## Differences from AWS

| Area                                                              | On AWS                                     | Overcast                                                                                         |
| ----------------------------------------------------------------- | ------------------------------------------ | ------------------------------------------------------------------------------------------------ |
| Enforcement                                                       | Integrated with Service Catalog governance | Associations are records. Nothing about an application governs, restricts or provisions anything |
| `awsApplication` tag scan                                         | Continuous                                 | Runs on resource **create** only, and a failure is logged rather than failing the stack          |
| Application-scoped cost, resource groups, attribute-group syncing | Supported                                  | Not modelled                                                                                     |
| Pagination on `ListApplications`, `ListAssociatedResources`, `ListAttributeGroups`, `ListAssociatedAttributeGroups` | Results are paginated | `maxResults`/`nextToken` are accepted but ignored; every call returns the full result set in one page and `nextToken` is never set |

## Gotchas

> [!IMPORTANT]
> `/applications` is shared with [AppConfig](./appconfig.md), which models the same
> path tree. Overcast picks the service from the SigV4 credential scope: a request
> signed as `servicecatalog` reaches AppRegistry, one signed as `appconfig` reaches
> AppConfig, and an unsigned or unparseable request reaches AppRegistry. Every AWS
> SDK and the CLI sign correctly.

<!-- BEGIN overcast:capabilities -->

## Operations

All 24 listed operations are implemented.
Per-operation status, notes and AWS API links: [AppRegistry operations](appregistry/operations.md).

<!-- END overcast:capabilities -->

## Related

- [CloudFormation](./cloudformation.md) — what creates most associations
- [AppConfig](./appconfig.md) — the other service on `/applications`
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/servicecatalog/latest/dg/applications.html)
