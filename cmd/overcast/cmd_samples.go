package main

// cmd_samples.go — `overcast samples load <dataset>`. Loads a sample dataset
// through the AWS API with the same loader the console's Load sample dataset
// action runs (internal/samples), printing each step to stderr and what it
// left in place to stdout.

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/overcast-sh/overcast/internal/samples"
)

func newSamplesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "samples",
		Short: "Load sample datasets into overcast",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newSamplesLoadCmd())
	return cmd
}

func newSamplesLoadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "load <dataset>",
		Short: "Load a sample dataset: files in S3, Glue tables over them, and an Iceberg copy",
		Long: "Load a sample dataset: files in S3, Glue tables over them and, when\n" +
			"the Athena engine is running, an Iceberg copy made with CREATE TABLE AS.\n\n" +
			"The data is the same on every load, and loading again changes nothing.\n" +
			"`overcast reset athena glue s3` removes it, with everything else in\n" +
			"those three services.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: samples.Names(),
		RunE: func(cmd *cobra.Command, args []string) error {
			daemon := newDaemonClient(cmd)
			loader := samples.NewLoader(daemon.aws, daemon.engineStatus)
			loader.Progress = func(step string) { fmt.Fprintln(cmd.ErrOrStderr(), "samples: "+step) }
			report, err := loader.Load(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("load %s at %s: %w", args[0], daemon.endpoint, err)
			}
			return printSamplesReport(cmd.OutOrStdout(), report)
		},
	}
}

func printSamplesReport(out io.Writer, r *samples.Report) error {
	fmt.Fprintf(out, "Loaded %s: bucket s3://%s, database %s\n\n", r.Dataset, r.Bucket, r.Database)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TABLE\tFORMAT\tROWS\tLOCATION")
	for _, t := range r.Tables {
		fmt.Fprintf(tw, "%s.%s\t%s\t%d\t%s\n", r.Database, t.Name, t.Format, t.Rows, t.Location)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if r.IcebergSkipped != "" {
		fmt.Fprintf(out, "\nNo Iceberg copy: %s\n", r.IcebergSkipped)
	}
	return nil
}
