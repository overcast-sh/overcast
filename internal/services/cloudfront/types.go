package cloudfront

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ─── Wire format ─────────────────────────────────────────────────────────────

// xmlNamespace is the namespace the pinned model declares on the CloudFront
// service shape (smithy.api#xmlNamespace). Every response root carries it as
// its default xmlns; nested elements inherit it.
const xmlNamespace = "http://cloudfront.amazonaws.com/doc/2020-05-31/"

// writeXML writes a successful CloudFront response, namespaced. Handlers call
// this rather than protocol.WriteXML so that no response can be added without
// the namespace.
func writeXML(w http.ResponseWriter, r *http.Request, status int, v any) {
	protocol.WriteXMLNS(w, r, status, xmlNamespace, v)
}

// writeError writes a CloudFront error. The service's protocol trait is
// "aws.protocols#restXml": {} with no noErrorWrapping, so errors go in the
// wrapped <ErrorResponse> envelope — not S3's bare <Error>, which every
// handler here used to reach for because protocol.WriteXMLError was the only
// REST-XML error writer (#2010).
func writeError(w http.ResponseWriter, r *http.Request, aerr *protocol.AWSError) {
	protocol.WriteRESTXMLError(w, r, xmlNamespace, aerr)
}

// ─── Distribution ────────────────────────────────────────────────────────────

// Distribution is the top-level response wrapper returned by CreateDistribution
// and GetDistribution. Dual xml+json tags allow the same type to be used for
// both REST-XML wire format and JSON state persistence.
//
// Field order is element order on the wire, so it follows the pinned model's
// member order for the Distribution shape.
type Distribution struct {
	XMLName                       xml.Name           `xml:"Distribution" json:"-"`
	ID                            string             `xml:"Id" json:"id"`
	ARN                           string             `xml:"ARN" json:"arn"`
	Status                        string             `xml:"Status" json:"status"`
	LastModifiedTime              time.Time          `xml:"LastModifiedTime" json:"last_modified_time"`
	InProgressInvalidationBatches int                `xml:"InProgressInvalidationBatches" json:"in_progress_invalidation_batches"`
	DomainName                    string             `xml:"DomainName" json:"domain_name"`
	ActiveTrustedSigners          *ActiveTrustedList `xml:"ActiveTrustedSigners,omitempty" json:"active_trusted_signers,omitempty"`
	ActiveTrustedKeyGroups        *ActiveTrustedList `xml:"ActiveTrustedKeyGroups,omitempty" json:"active_trusted_key_groups,omitempty"`
	DistributionConfig            DistributionConfig `xml:"DistributionConfig" json:"distribution_config"`

	// Version is the internal optimistic-concurrency counter. It is never
	// sent on the wire — the ETag header carries a quoted representation.
	Version int `xml:"-" json:"version"`
}

// ActiveTrustedList represents ActiveTrustedSigners / ActiveTrustedKeyGroups.
type ActiveTrustedList struct {
	Enabled  bool `xml:"Enabled" json:"enabled"`
	Quantity int  `xml:"Quantity" json:"quantity"`
}

// ─── DistributionConfig ──────────────────────────────────────────────────────

// DistributionConfig holds the full configuration for a distribution.
// Every field the SDKs commonly send is modelled so that round-trip
// (decode → store → encode) preserves the caller's intent.
//
// Field order is element order on the wire, so it follows the pinned model's
// member order for the DistributionConfig shape. The members Overcast does
// not model yet (AnycastIpListId, TenantConfig, ConnectionMode,
// ViewerMtlsConfig, ConnectionFunctionAssociation) sit between Staging and
// CacheTagConfig there.
type DistributionConfig struct {
	XMLName              xml.Name              `xml:"DistributionConfig" json:"-"`
	CallerReference      string                `xml:"CallerReference" json:"caller_reference"`
	Aliases              *StringList           `xml:"Aliases,omitempty" json:"aliases,omitempty"`
	DefaultRootObject    string                `xml:"DefaultRootObject,omitempty" json:"default_root_object,omitempty"`
	Origins              Origins               `xml:"Origins" json:"origins"`
	OriginGroups         *OriginGroups         `xml:"OriginGroups,omitempty" json:"origin_groups,omitempty"`
	DefaultCacheBehavior DefaultCacheBehavior  `xml:"DefaultCacheBehavior" json:"default_cache_behavior"`
	CacheBehaviors       *CacheBehaviors       `xml:"CacheBehaviors,omitempty" json:"cache_behaviors,omitempty"`
	CustomErrorResponses *CustomErrorResponses `xml:"CustomErrorResponses,omitempty" json:"custom_error_responses,omitempty"`
	Comment              string                `xml:"Comment" json:"comment"`
	Logging              *LoggingConfig        `xml:"Logging,omitempty" json:"logging,omitempty"`
	PriceClass           string                `xml:"PriceClass,omitempty" json:"price_class,omitempty"`
	Enabled              bool                  `xml:"Enabled" json:"enabled"`
	ViewerCertificate    *ViewerCertificate    `xml:"ViewerCertificate,omitempty" json:"viewer_certificate,omitempty"`
	Restrictions         *Restrictions         `xml:"Restrictions,omitempty" json:"restrictions,omitempty"`
	WebACLId             string                `xml:"WebACLId,omitempty" json:"web_acl_id,omitempty"`
	HttpVersion          string                `xml:"HttpVersion,omitempty" json:"http_version,omitempty"`
	IsIPV6Enabled        *bool                 `xml:"IsIPV6Enabled,omitempty" json:"is_ipv6_enabled,omitempty"`

	// ContinuousDeploymentPolicyId links this distribution to a continuous
	// deployment policy that routes a portion of traffic to a staging distribution.
	ContinuousDeploymentPolicyId string `xml:"ContinuousDeploymentPolicyId,omitempty" json:"continuous_deployment_policy_id,omitempty"`

	// Staging marks this distribution as a staging distribution used for
	// continuous deployment testing before promoting to primary.
	Staging *bool `xml:"Staging,omitempty" json:"staging,omitempty"`

	// CacheTagConfig configures cache-tag-based invalidation. When set,
	// the proxy extracts tags from the origin response header specified
	// by HeaderName, and the CreateInvalidation API accepts "#tag" paths
	// to invalidate by tag.
	CacheTagConfig *CacheTagConfig `xml:"CacheTagConfig,omitempty" json:"cache_tag_config,omitempty"`
}

// CacheTagConfig configures which origin response header to use as the
// source of cache tags for tag-based invalidation.
type CacheTagConfig struct {
	HeaderName string `xml:"HeaderName" json:"header_name"`
}

// ─── Origins ─────────────────────────────────────────────────────────────────

// Origins holds the list of origins for a distribution.
type Origins struct {
	Quantity int      `xml:"Quantity" json:"quantity"`
	Items    []Origin `xml:"Items>Origin" json:"items"`
}

// Origin represents a single origin server (S3 bucket or custom HTTP endpoint).
type Origin struct {
	ID                    string              `xml:"Id" json:"id"`
	DomainName            string              `xml:"DomainName" json:"domain_name"`
	OriginPath            string              `xml:"OriginPath,omitempty" json:"origin_path,omitempty"`
	S3OriginConfig        *S3OriginConfig     `xml:"S3OriginConfig,omitempty" json:"s3_origin_config,omitempty"`
	CustomOriginConfig    *CustomOriginConfig `xml:"CustomOriginConfig,omitempty" json:"custom_origin_config,omitempty"`
	CustomHeaders         *CustomHeaders      `xml:"CustomHeaders,omitempty" json:"custom_headers,omitempty"`
	ConnectionAttempts    *int                `xml:"ConnectionAttempts,omitempty" json:"connection_attempts,omitempty"`
	ConnectionTimeout     *int                `xml:"ConnectionTimeout,omitempty" json:"connection_timeout,omitempty"`
	OriginAccessControlId string              `xml:"OriginAccessControlId,omitempty" json:"origin_access_control_id,omitempty"`
	OriginShield          *OriginShield       `xml:"OriginShield,omitempty" json:"origin_shield,omitempty"`
}

// S3OriginConfig is used when the origin is an S3 bucket with legacy OAI.
type S3OriginConfig struct {
	OriginAccessIdentity string `xml:"OriginAccessIdentity" json:"origin_access_identity"`
}

// CustomOriginConfig is used for non-S3 (HTTP/HTTPS) origins.
type CustomOriginConfig struct {
	HTTPPort               int         `xml:"HTTPPort" json:"http_port"`
	HTTPSPort              int         `xml:"HTTPSPort" json:"https_port"`
	OriginProtocolPolicy   string      `xml:"OriginProtocolPolicy" json:"origin_protocol_policy"`
	OriginSslProtocols     *StringList `xml:"OriginSslProtocols,omitempty" json:"origin_ssl_protocols,omitempty"`
	OriginReadTimeout      *int        `xml:"OriginReadTimeout,omitempty" json:"origin_read_timeout,omitempty"`
	OriginKeepaliveTimeout *int        `xml:"OriginKeepaliveTimeout,omitempty" json:"origin_keepalive_timeout,omitempty"`
}

