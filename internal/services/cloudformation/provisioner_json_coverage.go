package cloudformation

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/config"
)

// ── AWS::CertificateManager::Certificate ─────────────────────────────────────

type acmCertificateHandler struct{}

func (h *acmCertificateHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	if v, _ := props["DomainName"].(string); v != "" {
		body["DomainName"] = v
	}
	if v, ok := props["SubjectAlternativeNames"]; ok {
		body["SubjectAlternativeNames"] = v
	}
	if v, _ := props["ValidationMethod"].(string); v != "" {
		body["ValidationMethod"] = v
	}
	// RequestCertificate applies Tags at creation (acm/typed_logic.go), so
	// there is no separate tagging call the way Shield's CreateProtection
	// needs one.
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = ecrTagsFromMap(tags)
	}
	noteUnconsumedProperties(ctx, "AWS::CertificateManager::Certificate", props,
		"DomainName", "SubjectAlternativeNames", "ValidationMethod", "Tags")

	rec, err := internalJSON(ctx, router, rCtx.Region, "CertificateManager.RequestCertificate", body)
	if err != nil {
		return "", nil, fmt.Errorf("RequestCertificate: %w", err)
	}

	var resp struct {
		CertificateArn string `json:"CertificateArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("RequestCertificate: parse response: %w", err)
	}

	arn := resp.CertificateArn
	if arn == "" {
		domain, _ := props["DomainName"].(string)
		arn = fmt.Sprintf("arn:aws:acm:%s:%s:certificate/%s", rCtx.Region, rCtx.AccountID, domain)
	}

	attrs := map[string]string{
		"Arn": arn,
	}
	return arn, attrs, nil
}

func (h *acmCertificateHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"CertificateArn": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "CertificateManager.DeleteCertificate", body)
	return teardownError("DeleteCertificate", rec, err)
}

// acmReconcileTags diffs desired against previous and applies only the
// change via AddTagsToCertificate/RemoveTagsFromCertificate, mirroring
// cloudtrailReconcileTags' and transferReconcileTags' add/remove split.
// RemoveTagsFromCertificate takes a Tags list rather than TagKeys
// (acm/typed_logic.go's removeTagsFromCertificateTyped reads only each
// entry's Key), so a removed key is sent with no Value, the same way
// cloudtrailRemoveTags does for CloudTrail's own Key-only RemoveTags.
func acmReconcileTags(ctx context.Context, router http.Handler, region, certArn string, tags, prior map[string]string) error {
	upserts, removals := logsLogGroupTagChanges(tags, prior)
	if len(upserts) > 0 {
		body := map[string]any{"CertificateArn": certArn, "Tags": ecrTagsFromMap(upserts)}
		if _, err := internalJSON(ctx, router, region, "CertificateManager.AddTagsToCertificate", body); err != nil {
			return fmt.Errorf("AddTagsToCertificate: %w", err)
		}
	}
	if len(removals) > 0 {
		entries := make([]map[string]string, 0, len(removals))
		for _, k := range removals {
			entries = append(entries, map[string]string{"Key": k})
		}
		body := map[string]any{"CertificateArn": certArn, "Tags": entries}
		if _, err := internalJSON(ctx, router, region, "CertificateManager.RemoveTagsFromCertificate", body); err != nil {
			return fmt.Errorf("RemoveTagsFromCertificate: %w", err)
		}
	}
	return nil
}

// Update forces replacement for DomainName, SubjectAlternativeNames or
// ValidationMethod — RequestCertificate is the only way to change any of
// them, and ACM has no in-place rename — but reconciles a Tags-only change
// via AddTagsToCertificate/RemoveTagsFromCertificate instead, matching real
// ACM: Tags never force replacement.
func (h *acmCertificateHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, property := range []string{"DomainName", "SubjectAlternativeNames", "ValidationMethod"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		if err := acmReconcileTags(ctx, router, rCtx.Region, physicalID, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("acm tags: %w", err))
		}
	}
	return physicalID, map[string]string{"Arn": physicalID}, nil
}

// ── AWS::ECR::Repository ────────────────────────────────────────────────────

const ecrTargetPrefix = "AmazonEC2ContainerRegistry_V20150921."

type ecrRepositoryHandler struct{}

// ecrPolicyText renders a policy property as the JSON text the ECR API takes.
// Templates supply the policy either inline as an object or pre-rendered as a
// string.
func ecrPolicyText(v any) (string, bool) {
	switch p := v.(type) {
	case string:
		return p, p != ""
	case map[string]any:
		b, err := json.Marshal(p)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

// ecrApplyRepositoryPolicies pushes the mutable policy properties
// (RepositoryPolicyText, LifecyclePolicy) onto an existing repository.
// oldProps is consulted only to remove a policy the previous template carried
// and the new one dropped.
func ecrApplyRepositoryPolicies(ctx context.Context, router http.Handler, rCtx *resolveContext, name string, props, oldProps map[string]any) error {
	if text, ok := ecrPolicyText(props["RepositoryPolicyText"]); ok {
		body := map[string]any{"repositoryName": name, "policyText": text}
		if _, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"SetRepositoryPolicy", body); err != nil {
			return fmt.Errorf("SetRepositoryPolicy: %w", err)
		}
	} else if _, had := oldProps["RepositoryPolicyText"]; had {
		// Removing a policy the new template dropped. A policy that is not
		// there is already in the state being asked for; anything else left it
		// attached, which the update must not report as applied.
		rec, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"DeleteRepositoryPolicy", map[string]any{"repositoryName": name})
		if err := teardownError("DeleteRepositoryPolicy", rec, err); err != nil {
			return err
		}
	}

	lifecycleText := ""
	if lp, ok := props["LifecyclePolicy"].(map[string]any); ok {
		lifecycleText, _ = lp["LifecyclePolicyText"].(string)
	}
	if lifecycleText != "" {
		body := map[string]any{"repositoryName": name, "lifecyclePolicyText": lifecycleText}
		if _, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"PutLifecyclePolicy", body); err != nil {
			return fmt.Errorf("PutLifecyclePolicy: %w", err)
		}
	} else if _, had := oldProps["LifecyclePolicy"]; had {
		rec, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"DeleteLifecyclePolicy", map[string]any{"repositoryName": name})
		if err := teardownError("DeleteLifecyclePolicy", rec, err); err != nil {
			return err
		}
	}
	return nil
}

// ecrImageScanningConfigBody renders the ImageScanningConfiguration property
// into the scanOnPush body CreateRepository/PutImageScanningConfiguration
// take. A template that omits the property gets no call at all on Create —
// the service's own CreateRepository default (scanOnPush: false) stands —
// and no reconciliation on Update, since there is nothing to compare against.
func ecrImageScanningConfigBody(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	scanOnPush, _ := m["ScanOnPush"].(bool)
	return map[string]any{"scanOnPush": scanOnPush}, true
}

// ecrEncryptionConfigBody renders the EncryptionConfiguration property into
// the body CreateRepository takes, applying the same AES256 default the ECR
// service applies. Comparing two calls to this — see the Update handler — is
// therefore not fooled by an explicit "AES256" looking different from an
// absent property that resolves to the same thing.
func ecrEncryptionConfigBody(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	encType, _ := m["EncryptionType"].(string)
	if encType == "" {
		encType = "AES256"
	}
	body := map[string]any{"encryptionType": encType}
	if kmsKey, _ := m["KmsKey"].(string); kmsKey != "" {
		body["kmsKey"] = kmsKey
	}
	return body
}

// ecrTagsFromMap renders a merged stack+resource tag map into the
// {Key,Value} list shape ecr.Tag — and therefore CreateRepository's "tags"
// and TagResource's "tags" — takes, sorted for a deterministic request body.
func ecrTagsFromMap(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{"Key": k, "Value": tags[k]})
	}
	return out
}

// ecrReconcileTags reconciles a repository's tags on Update: added or
// changed keys go through TagResource, keys dropped from the template go
// through UntagResource. Mirrors updateLambdaTags/updateSQSQueueTags's diff
// shape for ECR's ARN-keyed, AWSJSON1.1 TagResource/UntagResource pair.
func ecrReconcileTags(ctx context.Context, router http.Handler, region, arn string, tags, prior map[string]string) error {
	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)

	if len(added) > 0 {
		body := map[string]any{"resourceArn": arn, "tags": ecrTagsFromMap(added)}
		if _, err := internalJSON(ctx, router, region, ecrTargetPrefix+"TagResource", body); err != nil {
			return fmt.Errorf("ecr TagResource: %w", err)
		}
	}
	if len(removed) > 0 {
		body := map[string]any{"resourceArn": arn, "tagKeys": removed}
		if _, err := internalJSON(ctx, router, region, ecrTargetPrefix+"UntagResource", body); err != nil {
			return fmt.Errorf("ecr UntagResource: %w", err)
		}
	}
	return nil
}

func (h *ecrRepositoryHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["RepositoryName"].(string)
	if name == "" {
		// Lowercase: ECR rejects uppercase repository names, and both halves
		// of a generated name are mixed case.
		name = rCtx.generatedNameLowerWithin(maxNameLenECR)
	}

	body := map[string]any{
		"repositoryName": name,
	}
	if mutability, ok := props["ImageTagMutability"].(string); ok && mutability != "" {
		body["imageTagMutability"] = mutability
	}
	if sc, ok := ecrImageScanningConfigBody(props["ImageScanningConfiguration"]); ok {
		body["imageScanningConfiguration"] = sc
	}
	if ec := ecrEncryptionConfigBody(props["EncryptionConfiguration"]); ec != nil {
		body["encryptionConfiguration"] = ec
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["tags"] = ecrTagsFromMap(tags)
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"CreateRepository", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateRepository: %w", err)
	}

	if err := ecrApplyRepositoryPolicies(ctx, router, rCtx, name, props, nil); err != nil {
		return "", nil, err
	}

	var resp struct {
		Repository struct {
			RepositoryArn string `json:"repositoryArn"`
			RepositoryUri string `json:"repositoryUri"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateRepository: parse response: %w", err)
	}

	arn := resp.Repository.RepositoryArn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:ecr:%s:%s:repository/%s", rCtx.Region, rCtx.AccountID, name)
	}

	// RepositoryUri comes from the CreateRepository response rather than being
	// rebuilt here. ECR mints it on the address its registry is actually
	// listening on (see Service.registryEndpoint); synthesising
	// "{account}.dkr.ecr.{region}.amazonaws.com/{name}" instead meant
	// Fn::GetAtt RepositoryUri handed stacks a registry no `docker push` in
	// this environment can reach, while `aws ecr describe-repositories` on the
	// same repository returned the working one.
	uri := resp.Repository.RepositoryUri
	if uri == "" {
		uri = fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/%s", rCtx.AccountID, rCtx.Region, name)
	}

	attrs := map[string]string{
		"Arn":            arn,
		"RepositoryUri":  uri,
		"RepositoryName": name,
	}
	return arn, attrs, nil
}

