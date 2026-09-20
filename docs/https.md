---
title: "HTTPS and HTTP/2"
description: "Two commands for browser-trusted TLS on the API and the web console, what HTTP/2 fixes in the console, and how to turn it off again."
section: "Networking"
tags:
  - docs
  - guide
  - https
  - tls
  - http2
  - certificates
---

# HTTPS and HTTP/2

Serve the Overcast API **and** the web console over browser-trusted HTTPS with
two commands. The payoff is **HTTP/2 for the web console**, which keeps the UI
responsive under load.

## Quick start

```bash
# 1. Once per machine: create the local CA, install it into the system
#    trust store, and mint the server certificate. Your OS will ask you to
#    approve the CA install — approving that prompt is the only manual step.
overcast https enable

# 2. Start the daemon with TLS on:
OVERCAST_TLS=auto overcast serve
```

Then open **<https://localhost.overcast.sh:4567>** — web console over
HTTPS + HTTP/2, no browser warnings. The API is on
`https://localhost.overcast.sh:4566` (and plain `https://localhost:4566`).

| Platform | What `overcast https enable` asks for |
| --- | --- |
| Windows | A certificate-store confirmation dialog. The CA goes into the *current user's* Root store; no administrator shell needed |
| macOS | A keychain authorisation prompt. The CA goes into your login keychain |
| Linux | Root, to write the system CA bundle: run `sudo overcast https enable`. Firefox and Chromium read their own NSS store — see [Installing the CA by hand](./https/manual-trust.md) |

Re-running `overcast https enable` is safe: it reports what is already in
place. `overcast https status` shows the current state, and
`overcast https disable` removes the CA from the trust store again.

The same flow is available UI-first at **Settings → HTTPS & certificates**
(the gear in the console header), including the Docker route, a CA certificate
download, and a **Switch to HTTPS** button once the https listener answers.

> [!IMPORTANT]
> Running Overcast in Docker? Run `overcast https enable` on the **host** and
> mount that CA into the container, so one trust install survives every
> recreation — see [Overcast in Docker over HTTPS](./https/docker.md).

## What HTTP/2 buys

- **The console stops queueing.** Browsers cap HTTP/1.1 at six connections per
  origin and never negotiate cleartext HTTP/2, so over plain HTTP the live
  event feed, every Lambda progress stream and every S3 transfer compete for
  six sockets. ALPN over TLS gives the console one multiplexed connection
  instead.
- **Trusted names with no hosts-file edits.** The certificate covers
  `localhost.overcast.sh` and `*.localhost.overcast.sh`, and host-routed invoke
  URLs (`{id}.execute-api.{region}.localhost.overcast.sh` and friends) are
  minted on first use — see
  [Hostnames that resolve for every caller](./networking/hostnames.md) and
  [How the local CA works](./https/how-it-works.md#which-names-the-certificate-covers).
- **Production parity.** SDK clients and tools that insist on a TLS endpoint
  point at Overcast unchanged.

The API is fine over plain HTTP for SDKs and the CLI: they are not subject to
the browser cap, use keep-alive, and can use h2c, which Overcast's plain
listener accepts as a prior-knowledge HTTP/2 connection.

## Turning it off

```bash
overcast https disable   # removes the CA from the trust store
```

…and unset `OVERCAST_TLS`. The CA key material on disk is kept, so a later
`enable` reuses it and nothing needs re-minting. Delete `<data dir>/ca` if you
want the key material gone too.

## Then what

| Page | Answers |
| --- | --- |
| [Overcast in Docker over HTTPS](./https/docker.md) | Keeping one trust install across container recreations, in `docker run` and Compose, with or without the `overcast` CLI on the host |
| [Installing the CA by hand](./https/manual-trust.md) | A trust store the CLI cannot write: per-platform commands, Firefox and Chromium, WSL, and bringing your own certificate |
| [How the local CA works](./https/how-it-works.md) | What `enable` mints, which names the certificate covers, working offline, and what Lambda and ECS containers get |

## Related

- [`overcast https` and `overcast trust`](./cli/tls.md) — the command reference
- [Using AWS SDKs and CLI](./sdk-cli.md) — endpoint configuration for every SDK
- [Environment variable reference](./configuration/reference.md) — `OVERCAST_TLS`, `OVERCAST_CA_DIR` and the rest
