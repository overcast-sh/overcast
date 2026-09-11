# Compute debugger, phase 2: step debugging inside the console — plan

> Status: **in progress** 2026-09-12 — issue #1944. Builds on
> [compute-debugger.md](./compute-debugger.md) (#1939, shipped in #1941), whose
> § 11 and § 11.1 are the design this plan turns into work. Read that document
> first; this one only adds what phase 2 needs and pins the contracts the
> parallel phases share.

## 1. Goal

From the function page: set a breakpoint in the Code tab's gutter, press
Invoke on the Test tab, and be paused in the code view with Locals, Watch,
Call stack and Breakpoints beside it and the request's logs plus a debug
console below it — stepping with the same keys VS Code uses, through original
TypeScript when the deployment carries source maps. Node.js first; the panels
and bridge are protocol-shaped so DAP (Python) follows on the same surfaces.

## 2. Constraints (in addition to phase 1's)

- **Same origin.** The console reaches the bridge through the BFF's `/api`
  prefix, never by dialling the debug port or the API port directly, so
  nothing changes for a console served from a different port or behind the
  host bridge.
- **One more client, no new semantics.** A console session is an attached
  client of the phase 1 `Target`: attach count, pause observer and the
  suspended invocation clock apply exactly as for an editor. The Debug tab's
  state chip shows `attached` while a console session is open.
- **The CodeBrowser stays generic.** It gains decoration, gutter-click and
  reveal props; it learns nothing about debugging. Everything debugger-shaped
  lives under `web/src/features/debugger/`.
- **Nothing hand-written that the server types.** Descriptor additions go
  through `cmd/tsgen`.
- **Cheap when idle.** No WebSocket, no polling and no Monaco decorations
  unless a session is open.

## 3. Contracts shared by the parallel phases

### 3.1 The bridge endpoint

`GET /_overcast/debugger/targets/{service}/{resource}/ws[?container=]`,
served on the API router and proxied by the BFF as
`/api/debugger/targets/{service}/{resource}/ws` (upgrade passed through).

| Target protocol | Bridge behaviour | Frame kind |
| --- | --- | --- |
| `inspector` | Fetch `http://<upstream>/json/list`, take the first entry's `webSocketDebuggerUrl`, rewrite its host to the upstream, dial it, relay messages unchanged | text, one CDP JSON message per frame |
| anything else | Splice raw TCP bytes | binary |

- No upstream (no container yet) → close with code 1011 and reason
  `no container`. The console shows "waiting for a container — invoke once".
- Upstream replaced while a session is open (hot reload retired the
  container) → the bridge closes with code 1012 (`service restart`); the
  console reconnects and re-applies breakpoints on the next `scriptParsed`.
- Every open bridge connection counts as one attached client on the `Target`
  and feeds container→client frames through the protocol's observer. The
  Debug tab therefore reads `attached`/`paused` for console sessions too.
- Keepalive: ping every 20 s, close after two missed pongs. Origin check:
  accept the console's own origin (the BFF passes it through) and localhost
  origins; refuse others with 403.

### 3.2 Descriptor additions

`Descriptor` gains `consoleDebug bool` (true only for `inspector` in this
phase) and `bridgePath string` (the `/_overcast/...` path above). Both through
`cmd/tsgen`; the console never derives the path itself.

### 3.3 Files and source maps

The console reads files through the existing Lambda source endpoint. Phase A
confirms it can return **any** file in the deployment by path, including
`.map` files and files under a hot-reload mount, and adds that if it cannot
(the endpoint is emulator-only under `/_overcast/`, so the shape is ours).

`scriptParsed` gives each generated script's `url` (`file:///var/task/...`)
and `sourceMapURL`. The console:

1. Resolves `sourceMapURL` — an inline `data:` URL decodes in place; a
   relative one is fetched from the source endpoint relative to the script's
   path under `/var/task`.
2. Parses it with `@jridgewell/trace-mapping` (small, browser-native; add it
   to `web/package.json`).
3. Registers each `sources` entry as an *original* file: content from the
   zip when the resolved path exists there, else from `sourcesContent`, else
   unavailable (listed, opens to a "no content" pane). Original files show
   under an *Original* group in the file list; generated files hide behind a
   *Show compiled* toggle once any map has loaded.
4. Translates gutter breakpoints on an original file to generated
   line/column before `Debugger.setBreakpointByUrl`, and maps `paused` call
   frame locations back. A frame no map resolves opens the generated file
   with a *no source map for this frame* badge.

Raw `.ts` on Node 24 hot reload and plain JS need none of this: the file list
is the deployed tree.

### 3.4 Session model

`web/src/features/debugger/session/`:

- `cdp-client.ts` — a minimal typed CDP client over the bridge WebSocket:
  request/response correlation by `id`, event subscription, and exactly the
  commands used: `Debugger.enable/disable/setBreakpointByUrl/removeBreakpoint/resume/stepOver/stepInto/stepOut/pause/evaluateOnCallFrame/setPauseOnExceptions`,
  `Runtime.enable/getProperties/evaluate/releaseObjectGroup`; events
  `Debugger.scriptParsed/paused/resumed`, `Runtime.consoleAPICalled/exceptionThrown/executionContextDestroyed`.
- `source-maps.ts` — § 3.3.
- `session.ts` — a store (React context + `useSyncExternalStore`; no new
  state library unless one is already in `web/package.json`) holding:
  connection state, scripts, breakpoints (persisted per function in
  `localStorage`, with condition and enabled flag), watches (persisted),
  the current pause (call frames, selected frame), console entries, and the
  pause-on-exceptions mode. Breakpoints are re-applied on every
  `scriptParsed` whose URL matches, which is what survives a container
  replacement.
- `DebugSessionProvider` mounted at `routes/lambda/$name.tsx` so Code, Test
  and Debug tabs share one session.

### 3.5 Surfaces

- **Debug tab:** *Debug in console* start/stop button (only when
  `consoleDebug`), session state, and the existing editor configs.
- **Code tab while a session is open:** gutter breakpoints (click to toggle;
  right-click for condition/logpoint), current-line marker, a toolbar over
  the pane — Continue, Step over, Step into, Step out, Pause on exceptions,
  Restart container, Stop — with F5/F10/F11/Shift+F11 bound while the pane
  has focus; a right sidebar with Locals, Watch, Call stack, Breakpoints; a
  bottom drawer with Logs and Debug console. Below a width breakpoint the
  sidebar becomes a tab strip above the drawer.
- **Test tab:** Invoke as today. On `Debugger.paused` the page switches to
  the Code tab at the paused location; the invoke result still lands in the
  Test tab when the function completes.
- **Locals:** scope chain of the selected frame, `Runtime.getProperties` on
  expand, previews inline. **Watch:** expressions evaluated with
  `evaluateOnCallFrame` on every pause and on frame change; editable.
  **Call stack:** mapped locations, click selects the frame. **Breakpoints:**
  enable/disable/remove, edit condition, pause-on-exceptions toggle.
- **Logs:** the Monitor tab's log viewer, live, filtered to the current
  request id when the invoke stream provides one, with a marker line at each
  pause and resume. **Debug console:** `consoleAPICalled` and
  `exceptionThrown` as they arrive, and a REPL input evaluating in the
  selected frame while paused, globally while running.

## 4. Phases

- **A — backend.** `internal/debugger/bridge.go` (+ tests with a fake
  inspector WebSocket server using `github.com/coder/websocket`, already a
  dependency), router registration, BFF upgrade proxy (+ test), descriptor
  additions and regenerated `api.gen.ts`, source endpoint per-file/`.map`
  support if missing, capability note. Task worktree.
- **B1 — console core.** Session store, CDP client, source maps, CodeBrowser
  props, DebugSessionProvider, Debug tab start/stop, gutter breakpoints,
  current-line marker, toolbar and keys, pause navigation from Test. Own
  worktree, in parallel with A against the § 3 contracts; vitest with a fake
  bridge transport.
- **B2 — panels and drawer.** Locals, Watch, Call stack, Breakpoints, Logs
  and Debug console; responsive layout. After A and B1 are on the task
  branch; verified live against a real function.
- **C — docs.** `docs/debugger-console.md` (one concern, linked from
  `docs/debugger.md`), changelog fragment, this plan's status. Parallel with
  B2 in its own worktree.
- **D — review.** Repo `code-review` skill over the branch; fixes; full
  verification; PR with screenshots (paused state, panels, dark and light).

Phase A notes. The bridge is `Target.ServeWebSocket` in
`internal/debugger/bridge.go`, reached through `Handler.Bridge` at
`GET /_overcast/debugger/targets/{service}/{resource}/ws[?container=]` and
proxied by the BFF at `/api/debugger/targets/{service}/{resource}/ws` with
`httputil.ReverseProxy` (the upgrade passes through natively, so close codes
reach the browser as sent); the BFF reads the endpoint from the query as well
as the header, since a browser cannot set headers on a handshake, and
preserves the browser's `Host`, which is what makes the emulator's origin
check accept the console's own origin on a non-loopback host. A bridge
session and a TCP client share one bookkeeping path — `Target.newConnection`
/ `connection.close` — and the CDP observer gained `FromServerMessage` for
messages the relay has already decoded. Close codes as shipped: 1011 with
reason `no container` (nothing bound), `container unreachable` (discovery or
dial failed) or `container closed` (the container ended the session) — the
console keys on the code; 1012 `service restart` on any change of the
target's upstream, including the container simply going away, since the
console's reconnect then lands on 1011 and waits; 1001 `keepalive timeout`
after two missed pongs at 20 s; and a target the flag keeps off answers 1011
`not listening: <reason>` rather than the descriptor's reason being fetched
first. The upstream change is a new `EventUpstream`, fired by `SetUpstream`
and `ClearContainer` only when the address actually changes. Descriptor
fields are `consoleDebug` (a `ConsoleProtocol` capability the inspector
declares) and `bridgePath` (`debugger.BridgePath`, set on every registered
target, empty on a synthesised entry). Origins accepted: the request's own
host and `localhost`/`127.0.0.1` on any port; `[::1]` is not, because
`path.Match` reads the brackets as a character class. Discovery is
`GET /json/list` with a 2 s bound, first entry with a `webSocketDebuggerUrl`,
host rewritten to the upstream. The source endpoint already had `?file=`; it
now reads a hot-reload mount (`overcast:hot-reload-path` under
`OVERCAST_LAMBDA_HOT_RELOAD`) for both the listing and the per-file read —
listing with the fingerprint's skip list and bounds, reading any path
resolved inside the mount, `.map` labelled `json` — and `PUT` still edits the
package only. `TestInvoke_debugger_consoleBridgeSpeaksCDPToTheContainer`
covers the bridge against a real Node container.

## 5. Tests

| Area | Tests |
| --- | --- |
| bridge | inspector relay both ways; `/json/list` discovery and host rewrite; no upstream → 1011; upstream change → 1012; attach count and observer pause seen on the `Target`; origin refused; keepalive close |
| BFF | upgrade proxied; a plain GET still answers |
| source endpoint | `.map` and nested paths returned by path; hot-reload mount read |
| cdp-client | correlation, event fan-out, reconnect, error responses |
| source-maps | inline and file maps; original↔generated both ways; sourcesContent fallback; unresolvable frame |
| session | breakpoint persistence and re-apply on `scriptParsed`; pause/resume state; watch evaluation on frame change |
| components | toolbar keys, gutter toggle, Locals expand, Watch edit, Call stack select, Breakpoints edit, console REPL, Test→Code navigation on pause |

## 6. Later

DAP client for Python on the same bridge and panels; logpoints via
`console.log` conditions; a "Restart container" that goes through the
existing hot-reload retire path; SSE events replacing the 2 s poll on the
Debug tab.
