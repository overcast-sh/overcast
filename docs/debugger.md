---
title: "Step debugging inside emulated compute"
description: "Set OVERCAST_DEBUGGER=true, tag the function or task definition with overcast:debug=true, and attach your editor's debugger to code running inside an emulated Lambda function or ECS task."
section: "Getting Started"
tags:
  - docs
  - guide
  - debugger
  - debugging
  - lambda
  - ecs
  - development
---

# Step debugging inside emulated compute

One server flag and one tag put a breakpoint inside the handler of a Lambda
function or the process of an ECS container running under Overcast, from
VS Code, a JetBrains IDE or Chrome DevTools, on Windows, macOS and Linux.

## Turning it on

Off by default, like hot reload, and one flag covers every compute service:

```bash
OVERCAST_DEBUGGER=true overcast serve
```

| Variable | Default | Effect |
| --- | --- | --- |
| `OVERCAST_DEBUGGER` | `false` | Every compute service |
| `OVERCAST_LAMBDA_DEBUGGER` | inherits the umbrella | Lambda only |
| `OVERCAST_ECS_DEBUGGER` | inherits the umbrella | ECS only |

The per-service variables override the umbrella in both directions. The port
range, the listen address and the timeout policy have variables of their own,
covered below and listed in the
[environment variable reference](./configuration/reference.md).

## Lambda

Tag the function, on the CLI or in CDK:

```bash
aws --endpoint-url http://localhost:4566 lambda tag-resource \
  --resource arn:aws:lambda:us-east-1:000000000000:function:demo \
  --tags overcast:debug=true
```

`create-function --tags overcast:debug=true` does the same at creation.

```typescript
cdk.Tags.of(fn).add("overcast:debug", "true");
```

On the next invocation Overcast starts the function's container with the
runtime's debug flag set and listens for your editor on `127.0.0.1`, on the
lowest free port from 9229 upward. Tag `overcast:debug-port=9229` instead to
pin the port; it implies `overcast:debug`. The port belongs to the function
rather than the container, so it survives hot reload replacing the container,
and an editor set to reconnect stays attached across edits.

Attach from VS Code with this `launch.json` entry, press F5, then invoke the
function:

```json
{
  "type": "node",
  "request": "attach",
  "name": "Attach to demo (Overcast)",
  "address": "127.0.0.1",
  "port": 9229,
  "restart": true,
  "localRoot": "${workspaceFolder}/src",
  "remoteRoot": "/var/task",
  "skipFiles": ["<node_internals>/**", "/var/runtime/**"]
}
```

`restart: true` keeps VS Code polling the port, so the order of F5 and the
first invocation does not matter. The **Debug** tab on the function's console
page shows this configuration with the real port and roots filled in, and the
equivalent for JetBrains, Chrome DevTools and the command line, each with a
copy button.

### In the console

A Node.js function can also be debugged with no editor at all, from the Code
tab of its console page: breakpoints in the gutter, stepping, locals, watches,
the call stack and source maps, in
[Debugging in the console](./debugger-console.md).

### Runtimes

| Runtime | What to add | Wire protocol |
| --- | --- | --- |
| Node.js | Nothing — `--inspect` is set for you | Inspector |
| Java | Nothing — the JDWP agent is set for you | JDWP |
| Python | Two lines in the handler module, and `debugpy` in the package | DAP (debugpy) |
| Container image or custom runtime | `overcast:debug-protocol`, and the runtime's flag yourself | any, or passthrough |

Python has no flag that starts a debug server, so the handler module opens one
on the port Overcast hands every debugged container:

```python
import debugpy, os
debugpy.listen(("0.0.0.0", int(os.environ["OVERCAST_DEBUG_PORT"])))
```

Ship `debugpy` in the deployment package or a layer.

For an image or a `provided.*` runtime the runtime name says nothing about what
could listen, so tag `overcast:debug-protocol` with `inspector`, `jdwp`, `dap`
or `passthrough`. `inspector` and `jdwp` inject the same flag the managed runtime
gets; `dap` renders the debugpy snippet and configuration above; `passthrough`
only forwards the port, and setting the runtime's flag on the port
`OVERCAST_DEBUG_PORT` names is yours to do. A function whose environment already
carries `--inspect` or a JDWP agent string is detected and proxied with no tag
at all once the flag is on.

### Path mappings

`remoteRoot` is `/var/task` for a zip function and the image's working directory
for an image function. `localRoot` comes from `overcast:source-path`, an absolute
host path, and falls back to `overcast:hot-reload-path`, so a hot-reloaded
function needs no second tag. Without either, the console's template leaves
`${localRoot}` for you to fill in.

