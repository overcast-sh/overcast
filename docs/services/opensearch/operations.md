---
title: "OpenSearch operations"
description: "Every OpenSearch operation Overcast declares — 8 of 8 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - opensearch
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# OpenSearch operations

All 8 listed operations are implemented. Back to [OpenSearch](../opensearch.md).

## Summary

| Category | ✅ Supported |
| -------- | ------------ |
| Domains  | 5            |
| Tags     | 3            |

---

## Endpoints

### Domains

| Operation         | Status       | Notes                                                                                                                                                                                                 | AWS Docs                                                                                            |
| ----------------- | ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `CreateDomain`    | ✅ Supported | creates a domain, immediately active; `DomainName` and `EngineVersion` are checked against their modeled patterns; inline `TagList` applied at creation; a repeat name in the same region is rejected | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_CreateDomain.html)    |
| `DescribeDomain`  | ✅ Supported | returns the stored domain: every required member plus the others an inert domain can populate honestly; `Endpoint` is an AWS-shaped hostname that serves nothing                                      | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_DescribeDomain.html)  |
| `DescribeDomains` | ✅ Supported | batch describe; a name that matches nothing is omitted from the list                                                                                                                                  | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_DescribeDomains.html) |
| `ListDomainNames` | ✅ Supported | lists the region's domains; the `engineType` filter is honoured, derived from each domain's `EngineVersion`                                                                                           | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_ListDomainNames.html) |
| `DeleteDomain`    | ✅ Supported | deletes a domain and the tags attached to it                                                                                                                                                          | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_DeleteDomain.html)    |

### Tags

| Operation    | Status       | Notes                                                             | AWS Docs                                                                                       |
| ------------ | ------------ | ----------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `AddTags`    | ✅ Supported | adds tags to a resource, addressed by ARN in the body             | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_AddTags.html)    |
| `ListTags`   | ✅ Supported | lists tags for a resource, addressed by the `arn` query parameter | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_ListTags.html)   |
| `RemoveTags` | ✅ Supported | removes the named tag keys from a resource                        | [docs](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/API_RemoveTags.html) |

## Related

- [OpenSearch](../opensearch.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
