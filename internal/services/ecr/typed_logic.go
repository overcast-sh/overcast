package ecr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ecrTagCfg tunes the shared tag validator to ECR's error shape (#1052).
// ECR reports every other rejected-input case this package models (see
// service.go's InvalidParameterException usages) the same way, and declares
// no dedicated tag-count exception, so the 50-tag limit is reported the same
// way an invalid key or value is.
var ecrTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidParameterException",
	InvalidCode:     "InvalidParameterException",
	ExceededMessage: "The parameter tags must be no more than 50 tags.",
}

func ecrTagsToMap(tags []Tag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

// ---- Request types ----

type createRepositoryRequest struct {
	RepositoryName             string                     `json:"repositoryName" cbor:"repositoryName"`
	ImageTagMutability         string                     `json:"imageTagMutability" cbor:"imageTagMutability"`
	ImageScanningConfiguration ImageScanningConfiguration `json:"imageScanningConfiguration" cbor:"imageScanningConfiguration"`
	EncryptionConfiguration    EncryptionConfiguration    `json:"encryptionConfiguration" cbor:"encryptionConfiguration"`
	Tags                       []Tag                      `json:"tags" cbor:"tags"`
}

type putImageTagMutabilityRequest struct {
	RepositoryName     string `json:"repositoryName" cbor:"repositoryName"`
	RegistryId         string `json:"registryId" cbor:"registryId"`
	ImageTagMutability string `json:"imageTagMutability" cbor:"imageTagMutability"`
}

type putImageScanningConfigurationRequest struct {
	RepositoryName             string                     `json:"repositoryName" cbor:"repositoryName"`
	RegistryId                 string                     `json:"registryId" cbor:"registryId"`
	ImageScanningConfiguration ImageScanningConfiguration `json:"imageScanningConfiguration" cbor:"imageScanningConfiguration"`
}

type describeRepositoriesRequest struct {
	RepositoryNames []string `json:"repositoryNames" cbor:"repositoryNames"`
	MaxResults      *int     `json:"maxResults" cbor:"maxResults"`
	NextToken       string   `json:"nextToken" cbor:"nextToken"`
}

// listImagesRequest is ListImages' input: a repository reference plus paging.
type listImagesRequest struct {
	RepositoryName string `json:"repositoryName" cbor:"repositoryName"`
	RegistryId     string `json:"registryId" cbor:"registryId"`
	MaxResults     *int   `json:"maxResults" cbor:"maxResults"`
	NextToken      string `json:"nextToken" cbor:"nextToken"`
}

// describeImagesRequest is DescribeImages' input: image selectors plus paging.
type describeImagesRequest struct {
	RepositoryName string            `json:"repositoryName" cbor:"repositoryName"`
	RegistryId     string            `json:"registryId" cbor:"registryId"`
	ImageIds       []ImageIdentifier `json:"imageIds" cbor:"imageIds"`
	MaxResults     *int              `json:"maxResults" cbor:"maxResults"`
	NextToken      string            `json:"nextToken" cbor:"nextToken"`
}

type deleteRepositoryRequest struct {
	RepositoryName string `json:"repositoryName" cbor:"repositoryName"`
	RegistryId     string `json:"registryId" cbor:"registryId"`
	Force          bool   `json:"force" cbor:"force"`
}

type repoRefRequest struct {
	RepositoryName string `json:"repositoryName" cbor:"repositoryName"`
	RegistryId     string `json:"registryId" cbor:"registryId"`
}

type putImageRequest struct {
	RepositoryName         string `json:"repositoryName" cbor:"repositoryName"`
	RegistryId             string `json:"registryId" cbor:"registryId"`
	ImageManifest          string `json:"imageManifest" cbor:"imageManifest"`
	ImageManifestMediaType string `json:"imageManifestMediaType" cbor:"imageManifestMediaType"`
	ImageTag               string `json:"imageTag" cbor:"imageTag"`
	ImageDigest            string `json:"imageDigest" cbor:"imageDigest"`
}

type imageIDSetRequest struct {
	RepositoryName string            `json:"repositoryName" cbor:"repositoryName"`
	RegistryId     string            `json:"registryId" cbor:"registryId"`
	ImageIds       []ImageIdentifier `json:"imageIds" cbor:"imageIds"`
}

type describeImageScanFindingsRequest struct {
	RepositoryName string          `json:"repositoryName" cbor:"repositoryName"`
	RegistryId     string          `json:"registryId" cbor:"registryId"`
	ImageID        ImageIdentifier `json:"imageId" cbor:"imageId"`
}

type setRepositoryPolicyRequest struct {
	RepositoryName string `json:"repositoryName" cbor:"repositoryName"`
	RegistryId     string `json:"registryId" cbor:"registryId"`
	PolicyText     string `json:"policyText" cbor:"policyText"`
	Force          bool   `json:"force" cbor:"force"`
}

type putLifecyclePolicyRequest struct {
	RepositoryName      string `json:"repositoryName" cbor:"repositoryName"`
	RegistryId          string `json:"registryId" cbor:"registryId"`
	LifecyclePolicyText string `json:"lifecyclePolicyText" cbor:"lifecyclePolicyText"`
}

type tagResourceRequest struct {
	ResourceArn string `json:"resourceArn" cbor:"resourceArn"`
	Tags        []Tag  `json:"tags" cbor:"tags"`
}

type untagResourceRequest struct {
	ResourceArn string   `json:"resourceArn" cbor:"resourceArn"`
	TagKeys     []string `json:"tagKeys" cbor:"tagKeys"`
}

type listTagsForResourceRequest struct {
	ResourceArn string `json:"resourceArn" cbor:"resourceArn"`
}

// ---- Response types ----

type createRepositoryResponse struct {
	Repository Repository `json:"repository" cbor:"repository"`
	// limitations is unexported (and untagged, so no codec writes it) — it is
	// read back by op.Limitationer, not the wire. See registryLimitation.
	limitations []string
}

// EmulationLimitations implements op.Limitationer.
func (r *createRepositoryResponse) EmulationLimitations() []string {
	if len(r.limitations) == 0 {
		return nil
	}
	return r.limitations
}

type putImageTagMutabilityResponse struct {
	RegistryId         string `json:"registryId" cbor:"registryId"`
	RepositoryName     string `json:"repositoryName" cbor:"repositoryName"`
	ImageTagMutability string `json:"imageTagMutability" cbor:"imageTagMutability"`
}

type putImageScanningConfigurationResponse struct {
	RegistryId                 string                     `json:"registryId" cbor:"registryId"`
	RepositoryName             string                     `json:"repositoryName" cbor:"repositoryName"`
	ImageScanningConfiguration ImageScanningConfiguration `json:"imageScanningConfiguration" cbor:"imageScanningConfiguration"`
}

type describeRepositoriesResponse struct {
	Repositories []*Repository `json:"repositories" cbor:"repositories"`
	NextToken    string        `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

type deleteRepositoryResponse struct {
	Repository Repository `json:"repository" cbor:"repository"`
}

type getAuthorizationTokenResponse struct {
	AuthorizationData []authDataItem `json:"authorizationData" cbor:"authorizationData"`
}

type authDataItem struct {
	AuthorizationToken string  `json:"authorizationToken" cbor:"authorizationToken"`
	ProxyEndpoint      string  `json:"proxyEndpoint" cbor:"proxyEndpoint"`
	ExpiresAt          float64 `json:"expiresAt" cbor:"expiresAt"`
}

type describeRegistryResponse struct {
	RegistryId               string            `json:"registryId" cbor:"registryId"`
	ReplicationConfiguration replicationConfig `json:"replicationConfiguration" cbor:"replicationConfiguration"`
}

type replicationConfig struct {
	Rules []any `json:"rules" cbor:"rules"`
}

type listImagesResponse struct {
	ImageIds  []ImageIdentifier `json:"imageIds" cbor:"imageIds"`
	NextToken string            `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

type describeImagesResponse struct {
	ImageDetails []imageDetailWire `json:"imageDetails" cbor:"imageDetails"`
	NextToken    string            `json:"nextToken,omitempty" cbor:"nextToken,omitempty"`
}

type imageDetailWire struct {
	RegistryId             string   `json:"registryId" cbor:"registryId"`
	RepositoryName         string   `json:"repositoryName" cbor:"repositoryName"`
	ImageDigest            string   `json:"imageDigest" cbor:"imageDigest"`
	ImageTags              []string `json:"imageTags,omitempty" cbor:"imageTags,omitempty"`
	ImageManifestMediaType string   `json:"imageManifestMediaType,omitempty" cbor:"imageManifestMediaType,omitempty"`
}

type putImageResponse struct {
	Image Image `json:"image" cbor:"image"`
}

type batchGetImageResponse struct {
	Images   []Image            `json:"images" cbor:"images"`
	Failures []imageFailureWire `json:"failures" cbor:"failures"`
}

type imageFailureWire struct {
	ImageID       ImageIdentifier `json:"imageId" cbor:"imageId"`
	FailureCode   string          `json:"failureCode" cbor:"failureCode"`
	FailureReason string          `json:"failureReason" cbor:"failureReason"`
}

type describeImageScanFindingsResponse struct {
	RegistryId        string           `json:"registryId" cbor:"registryId"`
	RepositoryName    string           `json:"repositoryName" cbor:"repositoryName"`
	ImageID           ImageIdentifier  `json:"imageId" cbor:"imageId"`
	ImageScanStatus   scanStatusWire   `json:"imageScanStatus" cbor:"imageScanStatus"`
	ImageScanFindings scanFindingsWire `json:"imageScanFindings" cbor:"imageScanFindings"`
}

type scanStatusWire struct {
	Status      string `json:"status" cbor:"status"`
	Description string `json:"description" cbor:"description"`
}

type scanFindingsWire struct {
	FindingSeverityCounts map[string]any `json:"findingSeverityCounts" cbor:"findingSeverityCounts"`
	Findings              []any          `json:"findings" cbor:"findings"`
	EnhancedFindings      []any          `json:"enhancedFindings" cbor:"enhancedFindings"`
}

type batchDeleteImageResponse struct {
	ImageIds []ImageIdentifier  `json:"imageIds" cbor:"imageIds"`
	Failures []imageFailureWire `json:"failures" cbor:"failures"`
}

type setRepositoryPolicyResponse struct {
	RegistryId     string `json:"registryId" cbor:"registryId"`
	RepositoryName string `json:"repositoryName" cbor:"repositoryName"`
	PolicyText     string `json:"policyText" cbor:"policyText"`
}

type getRepositoryPolicyResponse struct {
	RegistryId     string `json:"registryId" cbor:"registryId"`
	RepositoryName string `json:"repositoryName" cbor:"repositoryName"`
	PolicyText     string `json:"policyText" cbor:"policyText"`
}

type deleteRepositoryPolicyResponse struct {
	RegistryId     string `json:"registryId" cbor:"registryId"`
	RepositoryName string `json:"repositoryName" cbor:"repositoryName"`
	PolicyText     string `json:"policyText" cbor:"policyText"`
}

type putLifecyclePolicyResponse struct {
	RegistryId          string `json:"registryId" cbor:"registryId"`
	RepositoryName      string `json:"repositoryName" cbor:"repositoryName"`
	LifecyclePolicyText string `json:"lifecyclePolicyText" cbor:"lifecyclePolicyText"`
}

type getLifecyclePolicyResponse struct {
	RegistryId          string `json:"registryId" cbor:"registryId"`
	RepositoryName      string `json:"repositoryName" cbor:"repositoryName"`
	LifecyclePolicyText string `json:"lifecyclePolicyText" cbor:"lifecyclePolicyText"`
}

type deleteLifecyclePolicyResponse struct {
	RegistryId          string `json:"registryId" cbor:"registryId"`
	RepositoryName      string `json:"repositoryName" cbor:"repositoryName"`
	LifecyclePolicyText string `json:"lifecyclePolicyText" cbor:"lifecyclePolicyText"`
}

type tagResourceResponse struct{}

type untagResourceResponse struct{}

type listTagsForResourceResponse struct {
	Tags []Tag `json:"tags" cbor:"tags"`
}

// ---- Typed handler functions ----

// ecrPageOptions is the shared page size for DescribeRepositories, ListImages
// and DescribeImages.
// Per AWS docs (https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_ListImages.html
// and the DescribeRepositories / DescribeImages siblings): maxResults is
// 1..1000 (the pinned model's MaxResults range), and "If this parameter is not
// used, then ... returns up to 100 results and a nextToken value".
var ecrPageOptions = serviceutil.PaginateOptions{DefaultLimit: 100, MaxLimit: 1000}

// ecrPage validates maxResults against the model's 1..1000 range and applies
// maxResults/nextToken to items. Both failures are InvalidParameterException,
// the only validation error the pinned model lists for these operations. The
// token is serviceutil's opaque, deterministic start-offset token, so the same
// request always yields the same token; one the service never issued is
// refused rather than treated as the first page.
func ecrPage[T any](items []T, maxResults *int, nextToken string) ([]T, string, *protocol.AWSError) {
	limit := 0
	if maxResults != nil {
		limit = *maxResults
		if limit < 1 || limit > 1000 {
			return nil, "", ecrInvalidParameter(fmt.Sprintf("Invalid parameter at 'maxResults' failed to satisfy constraint: Member must have value between 1 and 1000, got %d", limit))
		}
	}
	page, err := serviceutil.Paginate(items, limit, nextToken, ecrPageOptions)
	if err != nil {
		return nil, "", ecrInvalidParameter("Invalid parameter at 'nextToken': the token is not valid")
	}
	return page.Items, page.NextToken, nil
}

func ecrInvalidParameter(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "InvalidParameterException", Message: msg, HTTPStatus: http.StatusBadRequest}
}

func (s *Service) regionCtx(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.cfg.Region)
}

