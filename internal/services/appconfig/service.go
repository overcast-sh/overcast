// Package appconfig provides a basic emulation of AWS AppConfig.
//
// Control plane implemented operations: CreateApplication, GetApplication,
// ListApplications, UpdateApplication, DeleteApplication, CreateEnvironment,
// GetEnvironment, ListEnvironments, DeleteEnvironment,
// CreateConfigurationProfile, GetConfigurationProfile,
// ListConfigurationProfiles, DeleteConfigurationProfile,
// CreateHostedConfigurationVersion, GetHostedConfigurationVersion,
// ListHostedConfigurationVersions, DeleteHostedConfigurationVersion,
// TagResource, UntagResource, ListTagsForResource.
//
// Data-plane operations (StartConfigurationSession, GetLatestConfiguration)
// are implemented in the sibling appconfigdata package: they are a separate
// AWS model with its own signing name, not two halves of one service.
//
// AWS models AppConfig as restJson1 with no X-Amz-Target namespace, so every
// operation is reached through a REST route and there is no target or RPC
// dispatcher here. The `/applications` tree is registered by the main router
// rather than by RegisterRoutes, because Service Catalog AppRegistry models
// the same paths — see ApplicationsRouter.
package appconfig

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const serviceName = "appconfig"

// maxContentLength is the largest hosted configuration AWS accepts (1 MB).
// Exceeding it is PayloadTooLargeException, which the model binds to
// CreateHostedConfigurationVersion alone.
const maxContentLength = 1 << 20

// maxPageSize is the model's range on the MaxResults member of every AppConfig
// list operation (`smithy.api#range` min 1, max 50).
const maxPageSize = 50

// errStaleLatestVersion reports a Latest-Version-Number header that is not the
// profile's current latest. It is a sentinel rather than an AWSError because it
// crosses the store boundary, where HTTP status codes do not belong.
var errStaleLatestVersion = errors.New("appconfig: stale latest version number")

// ─── Types ────────────────────────────────────────────────────────────────────
//
// These are the AWS shapes, member for member, and are written to the wire as
// they stand. AWS's Application, Environment and ConfigurationProfile carry no
// ARN and no Tags: an ARN identifies these resources in the tag operations but
// is never a response member, and tags are read back through
// ListTagsForResource. Both used to be serialised here, invented.

// Application is AWS's Application shape.
type Application struct {
	ID          string `json:"Id"`
	Name        string `json:"Name"`
	Description string `json:"Description,omitempty"`
}

// Monitor is AWS's Monitor shape, the CloudWatch alarms an environment rolls
// a deployment back on.
type Monitor struct {
	AlarmArn     string `json:"AlarmArn"`
	AlarmRoleArn string `json:"AlarmRoleArn,omitempty"`
}

// Environment is AWS's Environment shape.
type Environment struct {
	ApplicationID string    `json:"ApplicationId"`
	ID            string    `json:"Id"`
	Name          string    `json:"Name"`
	Description   string    `json:"Description,omitempty"`
	State         string    `json:"State"`
	Monitors      []Monitor `json:"Monitors,omitempty"`
}

// Validator is AWS's Validator shape.
type Validator struct {
	Type    string `json:"Type"`
	Content string `json:"Content"`
}

// ConfigurationProfile is AWS's ConfigurationProfile shape.
type ConfigurationProfile struct {
	ApplicationID    string      `json:"ApplicationId"`
	ID               string      `json:"Id"`
	Name             string      `json:"Name"`
	Description      string      `json:"Description,omitempty"`
	LocationUri      string      `json:"LocationUri,omitempty"`
	RetrievalRoleArn string      `json:"RetrievalRoleArn,omitempty"`
	Validators       []Validator `json:"Validators,omitempty"`
	Type             string      `json:"Type,omitempty"`
}

// configurationProfileSummary is AWS's ConfigurationProfileSummary, the item
// shape of ListConfigurationProfiles. It is narrower than the full profile:
// no Description, and validator *types* rather than whole validators.
type configurationProfileSummary struct {
	ApplicationID  string   `json:"ApplicationId"`
	ID             string   `json:"Id"`
	Name           string   `json:"Name"`
	LocationUri    string   `json:"LocationUri,omitempty"`
	ValidatorTypes []string `json:"ValidatorTypes,omitempty"`
	Type           string   `json:"Type,omitempty"`
}

