package apigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Store namespaces — one per resource type, shared between REST v1 and HTTP v2.
const (
	nsRestAPIs     = "apigw:restapis"
	nsResources    = "apigw:resources"
	nsStages       = "apigw:stages"
	nsDeployments  = "apigw:deployments"
	nsV2APIs       = "apigw:v2apis"
	nsV2Routes     = "apigw:v2routes"
	nsV2Integ      = "apigw:v2integrations"
	nsV2Stages     = "apigw:v2stages"
	nsV2Deploys    = "apigw:v2deployments"
	nsAPIKeys      = "apigw:apikeys"
	nsUsagePlans   = "apigw:usageplans"
	nsAuthorizers  = "apigw:authorizers"
	nsV2Authorizer = "apigw:v2authorizers"
	nsModels       = "apigw:models"
	nsValidators   = "apigw:requestvalidators"
	nsDomainNames  = "apigw:domainnames"
	nsBasePathMaps = "apigw:basepathmappings"
	nsVpcLinks     = "apigw:vpclinks"
	nsResourceTags = "apigw:resourcetags"
	nsV2DomainName = "apigw:v2domainnames"
	nsV2VpcLinks   = "apigw:v2vpclinks"
	nsV2ApiMapping = "apigw:v2apimappings"
	nsV2Tags       = "apigw:v2tags"
)

// ---- Domain types: REST API v1 ------------------------------------------

// RestAPI is the top-level REST API resource.
type RestAPI struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	CreatedDate       int64             `json:"createdDate"`
	Version           string            `json:"version,omitempty"`
	EndpointConfig    *EndpointConfig   `json:"endpointConfiguration,omitempty"`
	Policy            string            `json:"policy,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	BinaryMediaTypes  []string          `json:"binaryMediaTypes,omitempty"`
	DisableExecuteAPI bool              `json:"disableExecuteApiEndpoint,omitempty"`
	RootResourceID    string            `json:"rootResourceId"`
	// TODO(priority:P2): add minimumCompressionSize, apiKeySource
}

// EndpointConfig describes the API endpoint type.
type EndpointConfig struct {
	Types []string `json:"types"` // REGIONAL, EDGE, PRIVATE
}

// Resource is a URL path node in the REST API resource tree.
type Resource struct {
	ID              string             `json:"id"`
	ParentID        string             `json:"parentId,omitempty"`
	PathPart        string             `json:"pathPart"`
	Path            string             `json:"path"`
	ResourceMethods map[string]*Method `json:"resourceMethods,omitempty"`
}

// Method defines an HTTP method on a resource.
type Method struct {
	HTTPMethod        string                     `json:"httpMethod"`
	AuthorizationType string                     `json:"authorizationType"`
	AuthorizerID      string                     `json:"authorizerId,omitempty"`
	APIKeyRequired    bool                       `json:"apiKeyRequired"`
	RequestParameters map[string]bool            `json:"requestParameters,omitempty"`
	MethodIntegration *Integration               `json:"methodIntegration,omitempty"`
	MethodResponses   map[string]*MethodResponse `json:"methodResponses,omitempty"`
	// TODO(priority:P3): add requestModels, requestValidatorId, operationName
}

// MethodResponse defines a method response shape.
type MethodResponse struct {
	StatusCode         string          `json:"statusCode"`
	ResponseParameters map[string]bool `json:"responseParameters,omitempty"`
	// TODO(priority:P3): add responseModels
}

// Integration defines a method integration (backend binding).
type Integration struct {
	Type                 string                          `json:"type"` // AWS_PROXY, HTTP_PROXY, MOCK, AWS, HTTP
	HTTPMethod           string                          `json:"httpMethod,omitempty"`
	URI                  string                          `json:"uri,omitempty"`
	ConnectionType       string                          `json:"connectionType,omitempty"`
	ContentHandling      string                          `json:"contentHandling,omitempty"`
	Credentials          string                          `json:"credentials,omitempty"`
	PassthroughBehavior  string                          `json:"passthroughBehavior,omitempty"`
	RequestParameters    map[string]string               `json:"requestParameters,omitempty"`
	RequestTemplates     map[string]string               `json:"requestTemplates,omitempty"`
	TimeoutInMillis      int                             `json:"timeoutInMillis,omitempty"`
	IntegrationResponses map[string]*IntegrationResponse `json:"integrationResponses,omitempty"`
	// TODO(priority:P3): add cacheNamespace, cacheKeyParameters, connectionId (VPC Link)
}

// IntegrationResponse defines a response mapping from the integration.
type IntegrationResponse struct {
	StatusCode         string            `json:"statusCode"`
	SelectionPattern   string            `json:"selectionPattern,omitempty"`
	ResponseParameters map[string]string `json:"responseParameters,omitempty"`
	ResponseTemplates  map[string]string `json:"responseTemplates,omitempty"`
	ContentHandling    string            `json:"contentHandling,omitempty"`
}

// Stage represents a named deployment stage (e.g. "prod", "dev").
type Stage struct {
	StageName       string            `json:"stageName"`
	DeploymentID    string            `json:"deploymentId"`
	Description     string            `json:"description,omitempty"`
	CreatedDate     int64             `json:"createdDate"`
	LastUpdatedDate int64             `json:"lastUpdatedDate"`
	Tags            map[string]string `json:"tags,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
	// TODO(priority:P3): add cacheClusterEnabled, cacheClusterSize, methodSettings, tracingEnabled, accessLogSettings
}

// Deployment represents a snapshot of the API at a point in time.
type Deployment struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	CreatedDate int64  `json:"createdDate"`
}

// ---- Domain types: HTTP API v2 ------------------------------------------

// APIV2 represents an HTTP (or WebSocket) API.
type APIV2 struct {
	ApiID                    string            `json:"apiId"`
	Name                     string            `json:"name"`
	ProtocolType             string            `json:"protocolType"` // HTTP, WEBSOCKET
	Description              string            `json:"description,omitempty"`
	RouteSelectionExpression string            `json:"routeSelectionExpression,omitempty"`
	CorsConfiguration        *CorsConfig       `json:"corsConfiguration,omitempty"`
	CreatedDate              string            `json:"createdDate"`
	Tags                     map[string]string `json:"tags,omitempty"`
	Version                  string            `json:"version,omitempty"`
	DisableExecuteAPI        bool              `json:"disableExecuteApiEndpoint,omitempty"`
	// TODO(priority:P2): add apiGatewayManaged, importInfo
}

// CorsConfig is the CORS configuration for an HTTP API.
type CorsConfig struct {
	AllowCredentials bool     `json:"allowCredentials,omitempty"`
	AllowHeaders     []string `json:"allowHeaders,omitempty"`
	AllowMethods     []string `json:"allowMethods,omitempty"`
	AllowOrigins     []string `json:"allowOrigins,omitempty"`
	ExposeHeaders    []string `json:"exposeHeaders,omitempty"`
	MaxAge           int      `json:"maxAge,omitempty"`
}

