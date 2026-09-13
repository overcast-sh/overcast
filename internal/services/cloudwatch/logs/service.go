// Package logs implements the AWS CloudWatch Logs API emulator.
// See docs/services/cloudwatch-logs.md for the support matrix.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/metrics"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "logs"

// Service implements router.Service for CloudWatch Logs.
type Service struct {
	cfg     *config.Config
	store   state.Store
	log     *serviceutil.ServiceLogger
	handler *Handler
}

// New returns a configured CloudWatch Logs Service.
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	return &Service{
		cfg:     cfg,
		store:   store,
		log:     log,
		handler: newHandler(cfg, store, log, clk),
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return serviceName }

// TargetPrefix returns the X-Amz-Target prefix for CloudWatch Logs dispatch.
func (s *Service) TargetPrefix() string { return "Logs_20140328." }

// InitBus wires the event bus so that log group lifecycle events appear on the topology map.
func (s *Service) InitBus(b *events.Bus) {
	s.handler.bus = b
}

// InitMetrics wires the shared service-metrics recorder so metric filters
// (metric_filter.go) can publish the datapoints matching log events produce.
// Called once from router.New, after metrics.NewRecorder, only while
// collection is enabled; a Service without it stores, describes and tests
// metric filters but publishes nothing — matching SQS's InitMetrics contract.
// No I/O: the filters themselves are loaded lazily on the first batch that
// reaches their log group.
func (s *Service) InitMetrics(r metrics.Recorder) {
	s.handler.store.metrics = r
	s.handler.store.log = s.log
}

// RegisterRoutes is a no-op — CloudWatch Logs uses POST / which is handled
// by the router's target dispatcher.
func (s *Service) RegisterRoutes(r chi.Router) {}

// Stop flushes any in-memory log events that the debounced writer has not yet
// persisted to the state store. Implements router.Stopper so the router calls
// this during graceful shutdown.
func (s *Service) Stop(ctx context.Context) {
	s.handler.store.Stop(ctx)
}

// debugEventsScanLimit bounds how many rows DebugStateKeys/DebugStateValues
// return in one response. Log event volume is unbounded (that's the whole
// reason it graduated to a dedicated table — see storage-plan.md 2.3), so
// dumping every row into a synchronous JSON response is unsafe for a busy
// deployment. Real pagination for the raw state debugger is tracked
// separately (storage-plan.md 3.13); this is a stopgap that keeps the debug
// view usable in the meantime.
const debugEventsScanLimit = 500

// DebugNamespace returns the virtual raw-state namespace name for CloudWatch
// Logs events, implementing router.DebugStateProvider. Log events live in
// the dedicated logs_events SQL table (or the in-memory equivalent), not the
// generic kv store, so without this they'd be invisible to /_overcast/debug/state and
// exempt from /_overcast/reset — mirrors DynamoDB's "dynamodb:items" virtual
// namespace (internal/services/dynamodb/service.go).
func (s *Service) DebugNamespace() string { return "logs:events" }

// DebugStateKeys returns up to debugEventsScanLimit virtual keys for
// /_overcast/debug/state's top-level listing.
func (s *Service) DebugStateKeys(ctx context.Context) ([]string, error) {
	records, _, err := s.handler.store.backend.debugScan(ctx, debugEventsScanLimit)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(records))
	for _, r := range records {
		keys = append(keys, logsDebugEventKey(r))
	}
	return keys, nil
}

// DebugStateValues returns raw log event values keyed by
// region/group/stream/timestamp/seq, capped at debugEventsScanLimit rows. A
// "_truncated" pseudo-key is added when more rows exist than were returned.
func (s *Service) DebugStateValues(ctx context.Context) (map[string]string, error) {
	records, truncated, err := s.handler.store.backend.debugScan(ctx, debugEventsScanLimit)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(records)+1)
	for _, r := range records {
		raw, err := json.Marshal(LogEvent{Timestamp: r.Timestamp, Message: r.Message})
		if err != nil {
			return nil, err
		}
		values[logsDebugEventKey(r)] = string(raw)
	}
	if truncated {
		values["_truncated"] = fmt.Sprintf("showing first %d events only (storage-plan.md 3.13 tracks real pagination)", debugEventsScanLimit)
	}
	return values, nil
}

// DebugResetState deletes every persisted log event, for /_overcast/reset.
func (s *Service) DebugResetState(ctx context.Context) error {
	return s.handler.store.backend.debugDeleteAll(ctx)
}

func logsDebugEventKey(r debugEventRecord) string {
	return fmt.Sprintf("%s/%s/%s/%d/%d", r.Region, r.Group, r.Stream, r.Timestamp, r.Seq)
}