// Delete is the fallback teardown path, used when no template properties are
// available for the resource being removed (e.g. a resource orphaned by
// drift). With nothing to consult for EmptyOnDelete it keeps the previous
// behavior of always forcing.
func (h *ecrRepositoryHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	return ecrDeleteRepository(ctx, router, physicalID, rCtx, true)
}

// DeleteWithProperties honors EmptyOnDelete: real CloudFormation only forces
// the delete of a repository that still holds images when the template says
// EmptyOnDelete: true (default false) — otherwise DeleteRepository fails with
// RepositoryNotEmptyException the same as a bare `aws ecr delete-repository`
// would, and the stack operation reports that failure instead of silently
// discarding images nothing asked it to.
func (h *ecrRepositoryHandler) DeleteWithProperties(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	emptyOnDelete, _ := props["EmptyOnDelete"].(bool)
	return ecrDeleteRepository(ctx, router, physicalID, rCtx, emptyOnDelete)
}

func ecrDeleteRepository(ctx context.Context, router http.Handler, physicalID string, rCtx *resolveContext, force bool) error {
	// Extract repository name from ARN if possible, otherwise use physicalID as name.
	name := physicalID
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		name = physicalID[idx+1:]
	}
	body := map[string]any{
		"repositoryName": name,
		"force":          force,
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"DeleteRepository", body)
	return teardownError("DeleteRepository", rec, err)
}

func (h *ecrRepositoryHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the repository ARN, "arn:…:repository/{name}".
	oldName := physicalID
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		oldName = physicalID[idx+1:]
	}

	// RepositoryName is the only AWS::ECR::Repository property real
	// CloudFormation replaces on. Answering "replace" for anything else breaks
	// re-running `cdk bootstrap` after a CDK upgrade: the toolkit stack pins
	// the repository name, so the replacement re-creates the same name and
	// fails with RepositoryAlreadyExistsException.
	if n, ok := props["RepositoryName"].(string); ok && n != "" && n != oldName {
		return "", nil, errReplacementRequired
	}

	// EncryptionConfiguration is fixed at CreateRepository — real ECR has no
	// Put* to change it later — so a template that changes it must replace the
	// repository rather than have the update silently keep the old encryption.
	if !reflect.DeepEqual(ecrEncryptionConfigBody(props["EncryptionConfiguration"]), ecrEncryptionConfigBody(oldProps["EncryptionConfiguration"])) {
		return "", nil, errReplacementRequired
	}

	if err := ecrApplyRepositoryPolicies(ctx, router, rCtx, oldName, props, oldProps); err != nil {
		return "", nil, err
	}

	if mutability, ok := props["ImageTagMutability"].(string); ok && mutability != "" {
		body := map[string]any{"repositoryName": oldName, "imageTagMutability": mutability}
		if _, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"PutImageTagMutability", body); err != nil {
			return "", nil, fmt.Errorf("PutImageTagMutability: %w", err)
		}
	}

	if sc, ok := ecrImageScanningConfigBody(props["ImageScanningConfiguration"]); ok {
		body := map[string]any{"repositoryName": oldName, "imageScanningConfiguration": sc}
		if _, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"PutImageScanningConfiguration", body); err != nil {
			return "", nil, fmt.Errorf("PutImageScanningConfiguration: %w", err)
		}
	}

	newTags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	oldTags := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if err := ecrReconcileTags(ctx, router, rCtx.Region, physicalID, newTags, oldTags); err != nil {
		return "", nil, err
	}

	// Same reason as in Create: RepositoryUri must be the one ECR minted, so
	// re-read it rather than synthesising the amazonaws.com form.
	uri := ""
	if rec, err := internalJSON(ctx, router, rCtx.Region, ecrTargetPrefix+"DescribeRepositories", map[string]any{"repositoryNames": []string{oldName}}); err == nil {
		var resp struct {
			Repositories []struct {
				RepositoryUri string `json:"repositoryUri"`
			} `json:"repositories"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &resp) == nil && len(resp.Repositories) == 1 {
			uri = resp.Repositories[0].RepositoryUri
		}
	}
	if uri == "" {
		uri = fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/%s", rCtx.AccountID, rCtx.Region, oldName)
	}

	attrs := map[string]string{
		"Arn":            physicalID,
		"RepositoryUri":  uri,
		"RepositoryName": oldName,
	}
	return physicalID, attrs, nil
}

// ── AWS::CloudTrail::Trail ──────────────────────────────────────────────────

type cloudtrailTrailHandler struct{}

func (h *cloudtrailTrailHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// The schema's property is TrailName, not Name
	// (https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-cloudtrail-trail.html).
	// Per the alpha no-shims policy, Name is not accepted as an alias: a
	// template that sets it simply does not set the trail's name, exactly as
	// a template setting any other unrecognised property would not.
	name, _ := props["TrailName"].(string)
	if name == "" {
		// TrailName must start and end with a letter or a digit; the random
		// suffix guarantees the tail and a stack name guarantees the head.
		name = rCtx.generatedNameWithin(maxNameLenCloudTrail)
	}
	s3Bucket, _ := props["S3BucketName"].(string)
	if s3Bucket == "" {
		// This one is an S3 bucket name, not a trail name: lowercase only and
		// capped at 63 rather than 128, so it gets S3's rule rather than
		// CloudTrail's.
		s3Bucket = rCtx.generatedNameLowerWithin(maxNameLenS3)
	}

	includeGlobal := true
	if v, ok := props["IncludeGlobalServiceEvents"].(bool); ok {
		includeGlobal = v
	}
	isMultiRegion := false
	if v, ok := props["IsMultiRegionTrail"].(bool); ok {
		isMultiRegion = v
	}

	body := map[string]any{
		"Name":                       name,
		"S3BucketName":               s3Bucket,
		"IncludeGlobalServiceEvents": includeGlobal,
		"IsMultiRegionTrail":         isMultiRegion,
	}
	// CreateTrailInput's member names (internal/services/cloudtrail/
	// typed_logic.go) already match the template's own PascalCase 1:1 apart
	// from KMSKeyId/KmsKeyId, so these are copied verbatim rather than run
	// through forwardProperties, which would lowercase the leading letter.
	if v, _ := props["S3KeyPrefix"].(string); v != "" {
		body["S3KeyPrefix"] = v
	}
	if v, _ := props["CloudWatchLogsLogGroupArn"].(string); v != "" {
		body["CloudWatchLogsLogGroupArn"] = v
	}
	if v, _ := props["CloudWatchLogsRoleArn"].(string); v != "" {
		body["CloudWatchLogsRoleArn"] = v
	}
	if v, ok := props["EnableLogFileValidation"].(bool); ok {
		body["EnableLogFileValidation"] = v
	}
	if v, _ := props["KMSKeyId"].(string); v != "" {
		body["KmsKeyId"] = v
	}
	if v, ok := props["IsOrganizationTrail"].(bool); ok {
		body["IsOrganizationTrail"] = v
	}
	// CreateTrail is the only trail operation that carries tags inline
	// (internal/services/cloudtrail/typed_logic.go); Update reconciles via
	// AddTags/RemoveTags below. Stack tags merge in here the same way every
	// other propagating resource type does (#1310).
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["TagsList"] = cloudtrailTagsWire(tags)
	}
	noteUnconsumedProperties(ctx, "AWS::CloudTrail::Trail", props,
		"TrailName", "S3BucketName", "IncludeGlobalServiceEvents", "IsMultiRegionTrail", "Tags",
		"S3KeyPrefix", "CloudWatchLogsLogGroupArn", "CloudWatchLogsRoleArn", "EnableLogFileValidation",
		"KMSKeyId", "IsOrganizationTrail")

	rec, err := internalJSON(ctx, router, rCtx.Region, "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.CreateTrail", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateTrail: %w", err)
	}

	var resp struct {
		TrailARN string `json:"TrailARN"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateTrail: parse response: %w", err)
	}

	arn := resp.TrailARN
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:cloudtrail:%s:%s:trail/%s", rCtx.Region, rCtx.AccountID, name)
	}

	attrs := map[string]string{
		"Arn": arn,
		// Ref returns the trail's name, matching AWS: "Ref returns the
		// friendly name of the trail, such as MyTrail." The physical ID stays
		// the ARN (Delete recovers the name from it).
		"Ref":  name,
		"Name": name,
	}
	return arn, attrs, nil
}

func (h *cloudtrailTrailHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	name := physicalID
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		name = physicalID[idx+1:]
	}
	body := map[string]any{"Name": name}
	rec, err := internalJSON(ctx, router, rCtx.Region, "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.DeleteTrail", body)
	return teardownError("DeleteTrail", rec, err)
}

func (h *cloudtrailTrailHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// TrailName is create-only in the schema — CloudTrail's UpdateTrail has no
	// way to rename a trail — so a changed value forces replacement, same as
	// the pre-fix "Name" comparison did.
	if n, ok := props["TrailName"].(string); ok && n != "" {
		tail := physicalID
		if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
			tail = physicalID[idx+1:]
		}
		if tail != n {
			return "", nil, errReplacementRequired
		}
	}

	name, _ := props["TrailName"].(string)
	if name == "" {
		name = physicalID
		if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
			name = physicalID[idx+1:]
		}
	}
	s3Bucket, _ := props["S3BucketName"].(string)

	body := map[string]any{
		"Name":         name,
		"S3BucketName": s3Bucket,
	}
	if v, ok := props["IncludeGlobalServiceEvents"]; ok {
		body["IncludeGlobalServiceEvents"] = v
	}
	if v, ok := props["IsMultiRegionTrail"]; ok {
		body["IsMultiRegionTrail"] = v
	}
	// updateTrailInput's members are pointers (internal/services/cloudtrail/
	// typed_logic.go), so an omitted property leaves the stored value alone —
	// forwarding only what the template set, same as the pair above.
	if v, ok := props["S3KeyPrefix"]; ok {
		body["S3KeyPrefix"] = v
	}
	if v, ok := props["CloudWatchLogsLogGroupArn"]; ok {
		body["CloudWatchLogsLogGroupArn"] = v
	}
	if v, ok := props["CloudWatchLogsRoleArn"]; ok {
		body["CloudWatchLogsRoleArn"] = v
	}
	if v, ok := props["EnableLogFileValidation"]; ok {
		body["EnableLogFileValidation"] = v
	}
	if v, ok := props["KMSKeyId"]; ok {
		body["KmsKeyId"] = v
	}
	if v, ok := props["IsOrganizationTrail"]; ok {
		body["IsOrganizationTrail"] = v
	}
	noteUnconsumedProperties(ctx, "AWS::CloudTrail::Trail", props,
		"TrailName", "S3BucketName", "IncludeGlobalServiceEvents", "IsMultiRegionTrail", "Tags",
		"S3KeyPrefix", "CloudWatchLogsLogGroupArn", "CloudWatchLogsRoleArn", "EnableLogFileValidation",
		"KMSKeyId", "IsOrganizationTrail")

	if _, err := internalJSON(ctx, router, rCtx.Region, "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.UpdateTrail", body); err != nil {
		return "", nil, fmt.Errorf("UpdateTrail: %w", err)
	}

	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		if err := cloudtrailReconcileTags(ctx, router, rCtx.Region, physicalID, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("cloudtrail tags: %w", err))
		}
	}

	return physicalID, map[string]string{"Arn": physicalID, "Ref": name, "Name": name}, nil
}