func (s *Service) errRepoNotFoundTyped(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "RepositoryNotFoundException",
		Message:    fmt.Sprintf("The repository with name '%s' does not exist in the registry with id '%s'", name, s.accountID()),
		HTTPStatus: http.StatusBadRequest,
	}
}

func (s *Service) createRepositoryTyped(ctx context.Context, req *createRepositoryRequest) (*createRepositoryResponse, *protocol.AWSError) {
	if strings.TrimSpace(req.RepositoryName) == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "repositoryName is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if found {
		return nil, &protocol.AWSError{
			Code: "RepositoryAlreadyExistsException", Message: fmt.Sprintf("The repository with name '%s' already exists in the registry with id '%s'", req.RepositoryName, s.accountID()), HTTPStatus: http.StatusBadRequest,
		}
	}
	// Request-shape validation before the repository is created — the same
	// ordering createLogGroupTyped uses (internal/services/cloudwatch/logs/
	// typed_logic.go) — so a rejected create leaves no repository behind
	// with nothing able to fix its tags (#1052).
	if aerr := serviceutil.ValidateTags(ecrTagCfg, ecrTagsToMap(req.Tags)); aerr != nil {
		return nil, aerr
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
	s.applyCurrentRepoURI(ctx, region, repo)
	if err := s.saveRepo(ctx, region, repo); err != nil {
		return nil, protocol.ErrInternalError
	}
	if len(req.Tags) > 0 {
		_ = s.saveTags(ctx, repo.RepositoryArn, req.Tags)
	}
	s.publish(ctx, events.ECRRepositoryCreated, events.ResourcePayload{Name: req.RepositoryName, ARN: repo.RepositoryArn})
	resp := &createRepositoryResponse{Repository: *repo}
	if reason := s.registryLimitation(); reason != "" {
		resp.limitations = []string{reason}
	}
	return resp, nil
}