// Dispatch routes to the correct CloudWatch Logs handler based on X-Amz-Target.
func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code:       "UnsupportedProtocol",
				Message:    "CloudWatch Logs does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		// Preserve the existing JSON 1.1 path until JSON wire-byte goldens
		// cover the full Logs surface. CBOR uses the typed operation path.
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
		// A real Logs operation Overcast has not implemented gets an honest
		// 501, in the request's own wire format; UnknownOperationException
		// stays for a name AWS does not model (#1645).
		serviceutil.WriteUnhandledOperation(w, r, c, awsapiService, opName, &protocol.AWSError{
			Code:       "UnknownOperationException",
			Message:    "Unknown CloudWatch Logs operation: " + opName,
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	target := r.Header.Get("X-Amz-Target")
	const prefix = "Logs_20140328."
	target = strings.TrimPrefix(target, prefix)
	if fn, ok := s.handler.ops[target]; ok {
		fn(w, r)
		return
	}
	serviceutil.WriteUnhandledOperation(w, r, codec.JSON11, awsapiService, target, &protocol.AWSError{
		Code:       "UnknownOperationException",
		Message:    "Unknown operation: " + target,
		HTTPStatus: http.StatusBadRequest,
	})
}

// awsapiService is Logs' key in the generated AWS model corpus — the one
// capabilities_dev.go declares under — which is not serviceName ("logs"
// aliases to it and is not itself a key). serviceutil.MustAWSService
// validates that at package initialisation: getting this wrong is what
// silently answers every unimplemented Logs operation with a 400.
var awsapiService = serviceutil.MustAWSService("cloudwatch-logs")

// LogWriter returns an events.LogWriter backed by this service's store.
// Lambda and other services use this to write invocation logs to CloudWatch
// without importing the logs package directly (avoids import cycles).
func (s *Service) LogWriter() events.LogWriter {
	return &logWriter{store: s.handler.store, cfg: s.cfg}
}

// logWriter implements events.LogWriter using the logs store.
type logWriter struct {
	store *logsStore
	cfg   *config.Config
}

// EnsureLogGroup creates the log group if it does not already exist.
// Creation is idempotent — an existing group is left untouched.
func (lw *logWriter) EnsureLogGroup(ctx context.Context, groupName string) error {
	if _, aerr := lw.store.getLogGroup(ctx, groupName); aerr != nil {
		if aerr.Code != "ResourceNotFoundException" {
			return fmt.Errorf("logs: ensure log group %s: get group: %w", groupName, aerr)
		}
		region := middleware.RegionFromContext(ctx, lw.cfg.Region)
		g := &LogGroup{
			Name:         groupName,
			ARN:          protocol.LogGroupARN(region, lw.cfg.AccountID, groupName),
			CreationTime: lw.store.clk.Now().UnixMilli(),
		}
		if putErr := lw.store.putLogGroup(ctx, g); putErr != nil {
			return fmt.Errorf("logs: ensure log group %s: put group: %w", groupName, putErr)
		}
	}
	return nil
}

// EnsureLogStream creates the log group (if absent) then the log stream (if
// absent).  Both creations are idempotent — existing resources are left
// untouched and no error is returned.
func (lw *logWriter) EnsureLogStream(ctx context.Context, groupName, streamName string) error {
	if groupName == "" || streamName == "" {
		return fmt.Errorf("logs: EnsureLogStream: groupName and streamName must be non-empty (got %q / %q)", groupName, streamName)
	}
	// Create group if missing.
	region := middleware.RegionFromContext(ctx, lw.cfg.Region)
	if _, aerr := lw.store.getLogGroup(ctx, groupName); aerr != nil {
		if aerr.Code != "ResourceNotFoundException" {
			return fmt.Errorf("logs: ensure log stream %s/%s: get group: %w", groupName, streamName, aerr)
		}
		g := &LogGroup{
			Name:         groupName,
			ARN:          protocol.LogGroupARN(region, lw.cfg.AccountID, groupName),
			CreationTime: lw.store.clk.Now().UnixMilli(),
		}
		if putErr := lw.store.putLogGroup(ctx, g); putErr != nil {
			return fmt.Errorf("logs: ensure log stream %s/%s: put group: %w", groupName, streamName, putErr)
		}
	}
	// Create stream if missing.
	if _, aerr := lw.store.getLogStream(ctx, groupName, streamName); aerr != nil {
		if aerr.Code != "ResourceNotFoundException" {
			return fmt.Errorf("logs: ensure log stream %s/%s: get stream: %w", groupName, streamName, aerr)
		}
		ls := &LogStream{
			Name:                streamName,
			ARN:                 protocol.LogStreamARN(region, lw.cfg.AccountID, groupName, streamName),
			CreationTime:        lw.store.clk.Now().UnixMilli(),
			UploadSequenceToken: "1",
		}
		if putErr := lw.store.putLogStream(ctx, groupName, ls); putErr != nil {
			return fmt.Errorf("logs: ensure log stream %s/%s: put stream: %w", groupName, streamName, putErr)
		}
	}
	return nil
}

// WriteLogEvents appends log entries to the named stream.
// The group and stream must exist (call EnsureLogStream first).
func (lw *logWriter) WriteLogEvents(ctx context.Context, groupName, streamName string, entries []events.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	ev := make([]LogEvent, len(entries))
	ingestionTime := lw.store.clk.Now().UnixMilli()
	for i, e := range entries {
		ev[i] = LogEvent{
			Timestamp:     e.Timestamp,
			Message:       e.Message,
			IngestionTime: ingestionTime,
		}
	}
	if aerr := lw.store.appendEvents(ctx, groupName, streamName, ev); aerr != nil {
		return fmt.Errorf("logs: write log events %s/%s: %w", groupName, streamName, aerr)
	}
	// Update stream timestamps so the UI shows the correct Last Event column.
	ls, aerr := lw.store.getLogStream(ctx, groupName, streamName)
	if aerr != nil {
		return fmt.Errorf("logs: get stream for timestamp update %s/%s: %w", groupName, streamName, aerr)
	}
	for _, e := range ev {
		if ls.FirstEventTimestamp == 0 || e.Timestamp < ls.FirstEventTimestamp {
			ls.FirstEventTimestamp = e.Timestamp
		}
		if e.Timestamp > ls.LastEventTimestamp {
			ls.LastEventTimestamp = e.Timestamp
		}
	}
	ls.LastIngestionTime = ingestionTime
	if aerr := lw.store.putLogStream(ctx, groupName, ls); aerr != nil {
		return fmt.Errorf("logs: update stream timestamps %s/%s: %w", groupName, streamName, aerr)
	}
	return nil
}
