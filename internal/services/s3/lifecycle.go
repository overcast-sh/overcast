package s3

// lifecycle.go holds the stored model for S3 bucket lifecycle configuration,
// the in-memory index that keeps the object hot paths free of lifecycle work,
// and the clock-driven sweeper that actually applies the rules.
//
// Scope (docs/plans/full-emulation-priority.md §7): expirations really delete;
// transitions stop at a synthetic storage-class marker. There is no Glacier
// retrieval-delay simulation and no replication.
//
// Versioned buckets are swept differently from unversioned ones, because on AWS
// the same rule means different things there: expiring the current version of a
// versioned object adds a delete marker rather than removing anything, and the
// noncurrent actions only have anything to act on once a key has history. The
// two sweeps are sweepBucketObjects and sweepBucketVersions below.
//
// Anything the sweeper cannot act on must be refused by handler_lifecycle.go
// rather than silently dropped. Keep the model, validation, wire response and
// capability note in step.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// nsLifecycle stores one LifecycleConfiguration per bucket, keyed by bucket
// name. A bucket with no configuration has no record — which is what makes the
// idle sweep a single scan of an empty namespace.
const nsLifecycle = "s3:lifecycle"

// lifecycleSweepInterval is how often the sweeper wakes. Real S3 applies
// lifecycle actions asynchronously, "typically within 48 hours" of an object
// becoming eligible; an hourly local sweep is well inside that and matches the
// DynamoDB TTL sweeper's cadence. The sweep is driven by the injected clock,
// so tests reach it by advancing a mock rather than waiting.
const lifecycleSweepInterval = time.Hour

// lifecycleSweepPageSize bounds how many objects one sweep step holds in
// memory. The sweeper pages through a bucket rather than materialising it, so
// peak memory is a function of this constant and not of bucket size.
const lifecycleSweepPageSize = 1000

// ---- Stored model ----------------------------------------------------------

// LifecycleConfiguration is a bucket's stored lifecycle rules.
type LifecycleConfiguration struct {
	Rules []LifecycleRule `json:"rules"`

	// TransitionDefaultMinimumObjectSize carries the
	// x-amz-transition-default-minimum-object-size parameter, which
	// PutBucketLifecycleConfiguration takes as a request header rather than in
	// the body. Empty means the caller supplied none, which reads as AWS's
	// default — see transitionDefaultMinimum.
	TransitionDefaultMinimumObjectSize string `json:"transition_default_minimum_object_size,omitempty"`
}

// The values of com.amazonaws.s3#TransitionDefaultMinimumObjectSize, and the
// size the enum is named after.
//
// AWS documents them as: all_storage_classes_128K — objects smaller than
// 128 KB will not transition to any storage class by default;
// varies_by_storage_class — objects smaller than 128 KB will transition to
// Glacier Flexible Retrieval or Glacier Deep Archive storage classes, and by
// default all other storage classes will prevent transitions smaller than
// 128 KB.
const (
	transitionDefaultMinimumAll128K = "all_storage_classes_128K"
	transitionDefaultMinimumVaries  = "varies_by_storage_class"
	transitionDefaultMinimumBytes   = 128 * 1024
)

// glacierTransitionStorageClasses are the two classes varies_by_storage_class
// exempts from the 128 KB minimum: Glacier Flexible Retrieval (GLACIER) and
// Glacier Deep Archive (DEEP_ARCHIVE). Glacier Instant Retrieval (GLACIER_IR)
// is deliberately absent — AWS names only the other two.
var glacierTransitionStorageClasses = map[string]bool{
	"GLACIER":      true,
	"DEEP_ARCHIVE": true,
}

// transitionDefaultMinimum resolves the behaviour in force, mapping the unset
// case to AWS's default.
func (c *LifecycleConfiguration) transitionDefaultMinimum() string {
	if c.TransitionDefaultMinimumObjectSize == "" {
		return transitionDefaultMinimumAll128K
	}
	return c.TransitionDefaultMinimumObjectSize
}

// allowsTransition reports whether the configuration's default minimum object
// size lets obj transition to storageClass under rule.
//
// A rule whose own filter names an object-size bound opts out entirely:
// "custom filters always take precedence over the default transition
// behavior".
func (c *LifecycleConfiguration) allowsTransition(rule *LifecycleRule, obj *Object, storageClass string) bool {
	if obj.ContentLength >= transitionDefaultMinimumBytes || rule.hasObjectSizeFilter() {
		return true
	}
	return c.transitionDefaultMinimum() == transitionDefaultMinimumVaries &&
		glacierTransitionStorageClasses[storageClass]
}

