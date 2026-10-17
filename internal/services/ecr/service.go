// Package ecr provides emulation of Amazon Elastic Container Registry (ECR).
//
// Implemented operations: CreateRepository, DescribeRepositories,
// DeleteRepository, GetAuthorizationToken, DescribeRegistry, ListImages, PutImage,
// BatchGetImage, BatchDeleteImage, SetRepositoryPolicy, GetRepositoryPolicy,
// DeleteRepositoryPolicy, PutLifecyclePolicy, GetLifecyclePolicy,
// DeleteLifecyclePolicy, PutImageTagMutability, PutImageScanningConfiguration,
// TagResource, UntagResource, ListTagsForResource.
//
// The control-plane operations (repository CRUD, image metadata, tags, policies)
// are fully implemented in-memory. When Docker is available, ECR also lazy-starts
// one shared local registry:2 container per process and returns matching auth
// credentials via GetAuthorizationToken.
package ecr

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
	"golang.org/x/crypto/bcrypt"
)

const serviceName = "ecr"

const (
	// ecrRegistryNamePrefix is the base of every registry container name. The
	// full name is per-claim — "overcast-ecr-registry-4510" for a fixed port,
	// "overcast-ecr-registry-<random>" for an ephemeral one — because the name
	// is the unit of contention on a shared daemon. A singleton name meant any
	// two Overcast processes (or two test servers in parallel test packages)
	// each removed the other's registry on startup, killing pushes mid-flight
	// with connection resets. A fixed port is an exclusive slot, so replacing
	// whatever holds its name is legitimate predecessor reaping; an ephemeral
	// registry contends with nobody and gets a name nobody else will claim.
	ecrRegistryNamePrefix = "overcast-ecr-registry"
	// ecrRegistryResource is the LabelResourceID carried by every registry
	// container, the stable way to recognise one regardless of its name.
	ecrRegistryResource = "registry"
	ecrRegistryImage    = "registry:2"
	ecrRegistryPortKey  = "5000/tcp"
	// ecrRegistryHTPasswdPath is where the registry container reads its
	// credentials from; the file is copied in at create time.
	ecrRegistryHTPasswdPath = "/auth/htpasswd"
	// ecrRegistryStoragePath is where registry:2 keeps its blobs. The image
	// declares it a VOLUME, so Docker mounts *something* there either way; the
	// only question is whether it is an anonymous volume that AutoRemove reaps
	// with the container or a named one that outlives it.
	ecrRegistryStoragePath = "/var/lib/registry"
	// registryCreateAttempts bounds how long createRegistryContainer waits for
	// a predecessor's name to be released — 5s at the retry interval below.
	registryCreateAttempts = 20
	registryCreateBackoff  = 250 * time.Millisecond
	// registryAnswerTimeout bounds how long a started registry container gets
	// to begin answering HTTP before startup gives up on it. registry:2 boots
	// in a second or two; the bound exists for wedged containers and broken
	// daemon topologies, and it is on the first waitRegistryReady caller's
	// critical path, so it is generous without being an outage of its own.
	registryAnswerTimeout = 15 * time.Second
	registryAnswerBackoff = 200 * time.Millisecond
	// registryProbeHost is the host the startup probe asks the daemon to dial,
	// and — when the daemon answers there — the host repositoryUri advertises.
	// One constant for both, because a probe that proves one address while the
	// advertisement names another proves nothing. See adoptRegistryAddress.
	registryProbeHost = "localhost"
)

// ─── Store namespaces ─────────────────────────────────────────────────────────

const (
	repoNamespace   = "ecr:repositories" // key: region/name
	imageNamespace  = "ecr:images"       // key: region/repoName/digest
	tagNamespace    = "ecr:tags"         // key: region/arn → JSON tag list
	policyNamespace = "ecr:policies"     // key: region/name → policy text
	lifecycleNS     = "ecr:lifecycle"    // key: region/name → policy text
)

// ─── Types ────────────────────────────────────────────────────────────────────

// Repository represents an ECR repository.
type Repository struct {
	RepositoryArn              string                     `json:"repositoryArn"`
	RegistryId                 string                     `json:"registryId"`
	RepositoryName             string                     `json:"repositoryName"`
	RepositoryUri              string                     `json:"repositoryUri"`
	CreatedAt                  float64                    `json:"createdAt"`
	ImageTagMutability         string                     `json:"imageTagMutability"`
	ImageScanningConfiguration ImageScanningConfiguration `json:"imageScanningConfiguration"`
	EncryptionConfiguration    EncryptionConfiguration    `json:"encryptionConfiguration"`
}

// ImageScanningConfiguration is the scan-on-push toggle CreateRepository
// accepts and DescribeRepositories echoes back. Overcast stores and returns
// the value; no scan engine runs (see DescribeImageScanFindings), so setting
// it true does not make PutImage trigger a scan.
type ImageScanningConfiguration struct {
	ScanOnPush bool `json:"scanOnPush"`
}

// EncryptionConfiguration is the at-rest encryption choice CreateRepository
// accepts and DescribeRepositories echoes back. Real ECR always encrypts
// storage — AES256 (the default) or a customer KMS key — and Overcast does
// not model either, so the value round-trips without changing how images are
// stored. Real AWS treats this property as fixed for the life of the
// repository (CreateRepository only, no Put* to change it); Overcast matches
// that by requiring replacement when a template changes it — see
// ecrRepositoryHandler.Update.
type EncryptionConfiguration struct {
	EncryptionType string `json:"encryptionType"`
	KmsKey         string `json:"kmsKey,omitempty"`
}

// ImageIdentifier uniquely identifies an image by tag or digest.
type ImageIdentifier struct {
	ImageDigest string `json:"imageDigest,omitempty"`
	ImageTag    string `json:"imageTag,omitempty"`
}

// Image represents an ECR image record.
type Image struct {
	RegistryId             string          `json:"registryId"`
	RepositoryName         string          `json:"repositoryName"`
	ImageId                ImageIdentifier `json:"imageId"`
	ImageManifest          string          `json:"imageManifest"`
	ImageManifestMediaType string          `json:"imageManifestMediaType,omitempty"`
}

// imageRecord is an Image as it is persisted: the image itself, plus how
// Overcast came to know about it. The extra field is stored, never served —
// responses are built from Image, and an unknown key is ignored when a record
// is read back as one.
//
// Provenance is what makes the two sources reconcilable. An image observed in
// the registry is the registry's to withdraw: when the manifest goes, so does
// the record. An image registered through PutImage was never in the registry
// to begin with, so the registry's silence about it says nothing.
type imageRecord struct {
	Image
	FromRegistry bool `json:"overcastFromRegistry,omitempty"`
}

// Tag is a key/value tag.
type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// ─── Service ──────────────────────────────────────────────────────────────────

// Service implements the ECR emulator.
type Service struct {
	cfg     *config.Config
	store   state.Store
	log     *serviceutil.ServiceLogger
	clk     clock.Clock
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
	bus     *events.Bus

	docker            *docker.Client
	puller            *docker.ImagePuller
	registryMu        sync.Mutex
	registryContainer string
	registryName      string
	// registryHost and registryHostPort are the address every client-facing
	// registry address is built from. They are written together, by
	// adoptRegistryAddress and nowhere else — a port without its host mints a
	// hostless ":4510/…", which is not an address at all.
	registryHost     string
	registryHostPort int
	// registryProven is true only once selectDaemonReachablePort watched the
	// daemon itself answer at registryHost:registryHostPort with this
	// instance's credentials. A hostPort can be set (adoptRegistryAddress
	// always sets one) while this stays false — the container started but
	// nothing showed it was actually reachable. registryLimitation reads it to
	// decide whether repositoryUri names a registry that was demonstrated to
	// work or one merely believed to.
	registryProven   bool
	registryPassword string
	registryInitOnce sync.Once
	// registrylessOnce keeps the "there is no registry" warning to one line per
	// process: it is minted on every repository read, which the console polls.
	registrylessOnce sync.Once
	registryReady    chan struct{}
	registryStopping bool
	// probeTimeout overrides registryAnswerTimeout for the port sweep. Set only
	// by tests, which would otherwise wait out the real deadline to reach the
	// nothing-answered path.
	probeTimeout time.Duration
	// registryClientBases are candidate base URLs at which this process might
	// reach the registry over HTTP; registryClientBase is the probed winner.
	// Distinct from the daemon-facing address in repositoryUri: which of them
	// answers depends on where Overcast itself runs. See registryBaseURL.
	registryClientBases []string
	registryClientBase  string
}

// InitBus wires the event bus for ECR lifecycle events.
func (s *Service) InitBus(bus *events.Bus) {
	s.bus = bus
	bus.Subscribe(events.DockerContainerDied, s.handleRegistryContainerDied)
}

// handleRegistryContainerDied invalidates the cached ready address and starts
// a replacement. Amazon ECR is managed; callers must not keep receiving the
// port of an AutoRemove registry container that no longer exists.
func (s *Service) handleRegistryContainerDied(_ context.Context, e events.Event) {
	p, ok := e.Payload.(events.DockerContainerPayload)
	if !ok || p.Service != serviceName {
		return
	}
	s.registryMu.Lock()
	if s.registryStopping || s.registryContainer == "" || p.ContainerID != s.registryContainer {
		s.registryMu.Unlock()
		return
	}
	s.registryContainer = ""
	s.registryName = ""
	s.registryHost = ""
	s.registryHostPort = 0
	s.registryProven = false
	s.registryClientBases = nil
	s.registryClientBase = ""
	s.registryReady = nil
	s.registryInitOnce = sync.Once{}
	s.registryMu.Unlock()

	if err := s.ensureRegistry(context.Background()); err != nil {
		s.log.Warn("failed to restart ECR registry after its container exited", zap.Error(err))
	}
}

