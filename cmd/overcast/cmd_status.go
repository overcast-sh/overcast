package main

// cmd_status.go — `overcast status`. Pings overcast's /_overcast/health
// endpoint and reports whether the daemon is reachable. Deliberately minimal;
// the goal is a human-friendly one-liner, not a dashboard. Two things follow
// it: when Athena is enabled, a line for its query engine, whose state is
// what explains a slow first query; and, when this
// CLI's own instance registry (instances.go) is non-empty, a short table of
// every instance it knows about (not just the one at --endpoint), since
// `overcast start`/`stop` can manage several under different names.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check that overcast is reachable",
		RunE: func(cmd *cobra.Command, _ []string) error {
			endpoint, _ := cmd.Flags().GetString("endpoint")
			url := strings.TrimRight(endpoint, "/") + "/_overcast/health"

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("overcast unreachable at %s: %w", endpoint, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("overcast returned %s", resp.Status)
			}

			// Enrich the line from fields the health endpoint already carries,
			// tolerating a body this build cannot parse (older daemon, proxy).
			var health struct {
				Status  string `json:"status"`
				Version string `json:"version"`
				Storage struct {
					Default string `json:"default"`
				} `json:"storage"`
				AthenaEngine *athenasvc.EngineStatus `json:"athenaEngine"`
			}
			statusWord := "OK"
			var details []string
			if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err == nil {
				if health.Status != "" && health.Status != "ok" {
					statusWord = health.Status
				}
				if health.Version != "" {
					details = append(details, "version "+health.Version)
				}
				if health.Storage.Default != "" {
					details = append(details, "storage "+health.Storage.Default)
				}
			}
			line := fmt.Sprintf("overcast %s at %s", statusWord, endpoint)
			if len(details) > 0 {
				line += " (" + strings.Join(details, ", ") + ")"
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
			if health.AthenaEngine != nil {
				fmt.Fprintln(cmd.OutOrStdout(), athenaEngineLine(*health.AthenaEngine))
			}

			printInstanceTable(cmd.OutOrStdout())
			return nil
		},
	}
}

// printInstanceTable appends a NAME/BACKEND/ENDPOINT/STATE table for every
// instance this CLI's registry (instances.go) knows about, regardless of
// whether it matches the --endpoint just probed above — `overcast
// start`/`stop` can manage several named instances at once, and this is the
// only place a caller sees them all in one shot. Prints nothing when the
// registry is empty (nothing has ever been started) or unreadable, so
// `overcast status` on a machine that has never run `overcast start` keeps
// its exact original one-line output.
func printInstanceTable(out io.Writer) {
	recs, err := listInstances()
	if err != nil || len(recs) == 0 {
		return
	}
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tBACKEND\tENDPOINT\tSTATE")
	for _, rec := range recs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", rec.Name, rec.Backend, rec.Endpoint, instanceLifecycleState(rec))
	}
	_ = tw.Flush()
}
