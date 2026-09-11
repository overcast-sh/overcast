/**
 * The console debug session store (docs/plans/compute-debugger-console.md
 * § 3.4): one per function page, shared by the Code, Test and Debug tabs
 * through `DebugSessionProvider`. Components read `getState()` through
 * `useSyncExternalStore` and call the methods; nothing they see names a
 * protocol. This file and `cdp-client.ts` are the only CDP-aware modules
 * besides `source-maps.ts`.
 *
 * What it holds: the connection state, the scripts the inspector has
 * parsed, breakpoints (persisted per function, with condition and enabled
 * flag), watches (persisted; evaluated on every pause and frame change),
 * the current pause with its call frames mapped through source maps, a
 * console buffer with the REPL's history, and the pause-on-exceptions mode.
 *
 * Values cross to the panels as `RemoteValue` — one line of text plus a
 * handle for `getProperties` — so a Locals tree, a watch and a console
 * result are drawn by one component that never sees a `RemoteObject`.
 * Evaluations are grouped (`watch`, `console`) and the groups released on
 * resume, so a pause's handles do not pin the runtime's heap after it.
 *
 * Breakpoints survive a container replacement because every open of the
 * socket starts a new *epoch*: nothing is considered bound until it has been
 * sent to the inspector on this connection, and `scriptParsed` re-runs the
 * apply pass — which is also when a breakpoint on an original file first
 * becomes translatable, once the script's map has loaded.
 */
import { createId } from "@/lib/id"
import { CdpClient, type BridgeDial, type CdpClientStatus, webSocketDial } from "./cdp-client"
import type {
  CdpCallFrame,
  CdpCommands,
  CdpEvents,
  CdpExceptionDetails,
  CdpMethod,
  CdpPropertyDescriptor,
  CdpRemoteObject,
} from "./cdp-protocol"
import {
  type OriginalContentOrigin,
  type Position,
  SourceMapRegistry,
  scriptPath,
  scriptUrl,
} from "./source-maps"

// ─── Public state ─────────────────────────────────────────────────────────

/**
 * `idle` — no session. `connecting` — dialling. `attached` — the inspector is
 * on the line. `reconnecting` — the container went away under us and a
 * retry is scheduled. `waiting` — the bridge has no container to relay to
 * yet ("invoke once"). `error` — stopped by something that will not fix
 * itself; `error` says what.
 */
export type SessionStatus =
  "idle" | "connecting" | "attached" | "reconnecting" | "waiting" | "error"

export type PauseOnExceptionsMode = "none" | "uncaught" | "all"

export interface Breakpoint {
  /** Stable client id, kept across sessions and reloads. */
  id: string
  /** The file as shown — an original file when a map names it, else the deployed one. */
  path: string
  /** 1-based. */
  line: number
  /** JavaScript expression; empty for an unconditional breakpoint. */
  condition: string
  enabled: boolean
  /** Whether the inspector holds it on the current connection. */
  bound: boolean
}

/**
 * A value as the panels see it: one line of text and, for anything with
 * children, a handle `getProperties` accepts. `type` and `subtype` are the
 * runtime's own words (`object`/`array`, `string`, `function`, …) and are
 * only used to colour the text.
 */
export interface RemoteValue {
  type: string
  subtype: string | null
  /** The primitive as written (strings quoted), or an object's preview. */
  description: string
  objectId: string | null
}

/** One row under an expanded value. */
export interface Property {
  name: string
  /** `null` for an accessor whose getter has not been run. */
  value: RemoteValue | null
  /** A getter-backed property. It is never invoked on the reader's behalf. */
  accessor: boolean
  enumerable: boolean
  /** A runtime-internal row such as `[[Prototype]]` or `[[Entries]]`. */
  internal: boolean
}

export type EvalResult = { ok: true; value: RemoteValue } | { ok: false; error: string }

export interface Watch {
  id: string
  expression: string
  /** The last evaluation's value, or `null` before one or after an error. */
  result: RemoteValue | null
  error: string | null
}

export interface Scope {
  kind: string
  name: string | null
  /** Handle for the variables in this scope; `null` when the runtime gave none. */
  objectId: string | null
}

export interface StackFrame {
  /** The runtime's handle for the frame — what evaluation needs. */
  id: string
  functionName: string
  /** Where to show it: the original position when a map resolves it. */
  location: Position
  /** The position in the deployed file, always. */
  generated: Position
  /** False when no map resolves this frame — show `generated` with a badge. */
  mapped: boolean
  /** A frame outside the deployment — the runtime's own or Node's internals; folded in the call stack. */
  internal: boolean
  scopes: Scope[]
}

