---
title: "ACM — AWS Certificate Manager"
description: "Quick start, the certificate and tag operations that work, and what a request skips: no validation round trip, no key material, no import or renewal."
section: "Service Reference"
tags:
  - acm
  - aws
  - certificate
  - docs
  - manager
  - services
---

# ACM — AWS Certificate Manager

Certificate records for stacks that need a certificate ARN to reference.
Nothing is issued cryptographically: a requested certificate is `ISSUED` on
return.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

CERT=$(aws acm request-certificate \
  --domain-name example.com \
  --validation-method DNS \
  --query CertificateArn --output text)

aws acm describe-certificate --certificate-arn "$CERT"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area         | Behaviour                                                                                                                 |
| ------------ | -------------------------------------------------------------------------------------------------------------------------- |
| Certificates | `RequestCertificate`, `DescribeCertificate`, `ListCertificates`, `ListCertificateDomainValidations`, `DeleteCertificate` |
| Tags         | The legacy `AddTagsToCertificate` family and the modern `TagResource` / `UntagResource` / `ListTagsForResource` aliases    |
| Inline tags  | `Tags` supplied on `RequestCertificate` are applied at creation                                                            |
| Validation records | `DescribeCertificate` returns one `DomainValidationOptions` entry per domain, with a CNAME to publish for `DNS`     |
| List filtering | `ListCertificates` honours `CertificateStatuses`                                                                        |
| Protocols    | AWS JSON 1.1, plus AWS JSON 1.0 and Smithy RPC v2 CBOR — accepting 1.0 alongside 1.1 is a framework-wide rule applied to every JSON-tier service, not an ACM-specific relaxation |

## Differences from AWS

| Area                        | On AWS                                                                                         | Overcast                                                                                                               |
| ---------------------------- | ------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| Validation                  | DNS or email round-trip; `PENDING_VALIDATION` first                                            | Skipped; the certificate is `ISSUED` on return                                                                        |
| Certificate material        | A real X.509 chain is issued                                                                   | No key or chain is generated                                                                                           |
| Validation records          | The CNAME in `DomainValidationOptions` is what proves you control the domain                   | Derived from the certificate ARN, already `SUCCESS`, and checked by nothing — publishing it changes no outcome        |
| Validation emails           | `DomainValidation.ValidationEmails` lists the addresses ACM wrote to                           | Omitted; no mail is sent, so there are no addresses to report                                                          |
| List filters                | `ListCertificates` also filters on key type, key usage, export option, and paginates           | Only `CertificateStatuses` is honoured; the rest select on material Overcast never generates                           |
| Domain validation summaries | `ListCertificateDomainValidations` reports the DNS or email challenge itself                   | Every domain is `SUCCESS` with the requested method and no challenge — the CNAME is on `DescribeCertificate`          |
| Import and renewal          | `ImportCertificate`, `RenewCertificate`, `ExportCertificate`                                   | Not implemented — `501 NotImplemented`                                                                                |

## Gotchas

> [!TIP]
> ACM issues nothing you can actually serve. For browser-trusted TLS in front
> of Overcast itself, see [HTTPS](../https.md).

<!-- BEGIN overcast:capabilities -->

## Operations

All 11 listed operations are implemented.
Per-operation status, notes and AWS API links: [ACM operations](acm/operations.md).

<!-- END overcast:capabilities -->

## Related

- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/acm/latest/APIReference/)
