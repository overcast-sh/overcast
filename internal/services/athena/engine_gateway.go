package athena

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
)

// engine_gateway.go — where the engine reaches Overcast's API.
//
// The engine calls Glue and S3 for every query, so it has to reach Overcast,
// and Overcast's API is often somewhere a container cannot: natively it binds
// loopback only, and under OVERCAST_TLS it speaks only TLS, which the engine's
// JVM would refuse for want of Overcast's CA. So the engine gets a listener of
// its own, as the Lambda Runtime API does (docs/dev/container-networking.md
// §1a): bound where containerendpoint.ResolveListen has proved a container on
// the control plane can connect, plain HTTP, serving the same router.

// gatewayShutdownTimeout bounds closing the gateway at shutdown.
const gatewayShutdownTimeout = 5 * time.Second

type engineGateway struct {
	mu       sync.Mutex
	handler  http.Handler
	server   *http.Server
	endpoint string
}

// setHandler is the router the gateway serves.
func (g *engineGateway) setHandler(h http.Handler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.handler = h
}

// open starts the gateway, once, and returns the origin the engine reaches
// Overcast on.
func (g *engineGateway) open(ctx context.Context, dc *docker.Client, cfg *config.Config, log *zap.Logger) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.endpoint != "" {
		return g.endpoint, nil
	}
	if g.handler == nil {
		return "", errors.New("no API handler to serve the engine")
	}
	network := dataplane.Primary(cfg)
	listen := containerendpoint.ResolveListen(ctx, dc, containerendpoint.ListenOptions{
		Network: network, HintPath: containerendpoint.HintPath(cfg.DataDir, network), Logger: log,
	})
	if listen.Unreachable {
		return "", fmt.Errorf("no container on this Docker daemon can connect back to this machine (tried %s)",
			strings.Join(containerendpoint.AttemptStrings(listen.Attempts), "; "))
	}
	lns, err := containerendpoint.ListenOn(listen.BindHosts, 0, log)
	if err != nil {
		return "", err
	}
	g.server = &http.Server{Handler: pathStyleHost(g.handler), ReadHeaderTimeout: 30 * time.Second}
	for _, ln := range lns {
		go g.serve(ln, log)
	}
	port := strconv.Itoa(lns[0].Addr().(*net.TCPAddr).Port)
	g.endpoint = "http://" + net.JoinHostPort(listen.ContainerHost, port)
	log.Info("athena engine gateway listening", zap.String("endpoint", g.endpoint), zap.Strings("bind", listen.BindHosts))
	return g.endpoint, nil
}

// pathStyleHost serves every request as addressed to "localhost". The engine
// addresses S3 path-style, but the Host it sends is whatever it dialled —
// host.docker.internal, say — which S3's virtual-host detection would read
// as a bucket label on an unknown domain.
func pathStyleHost(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "localhost"
		h.ServeHTTP(w, r)
	})
}

func (g *engineGateway) serve(ln net.Listener, log *zap.Logger) {
	if err := g.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("athena engine gateway stopped", zap.String("listen", ln.Addr().String()), zap.Error(err))
	}
}

// close stops the gateway.
func (g *engineGateway) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayShutdownTimeout)
	defer cancel()
	_ = g.server.Shutdown(ctx)
	g.server, g.endpoint = nil, ""
}