// ReconcileContainers invalidates a cached registry address when its container
// disappeared while the watcher was disconnected. A fresh process has no
// cached address, so startup remains lazy; reconnect heals an already-used ECR.
func (s *Service) ReconcileContainers(_ context.Context, containers []docker.ContainerSummary) {
	s.registryMu.Lock()
	containerID := s.registryContainer
	wasReady := containerID != "" && s.registryHostPort > 0
	s.registryMu.Unlock()
	if !wasReady {
		return
	}
	for i := range containers {
		if containers[i].ID == containerID && strings.EqualFold(containers[i].State, "running") {
			return
		}
	}
	s.handleRegistryContainerDied(context.Background(), events.Event{
		Type: events.DockerContainerDied,
		Payload: events.DockerContainerPayload{
			ContainerID: containerID,
			Service:     serviceName,
			ResourceID:  ecrRegistryResource,
			Action:      "reconcile",
		},
	})
}

// publish emits an event if the bus is wired.
func (s *Service) publish(ctx context.Context, t events.Type, payload any) {
	if s.bus != nil {
		s.bus.Publish(ctx, events.Event{Type: t, Payload: payload})
	}
}

// New returns a configured ECR Service.
func New(cfg *config.Config, st state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	s := &Service{
		cfg:   cfg,
		store: st,
		log:   serviceutil.NewServiceLogger(logger, serviceName),
		clk:   clk,
	}
	if cfg != nil && cfg.LambdaDockerSocket != "" {
		s.docker = docker.NewClient(cfg.LambdaDockerSocket, logger)
		s.puller = docker.NewImagePuller(s.docker)
	}
	s.ops = map[string]http.HandlerFunc{
		"CreateRepository":              s.createRepository,
		"DescribeRepositories":          s.describeRepositories,
		"DeleteRepository":              s.deleteRepository,
		"GetAuthorizationToken":         s.getAuthorizationToken,
		"DescribeRegistry":              s.describeRegistry,
		"ListImages":                    s.listImages,
		"DescribeImages":                s.describeImages,
		"PutImage":                      s.putImage,
		"BatchGetImage":                 s.batchGetImage,
		"DescribeImageScanFindings":     s.describeImageScanFindings,
		"BatchDeleteImage":              s.batchDeleteImage,
		"PutImageTagMutability":         s.putImageTagMutability,
		"PutImageScanningConfiguration": s.putImageScanningConfiguration,
		"SetRepositoryPolicy":           s.setRepositoryPolicy,
		"GetRepositoryPolicy":           s.getRepositoryPolicy,
		"DeleteRepositoryPolicy":        s.deleteRepositoryPolicy,
		"PutLifecyclePolicy":            s.putLifecyclePolicy,
		"GetLifecyclePolicy":            s.getLifecyclePolicy,
		"DeleteLifecyclePolicy":         s.deleteLifecyclePolicy,
		"TagResource":                   s.tagResource,
		"UntagResource":                 s.untagResource,
		"ListTagsForResource":           s.listTagsForResource,
	}
	s.typedOp = s.typedOps()
	return s
}

func (s *Service) Name() string                { return serviceName }
func (s *Service) RegisterRoutes(_ chi.Router) {}
func (s *Service) TargetPrefix() string        { return "AmazonEC2ContainerRegistry_V20150921." }

func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code:       "UnsupportedProtocol",
				Message:    "ECR does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		if c.Name() != codec.NameRPCv2CBOR {
			s.dispatchLegacy(w, r, opName)
			return
		}
		if typed, ok := s.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		c.WriteError(w, r, protocol.ErrNotImplemented)
		return
	}

	target := r.Header.Get("X-Amz-Target")
	opName := target
	if idx := strings.LastIndex(target, "."); idx >= 0 {
		opName = target[idx+1:]
	}
	s.dispatchLegacy(w, r, opName)
}

func (s *Service) dispatchLegacy(w http.ResponseWriter, r *http.Request, opName string) {
	if fn, ok := s.ops[opName]; ok {
		fn(w, r)
		return
	}
	protocol.NotImplementedJSON(w, r)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (s *Service) region(r *http.Request) string {
	return middleware.RegionFromContext(r.Context(), s.cfg.Region)
}

// registryEndpoint is the address every client-facing ECR address is built
// from: repositoryUri, and proxyEndpoint from GetAuthorizationToken.
//
// With no registry there is nothing to name but Overcast's own API base, and
// AWS's API has no way to answer "there is nowhere to push" — so the fallback
// stands, and says so once in the log. It is not a registry: a `docker push`
// at the API port reaches the router, falls through to S3 (the final fallback
// for an unclaimed path) and comes back as `405 Method Not Allowed` from a
// service that was asked for the OCI Distribution API, which Overcast does not
// speak on this port and never will — the registry container does, on its own.
func (s *Service) registryEndpoint() string {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	if s.registryHostPort > 0 {
		return fmt.Sprintf("http://%s:%d", s.registryHost, s.registryHostPort)
	}
	s.registrylessOnce.Do(func() {
		s.log.Warn("no ECR registry is running, so repositoryUri and proxyEndpoint name Overcast's API port; "+
			"`docker push` there fails with 405 Method Not Allowed, and an ECS task or Lambda function built from a "+
			"container asset cannot start. Overcast needs a reachable Docker daemon to run one — check the registry "+
			"startup warnings above if it has one.",
			zap.String("advertised", s.cfg.ExternalBaseURL()))
	})
	return s.cfg.ExternalBaseURL()
}

func (s *Service) accountID() string {
	if s.cfg != nil && strings.TrimSpace(s.cfg.AccountID) != "" {
		return s.cfg.AccountID
	}
	return "000000000000"
}

func (s *Service) repoURI(region, name string) string {
	base := s.registryEndpoint()
	// Trim scheme for the registry hostname part.
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	return fmt.Sprintf("%s/%s/%s", host, s.accountID(), name)
}

func (s *Service) repoARN(region, name string) string {
	return fmt.Sprintf("arn:aws:ecr:%s:%s:repository/%s", region, s.accountID(), name)
}

func (s *Service) getRepo(ctx context.Context, region, name string) (*Repository, bool, error) {
	key := serviceutil.RegionKey(region, name)
	raw, found, err := s.store.Get(ctx, repoNamespace, key)
	if err != nil || !found {
		return nil, found, err
	}
	var repo Repository
	if err := json.Unmarshal([]byte(raw), &repo); err != nil {
		return nil, false, err
	}
	return &repo, true, nil
}

// applyCurrentRepoURI sets repo.RepositoryUri to the address of the registry
// this process is serving. Every path that hands a Repository to a client calls
// it; the stored value is a starting point, not the answer.
//
// The address is not a property of the repository. It is the registry
// container's published port — the fixed one when it is free, an ephemeral one
// when it is not, and, with no Docker at all, no registry and nothing to name
// but Overcast's own API base. Repositories are persisted; the registry is not.
// So the value written at CreateRepository is a fact about the run that created
// it, and serving it later is serving an address that may no longer be anything.
//
// `cdk bootstrap` creates the container-asset repository once and every deploy
// afterwards reads it back to decide where to push, which is how a bootstrap
// that ran before the registry was up sent pushes at the API port for good:
//
//	unexpected status from POST request to
//	http://…:4566/v2/…/blobs/uploads/: 405 Method Not Allowed
//
// while the pull side, resolving through the live registry rather than the
// record, was reaching :4510 in the same deploy.
//
// Nothing is written back. A read that repaired the record would race
// DeleteRepository and restore what it had just removed, and the record is not
// what any client reads: the console and every SDK go through this API. The
// store's copy is therefore left as the historical value it always was.
func (s *Service) applyCurrentRepoURI(ctx context.Context, region string, repo *Repository) {
	// The registry starts lazily, so a call that lands before it is up would
	// otherwise mint the fallback address and hand out the API port.
	_ = s.waitRegistryReady(ctx)
	repo.RepositoryUri = s.repoURI(region, repo.RepositoryName)
}

func (s *Service) saveRepo(ctx context.Context, region string, repo *Repository) error {
	raw, err := json.Marshal(repo)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, repoNamespace, serviceutil.RegionKey(region, repo.RepositoryName), string(raw))
}

func (s *Service) errRepoNotFound(w http.ResponseWriter, r *http.Request, name string) {
	protocol.WriteJSONError(w, r, &protocol.AWSError{
		Code:       "RepositoryNotFoundException",
		Message:    fmt.Sprintf("The repository with name '%s' does not exist in the registry with id '%s'", name, s.accountID()),
		HTTPStatus: http.StatusBadRequest,
	})
}

// errRepoNotEmpty is DeleteRepository's answer to a repository that still
// holds images when force was not set.
//
// Per the ECR API Reference (DeleteRepository § Errors): "The specified
// repository contains images. To delete a repository that contains images,
// you must force the deletion with the force parameter." It is the reason
// force exists. Without the guard the call succeeds and takes the images with
// it, and there is no other signal a caller could read: DeleteRepository
// answers 200 with the repository either way, so a teardown that meant to be
// stopped is instead told it did the right thing.
func (s *Service) errRepoNotEmpty(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code: "RepositoryNotEmptyException",
		Message: fmt.Sprintf(
			"The repository with name '%s' in registry with id '%s' cannot be deleted because it still contains images",
			name, s.accountID()),
		HTTPStatus: http.StatusBadRequest,
	}
}