export interface PauseState {
  /** Increments per pause, so a consumer can tell a new pause from a re-render. */
  id: number
  reason: string
  frames: StackFrame[]
  selectedFrame: number
  /** Client breakpoint ids the runtime says it stopped on. */
  hitBreakpointIds: string[]
  /** The exception's description when the pause is on one. */
  exception: string | null
}

/**
 * `log`…`debug` and `exception` come from the runtime; `marker` is a pause or
 * resume; `input` and `result` are the REPL's own echo and answer.
 */
export type ConsoleEntryKind =
  "log" | "info" | "warn" | "error" | "debug" | "exception" | "marker" | "input" | "result"

export interface ConsoleEntry {
  id: number
  kind: ConsoleEntryKind
  text: string
  /** Epoch milliseconds. */
  timestamp: number
  /** A `result` with children to expand; absent on every other kind. */
  value?: RemoteValue
}

export interface ScriptRecord {
  path: string
  mapped: boolean
}

export interface OriginalFileRecord {
  path: string
  origin: OriginalContentOrigin
  generated: string[]
}

export interface DebugSessionState {
  status: SessionStatus
  error: string | null
  scripts: ScriptRecord[]
  originalFiles: OriginalFileRecord[]
  hasSourceMaps: boolean
  breakpoints: Breakpoint[]
  watches: Watch[]
  pause: PauseState | null
  console: ConsoleEntry[]
  /** What the REPL has been asked so far, oldest first; not persisted. */
  consoleHistory: string[]
  pauseOnExceptions: PauseOnExceptionsMode
}

// ─── Persistence ──────────────────────────────────────────────────────────

const STORAGE_PREFIX = "overcast-debug:"
const CONSOLE_LIMIT = 500
/** Retries after an invoke while the bridge still reports no container. */
const WAIT_RETRY_MS = 1_500
const WAIT_RETRY_LIMIT = 20

interface Persisted {
  breakpoints: Array<Pick<Breakpoint, "id" | "path" | "line" | "condition" | "enabled">>
  watches: Array<Pick<Watch, "id" | "expression">>
  pauseOnExceptions: PauseOnExceptionsMode
}

const EMPTY_PERSISTED: Persisted = { breakpoints: [], watches: [], pauseOnExceptions: "none" }

function readPersisted(storage: Storage | null, key: string): Persisted {
  if (!storage) return EMPTY_PERSISTED
  try {
    const raw = storage.getItem(STORAGE_PREFIX + key)
    if (raw === null) return EMPTY_PERSISTED
    const parsed = JSON.parse(raw) as Partial<Persisted>
    return {
      breakpoints: Array.isArray(parsed.breakpoints) ? parsed.breakpoints : [],
      watches: Array.isArray(parsed.watches) ? parsed.watches : [],
      pauseOnExceptions: parsed.pauseOnExceptions ?? "none",
    }
  } catch {
    return EMPTY_PERSISTED
  }
}

function writePersisted(storage: Storage | null, key: string, value: Persisted): void {
  if (!storage) return
  try {
    storage.setItem(STORAGE_PREFIX + key, JSON.stringify(value))
  } catch {
    // Quota or a private window: the session still works, it just forgets.
  }
}

function defaultStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage
  } catch {
    return null
  }
}

// ─── Helpers ──────────────────────────────────────────────────────────────

/** A `RemoteObject` as one line of console text — strings bare, as `console.log` prints them. */
export function describeRemoteObject(obj: CdpRemoteObject): string {
  if (obj.value !== undefined) {
    if (typeof obj.value === "string") return obj.value
    try {
      return JSON.stringify(obj.value)
    } catch {
      return String(obj.value)
    }
  }
  return obj.unserializableValue ?? obj.description ?? obj.type
}

const PREVIEW_ITEMS = 6
const FUNCTION_PREVIEW_CHARS = 60

/** `{a: 1, b: 'x', …}` / `(3) [1, 2, 3]` from the runtime's own preview, the way DevTools draws it. */
function previewText(obj: CdpRemoteObject): string {
  const preview = obj.preview
  const description = obj.description ?? obj.className ?? "Object"
  if (!preview) return description
  if (preview.subtype && preview.subtype !== "array") return description
  const items = preview.properties.slice(0, PREVIEW_ITEMS).map((p) => {
    const value =
      p.type === "string"
        ? JSON.stringify(p.value ?? "")
        : p.type === "object"
          ? p.subtype === "array"
            ? (p.value ?? "[…]")
            : p.subtype === "null"
              ? "null"
              : "{…}"
          : p.type === "function"
            ? "ƒ"
            : (p.value ?? p.type)
    return preview.subtype === "array" ? value : `${p.name}: ${value}`
  })
  const overflow = preview.overflow || preview.properties.length > PREVIEW_ITEMS
  if (overflow) items.push("…")
  return preview.subtype === "array"
    ? `(${preview.properties.length}${overflow ? "+" : ""}) [${items.join(", ")}]`
    : `{${items.join(", ")}}`
}

