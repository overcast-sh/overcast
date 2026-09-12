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
  CdpLocation,
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
  /**
   * Whether the inspector has placed it on a statement of a loaded script.
   * A breakpoint set before the script parses is held (`bound`) but sits on
   * nothing until the script loads; one on a blank line is moved to the
   * next statement, and `line` follows it.
   */
  resolved: boolean
  /**
   * The file is an original a source map named, as last seen while binding.
   * With source maps off such a breakpoint has nothing to bind to — the
   * container never loads that file — so it is held inactive and listed as
   * such. Persisted, so it reads the same after a reload with maps off.
   */
  original: boolean
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
  /**
   * How many times a connection to a container has opened on this session:
   * 0 before the first, 2 or more after a replacement. A consumer holding
   * anything read from the container — its files — re-reads on a change.
   */
  connections: number
  /** How many pauses the session has seen, so a consumer can tell whether an invocation paused at all. */
  pauseCount: number
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
  /**
   * Whether source maps are read (§ 6). Off, every script is debugged as
   * deployed: no *Original* files, breakpoints bind on compiled lines, the
   * call stack shows compiled locations. Persisted per function; on by default.
   */
  sourceMaps: boolean
  /** Whether compiled files stay listed while a map hides them. Persisted per function. */
  showCompiled: boolean
  /**
   * The page started this session again for one that was open when it was
   * last left (§ 6, auto-restore). Cleared at the first pause and on stop —
   * a note, not a mode.
   */
  restored: boolean
}

// ─── Persistence ──────────────────────────────────────────────────────────

const STORAGE_PREFIX = "overcast-debug:"
const CONSOLE_LIMIT = 500
/** Retries after an invoke while the bridge still reports no container. */
const WAIT_RETRY_MS = 1_500
const WAIT_RETRY_LIMIT = 20
/**
 * How many runtime-internal frames a step is walked out of before it is
 * left where it landed — a guard against a step that never reaches the
 * deployment again, not a limit anyone should meet.
 */
const AUTO_STEP_OUT_LIMIT = 16

/**
 * The record under `localStorage["overcast-debug:<key>"]`. `sessionOpen` is
 * what auto-restore reads: set by `start`, cleared by `stop` and by nothing
 * else — leaving the page keeps it, which is the point.
 */
interface Persisted {
  breakpoints: Array<
    Pick<Breakpoint, "id" | "path" | "line" | "condition" | "enabled"> &
      Partial<Pick<Breakpoint, "original">>
  >
  watches: Array<Pick<Watch, "id" | "expression">>
  pauseOnExceptions: PauseOnExceptionsMode
  sessionOpen: boolean
  sourceMaps: boolean
  showCompiled: boolean
}

const EMPTY_PERSISTED: Persisted = {
  breakpoints: [],
  watches: [],
  pauseOnExceptions: "none",
  sessionOpen: false,
  sourceMaps: true,
  showCompiled: false,
}

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
      sessionOpen: parsed.sessionOpen === true,
      // A record written before the switch existed reads as "on".
      sourceMaps: parsed.sourceMaps !== false,
      showCompiled: parsed.showCompiled === true,
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

/**
 * The word for a pause, from the runtime's reason and what it stopped on.
 * V8 says `other` for a breakpoint hit and `ambiguous` for a step that
 * lands on one, so the reason alone reads wrong in a marker; a hit
 * breakpoint is a breakpoint, a thrown value an exception, and everything
 * else a step.
 */
