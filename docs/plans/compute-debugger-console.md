# Compute debugger, phase 2: step debugging inside the console — plan

> Status: **complete** 2026-09-12 — issue #1944. Phase A (`438af1481`, the
> WebSocket bridge, BFF upgrade proxy, descriptor fields and the source
> endpoint's per-file reads), Phase B1 (`a66987eb1`, the session store, CDP
> client, source maps, CodeBrowser props, gutter breakpoints, toolbar and
> pause navigation), Phase B2 (`0f9cb6d0b`, the Locals/Watch/Call stack/
> Breakpoints panels, the Logs + Debug console drawer, the lifted invoke and
> the responsive layout), Phase C (`8f8173189`, `docs/debugger-console.md`
> and the changelog fragment) and Phase D (the review pass over the whole
> branch, whose fixes landed as `6194dd273`, `cae983087`, `72fccb9ba`,
> `0743ac7b9` and `159255875`) are all in. Builds on
> [compute-debugger.md](./compute-debugger.md) (#1939, shipped in #1941), whose
> § 11 and § 11.1 are the design this plan turned into work.
>
> What shipped matches § 3's contracts, with the deviations the phase notes
> record. The invoke stream carries no request id, so the Logs drawer is the
> function's whole log-group tail rather than one request's, and the program's
> `console.log` lines show there — the Lambda runtime writes them to the log
> stream, not to the inspector — while the Debug console shows evaluations,
> exceptions and pause markers. The invoke moved out of the Test tab (which a
> pause unmounts) into `features/lambda/use-invoke.ts`, owned by the route, so
> Continue still lands the result where it was asked for. Fixed panel widths
> stand in for a resizable-panel primitive the repo does not have, and the
> code pane takes a bounded height while a session is open so the whole
> workspace fits one screen. Source maps are re-read on every reconnect, since
> a hot-reload rebuild may change them. "Restart container" is on the toolbar
> disabled: it waits on the hot-reload retire path in § 6. The review pass
> also made the bridge's truncated close reason valid UTF-8 and the call stack
> a plain ARIA list. Read [compute-debugger.md](./compute-debugger.md) first;
> this document only added what phase 2 needed and pinned the shared contracts.

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
  **Phase B1 notes** (landed; what B2 builds on). The session lives in
  `web/src/features/debugger/session/`: `cdp-protocol.ts` (the typed
  command/event slice), `cdp-client.ts` (`CdpClient` over an injectable
  `BridgeDial`; `webSocketDial` in production, `src/test/fake-bridge.ts` in
  tests; 1011 → `no-container`, 1012 and plain drops → backoff reconnect),
  `source-maps.ts` (`SourceMapRegistry`: `register`, `toGenerated`,
  `toOriginal`, `originalFiles`, `originalContent`), and `session.ts` —
  `DebugSession`, a `useSyncExternalStore` store whose `DebugSessionState`
  is `{ status, error, scripts, originalFiles, hasSourceMaps, breakpoints,
  watches, pause, console, pauseOnExceptions }` with `Breakpoint { id, path,
  line, condition, enabled, bound }`, `Watch { id, expression, value,
  error }`, `PauseState { id, reason, frames, selectedFrame,
  hitBreakpointIds, exception }`, `StackFrame { id, functionName, location,
  generated, mapped, scopes[{ kind, name, objectId }] }` and `ConsoleEntry {
  id, kind, text, timestamp }` (kinds `log|info|warn|error|debug|exception|
  marker`; pause/resume markers are already written). Actions: `start(url)`,
  `stop`, `retry`, `invokeStarted`, `containerChanged`, `resume`, `stepOver`,
  `stepInto`, `stepOut`, `pause`, `setPauseOnExceptions`, `selectFrame`,
  `toggleBreakpoint`, `addBreakpoint`, `updateBreakpoint`,
  `removeBreakpoint`, `breakpointAt`, `addWatch`, `updateWatch`,
  `removeWatch`, `clearConsole`, `originalContent`, `isOriginalFile`,
  `onPause(listener)`. Hooks (`session/hooks.ts`): `useDebugSession`,
  `useOptionalDebugSession`, `useDebugSessionState(selector)`; the provider
  is `session/provider.tsx` (`DebugSessionProvider`, mounted around the
  tabs in `routes/lambda/$name.tsx`, with `session/context.ts` for tests).
  `session/status.ts` has `sessionStatusLine`, `isSessionOpen`,
  `selectedFrame`. Components (`components/`): `DebugCodeBrowser` (wraps
  `CodeBrowser`; the toolbar, keys, gutter, condition editor, *Original*
  group and *Show compiled* toggle), `DebugToolbar`,
  `BreakpointConditionEditor`, `DebugSessionControls` (Debug tab),
  `OnPause`. `CodeBrowser` gained `decorations`, `onGutterClick`,
  `revealPosition`, `explorerActions`, `BrowserFile.group` and
  `LoadedFile.readOnly`/`notice`. B2 adds `evaluate`/`getProperties` to
  `DebugSession` (the client stays private to it) and the watch evaluation
  on pause and frame change; the `watches` shape and `Scope.objectId` are
  what Locals and Watch consume. Two things B1 could not settle: the
  descriptor's `consoleDebug`/`bridgePath` are read through
  `target.ts#consoleDebugOf` as optional until the regenerated `api.gen.ts`
  lands (then the cast in `debug-session-controls.test.tsx` goes); and the
  bridge URL is minted on the console's origin under `/api` — the
  endpoint-selection headers cannot ride a WebSocket upgrade, so a console
  pointed at a non-default emulator needs the BFF to accept the endpoint
  another way (phase A, or later).

- **B2 — panels and drawer.** Locals, Watch, Call stack, Breakpoints, Logs
  and Debug console; responsive layout. After A and B1 are on the task
  branch; verified live against a real function.
  **Phase B2 notes** (landed). The session gained the evaluation surface
  and nothing else learned CDP: `RemoteValue` (`type`, `subtype`,
  `description`, `objectId`) and `Property` (`value`, `accessor`,
  `enumerable`, `internal`) are what the panels see; `evaluate(expression,
  frameIndex?)` runs `evaluateOnCallFrame` in the selected frame while
  paused and `Runtime.evaluate` otherwise, never throws (`EvalResult`);
  `getProperties(objectId)` reads own properties with previews, leaves a
  getter unread (`accessor: true`, drawn as `(getter)`) and lists
  `[[Prototype]]`-style rows last; `runConsoleCommand` echoes, evaluates and
  records the answer (`input`/`result` console kinds, a `result` carrying
  its `RemoteValue` for expansion) and keeps `consoleHistory` in state.
  Watches now hold `result: RemoteValue | null` rather than a string and are
  re-evaluated in the `watch` object group on every pause, `selectFrame`,
  add and edit, with a stale answer (frame moved before the reply) dropped;
  `watch` and `console` groups are released on resume and the handles
  cleared. `StackFrame.internal` marks a frame outside the deployment (the
  runtime's bootstrap, `node:` modules; a script with no URL reads
  `(unknown)`), which the call stack folds and the code pane neither
  reveals nor marks. Components, one per panel, all reading through the
  hooks: `ValueTree` (the one tree — WAI-ARIA `tree`/`treeitem`, lazy
  children, arrow-key navigation, items named by their own row), `LocalsPanel`
  (scopes as roots, Local open by default, keyed per pause and frame),
  `WatchPanel`, `CallStackPanel` (mapped location shown, compiled one in
  the tooltip, "n internal frames" folds), `BreakpointsPanel` (checkbox,
  condition through the same `BreakpointConditionEditor` as the gutter,
  pause-on-exceptions with all three modes), `DebugConsole` (live-region
  log, REPL with ↑/↓ history, expandable results), `DebugLogs` (the Monitor
  tab's `LogViewer` over `logPanelQueryOptions`, 15-minute relative window
  polled every 2 s while shown, the session's markers merged by time — see
  `log-markers.ts`), `DebugSidebar` (collapsible sections wide, a tab strip
  below the app's 900 px breakpoint via `hooks/use-media-query.ts`),
  `DebugDrawer` (Logs | Debug console, unseen-entry badge) and
  `DebugWorkspace`, which `CodeTab` wraps around `DebugCodeBrowser` and
  which is the child alone until a session opens. Fixed widths (20 rem
  sidebar scrolling inside the code pane's height, 16 rem drawer): the repo
  has no resizable-panel primitive and none was added. The two B1 loose
  ends closed: `bridgeUrl` names a non-default emulator as `?ep=`, the
  query the BFF's `resolveEndpointQP` reads, and `consoleDebugOf` reads the
  generated fields directly. Three deviations. The invoke stream carries no
  request id, so the Logs drawer is the group's whole tail with a note
  saying so. The Lambda Node runtime patches `console.*` to write to
  stdout, so the program's `console.log` lines never arrive as
  `Runtime.consoleAPICalled`; they show in the Logs drawer (from CloudWatch)
  and the Debug console shows evaluations, exceptions and markers. And the
  invoke result would have been lost with the Test tab, which the pause
  unmounts: it now lives in `features/lambda/use-invoke.ts`
  (`useLambdaInvoke`), owned by the function route and handed to
  `TestTab`, so Continue lands the result where it was asked for. For
  `pnpm run dev` the Vite config proxies `/api/debugger/targets/*/ws`
  upgrades straight to the Go BFF (the Hono middleware is a fetch per
  request and cannot carry a WebSocket). Verified live on 2026-09-12
  against `go run ./cmd/overcast serve` with `OVERCAST_DEBUGGER=true` on
  4590/4591 and `pnpm run dev` on a non-default endpoint: a `nodejs22.x`
  function tagged `overcast:debug=true` paused at a gutter breakpoint in
  its helper with the marker on the line, Locals (`n: 21`, `doubled: 42`,
  an uninitialised `const` as `(unavailable)`, an array expanded lazily
  with `(3) [42, "value-42", {…}]` previews), a watch (`doubled * 10:
  420`, re-evaluated per step and per frame), the call stack (`helper`,
  `exports.handler`, one folded internal frame), the console REPL
  (`[n * 3, { doubled, half: doubled / 2 }]` → expandable `(2) [63, {…}]`),
  Step over, Step into (into the runtime's patched `console.info`, folded),
  Step out, Continue, the result on the Test tab, and the Logs drawer with
  `── Paused at index.js:3 (other) ──` between the request's START line and
  its END; a second function deployed as `dist/index.js` + `.js.map` +
  `src/index.ts` (tsc) showed the *Original* group, bound a breakpoint set
  in the `.ts` (`Compiled: dist/index.js:6:18` on hover) and paused at
  `src/index.ts:8` with `.ts` locations in the call stack. Screenshots
  (outside the repo): `paused-panels-dark.png`, `paused-panels-light.png`,
  `paused-source-map-ts.png`, `paused-narrow-layout.png` in the session
  scratchpad.
- **C — docs.** `docs/debugger-console.md` (one concern, linked from
  `docs/debugger.md`), changelog fragment, this plan's status. Parallel with
  B2 in its own worktree.
- **D — review.** Repo `code-review` skill over the branch; fixes; full
  verification; PR with screenshots (paused state, panels, dark and light).
- **Polish pass** (2026-09-12, driven end to end in a real browser against
  five functions: plain, hot-reload, tsc + maps, untagged, Python). What it
  found and changed, first-time path first. A tagged function that had never
  cold-started read *OFF · not tagged* on the Debug tab and the overview and
  offered no console button — registration only happened at the first
  container start — so `Service.DescribeUntagged` now registers a tagged
  function's target on describe (the same `Ensure` the cold start makes),
  and the page reads `unbound` and offers the console before the first
  invoke. The Code tab is now a place to start: idle on a resource that
  offers a console session it shows a *Debug in console* strip, keeps the
  gutter live so breakpoints are set before a session and between sessions,
  and draws them hollow. Breakpoints now report where the runtime placed
  them — the reply's `locations` and `Debugger.breakpointResolved` — so one
  set on a blank line moves to the next statement, `Breakpoint.resolved`
  drives a filled / hollow (`glyph-pending`) / dim glyph with a title saying
  which, and the Breakpoints panel reads the same. The stepping keys moved
  from the code pane to `DebugWorkspace`, so they work from a panel or the
  console input, and F5 is swallowed whenever a session is open (a reflex
  reload dropped the session). A step landing in a runtime-internal frame
  — the runtime's patched `console.log` — is walked back out automatically
  (bounded), as an editor's skipFiles would; pause markers say *breakpoint*
  / *step* / *exception* rather than V8's `other` / `ambiguous`. The first
  invocation after a session starts waiting, or after hot reload replaces
  the container, runs before the session reaches the new container and so
  never pauses: the Test tab now says so under the result, the console marks
  the replacement, and the Code tab re-reads its files (`CodeBrowser`
  gained `contentVersion`, keyed on the session's `connections`, keeping
  edited files) so a hot-reloaded function shows what now runs. The invoke
  result arriving while another tab is up raises a toast. Smaller: the Logs
  drawer follows its tail (`LogViewer` `follow`), the drawer remembers its
  tab, a watch value from the last pause dims while running, the console's
  empty line no longer promises the program's output, the Debug tab
  explains a non-inspector target (Python) instead of showing nothing, and
  the Test tab counts breakpoints when nothing paused. Left as design
  questions in the report: holding the first invocation until a waiting
  console attaches (server-side), auto-restoring a session on reload, the
  function list's badge for never-run tagged functions, resizable panels.

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

## 6. Follow-ups decided 2026-09-12 (Phase F, same PR)

The four questions the polish pass left were answered: the first-invocation
hold is a per-function toggle; auto-restore, create-time registration and
resizable panels are all in, chosen for the best experience. A fifth item
came from review: a source-maps on/off switch. Contracts:

- **Wait for a debugger** (`overcast:debug-wait=true`, `Spec.Wait`). With it
  set, an invocation whose target has no attached client is held after the
  environment is acquired and before the event is dispatched, until a client
  attaches plus a 750 ms settle, so a console or editor that attaches on the
  cold start still sees its breakpoints bind before the handler runs. The
  invocation clock is suspended while holding (it already is while attached).
  The hold is bounded by `OVERCAST_DEBUGGER_WAIT_TIMEOUT` (default `120s`);
  on expiry the invocation proceeds and a `WARN` says so. `Descriptor` gains
  `waitForDebugger bool`. The console toggles it on the Debug tab and the idle
  Code tab strip by calling the AWS `TagResource`/`UntagResource` operations
  it already has — no new endpoint. Applies to editor sessions equally.
- **Registration at create time.** `CreateFunction`, `TagResource`,
  `UntagResource`, `UpdateFunctionConfiguration` and `DeleteFunction` keep the
  `Manager` in step, and the first `ListTargets` after a restart scans the
  store once (`sync.Once`, lazily) so the function list badge shows every
  tagged function without a page visit. Describe-time registration stays.
- **Auto-restore.** Starting a console session records
  `localStorage["overcast-debug:<id>"].sessionOpen = true`; Stop clears it.
  Mounting the provider with it set and `consoleDebug` true starts the
  session again. Cheap-when-idle holds: nothing happens for a function the
  user never opened a session on.
- **Resizable panels.** A generic `ResizableSplit` primitive in
  `web/src/components/ui/` (pointer drag, arrow keys on the focused handle,
  min/max, sizes persisted per key in `localStorage`) used for the sidebar
  width and the drawer height. No new dependency.
- **Source maps on/off.** A *Source maps* switch in the Code tab's explorer
  actions while a session is open, persisted per function
  (`localStorage["overcast-debug:<id>"].sourceMaps`, default on). Off: no map
  is loaded, no *Original* group, breakpoints bind on compiled lines, the call
  stack shows compiled locations, and breakpoints that belong to original
  files are listed in the Breakpoints panel as inactive with the reason.
  *Show compiled* stays as the visibility toggle when maps are on and is now
  remembered with the same key.

## 7. Later

DAP client for Python on the same bridge and panels; logpoints via
`console.log` conditions; a "Restart container" that goes through the
existing hot-reload retire path; SSE events replacing the 2 s poll on the
Debug tab.
