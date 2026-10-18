package lambdaweb

// The codec-agnostic implementation of every lambda-web operation. The REST
// adapters in routes.go lift the @http bindings (path labels, query string,
// body) into these request structs and hand them over; nothing else decides
// behaviour.

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// tagCfg: ValidationException is the model's client-input error for tags.
// Tag keys are 1–128 characters and values 0–256, with the usual character
// set (web-functions-configuration-tags.md).
var tagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "ValidationException",
	ExceededMessage: "The number of tags exceeds the limit of 50 tags.",
	InvalidCode:     "ValidationException",
}

// ─── Resolution ───────────────────────────────────────────────────────────────

// resolveFunction loads the function a name or a web-function ARN names, in
// the caller's region. An ARN of another region or account names a function
// this endpoint does not hold, so it is not found here.
func (s *Service) resolveFunction(ctx context.Context, ref string) (*webFunction, *protocol.AWSError) {
	region := s.regionOf(ctx)
	name := ref
	if strings.HasPrefix(ref, "arn:") {
		p, ok := parseARN(ref)
		if !ok || p.Kind != "" {
			return nil, validationError("%q is not a valid web function ARN.", ref)
		}
		if p.Region != region || p.Account != s.accountID() {
			return nil, notFound("Web function not found: %s", ref)
		}
		name = p.Function
	}
	if name == "" {
		return nil, validationError("functionName is required.")
	}
	fn, found, aerr := s.loadFunction(ctx, region, name)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, notFound("Web function not found: %s", s.functionARN(region, name))
	}
	return fn, nil
}

// childRef reduces an endpoint name, revision ID or child ARN to its bare
// name, checking that an ARN names a child of kind under function fn.
func childRef(ref, kind string, fn *webFunction) (string, *protocol.AWSError) {
	if !strings.HasPrefix(ref, "arn:") {
		return ref, nil
	}
	p, ok := parseARN(ref)
	if !ok || p.Kind != kind || p.Function != fn.Name || p.Region != fn.Region {
		return "", validationError("%q is not a valid %s ARN for web function %s.", ref, kind, fn.Name)
	}
	return p.Child, nil
}

func (s *Service) resolveEndpoint(ctx context.Context, fn *webFunction, ref string) (*endpoint, *protocol.AWSError) {
	name, aerr := childRef(ref, "endpoint", fn)
	if aerr != nil {
		return nil, aerr
	}
	ep, found, aerr := s.loadEndpoint(ctx, fn.Region, fn.Name, name)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, notFound("Endpoint not found: %s", s.endpointARN(fn.Region, fn.Name, name))
	}
	return ep, nil
}

func (s *Service) resolveRevision(ctx context.Context, fn *webFunction, ref string) (*revision, *protocol.AWSError) {
	id, aerr := childRef(ref, "revision", fn)
	if aerr != nil {
		return nil, aerr
	}
	rev, found, aerr := s.loadRevision(ctx, fn.Region, fn.Name, id)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, notFound("Revision not found: %s", s.revisionARN(fn.Region, fn.Name, id))
	}
	return rev, nil
}

// resolveResourceARN is resolveFunction for the operations that take a
// resource ARN (tags and policies), which the model's pattern limits to a
// web function's own ARN.
func (s *Service) resolveResourceARN(ctx context.Context, arn string) (*webFunction, *protocol.AWSError) {
	p, ok := parseARN(arn)
	if !ok || p.Kind != "" {
		return nil, validationError("%q is not a valid web function ARN.", arn)
	}
	return s.resolveFunction(ctx, arn)
}

// ─── Views ────────────────────────────────────────────────────────────────────

func stateReason(st, kind string) string {
	switch st {
	case statePending:
		return "The " + kind + " is being created."
	case stateDeleting:
		return "The " + kind + " is being deleted."
	case stateFailed:
		return "The " + kind + " failed."
	}
	return "The " + kind + " is active."
}

// reasonOf is the stored reason for a state, or the generic text for it.
func reasonOf(stored, st, kind string) string {
	if stored != "" {
		return stored
	}
	return stateReason(st, kind)
}

// stateOf is what a web function reports. A function is Active from the moment
// it exists, but the response to the call that creates it reports Pending, as
// AWS's does: that is the state clients of the create call are written to wait
// out. (Revisions and endpoints report their stored state: lifecycle.go.)
func stateOf(pending bool) string {
	if pending {
		return statePending
	}
	return stateActive
}

func (s *Service) functionSummary(fn *webFunction, pending bool) functionSummary {
	st := stateOf(pending)
	return functionSummary{
		FunctionName: fn.Name, FunctionARN: fn.ARN, State: st, StateReason: stateReason(st, "web function"),
		CreatedAt: timestamp(fn.CreatedAt), UpdatedAt: timestamp(fn.UpdatedAt),
	}
}

func (s *Service) revisionSummary(rev *revision) revisionSummary {
	st := revState(rev)
	return revisionSummary{
		RevisionARN: rev.ARN, RevisionID: rev.ID, Description: rev.Description, State: st,
		StateReason: reasonOf(rev.StateReason, st, "revision"), CreatedAt: timestamp(rev.CreatedAt),
	}
}

func weightsOrEmpty(w []revisionWt) []revisionWt {
	if w == nil {
		return []revisionWt{}
	}
	return w
}