| Your source | Mapping |
| --- | --- |
| Raw `.ts` on Node.js 24, or plain `.js` | The two roots and nothing else |
| Compiled `dist/` with `.map` files beside the output | `localRoot` at the compiled output; the maps lead back to the `.ts` files |
| A bundle produced by `cdk watch` | Unverified so far — [#1939](https://github.com/overcast-sh/overcast/issues/1939) |

### Timeouts while paused

The function timeout is enforced exactly as on AWS, except that the clock stops
while a debugger is attached, so a function with a 3-second timeout can sit at a
breakpoint for as long as you need. `OVERCAST_DEBUGGER_TIMEOUT` chooses when it
stops:

| Value | The clock stops |
| --- | --- |
| `attached` (default) | Whenever an editor is connected to the port |
| `paused` | Only while the debugger reports a stop; Node.js so far, other protocols behave as `attached` |
| `strict` | Never — the real timeout applies, debugger or not |

The one thing the function sees differently: `context.getRemainingTimeInMillis()`
keeps counting down through a pause and can go negative, because the deadline
the Runtime API hands the function is sent once and stays what AWS would send.
Nothing diverges without a debugger attached.

From its first cold start on, a function with a debug port runs in one execution
environment, so breakpoints land in the container your editor is attached to;
concurrent invocations queue behind it, as they do under a concurrency limit.

## ECS

The same tags go on the task definition, and Overcast opens a listener per
tagged container when the task starts. With one container the bare keys apply;
with several, suffix the key with the container name, the way hot reload names a
volume:

```typescript
cdk.Tags.of(taskDef).add("overcast:debug-port/app", "9229");
cdk.Tags.of(taskDef).add("overcast:debug-protocol/app", "inspector");
```

A bare key on a multi-container definition is refused with a warning naming the
containers. There is no runtime name to go on, so name the protocol with
`overcast:debug-protocol`; a container whose environment already carries the
flag is detected without it, and anything else is passthrough. `remoteRoot` is
the definition's `workingDirectory`, else the image's, else `/`; `localRoot`
follows the Lambda rules above.

Tasks have no timeout, so there is nothing to configure there. A second task
from the same definition gets no port: Overcast warns and runs it undebugged.

With `OVERCAST_ECS_DEBUGGER` off, an ordinary port mapping with a `hostPort` on
the debug port, plus the runtime's flag in the container's environment, is
published like any other `hostPort` and works on its own, with no target in the
console. Once the flag is on, that container is detected and proxied on the port
its flag names, so drop the `hostPort` mapping rather than publish the same port
twice.

## Overcast in Docker

Inside a container the debug ports bind on every interface, so publish the range
beside the API port:

```bash
docker run --rm -p 4566:4566 -p 9229-9329:9229-9329 \
  ghcr.io/overcast-sh/overcast:latest
```

| Variable | Default | Effect |
| --- | --- | --- |
| `OVERCAST_DEBUGGER_PORTS` | `9229-9329` | Range auto-allocated ports come from, lowest free first |
| `OVERCAST_DEBUGGER_LISTEN` | follows `OVERCAST_LISTEN` | Address the ports bind on: `127.0.0.1` native, `0.0.0.0` in a container |

The console's editor configurations still say `127.0.0.1`: the published port
is on your machine, whatever address the listener binds inside the container.

## When it does not work

Nothing degrades silently: a tag that cannot be honoured is named in a `WARN`,
and the function or task runs exactly as it would untagged.

| Symptom | Cause | Fix |
| --- | --- | --- |
| A `WARN` naming `OVERCAST_DEBUGGER`, and no port | The tag is set and the flag is off | Start Overcast with the flag, or the per-service one |
| The Debug tab shows `error`, naming the port | `overcast:debug-port` asks for a port something else holds | Free it, pick another, or use `overcast:debug=true` and let Overcast choose |
| The editor attaches and nothing ever pauses | The breakpoints are in module-level code, which runs during init before a client can connect | Put the first breakpoint inside the handler; init cannot pause |
| Docker Desktop on Windows or macOS cannot route to container addresses | Expected | Nothing to do: Overcast reaches the container through a port the daemon publishes on `127.0.0.1` |
| A second task, or a second instance of a function, has no port | One listener per function or task definition | Lambda queues invocations on the one instance; ECS warns and runs the extra task undebugged |

## Related

- [The inner loop](./local-dev.md) — hot reload, which shares the tag family and the `localRoot` path
- [Lambda examples](./services/lambda/examples.md) — the tag beside the other Lambda setups
- [ECS examples](./services/ecs/examples.md) — the tag on a task definition
- [Environment variable reference](./configuration/reference.md) — every `OVERCAST_DEBUGGER*` variable with its default
