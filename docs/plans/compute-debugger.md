# Compute debugger: step debugging inside emulated Lambda and ECS — plan

> Status: **complete** 2026-09-07 — issue #1939. Phase A (`c2ab20dd9`, the
> shared `internal/debugger` package and config), Phase B (`3df066440` Lambda,
> router and console types; `d052dc49f` ECS), Phase C (`28ab5df17`, the
> console's Debug tab, list badge and Test tab hint), Phase D (`56b5c60ef`,
> the published page and cross-links) and Phase E (the review pass over the
> whole branch, whose fixes landed as the commits after it) are all in.
>
> What shipped differs from the design in a few measured ways. The one-instance
> pin is `admit`'s per-function cap becoming 1 while a bound target exists,
> from the first cold start on, so a burst of *first* invocations is still
> admitted at the ordinary cap — § 4 asked for the existing admission seam,
> and that is what the seam gives. Protocol resolution applies the tag and
> runtime steps only to a tagged resource while environment detection always
> runs when the flag is on, so an untagged Node function gets no port unless
> it already carries `--inspect`, and a WARN says so when it does. ECS resolves
> its targets before the namespace container, because under awsvpc that is the
> container that publishes the task's ports; duplicate tasks are tracked by an
> owner map keyed by task definition ARN and container, claimed under one lock;
> and the remote root is the definition's `workingDirectory`, else one image
> inspect, else `/`. Everything a service does with a container — the upstream
> binding sequence, the "could not bind" and "flag without a tag" warnings,
> the tag-problem lines, releasing a target whose tag was removed — lives in
> the package (`Target.Bind`, `Manager.Ensure`, `WarnProblems`), so the Lambda
> and ECS glue are the five touches § 4 and § 5 list and nothing else; the
> suspendable deadline seeds its state under the target's transition lock.
> The console renders `127.0.0.1` for a wildcard listen host, polls only while
> an answer can still change, and keeps the editor strip in its own component.
> Deviations that stay deliberate: `TimeoutPolicy` aliases the config enum,
> `Subscribe` returns an unsubscribe, the CDP observer reports the net change
> per read, and `--inspect-wait`, DAP/JDWP observers and the console session
> of § 11 are later work (§ 12).
>
> This document is the brief for every agent working on the feature. Read it
> end to end before touching code. Where it says "mirror X", open X first.

## 1. Goal

A developer sets one server flag and one tag, presses F5 in their editor, and
steps through the handler of a Lambda function or the process of an ECS
container running inside Overcast — on Windows, macOS and Linux, with Overcast
native or in Docker — without the invocation timeout killing the paused
function, and without touching the AWS-facing API surface.

Today `docs/dev/debugging.md` covers Delve for Overcast's own Go code. Nothing
supports attaching a debugger to *user* code. The container runtime publishes
no ports ([container_runtime.go](../../internal/services/lambda/container_runtime.go),
the `CreateContainerRequest`), injects no inspector flag, and enforces the
function timeout by cancelling the context and retiring the container — which
is correct AWS behaviour and stays, but is fatal to a debugging session.

## 2. Constraints

- **AWS fidelity.** Nothing here may change what an SDK client observes on the
  AWS API. All switches are `overcast:` tags (inert metadata AWS stores and
  ignores) and `OVERCAST_*` environment. The container *environment* may carry
  emulator-only values — it already carries `AWS_ENDPOINT_URL` — but
  `GetFunctionConfiguration` must never echo them. The one behavioural
  divergence, the suspended invocation clock, only exists while a debugger is
  attached, which never happens on AWS; it is documented, and a strict mode
  restores the real timeout.
