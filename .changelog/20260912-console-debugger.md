+ [web] step debugging inside the console: breakpoints, stepping and a paused view in the function page's Code tab, for Node.js functions
  Locals, Watch, Call stack and Breakpoints beside the code; the function's logs and a debug console with a REPL below it
  source maps beside or inline in the deployment show the original TypeScript, with breakpoints and the call stack translated both ways
  a session is one more attached client of the function's debug port, so the Debug tab reads `attached`/`paused` and the timeout clock stops
+ [lambda] a tagged function is registered with the debugger when created or tagged, and after a restart, so the console offers a session before it first runs
+ [lambda] `overcast:debug-wait=true` holds an invocation until a debugger attaches, so the invocation that starts the container pauses at its breakpoints too
  bounded by `OVERCAST_DEBUGGER_WAIT_TIMEOUT` (default 120s), after which it runs with a `WARN`; the function's clock starts when the event is dispatched
+ [lambda] a WebSocket bridge onto a function's debug port for the console, and the source endpoint reads every deployment file by path
  a hot-reloaded function's mounted directory is listed and read live, `.map` files and nested paths included, so the Code tab shows what the container runs