// LifecycleRule is one lifecycle rule.
//
// Exactly one of Prefix and Filter is non-nil: Prefix is the deprecated
// rule-level form (still what the CLI's simplest example emits), Filter the
// current one. AWS refuses a rule carrying both, and echoes back whichever
// form was supplied rather than rewriting one into the other — so both are
// modelled rather than normalised.
type LifecycleRule struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "Enabled" or "Disabled"

	Prefix *string          `json:"prefix,omitempty"`
	Filter *LifecycleFilter `json:"filter,omitempty"`

	Expiration                     *LifecycleExpiration                   `json:"expiration,omitempty"`
	NoncurrentVersionExpiration    *LifecycleNoncurrentVersionExpiration  `json:"noncurrent_version_expiration,omitempty"`
	NoncurrentVersionTransitions   []LifecycleNoncurrentVersionTransition `json:"noncurrent_version_transitions,omitempty"`
	Transitions                    []LifecycleTransition                  `json:"transitions,omitempty"`
	AbortIncompleteMultipartUpload *LifecycleAbortMPU                     `json:"abort_incomplete_multipart_upload,omitempty"`
}

// LifecycleFilter selects which objects a rule applies to. AWS models it as a
// union: at most one of the fields below is set, with And carrying the
// conjunction of several predicates.
type LifecycleFilter struct {
	Prefix                string        `json:"prefix,omitempty"`
	Tag                   *LifecycleTag `json:"tag,omitempty"`
	ObjectSizeGreaterThan *int64        `json:"object_size_greater_than,omitempty"`
	ObjectSizeLessThan    *int64        `json:"object_size_less_than,omitempty"`
	And                   *LifecycleAnd `json:"and,omitempty"`
}

// LifecycleAnd is the conjunction form of a filter.
type LifecycleAnd struct {
	Prefix                string         `json:"prefix,omitempty"`
	Tags                  []LifecycleTag `json:"tags,omitempty"`
	ObjectSizeGreaterThan *int64         `json:"object_size_greater_than,omitempty"`
	ObjectSizeLessThan    *int64         `json:"object_size_less_than,omitempty"`
}

// LifecycleTag is one object-tag predicate.
type LifecycleTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// LifecycleExpiration deletes matching objects. Exactly one of Days, Date and
// ExpiredObjectDeleteMarker is set — AWS rejects a rule carrying more.
//
// ExpiredObjectDeleteMarker is not an age at all: it removes a delete marker
// that has become the only version of its key, which is the tidy-up AWS offers
// for a versioned bucket whose noncurrent versions have already expired away
// beneath their marker.
type LifecycleExpiration struct {
	Days                      int        `json:"days,omitempty"`
	Date                      *time.Time `json:"date,omitempty"`
	ExpiredObjectDeleteMarker bool       `json:"expired_object_delete_marker,omitempty"`
}

// LifecycleNoncurrentVersionExpiration permanently removes versions that have
// been noncurrent for NoncurrentDays.
//
// NewerNoncurrentVersions is how many noncurrent versions must sit above one
// before it is eligible — "retain this many, then start expiring" — and is nil
// when the caller set none, which AWS reads as zero.
type LifecycleNoncurrentVersionExpiration struct {
	NoncurrentDays          int  `json:"noncurrent_days"`
	NewerNoncurrentVersions *int `json:"newer_noncurrent_versions,omitempty"`
}

// LifecycleNoncurrentVersionTransition marks noncurrent versions with a storage
// class, on the same eligibility rules as the expiration above.
type LifecycleNoncurrentVersionTransition struct {
	NoncurrentDays          int    `json:"noncurrent_days"`
	NewerNoncurrentVersions *int   `json:"newer_noncurrent_versions,omitempty"`
	StorageClass            string `json:"storage_class"`
}

// retained returns how many newer noncurrent versions must exist before the
// action applies, mapping AWS's unset case to zero.
func (e *LifecycleNoncurrentVersionExpiration) retained() int {
	if e == nil || e.NewerNoncurrentVersions == nil {
		return 0
	}
	return *e.NewerNoncurrentVersions
}

// retained is LifecycleNoncurrentVersionExpiration.retained for transitions.
func (t *LifecycleNoncurrentVersionTransition) retained() int {
	if t == nil || t.NewerNoncurrentVersions == nil {
		return 0
	}
	return *t.NewerNoncurrentVersions
}

// LifecycleTransition marks matching objects with a storage class. Exactly one
// of Days and Date is set.
type LifecycleTransition struct {
	Days         int        `json:"days,omitempty"`
	Date         *time.Time `json:"date,omitempty"`
	StorageClass string     `json:"storage_class"`
}

// LifecycleAbortMPU aborts multipart uploads left in progress too long.
type LifecycleAbortMPU struct {
	DaysAfterInitiation int `json:"days_after_initiation"`
}

// enabled reports whether the rule's Status selects it for action.
func (r *LifecycleRule) enabled() bool { return r.Status == lifecycleStatusEnabled }