// CustomHeaders holds origin custom headers injected on every origin request.
type CustomHeaders struct {
	Quantity int                  `xml:"Quantity" json:"quantity"`
	Items    []OriginCustomHeader `xml:"Items>OriginCustomHeader,omitempty" json:"items,omitempty"`
}

// OriginCustomHeader is a single header name-value pair forwarded to the origin.
type OriginCustomHeader struct {
	HeaderName  string `xml:"HeaderName" json:"header_name"`
	HeaderValue string `xml:"HeaderValue" json:"header_value"`
}

// ─── OriginGroups ─────────────────────────────────────────────────────────────

// OriginGroups represents the OriginGroups list in a distribution config.
type OriginGroups struct {
	XMLName  xml.Name      `xml:"OriginGroups" json:"-"`
	Quantity int           `xml:"Quantity" json:"quantity"`
	Items    []OriginGroup `xml:"Items>OriginGroup" json:"items,omitempty"`
}

// OriginGroup represents a single origin group with primary and failover members.
type OriginGroup struct {
	ID               string              `xml:"Id" json:"id"`
	FailoverCriteria OriginGroupFailover `xml:"FailoverCriteria" json:"failover_criteria"`
	Members          OriginGroupMembers  `xml:"Members" json:"members"`
}

// OriginGroupFailover defines the HTTP status codes that trigger failover.
type OriginGroupFailover struct {
	StatusCodes OriginGroupStatusCodes `xml:"StatusCodes" json:"status_codes"`
}

// OriginGroupStatusCodes is the list of HTTP status codes for failover.
type OriginGroupStatusCodes struct {
	Quantity int   `xml:"Quantity" json:"quantity"`
	Items    []int `xml:"Items>StatusCode" json:"items,omitempty"`
}

// OriginGroupMembers lists the origins in an origin group.
type OriginGroupMembers struct {
	Quantity int                 `xml:"Quantity" json:"quantity"`
	Items    []OriginGroupMember `xml:"Items>OriginGroupMember" json:"items,omitempty"`
}

// OriginGroupMember references an origin by ID within an origin group.
type OriginGroupMember struct {
	OriginId string `xml:"OriginId" json:"origin_id"`
}

// OriginShield represents the OriginShield configuration for an origin.
type OriginShield struct {
	Enabled            bool   `xml:"Enabled" json:"enabled"`
	OriginShieldRegion string `xml:"OriginShieldRegion,omitempty" json:"origin_shield_region,omitempty"`
}

// ─── Cache Behaviors ─────────────────────────────────────────────────────────

// DefaultCacheBehavior controls how CloudFront handles requests when no
// path-pattern-specific CacheBehavior matches.
type DefaultCacheBehavior struct {
	TargetOriginId             string                      `xml:"TargetOriginId" json:"target_origin_id"`
	ViewerProtocolPolicy       string                      `xml:"ViewerProtocolPolicy" json:"viewer_protocol_policy"`
	AllowedMethods             *AllowedMethods             `xml:"AllowedMethods,omitempty" json:"allowed_methods,omitempty"`
	ForwardedValues            *ForwardedValues            `xml:"ForwardedValues,omitempty" json:"forwarded_values,omitempty"`
	MinTTL                     *int64                      `xml:"MinTTL,omitempty" json:"min_ttl,omitempty"`
	DefaultTTL                 *int64                      `xml:"DefaultTTL,omitempty" json:"default_ttl,omitempty"`
	MaxTTL                     *int64                      `xml:"MaxTTL,omitempty" json:"max_ttl,omitempty"`
	Compress                   *bool                       `xml:"Compress,omitempty" json:"compress,omitempty"`
	SmoothStreaming            *bool                       `xml:"SmoothStreaming,omitempty" json:"smooth_streaming,omitempty"`
	CachePolicyId              string                      `xml:"CachePolicyId,omitempty" json:"cache_policy_id,omitempty"`
	OriginRequestPolicyId      string                      `xml:"OriginRequestPolicyId,omitempty" json:"origin_request_policy_id,omitempty"`
	ResponseHeadersPolicyId    string                      `xml:"ResponseHeadersPolicyId,omitempty" json:"response_headers_policy_id,omitempty"`
	FieldLevelEncryptionId     string                      `xml:"FieldLevelEncryptionId,omitempty" json:"field_level_encryption_id,omitempty"`
	RealtimeLogConfigArn       string                      `xml:"RealtimeLogConfigArn,omitempty" json:"realtime_log_config_arn,omitempty"`
	TrustedSigners             *TrustedEntities            `xml:"TrustedSigners,omitempty" json:"trusted_signers,omitempty"`
	TrustedKeyGroups           *TrustedEntities            `xml:"TrustedKeyGroups,omitempty" json:"trusted_key_groups,omitempty"`
	LambdaFunctionAssociations *LambdaFunctionAssociations `xml:"LambdaFunctionAssociations,omitempty" json:"lambda_function_associations,omitempty"`
	FunctionAssociations       *FunctionAssociations       `xml:"FunctionAssociations,omitempty" json:"function_associations,omitempty"`

	// TODO(priority:P5): enforce signed URL/cookie validation via TrustedSigners/TrustedKeyGroups.
	// TODO(priority:P5): FieldLevelEncryption support.
}

// CacheBehaviors holds path-pattern-specific cache behaviours.
type CacheBehaviors struct {
	Quantity int             `xml:"Quantity" json:"quantity"`
	Items    []CacheBehavior `xml:"Items>CacheBehavior,omitempty" json:"items,omitempty"`
}

// CacheBehavior is a path-pattern-specific behaviour. It includes all fields
// from DefaultCacheBehavior plus a PathPattern for matching.
type CacheBehavior struct {
	PathPattern                string                      `xml:"PathPattern" json:"path_pattern"`
	TargetOriginId             string                      `xml:"TargetOriginId" json:"target_origin_id"`
	ViewerProtocolPolicy       string                      `xml:"ViewerProtocolPolicy" json:"viewer_protocol_policy"`
	AllowedMethods             *AllowedMethods             `xml:"AllowedMethods,omitempty" json:"allowed_methods,omitempty"`
	ForwardedValues            *ForwardedValues            `xml:"ForwardedValues,omitempty" json:"forwarded_values,omitempty"`
	MinTTL                     *int64                      `xml:"MinTTL,omitempty" json:"min_ttl,omitempty"`
	DefaultTTL                 *int64                      `xml:"DefaultTTL,omitempty" json:"default_ttl,omitempty"`
	MaxTTL                     *int64                      `xml:"MaxTTL,omitempty" json:"max_ttl,omitempty"`
	Compress                   *bool                       `xml:"Compress,omitempty" json:"compress,omitempty"`
	SmoothStreaming            *bool                       `xml:"SmoothStreaming,omitempty" json:"smooth_streaming,omitempty"`
	CachePolicyId              string                      `xml:"CachePolicyId,omitempty" json:"cache_policy_id,omitempty"`
	OriginRequestPolicyId      string                      `xml:"OriginRequestPolicyId,omitempty" json:"origin_request_policy_id,omitempty"`
	ResponseHeadersPolicyId    string                      `xml:"ResponseHeadersPolicyId,omitempty" json:"response_headers_policy_id,omitempty"`
	FieldLevelEncryptionId     string                      `xml:"FieldLevelEncryptionId,omitempty" json:"field_level_encryption_id,omitempty"`
	RealtimeLogConfigArn       string                      `xml:"RealtimeLogConfigArn,omitempty" json:"realtime_log_config_arn,omitempty"`
	TrustedSigners             *TrustedEntities            `xml:"TrustedSigners,omitempty" json:"trusted_signers,omitempty"`
	TrustedKeyGroups           *TrustedEntities            `xml:"TrustedKeyGroups,omitempty" json:"trusted_key_groups,omitempty"`
	LambdaFunctionAssociations *LambdaFunctionAssociations `xml:"LambdaFunctionAssociations,omitempty" json:"lambda_function_associations,omitempty"`
	FunctionAssociations       *FunctionAssociations       `xml:"FunctionAssociations,omitempty" json:"function_associations,omitempty"`
}

// ─── Forwarding & Allowed Methods ────────────────────────────────────────────

// ForwardedValues controls which request components are forwarded to the origin
// and included in the cache key (legacy — CachePolicy is the modern replacement).
type ForwardedValues struct {
	QueryString          bool              `xml:"QueryString" json:"query_string"`
	Cookies              *CookiePreference `xml:"Cookies,omitempty" json:"cookies,omitempty"`
	Headers              *StringList       `xml:"Headers,omitempty" json:"headers,omitempty"`
	QueryStringCacheKeys *StringList       `xml:"QueryStringCacheKeys,omitempty" json:"query_string_cache_keys,omitempty"`
}