export function pauseLabel(reason: string, hitBreakpoints: number, exception: string | null): string {
  if (hitBreakpoints > 0) return "breakpoint"
  if (exception !== null) return "exception"
  if (reason === "debugCommand") return "paused"
  return "step"
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
  /** A `breakpointResolved` that arrived before its bind's reply was recorded, by inspector id. */
  private readonly resolvedEarly = new Map<string, CdpLocation>()
  /** Whether the last execution command was a step, which is what auto-stepping out of internals keys on. */
  private stepping = false
  private autoStepOuts = 0
  private readonly scriptPaths = new Map<string, string>()
  /**
   * Every script parsed on this connection with the map URL it declared,
   * by path — what turning source maps on mid-session reads the maps from,
   * since the inspector does not announce a script twice.
   */
  private readonly scriptMapUrls = new Map<string, string | undefined>()
  /** The current pause's frames as the inspector sent them, so a map change can draw them again. */
  private rawFrames: CdpCallFrame[] = []
  /** `Persisted.sessionOpen` — see `restorable`. */
  private sessionOpenPersisted: boolean
  /** Whether the restore question has been answered for this session object — see `settleRestore`. */
  private restoreSettled = false
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
    this.sessionOpenPersisted = persisted.sessionOpen
    this.state = {
      status: "idle",
      error: null,
      connections: 0,
      pauseCount: 0,
      scripts: [],
      originalFiles: [],
      hasSourceMaps: false,
      breakpoints: persisted.breakpoints.map((bp) => ({
        ...bp,
        original: bp.original === true,
        bound: false,
        resolved: false,
      })),
      watches: persisted.watches.map((w) => ({ ...w, result: null, error: null })),
      pause: null,
      console: [],
      consoleHistory: [],
      pauseOnExceptions: persisted.pauseOnExceptions,
      sourceMaps: persisted.sourceMaps,
      showCompiled: persisted.showCompiled,
      restored: false,
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

  /**
   * Whether the page should start this session again: one was open on this
   * function when its page was last left — `start` records it, `stop`
   * clears it, leaving the page does not — and the question has not been
   * answered yet (§ 6). Read through `subscribe`, like the state.
   */
  get restorable(): boolean {
    return this.sessionOpenPersisted && !this.restoreSettled
  }

  /**
   * The restore question is answered — the session was started, or the
   * descriptor says the console is not on offer — so `restorable` stops
   * asking. A `start` of any kind settles it too.
   */
  settleRestore(): void {
    if (this.restoreSettled) return
    this.restoreSettled = true
    this.notify()
  }

  // ─── Lifecycle ──────────────────────────────────────────────────────────

  /**
   * Open a session against the bridge URL. A second call with a session
   * open is a no-op. `restored` marks a start the page made on the reader's
   * behalf, for the status line.
   */
  start(bridgeUrl: string, { restored = false }: { restored?: boolean } = {}): void {
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
      client.on("Debugger.breakpointResolved", (params) => this.onBreakpointResolved(params)),
      client.on("Runtime.consoleAPICalled", (params) => this.onConsole(params)),
      client.on("Runtime.exceptionThrown", (params) => this.onException(params)),
      client.on("Runtime.executionContextDestroyed", () => this.onResumed()),
    ]
    this.sessionOpenPersisted = true
    this.restoreSettled = true
    this.persist()
    this.set({ error: null, restored })
    client.open()
  }

  /**
   * Close the session and forget everything but what is persisted. The
   * reader's stop, so the session is not restored on the next visit;
   * `dispose` closes the same way without touching that.
   */
  stop(): void {
    if (this.sessionOpenPersisted) {
      this.sessionOpenPersisted = false
      this.persist()
    }
    this.close()
  }

  private close(): void {
    this.clearWaitRetry()
    const client = this.client
    if (!client) return
    for (const off of this.unsubscribeClient) off()
    this.unsubscribeClient = []
    this.client = null
    client.close()
    this.bound.clear()
    this.resolvedEarly.clear()
    this.scriptPaths.clear()
    this.scriptMapUrls.clear()
    this.rawFrames = []
    this.registry.clear()
    this.stepping = false
    this.set({
      status: "idle",
      restored: false,
      pause: null,
      scripts: [],
      originalFiles: [],
      hasSourceMaps: false,
      breakpoints: this.state.breakpoints.map((bp) => ({ ...bp, bound: false, resolved: false })),
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

  /** The "Session restored" note has been up long enough. */
  dismissRestoredNote(): void {
    if (this.state.restored) this.set({ restored: false })
  }

  /** The page is going away: close the socket, keep the restore flag so the next visit picks the session up. */
  dispose(): void {
    this.close()
    this.listeners.clear()
    this.pauseListeners.clear()
  }

  // ─── Execution control ──────────────────────────────────────────────────

  resume(): void {
    this.stepping = false
    this.command("Debugger.resume", {})
  }

  stepOver(): void {
    this.step("Debugger.stepOver")
  }

  stepInto(): void {
    this.step("Debugger.stepInto")
  }

  stepOut(): void {
    this.step("Debugger.stepOut")
  }

  pause(): void {
    this.stepping = false
    this.command("Debugger.pause", {})
  }

  private step(method: "Debugger.stepOver" | "Debugger.stepInto" | "Debugger.stepOut"): void {
    this.stepping = true
    this.autoStepOuts = 0
    this.command(method, {})
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

  // ─── Source maps ────────────────────────────────────────────────────────

  /**
   * Read source maps, or stop reading them (§ 6). Persisted per function.
   * With a session open the change applies at once: the registry is
   * emptied and, when turning on, filled again from the scripts this
   * connection has parsed; breakpoints are re-bound where the maps now say,
   * and a current pause is drawn again in the new terms.
   */
  setSourceMaps(on: boolean): void {
    if (this.state.sourceMaps === on) return
    this.set({ sourceMaps: on })
    this.persist()
    if (this.client) void this.reloadSourceMaps()
  }

  /** Keep compiled files listed while a map hides them. Persisted per function. */
  setShowCompiled(on: boolean): void {
    if (this.state.showCompiled === on) return
    this.set({ showCompiled: on })
    this.persist()
  }

  private async reloadSourceMaps(): Promise<void> {
    const epoch = this.epoch
    this.registry.clear()
    if (this.state.sourceMaps) {
      for (const [path, sourceMapURL] of this.scriptMapUrls) {
        await this.registry.register({ path, sourceMapURL })
        if (this.epoch !== epoch || !this.client) return
      }
    }
    this.publishScripts()
    this.remapPause()
    // Every breakpoint bound as an original is bound at a position a map
    // gave, which the apply pass drops and binds afresh — or, with maps off,
    // leaves inactive.
    await this.applyAll()
  }

  /** The current pause's frames, mapped through whatever the registry holds now. */
  private remapPause(): void {
    const pause = this.state.pause
    if (!pause) return
    this.set({ pause: { ...pause, frames: this.rawFrames.map((f) => this.toStackFrame(f)) } })
  }

  /** Scripts, original files and the maps flag, from the registry and the scripts seen. */
  private publishScripts(): void {
    this.set({
      scripts: [...this.scriptMapUrls.keys()].map((path) => ({
        path,
        mapped: this.registry.isMapped(path),
      })),
      originalFiles: this.registry
        .originalFiles()
        .map(({ path, origin, generated }) => ({ path, origin, generated: [...generated] })),
      hasSourceMaps: this.registry.hasMaps,
    })
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
    const bp: Breakpoint = {
      id: createId(),
      path,
      line,
      condition,
      enabled: true,
      bound: false,
      resolved: false,
      original: this.registry.isOriginal(path),
    }
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
      if (this.state.breakpoints.some((bp) => bp.bound || bp.resolved)) {
        patch.breakpoints = this.state.breakpoints.map((bp) => ({
          ...bp,
          bound: false,
          resolved: false,
        }))
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
    this.scriptMapUrls.clear()
    this.resolvedEarly.clear()
    // A new connection is a new container, and after a hot reload its
    // compiled output and maps may differ from the last one's: every map is
    // read again as its script parses, so a breakpoint on an original file
    // translates against what this container runs.
    this.registry.clear()
    this.set({
      scripts: [],
      originalFiles: [],
      hasSourceMaps: false,
      breakpoints: this.state.breakpoints.map((bp) => ({ ...bp, bound: false, resolved: false })),
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
    // Counted once the inspector has answered: a socket the bridge opens
    // only to close with "no container" reached nothing, and a session
    // waiting through several of those has not seen a replacement.
    const connections = this.state.connections + 1
    this.set({ connections })
    if (connections > 1) {
      // Said once, in the console and the logs, because the toolbar's
      // "reconnecting" comes and goes too fast to read — and because an
      // invocation already running in the new container by the time the
      // breakpoints land there ran past them.
      this.log(
        "marker",
        "Container replaced — reconnected; breakpoints are re-applied as the new container loads the code. An invocation already running there was not stopped.",
      )
    }
    await this.applyAll()
  }

  private async onScriptParsed(params: CdpEvents["Debugger.scriptParsed"]): Promise<void> {
    const path = scriptPath(params.url)
    if (path === null) return
    const epoch = this.epoch
    this.scriptPaths.set(params.scriptId, path)
    this.scriptMapUrls.set(path, params.sourceMapURL)
    // A script the container loads is a deployed file, whatever a map once
    // said about a breakpoint on it — so with maps off it binds as one.
    for (const bp of this.state.breakpoints) {
      if (bp.original && bp.path === path) {
        this.patchBreakpoint(bp.id, { original: false })
        this.persist()
      }
    }
    if (this.state.sourceMaps) {
      await this.registry.register({ path, sourceMapURL: params.sourceMapURL })
      // The map was fetched for a connection that has since been replaced:
      // the new one's own scriptParsed reports what it runs.
      if (this.epoch !== epoch || !this.client) return
    }
    this.publishScripts()
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
    const top = frames.at(0)
    // A step that lands in the runtime's own code — the patched console,
    // Node's internals — is walked back out, as an editor's skipFiles
    // would: the reader asked to step through their function, and a pane
    // that can show nothing for the frame is not where the step ends. A
    // breakpoint or an exception there is a real stop and is kept.
    if (
      this.stepping &&
      top?.internal &&
      hitBreakpointIds.length === 0 &&
      exception === null &&
      this.autoStepOuts < AUTO_STEP_OUT_LIMIT
    ) {
      this.autoStepOuts += 1
      this.command("Debugger.stepOut", {})
      return
    }
    this.pauseCounter += 1
    this.rawFrames = params.callFrames
    const pause: PauseState = {
      id: this.pauseCounter,
      reason: params.reason,
      frames,
      selectedFrame: 0,
      hitBreakpointIds,
      exception,
    }
    const label = pauseLabel(params.reason, hitBreakpointIds.length, exception)
    this.log(
      "marker",
      top ? `Paused at ${top.location.path}:${top.location.line} (${label})` : `Paused (${label})`,
    )
    // The restore note has done its job once the session is visibly working.
    this.set({ pause, pauseCount: this.pauseCounter, restored: false })
    this.evaluateWatches()
    for (const listener of this.pauseListeners) listener(pause)
  }

  /**
   * The inspector placed a held breakpoint on a statement — when its script
   * loaded, or right away for a script already loaded. The breakpoint moves
   * to that line if the inspector shifted it off a line with no code, which
   * is what an editor's gutter does too.
   */
  private onBreakpointResolved(params: CdpEvents["Debugger.breakpointResolved"]): void {
    const entry = [...this.bound.entries()].find(([, b]) => b.cdpId === params.breakpointId)
    if (!entry) {
      this.resolvedEarly.set(params.breakpointId, params.location)
      return
    }
    this.resolveBreakpoint(entry[0], params.location)
  }

  private resolveBreakpoint(id: string, location: CdpLocation): void {
    const bp = this.state.breakpoints.find((b) => b.id === id)
    if (!bp) return
    const path = this.scriptPaths.get(location.scriptId)
    const generated: Position = {
      path: path ?? bp.path,
      line: location.lineNumber + 1,
      column: location.columnNumber ?? 0,
    }
    const shown = (path && this.registry.toOriginal(generated)) || generated
    const line = shown.path === bp.path ? shown.line : bp.line
    if (line !== bp.line) {
      const occupant = this.breakpointAt(bp.path, line)
      if (occupant && occupant.id !== bp.id) {
        // The next statement already has one: this breakpoint would sit on
        // top of it, so it goes, and the other keeps the line.
        this.removeBreakpoint(bp.id)
        return
      }
      this.patchBreakpoint(bp.id, { line, resolved: true })
      this.persist()
      return
    }
    this.patchBreakpoint(bp.id, { resolved: true })
  }

  private onResumed(): void {
    if (!this.state.pause) return
    this.rawFrames = []
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
    // A script the runtime gave no URL (an eval, a patched builtin) is the
    // runtime's own; it is named as such rather than as an unknown file.
    const path = deployed ?? (frame.url || "(runtime internals)")
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
    // An original file's breakpoint has nowhere to bind without its map:
    // the container never loads that file. Held, and listed as inactive.
    if (!this.state.sourceMaps && bp.original) return
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
      const { breakpointId, locations } = await client.send("Debugger.setBreakpointByUrl", {
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
        this.patchBreakpoint(bp.id, { bound: true, original: asOriginal })
        if (asOriginal !== bp.original) this.persist()
        // A script already loaded answers with where the breakpoint landed;
        // one not yet loaded answers later, through breakpointResolved —
        // which may already have arrived while this reply was in flight.
        const location = locations.at(0) ?? this.resolvedEarly.get(breakpointId)
        this.resolvedEarly.delete(breakpointId)
        if (location) this.resolveBreakpoint(bp.id, location)
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
    this.patchBreakpoint(bp.id, { bound: false, resolved: false })
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
    this.notify()
  }

  private notify(): void {
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
      breakpoints: this.state.breakpoints.map(
        ({ id, path, line, condition, enabled, original }) => ({
          id,
          path,
          line,
          condition,
          enabled,
          original,
        }),
      ),
      watches: this.state.watches.map(({ id, expression }) => ({ id, expression })),
      pauseOnExceptions: this.state.pauseOnExceptions,
      sessionOpen: this.sessionOpenPersisted,
      sourceMaps: this.state.sourceMaps,
      showCompiled: this.state.showCompiled,
    })
  }
}