// putImageTagMutabilityTyped is the CBOR counterpart of putImageTagMutability;
// see that handler for why IMMUTABLE is stored but not enforced on push.
func (s *Service) putImageTagMutabilityTyped(ctx context.Context, req *putImageTagMutabilityRequest) (*putImageTagMutabilityResponse, *protocol.AWSError) {
	if req.ImageTagMutability != "MUTABLE" && req.ImageTagMutability != "IMMUTABLE" {
		return nil, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "imageTagMutability must be MUTABLE or IMMUTABLE", HTTPStatus: http.StatusBadRequest,
		}
	}
	region := s.regionCtx(ctx)
	repo, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	repo.ImageTagMutability = req.ImageTagMutability
	if err := s.saveRepo(ctx, region, repo); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &putImageTagMutabilityResponse{
		RegistryId:         s.accountID(),
		RepositoryName:     req.RepositoryName,
		ImageTagMutability: req.ImageTagMutability,
	}, nil
}

// putImageScanningConfigurationTyped is the CBOR counterpart of
// putImageScanningConfiguration; see that handler for why no scan engine
// runs either way.
func (s *Service) putImageScanningConfigurationTyped(ctx context.Context, req *putImageScanningConfigurationRequest) (*putImageScanningConfigurationResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	repo, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	repo.ImageScanningConfiguration = req.ImageScanningConfiguration
	if err := s.saveRepo(ctx, region, repo); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &putImageScanningConfigurationResponse{
		RegistryId:                 s.accountID(),
		RepositoryName:             req.RepositoryName,
		ImageScanningConfiguration: repo.ImageScanningConfiguration,
	}, nil
}