// cloudtrailTagsWire renders a merged tag map in CloudTrail's TagsList wire
// shape ([{Key,Value}]).
func cloudtrailTagsWire(tags map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(tags))
	for k, v := range tags {
		out = append(out, map[string]string{"Key": k, "Value": v})
	}
	return out
}

func cloudtrailAddTags(ctx context.Context, router http.Handler, region, resourceID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if _, err := internalJSON(ctx, router, region, "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.AddTags", map[string]any{
		"ResourceId": resourceID,
		"TagsList":   cloudtrailTagsWire(tags),
	}); err != nil {
		return fmt.Errorf("cloudtrail AddTags: %w", err)
	}
	return nil
}

func cloudtrailRemoveTags(ctx context.Context, router http.Handler, region, resourceID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	// RemoveTags matches on each TagsList entry's Key alone and ignores Value
	// (internal/services/cloudtrail/typed_logic.go's removeTagsTyped).
	entries := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, map[string]string{"Key": k})
	}
	if _, err := internalJSON(ctx, router, region, "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.RemoveTags", map[string]any{
		"ResourceId": resourceID,
		"TagsList":   entries,
	}); err != nil {
		return fmt.Errorf("cloudtrail RemoveTags: %w", err)
	}
	return nil
}

// cloudtrailReconcileTags diffs desired against previous and applies only the
// change, mirroring sfnReconcileTags' and the SSM parameter handler's
// add/remove split.
func cloudtrailReconcileTags(ctx context.Context, router http.Handler, region, resourceID string, tags, prior map[string]string) error {
	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	if err := cloudtrailAddTags(ctx, router, region, resourceID, added); err != nil {
		return err
	}
	return cloudtrailRemoveTags(ctx, router, region, resourceID, removed)
}

// ── AWS::Backup::BackupVault ────────────────────────────────────────────────

// backupVaultsPath and backupPlansPath are Backup's modeled collection
// bindings. Vaults are addressed by name beneath the first and plans by id
// beneath the second; both handlers return an ARN as the physical ID, so both
// Deletes have to recover the identifier from it.
const (
	backupVaultsPath = "/backup-vaults"
	backupPlansPath  = "/backup/plans"
	// backupTagsPath and backupUntagPath are Backup's modeled tagging
	// bindings (#1195): TagResource/ListTags share POST/GET /tags/{ResourceArn}
	// while UntagResource is the separate POST /untag/{ResourceArn} — see
	// internal/services/backup/handler_tags.go's header comment for why the
	// service itself keeps them apart.
	backupTagsPath  = "/tags"
	backupUntagPath = "/untag"
)

// backupIDFromARN returns the identifier a Backup ARN's last segment carries —
// the vault name in arn:…:backup-vault:<name>, the plan id in
// arn:…:backup-plan:<id>.
func backupIDFromARN(physicalID string) string {
	id := physicalID
	if i := strings.LastIndex(id, ":"); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// backupResourceTagMap converts CloudFormation's BackupVaultTags/
// BackupPlanTags shape — already a string-keyed map on the wire, unlike the
// [{Key,Value}] array most other resources use — to the map Backup's own
// TagResource/CreateBackupVault/CreateBackupPlan expect. There is no shape
// translation to do; this only guards against a template that put something
// other than an object there.
func backupResourceTagMap(raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Backup resource Tags must be an object")
	}
	tags := make(map[string]string, len(obj))
	for k, v := range obj {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("Backup resource Tags[%q] must be a string", k)
		}
		tags[k] = s
	}
	return tags, nil
}

// putBackupResourceTags calls TagResource for a vault or plan ARN. Whether a
// tag is acceptable (length, `aws:` prefix, the 50-tag limit) is the Backup
// service's own business, checked there — this only ships the shape.
// internalRequest already turns a >=400 response into a non-nil error
// (statusError), so a rejected tag surfaces as a Create/Update failure rather
// than silently succeeding.
func putBackupResourceTags(ctx context.Context, router http.Handler, region, resourceArn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	data, err := json.Marshal(map[string]any{"Tags": tags})
	if err != nil {
		return err
	}
	if _, err := internalRequest(ctx, router, region, http.MethodPost,
		backupTagsPath+"/"+url.PathEscape(resourceArn), "application/json", data); err != nil {
		return fmt.Errorf("TagResource: %w", err)
	}
	return nil
}

// untagBackupResource calls UntagResource — Backup's separate POST
// /untag/{ResourceArn}, not another member of the /tags dispatcher (see
// backupTagsPath/backupUntagPath above). The member is TagKeyList, Backup's
// own spelling (confirmed against the real AWS CLI, #1195) rather than the
// TagKeys most other services use.
func untagBackupResource(ctx context.Context, router http.Handler, region, resourceArn string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	data, err := json.Marshal(map[string]any{"TagKeyList": keys})
	if err != nil {
		return err
	}
	if _, err := internalRequest(ctx, router, region, http.MethodPost,
		backupUntagPath+"/"+url.PathEscape(resourceArn), "application/json", data); err != nil {
		return fmt.Errorf("UntagResource: %w", err)
	}
	return nil
}

type backupBackupVaultHandler struct{}

func (h *backupBackupVaultHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["BackupVaultName"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenBackup)
	}

	// BackupVaultName is an httpLabel, so it goes in the path rather than the
	// body. Its modeled pattern admits no character url.PathEscape would
	// rewrite, but escaping keeps a name that violates it from forging a path.
	body := map[string]any{}
	if v, ok := props["EncryptionKeyArn"].(string); ok && v != "" {
		body["EncryptionKeyArn"] = v
	}
	tags, err := backupResourceTagMap(props["BackupVaultTags"])
	if err != nil {
		return "", nil, err
	}
	if len(tags) > 0 {
		body["BackupVaultTags"] = tags
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateBackupVault: %w", err)
	}

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut,
		backupVaultsPath+"/"+url.PathEscape(name), "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("CreateBackupVault: %w", err)
	}

	var resp struct {
		BackupVaultArn  string `json:"BackupVaultArn"`
		BackupVaultName string `json:"BackupVaultName"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateBackupVault: parse response: %w", err)
	}

	arn := resp.BackupVaultArn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:backup:%s:%s:backup-vault:%s", rCtx.Region, rCtx.AccountID, name)
	}

	attrs := map[string]string{
		"BackupVaultArn":  arn,
		"BackupVaultName": name,
	}
	return arn, attrs, nil
}

func (h *backupBackupVaultHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		backupVaultsPath+"/"+url.PathEscape(backupIDFromARN(physicalID)), "", nil)
	return teardownError("DeleteBackupVault", rec, err)
}

func (h *backupBackupVaultHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the vault ARN, "arn:…:backup-vault:{name}".
	oldName := physicalID
	if idx := strings.LastIndex(oldName, ":"); idx >= 0 {
		oldName = oldName[idx+1:]
	}
	if n, ok := props["BackupVaultName"].(string); ok && n != "" && n != oldName {
		return "", nil, errReplacementRequired
	}
	// BackupVaultName is the only replacement property on real AWS, and
	// CreateBackupVault rejects duplicates, so replacing under an unchanged
	// name can never succeed. Nothing else this handler provisions is mutable
	// (EncryptionKeyArn is create-only) except its tags (#1195), which have no
	// in-place UpdateBackupVault member to ride along with — CreateBackupVault
	// is the only operation BackupVaultTags is modeled on — so a stack update
	// reconciles them with a TagResource/UntagResource follow-up instead, the
	// same pattern AWS::SNS::Topic uses for attributes SetTopicAttributes
	// itself cannot carry.
	newTags, err := backupResourceTagMap(props["BackupVaultTags"])
	if err != nil {
		return "", nil, err
	}
	oldTags, err := backupResourceTagMap(oldProps["BackupVaultTags"])
	if err != nil {
		return "", nil, err
	}
	upserts, removals := logsLogGroupTagChanges(newTags, oldTags)
	if err := putBackupResourceTags(ctx, router, rCtx.Region, physicalID, upserts); err != nil {
		return "", nil, err
	}
	if err := untagBackupResource(ctx, router, rCtx.Region, physicalID, removals); err != nil {
		return "", nil, err
	}
	return physicalID, nil, nil
}

// ── AWS::Backup::BackupPlan ─────────────────────────────────────────────────

type backupBackupPlanHandler struct{}

func (h *backupBackupPlanHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	backupPlan, _ := props["BackupPlan"].(map[string]any)
	body := map[string]any{"BackupPlan": backupPlan}
	tags, err := backupResourceTagMap(props["BackupPlanTags"])
	if err != nil {
		return "", nil, err
	}
	if len(tags) > 0 {
		body["BackupPlanTags"] = tags
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateBackupPlan: %w", err)
	}

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut,
		backupPlansPath, "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("CreateBackupPlan: %w", err)
	}

	var resp struct {
		BackupPlanArn string `json:"BackupPlanArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateBackupPlan: parse response: %w", err)
	}

	arn := resp.BackupPlanArn
	if arn == "" {
		// BackupPlanName is Required: Yes on the BackupPlan structure, so
		// unlike the rest of these fallbacks this is not CloudFormation
		// naming an unnamed resource — it is a placeholder for a
		// CreateBackupPlan that answered without an ARN. It is generated all
		// the same so two such plans in one stack are two ARNs.
		name := rCtx.generatedNameWithin(maxNameLenBackup)
		if bp, ok := props["BackupPlan"].(map[string]any); ok {
			if n, _ := bp["BackupPlanName"].(string); n != "" {
				name = n
			}
		}
		arn = fmt.Sprintf("arn:aws:backup:%s:%s:backup-plan:%s", rCtx.Region, rCtx.AccountID, name)
	}

	attrs := map[string]string{
		"BackupPlanArn": arn,
	}
	return arn, attrs, nil
}

// Delete addresses the plan by id. The physical ID is the ARN
// (arn:…:backup-plan:<id>) and DeleteBackupPlan binds the id as a path label,
// so the id comes from the ARN's last segment — sending the whole ARN as the
// id, as this did before, never matched a plan and a stack delete left it
// behind.
func (h *backupBackupPlanHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		backupPlansPath+"/"+url.PathEscape(backupIDFromARN(physicalID)), "", nil)
	return teardownError("DeleteBackupPlan", rec, err)
}

