// Package s3 implements the AWS S3 REST API emulator.
//
// S3 uses a REST-style XML API. Each HTTP method+path combination maps to an
// AWS S3 operation. Sub-resource query parameters (e.g. ?acl, ?cors, ?policy)
// further specialise the operation. Each sub-resource routes to its own named
// handler in handler.go, which either implements the operation or returns a
// clear HTTP 501 with x-emulator-unsupported: true.
//
// Implemented:
//
//	GET  /                           → ListBuckets
//	PUT  /{bucket}                   → CreateBucket
//	HEAD /{bucket}                   → HeadBucket
//	DELETE /{bucket}                 → DeleteBucket
//	GET  /{bucket}?location          → GetBucketLocation
//	GET  /{bucket}?list-type=2       → ListObjectsV2
//	PUT  /{bucket}/{key}             → PutObject
//	PUT  /{bucket}/{key} (+copy hdr) → CopyObject
//	GET  /{bucket}/{key}             → GetObject
//	HEAD /{bucket}/{key}             → HeadObject
//	DELETE /{bucket}/{key}           → DeleteObject
//
// All other operations are routed to named stubs that return HTTP 501.
// See docs/services/s3.md for the full support matrix.
package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "s3"

// Service implements router.Service for S3.
type Service struct {
	cfg             *config.Config
	store           state.Store
	log             *serviceutil.ServiceLogger
	handler         *Handler
	lifecycleCancel context.CancelFunc
	// lifecycleDone closes when the sweeper loop has exited, so Stop can drain
	// an in-flight sweep instead of returning while it is still deleting.
	lifecycleDone <-chan struct{}
}

// New returns a configured S3 Service ready to be registered.
// bus is the shared event bus; pass events.NewBus() from the router.
//
// Starting the lifecycle sweeper here costs one goroutine parked on a ticker —
// it reads nothing from the store until its first tick, so New stays free of
// blocking work (AGENTS.md startup budget).
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock, bus *events.Bus) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	svc := &Service{
		cfg:             cfg,
		store:           store,
		log:             log,
		handler:         newHandler(cfg, store, log, clk, bus),
		lifecycleCancel: lifecycleCancel,
	}
	svc.lifecycleDone = svc.handler.startLifecycleSweeper(lifecycleCtx)
	return svc
}

// Name satisfies router.Service.
func (s *Service) Name() string { return serviceName }

// Stop cancels the lifecycle sweeper and waits for it to drain, so shutdown
// does not return while a sweep is still deleting objects and rewriting
// metadata. Satisfies router.Stopper.
//
// ctx is the shutdown deadline: if the sweeper has not finished by the time it
// expires, Stop gives up waiting and says so rather than blocking the process
// from exiting. The sweep loop checks ctx.Err() between buckets and between
// objects, so cancellation normally ends it within one object.
func (s *Service) Stop(ctx context.Context) {
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	if s.lifecycleDone == nil {
		return
	}
	if ctx == nil {
		<-s.lifecycleDone
		return
	}
	select {
	case <-s.lifecycleDone:
	case <-ctx.Done():
		s.log.Warn("lifecycle: sweeper still running at the shutdown deadline")
	}
}

// InitNotifications wires up the S3 event notification dispatcher.
// Call this after constructing the S3, SQS, SNS, Lambda and EventBridge
// services so the router can pass their narrow sink interfaces without
// creating an import cycle between services.
//
// topics is nil only in tests that wire notifications without SNS, invoker and
// auth only in tests that wire them without Lambda, and eventBus only in tests
// that wire them without EventBridge.
func (s *Service) InitNotifications(enqueuer events.MessageEnqueuer, topics events.TopicPublisher, invoker events.FunctionInvoker, auth events.FunctionInvokeAuthorizer, eventBus events.BusPublisher, bus *events.Bus, logger *zap.Logger) {
	// PutBucketNotificationConfiguration validates a Lambda destination before
	// it is stored, so the handler needs the same authorizer the dispatcher
	// consults at delivery time.
	s.handler.lambdaAuth = auth
	NewNotificationDispatcher(s.handler.store, enqueuer, topics, invoker, auth, eventBus, bus, logger, s.cfg.Region, s.cfg.AccountID)
}