// HostedConfigurationVersion is a stored configuration payload. Content is the
// operation's @httpPayload blob and every other member is bound to a response
// header, so this type is the persisted record rather than a response body —
// see writeHostedConfigurationVersion.
type HostedConfigurationVersion struct {
	ApplicationID          string `json:"ApplicationId"`
	ConfigurationProfileID string `json:"ConfigurationProfileId"`
	VersionNumber          int    `json:"VersionNumber"`
	ContentType            string `json:"ContentType"`
	Description            string `json:"Description,omitempty"`
	VersionLabel           string `json:"VersionLabel,omitempty"`
	Content                string `json:"Content"` // raw bytes stored as a string
}

// hostedConfigurationVersionSummary is AWS's
// HostedConfigurationVersionSummaryList item: the metadata without the content.
type hostedConfigurationVersionSummary struct {
	ApplicationID          string `json:"ApplicationId"`
	ConfigurationProfileID string `json:"ConfigurationProfileId"`
	VersionNumber          int    `json:"VersionNumber"`
	Description            string `json:"Description,omitempty"`
	ContentType            string `json:"ContentType"`
	VersionLabel           string `json:"VersionLabel,omitempty"`
}

// ─── Errors ───────────────────────────────────────────────────────────────────
//
// Codes and statuses come from the pinned model's error shapes:
// BadRequestException 400, ConflictException 409, PayloadTooLargeException 413,
// ResourceNotFoundException 404.

func badRequest(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "BadRequestException", Message: message, HTTPStatus: http.StatusBadRequest}
}

func notFound(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "ResourceNotFoundException", Message: message, HTTPStatus: http.StatusNotFound}
}

func conflict(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "ConflictException", Message: message, HTTPStatus: http.StatusConflict}
}

// ─── Service ──────────────────────────────────────────────────────────────────

// Service implements router.Service for AppConfig.
type Service struct {
	store *appConfigStore
}

// New returns a configured AppConfig Service.
//
// The config and the clock go unused, and both follow from the model: no
// AppConfig shape carries a timestamp, and none carries an ARN, so the service
// never has to name its own region or account. They stay in the signature
// because every service constructor takes them.
func New(_ *config.Config, st state.Store, logger *zap.Logger, _ clock.Clock) *Service {
	return &Service{store: newAppConfigStore(st, serviceutil.NewServiceLogger(logger, serviceName))}
}

func (s *Service) Name() string { return serviceName }

// RegisterRoutes satisfies router.Service and deliberately registers nothing.
//
// Both of AppConfig's path spaces are shared with another service and are
// therefore owned by the main router, which picks the handler at request time:
// `/applications` with Service Catalog AppRegistry (dispatched on the SigV4
// credential scope) and `/tags/{ResourceArn}` with Pipes, EKS, Scheduler and
// API Gateway (dispatched on the ARN). Registering either here would race those
// services for the same chi patterns, where the last registration silently
// wins. See ApplicationsRouter and TagsRouter.
//
// PathPrefixes is not implemented for the same reason: `/applications` is not
// AppConfig's to claim, and claiming it would collide with the main router's
// dispatcher when a subset test excludes this service.
func (s *Service) RegisterRoutes(chi.Router) {}

