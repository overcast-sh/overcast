package cloudformation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
)

// extractAwsApplicationTag returns the value of the `awsApplication` tag
// inside a CFN resource's Properties, or "" if no such tag is present. CDK's
// `Application` L2 construct propagates this tag to every resource in the
// stack — its value is the owning application's ARN (which also equals the
// `applicationTag.awsApplication` field returned by CreateApplication). The
// provisioner uses this to auto-associate each resource with its application
// immediately after provisioning, so the web UI can resolve ownership without
// joining stack→app × stack→resources on the client.
//
// Tags come through in CFN's standard array-of-objects shape:
//
//	"Tags": [ { "Key": "awsApplication", "Value": "arn:aws:..." }, ... ]
func extractAwsApplicationTag(props map[string]any) string {
	raw, ok := props["Tags"]
	if !ok {
		return ""
	}
	arr, ok := raw.([]any)
	if !ok {
		return ""
	}
	for _, entry := range arr {
		tag, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		key, _ := tag["Key"].(string)
		if key != "awsApplication" {
			continue
		}
		val, _ := tag["Value"].(string)
		return val
	}
	return ""
}

// autoAssociateResource records an association between the given application
// (identified by name/ID/ARN — typically the ARN from the `awsApplication`
// tag) and a concrete CloudFormation-provisioned resource. Failures are
// logged but never fail the stack: the association is a UI convenience, not
// a correctness requirement. The resource key is the resource's physical ID
// which lets the frontend's reverse-map hit regardless of whether a detail
// page knows the ARN or the bare name.
func (p *provisioner) autoAssociateResource(ctx context.Context, rCtx *resolveContext, appRef, physID string) {
	log := p.log.WithRecorder(ctx)
	// The tag value may be an application ID, name, or ARN. Chi's URL-param
	// matcher captures a single path segment, so ARNs (which contain `/` in
	// the `/applications/<id>` tail) can't be passed through directly — take
	// the last path segment, which for an AppRegistry ARN is the app ID and
	// for a bare ID/name is a no-op.
	appKey := appRef
	if i := strings.LastIndex(appKey, "/"); i != -1 {
		appKey = appKey[i+1:]
	}
	// Tag-propagated associations use AWS's RESOURCE_TAG_VALUE resource type,
	// not the CFN type — matching how real AppRegistry classifies tag-based
	// associations from the `awsApplication` tag.
	path := fmt.Sprintf("/applications/%s/resources/RESOURCE_TAG_VALUE/%s", appKey, physID)
	if _, err := internalRequest(ctx, p.router, rCtx.Region, http.MethodPut, path, "application/json", []byte(`{}`)); err != nil {
		log.Warn("appregistry auto-association failed",
			zap.String("application", appRef),
			zap.String("resource", physID),
			zap.Error(err))
	}
}

// ── AWS::ServiceCatalogAppRegistry::Application ────────────────────────────
//
// CDK's `Application` L2 construct synthesizes this resource. The physical ID
// is the application ID returned by the emulator; GetAtt Name/ApplicationName
// expose the friendly name, and GetAtt ApplicationTagValue/ApplicationTagKey
// surface the `awsApplication` tag that CDK propagates to every child resource.

type appregistryApplicationHandler struct{}

