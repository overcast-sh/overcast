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
press **Debug in console**, on the strip above the Code tab's editor or on
the **Debug** tab. It is there before the function has ever run, for tagged
Node.js functions only, so far — the Debug tab says so for Python, Java and
custom runtimes, which keep the editor configurations — and stays disabled
until the flag is on.

The status line under the button says what the session is doing:

| Status | Meaning |
| --- | --- |
| Connecting | The console is opening its connection to Overcast |
| Attached | A container is on the other end: set breakpoints and invoke |
| Waiting for a container | The function has no container yet; invoke once and the session attaches to the one that starts |
| Reconnecting | Hot reload replaced the container; breakpoints are re-applied when the new one loads the code |

The invocation that starts a container — the first, or the first after hot
reload replaced it — runs before the session reaches it and does not stop;
the Test tab says so under the result, and the next one does. Turn on **Wait
for a debugger before the first invocation** — on the same strip, or the
Debug tab — and such an invocation is held until a debugger attaches, up to
`OVERCAST_DEBUGGER_WAIT_TIMEOUT` (120 s by default), so it pauses too. The
switch sets the `overcast:debug-wait` tag on the function and holds for an
editor as well — see [Wait for a debugger](./debugger.md#wait-for-a-debugger).

A console session is one more attached client of the function's debug port.
The Debug tab's state chip reads `attached` and `paused` for it, and the
timeout clock stops as it does for an editor. **Stop debugging** ends the
session; breakpoints and watches are kept per function in the browser and
come back with the next one. A session still open when you leave the page is
started again when you come back, and the status line says *Session restored*
for a moment; only **Stop debugging** ends it.

## Breakpoints and stepping

Click a line's gutter in the Code tab to set or clear a breakpoint; F9 does
the same for the cursor's line. The gutter takes breakpoints before a session
starts too, and keeps them between sessions. Right-click the gutter, or click
it with Ctrl, Cmd or Alt held, for a condition: a strip opens inline above the
line and takes a JavaScript expression, evaluated in the function's scope, so
the breakpoint pauses only when it is truthy. Enter saves, Escape closes.

The glyph says what the runtime has done with the breakpoint: a filled dot is
placed on a statement, a hollow grey ring is waiting for a session or for the
container to load the file, a dim ring is disabled; hovering it says which.
A breakpoint on a line with no code moves to the next statement.

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

The keys work anywhere in the debug workspace — code, panels, drawer — and
nowhere else. While a session is open F5 is Continue, never the browser's
reload. A step that lands in the runtime's own code steps straight back out.
**Restart container** is on the strip, disabled until it is wired to hot
reload's replace path
([#1944](https://github.com/overcast-sh/overcast/issues/1944)).

## Invoke and pause

Invoke from the **Test** tab as usual. When execution stops, the page switches
to the Code tab with the paused line marked and the toolbar naming the function
and location. Continue to the end and the result lands in the Test tab; a
toast says so while another tab is up. An invocation that finishes without
pausing says why under its result.

The timeout clock is suspended while the session is open, as for an attached
editor; [Timeouts while paused](./debugger.md#timeouts-while-paused) says
what `context.getRemainingTimeInMillis()` does meanwhile. Module-level code
runs during init, before the session reaches the container, so the first
breakpoint goes inside the handler.

## Panels

Beside the code, while a session is open:

| Panel | Shows |
| --- | --- |
| Locals | The selected frame's scopes, expanding on click, with values previewed inline |
| Watch | Expressions you add, re-evaluated in the selected frame on every pause and frame change; the pencil beside one edits it. A value from the last pause stays, dimmed, while the function runs |
| Call stack | Every frame at the pause, at its original location when a source map applies; clicking a frame selects it for Locals and Watch |
| Breakpoints | Every breakpoint on the function — enable, disable, remove, edit the condition — and the pause-on-exceptions toggle |

Below the code, a drawer with two tabs, opening on whichever you used last:

| Drawer tab | Shows |
| --- | --- |
| Logs | The Monitor tab's log viewer on the function's log group, following its tail: the last 15 minutes, every request, with a marker at each pause and resume |
| Debug console | Thrown exceptions as they arrive, the same markers, and a prompt evaluated in the selected frame while paused, in the global scope while it runs |

The function's own `console.log` lines are in Logs: the runtime writes them
to the log stream, not to the debugger.

On a narrow window the panels become a tab strip above the drawer. Drag the
bar beside the panels or above the drawer to resize them, or focus it and use
the arrow keys; a double-click puts the default back. The sizes are
remembered in the browser.

## Source maps

Compiled TypeScript needs no path mapping here. The deployment's `.map` files,
beside the compiled output or inline in it, are read when the container loads
a script, and the file list gains an *Original* group holding the `.ts` files
the maps name. A breakpoint set in an original file is translated to the
compiled position, and the paused line, the call stack and Locals are shown in
original terms. The compiled files hide behind **Show compiled** once a map has
loaded. **Source maps**, beside it, turns the maps off for the function: the
file list is the deployed tree, breakpoints bind on compiled lines, the call
stack shows compiled locations, and a breakpoint in an original file is
listed in the Breakpoints panel as inactive, *source maps off*. Switching
while paused redraws the pause. Both switches are remembered per function.

| The map's source | The Code tab opens |
| --- | --- |
| Is in the deployment at that path | The file itself, as for any other file |
| Is only in the map's `sourcesContent` | The embedded text, read-only, with a notice saying so |
| Is in neither | An empty pane; set the breakpoint in the compiled file instead |

A frame no map resolves — a bundled dependency, say — opens the compiled file
with a *no source map for this frame* badge. Raw `.ts` on Node.js 24 and
plain JavaScript need none of this. A hot-reloaded function's files are read
live from the mounted directory, again each time the session reaches a new
container, so the tab shows what the container runs; a file you edited in
the tab keeps the edit.

## When it does not work

| Symptom | Cause | Fix |
| --- | --- | --- |
| No **Debug in console** button on the Debug tab | The runtime's protocol is not the inspector; Node.js only, so far | Attach an editor with the configurations beside it |
| The button is disabled | The flag is off | [Turn the debugger on](./debugger.md#turning-it-on) |
| "Waiting for a container — invoke once", and nothing happens | No container exists before the first invocation | Invoke from the Test tab; the session attaches to the container that starts |
| The invocation that started the container did not pause | It ran before the session reached the new container | Invoke again: the container is warm and the breakpoints are bound; or turn on **Wait for a debugger** |
| The session dropped after an edit | Hot reload replaced the container | Nothing: it reconnects and re-applies breakpoints once the new container loads the code |
| A breakpoint stays a hollow ring | The container has not loaded that file, or the path is not one it loads from `/var/task` | Invoke once so the file loads; otherwise open the file from the Code tab's list and set it there — for a bundle, in the original file the map lists |
| A breakpoint in module-level code never pauses | Init runs before the session attaches | Put the first breakpoint inside the handler |

## Related

- [Step debugging inside emulated compute](./debugger.md) — the flag, the tag, runtimes, timeouts and attaching an editor
- [The inner loop](./local-dev.md) — hot reload, which replaces the container under a session
- [Lambda examples](./services/lambda/examples.md) — the tag beside the other Lambda setups
