package athena

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/services/s3tables"
)

// engine_gateway.go — where the engine reaches Overcast's API.
//
// The engine calls Glue and S3 for every query, so it has to reach Overcast,
// and Overcast's API is often somewhere a container cannot: natively it binds
// loopback only, and under OVERCAST_TLS it speaks only TLS, which the engine's
// JVM would refuse for want of Overcast's CA. So the engine gets a listener of
// its own, as the Lambda Runtime API does (docs/dev/container-networking.md
// §1a): bound where containerendpoint.ResolveListen has proved a container on
// the control plane can connect, and plain HTTP.
//
// That address may be reachable from more than the engine, so the gateway
// serves only what the engine calls — Glue's JSON API, S3 and S3 Tables'
// Iceberg REST catalog — and only to requests signed with an access key
// minted for this process, which only the engine's catalogs carry.

// gatewayShutdownTimeout bounds closing the gateway at shutdown.
const gatewayShutdownTimeout = 5 * time.Second

// glueTargetPrefix is the X-Amz-Target prefix of Glue's JSON operations.
const glueTargetPrefix = "AWSGlue."

type engineGateway struct {
	mu        sync.Mutex
	handler   http.Handler
	server    *http.Server
	endpoint  string
	accessKey string
}

// setHandler is the router the gateway serves.
func (g *engineGateway) setHandler(h http.Handler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.handler = h
}

// open starts the gateway, once, and returns the origin the engine reaches
// Overcast on and the access key it must sign with.
func (g *engineGateway) open(ctx context.Context, dc *docker.Client, cfg *config.Config, log *zap.Logger) (endpoint, accessKey string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.endpoint != "" {
		return g.endpoint, g.accessKey, nil
	}
	if g.handler == nil {
		return "", "", errors.New("no API handler to serve the engine")
	}
	network := dataplane.Primary(cfg)
	listen := containerendpoint.ResolveListen(ctx, dc, containerendpoint.ListenOptions{
		Network: network, HintPath: containerendpoint.HintPath(cfg.DataDir, network), Logger: log,
	})
	if listen.Unreachable {
		return "", "", fmt.Errorf("no container on this Docker daemon can connect back to this machine (tried %s)",
			strings.Join(containerendpoint.AttemptStrings(listen.Attempts), "; "))
	}
	if listen.Wildcard {
		log.Warn("athena engine gateway binds every interface: no narrower address was proved reachable from a container")
	}
	key, err := mintAccessKey()
	if err != nil {
		return "", "", err
	}
	lns, err := containerendpoint.ListenOn(listen.BindHosts, 0, log)
	if err != nil {
		return "", "", err
	}
	g.accessKey = key
	g.server = &http.Server{Handler: engineOnly(key, g.handler), ReadHeaderTimeout: 30 * time.Second}
	for _, ln := range lns {
		go g.serve(ln, log)
	}
	port := strconv.Itoa(lns[0].Addr().(*net.TCPAddr).Port)
	g.endpoint = "http://" + net.JoinHostPort(listen.ContainerHost, port)
	log.Info("athena engine gateway listening", zap.String("endpoint", g.endpoint), zap.Strings("bind", listen.BindHosts))
	return g.endpoint, g.accessKey, nil
}

// mintAccessKey is a random access key for the engine to sign with.
func mintAccessKey() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "OVERCASTATHENA" + strings.ToUpper(hex.EncodeToString(b)), nil
}

// engineOnly serves the engine's calls and refuses everything else: a
// request not signed with accessKey, and any that engineCall does not name.
//
// It also serves every request as addressed to "localhost". The engine
// addresses S3 path-style, but the Host it sends is whatever it dialled —
// host.docker.internal, say — which S3's virtual-host detection would read
// as a bucket label on an unknown domain.
func engineOnly(accessKey string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if middleware.CredentialAccessKey(r) != accessKey || !engineCall(r) {
			http.Error(w, "the Athena engine gateway serves only the engine's Glue, S3 and S3 Tables Iceberg calls", http.StatusForbidden)
			return
		}
		r.Host = "localhost"
		h.ServeHTTP(w, r)
	})
}

// engineCall reports whether r is a call the engine makes, by the service
// it is signed for: Glue's JSON operations; S3, on a path no other service
// is routed by; and S3 Tables' Iceberg REST catalog, but nothing else of S3
// Tables' API.
func engineCall(r *http.Request) bool {
	switch middleware.ServiceFromCredential(r) {
	case "glue":
		return strings.HasPrefix(r.Header.Get("X-Amz-Target"), glueTargetPrefix)
	case "s3":
		return middleware.ServiceOf(r) == "s3"
	case "s3tables":
		return r.Header.Get("X-Amz-Target") == "" && strings.HasPrefix(r.URL.Path, s3tables.IcebergRoot+"/")
	}
	return false
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