func (h *appregistryApplicationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	if v, _ := props["Name"].(string); v != "" {
		body["name"] = v
	} else {
		// The CloudFormation page's pattern for Name is \w+, which forbids the
		// hyphens CloudFormation's own generated names are made of;
		// CreateApplication's is [-.\w]+, which admits them. The API's is the
		// one the service enforces.
		body["name"] = rCtx.generatedNameWithin(maxNameLenAppRegistry)
	}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	// AWS::ServiceCatalogAppRegistry::Application's Tags is `Object of String`
	// (a plain map), not the List<Tag> shape most CFN resources use — see
	// mergeMapResourceTags' header comment. Stack tags merge in here the same
	// way every other propagating resource type does (#1310); Update
	// reconciles a change via TagResource/UntagResource below (#1763).
	tags := mergeMapResourceTags(rCtx.StackTags, props["Tags"])
	if len(tags) > 0 {
		body["tags"] = tags
	}

	data, _ := json.Marshal(body)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/applications", "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("CreateApplication: %w", err)
	}

	var resp struct {
		Application struct {
			ID             string            `json:"id"`
			Name           string            `json:"name"`
			Arn            string            `json:"arn"`
			ApplicationTag map[string]string `json:"applicationTag"`
		} `json:"application"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateApplication: parse response: %w", err)
	}

	// CreateApplication's own `tags` member lands in AppRegistry's Application
	// record (what GetApplication reports), a different store than the shared
	// ARN-keyed one TagResource/UntagResource/ListTagsForResource read and
	// write (see appregistryTagResource's header comment) — the two never
	// merge on their own. Writing the same tags here too keeps a fresh
	// application's ListTagsForResource answer in agreement with GetApplication
	// from the moment it is created, rather than only after the first Update
	// reconciliation touches the shared store.
	if err := appregistryTagResource(ctx, router, rCtx.Region, resp.Application.Arn, tags); err != nil {
		return "", nil, err
	}

	attrs := map[string]string{
		"Id":                  resp.Application.ID,
		"Arn":                 resp.Application.Arn,
		"Name":                resp.Application.Name,
		"ApplicationName":     resp.Application.Name,
		"ApplicationTagKey":   "awsApplication",
		"ApplicationTagValue": resp.Application.ApplicationTag["awsApplication"],
	}
	return resp.Application.ID, attrs, nil
}

func (h *appregistryApplicationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/applications/"+physicalID, "", nil)
	return teardownError("DeleteApplication", rec, err)
}

func (h *appregistryApplicationHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	if v, _ := props["Name"].(string); v != "" {
		body["name"] = v
	}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}

	data, _ := json.Marshal(body)
	path := "/applications/" + physicalID
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPatch, path, "application/json", data); err != nil {
		return "", nil, fmt.Errorf("UpdateApplication: %w", err)
	}

	// UpdateApplication has no tags member of its own (same shape as
	// AppConfig's UpdateApplication) — a tag change is applied through
	// TagResource/UntagResource against the application ARN instead (#1763).
	tags := mergeMapResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeMapResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		arn := appregistryApplicationARN(rCtx, physicalID)
		if err := appregistryReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("appregistry tags: %w", err))
		}
	}
	return physicalID, nil, nil
}

// appregistryApplicationARN builds the ARN AppRegistry's shared
// /tags/{ResourceArn} dispatch (internal/router/router.go's "---- /tags
// service dispatch" section) reads a resource's identity from — the same
// format CreateApplication mints via protocol.ARN(region, accountID,
// "servicecatalog", "/applications/<id>") in
// internal/services/appregistry/handler.go.
func appregistryApplicationARN(rCtx *resolveContext, appID string) string {
	return fmt.Sprintf("arn:aws:servicecatalog:%s:%s:/applications/%s", rCtx.Region, rCtx.AccountID, appID)
}

// appregistryTagResource and appregistryUntagResource dispatch to the shared
// /tags/{ResourceArn} routes the main router owns, which AppRegistry's SDK
// shares with API Gateway (internal/services/appregistry/service.go's
// RegisterRoutes comment): TagResource is a plain POST/PUT with a
// {tags: {key: value}} body; UntagResource is a DELETE whose keys travel as
// repeated ?tagKeys= query parameters — mirroring appconfigTagResource/
// appconfigUntagResource, the same shared-endpoint shape AppConfig uses.
func appregistryTagResource(ctx context.Context, router http.Handler, region, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	data, err := json.Marshal(map[string]any{"tags": tags})
	if err != nil {
		return err
	}
	if _, err := internalRequest(ctx, router, region, http.MethodPost,
		"/tags/"+url.PathEscape(arn), "application/json", data); err != nil {
		return fmt.Errorf("appregistry TagResource: %w", err)
	}
	return nil
}

func appregistryUntagResource(ctx context.Context, router http.Handler, region, arn string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	values := url.Values{}
	for _, k := range keys {
		values.Add("tagKeys", k)
	}
	path := "/tags/" + url.PathEscape(arn) + "?" + values.Encode()
	if _, err := internalRequest(ctx, router, region, http.MethodDelete, path, "", nil); err != nil {
		return fmt.Errorf("appregistry UntagResource: %w", err)
	}
	return nil
}

// appregistryReconcileTags diffs desired against previous and applies only
// the change, mirroring cloudtrailReconcileTags'/appconfigReconcileTags' add/
// remove split.
func appregistryReconcileTags(ctx context.Context, router http.Handler, region, arn string, tags, prior map[string]string) error {
	upserts, removals := logsLogGroupTagChanges(tags, prior)
	if err := appregistryTagResource(ctx, router, region, arn, upserts); err != nil {
		return err
	}
	return appregistryUntagResource(ctx, router, region, arn, removals)
}

// ── AWS::ServiceCatalogAppRegistry::ResourceAssociation ────────────────────
//
// CDK uses this to associate a CloudFormation stack with an application. The
// physical ID we return is opaque (<appID>/CFN_STACK/<stackARN>) — it is only
// used so Delete can reconstruct the association path.

type appregistryResourceAssociationHandler struct{}

func (h *appregistryResourceAssociationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	appID, _ := props["Application"].(string)
	resource, _ := props["Resource"].(string)
	resourceType, _ := props["ResourceType"].(string)
	if resourceType == "" {
		resourceType = "CFN_STACK"
	}
	if appID == "" || resource == "" {
		return "", nil, fmt.Errorf("ResourceAssociation: Application and Resource are required")
	}

	// AppRegistry's PUT path treats {resource} as the stack identifier. For
	// CFN_STACK associations CDK passes the stack ARN — we forward it verbatim.
	path := fmt.Sprintf("/applications/%s/resources/%s/%s", appID, resourceType, resource)
	_, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, path, "application/json", []byte(`{}`))
	if err != nil {
		return "", nil, fmt.Errorf("AssociateResource: %w", err)
	}

	physID := fmt.Sprintf("%s/%s/%s", appID, resourceType, resource)
	return physID, nil, nil
}

func (h *appregistryResourceAssociationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// physicalID is "<appID>/<resourceType>/<resource>" — split on the first
	// two '/' so the resource ARN (which may contain more slashes) stays intact.
	first := -1
	second := -1
	for i, c := range physicalID {
		if c != '/' {
			continue
		}
		if first == -1 {
			first = i
		} else if second == -1 {
			second = i
			break
		}
	}
	if first == -1 || second == -1 {
		return fmt.Errorf("DisassociateResource: malformed physical ID %q", physicalID)
	}
	path := "/applications/" + physicalID[:first] + "/resources/" + physicalID[first+1:second] + "/" + physicalID[second+1:]
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DisassociateResource", rec, err)
}

// ── AWS::ServiceCatalogAppRegistry::AttributeGroup ─────────────────────────
//
// #1763: the underlying service has carried CreateAttributeGroup/
// GetAttributeGroup/UpdateAttributeGroup/DeleteAttributeGroup since the
// "inert tier" attribute-group work (see
// internal/services/appregistry/handler_attribute_groups.go) — only the CFN
// resource type was missing a handler, so a template using it got a
// synthetic physical ID and nothing behind it. Decision: real handlers
// dispatching to those operations, the same choice ResourceAssociation above
// already made, rather than a stub — the service-side support already exists
// and templates using this type are common enough (CDK's `AttributeGroup`
// L2 construct) to be worth wiring rather than declining.

type appregistryAttributeGroupHandler struct{}

func (h *appregistryAttributeGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	if v, _ := props["Name"].(string); v != "" {
		body["name"] = v
	} else {
		body["name"] = rCtx.generatedNameWithin(maxNameLenAppRegistry)
	}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	// Attributes is CloudFormation's Json type — an arbitrary nested object —
	// and CreateAttributeGroup accepts either a JSON string or a JSON object
	// for it (decodeAttributes in handler_attribute_groups.go), so the parsed
	// template value is forwarded as-is.
	if v, ok := props["Attributes"]; ok {
		body["attributes"] = v
	}
	tags := mergeMapResourceTags(rCtx.StackTags, props["Tags"])
	if len(tags) > 0 {
		body["tags"] = tags
	}

	data, _ := json.Marshal(body)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/attribute-groups", "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("CreateAttributeGroup: %w", err)
	}

	var resp struct {
		AttributeGroup struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Arn  string `json:"arn"`
		} `json:"attributeGroup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateAttributeGroup: parse response: %w", err)
	}

	// See the matching comment in appregistryApplicationHandler.Create: keeps
	// ListTagsForResource in agreement with GetAttributeGroup from creation.
	if err := appregistryTagResource(ctx, router, rCtx.Region, resp.AttributeGroup.Arn, tags); err != nil {
		return "", nil, err
	}

	attrs := map[string]string{
		"Id":  resp.AttributeGroup.ID,
		"Arn": resp.AttributeGroup.Arn,
	}
	return resp.AttributeGroup.ID, attrs, nil
}