// repoHoldsImages reports whether a repository still has images in it, for the
// benefit of an unforced DeleteRepository.
//
// The registry is swept first, for the same reason ListImages sweeps: an image
// that arrived by `docker push` is known to the registry container before it
// is known here, and that is the case the guard exists for — a CDK container
// asset pushed by one deploy and a `cdk destroy` that must not silently
// discard it. The sweep is a no-op without Docker and changes nothing when the
// registry cannot be reached, so an unreachable registry falls back to what
// the store knows rather than refusing every delete.
func (s *Service) repoHoldsImages(ctx context.Context, region, name string) (bool, error) {
	// The outcome is deliberately ignored here, where DescribeImages acts on
	// it: the paragraph above is the reason. Refusing to delete whenever the
	// registry is unreachable would be the stricter reading and the wrong one.
	_ = s.syncRepoImagesFromRegistry(ctx, region, name)
	keys, err := s.store.List(ctx, imageNamespace, serviceutil.RegionKey(region, name+"/"))
	if err != nil {
		return false, err
	}
	return len(keys) > 0, nil
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (s *Service) createRepository(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName             string                     `json:"repositoryName"`
		ImageTagMutability         string                     `json:"imageTagMutability"`
		ImageScanningConfiguration ImageScanningConfiguration `json:"imageScanningConfiguration"`
		EncryptionConfiguration    EncryptionConfiguration    `json:"encryptionConfiguration"`
		Tags                       []Tag                      `json:"tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RepositoryName) == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "repositoryName is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	region := s.region(r)
	existing, found, err := s.getRepo(r.Context(), region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "RepositoryAlreadyExistsException",
			Message:    fmt.Sprintf("The repository with name '%s' already exists in the registry with id '%s'", req.RepositoryName, s.accountID()),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	_ = existing

	// See createRepositoryTyped (typed_logic.go) — this JSON1.1 path
	// duplicates it rather than delegating, so the tag validation added for
	// #1052 has to be kept in lockstep here too.
	if aerr := serviceutil.ValidateTags(ecrTagCfg, ecrTagsToMap(req.Tags)); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	mutability := req.ImageTagMutability
	if mutability == "" {
		mutability = "MUTABLE"
	}
	encryption := req.EncryptionConfiguration
	if encryption.EncryptionType == "" {
		encryption.EncryptionType = "AES256"
	}
	repo := &Repository{
		RepositoryArn:              s.repoARN(region, req.RepositoryName),
		RegistryId:                 s.accountID(),
		RepositoryName:             req.RepositoryName,
		CreatedAt:                  float64(s.clk.Now().Unix()),
		ImageTagMutability:         mutability,
		ImageScanningConfiguration: req.ImageScanningConfiguration,
		EncryptionConfiguration:    encryption,
	}
	s.applyCurrentRepoURI(r.Context(), region, repo)
	if err := s.saveRepo(r.Context(), region, repo); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	// Save initial tags if any.
	if len(req.Tags) > 0 {
		_ = s.saveTags(r.Context(), repo.RepositoryArn, req.Tags)
	}

	s.publish(r.Context(), events.ECRRepositoryCreated, events.ResourcePayload{Name: req.RepositoryName, ARN: repo.RepositoryArn})
	protocol.MarkLimitation(w, s.registryLimitation())
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"repository": repo})
}

func (s *Service) describeRepositories(w http.ResponseWriter, r *http.Request) {
	var req describeRepositoriesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.describeRepositoriesTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) deleteRepository(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		RegistryId     string `json:"registryId"`
		Force          bool   `json:"force"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	repo, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}
	if !req.Force {
		hasImages, err := s.repoHoldsImages(ctx, region, req.RepositoryName)
		if err != nil {
			protocol.WriteJSONError(w, r, protocol.ErrInternalError)
			return
		}
		if hasImages {
			protocol.WriteJSONError(w, r, s.errRepoNotEmpty(req.RepositoryName))
			return
		}
	}
	s.applyCurrentRepoURI(ctx, region, repo)

	key := serviceutil.RegionKey(region, req.RepositoryName)
	if err := s.store.Delete(ctx, repoNamespace, key); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	// Clean up images and policies.
	imgKeys, _ := s.store.List(ctx, imageNamespace, serviceutil.RegionKey(region, req.RepositoryName+"/"))
	for _, k := range imgKeys {
		_ = s.store.Delete(ctx, imageNamespace, k)
	}
	_ = s.store.Delete(ctx, policyNamespace, key)
	_ = s.store.Delete(ctx, lifecycleNS, key)

	s.publish(r.Context(), events.ECRRepositoryDeleted, events.ResourcePayload{Name: req.RepositoryName, ARN: repo.RepositoryArn})
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"repository": repo})
}

func (s *Service) getAuthorizationToken(w http.ResponseWriter, r *http.Request) {
	_ = s.ensureRegistry(r.Context())
	_ = s.waitRegistryReady(r.Context())

	password := "test"
	s.registryMu.Lock()
	if s.registryPassword != "" {
		password = s.registryPassword
	}
	s.registryMu.Unlock()

	// ECR token format is base64("AWS:<password>").
	token := base64.StdEncoding.EncodeToString([]byte("AWS:" + password))
	base := s.registryEndpoint()
	expires := s.clk.Now().Add(12 * time.Hour).Unix()
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"authorizationData": []map[string]any{{
			"authorizationToken": token,
			"proxyEndpoint":      base,
			"expiresAt":          float64(expires),
		}},
	})
}

func (s *Service) describeRegistry(w http.ResponseWriter, r *http.Request) {
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId": s.accountID(),
		"replicationConfiguration": map[string]any{
			"rules": []any{},
		},
	})
}

// imageKey returns the store key for an image.
func imageKey(region, repoName, digest string) string {
	return serviceutil.RegionKey(region, repoName+"/"+digest)
}

// selectImages resolves the requested identifiers against a repository's
// images, returning the matches in request order and the first identifier that
// matched nothing. An empty request selects everything: nothing was asked for
// by name, so nothing can be missing.
func selectImages(images []Image, wanted []ImageIdentifier) ([]Image, *ImageIdentifier) {
	if len(wanted) == 0 {
		return images, nil
	}
	selected := make([]Image, 0, len(wanted))
	for _, want := range wanted {
		found := false
		for _, img := range images {
			if want.ImageDigest != "" && img.ImageId.ImageDigest == want.ImageDigest {
				selected = append(selected, img)
				found = true
				break
			}
			if want.ImageTag != "" && img.ImageId.ImageTag == want.ImageTag {
				selected = append(selected, img)
				found = true
				break
			}
		}
		if !found {
			return nil, &want
		}
	}
	return selected, nil
}

// errImageNotFound is DescribeImages' answer to an identifier it cannot
// resolve. It is an error rather than an omission from the list because
// DescribeImages has no per-image failure channel — unlike BatchGetImage — so a
// short list is indistinguishable from a complete one, and every client that
// asks "is this image published?" reads a non-error as yes. cdk-assets is the
// one that matters here: it calls DescribeImages for the asset's tag and
// pushes only if the call throws ImageNotFoundException, so a 200 with no
// details silently skips the push and the ECS task that runs the asset dies
// with a 404 from the registry.
//
// Per the AWS ECR API Reference (DescribeImages § Errors): "The image requested
// does not exist in the specified repository." Message shape as real ECR
// renders it, absent fields as 'null':
//
//	The image with imageId {imageDigest:'null', imageTag:'latest'} does not
//	exist within the repository with name 'app' in the registry with id '…'
//
// errRegistryUnavailable is what a read path answers when the backing registry
// could not be consulted, so an image's absence from the store proves nothing.
//
// ServerException is ECR's own code for a server-side failure, and the shape a
// caller already handles: the AWS SDKs classify it retryable, where
// ImageNotFoundException is a definitive answer they act on. cdk-assets acting
// on the wrong one is a rebuild and a re-push per asset.
func (s *Service) errRegistryUnavailable(repoName string) *protocol.AWSError {
	return &protocol.AWSError{
		Code: "ServerException",
		Message: fmt.Sprintf(
			"The image registry backing repository '%s' could not be reached, so its contents are unknown. Retry the request.",
			repoName),
		HTTPStatus: http.StatusInternalServerError,
	}
}

func (s *Service) errImageNotFound(repoName string, id ImageIdentifier) *protocol.AWSError {
	quoted := func(v string) string {
		if v == "" {
			return "null"
		}
		return "'" + v + "'"
	}
	return &protocol.AWSError{
		Code: "ImageNotFoundException",
		Message: fmt.Sprintf(
			"The image with imageId {imageDigest:%s, imageTag:%s} does not exist within the repository with name '%s' in the registry with id '%s'",
			quoted(id.ImageDigest), quoted(id.ImageTag), repoName, s.accountID()),
		HTTPStatus: http.StatusBadRequest,
	}
}