- **Cross-platform.** Native Windows/macOS/Linux and Overcast-in-Docker. No
  unix-only syscalls, no fixed ports in tests, host paths through
  `internal/hostpath`. The listen address follows the same containerised-vs-
  native rule as `OVERCAST_LISTEN` (#761).
- **DRY.** One shared package. Hot reload was implemented twice
  (`internal/services/lambda/hot_reload.go`, `internal/services/ecs/hot_reload.go`);
  the debugger is not. Editor instructions live in Go, once, and the console
  and CLI render the same descriptor.
- **Zero cost when off.** The invoke hot path gains one nil check. No
  background goroutines exist for a function that carries no debug spec.
- **Reliable degradation.** Anything that cannot be honoured logs a `WARN`
  naming the fix and the function or task runs exactly as it would without
  the tag. A debugger that disconnects mid-pause simply resumes the clock.

## 3. Architecture

### 3.1 Package layout — `internal/debugger`

| File | Owns |
| --- | --- |
| `spec.go` | Tag parsing (`Spec`), config gating, ECS container-name suffixes |
| `protocol.go` | `Protocol`, optional `PauseObserver`, `Registry`, resolution order |
| `inspector.go` | Node.js — Chrome DevTools Protocol |
| `jdwp.go` | Java — JDWP agent |
| `dap.go` | Python — debugpy (DAP) |
| `passthrough.go` | Images and custom runtimes: proxy only, user sets the flag |
| `proxy.go` | Per-target loopback listener, forwarding, attach tracking |
| `observer_cdp.go` | WebSocket frame reader that reports `Debugger.paused` / `Debugger.resumed` |
| `deadline.go` | Suspendable deadline context |
| `target.go` | `Target` lifecycle, `Manager` (registry of live targets), port allocation |
| `descriptor.go` | JSON descriptor, editor template rendering |
| `editors.go` | Template text per protocol per editor |
| `handler.go` | `GET /_overcast/debugger/targets`, `GET /_overcast/debugger/targets/{service}/{resource}` |

Everything a service touches is `Spec`, `Registry.Resolve`, `Manager.Ensure`,
`Target.SetUpstream/ClearUpstream`, `Manager.Release`, and `WithDeadline`.
Nothing else is exported for services.

### 3.2 Configuration (`internal/config`)

| Variable | Default | Meaning |
| --- | --- | --- |
| `OVERCAST_DEBUGGER` | `false` | Umbrella for every compute service — mirrors `OVERCAST_HOT_RELOAD` |
| `OVERCAST_LAMBDA_DEBUGGER` | inherits | Per-service override, both directions |
| `OVERCAST_ECS_DEBUGGER` | inherits | Per-service override, both directions |
| `OVERCAST_DEBUGGER_LISTEN` | resolved `OVERCAST_LISTEN` host | Address the debug ports bind on. `127.0.0.1` native, `0.0.0.0` containerised, so `-p 9229-9329:9229-9329` reaches it |
| `OVERCAST_DEBUGGER_PORTS` | `9229-9329` | Range auto-allocated ports are taken from, lowest free first |
| `OVERCAST_DEBUGGER_TIMEOUT` | `attached` | `attached`: Lambda clock stops while a client is connected. `paused`: stops only while the protocol reports a pause (falls back to `attached` for protocols without an observer). `strict`: real timeout always |

Config fields: `Debugger`, `LambdaDebugger`, `ECSDebugger`, `DebuggerListen`,
`DebuggerPorts` (parsed `[2]int`), `DebuggerTimeout` (typed enum). Document
every one in `config.go`'s reference comment block and `docs/configuration/reference.md`.

### 3.3 Tags

| Key | Value | Effect |
| --- | --- | --- |
| `overcast:debug` | `true` | Enable, auto-allocated port |
| `overcast:debug-port` | `9229` | Enable, fixed port (implies `overcast:debug`) |
| `overcast:debug-protocol` | `inspector` `jdwp` `dap` `passthrough` | Override resolution; required for images and custom runtimes when detection from env finds nothing |
| `overcast:source-path` | absolute host path | Local root for path mappings; falls back to `overcast:hot-reload-path` |

ECS task definitions carry the same keys, and a container is named the way
hot reload names a volume: `overcast:debug-port/<container-name>`. The bare
key applies when the task definition has exactly one container; otherwise it
is refused with a `WARN` naming the containers, mirroring `hotReloadPaths`.

A tag with the flag off logs one `WARN` naming the flag, exactly as hot reload
does, and the resource runs as if untagged. A tagged-but-disabled resource is
still registered with the `Manager` as an *inert* target so the console can
explain why it is off.

### 3.4 Protocol interface

```go
// Protocol is one debugger wire protocol. Adding one is a file that
// registers itself in Default; no service, proxy or console code changes.
type Protocol interface {
    Name() string                 // "inspector", "jdwp", "dap", "passthrough"
    Runtimes() []string           // AWS runtime prefixes: "nodejs", "java", "python"; nil for none
    Inject(env map[string]string, port int) // append the listen flag; never clobber a user value
    Detect(env map[string]string) (port int, ok bool) // an existing user flag → implicit enable
    Editors() []EditorTemplate    // per-editor templates, rendered by descriptor.go
}

// PauseObserver is optional. A protocol implementing it can tell a pause from
// a running program, which OVERCAST_DEBUGGER_TIMEOUT=paused uses.
type PauseObserver interface {
    NewObserver() Observer
}

// Observer sees one connection's bytes. FromServer is fed the container→client
// direction; it must tolerate arbitrary chunking and never block or fail —
// an unparseable stream simply reports nothing.
type Observer interface {
    FromServer(b []byte) (paused, resumed bool)
}
```

Every protocol additionally gets `OVERCAST_DEBUG_PORT=<port>` injected by the
package, not by the protocol, so user code and images can read the port
without knowing the protocol.

**Resolution order** (`Registry.Resolve(spec, runtime, env)`):

1. `overcast:debug-protocol` tag names one → that protocol.
2. `runtime` matches a protocol's `Runtimes()` prefix → that protocol.
3. Any protocol's `Detect(env)` succeeds → that protocol, port from env,
   `protocolSource: env`. Runs even without a tag when the flag is on, so a
   function that already carries `NODE_OPTIONS=--inspect` is proxied; logged
   at `WARN` once per resource.
4. Spec enabled but nothing matched (image, `provided.*`) → `passthrough`.
5. Nothing → no target.

**Injection rules.** `Inject` appends to an existing value with a single
space and does nothing if the flag is already present:

| Protocol | Variable | Appended |
| --- | --- | --- |
| inspector | `NODE_OPTIONS` | `--inspect=0.0.0.0:<port>` |
| jdwp | `JAVA_TOOL_OPTIONS` | `-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:<port>` |
| dap | — | nothing; the editor template carries the two-line `debugpy.listen` snippet reading `OVERCAST_DEBUG_PORT` |
| passthrough | — | nothing |

`Detect` parses `--inspect`, `--inspect-brk`, `--inspect-wait` with optional
`=[host:]port` (default 9229) and `address=[host:]port` / `address=*:port`
in a JDWP agent string. Table tests cover every spelling.

### 3.5 Proxy and port allocation

Overcast owns the debug listener. That is the decision that makes the rest
generic: attach state is known at TCP level for any protocol and any editor,
and the port is stable across hot reload retiring the container, which is what
lets VS Code's `"restart": true` reconnect without the user doing anything.

- `Manager.Ensure(spec, id)` returns the `Target`, binding
  `<DebuggerListen>:<port>`. Auto ports scan `DebuggerPorts` lowest first via
  `net.Listen`; the first success wins. An explicit port already in use is a
  target in state `error` with the reason, never a silent fallback.
- The container listens on the **same port number** the host side uses, so
  the inspector's own `/json/list` URLs match what the editor asked for.
- A connection accepted with no upstream is closed immediately; editors that
  poll (`restart: true`) retry. With an upstream it is dialled with a 2 s
  timeout and spliced with `io.Copy` both ways. The container→client leg is
  wrapped in the protocol's `Observer` when it has one.
- Attach count 0→1 and 1→0 fire subscribers; the observer's paused/resumed
  do too. `Target.State()` is `unbound | listening | attached | paused | error`.
- `Target.SetUpstream(addr)` may be called repeatedly (container replaced);
  live connections to the old upstream are left to close on their own.
- `Manager.Release(id)` closes the listener. Called on function delete, task
  stop, and shutdown via the existing lifecycle hooks.
- A second ECS task from the same definition, or a second Lambda container
  for the same function, has no port of its own: Lambda pins the function to
  one instance (§ 4), ECS logs a `WARN` and runs the extra task undebugged.

### 3.6 Reaching the container

Three environments, one predicate that already exists —
`dataplane.ContainerAddr` returns a routable address only when Overcast is
itself containerised:

| Overcast runs | `ContainerAddr` | Upstream |
| --- | --- | --- |
| Native Windows / macOS (Docker Desktop, container IPs unroutable) | `""` | Ephemeral published port on `127.0.0.1` |
| Native Linux | `""` | Same — the published port works everywhere native, so one path |
| In Docker | container IP on the control network | `<ip>:<port>` |

At create time a target adds `ExposedPorts["<port>/tcp"]` and
`PortBindings["<port>/tcp"] = [{HostIP: "127.0.0.1", HostPort: "0"}]`. After
start, when `ContainerAddr` is empty, the host port is read from
`NetworkSettings.Ports` and the upstream is `127.0.0.1:<hostPort>`. The
binding is added in both environments — it is cheap and avoids a second
decision at create time.

### 3.7 Suspendable deadline

`debugger.WithDeadline(ctx, clk, timeout, target, policy) (context.Context, cancel)`.

- Returns a context whose `Deadline()` reports the **nominal** deadline
  (`start + timeout`) — the Runtime API derives `Lambda-Runtime-Deadline-Ms`
  from it and that header is sent once, so it stays what AWS would send —
  while `Done()` fires only when *running* time reaches `timeout`.
- Running time excludes intervals during which the policy says suspended:
  `attached` while `Target` has ≥1 client, `paused` while the observer holds
  a pause (falling back to `attached` when the protocol has no observer),
  `strict` never.
- Built on `clock.Clock` timers (never `time.Now()`); on suspend the timer is
  stopped and the remaining budget recorded, on resume it is restarted. Fake
  clock tests cover suspend before start, mid-run, multiple cycles, and detach
  with budget exhausted.
- With no target or with `strict`, it is exactly `context.WithTimeout`.

Documented divergence: `context.getRemainingTimeInMillis()` in the function
counts down through a pause and can go negative. Only ever with a debugger
attached.

## 4. Lambda integration

All in `internal/services/lambda`, each a few lines calling the package:

1. **Spec.** `debugger.SpecFromTags(fn.Tags, cfg.LambdaDebugger)` at
   `acquireContainer`. Resolve via `Registry.Resolve(spec, fn.Runtime, fn.Environment)`
   and `Manager.Ensure`. Target id: `lambda/<function name>`.
2. **Env.** In `buildEnv`, after runtime env is applied, `target.Inject(env)`
   (which calls the protocol's `Inject` and sets `OVERCAST_DEBUG_PORT`).
   `GetFunctionConfiguration` reads `fn.Environment`, never the container env,
   so nothing leaks to the API. Add a test asserting exactly that.
3. **Create.** `target.ApplyPortBinding(ccfg, hostConfig)` before
   `CreateContainer`.
4. **Bind.** After start, once the container IP is known, resolve the upstream
   per § 3.6 and `SetUpstream`. On `containerInstance.Close()` → `ClearUpstream`.
   On `DeleteFunction` → `Manager.Release`.
5. **One instance.** A function with a live target is capped at one execution
   environment through the existing per-function admission limit in
   `runtime_pool_admission.go` (find the seam; do not add a second limiter).
   Further invocations queue exactly as the host-protection path already does.
   Proactive and provisioned acquisition respect the cap.
6. **Deadline.** Wherever the invoke context is bounded by the function
   timeout (`invokeTimeout(fn)` in `handler_functions.go` and its callers,
   including the streaming and async paths) use `debugger.WithDeadline`. The
   INIT phase deadline gets the same wrapper so `--inspect-wait` works.
7. **Descriptor.** `remoteRoot`: `/var/task` for zips; for images
   `ImageConfig.WorkingDirectory`, else the cached image config's `WorkingDir`,
   else `/var/task`. `localRoot`: `overcast:source-path`, else the hot reload
   path, else `""`.

Tags are validated nowhere on the AWS surface: an unparseable value logs a
`WARN` and is ignored, because AWS accepts any tag.

## 5. ECS integration

`internal/services/ecs`, the same five touches:

1. **Spec** from task definition tags with the container-name suffix rule
   (§ 3.3), resolved once per container at task start. No runtime string:
   resolution is tag → `Detect(container env)` → passthrough. Target id:
   `ecs/<task id>/<container name>`.
2. **Env** appended to the container definition's env at container create.
3. **Ports.** The task's containers share the namespace container's network
   (`task_netns.go`, `portSurface`), so the binding goes where `hostPort`
   bindings already go. Mirror `applyTaskNetwork`.
4. **Bind/Release** after start and on task stop, through the same hooks that
   manage the task's containers today.
5. **Descriptor.** `remoteRoot` from the container image's `WorkingDir`, else
   `/`; `localRoot` from `overcast:source-path[/<container>]`, else the hot
   reload path redirecting one of that container's mount points, else `""`.
   No deadline: tasks have none, and Overcast does not enforce container
   health checks.

## 6. Descriptor and endpoints

`GET /_overcast/debugger/targets` → `{ "targets": [Target...] }`.
`GET /_overcast/debugger/targets/{service}/{resource}` → one `Target`, or for
an untagged resource a synthesised entry with `enabled: false`,
`reason: "not tagged"` and `setup` filled in, so the console tab always has
something to render. Both are emulator-only and live under `/_overcast/`, so
`TestNoRouteIsRegisteredOutsideTheNamespace` is satisfied; register in
`internal/router` next to the other `/_overcast/debug*` handlers and add the
types to `cmd/tsgen`'s manifest.

```jsonc
{
  "id": "lambda/my-fn",
  "service": "lambda",            // "lambda" | "ecs"
  "resource": "my-fn",            // function name, or task id
  "container": "",                // ECS container name
  "enabled": true,
  "reason": "",                   // why not, when !enabled
  "protocol": "inspector",
  "protocolSource": "runtime",    // "tag" | "runtime" | "env" | "fallback"
  "listen": { "host": "127.0.0.1", "port": 9229 },
  "state": "attached",            // "inert" | "unbound" | "listening" | "attached" | "paused" | "error"
  "attachedSince": "2026-09-07T10:00:00Z",
  "pausedSince": "",
  "containerId": "0a1b…",
  "upstream": "127.0.0.1:55012",
  "remoteRoot": "/var/task",
  "localRoot": "",                // as the user wrote it; "" when unknown
  "timeoutPolicy": "attached",
  "setup": {                      // always present
    "flag": "OVERCAST_DEBUGGER=true",
    "tagCli": "aws lambda tag-resource --resource <arn> --tags overcast:debug=true",
    "tagCdk": "cdk.Tags.of(fn).add(\"overcast:debug\", \"true\")"
  },
  "editors": [
    { "id": "vscode", "label": "VS Code", "kind": "json", "body": "{…}", "verified": true },
    { "id": "jetbrains", "label": "JetBrains", "kind": "steps", "body": "1. …" },
    { "id": "devtools", "label": "Chrome DevTools", "kind": "steps", "body": "…" },
    { "id": "cli", "label": "Command line", "kind": "shell", "body": "…" }
  ]
}
```

Editor bodies are rendered server-side with the real host, port, roots and
resource name. When `localRoot` is unknown the template keeps a literal
`${localRoot}` and the body is preceded by one line saying which tag fills it.
A template can set `verified: false`, which the console renders as a note;
the `cdk watch` bundled-asset source-map case ships unverified.

Templates, first set:

| Protocol | VS Code (`kind: json`) | JetBrains (steps) | Other |
| --- | --- | --- | --- |
| inspector | `type: node, request: attach, address, port, restart: true, localRoot, remoteRoot, skipFiles: ["<node_internals>/**", "/var/runtime/**"]` | Attach to Node.js/Chrome, host/port, remote root mapping | DevTools: `chrome://inspect` → Configure → `host:port` |
| jdwp | `type: java, request: attach, hostName, port, sourcePaths` | Remote JVM Debug, host/port | `jdb -attach host:port` |
| dap | `type: debugpy, request: attach, connect: {host, port}, pathMappings: [{localRoot, remoteRoot}]` + the `debugpy.listen` snippet | Python Remote Debug steps | — |
| passthrough | comment naming the port and that the runtime flag is theirs to set | same | same |

## 7. Web console

One shared component, `web/src/features/debugger/components/debug-panel.tsx`,
takes a `Target` and renders: state with reason and inline setup (copy
buttons on the tag command, CDK line and flag); the resolved target table
(protocol and source, port, container, roots — with the tag hint when the
local root is unknown, timeout policy); the live line (attached since, paused
since, "container replaced — your editor should reconnect" when the upstream
changes while attached); and an editor tab strip rendering `editors[]` by
`kind` with a copy button. It is a dumb renderer: no per-protocol logic.

Surfaces, all using that one component or the list query:

- **Lambda function page** — a `Debug` tab after `Test`
  (`web/src/routes/lambda/$name.tsx`), rendering `<DebugPanel>` for
  `lambda/<name>`.
- **Function overview** — one line: `Debugger: attached on 127.0.0.1:9229` /
  `off`, linking to the tab.
- **Function list** — a state-coloured badge on rows with a target (one cached
  list query, not one per row).
- **Test tab** — hint above Invoke: attached and clock suspended, or no client
  and the real timeout applies.
- **ECS task detail** — `<DebugPanel>` per container inside the existing
  Containers block.

Data: `web/src/services/api/debugger.ts` with `listTargets()` and
`getTarget(service, resource)`; react-query with a 2 s `refetchInterval` while
the Debug tab or a badge is mounted, off otherwise. SSE is not required for
v1. Types come from `make generate-ts`; never hand-written. Tests: vitest for
`DebugPanel` states and the badge, following the existing `*.test.tsx`.

## 8. Documentation

- **Published:** `docs/debugger.md` — "Step debugging inside emulated
  compute", section Getting Started beside `docs/local-dev.md`. One concern:
  the flag, the tag, press F5. Runtime table, the timeout note, ECS, the
  troubleshooting table, `## Related`. Under the length budget; `make docs-lint`
  must pass. Link from `local-dev.md`, `services/lambda/examples.md`,
  `services/ecs/examples.md`, `configuration/reference.md` (new rows), and
  `docs/dev/debugging.md` (one pointer: this page is for user code).
- **Changelog** fragment under `.changelog/`, `+ [lambda/ecs/web] …`.
- **Capabilities:** the Lambda `Invoke` and ECS `RunTask` notes gain one
  clause; `make docs` regenerates the tables.
- **This plan** is updated to `complete` with what shipped and what changed.

## 9. Tests

| Area | Tests |
| --- | --- |
| `spec.go` | tag forms, suffixes, bare-key ambiguity, flag off, bad values |
| protocols | table tests for `Inject` (fresh, append, already present) and `Detect` (every flag spelling) |
| `proxy.go` | fake upstream echo server; attach/detach callbacks; no-upstream close; `SetUpstream` mid-life; explicit port in use → `error`; auto allocation skips a bound port. Port 0 / scanning only — never a fixed port |
| `observer_cdp.go` | recorded WebSocket frames: handshake, single frame, fragmented, two messages in one read, 16-bit and 64-bit lengths |
| `deadline.go` | fake clock: strict; suspend before start; mid-run; several cycles; exhaust after resume; `Deadline()` nominal |
| `descriptor.go` | every editor renders for every protocol; unknown local root placeholder; `verified` |
| `handler.go` | list, get, synthesised untagged entry |
| Lambda | env injection and `GetFunctionConfiguration` untouched; port binding on the create request; one-instance cap; deadline wrapper chosen only with a target; `Close` clears upstream; delete releases |
| ECS | suffix resolution per container; env; binding on the namespace container; release on stop |
| Docker-backed | `tests/integration/lambdadocker`: a `nodejs` function tagged `overcast:debug=true`; `GET http://127.0.0.1:<port>/json/list` answers through the proxy; attach a raw WebSocket, hold it, confirm an invoke outlives its 3 s timeout under `attached`. Skips without Docker like its neighbours |

CI's OS matrix job runs on all three platforms; nothing here may be
Linux-only.

## 10. Phases

Phase A landed as `c2ab20dd9`. Deviations recorded there: `TimeoutPolicy` aliases
`config.DebuggerTimeoutPolicy` (one enum, config cannot import debugger);
`Subscribe` returns an unsubscribe func; tag/runtime resolution applies only to a
tagged spec while env detection always runs, so an untagged Node function gets no
target; `Ensure` replaces a target whose spec changed; the CDP observer reports the
net change per read.

Phase B1 (Lambda, router, types) notes for the ECS half: the router builds one
`debugger.Manager` and passes it to `lambda.New`; the endpoints are registered by
`registerDebuggerRoutes` in `internal/router/debugger.go` with a
`debuggerDescribers` map keyed by `debugger.Service` — ECS adds
`debugger.ServiceECS: ecsSvc` and implements `DescribeUntagged(ctx, service,
resource)`, which gained a context so a describer can read its store.
`Target.ClearContainer(id)` replaced the unconditional `ClearUpstream` on
container close, so a container retired after its replacement was bound does not
blind the proxy. The target rides on the `containerInstance` (`DebugTarget()`),
so the invoke path never looks one up. The debug tags are part of
`functionInstanceIdentity`: a tag change retires the environment, and the next
cold start re-resolves. The one-instance pin is `admit`'s per-function limit
becoming 1 while `liveDebugTarget` (enabled, not in error) exists; it takes
effect from the first cold start on, so a burst of first invocations is admitted
at the ordinary cap.

Phase B2 (ECS) notes: `ecs.New` takes the same manager and the router registers
`debugger.ServiceECS: ecsSvc` once both services exist (the route registration
moved below `ecs.New`; chi does not order absolute routes). All ECS glue is
`internal/services/ecs/debugger.go`. Targets are resolved once per task in
`startTaskContainers`, before the namespace container, because under awsvpc that
container is the one whose `HostConfig` publishes the task's ports — so the debug
binding goes on it and the post-start inspect reads it, while `containerId` and
the die-event `ClearContainer` name the application container. In bridge/host
modes the application container carries its own binding. The upstream rule left
Lambda's glue for `Target.UpstreamFor`, shared by both services; the
port-range/manager test helpers likewise moved to `internal/debugger/debuggertest`.
`SpecsFromTaskTags` now ignores `overcast:hot-reload-path[/<volume>]` outright —
its suffix names a volume, not a container, and treating it as one warned on every
hot-reloaded task — so the ECS glue derives the local root from the first mount
point a hot-reload tag redirects. Two deviations from § 5: `SetResourceARN` records
the task definition ARN, not the task's, because that is the resource the tag
lives on and what `setup.tagCli` must name; and the remote root is the
definition's `workingDirectory`, else one `InspectImage` of the container's image
(only for a debugged container, after the pull), else `/`. Duplicate tasks are
tracked by an in-memory owner map keyed by task definition ARN and container:
the second task of a definition whose target is still registered runs undebugged
with a WARN naming the first, and the port moves to the next task once the first
stops. Every stop path releases — `StopTask`, the service scheduler's retire, the
last container's die event, and a failed placement. There is no ECS runtime
string, so a plain `overcast:debug=true` on a Node image resolves to passthrough
unless `NODE_OPTIONS` already carries `--inspect`; the Docker-backed test tags
`overcast:debug-protocol=inspector` for that reason.

- **A — core package and config.** `internal/debugger` complete with tests;
  config fields and reference comments. No service changes. Compiles under
  `slim`, `slim,nosqlite`, `slim,dev`.
- **B — services, router, types.** Lambda and ECS integration, endpoints,
  tsgen manifest and regenerated `api.gen.ts`, Docker-backed test,
  capability notes, `make docs`.
- **C — console.** `DebugPanel`, Debug tab, overview line, list badge, Test
  tab hint, ECS containers section; vitest; `pnpm run typecheck`, `pnpm run lint`.
- **D — docs.** `docs/debugger.md`, cross-links, config reference rows,
  changelog fragment; `make docs-lint`. Runs in parallel with C in its own
  worktree.
- **E — review.** Repo `code-review` skill over the whole branch; fixes;
  `make verify`; PR.

## 11. Phase 2 — step debugging inside the console's Code tab

Possible, and worth doing after v1 ships. The Code tab is Monaco with a file
list (`CodeBrowser`), and `GET /_overcast/lambda/functions/{name}/source`
already serves every file in the deployed zip, `.map` files included. The
pieces:

- **A WebSocket bridge on Overcast's own port**,
  `/_overcast/debugger/targets/{service}/{resource}/ws`, splicing to the
  target's upstream exactly as the TCP proxy does. Browsers cannot open raw
  TCP, and going through Overcast's port avoids the mixed-content and
  cross-origin questions of connecting the console straight to `:9229`. It
  is one more consumer of `Target`, and its attach count and observer apply
  unchanged — a console session suspends the clock like an editor does.
- **A CDP client for Node first.** `Debugger.enable`, `scriptParsed`,
  `setBreakpointByUrl`, `paused` with call frames, `Runtime.getProperties`
  for scopes, and the step commands. It is JSON over the bridge and the
  protocol Overcast's observer already reads.
- **Source maps in the browser.** `scriptParsed` names the generated file's
  `sourceMapURL`; the console fetches the `.map` from the source endpoint,
  resolves it with the `source-map` library, shows the original TypeScript
  read-only in Monaco, and translates gutter breakpoints to generated
  line/column before `setBreakpointByUrl`. Inline maps need no fetch. The
  hot-reload case reads the map from the mounted directory through the same
  endpoint.
- **Panels.** Gutter breakpoints, a call stack list, a scope tree, and
  continue/step over/into/out buttons beside Invoke on the Test tab, so
  invoke-and-pause is one screen.
- **Other protocols** reuse the bridge with a DAP client (debugpy, and the
  same client later serves any DAP server); JDWP is binary and stays an
  editor-only protocol.

v1 lays the groundwork by keeping the observer and the target abstraction
protocol-shaped, and by putting the descriptor on the same endpoint family
the bridge will join.

### 11.1 Console debugging UX

Modelled on VS Code's Run view, because that is what the user already knows,
but arranged around the one flow the console has that an editor does not:
the Test tab's Invoke button.

**Session model.** A `DebugSessionProvider` mounted at the function route
holds the CDP client (over the WebSocket bridge), breakpoints, watch
expressions and the current pause. Every tab reads it, so Code and Test stay
in step. The session is one more attached client of the `Target`: it
suspends the clock exactly as an editor would. Breakpoints and watches
persist per function in `localStorage` and are re-applied on every
`scriptParsed`, which is what survives hot reload recycling the container.

**Flow.**

1. Debug tab → *Debug in console* starts the session (or it starts the first
   time a gutter breakpoint is placed while the target is enabled). State
   shows `attached` immediately, before any invoke.
2. Code tab: click the gutter for a breakpoint; right-click for a condition
   or a logpoint (a condition that logs and returns false, as VS Code does).
   Breakpoints show in the gutter and in the Breakpoints panel.
3. Test tab → Invoke, as today. The invoke stream keeps rendering.
4. On `Debugger.paused` the console switches to the Code tab at the paused
   location: file opened, line highlighted, current-line marker in the gutter.
   A toolbar over the code pane carries Continue, Step over, Step into, Step
   out, Restart container, Stop, with F5/F10/F11/Shift+F11 bound while the
   pane has focus.
5. The right sidebar has four panels: **Locals** (scope chain from the
   selected call frame, `Runtime.getProperties` on expand, previews inline),
   **Watch** (expressions evaluated with `Debugger.evaluateOnCallFrame` on
   every pause, editable, persisted), **Call stack** (frames with mapped
   locations; selecting one re-scopes Locals and Watch), **Breakpoints**
   (enable, disable, remove, edit condition; a *Pause on exceptions* toggle).
6. The bottom drawer has two tabs. **Logs** reuses the Monitor tab's log
   viewer, live, filtered to the current request id, with a marker line at
   each pause and resume so output reads in order against the stepping.
   **Debug console** shows `Runtime.consoleAPICalled` output the instant it
   happens — before it reaches CloudWatch — and has a REPL input that
   evaluates in the selected frame while paused, or globally while running.
7. Continue to completion: the result lands in the Test tab as it does now,
   the pause UI clears, the session stays attached for the next invoke.
   Stop detaches and the clock resumes; a strict-timeout target shows the
   countdown in the toolbar instead of "clock suspended".

**With and without source maps.**

- *No map* (plain JS, Python, or raw `.ts` on Node 24 hot reload): the file
  list is the deployed tree, breakpoints and pauses are on those files.
  Nothing to translate.
- *Map beside the file* (`tsc`, esbuild with `sourcemap: true`): on
  `scriptParsed` the console fetches the `.map` through the source endpoint,
  adds the original sources to the file list under an *Original* group, and
  hides generated files behind a *Show compiled* toggle. Gutter breakpoints
  on an original file are translated to generated line/column before
  `setBreakpointByUrl`; pause locations and call-stack frames are mapped
  back. Sources present in the zip open editable-read-only from the zip;
  sources outside it (a `cdk watch` bundle whose map points at `../src`)
  open from the map's `sourcesContent`, read-only, with a badge saying so.
- *Inline map*: same, without the fetch.
- A frame the map cannot resolve falls back to the generated file with a
  *no source map for this frame* badge rather than an empty pane.

**Scope note.** Locals, Watch and the REPL are per-protocol clients. Node
ships first because CDP already drives the observer and the bridge; DAP
(Python) reuses the same panels with a DAP client, since DAP's `scopes`,
`variables`, `evaluate` and `stackTrace` map one-to-one onto them.

## 12. Later, deliberately

- Ship debugpy through the init volume with a `sitecustomize.py`, making
  Python zero-config (the volume mechanism exists — `init_volume.go`).
- `overcast:debug-wait` injecting `--inspect-wait` / `suspend=y`.
- Observers for DAP (`stopped`/`continued`, Content-Length framed JSON) and
  JDWP; `rdbg`, Delve and vsdbg protocols (vsdbg needs an exec-pipe kind).
- SSE events for attach/pause, a global "1 debugger attached" chip, glyphs on
  the topology map.
- `overcast debug <resource> --editor vscode` printing the descriptor's body.
- A VS Code extension that reads the descriptor and offers "attach to …".