// CookiePreference controls cookie forwarding to origin.
type CookiePreference struct {
	Forward          string      `xml:"Forward" json:"forward"` // none | whitelist | all
	WhitelistedNames *StringList `xml:"WhitelistedNames,omitempty" json:"whitelisted_names,omitempty"`
}

// AllowedMethods lists the HTTP methods the distribution accepts.
type AllowedMethods struct {
	Quantity      int         `xml:"Quantity" json:"quantity"`
	Items         []string    `xml:"Items>Method" json:"items"`
	CachedMethods *StringList `xml:"CachedMethods,omitempty" json:"cached_methods,omitempty"`
}

// TrustedEntities represents TrustedSigners or TrustedKeyGroups.
type TrustedEntities struct {
	Enabled  bool     `xml:"Enabled" json:"enabled"`
	Quantity int      `xml:"Quantity" json:"quantity"`
	Items    []string `xml:"Items>AwsAccountNumber,omitempty" json:"items,omitempty"`
}

// ─── Lambda@Edge & CloudFront Functions ──────────────────────────────────────

// LambdaFunctionAssociations holds Lambda@Edge function triggers for a behaviour.
type LambdaFunctionAssociations struct {
	Quantity int                         `xml:"Quantity" json:"quantity"`
	Items    []LambdaFunctionAssociation `xml:"Items>LambdaFunctionAssociation,omitempty" json:"items,omitempty"`
}

// LambdaFunctionAssociation maps a Lambda function ARN to a CloudFront event type.
type LambdaFunctionAssociation struct {
	LambdaFunctionARN string `xml:"LambdaFunctionARN" json:"lambda_function_arn"`
	EventType         string `xml:"EventType" json:"event_type"` // viewer-request | viewer-response | origin-request | origin-response
	IncludeBody       *bool  `xml:"IncludeBody,omitempty" json:"include_body,omitempty"`
}

// FunctionAssociations holds CloudFront Functions triggers for a behaviour.
type FunctionAssociations struct {
	Quantity int                   `xml:"Quantity" json:"quantity"`
	Items    []FunctionAssociation `xml:"Items>FunctionAssociation,omitempty" json:"items,omitempty"`
}

// FunctionAssociation maps a CF Function ARN to a CloudFront event type.
type FunctionAssociation struct {
	FunctionARN string `xml:"FunctionARN" json:"function_arn"`
	EventType   string `xml:"EventType" json:"event_type"` // viewer-request | viewer-response
}

// ─── Distribution extras ────────────────────────────────────────────────────

// StringList is a generic Quantity + Items wrapper used by Aliases,
// OriginSslProtocols, Headers, WhitelistedNames, etc.
type StringList struct {
	Quantity int      `xml:"Quantity" json:"quantity"`
	Items    []string `xml:"Items>Item,omitempty" json:"items,omitempty"`
}

// Restrictions controls geo-restriction.
type Restrictions struct {
	GeoRestriction GeoRestriction `xml:"GeoRestriction" json:"geo_restriction"`
}

// GeoRestriction configures geographic access restrictions.
type GeoRestriction struct {
	RestrictionType string   `xml:"RestrictionType" json:"restriction_type"` // none | whitelist | blacklist
	Quantity        int      `xml:"Quantity" json:"quantity"`
	Items           []string `xml:"Items>Location,omitempty" json:"items,omitempty"`
}

// ViewerCertificate controls the SSL/TLS certificate for the distribution.
type ViewerCertificate struct {
	CloudFrontDefaultCertificate *bool  `xml:"CloudFrontDefaultCertificate,omitempty" json:"cloudfront_default_certificate,omitempty"`
	ACMCertificateArn            string `xml:"ACMCertificateArn,omitempty" json:"acm_certificate_arn,omitempty"`
	IAMCertificateId             string `xml:"IAMCertificateId,omitempty" json:"iam_certificate_id,omitempty"`
	SSLSupportMethod             string `xml:"SSLSupportMethod,omitempty" json:"ssl_support_method,omitempty"`
	MinimumProtocolVersion       string `xml:"MinimumProtocolVersion,omitempty" json:"minimum_protocol_version,omitempty"`

	// TODO(priority:P5): TLS termination and certificate validation.
}

// CustomErrorResponses holds custom error page configuration.
type CustomErrorResponses struct {
	Quantity int                   `xml:"Quantity" json:"quantity"`
	Items    []CustomErrorResponse `xml:"Items>CustomErrorResponse,omitempty" json:"items,omitempty"`
}

// CustomErrorResponse maps an origin error code to a custom response page.
type CustomErrorResponse struct {
	ErrorCode          int    `xml:"ErrorCode" json:"error_code"`
	ResponsePagePath   string `xml:"ResponsePagePath,omitempty" json:"response_page_path,omitempty"`
	ResponseCode       string `xml:"ResponseCode,omitempty" json:"response_code,omitempty"`
	ErrorCachingMinTTL *int64 `xml:"ErrorCachingMinTTL,omitempty" json:"error_caching_min_ttl,omitempty"`
}

// LoggingConfig controls access logging to an S3 bucket.
type LoggingConfig struct {
	Enabled        bool   `xml:"Enabled" json:"enabled"`
	IncludeCookies bool   `xml:"IncludeCookies" json:"include_cookies"`
	Bucket         string `xml:"Bucket" json:"bucket"`
	Prefix         string `xml:"Prefix" json:"prefix"`
}

// ─── List response ──────────────────────────────────────────────────────────

// DistributionList is the response envelope for ListDistributions.
type DistributionList struct {
	XMLName     xml.Name              `xml:"DistributionList" json:"-"`
	Marker      string                `xml:"Marker" json:"marker"`
	NextMarker  string                `xml:"NextMarker,omitempty" json:"next_marker,omitempty"`
	MaxItems    int                   `xml:"MaxItems" json:"max_items"`
	IsTruncated bool                  `xml:"IsTruncated" json:"is_truncated"`
	Quantity    int                   `xml:"Quantity" json:"quantity"`
	Items       []DistributionSummary `xml:"Items>DistributionSummary,omitempty" json:"items,omitempty"`
}

// DistributionSummary is the per-item element within a DistributionList.
//
// Field order is element order on the wire, so it follows the pinned model's
// member order for the DistributionSummary shape.
type DistributionSummary struct {
	ID                   string               `xml:"Id" json:"id"`
	ARN                  string               `xml:"ARN" json:"arn"`
	Status               string               `xml:"Status" json:"status"`
	LastModifiedTime     time.Time            `xml:"LastModifiedTime" json:"last_modified_time"`
	DomainName           string               `xml:"DomainName" json:"domain_name"`
	Aliases              *StringList          `xml:"Aliases,omitempty" json:"aliases,omitempty"`
	Origins              Origins              `xml:"Origins" json:"origins"`
	DefaultCacheBehavior DefaultCacheBehavior `xml:"DefaultCacheBehavior" json:"default_cache_behavior"`
	CacheBehaviors       *CacheBehaviors      `xml:"CacheBehaviors,omitempty" json:"cache_behaviors,omitempty"`
	Comment              string               `xml:"Comment" json:"comment"`
	PriceClass           string               `xml:"PriceClass,omitempty" json:"price_class,omitempty"`
	Enabled              bool                 `xml:"Enabled" json:"enabled"`
	ViewerCertificate    *ViewerCertificate   `xml:"ViewerCertificate,omitempty" json:"viewer_certificate,omitempty"`
	Restrictions         *Restrictions        `xml:"Restrictions,omitempty" json:"restrictions,omitempty"`
	WebACLId             string               `xml:"WebACLId,omitempty" json:"web_acl_id,omitempty"`
	HttpVersion          string               `xml:"HttpVersion,omitempty" json:"http_version,omitempty"`
	IsIPV6Enabled        *bool                `xml:"IsIPV6Enabled,omitempty" json:"is_ipv6_enabled,omitempty"`
}

// ─── Invalidation types ─────────────────────────────────────────────────────

// Invalidation represents a single invalidation request.
type Invalidation struct {
	XMLName           xml.Name          `xml:"Invalidation" json:"-"`
	ID                string            `xml:"Id" json:"id"`
	Status            string            `xml:"Status" json:"status"` // InProgress | Completed
	CreateTime        time.Time         `xml:"CreateTime" json:"create_time"`
	InvalidationBatch InvalidationBatch `xml:"InvalidationBatch" json:"invalidation_batch"`
}

// InvalidationBatch holds the caller reference and paths to invalidate.
type InvalidationBatch struct {
	CallerReference string `xml:"CallerReference" json:"caller_reference"`
	Paths           Paths  `xml:"Paths" json:"paths"`
}

