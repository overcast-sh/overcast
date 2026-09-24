// Package athena emulates the Amazon Athena control plane.
//
// Implemented: workgroups (with the built-in primary workgroup), query
// executions, named queries, prepared statements, data catalogs (with the
// built-in AwsDataCatalog), the metadata operations that read the Glue Data
// Catalog, engine versions and tags.
//
// Queries run through a queryExecutor (executor.go). The one wired today runs
// nothing: a query succeeds as soon as it is started, with an empty result.
package athena

import (
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/services/glue"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "athena"

// Service implements router.Service and router.TargetDispatcher for Athena.
type Service struct {
	log      *serviceutil.ServiceLogger
	store    *athenaStore
	cfg      *config.Config
	clk      clock.Clock
	typedOp  map[string]op.Operation
	catalog  glue.Catalog // set by InitGlueCatalog; see glue_catalog.go
	executor queryExecutor

	// locks serialises each record's read-modify-write: a workgroup update
	// against a tag change, a query's state transition against a stop, and
	// a create against another create of the same name or token.
	locks serviceutil.RecordLocks
	// primaryMu serialises seeding the primary workgroup. It is separate from
	// locks so that a caller holding primary's record lock can still seed it.
	primaryMu sync.Mutex
}

// New returns a configured Athena Service.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	s := &Service{
		log:      log,
		store:    newAthenaStore(st, log),
		cfg:      cfg,
		clk:      clk,
		executor: inertExecutor{},
	}
	s.typedOp = s.typedOps()
	return s
}

func (s *Service) Name() string                { return serviceName }
func (s *Service) RegisterRoutes(_ chi.Router) {}
func (s *Service) TargetPrefix() string        { return "AmazonAthena." }

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