func (s *Service) endpointSummary(ctx context.Context, ep *endpoint) endpointSummary {
	st := epState(ep)
	return endpointSummary{
		EndpointARN: ep.ARN, EndpointName: ep.Name, Description: ep.Description, EndpointType: ep.Type,
		DomainName: s.domainOf(ctx, ep), AuthType: ep.AuthType, AutoDeploymentMode: ep.AutoDeploy,
		RevisionWeights: weightsOrEmpty(ep.Weights), Regions: ep.Regions, ScalingConfig: ep.Scaling,
		ThrottleConfig: ep.Throttle, State: st, StateReason: reasonOf(ep.StateReason, st, "endpoint"),
		UpdateStatus: epUpdateStatus(ep), UpdateStatusReason: ep.UpdateStatusReason,
		CreatedAt: timestamp(ep.CreatedAt), UpdatedAt: timestamp(ep.UpdatedAt),
	}
}

// endpointView is the full endpoint shape Create, Get and UpdateWebFunctionEndpoint
// share: the summary plus the function's ARN and the per-Region endpoints.
type endpointView struct {
	FunctionARN string `json:"functionArn"`
	endpointSummary
	RegionalEndpoints map[string]regionalEndpoint `json:"regionalEndpoints"`
}

// endpointView renders ep. Every Region of a MultiRegion or PerRegion endpoint
// is applied together here, so each carries the endpoint's own state and update
// status.
func (s *Service) endpointView(ctx context.Context, fn *webFunction, ep *endpoint) *endpointView {
	sum := s.endpointSummary(ctx, ep)
	regional := make(map[string]regionalEndpoint, len(ep.Regions))
	for _, r := range ep.Regions {
		re := regionalEndpoint{
			AuthType: ep.AuthType, RevisionWeights: weightsOrEmpty(ep.Weights), ScalingConfig: ep.Scaling,
			ThrottleConfig: ep.Throttle, State: sum.State, StateReason: sum.StateReason,
			UpdateStatus: sum.UpdateStatus, UpdateStatusReason: sum.UpdateStatusReason,
		}
		if ep.Type != endpointMultiRegion {
			re.DomainName = s.regionDomainOf(ctx, ep, r)
		}
		regional[r] = re
	}
	return &endpointView{FunctionARN: fn.ARN, endpointSummary: sum, RegionalEndpoints: regional}
}

// revisionView is the full revision shape Create and GetWebFunctionRevision share.
type revisionView struct {
	FunctionARN string          `json:"functionArn"`
	RevisionARN string          `json:"revisionArn"`
	RevisionID  string          `json:"revisionId"`
	Description string          `json:"description,omitempty"`
	KMSKeyARN   string          `json:"kmsKeyArn,omitempty"`
	BuildConfig buildConfig     `json:"buildConfig"`
	Service     serviceConfig   `json:"serviceConfig"`
	State       string          `json:"state"`
	StateReason string          `json:"stateReason"`
	Errors      []revisionError `json:"errors,omitempty"`
	CreatedAt   string          `json:"createdAt"`
}

func (s *Service) revisionView(fn *webFunction, rev *revision) *revisionView {
	st := revState(rev)
	return &revisionView{
		FunctionARN: fn.ARN, RevisionARN: rev.ARN, RevisionID: rev.ID, Description: rev.Description,
		KMSKeyARN: rev.KMSKeyARN, BuildConfig: rev.BuildConfig, Service: rev.Service, State: st,
		StateReason: reasonOf(rev.StateReason, st, "revision"), Errors: rev.Errors, CreatedAt: timestamp(rev.CreatedAt),
	}
}

// ─── Pagination ───────────────────────────────────────────────────────────────

// The model's NextToken is a 36-character hex-and-hyphen string
// (^[a-f0-9-]{36}$), so a token is the next index laid out as one.
const tokenFormat = "7a3c1e55-0000-4000-8000-%012x"

func encodeToken(next int) string { return fmt.Sprintf(tokenFormat, next) }

func decodeToken(token string) (int, bool) {
	const prefix = "7a3c1e55-0000-4000-8000-"
	if len(token) != len(prefix)+12 || !strings.HasPrefix(token, prefix) {
		return 0, false
	}
	n, err := strconv.ParseUint(token[len(prefix):], 16, 32)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

// page returns the maxResults items of items that follow token, and the token
// for the next page when there are more.
func page[T any](items []T, maxResults *int, token string) ([]T, string, *protocol.AWSError) {
	size := defaultListPageSize
	if maxResults != nil {
		if *maxResults < 1 || *maxResults > 50 {
			return nil, "", validationError("maxResults must be between 1 and 50.")
		}
		size = *maxResults
	}
	start := 0
	if token != "" {
		n, ok := decodeToken(token)
		if !ok || n > len(items) {
			return nil, "", errBadToken
		}
		start = n
	}
	end := min(start+size, len(items))
	next := ""
	if end < len(items) {
		next = encodeToken(end)
	}
	return items[start:end], next, nil
}

// ─── Events ───────────────────────────────────────────────────────────────────

func (s *Service) publish(ctx context.Context, typ events.Type, fn *webFunction, revisionID, endpointName string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, events.Event{
		Type: typ, Time: s.clk.Now(), Source: serviceName,
		Payload: events.LambdaWebPayload{
			Function: fn.Name, FunctionARN: fn.ARN, Region: fn.Region, Revision: revisionID, Endpoint: endpointName,
		},
	})
}

// ─── Routing ─────────────────────────────────────────────────────────────────

