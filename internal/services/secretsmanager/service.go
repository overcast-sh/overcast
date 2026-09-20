// Package secretsmanager provides emulation of AWS Secrets Manager.
// See docs/services/secretsmanager.md for the support matrix.
package secretsmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "secretsmanager"

// awsapiService is Secrets Manager's key in the generated AWS model corpus.
// serviceutil.MustAWSService validates it at package initialisation, so a
// key the models do not carry fails immediately rather than silently
// answering every unimplemented operation with a 400.
var awsapiService = serviceutil.MustAWSService(serviceName)

// Service implements router.Service for Secrets Manager.
type Service struct {
	cfg     *config.Config
	store   state.Store
	log     *serviceutil.ServiceLogger
	handler *Handler

	// Rotation engine lifecycle. One goroutine for the whole service, started
	// from RegisterRoutes and drained by Stop.
	engineOnce sync.Once
	stopOnce   sync.Once
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// New returns a configured Secrets Manager Service.
//
// It does no I/O: the rotation engine starts from RegisterRoutes, and its first
// store read happens on the goroutine, never on router.New's critical path.
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock) *Service {
	log := serviceutil.NewServiceLogger(logger, serviceName)
	return &Service{
		cfg:     cfg,
		store:   store,
		log:     log,
		handler: newHandler(cfg, store, log, clk),
		stopCh:  make(chan struct{}),
	}
}

// InitBus wires the event bus for secret lifecycle events.
func (s *Service) InitBus(bus *events.Bus) {
	s.handler.bus = bus
}

// InitLambdaInvoker wires the synchronous Lambda invoker the rotation protocol
// drives. Rotation refuses to run rather than pretending, when it is absent.
func (s *Service) InitLambdaInvoker(invoker events.FunctionSyncInvoker) {
	s.handler.InitLambdaInvoker(invoker)
}

// SecretValue returns the current SecretString for a secret named by ID, name
// or ARN. Used by ECS to resolve a container definition's `secrets`, which is
// how a task receives credentials without them being written into the task
// definition. ok is false for an unknown secret or one holding only binary.
func (s *Service) SecretValue(ctx context.Context, secretID string) (string, bool) {
	sec, aerr := s.handler.resolveLiveSecret(ctx, secretID)
	if aerr != nil || sec == nil {
		return "", false
	}
	for i := range sec.Versions {
		if sec.Versions[i].VersionId == sec.CurrentVersionId {
			if sec.Versions[i].SecretString == "" {
				return "", false
			}
			return sec.Versions[i].SecretString, true
		}
	}
	return "", false
}

// Name returns the service identifier.
func (s *Service) Name() string { return serviceName }

// TargetPrefix returns the X-Amz-Target prefix for Secrets Manager dispatch.
func (s *Service) TargetPrefix() string { return "secretsmanager." }

// RegisterRoutes mounts admin endpoints for the web console.
func (s *Service) RegisterRoutes(r chi.Router) {
	r.Get("/_overcast/secretsmanager/secrets", s.adminListSecrets)
	r.Post("/_overcast/secretsmanager/secrets", s.adminCreateSecret)
	r.Get("/_overcast/secretsmanager/secrets/{secretId}/value", s.adminGetSecretValue)
	r.Put("/_overcast/secretsmanager/secrets/{secretId}/value", s.adminUpdateSecretValue)
	r.Delete("/_overcast/secretsmanager/secrets/{secretId}", s.adminDeleteSecret)
	r.Get("/_overcast/secretsmanager/secrets/{secretId}/rotation", s.adminRotationStatus)

	s.startRotationEngine()
}

// adminRotationStatus reports the last rotation attempt for one secret.
//
// It exists because AWS has no API for it — real Secrets Manager surfaces
// rotation progress through CloudTrail and the console, neither of which a
// local emulator has. The AWS-shaped operations stay AWS-shaped; this is the
// console's own endpoint, under the /_overcast/ prefix like every other one.
func (s *Service) adminRotationStatus(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "secretId")
	secretId, err := url.PathUnescape(raw)
	if err != nil {
		secretId = raw
	}
	sec, aerr := s.handler.store.resolveSecret(r.Context(), secretId)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	type versionOut struct {
		VersionId   string   `json:"versionId"`
		Stages      []string `json:"stages"`
		CreatedDate float64  `json:"createdDate"`
	}
	versions := make([]versionOut, 0, len(sec.Versions))
	for _, v := range sec.Versions {
		versions = append(versions, versionOut{VersionId: v.VersionId, Stages: v.Stages, CreatedDate: v.CreatedDate})
	}

	out := map[string]any{
		"name":              sec.Name,
		"arn":               sec.ARN,
		"rotationEnabled":   sec.RotationEnabled,
		"rotationLambdaArn": sec.RotationLambdaARN,
		"rotationRules":     sec.RotationRules,
		"lastRotatedDate":   sec.LastRotatedDate,
		"nextRotationDate":  sec.NextRotationDate,
		"versions":          versions,
		"steps":             rotationSteps,
	}
	if a := sec.LastRotationAttempt; a != nil {
		out["lastAttempt"] = map[string]any{
			"status":             a.Status,
			"step":               a.Step,
			"error":              a.Error,
			"trigger":            a.Trigger,
			"clientRequestToken": a.ClientRequestToken,
			"startedDate":        a.StartedDate,
			"completedDate":      a.CompletedDate,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// Dispatch routes to the correct Secrets Manager handler based on X-Amz-Target.
func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request) {
	if c, opName := codec.FromContext(r.Context()); c != nil && opName != "" {
		if !codec.Supports(s.SupportedProtocols(), c) {
			w.Header().Set("x-emulator-unsupported-protocol", c.Name())
			c.WriteError(w, r, &protocol.AWSError{
				Code:       "UnsupportedProtocol",
				Message:    "Secrets Manager does not support wire protocol " + c.Name() + ".",
				HTTPStatus: http.StatusUnsupportedMediaType,
			})
			return
		}
		// Preserve AWS JSON 1.1 on the existing handler path until JSON
		// wire-byte goldens cover Secrets Manager. CBOR uses typed ops.
		if c.Name() != codec.NameRPCv2CBOR {
			s.dispatchLegacy(w, r, opName)
			return
		}
		if typed, ok := s.handler.typedOp[opName]; ok {
			typed.Invoke(w, r, c)
			return
		}
		// A real Secrets Manager operation Overcast has not implemented gets
		// an honest 501, in the request's own wire format;
		// UnknownOperationException stays for a name AWS does not model (#1645).
		serviceutil.WriteUnhandledOperation(w, r, c, awsapiService, opName, errUnknownOperation(opName))
		return
	}

	target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "secretsmanager.")
	s.dispatchLegacy(w, r, target)
}