func (h *appregistryAttributeGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/attribute-groups/"+physicalID, "", nil)
	return teardownError("DeleteAttributeGroup", rec, err)
}

func (h *appregistryAttributeGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	if v, _ := props["Name"].(string); v != "" {
		body["name"] = v
	}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	if v, ok := props["Attributes"]; ok {
		body["attributes"] = v
	}

	data, _ := json.Marshal(body)
	path := "/attribute-groups/" + physicalID
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPatch, path, "application/json", data); err != nil {
		return "", nil, fmt.Errorf("UpdateAttributeGroup: %w", err)
	}

	// UpdateAttributeGroup carries no tags member either — same shape as
	// Application's Update, reconciled via TagResource/UntagResource.
	tags := mergeMapResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeMapResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		arn := appregistryAttributeGroupARN(rCtx, physicalID)
		if err := appregistryReconcileTags(ctx, router, rCtx.Region, arn, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("appregistry tags: %w", err))
		}
	}
	return physicalID, nil, nil
}

// appregistryAttributeGroupARN mirrors appregistryApplicationARN for the
// attribute-group ARN shape CreateAttributeGroup mints
// (handler_attribute_groups.go): arn:aws:servicecatalog:<region>:<account>:/attribute-groups/<id>.
func appregistryAttributeGroupARN(rCtx *resolveContext, groupID string) string {
	return fmt.Sprintf("arn:aws:servicecatalog:%s:%s:/attribute-groups/%s", rCtx.Region, rCtx.AccountID, groupID)
}

