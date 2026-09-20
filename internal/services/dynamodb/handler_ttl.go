package dynamodb

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

const ttlSweepInterval = 1 * time.Hour

// ttlTransitionDuration is how long a TTL change stays in its intermediate
// ENABLING/DISABLING state before settling, and — as on AWS — the window in
// which a second UpdateTimeToLive for the same table is refused.
//
// AWS's window is "up to one hour"
// (https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateTimeToLive.html).
// Waiting an hour is not the behaviour worth emulating: what a caller has to
// cope with is that the change is *asynchronous* — that DescribeTimeToLive
// answers ENABLING before ENABLED, that expiry does not start until it
// settles, and that a second update inside the window is rejected. Thirty
// seconds reproduces every one of those observably while leaving a poll loop,
// a CDK deploy or a test suite to finish in seconds rather than blocking for
// an hour. It is deliberately long enough that a client which updates and
// immediately reads back sees the intermediate state, rather than a window so
// short that the asynchrony is invisible and code written against Overcast
// breaks on AWS.
const ttlTransitionDuration = 30 * time.Second

// ttlTransitionKey names the per-table transition in the lifecycle scheduler.
const ttlTransitionKey = "ttl"

// AWS's UpdateTimeToLive rejections. The wording is AWS's own, so a caller
// matching on the message keeps working here.
var (
	errTTLModifiedTooOften = &protocol.AWSError{
		Code:       "ValidationException",
		Message:    "Time to live has been modified multiple times within a fixed interval",
		HTTPStatus: http.StatusBadRequest,
	}
	errTTLAlreadyEnabled = &protocol.AWSError{
		Code:       "ValidationException",
		Message:    "TimeToLive is already enabled",
		HTTPStatus: http.StatusBadRequest,
	}
	errTTLAlreadyDisabled = &protocol.AWSError{
		Code:       "ValidationException",
		Message:    "TimeToLive is already disabled",
		HTTPStatus: http.StatusBadRequest,
	}
)

// ---- Request / response types ----------------------------------------------

// timeToLiveSpecificationInput is the request-side TimeToLiveSpecification.
// AWS models both members as required, so both are pointers here: an omitted
// Enabled would otherwise be indistinguishable from false, and an omitted
// AttributeName from an empty one, and both are rejections AWS makes.
type timeToLiveSpecificationInput struct {
	Enabled       *bool   `json:"Enabled"`
	AttributeName *string `json:"AttributeName"`
}

type updateTimeToLiveRequest struct {
	TableName               string                        `json:"TableName"`
	TimeToLiveSpecification *timeToLiveSpecificationInput `json:"TimeToLiveSpecification"`
}

type updateTimeToLiveResponse struct {
	TimeToLiveSpecification *TimeToLiveSpecification `json:"TimeToLiveSpecification"`
}

type describeTimeToLiveRequest struct {
	TableName string `json:"TableName"`
}

type describeTimeToLiveResponse struct {
	TimeToLiveDescription *TimeToLiveDescription `json:"TimeToLiveDescription"`
}

// ---- UpdateTimeToLive / DescribeTimeToLive ---------------------------------

// UpdateTimeToLive handles the DynamoDB UpdateTimeToLive operation.
func (h *Handler) UpdateTimeToLive(w http.ResponseWriter, r *http.Request) {
	var req updateTimeToLiveRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.updateTimeToLiveTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) updateTimeToLiveTyped(ctx context.Context, req *updateTimeToLiveRequest) (*updateTimeToLiveResponse, *protocol.AWSError) {
	log := h.log.WithRecorder(ctx)
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}
	// Request shape first, resource second: AWS answers a malformed
	// TimeToLiveSpecification with a ValidationException even when the table
	// does not exist.
	spec, aerr := validateTimeToLiveSpecification(req.TimeToLiveSpecification)
	if aerr != nil {
		return nil, aerr
	}

	region := h.store.region(ctx)
	defer h.ttlLocks.Lock(ttlLockKey(region, req.TableName))()

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}

	// UpdateTimeToLive is not idempotent on AWS, and no request mutates a
	// table it is refused for.
	now := h.clk.Now()
	switch status := table.ttlStatus(now); {
	case status == ttlStatusEnabling || status == ttlStatusDisabling:
		return nil, errTTLModifiedTooOften
	case spec.Enabled && status == ttlStatusEnabled:
		// Covers a repeat of the same attribute and an attempt to swap it:
		// the attribute can only change by disabling TTL first.
		return nil, errTTLAlreadyEnabled
	case !spec.Enabled && status == ttlStatusDisabled:
		return nil, errTTLAlreadyDisabled
	}

	table.TTL = spec
	table.TTLTransitionAt = now.Add(ttlTransitionDuration).UnixNano()
	if aerr := h.store.putTable(ctx, table); aerr != nil {
		return nil, aerr
	}
	h.scheduleTTLTransition(region, table.TableName, ttlTransitionDuration, table.TTLTransitionAt)

	log.Info("table TTL update accepted",
		zap.String("table", req.TableName),
		zap.Bool("enabled", spec.Enabled),
		zap.String("attribute", spec.AttributeName),
		zap.String("status", table.ttlStatus(now)),
	)

	return &updateTimeToLiveResponse{TimeToLiveSpecification: spec}, nil
}

