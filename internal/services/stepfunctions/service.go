// Package stepfunctions provides emulation of AWS Step Functions: the whole
// control-plane API and an Amazon States Language interpreter for both query
// languages. See docs/services/stepfunctions.md for the support matrix.
//
// Wire protocol: JSON 1.0 (X-Amz-Target: AWSStepFunctions.*) and RPC v2 CBOR.
package stepfunctions

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "stepfunctions"

// awsapiService is Step Functions's key in the generated AWS model corpus.
// serviceutil.MustAWSService validates it at package initialisation, so a
// key the models do not carry fails immediately rather than silently
// answering every unimplemented operation with a 400.
var awsapiService = serviceutil.MustAWSService(serviceName)

// Service implements router.Service and router.TargetDispatcher for Step Functions.
type Service struct {
	log     *serviceutil.ServiceLogger
	handler *Handler
}

// New returns a configured Step Functions Service.
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	s := newStore(store, cfg.Region)
	return &Service{
		log:     log,
		handler: newHandler(cfg, s, log, clk),
	}
}

// InitBus wires the event bus for state machine lifecycle events.
func (s *Service) InitBus(bus *events.Bus) {
	s.handler.bus = bus
}

// InitRouter wires Overcast's root router so Task states can reach the other
// emulated services. Storing the handle is all this does — no I/O — so it is
// safe to call from router.New(). Until it is called, a Task state fails the
// execution loudly rather than being skipped.
func (s *Service) InitRouter(router http.Handler) {
	s.handler.router = router
}

// Name satisfies router.Service.
func (s *Service) Name() string { return serviceName }

// Stop satisfies router.Stopper. Executions run on their own goroutines, so
// shutdown cancels them and waits for each to write its terminal state, or
// until ctx expires.
func (s *Service) Stop(ctx context.Context) { s.handler.Stop(ctx) }

// RegisterRoutes satisfies router.Service. Step Functions has no path-routed endpoints.
func (s *Service) RegisterRoutes(_ chi.Router) {}

// TargetPrefix satisfies router.TargetDispatcher.
// The AWS SDK sends "AWSStepFunctions." as the target prefix; "AmazonStates." is an older alias.
func (s *Service) TargetPrefix() string { return "AWSStepFunctions." }

// Dispatch satisfies router.TargetDispatcher.
func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	// Executions parked by a previous process resume before the first
	// request is answered, so their tokens and activity tasks are live again.
	s.handler.ensureRehydrated()
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code:       "UnsupportedProtocol",
				Message:    "Step Functions does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		// Keep AWS JSON 1.0 on the legacy handler path until JSON wire-byte
		// goldens cover this service. CBOR uses the typed operation path.
		if c.Name() != codec.NameRPCv2CBOR {
			if fn, ok := s.handler.ops[opName]; ok {
				fn(w, r)
				return
			}
		}
		if typed, ok := s.handler.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		// A real Step Functions operation Overcast has not implemented gets an
		// honest 501, in the request's own wire format;
		// UnknownOperationException stays for a name AWS does not model (#1645).
		serviceutil.WriteUnhandledOperation(w, r, c, awsapiService, opName, errUnknownOperation(opName))
		return
	}

	target := r.Header.Get("X-Amz-Target")
	suffix := target
	if strings.HasPrefix(target, "AWSStepFunctions.") {
		suffix = target[len("AWSStepFunctions."):]
	}
	if fn, ok := s.handler.ops[suffix]; ok {
		fn(w, r)
		return
	}
	serviceutil.WriteUnhandledOperation(w, r, codec.JSON10, awsapiService, suffix, errUnknownOperation(suffix))
}

// errUnknownOperation is AWS's answer for a target naming no Step Functions
// operation at all.
func errUnknownOperation(opName string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "UnknownOperationException",
		Message:    "Unknown Step Functions operation: " + opName,
		HTTPStatus: http.StatusBadRequest,
	}
}