// RouteV2 defines a route in an HTTP API.
type RouteV2 struct {
	RouteID           string `json:"routeId"`
	RouteKey          string `json:"routeKey"` // "GET /users", "$default"
	Target            string `json:"target,omitempty"`
	AuthorizationType string `json:"authorizationType,omitempty"`
	AuthorizerID      string `json:"authorizerId,omitempty"`
	APIKeyRequired    bool   `json:"apiKeyRequired,omitempty"`
	// TODO(priority:P3): add modelSelectionExpression, operationName, requestModels, requestParameters, routeResponseSelectionExpression
}

// IntegrationV2 defines an integration in an HTTP API.
type IntegrationV2 struct {
	IntegrationID        string `json:"integrationId"`
	IntegrationType      string `json:"integrationType"` // AWS_PROXY, HTTP_PROXY
	IntegrationURI       string `json:"integrationUri,omitempty"`
	IntegrationMethod    string `json:"integrationMethod,omitempty"`
	PayloadFormatVersion string `json:"payloadFormatVersion,omitempty"` // "1.0", "2.0"
	ConnectionType       string `json:"connectionType,omitempty"`
	TimeoutInMillis      int    `json:"timeoutInMillis,omitempty"`
	Description          string `json:"description,omitempty"`
	// TODO(priority:P3): add the remaining IntegrationV2 fields
	// credentialsArn, requestParameters, requestTemplates, responseParameters,
	// templateSelectionExpression and connectionId (VPC Link).
}

// StageV2 represents a stage in an HTTP API.
type StageV2 struct {
	StageName       string            `json:"stageName"`
	DeploymentID    string            `json:"deploymentId,omitempty"`
	Description     string            `json:"description,omitempty"`
	AutoDeploy      bool              `json:"autoDeploy,omitempty"`
	CreatedDate     string            `json:"createdDate"`
	LastUpdatedDate string            `json:"lastUpdatedDate"`
	StageVariables  map[string]string `json:"stageVariables,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	// TODO(priority:P3): add defaultRouteSettings, routeSettings, accessLogSettings, lastDeploymentStatusMessage
}

// DeploymentV2 represents a deployment snapshot in an HTTP API.
type DeploymentV2 struct {
	DeploymentID     string `json:"deploymentId"`
	Description      string `json:"description,omitempty"`
	AutoDeployed     bool   `json:"autoDeployed,omitempty"`
	CreatedDate      string `json:"createdDate"`
	DeploymentStatus string `json:"deploymentStatus,omitempty"` // PENDING, FAILED, DEPLOYED
}

// ---- Domain types: API Keys & Usage Plans (P2) -------------------------

// APIKey represents an API key used for authentication.
type APIKey struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Value       string            `json:"value"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	CreatedDate int64             `json:"createdDate"`
	Tags        map[string]string `json:"tags,omitempty"`
	StageKeys   []string          `json:"stageKeys,omitempty"` // "{restApiId}/{stageName}"
	// TODO(priority:P3): add customerId, lastUpdatedDate
}

// UsagePlan links API keys to stages and sets throttle/quota limits.
type UsagePlan struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	APIStages   []UsagePlanStage  `json:"apiStages,omitempty"`
	Throttle    *ThrottleSettings `json:"throttle,omitempty"`
	Quota       *QuotaSettings    `json:"quota,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	// KeyIDs lists the API key IDs attached to this plan via UsagePlanKey.
	// It is persisted alongside the plan (it backs the plan/key
	// association) but is not part of the modeled UsagePlan shape, so
	// handler_apikeys.go projects it out of plan responses via
	// usagePlanWire; GetUsagePlanKeys is the modeled channel for reading
	// it back.
	KeyIDs []string `json:"keyIds,omitempty"`
	// TODO(priority:P3): add productCode
}

// UsagePlanStage links a usage plan to a specific API + stage.
type UsagePlanStage struct {
	ApiID string `json:"apiId"`
	Stage string `json:"stage"`
}

// ThrottleSettings defines rate limiting parameters. Applied at request time
// as a token bucket per (usage plan, API key): RateLimit is the per-second
// refill, BurstLimit the bucket capacity. See usage.go.
type ThrottleSettings struct {
	BurstLimit int     `json:"burstLimit,omitempty"`
	RateLimit  float64 `json:"rateLimit,omitempty"`
}

// QuotaSettings defines usage quota parameters. Consumption is counted per
// calendar-aligned period at request time and reported by GetUsage; see
// usage.go.
type QuotaSettings struct {
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Period string `json:"period,omitempty"` // DAY, WEEK, MONTH
}

// ---- Domain types: Authorizers (P2/P3) ----------------------------------

// Authorizer represents a REST API authorizer. TOKEN, REQUEST and
// COGNITO_USER_POOLS types are all invoked/validated during request
// execution — see handler_lambda_auth.go and handler_auth.go.
type Authorizer struct {
	ID                           string   `json:"id"`
	Name                         string   `json:"name"`
	Type                         string   `json:"type"` // TOKEN, REQUEST, COGNITO_USER_POOLS
	AuthorizerURI                string   `json:"authorizerUri,omitempty"`
	AuthorizerCredentials        string   `json:"authorizerCredentials,omitempty"`
	IdentitySource               string   `json:"identitySource,omitempty"`
	IdentityValidationExpression string   `json:"identityValidationExpression,omitempty"`
	AuthorizerResultTTLInSeconds int      `json:"authorizerResultTtlInSeconds,omitempty"`
	ProviderARNs                 []string `json:"providerARNs,omitempty"`
}

// AuthorizerV2 represents an HTTP API authorizer.
type AuthorizerV2 struct {
	AuthorizerID                 string     `json:"authorizerId"`
	Name                         string     `json:"name"`
	AuthorizerType               string     `json:"authorizerType"` // REQUEST, JWT
	IdentitySource               string     `json:"identitySource,omitempty"`
	AuthorizerURI                string     `json:"authorizerUri,omitempty"`
	AuthorizerCredentialsArn     string     `json:"authorizerCredentialsArn,omitempty"`
	AuthorizerResultTTLInSeconds int        `json:"authorizerResultTtlInSeconds,omitempty"`
	JwtConfiguration             *JwtConfig `json:"jwtConfiguration,omitempty"`
	// AuthorizerPayloadFormatVersion ("1.0" or "2.0") and EnableSimpleResponses
	// apply to REQUEST authorizers only, and select the input event shape and
	// the accepted response contract — see handler_lambda_auth.go.
	AuthorizerPayloadFormatVersion string `json:"authorizerPayloadFormatVersion,omitempty"`
	EnableSimpleResponses          bool   `json:"enableSimpleResponses,omitempty"`
}

// JwtConfig defines JWT authorizer configuration for HTTP APIs.
type JwtConfig struct {
	Audience []string `json:"audience,omitempty"`
	Issuer   string   `json:"issuer,omitempty"`
}

// ---- Domain types: Models (P3) ------------------------------------------

// Model defines a data model schema for an API.
type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType,omitempty"`
	Description string `json:"description,omitempty"`
	Schema      string `json:"schema,omitempty"`
}

