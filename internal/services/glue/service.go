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
// Catalog is the read-only view other services use, and Catalogs resolves a
// CatalogId to it. s3tablescatalog, S3 Tables' federated catalog, is read
// live from S3 Tables (federation.go).
package glue

import (
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
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
	typedOp map[string]op.Operation
	// bus receives table and partition changes; nil until InitBus.
	bus *events.Bus
	// s3tables is what s3tablescatalog is read from; nil until InitS3Tables.
	s3tables events.S3TablesCatalog

	// Locking. A write takes cascadeMu shared and then its record's stripe
	// in locks (writeLock); a delete that cascades — DeleteDatabase,
	// DeleteTable, BatchDeleteTable — takes cascadeMu exclusively
	// (cascadeLock) and nothing else. So no write can land a table or
	// partition under a parent a cascade is removing, and no goroutine ever
	// holds two stripes, which could be one stripe twice.
	cascadeMu sync.RWMutex
	locks     serviceutil.RecordLocks
}

// writeLock serialises a write to the record named by key against other
// writes to it and against every cascading delete.
func (s *Service) writeLock(key string) func() {
	s.cascadeMu.RLock()
	unlock := s.locks.Lock(key)
	return func() {
		unlock()
		s.cascadeMu.RUnlock()
	}
}

// databaseLockKey and tableLockKey name the writeLock stripe guarding a
// database's or a table's record.
func databaseLockKey(name string) string { return "db:" + name }

func tableLockKey(dbName, tableName string) string { return "table:" + tableKey(dbName, tableName) }

// cascadeLock excludes every other write for the length of a cascading delete.
func (s *Service) cascadeLock() func() {
	s.cascadeMu.Lock()
	return s.cascadeMu.Unlock
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
