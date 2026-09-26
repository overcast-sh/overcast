---
title: "Wiping and importing state"
description: "overcast reset wipes emulated state, all of it or one service's; overcast import cognito-users copies users out of a real AWS user pool into an Overcast one."
section: "Reference"
tags:
  - cli
  - cognito
  - docs
  - import
  - overcast
  - reset
  - state
---

# Wiping and importing state

`overcast reset` clears emulated state. `overcast import` is the one command
that reads from real AWS, and so far it copies Cognito users.

```bash
overcast reset athena glue s3
overcast import cognito-users --from-pool-id us-east-1_abc123 --to-pool-id us-east-1_def456
```

Part of the [CLI reference](../cli.md).

## `overcast reset [service...]`

Wipes all emulated state, or the state of each service named, via
`POST /_overcast/reset[/{service}]`. It is available on every daemon, not only
a debug one: it grants no more power than deleting every resource by hand
through the ordinary AWS API already does.

> [!CAUTION]
> This destroys state. In an interactive terminal you are asked to confirm
> unless `--yes`/`-y` is given; a non-interactive caller (CI, a pipe) proceeds
> without prompting.

```bash
overcast reset                # wipe everything, with a confirmation prompt
overcast reset s3             # wipe only S3 state
overcast reset athena glue s3 # wipe these three, such as the sample dataset
overcast reset dynamodb --yes # skip the prompt
```

## `overcast import cognito-users`

Copies users from a real AWS Cognito user pool into an Overcast pool, in
batches. Passwords cannot be read out of AWS, so imported users land in
`FORCE_CHANGE_PASSWORD` status.

| Flag | Default | Description |
| --- | --- | --- |
| `--from-pool-id` | — | **Required.** Source user pool ID in real AWS. |
| `--to-pool-id` | — | **Required.** Target user pool ID in Overcast. |
| `--from-profile` | — | AWS profile for the source account. |
| `--from-region` | _(auto)_ | Region for the source pool. |
| `--user` | — | Import one user by sub (UUID) instead of the whole pool. |
| `--max-users` | `0` | Cap on users imported (`0` = unlimited). |
| `--batch-size` | `100` | Users per batch sent to the daemon. |

```bash
overcast import cognito-users \
  --from-pool-id us-east-1_abc123 --to-pool-id us-east-1_def456
```

## Related

- [Cognito](../services/cognito.md) — what the target pool supports
- [Storage and persistence](../storage.md) — what survives a restart, and what `reset` is clearing
- [Debug endpoints](../debug-endpoints.md) — the rest of the `/_overcast/` surface