// ApplicationsRouter returns the `/applications` sub-tree, for the main router
// to dispatch to when a request carries AppConfig's SigV4 credential scope.
//
// Every route is registered at the method and URI the pinned model binds:
//
//	CreateApplication                 POST   /applications
//	ListApplications                  GET    /applications
//	GetApplication                    GET    /applications/{ApplicationId}
//	UpdateApplication                 PATCH  /applications/{ApplicationId}
//	DeleteApplication                 DELETE /applications/{ApplicationId}
//	CreateEnvironment                 POST   /applications/{ApplicationId}/environments
//	ListEnvironments                  GET    /applications/{ApplicationId}/environments
//	GetEnvironment                    GET    /applications/{ApplicationId}/environments/{EnvironmentId}
//	DeleteEnvironment                 DELETE /applications/{ApplicationId}/environments/{EnvironmentId}
//	CreateConfigurationProfile        POST   /applications/{ApplicationId}/configurationprofiles
//	ListConfigurationProfiles         GET    /applications/{ApplicationId}/configurationprofiles
//	GetConfigurationProfile           GET    /applications/{ApplicationId}/configurationprofiles/{ConfigurationProfileId}
//	DeleteConfigurationProfile        DELETE /applications/{ApplicationId}/configurationprofiles/{ConfigurationProfileId}
//	CreateHostedConfigurationVersion  POST   …/hostedconfigurationversions
//	ListHostedConfigurationVersions   GET    …/hostedconfigurationversions
//	GetHostedConfigurationVersion     GET    …/hostedconfigurationversions/{VersionNumber}
//	DeleteHostedConfigurationVersion  DELETE …/hostedconfigurationversions/{VersionNumber}
func (s *Service) ApplicationsRouter() chi.Router {
	r := chi.NewRouter()

	r.Post("/", s.createApplication)
	r.Get("/", s.listApplications)
	r.Get("/{ApplicationId}", s.getApplication)
	r.Patch("/{ApplicationId}", s.updateApplication)
	r.Delete("/{ApplicationId}", s.deleteApplication)

	r.Post("/{ApplicationId}/environments", s.createEnvironment)
	r.Get("/{ApplicationId}/environments", s.listEnvironments)
	r.Get("/{ApplicationId}/environments/{EnvironmentId}", s.getEnvironment)
	r.Delete("/{ApplicationId}/environments/{EnvironmentId}", s.deleteEnvironment)

	r.Post("/{ApplicationId}/configurationprofiles", s.createConfigurationProfile)
	r.Get("/{ApplicationId}/configurationprofiles", s.listConfigurationProfiles)
	r.Get("/{ApplicationId}/configurationprofiles/{ConfigurationProfileId}", s.getConfigurationProfile)
	r.Delete("/{ApplicationId}/configurationprofiles/{ConfigurationProfileId}", s.deleteConfigurationProfile)

	const hcv = "/{ApplicationId}/configurationprofiles/{ConfigurationProfileId}/hostedconfigurationversions"
	r.Post(hcv, s.createHostedConfigurationVersion)
	r.Get(hcv, s.listHostedConfigurationVersions)
	r.Get(hcv+"/{VersionNumber}", s.getHostedConfigurationVersion)
	r.Delete(hcv+"/{VersionNumber}", s.deleteHostedConfigurationVersion)

	return r
}

// TagsRouter returns AppConfig's handlers for /tags/{ResourceArn}, for the main
// router's ARN-keyed tag dispatcher. Without it an AppConfig ARN would fall to
// the dispatcher's fallback — historically API Gateway's service-agnostic tag
// store, which answered 200 with someone else's tags; since #976 the router's
// generated 501.
func (s *Service) TagsRouter() chi.Router {
	r := chi.NewRouter()
	r.Post("/*", s.tagResource)
	r.Delete("/*", s.untagResource)
	r.Get("/*", s.listTagsForResource)
	return r
}

// ─── Cross-service accessors (used by appconfigdata) ──────────────────────────

// ResolveApplication finds an application by ID or name.
func (s *Service) ResolveApplication(ctx context.Context, identifier string) (*Application, bool) {
	if a, ok := s.store.getApp(ctx, identifier); ok {
		return a, true
	}
	apps, err := s.store.listApps(ctx)
	if err != nil {
		return nil, false
	}
	for i := range apps {
		if apps[i].Name == identifier {
			return &apps[i], true
		}
	}
	return nil, false
}

// ResolveEnvironment finds an environment by ID or name within the given app.
func (s *Service) ResolveEnvironment(ctx context.Context, appID, identifier string) (*Environment, bool) {
	envs, err := s.store.listEnvs(ctx, appID)
	if err != nil {
		return nil, false
	}
	for i := range envs {
		if envs[i].ID == identifier || envs[i].Name == identifier {
			return &envs[i], true
		}
	}
	return nil, false
}