func (s *Service) describeRepositoriesTyped(ctx context.Context, req *describeRepositoriesRequest) (*describeRepositoriesResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	if len(req.RepositoryNames) > 0 {
		// Per AWS docs (API_DescribeRepositories.html): maxResults and nextToken
		// "cannot be used when you specify repositories with repositoryNames".
		if req.MaxResults != nil || req.NextToken != "" {
			return nil, ecrInvalidParameter("maxResults and nextToken cannot be used when repositoryNames is specified")
		}
		repos := make([]*Repository, 0, len(req.RepositoryNames))
		for _, name := range req.RepositoryNames {
			repo, found, err := s.getRepo(ctx, region, name)
			if err != nil {
				return nil, protocol.ErrInternalError
			}
			if !found {
				return nil, s.errRepoNotFoundTyped(name)
			}
			s.applyCurrentRepoURI(ctx, region, repo)
			repos = append(repos, repo)
		}
		return &describeRepositoriesResponse{Repositories: repos}, nil
	}
	kvs, err := s.store.Scan(ctx, repoNamespace, serviceutil.RegionKey(region, ""))
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	repos := make([]*Repository, 0, len(kvs))
	for _, kv := range kvs {
		var repo Repository
		if err := json.Unmarshal([]byte(kv.Value), &repo); err != nil {
			continue
		}
		s.applyCurrentRepoURI(ctx, region, &repo)
		repos = append(repos, &repo)
	}
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].RepositoryName < repos[j].RepositoryName
	})
	repos, next, aerr := ecrPage(repos, req.MaxResults, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &describeRepositoriesResponse{Repositories: repos, NextToken: next}, nil
}