func (s *Service) dispatchLegacy(w http.ResponseWriter, r *http.Request, target string) {
	if fn, ok := s.handler.ops[target]; ok {
		fn(w, r)
		return
	}
	// This path serves the JSON families only — Dispatch sends RPC v2 CBOR to
	// the typed operations — so JSON11 writes the bytes WriteJSONError always
	// did for an unknown name.
	serviceutil.WriteUnhandledOperation(w, r, codec.JSON11, awsapiService, target, errUnknownOperation(target))
}

// errUnknownOperation is AWS's answer for a target naming no Secrets Manager
// operation at all.
func errUnknownOperation(opName string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "UnknownOperationException",
		Message:    "Unknown operation: " + opName,
		HTTPStatus: http.StatusBadRequest,
	}
}

// ─── Admin handlers (web console) ──────────────────────────────────────────

func (s *Service) adminListSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, aerr := s.handler.store.listSecrets(r.Context())
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	type secretOut struct {
		Name            string  `json:"name"`
		ARN             string  `json:"arn"`
		Description     string  `json:"description,omitempty"`
		CreatedDate     float64 `json:"createdDate"`
		LastChangedDate float64 `json:"lastChangedDate"`
		RotationEnabled bool    `json:"rotationEnabled"`
	}
	out := make([]secretOut, 0, len(secrets))
	for _, sec := range secrets {
		out = append(out, secretOut{
			Name:            sec.Name,
			ARN:             sec.ARN,
			Description:     sec.Description,
			CreatedDate:     sec.CreatedDate,
			LastChangedDate: sec.LastChangedDate,
			RotationEnabled: sec.RotationEnabled,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"secrets": out})
}

func (s *Service) adminCreateSecret(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	var req struct {
		Name         string `json:"name"`
		SecretString string `json:"secretString"`
		Description  string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		protocol.WriteJSONError(w, r, protocol.ErrInvalidArgument("name is required"))
		return
	}
	ctx := r.Context()
	if _, aerr := s.handler.store.getSecret(ctx, req.Name); aerr == nil {
		protocol.WriteJSONError(w, r, errResourceExists(req.Name))
		return
	}

	now := s.handler.store.now()
	versionId := uuid.New().String()
	arn := secretARN(middleware.RegionFromContext(r.Context(), s.cfg.Region), s.cfg.AccountID, req.Name)

	version := SecretVersion{
		VersionId:    versionId,
		SecretString: req.SecretString,
		Stages:       []string{stageAWSCurrent},
		CreatedDate:  float64(now.Unix()),
	}
	sec := &Secret{
		ARN:              arn,
		Name:             req.Name,
		Description:      req.Description,
		Versions:         []SecretVersion{version},
		CurrentVersionId: versionId,
		CreatedDate:      float64(now.Unix()),
		LastChangedDate:  float64(now.Unix()),
	}
	if aerr := s.handler.store.putSecret(ctx, sec); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	log.Info("secret created (admin)", zap.String("name", req.Name))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": req.Name, "arn": arn, "versionId": versionId,
	})
}

func (s *Service) adminGetSecretValue(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "secretId")
	secretId, err := url.PathUnescape(raw)
	if err != nil {
		secretId = raw
	}
	sec, aerr := s.handler.store.resolveSecret(r.Context(), secretId)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	v := sec.currentVersion()
	if v == nil {
		protocol.WriteJSONError(w, r, errResourceNotFound(secretId))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"secretId":     sec.Name,
		"versionId":    v.VersionId,
		"secretString": v.SecretString,
		"secretBinary": v.SecretBinary,
		"stages":       v.Stages,
	})
}

func (s *Service) adminUpdateSecretValue(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "secretId")
	secretId, err := url.PathUnescape(raw)
	if err != nil {
		secretId = raw
	}
	var req struct {
		SecretString string `json:"secretString"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInvalidArgument("invalid JSON body"))
		return
	}
	ctx := r.Context()
	sec, aerr := s.handler.store.resolveSecret(ctx, secretId)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	version, aerr := s.handler.stageVersion(sec, "", req.SecretString, "", nil)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if aerr := s.handler.store.putSecret(ctx, sec); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"secretId":  sec.Name,
		"versionId": version.VersionId,
	})
}

func (s *Service) adminDeleteSecret(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithRecorder(r.Context())
	raw := chi.URLParam(r, "secretId")
	secretId, err := url.PathUnescape(raw)
	if err != nil {
		secretId = raw
	}
	sec, aerr := s.handler.store.resolveSecret(r.Context(), secretId)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if aerr := s.handler.store.deleteSecret(r.Context(), sec.Name); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	log.Info("secret deleted (admin)", zap.String("name", sec.Name))
	w.WriteHeader(http.StatusNoContent)
}