// GetObjectBytes returns the full body of an S3 object for internal callers
// such as the Lambda S3-reactive sync watcher and Lambda's deployment-package
// fetch. An empty versionID reads the key's current version; otherwise it reads
// exactly that version, which is what Lambda's Code.S3ObjectVersion names.
// Returns an error if the bucket, key or version does not exist — the same
// errors GetObject answers with over HTTP, so callers can translate one set.
func (s *Service) GetObjectBytes(ctx context.Context, bucket, key, versionID string) ([]byte, *protocol.AWSError) {
	obj, aerr := s.resolveObjectForRead(ctx, bucket, key, versionID)
	if aerr != nil {
		return nil, aerr
	}
	f, aerr := s.handler.store.openBody(obj)
	if aerr != nil {
		return nil, aerr
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, fmt.Errorf("s3: read object %s/%s: %w", bucket, key, err))
	}
	return body, nil
}

// resolveObjectForRead picks the version an internal read addresses and refuses
// the two "nothing to read" cases the way GetObject does over HTTP: a current
// version that is a delete marker reads as absent, and a version id that names
// a delete marker exists but cannot be read.
func (s *Service) resolveObjectForRead(ctx context.Context, bucket, key, versionID string) (*Object, *protocol.AWSError) {
	if versionID == "" {
		obj, aerr := s.handler.store.getObjectMeta(ctx, bucket, key)
		if aerr != nil {
			return nil, aerr
		}
		if obj.DeleteMarker {
			return nil, errNoSuchKey(key)
		}
		return obj, nil
	}
	obj, aerr := s.handler.resolveVersion(ctx, bucket, key, versionID)
	if aerr != nil {
		return nil, aerr
	}
	if obj.DeleteMarker {
		return nil, errMethodNotAllowedOnDeleteMarker()
	}
	return obj, nil
}

// PutObjectBytes stores body at bucket/key for internal callers such as
// Athena's query-result writer. It goes through PutObject's own write path —
// versioning, the MD5 ETag, and the bucket's event notifications (SQS, SNS,
// Lambda, EventBridge) fire exactly as for an HTTP PutObject — and returns
// the errors PutObject would (NoSuchBucket above all). Satisfies
// events.S3PutObjectFunc.
func (s *Service) PutObjectBytes(ctx context.Context, bucket, key string, body []byte, opts events.S3PutObjectOptions) (events.S3PutObjectResult, *protocol.AWSError) {
	h := s.handler
	b, aerr := h.store.getBucket(ctx, bucket)
	if aerr != nil {
		return events.S3PutObjectResult{}, aerr
	}
	// An HTTP PutObject cannot name an empty key — PUT /{bucket}/ is a
	// bucket operation — so this guards a caller bug rather than mirroring
	// an AWS answer.
	if key == "" {
		return events.S3PutObjectResult{}, protocol.ErrInvalidArgument("An object key must not be empty.")
	}

	var meta map[string]string
	if len(opts.Metadata) > 0 {
		// Stored lower-cased, as PutObject stores x-amz-meta-* names.
		meta = make(map[string]string, len(opts.Metadata))
		for name, value := range opts.Metadata {
			meta[strings.ToLower(name)] = value
		}
	}
	obj := &Object{
		Bucket:       bucket,
		Key:          key,
		ContentType:  objectContentType(opts.ContentType),
		LastModified: h.clk.Now().UTC(),
		Metadata:     meta,
	}

	etag, aerr := h.writeObject(ctx, b, obj, bytes.NewReader(body))
	if aerr != nil {
		return events.S3PutObjectResult{}, aerr
	}
	return events.S3PutObjectResult{ETag: etag, VersionID: obj.headerVersionID()}, nil
}

