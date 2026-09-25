// Package athena emulates Amazon Athena.
//
// Implemented: workgroups (with the built-in primary workgroup), query
// executions, named queries, prepared statements, data catalogs (with the
// built-in AwsDataCatalog), the metadata operations that read the Glue Data
// Catalog, engine versions and tags.
//
// Queries run through a queryExecutor (executor.go). Athena's Hive DDL runs
// against the Glue Data Catalog in-process; every other statement runs on a
// Trino container started on the first query (engine_manager.go), or, with
// ATHENA_ENGINE=inert or no Docker, succeeds at once with an empty result.
package athena

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/services/glue"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	serviceName = "athena"
	// nsInstance holds the identity the engine container's labels carry.
	nsInstance = "athena:instance"
)

// Service implements router.Service and router.TargetDispatcher for Athena.
type Service struct {
	log     *serviceutil.ServiceLogger
	store   *athenaStore
	cfg     *config.Config
	clk     clock.Clock
	typedOp map[string]op.Operation

	// Wired after construction; see glue_catalog.go and InitS3Access.
	catalog       glue.Catalog
	catalogWriter glue.CatalogWriter
	listObjects   events.S3ListObjectsFunc
	// bus receives each query's state changes; nil until InitBus.
	bus *events.Bus

	executor   queryExecutor
	statements *statementExecutor
	// engine runs queries on Trino; nil with ATHENA_ENGINE=inert.
	engine *engineManager
	// reaped fails, once, the queries a previous process left running.
	reaped serviceutil.LazyInit

	// locks serialises each record's read-modify-write: a workgroup update
	// against a tag change, a query's state transition against a stop, and
	// a create against another create of the same name or token.
	locks serviceutil.RecordLocks
	// primaryMu serialises seeding the primary workgroup. It is separate from
	// locks so that a caller holding primary's record lock can still seed it.
	primaryMu sync.Mutex
	// contentsMu keeps a workgroup's contents still while it is deleted:
	// named-query and prepared-statement writes hold it shared, and
	// DeleteWorkGroup exclusively. It is always taken before a record lock.
	contentsMu sync.RWMutex
}

// New returns a configured Athena Service.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	s := &Service{
		log:   log,
		store: newAthenaStore(st, log),
		cfg:   cfg,
		clk:   clk,
	}
	if cfg.AthenaEngine != config.AthenaEngineInert {
		instances := serviceutil.NewAnchoredInstanceDomain(st, nsInstance, serviceutil.DataDirAnchor(cfg.DataDir))
		s.engine = newEngineManager(cfg, log, clk, instances)
	}
	s.statements = &statementExecutor{route: s.runnerFor, results: resultStore{s.store}, maxResultBytes: cfg.AthenaMaxResultBytes, clk: clk, log: log}
	s.executor = s.statements
	s.typedOp = s.typedOps()
	return s
}

// InitS3Access wires the in-process S3 accessor: query results are streamed
// through put, and MSCK REPAIR TABLE lists a table's partitions through list.
func (s *Service) InitS3Access(put events.S3PutObjectStreamFunc, list events.S3ListObjectsFunc) {
	s.statements.output = &resultWriter{put: put}
	s.listObjects = list
}

// InitRouter hands the service the router the engine reaches Overcast's API
// through; see engine_gateway.go.
func (s *Service) InitRouter(h http.Handler) {
	if s.engine != nil {
		s.engine.gateway.setHandler(h)
	}
}

// SetDocker wires the daemon the query engine runs on. Until it is called,
// or DockerUnavailable is, a query waits QUEUED.
func (s *Service) SetDocker(dc *docker.Client) {
	if s.engine != nil {
		s.engine.setDocker(dc)
	}
}

// DockerUnavailable records that no daemon answered the probe: queries run
// inert for the life of the process.
func (s *Service) DockerUnavailable() {
	if s.engine != nil {
		s.engine.dockerUnavailable()
	}
}

// Stop fails the queries still running and removes the engine container.
func (s *Service) Stop(ctx context.Context) {
	s.statements.stop(ctx)
	if s.engine != nil {
		s.engine.stop(ctx)
	}
}

func (s *Service) Name() string { return serviceName }

// RegisterRoutes serves the emulator-only engine status endpoint.
func (s *Service) RegisterRoutes(r chi.Router) { r.Get(engineStatusPath, s.serveEngineStatus) }

func (s *Service) TargetPrefix() string { return "AmazonAthena." }

// Dispatch serves every operation through its typed implementation, in the
// wire protocol the request arrived in: AWS JSON 1.1 (Athena's own), JSON 1.0
// or RPC v2 CBOR. A request dispatched without an identified codec is the
// legacy X-Amz-Target path, which is JSON 1.1.
func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	c, opName := codec.FromContext(r.Context())
	if c == nil || opName == "" {
		c = codec.JSON11
		opName = r.Header.Get("X-Amz-Target")
		if idx := strings.LastIndex(opName, "."); idx >= 0 {
			opName = opName[idx+1:]
		}
	}
	if !codec.Supports(s.SupportedProtocols(), c) {
		w.Header().Set("x-emulator-unsupported-protocol", c.Name())
		c.WriteError(w, r, &protocol.AWSError{
			Code: "UnsupportedProtocol", Message: "Athena does not support wire protocol " + c.Name() + ".",
			HTTPStatus: http.StatusUnsupportedMediaType,
		})
		return
	}
	if typed, ok := s.typedOp[opName]; ok {
		typed.Invoke(w, r, c)
		return
	}
	c.WriteError(w, r, protocol.ErrNotImplemented)
}