/** A `RemoteObject` as the panels show it: strings quoted, objects previewed, functions signed. */
export function toRemoteValue(obj: CdpRemoteObject): RemoteValue {
  const base = { type: obj.type, subtype: obj.subtype ?? null, objectId: obj.objectId ?? null }
  if (obj.type === "string") return { ...base, description: JSON.stringify(obj.value ?? "") }
  if (obj.type === "function") {
    const first = (obj.description ?? "ƒ").split("\n", 1)[0].trim()
    return {
      ...base,
      description:
        first.length > FUNCTION_PREVIEW_CHARS
          ? `${first.slice(0, FUNCTION_PREVIEW_CHARS)}…`
          : first,
    }
  }
  if (obj.type === "object") {
    if (obj.subtype === "null") return { ...base, description: "null" }
    return { ...base, description: previewText(obj) }
  }
  return { ...base, description: describeRemoteObject(obj) }
}

/** The text of a failed evaluation: the thrown value when there is one, else the runtime's summary. */
function exceptionText(details: CdpExceptionDetails): string {
  return details.exception ? describeRemoteObject(details.exception) : details.text
}

/** A property descriptor as a tree row. An accessor's getter is not run: the row says so and stops there. */
function toProperty(d: CdpPropertyDescriptor): Property {
  return {
    name: d.name,
    value: d.value ? toRemoteValue(d.value) : null,
    accessor: !d.value && d.get !== undefined,
    enumerable: d.enumerable,
    internal: false,
  }
}

/** The description of a thrown value carried in a pause's `data`, when it is one. */
function describeException(data: Record<string, unknown> | undefined): string | null {
  if (!data) return null
  const description = data.description ?? data.value
  return typeof description === "string" ? description : null
}

