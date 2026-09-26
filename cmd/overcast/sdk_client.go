package main

// sdk_client.go — how the commands that call the AWS API in process (`athena
// query`, `samples load`) reach the daemon: the endpoint and region `overcast
// aws` would use, and for an https endpoint the daemon's own CA.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"

	"github.com/overcast-sh/overcast/internal/athenaquery"
	"github.com/overcast-sh/overcast/internal/hostbridge/trust"
	"github.com/overcast-sh/overcast/internal/sdkconfig"
	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
)

// daemonClient is an SDK configuration and a plain HTTP client for one daemon.
type daemonClient struct {
	endpoint string
	http     *http.Client
	aws      aws.Config
}

// newDaemonClient resolves the daemon the way `overcast aws` does: --endpoint,
// else OVERCAST_ENDPOINT or OVERCAST_PORT, and OVERCAST_REGION or us-east-1.
func newDaemonClient(cmd *cobra.Command) *daemonClient {
	endpoint := resolveAWSEndpoint(cmd, "", false)
	client := httpClientTrusting(cmd.ErrOrStderr(), resolveCABundle(cmd, endpoint))
	return &daemonClient{endpoint: endpoint, http: client, aws: sdkconfig.ForEndpoint(endpoint, resolveAWSRegion(), client)}
}

// engineStatus reads the Athena engine's status from the daemon.
func (c *daemonClient) engineStatus(ctx context.Context) (athenasvc.EngineStatus, error) {
	return athenaquery.FetchEngineStatus(ctx, c.http, c.endpoint)
}

// httpClientTrusting is a client that also trusts the CA in the PEM file at
// caPath, or the default client when there is none; a CA it cannot read is
// reported on stderr, since TLS verification is then about to fail.
func httpClientTrusting(stderr io.Writer, caPath string) *http.Client {
	if caPath == "" {
		return http.DefaultClient
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		fmt.Fprintf(stderr, "overcast: could not read the overcast CA at %s: %v\n", caPath, err)
		return http.DefaultClient
	}
	return &http.Client{Transport: trust.TransportTrusting(pem)}
}