// ResolveProfile finds a configuration profile by ID or name within the given app.
func (s *Service) ResolveProfile(ctx context.Context, appID, identifier string) (*ConfigurationProfile, bool) {
	profiles, err := s.store.listProfiles(ctx, appID)
	if err != nil {
		return nil, false
	}
	for i := range profiles {
		if profiles[i].ID == identifier || profiles[i].Name == identifier {
			return &profiles[i], true
		}
	}
	return nil, false
}

// LatestVersionNumber returns the latest hosted config version number for a
// profile (0 = none).
func (s *Service) LatestVersionNumber(ctx context.Context, appID, profID string) (int, error) {
	return s.store.latestVersionNumber(ctx, appID, profID)
}

// GetHostedConfigVersionByNum retrieves a specific hosted config version.
func (s *Service) GetHostedConfigVersionByNum(ctx context.Context, appID, profID string, version int) (*HostedConfigurationVersion, bool) {
	return s.store.getHCV(ctx, appID, profID, version)
}

// ─── Application handlers ─────────────────────────────────────────────────────

func (s *Service) createApplication(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string            `json:"Name"`
		Description string            `json:"Description"`
		Tags        map[string]string `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		protocol.WriteJSONError(w, r, badRequest("Name is required"))
		return
	}

	app := &Application{ID: shortID(), Name: req.Name, Description: req.Description}
	if err := s.store.putApp(r.Context(), app); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !s.applyInlineTags(w, r, applicationTagKey(app.ID), req.Tags) {
		return
	}
	protocol.WriteJSON(w, r, http.StatusCreated, app)
}

func (s *Service) getApplication(w http.ResponseWriter, r *http.Request) {
	app, ok := s.store.getApp(r.Context(), chi.URLParam(r, "ApplicationId"))
	if !ok {
		protocol.WriteJSONError(w, r, notFound("Application not found"))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, app)
}

func (s *Service) updateApplication(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	app, ok := s.store.getApp(r.Context(), appID)
	if !ok {
		protocol.WriteJSONError(w, r, notFound("Application not found"))
		return
	}
	// Both members are optional on UpdateApplication, so a member the caller
	// omitted has to leave the stored value alone. Pointers are what tells
	// "absent" from "set to empty", which a plain string cannot.
	var req struct {
		Name        *string `json:"Name"`
		Description *string `json:"Description"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		if *req.Name == "" {
			protocol.WriteJSONError(w, r, badRequest("Name is required"))
			return
		}
		app.Name = *req.Name
	}
	if req.Description != nil {
		app.Description = *req.Description
	}
	if err := s.store.putApp(r.Context(), app); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, app)
}

func (s *Service) listApplications(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.listApps(r.Context())
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	writePage(w, r, apps)
}

