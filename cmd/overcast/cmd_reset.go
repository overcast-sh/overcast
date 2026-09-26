package main

// cmd_reset.go — `overcast reset [service...]`. POSTs to the daemon's
// always-on reset endpoint (POST /_overcast/reset, or
// /_overcast/reset/{service} once per named service) — see
// internal/router/reset.go. Reset was moved out from
// under the OVERCAST_DEBUG gate on 2026-09-01: OVERCAST_DEBUG exists to gate
// expensive or leaky instrumentation (state dumps, request tracing, pprof),
// and reset is neither — it also grants no destructive power beyond what the
// unauthenticated AWS API surface already exposes (any caller can already
// delete every resource one at a time through ordinary AWS calls). It is
// still destructive to run by accident, though, so an interactive terminal
// is asked to confirm unless --yes is given; a non-interactive caller (CI,
// scripts, a pipe) proceeds without prompting.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/overcast-sh/overcast/internal/config"
)

// resetHTTPTimeout bounds the reset request itself. Longer than the 2s used
// by the plain-GET commands (status, services): a reset can have real work
// to do — sweeping every namespace of a large SQLite-backed store — so a
// short timeout would fail a reset that was actually still in progress.
const resetHTTPTimeout = 30 * time.Second

// resetCompletionTimeout bounds the /_overcast/health probe used for
// [service] completion. Short and deliberately so: a completion function
// runs on every tab-press, and must never make the prompt feel like it hung
// — see completeResetServiceArgs.
const resetCompletionTimeout = 1 * time.Second

// resetStdinIsTerminal reports whether the real process stdin is an
// interactive terminal. A package-level seam (mirroring the withTLSSeams
// pattern in tls_settings_test.go) so tests can simulate both an
// interactive and a non-interactive (CI, piped) stdin without needing an
// actual terminal.
var resetStdinIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

func newResetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reset [service...]",
		Short: "Wipe emulated state",
		Long: "Wipe all emulated state, or the state of the services named.\n\n" +
			"This is destructive. In an interactive terminal you are asked to\n" +
			"confirm what will be wiped unless --yes is given; a non-interactive\n" +
			"caller (CI, scripts, a pipe) proceeds without prompting.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeResetServiceArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint, _ := cmd.Flags().GetString("endpoint")
			yes, _ := cmd.Flags().GetBool("yes")
			services, err := resetServices(args)
			if err != nil {
				return err
			}

			if !yes && resetStdinIsTerminal() {
				confirmed, err := confirmReset(cmd, endpoint, services)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}

			if len(services) == 0 {
				return runReset(cmd, endpoint, "")
			}
			for _, service := range services {
				if err := runReset(cmd, endpoint, service); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// confirmReset prints what will be wiped and reads a y/N answer from stdin,
// reporting whether the caller confirmed. It reads via cmd.InOrStdin() (not
// os.Stdin directly) so tests can supply canned input.
func confirmReset(cmd *cobra.Command, endpoint string, services []string) (bool, error) {
	if len(services) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "This will wipe all %s state at %s.\n", listSentence(services), endpoint)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "This will wipe all emulated state at %s.\n", endpoint)
	}
	fmt.Fprint(cmd.OutOrStdout(), "Continue? [y/N] ")

	scanner := bufio.NewScanner(cmd.InOrStdin())
	if !scanner.Scan() {
		// EOF (e.g. stdin closed mid-prompt) reads the same as a bare Enter:
		// the default answer, "no".
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}

// resetServices is the services named, each once, in the order given. Every
// name is checked before anything is wiped, so a typo in the third name
// cannot leave the first two reset and the rest not.
func resetServices(args []string) ([]string, error) {
	var services, unknown []string
	for _, name := range args {
		switch {
		case slices.Contains(services, name):
		case slices.Contains(config.AllServices(), name):
			services = append(services, name)
		default:
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown service: %s", strings.Join(unknown, ", "))
	}
	return services, nil
}

// listSentence joins items as prose: "a", "a and b", "a, b and c".
func listSentence(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// resetResponse is the JSON body POST /_overcast/reset[/{service}] returns on
// success. Field names mirror the map[string]string the router hand-builds
// in internal/router/reset.go — kept in sync by hand since the CLI must not
// import that package.
type resetResponse struct {
	Status  string `json:"status"`
	Service string `json:"service,omitempty"`
}

// resetErrorResponse is the JSON body POST /_overcast/reset/{service} returns
// for an unrecognized service (400).
type resetErrorResponse struct {
	Error string `json:"error"`
}

// runReset issues the actual POST and reports the outcome. endpoint and
// service are exactly what the caller already confirmed (or skipped
// confirming via --yes / a non-interactive stdin).
func runReset(cmd *cobra.Command, endpoint, service string) error {
	url := strings.TrimRight(endpoint, "/") + "/_overcast/reset"
	if service != "" {
		url += "/" + service
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), resetHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("overcast unreachable at %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusBadRequest {
			var errBody resetErrorResponse
			// No "overcast:" prefix — main.go's error path already adds one.
			if err := json.NewDecoder(resp.Body).Decode(&errBody); err == nil && errBody.Error != "" {
				return fmt.Errorf("%s", errBody.Error)
			}
		}
		return fmt.Errorf("overcast returned %s", resp.Status)
	}

	var result resetResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// The reset itself already succeeded (200) — a body this build
		// cannot parse (older daemon, proxy) is not worth failing over.
		fmt.Fprintf(cmd.OutOrStdout(), "overcast reset OK at %s\n", endpoint)
		return nil
	}
	if result.Service != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "overcast reset %s state OK at %s\n", result.Service, endpoint)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "overcast reset all state OK at %s\n", endpoint)
	}
	return nil
}

// completeResetServiceArgs completes the [service] argument from the
// daemon's own enabled-services list (GET /_overcast/health), the same
// source `overcast services` reads. Mirrors completeAWSArgs in cmd_aws.go:
// a completion function must never error or hang the prompt, so any failure
// — daemon unreachable, bad response, timeout — falls back to no
// candidates rather than surfacing an error.
func completeResetServiceArgs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	endpoint, _ := cmd.Flags().GetString("endpoint")
	services, err := fetchResetCompletionServices(cmd.Context(), endpoint)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// A service already named is not offered again.
	services = slices.DeleteFunc(services, func(s string) bool { return slices.Contains(args, s) })
	return services, cobra.ShellCompDirectiveNoFileComp
}

// fetchResetCompletionServices fetches and decodes GET
// {endpoint}/_overcast/health, bounded to resetCompletionTimeout, returning
// just the enabled-service names.
func fetchResetCompletionServices(ctx context.Context, endpoint string) ([]string, error) {
	url := strings.TrimRight(endpoint, "/") + "/_overcast/health"

	reqCtx, cancel := context.WithTimeout(ctx, resetCompletionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overcast returned %s", resp.Status)
	}

	var health struct {
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, err
	}
	return health.Services, nil
}