// Update forces replacement for any BackupPlan structure or name change —
// UpdateBackupPlan is not wired to this handler, a pre-existing gap this PR
// does not extend. A tag-only change is handled without replacement, since
// forcing one there would be a correctness regression this PR would
// introduce: tags never force replacement on real AWS, and BackupPlanTags is
// the one property #1195 makes mutable in place (via TagResource/
// UntagResource — UpdateBackupPlan itself models no tags member either).
func (h *backupBackupPlanHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if changed, err := cfnPropertyChanged(props, oldProps, "BackupPlan"); err != nil {
		return "", nil, err
	} else if changed {
		return "", nil, errReplacementRequired
	}
	newTags, err := backupResourceTagMap(props["BackupPlanTags"])
	if err != nil {
		return "", nil, err
	}
	oldTags, err := backupResourceTagMap(oldProps["BackupPlanTags"])
	if err != nil {
		return "", nil, err
	}
	upserts, removals := logsLogGroupTagChanges(newTags, oldTags)
	if err := putBackupResourceTags(ctx, router, rCtx.Region, physicalID, upserts); err != nil {
		return "", nil, err
	}
	if err := untagBackupResource(ctx, router, rCtx.Region, physicalID, removals); err != nil {
		return "", nil, err
	}
	return physicalID, nil, nil
}

// ── AWS::Transfer::Server ───────────────────────────────────────────────────

type transferServerHandler struct{}

func (h *transferServerHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	endpointType, _ := props["EndpointType"].(string)
	if endpointType == "" {
		endpointType = "PUBLIC"
	}
	identityProviderType, _ := props["IdentityProviderType"].(string)
	if identityProviderType == "" {
		identityProviderType = "SERVICE_MANAGED"
	}

	body := map[string]any{
		"EndpointType":         endpointType,
		"IdentityProviderType": identityProviderType,
	}
	if v, ok := props["Protocols"]; ok {
		body["Protocols"] = v
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = transferTagsWire(tags)
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "TransferService.CreateServer", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateServer: %w", err)
	}

	var resp struct {
		ServerId string `json:"ServerId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateServer: parse response: %w", err)
	}

	attrs := map[string]string{
		"ServerId": resp.ServerId,
		"Arn":      transferServerARN(rCtx.Region, rCtx.AccountID, resp.ServerId),
	}
	return resp.ServerId, attrs, nil
}

func (h *transferServerHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"ServerId": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "TransferService.DeleteServer", body)
	return teardownError("DeleteServer", rec, err)
}

// transferServerARN builds the ARN transferReconcileTags addresses a server
// by; Transfer's Tag/UntagResource key on Arn, not ServerId.
func transferServerARN(region, accountID, serverID string) string {
	return fmt.Sprintf("arn:aws:transfer:%s:%s:server/%s", region, accountID, serverID)
}

func (h *transferServerHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Every property but Tags forces replacement — real AWS lets several of
	// these change in place, but this handler predates that and widening it is
	// a separate change (see PR #1308). A tags-only change reconciles via
	// TagResource/UntagResource instead of replacing the server (#1310).
	for _, property := range []string{"EndpointType", "IdentityProviderType", "Protocols"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}

	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		if err := transferReconcileTags(ctx, router, rCtx.Region, transferServerARN(rCtx.Region, rCtx.AccountID, physicalID), tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("transfer tags: %w", err))
		}
	}

	attrs := map[string]string{
		"ServerId": physicalID,
		"Arn":      transferServerARN(rCtx.Region, rCtx.AccountID, physicalID),
	}
	return physicalID, attrs, nil
}

// transferTagsWire renders a merged tag map in Transfer's Tags wire shape
// ([{Key,Value}]), shared by CreateServer/CreateUser's inline Tags and
// Tag/UntagResource.
func transferTagsWire(tags map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(tags))
	for k, v := range tags {
		out = append(out, map[string]string{"Key": k, "Value": v})
	}
	return out
}

func transferTagResource(ctx context.Context, router http.Handler, region, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if _, err := internalJSON(ctx, router, region, "TransferService.TagResource", map[string]any{
		"Arn":  arn,
		"Tags": transferTagsWire(tags),
	}); err != nil {
		return fmt.Errorf("transfer TagResource: %w", err)
	}
	return nil
}

func transferUntagResource(ctx context.Context, router http.Handler, region, arn string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if _, err := internalJSON(ctx, router, region, "TransferService.UntagResource", map[string]any{
		"Arn":     arn,
		"TagKeys": keys,
	}); err != nil {
		return fmt.Errorf("transfer UntagResource: %w", err)
	}
	return nil
}

// transferReconcileTags diffs desired against previous and applies only the
// change, mirroring sfnReconcileTags' and the SSM parameter handler's
// add/remove split.
func transferReconcileTags(ctx context.Context, router http.Handler, region, arn string, tags, prior map[string]string) error {
	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	if err := transferTagResource(ctx, router, region, arn, added); err != nil {
		return err
	}
	return transferUntagResource(ctx, router, region, arn, removed)
}

// ── AWS::Transfer::User ─────────────────────────────────────────────────────

type transferUserHandler struct{}

func (h *transferUserHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	serverID, _ := props["ServerId"].(string)
	userName, _ := props["UserName"].(string)
	role, _ := props["Role"].(string)

	body := map[string]any{
		"ServerId": serverID,
		"UserName": userName,
		"Role":     role,
	}
	if v, _ := props["HomeDirectory"].(string); v != "" {
		body["HomeDirectory"] = v
	}
	if v, _ := props["Policy"].(string); v != "" {
		body["Policy"] = v
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = transferTagsWire(tags)
	}
	noteUnconsumedProperties(ctx, "AWS::Transfer::User", props,
		"ServerId", "UserName", "Role", "HomeDirectory", "Policy", "Tags")

	rec, err := internalJSON(ctx, router, rCtx.Region, "TransferService.CreateUser", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateUser: %w", err)
	}

	var resp struct {
		ServerId string `json:"ServerId"`
		UserName string `json:"UserName"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateUser: parse response: %w", err)
	}

	sid := resp.ServerId
	if sid == "" {
		sid = serverID
	}
	uname := resp.UserName
	if uname == "" {
		uname = userName
	}

	physicalID := sid + "/" + uname
	attrs := map[string]string{
		"ServerId": sid,
		"UserName": uname,
		"Arn":      transferUserARN(rCtx.Region, rCtx.AccountID, sid, uname),
	}
	return physicalID, attrs, nil
}

// transferUserARN builds the ARN transferReconcileTags addresses a user by;
// Transfer's Tag/UntagResource key on Arn, not the ServerId/UserName pair.
func transferUserARN(region, accountID, serverID, userName string) string {
	return fmt.Sprintf("arn:aws:transfer:%s:%s:user/%s/%s", region, accountID, serverID, userName)
}

func (h *transferUserHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	body := map[string]any{
		"ServerId": parts[0],
		"UserName": parts[1],
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "TransferService.DeleteUser", body)
	return teardownError("DeleteUser", rec, err)
}

func (h *transferUserHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is "{serverID}/{userName}"; both replace on real AWS, and
	// CreateUser rejects duplicates, so replacing under an unchanged pair can
	// never succeed.
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return "", nil, errReplacementRequired
	}
	if sid, ok := props["ServerId"].(string); ok && sid != "" && sid != parts[0] {
		return "", nil, errReplacementRequired
	}
	if un, ok := props["UserName"].(string); ok && un != "" && un != parts[1] {
		return "", nil, errReplacementRequired
	}

	body := map[string]any{
		"ServerId": parts[0],
		"UserName": parts[1],
	}
	if v, _ := props["Role"].(string); v != "" {
		body["Role"] = v
	}
	if v, _ := props["HomeDirectory"].(string); v != "" {
		body["HomeDirectory"] = v
	}
	if v, _ := props["Policy"].(string); v != "" {
		body["Policy"] = v
	}
	noteUnconsumedProperties(ctx, "AWS::Transfer::User", props,
		"ServerId", "UserName", "Role", "HomeDirectory", "Policy", "Tags")
	if _, err := internalJSON(ctx, router, rCtx.Region, "TransferService.UpdateUser", body); err != nil {
		return "", nil, fmt.Errorf("UpdateUser: %w", err)
	}

	arn := transferUserARN(rCtx.Region, rCtx.AccountID, parts[0], parts[1])
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		if err := transferReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("transfer tags: %w", err))
		}
	}

	return physicalID, map[string]string{"ServerId": parts[0], "UserName": parts[1], "Arn": arn}, nil
}

// ── AWS::Shield::Protection ─────────────────────────────────────────────────

type shieldProtectionHandler struct{}