// Paths lists the object paths to invalidate.
type Paths struct {
	Quantity int      `xml:"Quantity" json:"quantity"`
	Items    []string `xml:"Items>Path,omitempty" json:"items,omitempty"`
}

// InvalidationList is the response envelope for ListInvalidations.
type InvalidationList struct {
	XMLName     xml.Name              `xml:"InvalidationList" json:"-"`
	Marker      string                `xml:"Marker" json:"marker"`
	NextMarker  string                `xml:"NextMarker,omitempty" json:"next_marker,omitempty"`
	MaxItems    int                   `xml:"MaxItems" json:"max_items"`
	IsTruncated bool                  `xml:"IsTruncated" json:"is_truncated"`
	Quantity    int                   `xml:"Quantity" json:"quantity"`
	Items       []InvalidationSummary `xml:"Items>InvalidationSummary,omitempty" json:"items,omitempty"`
}

// InvalidationSummary is the per-item element within an InvalidationList.
type InvalidationSummary struct {
	ID         string    `xml:"Id" json:"id"`
	CreateTime time.Time `xml:"CreateTime" json:"create_time"`
	Status     string    `xml:"Status" json:"status"`
}

// ─── Origin Access Control ──────────────────────────────────────────────────

// OriginAccessControl represents an OAC resource with its config.
type OriginAccessControl struct {
	XMLName                   xml.Name                  `xml:"OriginAccessControl" json:"-"`
	ID                        string                    `xml:"Id" json:"id"`
	OriginAccessControlConfig OriginAccessControlConfig `xml:"OriginAccessControlConfig" json:"origin_access_control_config"`
	Version                   int                       `xml:"-" json:"version"`
}

// OriginAccessControlConfig holds the configuration for an OAC.
type OriginAccessControlConfig struct {
	Name                          string `xml:"Name" json:"name"`
	Description                   string `xml:"Description,omitempty" json:"description,omitempty"`
	SigningProtocol               string `xml:"SigningProtocol" json:"signing_protocol"`                                // sigv4
	SigningBehavior               string `xml:"SigningBehavior" json:"signing_behavior"`                                // always | never | no-override
	OriginAccessControlOriginType string `xml:"OriginAccessControlOriginType" json:"origin_access_control_origin_type"` // s3 | mediastore
}

// OriginAccessControlList is the response envelope for ListOriginAccessControls.
type OriginAccessControlList struct {
	XMLName     xml.Name                     `xml:"OriginAccessControlList" json:"-"`
	Marker      string                       `xml:"Marker" json:"marker"`
	NextMarker  string                       `xml:"NextMarker,omitempty" json:"next_marker,omitempty"`
	MaxItems    int                          `xml:"MaxItems" json:"max_items"`
	IsTruncated bool                         `xml:"IsTruncated" json:"is_truncated"`
	Quantity    int                          `xml:"Quantity" json:"quantity"`
	Items       []OriginAccessControlSummary `xml:"Items>OriginAccessControlSummary,omitempty" json:"items,omitempty"`
}

// OriginAccessControlSummary is the per-item element within an OriginAccessControlList.
type OriginAccessControlSummary struct {
	ID                            string `xml:"Id" json:"id"`
	Name                          string `xml:"Name" json:"name"`
	Description                   string `xml:"Description,omitempty" json:"description,omitempty"`
	SigningProtocol               string `xml:"SigningProtocol" json:"signing_protocol"`
	SigningBehavior               string `xml:"SigningBehavior" json:"signing_behavior"`
	OriginAccessControlOriginType string `xml:"OriginAccessControlOriginType" json:"origin_access_control_origin_type"`
}

// ─── Tags ───────────────────────────────────────────────────────────────────

// Tags is the container for resource tags.
type Tags struct {
	XMLName xml.Name `xml:"Tags" json:"-"`
	Items   []Tag    `xml:"Items>Tag,omitempty" json:"items,omitempty"`
}

// Tagging is the response envelope for ListTagsForResource.
type Tagging struct {
	XMLName xml.Name `xml:"Tagging" json:"-"`
	Tags    Tags     `xml:"Tags" json:"tags"`
}

// Tag is a single key-value tag.
type Tag struct {
	Key   string `xml:"Key" json:"key"`
	Value string `xml:"Value" json:"value"`
}

// TagKeys is used in UntagResource requests.
type TagKeys struct {
	XMLName xml.Name `xml:"TagKeys" json:"-"`
	Items   []string `xml:"Items>Key,omitempty" json:"items,omitempty"`
}

// DistributionConfigWithTags wraps config + tags for CreateDistributionWithTags.
type DistributionConfigWithTags struct {
	XMLName            xml.Name           `xml:"DistributionConfigWithTags" json:"-"`
	DistributionConfig DistributionConfig `xml:"DistributionConfig" json:"distribution_config"`
	Tags               Tags               `xml:"Tags" json:"tags"`
}

// ─── Cache Policy ───────────────────────────────────────────────────────────

// CachePolicy represents a managed cache policy.
type CachePolicy struct {
	XMLName           xml.Name          `xml:"CachePolicy" json:"-"`
	ID                string            `xml:"Id" json:"id"`
	LastModifiedTime  time.Time         `xml:"LastModifiedTime" json:"last_modified_time"`
	CachePolicyConfig CachePolicyConfig `xml:"CachePolicyConfig" json:"cache_policy_config"`
	Version           int               `xml:"-" json:"version"`
}

// CachePolicyConfig holds the configuration for a cache policy.
type CachePolicyConfig struct {
	Name                                     string                                    `xml:"Name" json:"name"`
	Comment                                  string                                    `xml:"Comment,omitempty" json:"comment,omitempty"`
	DefaultTTL                               *int64                                    `xml:"DefaultTTL,omitempty" json:"default_ttl,omitempty"`
	MaxTTL                                   *int64                                    `xml:"MaxTTL,omitempty" json:"max_ttl,omitempty"`
	MinTTL                                   int64                                     `xml:"MinTTL" json:"min_ttl"`
	ParametersInCacheKeyAndForwardedToOrigin *ParametersInCacheKeyAndForwardedToOrigin `xml:"ParametersInCacheKeyAndForwardedToOrigin,omitempty" json:"parameters,omitempty"`
}

// ParametersInCacheKeyAndForwardedToOrigin controls what goes into the cache key.
type ParametersInCacheKeyAndForwardedToOrigin struct {
	EnableAcceptEncodingGzip   bool                          `xml:"EnableAcceptEncodingGzip" json:"enable_accept_encoding_gzip"`
	EnableAcceptEncodingBrotli bool                          `xml:"EnableAcceptEncodingBrotli" json:"enable_accept_encoding_brotli"`
	CookiesConfig              CachePolicyCookiesConfig      `xml:"CookiesConfig" json:"cookies_config"`
	HeadersConfig              CachePolicyHeadersConfig      `xml:"HeadersConfig" json:"headers_config"`
	QueryStringsConfig         CachePolicyQueryStringsConfig `xml:"QueryStringsConfig" json:"query_strings_config"`
}

// CachePolicyCookiesConfig controls cookie inclusion in the cache key.
type CachePolicyCookiesConfig struct {
	CookieBehavior string      `xml:"CookieBehavior" json:"cookie_behavior"` // none | whitelist | allExcept | all
	Cookies        *StringList `xml:"Cookies,omitempty" json:"cookies,omitempty"`
}

// CachePolicyHeadersConfig controls header inclusion in the cache key.
type CachePolicyHeadersConfig struct {
	HeaderBehavior string      `xml:"HeaderBehavior" json:"header_behavior"` // none | whitelist
	Headers        *StringList `xml:"Headers,omitempty" json:"headers,omitempty"`
}

// CachePolicyQueryStringsConfig controls query string inclusion in the cache key.
type CachePolicyQueryStringsConfig struct {
	QueryStringBehavior string      `xml:"QueryStringBehavior" json:"query_string_behavior"` // none | whitelist | allExcept | all
	QueryStrings        *StringList `xml:"QueryStrings,omitempty" json:"query_strings,omitempty"`
}

// CachePolicyList is the response envelope for ListCachePolicies.
type CachePolicyList struct {
	XMLName    xml.Name             `xml:"CachePolicyList" json:"-"`
	NextMarker string               `xml:"NextMarker,omitempty" json:"next_marker,omitempty"`
	MaxItems   int                  `xml:"MaxItems" json:"max_items"`
	Quantity   int                  `xml:"Quantity" json:"quantity"`
	Items      []CachePolicySummary `xml:"Items>CachePolicySummary,omitempty" json:"items,omitempty"`
}

// CachePolicySummary is the per-item element within a CachePolicyList.
type CachePolicySummary struct {
	Type        string      `xml:"Type" json:"type"` // managed | custom
	CachePolicy CachePolicy `xml:"CachePolicy" json:"cache_policy"`
}

// ─── Origin Request Policy ──────────────────────────────────────────────────

