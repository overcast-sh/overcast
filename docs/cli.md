---
title: "CLI reference"
description: "The overcast binary: the three commands most people need, and a page per command group — running the daemon, checking it, state, AWS tools, Docker networks, .local hostnames and TLS."
section: "Reference"
tags:
  - cli
  - commands
  - docs
  - overcast
  - reference
---

# CLI reference

`overcast` is the emulator daemon and the host-side tooling around it.
`overcastd`, the slim binary, has the same subcommands without the web console.

```bash
overcast serve            # run the emulator in the foreground
eval "$(overcast env)"    # point every AWS tool in this shell at it
overcast status           # check it is up, and see what it is running
```

Every command takes `--endpoint` (default `http://localhost:4566`),
`overcast --version` prints the build, and `overcast <command> --help` prints
that command's flags.

## Command groups

| Page | Commands |
| --- | --- |
| [Running the daemon](./cli/daemon.md) | `serve`, `start`, `stop`, `restart`, `logs` |
| [Checking a running daemon](./cli/inspect.md) | `status`, `wait`, `services`, `config` |
| [Wiping and importing state](./cli/state.md) | `reset`, `import cognito-users` |
| [Pointing AWS tools at Overcast](./cli/aws.md) | `env`, `aws` |
| [Querying and sample data](./cli/data.md) | `athena query`, `samples load` |
| [Docker networks](./cli/networks.md) | `network status`, `network reset` |
| [Reaching Overcast by name](./cli/bridge.md) | `bridge` |
| [HTTPS and the trust store](./cli/tls.md) | `https`, `trust` |

## Related

- [Using AWS SDKs and CLI](./sdk-cli.md) — SDK, Terraform and AWS CLI setup, with examples
- [Configuration reference](./configuration.md) — the environment variables the daemon reads
- [HTTPS and HTTP/2](./https.md) — the guide behind `overcast https`
- [Networking and host-based addressing](./networking.md) — what `bridge` and `network` sit on top of
- [Troubleshooting](./troubleshooting.md) — a symptom, and where its answer lives
