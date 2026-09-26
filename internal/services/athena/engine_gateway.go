package athena

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
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
//
// It serves an EngineAPI, not the root router: one handler per signing name,
// each reaching only its own service (see the router's engine_api.go).

// gatewayShutdownTimeout bounds closing the gateway at shutdown.
const gatewayShutdownTimeout = 5 * time.Second

// EngineAPI is the part of Overcast's API the engine calls, as one handler per
// SigV4 signing name its catalogs sign with. Each is served with the API's
// request middleware and reaches only its own service. A nil handler is a
// service that is not enabled, whose calls the gateway refuses.
type EngineAPI struct {
	Glue    http.Handler // Glue's JSON API, signed "glue"
	S3      http.Handler // S3, signed "s3"
	Iceberg http.Handler // S3 Tables' Iceberg REST catalog, signed "s3tables"
}

// forSigningName is the handler for requests signed for name, or nil.
func (a EngineAPI) forSigningName(name string) http.Handler {
	switch name {
	case "glue":
		return a.Glue
	case "s3":
		return a.S3
	case "s3tables":
		return a.Iceberg
	}
	return nil
}

type engineGateway struct {
	mu        sync.Mutex
	api       EngineAPI
	server    *http.Server
	endpoint  string
	accessKey string
}

// setAPI is the API the gateway serves.
func (g *engineGateway) setAPI(api EngineAPI) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.api = api
}

// open starts the gateway, once, and returns the origin the engine reaches
// Overcast on and the access key it must sign with.
func (g *engineGateway) open(ctx context.Context, dc *docker.Client, cfg *config.Config, log *zap.Logger) (endpoint, accessKey string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.endpoint != "" {
		return g.endpoint, g.accessKey, nil
	}
	if g.api.S3 == nil {
		// Every query writes its results to S3, so there is no engine without it.
		return "", "", errors.New("no S3 API to serve the engine")
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
	g.server = &http.Server{Handler: engineOnly(key, g.api), ReadHeaderTimeout: 30 * time.Second}
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

// engineOnly serves a request signed with accessKey to the API's handler for
// the service it is signed for, and refuses everything else.
//
// It also serves every request as addressed to "localhost". The engine
// addresses S3 path-style, but the Host it sends is whatever it dialled —
// host.docker.internal, say — which S3's virtual-host detection would read
// as a bucket label on an unknown domain.
func engineOnly(accessKey string, api EngineAPI) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := api.forSigningName(middleware.ServiceFromCredential(r))
		if h == nil || subtle.ConstantTimeCompare([]byte(middleware.CredentialAccessKey(r)), []byte(accessKey)) != 1 {
			http.Error(w, "the Athena engine gateway serves only the engine's Glue, S3 and S3 Tables Iceberg calls", http.StatusForbidden)
			return
		}
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