// OriginRequestPolicy represents a managed origin request policy.
type OriginRequestPolicy struct {
	XMLName                   xml.Name                  `xml:"OriginRequestPolicy" json:"-"`
	ID                        string                    `xml:"Id" json:"id"`
	LastModifiedTime          time.Time                 `xml:"LastModifiedTime" json:"last_modified_time"`
	OriginRequestPolicyConfig OriginRequestPolicyConfig `xml:"OriginRequestPolicyConfig" json:"origin_request_policy_config"`
	Version                   int                       `xml:"-" json:"version"`
}

// OriginRequestPolicyConfig holds the configuration for an origin request policy.
type OriginRequestPolicyConfig struct {
	Name               string                                `xml:"Name" json:"name"`
	Comment            string                                `xml:"Comment,omitempty" json:"comment,omitempty"`
	HeadersConfig      OriginRequestPolicyHeadersConfig      `xml:"HeadersConfig" json:"headers_config"`
	CookiesConfig      OriginRequestPolicyCookiesConfig      `xml:"CookiesConfig" json:"cookies_config"`
	QueryStringsConfig OriginRequestPolicyQueryStringsConfig `xml:"QueryStringsConfig" json:"query_strings_config"`
}

// OriginRequestPolicyCookiesConfig determines whether cookies in viewer requests
// are included in requests that CloudFront sends to the origin.
type OriginRequestPolicyCookiesConfig struct {
	CookieBehavior string      `xml:"CookieBehavior" json:"cookie_behavior"` // none | whitelist | all | allExcept
	Cookies        *StringList `xml:"Cookies,omitempty" json:"cookies,omitempty"`
}

// OriginRequestPolicyHeadersConfig determines whether HTTP headers in viewer requests
// are included in requests that CloudFront sends to the origin.
type OriginRequestPolicyHeadersConfig struct {
	HeaderBehavior string      `xml:"HeaderBehavior" json:"header_behavior"` // none | whitelist | allViewer | allViewerAndWhitelistCloudFront | allExcept
	Headers        *StringList `xml:"Headers,omitempty" json:"headers,omitempty"`
}

// OriginRequestPolicyQueryStringsConfig determines whether URL query strings in viewer
// requests are included in requests that CloudFront sends to the origin.
type OriginRequestPolicyQueryStringsConfig struct {
	QueryStringBehavior string      `xml:"QueryStringBehavior" json:"query_string_behavior"` // none | whitelist | all | allExcept
	QueryStrings        *StringList `xml:"QueryStrings,omitempty" json:"query_strings,omitempty"`
}

// OriginRequestPolicyList is the response envelope for ListOriginRequestPolicies.
type OriginRequestPolicyList struct {
	XMLName    xml.Name                     `xml:"OriginRequestPolicyList" json:"-"`
	NextMarker string                       `xml:"NextMarker,omitempty" json:"next_marker,omitempty"`
	MaxItems   int                          `xml:"MaxItems" json:"max_items"`
	Quantity   int                          `xml:"Quantity" json:"quantity"`
	Items      []OriginRequestPolicySummary `xml:"Items>OriginRequestPolicySummary,omitempty" json:"items,omitempty"`
}

// OriginRequestPolicySummary is the per-item element within an OriginRequestPolicyList.
type OriginRequestPolicySummary struct {
	Type                string              `xml:"Type" json:"type"`
	OriginRequestPolicy OriginRequestPolicy `xml:"OriginRequestPolicy" json:"origin_request_policy"`
}

// ─── Response Headers Policy ────────────────────────────────────────────────

// ResponseHeadersPolicy represents a managed response headers policy.
type ResponseHeadersPolicy struct {
	XMLName                     xml.Name                    `xml:"ResponseHeadersPolicy" json:"-"`
	ID                          string                      `xml:"Id" json:"id"`
	LastModifiedTime            time.Time                   `xml:"LastModifiedTime" json:"last_modified_time"`
	ResponseHeadersPolicyConfig ResponseHeadersPolicyConfig `xml:"ResponseHeadersPolicyConfig" json:"response_headers_policy_config"`
	Version                     int                         `xml:"-" json:"version"`
}

// ResponseHeadersPolicyConfig holds the configuration for a response headers policy.
type ResponseHeadersPolicyConfig struct {
	Name                  string                       `xml:"Name" json:"name"`
	Comment               string                       `xml:"Comment,omitempty" json:"comment,omitempty"`
	CorsConfig            *CorsConfig                  `xml:"CorsConfig,omitempty" json:"cors_config,omitempty"`
	SecurityHeadersConfig *SecurityHeadersConfig       `xml:"SecurityHeadersConfig,omitempty" json:"security_headers_config,omitempty"`
	CustomHeadersConfig   *ResponseCustomHeadersConfig `xml:"CustomHeadersConfig,omitempty" json:"custom_headers_config,omitempty"`
}

// ResponseHeadersPolicyList is the response envelope for ListResponseHeadersPolicies.
type ResponseHeadersPolicyList struct {
	XMLName    xml.Name                       `xml:"ResponseHeadersPolicyList" json:"-"`
	NextMarker string                         `xml:"NextMarker,omitempty" json:"next_marker,omitempty"`
	MaxItems   int                            `xml:"MaxItems" json:"max_items"`
	Quantity   int                            `xml:"Quantity" json:"quantity"`
	Items      []ResponseHeadersPolicySummary `xml:"Items>ResponseHeadersPolicySummary,omitempty" json:"items,omitempty"`
}

// ResponseHeadersPolicySummary is the per-item element within a ResponseHeadersPolicyList.
type ResponseHeadersPolicySummary struct {
	Type                  string                `xml:"Type" json:"type"`
	ResponseHeadersPolicy ResponseHeadersPolicy `xml:"ResponseHeadersPolicy" json:"response_headers_policy"`
}

// CorsConfig holds CORS override settings for responses.
type CorsConfig struct {
	AccessControlAllowCredentials bool        `xml:"AccessControlAllowCredentials" json:"access_control_allow_credentials"`
	AccessControlAllowHeaders     StringList  `xml:"AccessControlAllowHeaders" json:"access_control_allow_headers"`
	AccessControlAllowMethods     StringList  `xml:"AccessControlAllowMethods" json:"access_control_allow_methods"`
	AccessControlAllowOrigins     StringList  `xml:"AccessControlAllowOrigins" json:"access_control_allow_origins"`
	AccessControlExposeHeaders    *StringList `xml:"AccessControlExposeHeaders,omitempty" json:"access_control_expose_headers,omitempty"`
	AccessControlMaxAgeSec        *int        `xml:"AccessControlMaxAgeSec,omitempty" json:"access_control_max_age_sec,omitempty"`
	OriginOverride                bool        `xml:"OriginOverride" json:"origin_override"`
}

// SecurityHeadersConfig holds security header overrides.
type SecurityHeadersConfig struct {
	ContentSecurityPolicy   *ContentSecurityPolicy   `xml:"ContentSecurityPolicy,omitempty" json:"content_security_policy,omitempty"`
	ContentTypeOptions      *ContentTypeOptions      `xml:"ContentTypeOptions,omitempty" json:"content_type_options,omitempty"`
	FrameOptions            *FrameOptions            `xml:"FrameOptions,omitempty" json:"frame_options,omitempty"`
	ReferrerPolicy          *ReferrerPolicy          `xml:"ReferrerPolicy,omitempty" json:"referrer_policy,omitempty"`
	StrictTransportSecurity *StrictTransportSecurity `xml:"StrictTransportSecurity,omitempty" json:"strict_transport_security,omitempty"`
	XSSProtection           *XSSProtection           `xml:"XSSProtection,omitempty" json:"xss_protection,omitempty"`
}

// ContentSecurityPolicy configures the Content-Security-Policy header.
type ContentSecurityPolicy struct {
	ContentSecurityPolicy string `xml:"ContentSecurityPolicy" json:"content_security_policy"`
	Override              bool   `xml:"Override" json:"override"`
}

// ContentTypeOptions configures the X-Content-Type-Options header.
type ContentTypeOptions struct {
	Override bool `xml:"Override" json:"override"`
}

// FrameOptions configures the X-Frame-Options header.
type FrameOptions struct {
	FrameOption string `xml:"FrameOption" json:"frame_option"` // DENY | SAMEORIGIN
	Override    bool   `xml:"Override" json:"override"`
}

// ReferrerPolicy configures the Referrer-Policy header.
type ReferrerPolicy struct {
	ReferrerPolicy string `xml:"ReferrerPolicy" json:"referrer_policy"`
	Override       bool   `xml:"Override" json:"override"`
}

// StrictTransportSecurity configures the Strict-Transport-Security header.
type StrictTransportSecurity struct {
	AccessControlMaxAgeSec int  `xml:"AccessControlMaxAgeSec" json:"access_control_max_age_sec"`
	IncludeSubdomains      bool `xml:"IncludeSubdomains" json:"include_subdomains"`
	Preload                bool `xml:"Preload" json:"preload"`
	Override               bool `xml:"Override" json:"override"`
}