// ── AWS::ServiceCatalogAppRegistry::AttributeGroupAssociation ──────────────
//
// Both Application and AttributeGroup are "Update requires: Replacement" on
// this resource type (checked against the CloudFormation docs, 2026-09), so
// — like appregistryResourceAssociationHandler above, which has the same
// shape — this handler implements no Update: the provisioner's default for a
// handler with no resourceUpdater is delete-then-create, which is exactly
// AWS's documented behavior here.

type appregistryAttributeGroupAssociationHandler struct{}

func (h *appregistryAttributeGroupAssociationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	appID, _ := props["Application"].(string)
	groupID, _ := props["AttributeGroup"].(string)
	if appID == "" || groupID == "" {
		return "", nil, fmt.Errorf("AttributeGroupAssociation: Application and AttributeGroup are required")
	}

	path := fmt.Sprintf("/applications/%s/attribute-groups/%s", appID, groupID)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, path, "application/json", []byte(`{}`))
	if err != nil {
		return "", nil, fmt.Errorf("AssociateAttributeGroup: %w", err)
	}

	var resp struct {
		ApplicationArn    string `json:"applicationArn"`
		AttributeGroupArn string `json:"attributeGroupArn"`
	}
	attrs := map[string]string{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		attrs["ApplicationArn"] = resp.ApplicationArn
		attrs["AttributeGroupArn"] = resp.AttributeGroupArn
	}

	physID := fmt.Sprintf("%s/%s", appID, groupID)
	return physID, attrs, nil
}

func (h *appregistryAttributeGroupAssociationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	appID, groupID, ok := strings.Cut(physicalID, "/")
	if !ok {
		return fmt.Errorf("DisassociateAttributeGroup: malformed physical ID %q", physicalID)
	}
	path := fmt.Sprintf("/applications/%s/attribute-groups/%s", appID, groupID)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DisassociateAttributeGroup", rec, err)
}
