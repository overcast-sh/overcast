---
title: "Checking a running daemon"
description: "overcast status, wait, services and config: is the daemon up, is it ready yet, which services are enabled, and what configuration did it actually resolve."
section: "Reference"
tags:
  - cli
  - config
  - docs
  - overcast
  - services
  - status
---

# Checking a running daemon

Four read-only commands against `--endpoint`: `status` for a one-line answer,
`wait` for a script that has to block, `services` for what is enabled, and
`config` for what the daemon resolved.

```bash
overcast status
overcast wait --timeout 30s
```

Part of the [CLI reference](../cli.md).

## `overcast status`

Pings `/_overcast/health` and prints one line — reachability, version, storage
backend. When Athena is enabled, a second line gives its query engine's state
(off, starting, ready or stopped-idle), the memory it is given and how long it
has been up. When the local instance registry is not empty it also prints a table
of every instance [`overcast start`](./daemon.md#overcast-start) knows about —
name, backend, endpoint and lifecycle state — whichever one `--endpoint`
pointed at.

```bash
overcast status
overcast status --endpoint http://localhost:4570
# overcast OK at http://localhost:4566 (version 0.0.1, storage memory)
# athena engine: ready, memory 1.0 GiB, up 4m12s
```

A starting engine is how the first query begins, so it never makes the
daemon report anything but OK.

## `overcast wait`

Blocks until the daemon at `--endpoint` reports healthy, then exits 0. This is
what a CI job runs after starting `overcast serve` in the background, before
the first AWS call; `status` checks once and returns.

| Flag | Default | Description |
| --- | --- | --- |
| `--timeout` | `60s` | Give up waiting after this long. |
| `--interval` | `500ms` | How often to poll the health endpoint. |
| `--quiet` | `false` | Print nothing on success. |

```bash
overcast wait --timeout 30s
```

## `overcast services`

Lists the AWS services the daemon has enabled and the runtime emulation tier of
each — full, partial, inert or stub, explained in
[Reference index § Runtime emulation tiers](../README.md#runtime-emulation-tiers).

| Flag | Default | Description |
| --- | --- | --- |
| `--output` | `text` | `text` (aligned table) or `json`. |

```bash
overcast services
overcast services --output json | jq '.services[] | select(.tier=="stub")'
```

## `overcast config`

Prints the running daemon's effective configuration.

> [!IMPORTANT]
> The daemon has to have been started with `OVERCAST_DEBUG=true`. Without it
> the command returns an error naming the variable rather than a bare 404.

| Flag | Default | Description |
| --- | --- | --- |
| `--output` | `pretty` | `pretty` (indented) or `json` (exactly what the daemon sent). |

```bash
OVERCAST_DEBUG=true overcast serve &
overcast config
```

## Related

- [Running the daemon](./daemon.md) — `serve`, `start`, `stop`, `restart`, `logs`
- [Debug endpoints](../debug-endpoints.md) — the HTTP surface these commands read
- [Environment variable reference](../configuration/reference.md) — what `overcast config` is showing you