// keyPrefix returns the key prefix the rule selects on, in either the legacy
// or the Filter form. An empty string means the whole bucket.
func (r *LifecycleRule) keyPrefix() string {
	switch {
	case r.Prefix != nil:
		return *r.Prefix
	case r.Filter == nil:
		return ""
	case r.Filter.And != nil:
		return r.Filter.And.Prefix
	default:
		return r.Filter.Prefix
	}
}

// matches reports whether obj is selected by the rule's filter.
func (r *LifecycleRule) matches(obj *Object) bool {
	if !strings.HasPrefix(obj.Key, r.keyPrefix()) {
		return false
	}
	if r.Prefix != nil || r.Filter == nil {
		return true
	}
	f := r.Filter
	if f.And != nil {
		for i := range f.And.Tags {
			if !objectHasTag(obj, &f.And.Tags[i]) {
				return false
			}
		}
		return sizeWithin(obj.ContentLength, f.And.ObjectSizeGreaterThan, f.And.ObjectSizeLessThan)
	}
	if f.Tag != nil && !objectHasTag(obj, f.Tag) {
		return false
	}
	return sizeWithin(obj.ContentLength, f.ObjectSizeGreaterThan, f.ObjectSizeLessThan)
}

// hasObjectSizeFilter reports whether the rule's own filter bounds object size,
// in either the plain or the And form. Such a rule sets its own minimum, so
// the bucket's default minimum transition size does not apply to it.
func (r *LifecycleRule) hasObjectSizeFilter() bool {
	if r.Filter == nil {
		return false
	}
	if and := r.Filter.And; and != nil {
		return and.ObjectSizeGreaterThan != nil || and.ObjectSizeLessThan != nil
	}
	return r.Filter.ObjectSizeGreaterThan != nil || r.Filter.ObjectSizeLessThan != nil
}

func objectHasTag(obj *Object, tag *LifecycleTag) bool {
	v, ok := obj.Tags[tag.Key]
	return ok && v == tag.Value
}

// sizeWithin applies AWS's exclusive object-size bounds.
func sizeWithin(size int64, greaterThan, lessThan *int64) bool {
	if greaterThan != nil && size <= *greaterThan {
		return false
	}
	if lessThan != nil && size >= *lessThan {
		return false
	}
	return true
}

// expiresAt returns when AWS would expire obj under this rule.
//
// For a Days rule S3 "adds the number of days to the object's last modified
// date and rounds the resulting time to the next day midnight UTC", so an
// object written at 15:00 under a 1-day rule expires at 00:00 UTC two
// calendar days later, not 24 hours later. A Date rule expires at the date
// itself, which validation has already pinned to UTC midnight.
func (r *LifecycleRule) expiresAt(obj *Object) (time.Time, bool) {
	if r.Expiration == nil || r.Expiration.ExpiredObjectDeleteMarker {
		// An ExpiredObjectDeleteMarker rule sets no age, so it schedules
		// nothing here — it is evaluated against a key's whole history in
		// expiresDeleteMarker instead.
		return time.Time{}, false
	}
	if r.Expiration.Date != nil {
		return r.Expiration.Date.UTC(), true
	}
	return nextUTCMidnight(obj.LastModified.UTC().AddDate(0, 0, r.Expiration.Days)), true
}

// transitionAt returns when the given transition takes effect for obj, using
// the same midnight rounding as expiresAt.
func transitionAt(t *LifecycleTransition, obj *Object) time.Time {
	if t.Date != nil {
		return t.Date.UTC()
	}
	return nextUTCMidnight(obj.LastModified.UTC().AddDate(0, 0, t.Days))
}

// nextUTCMidnight rounds t up to the next UTC midnight. Truncate works on
// absolute time, which is UTC-aligned, so truncating to 24h yields the UTC
// midnight that starts t's day.
//
// A time that is *already* midnight is returned unchanged: AWS rounds the
// computed expiry to the next midnight, and a value sitting on one needs no
// rounding. Rounding it forward anyway would give an object written at
// 00:00:00 UTC a whole extra day of life. Rare against wall-clock timestamps,
// routine against a mock clock started at a round time.
func nextUTCMidnight(t time.Time) time.Time {
	utc := t.UTC()
	day := utc.Truncate(24 * time.Hour)
	if day.Equal(utc) {
		return day
	}
	return day.Add(24 * time.Hour)
}

// expirationFor returns the earliest expiry the configuration schedules for
// obj, and the rule that schedules it. AWS resolves overlapping expiration
// rules in the object's favour by applying the one that fires first.
func (c *LifecycleConfiguration) expirationFor(obj *Object) (time.Time, *LifecycleRule) {
	var (
		soonest time.Time
		winner  *LifecycleRule
	)
	for i := range c.Rules {
		rule := &c.Rules[i]
		if !rule.enabled() || rule.Expiration == nil || !rule.matches(obj) {
			continue
		}
		at, ok := rule.expiresAt(obj)
		if !ok {
			continue
		}
		if winner == nil || at.Before(soonest) {
			soonest, winner = at, rule
		}
	}
	return soonest, winner
}