// XSSProtection configures the X-XSS-Protection header.
type XSSProtection struct {
	ModeBlock  bool   `xml:"ModeBlock" json:"mode_block"`
	Protection bool   `xml:"Protection" json:"protection"`
	ReportUri  string `xml:"ReportUri,omitempty" json:"report_uri,omitempty"`
	Override   bool   `xml:"Override" json:"override"`
}

// ResponseCustomHeadersConfig holds custom headers injected into responses.
type ResponseCustomHeadersConfig struct {
	Quantity int                    `xml:"Quantity" json:"quantity"`
	Items    []ResponseCustomHeader `xml:"Items>ResponseCustomHeader,omitempty" json:"items,omitempty"`
}

// ResponseCustomHeader is a single custom response header.
type ResponseCustomHeader struct {
	Header   string `xml:"Header" json:"header"`
	Value    string `xml:"Value" json:"value"`
	Override bool   `xml:"Override" json:"override"`
}

// ─── CloudFront Origin Access Identity (legacy OAI) ─────────────────────────

// CloudFrontOriginAccessIdentity represents a legacy OAI.
//
//revive:disable-next-line:exported
type CloudFrontOriginAccessIdentity struct {
	XMLName                              xml.Name                             `xml:"CloudFrontOriginAccessIdentity" json:"-"`
	ID                                   string                               `xml:"Id" json:"id"`
	S3CanonicalUserId                    string                               `xml:"S3CanonicalUserId" json:"s3_canonical_user_id"`
	CloudFrontOriginAccessIdentityConfig CloudFrontOriginAccessIdentityConfig `xml:"CloudFrontOriginAccessIdentityConfig" json:"config"`
	Version                              int                                  `xml:"-" json:"version"`
}

// CloudFrontOriginAccessIdentityConfig holds the OAI config.
//
//revive:disable-next-line:exported
type CloudFrontOriginAccessIdentityConfig struct {
	CallerReference string `xml:"CallerReference" json:"caller_reference"`
	Comment         string `xml:"Comment" json:"comment"`
}

// CloudFrontOriginAccessIdentityList is the response envelope for ListCloudFrontOriginAccessIdentities.
//
//revive:disable-next-line:exported
type CloudFrontOriginAccessIdentityList struct {
	XMLName     xml.Name                                `xml:"CloudFrontOriginAccessIdentityList" json:"-"`
	Marker      string                                  `xml:"Marker" json:"marker"`
	NextMarker  string                                  `xml:"NextMarker,omitempty" json:"next_marker,omitempty"`
	MaxItems    int                                     `xml:"MaxItems" json:"max_items"`
	IsTruncated bool                                    `xml:"IsTruncated" json:"is_truncated"`
	Quantity    int                                     `xml:"Quantity" json:"quantity"`
	Items       []CloudFrontOriginAccessIdentitySummary `xml:"Items>CloudFrontOriginAccessIdentitySummary,omitempty" json:"items,omitempty"`
}

// CloudFrontOriginAccessIdentitySummary is the per-item element within a CloudFrontOriginAccessIdentityList.
//
//revive:disable-next-line:exported
type CloudFrontOriginAccessIdentitySummary struct {
	ID                string `xml:"Id" json:"id"`
	S3CanonicalUserId string `xml:"S3CanonicalUserId" json:"s3_canonical_user_id"`
	Comment           string `xml:"Comment" json:"comment"`
}

// ─── CloudFront Functions ───────────────────────────────────────────────────

// ─── Key Groups ─────────────────────────────────────────────────────────────

// KeyGroup represents a CloudFront key group resource.
type KeyGroup struct {
	ID               string         `json:"id"`
	LastModifiedTime time.Time      `json:"last_modified_time"`
	KeyGroupConfig   KeyGroupConfig `json:"key_group_config"`
	Version          int            `json:"version"`
}

// KeyGroupConfig holds the configuration for a key group.
type KeyGroupConfig struct {
	Name    string   `xml:"Name" json:"name"`
	Comment string   `xml:"Comment,omitempty" json:"comment,omitempty"`
	Items   []string `xml:"Items>PublicKey" json:"items"`
}

// keyGroupConfigWrapper is the XML envelope for KeyGroupConfig.
type keyGroupConfigWrapper struct {
	XMLName xml.Name `xml:"KeyGroupConfig"`
	Name    string   `xml:"Name"`
	Comment string   `xml:"Comment,omitempty"`
	Items   []string `xml:"Items>PublicKey,omitempty"`
}

// keyGroupXML is the XML response for a single key group.
type keyGroupXML struct {
	XMLName          xml.Name       `xml:"KeyGroup"`
	ID               string         `xml:"Id"`
	LastModifiedTime string         `xml:"LastModifiedTime"`
	KeyGroupConfig   KeyGroupConfig `xml:"KeyGroupConfig"`
}

// keyGroupListXML is the XML response for ListKeyGroups.
type keyGroupListXML struct {
	XMLName  xml.Name          `xml:"KeyGroupList"`
	MaxItems int               `xml:"MaxItems"`
	Quantity int               `xml:"Quantity"`
	Items    []keyGroupSummary `xml:"Items>KeyGroupSummary,omitempty"`
}

// keyGroupSummary is one entry in the key group list.
type keyGroupSummary struct {
	KeyGroup keyGroupXML `xml:"KeyGroup"`
}

// ─── Public Keys ────────────────────────────────────────────────────────────

// PublicKey represents a CloudFront public key resource.
type PublicKey struct {
	ID              string          `json:"id"`
	CreatedTime     time.Time       `json:"created_time"`
	PublicKeyConfig PublicKeyConfig `json:"public_key_config"`
	Version         int             `json:"version"`
}

// PublicKeyConfig holds the configuration for a public key.
type PublicKeyConfig struct {
	CallerReference string `xml:"CallerReference" json:"caller_reference"`
	Name            string `xml:"Name" json:"name"`
	Comment         string `xml:"Comment,omitempty" json:"comment,omitempty"`
	EncodedKey      string `xml:"EncodedKey" json:"encoded_key"`
}

// publicKeyConfigWrapper is the XML envelope for PublicKeyConfig.
type publicKeyConfigWrapper struct {
	XMLName         xml.Name `xml:"PublicKeyConfig"`
	CallerReference string   `xml:"CallerReference"`
	Name            string   `xml:"Name"`
	Comment         string   `xml:"Comment,omitempty"`
	EncodedKey      string   `xml:"EncodedKey"`
}

// publicKeyXML is the XML response for a single public key.
type publicKeyXML struct {
	XMLName         xml.Name        `xml:"PublicKey"`
	ID              string          `xml:"Id"`
	CreatedTime     string          `xml:"CreatedTime"`
	PublicKeyConfig PublicKeyConfig `xml:"PublicKeyConfig"`
}

// publicKeyListXML is the XML response for ListPublicKeys.
type publicKeyListXML struct {
	XMLName  xml.Name           `xml:"PublicKeyList"`
	MaxItems int                `xml:"MaxItems"`
	Quantity int                `xml:"Quantity"`
	Items    []publicKeySummary `xml:"Items>PublicKeySummary,omitempty"`
}

// publicKeySummary is one entry in the public key list.
type publicKeySummary struct {
	ID          string `xml:"Id"`
	Name        string `xml:"Name"`
	CreatedTime string `xml:"CreatedTime"`
	Comment     string `xml:"Comment,omitempty"`
	EncodedKey  string `xml:"EncodedKey"`
}

// ─── CloudFront Functions ───────────────────────────────────────────────────

// CloudFrontFunction represents a CF Functions resource.
//
//revive:disable-next-line:exported
type CloudFrontFunction struct {
	Name             string           `json:"name"`
	Status           string           `json:"status"` // UNPUBLISHED | UNASSOCIATED | DEPLOYED
	Stage            string           `json:"stage"`  // DEVELOPMENT | LIVE
	FunctionConfig   FunctionConfig   `json:"function_config"`
	FunctionMetadata FunctionMetadata `json:"function_metadata"`
	FunctionCode     string           `json:"function_code"` // base64-encoded source
	Version          int              `json:"version"`
}

// FunctionConfig holds the configuration for a CloudFront Function.
type FunctionConfig struct {
	Comment string `xml:"Comment,omitempty" json:"comment,omitempty"`
	Runtime string `xml:"Runtime" json:"runtime"` // cloudfront-js-1.0 | cloudfront-js-2.0
}

// FunctionMetadata holds metadata about a CloudFront Function.
type FunctionMetadata struct {
	FunctionARN      string    `xml:"FunctionARN" json:"function_arn"`
	Stage            string    `xml:"Stage" json:"stage"`
	CreatedTime      time.Time `xml:"CreatedTime" json:"created_time"`
	LastModifiedTime time.Time `xml:"LastModifiedTime" json:"last_modified_time"`
}

