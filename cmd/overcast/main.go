// Command overcast is the unified CLI for the Overcast AWS emulator.
//
// When running natively (installed via brew, scoop, or `go install`), all
// subcommands are available:
//
//   - overcast serve        — start the emulator daemon
//   - overcast bridge       — publish *.local domains via mDNS + port-80 proxy
//   - overcast trust        — manage the local trust store for TLS certificates
//   - overcast https        — one-shot HTTPS setup (CA + trust store + certificate)
//   - overcast start        — run a background daemon instance (native or --docker)
//   - overcast stop         — stop a background instance
//   - overcast restart      — restart a background instance with its saved options
//   - overcast logs         — tail a background instance's output
//   - overcast status       — check a running daemon is reachable
//   - overcast wait         — block until a daemon reports healthy
//   - overcast services     — list enabled services and emulation tiers
//   - overcast network      — inspect and rebuild the Docker networks Overcast manages
//   - overcast reset        — wipe emulated state, all or one service
//   - overcast config       — show the daemon's effective configuration
//   - overcast env          — print AWS environment exports for the daemon
//   - overcast aws          — run the host AWS CLI against the daemon
//   - overcast athena       — run an Athena query and print its result
//   - overcast samples      — load a sample dataset
//
// The workspace MCP server (repo-aware tools for agents/editors) is
// dev-only tooling and does not live here — see cmd/overcast-mcp.
//
// The Docker image uses `overcast serve` as its entrypoint. Host-only
// commands (bridge, trust) require host-network access and are not useful
// inside a container.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overwritten at build time via -ldflags.
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "overcast",
		Short:         "AWS service emulator — daemon and host-side tooling",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("endpoint", "http://localhost:4566", "overcast daemon base URL")

	root.AddCommand(newServeCmd())
	root.AddCommand(newBridgeCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newTrustCmd())
	root.AddCommand(newHTTPSCmd())
	root.AddCommand(newImportCmd())
	root.AddCommand(newEnvCmd())
	root.AddCommand(newAWSCmd())
	root.AddCommand(newWaitCmd())
	root.AddCommand(newServicesCmd())
	root.AddCommand(newStartCmd())
	root.AddCommand(newStopCmd())
	root.AddCommand(newRestartCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newNetworkCmd())
	root.AddCommand(newResetCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newAthenaCmd())
	root.AddCommand(newSamplesCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "overcast:", err)
		os.Exit(1)
	}
}
