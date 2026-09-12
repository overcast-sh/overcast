/**
 * The slice of the Chrome DevTools Protocol the console debugger speaks —
 * exactly the commands and events docs/plans/compute-debugger-console.md
 * § 3.4 lists, typed by hand because CDP ships no TypeScript package small
 * enough to be worth a dependency. Wire shapes are the protocol's own
 * (https://chromedevtools.github.io/devtools-protocol/); nothing here is
 * renamed or reshaped. Only `cdp-client.ts` and `session.ts` import it —
 * components read the protocol-neutral session state instead.
 */

// ─── Shared shapes ────────────────────────────────────────────────────────

/** `Debugger.Location`: a 0-based line and column in a parsed script. */
export interface CdpLocation {
  scriptId: string
  lineNumber: number
  columnNumber?: number
}

/** `Runtime.RemoteObject`, the value shape every evaluation and property answers with. */
export interface CdpRemoteObject {
  type: "object" | "function" | "undefined" | "string" | "number" | "boolean" | "symbol" | "bigint"
  subtype?: string
  className?: string
  value?: unknown
  unserializableValue?: string
  description?: string
  objectId?: string
  preview?: CdpObjectPreview
}

export interface CdpObjectPreview {
  type: string
  subtype?: string
  description?: string
  overflow: boolean
  properties: Array<{ name: string; type: string; value?: string; subtype?: string }>
}

export interface CdpPropertyDescriptor {
  name: string
  value?: CdpRemoteObject
  writable?: boolean
  get?: CdpRemoteObject
  set?: CdpRemoteObject
  configurable: boolean
  enumerable: boolean
  isOwn?: boolean
  symbol?: CdpRemoteObject
}

export interface CdpScope {
  type:
    | "global"
    | "local"
    | "with"
    | "closure"
    | "catch"
    | "block"
    | "script"
    | "eval"
    | "module"
    | "wasm-expression-stack"
  object: CdpRemoteObject
  name?: string
  startLocation?: CdpLocation
  endLocation?: CdpLocation
}

export interface CdpCallFrame {
  callFrameId: string
  functionName: string
  functionLocation?: CdpLocation
  location: CdpLocation
  url: string
  scopeChain: CdpScope[]
  this: CdpRemoteObject
  returnValue?: CdpRemoteObject
}

export interface CdpStackTrace {
  description?: string
  callFrames: Array<{
    functionName: string
    scriptId: string
    url: string
    lineNumber: number
    columnNumber: number
  }>
}

export interface CdpExceptionDetails {
  exceptionId: number
  text: string
  lineNumber: number
  columnNumber: number
  scriptId?: string
  url?: string
  stackTrace?: CdpStackTrace
  exception?: CdpRemoteObject
  executionContextId?: number
}

export type CdpPauseOnExceptionsState = "none" | "caught" | "uncaught" | "all"

// ─── Commands ─────────────────────────────────────────────────────────────

/** Every command the client may send, with its parameter and result shapes. */
export interface CdpCommands {
  "Debugger.enable": { params: { maxScriptsCacheSize?: number }; result: { debuggerId: string } }
  "Debugger.disable": { params: Record<string, never>; result: Record<string, never> }
  "Debugger.setBreakpointByUrl": {
    params: {
      lineNumber: number
      url?: string
      urlRegex?: string
      scriptHash?: string
      columnNumber?: number
      condition?: string
    }
    result: { breakpointId: string; locations: CdpLocation[] }
  }
  "Debugger.removeBreakpoint": {
    params: { breakpointId: string }
    result: Record<string, never>
  }
  "Debugger.resume": {
    params: { terminateOnResume?: boolean }
    result: Record<string, never>
  }
  "Debugger.stepOver": { params: Record<string, never>; result: Record<string, never> }
  "Debugger.stepInto": {
    params: { breakOnAsyncCall?: boolean }
    result: Record<string, never>
  }
  "Debugger.stepOut": { params: Record<string, never>; result: Record<string, never> }
  "Debugger.pause": { params: Record<string, never>; result: Record<string, never> }
  "Debugger.evaluateOnCallFrame": {
    params: {
      callFrameId: string
      expression: string
      objectGroup?: string
      includeCommandLineAPI?: boolean
      silent?: boolean
      returnByValue?: boolean
      generatePreview?: boolean
      throwOnSideEffect?: boolean
    }
    result: { result: CdpRemoteObject; exceptionDetails?: CdpExceptionDetails }
  }
  "Debugger.setPauseOnExceptions": {
    params: { state: CdpPauseOnExceptionsState }
    result: Record<string, never>
  }
  "Runtime.enable": { params: Record<string, never>; result: Record<string, never> }
  "Runtime.getProperties": {
    params: {
      objectId: string
      ownProperties?: boolean
      accessorPropertiesOnly?: boolean
      generatePreview?: boolean
      nonIndexedPropertiesOnly?: boolean
    }
    result: {
      result: CdpPropertyDescriptor[]
      internalProperties?: Array<{ name: string; value?: CdpRemoteObject }>
      exceptionDetails?: CdpExceptionDetails
    }
  }
  "Runtime.evaluate": {
    params: {
      expression: string
      objectGroup?: string
      includeCommandLineAPI?: boolean
      silent?: boolean
      contextId?: number
      returnByValue?: boolean
      generatePreview?: boolean
      awaitPromise?: boolean
    }
    result: { result: CdpRemoteObject; exceptionDetails?: CdpExceptionDetails }
  }
  "Runtime.releaseObjectGroup": {
    params: { objectGroup: string }
    result: Record<string, never>
  }
}

export type CdpMethod = keyof CdpCommands

// ─── Events ───────────────────────────────────────────────────────────────

/** Every event the client subscribes to, with its parameter shape. */
export interface CdpEvents {
  "Debugger.scriptParsed": {
    scriptId: string
    url: string
    startLine: number
    startColumn: number
    endLine: number
    endColumn: number
    executionContextId: number
    hash: string
    sourceMapURL?: string
    hasSourceURL?: boolean
    isModule?: boolean
    length?: number
    embedderName?: string
  }
  "Debugger.paused": {
    callFrames: CdpCallFrame[]
    reason: string
    data?: Record<string, unknown>
    hitBreakpoints?: string[]
    asyncStackTrace?: CdpStackTrace
  }
  "Debugger.resumed": Record<string, never>
  "Debugger.breakpointResolved": {
    breakpointId: string
    location: CdpLocation
  }
  "Runtime.consoleAPICalled": {
    type: string
    args: CdpRemoteObject[]
    executionContextId: number
    timestamp: number
    stackTrace?: CdpStackTrace
    context?: string
  }
  "Runtime.exceptionThrown": {
    timestamp: number
    exceptionDetails: CdpExceptionDetails
  }
  "Runtime.executionContextDestroyed": {
    executionContextId: number
    executionContextUniqueId?: string
  }
}

export type CdpEvent = keyof CdpEvents

// ─── Wire frames ──────────────────────────────────────────────────────────

export interface CdpRequestFrame {
  id: number
  method: string
  params?: unknown
}

export interface CdpResponseFrame {
  id: number
  result?: unknown
  error?: { code: number; message: string; data?: string }
}

export interface CdpEventFrame {
  method: string
  params?: unknown
}

export type CdpIncomingFrame = CdpResponseFrame | CdpEventFrame

export function isResponseFrame(frame: CdpIncomingFrame): frame is CdpResponseFrame {
  return typeof (frame as CdpResponseFrame).id === "number"
}
