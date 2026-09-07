+ [lambda/ecs] step debugging of user code inside emulated Lambda functions and ECS tasks: `OVERCAST_DEBUGGER=true` plus an `overcast:debug=true` tag
  Node.js and Java get the inspector or JDWP flag injected; Python reads `OVERCAST_DEBUG_PORT` for `debugpy`; images and custom runtimes name a protocol with `overcast:debug-protocol`
  the function timeout clock stops while a debugger is attached (`OVERCAST_DEBUGGER_TIMEOUT=attached|paused|strict`), and the port survives hot reload replacing the container
  the listener follows `OVERCAST_LISTEN`, so `-p 9229-9329:9229-9329` reaches it when Overcast runs in Docker
+ [web] a Debug tab on the function and task pages with a ready-made attach configuration for VS Code, JetBrains, Chrome DevTools and the command line
