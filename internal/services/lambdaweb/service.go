// Package lambdaweb emulates the AWS Lambda Web Functions control plane (the
// "lambda-web" service, API version 2025-03-07, REST-JSON, signing name
// "lambda"): web functions, their immutable revisions and their endpoints,
// with resource policies and tags.
//
// A web function contains revisions and endpoints. A revision is an immutable
// snapshot of code (a zip in a versioned S3 bucket) and configuration; an
// endpoint routes HTTP traffic to one or two revisions by weight, directly
// (autoDeploymentMode Disabled) or to the newest active revision
// (LatestRevision). It is a different resource model from Lambda Event
// Functions, not a parameter of CreateFunction, so it is its own service.
//
// Scope. This is the control plane: every operation the model declares is
// served. Nothing runs yet — a revision's code is not fetched from S3 or
// started, and an endpoint's domain name is allocated but not served. A
// revision is Active as soon as it exists (its create response reports
// Pending, as AWS's does), and so is an endpoint. See
// docs/services/lambda-web/limitations.md.
//
// Routing. Every binding sits under /2025-03-07/, a version no other service
// uses, so the routes are registered directly (compare lambda's /2015-03-31).
package lambdaweb

import (
	"context"
	"crypto/rand"
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

// serviceName is Overcast's key for the service. The model calls it lambda-web;
// the key drops the hyphen because it names OVERCAST_STATE_<SERVICE>. IAM
// actions use the "lambda:" prefix, as AWS does.
const serviceName = "lambdaweb"

// Service implements router.Service for Lambda Web Functions.
type Service struct {
	cfg   *config.Config
	store state.Store
	clk   clock.Clock
	log   *serviceutil.ServiceLogger

	// mu serialises every write. Most operations check a parent or a sibling
	// before writing (a function's endpoint count, whether an endpoint routes
	// to a revision), and one lock over the service makes each check-then-write
	// a single step. Writes are small and in-process.
	mu sync.Mutex

	// bus receives lifecycle events; nil until InitBus.
	bus *events.Bus

	// The data plane's collaborators, wired by the Init methods below. Each is
	// nil until wired: a service built without them (unit tests, a control-plane
	// only run) serves the control plane and answers 503 on the data plane.
	provider environmentProvider
	auth     invokeAuth
	tel      telemetry
	code     codeSource
}

// New returns a configured Lambda Web Functions Service. It does no I/O.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	return &Service{cfg: cfg, store: st, clk: clk, log: serviceutil.NewServiceLogger(logger, serviceName)}
}

// InitProvider wires the environment provider the data plane starts servers with.
func (s *Service) InitProvider(p environmentProvider) { s.provider = p }

// InitAuth wires the IamAuth authoriser.
func (s *Service) InitAuth(a invokeAuth) { s.auth = a }

// InitTelemetry wires the sink for metrics, system logs and events.
func (s *Service) InitTelemetry(t telemetry) { s.tel = t }

// InitCodeSource wires the reader of revision packages.
func (s *Service) InitCodeSource(c codeSource) { s.code = c }

// Name satisfies router.Service.
func (s *Service) Name() string { return serviceName }

// InitBus wires the event bus lifecycle changes are published to.
func (s *Service) InitBus(b *events.Bus) { s.bus = b }

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

// timestamp renders t as the model's date-time: RFC 3339 with milliseconds.
func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// randomString is n characters of lowercase letters and digits.
func randomString(n int, alphabet string) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// newRevisionID is "rev-" and five characters, the shape AWS's examples show
// (rev-a1b2c).
func newRevisionID() string { return "rev-" + randomString(5, idAlphabet) }

// domainAlphabet is the consonants AWS's example domain names are drawn from
// (bcdfghjklmnpqrst.b7t.lambda-web.…), which keeps a generated name from
// spelling a word.
const domainAlphabet = "bcdfghjklmnpqrstvwxz"

// newDomainName allocates an endpoint domain: the regional form
// {domain-id}.{shard-id}.lambda-web.{region}.on.aws, or for a MultiRegion
// endpoint the global {domain-id}.lambda-web.global.on.aws.
func newDomainName(global bool, region string) string {
	if global {
		return randomString(11, domainAlphabet) + ".lambda-web.global.on.aws"
	}
	shard := randomString(1, domainAlphabet) + randomString(1, "0123456789") + randomString(1, domainAlphabet)
	return randomString(16, domainAlphabet) + "." + shard + ".lambda-web." + region + ".on.aws"
}

func newPolicyRevisionID() string { return uuid.NewString() }

func (s *Service) logSkippedRecord(ns, key string, err error) {
	s.log.Error("lambda-web: skipping undecodable record",
		zap.String("namespace", ns), zap.String("key", key), zap.Error(err))
}

// RegisterRoutes satisfies router.Service. The bindings are in routes.go.
func (s *Service) RegisterRoutes(r chi.Router) { s.registerRoutes(r) }