// transitionFor returns the storage class obj should carry as of now: the
// class of the latest-firing transition that is already due.
//
// A transition the bucket's default minimum object size blocks is skipped as
// though it were not due, so a small object keeps the class an earlier,
// allowed transition gave it rather than being promoted past the minimum.
func (c *LifecycleConfiguration) transitionFor(obj *Object, now time.Time) (string, bool) {
	var (
		latest time.Time
		class  string
	)
	for i := range c.Rules {
		rule := &c.Rules[i]
		if !rule.enabled() || len(rule.Transitions) == 0 || !rule.matches(obj) {
			continue
		}
		for j := range rule.Transitions {
			at := transitionAt(&rule.Transitions[j], obj)
			if at.After(now) {
				continue
			}
			if !c.allowsTransition(rule, obj, rule.Transitions[j].StorageClass) {
				continue
			}
			if class == "" || at.After(latest) {
				latest, class = at, rule.Transitions[j].StorageClass
			}
		}
	}
	return class, class != ""
}

// abortIncompleteAfter returns the shortest DaysAfterInitiation any enabled
// rule sets. AWS scopes this action by the rule's filter, but an in-progress
// upload has no tags or size yet, so only rules whose prefix the key matches
// are consulted.
func (c *LifecycleConfiguration) abortIncompleteAfter(key string) (int, bool) {
	best := 0
	for i := range c.Rules {
		rule := &c.Rules[i]
		if !rule.enabled() || rule.AbortIncompleteMultipartUpload == nil {
			continue
		}
		if !strings.HasPrefix(key, rule.keyPrefix()) {
			continue
		}
		days := rule.AbortIncompleteMultipartUpload.DaysAfterInitiation
		if best == 0 || days < best {
			best = days
		}
	}
	return best, best > 0
}

// hasObjectActions reports whether the configuration schedules anything the
// object sweep needs to look at. A configuration with only an
// AbortIncompleteMultipartUpload rule never justifies paging the bucket.
func (c *LifecycleConfiguration) hasObjectActions() bool {
	for i := range c.Rules {
		rule := &c.Rules[i]
		if !rule.enabled() {
			continue
		}
		if rule.Expiration != nil || len(rule.Transitions) > 0 ||
			rule.NoncurrentVersionExpiration != nil || len(rule.NoncurrentVersionTransitions) > 0 {
			return true
		}
	}
	return false
}

// noncurrentActionFor returns the rule and eligibility time for permanently
// expiring one noncurrent version.
//
// rank is the version's position among the key's noncurrent versions, newest
// first, which is what NewerNoncurrentVersions counts. noncurrentSince is when
// the version stopped being current, which AWS defines as the moment its
// successor was written — not when the version itself was written.
func (c *LifecycleConfiguration) noncurrentExpiryFor(obj *Object, noncurrentSince time.Time, rank int) (time.Time, *LifecycleRule) {
	var (
		soonest time.Time
		winner  *LifecycleRule
	)
	for i := range c.Rules {
		rule := &c.Rules[i]
		action := rule.NoncurrentVersionExpiration
		if !rule.enabled() || action == nil || rank < action.retained() || !rule.matches(obj) {
			continue
		}
		at := noncurrentAt(noncurrentSince, action.NoncurrentDays)
		if winner == nil || at.Before(soonest) {
			soonest, winner = at, rule
		}
	}
	return soonest, winner
}

// noncurrentTransitionFor returns the storage class a noncurrent version should
// carry as of now: the class of the latest-firing eligible transition.
//
// It composes with the bucket's default minimum object size exactly as the
// current-version transition does — a small version is skipped as though its
// transition were not due, so it keeps whatever class an earlier allowed
// transition gave it.
func (c *LifecycleConfiguration) noncurrentTransitionFor(obj *Object, noncurrentSince time.Time, rank int, now time.Time) (string, bool) {
	var (
		latest time.Time
		class  string
	)
	for i := range c.Rules {
		rule := &c.Rules[i]
		if !rule.enabled() || len(rule.NoncurrentVersionTransitions) == 0 || !rule.matches(obj) {
			continue
		}
		for j := range rule.NoncurrentVersionTransitions {
			action := &rule.NoncurrentVersionTransitions[j]
			if rank < action.retained() {
				continue
			}
			at := noncurrentAt(noncurrentSince, action.NoncurrentDays)
			if at.After(now) {
				continue
			}
			if !c.allowsTransition(rule, obj, action.StorageClass) {
				continue
			}
			if class == "" || at.After(latest) {
				latest, class = at, action.StorageClass
			}
		}
	}
	return class, class != ""
}

// noncurrentAt is expiresAt's day arithmetic for a noncurrent version: the same
// "add the days, round up to the next UTC midnight" rule, counted from when the
// version became noncurrent.
func noncurrentAt(noncurrentSince time.Time, days int) time.Time {
	return nextUTCMidnight(noncurrentSince.UTC().AddDate(0, 0, days))
}

