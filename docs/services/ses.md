---
title: "SES — Simple Email Service"
description: "Quick start, what lands in the console Inbox and how to read it, the v1 and v2 sending surface, and the verification, quotas and configuration sets that are skipped."
section: "Service Reference"
tags:
  - docs
  - email
  - service
  - services
  - ses
  - simple
---

# SES — Simple Email Service

Send with the v1 or v2 API and the message lands in the console's Inbox
instead of a mailbox. Nothing is delivered off the machine.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws ses verify-email-identity --email-address app@example.com
aws ses send-email \
  --from app@example.com \
  --destination ToAddresses=someone@example.com \
  --message 'Subject={Data=Hello},Body={Text={Data=It works}}'
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

Open <http://localhost:4567/inbox> to read it.

## What lands in the Inbox

Overcast runs an SMTP server on port `1025` (`OVERCAST_SMTP_PORT`) and captures
everything sent through it, plus deliveries that never touch SMTP at all:

| Source                                | Appears as                        |
| ------------------------------------- | --------------------------------- |
| SES `SendEmail`, `SendRawEmail`, `SendTemplatedEmail` | An email               |
| SNS `email` / `email-json` subscriptions | An email, threaded by publish   |
| SNS `sms` subscriptions and `Publish --phone-number` | An SMS                 |
| SNS `http` / `https` subscriptions    | A captured webhook delivery       |
| Cognito confirmation, invite and reset messages | An email or an SMS      |

The same messages are available over HTTP at
`GET /_overcast/ses/inbox/messages`. The buffer holds the most recent 500
messages (`OVERCAST_SMTP_INBOX_MAX`); set `OVERCAST_SMTP_HOST` to relay to a
real server instead of capturing. A test suite written against LocalStack's
`GET /_aws/ses` works unchanged: the emails are served there too, in
LocalStack's shape — see
[Debug endpoints § Compatibility aliases](../debug-endpoints.md#compatibility-aliases).

## What works

| Area          | Behaviour                                                                              |
| ------------- | ---------------------------------------------------------------------------------------- |
| Sending (v1)  | `SendEmail`, `SendRawEmail` (raw MIME), `SendTemplatedEmail`                             |
| Sending (v2)  | `SendEmail` at `POST /v2/email/outbound-emails`                                          |
| Templates     | Full CRUD plus `{{key}}` substitution on send                                            |
| Identities    | v1 verify/list/delete and the v2 `CreateEmailIdentity` family, with inline `Tags`         |
| Tags (v2)     | `TagResource`, `UntagResource`, `ListTagsForResource` by identity ARN                    |
| Console       | The [SES page](http://localhost:4567/ses) adds and removes identities                    |
| CloudFormation | `AWS::SES::Template` provisions, updates via `UpdateTemplate` and deletes; `AWS::SES::EmailIdentity` provisions, deletes and syncs tags — `DkimAttributes`, `DkimSigningAttributes`, `ConfigurationSetAttributes` and `FeedbackAttributes`/`MailFromAttributes` are accepted and dropped, matching the DKIM/MAIL FROM row below |

## Differences from AWS

| Area                                 | On AWS                                                | Overcast                                                                 |
| ------------------------------------ | ----------------------------------------------------- | ------------------------------------------------------------------------ |
| Delivery                             | The message reaches the recipient                     | Captured in the Inbox; nothing is sent                                   |
| Identity verification                | A DNS or email round-trip, then `Pending` → `Success` | Auto-verified; `GetIdentityVerificationAttributes` always says `Success` |
| Unverified senders                   | `MessageRejected`                                     | Accepted — the sending identity is not checked                           |
| Sandbox and quotas                   | Enforced; `GetSendQuota` reports real limits          | Unlimited quota, empty send statistics                                   |
| DKIM, MAIL FROM, notification topics | Configurable per identity                             | Not implemented — `501 Not Implemented`                                  |
| Configuration sets and receipt rules | Full API                                              | Not implemented — `501 Not Implemented`                                  |

## Gotchas

> [!NOTE]
> The console's SES page manages identities only. Sent mail is in the Inbox,
> not on that page.

A second Overcast on the same host finds `1025` taken and moves its capture
server to an ephemeral port; the Inbox follows, and the startup log says where
it went. Only a pinned `OVERCAST_SMTP_PORT` that is taken leaves capture
unavailable — sends then fail naming the variable, and `/_overcast/health`
reports the failed listener. See
[Running two instances on one host](../configuration/two-instances.md).

<!-- BEGIN overcast:capabilities -->

## Operations

27 of 45 listed operations are implemented.
Per-operation status, notes and AWS API links: [SES operations](ses/operations.md).

<!-- END overcast:capabilities -->

## Related

- [SNS](./sns.md) — email, SMS and webhook subscriptions land in the same Inbox
- [Cognito](./cognito.md) — user pool mail lands there too
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/ses/latest/APIReference/Welcome.html)
