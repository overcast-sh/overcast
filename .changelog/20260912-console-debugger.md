+ [web] step debugging inside the console: breakpoints, stepping and a paused view in the function page's Code tab, for Node.js functions
  Locals, Watch, Call stack and Breakpoints beside the code; the request's logs and a debug console with a REPL below it
  source maps beside or inline in the deployment show the original TypeScript, with breakpoints and the call stack translated both ways
  a session is one more attached client of the function's debug port, so the Debug tab reads `attached`/`paused` for it and the timeout clock stops
  the Code tab starts a session and takes breakpoints before one exists; breakpoints move to the statement the runtime placed them on and show whether they have bound; the stepping keys work from every debug panel and F5 never reloads a page with a session open; a step into the runtime's own code steps straight back out
+ [lambda] a tagged function is registered with the debugger when its page opens, so the Debug tab offers the console and reads `unbound` before the first invocation instead of "not tagged"
+ [lambda] a WebSocket bridge onto a function's debug port for the console, and the source endpoint reads every file of a deployment by path
  a hot-reloaded function's mounted directory is listed and read live, `.map` files and nested paths included, so the Code tab shows what the container runs