// expiresDeleteMarker reports whether an enabled ExpiredObjectDeleteMarker rule
// covers this marker. The caller is responsible for the other half of AWS's
// condition — that the marker has no noncurrent versions under it — because
// that is a property of the key's history rather than of the marker.
func (c *LifecycleConfiguration) expiresDeleteMarker(obj *Object) *LifecycleRule {
	for i := range c.Rules {
		rule := &c.Rules[i]
		if !rule.enabled() || rule.Expiration == nil || !rule.Expiration.ExpiredObjectDeleteMarker {
			continue
		}
		if !rule.matches(obj) {
			continue
		}
		return rule
	}
	return nil
}

// hasAbortActions reports whether any enabled rule aborts incomplete uploads.
func (c *LifecycleConfiguration) hasAbortActions() bool {
	for i := range c.Rules {
		if c.Rules[i].enabled() && c.Rules[i].AbortIncompleteMultipartUpload != nil {
			return true
		}
	}
	return false
}

// ---- Store access ----------------------------------------------------------

func (s *s3Store) getLifecycle(ctx context.Context, bucket string) (*LifecycleConfiguration, bool, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsLifecycle, bucket)
	if err != nil {
		return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, false, nil
	}
	var cfg LifecycleConfiguration
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		// A single unreadable record must not become a 500 for the bucket:
		// report it the way AWS reports an absent configuration, which is the
		// state the caller can actually act on (CONTRIBUTING.md
		// malformed-persisted-state policy).
		return nil, false, nil
	}
	return &cfg, true, nil
}

func (s *s3Store) putLifecycle(ctx context.Context, bucket string, cfg *LifecycleConfiguration) *protocol.AWSError {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsLifecycle, bucket, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *s3Store) deleteLifecycle(ctx context.Context, bucket string) *protocol.AWSError {
	if err := s.store.Delete(ctx, nsLifecycle, bucket); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// scanLifecycle returns every stored configuration, keyed by bucket. One store
// round trip; an empty namespace costs a single empty scan.
func (s *s3Store) scanLifecycle(ctx context.Context) (map[string]*LifecycleConfiguration, error) {
	pairs, err := s.store.Scan(ctx, nsLifecycle, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*LifecycleConfiguration, len(pairs))
	for _, kv := range pairs {
		var cfg LifecycleConfiguration
		if jsonErr := json.Unmarshal([]byte(kv.Value), &cfg); jsonErr != nil {
			// One malformed persisted record must not fail the whole scan
			// (CONTRIBUTING.md malformed-persisted-state rule).
			continue
		}
		out[kv.Key] = &cfg
	}
	return out, nil
}

// ---- In-memory index -------------------------------------------------------

// lifecycleIndex is a snapshot of every bucket's lifecycle configuration,
// published as one immutable map behind an atomic pointer.
//
// It exists so that the object hot paths — GetObject, HeadObject, PutObject,
// which are the emulator's highest-traffic routes — pay one atomic load and
// one map lookup for a feature almost no bucket uses, instead of a store read
// per request. The snapshot is replaced wholesale on every configuration
// change and at the top of every sweep, never mutated in place.
type lifecycleIndex struct {
	init serviceutil.LazyInit
	snap atomic.Pointer[map[string]*LifecycleConfiguration]
}

// refreshLifecycleIndex rebuilds and publishes the snapshot. Returns the new
// snapshot so the sweeper can use it without a second load.
func (h *Handler) refreshLifecycleIndex(ctx context.Context) (map[string]*LifecycleConfiguration, error) {
	configs, err := h.store.scanLifecycle(ctx)
	if err != nil {
		return nil, err
	}
	h.lifecycle.snap.Store(&configs)
	return configs, nil
}

// lifecycleConfigFor returns the cached configuration for bucket, or nil when
// it has none. The first call loads the snapshot; every later call is an
// atomic load and a map lookup.
//
// Lazy-init only: no store read happens in New() (AGENTS.md startup budget).
func (h *Handler) lifecycleConfigFor(ctx context.Context, bucket string) *LifecycleConfiguration {
	if err := h.lifecycle.init.Do(func() error {
		_, err := h.refreshLifecycleIndex(ctx)
		return err
	}); err != nil {
		h.log.Warn("lifecycle: load configuration index", zap.Error(err))
		return nil
	}
	snap := h.lifecycle.snap.Load()
	if snap == nil {
		return nil
	}
	return (*snap)[bucket]
}

// ---- Object-path hooks -----------------------------------------------------

// expirationHeader returns the value of the x-amz-expiration header AWS sets
// on an object a lifecycle rule will delete, or "" when no rule expires it.
//
// Shape: expiry-date="Fri, 21 Dec 2012 00:00:00 GMT", rule-id="rule-1".
func (h *Handler) expirationHeader(ctx context.Context, obj *Object) string {
	cfg := h.lifecycleConfigFor(ctx, obj.Bucket)
	if cfg == nil {
		return ""
	}
	at, rule := cfg.expirationFor(obj)
	if rule == nil {
		return ""
	}
	return `expiry-date="` + at.Format(lifecycleExpiryDateFormat) + `", rule-id="` + rule.ID + `"`
}

// lifecycleExpiryDateFormat is RFC 1123 with a literal GMT zone, which is what
// S3 puts in x-amz-expiration.
const lifecycleExpiryDateFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

// setExpirationHeader writes x-amz-expiration when a rule expires obj, and
// nothing at all when none does.
func (h *Handler) setExpirationHeader(ctx context.Context, w http.ResponseWriter, obj *Object) {
	if v := h.expirationHeader(ctx, obj); v != "" {
		w.Header().Set("x-amz-expiration", v)
	}
}

// ---- Sweeper ---------------------------------------------------------------

// startLifecycleSweeper starts the single background loop that applies every
// bucket's lifecycle rules. One goroutine and one ticker for the whole
// service — never one per bucket or per object — cancelled by ctx on shutdown.
//
// The returned channel is closed once the loop has exited, so a caller can
// wait for an in-flight sweep to finish rather than returning from shutdown
// while the sweeper is still deleting objects. See Service.Stop.
func (h *Handler) startLifecycleSweeper(ctx context.Context) <-chan struct{} {
	ticker := h.clk.Ticker(lifecycleSweepInterval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.sweepLifecycle(ctx)
			}
		}
	}()
	return done
}