func (h *shieldProtectionHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenShield)
	}
	resourceArn, _ := props["ResourceArn"].(string)

	body := map[string]any{
		"Name":        name,
		"ResourceArn": resourceArn,
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSShield_20160616.CreateProtection", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateProtection: %w", err)
	}

	var resp struct {
		ProtectionId string `json:"ProtectionId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateProtection: parse response: %w", err)
	}

	// CreateProtection's own request (shield/typed_logic.go's
	// createProtectionRequest) carries no Tags member — real Shield applies
	// tags to a protection the same way Backup/CloudWatch do, with a
	// follow-up TagResource against the resource's ARN once it exists.
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		tagBody := map[string]any{
			"ResourceARN": shieldProtectionARN(rCtx.AccountID, resp.ProtectionId),
			"Tags":        ecrTagsFromMap(tags),
		}
		if _, err := internalJSON(ctx, router, rCtx.Region, "AWSShield_20160616.TagResource", tagBody); err != nil {
			return "", nil, fmt.Errorf("TagResource: %w", err)
		}
	}
	noteUnconsumedProperties(ctx, "AWS::Shield::Protection", props, "Name", "ResourceArn", "Tags")

	attrs := map[string]string{
		"ProtectionId": resp.ProtectionId,
	}
	return resp.ProtectionId, attrs, nil
}

func (h *shieldProtectionHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"ProtectionId": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSShield_20160616.DeleteProtection", body)
	return teardownError("DeleteProtection", rec, err)
}

// shieldProtectionARN builds the ARN Shield's TagResource/UntagResource
// address a protection by (shield/handler.go's protectionARN): the service's
// ARNs carry no region, "arn:aws:shield::<account>:protection/<id>".
func shieldProtectionARN(accountID, protectionID string) string {
	return fmt.Sprintf("arn:aws:shield::%s:protection/%s", accountID, protectionID)
}

// shieldReconcileTags diffs desired against previous and applies only the
// change, mirroring cloudtrailReconcileTags'/transferReconcileTags' add/remove
// split.
func shieldReconcileTags(ctx context.Context, router http.Handler, region, protectionArn string, tags, prior map[string]string) error {
	upserts, removals := logsLogGroupTagChanges(tags, prior)
	if len(upserts) > 0 {
		body := map[string]any{"ResourceARN": protectionArn, "Tags": ecrTagsFromMap(upserts)}
		if _, err := internalJSON(ctx, router, region, "AWSShield_20160616.TagResource", body); err != nil {
			return fmt.Errorf("shield TagResource: %w", err)
		}
	}
	if len(removals) > 0 {
		body := map[string]any{"ResourceARN": protectionArn, "TagKeys": removals}
		if _, err := internalJSON(ctx, router, region, "AWSShield_20160616.UntagResource", body); err != nil {
			return fmt.Errorf("shield UntagResource: %w", err)
		}
	}
	return nil
}

// Update forces replacement for Name or ResourceArn — CreateProtection is the
// only way to associate a protection with a resource, and there is no
// in-place retarget — but reconciles a Tags-only change via
// TagResource/UntagResource instead, matching real Shield: Tags never force
// replacement.
func (h *shieldProtectionHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, property := range []string{"Name", "ResourceArn"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		arn := shieldProtectionARN(rCtx.AccountID, physicalID)
		if err := shieldReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("shield tags: %w", err))
		}
	}
	return physicalID, map[string]string{"ProtectionId": physicalID}, nil
}

// ── AWS::KinesisFirehose::DeliveryStream ────────────────────────────────────

type firehoseDeliveryStreamHandler struct{}

func (h *firehoseDeliveryStreamHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["DeliveryStreamName"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenFirehose)
	}
	streamType, _ := props["DeliveryStreamType"].(string)
	if streamType == "" {
		streamType = "DirectPut"
	}

	body := map[string]any{
		"DeliveryStreamName": name,
		"DeliveryStreamType": streamType,
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "Firehose_20150804.CreateDeliveryStream", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDeliveryStream: %w", err)
	}

	var resp struct {
		DeliveryStreamARN string `json:"DeliveryStreamARN"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateDeliveryStream: parse response: %w", err)
	}

	arn := resp.DeliveryStreamARN
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:firehose:%s:%s:deliverystream/%s", rCtx.Region, rCtx.AccountID, name)
	}

	attrs := map[string]string{
		"Arn": arn,
	}
	// The physical ID is the delivery stream NAME, which is what AWS documents
	// Ref as returning for this resource type and what Delete below needs to
	// name the stream. It used to be the ARN, so Ref handed consumers an ARN
	// where they expected a name, and the teardown dispatched a
	// DeleteDeliveryStream whose DeliveryStreamName was an ARN — which matches
	// no stream, so every deleted stack left its delivery stream behind. The
	// ARN stays available on GetAtt Arn, above.
	return name, attrs, nil
}

func (h *firehoseDeliveryStreamHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"DeliveryStreamName": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "Firehose_20150804.DeleteDeliveryStream", body)
	return teardownError("DeleteDeliveryStream", rec, err)
}

func (h *firehoseDeliveryStreamHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::Athena::WorkGroup ──────────────────────────────────────────────────

type athenaWorkGroupHandler struct{}

func (h *athenaWorkGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenAthena)
	}

	body := map[string]any{
		"Name": name,
	}
	if v, ok := props["Description"].(string); ok && v != "" {
		body["Description"] = v
	}
	// The schema's property is WorkGroupConfiguration; Configuration is the
	// CreateWorkGroup API member it maps onto
	// (https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-athena-workgroup.html).
	if v, ok := props["WorkGroupConfiguration"]; ok {
		body["Configuration"] = v
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = ecrTagsFromMap(tags)
	}
	noteUnconsumedProperties(ctx, "AWS::Athena::WorkGroup", props, "Name", "Description", "WorkGroupConfiguration", "Tags")

	_, err := internalJSON(ctx, router, rCtx.Region, "AmazonAthena.CreateWorkGroup", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateWorkGroup: %w", err)
	}

	attrs := map[string]string{
		"Name": name,
	}
	return name, attrs, nil
}

func (h *athenaWorkGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"WorkGroup": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AmazonAthena.DeleteWorkGroup", body)
	return teardownError("DeleteWorkGroup", rec, err)
}

// athenaWorkGroupARN builds the ARN Athena's TagResource/UntagResource
// address a workgroup by; the physical ID is the bare workgroup name.
func athenaWorkGroupARN(region, accountID, name string) string {
	return fmt.Sprintf("arn:aws:athena:%s:%s:workgroup/%s", region, accountID, name)
}

// athenaReconcileTags diffs desired against previous and applies only the
// change, mirroring cloudtrailReconcileTags'/transferReconcileTags'
// add/remove split.
func athenaReconcileTags(ctx context.Context, router http.Handler, region, arn string, tags, prior map[string]string) error {
	upserts, removals := logsLogGroupTagChanges(tags, prior)
	if len(upserts) > 0 {
		body := map[string]any{"ResourceARN": arn, "Tags": ecrTagsFromMap(upserts)}
		if _, err := internalJSON(ctx, router, region, "AmazonAthena.TagResource", body); err != nil {
			return fmt.Errorf("athena TagResource: %w", err)
		}
	}
	if len(removals) > 0 {
		body := map[string]any{"ResourceARN": arn, "TagKeys": removals}
		if _, err := internalJSON(ctx, router, region, "AmazonAthena.UntagResource", body); err != nil {
			return fmt.Errorf("athena UntagResource: %w", err)
		}
	}
	return nil
}

// Update forces replacement for Name, Description or WorkGroupConfiguration —
// Athena has no UpdateWorkGroup wired to this handler, a pre-existing gap
// this fix does not extend — but reconciles a Tags-only change via
// TagResource/UntagResource instead, matching real Athena: Tags never force
// replacement.
func (h *athenaWorkGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, property := range []string{"Name", "Description", "WorkGroupConfiguration"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		arn := athenaWorkGroupARN(rCtx.Region, rCtx.AccountID, physicalID)
		if err := athenaReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("athena tags: %w", err))
		}
	}
	return physicalID, map[string]string{"Name": physicalID}, nil
}

// ── AWS::Glue::Database ─────────────────────────────────────────────────────

type glueDatabaseHandler struct{}

func (h *glueDatabaseHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	databaseInput, _ := props["DatabaseInput"].(map[string]any)

	// DatabaseInput.Name is Required: No, so CloudFormation names a database
	// the template leaves unnamed — and the name has to reach CreateDatabase,
	// which requires it. Before, the generated name was only ever used as the
	// physical ID *after* the call, so an unnamed database failed the stack
	// outright and a named one was fine; the fallback described a database
	// that was never created.
	//
	// Glue folds a database name to lowercase for Hive compatibility, so the
	// name is generated lowercase rather than lowercased on read-back: an
	// uppercase name would not round-trip through Ref.
	dbName := ""
	if databaseInput != nil {
		dbName, _ = databaseInput["Name"].(string)
	}
	if dbName == "" {
		dbName = rCtx.generatedNameLowerWithin(maxNameLenDefault)
		// props belongs to the resolved template; copy rather than mutate.
		named := make(map[string]any, len(databaseInput)+1)
		for k, v := range databaseInput {
			named[k] = v
		}
		named["Name"] = dbName
		databaseInput = named
	}

	body := map[string]any{
		"DatabaseInput": databaseInput,
	}
	catalogID, _ := props["CatalogId"].(string)
	body["CatalogId"] = catalogID

	_, err := internalJSON(ctx, router, rCtx.Region, "AWSGlue.CreateDatabase", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDatabase: %w", err)
	}

	attrs := map[string]string{
		"Name": dbName,
	}
	return dbName, attrs, nil
}

func (h *glueDatabaseHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"Name": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSGlue.DeleteDatabase", body)
	return teardownError("DeleteDatabase", rec, err)
}