func digestForManifest(manifest string) string {
	sum := sha256.Sum256([]byte(manifest))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// registrySweep is what a sweep established, so a caller can tell an empty
// repository from a question that was never answered.
//
// repoRegistryState already draws this line for the delete path — "only the
// first is grounds for deleting anything". This carries the same distinction
// out to the read paths, which used to see one `error` that was always nil and
// so could not tell an outage from an empty registry. Answering
// ImageNotFoundException off an unanswered sweep is wrong twice over: real ECR
// reports a server-side failure as ServerException, and cdk-assets reads a
// missing image as "publish this again", so an unreachable registry silently
// costs a Docker build and a layer upload per asset.
type registrySweep int

const (
	// sweepNotApplicable: no registry is wired, so the store is the only
	// authority there has ever been and an empty answer is the truth. This is
	// every no-Docker deployment, and must never read as an outage.
	sweepNotApplicable registrySweep = iota
	// sweepSynced: the registry answered, and the store now reflects it.
	sweepSynced
	// sweepUnavailable: a registry is wired but could not be consulted, so the
	// store is whatever the last successful sweep left and an absence proves
	// nothing.
	sweepUnavailable
)

func (s *Service) syncRepoImagesFromRegistry(ctx context.Context, region, repoName string) registrySweep {
	if s.docker == nil {
		return sweepNotApplicable
	}
	// Each give-up below says which one it took. They used to be silent, which
	// is why a CI failure here left no trace of the registry at all — see the
	// investigation on issue #1444.
	if err := s.ensureRegistry(ctx); err != nil {
		s.log.Debug("ecr: registry sweep skipped, the registry could not be started",
			zap.String("repository", repoName), zap.Error(err))
		return sweepUnavailable
	}

	s.registryMu.Lock()
	hostPort := s.registryHostPort
	password := s.registryPassword
	s.registryMu.Unlock()
	if hostPort <= 0 || strings.TrimSpace(password) == "" {
		s.log.Debug("ecr: registry sweep skipped, no registry address or credential yet",
			zap.String("repository", repoName), zap.Int("hostPort", hostPort))
		return sweepUnavailable
	}
	base := s.registryBaseURL(ctx, password)
	if base == "" {
		s.log.Debug("ecr: registry sweep skipped, no reachable registry base URL",
			zap.String("repository", repoName))
		return sweepUnavailable
	}

	client := &http.Client{Timeout: 5 * time.Second}
	repoPath := fmt.Sprintf("%s/%s", s.accountID(), repoName)

	tags, state := s.registryTags(ctx, client, base, repoPath, password)
	if state == repoUnknown {
		// waitRegistryReady only proves the registry answers /v2/ before letting
		// a caller past it; it says nothing about a repository-scoped request
		// like this one, which can still stumble the instant the container's
		// listener starts accepting connections — the gap is the same one
		// awaitRegistryAnswering's own comment names for container start versus
		// process listening, one layer further in. One retry, after a short
		// pause, tells a registry that was merely mid-startup apart from one
		// that is truly unreachable, without widening the per-request timeout
		// that bounds the latter. See the investigation on issue #1444.
		select {
		case <-ctx.Done():
		case <-time.After(registryAnswerBackoff):
		}
		tags, state = s.registryTags(ctx, client, base, repoPath, password)
	}
	if state == repoUnknown {
		s.log.Debug("ecr: registry sweep inconclusive, the registry did not answer for this repository after a retry",
			zap.String("repository", repoName))
		return sweepUnavailable
	}
	if state == repoAbsent {
		// The registry has no repository under this name, so it holds no
		// manifest under it either — including by digest. Every record this
		// sweep created is stale, and none of them needs to be asked about
		// individually. This is the restart case, and the whole point of
		// answering it in one request rather than one per image.
		s.forgetAllRegistryImages(ctx, region, repoName)
		return sweepSynced
	}

	present := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", base, repoPath, tag)
		manifestReq, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
		if err != nil {
			continue
		}
		manifestReq.SetBasicAuth(ecrRegistryUser, password)
		manifestReq.Header.Set("Accept", strings.Join(registryManifestTypes, ", "))
		manifestResp, err := client.Do(manifestReq)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(manifestResp.Body)
		manifestResp.Body.Close()
		if readErr != nil || manifestResp.StatusCode < 200 || manifestResp.StatusCode >= 300 {
			continue
		}
		manifest := string(body)
		digest := strings.TrimSpace(manifestResp.Header.Get("Docker-Content-Digest"))
		if digest == "" {
			digest = digestForManifest(manifest)
		}
		present[digest] = true
		rec := imageRecord{
			Image: Image{
				RegistryId:             s.accountID(),
				RepositoryName:         repoName,
				ImageManifest:          manifest,
				ImageManifestMediaType: manifestResp.Header.Get("Content-Type"),
				ImageId: ImageIdentifier{
					ImageDigest: digest,
					ImageTag:    tag,
				},
			},
			FromRegistry: true,
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		if err := s.store.Set(ctx, imageNamespace, imageKey(region, repoName, digest), string(raw)); err != nil {
			continue
		}
	}

	s.forgetVanishedImages(ctx, client, base, repoPath, password, region, repoName, present)
	return sweepSynced
}

// registryManifestTypes are the manifest media types the sweep will accept, so
// the registry serves the manifest itself rather than converting it.
var registryManifestTypes = []string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.index.v1+json",
}

// repoRegistryState is what one look at the registry established about a
// repository. The distinction that matters is between "not there" and "could
// not tell", because only the first is grounds for deleting anything.
type repoRegistryState int

const (
	// repoUnknown: the sweep learned nothing — an unreachable registry, an
	// error status, an unreadable body. Absence of evidence is not evidence of
	// absence, so the caller must change nothing.
	repoUnknown repoRegistryState = iota
	// repoAbsent: the registry has no repository under this name, and so no
	// manifest under it by any tag or digest.
	repoAbsent
	// repoPresent: the registry served the repository's tag list.
	repoPresent
)

// registryTags lists the tags the registry serves for repoPath, and what it
// established about the repository itself. A 404 is an answer, not a failure.
func (s *Service) registryTags(ctx context.Context, client *http.Client, base, repoPath, password string) ([]string, repoRegistryState) {
	tagsURL := fmt.Sprintf("%s/v2/%s/tags/list", base, repoPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
	if err != nil {
		return nil, repoUnknown
	}
	req.SetBasicAuth(ecrRegistryUser, password)
	resp, err := client.Do(req)
	if err != nil {
		return nil, repoUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, repoAbsent
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, repoUnknown
	}
	var tagsResp struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return nil, repoUnknown
	}
	return tagsResp.Tags, repoPresent
}

// forgetAllRegistryImages deletes every record this sweep created for a
// repository the registry does not have. It is the one-request form of
// forgetVanishedImages, for the case where no per-image question can change the
// answer: a repository nobody ever pushed to, one whose images were deleted out
// from under Overcast, or a registry whose storage really did start empty —
// which a fixed-port claim's no longer does, but the ephemeral fallback and an
// OVERCAST_ECR_REGISTRY_PERSIST=false run still do.
func (s *Service) forgetAllRegistryImages(ctx context.Context, region, repoName string) {
	s.forgetImages(ctx, region, repoName, func(rec imageRecord) bool { return true })
}

// forgetVanishedImages deletes the records of images the registry has stopped
// serving, given the digests just observed under a tag.
//
// The two can disagree in both directions and this closes one of them. A record
// outliving its image — the registry's storage was thrown away, or a fixed-port
// claim fell back to an ephemeral registry that never had the image — leaves a
// publisher asking "is this already pushed?" told yes, skipping the push, and
// the task that runs the image failing at pull time with a 404 the deploy gave
// no warning of. The other direction needs nothing here: an image in the
// registry that the store has forgotten is re-recorded by the sweep above,
// which is how a fresh in-memory store rediscovers what the volume kept.
//
// Only two things are deleted: a record this sweep created (a caller's own
// PutImage is not the registry's to revoke), and only when the registry answers
// a definite 404 for its manifest. Absence from the tag list is not enough —
// an image whose tag has moved to a newer push is still served by digest, and
// real ECR keeps untagged images too.
func (s *Service) forgetVanishedImages(ctx context.Context, client *http.Client, base, repoPath, password, region, repoName string, present map[string]bool) {
	s.forgetImages(ctx, region, repoName, func(rec imageRecord) bool {
		if present[rec.ImageId.ImageDigest] {
			return false
		}
		return s.registryLacksManifest(ctx, client, base, repoPath, password, rec.ImageId.ImageDigest)
	})
}

// forgetImages deletes the records this sweep created that vanished reports
// gone. Records from PutImage are never offered to it: they were never in the
// registry, so the registry's silence says nothing about them.
func (s *Service) forgetImages(ctx context.Context, region, repoName string, vanished func(imageRecord) bool) {
	kvs, err := s.store.Scan(ctx, imageNamespace, serviceutil.RegionKey(region, repoName+"/"))
	if err != nil {
		return
	}
	for _, kv := range kvs {
		var rec imageRecord
		if err := json.Unmarshal([]byte(kv.Value), &rec); err != nil {
			continue
		}
		if !rec.FromRegistry || rec.ImageId.ImageDigest == "" || !vanished(rec) {
			continue
		}
		if err := s.store.Delete(ctx, imageNamespace, kv.Key); err != nil {
			s.log.Warn("failed to drop the record of an image the registry no longer serves",
				zap.String("repository", repoName),
				zap.String("digest", rec.ImageId.ImageDigest), zap.Error(err))
		}
	}
}

// registryLacksManifest reports whether the registry answers a definite 404 for
// a manifest. Only that is grounds for deleting its record: an error, a refusal
// or an unreachable registry all mean the question went unanswered, and the
// record stays.
func (s *Service) registryLacksManifest(ctx context.Context, client *http.Client, base, repoPath, password, digest string) bool {
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", base, repoPath, digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth(ecrRegistryUser, password)
	req.Header.Set("Accept", strings.Join(registryManifestTypes, ", "))
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusNotFound
}