// DescribeTimeToLive handles the DynamoDB DescribeTimeToLive operation.
func (h *Handler) DescribeTimeToLive(w http.ResponseWriter, r *http.Request) {
	var req describeTimeToLiveRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.describeTimeToLiveTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) describeTimeToLiveTyped(ctx context.Context, req *describeTimeToLiveRequest) (*describeTimeToLiveResponse, *protocol.AWSError) {
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}

	return &describeTimeToLiveResponse{
		TimeToLiveDescription: table.ttlDescription(h.clk.Now()),
	}, nil
}

// validateTimeToLiveSpecification enforces the shape AWS models: both members
// required, AttributeName 1..255 characters. It returns the store-side
// specification so the caller never has to dereference the pointers again.
func validateTimeToLiveSpecification(in *timeToLiveSpecificationInput) (*TimeToLiveSpecification, *protocol.AWSError) {
	if in == nil {
		return nil, ttlNullMemberError("timeToLiveSpecification")
	}
	if in.Enabled == nil {
		return nil, ttlNullMemberError("timeToLiveSpecification.enabled")
	}
	if in.AttributeName == nil {
		return nil, ttlNullMemberError("timeToLiveSpecification.attributeName")
	}
	switch length := utf8.RuneCountInString(*in.AttributeName); {
	case length < 1:
		return nil, ttlConstraintError("timeToLiveSpecification.attributeName", *in.AttributeName,
			"Member must have length greater than or equal to 1")
	case length > 255:
		return nil, ttlConstraintError("timeToLiveSpecification.attributeName", *in.AttributeName,
			"Member must have length less than or equal to 255")
	}
	return &TimeToLiveSpecification{Enabled: *in.Enabled, AttributeName: *in.AttributeName}, nil
}

func ttlNullMemberError(member string) *protocol.AWSError {
	return ttlValidationError("1 validation error detected: Value null at '" + member +
		"' failed to satisfy constraint: Member must not be null")
}

func ttlConstraintError(member, value, constraint string) *protocol.AWSError {
	return ttlValidationError("1 validation error detected: Value '" + value + "' at '" + member +
		"' failed to satisfy constraint: " + constraint)
}