func (s *Service) deleteApplication(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	if _, ok := s.store.getApp(r.Context(), appID); !ok {
		protocol.WriteJSONError(w, r, notFound("Application not found"))
		return
	}
	if err := s.store.deleteApp(r.Context(), appID); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Environment handlers ─────────────────────────────────────────────────────

func (s *Service) createEnvironment(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	if _, ok := s.store.getApp(r.Context(), appID); !ok {
		protocol.WriteJSONError(w, r, notFound("Application not found"))
		return
	}
	var req struct {
		Name        string            `json:"Name"`
		Description string            `json:"Description"`
		Monitors    []Monitor         `json:"Monitors"`
		Tags        map[string]string `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		protocol.WriteJSONError(w, r, badRequest("Name is required"))
		return
	}

	env := &Environment{
		ApplicationID: appID,
		ID:            shortID(),
		Name:          req.Name,
		Description:   req.Description,
		// AWS reports a new environment as ready until a deployment moves it
		// on; Overcast emulates no deployments, so it stays there.
		State:    "READY_FOR_DEPLOYMENT",
		Monitors: req.Monitors,
	}
	if err := s.store.putEnv(r.Context(), env); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !s.applyInlineTags(w, r, environmentTagKey(appID, env.ID), req.Tags) {
		return
	}
	protocol.WriteJSON(w, r, http.StatusCreated, env)
}

func (s *Service) getEnvironment(w http.ResponseWriter, r *http.Request) {
	env, ok := s.store.getEnv(r.Context(), chi.URLParam(r, "ApplicationId"), chi.URLParam(r, "EnvironmentId"))
	if !ok {
		protocol.WriteJSONError(w, r, notFound("Environment not found"))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, env)
}

func (s *Service) listEnvironments(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	if _, ok := s.store.getApp(r.Context(), appID); !ok {
		protocol.WriteJSONError(w, r, notFound("Application not found"))
		return
	}
	envs, err := s.store.listEnvs(r.Context(), appID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	writePage(w, r, envs)
}

func (s *Service) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	envID := chi.URLParam(r, "EnvironmentId")
	if _, ok := s.store.getEnv(r.Context(), appID, envID); !ok {
		protocol.WriteJSONError(w, r, notFound("Environment not found"))
		return
	}
	if err := s.store.deleteEnv(r.Context(), appID, envID); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Configuration profile handlers ───────────────────────────────────────────

func (s *Service) createConfigurationProfile(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	if _, ok := s.store.getApp(r.Context(), appID); !ok {
		protocol.WriteJSONError(w, r, notFound("Application not found"))
		return
	}
	var req struct {
		Name             string            `json:"Name"`
		Description      string            `json:"Description"`
		LocationUri      string            `json:"LocationUri"`
		RetrievalRoleArn string            `json:"RetrievalRoleArn"`
		Validators       []Validator       `json:"Validators"`
		Type             string            `json:"Type"`
		Tags             map[string]string `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	// Name and LocationUri are the shape's two required members.
	if req.Name == "" {
		protocol.WriteJSONError(w, r, badRequest("Name is required"))
		return
	}
	if req.LocationUri == "" {
		protocol.WriteJSONError(w, r, badRequest("LocationUri is required"))
		return
	}

	prof := &ConfigurationProfile{
		ApplicationID:    appID,
		ID:               shortID(),
		Name:             req.Name,
		Description:      req.Description,
		LocationUri:      req.LocationUri,
		RetrievalRoleArn: req.RetrievalRoleArn,
		Validators:       req.Validators,
		Type:             req.Type,
	}
	if err := s.store.putProfile(r.Context(), prof); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !s.applyInlineTags(w, r, profileTagKey(appID, prof.ID), req.Tags) {
		return
	}
	protocol.WriteJSON(w, r, http.StatusCreated, prof)
}

func (s *Service) getConfigurationProfile(w http.ResponseWriter, r *http.Request) {
	prof, ok := s.store.getProfile(r.Context(),
		chi.URLParam(r, "ApplicationId"), chi.URLParam(r, "ConfigurationProfileId"))
	if !ok {
		protocol.WriteJSONError(w, r, notFound("Configuration profile not found"))
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, prof)
}

func (s *Service) listConfigurationProfiles(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	if _, ok := s.store.getApp(r.Context(), appID); !ok {
		protocol.WriteJSONError(w, r, notFound("Application not found"))
		return
	}
	profiles, err := s.store.listProfiles(r.Context(), appID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	// ListConfigurationProfiles binds a `type` query parameter that filters on
	// the profile's Type. Ignoring it answered with profiles the caller asked
	// to exclude.
	wantType := r.URL.Query().Get("type")
	items := make([]configurationProfileSummary, 0, len(profiles))
	for _, p := range profiles {
		if wantType != "" && p.Type != wantType {
			continue
		}
		items = append(items, configurationProfileSummary{
			ApplicationID:  p.ApplicationID,
			ID:             p.ID,
			Name:           p.Name,
			LocationUri:    p.LocationUri,
			ValidatorTypes: validatorTypes(p.Validators),
			Type:           p.Type,
		})
	}
	writePage(w, r, items)
}

func (s *Service) deleteConfigurationProfile(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	profID := chi.URLParam(r, "ConfigurationProfileId")
	if _, ok := s.store.getProfile(r.Context(), appID, profID); !ok {
		protocol.WriteJSONError(w, r, notFound("Configuration profile not found"))
		return
	}
	if err := s.store.deleteProfile(r.Context(), appID, profID); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validatorTypes(validators []Validator) []string {
	if len(validators) == 0 {
		return nil
	}
	types := make([]string, 0, len(validators))
	for _, v := range validators {
		types = append(types, v.Type)
	}
	return types
}

// ─── Hosted configuration version handlers ────────────────────────────────────

// createHostedConfigurationVersion stores raw configuration content.
//
// The model binds this operation as a binary upload, not a JSON envelope:
// Content is an @httpPayload blob, Content-Type is a required header, and
// Description, VersionLabel and Latest-Version-Number are headers too. The
// response is the same shape — the content back as the payload, the metadata
// in headers.
func (s *Service) createHostedConfigurationVersion(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	profID := chi.URLParam(r, "ConfigurationProfileId")
	if _, ok := s.store.getProfile(r.Context(), appID, profID); !ok {
		protocol.WriteJSONError(w, r, notFound("Configuration profile not found"))
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		protocol.WriteJSONError(w, r, badRequest("Content-Type is required"))
		return
	}
	expected, aerr := optionalIntHeader(r, "Latest-Version-Number")
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxContentLength+1))
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if len(body) == 0 {
		protocol.WriteJSONError(w, r, badRequest("Content is required"))
		return
	}
	if len(body) > maxContentLength {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "PayloadTooLargeException",
			Message:    "Configuration content exceeds the maximum size of 1 MB.",
			HTTPStatus: http.StatusRequestEntityTooLarge,
		})
		return
	}

	versionNum, err := s.store.nextVersionNumber(r.Context(), appID, profID, expected)
	switch {
	case errors.Is(err, errStaleLatestVersion):
		protocol.WriteJSONError(w, r, conflict("The Latest-Version-Number is not the latest version of the configuration profile."))
		return
	case err != nil:
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	hcv := &HostedConfigurationVersion{
		ApplicationID:          appID,
		ConfigurationProfileID: profID,
		VersionNumber:          versionNum,
		ContentType:            contentType,
		Description:            r.Header.Get("Description"),
		VersionLabel:           r.Header.Get("VersionLabel"),
		Content:                string(body),
	}
	if err := s.store.putHCV(r.Context(), hcv); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	writeHostedConfigurationVersion(w, hcv, http.StatusCreated)
}

func (s *Service) getHostedConfigurationVersion(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	profID := chi.URLParam(r, "ConfigurationProfileId")
	version, aerr := versionNumberParam(r)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	hcv, ok := s.store.getHCV(r.Context(), appID, profID, version)
	if !ok {
		protocol.WriteJSONError(w, r, notFound("Hosted configuration version not found"))
		return
	}
	writeHostedConfigurationVersion(w, hcv, http.StatusOK)
}

func (s *Service) listHostedConfigurationVersions(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	profID := chi.URLParam(r, "ConfigurationProfileId")
	if _, ok := s.store.getProfile(r.Context(), appID, profID); !ok {
		protocol.WriteJSONError(w, r, notFound("Configuration profile not found"))
		return
	}
	versions, err := s.store.listHCVs(r.Context(), appID, profID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	// The model binds a `version_label` query filter, and the item shape
	// carries metadata without the content.
	wantLabel := r.URL.Query().Get("version_label")
	items := make([]hostedConfigurationVersionSummary, 0, len(versions))
	for _, v := range versions {
		if wantLabel != "" && !matchesVersionLabelFilter(v.VersionLabel, wantLabel) {
			continue
		}
		items = append(items, hostedConfigurationVersionSummary{
			ApplicationID:          v.ApplicationID,
			ConfigurationProfileID: v.ConfigurationProfileID,
			VersionNumber:          v.VersionNumber,
			Description:            v.Description,
			ContentType:            v.ContentType,
			VersionLabel:           v.VersionLabel,
		})
	}
	writePage(w, r, items)
}

// matchesVersionLabelFilter applies AWS's documented version_label matching:
// a filter ending in "*" matches by prefix (the model's wildcard), and any
// other filter matches only an exact VersionLabel
// (API_ListHostedConfigurationVersions.html).
func matchesVersionLabelFilter(label, filter string) bool {
	if prefix, ok := strings.CutSuffix(filter, "*"); ok {
		return strings.HasPrefix(label, prefix)
	}
	return label == filter
}

func (s *Service) deleteHostedConfigurationVersion(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "ApplicationId")
	profID := chi.URLParam(r, "ConfigurationProfileId")
	version, aerr := versionNumberParam(r)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if _, ok := s.store.getHCV(r.Context(), appID, profID, version); !ok {
		protocol.WriteJSONError(w, r, notFound("Hosted configuration version not found"))
		return
	}
	if err := s.store.deleteHCV(r.Context(), appID, profID, version); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeHostedConfigurationVersion writes the response both the create and the
// get bind: every metadata member as a header, the content as the payload.
func writeHostedConfigurationVersion(w http.ResponseWriter, hcv *HostedConfigurationVersion, status int) {
	w.Header().Set("Content-Type", hcv.ContentType)
	w.Header().Set("Application-Id", hcv.ApplicationID)
	w.Header().Set("Configuration-Profile-Id", hcv.ConfigurationProfileID)
	w.Header().Set("Version-Number", strconv.Itoa(hcv.VersionNumber))
	if hcv.Description != "" {
		w.Header().Set("Description", hcv.Description)
	}
	if hcv.VersionLabel != "" {
		w.Header().Set("VersionLabel", hcv.VersionLabel)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(hcv.Content))
}

// versionNumberParam reads the {VersionNumber} path label, which the model
// binds as an integer.
func versionNumberParam(r *http.Request) (int, *protocol.AWSError) {
	version, err := strconv.Atoi(chi.URLParam(r, "VersionNumber"))
	if err != nil {
		return 0, badRequest("VersionNumber must be an integer")
	}
	return version, nil
}

// optionalIntHeader reads an integer-valued header, returning nil when it is
// absent so a caller that omitted it is not treated as having sent zero.
func optionalIntHeader(r *http.Request, name string) (*int, *protocol.AWSError) {
	raw := r.Header.Get(name)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil, badRequest(name + " must be an integer")
	}
	return &value, nil
}

// ─── Pagination ───────────────────────────────────────────────────────────────

// writePage answers a list operation with the modeled {Items, NextToken}
// envelope. Every AppConfig list binds MaxResults to `max_results` and
// NextToken to `next_token`, and caps MaxResults at 50; a token the emulator
// did not issue is BadRequestException rather than a silent restart from the
// first page.
func writePage[T any](w http.ResponseWriter, r *http.Request, items []T) {
	page, err := serviceutil.Paginate(items,
		serviceutil.QueryInt(r, "max_results", 0),
		r.URL.Query().Get("next_token"),
		serviceutil.PaginateOptions{DefaultLimit: maxPageSize, MaxLimit: maxPageSize})
	if err != nil {
		protocol.WriteJSONError(w, r, badRequest("The specified next_token is not valid."))
		return
	}
	body := map[string]any{"Items": page.Items}
	if page.NextToken != "" {
		body["NextToken"] = page.NextToken
	}
	protocol.WriteJSON(w, r, http.StatusOK, body)
}

// ─── Tags ─────────────────────────────────────────────────────────────────────

// appConfigTagCfg tunes tag-validation error codes to match AppConfig, whose
// model binds BadRequestException to all three tag operations.
var appConfigTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "BadRequestException",
	InvalidCode:     "BadRequestException",
	ExceededMessage: "Too many tags.",
}

