---
title: "Debugging in the console"
description: "Set a breakpoint in the Code tab's gutter, invoke from the Test tab, and step through a Node.js function inside the Overcast console, with locals, watches, the call stack and logs beside the code."
section: "Getting Started"
tags:
  - docs
  - guide
  - debugger
  - debugging
  - lambda
  - console
  - development
---

# Debugging in the console

The function page's Code tab is a step debugger for Node.js functions: a
breakpoint in the gutter, Invoke on the Test tab, and the page stops at the
paused line with locals, watches, the call stack, the request's logs and a
debug console beside it, in the browser.

## Start a session

Turn the debugger on as for an editor — `OVERCAST_DEBUGGER=true` and the
`overcast:debug=true` tag, from
[Step debugging inside emulated compute](./debugger.md#turning-it-on) — then
open the function's **Debug** tab and press **Debug in console**. The button
appears for Node.js functions only, so far: the console speaks the inspector
protocol, and Python, Java and custom runtimes keep the editor configurations
beside it. It stays disabled, with a tooltip saying so, until the flag and the
tag are both in place.

The status line under the button says what the session is doing:

| Status | Meaning |
| --- | --- |
| Connecting | The console is opening its connection to Overcast |
| Attached | A container is on the other end: set breakpoints and invoke |
| Waiting for a container | The function has no container yet; invoke once and the session attaches to the one that starts |
| Reconnecting | Hot reload replaced the container; breakpoints are re-applied when the new one loads the code |

A console session is one more attached client of the function's debug port.
The Debug tab's state chip reads `attached` and `paused` for it, and the
timeout clock stops as it does for an editor. **Stop debugging** ends the
session; breakpoints and watches are kept per function in the browser and
come back with the next one.

## Breakpoints and stepping

Click a line's gutter in the Code tab to set or clear a breakpoint; F9 does
the same for the cursor's line. Right-click the gutter, or click it with Ctrl,
Cmd or Alt held, for a condition: a strip opens inline above the line and
takes a JavaScript expression, evaluated in the function's scope, so the
breakpoint pauses only when it is truthy. Enter saves, Escape closes. Hovering
a breakpoint's glyph shows its condition and whether it has bound yet — a
breakpoint set before the first invocation waits for the container to load
the script.

Once paused, the toolbar over the pane drives execution, with the keys VS Code
uses:

| Control | Key | Does |
| --- | --- | --- |
| Continue | F5 | Run to the next breakpoint, or to the end of the invocation |
| Step over | F10 | The next line in the current function |
| Step into | F11 | Into the call on the current line |
| Step out | Shift+F11 | Out to the caller |
| Pause on exceptions | — | Toggle; pauses at uncaught exceptions, off by default |
| Stop | — | End the session; the invocation runs on |

The keys are bound while the code pane has focus, so they do nothing from the
Test tab's editor. **Restart container** sits on the strip disabled until it is
wired to hot reload's replace path
([#1944](https://github.com/overcast-sh/overcast/issues/1944)).

## Invoke and pause

Invoke from the **Test** tab as usual. When execution stops, the page switches
to the Code tab with the paused line marked and the toolbar naming the function
and location. Continue to the end and the result lands in the Test tab as it
always does.

The function's timeout clock is suspended while the session is open, exactly
as for an attached editor, so a 3-second function can sit at a breakpoint for
as long as you need; `context.getRemainingTimeInMillis()` keeps counting down
regardless, as
[Timeouts while paused](./debugger.md#timeouts-while-paused) explains.
Module-level code runs during init, before the session can reach the
container, so the first breakpoint goes inside the handler.

## Panels

Beside the code, while a session is open:

| Panel | Shows |
| --- | --- |
| Locals | The selected frame's scopes, expanding on click, with values previewed inline |
| Watch | Expressions you add, re-evaluated in the selected frame on every pause and frame change; click one to edit it |
| Call stack | Every frame at the pause, at its original location when a source map applies; clicking a frame selects it for Locals and Watch |
| Breakpoints | Every breakpoint on the function — enable, disable, remove, edit the condition — and the pause-on-exceptions toggle |

Below the code, a drawer with two tabs. **Logs** is the Monitor tab's log
viewer, live, filtered to the current request when the invocation supplies a
request id, with a marker line at each pause and resume. **Debug console**
shows `console.*` output and thrown exceptions as they arrive, and takes an
expression: evaluated in the selected frame while paused, in the function's
global scope while it runs. On a narrow window the panels become a tab strip
above the drawer.

## Source maps

Compiled TypeScript needs no path mapping here. The deployment's `.map` files,
beside the compiled output or inline in it, are read when the container loads
a script, and the file list gains an *Original* group holding the `.ts` files
the maps name. A breakpoint set in an original file is translated to the
compiled position, and the paused line, the call stack and Locals are shown in
original terms. The compiled files hide behind **Show compiled** once a map has
loaded.

| The map's source | The Code tab opens |
| --- | --- |
| Is in the deployment at that path | The file itself, as for any other file |
| Is only in the map's `sourcesContent` | The embedded text, read-only, with a notice saying so |
| Is in neither | An empty pane; set the breakpoint in the compiled file instead |

A frame no map resolves — a bundled dependency, say — opens the compiled file
with a *no source map for this frame* badge on the toolbar. Raw `.ts` on
Node.js 24 and plain JavaScript need none of this: the file list is the
deployed tree. A hot-reloaded function's files are read live from the mounted
directory, so the tab shows what the container runs.

## When it does not work

| Symptom | Cause | Fix |
| --- | --- | --- |
| No **Debug in console** button on the Debug tab | The runtime's protocol is not the inspector; Node.js only, so far | Attach an editor with the configurations beside it |
| The button is disabled | The flag or the tag is missing | [Turn the debugger on](./debugger.md#turning-it-on) and tag the function |
| "Waiting for a container — invoke once", and nothing happens | No container exists before the first invocation | Invoke from the Test tab; the session attaches to the container that starts |
| The session dropped after an edit | Hot reload replaced the container | Nothing: it reconnects and re-applies breakpoints once the new container loads the code |
| A breakpoint never binds | The path is not one the container loads from `/var/task` | Open the file from the Code tab's list and set it there; for a bundle, set it in the original file the map lists |
| A breakpoint in module-level code never pauses | Init runs before the session attaches | Put the first breakpoint inside the handler |

## Related

- [Step debugging inside emulated compute](./debugger.md) — the flag, the tag, runtimes, timeouts and attaching an editor
- [The inner loop](./local-dev.md) — hot reload, which replaces the container under a session
- [Lambda examples](./services/lambda/examples.md) — the tag beside the other Lambda setups