// functionConfigWrapper is the XML envelope for creating/updating a function.
type functionConfigWrapper struct {
	XMLName        xml.Name       `xml:"CreateFunctionRequest"`
	Name           string         `xml:"Name"`
	FunctionConfig FunctionConfig `xml:"FunctionConfig"`
	FunctionCode   string         `xml:"FunctionCode"` // base64-encoded
}

// functionUpdateWrapper is the XML envelope for UpdateFunction.
type functionUpdateWrapper struct {
	XMLName        xml.Name       `xml:"UpdateFunctionRequest"`
	FunctionConfig FunctionConfig `xml:"FunctionConfig"`
	FunctionCode   string         `xml:"FunctionCode"` // base64-encoded
}

// functionSummaryXML is the XML shape of a function summary.
type functionSummaryXML struct {
	XMLName          xml.Name         `xml:"FunctionSummary"`
	Name             string           `xml:"Name"`
	Status           string           `xml:"Status"`
	FunctionConfig   FunctionConfig   `xml:"FunctionConfig"`
	FunctionMetadata FunctionMetadata `xml:"FunctionMetadata"`
}

// functionListXML is the XML response for ListFunctions.
type functionListXML struct {
	XMLName  xml.Name             `xml:"FunctionList"`
	MaxItems int                  `xml:"MaxItems"`
	Quantity int                  `xml:"Quantity"`
	Items    []functionSummaryXML `xml:"Items>FunctionSummary,omitempty"`
}

// functionDescribeXML wraps the DescribeFunction response.
type functionDescribeXML struct {
	XMLName          xml.Name         `xml:"FunctionSummary"`
	Name             string           `xml:"Name"`
	Status           string           `xml:"Status"`
	FunctionConfig   FunctionConfig   `xml:"FunctionConfig"`
	FunctionMetadata FunctionMetadata `xml:"FunctionMetadata"`
}

// testResultXML wraps the TestFunction response.
type testResultXML struct {
	XMLName               xml.Name           `xml:"TestResult"`
	FunctionSummary       functionSummaryXML `xml:"FunctionSummary"`
	ComputeUtilization    string             `xml:"ComputeUtilization"`
	FunctionExecutionLogs []string           `xml:"FunctionExecutionLogs>member,omitempty"`
	FunctionErrorMessage  string             `xml:"FunctionErrorMessage,omitempty"`
	FunctionOutput        string             `xml:"FunctionOutput,omitempty"`
}

// ─── Monitoring Subscriptions ───────────────────────────────────────────────

// MonitoringSubscription represents a CloudFront monitoring subscription.
type MonitoringSubscription struct {
	RealtimeMetricsSubscriptionConfig RealtimeMetricsConfig `json:"realtime_metrics_config"`
}

// RealtimeMetricsConfig holds the realtime metrics subscription configuration.
type RealtimeMetricsConfig struct {
	RealtimeMetricsSubscriptionStatus string `xml:"RealtimeMetricsSubscriptionStatus" json:"status"` // Enabled | Disabled
}

// monitoringSubscriptionXML is the XML envelope for monitoring subscriptions.
type monitoringSubscriptionXML struct {
	XMLName                           xml.Name              `xml:"MonitoringSubscription"`
	RealtimeMetricsSubscriptionConfig RealtimeMetricsConfig `xml:"RealtimeMetricsSubscriptionConfig"`
}

// ─── Realtime Log Configs ───────────────────────────────────────────────────

// RealtimeLogConfig represents a CloudFront realtime log configuration.
type RealtimeLogConfig struct {
	ARN          string     `json:"arn"`
	Name         string     `json:"name"`
	SamplingRate int64      `json:"sampling_rate"`
	Fields       []string   `json:"fields"`
	EndPoints    []EndPoint `json:"end_points"`
	Version      int        `json:"version"`
}

// EndPoint represents a realtime log config endpoint.
type EndPoint struct {
	StreamType          string               `xml:"StreamType" json:"stream_type"`
	KinesisStreamConfig *KinesisStreamConfig `xml:"KinesisStreamConfig,omitempty" json:"kinesis_stream_config,omitempty"`
}

// KinesisStreamConfig holds the Kinesis stream destination config.
type KinesisStreamConfig struct {
	RoleARN   string `xml:"RoleARN" json:"role_arn"`
	StreamARN string `xml:"StreamARN" json:"stream_arn"`
}

// realtimeLogConfigXML is the XML response for a single realtime log config.
type realtimeLogConfigXML struct {
	XMLName      xml.Name     `xml:"RealtimeLogConfig"`
	ARN          string       `xml:"ARN"`
	Name         string       `xml:"Name"`
	SamplingRate int64        `xml:"SamplingRate"`
	Fields       fieldList    `xml:"Fields"`
	EndPoints    endPointList `xml:"EndPoints"`
}

type fieldList struct {
	Items []string `xml:"Field"`
}

type endPointList struct {
	Items []EndPoint `xml:"member"`
}

// realtimeLogConfigListXML is the XML response for ListRealtimeLogConfigs.
type realtimeLogConfigListXML struct {
	XMLName  xml.Name               `xml:"RealtimeLogConfigs"`
	MaxItems int                    `xml:"MaxItems"`
	Items    []realtimeLogConfigXML `xml:"Items>RealtimeLogConfig,omitempty"`
}

// ─── Field-Level Encryption ─────────────────────────────────────────────────

// FLEConfig represents a field-level encryption configuration.
type FLEConfig struct {
	ID               string        `json:"id"`
	LastModifiedTime time.Time     `json:"last_modified_time"`
	FLEConfigData    FLEConfigData `json:"fle_config_data"`
	Version          int           `json:"version"`
}

// FLEConfigData holds the configuration data for field-level encryption.
type FLEConfigData struct {
	CallerReference          string `xml:"CallerReference" json:"caller_reference"`
	Comment                  string `xml:"Comment,omitempty" json:"comment,omitempty"`
	ContentTypeProfileConfig string `xml:"ContentTypeProfileConfig,omitempty" json:"content_type_profile_config,omitempty"`
	QueryArgProfileConfig    string `xml:"QueryArgProfileConfig,omitempty" json:"query_arg_profile_config,omitempty"`
}

// fleConfigXML is the XML response for a single field-level encryption config.
type fleConfigXML struct {
	XMLName                    xml.Name      `xml:"FieldLevelEncryption"`
	ID                         string        `xml:"Id"`
	LastModifiedTime           string        `xml:"LastModifiedTime"`
	FieldLevelEncryptionConfig FLEConfigData `xml:"FieldLevelEncryptionConfig"`
}

// fleConfigDataXML is the XML response for GetFieldLevelEncryptionConfig.
type fleConfigDataXML struct {
	XMLName         xml.Name `xml:"FieldLevelEncryptionConfig"`
	CallerReference string   `xml:"CallerReference"`
	Comment         string   `xml:"Comment,omitempty"`
}

// fleConfigListXML is the XML response for ListFieldLevelEncryptionConfigs.
type fleConfigListXML struct {
	XMLName  xml.Name           `xml:"FieldLevelEncryptionList"`
	MaxItems int                `xml:"MaxItems"`
	Quantity int                `xml:"Quantity"`
	Items    []fleConfigSummary `xml:"Items>FieldLevelEncryptionSummary,omitempty"`
}

type fleConfigSummary struct {
	ID               string `xml:"Id"`
	LastModifiedTime string `xml:"LastModifiedTime"`
	Comment          string `xml:"Comment,omitempty"`
}

// ─── Field-Level Encryption Profiles ────────────────────────────────────────

// FLEProfile represents a field-level encryption profile.
type FLEProfile struct {
	ID               string         `json:"id"`
	LastModifiedTime time.Time      `json:"last_modified_time"`
	FLEProfileData   FLEProfileData `json:"fle_profile_data"`
	Version          int            `json:"version"`
}

// FLEProfileData holds the configuration data for a field-level encryption profile.
type FLEProfileData struct {
	CallerReference    string `xml:"CallerReference" json:"caller_reference"`
	Name               string `xml:"Name" json:"name"`
	Comment            string `xml:"Comment,omitempty" json:"comment,omitempty"`
	EncryptionEntities string `xml:"EncryptionEntities,omitempty" json:"encryption_entities,omitempty"`
}

// fleProfileXML is the XML response for a single FLE profile.
type fleProfileXML struct {
	XMLName                           xml.Name       `xml:"FieldLevelEncryptionProfile"`
	ID                                string         `xml:"Id"`
	LastModifiedTime                  string         `xml:"LastModifiedTime"`
	FieldLevelEncryptionProfileConfig FLEProfileData `xml:"FieldLevelEncryptionProfileConfig"`
}