func applicationTagKey(appID string) string { return "application/" + appID }

func environmentTagKey(appID, envID string) string {
	return "environment/" + appID + "/" + envID
}

func profileTagKey(appID, profID string) string {
	return "configurationprofile/" + appID + "/" + profID
}

// resolveTagTarget maps an AppConfig resource ARN to the key its tags are
// stored under, confirming the resource exists on the way: AWS answers
// ResourceNotFoundException for an ARN that names nothing, where API Gateway's
// service-agnostic store — which used to answer these requests — returns an
// empty map for any ARN at all.
//
// Tags are keyed by resource identity rather than by the ARN itself so that
// they follow the records, which are not region-scoped. See appConfigStore.
func (s *Service) resolveTagTarget(ctx context.Context, arn string) (string, *protocol.AWSError) {
	segments := arnResourceSegments(arn)
	if len(segments) < 2 || segments[0] != "application" {
		return "", notFound("Resource not found")
	}
	appID := segments[1]

	switch {
	case len(segments) == 2:
		if _, ok := s.store.getApp(ctx, appID); ok {
			return applicationTagKey(appID), nil
		}
	case len(segments) == 4 && segments[2] == "environment":
		if _, ok := s.store.getEnv(ctx, appID, segments[3]); ok {
			return environmentTagKey(appID, segments[3]), nil
		}
	case len(segments) == 4 && segments[2] == "configurationprofile":
		if _, ok := s.store.getProfile(ctx, appID, segments[3]); ok {
			return profileTagKey(appID, segments[3]), nil
		}
	case len(segments) == 6 && segments[4] == "hostedconfigurationversion":
		// AWS does not tag hosted configuration versions; the resource exists
		// but is not a tagging target.
		return "", badRequest("Tagging hosted configuration versions is not supported")
	}
	return "", notFound("Resource not found")
}