func ttlValidationError(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

// ---- Transition scheduling -------------------------------------------------

// scheduleTTLTransition arms the settle for a table's in-flight TTL change.
// The scheduler is region-scoped because table records are (dynamoStore
// .tableKey), so a same-named table in another region neither cancels this
// transition nor is settled by it.
func (h *Handler) scheduleTTLTransition(region, tableName string, delay time.Duration, deadline int64) {
	h.ttlSched.AfterScoped(region, tableName, ttlTransitionKey, delay, func(ctx context.Context) {
		h.settleTTLTransition(ctx, tableName, deadline)
	})
}

func ttlLockKey(region, tableName string) string { return region + "/" + tableName }

// settleTTLTransition normalises a table record once its transition window has
// closed: the pending deadline is dropped, and a completed disable drops the
// TTL configuration outright so the stored record says what DescribeTimeToLive
// reports — DISABLED, with no attribute.
//
// The status a client reads never depends on this having run (Table.ttlStatus
// derives it from the deadline), so this is convergence, not the transition
// itself: it is safe to run late, twice, or never.
//
// It settles only the transition it was armed for. The window closes at the
// instant a new UpdateTimeToLive becomes acceptable, so this and the new
// update can run at the same moment; without the lock and the deadline check
// a settle that had already read the table wrote the old specification back
// over the update, deadline cleared (#1868).
func (h *Handler) settleTTLTransition(ctx context.Context, tableName string, deadline int64) {
	defer h.ttlLocks.Lock(ttlLockKey(h.store.region(ctx), tableName))()

	table, aerr := h.store.getTable(ctx, tableName)
	if aerr != nil {
		// Deleted mid-transition, or unreadable — there is nothing to settle.
		return
	}
	if table.TTLTransitionAt != deadline || table.ttlTransitionPending(h.clk.Now()) {
		return
	}
	table.TTLTransitionAt = 0
	if table.TTL != nil && !table.TTL.Enabled {
		table.TTL = nil
	}
	if aerr := h.store.putTable(ctx, table); aerr != nil {
		h.log.Error("ttl: settle transition", zap.String("table", tableName), zap.Error(aerr))
		return
	}
	h.log.Debug("ttl: transition settled",
		zap.String("table", tableName),
		zap.String("status", table.ttlStatus(h.clk.Now())),
	)
}

// rearmTTLTransitions re-arms every TTL transition that was still in flight
// when the process last stopped. The deadline is persisted with the table, so
// a window that elapsed while Overcast was down settles immediately and one
// still open is re-armed for the time it has left — the transition completes
// across a restart either way, rather than leaving a table stuck ENABLING.
func (h *Handler) rearmTTLTransitions(ctx context.Context) {
	pairs, err := h.store.scanAllTables(ctx)
	if err != nil {
		h.log.Error("ttl: scan all tables for pending transitions", zap.Error(err))
		return
	}
	for _, kv := range pairs {
		region, name := serviceutil.SplitRegionKey(kv.Key)
		var table Table
		if err := json.Unmarshal([]byte(kv.Value), &table); err != nil {
			h.log.Warn("ttl: unmarshal table; skipping", zap.String("key", name), zap.Error(err))
			continue
		}
		// The scan is a snapshot, and only a filter: which transition each of
		// these tables is in, if any, is decided by the read under the lock.
		if table.TTLTransitionAt == 0 {
			continue
		}
		h.rearmTTLTransition(middleware.ContextWithRegion(ctx, region), region, table.TableName)
	}
}

// rearmTTLTransition arms the settle for one table's in-flight transition, or
// runs it now if the window has already closed.
//
// It reads the record itself, under the table's TTL lock, rather than trusting
// the scan that found it. The scan is a snapshot taken at an unknown remove:
// by the time this runs the transition may have settled, or an
// UpdateTimeToLive may have replaced it with a different one — and arming a
// transition the table no longer has is not merely stale, it is destructive.
// The scheduler keys one pending transition per table, so arming from the
// snapshot *replaces* the entry a newer update armed; and an entry replaced
// after its callback has fired but before that callback claims it stands down
// without running, so the newer transition then never settles at all
// (#1962 — a table left carrying a TTLTransitionAt that outlived its window,
// and a completed disable that never drops its TTL configuration).
//
// updateTimeToLiveTyped holds this same lock across its own write and its own
// arming, so the two possible orders are the two serial ones, and whichever
// arms second arms the deadline the record actually carries.
//
// The settle runs after the lock is released, because settleTTLTransition
// takes the lock itself — and for the same reason it is not armed with a zero
// delay instead, which Scheduler.After runs inline on a real clock.
func (h *Handler) rearmTTLTransition(ctx context.Context, region, tableName string) {
	// A non-zero deadline out of this is one whose window has already closed,
	// so the settle is this call's to run. Zero means there was nothing to
	// re-arm, or that it has been armed for later.
	elapsedDeadline := func() int64 {
		defer h.ttlLocks.Lock(ttlLockKey(region, tableName))()

		table, aerr := h.store.getTable(ctx, tableName)
		if aerr != nil || table.TTLTransitionAt == 0 {
			// Deleted, unreadable, or settled since the scan.
			return 0
		}
		if remaining := time.Unix(0, table.TTLTransitionAt).Sub(h.clk.Now()); remaining > 0 {
			h.scheduleTTLTransition(region, table.TableName, remaining, table.TTLTransitionAt)
			return 0
		}
		return table.TTLTransitionAt
	}()
	if elapsedDeadline != 0 {
		h.settleTTLTransition(ctx, tableName, elapsedDeadline)
	}
}

// ---- Sweeper ---------------------------------------------------------------

// startTTLSweeper re-arms any transition interrupted by a restart, then starts
// a background goroutine that periodically scans TTL-enabled tables and
// deletes expired items (where the TTL attribute value is a Unix epoch
// timestamp in the past).
//
// Real AWS deletes expired items within 48 hours. The emulator sweeps
// once per hour — close to production behaviour and cheap. Tests use a
// mock clock so the interval has no effect on test speed.
func (h *Handler) startTTLSweeper(ctx context.Context) {
	ticker := h.clk.Ticker(ttlSweepInterval)
	go func() {
		defer ticker.Stop()
		// Off the constructor's critical path — Service.New does no store I/O
		// (the startup-budget rule) — but before the first sweep, so a table
		// whose enable completed while Overcast was down is expiring items by
		// the time one runs.
		h.rearmTTLTransitions(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.sweepExpiredItems(ctx)
			}
		}
	}()
}