func (s *Service) listImages(w http.ResponseWriter, r *http.Request) {
	var req listImagesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.listImagesTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) describeImages(w http.ResponseWriter, r *http.Request) {
	var req describeImagesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.describeImagesTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) putImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName         string `json:"repositoryName"`
		RegistryId             string `json:"registryId"`
		ImageManifest          string `json:"imageManifest"`
		ImageManifestMediaType string `json:"imageManifestMediaType"`
		ImageTag               string `json:"imageTag"`
		ImageDigest            string `json:"imageDigest"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	// Generate digest if not provided.
	digest := req.ImageDigest
	if digest == "" {
		digest = digestForManifest(req.ImageManifest)
	}

	img := Image{
		RegistryId:             s.accountID(),
		RepositoryName:         req.RepositoryName,
		ImageManifest:          req.ImageManifest,
		ImageManifestMediaType: req.ImageManifestMediaType,
		ImageId: ImageIdentifier{
			ImageDigest: digest,
			ImageTag:    req.ImageTag,
		},
	}
	raw, err := json.Marshal(img)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if err := s.store.Set(ctx, imageNamespace, imageKey(region, req.RepositoryName, digest), string(raw)); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	s.publish(r.Context(), events.ECRImagePushed, events.ResourcePayload{Name: req.RepositoryName, ARN: s.repoARN(region, req.RepositoryName)})
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"image": img})
}

func (s *Service) batchGetImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string            `json:"repositoryName"`
		RegistryId     string            `json:"registryId"`
		ImageIds       []ImageIdentifier `json:"imageIds"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}
	sweep := s.syncRepoImagesFromRegistry(ctx, region, req.RepositoryName)

	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	// Index images by tag and digest.
	byDigest := map[string]Image{}
	byTag := map[string]Image{}
	for _, kv := range kvs {
		var img Image
		if err := json.Unmarshal([]byte(kv.Value), &img); err != nil {
			continue
		}
		byDigest[img.ImageId.ImageDigest] = img
		if img.ImageId.ImageTag != "" {
			byTag[img.ImageId.ImageTag] = img
		}
	}

	images := []Image{}
	failures := []map[string]any{}
	for _, id := range req.ImageIds {
		var img Image
		var ok bool
		if id.ImageDigest != "" {
			img, ok = byDigest[id.ImageDigest]
		} else if id.ImageTag != "" {
			img, ok = byTag[id.ImageTag]
		}
		if ok {
			images = append(images, img)
		} else {
			failures = append(failures, map[string]any{
				"imageId":       id,
				"failureCode":   "ImageNotFoundException",
				"failureReason": "Requested image not found",
			})
		}
	}
	// Same rule as batchGetImageTyped: "not found" about images no sweep could
	// look for is not an answer this can give.
	if len(failures) > 0 && sweep == sweepUnavailable {
		protocol.WriteJSONError(w, r, s.errRegistryUnavailable(req.RepositoryName))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"images":   images,
		"failures": failures,
	})
}

func (s *Service) describeImageScanFindings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string          `json:"repositoryName"`
		RegistryId     string          `json:"registryId"`
		ImageID        ImageIdentifier `json:"imageId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	var matched *Image
	for _, kv := range kvs {
		var img Image
		if err := json.Unmarshal([]byte(kv.Value), &img); err != nil {
			continue
		}
		if req.ImageID.ImageDigest != "" && img.ImageId.ImageDigest == req.ImageID.ImageDigest {
			matched = &img
			break
		}
		if req.ImageID.ImageTag != "" && img.ImageId.ImageTag == req.ImageID.ImageTag {
			matched = &img
			break
		}
	}
	if matched == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ImageNotFoundException",
			Message:    "Requested image not found",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	// Overcast stores image metadata but does not emulate scanner engines.
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId":     s.accountID(),
		"repositoryName": req.RepositoryName,
		"imageId":        matched.ImageId,
		"imageScanStatus": map[string]any{
			"status":      "UNSUPPORTED_IMAGE",
			"description": "Image scanning is not emulated",
		},
		"imageScanFindings": map[string]any{
			"findingSeverityCounts": map[string]any{},
			"findings":              []any{},
			"enhancedFindings":      []any{},
		},
	})
}

func (s *Service) batchDeleteImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string            `json:"repositoryName"`
		RegistryId     string            `json:"registryId"`
		ImageIds       []ImageIdentifier `json:"imageIds"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	byDigest := map[string]string{}
	byTag := map[string]string{} // tag -> key
	for _, kv := range kvs {
		var img Image
		if err := json.Unmarshal([]byte(kv.Value), &img); err != nil {
			continue
		}
		byDigest[img.ImageId.ImageDigest] = kv.Key
		if img.ImageId.ImageTag != "" {
			byTag[img.ImageId.ImageTag] = kv.Key
		}
	}

	deleted := []ImageIdentifier{}
	failures := []map[string]any{}
	for _, id := range req.ImageIds {
		var storeKey string
		var ok bool
		if id.ImageDigest != "" {
			storeKey, ok = byDigest[id.ImageDigest]
		} else if id.ImageTag != "" {
			storeKey, ok = byTag[id.ImageTag]
		}
		if ok {
			_ = s.store.Delete(ctx, imageNamespace, storeKey)
			deleted = append(deleted, id)
		} else {
			failures = append(failures, map[string]any{
				"imageId":       id,
				"failureCode":   "ImageNotFoundException",
				"failureReason": "Requested image not found",
			})
		}
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"imageIds": deleted,
		"failures": failures,
	})
}

// putImageTagMutability reconciles the mutability setting CreateRepository
// stored. Real ECR does not enforce it here on the control plane — the
// rejection happens on PutImage, when a tag that already exists is pushed
// again to an IMMUTABLE repository. Overcast does not implement that push-time
// check (see putImage/putImageTyped), so setting IMMUTABLE changes what
// DescribeRepositories reports without changing what a second `docker push`
// of the same tag is allowed to do.
func (s *Service) putImageTagMutability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName     string `json:"repositoryName"`
		RegistryId         string `json:"registryId"`
		ImageTagMutability string `json:"imageTagMutability"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ImageTagMutability != "MUTABLE" && req.ImageTagMutability != "IMMUTABLE" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "imageTagMutability must be MUTABLE or IMMUTABLE",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	region := s.region(r)
	ctx := r.Context()
	repo, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	repo.ImageTagMutability = req.ImageTagMutability
	if err := s.saveRepo(ctx, region, repo); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId":         s.accountID(),
		"repositoryName":     req.RepositoryName,
		"imageTagMutability": req.ImageTagMutability,
	})
}

// putImageScanningConfiguration reconciles the scan-on-push setting
// CreateRepository stored. No scan engine runs either way — see
// DescribeImageScanFindings — so this changes what DescribeRepositories
// reports, not what happens when an image is pushed.
func (s *Service) putImageScanningConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName             string                     `json:"repositoryName"`
		RegistryId                 string                     `json:"registryId"`
		ImageScanningConfiguration ImageScanningConfiguration `json:"imageScanningConfiguration"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	repo, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	repo.ImageScanningConfiguration = req.ImageScanningConfiguration
	if err := s.saveRepo(ctx, region, repo); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId":                 s.accountID(),
		"repositoryName":             req.RepositoryName,
		"imageScanningConfiguration": repo.ImageScanningConfiguration,
	})
}

func (s *Service) setRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		RegistryId     string `json:"registryId"`
		PolicyText     string `json:"policyText"`
		Force          bool   `json:"force"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	key := serviceutil.RegionKey(region, req.RepositoryName)
	if err := s.store.Set(ctx, policyNamespace, key, req.PolicyText); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId":     s.accountID(),
		"repositoryName": req.RepositoryName,
		"policyText":     req.PolicyText,
	})
}

func (s *Service) getRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		RegistryId     string `json:"registryId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	key := serviceutil.RegionKey(region, req.RepositoryName)
	policyText, found, err := s.store.Get(ctx, policyNamespace, key)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "RepositoryPolicyNotFoundException",
			Message:    fmt.Sprintf("Repository policy does not exist for the repository with name '%s'", req.RepositoryName),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId":     s.accountID(),
		"repositoryName": req.RepositoryName,
		"policyText":     policyText,
	})
}

func (s *Service) deleteRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	var req repoRefRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.deleteRepositoryPolicyTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (s *Service) putLifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName      string `json:"repositoryName"`
		RegistryId          string `json:"registryId"`
		LifecyclePolicyText string `json:"lifecyclePolicyText"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	key := serviceutil.RegionKey(region, req.RepositoryName)
	if err := s.store.Set(ctx, lifecycleNS, key, req.LifecyclePolicyText); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId":          s.accountID(),
		"repositoryName":      req.RepositoryName,
		"lifecyclePolicyText": req.LifecyclePolicyText,
	})
}

func (s *Service) getLifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		RegistryId     string `json:"registryId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	region := s.region(r)
	ctx := r.Context()
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		s.errRepoNotFound(w, r, req.RepositoryName)
		return
	}

	key := serviceutil.RegionKey(region, req.RepositoryName)
	policyText, found, err := s.store.Get(ctx, lifecycleNS, key)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "LifecyclePolicyNotFoundException",
			Message:    fmt.Sprintf("Lifecycle policy does not exist for the repository with name '%s'", req.RepositoryName),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"registryId":          s.accountID(),
		"repositoryName":      req.RepositoryName,
		"lifecyclePolicyText": policyText,
	})
}

func (s *Service) deleteLifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	var req repoRefRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := s.deleteLifecyclePolicyTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// ─── Tag operations ───────────────────────────────────────────────────────────

func (s *Service) saveTags(ctx context.Context, arn string, tags []Tag) error {
	raw, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, tagNamespace, arn, string(raw))
}