// arnResourceSegments splits the resource part of an AppConfig ARN
// ("application/{id}[/environment/{id}]") into its segments. It returns nil for
// anything that is not a six-field ARN.
func arnResourceSegments(arn string) []string {
	fields := strings.SplitN(arn, ":", 6)
	if len(fields) < 6 {
		return nil
	}
	return strings.Split(fields[5], "/")
}

// tagResourceArn reads the {ResourceArn} path label. The router mounts these
// handlers on a wildcard because an ARN contains slashes, and SDKs percent-
// encode it.
func tagResourceArn(r *http.Request) string {
	arn := chi.URLParam(r, "*")
	if decoded, err := url.PathUnescape(arn); err == nil {
		return decoded
	}
	return arn
}

func (s *Service) tagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tags map[string]string `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Tags) == 0 {
		protocol.WriteJSONError(w, r, badRequest("Tags is required"))
		return
	}
	key, aerr := s.resolveTagTarget(r.Context(), tagResourceArn(r))
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if _, aerr := serviceutil.ApplyStoreTags(r.Context(), s.store.tags, key, req.Tags, appConfigTagCfg); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) untagResource(w http.ResponseWriter, r *http.Request) {
	keys := r.URL.Query()["tagKeys"]
	if len(keys) == 0 {
		protocol.WriteJSONError(w, r, badRequest("tagKeys is required"))
		return
	}
	key, aerr := s.resolveTagTarget(r.Context(), tagResourceArn(r))
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if _, aerr := serviceutil.RemoveStoreTags(r.Context(), s.store.tags, key, keys); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) listTagsForResource(w http.ResponseWriter, r *http.Request) {
	key, aerr := s.resolveTagTarget(r.Context(), tagResourceArn(r))
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	tags, aerr := s.store.tags.Load(r.Context(), key)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]map[string]string{"Tags": tags})
}

// applyInlineTags stores the Tags map the three create operations accept in
// their body, and reports whether the request may continue. A rejected tag map
// fails the create rather than leaving the resource half-tagged.
func (s *Service) applyInlineTags(w http.ResponseWriter, r *http.Request, key string, tags map[string]string) bool {
	if len(tags) == 0 {
		return true
	}
	if _, aerr := serviceutil.ApplyStoreTags(r.Context(), s.store.tags, key, tags, appConfigTagCfg); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return false
	}
	return true
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// shortID generates an ID in AppConfig's format: the model constrains Id to
// `^[a-z0-9]{4,7}$`, and AWS issues seven lowercase hex characters. A UUID's
// first seven characters are hex, so the prefix satisfies the pattern.
func shortID() string { return uuid.NewString()[:7] }