// fleProfileDataXML is the XML response for GetFieldLevelEncryptionProfileConfig.
type fleProfileDataXML struct {
	XMLName         xml.Name `xml:"FieldLevelEncryptionProfileConfig"`
	CallerReference string   `xml:"CallerReference"`
	Name            string   `xml:"Name"`
	Comment         string   `xml:"Comment,omitempty"`
}

// fleProfileListXML is the XML response for ListFieldLevelEncryptionProfiles.
type fleProfileListXML struct {
	XMLName  xml.Name            `xml:"FieldLevelEncryptionProfileList"`
	MaxItems int                 `xml:"MaxItems"`
	Quantity int                 `xml:"Quantity"`
	Items    []fleProfileSummary `xml:"Items>FieldLevelEncryptionProfileSummary,omitempty"`
}

type fleProfileSummary struct {
	ID               string `xml:"Id"`
	LastModifiedTime string `xml:"LastModifiedTime"`
	Name             string `xml:"Name"`
	Comment          string `xml:"Comment,omitempty"`
}

// ─── Continuous Deployment Policies ─────────────────────────────────────────

// ContinuousDeploymentPolicy represents a continuous deployment policy.
type ContinuousDeploymentPolicy struct {
	ID                               string    `json:"id"`
	LastModifiedTime                 time.Time `json:"last_modified_time"`
	ContinuousDeploymentPolicyConfig CDPConfig `json:"cdp_config"`
	Version                          int       `json:"version"`
}

// CDPConfig holds the configuration for a continuous deployment policy.
type CDPConfig struct {
	StagingDistributionDnsNames StagingDNSNames   `xml:"StagingDistributionDnsNames" json:"staging_dns_names"`
	Enabled                     bool              `xml:"Enabled" json:"enabled"`
	TrafficConfig               *CDPTrafficConfig `xml:"TrafficConfig,omitempty" json:"traffic_config,omitempty"`
}

// CDPTrafficConfig defines how traffic is split between primary and staging.
type CDPTrafficConfig struct {
	Type               string                 `xml:"Type" json:"type"`
	SingleWeightConfig *CDPSingleWeightConfig `xml:"SingleWeightConfig,omitempty" json:"single_weight_config,omitempty"`
	SingleHeaderConfig *CDPSingleHeaderConfig `xml:"SingleHeaderConfig,omitempty" json:"single_header_config,omitempty"`
}

// CDPSingleWeightConfig routes a fraction of traffic by random sampling.
type CDPSingleWeightConfig struct {
	Weight float64 `xml:"Weight" json:"weight"`
}

// CDPSingleHeaderConfig routes traffic based on the presence of a specific header value.
type CDPSingleHeaderConfig struct {
	Header string `xml:"Header" json:"header"`
	Value  string `xml:"Value" json:"value"`
}

// StagingDNSNames holds the staging distribution DNS names.
type StagingDNSNames struct {
	Quantity int      `xml:"Quantity" json:"quantity"`
	Items    []string `xml:"Items>DnsName,omitempty" json:"items,omitempty"`
}

// cdpXML is the XML response for a single continuous deployment policy.
type cdpXML struct {
	XMLName                          xml.Name  `xml:"ContinuousDeploymentPolicy"`
	ID                               string    `xml:"Id"`
	LastModifiedTime                 string    `xml:"LastModifiedTime"`
	ContinuousDeploymentPolicyConfig CDPConfig `xml:"ContinuousDeploymentPolicyConfig"`
}

// cdpConfigXML is the XML response for GetContinuousDeploymentPolicyConfig.
type cdpConfigXML struct {
	XMLName                     xml.Name        `xml:"ContinuousDeploymentPolicyConfig"`
	StagingDistributionDnsNames StagingDNSNames `xml:"StagingDistributionDnsNames"`
	Enabled                     bool            `xml:"Enabled"`
}

// cdpListXML is the XML response for ListContinuousDeploymentPolicies.
type cdpListXML struct {
	XMLName  xml.Name     `xml:"ContinuousDeploymentPolicyList"`
	MaxItems int          `xml:"MaxItems"`
	Quantity int          `xml:"Quantity"`
	Items    []cdpSummary `xml:"Items>ContinuousDeploymentPolicySummary,omitempty"`
}

type cdpSummary struct {
	ContinuousDeploymentPolicy cdpXML `xml:"ContinuousDeploymentPolicy"`
}

// ─── Error constructors ─────────────────────────────────────────────────────

func errDistributionNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchDistribution",
		Message:    fmt.Sprintf("The specified distribution does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errDistributionNotDisabled(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DistributionNotDisabled",
		Message:    fmt.Sprintf("The distribution you are trying to delete has not been disabled: %s", id),
		HTTPStatus: 409,
	}
}

func errPreconditionFailed() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "PreconditionFailed",
		Message:    "The precondition given in one or more of the request-header fields evaluated to false.",
		HTTPStatus: 412,
	}
}

func errInvalidIfMatch() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidIfMatchVersion",
		Message:    "The If-Match version is missing or not valid for the resource.",
		HTTPStatus: 400,
	}
}

func errDistributionAlreadyExists(callerRef string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DistributionAlreadyExists",
		Message:    fmt.Sprintf("The caller reference you are using to create a distribution is associated with another distribution: %s", callerRef),
		HTTPStatus: 409,
	}
}

func errNoSuchInvalidation(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchInvalidation",
		Message:    fmt.Sprintf("The specified invalidation does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchOriginAccessControl(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchOriginAccessControl",
		Message:    fmt.Sprintf("The specified origin access control does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchCachePolicy(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchCachePolicy",
		Message:    fmt.Sprintf("The cache policy does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchOriginRequestPolicy(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchOriginRequestPolicy",
		Message:    fmt.Sprintf("The origin request policy does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchResponseHeadersPolicy(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchResponseHeadersPolicy",
		Message:    fmt.Sprintf("The response headers policy does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchCloudFrontOAI(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchCloudFrontOriginAccessIdentity",
		Message:    fmt.Sprintf("The specified origin access identity does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchFunction(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchFunctionExists",
		Message:    fmt.Sprintf("The specified function does not exist: %s", name),
		HTTPStatus: 404,
	}
}

func errMissingCallerReference() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArgument",
		Message:    "CallerReference is required.",
		HTTPStatus: 400,
	}
}

// errInvalidMarker maps a garbled/out-of-range pagination Marker to
// CloudFront's documented error. A silent restart from page 1 (this
// codebase's most common pagination divergence, see
// docs/plans/pagination-plan.md G3) causes duplicate delivery to any client
// polling with a stale token. Verified against every List op that uses
// serviceutil.Paginate in this package:
//   - https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListDistributions.html#API_ListDistributions_Errors
//   - https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListInvalidations.html#API_ListInvalidations_Errors
//   - https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListOriginAccessControls.html#API_ListOriginAccessControls_Errors
//   - https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListCloudFrontOriginAccessIdentities.html#API_ListCloudFrontOriginAccessIdentities_Errors
//
// All four document InvalidArgument, HTTP 400.
func errInvalidMarker() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArgument",
		Message:    "The Marker parameter is not valid.",
		HTTPStatus: 400,
	}
}

func errNoSuchKeyGroup(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchResource",
		Message:    fmt.Sprintf("The specified key group does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchPublicKey(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchPublicKey",
		Message:    fmt.Sprintf("The specified public key does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errFunctionAlreadyExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "FunctionAlreadyExists",
		Message:    fmt.Sprintf("A function with the same name already exists: %s", name),
		HTTPStatus: 409,
	}
}

func errPublicKeyAlreadyExists(callerRef string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "PublicKeyAlreadyUploaded",
		Message:    fmt.Sprintf("The specified public key already exists: %s", callerRef),
		HTTPStatus: 409,
	}
}

func errNoSuchMonitoringSubscription(distID string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchMonitoringSubscription",
		Message:    fmt.Sprintf("The specified monitoring subscription does not exist: %s", distID),
		HTTPStatus: 404,
	}
}

func errNoSuchRealtimeLogConfig(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchRealtimeLogConfig",
		Message:    fmt.Sprintf("The specified realtime log config does not exist: %s", name),
		HTTPStatus: 404,
	}
}

func errNoSuchFieldLevelEncryptionConfig(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchFieldLevelEncryptionConfig",
		Message:    fmt.Sprintf("The specified field-level encryption config does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchFieldLevelEncryptionProfile(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchFieldLevelEncryptionProfile",
		Message:    fmt.Sprintf("The specified field-level encryption profile does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errNoSuchContinuousDeploymentPolicy(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchContinuousDeploymentPolicy",
		Message:    fmt.Sprintf("The specified continuous deployment policy does not exist: %s", id),
		HTTPStatus: 404,
	}
}

func errRealtimeLogConfigAlreadyExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "RealtimeLogConfigAlreadyExists",
		Message:    fmt.Sprintf("A realtime log config with the same name already exists: %s", name),
		HTTPStatus: 409,
	}
}