func (h *glueDatabaseHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::Glue::Table ────────────────────────────────────────────────────────

type glueTableHandler struct{}

func (h *glueTableHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	databaseName, _ := props["DatabaseName"].(string)
	tableInput, _ := props["TableInput"].(map[string]any)
	catalogID, _ := props["CatalogId"].(string)

	body := map[string]any{
		"DatabaseName": databaseName,
		"TableInput":   tableInput,
	}
	if catalogID != "" {
		body["CatalogId"] = catalogID
	}

	_, err := internalJSON(ctx, router, rCtx.Region, "AWSGlue.CreateTable", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateTable: %w", err)
	}

	tableName := ""
	if tableInput != nil {
		tableName, _ = tableInput["Name"].(string)
	}
	physicalID := databaseName + "/" + tableName
	attrs := map[string]string{
		"TableName": tableName,
	}
	return physicalID, attrs, nil
}

func (h *glueTableHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	body := map[string]any{
		"DatabaseName": parts[0],
		"Name":         parts[1],
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSGlue.DeleteTable", body)
	return teardownError("DeleteTable", rec, err)
}

func (h *glueTableHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::CloudWatch::Alarm ──────────────────────────────────────────────────

type cloudwatchAlarmHandler struct{}

// cloudwatchAlarmProperties are every AWS::CloudWatch::Alarm property other
// than AlarmName, each of which maps one-for-one onto PutMetricAlarm.
//
// The list is exhaustive on purpose. Every one of them is optional on the
// resource, and a dropped optional property does not fail the stack — it
// produces an alarm that exists and is evaluated under a configuration the
// template did not ask for (a dropped TreatMissingData turns notBreaching back
// into missing; a dropped DatapointsToAlarm turns an "M out of N" alarm into
// "N out of N"). The ones Overcast refuses — Metrics, ThresholdMetricId,
// ExtendedStatistic — have to reach the service to be refused at all, or the
// stack dies claiming a missing MetricName the template never had to supply.
var cloudwatchAlarmProperties = []string{
	"ActionsEnabled", "AlarmActions", "AlarmDescription", "ComparisonOperator",
	"DatapointsToAlarm", "Dimensions", "EvaluateLowSampleCountPercentile",
	"EvaluationCriteria", "EvaluationInterval", "EvaluationPeriods",
	"EvaluationWindow", "ExtendedStatistic", "InsufficientDataActions",
	"MetricName", "Metrics", "Namespace", "OKActions", "Period", "Statistic",
	"Tags", "Threshold", "ThresholdMetricId", "TreatMissingData", "Unit",
}

// putMetricAlarm issues PutMetricAlarm for the given alarm name and template
// properties, and returns the physical ID and attributes. Shared by Create and
// Update: PutMetricAlarm is itself an upsert, so the two are the same call.
//
// A property the template omitted is left out of the request rather than sent
// empty, so the service applies AWS's own defaults instead of being told the
// caller asked for "".
func (h *cloudwatchAlarmHandler) putMetricAlarm(ctx context.Context, router http.Handler, rCtx *resolveContext, alarmName string, props map[string]any) (string, map[string]string, error) {
	body := map[string]any{"AlarmName": alarmName}
	for _, name := range cloudwatchAlarmProperties {
		if v, ok := props[name]; ok && v != nil {
			body[name] = v
		}
	}

	_, err := internalJSON(ctx, router, rCtx.Region, "GraniteServiceVersion20100801.PutMetricAlarm", body)
	if err != nil {
		return "", nil, fmt.Errorf("PutMetricAlarm: %w", err)
	}

	arn := fmt.Sprintf("arn:aws:cloudwatch:%s:%s:alarm:%s", rCtx.Region, rCtx.AccountID, alarmName)
	attrs := map[string]string{
		"Arn": arn,
	}
	return alarmName, attrs, nil
}

func (h *cloudwatchAlarmHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// AlarmName is optional on the resource — CloudFormation names the alarm
	// when the template leaves it out, and CDK leaves it out unless the caller
	// passes `alarmName`, which the constructs that build alarms for you
	// (metric.createAlarm, queue/lambda metric helpers) never do. PutMetricAlarm
	// itself requires the name, so forwarding the empty string turned every
	// such stack into AWS's own "Value null at 'alarmName'" ValidationError.
	alarmName, _ := props["AlarmName"].(string)
	if alarmName == "" {
		alarmName = rCtx.generatedName()
	}
	return h.putMetricAlarm(ctx, router, rCtx, alarmName, props)
}

func (h *cloudwatchAlarmHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{
		"AlarmNames": []string{physicalID},
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "GraniteServiceVersion20100801.DeleteAlarms", body)
	return teardownError("DeleteAlarms", rec, err)
}

func (h *cloudwatchAlarmHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the alarm name; only AlarmName replaces on real AWS.
	if n, ok := props["AlarmName"].(string); ok && n != "" && n != physicalID {
		return "", nil, errReplacementRequired
	}
	id, attrs, err := h.putMetricAlarm(ctx, router, rCtx, physicalID, props)
	if err != nil {
		return "", nil, err
	}
	// After PutMetricAlarm, not before: tagging is addressed to the alarm's ARN,
	// and the alarm has to be there to carry the tags.
	//
	// A tag call that fails here leaves the alarm holding its new configuration
	// and some part of its old tags, which is why the failure is dirty rather
	// than terminal: rollback must report the resource as failed instead of
	// claiming the previous state was restored, and it must not answer a
	// half-applied update by building a second alarm.
	if err := h.syncTags(ctx, router, rCtx.Region, attrs["Arn"], props["Tags"], oldProps["Tags"]); err != nil {
		return "", nil, failDirtyUpdate(err)
	}
	return id, attrs, nil
}

// syncTags reconciles an alarm's tags with the template's.
//
// PutMetricAlarm applies Tags only when it creates an alarm and ignores them
// when it updates one, exactly as real CloudWatch does (see the Tagging section
// of docs/services/cloudwatch.md). Left to the upsert alone, a stack update that
// changed only an alarm's tags would reach UPDATE_COMPLETE having changed
// nothing. Real CloudFormation calls TagResource/UntagResource against the
// resource's ARN when its tags change, and so does this.
//
// The calls go over the Query protocol rather than through internalJSON,
// because CloudWatch's JSON dispatch covers only the alarm and metric
// operations — TagResource over a GraniteServiceVersion20100801 target is an
// UnknownOperationException. This is the same route applySNSTopicTags takes for
// the same reason.
func (h *cloudwatchAlarmHandler) syncTags(ctx context.Context, router http.Handler, region, alarmARN string, newTags, oldTags any) error {
	want, have := cloudwatchAlarmTagMap(newTags), cloudwatchAlarmTagMap(oldTags)

	// Sorted so the request a given diff produces is always the same one —
	// the member indices have to be contiguous from 1 either way, because the
	// service stops reading the list at the first gap.
	var added []string
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if old, ok := have[k]; !ok || old != want[k] {
			added = append(added, k)
		}
	}
	var removed []string
	for _, k := range slices.Sorted(maps.Keys(have)) {
		if _, ok := want[k]; !ok {
			removed = append(removed, k)
		}
	}

	if len(added) > 0 {
		params := map[string]string{
			"Action":      "TagResource",
			"ResourceARN": alarmARN,
			"Version":     awsapi.VersionCloudWatch,
		}
		for i, k := range added {
			params[fmt.Sprintf("Tags.member.%d.Key", i+1)] = k
			params[fmt.Sprintf("Tags.member.%d.Value", i+1)] = want[k]
		}
		if _, err := internalQuery(ctx, router, region, params); err != nil {
			return fmt.Errorf("TagResource: %w", err)
		}
	}
	if len(removed) > 0 {
		params := map[string]string{
			"Action":      "UntagResource",
			"ResourceARN": alarmARN,
			"Version":     awsapi.VersionCloudWatch,
		}
		for i, k := range removed {
			params[fmt.Sprintf("TagKeys.member.%d", i+1)] = k
		}
		if _, err := internalQuery(ctx, router, region, params); err != nil {
			return fmt.Errorf("UntagResource: %w", err)
		}
	}
	return nil
}

// cloudwatchAlarmTagMap flattens a template Tags list into key/value pairs.
//
// A tag with an empty key is dropped rather than sent: the Query-protocol tag
// lists are read until the first missing member, so one empty key would
// silently truncate every tag after it.
func cloudwatchAlarmTagMap(raw any) map[string]string {
	out := map[string]string{}
	list, _ := raw.([]any)
	for _, item := range list {
		tag, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := tag["Key"].(string)
		if key == "" {
			continue
		}
		value := ""
		if v := tag["Value"]; v != nil {
			value = cfnScalarString(v)
		}
		out[key] = value
	}
	return out
}

// ── AWS::Scheduler::Schedule ────────────────────────────────────────────────
//
// Scheduler's handlers dispatch over its own REST-JSON bindings
// (/schedules/{Name}, /schedule-groups/{Name}) instead of the invented
// "Scheduler.<Op>" X-Amz-Target prefix #1226 retired: EventBridge Scheduler is
// restJson1 and the pinned models never gave it one, so nothing but this
// provisioner and Overcast's own tests ever spoke it.

// schedulerJSON POSTs/PUTs a JSON body to one Scheduler REST path.
func schedulerJSON(ctx context.Context, router http.Handler, region, method, path string, body map[string]any) (*httptest.ResponseRecorder, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return internalRequest(ctx, router, region, method, path, "application/json", data)
}

type schedulerScheduleHandler struct{}

// schedulerScheduleBody builds the CreateSchedule/UpdateSchedule request body
// shared by Create and Update: the four scalars every schedule carries, plus
// every optional property #537 found being parsed into nothing —
// Description, ScheduleExpressionTimezone, StartDate, EndDate and
// KmsKeyArn — despite the scheduler service already storing and returning
// all five (see docs/services/scheduler.md). FlexibleTimeWindow and Target
// are threaded whole; the service validates their shape itself.
func schedulerScheduleBody(name, groupName string, props map[string]any) map[string]any {
	scheduleExpression, _ := props["ScheduleExpression"].(string)
	state, _ := props["State"].(string)
	if state == "" {
		state = "ENABLED"
	}

	body := map[string]any{
		"Name":               name,
		"GroupName":          groupName,
		"ScheduleExpression": scheduleExpression,
		"State":              state,
	}
	if v, ok := props["FlexibleTimeWindow"]; ok {
		body["FlexibleTimeWindow"] = v
	}
	if v, ok := props["Target"]; ok {
		body["Target"] = v
	}
	copyStringProp(body, props, "Description", "Description")
	copyStringProp(body, props, "ScheduleExpressionTimezone", "ScheduleExpressionTimezone")
	copyStringProp(body, props, "KmsKeyArn", "KmsKeyArn")
	copyAnyProp(body, props, "StartDate", "StartDate")
	copyAnyProp(body, props, "EndDate", "EndDate")
	return body
}

func (h *schedulerScheduleHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Name is optional on AWS::Scheduler::Schedule, and CDK's L2 Schedule
	// leaves it out unless the caller passes scheduleName — the common shape,
	// not an edge case. The empty name was forwarded, and CreateSchedule binds
	// the name into the path, so the dispatch went to "/schedules/", which
	// "/schedules/{name}" cannot match. It fell through to the router's
	// modeled-route fallback, which claims the bare "/schedules" that DataBrew
	// models and answers 501 NotImplemented — so the stack rolled back on
	// "CreateSchedule: HTTP 501" for a template real CloudFormation deploys.
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenScheduler)
	}
	groupName, _ := props["GroupName"].(string)
	if groupName == "" {
		groupName = "default"
	}

	rec, err := schedulerJSON(ctx, router, rCtx.Region, http.MethodPost,
		"/schedules/"+url.PathEscape(name), schedulerScheduleBody(name, groupName, props))
	if err != nil {
		return "", nil, fmt.Errorf("CreateSchedule: %w", err)
	}

	var resp struct {
		ScheduleArn string `json:"ScheduleArn"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	physicalID := groupName + "/" + name
	attrs := map[string]string{
		"Arn":       resp.ScheduleArn,
		"GroupName": groupName,
		"Name":      name,
	}
	return physicalID, attrs, nil
}

// schedulerScheduleID splits the physical ID Create mints, "{groupName}/{name}".
// Neither a group name nor a schedule name may contain a slash, so the first
// one separates them.
//
// It is the only record of the name a schedule the template left unnamed was
// given, which is why Update reads it rather than the previous properties.
func schedulerScheduleID(physicalID string) (groupName, name string, ok bool) {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (h *schedulerScheduleHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	groupName, name, ok := schedulerScheduleID(physicalID)
	if !ok {
		return nil
	}
	path := "/schedules/" + url.PathEscape(name) + "?" + url.Values{"groupName": {groupName}}.Encode()
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteSchedule", rec, err)
}

// Update applies in place through UpdateSchedule. Name and GroupName are the
// resource's only createOnly properties on real AWS, and the emulator's
// UpdateSchedule locates the record it is replacing by exactly those two
// fields, so there is nothing to update once either changes: the old
// physical ID no longer names anything UpdateSchedule can find.
func (h *schedulerScheduleHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// The physical ID carries the name the schedule is actually held under,
	// which for a template that never named it is the one Create generated.
	// The comparison used to be against oldProps alone, where that name is the
	// empty string — so an unnamed schedule read as unchanged and UpdateSchedule
	// was re-issued with no name, reaching the same unroutable "/schedules/"
	// that Create used to.
	oldGroupName, oldName, ok := schedulerScheduleID(physicalID)
	if !ok {
		return "", nil, errReplacementRequired
	}
	name, _ := props["Name"].(string)
	if oldTemplateName, _ := oldProps["Name"].(string); name == "" && oldTemplateName == "" {
		// Unnamed before and after: CloudFormation keeps the name it generated
		// rather than minting a second one. A name that appears or disappears
		// falls through to the replacement below, as it does on real AWS.
		name = oldName
	}
	groupName, _ := props["GroupName"].(string)
	if groupName == "" {
		groupName = "default"
	}
	if name != oldName || groupName != oldGroupName {
		return "", nil, errReplacementRequired
	}

	rec, err := schedulerJSON(ctx, router, rCtx.Region, http.MethodPut,
		"/schedules/"+url.PathEscape(name), schedulerScheduleBody(name, groupName, props))
	if err != nil {
		return "", nil, fmt.Errorf("UpdateSchedule: %w", err)
	}

	var resp struct {
		ScheduleArn string `json:"ScheduleArn"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	attrs := map[string]string{
		"Arn":       resp.ScheduleArn,
		"GroupName": groupName,
		"Name":      name,
	}
	return physicalID, attrs, nil
}

// ── AWS::Scheduler::ScheduleGroup ───────────────────────────────────────────

type schedulerScheduleGroupHandler struct{}

func (h *schedulerScheduleGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Name is optional on AWS::Scheduler::ScheduleGroup. The empty name was
	// accepted, so a second unnamed group in the same stack collided with the
	// first — "Schedule group  already exists".
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenScheduler)
	}

	body := map[string]any{
		"Name": name,
	}
	// The template's [{Key,Value}] is the shape CreateScheduleGroup models, so
	// the tags go straight through. They were flattened into a map here to
	// match the emulator's own decoding of the member, which no longer differs
	// from the template's.
	if tags, ok := props["Tags"].([]any); ok && len(tags) > 0 {
		body["Tags"] = tags
	}

	rec, err := schedulerJSON(ctx, router, rCtx.Region, http.MethodPost,
		"/schedule-groups/"+url.PathEscape(name), body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateScheduleGroup: %w", err)
	}

	var resp struct {
		ScheduleGroupArn string `json:"ScheduleGroupArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateScheduleGroup: parse response: %w", err)
	}

	arn := resp.ScheduleGroupArn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:scheduler:%s:%s:schedule-group/%s", rCtx.Region, rCtx.AccountID, name)
	}

	attrs := map[string]string{
		"Arn":  arn,
		"Name": name,
	}
	return arn, attrs, nil
}

func (h *schedulerScheduleGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Physical ID is the group ARN, "arn:…:schedule-group/{name}".
	name := physicalID
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		"/schedule-groups/"+url.PathEscape(name), "", nil)
	return teardownError("DeleteScheduleGroup", rec, err)
}

func (h *schedulerScheduleGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the group ARN, "arn:…:schedule-group/{name}".
	oldName := physicalID
	if idx := strings.LastIndex(oldName, "/"); idx >= 0 {
		oldName = oldName[idx+1:]
	}
	if n, ok := props["Name"].(string); ok && n != "" && n != oldName {
		return "", nil, errReplacementRequired
	}
	// Name is the only replacement property on real AWS (the rest is Tags),
	// and CreateScheduleGroup rejects duplicates, so replacing under an
	// unchanged name can never succeed. Keep the group.
	return physicalID, nil, nil
}

// ── AWS::OpenSearchService::Domain ──────────────────────────────────────────

// opensearchDomainPath is OpenSearch's modeled CreateDomain binding. Domains
// are addressed by name beneath it, and the physical ID this handler returns
// is the ARN, so Delete has to recover the name from it.
//
// opensearchTagsPath and opensearchTagsRemovalPath are OpenSearch's modeled
// AddTags/RemoveTags bindings, both addressed by the domain's ARN in the
// body rather than a path label (opensearch/service.go's addTagsRequest/
// removeTagsRequest).
const (
	opensearchDomainPath      = "/2021-01-01/opensearch/domain"
	opensearchTagsPath        = "/2021-01-01/tags"
	opensearchTagsRemovalPath = "/2021-01-01/tags-removal"
)

type opensearchDomainHandler struct{}

func (h *opensearchDomainHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	domainName, _ := props["DomainName"].(string)
	if domainName == "" {
		// DomainName is optional on the resource; CDK's opensearch.Domain
		// construct omits it by default. CloudFormation still has to mint
		// something to send CreateDomain, shaped to what OpenSearch accepts:
		// lowercase, 3-28 characters, starting with a letter
		// (domainNamePatternSource in opensearch/service.go). generatedName
		// already starts with a letter — CloudFormation stack names are
		// themselves anchored `[a-zA-Z][-a-zA-Z0-9]*` — so lowercasing it is
		// enough to satisfy the pattern too.
		domainName = strings.ToLower(rCtx.generatedNameWithin(maxNameLenOpenSearch))
	}
	engineVersion, _ := props["EngineVersion"].(string)

	body := map[string]any{
		"DomainName":    domainName,
		"EngineVersion": engineVersion,
	}
	// ClusterConfig's members (InstanceType, InstanceCount, …) are the same
	// PascalCase names in the template and in CreateDomainRequest, so it is
	// forwarded whole rather than through forwardProperties: the opensearch
	// service stores it as opaque json.RawMessage and echoes it back verbatim
	// (opensearch/service.go's ensureClusterConfig), never parsing a member
	// out of it.
	if v, ok := props["ClusterConfig"]; ok {
		body["ClusterConfig"] = v
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["TagList"] = ecrTagsFromMap(tags)
	}
	noteUnconsumedProperties(ctx, "AWS::OpenSearchService::Domain", props, "DomainName", "EngineVersion", "ClusterConfig", "Tags")

	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDomain: %w", err)
	}

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost,
		opensearchDomainPath, "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDomain: %w", err)
	}

	var resp struct {
		DomainStatus struct {
			ARN string `json:"ARN"`
		} `json:"DomainStatus"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateDomain: parse response: %w", err)
	}

	arn := resp.DomainStatus.ARN
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:es:%s:%s:domain/%s", rCtx.Region, rCtx.AccountID, domainName)
	}

	attrs := map[string]string{
		"Arn":        arn,
		"DomainName": domainName,
	}
	return arn, attrs, nil
}