// validateWeights checks revision weights against the function's revisions:
// one or two distinct, existing revisions with weights of 1–100 that total 100.
// A revision with a network connector serves HomeRegion endpoints only.
func (s *Service) validateWeights(ctx context.Context, fn *webFunction, epType string, weights []revisionWt) ([]revisionWt, *protocol.AWSError) {
	if len(weights) < 1 || len(weights) > maxRevisionWeightCount {
		return nil, validationError("revisionWeights must list one or two revisions.")
	}
	out := make([]revisionWt, 0, len(weights))
	seen := map[string]bool{}
	total := 0
	for _, w := range weights {
		if w.Weight < 1 || w.Weight > 100 {
			return nil, validationError("Each revision weight must be between 1 and 100.")
		}
		id, aerr := childRef(w.RevisionID, "revision", fn)
		if aerr != nil {
			return nil, aerr
		}
		if seen[id] {
			return nil, validationError("revisionWeights must not list a revision twice.")
		}
		seen[id] = true
		rev, found, aerr := s.loadRevision(ctx, fn.Region, fn.Name, id)
		if aerr != nil {
			return nil, aerr
		}
		if !found {
			return nil, notFound("Revision not found: %s", s.revisionARN(fn.Region, fn.Name, id))
		}
		// Per AWS docs (web-functions-deployment.md, "Revision weights"): each
		// revision in the weights must be Active when you set them.
		if st := revState(rev); st != stateActive {
			return nil, validationError("Revision %s is %s: a revision must be Active to receive traffic.", id, st)
		}
		if rev.Service.EgressNetworkConnectorARN != "" && epType != endpointHomeRegion {
			return nil, validationError("Revision %s uses a network connector, so it supports HomeRegion endpoints only.", id)
		}
		total += w.Weight
		out = append(out, revisionWt{RevisionID: id, Weight: w.Weight})
	}
	if total != 100 {
		return nil, validationError("Revision weights must total 100.")
	}
	return out, nil
}

// latestWeights routes all traffic to the newest Active revision of revs
// (oldest first) that can serve a type of endpoint, or to none.
func latestWeights(revs []*revision, epType string) []revisionWt {
	for i := len(revs) - 1; i >= 0; i-- {
		if revState(revs[i]) != stateActive {
			continue
		}
		if revs[i].Service.EgressNetworkConnectorARN != "" && epType != endpointHomeRegion {
			continue
		}
		return []revisionWt{{RevisionID: revs[i].ID, Weight: 100}}
	}
	return nil
}

func routesTo(ep *endpoint, revisionID string) bool {
	return slices.ContainsFunc(ep.Weights, func(w revisionWt) bool { return w.RevisionID == revisionID && w.Weight > 0 })
}

// ─── CreateWebFunction ───────────────────────────────────────────────────────

type revisionConfigIn struct {
	Description string         `json:"description,omitempty"`
	KMSKeyARN   string         `json:"kmsKeyArn,omitempty"`
	BuildConfig *buildConfig   `json:"buildConfig"`
	Service     *serviceConfig `json:"serviceConfig"`
}

type endpointConfigIn struct {
	EndpointName       string         `json:"endpointName"`
	Description        string         `json:"description,omitempty"`
	EndpointType       string         `json:"endpointType"`
	AuthType           string         `json:"authType"`
	AutoDeploymentMode string         `json:"autoDeploymentMode,omitempty"`
	Regions            []string       `json:"regions,omitempty"`
	ScalingConfig      *scalingConfig `json:"scalingConfig,omitempty"`
	ThrottleConfig     *throttleCfg   `json:"throttleConfig,omitempty"`
}

type createWebFunctionRequest struct {
	FunctionName   string            `json:"functionName"`
	RevisionConfig *revisionConfigIn `json:"revisionConfig,omitempty"`
	EndpointConfig *endpointConfigIn `json:"endpointConfig,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

type createWebFunctionResponse struct {
	functionSummary
	Revision *revisionSummary  `json:"revision,omitempty"`
	Endpoint *endpointSummary  `json:"endpoint,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
}

// newRevision validates and builds a revision. The defaults the model says the
// response returns (timeoutSeconds 30, maxConcurrencyPerEnvironment 64) are
// stored with it, so a read and the create response agree.
func (s *Service) newRevision(fn *webFunction, in *revisionConfigIn, seq int) (*revision, *protocol.AWSError) {
	if in == nil || in.BuildConfig == nil || in.Service == nil {
		return nil, validationError("buildConfig and serviceConfig are required.")
	}
	if aerr := validateRevision(in.BuildConfig, in.Service, in.KMSKeyARN, in.Description); aerr != nil {
		return nil, aerr
	}
	sc := *in.Service
	if sc.TimeoutSeconds == nil {
		sc.TimeoutSeconds = new(int)
		*sc.TimeoutSeconds = defaultTimeoutSeconds
	}
	if sc.MaxConcurrencyPerEnvironment == nil {
		sc.MaxConcurrencyPerEnvironment = new(int)
		*sc.MaxConcurrencyPerEnvironment = defaultMaxConcurrency
	}
	id := newRevisionID()
	return &revision{
		Function: fn.Name, Region: fn.Region, ID: id, ARN: s.revisionARN(fn.Region, fn.Name, id),
		Description: in.Description, KMSKeyARN: in.KMSKeyARN, BuildConfig: *in.BuildConfig, Service: sc, CreatedAt: s.now(), Seq: seq,
		State: statePending,
	}, nil
}