func (s *Service) deleteRepositoryTyped(ctx context.Context, req *deleteRepositoryRequest) (*deleteRepositoryResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	repo, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	if !req.Force {
		hasImages, err := s.repoHoldsImages(ctx, region, req.RepositoryName)
		if err != nil {
			return nil, protocol.ErrInternalError
		}
		if hasImages {
			return nil, s.errRepoNotEmpty(req.RepositoryName)
		}
	}
	s.applyCurrentRepoURI(ctx, region, repo)
	key := serviceutil.RegionKey(region, req.RepositoryName)
	if err := s.store.Delete(ctx, repoNamespace, key); err != nil {
		return nil, protocol.ErrInternalError
	}
	imgKeys, _ := s.store.List(ctx, imageNamespace, serviceutil.RegionKey(region, req.RepositoryName+"/"))
	for _, k := range imgKeys {
		_ = s.store.Delete(ctx, imageNamespace, k)
	}
	_ = s.store.Delete(ctx, policyNamespace, key)
	_ = s.store.Delete(ctx, lifecycleNS, key)
	s.publish(ctx, events.ECRRepositoryDeleted, events.ResourcePayload{Name: req.RepositoryName, ARN: repo.RepositoryArn})
	return &deleteRepositoryResponse{Repository: *repo}, nil
}

func (s *Service) getAuthorizationTokenTyped(ctx context.Context, _ *struct{}) (*getAuthorizationTokenResponse, *protocol.AWSError) {
	_ = s.ensureRegistry(ctx)
	password := "test"
	s.registryMu.Lock()
	if s.registryPassword != "" {
		password = s.registryPassword
	}
	s.registryMu.Unlock()
	token := base64.StdEncoding.EncodeToString([]byte("AWS:" + password))
	base := s.registryEndpoint()
	expires := float64(s.clk.Now().Add(12 * time.Hour).Unix())
	return &getAuthorizationTokenResponse{AuthorizationData: []authDataItem{{
		AuthorizationToken: token,
		ProxyEndpoint:      base,
		ExpiresAt:          expires,
	}}}, nil
}