const CONSOLE_KINDS: Partial<Record<string, ConsoleEntryKind>> = {
  log: "log",
  info: "info",
  warning: "warn",
  error: "error",
  debug: "debug",
  trace: "debug",
  assert: "error",
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function escapeRegex(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
}

function clientStatusToSession(status: CdpClientStatus): SessionStatus {
  switch (status) {
    case "closed":
      return "idle"
    case "connecting":
      return "connecting"
    case "open":
      return "attached"
    case "reconnecting":
      return "reconnecting"
    case "no-container":
      return "waiting"
  }
}

// ─── Session ──────────────────────────────────────────────────────────────

export interface DebugSessionOptions {
  /** Persistence key — `service/resource`. */
  key: string
  /** Read a deployed file by root-relative path. */
  fetchFile: (path: string) => Promise<string>
  /** Test seam: a scripted socket instead of a WebSocket. */
  dial?: BridgeDial
  /** Test seam: where breakpoints persist. `null` disables persistence. */
  storage?: Storage | null
  /** Test seam: reconnect delays. */
  backoffMs?: readonly number[]
}

type Listener = () => void

export class DebugSession {
  private readonly key: string
  private readonly storage: Storage | null
  private readonly dial: BridgeDial
  private readonly backoffMs: readonly number[] | undefined
  private readonly registry: SourceMapRegistry
  private readonly listeners = new Set<Listener>()
  private readonly pauseListeners = new Set<(pause: PauseState) => void>()

  private state: DebugSessionState
  private client: CdpClient | null = null
  private unsubscribeClient: Array<() => void> = []
  private epoch = 0
  /**
   * What the inspector holds per client breakpoint on the current epoch.
   * `asOriginal` records whether the path was a mapped original when it was
   * bound: a breakpoint set on a file before its map arrived is bound to the
   * raw path, and must be re-bound at the translated position once the map
   * says the file is an original.
   */
  private readonly bound = new Map<string, { epoch: number; cdpId: string; asOriginal: boolean }>()
  /** Breakpoints with a bind in flight, so two apply passes cannot double-bind one. */
  private readonly binding = new Set<string>()
  private readonly scriptPaths = new Map<string, string>()
  private deploymentFiles = new Set<string>()
  private pauseCounter = 0
  private consoleCounter = 0
  private waitRetryTimer: ReturnType<typeof setTimeout> | null = null
  private waitRetries = 0

  constructor({ key, fetchFile, dial = webSocketDial, storage, backoffMs }: DebugSessionOptions) {
    this.key = key
    this.storage = storage === undefined ? defaultStorage() : storage
    this.dial = dial
    this.backoffMs = backoffMs
    this.registry = new SourceMapRegistry({
      fetchFile,
      hasFile: (path) => this.deploymentFiles.has(path),
    })
    const persisted = readPersisted(this.storage, key)
    this.state = {
      status: "idle",
      error: null,
      scripts: [],
      originalFiles: [],
      hasSourceMaps: false,
      breakpoints: persisted.breakpoints.map((bp) => ({ ...bp, bound: false })),
      watches: persisted.watches.map((w) => ({ ...w, result: null, error: null })),
      pause: null,
      console: [],
      consoleHistory: [],
      pauseOnExceptions: persisted.pauseOnExceptions,
    }
  }

  // ─── Store contract ─────────────────────────────────────────────────────

  getState = (): DebugSessionState => this.state

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  /** Fires once per pause with the new pause state — the hook for "switch to the Code tab". */
  onPause(listener: (pause: PauseState) => void): () => void {
    this.pauseListeners.add(listener)
    return () => {
      this.pauseListeners.delete(listener)
    }
  }

  /** The deployed file list, so an original file the zip also carries opens from the zip. */
  setDeploymentFiles(paths: Iterable<string>): void {
    this.deploymentFiles = new Set(paths)
  }

  /** Whether the session holds anything the socket needs — true in every status but `idle` and `error`. */
  get isOpen(): boolean {
    return this.client !== null
  }

  // ─── Lifecycle ──────────────────────────────────────────────────────────

  /** Open a session against the bridge URL. A second call with a session open is a no-op. */
  start(bridgeUrl: string): void {
    if (this.client) return
    const client = new CdpClient({ url: bridgeUrl, dial: this.dial, backoffMs: this.backoffMs })
    this.client = client
    this.unsubscribeClient = [
      client.onStatus((status) => this.onClientStatus(status)),
      client.onOpen(() => {
        void this.onOpen()
      }),
      client.on("Debugger.scriptParsed", (params) => {
        void this.onScriptParsed(params)
      }),
      client.on("Debugger.paused", (params) => this.onPaused(params)),
      client.on("Debugger.resumed", () => this.onResumed()),
      client.on("Runtime.consoleAPICalled", (params) => this.onConsole(params)),
      client.on("Runtime.exceptionThrown", (params) => this.onException(params)),
      client.on("Runtime.executionContextDestroyed", () => this.onResumed()),
    ]
    this.set({ error: null })
    client.open()
  }

  /** Close the session and forget everything but what is persisted. */
  stop(): void {
    this.clearWaitRetry()
    const client = this.client
    if (!client) return
    for (const off of this.unsubscribeClient) off()
    this.unsubscribeClient = []
    this.client = null
    client.close()
    this.bound.clear()
    this.scriptPaths.clear()
    this.registry.clear()
    this.set({
      status: "idle",
      pause: null,
      scripts: [],
      originalFiles: [],
      hasSourceMaps: false,
      breakpoints: this.state.breakpoints.map((bp) => ({ ...bp, bound: false })),
      watches: this.state.watches.map((w) => ({ ...w, result: null, error: null })),
    })
  }

  /** Dial again — the way out of `waiting` once a container exists. */
  retry(): void {
    if (!this.client || this.state.status !== "waiting") return
    this.client.open()
  }

  /**
   * The Test tab pressed Invoke. A session waiting for a container keeps
   * retrying for a while, since the container the invoke starts takes a
   * moment to come up and bind.
   */
  invokeStarted(): void {
    if (this.state.status !== "waiting") return
    this.waitRetries = 0
    this.retry()
    this.scheduleWaitRetry()
  }

  /** The polled descriptor reports a different container: a waiting session tries again. */
  containerChanged(): void {
    this.retry()
  }

  dispose(): void {
    this.stop()
    this.listeners.clear()
    this.pauseListeners.clear()
  }

  // ─── Execution control ──────────────────────────────────────────────────

  resume(): void {
    this.command("Debugger.resume", {})
  }

  stepOver(): void {
    this.command("Debugger.stepOver", {})
  }

  stepInto(): void {
    this.command("Debugger.stepInto", {})
  }

  stepOut(): void {
    this.command("Debugger.stepOut", {})
  }

  pause(): void {
    this.command("Debugger.pause", {})
  }

  setPauseOnExceptions(mode: PauseOnExceptionsMode): void {
    this.set({ pauseOnExceptions: mode })
    this.persist()
    this.command("Debugger.setPauseOnExceptions", { state: mode })
  }

  selectFrame(index: number): void {
    const pause = this.state.pause
    if (!pause || index < 0 || index >= pause.frames.length) return
    this.set({ pause: { ...pause, selectedFrame: index } })
    this.evaluateWatches()
  }

  // ─── Evaluation ─────────────────────────────────────────────────────────

  /**
   * Evaluate an expression: in a call frame while paused (the selected one
   * unless `frameIndex` names another), globally otherwise. Never throws —
   * a runtime exception, a closed socket and a missing session all come back
   * as `{ ok: false }` with the reason.
   */
  evaluate(expression: string, frameIndex?: number): Promise<EvalResult> {
    return this.evaluateIn(expression, "console", frameIndex)
  }

  /** The own properties of a value, getters left unread. Rejects when the handle is stale or the socket gone. */
  async getProperties(objectId: string): Promise<Property[]> {
    const client = this.client
    if (!client) throw new Error("No session")
    const reply = await client.send("Runtime.getProperties", {
      objectId,
      ownProperties: true,
      generatePreview: true,
    })
    if (reply.exceptionDetails) throw new Error(exceptionText(reply.exceptionDetails))
    const own = reply.result.map((d) => toProperty(d))
    const internal = (reply.internalProperties ?? []).map((p): Property => ({
      name: p.name,
      value: p.value ? toRemoteValue(p.value) : null,
      accessor: false,
      enumerable: false,
      internal: true,
    }))
    return [...own, ...internal]
  }

  /**
   * The REPL: echo the expression, evaluate it where `evaluate` would, and
   * record the answer or the error as console entries.
   */
  async runConsoleCommand(expression: string): Promise<void> {
    const trimmed = expression.trim()
    if (trimmed === "") return
    const history = this.state.consoleHistory
    this.set({
      consoleHistory: history.at(-1) === trimmed ? history : [...history, trimmed],
    })
    this.log("input", trimmed)
    const result = await this.evaluateIn(trimmed, "console")
    if (result.ok) this.log("result", result.value.description, undefined, result.value)
    else this.log("error", result.error)
  }

  private async evaluateIn(
    expression: string,
    objectGroup: "console" | "watch",
    frameIndex?: number,
  ): Promise<EvalResult> {
    const client = this.client
    if (!client || client.status !== "open") return { ok: false, error: "No session" }
    const pause = this.state.pause
    const frame = pause?.frames.at(frameIndex ?? pause.selectedFrame)
    try {
      const reply = frame
        ? await client.send("Debugger.evaluateOnCallFrame", {
            callFrameId: frame.id,
            expression,
            objectGroup,
            includeCommandLineAPI: true,
            generatePreview: true,
          })
        : await client.send("Runtime.evaluate", {
            expression,
            objectGroup,
            includeCommandLineAPI: true,
            generatePreview: true,
            awaitPromise: true,
          })
      if (reply.exceptionDetails) return { ok: false, error: exceptionText(reply.exceptionDetails) }
      return { ok: true, value: toRemoteValue(reply.result) }
    } catch (err) {
      return { ok: false, error: errorMessage(err) }
    }
  }

  /**
   * Re-evaluate every watch against the selected frame. Each answer is
   * applied only if the pause and frame it was asked for are still the
   * current ones, so a step that lands before a slow reply cannot paint a
   * stale value over the new frame.
   */
  private evaluateWatches(): void {
    const pause = this.state.pause
    if (!pause) return
    const { id: pauseId, selectedFrame } = pause
    for (const watch of this.state.watches) {
      void this.evaluateIn(watch.expression, "watch", selectedFrame).then((result) => {
        const current = this.state.pause
        if (!current || current.id !== pauseId || current.selectedFrame !== selectedFrame) return
        // A watch has one line; the stack trace of a failed one belongs to the console.
        this.patchWatch(watch.id, {
          result: result.ok ? result.value : null,
          error: result.ok ? null : result.error.split("\n", 1)[0],
        })
      })
    }
  }

  /** Let go of everything a pause's evaluations pinned; their handles are invalid after it. */
  private releaseObjectGroups(): void {
    const client = this.client
    if (client?.status === "open") {
      for (const objectGroup of ["watch", "console"] as const) {
        // Nothing to report on failure: a context that has already gone
        // took the group with it.
        void client.send("Runtime.releaseObjectGroup", { objectGroup }).catch(() => {})
      }
    }
    this.set({
      watches: this.state.watches.map((w) =>
        w.result?.objectId ? { ...w, result: { ...w.result, objectId: null } } : w,
      ),
    })
  }

  // ─── Breakpoints ────────────────────────────────────────────────────────

  breakpointAt(path: string, line: number): Breakpoint | undefined {
    return this.state.breakpoints.find((bp) => bp.path === path && bp.line === line)
  }

  /** Add a breakpoint on a line, or remove the one already there. */
  toggleBreakpoint(path: string, line: number): void {
    const existing = this.breakpointAt(path, line)
    if (existing) this.removeBreakpoint(existing.id)
    else this.addBreakpoint(path, line)
  }

  addBreakpoint(path: string, line: number, condition = ""): Breakpoint {
    const existing = this.breakpointAt(path, line)
    if (existing) {
      this.updateBreakpoint(existing.id, { condition, enabled: true })
      return this.breakpointAt(path, line) ?? existing
    }
    const bp: Breakpoint = { id: createId(), path, line, condition, enabled: true, bound: false }
    this.set({ breakpoints: [...this.state.breakpoints, bp] })
    this.persist()
    void this.apply(bp)
    return bp
  }

  updateBreakpoint(id: string, patch: Partial<Pick<Breakpoint, "condition" | "enabled">>): void {
    const bp = this.state.breakpoints.find((b) => b.id === id)
    if (!bp) return
    const next = { ...bp, ...patch }
    if (next.condition === bp.condition && next.enabled === bp.enabled) return
    this.patchBreakpoint(id, patch)
    this.persist()
    this.unapply(bp)
    void this.apply(next)
  }

  removeBreakpoint(id: string): void {
    const bp = this.state.breakpoints.find((b) => b.id === id)
    if (!bp) return
    this.set({ breakpoints: this.state.breakpoints.filter((b) => b.id !== id) })
    this.persist()
    this.unapply(bp)
  }

  // ─── Watches ────────────────────────────────────────────────────────────

  addWatch(expression: string): Watch {
    const watch: Watch = { id: createId(), expression, result: null, error: null }
    this.set({ watches: [...this.state.watches, watch] })
    this.persist()
    this.evaluateWatches()
    return watch
  }

  updateWatch(id: string, expression: string): void {
    this.patchWatch(id, { expression, result: null, error: null })
    this.persist()
    this.evaluateWatches()
  }

  removeWatch(id: string): void {
    this.set({ watches: this.state.watches.filter((w) => w.id !== id) })
    this.persist()
  }

  // ─── Console ────────────────────────────────────────────────────────────

  clearConsole(): void {
    this.set({ console: [] })
  }

  // ─── Files ──────────────────────────────────────────────────────────────

  /** The text of an original file the maps named, or `null` when nothing has it. */
  originalContent(path: string): Promise<string | null> {
    return this.registry.originalContent(path)
  }

  isOriginalFile(path: string): boolean {
    return this.registry.isOriginal(path)
  }

  // ─── Protocol edge ──────────────────────────────────────────────────────

  /** A command whose reply carries nothing; a failure is reported to the console buffer. */
  private command<M extends CdpMethod>(method: M, params: CdpCommands[M]["params"]): void {
    const client = this.client
    if (!client) return
    void client.send(method, params).catch((err: unknown) => {
      this.log("error", `${method} failed: ${errorMessage(err)}`)
    })
  }

  private onClientStatus(status: CdpClientStatus): void {
    const next = clientStatusToSession(status)
    const patch: Partial<DebugSessionState> = { status: next }
    if (next !== "attached") {
      // Whatever the old connection held is gone with it: a pause cannot
      // outlive its socket, and a breakpoint is bound again only once the
      // next open re-sends it.
      patch.pause = null
      if (this.state.breakpoints.some((bp) => bp.bound)) {
        patch.breakpoints = this.state.breakpoints.map((bp) => ({ ...bp, bound: false }))
      }
    }
    this.set(patch)
    if (next === "attached") this.clearWaitRetry()
  }

  private async onOpen(): Promise<void> {
    const client = this.client
    if (!client) return
    this.epoch += 1
    this.scriptPaths.clear()
    // A new connection is a new container, and after a hot reload its
    // compiled output and maps may differ from the last one's: every map is
    // read again as its script parses, so a breakpoint on an original file
    // translates against what this container runs.
    this.registry.clear()
    this.set({
      scripts: [],
      originalFiles: [],
      hasSourceMaps: false,
      breakpoints: this.state.breakpoints.map((bp) => ({ ...bp, bound: false })),
    })
    try {
      await client.send("Debugger.enable", {})
      await client.send("Runtime.enable", {})
      if (this.state.pauseOnExceptions !== "none") {
        await client.send("Debugger.setPauseOnExceptions", { state: this.state.pauseOnExceptions })
      }
    } catch (err) {
      // The socket dropped mid-handshake; the reconnect will run this again.
      if (this.client === client && client.status === "open") {
        this.set({
          status: "error",
          error: `Could not enable the debugger: ${errorMessage(err)}`,
        })
      }
      return
    }
    await this.applyAll()
  }

  private async onScriptParsed(params: CdpEvents["Debugger.scriptParsed"]): Promise<void> {
    const path = scriptPath(params.url)
    if (path === null) return
    const epoch = this.epoch
    this.scriptPaths.set(params.scriptId, path)
    const mapped = await this.registry.register({ path, sourceMapURL: params.sourceMapURL })
    // The map was fetched for a connection that has since been replaced:
    // the new one's own scriptParsed reports what it runs.
    if (this.epoch !== epoch || !this.client) return
    const scripts = this.state.scripts.filter((s) => s.path !== path)
    this.set({
      scripts: [...scripts, { path, mapped }],
      originalFiles: this.registry
        .originalFiles()
        .map(({ path: p, origin, generated }) => ({ path: p, origin, generated: [...generated] })),
      hasSourceMaps: this.registry.hasMaps,
    })
    await this.applyAll()
  }

  private onPaused(params: CdpEvents["Debugger.paused"]): void {
    const frames = params.callFrames.map((frame) => this.toStackFrame(frame))
    const cdpIds = new Set(params.hitBreakpoints ?? [])
    const hitBreakpointIds = [...this.bound.entries()]
      .filter(([, b]) => cdpIds.has(b.cdpId))
      .map(([id]) => id)
    // On an exception pause `data` is the thrown value as a RemoteObject.
    const exception =
      params.reason === "exception" || params.reason === "promiseRejection"
        ? (describeException(params.data) ?? params.reason)
        : null
    this.pauseCounter += 1
    const pause: PauseState = {
      id: this.pauseCounter,
      reason: params.reason,
      frames,
      selectedFrame: 0,
      hitBreakpointIds,
      exception,
    }
    const top = frames.at(0)
    this.log(
      "marker",
      top
        ? `Paused at ${top.location.path}:${top.location.line} (${params.reason})`
        : `Paused (${params.reason})`,
    )
    this.set({ pause })
    this.evaluateWatches()
    for (const listener of this.pauseListeners) listener(pause)
  }

  private onResumed(): void {
    if (!this.state.pause) return
    this.log("marker", "Resumed")
    this.set({ pause: null })
    this.releaseObjectGroups()
  }

  private onConsole(params: CdpEvents["Runtime.consoleAPICalled"]): void {
    // Strings bare, objects previewed — the line `console.log` would print.
    const text = params.args
      .map((arg) =>
        arg.type === "object" ? toRemoteValue(arg).description : describeRemoteObject(arg),
      )
      .join(" ")
    this.log(CONSOLE_KINDS[params.type] ?? "log", text, params.timestamp)
  }

  private onException(params: CdpEvents["Runtime.exceptionThrown"]): void {
    const details = params.exceptionDetails
    const text = details.exception ? describeRemoteObject(details.exception) : details.text
    this.log("exception", text, params.timestamp)
  }

  private toStackFrame(frame: CdpCallFrame): StackFrame {
    const deployed = this.scriptPaths.get(frame.location.scriptId) ?? scriptPath(frame.url)
    // A script the runtime gave no URL (an eval, a patched builtin) is shown as such.
    const path = deployed ?? (frame.url || "(unknown)")
    const generated: Position = {
      path,
      line: frame.location.lineNumber + 1,
      column: frame.location.columnNumber ?? 0,
    }
    const original = this.registry.toOriginal(generated)
    return {
      id: frame.callFrameId,
      functionName: frame.functionName || "(anonymous)",
      location: original ?? generated,
      generated,
      mapped: original !== null,
      internal: deployed === null,
      scopes: frame.scopeChain.map((scope) => ({
        kind: scope.type,
        name: scope.name ?? null,
        objectId: scope.object.objectId ?? null,
      })),
    }
  }

  // ─── Breakpoint binding ─────────────────────────────────────────────────

  /** Bind every enabled breakpoint not yet bound on this epoch, re-binding any a newly loaded map has re-classified. */
  private async applyAll(): Promise<void> {
    for (const bp of this.state.breakpoints) {
      const entry = this.bound.get(bp.id)
      if (entry?.epoch === this.epoch && entry.asOriginal !== this.registry.isOriginal(bp.path)) {
        this.unapply(bp)
      }
      await this.apply(bp)
    }
  }

  /** The deployed position a breakpoint binds at, or `null` when its original line has no mapping yet. */
  private generatedFor(bp: Breakpoint): Position | null {
    if (this.registry.isOriginal(bp.path)) {
      return this.registry.toGenerated({ path: bp.path, line: bp.line, column: 0 })
    }
    return { path: bp.path, line: bp.line, column: 0 }
  }

  private async apply(bp: Breakpoint): Promise<void> {
    const client = this.client
    if (!client || client.status !== "open" || !bp.enabled) return
    if (this.bound.get(bp.id)?.epoch === this.epoch || this.binding.has(bp.id)) return
    const generated = this.generatedFor(bp)
    if (!generated) return
    const epoch = this.epoch
    const asOriginal = this.registry.isOriginal(bp.path)
    // Set when the reply describes a binding the client no longer wants —
    // the breakpoint changed, or its file became a mapped original while
    // the bind was in flight; the inspector's copy is dropped and the
    // current breakpoint bound afresh once the in-flight mark is cleared.
    let stale = false
    this.binding.add(bp.id)
    try {
      const { breakpointId } = await client.send("Debugger.setBreakpointByUrl", {
        lineNumber: generated.line - 1,
        columnNumber: asOriginal ? generated.column : undefined,
        urlRegex: `${escapeRegex(scriptUrl(generated.path))}$`,
        condition: bp.condition || undefined,
      })
      if (this.epoch !== epoch || this.client !== client) return
      const current = this.state.breakpoints.find((b) => b.id === bp.id)
      stale =
        !current ||
        current.condition !== bp.condition ||
        !current.enabled ||
        this.registry.isOriginal(bp.path) !== asOriginal
      if (stale) {
        void client.send("Debugger.removeBreakpoint", { breakpointId }).catch(() => {})
      } else {
        this.bound.set(bp.id, { epoch, cdpId: breakpointId, asOriginal })
        this.patchBreakpoint(bp.id, { bound: true })
      }
    } catch (err) {
      this.log("error", `Could not set breakpoint at ${bp.path}:${bp.line}: ${errorMessage(err)}`)
    } finally {
      this.binding.delete(bp.id)
    }
    if (stale) {
      const current = this.state.breakpoints.find((b) => b.id === bp.id)
      if (current) await this.apply(current)
    }
  }

  /**
   * Drop the inspector's copy of a breakpoint. The remove is not awaited: a
   * re-bind that follows goes out behind it on the same socket, and the
   * reply carries nothing worth waiting for.
   */
  private unapply(bp: Breakpoint): void {
    const binding = this.bound.get(bp.id)
    this.bound.delete(bp.id)
    this.patchBreakpoint(bp.id, { bound: false })
    const client = this.client
    if (!binding || binding.epoch !== this.epoch || !client || client.status !== "open") return
    void client.send("Debugger.removeBreakpoint", { breakpointId: binding.cdpId }).catch(() => {
      // Already gone on the inspector's side; nothing to restore.
    })
  }

  // ─── Waiting for a container ────────────────────────────────────────────

  private scheduleWaitRetry(): void {
    this.clearWaitRetry()
    if (this.waitRetries >= WAIT_RETRY_LIMIT) return
    this.waitRetryTimer = setTimeout(() => {
      this.waitRetryTimer = null
      if (this.state.status !== "waiting") return
      this.waitRetries += 1
      this.retry()
      this.scheduleWaitRetry()
    }, WAIT_RETRY_MS)
  }

  private clearWaitRetry(): void {
    if (this.waitRetryTimer !== null) {
      clearTimeout(this.waitRetryTimer)
      this.waitRetryTimer = null
    }
  }

  // ─── State plumbing ─────────────────────────────────────────────────────

  private set(patch: Partial<DebugSessionState>): void {
    this.state = { ...this.state, ...patch }
    for (const listener of this.listeners) listener()
  }

  private patchBreakpoint(id: string, patch: Partial<Breakpoint>): void {
    if (!this.state.breakpoints.some((bp) => bp.id === id)) return
    this.set({
      breakpoints: this.state.breakpoints.map((bp) => (bp.id === id ? { ...bp, ...patch } : bp)),
    })
  }

  private patchWatch(id: string, patch: Partial<Watch>): void {
    if (!this.state.watches.some((w) => w.id === id)) return
    this.set({ watches: this.state.watches.map((w) => (w.id === id ? { ...w, ...patch } : w)) })
  }

  private log(
    kind: ConsoleEntryKind,
    text: string,
    timestamp = Date.now(),
    value?: RemoteValue,
  ): void {
    this.consoleCounter += 1
    const entry: ConsoleEntry = { id: this.consoleCounter, kind, text, timestamp }
    if (value?.objectId) entry.value = value
    const next = [...this.state.console, entry]
    this.set({ console: next.length > CONSOLE_LIMIT ? next.slice(-CONSOLE_LIMIT) : next })
  }

  private persist(): void {
    writePersisted(this.storage, this.key, {
      breakpoints: this.state.breakpoints.map(({ id, path, line, condition, enabled }) => ({
        id,
        path,
        line,
        condition,
        enabled,
      })),
      watches: this.state.watches.map(({ id, expression }) => ({ id, expression })),
      pauseOnExceptions: this.state.pauseOnExceptions,
    })
  }
}