// ---- Domain types: Request Validators (P3) ------------------------------

// RequestValidator validates request body and/or parameters.
type RequestValidator struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	ValidateRequestBody       bool   `json:"validateRequestBody"`
	ValidateRequestParameters bool   `json:"validateRequestParameters"`
}

// ---- Domain types: Domain Names, VPC Links, Tags (inert metadata) --------

// DomainName represents a custom domain name for a REST API.
type DomainName struct {
	DomainName             string            `json:"domainName"`
	CertificateArn         string            `json:"certificateArn,omitempty"`
	CertificateName        string            `json:"certificateName,omitempty"`
	DistributionDomainName string            `json:"distributionDomainName,omitempty"`
	RegionalDomainName     string            `json:"regionalDomainName,omitempty"`
	RegionalCertificateArn string            `json:"regionalCertificateArn,omitempty"`
	SecurityPolicy         string            `json:"securityPolicy,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
}

// BasePathMapping maps a base path under a custom domain to a REST API stage.
type BasePathMapping struct {
	BasePath  string `json:"basePath"`
	RestApiID string `json:"restApiId"`
	Stage     string `json:"stage,omitempty"`
}

// VpcLink represents a VPC Link resource for REST API v1.
type VpcLink struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	TargetArns  []string          `json:"targetArns,omitempty"`
	Status      string            `json:"status"` // AVAILABLE, PENDING, DELETING, FAILED
	Tags        map[string]string `json:"tags,omitempty"`
}

// DomainNameV2 represents a custom domain name for an HTTP API.
type DomainNameV2 struct {
	DomainName               string             `json:"domainName"`
	DomainNameConfigurations []DomainNameConfig `json:"domainNameConfigurations,omitempty"`
	Tags                     map[string]string  `json:"tags,omitempty"`
}

// DomainNameConfig describes a domain name configuration entry.
type DomainNameConfig struct {
	ApiGatewayDomainName string `json:"apiGatewayDomainName,omitempty"`
	CertificateArn       string `json:"certificateArn,omitempty"`
	EndpointType         string `json:"endpointType,omitempty"` // REGIONAL, EDGE
	SecurityPolicy       string `json:"securityPolicy,omitempty"`
}

// VpcLinkV2 represents a VPC Link resource for HTTP API v2.
type VpcLinkV2 struct {
	VpcLinkID        string            `json:"vpcLinkId"`
	Name             string            `json:"name"`
	SecurityGroupIDs []string          `json:"securityGroupIds,omitempty"`
	SubnetIDs        []string          `json:"subnetIds,omitempty"`
	Status           string            `json:"vpcLinkStatus"` // AVAILABLE, PENDING, DELETING, FAILED
	Tags             map[string]string `json:"tags,omitempty"`
}

// ApiMapping maps a domain name to an HTTP API stage.
type ApiMapping struct {
	ApiMappingID  string `json:"apiMappingId"`
	ApiID         string `json:"apiId"`
	Stage         string `json:"stage"`
	ApiMappingKey string `json:"apiMappingKey,omitempty"`
}

// ---- Store wrapper --------------------------------------------------------

// apigatewayStore wraps state.Store with API Gateway-specific helpers.
type apigatewayStore struct {
	store         state.Store
	defaultRegion string
	// routes caches the execution path's routing state. Management reads keep
	// hitting the store; only the request-serving handlers use the cached
	// variants.
	routes routeCache
}

func newAPIGatewayStore(store state.Store, defaultRegion string) *apigatewayStore {
	return &apigatewayStore{store: store, defaultRegion: defaultRegion}
}

// routeCache holds each API's routing state for the execution path, which
// otherwise re-Scans and re-unmarshals every resource (with its embedded
// methods and integrations) or route on every proxied request. Entries are
// keyed by the region-scoped API ID and dropped whenever any write lands for
// that API's resources or routes — the write methods below are the only
// funnels, so a stale route cannot outlive the request that changed it.
//
// Cached slices and the objects they point at are shared across concurrent
// requests: the execution handlers treat them as read-only (stage-variable
// substitution already copies before mutating). Do not hand them to
// management handlers, which mutate.
type routeCache struct {
	mu        sync.Mutex
	resources map[string][]*Resource
	v2Routes  map[string][]*RouteV2
}

func (c *routeCache) getResources(key string) ([]*Resource, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rs, ok := c.resources[key]
	return rs, ok
}

func (c *routeCache) setResources(key string, rs []*Resource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resources == nil {
		c.resources = make(map[string][]*Resource)
	}
	c.resources[key] = rs
}

func (c *routeCache) dropResources(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.resources, key)
}

func (c *routeCache) getV2Routes(key string) ([]*RouteV2, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rs, ok := c.v2Routes[key]
	return rs, ok
}

func (c *routeCache) setV2Routes(key string, rs []*RouteV2) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.v2Routes == nil {
		c.v2Routes = make(map[string][]*RouteV2)
	}
	c.v2Routes[key] = rs
}

func (c *routeCache) dropV2Routes(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.v2Routes, key)
}

// region extracts the per-request region from context, falling back to the default.
func (s *apigatewayStore) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.defaultRegion)
}

// findRestAPIRegion scans every region partition for an API with the given ID
// and returns the region that owns it. Returns "" when not found. Used by the
// invoke handlers to support path-style URLs (no SigV4, no Host hint), which
// real AWS doesn't have to handle but local emulators do — this matches
// LocalStack's "API IDs are globally unique within the instance" behaviour.
func (s *apigatewayStore) findRestAPIRegion(ctx context.Context, apiID string) string {
	pairs, err := s.store.Scan(ctx, nsRestAPIs, "")
	if err != nil {
		return ""
	}
	for _, p := range pairs {
		region, rest := serviceutil.SplitRegionKey(p.Key)
		if rest == apiID {
			return region
		}
	}
	return ""
}

// findV2APIRegion is the HTTP API v2 counterpart of findRestAPIRegion.
func (s *apigatewayStore) findV2APIRegion(ctx context.Context, apiID string) string {
	pairs, err := s.store.Scan(ctx, nsV2APIs, "")
	if err != nil {
		return ""
	}
	for _, p := range pairs {
		region, rest := serviceutil.SplitRegionKey(p.Key)
		if rest == apiID {
			return region
		}
	}
	return ""
}

// ---- REST API CRUD --------------------------------------------------------

func (s *apigatewayStore) putRestAPI(ctx context.Context, api *RestAPI) *protocol.AWSError {
	raw, err := json.Marshal(api)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), api.ID)
	if err := s.store.Set(ctx, nsRestAPIs, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getRestAPI(ctx context.Context, apiID string) (*RestAPI, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID)
	raw, found, err := s.store.Get(ctx, nsRestAPIs, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errRestAPINotFound(apiID)
	}
	var api RestAPI
	if err := json.Unmarshal([]byte(raw), &api); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &api, nil
}

func (s *apigatewayStore) deleteRestAPI(ctx context.Context, apiID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), apiID)
	if err := s.store.Delete(ctx, nsRestAPIs, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listRestAPIs(ctx context.Context) ([]*RestAPI, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsRestAPIs, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	apis := make([]*RestAPI, 0, len(pairs))
	for _, p := range pairs {
		var api RestAPI
		if err := json.Unmarshal([]byte(p.Value), &api); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		apis = append(apis, &api)
	}
	return apis, nil
}

// ---- Resource CRUD --------------------------------------------------------

// resourceKey builds a store key for a resource: {apiId}/{resourceId}.
func resourceKey(apiID, resourceID string) string {
	return apiID + "/" + resourceID
}

func (s *apigatewayStore) putResource(ctx context.Context, apiID string, res *Resource) *protocol.AWSError {
	raw, err := json.Marshal(res)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), resourceKey(apiID, res.ID))
	if err := s.store.Set(ctx, nsResources, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.routes.dropResources(serviceutil.RegionKey(s.region(ctx), apiID))
	return nil
}

func (s *apigatewayStore) getResource(ctx context.Context, apiID, resourceID string) (*Resource, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), resourceKey(apiID, resourceID))
	raw, found, err := s.store.Get(ctx, nsResources, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errResourceNotFound(resourceID)
	}
	var res Resource
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &res, nil
}

func (s *apigatewayStore) deleteResource(ctx context.Context, apiID, resourceID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), resourceKey(apiID, resourceID))
	if err := s.store.Delete(ctx, nsResources, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.routes.dropResources(serviceutil.RegionKey(s.region(ctx), apiID))
	return nil
}

func (s *apigatewayStore) listResources(ctx context.Context, apiID string) ([]*Resource, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsResources, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	resources := make([]*Resource, 0, len(pairs))
	for _, p := range pairs {
		var res Resource
		if err := json.Unmarshal([]byte(p.Value), &res); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule; storage-plan.md 3.1).
			continue
		}
		resources = append(resources, &res)
	}
	return resources, nil
}

// listResourcesCached is listResources behind the route cache, for the
// execution path only: the result is shared across concurrent requests and
// must be treated as read-only (see routeCache).
func (s *apigatewayStore) listResourcesCached(ctx context.Context, apiID string) ([]*Resource, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID)
	if rs, ok := s.routes.getResources(key); ok {
		return rs, nil
	}
	rs, aerr := s.listResources(ctx, apiID)
	if aerr != nil {
		return nil, aerr
	}
	s.routes.setResources(key, rs)
	return rs, nil
}

// deleteAllResources removes every resource belonging to an API. Uses
// PrefixDeleter for a single ranged delete when the store supports it
// (storage-plan.md item 3.1), falling back to List+Delete otherwise —
// mirroring sqs.deleteMessagesByQueuePrefix.
func (s *apigatewayStore) deleteAllResources(ctx context.Context, apiID string) *protocol.AWSError {
	defer s.routes.dropResources(serviceutil.RegionKey(s.region(ctx), apiID))
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	if deleter, ok := s.store.(state.PrefixDeleter); ok {
		if err := deleter.DeletePrefix(ctx, nsResources, prefix); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	keys, err := s.store.List(ctx, nsResources, prefix)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, k := range keys {
		if err := s.store.Delete(ctx, nsResources, k); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return nil
}

// ---- Stage CRUD -----------------------------------------------------------

// stageKey builds a store key: {apiId}/{stageName}.
func stageKey(apiID, stageName string) string {
	return apiID + "/" + stageName
}

func (s *apigatewayStore) putStage(ctx context.Context, apiID string, stage *Stage) *protocol.AWSError {
	raw, err := json.Marshal(stage)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), stageKey(apiID, stage.StageName))
	if err := s.store.Set(ctx, nsStages, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getStage(ctx context.Context, apiID, stageName string) (*Stage, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), stageKey(apiID, stageName))
	raw, found, err := s.store.Get(ctx, nsStages, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errStageNotFound(stageName)
	}
	var stage Stage
	if err := json.Unmarshal([]byte(raw), &stage); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &stage, nil
}

func (s *apigatewayStore) deleteStage(ctx context.Context, apiID, stageName string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), stageKey(apiID, stageName))
	if err := s.store.Delete(ctx, nsStages, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listStages(ctx context.Context, apiID string) ([]*Stage, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsStages, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	stages := make([]*Stage, 0, len(pairs))
	for _, p := range pairs {
		var stage Stage
		if err := json.Unmarshal([]byte(p.Value), &stage); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule; storage-plan.md 3.1).
			continue
		}
		stages = append(stages, &stage)
	}
	return stages, nil
}

// deleteAllStages removes every stage belonging to an API. Uses PrefixDeleter
// for a single ranged delete when the store supports it (storage-plan.md
// item 3.1), falling back to List+Delete otherwise.
func (s *apigatewayStore) deleteAllStages(ctx context.Context, apiID string) *protocol.AWSError {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	if deleter, ok := s.store.(state.PrefixDeleter); ok {
		if err := deleter.DeletePrefix(ctx, nsStages, prefix); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	keys, err := s.store.List(ctx, nsStages, prefix)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, k := range keys {
		if err := s.store.Delete(ctx, nsStages, k); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return nil
}

// ---- Deployment CRUD ------------------------------------------------------

// deploymentKey builds a store key: {apiId}/{deploymentId}.
func deploymentKey(apiID, deploymentID string) string {
	return apiID + "/" + deploymentID
}

func (s *apigatewayStore) putDeployment(ctx context.Context, apiID string, dep *Deployment) *protocol.AWSError {
	raw, err := json.Marshal(dep)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), deploymentKey(apiID, dep.ID))
	if err := s.store.Set(ctx, nsDeployments, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listDeployments(ctx context.Context, apiID string) ([]*Deployment, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsDeployments, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	deps := make([]*Deployment, 0, len(pairs))
	for _, p := range pairs {
		var dep Deployment
		if err := json.Unmarshal([]byte(p.Value), &dep); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule; storage-plan.md 3.1).
			continue
		}
		deps = append(deps, &dep)
	}
	return deps, nil
}

// deleteAllDeployments removes every deployment belonging to an API. Uses
// PrefixDeleter for a single ranged delete when the store supports it
// (storage-plan.md item 3.1), falling back to List+Delete otherwise.
func (s *apigatewayStore) deleteAllDeployments(ctx context.Context, apiID string) *protocol.AWSError {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	if deleter, ok := s.store.(state.PrefixDeleter); ok {
		if err := deleter.DeletePrefix(ctx, nsDeployments, prefix); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	keys, err := s.store.List(ctx, nsDeployments, prefix)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, k := range keys {
		if err := s.store.Delete(ctx, nsDeployments, k); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return nil
}

// ---- HTTP API v2 CRUD -----------------------------------------------------

func (s *apigatewayStore) putV2API(ctx context.Context, api *APIV2) *protocol.AWSError {
	raw, err := json.Marshal(api)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), api.ApiID)
	if err := s.store.Set(ctx, nsV2APIs, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getV2API(ctx context.Context, apiID string) (*APIV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID)
	raw, found, err := s.store.Get(ctx, nsV2APIs, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errV2APINotFound(apiID)
	}
	var api APIV2
	if err := json.Unmarshal([]byte(raw), &api); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &api, nil
}

func (s *apigatewayStore) deleteV2API(ctx context.Context, apiID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), apiID)
	if err := s.store.Delete(ctx, nsV2APIs, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2APIs(ctx context.Context) ([]*APIV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsV2APIs, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	apis := make([]*APIV2, 0, len(pairs))
	for _, p := range pairs {
		var api APIV2
		if err := json.Unmarshal([]byte(p.Value), &api); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		apis = append(apis, &api)
	}
	return apis, nil
}

// ---- V2 Route CRUD --------------------------------------------------------

func (s *apigatewayStore) putV2Route(ctx context.Context, apiID string, route *RouteV2) *protocol.AWSError {
	raw, err := json.Marshal(route)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+route.RouteID)
	if err := s.store.Set(ctx, nsV2Routes, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.routes.dropV2Routes(serviceutil.RegionKey(s.region(ctx), apiID))
	return nil
}

func (s *apigatewayStore) getV2Route(ctx context.Context, apiID, routeID string) (*RouteV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+routeID)
	raw, found, err := s.store.Get(ctx, nsV2Routes, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errV2RouteNotFound(routeID)
	}
	var route RouteV2
	if err := json.Unmarshal([]byte(raw), &route); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &route, nil
}

func (s *apigatewayStore) deleteV2Route(ctx context.Context, apiID, routeID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+routeID)
	if err := s.store.Delete(ctx, nsV2Routes, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.routes.dropV2Routes(serviceutil.RegionKey(s.region(ctx), apiID))
	return nil
}

func (s *apigatewayStore) listV2Routes(ctx context.Context, apiID string) ([]*RouteV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsV2Routes, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	routes := make([]*RouteV2, 0, len(pairs))
	for _, p := range pairs {
		var route RouteV2
		if err := json.Unmarshal([]byte(p.Value), &route); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule; storage-plan.md 3.1).
			continue
		}
		routes = append(routes, &route)
	}
	return routes, nil
}

// listV2RoutesCached is listV2Routes behind the route cache, for the
// execution path only: the result is shared across concurrent requests and
// must be treated as read-only (see routeCache).
func (s *apigatewayStore) listV2RoutesCached(ctx context.Context, apiID string) ([]*RouteV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID)
	if rs, ok := s.routes.getV2Routes(key); ok {
		return rs, nil
	}
	rs, aerr := s.listV2Routes(ctx, apiID)
	if aerr != nil {
		return nil, aerr
	}
	s.routes.setV2Routes(key, rs)
	return rs, nil
}

// deleteAllV2Routes removes every route belonging to a v2 API. Uses
// PrefixDeleter for a single ranged delete when the store supports it
// (storage-plan.md item 3.1), falling back to List+Delete otherwise.
func (s *apigatewayStore) deleteAllV2Routes(ctx context.Context, apiID string) *protocol.AWSError {
	defer s.routes.dropV2Routes(serviceutil.RegionKey(s.region(ctx), apiID))
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	if deleter, ok := s.store.(state.PrefixDeleter); ok {
		if err := deleter.DeletePrefix(ctx, nsV2Routes, prefix); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	keys, err := s.store.List(ctx, nsV2Routes, prefix)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, k := range keys {
		if err := s.store.Delete(ctx, nsV2Routes, k); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return nil
}

// ---- V2 Integration CRUD --------------------------------------------------

func (s *apigatewayStore) putV2Integration(ctx context.Context, apiID string, integ *IntegrationV2) *protocol.AWSError {
	raw, err := json.Marshal(integ)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+integ.IntegrationID)
	if err := s.store.Set(ctx, nsV2Integ, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getV2Integration(ctx context.Context, apiID, integID string) (*IntegrationV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+integID)
	raw, found, err := s.store.Get(ctx, nsV2Integ, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errV2IntegrationNotFound(integID)
	}
	var integ IntegrationV2
	if err := json.Unmarshal([]byte(raw), &integ); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &integ, nil
}

func (s *apigatewayStore) deleteV2Integration(ctx context.Context, apiID, integID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+integID)
	if err := s.store.Delete(ctx, nsV2Integ, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2Integrations(ctx context.Context, apiID string) ([]*IntegrationV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsV2Integ, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	integs := make([]*IntegrationV2, 0, len(pairs))
	for _, p := range pairs {
		var integ IntegrationV2
		if err := json.Unmarshal([]byte(p.Value), &integ); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule; storage-plan.md 3.1).
			continue
		}
		integs = append(integs, &integ)
	}
	return integs, nil
}

// deleteAllV2Integrations removes every integration belonging to a v2 API.
// Uses PrefixDeleter for a single ranged delete when the store supports it
// (storage-plan.md item 3.1), falling back to List+Delete otherwise.
func (s *apigatewayStore) deleteAllV2Integrations(ctx context.Context, apiID string) *protocol.AWSError {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	if deleter, ok := s.store.(state.PrefixDeleter); ok {
		if err := deleter.DeletePrefix(ctx, nsV2Integ, prefix); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	keys, err := s.store.List(ctx, nsV2Integ, prefix)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, k := range keys {
		if err := s.store.Delete(ctx, nsV2Integ, k); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return nil
}

// ---- V2 Stage CRUD --------------------------------------------------------

func (s *apigatewayStore) putV2Stage(ctx context.Context, apiID string, stage *StageV2) *protocol.AWSError {
	raw, err := json.Marshal(stage)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), stageKey(apiID, stage.StageName))
	if err := s.store.Set(ctx, nsV2Stages, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getV2Stage(ctx context.Context, apiID, stageName string) (*StageV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), stageKey(apiID, stageName))
	raw, found, err := s.store.Get(ctx, nsV2Stages, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errStageNotFound(stageName)
	}
	var stage StageV2
	if err := json.Unmarshal([]byte(raw), &stage); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &stage, nil
}

func (s *apigatewayStore) deleteV2Stage(ctx context.Context, apiID, stageName string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), stageKey(apiID, stageName))
	if err := s.store.Delete(ctx, nsV2Stages, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2Stages(ctx context.Context, apiID string) ([]*StageV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsV2Stages, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	stages := make([]*StageV2, 0, len(pairs))
	for _, p := range pairs {
		var stage StageV2
		if err := json.Unmarshal([]byte(p.Value), &stage); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule; storage-plan.md 3.1).
			continue
		}
		stages = append(stages, &stage)
	}
	return stages, nil
}

// deleteAllV2Stages removes every stage belonging to a v2 API. Uses
// PrefixDeleter for a single ranged delete when the store supports it
// (storage-plan.md item 3.1), falling back to List+Delete otherwise.
func (s *apigatewayStore) deleteAllV2Stages(ctx context.Context, apiID string) *protocol.AWSError {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	if deleter, ok := s.store.(state.PrefixDeleter); ok {
		if err := deleter.DeletePrefix(ctx, nsV2Stages, prefix); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	keys, err := s.store.List(ctx, nsV2Stages, prefix)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, k := range keys {
		if err := s.store.Delete(ctx, nsV2Stages, k); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return nil
}

// ---- V2 Deployment CRUD ---------------------------------------------------

func (s *apigatewayStore) putV2Deployment(ctx context.Context, apiID string, dep *DeploymentV2) *protocol.AWSError {
	raw, err := json.Marshal(dep)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), deploymentKey(apiID, dep.DeploymentID))
	if err := s.store.Set(ctx, nsV2Deploys, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2Deployments(ctx context.Context, apiID string) ([]*DeploymentV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsV2Deploys, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	deps := make([]*DeploymentV2, 0, len(pairs))
	for _, p := range pairs {
		var dep DeploymentV2
		if err := json.Unmarshal([]byte(p.Value), &dep); err != nil {
			// One malformed persisted record must not fail the whole list
			// (CLAUDE.md malformed-persisted-state rule; storage-plan.md 3.1).
			continue
		}
		deps = append(deps, &dep)
	}
	return deps, nil
}

// deleteAllV2Deployments removes every deployment belonging to a v2 API.
// Uses PrefixDeleter for a single ranged delete when the store supports it
// (storage-plan.md item 3.1), falling back to List+Delete otherwise.
func (s *apigatewayStore) deleteAllV2Deployments(ctx context.Context, apiID string) *protocol.AWSError {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	if deleter, ok := s.store.(state.PrefixDeleter); ok {
		if err := deleter.DeletePrefix(ctx, nsV2Deploys, prefix); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
		return nil
	}
	keys, err := s.store.List(ctx, nsV2Deploys, prefix)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, k := range keys {
		if err := s.store.Delete(ctx, nsV2Deploys, k); err != nil {
			return protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	return nil
}

// ---- Authorizer CRUD (REST v1) --------------------------------------------

func (s *apigatewayStore) putAuthorizer(ctx context.Context, apiID string, auth *Authorizer) *protocol.AWSError {
	raw, err := json.Marshal(auth)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+auth.ID)
	if err := s.store.Set(ctx, nsAuthorizers, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getAuthorizer(ctx context.Context, apiID, authID string) (*Authorizer, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+authID)
	raw, found, err := s.store.Get(ctx, nsAuthorizers, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errAuthorizerNotFound(authID)
	}
	var auth Authorizer
	if err := json.Unmarshal([]byte(raw), &auth); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &auth, nil
}

func (s *apigatewayStore) deleteAuthorizer(ctx context.Context, apiID, authID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+authID)
	if err := s.store.Delete(ctx, nsAuthorizers, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listAuthorizers(ctx context.Context, apiID string) ([]*Authorizer, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsAuthorizers, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	auths := make([]*Authorizer, 0, len(pairs))
	for _, p := range pairs {
		var auth Authorizer
		if err := json.Unmarshal([]byte(p.Value), &auth); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		auths = append(auths, &auth)
	}
	return auths, nil
}

// ---- API Key CRUD ---------------------------------------------------------

func (s *apigatewayStore) putAPIKey(ctx context.Context, key *APIKey) *protocol.AWSError {
	raw, err := json.Marshal(key)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	storeKey := serviceutil.RegionKey(s.region(ctx), key.ID)
	if err := s.store.Set(ctx, nsAPIKeys, storeKey, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getAPIKey(ctx context.Context, keyID string) (*APIKey, *protocol.AWSError) {
	storeKey := serviceutil.RegionKey(s.region(ctx), keyID)
	raw, found, err := s.store.Get(ctx, nsAPIKeys, storeKey)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errAPIKeyNotFound(keyID)
	}
	var key APIKey
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &key, nil
}

func (s *apigatewayStore) deleteAPIKey(ctx context.Context, keyID string) *protocol.AWSError {
	storeKey := serviceutil.RegionKey(s.region(ctx), keyID)
	if err := s.store.Delete(ctx, nsAPIKeys, storeKey); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listAPIKeys(ctx context.Context) ([]*APIKey, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsAPIKeys, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	keys := make([]*APIKey, 0, len(pairs))
	for _, p := range pairs {
		var key APIKey
		if err := json.Unmarshal([]byte(p.Value), &key); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		keys = append(keys, &key)
	}
	return keys, nil
}

// getAPIKeyByValue scans API keys in the current region and returns the one
// whose Value matches. Used by the execution handler to authenticate the
// `x-api-key` request header. Returns nil, nil if no key matches.
func (s *apigatewayStore) getAPIKeyByValue(ctx context.Context, value string) (*APIKey, *protocol.AWSError) {
	if value == "" {
		return nil, nil
	}
	keys, aerr := s.listAPIKeys(ctx)
	if aerr != nil {
		return nil, aerr
	}
	for _, k := range keys {
		if k.Value == value {
			return k, nil
		}
	}
	return nil, nil
}

// findUsagePlanForAPIKey returns the first usage plan in the current region
// that has the given API key attached AND whose APIStages include the given
// {apiID, stageName} pair. Returns nil if no such plan exists.
func (s *apigatewayStore) findUsagePlanForAPIKey(ctx context.Context, keyID, apiID, stageName string) (*UsagePlan, *protocol.AWSError) {
	plans, aerr := s.listUsagePlans(ctx)
	if aerr != nil {
		return nil, aerr
	}
	for _, plan := range plans {
		hasKey := false
		for _, k := range plan.KeyIDs {
			if k == keyID {
				hasKey = true
				break
			}
		}
		if !hasKey {
			continue
		}
		for _, stage := range plan.APIStages {
			if stage.ApiID == apiID && stage.Stage == stageName {
				return plan, nil
			}
		}
	}
	return nil, nil
}

// ---- Usage Plan CRUD ------------------------------------------------------

func (s *apigatewayStore) putUsagePlan(ctx context.Context, plan *UsagePlan) *protocol.AWSError {
	raw, err := json.Marshal(plan)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), plan.ID)
	if err := s.store.Set(ctx, nsUsagePlans, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getUsagePlan(ctx context.Context, planID string) (*UsagePlan, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), planID)
	raw, found, err := s.store.Get(ctx, nsUsagePlans, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errUsagePlanNotFound(planID)
	}
	var plan UsagePlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &plan, nil
}

func (s *apigatewayStore) deleteUsagePlan(ctx context.Context, planID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), planID)
	if err := s.store.Delete(ctx, nsUsagePlans, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listUsagePlans(ctx context.Context) ([]*UsagePlan, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsUsagePlans, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	plans := make([]*UsagePlan, 0, len(pairs))
	for _, p := range pairs {
		var plan UsagePlan
		if err := json.Unmarshal([]byte(p.Value), &plan); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		plans = append(plans, &plan)
	}
	return plans, nil
}

// ---- V2 Authorizer CRUD ---------------------------------------------------

func (s *apigatewayStore) putV2Authorizer(ctx context.Context, apiID string, auth *AuthorizerV2) *protocol.AWSError {
	raw, err := json.Marshal(auth)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+auth.AuthorizerID)
	if err := s.store.Set(ctx, nsV2Authorizer, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getV2Authorizer(ctx context.Context, apiID, authID string) (*AuthorizerV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+authID)
	raw, found, err := s.store.Get(ctx, nsV2Authorizer, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errAuthorizerNotFound(authID)
	}
	var auth AuthorizerV2
	if err := json.Unmarshal([]byte(raw), &auth); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &auth, nil
}

func (s *apigatewayStore) deleteV2Authorizer(ctx context.Context, apiID, authID string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), apiID+"/"+authID)
	if err := s.store.Delete(ctx, nsV2Authorizer, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2Authorizers(ctx context.Context, apiID string) ([]*AuthorizerV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsV2Authorizer, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	auths := make([]*AuthorizerV2, 0, len(pairs))
	for _, p := range pairs {
		var auth AuthorizerV2
		if err := json.Unmarshal([]byte(p.Value), &auth); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		auths = append(auths, &auth)
	}
	return auths, nil
}

// ---- Model CRUD ----------------------------------------------------------

// modelKey builds a store key for a model: {apiId}/{modelName}.
func modelKey(apiID, name string) string { return apiID + "/" + name }

func (s *apigatewayStore) putModel(ctx context.Context, apiID string, m *Model) *protocol.AWSError {
	raw, err := json.Marshal(m)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), modelKey(apiID, m.Name))
	if err := s.store.Set(ctx, nsModels, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getModel(ctx context.Context, apiID, name string) (*Model, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), modelKey(apiID, name))
	raw, found, err := s.store.Get(ctx, nsModels, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errModelNotFound(name)
	}
	var m Model
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &m, nil
}

func (s *apigatewayStore) deleteModel(ctx context.Context, apiID, name string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), modelKey(apiID, name))
	if err := s.store.Delete(ctx, nsModels, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listModels(ctx context.Context, apiID string) ([]*Model, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsModels, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	models := make([]*Model, 0, len(pairs))
	for _, p := range pairs {
		var m Model
		if err := json.Unmarshal([]byte(p.Value), &m); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		models = append(models, &m)
	}
	return models, nil
}

// ---- RequestValidator CRUD -----------------------------------------------

// validatorKey builds a store key for a request validator: {apiId}/{id}.
func validatorKey(apiID, id string) string { return apiID + "/" + id }

func (s *apigatewayStore) putRequestValidator(ctx context.Context, apiID string, rv *RequestValidator) *protocol.AWSError {
	raw, err := json.Marshal(rv)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), validatorKey(apiID, rv.ID))
	if err := s.store.Set(ctx, nsValidators, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) deleteRequestValidator(ctx context.Context, apiID, id string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), validatorKey(apiID, id))
	if err := s.store.Delete(ctx, nsValidators, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listRequestValidators(ctx context.Context, apiID string) ([]*RequestValidator, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), apiID+"/")
	pairs, err := s.store.Scan(ctx, nsValidators, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	rvs := make([]*RequestValidator, 0, len(pairs))
	for _, p := range pairs {
		var rv RequestValidator
		if err := json.Unmarshal([]byte(p.Value), &rv); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		rvs = append(rvs, &rv)
	}
	return rvs, nil
}

// ---- Domain Names (v1) ---------------------------------------------------

func (s *apigatewayStore) putDomainName(ctx context.Context, dn *DomainName) *protocol.AWSError {
	raw, err := json.Marshal(dn)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), dn.DomainName)
	if err := s.store.Set(ctx, nsDomainNames, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getDomainName(ctx context.Context, domainName string) (*DomainName, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), domainName)
	raw, found, err := s.store.Get(ctx, nsDomainNames, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errDomainNameNotFound(domainName)
	}
	var dn DomainName
	if err := json.Unmarshal([]byte(raw), &dn); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &dn, nil
}

func (s *apigatewayStore) deleteDomainName(ctx context.Context, domainName string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), domainName)
	if err := s.store.Delete(ctx, nsDomainNames, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listDomainNames(ctx context.Context) ([]*DomainName, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsDomainNames, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*DomainName, 0, len(pairs))
	for _, p := range pairs {
		var dn DomainName
		if err := json.Unmarshal([]byte(p.Value), &dn); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &dn)
	}
	return items, nil
}

// listAllDomainNames returns all v1 domain names across all regions.
func (s *apigatewayStore) listAllDomainNames(ctx context.Context) ([]*DomainName, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsDomainNames, "")
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*DomainName, 0, len(pairs))
	for _, p := range pairs {
		var dn DomainName
		if err := json.Unmarshal([]byte(p.Value), &dn); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &dn)
	}
	return items, nil
}

// ---- Base Path Mappings (v1) ---------------------------------------------

func (s *apigatewayStore) putBasePathMapping(ctx context.Context, domainName string, bpm *BasePathMapping) *protocol.AWSError {
	raw, err := json.Marshal(bpm)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	basePath := bpm.BasePath
	if basePath == "" {
		basePath = "(none)"
	}
	key := serviceutil.RegionKey(s.region(ctx), domainName+"/"+basePath)
	if err := s.store.Set(ctx, nsBasePathMaps, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listBasePathMappings(ctx context.Context, domainName string) ([]*BasePathMapping, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), domainName+"/")
	pairs, err := s.store.Scan(ctx, nsBasePathMaps, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*BasePathMapping, 0, len(pairs))
	for _, p := range pairs {
		var bpm BasePathMapping
		if err := json.Unmarshal([]byte(p.Value), &bpm); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &bpm)
	}
	return items, nil
}

// ---- VPC Links (v1) ------------------------------------------------------

func (s *apigatewayStore) putVpcLink(ctx context.Context, vl *VpcLink) *protocol.AWSError {
	raw, err := json.Marshal(vl)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), vl.ID)
	if err := s.store.Set(ctx, nsVpcLinks, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getVpcLink(ctx context.Context, id string) (*VpcLink, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), id)
	raw, found, err := s.store.Get(ctx, nsVpcLinks, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errVpcLinkNotFound(id)
	}
	var vl VpcLink
	if err := json.Unmarshal([]byte(raw), &vl); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &vl, nil
}

func (s *apigatewayStore) deleteVpcLink(ctx context.Context, id string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), id)
	if err := s.store.Delete(ctx, nsVpcLinks, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listVpcLinks(ctx context.Context) ([]*VpcLink, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsVpcLinks, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*VpcLink, 0, len(pairs))
	for _, p := range pairs {
		var vl VpcLink
		if err := json.Unmarshal([]byte(p.Value), &vl); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &vl)
	}
	return items, nil
}

// ---- Resource Tags (v1) --------------------------------------------------

func (s *apigatewayStore) putResourceTags(ctx context.Context, arn string, tags map[string]string) *protocol.AWSError {
	raw, err := json.Marshal(tags)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsResourceTags, arn, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getResourceTags(ctx context.Context, arn string) (map[string]string, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsResourceTags, arn)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return map[string]string{}, nil
	}
	var tags map[string]string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return tags, nil
}

// ---- Domain Names (v2) ---------------------------------------------------

func (s *apigatewayStore) putV2DomainName(ctx context.Context, dn *DomainNameV2) *protocol.AWSError {
	raw, err := json.Marshal(dn)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), dn.DomainName)
	if err := s.store.Set(ctx, nsV2DomainName, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getV2DomainName(ctx context.Context, domainName string) (*DomainNameV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), domainName)
	raw, found, err := s.store.Get(ctx, nsV2DomainName, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errDomainNameNotFound(domainName)
	}
	var dn DomainNameV2
	if err := json.Unmarshal([]byte(raw), &dn); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &dn, nil
}

func (s *apigatewayStore) deleteV2DomainName(ctx context.Context, domainName string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), domainName)
	if err := s.store.Delete(ctx, nsV2DomainName, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2DomainNames(ctx context.Context) ([]*DomainNameV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsV2DomainName, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*DomainNameV2, 0, len(pairs))
	for _, p := range pairs {
		var dn DomainNameV2
		if err := json.Unmarshal([]byte(p.Value), &dn); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &dn)
	}
	return items, nil
}

// listAllV2DomainNames returns all v2 domain names across all regions.
func (s *apigatewayStore) listAllV2DomainNames(ctx context.Context) ([]*DomainNameV2, *protocol.AWSError) {
	pairs, err := s.store.Scan(ctx, nsV2DomainName, "")
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*DomainNameV2, 0, len(pairs))
	for _, p := range pairs {
		var dn DomainNameV2
		if err := json.Unmarshal([]byte(p.Value), &dn); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &dn)
	}
	return items, nil
}

// ---- VPC Links (v2) ------------------------------------------------------

func (s *apigatewayStore) putV2VpcLink(ctx context.Context, vl *VpcLinkV2) *protocol.AWSError {
	raw, err := json.Marshal(vl)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), vl.VpcLinkID)
	if err := s.store.Set(ctx, nsV2VpcLinks, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getV2VpcLink(ctx context.Context, id string) (*VpcLinkV2, *protocol.AWSError) {
	key := serviceutil.RegionKey(s.region(ctx), id)
	raw, found, err := s.store.Get(ctx, nsV2VpcLinks, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errVpcLinkNotFound(id)
	}
	var vl VpcLinkV2
	if err := json.Unmarshal([]byte(raw), &vl); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &vl, nil
}

func (s *apigatewayStore) deleteV2VpcLink(ctx context.Context, id string) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), id)
	if err := s.store.Delete(ctx, nsV2VpcLinks, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2VpcLinks(ctx context.Context) ([]*VpcLinkV2, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), "")
	pairs, err := s.store.Scan(ctx, nsV2VpcLinks, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*VpcLinkV2, 0, len(pairs))
	for _, p := range pairs {
		var vl VpcLinkV2
		if err := json.Unmarshal([]byte(p.Value), &vl); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &vl)
	}
	return items, nil
}

// ---- API Mappings (v2) ---------------------------------------------------

func (s *apigatewayStore) putV2ApiMapping(ctx context.Context, domainName string, m *ApiMapping) *protocol.AWSError {
	raw, err := json.Marshal(m)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := serviceutil.RegionKey(s.region(ctx), domainName+"/"+m.ApiMappingID)
	if err := s.store.Set(ctx, nsV2ApiMapping, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) listV2ApiMappings(ctx context.Context, domainName string) ([]*ApiMapping, *protocol.AWSError) {
	prefix := serviceutil.RegionKey(s.region(ctx), domainName+"/")
	pairs, err := s.store.Scan(ctx, nsV2ApiMapping, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	items := make([]*ApiMapping, 0, len(pairs))
	for _, p := range pairs {
		var m ApiMapping
		if err := json.Unmarshal([]byte(p.Value), &m); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		items = append(items, &m)
	}
	return items, nil
}

// ---- v2 Tags -------------------------------------------------------------

func (s *apigatewayStore) putV2Tags(ctx context.Context, arn string, tags map[string]string) *protocol.AWSError {
	raw, err := json.Marshal(tags)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsV2Tags, arn, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *apigatewayStore) getV2Tags(ctx context.Context, arn string) (map[string]string, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsV2Tags, arn)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return map[string]string{}, nil
	}
	var tags map[string]string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return tags, nil
}

// ---- API-specific errors --------------------------------------------------

func errRestAPINotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid REST API identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errResourceNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid Resource identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errStageNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid stage identifier specified: %s", name),
		HTTPStatus: http.StatusNotFound,
	}
}

func errV2APINotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid API identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errV2RouteNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid Route identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errV2IntegrationNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid Integration identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errConflict(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ConflictException",
		Message:    msg,
		HTTPStatus: http.StatusConflict,
	}
}

func errBadRequest(msg string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "BadRequestException",
		Message:    msg,
		HTTPStatus: http.StatusBadRequest,
	}
}

func errAuthorizerNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid Authorizer identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errAPIKeyNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid API Key identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errUsagePlanNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid Usage Plan identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errModelNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid Model name specified: %s", name),
		HTTPStatus: http.StatusNotFound,
	}
}

func errDomainNameNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid domain name identifier specified: %s", name),
		HTTPStatus: http.StatusNotFound,
	}
}

func errVpcLinkNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid VPC Link identifier specified: %s", id),
		HTTPStatus: http.StatusNotFound,
	}
}