func (s *Service) describeRegistryTyped(ctx context.Context, _ *struct{}) (*describeRegistryResponse, *protocol.AWSError) {
	return &describeRegistryResponse{
		RegistryId:               s.accountID(),
		ReplicationConfiguration: replicationConfig{Rules: []any{}},
	}, nil
}

func (s *Service) listImagesTyped(ctx context.Context, req *listImagesRequest) (*listImagesResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	// Deliberately not conditioned on the sweep's outcome, unlike DescribeImages
	// and BatchGetImage. An empty list is a legitimate answer for a repository
	// with no images, ListImages makes no claim about a specific image, and
	// cdk-assets checks DescribeImages rather than this. Failing the call would
	// take a working listing away from every caller whose registry is merely
	// slow to start. The sweep logs the outcome either way.
	_ = s.syncRepoImagesFromRegistry(ctx, region, req.RepositoryName)
	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	ids := make([]ImageIdentifier, 0, len(kvs))
	for _, kv := range kvs {
		var img Image
		if err := json.Unmarshal([]byte(kv.Value), &img); err != nil {
			continue
		}
		ids = append(ids, img.ImageId)
	}
	// Sorted so a token means the same offset on every call, whatever the
	// store backend's scan order.
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].ImageDigest != ids[j].ImageDigest {
			return ids[i].ImageDigest < ids[j].ImageDigest
		}
		return ids[i].ImageTag < ids[j].ImageTag
	})
	ids, next, aerr := ecrPage(ids, req.MaxResults, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &listImagesResponse{ImageIds: ids, NextToken: next}, nil
}

func (s *Service) describeImagesTyped(ctx context.Context, req *describeImagesRequest) (*describeImagesResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	// Per AWS docs (API_DescribeImages.html): maxResults and nextToken "cannot
	// be used when you specify images with imageIds".
	if len(req.ImageIds) > 0 && (req.MaxResults != nil || req.NextToken != "") {
		return nil, ecrInvalidParameter("maxResults and nextToken cannot be used when imageIds is specified")
	}
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	sweep := s.syncRepoImagesFromRegistry(ctx, region, req.RepositoryName)
	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	images := make([]Image, 0, len(kvs))
	for _, kv := range kvs {
		var img Image
		if err := json.Unmarshal([]byte(kv.Value), &img); err != nil {
			continue
		}
		images = append(images, img)
	}
	images, missing := selectImages(images, req.ImageIds)
	if missing != nil {
		// Only the registry can establish that an image is absent, because
		// only a sweep puts a pushed image on record. Without one, this store
		// looks identical to a repository nobody ever pushed to.
		if sweep == sweepUnavailable {
			return nil, s.errRegistryUnavailable(req.RepositoryName)
		}
		return nil, s.errImageNotFound(req.RepositoryName, *missing)
	}
	sort.Slice(images, func(i, j int) bool {
		return images[i].ImageId.ImageDigest < images[j].ImageId.ImageDigest
	})
	details := make([]imageDetailWire, 0, len(images))
	for _, img := range images {
		d := imageDetailWire{
			RegistryId:             img.RegistryId,
			RepositoryName:         img.RepositoryName,
			ImageDigest:            img.ImageId.ImageDigest,
			ImageManifestMediaType: img.ImageManifestMediaType,
		}
		if img.ImageId.ImageTag != "" {
			d.ImageTags = []string{img.ImageId.ImageTag}
		}
		details = append(details, d)
	}
	details, next, aerr := ecrPage(details, req.MaxResults, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &describeImagesResponse{ImageDetails: details, NextToken: next}, nil
}

func (s *Service) putImageTyped(ctx context.Context, req *putImageRequest) (*putImageResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
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
		return nil, protocol.ErrInternalError
	}
	if err := s.store.Set(ctx, imageNamespace, imageKey(region, req.RepositoryName, digest), string(raw)); err != nil {
		return nil, protocol.ErrInternalError
	}
	s.publish(ctx, events.ECRImagePushed, events.ResourcePayload{Name: req.RepositoryName, ARN: s.repoARN(region, req.RepositoryName)})
	return &putImageResponse{Image: img}, nil
}