func (s *Service) loadTags(ctx context.Context, arn string) ([]Tag, error) {
	raw, found, err := s.store.Get(ctx, tagNamespace, arn)
	if err != nil || !found {
		return nil, err
	}
	var tags []Tag
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

func (s *Service) tagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
		Tags        []Tag  `json:"tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	ctx := r.Context()
	existing, err := s.loadTags(ctx, req.ResourceArn)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	// Merge: existing tags overwritten by new ones.
	tagMap := map[string]string{}
	for _, t := range existing {
		tagMap[t.Key] = t.Value
	}
	for _, t := range req.Tags {
		tagMap[t.Key] = t.Value
	}
	// See tagResourceTyped (typed_logic.go) — this JSON1.1 path duplicates
	// it rather than delegating, so the tag validation added for #1052 has
	// to be kept in lockstep here too. Validated against the merged set, not
	// just the incoming delta.
	if aerr := serviceutil.ValidateTags(ecrTagCfg, tagMap); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	merged := make([]Tag, 0, len(tagMap))
	for k, v := range tagMap {
		merged = append(merged, Tag{Key: k, Value: v})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Key < merged[j].Key })

	if err := s.saveTags(ctx, req.ResourceArn, merged); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) untagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	ctx := r.Context()
	existing, err := s.loadTags(ctx, req.ResourceArn)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	remove := map[string]bool{}
	for _, k := range req.TagKeys {
		remove[k] = true
	}
	filtered := make([]Tag, 0, len(existing))
	for _, t := range existing {
		if !remove[t.Key] {
			filtered = append(filtered, t)
		}
	}
	if err := s.saveTags(ctx, req.ResourceArn, filtered); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (s *Service) listTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	ctx := r.Context()
	tags, err := s.loadTags(ctx, req.ResourceArn)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if tags == nil {
		tags = []Tag{}
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"tags": tags})
}

// ensureRegistry triggers lazy-start of the shared local registry container.
// When Docker is available it fires off the container setup in a background
// goroutine and returns immediately — callers that need the registry to be
// ready (e.g. GetAuthorizationToken) should call waitRegistryReady afterwards.
func (s *Service) ensureRegistry(ctx context.Context) error {
	if s.docker == nil {
		return nil
	}

	s.registryMu.Lock()
	if s.registryStopping {
		s.registryMu.Unlock()
		return nil
	}
	if s.registryHostPort > 0 {
		s.registryMu.Unlock()
		return nil
	}
	if s.registryPassword == "" {
		password, err := generateRegistryPassword()
		if err != nil {
			s.registryMu.Unlock()
			return err
		}
		s.registryPassword = password
	}
	var ready chan struct{}
	s.registryInitOnce.Do(func() {
		ready = make(chan struct{})
		s.registryReady = ready
	})
	s.registryMu.Unlock()
	if ready != nil {
		go s.initRegistryDocker(ready)
	}
	return nil
}

// waitRegistryReady blocks until the registry container is fully started, or
// the context is cancelled. Returns immediately if Docker is not configured or
// the registry is already ready.
func (s *Service) waitRegistryReady(ctx context.Context) error {
	if s.docker == nil {
		return nil
	}
	_ = s.ensureRegistry(ctx)
	s.registryMu.Lock()
	ready := s.registryReady
	s.registryMu.Unlock()
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// initRegistryDocker performs the blocking Docker setup for the shared local
// registry container. Runs in a background goroutine launched by registryInitOnce.
func (s *Service) initRegistryDocker(ready chan struct{}) {
	defer close(ready)

	pingCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	err := s.docker.Ping(pingCtx)
	cancel()
	if err != nil {
		return
	}

	s.registryMu.Lock()
	if s.registryHostPort > 0 || s.registryStopping {
		s.registryMu.Unlock()
		return
	}
	password := s.registryPassword
	s.registryMu.Unlock()

	// Password is already generated by ensureRegistry before launching this goroutine.
	if s.puller != nil {
		pullCtx, pullCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if perr := s.puller.Ensure(pullCtx, ecrRegistryImage); perr != nil {
			s.log.Warn("failed to pull ECR registry image", zap.Error(perr))
		}
		pullCancel()
	}

	htpasswd, err := htpasswdArchive(password)
	if err != nil {
		s.log.Warn("failed to build ECR registry credentials", zap.Error(err))
		return
	}

	s.registryMu.Lock()
	if s.registryStopping {
		s.registryMu.Unlock()
		return
	}
	s.registryMu.Unlock()

	// The only container startup may remove sight-unseen is one on the legacy
	// singleton name. Registries under per-claim names are never swept, in any
	// state: a "created" one is a sibling's being born, a running one is a
	// sibling's serving pushes, and an exited one is AutoRemove's to reclaim.
	s.reapLegacyRegistry(context.Background())

	// A fixed, well-known port first — stable repositoryUri across restarts
	// and a nameable address for daemon configuration — falling back to an
	// ephemeral one when something else holds it, because a registry on an
	// unexpected port beats no registry at all. The fixed port's name is
	// derived from the port: that slot is exclusive, so whatever holds the
	// name is a predecessor to replace. The ephemeral name is random: it
	// contends with nobody.
	var containerID string
	var inspect *docker.ContainerInspect
	if s.cfg.ECRRegistryPort > 0 {
		port := strconv.Itoa(s.cfg.ECRRegistryPort)
		name := ecrRegistryNamePrefix + "-" + port
		s.replaceRegistryHolder(context.Background(), name)
		containerID, inspect, err = s.startRegistry(context.Background(), name, htpasswd, port)
		if err != nil && docker.IsPortUnavailable(err) {
			s.log.Warn("ECR registry port is taken; falling back to an ephemeral port — repositoryUri will not be stable across restarts",
				zap.String("port", port), zap.Error(err))
			err = errRetryEphemeral
		}
	} else {
		err = errRetryEphemeral
	}
	if errors.Is(err, errRetryEphemeral) {
		containerID, inspect, err = s.startRegistry(context.Background(), ephemeralRegistryName(), htpasswd, "")
	}
	if err != nil {
		s.log.Warn("failed to start ECR registry container", zap.Error(err))
		return
	}

	hostPorts := publishedHostPorts(inspect)
	if len(hostPorts) == 0 {
		s.log.Warn("ECR registry container published no host port")
		return
	}

	// The port repositoryUri advertises is chosen by its only consumer: the
	// daemon, which performs every push, pull and login. A dual-stack
	// ephemeral publish can land the IPv4 and IPv6 bindings on different
	// ports, and which family "localhost" dials is the daemon's business —
	// a port verified from Overcast's own vantage still produced
	// `docker login … dial tcp [::1]:<v4port>: connection refused` on CI.
	// The probe makes the daemon itself dial, exactly as login does, and
	// carries our password so the port it settles on is one that answers as
	// *our* registry rather than as a sibling instance's.
	hostPort, reachable := s.selectDaemonReachablePort(hostPorts, password)
	if !reachable {
		s.log.Warn("the Docker daemon cannot reach the ECR registry it just published; "+
			"docker push to the emulated ECR and ECS/Lambda pulls of its images will fail. "+
			"If the daemon is remote or its published ports are not on its loopback, set "+
			"OVERCAST_ECR_REGISTRY_PORT and add the registry to the daemon's insecure-registries.",
			zap.Ints("ports", hostPorts))
	}

	s.registryMu.Lock()
	if s.registryStopping {
		s.registryMu.Unlock()
		_ = s.docker.StopContainer(context.Background(), containerID, 3)
		_ = s.docker.RemoveContainerForce(containerID)
		return
	}
	s.registryContainer = containerID
	s.registryName = strings.TrimPrefix(inspect.Name, "/")
	s.registryClientBases = registryClientCandidates(hostPort, inspect)
	s.registryMu.Unlock()
	s.adoptRegistryAddress(hostPort, reachable)

	// Only now, with an answering registry, is "ready" allowed to mean ready:
	// waitRegistryReady gates GetAuthorizationToken and repositoryUri minting,
	// and a token handed out before the registry process accepts connections
	// turns into `docker login` dying on an EOF. Container start is not
	// process listening; the gap is real on a loaded machine.
	if !s.awaitRegistryAnswering(password) {
		s.log.Warn("ECR registry container started but never answered /v2/",
			zap.Int("port", hostPort))
	}
}

// adoptRegistryAddress records the address every client-facing ECR address is
// then built from — repositoryUri, proxyEndpoint, and the reference an ECR
// image resolves to. proved says whether the daemon answered on it.
//
// When it did, the host is the one the probe dialled, not the configured
// hostname. Both name this machine, but only one of them is a demonstrated
// fact, and Docker does not treat them alike: it trusts plain HTTP to
// "localhost" without configuration and bypasses proxies for it, while
// OVERCAST_HOSTNAME is an ordinary domain to a daemon even when it resolves to
// loopback. On a machine with a proxy configured, advertising the domain sent
// the push through Docker Desktop's proxy —
//
//	proxyconnect tcp: dial tcp 192.168.65.1:3128: i/o timeout
//
// — so it never reached a registry that was listening the whole time, on the
// address startup had already proved. Advertise what was proved.
//
// When nothing was proved, "localhost" would be a guess about someone else's
// machine — a remote daemon, or published ports that are not on its loopback —
// so the configured hostname stands. That is the address the startup warning
// tells the operator to add to the daemon's insecure-registries, and the two
// must agree.
func (s *Service) adoptRegistryAddress(hostPort int, proved bool) {
	host := s.cfg.ExternalHostname()
	if proved {
		host = registryProbeHost
	}
	s.registryMu.Lock()
	s.registryHost = host
	s.registryHostPort = hostPort
	s.registryProven = proved
	s.registryMu.Unlock()
}

// registryLimitation reports why the address just handed to a client —
// repositoryUri, proxyEndpoint, or the reference an ECR image resolves to —
// may not actually reach a working registry, or "" when the daemon proved it
// does (see registryProven). Every path that hands out a Repository calls
// this, so CreateRepository's own response — not a log line, and not the
// `docker push` that fails minutes later — is where a developer first hears
// that this repository's address does not work. The message is worded to end
// up in CloudFormation's ResourceStatusReason (via
// protocol.EmulationLimitationHeader), beside the resource it is about.
func (s *Service) registryLimitation() string {
	s.registryMu.Lock()
	hasHostPort := s.registryHostPort > 0
	proven := s.registryProven
	s.registryMu.Unlock()
	switch {
	case hasHostPort && proven:
		return ""
	case hasHostPort:
		return "the ECR registry container started but the Docker daemon never confirmed it answers; docker push/pull against this repository may fail"
	default:
		return "no ECR registry container is running, so repositoryUri names Overcast's own API port; docker push will fail with 405 Method Not Allowed until a Docker daemon is available to back the registry"
	}
}

// publishedHostPorts returns the distinct host ports the registry's container
// port is published on, in the order the daemon lists them. Usually one; two
// when a dual-stack ephemeral publish gave IPv4 and IPv6 different ports.
func publishedHostPorts(inspect *docker.ContainerInspect) []int {
	var ports []int
	for _, b := range inspect.NetworkSettings.Ports[ecrRegistryPortKey] {
		p, err := strconv.Atoi(b.HostPort)
		if err != nil || p <= 0 || slices.Contains(ports, p) {
			continue
		}
		ports = append(ports, p)
	}
	return ports
}

// selectDaemonReachablePort asks the daemon to contact the registry at
// "localhost:<port>" for each candidate, carrying this instance's credentials,
// and returns the first port that answers as *our* registry. The whole sweep
// retries while the registry process is still booting (a transport failure
// during boot says nothing about the port), bounded so a wedged container
// cannot hang startup. When nothing answers within the deadline the first
// candidate is returned with reachable=false: Overcast's own registry access
// may still work, and a wrong-but-stated port beats no registry at all.
//
// The credentials are what make this an identity check rather than a liveness
// one. Two Overcast instances sharing a daemon can have their ephemeral
// dual-stack publishes interleave — A on v4 :32768 / v6 :32770, B on v4 :32769
// / v6 :32768 — and an anonymous probe of :32768 reaches B over IPv6, is
// refused exactly as A's own registry would refuse it, and A advertises a port
// serving B. Every token A then issues fails against B's htpasswd, as an
// authentication error carrying a valid-looking token. Offering our password
// separates the two: our registry accepts it and reports the probe repository
// missing, a sibling's rejects it.
func (s *Service) selectDaemonReachablePort(ports []int, password string) (port int, reachable bool) {
	deadline := time.Now().Add(s.registryProbeTimeout())
	for time.Now().Before(deadline) {
		for _, candidate := range ports {
			address := fmt.Sprintf("%s:%d", registryProbeHost, candidate)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := s.docker.DistributionInspectWithAuth(ctx,
				address+"/overcast/connectivity-probe:none",
				&docker.RegistryAuth{
					Username:      ecrRegistryUser,
					Password:      password,
					ServerAddress: address,
				})
			cancel()
			switch {
			case docker.RegistryRejectedCredentials(err):
				// Something is listening and it is not ours — or ours is not up
				// yet and a stranger holds the port. Either way this port must
				// not be advertised; keep sweeping until the deadline.
				continue
			case err == nil || !docker.RegistryUnreachable(err):
				// Answered, and let us in: our registry, missing repository.
				return candidate, true
			}
		}
		time.Sleep(registryAnswerBackoff)
	}
	return ports[0], false
}

// registryProbeTimeout bounds the port sweep. Indirected so a test can drive
// the exhausted-deadline path without waiting out the real one; zero means the
// production value.
func (s *Service) registryProbeTimeout() time.Duration {
	if s.probeTimeout > 0 {
		return s.probeTimeout
	}
	return registryAnswerTimeout
}

// awaitRegistryAnswering polls the registry until it answers HTTP on any of
// the addresses this process can reach it at, bounded so a wedged container
// cannot hang startup.
func (s *Service) awaitRegistryAnswering(password string) bool {
	deadline := time.Now().Add(registryAnswerTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		base := s.registryBaseURL(ctx, password)
		cancel()
		if base != "" {
			return true
		}
		time.Sleep(registryAnswerBackoff)
	}
	return false
}

// errRetryEphemeral routes registry startup to the ephemeral-port attempt —
// either because no fixed port is configured or because the fixed one is held.
var errRetryEphemeral = errors.New("retry with an ephemeral port")

// ephemeralRegistryName mints a container name no other process will claim.
func ephemeralRegistryName() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a clock-derived suffix; uniqueness within one daemon is
		// all that is asked of it.
		return fmt.Sprintf("%s-%d", ecrRegistryNamePrefix, time.Now().UnixNano())
	}
	return ecrRegistryNamePrefix + "-" + hex.EncodeToString(b)
}

// reapLegacyRegistry removes a registry still on the pre-suffix singleton
// name — an older Overcast's, which the per-claim names would otherwise never
// reclaim. Nothing else is swept, deliberately. A sibling's registry passes
// through the "created" state between its create and start, and a sweep that
// removed non-running registries force-removed exactly those newborns (list,
// then force-remove, is not atomic — the container could be running by the
// time the removal landed). Exited registries need no sweep: AutoRemove
// already reclaims them the moment they stop.
func (s *Service) reapLegacyRegistry(ctx context.Context) {
	legacy, err := s.docker.GetContainerByName(ctx, ecrRegistryNamePrefix)
	if err != nil || legacy == nil {
		return
	}
	if !legacy.HasOvercastLabels(serviceName, ecrRegistryResource) {
		return
	}
	_ = s.docker.StopContainer(ctx, legacy.ID, 3)
	_ = s.docker.RemoveContainerForce(legacy.ID)
}

// replaceRegistryHolder removes whatever Overcast-managed registry holds name.
// Only the fixed-port claim calls this: that name is derived from the port,
// the port is an exclusive resource, and so its holder is by definition a
// predecessor (possibly still running after an unclean shutdown, with an
// htpasswd no current token matches) rather than a peaceful neighbour.
//
// It removes the container and nothing else. The predecessor's storage volume
// is the successor's inheritance — deleting it here would leave a registry that
// persists across restarts in every way except the one that matters. Same for
// Stop: the container is this process's, the images in the volume are not.
func (s *Service) replaceRegistryHolder(ctx context.Context, name string) {
	info, err := s.docker.GetContainerByName(ctx, name)
	if err != nil || info == nil {
		return
	}
	if !info.HasOvercastLabels(serviceName, ecrRegistryResource) {
		s.log.Warn("existing ecr registry container is not overcast-managed", zap.String("container", name))
		return
	}
	_ = s.docker.StopContainer(ctx, info.ID, 3)
	_ = s.docker.RemoveContainerForce(info.ID)
}

// registryVolumeName returns the named volume backing a registry claim's
// storage, or "" for a claim that gets none.
//
// Only the fixed-port claim gets one, and the port is what names it. That claim
// is an exclusive slot with a stable address, so the volume a successor should
// inherit is unambiguous — it is the one the port names. An ephemeral claim has
// neither half: its container name is deliberately random so concurrent
// instances cannot collide over it, and its port is whatever the daemon had
// spare. A volume named after either would be a fresh orphan on every start,
// found again by no restart and reclaimable only by sweeping the label, so the
// ephemeral fallback stays as it was — storage that dies with its container.
//
// A single well-known name for ephemeral claims would fix the finding and break
// something worse: it is the *concurrent* case that drives an ephemeral port in
// the first place, and two registry:2 processes writing one filesystem is how
// the storage gets corrupted rather than shared.
func (s *Service) registryVolumeName(hostPort string) string {
	if hostPort == "" || s.cfg == nil || !s.cfg.ECRRegistryPersist {
		return ""
	}
	return ecrRegistryNamePrefix + "-data-" + hostPort
}

// startRegistry creates and starts the registry container under name,
// publishing its port on hostPort ("" for daemon-assigned), returning the
// started container's inspect result. The container that failed to start is
// removed before the error is returned, so a port-unavailable failure can be
// retried under a fresh claim.
func (s *Service) startRegistry(ctx context.Context, name string, htpasswd []byte, hostPort string) (string, *docker.ContainerInspect, error) {
	var mounts []docker.Mount
	if volume := s.registryVolumeName(hostPort); volume != "" {
		// Best-effort, like every other Docker call on this path: a registry
		// with anonymous storage is what every release before this one shipped,
		// and it beats no registry at all. Create is idempotent by name, so the
		// successor of a restart re-adopts the volume it already has.
		if err := s.docker.CreateVolume(ctx, volume, docker.ManagedLabels(serviceName, ecrRegistryResource)); err != nil {
			s.log.Warn("failed to create the ECR registry's storage volume — pushed images will not survive a restart",
				zap.String("volume", volume), zap.Error(err))
		} else {
			mounts = []docker.Mount{{
				Type:   "volume",
				Source: volume,
				Target: ecrRegistryStoragePath,
			}}
		}
	}

	req := &docker.CreateContainerRequest{
		ContainerConfig: &docker.ContainerConfig{
			Image: ecrRegistryImage,
			ExposedPorts: map[string]struct{}{
				ecrRegistryPortKey: {},
			},
			Env: []string{
				"REGISTRY_AUTH=htpasswd",
				"REGISTRY_AUTH_HTPASSWD_REALM=Registry Realm",
				"REGISTRY_AUTH_HTPASSWD_PATH=" + ecrRegistryHTPasswdPath,
			},
			Labels: docker.ManagedLabels(serviceName, ecrRegistryResource),
		},
		HostConfig: &docker.HostConfig{AutoRemove: true,
			PortBindings: map[string][]docker.PortBinding{
				// No HostIP. An explicit "0.0.0.0" restricts the binding to
				// IPv4, and Docker Desktop's port forwarding does not wire an
				// ephemeral v4-only binding into the VM the daemon runs in —
				// the daemon then cannot reach its own registry at localhost,
				// so every push and every task-image pull dies on a dial.
				// Leaving it empty binds dual-stack, which both the VM
				// forwarding and native Linux serve correctly. Measured, not
				// theorised: see docs/services/ecr.md § Limitations.
				ecrRegistryPortKey: {{HostPort: hostPort}},
			},
			// AutoRemove stays, and the volume is why it can. Docker's implicit
			// removal passes v=1, but that only reaps *anonymous* volumes — the
			// one registry:2's own VOLUME declaration would otherwise mint here.
			// A named volume survives both that and an explicit force-remove,
			// which is what lets the container be as disposable as it always
			// was while its blobs are not.
			Mounts: mounts,
		},
	}

	containerID, err := s.createRegistryContainer(ctx, name, req)
	if err != nil {
		return "", nil, fmt.Errorf("create: %w", err)
	}

	s.registryMu.Lock()
	stopping := s.registryStopping
	s.registryMu.Unlock()
	if stopping {
		_ = s.docker.StopContainer(ctx, containerID, 3)
		_ = s.docker.RemoveContainerForce(containerID)
		return "", nil, fmt.Errorf("registry is stopping")
	}

	// The credentials are copied in rather than bind-mounted. A bind source is
	// resolved by the daemon, not by us, so a dockerized Overcast would name a
	// path inside its own filesystem that the host does not have — Docker then
	// creates an empty directory there and the registry starts with no usable
	// htpasswd file. Same reason function code and the CA bundle travel this way.
	if err := s.docker.CopyToContainer(ctx, containerID, "/", bytes.NewReader(htpasswd)); err != nil {
		_ = s.docker.RemoveContainerForce(containerID)
		return "", nil, fmt.Errorf("install credentials: %w", err)
	}

	if err := s.docker.StartContainer(ctx, containerID); err != nil {
		_ = s.docker.RemoveContainerForce(containerID)
		return "", nil, fmt.Errorf("start: %w", err)
	}
	inspect, err := s.docker.InspectContainer(ctx, containerID)
	if err != nil {
		return "", nil, fmt.Errorf("inspect: %w", err)
	}
	return containerID, inspect, nil
}

// registryBaseURL returns the base URL at which this process reaches the
// registry, probing the candidates recorded at registry start on first use and
// caching the winner. "localhost is network-namespace local": the loopback
// address in repositoryUri is the daemon's, and when Overcast runs in a
// container of that daemon its own 127.0.0.1 is somewhere else entirely — the
// registry is then reachable at its container address or through the bridge
// gateway instead. Probing is the only honest way to pick, and a failed probe
// is retried on the next call rather than cached: the registry may simply not
// be answering yet.
func (s *Service) registryBaseURL(ctx context.Context, password string) string {
	s.registryMu.Lock()
	base := s.registryClientBase
	candidates := s.registryClientBases
	s.registryMu.Unlock()
	if base != "" {
		return base
	}

	client := &http.Client{Timeout: 3 * time.Second}
	for _, candidate := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate+"/v2/", nil)
		if err != nil {
			continue
		}
		req.SetBasicAuth(ecrRegistryUser, password)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		// Any HTTP answer proves the path; 401 just means wrong credentials,
		// which is not this function's question.
		if resp.StatusCode < http.StatusInternalServerError {
			s.registryMu.Lock()
			s.registryClientBase = candidate
			s.registryMu.Unlock()
			return candidate
		}
	}
	return ""
}

// registryClientCandidates lists base URLs at which Overcast itself might
// reach the registry, most likely first. Which one answers depends on where
// Overcast runs — loopback when it shares the daemon's host, the registry
// container's own address or the bridge gateway when Overcast is itself a
// container — and cannot be decided statically, so the caller probes them.
func registryClientCandidates(hostPort int, inspect *docker.ContainerInspect) []string {
	candidates := []string{fmt.Sprintf("http://127.0.0.1:%d", hostPort)}
	for _, network := range inspect.NetworkSettings.Networks {
		if network.IPAddress != "" {
			candidates = append(candidates, "http://"+network.IPAddress+":5000")
		}
		if network.Gateway != "" {
			candidates = append(candidates, fmt.Sprintf("http://%s:%d", network.Gateway, hostPort))
		}
	}
	return candidates
}

// createRegistryContainer creates the registry container under name, waiting
// out a name still held by a predecessor.
//
// Docker keeps a container's name reserved until removal completes, and
// removal is asynchronous — the container disappears from inspect before the
// name is free. A predecessor being reaped therefore answers the create with
// 409 Conflict for a moment. Failing on that left the registry down for the
// lifetime of the process, which means every `docker push` to the emulated ECR
// refused and every task launched from an image in it failed. Only the
// fixed-port name can meet this; an ephemeral claim's name is freshly random.
func (s *Service) createRegistryContainer(ctx context.Context, name string, req *docker.CreateContainerRequest) (string, error) {
	// Real elapsed time, not the injected clock: this waits on the Docker
	// daemon, which a test's mock clock does not move.
	for attempt := 0; ; attempt++ {
		id, err := s.docker.CreateContainer(ctx, name, req)
		if err == nil || !docker.IsConflict(err) || attempt >= registryCreateAttempts {
			return id, err
		}
		// A conflicting container that is still around is one we are entitled
		// to replace; one already on its way out just needs a moment.
		if info, inspectErr := s.docker.GetContainerByName(ctx, name); inspectErr == nil && info != nil {
			if !info.HasOvercastLabels(serviceName, ecrRegistryResource) {
				return "", err
			}
			_ = s.docker.StopContainer(ctx, info.ID, 3)
			_ = s.docker.RemoveContainerForce(info.ID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(registryCreateBackoff):
		}
	}
}

func generateRegistryPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate registry password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// htpasswdArchive builds the tar carrying the registry's credentials file, for
// CopyToContainer with destination "/". bcrypt is what registry:2 expects in an
// htpasswd entry, and the username matches the one every ECR authorization
// token carries.
func htpasswdArchive(password string) ([]byte, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash ecr registry password: %w", err)
	}
	line := []byte(ecrRegistryUser + ":" + string(hash) + "\n")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: strings.TrimPrefix(ecrRegistryHTPasswdPath, "/"),
		Mode: 0o444, // world-readable: the registry image drops to its own user
		Size: int64(len(line)),
	}); err != nil {
		return nil, fmt.Errorf("write ecr htpasswd header: %w", err)
	}
	if _, err := tw.Write(line); err != nil {
		return nil, fmt.Errorf("write ecr htpasswd entry: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close ecr htpasswd archive: %w", err)
	}
	return buf.Bytes(), nil
}

// Stop tears down this process's registry container on server shutdown. Only
// its own: another process's registry on the same daemon is none of ours.
func (s *Service) Stop(ctx context.Context) {
	if s.docker == nil {
		return
	}

	s.registryMu.Lock()
	s.registryStopping = true
	containerID := s.registryContainer
	name := s.registryName
	ready := s.registryReady
	s.registryMu.Unlock()
	if ready != nil {
		select {
		case <-ready:
		case <-ctx.Done():
		}
		s.registryMu.Lock()
		if s.registryContainer != "" {
			containerID = s.registryContainer
		}
		name = s.registryName
		s.registryMu.Unlock()
	}
	if containerID == "" && name == "" {
		// Init never claimed anything; the only slot that could still hold a
		// container of ours is the configured fixed port's name.
		if s.cfg == nil || s.cfg.ECRRegistryPort <= 0 {
			return
		}
		name = ecrRegistryNamePrefix + "-" + strconv.Itoa(s.cfg.ECRRegistryPort)
	}

	s.removeRegistryContainer(containerID, name)
}

// removeRegistryContainer stops and removes the named registry container,
// then waits for the daemon to confirm it is actually gone — "stopped" is
// not "removed" while AutoRemove is still working.
//
// It used to poll GetContainerByName until the name came back not-found, on
// a fixed 10s budget. That looked right — until CI showed a shutdown that
// returned clean while the daemon still listed the container as
// state=removing. The container has HostConfig.AutoRemove set (see
// ensureRegistry), so stopping it also queues Docker's own exit-triggered
// auto-remove goroutine — independent of, and racing, the explicit
// RemoveContainerForce call below. Whichever side the daemon accepts, name
// and container-store removal land together at the end of the daemon's own
// cleanup, which under load (layer unmount, contended graphdriver) can run
// longer than any fixed poll budget was willing to guess. Polling by name
// can only ever answer "how long did I wait", never "is it actually gone
// yet" — WaitContainerRemoved asks the daemon that question directly and
// blocks on its answer instead.
func (s *Service) removeRegistryContainer(containerID, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if containerID == "" && name != "" {
		if info, err := s.docker.GetContainerByName(ctx, name); err == nil && info != nil && info.HasOvercastLabels(serviceName, ecrRegistryResource) {
			containerID = info.ID
		}
	}
	if containerID == "" {
		return
	}

	_ = s.docker.StopContainer(ctx, containerID, 3)
	// This either performs the removal itself, or loses the race against
	// Docker's own AutoRemove and comes back a 409 "already in progress" —
	// either way something is now removing this container; the wait below is
	// what actually confirms it finished.
	_ = s.docker.RemoveContainerForce(containerID)
	if err := s.docker.WaitContainerRemoved(ctx, containerID); err != nil && !docker.IsNotFound(err) {
		s.log.Warn("ecr registry container removal did not complete before shutdown gave up",
			zap.String("container", containerID), zap.Error(err))
	}
}
