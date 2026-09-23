// Package s3tables emulates the Amazon S3 Tables control plane (API version
// 2018-05-10, REST-JSON, signing name "s3tables"): table buckets, namespaces
// and Iceberg tables, with their policies, encryption, storage class, metrics,
// maintenance, record-expiration and replication configuration, and tags.
//
// Every table gets a real warehouse bucket in the emulated S3, named with the
// "--table-s3" suffix S3 reserves for it, so engines and clients can read and
// write the table's Iceberg files over the ordinary S3 API. CreateTable with
// metadata.iceberg.schema writes the table's first metadata.json there.
//
// Nothing runs in the background: maintenance, record expiration and
// replication configuration are stored and echoed, and their job-status
// operations report that nothing has run.
//
// Routing. Every root the model binds — /buckets, /namespaces, /tables,
// /get-table, /tag and the replication and record-expiration roots — is also a
// legal S3 bucket name, so the main router sends a request here only when it
// is signed for "s3tables" and to S3 otherwise (see RootRouters and
// router.go).
package s3tables

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "s3tables"

// Service implements router.Service for S3 Tables.
type Service struct {
	cfg   *config.Config
	store state.Store
	clk   clock.Clock
	log   *serviceutil.ServiceLogger

	// mu serialises every write. The operations that change the tree check a
	// parent or a sibling before writing (a namespace's bucket, a bucket's
	// emptiness, a rename's destination, a version token), and one lock over
	// the whole service is what makes each check-then-write a single step.
	// Writes are small and in-process, so contention is not a concern.
	mu sync.Mutex

	// ensureWarehouse and putObject are the in-process S3 accessor, wired by
	// InitS3Access. router.New always wires them; they are nil only in unit
	// tests that build the service directly, where tables are created without
	// a warehouse bucket or metadata file.
	ensureWarehouse events.S3EnsureBucketFunc
	putObject       events.S3PutObjectFunc
}

// New returns a configured S3 Tables Service. It does no I/O.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	return &Service{cfg: cfg, store: st, clk: clk, log: serviceutil.NewServiceLogger(logger, serviceName)}
}

// InitS3Access wires the in-process S3 accessor: ensure creates a table's
// "--table-s3" warehouse bucket and put writes its metadata file.
func (s *Service) InitS3Access(ensure events.S3EnsureBucketFunc, put events.S3PutObjectFunc) {
	s.ensureWarehouse = ensure
	s.putObject = put
}

// Name satisfies router.Service.
func (s *Service) Name() string { return serviceName }

// RegisterRoutes satisfies router.Service. It registers nothing on the shared
// router: every S3 Tables root is also a legal S3 bucket name, so the main
// router mounts RootRouters behind a signing-name dispatcher instead.
func (s *Service) RegisterRoutes(chi.Router) {}

func (s *Service) lock() func() {
	s.mu.Lock()
	return s.mu.Unlock
}

func (s *Service) regionOf(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.cfg.Region)
}

func (s *Service) accountID() string {
	if s.cfg != nil && strings.TrimSpace(s.cfg.AccountID) != "" {
		return s.cfg.AccountID
	}
	return "000000000000"
}

func (s *Service) now() time.Time { return s.clk.Now().UTC() }

// bucketARN is arn:aws:s3tables:<region>:<account>:bucket/<name>.
func (s *Service) bucketARN(region, name string) string {
	return "arn:aws:s3tables:" + region + ":" + s.accountID() + ":bucket/" + name
}

func (s *Service) newID() string { return uuid.NewString() }

// versionTokenBytes gives a 20-character hex token, the length AWS's tokens
// have.
const versionTokenBytes = 10

func (s *Service) newVersionToken() string {
	b := make([]byte, versionTokenBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// warehouseAlphabet and warehouseRandomLength shape a warehouse bucket name
// like AWS's: the first three groups of a UUID, a 34-character random tail and
// the reserved suffix — 63 characters, the longest a bucket name may be.
const (
	warehouseAlphabet     = "abcdefghijklmnopqrstuvwxyz0123456789"
	warehouseRandomLength = 34
	warehouseUUIDPrefix   = 18
)

func (s *Service) newWarehouseBucketName() string {
	b := make([]byte, warehouseRandomLength)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = warehouseAlphabet[int(b[i])%len(warehouseAlphabet)]
	}
	return uuid.NewString()[:warehouseUUIDPrefix] + "-" + string(b) + serviceutil.TableWarehouseBucketSuffix
}

func (s *Service) logSkippedRecord(ns, key string, err error) {
	s.log.Error("s3tables: skipping undecodable record",
		zap.String("namespace", ns), zap.String("key", key), zap.Error(err))
}