// sweepExpiredItems scans all TTL-enabled tables across all regions and deletes expired items.
func (h *Handler) sweepExpiredItems(ctx context.Context) {
	pairs, err := h.store.scanAllTables(ctx)
	if err != nil {
		h.log.Error("ttl: scan all tables", zap.Error(err))
		return
	}

	now := h.clk.Now()

	for _, kv := range pairs {
		region, name := serviceutil.SplitRegionKey(kv.Key)
		var table Table
		if err := json.Unmarshal([]byte(kv.Value), &table); err != nil {
			// A single malformed persisted table record — skipped, not a
			// sweep-wide failure, so this is WARN per the malformed-persisted-
			// state policy (CONTRIBUTING.md / AGENTS.md), not ERROR.
			h.log.Warn("ttl: unmarshal table; skipping", zap.String("key", name), zap.Error(err))
			continue
		}
		// Only a settled ENABLED table expires anything: an enable that is
		// still ENABLING has not started expiring on AWS either, and a
		// DISABLING one has already stopped.
		if !table.ttlActive(now) {
			continue
		}
		// Item storage is region-qualified (dynamoStore.tableKey), and this
		// sweep runs on a background context that carries no region. Pin each
		// table's own region — read back from the key it was stored under —
		// or every table's sweep would resolve against the default region's
		// partition and silently delete nothing (or, for a same-named table
		// there, the wrong region's items).
		h.sweepTable(middleware.ContextWithRegion(ctx, region), &table, now.Unix())
	}
}

// sweepTable deletes items whose TTL attribute has expired (value > 0 and
// <= now). Uses the TTL-aware scan so only expired items are returned from
// the store, avoiding a full table scan in the SQL backend.
func (h *Handler) sweepTable(ctx context.Context, table *Table, nowUnix int64) {
	ttlAttr := table.TTL.AttributeName
	items, aerr := h.store.scanExpiredTTL(ctx, table.TableName, ttlAttr, nowUnix)
	if aerr != nil {
		h.log.Error("ttl: scan expired items", zap.String("table", table.TableName), zap.Error(aerr))
		return
	}

	for _, item := range items {
		// Capture old image before deleting for stream records.
		var oldItem Item
		if table.streamEnabled() {
			oldItem = item
		}

		if aerr := h.store.deleteItem(ctx, table, item); aerr != nil {
			h.log.Error("ttl: delete expired item",
				zap.String("table", table.TableName),
				zap.Error(aerr),
			)
			continue
		}

		if table.streamEnabled() && oldItem != nil {
			h.publishDeleteStreamRecord(ctx, table, extractKeys(table, item), oldItem)
		}

		// A per-tick TTL-sweep-cycle outcome (fires on the hourly sweeper's
		// own schedule, not because of a specific client request) — TRACE
		// per the trace-vs-debug policy (CONTRIBUTING.md § Log levels).
		h.log.Trace("ttl: expired item deleted",
			zap.String("table", table.TableName),
		)
	}
}

// parseTTLValue extracts a Unix epoch timestamp from a DynamoDB attribute value.
// The attribute must be of type N (Number). Returns (value, true) on success.
func parseTTLValue(av attrValue) (int64, bool) {
	nVal, ok := av["N"]
	if !ok {
		return 0, false
	}
	s, ok := nVal.(string)
	if !ok {
		return 0, false
	}
	// Parse as float64 first (AWS allows decimal epoch values), then truncate.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int64(math.Trunc(f)), true
}