// sweepLifecycle applies every stored configuration once.
//
// The scan of the (usually empty) lifecycle namespace is the whole idle cost:
// no configurations means one empty scan an hour and no object reads at all.
// Buckets without a configuration are never paged.
func (h *Handler) sweepLifecycle(ctx context.Context) {
	configs, err := h.refreshLifecycleIndex(ctx)
	if err != nil {
		h.log.Error("lifecycle: scan configurations", zap.Error(err))
		return
	}
	// The snapshot the object paths read is current as of this tick, so mark
	// the lazy load satisfied — a later object request must not re-scan.
	_ = h.lifecycle.init.Do(func() error { return nil })

	if len(configs) == 0 {
		return
	}

	now := h.clk.Now().UTC()
	for bucket, cfg := range configs {
		if ctx.Err() != nil {
			return
		}
		// A versioned bucket is swept over its history rather than over its
		// current objects: the same actions mean different things there, and
		// the noncurrent ones are not visible from the current-object namespace
		// at all. A bucket record that will not load — including a
		// configuration left behind for a bucket that no longer exists — falls
		// through to the unversioned sweep, which is what a bucket with no
		// versioning status gets anyway.
		b, aerr := h.store.getBucket(ctx, bucket)
		if aerr == nil && b.versioned() {
			if aerr := h.ensureVersionHistory(ctx, b); aerr != nil {
				h.log.Error("lifecycle: prepare version history",
					zap.String("bucket", bucket), zap.Error(aerr))
			} else {
				h.sweepBucketVersions(ctx, b, cfg, now)
			}
		} else {
			h.sweepBucketObjects(ctx, bucket, cfg, now)
		}
		h.sweepBucketUploads(ctx, bucket, cfg, now)
	}
}