// ListObjects returns one page of the keys under prefix for internal callers,
// through ListObjectsV2's own paging (no delimiter), so the keys, the
// continuation tokens and the errors — NoSuchBucket, InvalidArgument for a
// garbled token — are the ones a client sees. maxKeys <= 0 means
// ListObjectsV2's default page size, and larger values are capped at it, as
// the HTTP operation caps max-keys. Satisfies events.S3ListObjectsFunc.
func (s *Service) ListObjects(ctx context.Context, bucket, prefix, continuationToken string, maxKeys int) (events.S3ObjectListPage, *protocol.AWSError) {
	if maxKeys <= 0 || maxKeys > maxListPageSize {
		maxKeys = maxListPageSize
	}
	entries, next, aerr := s.handler.listObjectsV2Page(ctx, bucket, prefix, "", continuationToken, "", maxKeys)
	if aerr != nil {
		return events.S3ObjectListPage{}, aerr
	}
	page := events.S3ObjectListPage{
		Objects:               make([]events.S3ObjectSummary, 0, len(entries)),
		NextContinuationToken: next,
	}
	// With no delimiter every entry is an object, never a common prefix.
	for _, e := range entries {
		page.Objects = append(page.Objects, events.S3ObjectSummary{
			Key:          e.obj.Key,
			Size:         e.obj.ContentLength,
			ETag:         e.obj.ETag,
			LastModified: e.obj.LastModified,
		})
	}
	return page, nil
}

// EnsureBucket creates bucket in region (the configured region when empty)
// unless it already exists, in which case it succeeds and leaves the bucket as
// it is — the idempotent create an internal caller wants, in every region,
// where CreateBucket answers BucketAlreadyOwnedByYou outside us-east-1. The
// name is held to CreateBucket's global-namespace rules and refused with its
// InvalidBucketName. Satisfies events.S3EnsureBucketFunc.
func (s *Service) EnsureBucket(ctx context.Context, bucket, region string) *protocol.AWSError {
	h := s.handler
	if region == "" {
		region = s.cfg.Region
	}
	if aerr := h.validateNewBucketName(bucket, bucketNamespaceGlobal, region); aerr != nil {
		return aerr
	}
	_, aerr := h.createBucket(ctx, &Bucket{Name: bucket, Region: region})
	return aerr
}

// RegisterRoutes mounts all S3 endpoints onto the given router.
// Route order matters in chi — more specific routes must come before wildcards.
// Every route delegates to a named dispatcher or handler; there are no inline
// protocol.NotImplementedXML calls here.
func (s *Service) RegisterRoutes(r chi.Router) {
	h := s.handler

	// Root-level: ListBuckets, ListDirectoryBuckets
	r.Get("/", h.RootGet)

	// Bucket-level — all methods dispatched through named dispatchers so each
	// sub-resource (e.g. ?acl, ?cors) calls its own handler in handler.go.
	// Both with and without trailing slash: the AWS SDK sends PUT /bucket/ for
	// CreateBucket and some other bucket operations.
	r.Get("/{bucket}", h.BucketGet)
	r.Get("/{bucket}/", h.BucketGet)
	r.Put("/{bucket}", h.BucketPut)
	r.Put("/{bucket}/", h.BucketPut)
	r.Head("/{bucket}", h.HeadBucket)
	r.Head("/{bucket}/", h.HeadBucket)
	r.Delete("/{bucket}", h.BucketDelete)
	r.Delete("/{bucket}/", h.BucketDelete)
	r.Post("/{bucket}", h.BucketPost)
	r.Post("/{bucket}/", h.BucketPost)

	// Object-level — /* wildcard matches keys with slashes (e.g. logs/2024/jan.log).
	r.Get("/{bucket}/*", h.ObjectGet)
	r.Put("/{bucket}/*", h.PutObjectOrCopy)
	r.Head("/{bucket}/*", h.HeadObject)
	r.Delete("/{bucket}/*", h.ObjectDelete)
	r.Post("/{bucket}/*", h.ObjectPost)
}
