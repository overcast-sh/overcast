+ [web] step debugging inside the console: breakpoints, stepping and a paused view in the function page's Code tab, for Node.js functions
  Locals, Watch, Call stack and Breakpoints beside the code, logs and a debug console with a REPL below; panels and drawer resize, and a session left open is restored on return
  source maps beside or inline in the deployment show the original TypeScript, translated both ways, with a per-function switch to debug the compiled output instead
  a session is one more attached client of the debug port, so the Debug tab reads `attached`/`paused` and the clock stops; a *Wait for a debugger* switch holds the first invocation
+ [lambda] a tagged function is registered with the debugger when created or tagged, and after a restart, so the console offers a session before it first runs
+ [lambda] `overcast:debug-wait=true` holds an invocation until a debugger attaches, so the invocation that starts the container pauses at its breakpoints too
  bounded by `OVERCAST_DEBUGGER_WAIT_TIMEOUT` (default 120s), after which it runs with a `WARN`; the function's clock starts when the event is dispatched
+ [lambda] a WebSocket bridge onto a function's debug port for the console, and the source endpoint reads every deployment file by path
  a hot-reloaded function's mounted directory is listed and read live, `.map` files and nested paths included, so the Code tab shows what the container runs
