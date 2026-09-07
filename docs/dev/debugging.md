# Step debugging

This page is about Overcast's own Go code. Attaching a debugger to *your*
code inside an emulated Lambda function or ECS task is covered by
[Step debugging inside emulated compute](../debugger.md).

Overcast supports full step debugging via [Delve](https://github.com/go-delve/delve),
Go's debugger. You can set breakpoints, step through handler code, inspect
variables, watch expressions, and navigate the call stack — all from VS Code.

---

## Log-based debugging

Step debugging isn't always the fastest tool — sometimes `OVERCAST_LOG_LEVEL=debug`
tells you what you need without a single breakpoint. Overcast's log levels are
`trace` < `debug` < `info` < `warn` < `error`; `debug` is tuned to be exactly
what you'd want attached to a bug report (dispatch/protocol decisions, store
operation diagnostics, replay/seed detail), while `trace` adds the
machine-generated chatter (health probes, `/_overcast/debug/*` polling, background
flush/sweep ticks) that's rarely useful even mid-debugging session but can
help when you specifically suspect a background loop. See
[CONTRIBUTING.md § Log levels](../../CONTRIBUTING.md#log-levels) for the full
call-site policy and the decision rule for where a given line belongs.

The two tools compose well: run at `debug` (or `trace` if you're chasing a
background loop) to narrow down where to look, then set a breakpoint once
you know which handler or loop to step through.

---

## Quick start

1. Open VS Code with the project (ideally in the Dev Container)
2. Open any `.go` file — for example `internal/services/s3/handler.go`
3. Click in the gutter to the left of a line number — a red dot appears
4. Press **F5** and select **"Debug: server (memory state)"**
5. Send a request from another terminal:
   ```bash
   aws --endpoint-url http://localhost:4566 s3 mb s3://test-bucket
   ```
6. The debugger pauses at your breakpoint

---

## Keyboard reference

| Key           | Action                                        |
| ------------- | --------------------------------------------- |
| F5            | Start / Continue (run to next breakpoint)     |
| F10           | Step Over — run this line, pause on the next  |
| F11           | Step Into — follow the call into the function |
| Shift+F11     | Step Out — run to end of current function     |
| Shift+F5      | Stop the debug session                        |
| Ctrl+Shift+F5 | Restart the debug session                     |
| F9            | Toggle breakpoint on current line             |

---

## What you can inspect while paused

**VARIABLES panel** (left sidebar, Debug view)
Shows all local variables and their current values. Structs are expandable —
you can drill into nested fields. For example, paused in `GetObject`, you'll see
`h` (the handler), `r` (the request), `bucket` and `key` as strings, `obj` once
it's been fetched.

**WATCH panel**
Type any Go expression and it's evaluated in the current scope:

- `len(req.Body)` — body size
- `r.Header.Get("X-Amz-Target")` — target header
- `h.store` — the entire store object

**CALL STACK panel**
Shows the full call chain. You can click any frame to jump to that code and
inspect that frame's variables. For a request handler this will show:
`GetObject → ServeHTTP → middleware chain → net/http internals`

**DEBUG CONSOLE**
Type and evaluate Go expressions interactively while paused. You can call
functions, index into maps, format values — anything that's valid in that scope.

**Hover inspection**
Hover over any variable in the editor while paused — a tooltip shows its current
value. For structs, the tooltip is expandable.

---

## Debug configurations

All configurations are in `.vscode/launch.json`. Select one in the Run & Debug
panel (`Ctrl+Shift+D`) before pressing F5.

| Configuration                         | What it does                                                                                    |
| ------------------------------------- | ----------------------------------------------------------------------------------------------- |
| **Debug: server (memory state)**      | Start the full server under the debugger. Set breakpoints in handler files, then send requests. |
| **Debug: server (sqlite state)**      | Same but with SQLite persistence.                                                               |
| **Debug: test under cursor**          | Highlight a test function name, press F5. Only that test runs.                                  |
| **Debug: all tests in current file**  | Debug every test in the open file.                                                              |
| **Debug: S3 integration tests**       | Full S3 integration suite under the debugger.                                                   |
| **Debug: SQS integration tests**      | Full SQS integration suite under the debugger.                                                  |
| **Debug: DynamoDB integration tests** | Full DynamoDB integration suite under the debugger.                                             |
| **Debug: attach to running Delve**    | Attach to a Delve server on port 2345 (advanced — see below).                                   |

---

## Debugging a failing test

The most common debugging workflow is a failing test:

1. Open the test file where the test is failing
2. Set a breakpoint in the test function itself (on the `When:` line)
3. Also set a breakpoint in the handler code the test exercises
4. Select the test function name (highlight it with your cursor)
5. Press F5 → "Debug: test under cursor"
6. The debugger starts, runs the test, pauses at your breakpoint
7. Step through — inspect what the handler actually received vs what you expected
8. Check the `VARIABLES` panel to see exactly what's in `req`, `aerr`, `resp`, etc.

---

## Debugging handler code

To debug a specific endpoint being called by an external client:

1. Set a breakpoint at the top of the handler method, e.g. `func (h *Handler) PutObject`
2. F5 → "Debug: server (memory state)"
3. Wait for the server to start (watch the terminal — it prints "overcast listening")
4. From another terminal (on your host machine for Windows, or in a second VS Code terminal):
   ```bash
   aws --endpoint-url http://localhost:4566 s3 cp myfile.txt s3://my-bucket/
   ```
5. The debugger pauses at your breakpoint
6. Step through the handler — check the parsed bucket/key, inspect the store call, etc.

---

## Conditional breakpoints

Right-click a breakpoint dot and choose "Edit Breakpoint" to add a condition:

```
bucket == "production-bucket"
aerr != nil
req.MaxNumberOfMessages > 1
```

The debugger only pauses when the condition is true — useful for finding the
specific case that fails in a loop or across many requests.

---

## Logpoints (non-breaking)

Right-click the gutter and choose "Add Logpoint" instead of a breakpoint.
Enter a message like `"handling PutObject: bucket={bucket}, key={key}"`.
The debugger prints this to the Debug Console without pausing — zero-overhead
printf debugging without modifying code.

---

## Inside the Dev Container

The Dev Container is pre-configured for debugging:

- `--cap-add=SYS_PTRACE` and `--security-opt seccomp=unconfined` allow Delve to
  control processes (Docker's default seccomp profile blocks this)
- Delve is installed during container setup (`postCreateCommand`)
- Port 2345 is forwarded for remote attach scenarios
- The `substitutePath` in `devcontainer.json` maps the current container workspace
  folder to `${workspaceFolder}` so source paths resolve correctly in normal
  checkouts and git worktrees

If debugging stops working after a container rebuild, run:

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
```

---

## Native Windows (without Dev Container)

Step debugging works natively on Windows amd64. Install Delve:

```powershell
go install github.com/go-delve/delve/cmd/dlv@latest
```

The VS Code Go extension auto-detects and uses it. All launch.json configurations
work without changes — VS Code resolves paths correctly on Windows.

---

## Advanced: attach to a running server

For debugging a server that's already running (e.g. started by docker-compose):

```bash
# In the container, start Delve in headless mode instead of the binary directly:
dlv debug ./cmd/overcast \
  --headless \
  --listen=:2345 \
  --api-version=2 \
  --accept-multiclient \
  -- # (any normal env vars go before this)
```

Then in VS Code: F5 → "Debug: attach to running Delve (port 2345)".

This is useful when you need to debug startup code, or when you want to attach
to a process that was already handling requests before you set the breakpoint.

---

## Tips

- **Breakpoints in middleware** — set a breakpoint in `internal/middleware/logger.go`
  to see every request before it reaches a service handler.

- **Breakpoints in state layer** — set a breakpoint in `internal/state/memory.go`'s
  `Set` or `Get` to see every state read/write across all services.

- **Goroutines** — in the CALL STACK panel, you can switch between goroutines.
  Each request runs on its own goroutine. You can inspect them all while paused.

- **`-test.count=1`** in test configurations — prevents test caching so the
  test always runs fresh and your breakpoints are always hit.

- **Build flags** are empty in debug configurations — this preserves full debug
  info in the binary. The production build uses `-w -s` to strip debug info.