// Delete addresses the domain by name. The physical ID is the ARN
// (arn:aws:es:…:domain/<name>), and DeleteDomain binds the name as a path
// label, so the name is taken from the ARN's last segment — sending the whole
// ARN as the name, as this did before, never matched a domain.
func (h *opensearchDomainHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	name := physicalID
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		opensearchDomainPath+"/"+url.PathEscape(name), "", nil)
	return teardownError("DeleteDomain", rec, err)
}

// opensearchReconcileTags diffs desired against previous and applies only
// the change via AddTags/RemoveTags, mirroring cloudtrailReconcileTags'/
// transferReconcileTags' add/remove split.
func opensearchReconcileTags(ctx context.Context, router http.Handler, region, domainArn string, tags, prior map[string]string) error {
	upserts, removals := logsLogGroupTagChanges(tags, prior)
	if len(upserts) > 0 {
		data, err := json.Marshal(map[string]any{"ARN": domainArn, "TagList": ecrTagsFromMap(upserts)})
		if err != nil {
			return err
		}
		if _, err := internalRequest(ctx, router, region, http.MethodPost, opensearchTagsPath, "application/json", data); err != nil {
			return fmt.Errorf("opensearch AddTags: %w", err)
		}
	}
	if len(removals) > 0 {
		data, err := json.Marshal(map[string]any{"ARN": domainArn, "TagKeys": removals})
		if err != nil {
			return err
		}
		if _, err := internalRequest(ctx, router, region, http.MethodPost, opensearchTagsRemovalPath, "application/json", data); err != nil {
			return fmt.Errorf("opensearch RemoveTags: %w", err)
		}
	}
	return nil
}

// Update forces replacement for DomainName, EngineVersion or ClusterConfig —
// OpenSearch has no UpdateDomainConfig wired to this handler, a pre-existing
// gap this fix does not extend — but reconciles a Tags-only change via
// AddTags/RemoveTags instead, matching real OpenSearch: Tags never force
// replacement.
func (h *opensearchDomainHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, property := range []string{"DomainName", "EngineVersion", "ClusterConfig"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		if err := opensearchReconcileTags(ctx, router, rCtx.Region, physicalID, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("opensearch tags: %w", err))
		}
	}
	domainName, _ := props["DomainName"].(string)
	return physicalID, map[string]string{"Arn": physicalID, "DomainName": domainName}, nil
}

// ── AWS::AppConfig::Application ─────────────────────────────────────────────

// appconfigApplicationsPath is AppConfig's modeled CreateApplication binding.
// Every nested AppConfig resource hangs off an application beneath it.
const appconfigApplicationsPath = "/applications"

// internalAppConfigRequest dispatches an AppConfig REST request. It differs
// from a bare internalRequest only by the SigV4 scope header, which the main
// router needs to see to claim /applications for AppConfig rather than hand it
// to Service Catalog AppRegistry, whose tree it shares (see #854). Without it a
// stack that declares an AWS::AppConfig::Application would silently create an
// AppRegistry one.
func internalAppConfigRequest(ctx context.Context, router http.Handler, region, method, path string, body []byte) (*httptest.ResponseRecorder, error) {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	return restCall("appconfig", region, method, path, contentType, body, scopedAuthHeader("appconfig", region)).do(ctx, router)
}

// appconfigRESTJSON dispatches an AppConfig REST call and decodes its response.
func appconfigRESTJSON(ctx context.Context, router http.Handler, region, method, path, opName string, body map[string]any, out any) error {
	// A nil map must reach signedRESTJSON as a nil interface, or it is sent
	// as a JSON null.
	if body == nil {
		return signedRESTJSON(ctx, router, "appconfig", region, method, path, opName, nil, out)
	}
	return signedRESTJSON(ctx, router, "appconfig", region, method, path, opName, body, out)
}

// appconfigApplicationARN, appconfigEnvironmentARN and
// appconfigConfigurationProfileARN build the ARNs AppConfig's shared
// /tags/{ResourceArn} dispatch (internal/router/router.go's "---- /tags
// service dispatch" section) reads a resource's identity from, matching
// resolveTagTarget's segment layout (appconfig/service.go).
func appconfigApplicationARN(rCtx *resolveContext, appID string) string {
	return fmt.Sprintf("arn:aws:appconfig:%s:%s:application/%s", rCtx.Region, rCtx.AccountID, appID)
}

func appconfigEnvironmentARN(rCtx *resolveContext, appID, envID string) string {
	return fmt.Sprintf("arn:aws:appconfig:%s:%s:application/%s/environment/%s", rCtx.Region, rCtx.AccountID, appID, envID)
}

func appconfigConfigurationProfileARN(rCtx *resolveContext, appID, profID string) string {
	return fmt.Sprintf("arn:aws:appconfig:%s:%s:application/%s/configurationprofile/%s", rCtx.Region, rCtx.AccountID, appID, profID)
}

