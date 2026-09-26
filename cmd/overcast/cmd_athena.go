package main

// cmd_athena.go — `overcast athena query "<sql>"`. Runs one query through
// the Athena API, waits for it, and prints its rows. The first query after
// the daemon starts waits for the query engine too, so its start-up
// progress goes to stderr, and a query that FAILED or was CANCELLED exits
// non-zero — which makes this the quickest smoke test of a data stack.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/spf13/cobra"

	"github.com/overcast-sh/overcast/internal/athenaquery"
	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
)

func newAthenaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "athena",
		Short: "Run Athena queries against overcast",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newAthenaQueryCmd())
	return cmd
}

func newAthenaQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query <sql>",
		Short: "Run an Athena query, wait for it, and print the result",
		Long: "Run an Athena query, wait for it, and print the result.\n\n" +
			"Pass - as the SQL to read it from stdin. The daemon is the one\n" +
			"`overcast aws` uses: --endpoint, else OVERCAST_ENDPOINT or\n" +
			"OVERCAST_PORT, in OVERCAST_REGION (default us-east-1).\n\n" +
			"While the query engine starts, its progress is printed to stderr.\n" +
			"A query that fails or is cancelled exits with status 1.",
		Args: cobra.ExactArgs(1),
		RunE: runAthenaQuery,
	}
	cmd.Flags().String("database", "", "database the query runs in")
	cmd.Flags().String("workgroup", "", "workgroup to run in (default primary)")
	cmd.Flags().String("catalog", "", "data catalog (default AwsDataCatalog)")
	cmd.Flags().String("output-location", "", "s3:// location for the results, when the workgroup has none")
	cmd.Flags().StringP("output", "o", string(formatTable), "output format: table, json or csv")
	return cmd
}

func runAthenaQuery(cmd *cobra.Command, args []string) error {
	output, _ := cmd.Flags().GetString("output")
	format, err := parseResultFormat(output)
	if err != nil {
		return err
	}
	sql, err := querySQL(cmd.InOrStdin(), args[0])
	if err != nil {
		return err
	}
	req := athenaquery.Request{SQL: sql}
	req.Database, _ = cmd.Flags().GetString("database")
	req.WorkGroup, _ = cmd.Flags().GetString("workgroup")
	req.Catalog, _ = cmd.Flags().GetString("catalog")
	req.OutputLocation, _ = cmd.Flags().GetString("output-location")

	daemon := newDaemonClient(cmd)
	progress := &engineProgress{out: cmd.ErrOrStderr(), status: daemon.engineStatus}
	res, err := athenaquery.Run(cmd.Context(), athena.NewFromConfig(daemon.aws), req, athenaquery.Options{OnPoll: progress.poll})
	if err != nil {
		return fmt.Errorf("athena at %s: %w", daemon.endpoint, err)
	}
	if !res.Succeeded() {
		return res.Failure()
	}
	return writeResult(cmd.OutOrStdout(), format, res)
}

// querySQL is the SQL argument, or stdin when it is "-".
func querySQL(stdin io.Reader, arg string) (string, error) {
	if arg != "-" {
		return arg, nil
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read SQL from stdin: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// engineProgress reports the query engine's start-up while a query waits
// for it.
type engineProgress struct {
	out    io.Writer
	status func(context.Context) (athenasvc.EngineStatus, error)
	last   athenasvc.EngineState
	// starting is set once a start-up step has been reported, so the engine
	// being ready is reported too.
	starting bool
}

// poll runs on each poll that finds the query still running. A queued query
// may be waiting for the engine; a running one is waited on only to report
// that the engine it waited for is ready.
func (p *engineProgress) poll(ctx context.Context, qe types.QueryExecution) {
	if athenaquery.StateOf(qe) != types.QueryExecutionStateQueued && !p.starting {
		return
	}
	st, err := p.status(ctx)
	if err != nil || st.State == p.last {
		return
	}
	p.last = st.State
	switch st.State {
	case athenasvc.EnginePulling:
		p.starting = true
		fmt.Fprintf(p.out, "athena: starting the query engine: pulling %s\n", imageName(st.Image))
	case athenasvc.EngineStarting:
		p.starting = true
		fmt.Fprintln(p.out, "athena: starting the query engine: waiting for it to answer")
	case athenasvc.EngineReady:
		if p.starting {
			fmt.Fprintf(p.out, "athena: query engine ready (pull %s, start %s)\n", millis(st.PullMillis), millis(st.StartMillis))
			p.starting = false
		}
	}
}

// millis is ms as a duration to a tenth of a second.
func millis(ms int64) time.Duration {
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond)
}