// newEndpoint validates and builds an endpoint (without weights, which the
// caller routes).
func (s *Service) newEndpoint(fn *webFunction, in *endpointConfigIn) (*endpoint, *protocol.AWSError) {
	if aerr := validateName("endpointName", in.EndpointName, maxEndpointNameLength); aerr != nil {
		return nil, aerr
	}
	deploy := in.AutoDeploymentMode
	if deploy == "" {
		deploy = deployDisabled
	}
	if aerr := validateEndpointEnums(in.EndpointType, in.AuthType, deploy); aerr != nil {
		return nil, aerr
	}
	if aerr := validateDescription(in.Description); aerr != nil {
		return nil, aerr
	}
	if aerr := validateScaling(in.ScalingConfig); aerr != nil {
		return nil, aerr
	}
	if aerr := validateThrottle(in.ThrottleConfig); aerr != nil {
		return nil, aerr
	}
	// Per AWS docs (web-functions-endpoints.md, "Auto-deployment mode"):
	// LatestRevision is supported only for HomeRegion endpoints.
	if deploy == deployLatestRevision && in.EndpointType != endpointHomeRegion {
		return nil, validationError("autoDeploymentMode LatestRevision is supported only for HomeRegion endpoints.")
	}
	regions, aerr := resolveRegions(in.EndpointType, fn.Region, in.Regions)
	if aerr != nil {
		return nil, aerr
	}
	ep := &endpoint{
		Function: fn.Name, Region: fn.Region, Name: in.EndpointName, ARN: s.endpointARN(fn.Region, fn.Name, in.EndpointName),
		Description: in.Description, Type: in.EndpointType, AuthType: in.AuthType, AutoDeploy: deploy, Regions: regions,
		Scaling: in.ScalingConfig, Throttle: in.ThrottleConfig, RegionDomain: map[string]string{},
		CreatedAt: s.now(), UpdatedAt: s.now(), State: statePending,
	}
	if in.EndpointType == endpointMultiRegion {
		ep.DomainName = newDomainName(true, "")
	}
	for _, r := range regions {
		ep.RegionDomain[r] = ""
		if in.EndpointType != endpointMultiRegion {
			ep.RegionDomain[r] = newDomainName(false, r)
		}
	}
	if in.EndpointType != endpointMultiRegion {
		ep.DomainName = ep.RegionDomain[fn.Region]
	}
	return ep, nil
}

func (s *Service) createWebFunctionTyped(ctx context.Context, in *createWebFunctionRequest) (*createWebFunctionResponse, *protocol.AWSError) {
	s.ensureResumed()
	defer s.lock()()
	if aerr := validateName("functionName", in.FunctionName, maxFunctionNameLength); aerr != nil {
		return nil, aerr
	}
	if aerr := serviceutil.ValidateTags(tagCfg, in.Tags); aerr != nil {
		return nil, aerr
	}
	region := s.regionOf(ctx)
	if _, found, aerr := s.loadFunction(ctx, region, in.FunctionName); aerr != nil {
		return nil, aerr
	} else if found {
		return nil, conflict("Web function already exists: %s", s.functionARN(region, in.FunctionName))
	}
	now := s.now()
	fn := &webFunction{Name: in.FunctionName, ARN: s.functionARN(region, in.FunctionName), Region: region, CreatedAt: now, UpdatedAt: now}

	// Build and validate everything before writing anything: a rejected call
	// leaves no function behind.
	var rev *revision
	if in.RevisionConfig != nil {
		var aerr *protocol.AWSError
		if rev, aerr = s.newRevision(fn, in.RevisionConfig, 1); aerr != nil {
			return nil, aerr
		}
		if aerr = s.pinRevision(ctx, rev); aerr != nil {
			return nil, aerr
		}
	}
	var ep *endpoint
	if in.EndpointConfig != nil {
		var aerr *protocol.AWSError
		if ep, aerr = s.newEndpoint(fn, in.EndpointConfig); aerr != nil {
			return nil, aerr
		}
		// Per AWS docs (web-functions-endpoints.md): MultiRegion and PerRegion
		// endpoints require revision weights, which this call has no member for.
		if ep.Type != endpointHomeRegion {
			return nil, validationError("A %s endpoint requires revisionWeights: create it with CreateWebFunctionEndpoint.", ep.Type)
		}
	}

	if aerr := putRecord(ctx, s, nsFunctions, functionKey(region, fn.Name), fn); aerr != nil {
		return nil, aerr
	}
	out := &createWebFunctionResponse{functionSummary: s.functionSummary(fn, true)}
	if len(in.Tags) > 0 {
		if aerr := s.tagStore().Save(ctx, fn.ARN, in.Tags); aerr != nil {
			return nil, aerr
		}
		out.Tags = in.Tags
	}
	s.publish(ctx, events.LambdaWebFunctionCreated, fn, "", "")
	if rev != nil {
		if aerr := putRecord(ctx, s, nsRevisions, childKey(region, fn.Name, rev.ID), rev); aerr != nil {
			return nil, aerr
		}
		sum := s.revisionSummary(rev) // Pending: the state the create call reports
		out.Revision = &sum
	}
	if ep != nil {
		if aerr := putRecord(ctx, s, nsEndpoints, childKey(region, fn.Name, ep.Name), ep); aerr != nil {
			return nil, aerr
		}
		sum := s.endpointSummary(ctx, ep) // Pending, as stored
		out.Endpoint = &sum
		s.publish(ctx, events.LambdaWebEndpointCreated, fn, "", ep.Name)
		if aerr := s.registerEndpoint(ctx, fn, ep); aerr != nil {
			return nil, aerr
		}
	}
	// The revision starts last, so a LatestRevision endpoint that is already
	// registered is routed to it the moment it is Active.
	if rev != nil {
		if aerr := s.startRevision(ctx, fn, rev); aerr != nil {
			return nil, aerr
		}
	}
	return out, nil
}

// ─── Functions: get, list, delete ────────────────────────────────────────────

type functionRequest struct {
	FunctionName string `json:"-"`
}

func (s *Service) getWebFunctionTyped(ctx context.Context, in *functionRequest) (*functionSummary, *protocol.AWSError) {
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	sum := s.functionSummary(fn, false)
	return &sum, nil
}