// appconfigTagResource and appconfigUntagResource dispatch to the shared
// /tags/{ResourceArn} routes the main router owns (see the ARN builders
// above): TagResource is a plain POST with a {key: value} Tags body
// (appconfig/service.go's tagResource); UntagResource is a DELETE whose keys
// travel as repeated ?tagKeys= query parameters, not a body, because it has
// no typed operation of its own — see appconfig/service.go's untagResource.
func appconfigTagResource(ctx context.Context, router http.Handler, region, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	data, err := json.Marshal(map[string]any{"Tags": tags})
	if err != nil {
		return err
	}
	if _, err := internalRequest(ctx, router, region, http.MethodPost,
		"/tags/"+url.PathEscape(arn), "application/json", data); err != nil {
		return fmt.Errorf("appconfig TagResource: %w", err)
	}
	return nil
}

func appconfigUntagResource(ctx context.Context, router http.Handler, region, arn string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	values := url.Values{}
	for _, k := range keys {
		values.Add("tagKeys", k)
	}
	path := "/tags/" + url.PathEscape(arn) + "?" + values.Encode()
	if _, err := internalRequest(ctx, router, region, http.MethodDelete, path, "", nil); err != nil {
		return fmt.Errorf("appconfig UntagResource: %w", err)
	}
	return nil
}

// appconfigReconcileTags diffs desired against previous and applies only the
// change, mirroring cloudtrailReconcileTags'/transferReconcileTags'
// add/remove split. Shared by all three AppConfig resource types: the
// dispatch, body/query shape and ARN are the only things that differ between
// them.
func appconfigReconcileTags(ctx context.Context, router http.Handler, region, arn string, tags, prior map[string]string) error {
	upserts, removals := logsLogGroupTagChanges(tags, prior)
	if err := appconfigTagResource(ctx, router, region, arn, upserts); err != nil {
		return err
	}
	return appconfigUntagResource(ctx, router, region, arn, removals)
}

type appconfigApplicationHandler struct{}

func (h *appconfigApplicationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	desc, _ := props["Description"].(string)

	body := map[string]any{
		"Name": name,
	}
	if desc != "" {
		body["Description"] = desc
	}
	// CreateApplication takes Tags as the {key: value} map its own API uses
	// (appconfig/service.go's createApplication), not the template's
	// [{Key,Value}] list — mergeResourceTags already returns that shape.
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = tags
	}
	noteUnconsumedProperties(ctx, "AWS::AppConfig::Application", props, "Name", "Description", "Tags")

	var resp struct {
		Id   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := appconfigRESTJSON(ctx, router, rCtx.Region, http.MethodPost,
		appconfigApplicationsPath, "CreateApplication", body, &resp); err != nil {
		return "", nil, err
	}

	attrs := map[string]string{
		"Id":   resp.Id,
		"Name": resp.Name,
	}
	return resp.Id, attrs, nil
}

func (h *appconfigApplicationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalAppConfigRequest(ctx, router, rCtx.Region, http.MethodDelete,
		appconfigApplicationsPath+"/"+url.PathEscape(physicalID), nil)
	return teardownError("DeleteApplication", rec, err)
}

// Update forces replacement for Name or Description — this handler has no
// UpdateApplication call wired to it, a pre-existing gap this fix does not
// extend — but reconciles a Tags-only change via the shared /tags/
// {ResourceArn} TagResource/UntagResource instead, matching real AppConfig:
// Tags never force replacement.
func (h *appconfigApplicationHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, property := range []string{"Name", "Description"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		arn := appconfigApplicationARN(rCtx, physicalID)
		if err := appconfigReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("appconfig application tags: %w", err))
		}
	}
	name, _ := props["Name"].(string)
	return physicalID, map[string]string{"Id": physicalID, "Name": name}, nil
}

// ── AWS::AppConfig::Environment ─────────────────────────────────────────────

type appconfigEnvironmentHandler struct{}

func (h *appconfigEnvironmentHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	appID, _ := props["ApplicationId"].(string)
	name, _ := props["Name"].(string)

	// ApplicationId is a path label on this binding, not a body member.
	body := map[string]any{"Name": name}
	if desc, _ := props["Description"].(string); desc != "" {
		body["Description"] = desc
	}
	// Monitor's members (AlarmArn, AlarmRoleArn) are already the template's
	// own spelling (appconfig/service.go's Monitor), so the list is forwarded
	// whole rather than through forwardProperties.
	if v, ok := props["Monitors"]; ok {
		body["Monitors"] = v
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = tags
	}
	noteUnconsumedProperties(ctx, "AWS::AppConfig::Environment", props,
		"ApplicationId", "Name", "Description", "Monitors", "Tags")

	var resp struct {
		Id string `json:"Id"`
	}
	if err := appconfigRESTJSON(ctx, router, rCtx.Region, http.MethodPost,
		appconfigApplicationsPath+"/"+url.PathEscape(appID)+"/environments",
		"CreateEnvironment", body, &resp); err != nil {
		return "", nil, err
	}

	physicalID := appID + "/" + resp.Id
	attrs := map[string]string{
		"Id":            resp.Id,
		"ApplicationId": appID,
	}
	return physicalID, attrs, nil
}

func (h *appconfigEnvironmentHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	rec, err := internalAppConfigRequest(ctx, router, rCtx.Region, http.MethodDelete,
		appconfigApplicationsPath+"/"+url.PathEscape(parts[0])+"/environments/"+url.PathEscape(parts[1]), nil)
	return teardownError("DeleteEnvironment", rec, err)
}

// Update forces replacement for ApplicationId, Name, Description or
// Monitors — this handler has no UpdateEnvironment call wired to it, a
// pre-existing gap this fix does not extend — but reconciles a Tags-only
// change via the shared /tags/{ResourceArn} TagResource/UntagResource
// instead, matching real AppConfig: Tags never force replacement.
func (h *appconfigEnvironmentHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is "{applicationId}/{environmentId}".
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return "", nil, errReplacementRequired
	}
	for _, property := range []string{"ApplicationId", "Name", "Description", "Monitors"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		arn := appconfigEnvironmentARN(rCtx, parts[0], parts[1])
		if err := appconfigReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("appconfig environment tags: %w", err))
		}
	}
	return physicalID, map[string]string{"Id": parts[1], "ApplicationId": parts[0]}, nil
}

// ── AWS::AppConfig::ConfigurationProfile ────────────────────────────────────

type appconfigConfigurationProfileHandler struct{}

func (h *appconfigConfigurationProfileHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	appID, _ := props["ApplicationId"].(string)
	name, _ := props["Name"].(string)
	locationURI, _ := props["LocationUri"].(string)

	// ApplicationId is a path label on this binding, not a body member.
	body := map[string]any{
		"Name":        name,
		"LocationUri": locationURI,
	}
	if v, _ := props["Description"].(string); v != "" {
		body["Description"] = v
	}
	if v, _ := props["RetrievalRoleArn"].(string); v != "" {
		body["RetrievalRoleArn"] = v
	}
	if v, _ := props["Type"].(string); v != "" {
		body["Type"] = v
	}
	// Validator's members (Type, Content) are already the template's own
	// spelling (appconfig/service.go's Validator), so the list is forwarded
	// whole rather than through forwardProperties.
	if v, ok := props["Validators"]; ok {
		body["Validators"] = v
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = tags
	}
	noteUnconsumedProperties(ctx, "AWS::AppConfig::ConfigurationProfile", props,
		"ApplicationId", "Name", "LocationUri", "Description", "RetrievalRoleArn", "Type", "Validators", "Tags")

	var resp struct {
		Id string `json:"Id"`
	}
	if err := appconfigRESTJSON(ctx, router, rCtx.Region, http.MethodPost,
		appconfigApplicationsPath+"/"+url.PathEscape(appID)+"/configurationprofiles",
		"CreateConfigurationProfile", body, &resp); err != nil {
		return "", nil, err
	}

	physicalID := appID + "/" + resp.Id
	attrs := map[string]string{
		"Id":            resp.Id,
		"ApplicationId": appID,
	}
	return physicalID, attrs, nil
}

func (h *appconfigConfigurationProfileHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	rec, err := internalAppConfigRequest(ctx, router, rCtx.Region, http.MethodDelete,
		appconfigApplicationsPath+"/"+url.PathEscape(parts[0])+"/configurationprofiles/"+url.PathEscape(parts[1]), nil)
	return teardownError("DeleteConfigurationProfile", rec, err)
}

// Update forces replacement for ApplicationId, Name, LocationUri,
// Description, RetrievalRoleArn, Type or Validators — this handler has no
// UpdateConfigurationProfile call wired to it, a pre-existing gap this fix
// does not extend — but reconciles a Tags-only change via the shared
// /tags/{ResourceArn} TagResource/UntagResource instead, matching real
// AppConfig: Tags never force replacement.
func (h *appconfigConfigurationProfileHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is "{applicationId}/{configurationProfileId}".
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return "", nil, errReplacementRequired
	}
	for _, property := range []string{"ApplicationId", "Name", "LocationUri", "Description", "RetrievalRoleArn", "Type", "Validators"} {
		if !reflect.DeepEqual(props[property], oldProps[property]) {
			return "", nil, errReplacementRequired
		}
	}
	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		arn := appconfigConfigurationProfileARN(rCtx, parts[0], parts[1])
		if err := appconfigReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("appconfig configuration profile tags: %w", err))
		}
	}
	return physicalID, map[string]string{"Id": parts[1], "ApplicationId": parts[0]}, nil
}

// ── AWS::SES::ConfigurationSet ──────────────────────────────────────────────

// sesConfigurationSetHandler used to be a stubResourceHandler, which
// fabricates a physical ID and reports CREATE_COMPLETE without calling SES at
// all — so a stack declaring one always "succeeded" for a resource that was
// never created anywhere. SES's own CreateConfigurationSet answers a genuine
// 501 (ses/handler_stubs.go's stub; ses/capabilities_dev.go marks it
// Unsupported), so dispatching the real operation lets the existing 501 →
// GeneralServiceException mapping in status_reason.go fail the resource
// honestly instead — the same "dispatch to the gap rather than hide it" call
// AWS::ApiGateway::RestApi's Body property makes for an unimplemented feature
// with nothing to forward properties into.
type sesConfigurationSetHandler struct{}

func (h *sesConfigurationSetHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenSES)
	}
	params := map[string]string{
		"Action":                "CreateConfigurationSet",
		"ConfigurationSet.Name": name,
	}
	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("CreateConfigurationSet: %w", err)
	}
	// internalQuery only returns nil on a < 400 response, and
	// CreateConfigurationSet always answers 501 today, so this line is
	// unreachable in practice — kept so the handler still does the right
	// thing if the service ever grows a real implementation.
	return name, map[string]string{"Id": name}, nil
}

func (h *sesConfigurationSetHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":               "DeleteConfigurationSet",
		"ConfigurationSetName": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteConfigurationSet", rec, err)
}
