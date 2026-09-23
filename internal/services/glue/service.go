// Package glue emulates the AWS Glue Data Catalog.
//
// Implemented: databases (Create, Get, GetDatabases, Update, Delete), tables
// (Create, Get, GetTables, Update, Delete, BatchDelete), table versions (Get,
// GetTableVersions, Delete, BatchDelete), partitions (Create, BatchCreate,
// Get, GetPartitions with an Expression filter, BatchGet, Update, Delete,
// BatchDelete) and tags. Definitions are kept whole — StorageDescriptor,
// Parameters, PartitionKeys and the rest — because query engines (Trino's
// Glue metastore, the Iceberg Glue catalogs) read them back.
//
// Catalog is the read-only view other services use.
package glue

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "glue"

// Service implements router.Service and router.TargetDispatcher for Glue.
type Service struct {
	log     *serviceutil.ServiceLogger
	store   *glueStore
	cfg     *config.Config
	clk     clock.Clock
	locks   serviceutil.RecordLocks
	typedOp map[string]op.Operation
}

// New returns a configured Glue Service.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	s := &Service{
		log:   log,
		store: newGlueStore(st, log),
		cfg:   cfg,
		clk:   clk,
	}
	s.typedOp = s.typedOps()
	return s
}

func (s *Service) Name() string                { return serviceName }
func (s *Service) RegisterRoutes(_ chi.Router) {}
func (s *Service) TargetPrefix() string        { return "AWSGlue." }

// Dispatch serves every operation through its typed implementation, in the
// wire protocol the request arrived in: AWS JSON 1.1 (Glue's own), JSON 1.0
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
			Code: "UnsupportedProtocol", Message: "Glue does not support wire protocol " + c.Name() + ".",
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