// sweepBucketVersions applies one versioned bucket's rules over its whole
// version history.
//
// It pages s3:versions, whose order groups every version of a key together
// newest-first — so the sweep can hand one key's complete history to
// applyLifecycleToVersions while holding only that key plus one page in memory.
// A group that straddles a page boundary is carried forward rather than split.
func (h *Handler) sweepBucketVersions(ctx context.Context, b *Bucket, cfg *LifecycleConfiguration, now time.Time) {
	if !cfg.hasObjectActions() {
		return
	}

	var (
		cursor  string
		group   []*Object
		groupOf string
	)
	for {
		versions, next, aerr := h.store.listVersionsPage(ctx, b.Name, "", cursor, lifecycleSweepPageSize)
		if aerr != nil {
			h.log.Error("lifecycle: list object versions",
				zap.String("bucket", b.Name), zap.Error(aerr))
			return
		}
		for _, v := range versions {
			if ctx.Err() != nil {
				return
			}
			if v.Key != groupOf && len(group) > 0 {
				h.applyLifecycleToVersions(ctx, b, cfg, group, now)
				group = nil
			}
			groupOf = v.Key
			group = append(group, v)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(group) > 0 {
		h.applyLifecycleToVersions(ctx, b, cfg, group, now)
	}
}

// applyLifecycleToVersions applies every version-aware action to one key's
// complete history, newest version first.
//
// Three things happen here, in AWS's own order of precedence:
//
//   - the current version is expired or transitioned. Expiring it in a versioned
//     bucket deletes nothing: S3 "adds a delete marker", which becomes the new
//     current version and pushes the old one into the noncurrent set.
//   - a current version that is a delete marker with nothing under it is removed
//     outright by an ExpiredObjectDeleteMarker rule. That is the whole point of
//     the action — clearing the tombstone left behind once every version it hid
//     has expired away.
//   - each noncurrent version is expired or transitioned on its own clock, which
//     starts when its successor was written rather than when it was.
func (h *Handler) applyLifecycleToVersions(ctx context.Context, b *Bucket, cfg *LifecycleConfiguration, group []*Object, now time.Time) {
	current := group[0]
	noncurrent := group[1:]

	if current.DeleteMarker {
		if len(group) == 1 {
			if rule := cfg.expiresDeleteMarker(current); rule != nil {
				h.expireDeleteMarker(ctx, current, rule)
				return
			}
		}
	} else if at, rule := cfg.expirationFor(current); rule != nil && !at.After(now) {
		// The current version becomes noncurrent as of now, so it is not yet
		// eligible for any noncurrent action on this tick — which is why the
		// loop below still walks the pre-sweep noncurrent set.
		h.expireCurrentVersion(ctx, b, current, rule)
	} else if class, ok := cfg.transitionFor(current, now); ok && class != current.effectiveStorageClass() {
		h.transitionVersion(ctx, current, class, true)
	}

	for i, v := range noncurrent {
		if ctx.Err() != nil {
			return
		}
		// group[i] is v's successor: the version that displaced it, and so the
		// moment v stopped being current.
		noncurrentSince := group[i].LastModified.UTC()
		if at, rule := cfg.noncurrentExpiryFor(v, noncurrentSince, i); rule != nil && !at.After(now) {
			if aerr := h.store.deleteVersion(ctx, v); aerr != nil {
				h.log.Error("lifecycle: expire noncurrent version",
					zap.String("bucket", v.Bucket), zap.String("key", v.Key),
					zap.String("version_id", v.wireVersionID()), zap.Error(aerr))
				continue
			}
			h.log.Trace("lifecycle: noncurrent version expired",
				zap.String("bucket", v.Bucket), zap.String("key", v.Key),
				zap.String("version_id", v.wireVersionID()), zap.String("rule", rule.ID))
			continue
		}
		if v.DeleteMarker {
			// A delete marker has no bytes to move, so no transition applies.
			continue
		}
		if class, ok := cfg.noncurrentTransitionFor(v, noncurrentSince, i, now); ok && class != v.effectiveStorageClass() {
			h.transitionVersion(ctx, v, class, false)
		}
	}
}

// expireCurrentVersion is what an Expiration rule does to a versioned object:
// it adds a delete marker rather than removing anything.
func (h *Handler) expireCurrentVersion(ctx context.Context, b *Bucket, current *Object, rule *LifecycleRule) {
	now := h.clk.Now().UTC()
	marker := &Object{
		Bucket:       current.Bucket,
		Key:          current.Key,
		LastModified: now,
		DeleteMarker: true,
		Seq:          newVersionStamp(now).sortToken(),
	}
	if b.VersioningStatus == versioningEnabled {
		marker.VersionID = marker.Seq
	}

	// In a suspended bucket the marker is the key's null version, so this
	// also drops the null version it replaced.
	if aerr := h.commitMarker(ctx, b, marker); aerr != nil {
		h.log.Error("lifecycle: add expiration delete marker",
			zap.String("bucket", marker.Bucket), zap.String("key", marker.Key), zap.Error(aerr))
		return
	}
	h.log.Trace("lifecycle: current version expired to a delete marker",
		zap.String("bucket", marker.Bucket), zap.String("key", marker.Key),
		zap.String("rule", rule.ID))
}

// expireDeleteMarker removes a delete marker that is hiding nothing, along with
// the key's current-version record — the key is then gone entirely.
func (h *Handler) expireDeleteMarker(ctx context.Context, marker *Object, rule *LifecycleRule) {
	if aerr := h.store.deleteVersion(ctx, marker); aerr != nil {
		h.log.Error("lifecycle: expire delete marker",
			zap.String("bucket", marker.Bucket), zap.String("key", marker.Key), zap.Error(aerr))
		return
	}
	if aerr := h.store.deleteObjectRecord(ctx, marker.Bucket, marker.Key); aerr != nil {
		h.log.Error("lifecycle: clear expired delete marker",
			zap.String("bucket", marker.Bucket), zap.String("key", marker.Key), zap.Error(aerr))
		return
	}
	h.log.Trace("lifecycle: expired object delete marker removed",
		zap.String("bucket", marker.Bucket), zap.String("key", marker.Key),
		zap.String("rule", rule.ID))
}

// transitionVersion records a storage class on one version. isCurrent also
// updates the key's current-version record, which is a denormalised copy of it.
func (h *Handler) transitionVersion(ctx context.Context, v *Object, class string, isCurrent bool) {
	v.StorageClass = class
	if aerr := h.store.putVersion(ctx, v); aerr != nil {
		h.log.Error("lifecycle: transition version",
			zap.String("bucket", v.Bucket), zap.String("key", v.Key),
			zap.String("version_id", v.wireVersionID()), zap.Error(aerr))
		return
	}
	if isCurrent {
		if aerr := h.store.putObjectMeta(ctx, v); aerr != nil {
			h.log.Error("lifecycle: transition current version",
				zap.String("bucket", v.Bucket), zap.String("key", v.Key), zap.Error(aerr))
			return
		}
	}
	h.log.Trace("lifecycle: version transitioned",
		zap.String("bucket", v.Bucket), zap.String("key", v.Key),
		zap.String("version_id", v.wireVersionID()), zap.String("storage_class", class))
}

// sweepBucketObjects expires and transitions the objects of one bucket. It
// pages the bucket rather than listing it whole, so memory stays bounded by
// lifecycleSweepPageSize regardless of how many objects the bucket holds.
func (h *Handler) sweepBucketObjects(ctx context.Context, bucket string, cfg *LifecycleConfiguration, now time.Time) {
	if !cfg.hasObjectActions() {
		return
	}
	startAfter := ""
	for {
		objects, next, aerr := h.store.listObjectsPage(ctx, bucket, "", startAfter, lifecycleSweepPageSize)
		if aerr != nil {
			h.log.Error("lifecycle: list objects",
				zap.String("bucket", bucket), zap.Error(aerr))
			return
		}
		for _, obj := range objects {
			if ctx.Err() != nil {
				return
			}
			h.applyLifecycleToObject(ctx, cfg, obj, now)
		}
		if next == "" {
			return
		}
		startAfter = next
	}
}

// applyLifecycleToObject expires obj if any rule's expiry has passed, and
// otherwise marks it with the storage class of the latest due transition.
//
// Expiration wins: an object past its expiry is deleted rather than
// transitioned, which is also the order AWS applies the two actions in.
func (h *Handler) applyLifecycleToObject(ctx context.Context, cfg *LifecycleConfiguration, obj *Object, now time.Time) {
	if at, rule := cfg.expirationFor(obj); rule != nil && !at.After(now) {
		if aerr := h.store.deleteObject(ctx, obj.Bucket, obj.Key); aerr != nil {
			h.log.Error("lifecycle: expire object",
				zap.String("bucket", obj.Bucket), zap.String("key", obj.Key), zap.Error(aerr))
			return
		}
		// A per-tick sweep outcome on the sweeper's own schedule, not a
		// response to a client request — TRACE per CONTRIBUTING § Log levels.
		h.log.Trace("lifecycle: object expired",
			zap.String("bucket", obj.Bucket), zap.String("key", obj.Key),
			zap.String("rule", rule.ID))
		return
	}

	class, ok := cfg.transitionFor(obj, now)
	if !ok || class == obj.effectiveStorageClass() {
		return
	}
	obj.StorageClass = class
	if aerr := h.store.putObjectMeta(ctx, obj); aerr != nil {
		h.log.Error("lifecycle: transition object",
			zap.String("bucket", obj.Bucket), zap.String("key", obj.Key), zap.Error(aerr))
		return
	}
	h.log.Trace("lifecycle: object transitioned",
		zap.String("bucket", obj.Bucket), zap.String("key", obj.Key),
		zap.String("storage_class", class))
}

// sweepBucketUploads aborts multipart uploads left in progress longer than an
// AbortIncompleteMultipartUpload rule allows.
func (h *Handler) sweepBucketUploads(ctx context.Context, bucket string, cfg *LifecycleConfiguration, now time.Time) {
	if !cfg.hasAbortActions() {
		return
	}
	uploads, aerr := h.store.listMultipartUploads(ctx, bucket)
	if aerr != nil {
		h.log.Error("lifecycle: list multipart uploads",
			zap.String("bucket", bucket), zap.Error(aerr))
		return
	}
	for _, u := range uploads {
		days, ok := cfg.abortIncompleteAfter(u.Key)
		if !ok {
			continue
		}
		if u.Initiated.UTC().AddDate(0, 0, days).After(now) {
			continue
		}
		if delErr := h.store.deleteAllParts(ctx, u.UploadID); delErr != nil {
			h.log.Error("lifecycle: delete parts of abandoned upload",
				zap.String("bucket", bucket), zap.String("upload_id", u.UploadID), zap.Error(delErr))
			continue
		}
		if delErr := h.store.deleteMultipartUpload(ctx, u.UploadID); delErr != nil {
			h.log.Error("lifecycle: abort abandoned upload",
				zap.String("bucket", bucket), zap.String("upload_id", u.UploadID), zap.Error(delErr))
			continue
		}
		h.log.Trace("lifecycle: incomplete multipart upload aborted",
			zap.String("bucket", bucket), zap.String("key", u.Key))
	}
}