type listRequest struct {
	FunctionName string   `json:"-"`
	Filters      []filter `json:"filters,omitempty"`
	MaxResults   *int     `json:"maxResults,omitempty"`
	NextToken    string   `json:"nextToken,omitempty"`
}

type listWebFunctionsResponse struct {
	Functions []functionSummary `json:"functions"`
	NextToken string            `json:"nextToken,omitempty"`
}

func (s *Service) listWebFunctionsTyped(ctx context.Context, in *listRequest) (*listWebFunctionsResponse, *protocol.AWSError) {
	if aerr := validateFilters(in.Filters, functionFilterNames); aerr != nil {
		return nil, aerr
	}
	fns, aerr := scanRecords[webFunction](ctx, s, nsFunctions, functionKey(s.regionOf(ctx), ""))
	if aerr != nil {
		return nil, aerr
	}
	sums := make([]functionSummary, 0, len(fns))
	for _, fn := range fns {
		if matches(in.Filters, map[string]string{"state": stateActive}) {
			sums = append(sums, s.functionSummary(fn, false))
		}
	}
	items, next, aerr := page(sums, in.MaxResults, in.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listWebFunctionsResponse{Functions: items, NextToken: next}, nil
}

func (s *Service) deleteWebFunctionTyped(ctx context.Context, in *functionRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	// Deleting a function deletes its endpoints and revisions with it.
	eps, aerr := s.listEndpoints(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return nil, aerr
	}
	for _, ep := range eps {
		if aerr := deleteRecord(ctx, s, nsEndpoints, childKey(fn.Region, fn.Name, ep.Name)); aerr != nil {
			return nil, aerr
		}
	}
	revs, aerr := s.listRevisions(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return nil, aerr
	}
	for _, rev := range revs {
		if aerr := deleteRecord(ctx, s, nsRevisions, childKey(fn.Region, fn.Name, rev.ID)); aerr != nil {
			return nil, aerr
		}
	}
	if aerr := s.tagStore().Delete(ctx, fn.ARN); aerr != nil {
		return nil, aerr
	}
	if aerr := deleteRecord(ctx, s, nsPolicies, fn.ARN); aerr != nil {
		return nil, aerr
	}
	if aerr := deleteRecord(ctx, s, nsFunctions, functionKey(fn.Region, fn.Name)); aerr != nil {
		return nil, aerr
	}
	s.publish(ctx, events.LambdaWebFunctionDeleted, fn, "", "")
	if err := s.onChange(ctx, fn.Region, fn.Name); err != nil {
		s.log.Warn("lambda-web: change hook failed after a function was deleted", zap.String("function", fn.Name), zap.Error(err))
	}
	return nil, nil
}

// ─── Revisions ───────────────────────────────────────────────────────────────

type createRevisionRequest struct {
	FunctionName string `json:"-"`
	revisionConfigIn
}

func (s *Service) createWebFunctionRevisionTyped(ctx context.Context, in *createRevisionRequest) (*revisionView, *protocol.AWSError) {
	s.ensureResumed()
	defer s.lock()()
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	existing, aerr := s.listRevisions(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return nil, aerr
	}
	seq := 1
	if n := len(existing); n > 0 {
		seq = existing[n-1].Seq + 1
	}
	rev, aerr := s.newRevision(fn, &in.revisionConfigIn, seq)
	if aerr != nil {
		return nil, aerr
	}
	// The package is pinned and checked to exist now; its contents are checked
	// by the worker, which moves the revision to Active or Failed.
	if aerr := s.pinRevision(ctx, rev); aerr != nil {
		return nil, aerr
	}
	if aerr := putRecord(ctx, s, nsRevisions, childKey(fn.Region, fn.Name, rev.ID), rev); aerr != nil {
		return nil, aerr
	}
	out := s.revisionView(fn, rev) // Pending: the state the create call reports
	if aerr := s.startRevision(ctx, fn, rev); aerr != nil {
		return nil, aerr
	}
	return out, nil
}

type revisionRequest struct {
	FunctionName string `json:"-"`
	RevisionID   string `json:"-"`
}

func (s *Service) getWebFunctionRevisionTyped(ctx context.Context, in *revisionRequest) (*revisionView, *protocol.AWSError) {
	s.ensureResumed()
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	rev, aerr := s.resolveRevision(ctx, fn, in.RevisionID)
	if aerr != nil {
		return nil, aerr
	}
	return s.revisionView(fn, rev), nil
}

type listRevisionsResponse struct {
	Revisions []revisionSummary `json:"revisions"`
	NextToken string            `json:"nextToken,omitempty"`
}

func (s *Service) listWebFunctionRevisionsTyped(ctx context.Context, in *listRequest) (*listRevisionsResponse, *protocol.AWSError) {
	s.ensureResumed()
	if aerr := validateFilters(in.Filters, revisionFilterNames); aerr != nil {
		return nil, aerr
	}
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	revs, aerr := s.listRevisions(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return nil, aerr
	}
	// Per AWS docs (web-functions-deployment.md): revisions are listed in
	// descending order of createdAt, newest first.
	sums := make([]revisionSummary, 0, len(revs))
	for _, rev := range slices.Backward(revs) {
		sum := s.revisionSummary(rev)
		if matches(in.Filters, map[string]string{"state": sum.State}) {
			sums = append(sums, sum)
		}
	}
	items, next, aerr := page(sums, in.MaxResults, in.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listRevisionsResponse{Revisions: items, NextToken: next}, nil
}

func (s *Service) deleteWebFunctionRevisionTyped(ctx context.Context, in *revisionRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	rev, aerr := s.resolveRevision(ctx, fn, in.RevisionID)
	if aerr != nil {
		return nil, aerr
	}
	eps, aerr := s.listEndpoints(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return nil, aerr
	}
	// Per AWS docs (web-functions-revisions.md): a revision any endpoint routes
	// traffic to can't be deleted.
	for _, ep := range eps {
		if routesTo(ep, rev.ID) {
			return nil, conflict("Revision %s is serving traffic on endpoint %s and can't be deleted.", rev.ID, ep.Name)
		}
	}
	if aerr := deleteRecord(ctx, s, nsRevisions, childKey(fn.Region, fn.Name, rev.ID)); aerr != nil {
		return nil, aerr
	}
	s.publish(ctx, events.LambdaWebRevisionDeleted, fn, rev.ID, "")
	return nil, nil
}

// ─── Endpoints ───────────────────────────────────────────────────────────────

type createEndpointRequest struct {
	FunctionName string `json:"-"`
	endpointConfigIn
	RevisionWeights []revisionWt `json:"revisionWeights,omitempty"`
}

func (s *Service) createWebFunctionEndpointTyped(ctx context.Context, in *createEndpointRequest) (*endpointView, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	ep, aerr := s.newEndpoint(fn, &in.endpointConfigIn)
	if aerr != nil {
		return nil, aerr
	}
	existing, aerr := s.listEndpoints(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return nil, aerr
	}
	if slices.ContainsFunc(existing, func(e *endpoint) bool { return e.Name == ep.Name }) {
		return nil, conflict("Endpoint already exists: %s", ep.ARN)
	}
	if len(existing) >= maxEndpointsPerFn {
		return nil, quotaExceeded("A web function can have at most %d endpoints.", maxEndpointsPerFn)
	}
	switch {
	case ep.AutoDeploy == deployLatestRevision:
		if len(in.RevisionWeights) > 0 {
			return nil, validationError("revisionWeights can't be set when autoDeploymentMode is LatestRevision.")
		}
		revs, aerr := s.listRevisions(ctx, fn.Region, fn.Name)
		if aerr != nil {
			return nil, aerr
		}
		ep.Weights = latestWeights(revs, ep.Type)
	case len(in.RevisionWeights) > 0:
		if ep.Weights, aerr = s.validateWeights(ctx, fn, ep.Type, in.RevisionWeights); aerr != nil {
			return nil, aerr
		}
	case ep.Type != endpointHomeRegion:
		// Per AWS docs (web-functions-endpoints.md): MultiRegion and PerRegion
		// endpoints require revision weights.
		return nil, validationError("A %s endpoint requires revisionWeights.", ep.Type)
	}
	if aerr := putRecord(ctx, s, nsEndpoints, childKey(fn.Region, fn.Name, ep.Name), ep); aerr != nil {
		return nil, aerr
	}
	s.publish(ctx, events.LambdaWebEndpointCreated, fn, "", ep.Name)
	out := s.endpointView(ctx, fn, ep) // Pending: the state the create call reports
	if aerr := s.registerEndpoint(ctx, fn, ep); aerr != nil {
		return nil, aerr
	}
	return out, nil
}

type endpointRequest struct {
	FunctionName string `json:"-"`
	EndpointName string `json:"-"`
}

func (s *Service) getWebFunctionEndpointTyped(ctx context.Context, in *endpointRequest) (*endpointView, *protocol.AWSError) {
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	ep, aerr := s.resolveEndpoint(ctx, fn, in.EndpointName)
	if aerr != nil {
		return nil, aerr
	}
	return s.endpointView(ctx, fn, ep), nil
}

type listEndpointsResponse struct {
	Endpoints []endpointSummary `json:"endpoints"`
	NextToken string            `json:"nextToken,omitempty"`
}

func (s *Service) listWebFunctionEndpointsTyped(ctx context.Context, in *listRequest) (*listEndpointsResponse, *protocol.AWSError) {
	if aerr := validateFilters(in.Filters, endpointFilterNames); aerr != nil {
		return nil, aerr
	}
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	eps, aerr := s.listEndpoints(ctx, fn.Region, fn.Name)
	if aerr != nil {
		return nil, aerr
	}
	sums := make([]endpointSummary, 0, len(eps))
	for _, ep := range eps {
		sum := s.endpointSummary(ctx, ep)
		attrs := map[string]string{
			"authType": sum.AuthType, "autoDeploymentMode": sum.AutoDeploymentMode, "endpointType": sum.EndpointType,
			"state": sum.State, "updateStatus": sum.UpdateStatus,
		}
		if matches(in.Filters, attrs) {
			sums = append(sums, sum)
		}
	}
	items, next, aerr := page(sums, in.MaxResults, in.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listEndpointsResponse{Endpoints: items, NextToken: next}, nil
}

type updateEndpointRequest struct {
	FunctionName       string         `json:"-"`
	EndpointName       string         `json:"-"`
	Description        *string        `json:"description,omitempty"`
	AuthType           string         `json:"authType,omitempty"`
	AutoDeploymentMode string         `json:"autoDeploymentMode,omitempty"`
	RevisionWeights    []revisionWt   `json:"revisionWeights,omitempty"`
	ScalingConfig      *scalingConfig `json:"scalingConfig,omitempty"`
	ThrottleConfig     *throttleCfg   `json:"throttleConfig,omitempty"`
}

func (s *Service) updateWebFunctionEndpointTyped(ctx context.Context, in *updateEndpointRequest) (*endpointView, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	ep, aerr := s.resolveEndpoint(ctx, fn, in.EndpointName)
	if aerr != nil {
		return nil, aerr
	}
	// An omitted member keeps the current value; an empty scalingConfig or
	// throttleConfig object clears what was set.
	auth, deploy := ep.AuthType, ep.AutoDeploy
	if in.AuthType != "" {
		auth = in.AuthType
	}
	if in.AutoDeploymentMode != "" {
		deploy = in.AutoDeploymentMode
	}
	if aerr := validateEndpointEnums(ep.Type, auth, deploy); aerr != nil {
		return nil, aerr
	}
	if in.Description != nil {
		if aerr := validateDescription(*in.Description); aerr != nil {
			return nil, aerr
		}
	}
	if aerr := validateScaling(in.ScalingConfig); aerr != nil {
		return nil, aerr
	}
	if aerr := validateThrottle(in.ThrottleConfig); aerr != nil {
		return nil, aerr
	}
	if deploy == deployLatestRevision && ep.Type != endpointHomeRegion {
		return nil, validationError("autoDeploymentMode LatestRevision is supported only for HomeRegion endpoints.")
	}
	weights := ep.Weights
	switch {
	case in.RevisionWeights != nil && deploy == deployLatestRevision:
		return nil, validationError("revisionWeights can't be set when autoDeploymentMode is LatestRevision.")
	case in.RevisionWeights != nil:
		if weights, aerr = s.validateWeights(ctx, fn, ep.Type, in.RevisionWeights); aerr != nil {
			return nil, aerr
		}
	case deploy == deployLatestRevision:
		// Switching to (or staying in) LatestRevision serves the newest
		// revision.
		revs, aerr := s.listRevisions(ctx, fn.Region, fn.Name)
		if aerr != nil {
			return nil, aerr
		}
		// Per AWS docs (web-functions-deployment.md, "Switching between
		// modes"): switching from Disabled needs the latest revision Active;
		// Pending or Failed fails the request with a conflict error.
		if n := len(revs); ep.AutoDeploy == deployDisabled && n > 0 && revState(revs[n-1]) != stateActive {
			return nil, conflict("The latest revision %s is %s: it must be Active before the endpoint can follow the latest revision.", revs[n-1].ID, revState(revs[n-1]))
		}
		if latest := latestWeights(revs, ep.Type); latest != nil {
			weights = latest
		}
	}
	ep.AuthType, ep.AutoDeploy, ep.Weights = auth, deploy, weights
	if in.Description != nil {
		ep.Description = *in.Description
	}
	if in.ScalingConfig != nil {
		ep.Scaling = nil
		if in.ScalingConfig.MaxEnvironments != nil {
			ep.Scaling = in.ScalingConfig
		}
	}
	if in.ThrottleConfig != nil {
		ep.Throttle = nil
		if in.ThrottleConfig.RateLimit != nil {
			ep.Throttle = in.ThrottleConfig
		}
	}
	// An update is visible: InProgress is persisted, the data plane is told,
	// and only then is the update Successful (or Failed, when it refuses). The
	// response reports the update as accepted, still InProgress.
	if aerr := s.beginEndpointUpdate(ctx, fn, []*endpoint{ep}); aerr != nil {
		return nil, aerr
	}
	out := s.endpointView(ctx, fn, ep)
	if aerr := s.finishEndpointUpdate(ctx, fn, []*endpoint{ep}, s.onChange(ctx, fn.Region, fn.Name)); aerr != nil {
		return nil, aerr
	}
	return out, nil
}

func (s *Service) deleteWebFunctionEndpointTyped(ctx context.Context, in *endpointRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveFunction(ctx, in.FunctionName)
	if aerr != nil {
		return nil, aerr
	}
	ep, aerr := s.resolveEndpoint(ctx, fn, in.EndpointName)
	if aerr != nil {
		return nil, aerr
	}
	// Deleting: persisted first, so the data plane (told by the hook) stops
	// routing new requests, drains the ones in flight, and only then is the
	// endpoint removed.
	ep.State, ep.StateReason, ep.UpdatedAt = stateDeleting, "", s.now()
	if aerr := putRecord(ctx, s, nsEndpoints, childKey(fn.Region, fn.Name, ep.Name), ep); aerr != nil {
		return nil, aerr
	}
	if err := s.onChange(ctx, fn.Region, fn.Name); err != nil {
		s.log.Warn("lambda-web: change hook failed while draining an endpoint", zap.String("endpoint", ep.Name), zap.Error(err))
	}
	if aerr := deleteRecord(ctx, s, nsEndpoints, childKey(fn.Region, fn.Name, ep.Name)); aerr != nil {
		return nil, aerr
	}
	s.publish(ctx, events.LambdaWebEndpointDeleted, fn, "", ep.Name)
	if err := s.onChange(ctx, fn.Region, fn.Name); err != nil {
		s.log.Warn("lambda-web: change hook failed after an endpoint was deleted", zap.String("endpoint", ep.Name), zap.Error(err))
	}
	return nil, nil
}

// ─── Tags ────────────────────────────────────────────────────────────────────

type tagResourceRequest struct {
	Resource string            `json:"-"`
	Tags     map[string]string `json:"Tags"`
}

func (s *Service) tagResourceTyped(ctx context.Context, in *tagResourceRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveResourceARN(ctx, in.Resource)
	if aerr != nil {
		return nil, aerr
	}
	_, aerr = serviceutil.ApplyStoreTags(ctx, s.tagStore(), fn.ARN, in.Tags, tagCfg)
	if aerr == nil {
		s.invalidateRoutes() // the route table carries the tags that ABAC conditions read
	}
	return nil, aerr
}

type untagResourceRequest struct {
	Resource string   `json:"-"`
	TagKeys  []string `json:"-"`
}

func (s *Service) untagResourceTyped(ctx context.Context, in *untagResourceRequest) (any, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveResourceARN(ctx, in.Resource)
	if aerr != nil {
		return nil, aerr
	}
	_, aerr = serviceutil.RemoveStoreTags(ctx, s.tagStore(), fn.ARN, in.TagKeys)
	if aerr == nil {
		s.invalidateRoutes()
	}
	return nil, aerr
}

type listTagsRequest struct {
	Resource string `json:"-"`
}

type listTagsResponse struct {
	Tags map[string]string `json:"Tags"`
}

func (s *Service) listTagsTyped(ctx context.Context, in *listTagsRequest) (*listTagsResponse, *protocol.AWSError) {
	fn, aerr := s.resolveResourceARN(ctx, in.Resource)
	if aerr != nil {
		return nil, aerr
	}
	tags, aerr := s.tagStore().Load(ctx, fn.ARN)
	if aerr != nil {
		return nil, aerr
	}
	return &listTagsResponse{Tags: maps.Clone(tags)}, nil
}

// ─── Resource policy ─────────────────────────────────────────────────────────

type putPolicyRequest struct {
	ResourceARN string `json:"-"`
	Policy      string `json:"Policy"`
	RevisionID  string `json:"RevisionId,omitempty"`
}

type policyResponse struct {
	Policy     string `json:"Policy"`
	RevisionID string `json:"RevisionId"`
}

type policyRef struct {
	ResourceARN string `json:"-"`
	RevisionID  string `json:"-"`
}

func (s *Service) loadPolicy(ctx context.Context, fn *webFunction) (*policyRecord, bool, *protocol.AWSError) {
	return getRecord[policyRecord](ctx, s, nsPolicies, fn.ARN)
}

func (s *Service) putResourcePolicyTyped(ctx context.Context, in *putPolicyRequest) (*policyResponse, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveResourceARN(ctx, in.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	if in.Policy == "" || len(in.Policy) > 20480 {
		return nil, validationError("Policy must be between 1 and 20480 characters.")
	}
	if aerr := validatePolicy(in.Policy); aerr != nil {
		return nil, aerr
	}
	cur, found, aerr := s.loadPolicy(ctx, fn)
	if aerr != nil {
		return nil, aerr
	}
	if in.RevisionID != "" && (!found || cur.RevisionID != in.RevisionID) {
		return nil, conflict("The policy revision ID does not match the current policy revision.")
	}
	rec := &policyRecord{Policy: in.Policy, RevisionID: newPolicyRevisionID()}
	if aerr := putRecord(ctx, s, nsPolicies, fn.ARN, rec); aerr != nil {
		return nil, aerr
	}
	s.invalidateRoutes() // the route table carries the policy the next request is judged by
	return &policyResponse{Policy: rec.Policy, RevisionID: rec.RevisionID}, nil
}

func (s *Service) getResourcePolicyTyped(ctx context.Context, in *policyRef) (*policyResponse, *protocol.AWSError) {
	fn, aerr := s.resolveResourceARN(ctx, in.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	cur, found, aerr := s.loadPolicy(ctx, fn)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, notFound("The resource policy does not exist for %s.", fn.ARN)
	}
	return &policyResponse{Policy: cur.Policy, RevisionID: cur.RevisionID}, nil
}

func (s *Service) deleteResourcePolicyTyped(ctx context.Context, in *policyRef) (any, *protocol.AWSError) {
	defer s.lock()()
	fn, aerr := s.resolveResourceARN(ctx, in.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	cur, found, aerr := s.loadPolicy(ctx, fn)
	if aerr != nil {
		return nil, aerr
	}
	if !found {
		return nil, notFound("The resource policy does not exist for %s.", fn.ARN)
	}
	if in.RevisionID != "" && cur.RevisionID != in.RevisionID {
		return nil, conflict("The policy revision ID does not match the current policy revision.")
	}
	if aerr := deleteRecord(ctx, s, nsPolicies, fn.ARN); aerr != nil {
		return nil, aerr
	}
	s.invalidateRoutes()
	return nil, nil
}

// ─── Account settings ────────────────────────────────────────────────────────

type accountSettingsResponse struct {
	AccountQuotas struct {
		MaxTotalArmVCpus        int `json:"maxTotalArmVCpus"`
		MaxTotalRateLimit       int `json:"maxTotalRateLimit"`
		MaxRevisionsPerFunction int `json:"maxRevisionsPerFunction"`
		MaxEndpointsPerFunction int `json:"maxEndpointsPerFunction"`
	} `json:"accountQuotas"`
	AccountUsage struct {
		FunctionCount int `json:"functionCount"`
	} `json:"accountUsage"`
}

type noInput struct{}

func (s *Service) getWebAccountSettingsTyped(ctx context.Context, _ *noInput) (*accountSettingsResponse, *protocol.AWSError) {
	fns, aerr := scanRecords[webFunction](ctx, s, nsFunctions, functionKey(s.regionOf(ctx), ""))
	if aerr != nil {
		return nil, aerr
	}
	var out accountSettingsResponse
	out.AccountQuotas.MaxTotalArmVCpus = maxTotalArmVCPUs
	out.AccountQuotas.MaxTotalRateLimit = maxTotalRateLimit
	out.AccountQuotas.MaxRevisionsPerFunction = maxRevisionsPerFn
	out.AccountQuotas.MaxEndpointsPerFunction = maxEndpointsPerFn
	out.AccountUsage.FunctionCount = len(fns)
	return &out, nil
}
