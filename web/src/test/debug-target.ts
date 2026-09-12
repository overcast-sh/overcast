import type { DebuggerTarget } from "@/types"

/**
 * A registered, enabled inspector target for a Lambda function — the shape
 * from docs/plans/compute-debugger.md § 6 — for every test that renders one:
 * the Debug panel, the function list's badge, the Test tab's hint. One
 * fixture, so a field added to the Go descriptor is added here once.
 */
export function debugTarget(overrides: Partial<DebuggerTarget> = {}): DebuggerTarget {
  return {
    id: "lambda/my-fn",
    service: "lambda",
    resource: "my-fn",
    container: "",
    enabled: true,
    reason: "",
    protocol: "inspector",
    protocolSource: "runtime",
    listen: { host: "127.0.0.1", port: 9229 },
    state: "listening",
    attachedSince: "",
    pausedSince: "",
    containerId: "0a1b2c3d4e5f6a7b8c9d",
    upstream: "127.0.0.1:55012",
    remoteRoot: "/var/task",
    localRoot: "",
    timeoutPolicy: "attached",
    waitForDebugger: false,
    consoleDebug: false,
    bridgePath: "",
    setup: {
      flag: "OVERCAST_DEBUGGER=true",
      tagCli:
        "aws lambda tag-resource --resource arn:aws:lambda:us-east-1:000000000000:function:my-fn --tags overcast:debug=true",
      tagCdk: 'cdk.Tags.of(fn).add("overcast:debug", "true")',
    },
    editors: [
      {
        id: "vscode",
        label: "VS Code",
        kind: "json",
        body: '{\n  "type": "node",\n  "port": 9229\n}',
        verified: true,
      },
      {
        id: "jetbrains",
        label: "JetBrains",
        kind: "steps",
        body: "1. Run → Edit Configurations…\n2. Host: 127.0.0.1   Port: 9229",
        verified: false,
      },
      {
        id: "cli",
        label: "Command line",
        kind: "shell",
        body: "node inspect 127.0.0.1:9229",
        verified: true,
      },
    ],
    ...overrides,
  }
}