func (s *Service) batchGetImageTyped(ctx context.Context, req *imageIDSetRequest) (*batchGetImageResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	sweep := s.syncRepoImagesFromRegistry(ctx, region, req.RepositoryName)
	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
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
	failures := []imageFailureWire{}
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
			failures = append(failures, imageFailureWire{
				ImageID:       id,
				FailureCode:   "ImageNotFoundException",
				FailureReason: "Requested image not found",
			})
		}
	}
	// A failure list built without a sweep says "not found" about images whose
	// absence was never established. One ServerException for the call is the
	// honest answer, and the one an SDK will retry.
	if len(failures) > 0 && sweep == sweepUnavailable {
		return nil, s.errRegistryUnavailable(req.RepositoryName)
	}
	return &batchGetImageResponse{Images: images, Failures: failures}, nil
}

func (s *Service) describeImageScanFindingsTyped(ctx context.Context, req *describeImageScanFindingsRequest) (*describeImageScanFindingsResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		return nil, protocol.ErrInternalError
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
		return nil, &protocol.AWSError{
			Code: "ImageNotFoundException", Message: "Requested image not found", HTTPStatus: http.StatusBadRequest,
		}
	}
	return &describeImageScanFindingsResponse{
		RegistryId:     s.accountID(),
		RepositoryName: req.RepositoryName,
		ImageID:        matched.ImageId,
		ImageScanStatus: scanStatusWire{
			Status:      "UNSUPPORTED_IMAGE",
			Description: "Image scanning is not emulated",
		},
		ImageScanFindings: scanFindingsWire{
			FindingSeverityCounts: map[string]any{},
			Findings:              []any{},
			EnhancedFindings:      []any{},
		},
	}, nil
}

func (s *Service) batchDeleteImageTyped(ctx context.Context, req *imageIDSetRequest) (*batchDeleteImageResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	prefix := serviceutil.RegionKey(region, req.RepositoryName+"/")
	kvs, err := s.store.Scan(ctx, imageNamespace, prefix)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	byDigest := map[string]string{}
	byTag := map[string]string{}
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
	failures := []imageFailureWire{}
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
			failures = append(failures, imageFailureWire{
				ImageID:       id,
				FailureCode:   "ImageNotFoundException",
				FailureReason: "Requested image not found",
			})
		}
	}
	return &batchDeleteImageResponse{ImageIds: deleted, Failures: failures}, nil
}

func (s *Service) setRepositoryPolicyTyped(ctx context.Context, req *setRepositoryPolicyRequest) (*setRepositoryPolicyResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	key := serviceutil.RegionKey(region, req.RepositoryName)
	if err := s.store.Set(ctx, policyNamespace, key, req.PolicyText); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &setRepositoryPolicyResponse{
		RegistryId:     s.accountID(),
		RepositoryName: req.RepositoryName,
		PolicyText:     req.PolicyText,
	}, nil
}

func (s *Service) getRepositoryPolicyTyped(ctx context.Context, req *repoRefRequest) (*getRepositoryPolicyResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	key := serviceutil.RegionKey(region, req.RepositoryName)
	policyText, found, err := s.store.Get(ctx, policyNamespace, key)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, &protocol.AWSError{
			Code: "RepositoryPolicyNotFoundException", Message: fmt.Sprintf("Repository policy does not exist for the repository with name '%s'", req.RepositoryName), HTTPStatus: http.StatusBadRequest,
		}
	}
	return &getRepositoryPolicyResponse{
		RegistryId:     s.accountID(),
		RepositoryName: req.RepositoryName,
		PolicyText:     policyText,
	}, nil
}

func (s *Service) deleteRepositoryPolicyTyped(ctx context.Context, req *repoRefRequest) (*deleteRepositoryPolicyResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	key := serviceutil.RegionKey(region, req.RepositoryName)
	policyText, hasPolicy, err := s.store.Get(ctx, policyNamespace, key)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !hasPolicy {
		return nil, &protocol.AWSError{
			Code: "RepositoryPolicyNotFoundException", Message: fmt.Sprintf("Repository policy does not exist for the repository with name '%s'", req.RepositoryName), HTTPStatus: http.StatusBadRequest,
		}
	}
	_ = s.store.Delete(ctx, policyNamespace, key)
	return &deleteRepositoryPolicyResponse{
		RegistryId:     s.accountID(),
		RepositoryName: req.RepositoryName,
		PolicyText:     policyText,
	}, nil
}

func (s *Service) putLifecyclePolicyTyped(ctx context.Context, req *putLifecyclePolicyRequest) (*putLifecyclePolicyResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	key := serviceutil.RegionKey(region, req.RepositoryName)
	if err := s.store.Set(ctx, lifecycleNS, key, req.LifecyclePolicyText); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &putLifecyclePolicyResponse{
		RegistryId:          s.accountID(),
		RepositoryName:      req.RepositoryName,
		LifecyclePolicyText: req.LifecyclePolicyText,
	}, nil
}

func (s *Service) getLifecyclePolicyTyped(ctx context.Context, req *repoRefRequest) (*getLifecyclePolicyResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	key := serviceutil.RegionKey(region, req.RepositoryName)
	policyText, found, err := s.store.Get(ctx, lifecycleNS, key)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, &protocol.AWSError{
			Code: "LifecyclePolicyNotFoundException", Message: fmt.Sprintf("Lifecycle policy does not exist for the repository with name '%s'", req.RepositoryName), HTTPStatus: http.StatusBadRequest,
		}
	}
	return &getLifecyclePolicyResponse{
		RegistryId:          s.accountID(),
		RepositoryName:      req.RepositoryName,
		LifecyclePolicyText: policyText,
	}, nil
}

func (s *Service) deleteLifecyclePolicyTyped(ctx context.Context, req *repoRefRequest) (*deleteLifecyclePolicyResponse, *protocol.AWSError) {
	region := s.regionCtx(ctx)
	_, found, err := s.getRepo(ctx, region, req.RepositoryName)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, s.errRepoNotFoundTyped(req.RepositoryName)
	}
	key := serviceutil.RegionKey(region, req.RepositoryName)
	lifecycleText, hasLP, err := s.store.Get(ctx, lifecycleNS, key)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !hasLP {
		return nil, &protocol.AWSError{
			Code: "LifecyclePolicyNotFoundException", Message: fmt.Sprintf("Lifecycle policy does not exist for the repository with name '%s'", req.RepositoryName), HTTPStatus: http.StatusBadRequest,
		}
	}
	_ = s.store.Delete(ctx, lifecycleNS, key)
	return &deleteLifecyclePolicyResponse{
		RegistryId:          s.accountID(),
		RepositoryName:      req.RepositoryName,
		LifecyclePolicyText: lifecycleText,
	}, nil
}

func (s *Service) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*tagResourceResponse, *protocol.AWSError) {
	existing, err := s.loadTags(ctx, req.ResourceArn)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	tagMap := map[string]string{}
	for _, t := range existing {
		tagMap[t.Key] = t.Value
	}
	for _, t := range req.Tags {
		tagMap[t.Key] = t.Value
	}
	// Validated against the merged set, not just the incoming delta, so the
	// 50-tag limit holds across repeated TagResource calls (#1052).
	if aerr := serviceutil.ValidateTags(ecrTagCfg, tagMap); aerr != nil {
		return nil, aerr
	}
	merged := make([]Tag, 0, len(tagMap))
	for k, v := range tagMap {
		merged = append(merged, Tag{Key: k, Value: v})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Key < merged[j].Key })
	if err := s.saveTags(ctx, req.ResourceArn, merged); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &tagResourceResponse{}, nil
}

func (s *Service) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*untagResourceResponse, *protocol.AWSError) {
	existing, err := s.loadTags(ctx, req.ResourceArn)
	if err != nil {
		return nil, protocol.ErrInternalError
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
		return nil, protocol.ErrInternalError
	}
	return &untagResourceResponse{}, nil
}

func (s *Service) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceResponse, *protocol.AWSError) {
	tags, err := s.loadTags(ctx, req.ResourceArn)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if tags == nil {
		tags = []Tag{}
	}
	return &listTagsForResourceResponse{Tags: tags}, nil
}
